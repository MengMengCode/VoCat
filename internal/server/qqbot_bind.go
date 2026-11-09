package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"vocat/internal/store"
)

// QQ 机器人有两种绑定方式：
//
//   - 扫码绑定（qr）：在开放平台创建绑定任务，用户用手机 QQ 扫码选择机器人，
//     VoCat 轮询结果，得到机器人 AppID、加密的 AppSecret 和扫码用户的 openid。
//   - 绑定码（code）：已手动填写 AppID/AppSecret 时，VoCat 临时连接开放平台的
//     WebSocket 网关（出站连接，无需公网地址），等待用户把一次性绑定码私聊发给
//     机器人，从该消息事件中取得用户 openid。
//
// 两种方式都只在绑定期间访问开放平台，结束或超时后停止。
const (
	qqBotBindTTL = 5 * time.Minute
	// GROUP_AND_C2C_EVENT 包含单聊消息事件。
	qqBotGatewayIntents = 1 << 25
	qqBotBindReply      = "已绑定 VoCat 通知。"
	qqBotQRPollInterval = 2 * time.Second
	// 扫码页在机器人上线前一直显示“连接中”：绑定后短暂登录网关再断开。
	qqBotOnlineHold    = 10 * time.Second
	qqBotOnlineTimeout = 30 * time.Second
)

type qqBotBindStatus string

const (
	qqBotBindConnecting qqBotBindStatus = "connecting"
	qqBotBindWaiting    qqBotBindStatus = "waiting"
	qqBotBindBound      qqBotBindStatus = "bound"
	qqBotBindExpired    qqBotBindStatus = "expired"
	qqBotBindFailed     qqBotBindStatus = "failed"
	qqBotBindCanceled   qqBotBindStatus = "canceled"
)

type qqBotBindSession struct {
	Mode      string          `json:"mode"`
	Code      string          `json:"code,omitempty"`
	QRURL     string          `json:"qr_url,omitempty"`
	ExpiresAt time.Time       `json:"expires_at"`
	Status    qqBotBindStatus `json:"status"`
	Error     string          `json:"error,omitempty"`
	cancel    context.CancelFunc
}

func (session *qqBotBindSession) finished() bool {
	switch session.Status {
	case qqBotBindBound, qqBotBindExpired, qqBotBindFailed, qqBotBindCanceled:
		return true
	}
	return false
}

// qqBotBindEnv 描述一次绑定使用的网络环境；测试中替换为本地服务。
type qqBotBindEnv struct {
	client    *http.Client
	endpoints qqBotEndpoints
	// checkGateway 校验网关返回的 WebSocket 地址；测试中允许本地 ws://。
	checkGateway func(context.Context, string) (string, error)
	retryDelay   time.Duration
	pollInterval time.Duration
	// onlineHold 是扫码绑定后机器人保持在线的时长。
	onlineHold time.Duration
}

var qqBotBindEnvironment = defaultQQBotBindEnv

func defaultQQBotBindEnv(ctx context.Context, config map[string]any) (qqBotBindEnv, error) {
	endpoints := defaultQQBotEndpoints
	for _, endpoint := range []string{endpoints.TokenURL, endpoints.APIBase, endpoints.ConnectBase} {
		if _, err := validateOutboundURL(ctx, endpoint, true); err != nil {
			return qqBotBindEnv{}, err
		}
	}
	client, err := persistentRestrictedHTTPClient(ctx, 10*time.Second, configString(config, "proxy"))
	if err != nil {
		return qqBotBindEnv{}, err
	}
	return qqBotBindEnv{
		client:       client,
		endpoints:    endpoints,
		checkGateway: checkQQBotGatewayURL,
		retryDelay:   2 * time.Second,
		pollInterval: qqBotQRPollInterval,
		onlineHold:   qqBotOnlineHold,
	}, nil
}

