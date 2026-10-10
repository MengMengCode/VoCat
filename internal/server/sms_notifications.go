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
	"slices"
	"strconv"
	"strings"
	"time"

	"vocat/internal/smsdecode"
	"vocat/internal/store"
)

const smsNotificationPollInterval = 2 * time.Second

const (
	// smsNotificationMaxAttempts bounds how many times one message is retried
	// before the channel moves on. Without it a message that the provider
	// permanently rejects stalls every later notification for that channel.
	// Combined with the backoff below this keeps retrying for roughly thirteen
	// minutes, so a short provider outage does not drop a notification.
	smsNotificationMaxAttempts = 10
	// smsNotificationMaxRetryDelay caps the exponential retry backoff so a
	// provider that is down for a long time is still probed, just rarely.
	smsNotificationMaxRetryDelay = 5 * time.Minute
)

// smsNotificationRetryDelay returns how long to wait before the next poll after
// consecutiveFailures send failures in a row. The first failure keeps the
// normal two-second cadence; every further failure doubles the delay, so a
// permanently broken provider is not polled every two seconds forever.
func smsNotificationRetryDelay(consecutiveFailures int) time.Duration {
	delay := smsNotificationPollInterval
	for attempt := 1; attempt < consecutiveFailures; attempt++ {
		if delay >= smsNotificationMaxRetryDelay {
			return smsNotificationMaxRetryDelay
		}
		delay *= 2
	}
	if delay > smsNotificationMaxRetryDelay {
		return smsNotificationMaxRetryDelay
	}
	return delay
}

var smsOnlyNotificationChannels = []string{"bark", "email", "pushplus", "webhook", "wecom", "lark", "meow"}

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

// smsNotificationCursor is the per-channel dispatcher state carried across
// ticks. It is not safe for concurrent use; each channel owns one instance.
type smsNotificationCursor struct {
	id                  int64
	initialized         bool
	lastError           string
	lastErrorAt         time.Time
	consecutiveFailures int
	failedMessageID     int64
	failedAttempts      int
}

// logError reports a channel dispatch error through the server logger. Repeats
// of the same error within a minute are suppressed so a provider that is down
// cannot flood the log at the polling cadence.
func (state *smsNotificationCursor) logError(server *Server, channel string, err error) {
	if err.Error() != state.lastError || time.Since(state.lastErrorAt) >= time.Minute {
		server.logSMSNotificationError(channel, err)
		state.lastError, state.lastErrorAt = err.Error(), time.Now()
	}
}

// smsNotificationSender delivers one notification. It is a parameter so the
// retry policy can be tested without reaching a real provider.
type smsNotificationSender func(context.Context, string, map[string]any, smsNotification) error

// runSMSNotificationChannel polls one notification channel until ctx is
// cancelled, sleeping for the delay returned by each scheduling tick so a
// failing provider backs off instead of being retried every two seconds.
func (s *Server) runSMSNotificationChannel(ctx context.Context, channel string) {
	state := &smsNotificationCursor{}
	for ctx.Err() == nil {
		delay := s.deliverSMSNotificationsOnce(ctx, channel, state, sendSMSNotification)
		if !waitTelegram(ctx, delay) {
			return
		}
	}
}

