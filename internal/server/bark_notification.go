package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const barkEncryptionKeyField = "encryption_key"

var errInvalidBarkEncryptionKey = errors.New("bark.encryption_key must be 16, 24, or 32 printable ASCII characters")

func validateBarkEncryptionKey(key string) error {
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return errInvalidBarkEncryptionKey
	}
	for index := range len(key) {
		if key[index] < ' ' || key[index] > '~' {
			return errInvalidBarkEncryptionKey
		}
	}
	return nil
}

func barkNotificationCipher(config map[string]any) (cipher.Block, error) {
	raw, present := config[barkEncryptionKeyField]
	if !present {
		return nil, nil
	}
	key, ok := raw.(string)
	if !ok {
		return nil, errInvalidBarkEncryptionKey
	}
	if key == "" {
		return nil, nil
	}
	if err := validateBarkEncryptionKey(key); err != nil {
		return nil, err
	}
	return aes.NewCipher([]byte(key))
}

func encryptBarkNotification(block cipher.Block, plaintext []byte) ([]byte, error) {
	// Bark interprets the supplied IV as UTF-8 bytes. Encode 96 random bits
	// as 16 ASCII characters, then use those characters directly as the IV.
	var randomIV [12]byte
	if _, err := rand.Read(randomIV[:]); err != nil {
		return nil, fmt.Errorf("generate Bark notification IV: %w", err)
	}
	iv := base64.RawURLEncoding.EncodeToString(randomIV[:])
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	ciphertext := append(bytes.Clone(plaintext), bytes.Repeat([]byte{byte(padding)}, padding)...)
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(ciphertext, ciphertext)
	return json.Marshal(map[string]string{
		"ciphertext": base64.StdEncoding.EncodeToString(ciphertext),
		"iv":         iv,
	})
}

func sendBarkNotification(ctx context.Context, config map[string]any, destinations []string, title, body, userAgent string, timeout time.Duration) error {
	if len(destinations) == 0 {
		return nil
	}
	block, err := barkNotificationCipher(config)
	if err != nil {
		return err
	}
	client, err := restrictedHTTPClient(ctx, timeout, "")
	if err != nil {
		return err
	}
	message := map[string]string{"title": title, "body": body}
	for _, field := range []string{"group", "icon", "level"} {
		if value := configString(config, field); value != "" {
			message[field] = value
		}
	}
	plaintext, err := json.Marshal(message)
	if err != nil {
		return err
	}
	for _, destination := range destinations {
		parsed, err := validateOutboundURL(ctx, destination, false)
		if err != nil {
			return err
		}
		payload := plaintext
		if block != nil {
			// Bark's server applies query values after JSON fields. Keep the
			// configured URL from overriding this request's encryption data.
			query, err := url.ParseQuery(parsed.RawQuery)
			if err != nil {
				return errors.New("encrypted Bark notification URL contains an invalid query")
			}
			for name := range query {
				switch strings.ToLower(name) {
				case "iv", "ciphertext":
					return errors.New("encrypted Bark notification URLs cannot contain iv or ciphertext query parameters")
				}
			}
			payload, err = encryptBarkNotification(block, plaintext)
			if err != nil {
				return err
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create Bark notification request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		request.Header.Set("User-Agent", userAgent)
		if err := performNotificationRequest(client, request, false); err != nil {
			return err
		}
	}
	return nil
}
