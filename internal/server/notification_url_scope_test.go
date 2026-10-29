package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// storedConfig decodes JSON the same way a saved notification config is read
// back from the store (arrays as []any, objects as map[string]any).
func storedConfig(t *testing.T, raw string) map[string]any {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestNotificationDestinationsHonorURLScopes(t *testing.T) {
	const a, b, c = "https://api.day.app/KeyA/", "https://api.day.app/KeyB/", "https://api.day.app/KeyC/"
	for _, test := range []struct {
		name   string
		config string
		device string
		want   []string
	}{
		{"no url_scopes keeps every URL", `{"urls":["` + a + `","` + b + `"]}`, "reader-1", []string{a, b}},
		{"scoped URL only gets its device", `{"urls":["` + a + `","` + b + `"],"url_scopes":[{"url":"` + a + `","device_ids":["reader-1"]},{"url":"` + b + `","device_ids":["reader-2"]}]}`, "reader-1", []string{a}},
		{"second device gets the second URL", `{"urls":["` + a + `","` + b + `"],"url_scopes":[{"url":"` + a + `","device_ids":["reader-1"]},{"url":"` + b + `","device_ids":["reader-2"]}]}`, "reader-2", []string{b}},
		{"unscoped device gets nothing from scoped URLs", `{"urls":["` + a + `","` + b + `"],"url_scopes":[{"url":"` + a + `","device_ids":["reader-1"]},{"url":"` + b + `","device_ids":["reader-2"]}]}`, "modem-3", nil},
		{"URLs without a scope follow the channel", `{"urls":["` + a + `","` + c + `"],"url_scopes":[{"url":"` + a + `","device_ids":["reader-1"]}]}`, "modem-3", []string{c}},
		{"empty device list mutes the URL", `{"urls":["` + a + `","` + c + `"],"url_scopes":[{"url":"` + a + `","device_ids":[]}]}`, "reader-1", []string{c}},
		{"channel scope applies first", `{"device_ids":["reader-2"],"urls":["` + a + `"],"url_scopes":[{"url":"` + a + `","device_ids":["reader-1"]}]}`, "reader-1", nil},
		{"channel and URL scope both allow", `{"device_ids":["reader-1"],"urls":["` + a + `"],"url_scopes":[{"url":"` + a + `","device_ids":["reader-1"]}]}`, "reader-1", []string{a}},
	} {
		got := notificationDestinations(storedConfig(t, test.config), test.device)
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
}

func TestBarkURLScopesAPI(t *testing.T) {
	const a, b = "https://api.day.app/KeyA/", "https://api.day.app/KeyB/"
	api := newSettingsAPITest(t)

	valid := `{"bark":{"enabled":false,"urls":["` + a + `","` + b + `"],"url_scopes":[{"url":"` + a + `","device_ids":["reader-1"]},{"url":"` + b + `","device_ids":["reader-2"]}]}}`
	response := api.request(t, http.MethodPut, "/api/settings/notifications", valid)
	if response.Code != http.StatusOK {
		t.Fatalf("valid url_scopes: status=%d body=%s", response.Code, response.Body)
	}
	bark := decodeSettingsResponse(t, api.request(t, http.MethodGet, "/api/settings/notifications", ""))["data"].(map[string]any)["bark"].(map[string]any)
	scopes, _ := json.Marshal(bark["url_scopes"])
	if string(scopes) != `[{"device_ids":["reader-1"],"url":"`+a+`"},{"device_ids":["reader-2"],"url":"`+b+`"}]` {
		t.Fatalf("url_scopes did not round-trip: %s", scopes)
	}

	for name, scopes := range map[string]string{
		"null":               `null`,
		"not an array":       `{}`,
		"URL not in urls":    `[{"url":"https://api.day.app/Other/","device_ids":["reader-1"]}]`,
		"duplicate URL":      `[{"url":"` + a + `","device_ids":["reader-1"]},{"url":"` + a + `","device_ids":["reader-2"]}]`,
		"unknown field":      `[{"url":"` + a + `","device_ids":["reader-1"],"extra":1}]`,
		"null device_ids":    `[{"url":"` + a + `","device_ids":null}]`,
		"missing device_ids": `[{"url":"` + a + `"}]`,
		"empty device ID":    `[{"url":"` + a + `","device_ids":[" "]}]`,
		"invalid URL":        `[{"url":"ftp://example.com","device_ids":["reader-1"]}]`,
	} {
		body := `{"bark":{"enabled":false,"urls":["` + a + `","` + b + `"],"url_scopes":` + scopes + `}}`
		if response := api.request(t, http.MethodPut, "/api/settings/notifications", body); response.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400 (body=%s)", name, response.Code, response.Body)
		}
	}

	// A test notification goes to every draft URL: saved scopes must not block a draft that removed a URL.
	resolved, _, err := api.server.resolveNotificationTestConfig(context.Background(), "bark", json.RawMessage(`{"urls":["`+a+`"]}`))
	if err != nil {
		t.Fatalf("Bark test with a removed scoped URL: %v", err)
	}
	if _, present := resolved["url_scopes"]; present {
		t.Errorf("Bark test config kept url_scopes: %v", resolved["url_scopes"])
	}

	// url_scopes is a Bark field only; other channels still reject it.
	if response := api.request(t, http.MethodPut, "/api/settings/notifications",
		`{"webhook":{"enabled":false,"urls":["`+a+`"],"url_scopes":[]}}`); response.Code != http.StatusBadRequest {
		t.Errorf("webhook.url_scopes: status=%d, want 400", response.Code)
	}
}
