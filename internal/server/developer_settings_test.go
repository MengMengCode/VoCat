package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/developer"
	"vocat/internal/store"
)

func TestDeveloperOnlySettingsAreHiddenWhenModeIsOff(t *testing.T) {
	server := &Server{developerEnabled: false}
	for _, handler := range []func(http.ResponseWriter, *http.Request){
		server.handleDeveloperSettings,
		server.handleHTTPSSettings,
		server.handleHTTPSCertificate,
	} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodGet, "/api/settings/developer", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("developer-only endpoint status = %d, want 404", response.Code)
		}
	}
}

func TestDeveloperSettingsUpdatesGlobalSMSLimit(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	enabled, _ := json.Marshal(map[string]bool{"enabled": true})
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: developer.EnabledSettingKey, Value: enabled}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database, developerEnabled: true, logger: regionTestLogger(), maxRequestBodyBytes: 4096}
	request := httptest.NewRequest(http.MethodPut, "/api/settings/developer", strings.NewReader(`{"sms_hourly_limit":17}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleDeveloperSettings(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := developer.SMSHourlyLimit(ctx, database); got != 17 {
		t.Fatalf("SMS hourly limit = %d, want 17", got)
	}
}
