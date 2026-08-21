package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"vocat/internal/store"
)

const (
	callDeduplicationWindow     = 60 * time.Second
	cellularCallMonitorInterval = 3 * time.Second
)

var (
	callDeduplicationMu  sync.Mutex
	callDeduplicationMap = make(map[string]time.Time)
)

type IncomingCallNotification struct {
	DeviceID    string
	DeviceName  string
	DeviceLabel string
	Caller      string
	Called      string
	Time        time.Time
	Environment string
}

func (value IncomingCallNotification) Title() string {
	return "收到来电"
}

func (value IncomingCallNotification) Text() string {
	envText := "VoWiFi"
	if value.Environment == "cellular" {
		envText = "基站直连"
	}
	return strings.Join([]string{
		"📞 收到来电",
		"设备  " + value.DeviceLabel,
		"来电号码  " + value.Caller,
		"被呼号码  " + value.Called,
		"时间  " + value.Time.Local().Format("2006-01-02 15:04:05"),
		"网络  " + envText,
	}, "\n")
}

func (value IncomingCallNotification) DetailText() string {
	lines := strings.Split(value.Text(), "\n")
	return strings.Join(lines[1:], "\n")
}

func shouldSuppressDuplicateCall(key string, now time.Time, window time.Duration) bool {
	callDeduplicationMu.Lock()
	defer callDeduplicationMu.Unlock()
	for k, t := range callDeduplicationMap {
		if now.Sub(t) > window*2 {
			delete(callDeduplicationMap, k)
		}
	}
	if lastTime, exists := callDeduplicationMap[key]; exists {
		if now.Sub(lastTime) < window {
			return true
		}
	}
	callDeduplicationMap[key] = now
	return false
}

// NotifyIncomingCall delivers an incoming call alert to all configured notification channels.
func (s *Server) NotifyIncomingCall(ctx context.Context, notification IncomingCallNotification) {
	if ctx == nil {
		ctx = context.Background()
	}
	caller := strings.TrimSpace(notification.Caller)
	if caller == "" {
		caller = "未知号码"
	}
	notification.Caller = caller

	called := strings.TrimSpace(notification.Called)
	if called == "" {
		called = "--"
	}
	notification.Called = called

	if notification.Time.IsZero() {
		notification.Time = time.Now().UTC()
	}
	if s.logger != nil {
		s.logger.Info("incoming call detected",
			"category", "call",
			"event", "call.incoming",
			"device_id", notification.DeviceID,
			"caller", notification.Caller,
			"called", notification.Called,
			"transport", notification.Environment,
		)
	}

	dedupKey := fmt.Sprintf("%s:%s", notification.DeviceID, notification.Caller)
	if shouldSuppressDuplicateCall(dedupKey, notification.Time, callDeduplicationWindow) {
		if s.logger != nil {
			s.logger.Debug("suppressed duplicate incoming call notification", "category", "call", "device_id", notification.DeviceID, "caller", notification.Caller)
		}
		return
	}

	if notification.DeviceLabel == "" || notification.DeviceLabel == "--" {
		if configured, err := s.store.Device(ctx, notification.DeviceID); err == nil {
			notification.DeviceName = strings.TrimSpace(configured.Name)
			notification.DeviceLabel = firstNonEmpty(configured.Name, configured.ID, "--")
		} else {
			notification.DeviceLabel = firstNonEmpty(notification.DeviceID, "--")
		}
	}

	destCtx := s.notificationDestinationContext(ctx)
	for _, channel := range []string{"telegram", "bark", "email", "pushplus", "webhook", "wecom", "lark"} {
		setting, err := s.store.NotificationSetting(destCtx, channel)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !setting.Enabled) {
			continue
		}
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("read incoming call notification setting", "channel", channel, "error", err)
			}
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(setting.Config, &config); err != nil {
			if s.logger != nil {
				s.logger.Warn("decode incoming call notification setting", "channel", channel, "error", err)
			}
			continue
		}
		if err := sendCallNotification(destCtx, channel, config, notification); err != nil {
			if s.logger != nil {
				s.logger.Warn("send incoming call notification", "category", "call", "channel", channel, "device_id", notification.DeviceID, "caller", notification.Caller, "raw_error", err)
			}
		}
	}
}

func sendCallNotification(ctx context.Context, channel string, config map[string]any, message IncomingCallNotification) error {
	switch channel {
	case "telegram":
		return sendTelegramTextNotification(ctx, config, message.Text())
	case "bark":
		return sendBarkTextNotification(ctx, config, message.Title(), message.DetailText())
	case "email":
		return sendEmailTextNotification(ctx, config, message.Title()+" - "+message.DeviceLabel, message.Text())
	case "pushplus":
		return sendPushplusTextNotification(ctx, config, message.Title(), message.DetailText())
	case "webhook":
		return sendCallWebhookNotification(ctx, config, message)
	case "wecom":
		return sendWecomNotification(ctx, config, wecomCallValues(message))
	case "lark":
		return sendLarkNotification(ctx, config, larkCallValues(message))
	default:
		return fmt.Errorf("unsupported notification channel %q", channel)
	}
}

