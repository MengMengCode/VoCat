package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"vocat/internal/store"
)

const smsNotificationPollInterval = 2 * time.Second

var smsOnlyNotificationChannels = []string{"bark", "email", "pushplus", "webhook", "wecom", "lark"}

type smsNotification struct {
	DeviceID    string
	DeviceName  string
	DeviceLabel string
	Number      string
	Time        time.Time
	Content     string
}

func (value smsNotification) Text() string {
	return strings.Join([]string{
		"收到新短信",
		"设备  " + value.DeviceLabel,
		"号码  " + value.Number,
		"时间  " + value.Time.Local().Format("2006-01-02 15:04:05"),
		"内容  " + value.Content,
	}, "\n")
}

func (value smsNotification) DetailText() string {
	lines := strings.Split(value.Text(), "\n")
	return strings.Join(lines[1:], "\n")
}

// StartSMSNotificationDispatchers delivers future inbound messages to the
// notification-only providers. Each provider owns its cursor so a failing
// webhook, SMTP server, or push service cannot block the other providers.
func (s *Server) StartSMSNotificationDispatchers(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, channel := range smsOnlyNotificationChannels {
		channel := channel
		go s.runSMSNotificationChannel(ctx, channel)
	}
}

func (s *Server) runSMSNotificationChannel(ctx context.Context, channel string) {
	var cursor int64
	cursorInitialized := false
	lastError := ""
	lastErrorAt := time.Time{}
	for ctx.Err() == nil {
		if !cursorInitialized {
			latest, err := s.store.LatestSMSMessageID(ctx)
			if err != nil {
				if err.Error() != lastError || time.Since(lastErrorAt) >= time.Minute {
					s.logSMSNotificationError(channel, err)
					lastError, lastErrorAt = err.Error(), time.Now()
				}
				if !waitTelegram(ctx, smsNotificationPollInterval) {
					return
				}
				continue
			}
			cursor, cursorInitialized = latest, true
			lastError = ""
		}
		config, enabled, configErr := s.smsNotificationConfig(ctx, channel)
		if configErr != nil {
			if configErr.Error() != lastError || time.Since(lastErrorAt) >= time.Minute {
				s.logSMSNotificationError(channel, configErr)
				lastError, lastErrorAt = configErr.Error(), time.Now()
			}
		} else if !enabled {
			if newest, latestErr := s.store.LatestSMSMessageID(ctx); latestErr == nil {
				cursor = newest
			}
			lastError = ""
		} else {
			messages, listErr := s.store.ListInboundSMSAfterID(ctx, cursor, 100)
			if listErr != nil {
				if listErr.Error() != lastError || time.Since(lastErrorAt) >= time.Minute {
					s.logSMSNotificationError(channel, listErr)
					lastError, lastErrorAt = listErr.Error(), time.Now()
				}
			} else {
				for _, message := range messages {
					if !smsMessageReadyToNotify(message) {
						cursor = message.ID
						continue
					}
					notification := s.newSMSNotification(ctx, message)
					if sendErr := sendSMSNotification(s.notificationDestinationContext(ctx), channel, config, notification); sendErr != nil {
						if sendErr.Error() != lastError || time.Since(lastErrorAt) >= time.Minute {
							s.logSMSNotificationError(channel, sendErr)
							lastError, lastErrorAt = sendErr.Error(), time.Now()
						}
						break
					}
					cursor = message.ID
					lastError = ""
				}
			}
		}
		if !waitTelegram(ctx, smsNotificationPollInterval) {
			return
		}
	}
}

func smsMessageReadyToNotify(message store.SMSMessage) bool {
	return strings.TrimSpace(message.Body) != "" &&
		store.ConcatSMSReadyToNotify(message.MessageID, message.Extra)
}

func (s *Server) smsNotificationConfig(ctx context.Context, channel string) (map[string]any, bool, error) {
	setting, err := s.store.NotificationSetting(ctx, channel)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !setting.Enabled) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var config map[string]any
	if err := json.Unmarshal(setting.Config, &config); err != nil {
		return nil, false, fmt.Errorf("decode %s notification config: %w", channel, err)
	}
	if err := validateSMSNotificationConfig(channel, config); err != nil {
		return nil, false, err
	}
	return config, true, nil
}

