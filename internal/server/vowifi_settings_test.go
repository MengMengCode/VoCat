package server

import (
	"net/http"
	"testing"
)

// TestVoWiFiSettingsExposeAndPersistIMSAPN verifies the dedicated IMS APN
// control introduced for issue #147: the VoWiFi tunnel APN is configured
// independently of the cellular data APN and defaults to "ims".
func TestVoWiFiSettingsExposeAndPersistIMSAPN(t *testing.T) {
	test := newSettingsAPITest(t)

	recorder := test.request(t, http.MethodGet, "/api/settings/vowifi", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", recorder.Code, recorder.Body)
	}
	data, _ := decodeSettingsResponse(t, recorder)["data"].(map[string]any)
	if data["ims_apn"] != "ims" {
		t.Fatalf("default ims_apn = %#v, want ims", data["ims_apn"])
	}

	recorder = test.request(t, http.MethodPut, "/api/settings/vowifi", `{"ims_apn":"operator.ims"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT ims_apn status = %d, body = %s", recorder.Code, recorder.Body)
	}
	data, _ = decodeSettingsResponse(t, recorder)["data"].(map[string]any)
	if data["ims_apn"] != "operator.ims" {
		t.Fatalf("persisted ims_apn = %#v, want operator.ims", data["ims_apn"])
	}

	// Updating the unrelated MTU setting must not clear the IMS APN.
	recorder = test.request(t, http.MethodPut, "/api/settings/vowifi", `{"mtu_compatibility":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT mtu status = %d, body = %s", recorder.Code, recorder.Body)
	}
	data, _ = decodeSettingsResponse(t, recorder)["data"].(map[string]any)
	if data["mtu_compatibility"] != true || data["ims_apn"] != "operator.ims" {
		t.Fatalf("settings after MTU update = %#v", data)
	}

	for _, body := range []string{`{"ims_apn":"bad apn"}`, `{}`} {
		recorder = test.request(t, http.MethodPut, "/api/settings/vowifi", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s status = %d, want 400, body = %s", body, recorder.Code, recorder.Body)
		}
	}
	data, _ = decodeSettingsResponse(t, test.request(t, http.MethodGet, "/api/settings/vowifi", ""))["data"].(map[string]any)
	if data["ims_apn"] != "operator.ims" {
		t.Fatalf("ims_apn after rejected updates = %#v", data["ims_apn"])
	}
}