// checkQQBotGatewayURL 只接受指向公网地址的 wss:// 网关。
func checkQQBotGatewayURL(ctx context.Context, raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "wss" {
		return "", errors.New("QQ bot gateway must be a wss:// URL")
	}
	probe := *parsed
	probe.Scheme = "https"
	if _, err := validateOutboundURL(ctx, probe.String(), true); err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// qqBotBindCodeAlphabet 去掉了容易混淆的 0/O、1/I/L。
const qqBotBindCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// newQQBotBindCode 生成 VOCAT-XXXXX-XXXXX：31^10 ≈ 8×10^14 种组合，
// 有效期内无法靠给机器人发消息猜中，把自己绑定成接收方。
func newQQBotBindCode() (string, error) {
	var code strings.Builder
	code.WriteString("VOCAT-")
	limit := big.NewInt(int64(len(qqBotBindCodeAlphabet)))
	for index := range 10 {
		if index == 5 {
			code.WriteByte('-')
		}
		number, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		code.WriteByte(qqBotBindCodeAlphabet[number.Int64()])
	}
	return code.String(), nil
}

func (s *Server) handleQQBotBind(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.qqBotBindMu.Lock()
		var view *qqBotBindSession
		if session := s.qqBotBind; session != nil {
			copied := *session
			copied.cancel = nil
			// 绑定码和二维码可以把接收方改成任何人：只读令牌只能看到状态。
			if principal, ok := bearerPrincipalFromRequest(r); ok && principal.ReadOnly {
				copied.Code, copied.QRURL = "", ""
			}
			view = &copied
		}
		s.qqBotBindMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"data": view})
	case http.MethodPost:
		s.startQQBotBind(w, r)
	case http.MethodDelete:
		s.qqBotBindMu.Lock()
		if session := s.qqBotBind; session != nil && !session.finished() {
			session.cancel()
			session.Status = qqBotBindCanceled
		}
		s.qqBotBindMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": qqBotBindCanceled}})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// startQQBotBind 接受 {"mode":"qr"|"code", ...qqbot 配置草稿}。
func (s *Server) startQQBotBind(w http.ResponseWriter, r *http.Request) {
	var document map[string]json.RawMessage
	if err := s.decodeJSON(w, r, &document); err != nil || document == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a JSON object")
		return
	}
	mode := "code"
	if raw, ok := document["mode"]; ok {
		if json.Unmarshal(raw, &mode) != nil || (mode != "qr" && mode != "code") {
			writeError(w, http.StatusBadRequest, "invalid_request", "mode must be qr or code")
			return
		}
		delete(document, "mode")
	}
	raw, _ := json.Marshal(document)
	_, incoming, err := decodeNotificationConfig("qqbot", raw, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
		return
	}
	if !s.qqBotSecretReentered(w, r, incoming) {
		return
	}
	// 与测试通知相同：表单回传的掩码从已保存配置中恢复。
	resolved, _, err := s.resolveNotificationTestConfig(r.Context(), "qqbot", incoming)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err)
		return
	}
	if resolved == nil {
		resolved = map[string]any{}
	}
	if mode == "code" {
		// 绑定前还没有 openid，只校验凭据。
		probe := map[string]any{}
		for key, value := range resolved {
			probe[key] = value
		}
		probe["target_openid"] = "pending"
		if err := validateQQBotNotificationConfig(probe); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
			return
		}
	}
	env, err := qqBotBindEnvironment(s.notificationDestinationContext(r.Context()), resolved)
	if err != nil {
		if errors.Is(err, errUnsafeDestination) {
			writeError(w, http.StatusBadRequest, "unsafe_destination", "notification destination resolved to an unusable or protected system address")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(s.qqBotWorkContext(), qqBotBindTTL)
	session := &qqBotBindSession{
		Mode: mode, ExpiresAt: time.Now().Add(qqBotBindTTL).UTC(),
		Status: qqBotBindConnecting, cancel: cancel,
	}
	if mode == "qr" {
		task, err := createQQBotQRTask(ctx, env)
		if err != nil {
			cancel()
			s.logger.Warn("QQ bot QR binding could not start", "error", err)
			writeError(w, http.StatusBadGateway, "qqbot_bind_unavailable", store.RedactText(err.Error(), store.NotificationSetting{}))
			return
		}
		session.QRURL = task.URL
		session.Status = qqBotBindWaiting
		s.qqBotBindWork.Go(func() { s.runQQBotQRBind(ctx, session, resolved, env, task) })
	} else {
		code, err := newQQBotBindCode()
		if err != nil {
			cancel()
			s.writeStoreError(w, err)
			return
		}
		session.Code = code
		s.qqBotBindWork.Go(func() { s.runQQBotCodeBind(ctx, session, resolved, env) })
	}

	s.qqBotBindMu.Lock()
	if previous := s.qqBotBind; previous != nil && !previous.finished() {
		previous.cancel()
		previous.Status = qqBotBindCanceled
	}
	s.qqBotBind = session
	view := *session
	view.cancel = nil
	s.qqBotBindMu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{"data": view})
}