func validateSMSNotificationConfig(channel string, config map[string]any) error {
	switch channel {
	case "bark", "email", "webhook", "wecom", "lark":
		if err := validateNotificationTestConfig(channel, config); err != nil {
			return err
		}
	case "pushplus":
		if token := strings.TrimSpace(configString(config, "token")); token == "" || token == store.SecretMask {
			return errors.New("pushplus.token is required")
		}
	default:
		return fmt.Errorf("unsupported SMS notification channel %q", channel)
	}
	return nil
}

func (s *Server) newSMSNotification(ctx context.Context, message store.SMSMessage) smsNotification {
	name := ""
	deviceID := message.DeviceID
	if message.ModemIMEI != "" {
		if devices, err := s.store.ListDevices(ctx); err == nil {
			var newest time.Time
			for _, candidate := range devices {
				if candidate.ModemIMEI == message.ModemIMEI &&
					(newest.IsZero() || candidate.UpdatedAt.After(newest)) {
					deviceID = candidate.ID
					name = strings.TrimSpace(candidate.Name)
					newest = candidate.UpdatedAt
				}
			}
		}
	}
	if device, err := s.store.Device(ctx, deviceID); err == nil {
		name = strings.TrimSpace(device.Name)
	}
	return smsNotification{
		DeviceID:    deviceID,
		DeviceName:  name,
		DeviceLabel: firstNonEmpty(name, deviceID, "--"),
		Number:      firstNonEmpty(message.Peer, "--"),
		Time:        message.Timestamp,
		Content:     message.Body,
	}
}

func (s *Server) logSMSNotificationError(channel string, err error) {
	if err != nil && s.logger != nil {
		s.logger.Warn("send inbound SMS notification", "channel", channel, "error", err)
	}
}

func sendSMSNotification(ctx context.Context, channel string, config map[string]any, message smsNotification) error {
	switch channel {
	case "bark":
		return sendBarkSMSNotification(ctx, config, message)
	case "email":
		return sendEmailSMSNotification(ctx, config, message)
	case "pushplus":
		return sendPushplusSMSNotification(ctx, config, message)
	case "webhook":
		return sendWebhookSMSNotification(ctx, config, message)
	case "wecom":
		return sendWecomNotification(ctx, config, wecomSMSValues(message))
	case "lark":
		return sendLarkNotification(ctx, config, larkSMSValues(message))
	default:
		return fmt.Errorf("unsupported SMS notification channel %q", channel)
	}
}

