package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRenderLarkPayloadEscapesTemplateValues(t *testing.T) {
	payload, err := renderLarkPayload(
		`{"msg_type":"text","content":{"text":{{message}},"number":{{number}}}}`,
		larkTemplateValues{
			"message": "quote: \"\nline",
			"number":  "+447386",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(payload), `{"msg_type":"text","content":{"text":"quote: \"\nline","number":"+447386"}}`; got != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
}

func TestRenderLarkPayloadDoesNotInterpretPlaceholdersInsideValues(t *testing.T) {
	payload, err := renderLarkPayload(
		`{"msg_type":"text","content":{"text":{{message}}}}`,
		larkTemplateValues{"message": "keep {{timestamp}} literally", "timestamp": "changed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(payload), `{"msg_type":"text","content":{"text":"keep {{timestamp}} literally"}}`; got != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
}

func TestRenderLarkPayloadRejectsInvalidTemplate(t *testing.T) {
	for _, template := range []string{
		`{"text":{{unknown}}}`,
		`[]`,
		`{"msg_type":"text"`,
		`{"text":"` + strings.Repeat("x", maxLarkPayloadBytes) + `"}`,
	} {
		t.Run(template[:min(len(template), 40)], func(t *testing.T) {
			if _, err := renderLarkPayload(template, larkTemplateValues{}); err == nil {
				t.Fatalf("template was accepted")
			}
		})
	}
}

func TestSignLarkPayload(t *testing.T) {
	const timestamp = int64(1_599_360_473)
	if got, want := larkSignature(timestamp, "demo"), "l1N0gAcBjdwBvGm1xMjOF0XSyaLRpR7tuO5dHfhAYc8="; got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}

	unsigned := []byte(`{"msg_type":"text","content":{"text":"hello"}}`)
	signed, err := signLarkPayload(unsigned, "demo", time.Unix(timestamp, 0))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(signed, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["timestamp"] != "1599360473" || payload["sign"] != "l1N0gAcBjdwBvGm1xMjOF0XSyaLRpR7tuO5dHfhAYc8=" {
		t.Fatalf("signed payload = %#v", payload)
	}

	untouched, err := signLarkPayload(unsigned, "", time.Unix(timestamp, 0))
	if err != nil || string(untouched) != string(unsigned) {
		t.Fatalf("unsigned payload = %s, err = %v", untouched, err)
	}
}

func TestValidateLarkResponse(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"msg":"success"}`,
		`{"StatusCode":0,"StatusMessage":"success"}`,
	} {
		if err := validateLarkResponse(http.StatusOK, []byte(body)); err != nil {
			t.Fatalf("successful response %s = %v", body, err)
		}
	}
	for _, response := range []struct {
		status int
		body   string
	}{
		{http.StatusBadGateway, `{"code":0}`},
		{http.StatusOK, `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time","StatusCode":0}`},
		{http.StatusOK, `{"StatusCode":19021,"StatusMessage":"sign error"}`},
		{http.StatusOK, `{}`},
		{http.StatusOK, `not-json`},
	} {
		if err := validateLarkResponse(response.status, []byte(response.body)); !errors.Is(err, errProviderRejected) {
			t.Fatalf("validateLarkResponse(%d, %s) = %v", response.status, response.body, err)
		}
	}
}

func TestPostLarkNotificationSendsJSONPayload(t *testing.T) {
	payload := []byte(`{"msg_type":"text","content":{"text":"hello"}}`)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "vocat-lark-notification/1" {
			t.Errorf("User-Agent = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) != string(payload) {
			t.Errorf("body = %s, want %s", body, payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	t.Cleanup(provider.Close)

	if err := postLarkNotification(context.Background(), provider.Client(), provider.URL, payload); err != nil {
		t.Fatalf("postLarkNotification() = %v", err)
	}
}

func TestParseLarkWebhookURL(t *testing.T) {
	for _, raw := range []string{
		"https://open.feishu.cn/open-apis/bot/v2/hook/feishu-token",
		"https://open.larksuite.com/open-apis/bot/v2/hook/lark-token",
		"https://open.larksuite.com:443/open-apis/bot/v2/hook/lark-token",
	} {
		if _, err := parseLarkWebhookURL(raw); err != nil {
			t.Errorf("parseLarkWebhookURL(%q) = %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://open.larksuite.com/open-apis/bot/v2/hook/token",
		"https://example.com/open-apis/bot/v2/hook/token",
		"https://open.larksuite.com/open-apis/bot/hook/token",
		"https://open.larksuite.com/open-apis/bot/v2/hook/",
		"https://open.larksuite.com/open-apis/bot/v2/hook/token/extra",
		"https://open.larksuite.com/open-apis/bot/v2/hook/token?query=1",
	} {
		if _, err := parseLarkWebhookURL(raw); err == nil {
			t.Errorf("parseLarkWebhookURL(%q) accepted an invalid group bot webhook", raw)
		}
	}
}

func TestValidateLarkNotificationConfig(t *testing.T) {
	valid := map[string]any{
		"url":              "https://open.larksuite.com/open-apis/bot/v2/hook/token",
		"signing_enabled":  true,
		"secret":           "demo",
		"payload_template": `{"msg_type":"text","content":{"text":{{message}}}}`,
	}
	if err := validateLarkNotificationConfig(valid); err != nil {
		t.Fatalf("valid config = %v", err)
	}
	unsigned := map[string]any{
		"url":              valid["url"],
		"signing_enabled":  false,
		"payload_template": valid["payload_template"],
	}
	if err := validateLarkNotificationConfig(unsigned); err != nil {
		t.Fatalf("unsigned config = %v", err)
	}
	if secret := larkSigningSecret(map[string]any{"signing_enabled": false, "secret": "demo"}); secret != "" {
		t.Fatalf("disabled signing secret = %q", secret)
	}
	if secret := larkSigningSecret(valid); secret != "demo" {
		t.Fatalf("enabled signing secret = %q", secret)
	}
	for _, config := range []map[string]any{
		{"payload_template": valid["payload_template"]},
		{"url": valid["url"]},
		{"url": valid["url"], "signing_enabled": true, "payload_template": valid["payload_template"]},
	} {
		if err := validateLarkNotificationConfig(config); err == nil {
			t.Fatalf("invalid config was accepted: %#v", config)
		}
	}
}

func TestSanitizeLarkRequestErrorRemovesWebhookURL(t *testing.T) {
	const webhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/sensitive-token"
	err := sanitizeLarkRequestError(&url.Error{Op: "Post", URL: webhookURL, Err: errors.New("dial failed")})
	if strings.Contains(err.Error(), "sensitive-token") || err.Error() != "dial failed" {
		t.Fatalf("sanitized error = %q", err)
	}
}
