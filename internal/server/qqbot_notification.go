package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/store"
)

// QQ 官方机器人开放平台：先用 AppID/AppSecret 换取 access_token，再调用
// 单聊消息接口。主动消息受平台策略限制，被拒绝时按普通发送失败处理。
const (
	qqBotTokenURL    = "https://bots.qq.com/app/getAppAccessToken"
	qqBotAPIBase     = "https://api.sgroup.qq.com"
	qqBotConnectBase = "https://q.qq.com"
	// 令牌到期前提前刷新，避免请求途中过期。
	qqBotTokenRefreshMargin = 60 * time.Second
	maxQQBotContentRunes    = 2000
)

var (
	qqBotAppIDPattern  = regexp.MustCompile(`^[0-9]{1,20}$`)
	qqBotOpenIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// validateQQBotNotificationConfig 检查发送所需的 AppID、AppSecret 和接收方 openid。
func validateQQBotNotificationConfig(config map[string]any) error {
	if !qqBotAppIDPattern.MatchString(strings.TrimSpace(configString(config, "app_id"))) {
		return errors.New("qqbot.app_id must be the numeric AppID")
	}
	if secret := strings.TrimSpace(configString(config, "app_secret")); secret == "" || secret == store.SecretMask {
		return errors.New("qqbot.app_secret is required")
	}
	if !qqBotOpenIDPattern.MatchString(strings.TrimSpace(configString(config, "target_openid"))) {
		return errors.New("qqbot.target_openid must be a user openid; bind a recipient first")
	}
	return nil
}

// qqBotEndpoints 描述一次发送使用的地址；测试中替换为本地接收端。
type qqBotEndpoints struct {
	TokenURL string
	APIBase  string
	// ConnectBase 是扫码绑定接口所在的开放平台地址。
	ConnectBase string
}

var defaultQQBotEndpoints = qqBotEndpoints{TokenURL: qqBotTokenURL, APIBase: qqBotAPIBase, ConnectBase: qqBotConnectBase}

// errQQBotSecretRequired：AppSecret 回传掩码会沿用已保存的值，它属于原来的 AppID。
var errQQBotSecretRequired = errors.New("qqbot.app_secret must be re-entered when qqbot.app_id changes")

// checkQQBotSecretReentry 在草稿修改了 AppID 却没有填写新 AppSecret 时返回 errQQBotSecretRequired，
// 避免把新 AppID 与旧机器人的 AppSecret 组合在一起。
func (s *Server) checkQQBotSecretReentry(ctx context.Context, draft json.RawMessage) error {
	var config map[string]any
	if err := json.Unmarshal(draft, &config); err != nil {
		return err
	}
	if secret := strings.TrimSpace(configString(config, "app_secret")); secret != "" && secret != store.SecretMask {
		return nil
	}
	appID := strings.TrimSpace(configString(config, "app_id"))
	if appID == "" {
		return nil
	}
	current, err := s.store.NotificationSetting(ctx, "qqbot")
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var stored map[string]any
	if err := json.Unmarshal(current.Config, &stored); err != nil {
		return fmt.Errorf("decode stored QQ bot config: %w", err)
	}
	if storedID := strings.TrimSpace(configString(stored, "app_id")); storedID != "" && storedID != appID {
		return errQQBotSecretRequired
	}
	return nil
}

// qqBotSecretReentered 执行 checkQQBotSecretReentry 并写出错误响应；返回是否可以继续。
func (s *Server) qqBotSecretReentered(w http.ResponseWriter, r *http.Request, draft json.RawMessage) bool {
	err := s.checkQQBotSecretReentry(r.Context(), draft)
	switch {
	case err == nil:
		return true
	case errors.Is(err, errQQBotSecretRequired):
		writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
	default:
		s.writeStoreError(w, err)
	}
	return false
}

// 默认调用真实开放平台；测试中可替换为本地接收端，不访问真实收件人。
var qqBotNotificationSender = deliverQQBotNotification

// sendQQBotNotification 按设备范围过滤后发送一条 QQ 机器人通知。
func sendQQBotNotification(ctx context.Context, config map[string]any, deviceID *string, title, text string) error {
	if deviceID != nil && !notificationMatchesDevice(configStrings(config, "device_ids"), *deviceID) {
		return nil
	}
	return qqBotNotificationSender(ctx, config, title, text)
}

// deliverQQBotNotification 校验配置和目标地址，通过受限的 HTTP 客户端发送通知。
func deliverQQBotNotification(ctx context.Context, config map[string]any, title, text string) error {
	if err := validateQQBotNotificationConfig(config); err != nil {
		return err
	}
	endpoints := defaultQQBotEndpoints
	for _, endpoint := range []string{endpoints.TokenURL, endpoints.APIBase} {
		if _, err := validateOutboundURL(ctx, endpoint, true); err != nil {
			return err
		}
	}
	client, err := restrictedHTTPClient(ctx, 10*time.Second, configString(config, "proxy"))
	if err != nil {
		return err
	}
	return postQQBotNotification(ctx, client, endpoints, config, title, text)
}

// postQQBotNotification 用单聊消息接口把通知发给接收方；令牌被拒绝时换新令牌重试一次。
func postQQBotNotification(
	ctx context.Context,
	client *http.Client,
	endpoints qqBotEndpoints,
	config map[string]any,
	title, text string,
) error {
	if err := validateQQBotNotificationConfig(config); err != nil {
		return err
	}
	appID := strings.TrimSpace(configString(config, "app_id"))
	secret := strings.TrimSpace(configString(config, "app_secret"))
	endpoint := strings.TrimRight(endpoints.APIBase, "/") + "/v2/users/" +
		url.PathEscape(strings.TrimSpace(configString(config, "target_openid"))) + "/messages"
	payload, err := json.Marshal(map[string]any{
		"content":  qqBotContent(title, text),
		"msg_type": 0,
	})
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		token, err := qqBotTokens.get(ctx, client, endpoints.TokenURL, appID, secret)
		if err != nil {
			return err
		}
		status, reply, err := postQQBotMessage(ctx, client, endpoint, token, payload)
		if err != nil {
			return err
		}
		// 令牌可能在缓存期内被平台提前作废：丢弃后重试一次。
		if status == http.StatusUnauthorized && attempt == 0 {
			qqBotTokens.invalidate(endpoints.TokenURL, appID, token)
			continue
		}
		if status < 200 || status >= 300 || reply.Code != 0 {
			return fmt.Errorf("%w: QQ bot HTTP %d%s", errProviderRejected, status,
				reply.describe(configString(config, "target_openid")))
		}
		return nil
	}
}

