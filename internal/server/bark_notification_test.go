package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/store"
)

func TestBarkEncryptedEnvelopeMatchesClientCBC(t *testing.T) {
	plaintext := []byte(`{"title":"新短信","body":"Code 123456","group":"SMS","icon":"https://example.com/icon.png","level":"timeSensitive"}`)
	for _, key := range []string{" 0123456789abcd ", "123456789012345678901234", "12345678901234567890123456789012"} {
		config := map[string]any{barkEncryptionKeyField: key}
		block, err := barkNotificationCipher(config)
		if err != nil {
			t.Fatal(err)
		}
		clientBlock, err := aes.NewCipher([]byte(key))
		if err != nil {
			t.Fatal(err)
		}
		previousIV := ""
		for range 2 {
			payload, err := encryptBarkNotification(block, plaintext)
			if err != nil {
				t.Fatal(err)
			}
			var envelope map[string]string
			if err := json.Unmarshal(payload, &envelope); err != nil || len(envelope) != 2 {
				t.Fatalf("Bark relay must only receive ciphertext and iv: %s (%v)", payload, err)
			}
			iv := envelope["iv"]
			if len(iv) != aes.BlockSize || iv == previousIV {
				t.Fatalf("Bark expected a fresh literal 16-byte IV, got %q", iv)
			}
			if encoded, err := base64.RawURLEncoding.DecodeString(iv); err != nil || len(encoded) != 12 {
				t.Fatalf("IV was not encoded as 16 ASCII characters: %q", iv)
			}
			previousIV = iv
			ciphertext, err := base64.StdEncoding.DecodeString(envelope["ciphertext"])
			wantLength := (len(plaintext)/aes.BlockSize + 1) * aes.BlockSize
			if err != nil || len(ciphertext) != wantLength {
				t.Fatalf("ciphertext bytes = %d, want %d (IV is separate), err = %v", len(ciphertext), wantLength, err)
			}
			cipher.NewCBCDecrypter(clientBlock, []byte(iv)).CryptBlocks(ciphertext, ciphertext)
			padding := wantLength - len(plaintext)
			if !bytes.Equal(ciphertext[:len(plaintext)], plaintext) || !bytes.Equal(ciphertext[len(plaintext):], bytes.Repeat([]byte{byte(padding)}, padding)) {
				t.Fatal("Bark CBC decryption did not reproduce the original JSON with PKCS#7 padding")
			}
		}
	}
}

func TestBarkSettingsMaskPreserveReplaceAndClearEncryptionKey(t *testing.T) {
	api := newSettingsAPITest(t)
	const firstKey, replacementKey = " 0123456789abcd ", "abcdefghijklmnopqrstuvwx"
	const urls = `"urls":["https://bark.example.com/KeyA"],"url_scopes":[{"url":"https://bark.example.com/KeyA","device_ids":["reader-1"]}]`
	for _, step := range []struct {
		name   string
		fields string
		want   string
	}{
		{"set", `,"encryption_key":` + strconvJSON(firstKey), firstKey},
		{"preserve masked", `,"encryption_key":"********"`, firstKey},
		{"preserve omitted", ``, firstKey},
		{"preserve empty", `,"encryption_key":""`, firstKey},
		{"replace", `,"encryption_key":` + strconvJSON(replacementKey), replacementKey},
		{"clear masked", `,"encryption_key":"********","clear_secrets":true`, ""},
		{"clear and replace", `,"encryption_key":` + strconvJSON(firstKey) + `,"clear_secrets":true`, firstKey},
		{"clear empty", `,"encryption_key":"","clear_secrets":true`, ""},
	} {
		t.Run(step.name, func(t *testing.T) {
			response := api.request(t, http.MethodPut, "/api/settings/notifications",
				`{"bark":{"enabled":true,"group":"SMS",`+urls+step.fields+`}}`)
			if response.Code != http.StatusOK {
				t.Fatalf("save status = %d, response = %s", response.Code, response.Body)
			}
			for _, readback := range []string{response.Body.String(), api.request(t, http.MethodGet, "/api/settings/notifications", "").Body.String()} {
				if strings.Contains(readback, firstKey) || strings.Contains(readback, replacementKey) {
					t.Fatal("notification settings exposed a Bark encryption key")
				}
				document := storedConfig(t, readback)["data"].(map[string]any)["bark"].(map[string]any)
				if key, _ := document[barkEncryptionKeyField].(string); (step.want != "" && key != store.SecretMask) || (step.want == "" && key != "") {
					t.Fatal("readback did not reflect the key's configured state")
				}
			}
			stored, err := api.database.NotificationSetting(context.Background(), "bark")
			if err != nil {
				t.Fatal(err)
			}
			config := storedConfig(t, string(stored.Config))
			if key, _ := config[barkEncryptionKeyField].(string); key != step.want {
				t.Fatal("saved Bark key changed unexpectedly")
			}
			if !stored.Enabled || config["group"] != "SMS" || len(notificationDestinations(config, "reader-1")) != 1 || len(notificationDestinations(config, "reader-2")) != 0 {
				t.Fatal("Bark key operation changed notification settings or URL scope")
			}
			if _, ok := config["clear_secrets"]; ok {
				t.Fatal("a clear command was persisted in the Bark channel config")
			}
		})
	}
}

