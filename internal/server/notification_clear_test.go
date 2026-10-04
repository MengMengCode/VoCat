package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"vocat/internal/store"
)

func TestClearNotificationCredentials(t *testing.T) {
	for _, channel := range []string{"telegram", "email"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enabled=%v", channel, enabled), func(t *testing.T) {
				api := newSettingsAPITest(t)
				field := "bot_token"
				original := map[string]any{"bot_token": "old-secret", "chat_id": "123", "admin_id": "123"}
				empty := map[string]any{"bot_token": "", "chat_id": "", "admin_id": "", "base_url": "", "proxy": ""}
				if channel == "email" {
					field = "password"
					original = map[string]any{"password": "old-secret", "smtp_host": "smtp.example.com", "smtp_port": 465, "use_ssl": true}
					empty = map[string]any{"password": "", "username": "", "smtp_host": "", "smtp_port": 0, "use_ssl": false, "from_address": "", "to_addresses": []string{}}
				}
				raw, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				if err := api.database.UpsertNotificationSetting(context.Background(), store.NotificationSetting{Channel: channel, Enabled: enabled, Config: raw}); err != nil {
					t.Fatal(err)
				}
				save := func(document map[string]any) {
					t.Helper()
					document["enabled"] = enabled
					payload, err := json.Marshal(map[string]any{channel: document})
					if err != nil {
						t.Fatal(err)
					}
					response := api.request(t, http.MethodPut, "/api/settings/notifications", string(payload))
					if response.Code != http.StatusOK {
						t.Fatalf("save: %d %s", response.Code, response.Body)
					}
				}
				check := func(want string) {
					t.Helper()
					stored, err := api.database.NotificationSetting(context.Background(), channel)
					if err != nil {
						t.Fatal(err)
					}
					var config map[string]any
					if err := json.Unmarshal(stored.Config, &config); err != nil {
						t.Fatal(err)
					}
					if stored.Enabled != enabled || configString(config, field) != want {
						t.Fatal("credential or enabled state mismatch")
					}
					if _, present := config["clear_secrets"]; present {
						t.Fatal("command flag persisted")
					}
				}
				// 普通空值保存仍保留凭据；只有用户确认清空才删除。
				save(empty)
				check("old-secret")
				empty["clear_secrets"] = true
				save(empty)
				check("")
				response := api.request(t, http.MethodGet, "/api/settings/notifications", "")
				if response.Code != http.StatusOK {
					t.Fatal("readback failed")
				}
				document := decodeSettingsResponse(t, response)["data"].(map[string]any)[channel].(map[string]any)
				save(document)
				check("")
				// 清空后重填新凭据可以直接保存，无需先提交一次空配置。
				empty[field] = "replacement-secret"
				save(empty)
				check("replacement-secret")
			})
		}
	}
}

func TestNotificationClearRejectsUnsupportedRequests(t *testing.T) {
	api := newSettingsAPITest(t)
	for _, body := range []string{
		`{"email":{"enabled":false,"clear_secrets":null}}`,
		`{"telegram":{"enabled":false,"clear_secrets":"true"}}`,
		`{"bark":{"enabled":false,"clear_secrets":true}}`,
		`{"webhook":{"enabled":false,"clear_secrets":true}}`,
		`{"pushplus":{"enabled":false,"clear_secrets":true}}`,
		`{"wecom":{"enabled":false,"clear_secrets":true}}`,
		`{"lark":{"enabled":false,"clear_secrets":true}}`,
		`{"meow":{"enabled":false,"clear_secrets":true}}`,
	} {
		response := api.request(t, http.MethodPut, "/api/settings/notifications", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid clear request: %s", body)
		}
	}
}