// qqBotContent 把标题和正文合并为纯文本消息，并限制长度。
func qqBotContent(title, text string) string {
	content := strings.TrimSpace(text)
	if title = strings.TrimSpace(title); title != "" {
		content = title + "\n" + content
	}
	runes := []rune(content)
	if len(runes) > maxQQBotContentRunes {
		content = string(runes[:maxQQBotContentRunes-1]) + "…"
	}
	return content
}

type qqBotReply struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// qqBotOpaqueValue 匹配平台说明中可能出现的 openid、令牌等长标识。
var qqBotOpaqueValue = regexp.MustCompile(`[A-Za-z0-9_-]{20,}`)

// describe 只返回平台的错误码和说明。hidden 中的值（如收件 openid，可能较短）以及
// 说明中的长标识（openid、令牌等）会被隐去。
func (reply qqBotReply) describe(hidden ...string) string {
	if reply.Code == 0 && reply.Message == "" {
		return ""
	}
	redacted := strings.TrimSpace(reply.Message)
	for _, value := range hidden {
		if value = strings.TrimSpace(value); value != "" {
			redacted = strings.ReplaceAll(redacted, value, "[redacted]")
		}
	}
	redacted = qqBotOpaqueValue.ReplaceAllString(redacted, "[redacted]")
	message := []rune(redacted)
	if len(message) > 200 {
		message = message[:200]
	}
	return fmt.Sprintf(" (code %d: %s)", reply.Code, string(message))
}

