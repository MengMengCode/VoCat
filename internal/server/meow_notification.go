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
	"unicode"
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
	return postMeowNotification(ctx, client, parsed.String(), title, meowMessageBody(title, text), config)
}

// meowMessageBody drops the leading title line from a notification body. MeoW
// renders the title field inside the notification content, so a body that
// repeats the title shows it twice on the device. The title field itself is
// still sent, so no information is lost. Every MeoW push goes through here,
// which keeps future notification kinds from reintroducing the duplicate.
func meowMessageBody(title, text string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		// A single-line body cannot carry the title separately.
		return text
	}
	if !meowTitleLine(lines[0], title) {
		return text
	}
	return strings.Join(lines[1:], "\n")
}

// meowTitleLine reports whether a body line only repeats the title. Some
// notifications prefix the title with an emoji, for example "📞 收到来电", so a
// leading run of non-alphanumeric runes is ignored. Everything after that must
// equal the title exactly: a line that merely mentions the title inside other
// text carries real content and has to be kept.
func meowTitleLine(line, title string) bool {
	head := strings.TrimSpace(line)
	return head == title || strings.TrimLeftFunc(head, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) == title
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
