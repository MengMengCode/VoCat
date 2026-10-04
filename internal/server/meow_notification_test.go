package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMeowMessageBodyDropsRepeatedTitle(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		title string
		text  string
		want  string
	}{
		{
			name:  "sms repeats the title verbatim",
			title: "收到新短信",
			text:  "收到新短信\n设备  A\n内容  hello",
			want:  "设备  A\n内容  hello",
		},
		{
			name:  "call prefixes the title with an emoji",
			title: "收到来电",
			text:  "📞 收到来电\n设备  A",
			want:  "设备  A",
		},
		{
			name:  "single line body is kept",
			title: "VoCat 测试通知",
			text:  "VoCat 消息推送测试",
			want:  "VoCat 消息推送测试",
		},
		{
			name:  "body that does not repeat the title is kept",
			title: "VoCat 测试通知",
			text:  "第一行\n第二行",
			want:  "第一行\n第二行",
		},
		{
			name:  "empty title keeps the body",
			title: "",
			text:  "第一行\n第二行",
			want:  "第一行\n第二行",
		},
		{
			// A first line that merely mentions the title inside other text is
			// real content, not a repeated title, so it must survive.
			name:  "first line mentioning the title inside other text is kept",
			title: "收到新短信",
			text:  "提醒：收到新短信后请确认\n设备  A",
			want:  "提醒：收到新短信后请确认\n设备  A",
		},
		{
			name:  "title only in a later line is not touched",
			title: "收到新短信",
			text:  "第一行\n备注  收到新短信",
			want:  "第一行\n备注  收到新短信",
		},
		{
			name:  "emoji prefix and trailing space are trimmed",
			title: "收到来电",
			text:  "📞  收到来电  \n设备  A",
			want:  "设备  A",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := meowMessageBody(testCase.title, testCase.text); got != testCase.want {
				t.Fatalf("got %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestMeowBodiesDoNotRepeatTitle walks the real body of every notification kind
// and asserts the MeoW payload never repeats the title, which MeoW already
// renders inside the notification content.
func TestMeowBodiesDoNotRepeatTitle(t *testing.T) {
	now := time.Now()
	message := smsNotification{DeviceLabel: "A", Number: "10086", Time: now, Content: "hi"}
	call := IncomingCallNotification{DeviceLabel: "A", Caller: "10086", Called: "10010", Time: now}
	task := automaticTaskNotification{
		Title: "自动任务执行成功",
		Text: strings.Join([]string{
			"自动任务执行成功",
			"任务  定时上报",
			"设备  A",
			"类型  发送短信",
			"环境  VoWiFi",
			"时间  " + now.Local().Format("2006-01-02 15:04:05"),
			"结果  任务已完成",
		}, "\n"),
	}
	for _, testCase := range []struct {
		name  string
		title string
		text  string
	}{
		{name: "sms", title: "收到新短信", text: message.Text()},
		{name: "call", title: call.Title(), text: call.Text()},
		{name: "automatic task", title: task.Title, text: task.Text},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := meowMessageBody(testCase.title, testCase.text)
			if strings.Contains(body, testCase.title) {
				t.Fatalf("body %q repeats title %q", body, testCase.title)
			}
			if !strings.Contains(body, "\n") {
				t.Fatalf("body %q lost its detail lines", body)
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
