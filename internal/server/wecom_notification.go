package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var wecomTemplateVariableNames = []string{
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

type wecomTemplateValues map[string]string

func renderWecomPayload(template string, values wecomTemplateValues) ([]byte, error) {
	for _, name := range wecomTemplateVariableNames {
		encoded, err := json.Marshal(values[name])
		if err != nil {
			return nil, fmt.Errorf("encode WeCom template value %q: %w", name, err)
		}
		template = strings.ReplaceAll(template, "{{"+name+"}}", string(encoded))
	}
	if strings.Contains(template, "{{") {
		return nil, errors.New("wecom.payload_template contains an unsupported variable")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(template), &payload); err != nil || len(payload) == 0 {
		return nil, errors.New("wecom.payload_template must render to a non-empty JSON object")
	}
	return []byte(template), nil
}

func validateWecomResponse(status int, body []byte) error {
	var result struct {
		ErrCode *int `json:"errcode"`
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices ||
		json.Unmarshal(body, &result) != nil || result.ErrCode == nil || *result.ErrCode != 0 {
		return fmt.Errorf("%w: WeCom response was not successful", errProviderRejected)
	}
	return nil
}

func wecomTestValues(now time.Time) wecomTemplateValues {
	return wecomTemplateValues{
		"event": "test", "title": "vocat", "message": "vocat notification test",
		"timestamp": now.UTC().Format(time.RFC3339),
	}
}

func wecomSMSValues(message smsNotification) wecomTemplateValues {
	return wecomTemplateValues{
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

func wecomAutomaticTaskValues(message automaticTaskNotification) wecomTemplateValues {
	return wecomTemplateValues{
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

func validateWecomNotificationConfig(config map[string]any) error {
	urls := configStrings(config, "urls")
	if len(urls) == 0 {
		return errors.New("wecom.urls must contain at least one URL")
	}
	if len(urls) > 8 {
		return errors.New("wecom.urls cannot contain more than 8 URLs")
	}
	template := configString(config, "payload_template")
	if template == "" {
		return errors.New("wecom.payload_template is required")
	}
	_, err := renderWecomPayload(template, wecomTestValues(time.Unix(0, 0)))
	return err
}

func sendWecomNotification(ctx context.Context, config map[string]any, values wecomTemplateValues) error {
	payload, err := renderWecomPayload(configString(config, "payload_template"), values)
	if err != nil {
		return err
	}
	client, err := restrictedHTTPClient(ctx, 8*time.Second, "")
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
			return fmt.Errorf("create WeCom notification request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		request.Header.Set("User-Agent", "vocat-wecom-notification/1")
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("send WeCom notification: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		closeErr := response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read WeCom response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close WeCom response: %w", closeErr)
		}
		if err := validateWecomResponse(response.StatusCode, body); err != nil {
			return err
		}
	}
	return nil
}

func sendWecomNotificationTest(ctx context.Context, config map[string]any) error {
	return sendWecomNotification(ctx, config, wecomTestValues(time.Now()))
}