func TestBarkNotificationTestAppliesDraftKeyChanges(t *testing.T) {
	api := newSettingsAPITest(t)
	const key, replacement = "1234567823456789", "abcdefghijklmnopqrstuvwx"
	ctx := context.Background()
	if err := api.database.UpsertNotificationSetting(ctx, store.NotificationSetting{
		Channel: "bark", Enabled: true,
		Config: json.RawMessage(`{"encryption_key":"` + key + `","urls":["https://bark.example.com/key"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		draft string
		want  string
	}{
		{`{}`, key},
		{`{"encryption_key":"********"}`, key},
		{`{"encryption_key":"` + replacement + `"}`, replacement},
		{`{"encryption_key":"********","clear_secrets":true}`, ""},
		{`{"encryption_key":"","clear_secrets":true}`, ""},
		{`{"encryption_key":"` + replacement + `","clear_secrets":true}`, replacement},
	} {
		_, parsed, err := decodeNotificationConfig("bark", json.RawMessage(test.draft), false)
		if err != nil {
			t.Fatal(err)
		}
		settings, provider, err := api.server.resolveNotificationTestConfig(ctx, "bark", parsed)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := settings[barkEncryptionKeyField].(string); got != test.want {
			t.Fatal("Bark test did not use the requested draft key")
		}
		if _, err := barkNotificationCipher(settings); err != nil {
			t.Fatal(err)
		}
		if test.want != "" && store.RedactText(test.want, provider) != store.SecretMask {
			t.Fatal("Bark test key was missing from error redaction")
		}
	}
	stored, err := api.database.NotificationSetting(ctx, "bark")
	if err != nil || storedConfig(t, string(stored.Config))[barkEncryptionKeyField] != key {
		t.Fatal("test draft changed persisted config")
	}
}

func TestBarkLegacyPlaintextNotificationDraft(t *testing.T) {
	api := newSettingsAPITest(t)
	if err := api.database.UpsertNotificationSetting(context.Background(), store.NotificationSetting{
		Channel: "bark", Enabled: true,
		Config: json.RawMessage(`{"urls":["http://127.0.0.1/fixture"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, draft := range []string{`{"encryption_key":""}`, `{"encryption_key":"","clear_secrets":true}`} {
		response := api.request(t, http.MethodPost, "/api/settings/notifications/bark/test", draft)
		// Both plaintext drafts must pass config validation and reach the
		// usual destination guard without making a network request.
		if response.Code != http.StatusBadRequest || decodeSettingsResponse(t, response)["error"].(map[string]any)["code"] != "unsafe_destination" {
			t.Fatalf("Bark plaintext draft did not reach destination validation: %d %s", response.Code, response.Body)
		}
	}
}

func TestBarkInvalidEncryptionKeyFailsClosed(t *testing.T) {
	api := newSettingsAPITest(t)
	for _, value := range []string{`null`, `42`, `"short"`, `"12345678901234é"`, `"123456789012345\n"`} {
		response := api.request(t, http.MethodPut, "/api/settings/notifications", `{"bark":{"enabled":true,"encryption_key":`+value+`}}`)
		if response.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid Bark key type or format %s (status %d)", value, response.Code)
		}
	}
	ctx := context.Background()
	config := map[string]any{"urls": []string{"http://127.0.0.1/fixture"}, barkEncryptionKeyField: "invalid"}
	for _, err := range []error{
		sendBarkSMSNotification(ctx, config, smsNotification{DeviceID: "reader-1"}),
		sendBarkTextNotification(ctx, config, "reader-1", "title", "text"),
		sendBarkNotificationTest(ctx, config),
	} {
		if !errors.Is(err, errInvalidBarkEncryptionKey) {
			t.Fatalf("Bark sender did not fail closed on a saved invalid key: %v", err)
		}
	}
}

func TestBarkMaskedKeyRequiresSavedKey(t *testing.T) {
	api := newSettingsAPITest(t)
	for _, name := range []string{"new channel", "existing key cleared"} {
		t.Run(name, func(t *testing.T) {
			if name == "existing key cleared" {
				set := api.request(t, http.MethodPut, "/api/settings/notifications", `{"bark":{"enabled":true,"encryption_key":"1234567890123456"}}`)
				clear := api.request(t, http.MethodPut, "/api/settings/notifications", `{"bark":{"enabled":true,"encryption_key":"","clear_secrets":true}}`)
				if set.Code != http.StatusOK || clear.Code != http.StatusOK {
					t.Fatal("could not set and explicitly clear a Bark key")
				}
			}
			for _, response := range []*httptest.ResponseRecorder{
				api.request(t, http.MethodPut, "/api/settings/notifications", `{"bark":{"enabled":true,"encryption_key":"********"}}`),
				api.request(t, http.MethodPost, "/api/settings/notifications/bark/test", `{"encryption_key":"********","urls":["http://127.0.0.1/fixture"]}`),
			} {
				if response.Code != http.StatusBadRequest || decodeSettingsResponse(t, response)["error"].(map[string]any)["code"] != "invalid_notification_config" {
					t.Fatalf("accepted a masked key without a saved value: %d %s", response.Code, response.Body)
				}
			}
		})
	}
}