func sendBarkSMSNotification(ctx context.Context, config map[string]any, message smsNotification) error {
	client, err := restrictedHTTPClient(ctx, 6*time.Second, "")
	if err != nil {
		return err
	}
	payload := map[string]any{"title": "收到新短信", "body": message.DetailText()}
	for _, field := range []string{"group", "icon", "level"} {
		if value := configString(config, field); value != "" {
			payload[field] = value
		}
	}
	encoded, _ := json.Marshal(payload)
	for _, destination := range configStrings(config, "urls") {
		parsed, err := validateOutboundURL(ctx, destination, false)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(encoded))
		if err != nil {
			return fmt.Errorf("create Bark notification request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		request.Header.Set("User-Agent", "vocat-sms-notification/1")
		if err := performNotificationRequest(client, request, false); err != nil {
			return err
		}
	}
	return nil
}

func sendWebhookSMSNotification(ctx context.Context, config map[string]any, message smsNotification) error {
	rendered := message.Text()
	if template := configString(config, "text_template"); strings.TrimSpace(template) != "" {
		rendered = renderSMSWebhookTemplate(template, message)
	}
	payload, _ := json.Marshal(map[string]any{
		"event":        "sms.received",
		"message":      rendered,
		"timestamp":    message.Time.UTC().Format(time.RFC3339),
		"device_id":    message.DeviceID,
		"device_name":  message.DeviceName,
		"device_label": message.DeviceLabel,
		"number":       message.Number,
		"content":      message.Content,
	})
	timeout := durationMilliseconds(configInt(config, "timeout_ms"), 5*time.Second)
	client, err := restrictedHTTPClient(ctx, timeout, "")
	if err != nil {
		return err
	}
	retries := configInt(config, "retry_max")
	for _, destination := range configStrings(config, "urls") {
		parsed, err := validateOutboundURL(ctx, destination, false)
		if err != nil {
			return err
		}
		var sendErr error
		for attempt := 0; attempt <= retries; attempt++ {
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
			if requestErr != nil {
				return fmt.Errorf("create webhook notification request: %w", requestErr)
			}
			for name, value := range configStringMap(config, "headers") {
				request.Header.Set(name, value)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "vocat-sms-notification/1")
			if secret := configString(config, "secret"); secret != "" {
				signature := hmac.New(sha256.New, []byte(secret))
				_, _ = signature.Write(payload)
				request.Header.Set("X-vocat-Signature", "sha256="+hex.EncodeToString(signature.Sum(nil)))
			}
			sendErr = performNotificationRequest(client, request, false)
			if sendErr == nil {
				break
			}
		}
		if sendErr != nil {
			return sendErr
		}
	}
	return nil
}

func renderSMSWebhookTemplate(template string, message smsNotification) string {
	replacements := map[string]string{
		"{{text}}":         message.Content,
		"{{content}}":      message.Content,
		"{{event}}":        "sms.received",
		"{{timestamp}}":    message.Time.UTC().Format(time.RFC3339),
		"{{time}}":         message.Time.Local().Format("2006-01-02 15:04:05"),
		"{{number}}":       message.Number,
		"{{device_id}}":    message.DeviceID,
		"{{device_name}}":  message.DeviceName,
		"{{device_label}}": message.DeviceLabel,
	}
	for placeholder, value := range replacements {
		template = strings.ReplaceAll(template, placeholder, value)
	}
	return template
}

func sendPushplusSMSNotification(ctx context.Context, config map[string]any, message smsNotification) error {
	destination, err := validateOutboundURL(ctx, "https://www.pushplus.plus/send", true)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"token":     configString(config, "token"),
		"title":     "收到新短信",
		"content":   message.DetailText(),
		"template":  "txt",
		"timestamp": time.Now().UnixMilli(),
	}
	if topic := configString(config, "topic"); topic != "" {
		payload["topic"] = topic
	}
	if channel := configString(config, "channel"); channel != "" {
		payload["channel"] = channel
	}
	encoded, _ := json.Marshal(payload)
	client, err := restrictedHTTPClient(ctx, 8*time.Second, "")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.String(), bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create Pushplus notification request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", "vocat-sms-notification/1")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send Pushplus notification: %w", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if readErr != nil {
		return fmt.Errorf("read Pushplus response: %w", readErr)
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.Unmarshal(body, &result) != nil || result.Code != 200 {
		return fmt.Errorf("%w: Pushplus HTTP %d code %d %s", errProviderRejected, response.StatusCode, result.Code, result.Msg)
	}
	return nil
}

func sendEmailSMSNotification(ctx context.Context, config map[string]any, message smsNotification) error {
	host := strings.TrimSpace(configString(config, "smtp_host"))
	port := configInt(config, "smtp_port")
	if port == 0 {
		port = 587
	}
	timeout := 8 * time.Second
	connection, err := dialRestricted(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return fmt.Errorf("connect SMTP server: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	useSSL, _ := config["use_ssl"].(bool)
	implicitTLS := port == 465 || useSSL
	if implicitTLS {
		secure := tls.Client(connection, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("establish SMTP TLS: %w", err)
		}
		connection = secure
	}
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return fmt.Errorf("start SMTP session: %w", err)
	}
	defer client.Close()
	if !implicitTLS {
		if available, _ := client.Extension("STARTTLS"); !available {
			return errors.New("SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	username, password := configString(config, "username"), configString(config, "password")
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			return fmt.Errorf("%w: SMTP authentication failed", errProviderRejected)
		}
	}
	from, err := parseMailAddress(configString(config, "from_address"))
	if err != nil {
		return fmt.Errorf("parse sender address: %w", err)
	}
	recipients := make([]*mail.Address, 0)
	for _, item := range configStrings(config, "to_addresses") {
		address, err := parseMailAddress(item)
		if err != nil {
			return fmt.Errorf("parse recipient address: %w", err)
		}
		recipients = append(recipients, address)
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("%w: SMTP sender rejected", errProviderRejected)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return fmt.Errorf("%w: SMTP recipient rejected", errProviderRejected)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("%w: SMTP message rejected", errProviderRejected)
	}
	if err := writePlainTextMail(
		writer,
		from,
		recipients,
		"收到新短信 - "+message.DeviceLabel,
		message.Text(),
	); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP notification: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("%w: SMTP message not accepted", errProviderRejected)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}
