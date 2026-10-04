package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// 从三个真实调用入口发送到本地 HTTP 接收端，防止入口退回带标题的 Text。
func TestMeowCallersSendDetailText(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 30, 0, 0, time.Local)
	sms := smsNotification{
		DeviceLabel: "EC20", Number: "+447386", Time: now,
		Content: "收到新短信后请确认\n第二行",
	}
	call := IncomingCallNotification{
		DeviceLabel: "EC20", Caller: "+447386", Called: "+441234",
		Time: now, Environment: "vowifi",
	}
	task := automaticTaskNotification{
		Title: "自动任务执行成功",
		Text:  "自动任务执行成功\n任务  检查网络\n结果  自动任务执行成功后请确认",
	}

	for _, test := range []struct {
		name  string
		title string
		msg   string
		send  func(context.Context, map[string]any) error
	}{
		{
			name: "SMS", title: "收到新短信",
			msg: "设备  EC20\n号码  +447386\n时间  2026-08-20 10:30:00\n内容  收到新短信后请确认\n第二行",
			send: func(ctx context.Context, config map[string]any) error {
				return sendSMSNotification(ctx, "meow", config, sms)
			},
		},
		{
			name: "call", title: "收到来电",
			msg: "设备  EC20\n来电号码  +447386\n被呼号码  +441234\n时间  2026-08-20 10:30:00\n网络  VoWiFi",
			send: func(ctx context.Context, config map[string]any) error {
				return sendCallNotification(ctx, "meow", config, call)
			},
		},
		{
			name: "automatic task", title: "自动任务执行成功",
			msg: "任务  检查网络\n结果  自动任务执行成功后请确认",
			send: func(ctx context.Context, config map[string]any) error {
				return sendAutomaticTaskNotification(ctx, "meow", config, task)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payloads := make(chan map[string]string, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid request")
				}
				var payload map[string]string
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				payloads <- payload
				_, _ = io.WriteString(w, `{"status":200}`)
			}))
			t.Cleanup(provider.Close)

			// 测试串行执行，只替换 MeoW 收件端；请求组装仍使用原有 postMeowNotification。
			previous := meowNotificationSender
			meowNotificationSender = func(ctx context.Context, config map[string]any, title, text string) error {
				return postMeowNotification(ctx, provider.Client(), provider.URL, title, text, config)
			}
			t.Cleanup(func() { meowNotificationSender = previous })

			if err := test.send(context.Background(), map[string]any{"nickname": "测试昵称"}); err != nil {
				t.Fatal(err)
			}
			select {
			case payload := <-payloads:
				if payload["title"] != test.title || payload["msg"] != test.msg {
					t.Fatalf("payload = %#v, want title %q and msg %q", payload, test.title, test.msg)
				}
			default:
				t.Fatal("MeoW caller did not post a payload")
			}
		})
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
