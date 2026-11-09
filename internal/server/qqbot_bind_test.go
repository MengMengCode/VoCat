package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"vocat/internal/store"
)

// qqBotFakeGateway 模拟令牌接口、/gateway、WebSocket 网关和消息接口。
type qqBotFakeGateway struct {
	t        *testing.T
	server   *httptest.Server
	events   chan qqBotGatewayFrame
	mu       sync.Mutex
	identify map[string]any
	replies  []map[string]any
	// invalidSession 为真时网关在 Identify 后返回 op 9。
	invalidSession bool
	// 扫码绑定：每个任务的密钥、轮询次数，以及决定轮询结果的函数。
	qrKeys   map[string][]byte
	qrTasks  int
	qrPolls  map[string]int
	qrResult func(taskID string, poll int, key []byte) map[string]any
}

func newQQBotFakeGateway(t *testing.T) *qqBotFakeGateway {
	t.Helper()
	gateway := &qqBotFakeGateway{
		t: t, events: make(chan qqBotGatewayFrame, 4),
		qrKeys: map[string][]byte{}, qrPolls: map[string]int{},
	}
	gateway.server = httptest.NewServer(http.HandlerFunc(gateway.serve))
	t.Cleanup(gateway.server.Close)

	previousTokens := qqBotTokens
	qqBotTokens = &qqBotTokenCache{entries: map[string]qqBotToken{}, now: time.Now}
	previousEnv := qqBotBindEnvironment
	qqBotBindEnvironment = func(context.Context, map[string]any) (qqBotBindEnv, error) {
		return qqBotBindEnv{
			client: gateway.server.Client(),
			endpoints: qqBotEndpoints{
				TokenURL: gateway.server.URL + "/app/getAppAccessToken", APIBase: gateway.server.URL,
				ConnectBase: gateway.server.URL,
			},
			checkGateway: func(_ context.Context, raw string) (string, error) { return raw, nil },
			retryDelay:   10 * time.Millisecond,
			pollInterval: 10 * time.Millisecond,
		}, nil
	}
	t.Cleanup(func() {
		qqBotTokens = previousTokens
		qqBotBindEnvironment = previousEnv
	})
	return gateway
}

func (gateway *qqBotFakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/app/getAppAccessToken":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["appId"] != qqBotTestAppID || body["clientSecret"] != qqBotTestSecret {
			_, _ = w.Write([]byte(`{"code":100016,"message":"invalid appid or secret"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"gateway-token","expires_in":"7200"}`))
	case "/gateway":
		if r.Header.Get("Authorization") != "QQBot gateway-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(gateway.server.URL, "http") + "/ws"})
	case "/ws":
		gateway.serveWebSocket(w, r)
	case "/lite/create_bind_task":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		key, err := base64.StdEncoding.DecodeString(body["key"])
		if err != nil || len(key) != 32 {
			_, _ = w.Write([]byte(`{"retcode":1,"msg":"bad key"}`))
			return
		}
		gateway.mu.Lock()
		gateway.qrTasks++
		id := fmt.Sprintf("task-%d", gateway.qrTasks)
		gateway.qrKeys[id] = key
		gateway.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"retcode":0,"msg":"ok","data":{"task_id":%q}}`, id)
	case "/lite/poll_bind_result":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gateway.mu.Lock()
		key, ok := gateway.qrKeys[body["task_id"]]
		gateway.qrPolls[body["task_id"]]++
		poll := gateway.qrPolls[body["task_id"]]
		result := gateway.qrResult
		gateway.mu.Unlock()
		if !ok {
			_, _ = w.Write([]byte(`{"retcode":2,"msg":"unknown task"}`))
			return
		}
		data := map[string]any{"status": qqBotQRPending}
		if result != nil {
			data = result(body["task_id"], poll, key)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"retcode": 0, "msg": "ok", "data": data})
	default:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["path"] = r.URL.Path
		gateway.mu.Lock()
		gateway.replies = append(gateway.replies, body)
		gateway.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"reply-1"}`))
	}
}