// qqBotWorkContext 返回绑定协程的父 context；StopQQBotBinding 之后返回已取消的 context。
func (s *Server) qqBotWorkContext() context.Context {
	s.qqBotBindMu.Lock()
	defer s.qqBotBindMu.Unlock()
	if s.qqBotWorkCtx == nil {
		s.qqBotWorkCtx, s.qqBotWorkCancel = context.WithCancel(context.Background())
	}
	return s.qqBotWorkCtx
}

// StopQQBotBinding 取消进行中的绑定和扫码后的上线登录，并等待它们退出。
// 在关闭 HTTP 服务之后、关闭数据库之前调用，避免后台协程在关闭后访问存储。
func (s *Server) StopQQBotBinding() {
	s.qqBotWorkContext()
	s.qqBotBindMu.Lock()
	s.qqBotWorkCancel()
	if session := s.qqBotBind; session != nil && !session.finished() {
		session.cancel()
		session.Status = qqBotBindCanceled
	}
	s.qqBotBindMu.Unlock()
	s.qqBotBindWork.Wait()
}

func (s *Server) updateQQBotBind(session *qqBotBindSession, update func(*qqBotBindSession)) {
	s.qqBotBindMu.Lock()
	defer s.qqBotBindMu.Unlock()
	if session.Status == qqBotBindCanceled {
		return
	}
	update(session)
}

func (s *Server) failQQBotBind(session *qqBotBindSession, message string) {
	s.logger.Warn("QQ bot binding failed", "mode", session.Mode, "error", message)
	s.updateQQBotBind(session, func(session *qqBotBindSession) {
		session.Status = qqBotBindFailed
		session.Error = message
	})
}

// commitQQBotBind 在绑定锁内保存接收方并把会话标记为已绑定，与取消（DELETE 或新的绑定）串行：
// 已取消的会话不会再写入，已写入的绑定也不会被报告为已取消。返回是否已写入。
func (s *Server) commitQQBotBind(
	ctx context.Context,
	session *qqBotBindSession,
	config map[string]any,
	openID string,
) (bool, error) {
	s.qqBotBindMu.Lock()
	defer s.qqBotBindMu.Unlock()
	if session.Status == qqBotBindCanceled {
		return false, nil
	}
	if err := s.saveQQBotBinding(ctx, config, openID); err != nil {
		return false, err
	}
	session.Status, session.Error, session.QRURL = qqBotBindBound, "", ""
	return true, nil
}

// finishQQBotBindOnTimeout 在 ctx 结束时把未完成的绑定标记为过期。
func (s *Server) finishQQBotBindOnTimeout(session *qqBotBindSession) {
	s.updateQQBotBind(session, func(session *qqBotBindSession) {
		if !session.finished() {
			session.Status = qqBotBindExpired
		}
	})
}

func redactQQBotError(err error, config map[string]any) string {
	encoded, _ := json.Marshal(config)
	return store.RedactText(err.Error(), store.NotificationSetting{
		Channel: "qqbot", Config: encoded, SensitiveFields: store.DefaultNotificationSensitiveFields("qqbot"),
	})
}

// ---- 扫码绑定 ----

type qqBotQRTask struct {
	ID  string
	Key []byte
	URL string
}

