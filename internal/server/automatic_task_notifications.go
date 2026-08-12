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

type automaticTaskNotification struct {
	Title string
	Text  string
	Time  time.Time
	Task  store.AutomaticTask
	Run   store.AutomaticTaskRun
}

func (s *Server) notifyAutomaticTask(ctx context.Context, task store.AutomaticTask, run store.AutomaticTaskRun) {
	deviceLabel := task.DeviceID
	if configured, err := s.store.Device(ctx, task.DeviceID); err == nil {
		deviceLabel = firstNonEmpty(configured.Name, configured.ID)
	}
	status := "成功"
	detail := firstNonEmpty(run.Output, "任务已完成")
	if run.Status != "success" {
		status = "失败"
		detail = firstNonEmpty(run.Error, "未知错误")
	}
	taskType := map[string]string{"sms": "发送短信", "call": "拨打电话", "public_ip": "获取漫游公网 IP"}[task.TaskType]
	environment := map[string]string{"vowifi": "VoWiFi", "cellular": "基站直连"}[task.Environment]
	notification := automaticTaskNotification{
		Title: "自动任务执行" + status,
		Text: strings.Join([]string{
			"自动任务执行" + status,
			"任务  " + task.Name,
			"设备  " + deviceLabel,
			"类型  " + firstNonEmpty(taskType, task.TaskType),
			"环境  " + firstNonEmpty(environment, task.Environment),
			"时间  " + run.FinishedAt.Local().Format("2006-01-02 15:04:05"),
			"结果  " + detail,
		}, "\n"),
		Time: run.FinishedAt, Task: task, Run: run,
	}
	for _, channel := range []string{"telegram", "bark", "email", "pushplus", "webhook", "wecom"} {
		setting, err := s.store.NotificationSetting(ctx, channel)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !setting.Enabled) {
			continue
		}
		if err != nil {
			s.logger.Warn("read automatic task notification setting", "channel", channel, "error", err)
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(setting.Config, &config); err != nil {
			s.logger.Warn("decode automatic task notification setting", "channel", channel, "error", err)
			continue
		}
		if err := sendAutomaticTaskNotification(ctx, channel, config, notification); err != nil {
			s.logger.Warn("send automatic task notification", "channel", channel, "task_id", task.ID, "error", err)
		}
	}
}

func sendAutomaticTaskNotification(ctx context.Context, channel string, config map[string]any, message automaticTaskNotification) error {
	switch channel {
	case "telegram":
		return sendTelegramTextNotification(ctx, config, message.Text)
	case "bark":
		return sendBarkTextNotification(ctx, config, message.Title, message.Text)
	case "email":
		return sendEmailTextNotification(ctx, config, message.Title, message.Text)
	case "pushplus":
		return sendPushplusTextNotification(ctx, config, message.Title, message.Text)
	case "webhook":
		return sendAutomaticTaskWebhook(ctx, config, message)
	case "wecom":
		return sendWecomNotification(ctx, config, wecomAutomaticTaskValues(message))
	default:
		return fmt.Errorf("unsupported notification channel %q", channel)
	}
}

func sendTelegramTextNotification(ctx context.Context, config map[string]any, text string) error {
	token := configString(config, "bot_token")
	parsed, err := validateTelegramAPIURL(ctx, configString(config, "base_url"), token, "sendMessage")
	if err != nil {
		return err
	}
	client, err := restrictedHTTPClient(ctx, 8*time.Second, configString(config, "proxy"))
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"chat_id": configString(config, "chat_id"), "text": text})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "vocat-automatic-task/1")
	return performNotificationRequest(client, request, true)
}

func sendBarkTextNotification(ctx context.Context, config map[string]any, title, text string) error {
	client, err := restrictedHTTPClient(ctx, 8*time.Second, "")
	if err != nil {
		return err
	}
	payload := map[string]any{"title": title, "body": text}
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
			return err
		}
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		request.Header.Set("User-Agent", "vocat-automatic-task/1")
		if err := performNotificationRequest(client, request, false); err != nil {
			return err
		}
	}
	return nil
}

func sendPushplusTextNotification(ctx context.Context, config map[string]any, title, text string) error {
	destination, err := validateOutboundURL(ctx, "https://www.pushplus.plus/send", true)
	if err != nil {
		return err
	}
	payload := map[string]any{"token": configString(config, "token"), "title": title, "content": text, "template": "txt", "timestamp": time.Now().UnixMilli()}
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
		return err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", "vocat-automatic-task/1")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.Unmarshal(body, &result) != nil || result.Code != 200 {
		return fmt.Errorf("%w: Pushplus HTTP %d code %d %s", errProviderRejected, response.StatusCode, result.Code, result.Msg)
	}
	return nil
}

func sendAutomaticTaskWebhook(ctx context.Context, config map[string]any, message automaticTaskNotification) error {
	payload, _ := json.Marshal(map[string]any{
		"event": "automatic_task.completed", "message": message.Text,
		"timestamp": message.Time.UTC().Format(time.RFC3339), "task_id": message.Task.ID,
		"task_name": message.Task.Name, "device_id": message.Task.DeviceID,
		"task_type": message.Task.TaskType, "environment": message.Task.Environment,
		"status": message.Run.Status, "attempts": message.Run.Attempts,
		"output": message.Run.Output, "error": message.Run.Error,
	})
	client, err := restrictedHTTPClient(ctx, durationMilliseconds(configInt(config, "timeout_ms"), 5*time.Second), "")
	if err != nil {
		return err
	}
	for _, destination := range configStrings(config, "urls") {
		parsed, err := validateOutboundURL(ctx, destination, false)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
		if err != nil {
			return err
		}
		for name, value := range configStringMap(config, "headers") {
			request.Header.Set(name, value)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "vocat-automatic-task/1")
		if secret := configString(config, "secret"); secret != "" {
			signature := hmac.New(sha256.New, []byte(secret))
			_, _ = signature.Write(payload)
			request.Header.Set("X-vocat-Signature", "sha256="+hex.EncodeToString(signature.Sum(nil)))
		}
		if err := performNotificationRequest(client, request, false); err != nil {
			return err
		}
	}
	return nil
}

func sendEmailTextNotification(ctx context.Context, config map[string]any, subject, text string) error {
	host := strings.TrimSpace(configString(config, "smtp_host"))
	port := configInt(config, "smtp_port")
	if port == 0 {
		port = 587
	}
	timeout := 8 * time.Second
	connection, err := dialRestricted(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	useSSL, _ := config["use_ssl"].(bool)
	implicitTLS := port == 465 || useSSL
	if implicitTLS {
		secure := tls.Client(connection, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		connection = secure
	}
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if !implicitTLS {
		if available, _ := client.Extension("STARTTLS"); !available {
			return errors.New("SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	username, password := configString(config, "username"), configString(config, "password")
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			return err
		}
	}
	from, err := parseMailAddress(configString(config, "from_address"))
	if err != nil {
		return err
	}
	var recipients []*mail.Address
	for _, item := range configStrings(config, "to_addresses") {
		address, err := parseMailAddress(item)
		if err != nil {
			return err
		}
		recipients = append(recipients, address)
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if err := writePlainTextMail(writer, from, recipients, subject, text); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
