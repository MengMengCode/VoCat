package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/store"
)

const (
	qqBotTestAppID  = "102000001"
	qqBotTestSecret = "test-secret-value"
	qqBotTestOpenID = "OPENID_test-0123"
)

func qqBotTestConfig(extra map[string]any) map[string]any {
	config := map[string]any{
		"app_id": qqBotTestAppID, "app_secret": qqBotTestSecret,
		"target_openid": qqBotTestOpenID,
	}
	for key, value := range extra {
		config[key] = value
	}
	return config
}

// qqBotFakePlatform 模拟开放平台的令牌接口和消息接口。
type qqBotFakePlatform struct {
	t           *testing.T
	server      *httptest.Server
	mu          sync.Mutex
	tokens      int
	messages    []qqBotFakeMessage
	rejectToken string // 消息接口对该令牌返回 401
	rejectAll   bool   // 消息接口对所有令牌返回 401
	messageCode int    // 非零时消息接口返回业务错误
	tokenStatus int    // 非零时令牌接口返回该状态
}

type qqBotFakeMessage struct {
	Path          string
	Authorization string
	Body          map[string]any
}

func newQQBotFakePlatform(t *testing.T) *qqBotFakePlatform {
	t.Helper()
	platform := &qqBotFakePlatform{t: t}
	platform.server = httptest.NewServer(http.HandlerFunc(platform.serve))
	t.Cleanup(platform.server.Close)
	// 各测试使用独立的令牌缓存，避免互相影响。
	previous := qqBotTokens
	qqBotTokens = &qqBotTokenCache{entries: map[string]qqBotToken{}, now: time.Now}
	t.Cleanup(func() { qqBotTokens = previous })
	return platform
}

func (platform *qqBotFakePlatform) endpoints() qqBotEndpoints {
	return qqBotEndpoints{TokenURL: platform.server.URL + "/app/getAppAccessToken", APIBase: platform.server.URL}
}

func (platform *qqBotFakePlatform) serve(w http.ResponseWriter, r *http.Request) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		platform.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		platform.t.Errorf("decode body: %v", err)
	}
	if r.URL.Path == "/app/getAppAccessToken" {
		if body["appId"] != qqBotTestAppID || body["clientSecret"] != qqBotTestSecret {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":100016,"message":"invalid appid or secret"}`))
			return
		}
		if platform.tokenStatus != 0 {
			w.WriteHeader(platform.tokenStatus)
			return
		}
		platform.tokens++
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "token-" + string(rune('0'+platform.tokens)),
			"expires_in":   "7200",
		})
		return
	}
	platform.messages = append(platform.messages, qqBotFakeMessage{
		Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: body,
	})
	if platform.rejectAll || (platform.rejectToken != "" && r.Header.Get("Authorization") == "QQBot "+platform.rejectToken) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":11244,"message":"token not exist or expire"}`))
		return
	}
	if platform.messageCode != 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":22009,"message":"msg limit exceed"}`))
		return
	}
	_, _ = w.Write([]byte(`{"id":"msg-1","timestamp":"2026-11-09T12:00:00+08:00"}`))
}

func (platform *qqBotFakePlatform) post(t *testing.T, config map[string]any, title, text string) error {
	t.Helper()
	return postQQBotNotification(context.Background(), platform.server.Client(), platform.endpoints(), config, title, text)
}