func (gateway *qqBotFakeGateway) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		gateway.t.Errorf("accept: %v", err)
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	send := func(frame qqBotGatewayFrame) {
		payload, _ := json.Marshal(frame)
		_ = conn.Write(ctx, websocket.MessageText, payload)
	}
	send(qqBotGatewayFrame{Op: 10, D: json.RawMessage(`{"heartbeat_interval":20}`)})
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var identify struct {
		Op int            `json:"op"`
		D  map[string]any `json:"d"`
	}
	_ = json.Unmarshal(raw, &identify)
	gateway.mu.Lock()
	gateway.identify = identify.D
	gateway.mu.Unlock()
	if gateway.invalidSession {
		send(qqBotGatewayFrame{Op: 9, D: json.RawMessage(`false`)})
		return
	}
	sequence := int64(1)
	send(qqBotGatewayFrame{Op: 0, S: &sequence, T: "READY", D: json.RawMessage(`{"session_id":"s"}`)})
	// 后台读取心跳，避免阻塞客户端写入。
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-gateway.events:
			sequence++
			frame.S = &sequence
			send(frame)
		}
	}
}

func (gateway *qqBotFakeGateway) push(kind, data string) {
	gateway.events <- qqBotGatewayFrame{Op: 0, T: kind, D: json.RawMessage(data)}
}

