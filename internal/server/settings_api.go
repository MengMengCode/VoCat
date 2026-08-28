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
	"net/netip"
	"net/smtp"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
)

var (
	errUnsafeDestination = errors.New("notification destination is not allowed")
	errProviderRejected  = errors.New("notification provider rejected the test")
	telegramTokenPattern = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{20,128}$`)
)

var notificationChannels = []string{
	"telegram",
	"email",
	"webhook",
	"bark",
	"pushplus",
	"wecom",
	"lark",
}

var notificationFields = map[string]map[string]string{
	"telegram": {
		"bot_token": "string", "chat_id": "string", "admin_id": "string",
		"base_url": "string", "proxy": "string",
	},
	"email": {
		"use_ssl": "boolean", "smtp_host": "string", "smtp_port": "integer", "username": "string",
		"password": "string", "from_address": "string", "to_addresses": "strings",
	},
	"webhook": {
		"urls": "strings", "secret": "string", "timeout_ms": "integer",
		"retry_max": "integer", "text_template": "string", "headers": "string_map",
	},
	"bark": {
		"urls": "strings", "group": "string", "icon": "string", "level": "string",
	},
	"pushplus": {
		"token": "string", "topic": "string", "channel": "string",
	},
	"wecom": {
		"urls": "strings", "payload_template": "string",
	},
	"lark": {
		"url": "string", "signing_enabled": "boolean", "secret": "string", "payload_template": "string",
	},
}

// routeSettingsAPI is intentionally independent of the main router so it can
// be wired after the surrounding authentication and CSRF checks.
func (s *Server) routeSettingsAPI(
	w http.ResponseWriter,
	r *http.Request,
	cleanPath string,
) bool {
	cleanPath = strings.Trim(cleanPath, "/")
	switch cleanPath {
	case "settings/notifications":
		s.handleNotificationSettings(w, r)
		return true
	case "traffic/analysis":
		s.handleTrafficAnalysis(w, r)
		return true
	case "cards/policies":
		s.handleCardPolicies(w, r)
		return true
	case "settings/security":
		s.handleSecuritySettings(w, r)
		return true
	case "settings/logging":
		s.handleLoggingSettings(w, r)
		return true
	}
	segments := splitAPIPath(cleanPath)
	if len(segments) == 4 &&
		segments[0] == "settings" &&
		segments[1] == "notifications" &&
		segments[3] == "test" {
		s.handleNotificationTest(w, r, segments[2])
		return true
	}
	if len(segments) == 3 && segments[0] == "cards" && segments[2] == "policy" {
		s.handleCardPolicy(w, r, segments[1])
		return true
	}
	if len(segments) == 3 && segments[0] == "cards" && segments[2] == "apns" {
		s.handleCardAPNProfiles(w, r, segments[1], "")
		return true
	}
	if len(segments) == 4 && segments[0] == "cards" && segments[2] == "apns" {
		s.handleCardAPNProfiles(w, r, segments[1], segments[3])
		return true
	}
	return false
}

func (s *Server) handleNotificationSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeNotificationSettings(w, r)
	case http.MethodPut:
		var request map[string]json.RawMessage
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if request == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a JSON object")
			return
		}
		values := make([]store.NotificationSetting, 0, len(request))
		for _, channel := range notificationChannels {
			raw, present := request[channel]
			if !present {
				continue
			}
			enabled, config, err := decodeNotificationConfig(channel, raw, true)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
				return
			}
			values = append(values, store.NotificationSetting{
				Channel:         channel,
				Enabled:         enabled,
				Config:          config,
				SensitiveFields: store.DefaultNotificationSensitiveFields(channel),
			})
		}
		for channel := range request {
			if !knownNotificationChannel(channel) {
				writeError(
					w,
					http.StatusBadRequest,
					"invalid_notification_channel",
					fmt.Sprintf("unsupported notification channel %q", channel),
				)
				return
			}
		}
		if err := s.store.SaveNotificationSettings(r.Context(), values); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.writeNotificationSettings(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) writeNotificationSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.ListNotificationSettings(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	stored := make(map[string]store.NotificationSetting, len(settings))
	for _, setting := range settings {
		stored[setting.Channel] = setting
	}
	response := make(map[string]any, len(notificationChannels))
	for _, channel := range notificationChannels {
		document := map[string]any{"enabled": false}
		if setting, ok := stored[channel]; ok {
			redacted := setting.Redacted()
			if err := json.Unmarshal(redacted.Config, &document); err != nil {
				s.logger.Error(
					"notification setting contains invalid JSON",
					"channel",
					channel,
					"error",
					err,
				)
				writeError(w, http.StatusInternalServerError, "database_error", "the database operation failed")
				return
			}
			document["enabled"] = setting.Enabled
		}
		response[channel] = document
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": response})
}

func decodeNotificationConfig(
	channel string,
	raw json.RawMessage,
	requireEnabled bool,
) (bool, json.RawMessage, error) {
	if !knownNotificationChannel(channel) {
		return false, nil, fmt.Errorf("unsupported notification channel %q", channel)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil || document == nil {
		return false, nil, fmt.Errorf("%s notification config must be an object", channel)
	}
	enabled := false
	enabledRaw, hasEnabled := document["enabled"]
	if requireEnabled && !hasEnabled {
		return false, nil, fmt.Errorf("%s.enabled is required", channel)
	}
	if hasEnabled {
		if err := json.Unmarshal(enabledRaw, &enabled); err != nil {
			return false, nil, fmt.Errorf("%s.enabled must be a boolean", channel)
		}
		delete(document, "enabled")
	}

	fields := notificationFields[channel]
	for name, value := range document {
		kind, known := fields[name]
		if !known {
			return false, nil, fmt.Errorf("%s.%s is not supported", channel, name)
		}
		if err := validateNotificationField(channel, name, kind, value); err != nil {
			return false, nil, err
		}
	}
	config, err := json.Marshal(document)
	if err != nil {
		return false, nil, fmt.Errorf("encode %s notification config: %w", channel, err)
	}
	if enabled && channel == "lark" {
		var resolved map[string]any
		if err := json.Unmarshal(config, &resolved); err != nil {
			return false, nil, fmt.Errorf("decode lark notification config: %w", err)
		}
		signingEnabled, _ := resolved["signing_enabled"].(bool)
		if signingEnabled && configString(resolved, "url") != store.SecretMask &&
			configString(resolved, "secret") == store.SecretMask {
			return false, nil, errors.New("lark.secret must be re-entered when lark.url changes")
		}
		if err := validateLarkNotificationConfig(resolved); err != nil {
			return false, nil, err
		}
	}
	return enabled, config, nil
}

func validateNotificationField(
	channel string,
	name string,
	kind string,
	raw json.RawMessage,
) error {
	field := channel + "." + name
	switch kind {
	case "boolean":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be a boolean", field)
		}
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be a string", field)
		}
		limit := 4096
		if name == "text_template" {
			limit = 32768
		}
		if channel == "lark" && name == "payload_template" {
			limit = maxLarkPayloadBytes
		}
		if len(value) > limit || strings.ContainsAny(value, "\x00") {
			return fmt.Errorf("%s is too long or contains invalid characters", field)
		}
		if name == "base_url" && value != "" {
			if _, err := telegramAPIURL(value, "123456:validation-token", "sendMessage"); err != nil {
				return fmt.Errorf("%s must be an absolute HTTPS URL or a URL template with two %%s placeholders", field)
			}
		}
		if name == "proxy" && value != "" {
			if _, err := parseProxyURL(value); err != nil {
				return fmt.Errorf("%s is not a valid HTTP URL", field)
			}
		}
		if channel == "telegram" && name == "chat_id" && strings.TrimSpace(value) != "" {
			chatID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || chatID == 0 {
				return fmt.Errorf("%s must be a non-zero integer", field)
			}
		}
		if channel == "telegram" && name == "admin_id" && strings.TrimSpace(value) != "" {
			adminID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || adminID <= 0 {
				return fmt.Errorf("%s must be a positive integer", field)
			}
		}
		if name == "from_address" && value != "" {
			if _, err := parseMailAddress(value); err != nil {
				return fmt.Errorf("%s is not a valid email address", field)
			}
		}
		if channel == "wecom" && name == "payload_template" && value != "" {
			if _, err := renderWecomPayload(value, wecomTestValues(time.Unix(0, 0))); err != nil {
				return fmt.Errorf("%s is not a valid JSON template: %w", field, err)
			}
		}
		if channel == "lark" && name == "payload_template" && value != "" {
			if _, err := renderLarkPayload(value, larkTestValues(time.Unix(0, 0))); err != nil {
				return fmt.Errorf("%s is not a valid JSON template: %w", field, err)
			}
		}
		if channel == "lark" && name == "url" && value != "" && value != store.SecretMask {
			if _, err := parseLarkWebhookURL(value); err != nil {
				return fmt.Errorf("%s must be a valid Feishu or Lark group bot webhook URL: %w", field, err)
			}
		}
	case "integer":
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be an integer", field)
		}
		switch name {
		case "smtp_port":
			if value < 0 || value > 65535 {
				return fmt.Errorf("%s must be between 0 and 65535", field)
			}
		case "timeout_ms":
			if value != 0 && (value < 100 || value > 60000) {
				return fmt.Errorf("%s must be 0 or between 100 and 60000", field)
			}
		case "retry_max":
			if value < 0 || value > 10 {
				return fmt.Errorf("%s must be between 0 and 10", field)
			}
		}
	case "strings":
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("%s must be an array of strings", field)
		}
		if len(values) > 32 {
			return fmt.Errorf("%s cannot contain more than 32 values", field)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 4096 ||
				strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("%s contains an invalid value", field)
			}
			if name == "urls" {
				if channel == "wecom" && value == store.SecretMask {
					continue
				}
				if _, err := parseOutboundURL(value, false); err != nil {
					return fmt.Errorf("%s contains an invalid HTTP URL", field)
				}
			}
			if name == "to_addresses" {
				if _, err := mail.ParseAddress(value); err != nil {
					return fmt.Errorf("%s contains an invalid email address", field)
				}
			}
		}
	case "string_map":
		var values map[string]string
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("%s must be an object of strings", field)
		}
		if len(values) > 32 {
			return fmt.Errorf("%s cannot contain more than 32 entries", field)
		}
		for key, value := range values {
			if strings.TrimSpace(key) == "" || len(key) > 128 ||
				strings.ContainsAny(key, "\r\n:\x00") {
				return fmt.Errorf("%s contains an invalid header name", field)
			}
			if len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("%s contains an invalid header value", field)
			}
		}
	default:
		return fmt.Errorf("%s has an unsupported field type", field)
	}
	return nil
}

func knownNotificationChannel(channel string) bool {
	_, ok := notificationFields[channel]
	return ok
}

func (s *Server) handleNotificationTest(
	w http.ResponseWriter,
	r *http.Request,
	channel string,
) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if !knownNotificationChannel(channel) {
		writeError(w, http.StatusNotFound, "not_found", "notification channel was not found")
		return
	}
	if channel != "webhook" && channel != "telegram" && channel != "email" && channel != "bark" && channel != "wecom" && channel != "lark" {
		writeError(
			w,
			http.StatusNotImplemented,
			"notification_test_unsupported",
			"this notification channel does not support a connectivity test",
		)
		return
	}
	var raw json.RawMessage
	if err := s.decodeJSON(w, r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	_, incoming, err := decodeNotificationConfig(channel, raw, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
		return
	}
	resolved, provider, err := s.resolveNotificationTestConfig(
		r.Context(),
		channel,
		incoming,
	)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(
				w,
				http.StatusBadRequest,
				"notification_not_configured",
				"notification channel is not configured",
			)
			return
		}
		s.writeStoreError(w, err)
		return
	}
	if err := validateNotificationTestConfig(channel, resolved); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
		return
	}

	notificationContext := s.notificationDestinationContext(r.Context())
	switch channel {
	case "webhook":
		err = sendWebhookNotificationTest(notificationContext, resolved)
	case "telegram":
		err = sendTelegramNotificationTest(notificationContext, resolved)
	case "email":
		err = sendEmailNotificationTest(notificationContext, resolved)
	case "bark":
		err = sendBarkNotificationTest(notificationContext, resolved)
	case "wecom":
		err = sendWecomNotificationTest(notificationContext, resolved)
	case "lark":
		err = sendLarkNotificationTest(notificationContext, resolved)
	}
	if err != nil {
		redacted := store.RedactText(err.Error(), provider)
		if s.logger != nil {
			s.logger.Warn(
				"notification connectivity test failed",
				"channel",
				channel,
				"error",
				redacted,
			)
		}
		switch {
		case errors.Is(err, errUnsafeDestination):
			writeError(
				w,
				http.StatusBadRequest,
				"unsafe_destination",
				"notification destination resolved to an unusable or protected system address",
			)
		case errors.Is(err, errProviderRejected):
			writeError(
				w,
				http.StatusBadGateway,
				"notification_provider_rejected",
				"notification provider rejected the test message",
			)
		default:
			writeError(
				w,
				http.StatusBadGateway,
				"notification_test_failed",
				"notification provider could not be reached or the test message failed",
			)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"channel":   channel,
			"success":   true,
			"tested_at": time.Now().UTC(),
		},
	})
}

func (s *Server) resolveNotificationTestConfig(
	ctx context.Context,
	channel string,
	incoming json.RawMessage,
) (map[string]any, store.NotificationSetting, error) {
	current, err := s.store.NotificationSetting(ctx, channel)
	notConfigured := errors.Is(err, store.ErrNotFound)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, store.NotificationSetting{}, err
	}
	if notConfigured {
		current = store.NotificationSetting{
			Channel:         channel,
			Config:          json.RawMessage(`{}`),
			SensitiveFields: store.DefaultNotificationSensitiveFields(channel),
		}
	}
	var resolved map[string]any
	if err := json.Unmarshal(current.Config, &resolved); err != nil {
		return nil, store.NotificationSetting{}, fmt.Errorf("decode stored notification config: %w", err)
	}
	var overlay map[string]any
	if err := json.Unmarshal(incoming, &overlay); err != nil {
		return nil, store.NotificationSetting{}, fmt.Errorf("decode notification test config: %w", err)
	}
	sensitive := make(map[string]struct{})
	for _, field := range store.DefaultNotificationSensitiveFields(channel) {
		sensitive[field] = struct{}{}
	}
	for key, value := range overlay {
		if _, secret := sensitive[key]; secret {
			value = mergeNotificationTestSecretValue(value, resolved[key])
		}
		resolved[key] = value
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return nil, store.NotificationSetting{}, err
	}
	if len(resolved) == 0 && notConfigured {
		return nil, store.NotificationSetting{}, store.ErrNotFound
	}
	_, normalized, err := decodeNotificationConfig(channel, encoded, false)
	if err != nil {
		return nil, store.NotificationSetting{}, err
	}
	if err := json.Unmarshal(normalized, &resolved); err != nil {
		return nil, store.NotificationSetting{}, err
	}
	provider := store.NotificationSetting{
		Channel:         channel,
		Config:          normalized,
		SensitiveFields: store.DefaultNotificationSensitiveFields(channel),
	}
	return resolved, provider, nil
}

// mergeNotificationTestSecretValue preserves masked values submitted by the
// settings form while allowing newly entered sensitive values in the same
// request. Provider webhook URLs can be sensitive lists, unlike the
// string-based secrets used by the other notification channels.
func mergeNotificationTestSecretValue(incoming, existing any) any {
	if incoming == nil {
		return existing
	}
	switch next := incoming.(type) {
	case string:
		if next == "" || next == store.SecretMask {
			return existing
		}
	case []any:
		previous, ok := existing.([]any)
		if !ok {
			return incoming
		}
		merged := make([]any, len(next))
		for index, value := range next {
			if index < len(previous) {
				merged[index] = mergeNotificationTestSecretValue(value, previous[index])
			} else {
				merged[index] = value
			}
		}
		return merged
	}
	return incoming
}

func validateNotificationTestConfig(channel string, config map[string]any) error {
	switch channel {
	case "webhook":
		urls := configStrings(config, "urls")
		if len(urls) == 0 {
			return errors.New("webhook.urls must contain at least one URL")
		}
		if len(urls) > 8 {
			return errors.New("webhook test is limited to 8 URLs")
		}
	case "bark":
		urls := configStrings(config, "urls")
		if len(urls) == 0 {
			return errors.New("bark.urls must contain at least one URL")
		}
		if len(urls) > 8 {
			return errors.New("bark test is limited to 8 URLs")
		}
	case "wecom":
		return validateWecomNotificationConfig(config)
	case "lark":
		return validateLarkNotificationConfig(config)
	case "telegram":
		token := configString(config, "bot_token")
		if token == "" || token == store.SecretMask {
			return errors.New("telegram.bot_token is required")
		}
		if !telegramTokenPattern.MatchString(token) {
			return errors.New("telegram.bot_token has an invalid format")
		}
		if configString(config, "chat_id") == "" {
			return errors.New("telegram.chat_id is required")
		}
		if baseURL := configString(config, "base_url"); baseURL != "" {
			if _, err := telegramAPIURL(baseURL, token, "sendMessage"); err != nil {
				return errors.New("telegram.base_url must be an absolute HTTPS URL or a URL template with two %s placeholders")
			}
		}
	case "email":
		if configString(config, "smtp_host") == "" {
			return errors.New("email.smtp_host is required")
		}
		if configString(config, "from_address") == "" {
			return errors.New("email.from_address is required")
		}
		if len(configStrings(config, "to_addresses")) == 0 {
			return errors.New("email.to_addresses must contain at least one address")
		}
		if configString(config, "password") != "" && configString(config, "username") == "" {
			return errors.New("email.username is required when a password is configured")
		}
	}
	return nil
}

func sendWebhookNotificationTest(ctx context.Context, config map[string]any) error {
	timeout := durationMilliseconds(configInt(config, "timeout_ms"), 5*time.Second)
	client, err := restrictedHTTPClient(ctx, timeout, "")
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"event":     "test",
		"message":   "vocat notification test",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	for _, destination := range configStrings(config, "urls") {
		parsed, err := validateOutboundURL(ctx, destination, false)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			parsed.String(),
			bytes.NewReader(payload),
		)
		if err != nil {
			return fmt.Errorf("create webhook test request: %w", err)
		}
		for name, value := range configStringMap(config, "headers") {
			request.Header.Set(name, value)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "vocat-notification-test/1")
		if secret := configString(config, "secret"); secret != "" {
			signature := hmac.New(sha256.New, []byte(secret))
			_, _ = signature.Write(payload)
			request.Header.Set(
				"X-vocat-Signature",
				"sha256="+hex.EncodeToString(signature.Sum(nil)),
			)
		}
		if err := performNotificationRequest(client, request, false); err != nil {
			return err
		}
	}
	return nil
}

func sendBarkNotificationTest(ctx context.Context, config map[string]any) error {
	client, err := restrictedHTTPClient(ctx, 6*time.Second, "")
	if err != nil {
		return err
	}
	message := map[string]any{
		"title": "vocat",
		"body":  "vocat notification test",
	}
	if group := configString(config, "group"); group != "" {
		message["group"] = group
	}
	if icon := configString(config, "icon"); icon != "" {
		message["icon"] = icon
	}
	if level := configString(config, "level"); level != "" {
		message["level"] = level
	}
	payload, _ := json.Marshal(message)
	for _, destination := range configStrings(config, "urls") {
		parsed, err := validateOutboundURL(ctx, destination, false)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			parsed.String(),
			bytes.NewReader(payload),
		)
		if err != nil {
			return fmt.Errorf("create bark test request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		request.Header.Set("User-Agent", "vocat-notification-test/1")
		if err := performNotificationRequest(client, request, false); err != nil {
			return err
		}
	}
	return nil
}

func sendTelegramNotificationTest(ctx context.Context, config map[string]any) error {
	token := configString(config, "bot_token")
	parsed, err := validateTelegramAPIURL(ctx, configString(config, "base_url"), token, "sendMessage")
	if err != nil {
		return err
	}
	client, err := restrictedHTTPClient(ctx, 6*time.Second, configString(config, "proxy"))
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"chat_id": configString(config, "chat_id"),
		"text":    "vocat notification test",
	})
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		parsed.String(),
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("create Telegram test request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "vocat-notification-test/1")
	return performNotificationRequest(client, request, true)
}

func performNotificationRequest(
	client *http.Client,
	request *http.Request,
	requireTelegramOK bool,
) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send notification test: %w", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if readErr != nil {
		return fmt.Errorf("read notification response: %w", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", errProviderRejected, response.StatusCode)
	}
	if requireTelegramOK {
		var result struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(body, &result) != nil || !result.OK {
			return fmt.Errorf("%w: Telegram response was not successful", errProviderRejected)
		}
	}
	return nil
}

func sendEmailNotificationTest(ctx context.Context, config map[string]any) error {
	host := strings.TrimSpace(configString(config, "smtp_host"))
	port := configInt(config, "smtp_port")
	if port == 0 {
		port = 587
	}
	timeout := 8 * time.Second
	address := net.JoinHostPort(host, strconv.Itoa(port))
	connection, err := dialRestricted(ctx, "tcp", address, timeout)
	if err != nil {
		return fmt.Errorf("connect SMTP server: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
	}
	if port == 465 {
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
	if port != 465 {
		if available, _ := client.Extension("STARTTLS"); !available {
			return errors.New("SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	username := configString(config, "username")
	password := configString(config, "password")
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
	// Addresses are parsed as RFC mailboxes, the subject rejects control
	// characters, and the body is MIME-base64 encoded by writePlainTextMail.
	// CodeQL's email-injection query has no sanitizer model for these steps.
	// Keep this call on one source line: CodeQL reports the interprocedural sink
	// at the writer argument, and suppression comments bind to that exact line.
	// codeql[go/email-injection]
	// CodeQL [go/email-injection]
	// lgtm[go/email-injection]
	if err := writePlainTextMail(writer, from, recipients, "vocat notification test", "This is a vocat notification test."); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP test message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("%w: SMTP message not accepted", errProviderRejected)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}

func parseMailAddress(value string) (*mail.Address, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return nil, errors.New("email address contains a prohibited control character")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address == "" || strings.ContainsAny(address.Address, "\r\n\x00") {
		return nil, errors.New("invalid email address")
	}
	for _, character := range address.Name {
		if character < 0x20 || character == 0x7f {
			return nil, errors.New("email display name contains a prohibited control character")
		}
	}
	return address, nil
}

func formatMailAddress(address *mail.Address) string {
	if address.Name == "" {
		return address.Address
	}
	return (&mail.Address{Name: address.Name, Address: address.Address}).String()
}

func restrictedHTTPClient(
	ctx context.Context,
	timeout time.Duration,
	proxy string,
) (*http.Client, error) {
	return newRestrictedHTTPClient(ctx, timeout, proxy, false)
}

func persistentRestrictedHTTPClient(
	ctx context.Context,
	timeout time.Duration,
	proxy string,
) (*http.Client, error) {
	return newRestrictedHTTPClient(ctx, timeout, proxy, true)
}

func newRestrictedHTTPClient(
	ctx context.Context,
	timeout time.Duration,
	proxy string,
	keepAlives bool,
) (*http.Client, error) {
	timeout = clampNotificationTimeout(timeout)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           restrictedDialer(timeout),
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     !keepAlives,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	if keepAlives {
		transport.MaxIdleConns = 8
		transport.MaxIdleConnsPerHost = 4
	}
	if strings.TrimSpace(proxy) != "" {
		parsed, err := validateNotificationProxyURL(ctx, proxy)
		if err != nil {
			return nil, fmt.Errorf("validate notification proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(parsed)
		transport.DialContext = notificationProxyDialer(timeout)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("notification provider redirects are not allowed")
		},
	}, nil
}

func clampNotificationTimeout(timeout time.Duration) time.Duration {
	if timeout < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if timeout > 10*time.Second {
		return 10 * time.Second
	}
	return timeout
}

func durationMilliseconds(value int, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

func validateOutboundURL(
	ctx context.Context,
	raw string,
	requireHTTPS bool,
) (*url.URL, error) {
	parsed, err := parseOutboundURL(raw, requireHTTPS)
	if err != nil {
		return nil, err
	}
	if _, err := resolvePublicAddresses(ctx, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func validateNotificationProxyURL(ctx context.Context, raw string) (*url.URL, error) {
	parsed, err := parseProxyURL(raw)
	if err != nil {
		return nil, err
	}
	if _, err := resolveNotificationProxyAddresses(ctx, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

// parseProxyURL parses an HTTP(S) proxy URL. Unlike parseOutboundURL, it
// permits embedded userinfo (http://user:pass@host:port) because HTTP proxies
// commonly authenticate with Proxy-Authorization derived from the URL.
func parseProxyURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.IsAbs() == false {
		return nil, errors.New("proxy must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("proxy URL must use HTTP or HTTPS")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("proxy URL has an invalid port")
		}
	}
	return parsed, nil
}

func parseOutboundURL(raw string, requireHTTPS bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.IsAbs() == false {
		return nil, errors.New("destination must be an absolute HTTP URL")
	}
	if parsed.User != nil {
		return nil, errors.New("destination URL cannot contain user information")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("destination URL must use HTTP or HTTPS")
	}
	if requireHTTPS && parsed.Scheme != "https" {
		return nil, errors.New("destination URL must use HTTPS")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("destination URL has an invalid port")
		}
	}
	return parsed, nil
}

func restrictedDialer(timeout time.Duration) func(
	context.Context,
	string,
	string,
) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		return dialRestricted(ctx, network, address, timeout)
	}
}

func notificationProxyDialer(timeout time.Duration) func(
	context.Context,
	string,
	string,
) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		return dialNotification(ctx, network, address, timeout, true)
	}
}

func dialRestricted(
	ctx context.Context,
	network string,
	address string,
	timeout time.Duration,
) (net.Conn, error) {
	return dialNotification(ctx, network, address, timeout, false)
}

func dialNotification(
	ctx context.Context,
	network string,
	address string,
	timeout time.Duration,
	allowLocal bool,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse outbound address: %w", err)
	}
	var addresses []netip.Addr
	if allowLocal {
		addresses, err = resolveNotificationProxyAddresses(ctx, host)
	} else {
		addresses, err = resolvePublicAddresses(ctx, host)
	}
	if err != nil {
		return nil, err
	}
	perAddress := clampNotificationTimeout(timeout)
	stagger := 300 * time.Millisecond
	if perAddress < stagger {
		stagger = perAddress / 2
	}

	raceContext, cancel := context.WithCancel(ctx)
	defer cancel()

	type attempt struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan attempt, len(addresses))
	var wg sync.WaitGroup

	launcher := time.NewTicker(stagger)
	defer launcher.Stop()
	for index, ip := range addresses {
		if index > 0 {
			select {
			case <-raceContext.Done():
				break
			case <-launcher.C:
			}
		}
		if raceContext.Err() != nil {
			break
		}
		ip := ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialer := net.Dialer{Timeout: perAddress}
			connection, dialErr := dialer.DialContext(
				raceContext,
				network,
				net.JoinHostPort(ip.String(), port),
			)
			if dialErr != nil {
				resultCh <- attempt{err: dialErr}
				return
			}
			if raceContext.Err() != nil {
				connection.Close()
				resultCh <- attempt{err: raceContext.Err()}
				return
			}
			resultCh <- attempt{conn: connection}
		}()
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var failures []error
	for result := range resultCh {
		if result.conn != nil {
			cancel()
			return result.conn, nil
		}
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			failures = append(failures, result.err)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if len(failures) == 0 {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("dial notification destination: %w", errors.Join(failures...))
}

func (s *Server) notificationDestinationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	// Notification delivery is outbound administrator-configured traffic. It
	// must not inherit the inbound Web access policy: DNS Fake-IP ranges, LAN
	// gateways, and local proxies are valid notification paths.
	return ctx
}

func notificationAddressAllowed(_ context.Context, address netip.Addr) bool {
	address = address.Unmap()
	if !notificationTransportAddress(address) {
		return false
	}
	for _, fakeIP := range notificationFakeIPNetworks {
		if fakeIP.Contains(address) {
			return true
		}
	}
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, blocked := range blockedNotificationDestinationNetworks {
		if blocked.Contains(address) {
			return false
		}
	}
	return true
}

func notificationProxyAddressAllowed(address netip.Addr) bool {
	return notificationTransportAddress(address.Unmap())
}

func notificationTransportAddress(address netip.Addr) bool {
	return address.IsValid() && !address.IsUnspecified() && !address.IsMulticast() &&
		!address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() &&
		address != netip.MustParseAddr("255.255.255.255") &&
		address != netip.MustParseAddr("100.100.100.200")
}

func resolvePublicAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	return resolveNotificationAddresses(ctx, host, false)
}

func resolveNotificationProxyAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	return resolveNotificationAddresses(ctx, host, true)
}

func resolveNotificationAddresses(ctx context.Context, host string, allowLocal bool) ([]netip.Addr, error) {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if normalized == "" {
		return nil, fmt.Errorf("%w: blocked host name", errUnsafeDestination)
	}
	if literal, err := netip.ParseAddr(normalized); err == nil {
		literal = literal.Unmap()
		if (!allowLocal && !notificationAddressAllowed(ctx, literal)) ||
			(allowLocal && !notificationProxyAddressAllowed(literal)) {
			return nil, fmt.Errorf("%w: %s", errUnsafeDestination, literal)
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", normalized)
	if err != nil {
		return nil, fmt.Errorf("resolve notification destination: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("notification destination did not resolve")
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if (!allowLocal && !notificationAddressAllowed(ctx, address)) ||
			(allowLocal && !notificationProxyAddressAllowed(address)) {
			return nil, fmt.Errorf("%w: %s", errUnsafeDestination, address)
		}
		result = append(result, address)
	}
	return result, nil
}

var notificationFakeIPNetworks = []netip.Prefix{
	netip.MustParsePrefix("198.18.0.0/15"),
	// Mihomo/Clash can synthesize ULA addresses alongside its RFC 2544
	// IPv4 Fake-IP range. These addresses are consumed by the local TUN DNS
	// interceptor and do not identify a LAN service.
	netip.MustParsePrefix("fdfe:dcba:9876::/48"),
}

var blockedNotificationDestinationNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func configString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return strings.TrimSpace(value)
}

func configStrings(config map[string]any, key string) []string {
	switch value := config[key].(type) {
	case []string:
		return value
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if ok {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	default:
		return nil
	}
}

func configStringMap(config map[string]any, key string) map[string]string {
	object, ok := config[key].(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(object))
	for name, value := range object {
		text, ok := value.(string)
		if ok {
			result[name] = text
		}
	}
	return result
}

func configInt(config map[string]any, key string) int {
	switch value := config[key].(type) {
	case float64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
	case int:
		return value
	default:
		return 0
	}
}

// handleCardPolicies returns every stored card policy (VoHive: GET /cards/policies).
func (s *Server) handleCardPolicies(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	policies, err := s.store.ListCardPolicies(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	result := make([]map[string]any, 0, len(policies))
	for _, policy := range policies {
		result = append(result, cardPolicyResponse(policy))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

// liveCardPolicyFlags resolves the current VoWiFi/airplane state for the
// device that presently hosts the given SIM (matched by live ICCID), so the card
// policy toggles reflect what the card is actually doing now rather than a stale
// stored value. ok is false when no present device reports this ICCID.
func (s *Server) liveCardPolicyFlags(ctx context.Context, iccid string) (vowifi, airplane, ok bool) {
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		return false, false, false
	}
	clean := strings.TrimSpace(iccid)
	for _, config := range configs {
		entry, _, present := s.physicalForConfig(config)
		if !present || entry.Snapshot == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(entry.Snapshot.ICCID), clean) {
			continue
		}
		// VoWiFi is an RF-off service mode. Surface that fact explicitly: while
		// VoWiFi is selected both switches are on, but the airplane switch is
		// read-only in the UI. Once VoWiFi is disabled, airplane remains on until
		// the user explicitly turns it off.
		return config.VoWiFiEnabled, config.VoWiFiEnabled || entry.Snapshot.FlightMode, true
	}
	return false, false, false
}

func (s *Server) handleCardPolicy(w http.ResponseWriter, r *http.Request, iccid string) {
	iccid = strings.TrimSpace(iccid)
	if !validICCID(iccid) {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_iccid",
			"ICCID must contain between 10 and 32 decimal digits",
		)
		return
	}
	switch r.Method {
	case http.MethodGet:
		policy, err := s.store.CardPolicy(r.Context(), iccid)
		if errors.Is(err, store.ErrNotFound) {
			policy = defaultCardPolicy(iccid)
		} else if err != nil {
			s.writeStoreError(w, err)
			return
		}
		// Reflect the SIM's live current state in the toggles (APN / IP version
		// remain stored preferences); fall back to the stored policy when the card
		// is not currently present in any device.
		if vowifi, airplane, ok := s.liveCardPolicyFlags(r.Context(), iccid); ok {
			policy.VoWiFiEnabled = vowifi
			policy.AirplaneEnabled = airplane
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": cardPolicyResponse(policy)})
	case http.MethodPut:
		var request struct {
			VoWiFiEnabled     *bool   `json:"vowifi_enabled"`
			AirplaneEnabled   *bool   `json:"airplane_enabled"`
			APN               *string `json:"apn"`
			IPVersion         *string `json:"ip_version"`
			CustomPhoneNumber *string `json:"custom_phone_number"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if request.VoWiFiEnabled == nil && request.AirplaneEnabled == nil &&
			request.APN == nil && request.IPVersion == nil && request.CustomPhoneNumber == nil {
			writeError(
				w,
				http.StatusBadRequest,
				"invalid_card_policy",
				"at least one card policy field is required",
			)
			return
		}
		policy, err := s.store.CardPolicy(r.Context(), iccid)
		if errors.Is(err, store.ErrNotFound) {
			policy = defaultCardPolicy(iccid)
		} else if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if request.APN != nil {
			apn := strings.TrimSpace(*request.APN)
			if !device.ValidAPN(apn) {
				writeError(w, http.StatusBadRequest, "invalid_card_policy", "APN must contain only letters, digits, dots, underscores, or hyphens")
				return
			}
			policy.APN = apn
		}
		if request.IPVersion != nil {
			ipVersion := strings.ToUpper(strings.TrimSpace(*request.IPVersion))
			if ipVersion == "" {
				ipVersion = "IPV4V6"
			}
			if ipVersion != "IP" && ipVersion != "IPV6" && ipVersion != "IPV4V6" {
				writeError(
					w,
					http.StatusBadRequest,
					"invalid_card_policy",
					"IP version must be IP, IPV6, or IPV4V6",
				)
				return
			}
			policy.IPVersion = ipVersion
		}
		if request.CustomPhoneNumber != nil {
			phoneNumber, phoneErr := normalizeCustomPhoneNumber(*request.CustomPhoneNumber)
			if phoneErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_card_policy", phoneErr.Error())
				return
			}
			policy.CustomPhoneNumber = phoneNumber
		}
		if request.VoWiFiEnabled != nil {
			policy.VoWiFiEnabled = *request.VoWiFiEnabled
		}
		if request.AirplaneEnabled != nil {
			policy.AirplaneEnabled = *request.AirplaneEnabled
		}
		// VoWiFi always owns an RF-off modem. Store airplane=true even when an
		// older client omits that implication, so disabling VoWiFi cannot expose a
		// brief cellular attach window.
		if policy.VoWiFiEnabled {
			policy.AirplaneEnabled = true
			policy.NetworkEnabled = false
		}
		if policy.IPVersion == "" {
			policy.IPVersion = "IPV4V6"
		}
		policy.Source = "manual"
		if err := s.store.UpsertCardPolicy(r.Context(), policy); err != nil {
			s.writeStoreError(w, err)
			return
		}
		policy, err = s.store.CardPolicy(r.Context(), iccid)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": cardPolicyResponse(policy)})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func defaultCardPolicy(iccid string) store.CardPolicy {
	return store.CardPolicy{
		ICCID:           strings.TrimSpace(iccid),
		VoWiFiEnabled:   true,
		AirplaneEnabled: true,
		IPVersion:       "IPV4V6",
		Source:          "default",
	}
}

