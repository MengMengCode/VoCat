package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxLarkPayloadBytes = 20 << 10

var larkTemplateVariableNames = []string{
	"event",
	"title",
	"message",
	"timestamp",
	"content",
	"number",
	"device_id",
	"device_name",
	"device_label",
	"time",
}

var larkTemplatePlaceholderPattern = regexp.MustCompile(`\{\{[^{}]*\}\}`)

var larkWebhookHosts = map[string]struct{}{
	"open.feishu.cn":     {},
	"open.larksuite.com": {},
}

type larkTemplateValues map[string]string

func renderLarkPayload(template string, values larkTemplateValues) ([]byte, error) {
	encodedValues := make(map[string]string, len(larkTemplateVariableNames))
	for _, name := range larkTemplateVariableNames {
		encoded, err := json.Marshal(values[name])
		if err != nil {
			return nil, fmt.Errorf("encode Lark template value %q: %w", name, err)
		}
		encodedValues[name] = string(encoded)
	}
	unsupported := false
	rendered := larkTemplatePlaceholderPattern.ReplaceAllStringFunc(template, func(placeholder string) string {
		name := placeholder[2 : len(placeholder)-2]
		encoded, ok := encodedValues[name]
		if !ok {
			unsupported = true
			return placeholder
		}
		return encoded
	})
	remainder := larkTemplatePlaceholderPattern.ReplaceAllString(template, "")
	if unsupported || strings.Contains(remainder, "{{") {
		return nil, errors.New("lark.payload_template contains an unsupported variable")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil || len(payload) == 0 {
		return nil, errors.New("lark.payload_template must render to a non-empty JSON object")
	}
	if len(rendered) > maxLarkPayloadBytes {
		return nil, errors.New("lark.payload_template renders beyond the 20 KB Lark limit")
	}
	return []byte(rendered), nil
}

func larkSignature(timestamp int64, secret string) string {
	key := strconv.FormatInt(timestamp, 10) + "\n" + secret
	signature := hmac.New(sha256.New, []byte(key))
	return base64.StdEncoding.EncodeToString(signature.Sum(nil))
}

func signLarkPayload(payload []byte, secret string, now time.Time) ([]byte, error) {
	if secret == "" {
		return payload, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil || len(document) == 0 {
		return nil, errors.New("lark payload must be a non-empty JSON object")
	}
	timestamp := now.Unix()
	document["timestamp"], _ = json.Marshal(strconv.FormatInt(timestamp, 10))
	document["sign"], _ = json.Marshal(larkSignature(timestamp, secret))
	signed, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode signed Lark payload: %w", err)
	}
	if len(signed) > maxLarkPayloadBytes {
		return nil, errors.New("lark payload exceeds the 20 KB Lark limit after signing")
	}
	return signed, nil
}

func validateLarkResponse(status int, body []byte) error {
	var result struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices || json.Unmarshal(body, &result) != nil {
		return fmt.Errorf("%w: Lark response was not successful", errProviderRejected)
	}
	if result.Code != nil {
		if *result.Code == 0 {
			return nil
		}
		return fmt.Errorf("%w: Lark response was not successful", errProviderRejected)
	}
	if result.StatusCode == nil || *result.StatusCode != 0 {
		return fmt.Errorf("%w: Lark response was not successful", errProviderRejected)
	}
	return nil
}

func parseLarkWebhookURL(raw string) (*url.URL, error) {
	parsed, err := parseOutboundURL(raw, true)
	if err != nil {
		return nil, err
	}
	if _, ok := larkWebhookHosts[strings.ToLower(parsed.Hostname())]; !ok {
		return nil, errors.New("Lark group bot webhook must use open.feishu.cn or open.larksuite.com")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return nil, errors.New("Lark group bot webhook must use the default HTTPS port")
	}
	const prefix = "/open-apis/bot/v2/hook/"
	token := strings.TrimPrefix(parsed.Path, prefix)
	if token == parsed.Path || token == "" || strings.Contains(token, "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("Lark group bot webhook path is invalid")
	}
	return parsed, nil
}

func validateLarkWebhookURL(ctx context.Context, raw string) (*url.URL, error) {
	parsed, err := parseLarkWebhookURL(raw)
	if err != nil {
		return nil, err
	}
	if _, err := resolvePublicAddresses(ctx, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func larkTestValues(now time.Time) larkTemplateValues {
	return larkTemplateValues{
		"event": "test", "title": "vocat", "message": "vocat notification test",
		"timestamp": now.UTC().Format(time.RFC3339),
	}
}

func larkSMSValues(message smsNotification) larkTemplateValues {
	return larkTemplateValues{
		"event":        "sms.received",
		"title":        "收到新短信",
		"message":      message.Text(),
		"timestamp":    message.Time.UTC().Format(time.RFC3339),
		"content":      message.Content,
		"number":       message.Number,
		"device_id":    message.DeviceID,
		"device_name":  message.DeviceName,
		"device_label": message.DeviceLabel,
		"time":         message.Time.Local().Format("2006-01-02 15:04:05"),
	}
}

func larkAutomaticTaskValues(message automaticTaskNotification) larkTemplateValues {
	return larkTemplateValues{
		"event":        "automatic_task.completed",
		"title":        message.Title,
		"message":      message.Text,
		"timestamp":    message.Time.UTC().Format(time.RFC3339),
		"content":      "",
		"number":       "",
		"device_id":    "",
		"device_name":  "",
		"device_label": "",
		"time":         "",
	}
}

func validateLarkNotificationConfig(config map[string]any) error {
	if configString(config, "url") == "" {
		return errors.New("lark.url is required")
	}
	template := configString(config, "payload_template")
	if template == "" {
		return errors.New("lark.payload_template is required")
	}
	if signingEnabled, _ := config["signing_enabled"].(bool); signingEnabled {
		secret := configString(config, "secret")
		if secret == "" {
			return errors.New("lark.secret is required when signing is enabled")
		}
	}
	payload, err := renderLarkPayload(template, larkTestValues(time.Unix(0, 0)))
	if err != nil {
		return err
	}
	_, err = signLarkPayload(payload, larkSigningSecret(config), time.Unix(0, 0))
	return err
}

func larkSigningSecret(config map[string]any) string {
	enabled, _ := config["signing_enabled"].(bool)
	if !enabled {
		return ""
	}
	return configString(config, "secret")
}

func sendLarkNotification(ctx context.Context, config map[string]any, values larkTemplateValues) error {
	if err := validateLarkNotificationConfig(config); err != nil {
		return err
	}
	payload, err := renderLarkPayload(configString(config, "payload_template"), values)
	if err != nil {
		return err
	}
	payload, err = signLarkPayload(payload, larkSigningSecret(config), time.Now())
	if err != nil {
		return err
	}
	parsed, err := validateLarkWebhookURL(ctx, configString(config, "url"))
	if err != nil {
		return err
	}
	client, err := restrictedHTTPClient(ctx, 8*time.Second, "")
	if err != nil {
		return err
	}
	return postLarkNotification(ctx, client, parsed.String(), payload)
}

func postLarkNotification(ctx context.Context, client *http.Client, endpoint string, payload []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Lark notification request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", "vocat-lark-notification/1")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send Lark notification: %w", sanitizeLarkRequestError(err))
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read Lark response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Lark response: %w", closeErr)
	}
	if err := validateLarkResponse(response.StatusCode, body); err != nil {
		return err
	}
	return nil
}

func sanitizeLarkRequestError(err error) error {
	var requestErr *url.Error
	if errors.As(err, &requestErr) && requestErr.Err != nil {
		return requestErr.Err
	}
	return err
}

func sendLarkNotificationTest(ctx context.Context, config map[string]any) error {
	return sendLarkNotification(ctx, config, larkTestValues(time.Now()))
}