func waitQQBotBind(t *testing.T, api settingsAPITest, want qqBotBindStatus) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response := api.request(t, http.MethodGet, "/api/settings/notifications/qqbot/bind", "")
		data, _ := decodeSettingsResponse(t, response)["data"].(map[string]any)
		if data != nil && data["status"] == string(want) {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("bind status = %#v, want %s", data, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func beginQQBotBind(t *testing.T, api settingsAPITest, body string) map[string]any {
	t.Helper()
	response := api.request(t, http.MethodPost, "/api/settings/notifications/qqbot/bind", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start bind: %d %s", response.Code, response.Body)
	}
	// 先停止并等待后台协程退出，再由 newQQBotFakeGateway 的 cleanup 恢复全局变量。
	t.Cleanup(api.server.StopQQBotBinding)
	data, _ := decodeSettingsResponse(t, response)["data"].(map[string]any)
	return data
}

var qqBotBindCodePattern = regexp.MustCompile(`^VOCAT-[2-9A-HJKMNP-Z]{5}-[2-9A-HJKMNP-Z]{5}$`)

func checkQQBotBindCode(t *testing.T, data map[string]any) string {
	t.Helper()
	code, _ := data["code"].(string)
	if data["mode"] != "code" || !qqBotBindCodePattern.MatchString(code) {
		t.Fatalf("bind = %#v", data)
	}
	return code
}

func startQQBotBind(t *testing.T, api settingsAPITest, body string) string {
	t.Helper()
	return checkQQBotBindCode(t, beginQQBotBind(t, api, body))
}

func storedQQBotConfig(t *testing.T, api settingsAPITest) (store.NotificationSetting, map[string]any) {
	t.Helper()
	setting, err := api.database.NotificationSetting(context.Background(), "qqbot")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(setting.Config, &config); err != nil {
		t.Fatal(err)
	}
	return setting, config
}

const qqBotBindDraft = `{"enabled":true,"app_id":"102000001","app_secret":"test-secret-value"}`

func TestQQBotBindsDirectMessageSender(t *testing.T) {
	gateway := newQQBotFakeGateway(t)
	api := newSettingsAPITest(t)
	code := startQQBotBind(t, api, qqBotBindDraft)
	waitQQBotBind(t, api, qqBotBindWaiting)

	gateway.mu.Lock()
	identify := gateway.identify
	gateway.mu.Unlock()
	if identify["token"] != "QQBot gateway-token" || identify["intents"] != float64(qqBotGatewayIntents) {
		t.Fatalf("identify = %#v", identify)
	}

	// 不含绑定码的消息被忽略。
	gateway.push("C2C_MESSAGE_CREATE", `{"id":"m0","content":"hello","author":{"user_openid":"OTHER"}}`)
	gateway.push("C2C_MESSAGE_CREATE", `{"id":"m1","content":" `+strings.ToLower(code)+` ","author":{"user_openid":"USER_OPENID_1"}}`)
	data := waitQQBotBind(t, api, qqBotBindBound)
	if data["mode"] != "code" || strings.Contains(mustString(t, data), "USER_OPENID_1") {
		t.Fatalf("status = %#v", data)
	}

	setting, config := storedQQBotConfig(t, api)
	// 完成绑定后自动启用通道。
	if !setting.Enabled || config["target_openid"] != "USER_OPENID_1" ||
		config["app_id"] != qqBotTestAppID || config["app_secret"] != qqBotTestSecret {
		t.Fatalf("stored enabled=%v config=%#v", setting.Enabled, config)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		gateway.mu.Lock()
		replies := append([]map[string]any(nil), gateway.replies...)
		gateway.mu.Unlock()
		if len(replies) == 1 {
			reply := replies[0]
			if reply["path"] != "/v2/users/USER_OPENID_1/messages" || reply["msg_id"] != "m1" || reply["content"] != qqBotBindReply {
				t.Fatalf("reply = %#v", reply)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replies = %#v", replies)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustString(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestQQBotBindKeepsSavedSettings(t *testing.T) {
	gateway := newQQBotFakeGateway(t)
	api := newSettingsAPITest(t)
	if response := api.request(t, http.MethodPut, "/api/settings/notifications",
		`{"qqbot":{"enabled":true,"app_id":"102000001","app_secret":"test-secret-value","target_openid":"OLD","device_ids":["a"]}}`); response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body)
	}
	// 表单回传掩码，使用已保存的 AppSecret。
	code := startQQBotBind(t, api, `{"app_id":"102000001","app_secret":"`+store.SecretMask+`"}`)
	waitQQBotBind(t, api, qqBotBindWaiting)
	gateway.push("C2C_MESSAGE_CREATE", `{"id":"m2","content":"`+code+`","author":{"user_openid":"USER_OPENID_2"}}`)
	waitQQBotBind(t, api, qqBotBindBound)

	setting, config := storedQQBotConfig(t, api)
	ids, _ := config["device_ids"].([]any)
	if !setting.Enabled || config["target_openid"] != "USER_OPENID_2" || config["app_secret"] != qqBotTestSecret || len(ids) != 1 {
		t.Fatalf("stored enabled=%v config=%#v", setting.Enabled, config)
	}
}

func TestQQBotBindRejectsBadCredentials(t *testing.T) {
	newQQBotFakeGateway(t)
	api := newSettingsAPITest(t)
	if response := api.request(t, http.MethodPost, "/api/settings/notifications/qqbot/bind", `{"app_id":"abc"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid app id: %d", response.Code)
	}
	startQQBotBind(t, api, `{"app_id":"102000001","app_secret":"wrong-secret"}`)
	data := waitQQBotBind(t, api, qqBotBindFailed)
	if text, _ := data["error"].(string); !strings.Contains(text, "100016") || strings.Contains(text, "wrong-secret") {
		t.Fatalf("error = %q", text)
	}
}

func TestQQBotBindReportsInvalidSessionAndCancels(t *testing.T) {
	gateway := newQQBotFakeGateway(t)
	gateway.invalidSession = true
	api := newSettingsAPITest(t)
	startQQBotBind(t, api, qqBotBindDraft)
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := decodeSettingsResponse(t, api.request(t, http.MethodGet, "/api/settings/notifications/qqbot/bind", ""))["data"].(map[string]any)
		if text, _ := data["error"].(string); strings.Contains(text, "rejected the session") {
			if data["status"] != string(qqBotBindConnecting) {
				t.Fatalf("status = %#v", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %#v", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response := api.request(t, http.MethodDelete, "/api/settings/notifications/qqbot/bind", "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel: %d", response.Code)
	}
	waitQQBotBind(t, api, qqBotBindCanceled)
	if _, err := api.database.NotificationSetting(context.Background(), "qqbot"); err == nil {
		t.Fatal("canceled binding saved a setting")
	}
}

func TestQQBotBindMatchesOnlyMessagesWithCode(t *testing.T) {
	for _, test := range []struct {
		kind, data string
		ok         bool
	}{
		{"C2C_MESSAGE_CREATE", `{"content":"VOCAT-123456","author":{"user_openid":"U"}}`, true},
		{"C2C_MESSAGE_CREATE", `{"content":"VOCAT-654321","author":{"user_openid":"U"}}`, false},
		{"C2C_MESSAGE_CREATE", `{"content":"VOCAT-123456","author":{"user_openid":"a/b"}}`, false},
		// 只支持单聊：群消息即使包含绑定码也忽略。
		{"GROUP_AT_MESSAGE_CREATE", `{"content":"VOCAT-123456","group_openid":"G","author":{"user_openid":"U"}}`, false},
		{"AT_MESSAGE_CREATE", `{"content":"VOCAT-123456","author":{"user_openid":"U"}}`, false},
	} {
		if _, ok := matchQQBotBindEvent(test.kind, json.RawMessage(test.data), "VOCAT-123456"); ok != test.ok {
			t.Errorf("%s %s: ok=%v", test.kind, test.data, ok)
		}
	}
}

func TestQQBotGatewayURLMustBeWSS(t *testing.T) {
	for _, raw := range []string{"ws://api.sgroup.qq.com/websocket", "https://api.sgroup.qq.com/websocket", "wss://127.0.0.1/websocket"} {
		if _, err := checkQQBotGatewayURL(context.Background(), raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

// sealQQBotSecret 按开放平台格式加密：12 字节 nonce + 密文 + 16 字节 tag。
func sealQQBotSecret(t *testing.T, key []byte, secret string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(secret), nil))
}

func TestQQBotQRBindSavesScannedBotAndRecipient(t *testing.T) {
	gateway := newQQBotFakeGateway(t)
	gateway.qrResult = func(taskID string, poll int, key []byte) map[string]any {
		switch {
		case taskID == "task-1" && poll == 1:
			return map[string]any{"status": qqBotQRPending}
		case taskID == "task-1":
			// 第一个二维码过期，应自动换新。
			return map[string]any{"status": qqBotQRExpired}
		default:
			return map[string]any{
				"status": qqBotQRCompleted, "bot_appid": 102000001,
				"bot_encrypt_secret": sealQQBotSecret(t, key, qqBotTestSecret),
				"user_openid":        "USER_OPENID_QR",
			}
		}
	}
	api := newSettingsAPITest(t)
	// 已保存的设备过滤在扫码后保留，凭据和接收方被替换。
	if response := api.request(t, http.MethodPut, "/api/settings/notifications",
		`{"qqbot":{"enabled":true,"app_id":"1","app_secret":"old-secret","target_openid":"OLD","device_ids":["a"]}}`); response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body)
	}
	// 扫码不需要预先填写凭据。
	data := beginQQBotBind(t, api, `{"mode":"qr"}`)
	qrURL, _ := data["qr_url"].(string)
	if data["mode"] != "qr" || data["status"] != string(qqBotBindWaiting) ||
		!strings.Contains(qrURL, "/qqbot/openclaw/connect.html?") || !strings.Contains(qrURL, "task_id=task-1") {
		t.Fatalf("bind = %#v", data)
	}
	data = waitQQBotBind(t, api, qqBotBindBound)
	if text := mustString(t, data); strings.Contains(text, qqBotTestSecret) || strings.Contains(text, "USER_OPENID_QR") {
		t.Fatalf("status leaks binding: %s", text)
	}

	setting, config := storedQQBotConfig(t, api)
	ids, _ := config["device_ids"].([]any)
	if !setting.Enabled || config["app_id"] != qqBotTestAppID || config["app_secret"] != qqBotTestSecret ||
		config["target_openid"] != "USER_OPENID_QR" || len(ids) != 1 {
		t.Fatalf("stored enabled=%v config=%#v", setting.Enabled, config)
	}
	gateway.mu.Lock()
	tasks := gateway.qrTasks
	gateway.mu.Unlock()
	if tasks != 2 {
		t.Fatalf("tasks = %d, want 2 (expired QR refreshed)", tasks)
	}
	// 绑定后用扫到的凭据登录一次网关，让扫码页确认连接完成。
	deadline := time.Now().Add(5 * time.Second)
	for {
		gateway.mu.Lock()
		identify := gateway.identify
		gateway.mu.Unlock()
		if identify != nil {
			if identify["token"] != "QQBot gateway-token" {
				t.Fatalf("identify = %#v", identify)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bot did not log in to the gateway after QR binding")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQQBotQRBindFallsBackToCodeWithoutOpenID(t *testing.T) {
	gateway := newQQBotFakeGateway(t)
	gateway.qrResult = func(_ string, _ int, key []byte) map[string]any {
		return map[string]any{
			"status": qqBotQRCompleted, "bot_appid": qqBotTestAppID,
			"bot_encrypt_secret": sealQQBotSecret(t, key, qqBotTestSecret),
		}
	}
	api := newSettingsAPITest(t)
	// 已有另一个机器人的绑定：拿到新接收方之前必须原样保留。
	if err := api.database.UpsertNotificationSetting(context.Background(), store.NotificationSetting{
		Channel: "qqbot", Enabled: true,
		Config:          json.RawMessage(`{"app_id":"999","app_secret":"old-secret","target_openid":"OLD_OPENID"}`),
		SensitiveFields: store.DefaultNotificationSensitiveFields("qqbot"),
	}); err != nil {
		t.Fatal(err)
	}
	beginQQBotBind(t, api, `{"mode":"qr"}`)
	data := waitQQBotBind(t, api, qqBotBindWaiting)
	if data["mode"] != "code" {
		// 首次轮询前状态也是 waiting，等到切换为绑定码。
		deadline := time.Now().Add(5 * time.Second)
		for data["mode"] != "code" || data["status"] != string(qqBotBindWaiting) {
			if time.Now().After(deadline) {
				t.Fatalf("bind = %#v", data)
			}
			time.Sleep(10 * time.Millisecond)
			data, _ = decodeSettingsResponse(t, api.request(t, http.MethodGet, "/api/settings/notifications/qqbot/bind", ""))["data"].(map[string]any)
		}
	}
	code := checkQQBotBindCode(t, data)
	// 扫码得到的新凭据只留在会话中，原有绑定不变。
	if setting, config := storedQQBotConfig(t, api); !setting.Enabled || config["app_id"] != "999" ||
		config["app_secret"] != "old-secret" || config["target_openid"] != "OLD_OPENID" {
		t.Fatalf("enabled=%v config=%#v", setting.Enabled, config)
	}
	gateway.push("C2C_MESSAGE_CREATE", `{"id":"m3","content":"`+code+`","author":{"user_openid":"USER_OPENID_3"}}`)
	waitQQBotBind(t, api, qqBotBindBound)
	// 新凭据与新接收方一起写入。
	if setting, config := storedQQBotConfig(t, api); !setting.Enabled || config["app_id"] != qqBotTestAppID ||
		config["app_secret"] != qqBotTestSecret || config["target_openid"] != "USER_OPENID_3" {
		t.Fatalf("config = %#v", config)
	}
}

func TestQQBotQRBindRejectsUndecryptableSecret(t *testing.T) {
	gateway := newQQBotFakeGateway(t)
	gateway.qrResult = func(_ string, _ int, _ []byte) map[string]any {
		wrong := make([]byte, 32)
		return map[string]any{
			"status": qqBotQRCompleted, "bot_appid": qqBotTestAppID,
			"bot_encrypt_secret": sealQQBotSecret(t, wrong, qqBotTestSecret), "user_openid": "U",
		}
	}
	api := newSettingsAPITest(t)
	beginQQBotBind(t, api, `{"mode":"qr"}`)
	waitQQBotBind(t, api, qqBotBindFailed)
	if _, err := api.database.NotificationSetting(context.Background(), "qqbot"); err == nil {
		t.Fatal("failed binding saved a setting")
	}
}

func TestQQBotBindRejectsUnknownMode(t *testing.T) {
	newQQBotFakeGateway(t)
	api := newSettingsAPITest(t)
	if response := api.request(t, http.MethodPost, "/api/settings/notifications/qqbot/bind", `{"mode":"group"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("mode group: %d", response.Code)
	}
}

func TestQQBotBindHidesCodeAndQRFromReadOnlyTokens(t *testing.T) {
	newQQBotFakeGateway(t)
	app, authService, _ := newBearerTestApplication(t)
	write, err := authService.CreateAPIToken(context.Background(), "writer", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := authService.CreateAPIToken(context.Background(), "reader", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, token, body string) (int, map[string]any) {
		t.Helper()
		request, err := http.NewRequest(method, app.server.URL+"/api/settings/notifications/qqbot/bind", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		return response.StatusCode, envelope.Data
	}

	// 只读令牌不能开始绑定。
	if status, _ := call(http.MethodPost, readOnly.Token, `{"mode":"qr"}`); status != http.StatusForbidden {
		t.Fatalf("read-only start: %d", status)
	}
	status, data := call(http.MethodPost, write.Token, `{"mode":"qr"}`)
	if status != http.StatusAccepted || data["qr_url"] == nil {
		t.Fatalf("start: %d %#v", status, data)
	}
	t.Cleanup(func() { call(http.MethodDelete, write.Token, "") })

	if _, data := call(http.MethodGet, write.Token, ""); data["qr_url"] == nil || data["status"] != string(qqBotBindWaiting) {
		t.Fatalf("writer view = %#v", data)
	}
	_, data = call(http.MethodGet, readOnly.Token, "")
	if data["status"] != string(qqBotBindWaiting) || data["qr_url"] != nil || data["code"] != nil {
		t.Fatalf("read-only view = %#v", data)
	}
}

func TestQQBotBindRefreshesTokenRejectedByGateway(t *testing.T) {
	newQQBotFakeGateway(t)
	// 缓存中的令牌已被平台提前作废：网关查询返回 401 后应换新令牌重试。
	qqBotTokens.entries[qqBotTokenCacheKeyForTest(t)] = qqBotToken{
		value: "stale-token", expires: time.Now().Add(time.Hour), secret: qqBotTestSecret,
	}
	api := newSettingsAPITest(t)
	startQQBotBind(t, api, qqBotBindDraft)
	waitQQBotBind(t, api, qqBotBindWaiting)
}

func qqBotTokenCacheKeyForTest(t *testing.T) string {
	t.Helper()
	env, err := qqBotBindEnvironment(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return env.endpoints.TokenURL + "\x00" + qqBotTestAppID
}

func TestQQBotCanceledBindDoesNotSave(t *testing.T) {
	api := newSettingsAPITest(t)
	session := &qqBotBindSession{Status: qqBotBindCanceled, cancel: func() {}}
	committed, err := api.server.commitQQBotBind(context.Background(), session, qqBotTestConfig(nil), "USER_OPENID")
	if err != nil || committed {
		t.Fatalf("committed=%v err=%v", committed, err)
	}
	if _, err := api.database.NotificationSetting(context.Background(), "qqbot"); err == nil {
		t.Fatal("canceled binding was saved")
	}
}

func TestQQBotStopBindingCancelsAndWaits(t *testing.T) {
	newQQBotFakeGateway(t)
	api := newSettingsAPITest(t)
	startQQBotBind(t, api, qqBotBindDraft)
	waitQQBotBind(t, api, qqBotBindWaiting)
	api.server.StopQQBotBinding()
	waitQQBotBind(t, api, qqBotBindCanceled)
	// 停止后新的绑定立即结束，不会在关闭后继续访问存储。
	response := api.request(t, http.MethodPost, "/api/settings/notifications/qqbot/bind", qqBotBindDraft)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start after stop: %d %s", response.Code, response.Body)
	}
	api.server.StopQQBotBinding()
}