type cardAPNProfilePayload struct {
	APN              string  `json:"apn"`
	Username         string  `json:"username"`
	Password         *string `json:"password"`
	ClearPassword    bool    `json:"clear_password"`
	Proxy            string  `json:"proxy"`
	MCC              string  `json:"mcc"`
	MNC              string  `json:"mnc"`
	IPVersion        string  `json:"ip_version"`
	RoamingIPVersion string  `json:"roaming_ip_version"`
	AuthType         string  `json:"auth_type"`
}

func (s *Server) decodeCardAPNProfilePayload(w http.ResponseWriter, r *http.Request) (cardAPNProfilePayload, bool) {
	var request cardAPNProfilePayload
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return request, false
	}
	request.APN = strings.TrimSpace(request.APN)
	if request.APN == "" || !device.ValidAPN(request.APN) {
		writeError(w, http.StatusBadRequest, "invalid_apn", "APN must contain only letters, digits, dots, underscores, or hyphens")
		return request, false
	}
	request.IPVersion = strings.ToUpper(strings.TrimSpace(request.IPVersion))
	if request.IPVersion == "" {
		request.IPVersion = "IPV4V6"
	}
	if request.IPVersion != "IP" && request.IPVersion != "IPV6" && request.IPVersion != "IPV4V6" {
		writeError(w, http.StatusBadRequest, "invalid_ip_version", "IP version must be IP, IPV6, or IPV4V6")
		return request, false
	}
	request.RoamingIPVersion = strings.ToUpper(strings.TrimSpace(request.RoamingIPVersion))
	if request.RoamingIPVersion == "" {
		request.RoamingIPVersion = "IP"
	}
	if request.RoamingIPVersion != "IP" && request.RoamingIPVersion != "IPV6" && request.RoamingIPVersion != "IPV4V6" {
		writeError(w, http.StatusBadRequest, "invalid_roaming_ip_version", "roaming IP version must be IP, IPV6, or IPV4V6")
		return request, false
	}
	request.AuthType = strings.ToUpper(strings.TrimSpace(request.AuthType))
	if request.AuthType == "" {
		request.AuthType = "NONE"
	}
	if request.AuthType != "NONE" && request.AuthType != "PAP" && request.AuthType != "CHAP" && request.AuthType != "PAP_OR_CHAP" {
		writeError(w, http.StatusBadRequest, "invalid_auth_type", "authentication type must be NONE, PAP, CHAP, or PAP_OR_CHAP")
		return request, false
	}
	request.Username = strings.TrimSpace(request.Username)
	request.Proxy = strings.TrimSpace(request.Proxy)
	request.MCC = strings.TrimSpace(request.MCC)
	request.MNC = strings.TrimSpace(request.MNC)
	password := ""
	if request.Password != nil {
		password = *request.Password
	}
	if !validAPNText(request.Username, 128) || !validAPNText(password, 128) || !validAPNText(request.Proxy, 255) {
		writeError(w, http.StatusBadRequest, "invalid_apn_credentials", "APN username, password, or proxy contains unsupported characters")
		return request, false
	}
	if request.MCC != "" && !decimalLength(request.MCC, 3, 3) {
		writeError(w, http.StatusBadRequest, "invalid_mcc", "MCC must contain exactly 3 digits")
		return request, false
	}
	if request.MNC != "" && !decimalLength(request.MNC, 2, 3) {
		writeError(w, http.StatusBadRequest, "invalid_mnc", "MNC must contain 2 or 3 digits")
		return request, false
	}
	return request, true
}

