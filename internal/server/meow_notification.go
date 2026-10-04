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
	"strings"
	"time"
)

func validateMeowNotificationConfig(config map[string]any) error {
	nickname := strings.TrimSpace(configString(config, "nickname"))
	if nickname == "" {
		return errors.New("meow.nickname is required")
	}
	if strings.ContainsAny(nickname, "/\\") || nickname == "." || nickname == ".." {
		return errors.New("meow.nickname must be a single path segment")
	}
	return nil
}

// 默认调用原有 MeoW 发送函数；测试中可替换为本地接收端，不访问真实收件人。
var meowNotificationSender = sendMeowNotification

func sendMeowNotification(ctx context.Context, config map[string]any, title, text string) error {
	if err := validateMeowNotificationConfig(config); err != nil {
		return err
	}
	endpoint := "https://api.chuckfang.com/" + url.PathEscape(strings.TrimSpace(configString(config, "nickname"))) + "?msgType=text"
	parsed, err := validateOutboundURL(ctx, endpoint, true)
	if err != nil {
		return err
	}
	client, err := restrictedHTTPClient(ctx, 10*time.Second, "")
	if err != nil {
		return err
	}
	return postMeowNotification(ctx, client, parsed.String(), title, text, config)
}

func postMeowNotification(ctx context.Context, client *http.Client, endpoint, title, text string, config map[string]any) error {
	body := map[string]string{"title": title, "msg": text}
	if link := strings.TrimSpace(configString(config, "url")); link != "" {
		body["url"] = link
	}
	// VoCat 配置使用 snake_case，MeoW 接口要求 imgUrl。
	if image := strings.TrimSpace(configString(config, "img_url")); image != "" {
		body["imgUrl"] = image
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "vocat-meow-notification/1")
	response, err := client.Do(request)
	if err != nil {
		// URL 包含收件昵称，不将请求地址带入错误日志。
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("MeoW request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", errProviderRejected, response.StatusCode)
	}
	var reply struct {
		Status int `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&reply); err != nil {
		return fmt.Errorf("invalid MeoW response: %w", err)
	}
	if reply.Status != http.StatusOK {
		return fmt.Errorf("%w: MeoW status %d", errProviderRejected, reply.Status)
	}
	return nil
}
