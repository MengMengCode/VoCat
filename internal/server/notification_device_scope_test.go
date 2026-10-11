package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestNotificationDeviceScopeAPI(t *testing.T) {
	api := newSettingsAPITest(t)
	for _, channel := range notificationChannels {
		for _, item := range []struct {
			scope  string
			status int
		}{
			{`null`, http.StatusOK}, {`[]`, http.StatusOK}, {`["offline-device"]`, http.StatusOK},
			{`"all"`, http.StatusBadRequest}, {`[1]`, http.StatusBadRequest}, {`[""]`, http.StatusBadRequest},
		} {
			body := fmt.Sprintf(`{%q:{"enabled":false,"device_ids":%s}}`, channel, item.scope)
			response := api.request(t, http.MethodPut, "/api/settings/notifications", body)
			if response.Code != item.status {
				t.Fatalf("%s scope=%s: status=%d, body=%s", channel, item.scope, response.Code, response.Body)
			}
			if item.status == http.StatusOK {
				config := decodeSettingsResponse(t, response)["data"].(map[string]any)[channel].(map[string]any)
				got, _ := json.Marshal(config["device_ids"])
				if string(got) != item.scope {
					t.Fatalf("%s scope=%s, want %s", channel, got, item.scope)
				}
			}
		}
	}
}
