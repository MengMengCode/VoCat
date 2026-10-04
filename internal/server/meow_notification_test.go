package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMeowSettingsRoundTrip(t *testing.T) {
	api := newSettingsAPITest(t)
	response := api.request(t, http.MethodPut, "/api/settings/notifications", `{"meow":{"enabled":true,"nickname":"测试昵称","url":"https://example.com","img_url":"https://example.com/icon.png"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body)
	}
	stored, err := api.database.NotificationSetting(context.Background(), "meow")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(stored.Config, &config); err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled || config["nickname"] != "测试昵称" || config["url"] != "https://example.com" || config["img_url"] != "https://example.com/icon.png" {
		t.Fatalf("unexpected config: %#v", config)
	}
	response = api.request(t, http.MethodGet, "/api/settings/notifications", "")
	data := decodeSettingsResponse(t, response)["data"].(map[string]any)
	if len(data) != 8 || data["meow"].(map[string]any)["nickname"] != "测试昵称" {
		t.Fatalf("unexpected channels: %#v", data)
	}
}

func TestMeowRejectsInvalidNickname(t *testing.T) {
	for _, nickname := range []string{"", " ", ".", "..", "a/b", `a\b`} {
		if err := validateMeowNotificationConfig(map[string]any{"nickname": nickname}); err == nil {
			t.Errorf("accepted %q", nickname)
		}
	}
	if err := validateMeowNotificationConfig(map[string]any{"nickname": "昵称"}); err != nil {
		t.Fatal(err)
	}
}

func TestMeowProviderResponse(t *testing.T) {
	for _, reply := range []struct {
		code int
		body string
		ok   bool
	}{
		{http.StatusOK, `{"status":200}`, true},
		{http.StatusOK, `{"status":500}`, false},
		{http.StatusOK, `{}`, false},
		{http.StatusOK, `not-json`, false},
		{http.StatusBadGateway, `{"status":200}`, false},
	} {
		t.Run(fmt.Sprintf("%d/%s", reply.code, reply.body), func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid request")
				}
				var payload map[string]string
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["title"] != "标题" || payload["msg"] != "内容" {
					t.Errorf("unexpected payload: %#v", payload)
				}
				w.WriteHeader(reply.code)
				_, _ = io.WriteString(w, reply.body)
			}))
			defer provider.Close()
			err := postMeowNotification(context.Background(), provider.Client(), provider.URL, "标题", "内容", nil)
			if (err == nil) != reply.ok {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestMeowOptionalLinks(t *testing.T) {
	for _, configured := range []bool{false, true} {
		config := map[string]any{}
		if configured {
			config["url"] = "https://example.com"
			config["img_url"] = "https://example.com/icon.png"
		}
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if configured {
				if payload["url"] != config["url"] || payload["imgUrl"] != config["img_url"] {
					t.Errorf("missing optional links: %#v", payload)
				}
			} else if _, present := payload["url"]; present {
				t.Error("unexpected url")
			}
			if !configured {
				if _, present := payload["imgUrl"]; present {
					t.Error("unexpected icon")
				}
			}
			if _, present := payload["img_url"]; present {
				t.Error("wrong provider field casing")
			}
			_, _ = io.WriteString(w, `{"status":200}`)
		}))
		err := postMeowNotification(context.Background(), provider.Client(), provider.URL, "标题", "内容", config)
		provider.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