// postQQBotMessage 发送一次消息请求，返回 HTTP 状态和平台的错误码说明。
func postQQBotMessage(
	ctx context.Context,
	client *http.Client,
	endpoint, token string,
	payload []byte,
) (int, qqBotReply, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, qqBotReply{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "QQBot "+token)
	request.Header.Set("User-Agent", "vocat-qqbot-notification/1")
	response, err := client.Do(request)
	if err != nil {
		return 0, qqBotReply{}, fmt.Errorf("QQ bot request failed: %w", unwrapURLError(err))
	}
	defer response.Body.Close()
	var reply qqBotReply
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, qqBotReply{}, fmt.Errorf("QQ bot response could not be read: %w", err)
	}
	// 错误响应不一定是 JSON，只在成功状态下要求响应可解析，避免把异常响应当作已送达。
	if err := json.Unmarshal(body, &reply); err != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		return 0, qqBotReply{}, fmt.Errorf("%w: QQ bot HTTP %d returned an unreadable response", errProviderRejected, response.StatusCode)
	}
	return response.StatusCode, reply, nil
}

// unwrapURLError 去掉 url.Error 中的请求地址：地址包含收件 openid。
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// qqBotTokenCache 按令牌地址和 AppID 缓存 access_token。
type qqBotTokenCache struct {
	mu      sync.Mutex
	entries map[string]qqBotToken
	now     func() time.Time
}

type qqBotToken struct {
	value   string
	expires time.Time
	secret  string
}

var qqBotTokens = &qqBotTokenCache{entries: map[string]qqBotToken{}, now: time.Now}

// get 返回缓存中未过期的令牌，否则向开放平台获取新令牌。
func (cache *qqBotTokenCache) get(
	ctx context.Context,
	client *http.Client,
	tokenURL, appID, secret string,
) (string, error) {
	key := tokenURL + "\x00" + appID
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry, ok := cache.entries[key]; ok && entry.secret == secret && cache.now().Before(entry.expires) {
		return entry.value, nil
	}
	token, lifetime, err := fetchQQBotToken(ctx, client, tokenURL, appID, secret)
	if err != nil {
		delete(cache.entries, key)
		return "", err
	}
	cache.entries[key] = qqBotToken{
		value:   token,
		expires: cache.now().Add(lifetime - qqBotTokenRefreshMargin),
		secret:  secret,
	}
	return token, nil
}

// invalidate 丢弃被平台拒绝的令牌，下次发送时重新获取。
func (cache *qqBotTokenCache) invalidate(tokenURL, appID, token string) {
	key := tokenURL + "\x00" + appID
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry, ok := cache.entries[key]; ok && entry.value == token {
		delete(cache.entries, key)
	}
}

// fetchQQBotToken 用 AppID 和 AppSecret 换取访问令牌及其有效期。
func fetchQQBotToken(
	ctx context.Context,
	client *http.Client,
	tokenURL, appID, secret string,
) (string, time.Duration, error) {
	payload, err := json.Marshal(map[string]string{"appId": appID, "clientSecret": secret})
	if err != nil {
		return "", 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "vocat-qqbot-notification/1")
	response, err := client.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("QQ bot token request failed: %w", unwrapURLError(err))
	}
	defer response.Body.Close()
	var reply struct {
		qqBotReply
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if json.Unmarshal(body, &reply) != nil || response.StatusCode < 200 || response.StatusCode >= 300 ||
		reply.Code != 0 || reply.AccessToken == "" {
		return "", 0, fmt.Errorf("%w: QQ bot rejected AppID %s credentials (HTTP %d%s)",
			errProviderRejected, appID, response.StatusCode, reply.describe())
	}
	// 平台以字符串返回 expires_in，兼容数字形式。
	seconds, err := strconv.Atoi(strings.Trim(string(reply.ExpiresIn), `" `))
	if err != nil || seconds <= 0 {
		seconds = 7200
	}
	lifetime := time.Duration(seconds) * time.Second
	if lifetime <= qqBotTokenRefreshMargin {
		lifetime = qqBotTokenRefreshMargin + time.Second
	}
	return reply.AccessToken, lifetime, nil
}