func (s *Server) handleCardAPNProfiles(w http.ResponseWriter, r *http.Request, iccid, profileID string) {
	iccid = strings.TrimSpace(iccid)
	if !validICCID(iccid) {
		writeError(w, http.StatusBadRequest, "invalid_iccid", "ICCID must contain between 10 and 32 decimal digits")
		return
	}
	if profileID != "" {
		id, err := strconv.ParseInt(profileID, 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "invalid_apn_profile", "APN profile ID is invalid")
			return
		}
		profiles, err := s.store.ListCardAPNProfiles(r.Context(), iccid)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		var existing store.CardAPNProfile
		for _, profile := range profiles {
			if profile.ID == id {
				existing = profile
				break
			}
		}
		if existing.ID == 0 {
			writeError(w, http.StatusNotFound, "apn_profile_not_found", "APN profile was not found")
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if err := s.store.DeleteCardAPNProfile(r.Context(), iccid, id); err != nil {
				s.writeStoreError(w, err)
				return
			}
			policy, err := s.store.CardPolicy(r.Context(), iccid)
			if err == nil && strings.EqualFold(policy.APN, existing.APN) && strings.EqualFold(policy.IPVersion, existing.IPVersion) {
				policy.APN = ""
				policy.IPVersion = "IPV4V6"
				policy.Source = "manual"
				if err := s.store.UpsertCardPolicy(r.Context(), policy); err != nil {
					s.writeStoreError(w, err)
					return
				}
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				s.writeStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": true, "id": id}})
		case http.MethodPatch, http.MethodPut:
			request, ok := s.decodeCardAPNProfilePayload(w, r)
			if !ok {
				return
			}
			password := existing.Password
			if request.ClearPassword {
				password = ""
			} else if request.Password != nil && *request.Password != "" {
				password = *request.Password
			}
			updated, err := s.store.UpdateCardAPNProfile(r.Context(), store.CardAPNProfile{
				ID: id, ICCID: iccid, APN: request.APN, Username: request.Username,
				Password: password, Proxy: request.Proxy, MCC: request.MCC, MNC: request.MNC,
				IPVersion: request.IPVersion, RoamingIPVersion: request.RoamingIPVersion,
				AuthType: request.AuthType,
			})
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			policy, policyErr := s.store.CardPolicy(r.Context(), iccid)
			if policyErr == nil && strings.EqualFold(policy.APN, existing.APN) && strings.EqualFold(policy.IPVersion, existing.IPVersion) {
				policy.APN = updated.APN
				policy.IPVersion = updated.IPVersion
				policy.Source = "manual"
				if err := s.store.UpsertCardPolicy(r.Context(), policy); err != nil {
					s.writeStoreError(w, err)
					return
				}
			} else if policyErr != nil && !errors.Is(policyErr, store.ErrNotFound) {
				s.writeStoreError(w, policyErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": cardAPNProfileResponse(updated)})
		default:
			w.Header().Set("Allow", "PATCH, PUT, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		profiles, err := s.store.ListCardAPNProfiles(r.Context(), iccid)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		items := make([]map[string]any, 0, len(profiles))
		for _, profile := range profiles {
			items = append(items, cardAPNProfileResponse(profile))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
	case http.MethodPost:
		request, ok := s.decodeCardAPNProfilePayload(w, r)
		if !ok {
			return
		}
		if _, err := s.store.CardPolicy(r.Context(), iccid); errors.Is(err, store.ErrNotFound) {
			if err := s.store.UpsertCardPolicy(r.Context(), defaultCardPolicy(iccid)); err != nil {
				s.writeStoreError(w, err)
				return
			}
		} else if err != nil {
			s.writeStoreError(w, err)
			return
		}
		password := ""
		if request.Password != nil {
			password = *request.Password
		}
		profile, err := s.store.UpsertCardAPNProfile(r.Context(), store.CardAPNProfile{
			ICCID: iccid, APN: request.APN, Username: request.Username,
			Password: password, Proxy: request.Proxy, MCC: request.MCC, MNC: request.MNC,
			IPVersion: request.IPVersion, RoamingIPVersion: request.RoamingIPVersion,
			AuthType: request.AuthType,
		})
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"data": cardAPNProfileResponse(profile)})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func cardAPNProfileResponse(profile store.CardAPNProfile) map[string]any {
	return map[string]any{
		"id": profile.ID, "iccid": profile.ICCID, "apn": profile.APN,
		"username": profile.Username, "has_password": profile.Password != "",
		"proxy": profile.Proxy, "mcc": profile.MCC, "mnc": profile.MNC,
		"ip_version": profile.IPVersion, "roaming_ip_version": profile.RoamingIPVersion,
		"auth_type": profile.AuthType, "created_at": profile.CreatedAt,
		"updated_at": profile.UpdatedAt,
	}
}

func validAPNText(value string, maxLength int) bool {
	if len(value) > maxLength || strings.ContainsAny(value, "\r\n\x00\"") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func decimalLength(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validICCID(value string) bool {
	if len(value) < 10 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func normalizeCustomPhoneNumber(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	var normalized strings.Builder
	digitCount := 0
	for index, character := range value {
		switch {
		case character >= '0' && character <= '9':
			normalized.WriteRune(character)
			digitCount++
		case character == '+' && index == 0:
			normalized.WriteRune(character)
		case character == ' ' || character == '-' || character == '(' || character == ')':
			// Common visual separators are accepted but not persisted.
		default:
			return "", errors.New("custom phone number may contain only digits, a leading plus sign, spaces, parentheses, or hyphens")
		}
	}
	if digitCount < 3 || digitCount > 20 {
		return "", errors.New("custom phone number must contain between 3 and 20 digits")
	}
	return normalized.String(), nil
}

func cardPolicyResponse(policy store.CardPolicy) map[string]any {
	response := map[string]any{
		"iccid":               policy.ICCID,
		"network_enabled":     false,
		"vowifi_enabled":      policy.VoWiFiEnabled,
		"airplane_enabled":    policy.AirplaneEnabled,
		"apn":                 policy.APN,
		"ip_version":          policy.IPVersion,
		"custom_phone_number": policy.CustomPhoneNumber,
		"source":              policy.Source,
	}
	if !policy.CreatedAt.IsZero() {
		response["created_at"] = policy.CreatedAt
	}
	if !policy.UpdatedAt.IsZero() {
		response["updated_at"] = policy.UpdatedAt
	}
	return response
}

func (s *Server) handleTrafficAnalysis(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if !s.developerActive(r.Context()) {
		writeError(w, http.StatusForbidden, "developer_mode_required", "traffic analysis is available only in developer mode")
		return
	}
	rangeName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range")))
	if rangeName == "" {
		rangeName = "day"
	}
	var window time.Duration
	switch rangeName {
	case "hour":
		window = time.Hour
	case "day":
		window = 24 * time.Hour
	case "week":
		window = 7 * 24 * time.Hour
	case "month":
		window = 30 * 24 * time.Hour
	default:
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_range",
			"traffic range must be hour, day, week, or month",
		)
		return
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device_id"))
	if len(deviceID) > 128 || strings.ContainsAny(deviceID, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "invalid_device", "device ID is invalid")
		return
	}
	now := time.Now().UTC()
	rows, err := s.store.ListTrafficBuckets(r.Context(), store.TrafficFilter{
		DeviceID: deviceID,
		Bucket:   rangeName,
		Since:    now.Add(-window),
		Until:    now.Add(time.Minute),
		Limit:    1000,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	type aggregate struct {
		period time.Time
		rx     int64
		tx     int64
	}
	byPeriod := make(map[int64]*aggregate)
	for _, row := range rows {
		key := row.PeriodStart.Unix()
		value := byPeriod[key]
		if value == nil {
			value = &aggregate{period: row.PeriodStart}
			byPeriod[key] = value
		}
		value.rx += row.RXBytes
		value.tx += row.TXBytes
	}
	values := make([]*aggregate, 0, len(byPeriod))
	for _, value := range byPeriod {
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left].period.Before(values[right].period)
	})
	buckets := make([]map[string]any, 0, len(values))
	for _, value := range values {
		buckets = append(buckets, map[string]any{
			"bucket":       rangeName,
			"period_start": value.period,
			"rx_bytes":     value.rx,
			"tx_bytes":     value.tx,
			"total_bytes":  value.rx + value.tx,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"status":  "ok",
			"range":   rangeName,
			"buckets": buckets,
		},
	})
}