// deliverSMSNotificationsOnce performs one scheduling tick for a channel and
// returns how long the caller should wait before the next tick.
func (s *Server) deliverSMSNotificationsOnce(
	ctx context.Context,
	channel string,
	state *smsNotificationCursor,
	send smsNotificationSender,
) time.Duration {
	if !state.initialized {
		latest, err := s.store.LatestSMSMessageID(ctx)
		if err != nil {
			state.logError(s, channel, err)
			return smsNotificationPollInterval
		}
		state.id, state.initialized = latest, true
		state.lastError = ""
	}
	config, enabled, configErr := s.smsNotificationConfig(ctx, channel)
	if configErr != nil {
		state.logError(s, channel, configErr)
	} else if !enabled {
		if newest, latestErr := s.store.LatestSMSMessageID(ctx); latestErr == nil {
			state.id = newest
		}
		state.lastError = ""
	} else {
		messages, listErr := s.store.ListInboundSMSAfterID(ctx, state.id, 100)
		if listErr != nil {
			state.logError(s, channel, listErr)
		} else {
			for _, message := range messages {
				if !smsMessageReadyToNotify(message) {
					state.id = message.ID
					continue
				}
				notification := s.newSMSNotification(ctx, message)
				if sendErr := send(s.notificationDestinationContext(ctx), channel, config, notification); sendErr != nil {
					if message.ID == state.failedMessageID {
						state.failedAttempts++
					} else {
						state.failedMessageID, state.failedAttempts = message.ID, 1
					}
					state.consecutiveFailures++
					state.logError(s, channel, sendErr)
					if state.failedAttempts >= smsNotificationMaxAttempts {
						// Give up on this one message so a permanently failing provider
						// cannot stall every later notification for this channel.
						state.logError(s, channel, fmt.Errorf(
							"dropping inbound SMS notification %d after %d attempts: %w",
							message.ID, state.failedAttempts, sendErr,
						))
						state.id = message.ID
						state.failedMessageID, state.failedAttempts = 0, 0
						state.consecutiveFailures = 0
						continue
					}
					break
				}
				state.id = message.ID
				state.lastError = ""
				state.consecutiveFailures = 0
				state.failedMessageID, state.failedAttempts = 0, 0
			}
		}
	}
	return smsNotificationRetryDelay(state.consecutiveFailures)
}

// A missing/null scope keeps the existing all-device behavior; [] sends nothing.
func notificationMatchesDevice(deviceIDs []string, deviceID string) bool {
	return deviceIDs == nil || slices.Contains(deviceIDs, deviceID)
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
	if channel == "meow" {
		return validateMeowNotificationConfig(config)
	}
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
		Content:     smsdecode.Preview(message.Body),
	}
}

func (s *Server) logSMSNotificationError(channel string, err error) {
	if err != nil && s.logger != nil {
		s.logger.Warn("send inbound SMS notification", "channel", channel, "error", err)
	}
}

// sendSMSNotification 按渠道发送短信，MeoW 与其他独立标题渠道复用 DetailText。
func sendSMSNotification(ctx context.Context, channel string, config map[string]any, message smsNotification) error {
	if channel == "meow" {
		return sendMeowNotification(ctx, config, &message.DeviceID, "收到新短信", message.DetailText())
	}
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
		return sendWecomNotification(ctx, config, &message.DeviceID, wecomSMSValues(message))
	case "lark":
		return sendLarkNotification(ctx, config, &message.DeviceID, larkSMSValues(message))
	default:
		return fmt.Errorf("unsupported SMS notification channel %q", channel)
	}
}

func sendBarkSMSNotification(ctx context.Context, config map[string]any, message smsNotification) error {
	if !notificationMatchesDevice(configStrings(config, "device_ids"), message.DeviceID) {
		return nil
	}
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
	if !notificationMatchesDevice(configStrings(config, "device_ids"), message.DeviceID) {
		return nil
	}
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

func buildPushplusPayload(token, title, content, topic, channel string) map[string]any {
	payload := map[string]any{
		"token":    token,
		"title":    title,
		"content":  content,
		"template": "txt",
	}
	if topic != "" {
		payload["topic"] = topic
	}
	if channel != "" {
		payload["channel"] = channel
	}
	return payload
}

func sendPushplusSMSNotification(ctx context.Context, config map[string]any, message smsNotification) error {
	if !notificationMatchesDevice(configStrings(config, "device_ids"), message.DeviceID) {
		return nil
	}
	destination, err := validateOutboundURL(ctx, "https://www.pushplus.plus/send", true)
	if err != nil {
		return err
	}
	payload := buildPushplusPayload(
		configString(config, "token"),
		"收到新短信",
		message.DetailText(),
		configString(config, "topic"),
		configString(config, "channel"),
	)
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
	if !notificationMatchesDevice(configStrings(config, "device_ids"), message.DeviceID) {
		return nil
	}
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