func TestQQBotValidatesConfig(t *testing.T) {
	if err := validateQQBotNotificationConfig(qqBotTestConfig(nil)); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string]map[string]any{
		"missing app id":   {"app_id": ""},
		"text app id":      {"app_id": "abc"},
		"missing secret":   {"app_secret": ""},
		"masked secret":    {"app_secret": store.SecretMask},
		"missing openid":   {"target_openid": ""},
		"openid with path": {"target_openid": "a/b"},
	} {
		if err := validateQQBotNotificationConfig(qqBotTestConfig(extra)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestQQBotSendsDirectMessage(t *testing.T) {
	platform := newQQBotFakePlatform(t)
	if err := platform.post(t, qqBotTestConfig(nil), "收到新短信", "内容  你好"); err != nil {
		t.Fatal(err)
	}
	if err := platform.post(t, qqBotTestConfig(nil), "", "第二条"); err != nil {
		t.Fatal(err)
	}
	if platform.tokens != 1 {
		t.Fatalf("token fetched %d times, want 1 (cached)", platform.tokens)
	}
	if len(platform.messages) != 2 {
		t.Fatalf("messages = %d", len(platform.messages))
	}
	first, second := platform.messages[0], platform.messages[1]
	for _, message := range platform.messages {
		if message.Path != "/v2/users/"+qqBotTestOpenID+"/messages" {
			t.Fatalf("path = %q", message.Path)
		}
	}
	if first.Authorization != "QQBot token-1" {
		t.Fatalf("authorization = %q", first.Authorization)
	}
	if first.Body["content"] != "收到新短信\n内容  你好" || first.Body["msg_type"] != float64(0) {
		t.Fatalf("first body = %#v", first.Body)
	}
	if second.Body["content"] != "第二条" {
		t.Fatalf("second body = %#v", second.Body)
	}
}

func TestQQBotRefreshesExpiredToken(t *testing.T) {
	platform := newQQBotFakePlatform(t)
	now := time.Now()
	qqBotTokens.now = func() time.Time { return now }
	if err := platform.post(t, qqBotTestConfig(nil), "a", "b"); err != nil {
		t.Fatal(err)
	}
	// 进入提前刷新窗口后重新获取令牌。
	now = now.Add(7200*time.Second - qqBotTokenRefreshMargin)
	if err := platform.post(t, qqBotTestConfig(nil), "a", "b"); err != nil {
		t.Fatal(err)
	}
	if platform.tokens != 2 || platform.messages[1].Authorization != "QQBot token-2" {
		t.Fatalf("tokens = %d, last auth = %q", platform.tokens, platform.messages[1].Authorization)
	}
}

func TestQQBotRetriesOnceAfterUnauthorized(t *testing.T) {
	platform := newQQBotFakePlatform(t)
	platform.rejectToken = "token-1"
	if err := platform.post(t, qqBotTestConfig(nil), "a", "b"); err != nil {
		t.Fatal(err)
	}
	if platform.tokens != 2 || len(platform.messages) != 2 || platform.messages[1].Authorization != "QQBot token-2" {
		t.Fatalf("tokens = %d, messages = %#v", platform.tokens, platform.messages)
	}

	// 新令牌仍被拒绝时只重试一次，并按发送失败返回。
	platform = newQQBotFakePlatform(t)
	platform.rejectAll = true
	err := platform.post(t, qqBotTestConfig(nil), "a", "b")
	if !errors.Is(err, errProviderRejected) || platform.tokens != 2 || len(platform.messages) != 2 {
		t.Fatalf("err = %v, tokens = %d, messages = %d", err, platform.tokens, len(platform.messages))
	}
}

func TestQQBotRejectionsAreProviderErrorsWithoutSecrets(t *testing.T) {
	platform := newQQBotFakePlatform(t)
	platform.messageCode = 22009
	err := platform.post(t, qqBotTestConfig(nil), "a", "b")
	if !errors.Is(err, errProviderRejected) || !strings.Contains(err.Error(), "22009") {
		t.Fatalf("message rejection: %v", err)
	}
	platform.messageCode = 0

	err = platform.post(t, qqBotTestConfig(map[string]any{"app_secret": "wrong-secret"}), "a", "b")
	if !errors.Is(err, errProviderRejected) || !strings.Contains(err.Error(), "100016") {
		t.Fatalf("credential rejection: %v", err)
	}
	for _, secret := range []string{"wrong-secret", qqBotTestSecret, qqBotTestOpenID, "token-"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}

	platform.tokenStatus = http.StatusInternalServerError
	qqBotTokens.entries = map[string]qqBotToken{}
	if err := platform.post(t, qqBotTestConfig(nil), "a", "b"); !errors.Is(err, errProviderRejected) {
		t.Fatalf("token outage: %v", err)
	}
}

func TestQQBotTransportErrorsOmitOpenID(t *testing.T) {
	platform := newQQBotFakePlatform(t)
	if err := platform.post(t, qqBotTestConfig(nil), "a", "b"); err != nil {
		t.Fatal(err)
	}
	endpoints := platform.endpoints()
	platform.server.Close()
	err := postQQBotNotification(context.Background(), platform.server.Client(), endpoints, qqBotTestConfig(nil), "a", "b")
	if err == nil || strings.Contains(err.Error(), qqBotTestOpenID) {
		t.Fatalf("transport error = %v", err)
	}
}

func TestQQBotContentIsBounded(t *testing.T) {
	content := qqBotContent("标题", strings.Repeat("长", 3000))
	if runes := []rune(content); len(runes) != maxQQBotContentRunes || !strings.HasPrefix(content, "标题\n") {
		t.Fatalf("content length = %d", len(runes))
	}
}

// 从三个真实调用入口发送到本地接收端，确认使用 DetailText 和对应标题。
func TestQQBotCallersSendDetailText(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 30, 0, 0, time.Local)
	sms := smsNotification{DeviceID: "a", DeviceLabel: "EC20", Number: "+447386", Time: now, Content: "验证码 1234"}
	call := IncomingCallNotification{DeviceID: "a", DeviceLabel: "EC20", Caller: "+447386", Time: now, Environment: "vowifi"}
	task := automaticTaskNotification{
		Task:  store.AutomaticTask{DeviceID: "a"},
		Title: "自动任务执行成功",
		Text:  "自动任务执行成功\n任务  检查网络",
	}
	for _, test := range []struct {
		name, title string
		detail      string
		send        func(context.Context, map[string]any) error
	}{
		{"SMS", "收到新短信", sms.DetailText(), func(ctx context.Context, config map[string]any) error {
			return sendSMSNotification(ctx, "qqbot", config, sms)
		}},
		{"call", call.Title(), call.DetailText(), func(ctx context.Context, config map[string]any) error {
			return sendCallNotification(ctx, "qqbot", config, call)
		}},
		{"automatic task", "自动任务执行成功", task.DetailText(), func(ctx context.Context, config map[string]any) error {
			return sendAutomaticTaskNotification(ctx, "qqbot", config, task)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			platform := newQQBotFakePlatform(t)
			previous := qqBotNotificationSender
			qqBotNotificationSender = func(ctx context.Context, config map[string]any, title, text string) error {
				return postQQBotNotification(ctx, platform.server.Client(), platform.endpoints(), config, title, text)
			}
			t.Cleanup(func() { qqBotNotificationSender = previous })

			if err := test.send(context.Background(), qqBotTestConfig(nil)); err != nil {
				t.Fatal(err)
			}
			if len(platform.messages) != 1 || platform.messages[0].Body["content"] != test.title+"\n"+test.detail {
				t.Fatalf("messages = %#v", platform.messages)
			}
			// 设备范围不包含该设备时不发送。
			if err := test.send(context.Background(), qqBotTestConfig(map[string]any{"device_ids": []string{"b"}})); err != nil {
				t.Fatal(err)
			}
			if len(platform.messages) != 1 {
				t.Fatalf("scoped-out notification was sent: %d", len(platform.messages))
			}
		})
	}
}

func TestQQBotSettingsMaskAndClearSecret(t *testing.T) {
	api := newSettingsAPITest(t)
	save := func(body string) map[string]any {
		t.Helper()
		response := api.request(t, http.MethodPut, "/api/settings/notifications", body)
		if response.Code != http.StatusOK {
			t.Fatalf("save %s: %d %s", body, response.Code, response.Body)
		}
		return decodeSettingsResponse(t, response)["data"].(map[string]any)["qqbot"].(map[string]any)
	}
	stored := func() map[string]any {
		t.Helper()
		setting, err := api.database.NotificationSetting(context.Background(), "qqbot")
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(setting.Config, &config); err != nil {
			t.Fatal(err)
		}
		return config
	}

	view := save(`{"qqbot":{"enabled":true,"app_id":"102000001","app_secret":"test-secret-value","target_openid":"OPENID_1"}}`)
	if view["app_secret"] != store.SecretMask || view["app_id"] != "102000001" || view["target_openid"] != "OPENID_1" {
		t.Fatalf("view = %#v", view)
	}
	if stored()["app_secret"] != qqBotTestSecret {
		t.Fatal("secret was not stored")
	}
	// 表单回传掩码时保留原值。
	save(`{"qqbot":{"enabled":true,"app_id":"102000001","app_secret":"` + store.SecretMask + `","target_openid":"OPENID_2"}}`)
	if config := stored(); config["app_secret"] != qqBotTestSecret || config["target_openid"] != "OPENID_2" {
		t.Fatalf("config = %#v", config)
	}
	save(`{"qqbot":{"enabled":false,"app_secret":"","clear_secrets":true}}`)
	if config := stored(); config["app_secret"] != "" {
		t.Fatalf("secret not cleared: %#v", config)
	}

	// 只使用正式环境：不接受沙箱等未知字段。
	if response := api.request(t, http.MethodPut, "/api/settings/notifications", `{"qqbot":{"enabled":false,"sandbox":true}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("accepted sandbox field: %d", response.Code)
	}
}

func TestQQBotTestEndpointUsesSavedSecret(t *testing.T) {
	api := newSettingsAPITest(t)
	if response := api.request(t, http.MethodPut, "/api/settings/notifications",
		`{"qqbot":{"enabled":true,"app_id":"102000001","app_secret":"test-secret-value","target_openid":"OPENID_test-0123"}}`); response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body)
	}
	platform := newQQBotFakePlatform(t)
	previous := qqBotNotificationSender
	qqBotNotificationSender = func(ctx context.Context, config map[string]any, title, text string) error {
		return postQQBotNotification(ctx, platform.server.Client(), platform.endpoints(), config, title, text)
	}
	t.Cleanup(func() { qqBotNotificationSender = previous })

	response := api.request(t, http.MethodPost, "/api/settings/notifications/qqbot/test",
		`{"app_id":"102000001","app_secret":"`+store.SecretMask+`","target_openid":"OPENID_test-0123"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("test: %d %s", response.Code, response.Body)
	}
	if len(platform.messages) != 1 || platform.messages[0].Body["content"] != "VoCat 测试通知\nVoCat 消息推送测试" {
		t.Fatalf("messages = %#v", platform.messages)
	}

	platform.messageCode = 22009
	response = api.request(t, http.MethodPost, "/api/settings/notifications/qqbot/test", `{}`)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "notification_provider_rejected") {
		t.Fatalf("rejected test: %d %s", response.Code, response.Body)
	}
}