type qqBotConnectReply struct {
	Retcode int             `json:"retcode"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

func postQQBotConnect(ctx context.Context, env qqBotBindEnv, path string, body any, data any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(env.endpoints.ConnectBase, "/")+path, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "vocat-qqbot-notification/1")
	response, err := env.client.Do(request)
	if err != nil {
		return fmt.Errorf("QQ bot binding request failed: %w", unwrapURLError(err))
	}
	defer response.Body.Close()
	var reply qqBotConnectReply
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK || json.Unmarshal(raw, &reply) != nil || reply.Retcode != 0 {
		message := []rune(strings.TrimSpace(reply.Msg))
		if len(message) > 200 {
			message = message[:200]
		}
		return fmt.Errorf("%w: QQ bot binding HTTP %d (retcode %d: %s)",
			errProviderRejected, response.StatusCode, reply.Retcode, string(message))
	}
	if data != nil && len(reply.Data) > 0 {
		if err := json.Unmarshal(reply.Data, data); err != nil {
			return fmt.Errorf("invalid QQ bot binding response: %w", err)
		}
	}
	return nil
}

// createQQBotQRTask 创建扫码绑定任务。key 只保存在内存中，用于解密返回的 AppSecret。
func createQQBotQRTask(ctx context.Context, env qqBotBindEnv) (qqBotQRTask, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return qqBotQRTask{}, err
	}
	var data struct {
		TaskID string `json:"task_id"`
	}
	if err := postQQBotConnect(ctx, env, "/lite/create_bind_task",
		map[string]string{"key": base64.StdEncoding.EncodeToString(key)}, &data); err != nil {
		return qqBotQRTask{}, err
	}
	if strings.TrimSpace(data.TaskID) == "" {
		return qqBotQRTask{}, errors.New("QQ bot binding did not return a task")
	}
	connect := strings.TrimRight(env.endpoints.ConnectBase, "/") + "/qqbot/openclaw/connect.html?" +
		url.Values{"task_id": {data.TaskID}, "source": {""}, "_wv": {"2"}}.Encode()
	return qqBotQRTask{ID: data.TaskID, Key: key, URL: connect}, nil
}

const (
	qqBotQRPending   = 1
	qqBotQRCompleted = 2
	qqBotQRExpired   = 3
)

type qqBotQRResult struct {
	Status        int    `json:"status"`
	BotAppID      any    `json:"bot_appid"`
	EncryptSecret string `json:"bot_encrypt_secret"`
	UserOpenID    string `json:"user_openid"`
}

// decryptQQBotSecret 解密 AES-256-GCM 密文：12 字节 nonce + 密文 + 16 字节 tag。
func decryptQQBotSecret(encoded string, key []byte) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(sealed) < 12+16 {
		return "", errors.New("QQ bot binding returned an invalid secret")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, sealed[:12], sealed[12:], nil)
	if err != nil {
		return "", errors.New("QQ bot binding secret could not be decrypted")
	}
	return string(plain), nil
}

func (s *Server) runQQBotQRBind(ctx context.Context, session *qqBotBindSession, config map[string]any, env qqBotBindEnv, task qqBotQRTask) {
	defer session.cancel()
	for {
		select {
		case <-ctx.Done():
			s.finishQQBotBindOnTimeout(session)
			return
		case <-time.After(env.pollInterval):
		}
		var result qqBotQRResult
		if err := postQQBotConnect(ctx, env, "/lite/poll_bind_result", map[string]string{"task_id": task.ID}, &result); err != nil {
			if ctx.Err() != nil {
				s.finishQQBotBindOnTimeout(session)
				return
			}
			// 轮询失败时继续重试。
			s.logger.Warn("QQ bot QR binding poll failed", "error", redactQQBotError(err, config))
			s.updateQQBotBind(session, func(session *qqBotBindSession) { session.Error = redactQQBotError(err, config) })
			continue
		}
		switch result.Status {
		case qqBotQRExpired:
			// 二维码过期后自动换新，直到绑定整体超时。
			next, err := createQQBotQRTask(ctx, env)
			if err != nil {
				if ctx.Err() == nil {
					s.failQQBotBind(session, redactQQBotError(err, config))
				} else {
					s.finishQQBotBindOnTimeout(session)
				}
				return
			}
			task = next
			s.updateQQBotBind(session, func(session *qqBotBindSession) { session.QRURL = next.URL })
		case qqBotQRCompleted:
			s.completeQQBotQRBind(ctx, session, config, env, task, result)
			return
		}
	}
}

func (s *Server) completeQQBotQRBind(
	ctx context.Context,
	session *qqBotBindSession,
	config map[string]any,
	env qqBotBindEnv,
	task qqBotQRTask,
	result qqBotQRResult,
) {
	appID := strings.TrimSpace(fmt.Sprint(result.BotAppID))
	if number, ok := result.BotAppID.(float64); ok {
		appID = fmt.Sprintf("%.0f", number)
	}
	secret, err := decryptQQBotSecret(result.EncryptSecret, task.Key)
	if err != nil || !qqBotAppIDPattern.MatchString(appID) || strings.TrimSpace(secret) == "" {
		s.logger.Warn("QQ bot QR binding returned unusable credentials",
			"app_id_valid", qqBotAppIDPattern.MatchString(appID), "decrypt_error", err, "secret_empty", strings.TrimSpace(secret) == "")
		s.failQQBotBind(session, "the scanned bot credentials were invalid")
		return
	}
	scanned := map[string]any{}
	for key, value := range config {
		scanned[key] = value
	}
	scanned["app_id"] = appID
	scanned["app_secret"] = secret

	openID := strings.TrimSpace(result.UserOpenID)
	if !qqBotOpenIDPattern.MatchString(openID) {
		// 平台未返回扫码用户：改用绑定码获取接收方。新凭据只保存在本次会话中，
		// 拿到接收方后与之一起写入，避免中途出现“新机器人 + 旧接收方”的配置。
		code, err := newQQBotBindCode()
		if err != nil {
			s.failQQBotBind(session, "the binding could not be saved")
			return
		}
		s.updateQQBotBind(session, func(session *qqBotBindSession) {
			session.Mode, session.Code, session.QRURL = "code", code, ""
			session.Status, session.Error = qqBotBindConnecting, ""
		})
		s.runQQBotCodeBind(ctx, session, scanned, env)
		return
	}
	committed, err := s.commitQQBotBind(ctx, session, scanned, openID)
	if err != nil {
		s.logger.Error("save QQ bot binding", "error", err)
		s.failQQBotBind(session, "the binding could not be saved")
		return
	}
	if !committed {
		return
	}
	s.logger.Info("QQ bot notification recipient bound", "method", "qr")
	// 绑定会话随即结束，上线使用独立的超时。
	s.qqBotBindWork.Go(func() {
		ctx, cancel := context.WithTimeout(s.qqBotWorkContext(), qqBotOnlineTimeout+env.onlineHold)
		defer cancel()
		if err := bringQQBotOnline(ctx, env, appID, secret); err != nil {
			s.logger.Warn("QQ bot could not come online after QR binding", "error", redactQQBotError(err, scanned))
			return
		}
		s.logger.Info("QQ bot came online after QR binding")
	})
}

// bringQQBotOnline 登录一次网关：收到 READY 后保持 env.onlineHold 再断开。
// 只用于让扫码页确认连接完成，不接收或处理任何消息。
func bringQQBotOnline(ctx context.Context, env qqBotBindEnv, appID, secret string) error {
	token, gateway, err := connectQQBotGateway(ctx, env, appID, secret)
	if err != nil {
		return err
	}
	dialClient := *env.client
	dialClient.Timeout = 0
	dialCtx, cancelDial := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, gateway, &websocket.DialOptions{HTTPClient: &dialClient})
	cancelDial()
	if err != nil {
		return fmt.Errorf("QQ bot gateway connection failed: %w", unwrapURLError(err))
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	readyCtx, cancelReady := context.WithTimeout(ctx, qqBotOnlineTimeout)
	defer cancelReady()
	for {
		_, raw, err := conn.Read(readyCtx)
		if err != nil {
			return fmt.Errorf("QQ bot gateway closed: %w", err)
		}
		var frame qqBotGatewayFrame
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		switch frame.Op {
		case 10: // Hello
			payload, _ := json.Marshal(map[string]any{"op": 2, "d": map[string]any{
				"token": "QQBot " + token, "intents": qqBotGatewayIntents, "shard": []int{0, 1},
			}})
			if err := conn.Write(readyCtx, websocket.MessageText, payload); err != nil {
				return err
			}
		case 7, 9:
			return fmt.Errorf("QQ bot gateway ended the session (op %d)", frame.Op)
		case 0:
			if frame.T != "READY" {
				continue
			}
			// 保持在线一小段时间，让扫码页看到机器人上线；期间网关的心跳间隔远大于该时长。
			select {
			case <-ctx.Done():
			case <-time.After(env.onlineHold):
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return nil
		}
	}
}

// ---- 绑定码 ----

func (s *Server) runQQBotCodeBind(ctx context.Context, session *qqBotBindSession, config map[string]any, env qqBotBindEnv) {
	defer session.cancel()
	appID := strings.TrimSpace(configString(config, "app_id"))
	secret := strings.TrimSpace(configString(config, "app_secret"))
	for {
		event, err := s.waitQQBotBindEvent(ctx, session, env, appID, secret)
		if err == nil {
			s.completeQQBotCodeBind(ctx, session, config, env, appID, secret, event)
			return
		}
		if ctx.Err() != nil {
			s.finishQQBotBindOnTimeout(session)
			return
		}
		// AppID/AppSecret 被拒绝时重试无意义，直接结束。
		if errors.Is(err, errProviderRejected) {
			s.failQQBotBind(session, redactQQBotError(err, config))
			return
		}
		s.updateQQBotBind(session, func(session *qqBotBindSession) {
			session.Status = qqBotBindConnecting
			session.Error = redactQQBotError(err, config)
		})
		s.logger.Warn("QQ bot gateway connection failed during binding", "error", redactQQBotError(err, config))
		select {
		case <-ctx.Done():
		case <-time.After(env.retryDelay):
		}
	}
}

// qqBotBindEvent 是包含绑定码的单聊消息事件。
type qqBotBindEvent struct {
	OpenID    string
	MessageID string
}

type qqBotGatewayFrame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// waitQQBotBindEvent 建立一次网关会话，直到收到包含绑定码的消息、会话结束或 ctx 取消。
func (s *Server) waitQQBotBindEvent(
	ctx context.Context,
	session *qqBotBindSession,
	env qqBotBindEnv,
	appID, secret string,
) (qqBotBindEvent, error) {
	token, gateway, err := connectQQBotGateway(ctx, env, appID, secret)
	if err != nil {
		return qqBotBindEvent{}, err
	}
	// coder/websocket 要求用 ctx 控制超时，不能设置 http.Client.Timeout。
	dialClient := *env.client
	dialClient.Timeout = 0
	dialCtx, cancelDial := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, gateway, &websocket.DialOptions{HTTPClient: &dialClient})
	cancelDial()
	if err != nil {
		return qqBotBindEvent{}, fmt.Errorf("QQ bot gateway connection failed: %w", unwrapURLError(err))
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	s.qqBotBindMu.Lock()
	code := session.Code
	s.qqBotBindMu.Unlock()

	var sequence atomic.Int64
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	write := func(value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return conn.Write(writeCtx, websocket.MessageText, payload)
	}
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return qqBotBindEvent{}, fmt.Errorf("QQ bot gateway closed: %w", err)
		}
		var frame qqBotGatewayFrame
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		if frame.S != nil {
			sequence.Store(*frame.S)
		}
		switch frame.Op {
		case 10: // Hello
			var hello struct {
				HeartbeatInterval int64 `json:"heartbeat_interval"`
			}
			_ = json.Unmarshal(frame.D, &hello)
			if err := write(map[string]any{"op": 2, "d": map[string]any{
				"token": "QQBot " + token, "intents": qqBotGatewayIntents, "shard": []int{0, 1},
			}}); err != nil {
				return qqBotBindEvent{}, err
			}
			if hello.HeartbeatInterval > 0 {
				interval := time.Duration(hello.HeartbeatInterval) * time.Millisecond
				go func() {
					ticker := time.NewTicker(interval)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-heartbeatDone:
							return
						case <-ticker.C:
							_ = write(map[string]any{"op": 1, "d": sequence.Load()})
						}
					}
				}()
			}
		case 7: // Reconnect
			return qqBotBindEvent{}, errors.New("QQ bot gateway requested a reconnect")
		case 9: // Invalid Session：通常是令牌无效或缺少事件权限。
			qqBotTokens.invalidate(env.endpoints.TokenURL, appID, token)
			return qqBotBindEvent{}, errors.New("QQ bot gateway rejected the session; check that the bot can receive direct messages")
		case 0: // Dispatch
			if frame.T == "READY" {
				s.updateQQBotBind(session, func(session *qqBotBindSession) {
					session.Status = qqBotBindWaiting
					session.Error = ""
				})
				continue
			}
			if event, ok := matchQQBotBindEvent(frame.T, frame.D, code); ok {
				return event, nil
			}
		}
	}
}

// errQQBotTokenRejected 表示令牌在缓存期内被平台提前作废。
var errQQBotTokenRejected = errors.New("QQ bot access token was rejected")

// connectQQBotGateway 取得令牌和网关地址；网关拒绝令牌时丢弃缓存并用新令牌重试一次，
// 与发送消息时的处理一致。
func connectQQBotGateway(ctx context.Context, env qqBotBindEnv, appID, secret string) (string, string, error) {
	for attempt := 0; ; attempt++ {
		token, err := qqBotTokens.get(ctx, env.client, env.endpoints.TokenURL, appID, secret)
		if err != nil {
			return "", "", err
		}
		gateway, err := fetchQQBotGateway(ctx, env, token)
		if errors.Is(err, errQQBotTokenRejected) && attempt == 0 {
			qqBotTokens.invalidate(env.endpoints.TokenURL, appID, token)
			continue
		}
		return token, gateway, err
	}
}

func fetchQQBotGateway(ctx context.Context, env qqBotBindEnv, token string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(env.endpoints.APIBase, "/")+"/gateway", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "QQBot "+token)
	request.Header.Set("User-Agent", "vocat-qqbot-notification/1")
	response, err := env.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("QQ bot gateway lookup failed: %w", unwrapURLError(err))
	}
	defer response.Body.Close()
	var reply struct {
		qqBotReply
		URL string `json:"url"`
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	decodeErr := json.Unmarshal(body, &reply)
	if response.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("%w: %w: QQ bot gateway lookup HTTP 401%s",
			errProviderRejected, errQQBotTokenRejected, reply.describe())
	}
	if decodeErr != nil || response.StatusCode != http.StatusOK || reply.URL == "" {
		return "", fmt.Errorf("%w: QQ bot gateway lookup HTTP %d%s", errProviderRejected, response.StatusCode, reply.describe())
	}
	return env.checkGateway(ctx, reply.URL)
}

// matchQQBotBindEvent 识别包含绑定码的单聊消息。
func matchQQBotBindEvent(kind string, raw json.RawMessage, code string) (qqBotBindEvent, bool) {
	if kind != "C2C_MESSAGE_CREATE" || code == "" {
		return qqBotBindEvent{}, false
	}
	var event struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Author  struct {
			UserOpenID string `json:"user_openid"`
		} `json:"author"`
	}
	if json.Unmarshal(raw, &event) != nil || !strings.Contains(strings.ToUpper(event.Content), code) ||
		!qqBotOpenIDPattern.MatchString(event.Author.UserOpenID) {
		return qqBotBindEvent{}, false
	}
	return qqBotBindEvent{OpenID: event.Author.UserOpenID, MessageID: event.ID}, true
}

func (s *Server) completeQQBotCodeBind(
	ctx context.Context,
	session *qqBotBindSession,
	config map[string]any,
	env qqBotBindEnv,
	appID, secret string,
	event qqBotBindEvent,
) {
	committed, err := s.commitQQBotBind(ctx, session, config, event.OpenID)
	if err != nil {
		s.logger.Error("save QQ bot binding", "error", err)
		s.failQQBotBind(session, "the binding could not be saved")
		return
	}
	if !committed {
		return
	}
	s.logger.Info("QQ bot notification recipient bound", "method", "code")
	// 被动回复带 msg_id，不占用主动消息额度；失败不影响绑定结果。
	if err := replyQQBotBind(ctx, env, appID, secret, event); err != nil {
		s.logger.Warn("QQ bot binding confirmation reply failed", "error", err)
	}
}

// saveQQBotBinding 把凭据和接收方一起写入已保存的配置并启用通道，保留其他字段（如设备范围）。
func (s *Server) saveQQBotBinding(ctx context.Context, config map[string]any, openID string) error {
	stored := map[string]any{}
	current, err := s.store.NotificationSetting(ctx, "qqbot")
	switch {
	case err == nil:
		if err := json.Unmarshal(current.Config, &stored); err != nil {
			return fmt.Errorf("decode stored QQ bot config: %w", err)
		}
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	for _, key := range []string{"app_id", "app_secret", "proxy"} {
		if value, ok := config[key]; ok {
			stored[key] = value
		}
	}
	stored["target_openid"] = openID
	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	return s.store.UpsertNotificationSetting(ctx, store.NotificationSetting{
		Channel:         "qqbot",
		Enabled:         true,
		Config:          encoded,
		SensitiveFields: store.DefaultNotificationSensitiveFields("qqbot"),
	})
}

func replyQQBotBind(ctx context.Context, env qqBotBindEnv, appID, secret string, event qqBotBindEvent) error {
	token, err := qqBotTokens.get(ctx, env.client, env.endpoints.TokenURL, appID, secret)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"content": qqBotBindReply, "msg_type": 0, "msg_id": event.MessageID, "msg_seq": 1,
	})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(env.endpoints.APIBase, "/") + "/v2/users/" + url.PathEscape(event.OpenID) + "/messages"
	status, reply, err := postQQBotMessage(ctx, env.client, endpoint, token, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 || reply.Code != 0 {
		return fmt.Errorf("%w: QQ bot HTTP %d%s", errProviderRejected, status, reply.describe())
	}
	return nil
}