func renderCallWebhookTemplate(template string, message IncomingCallNotification) string {
	rendered := message.Text()
	if strings.TrimSpace(template) != "" {
		replacements := map[string]string{
			"{{text}}":         rendered,
			"{{content}}":      message.DetailText(),
			"{{event}}":        "call.received",
			"{{timestamp}}":    message.Time.UTC().Format(time.RFC3339),
			"{{time}}":         message.Time.Local().Format("2006-01-02 15:04:05"),
			"{{number}}":       message.Caller,
			"{{caller}}":       message.Caller,
			"{{called}}":       message.Called,
			"{{device_id}}":    message.DeviceID,
			"{{device_name}}":  message.DeviceName,
			"{{device_label}}": message.DeviceLabel,
			"{{environment}}":  message.Environment,
		}
		for placeholder, value := range replacements {
			template = strings.ReplaceAll(template, placeholder, value)
		}
		return template
	}
	return rendered
}

func sendCallWebhookNotification(ctx context.Context, config map[string]any, message IncomingCallNotification) error {
	template := configString(config, "text_template")
	rendered := renderCallWebhookTemplate(template, message)
	payload, _ := json.Marshal(map[string]any{
		"event":        "call.received",
		"message":      rendered,
		"timestamp":    message.Time.UTC().Format(time.RFC3339),
		"device_id":    message.DeviceID,
		"device_name":  message.DeviceName,
		"device_label": message.DeviceLabel,
		"caller":       message.Caller,
		"called":       message.Called,
		"environment":  message.Environment,
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
				return fmt.Errorf("create call webhook notification request: %w", requestErr)
			}
			for name, value := range configStringMap(config, "headers") {
				request.Header.Set(name, value)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "vocat-call-notification/1")
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

func wecomCallValues(message IncomingCallNotification) wecomTemplateValues {
	return wecomTemplateValues{
		"event":        "call.received",
		"title":        message.Title(),
		"message":      message.Text(),
		"timestamp":    message.Time.UTC().Format(time.RFC3339),
		"content":      message.DetailText(),
		"number":       message.Caller,
		"device_id":    message.DeviceID,
		"device_name":  message.DeviceName,
		"device_label": message.DeviceLabel,
		"time":         message.Time.Local().Format("2006-01-02 15:04:05"),
	}
}

func larkCallValues(message IncomingCallNotification) larkTemplateValues {
	return larkTemplateValues{
		"event":        "call.received",
		"title":        message.Title(),
		"message":      message.Text(),
		"timestamp":    message.Time.UTC().Format(time.RFC3339),
		"content":      message.DetailText(),
		"number":       message.Caller,
		"device_id":    message.DeviceID,
		"device_name":  message.DeviceName,
		"device_label": message.DeviceLabel,
		"time":         message.Time.Local().Format("2006-01-02 15:04:05"),
	}
}

// StartCellularCallMonitor scans physical modems for incoming calls in cellular mode.
func (s *Server) StartCellularCallMonitor(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(cellularCallMonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollCellularCalls(ctx)
		}
	}
}

func (s *Server) pollCellularCalls(ctx context.Context) {
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return
	}
	for _, config := range devices {
		if !config.NetworkEnabled {
			continue
		}
		// If VoWiFi is active, incoming calls are handled directly by SIP INVITE in real time.
		if s.callTransport(config.ID) == "vowifi" {
			continue
		}
		entry, physicalID, present := s.physicalForConfig(config)
		if !present {
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		response, err := s.devices.ExecuteAT(pollCtx, physicalID, "AT+CLCC")
		cancel()
		if err != nil || !response.OK() {
			continue
		}
		calls := parseCLCC(response)
		for _, call := range calls {
			if isIncomingVoiceCLCC(call) {
				caller, _ := call["number"].(string)
				if caller == "" {
					caller = "未知号码"
				}
				called := ""
				if entry.Snapshot != nil {
					called = entry.Snapshot.Phone.Number
				}
				s.NotifyIncomingCall(ctx, IncomingCallNotification{
					DeviceID:    config.ID,
					DeviceName:  strings.TrimSpace(config.Name),
					DeviceLabel: firstNonEmpty(config.Name, config.ID, "--"),
					Caller:      caller,
					Called:      firstNonEmpty(called, "--"),
					Time:        time.Now().UTC(),
					Environment: "cellular",
				})
			}
		}
	}
}

func isIncomingVoiceCLCC(call map[string]any) bool {
	direction, _ := call["direction"].(int)
	state, _ := call["state"].(int)
	mode, _ := call["mode"].(int)
	// direction 1 = incoming (Mobile Terminated)
	// mode 0 = voice; some modems also expose packet-data sessions as CLCC mode 1
	// state 4 = incoming/ringing, 5 = waiting, 0 = active, 3 = alerting
	return direction == 1 && mode == 0 && (state == 4 || state == 5 || state == 0 || state == 3)
}
