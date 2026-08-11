package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/store"
)

func TestDeviceCarrierConfigResponseIsNotCacheable(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "modem-1", Name: "410", IMSPrivateIdentity: "impi@example.net",
		IMSPublicIdentity: "sip:+12025550123@example.net",
	}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/devices/modem-1/config", nil)
	server := &Server{store: database}
	if !server.handleDevicePath(recorder, request, "modem-1", []string{"config"}) {
		t.Fatal("device config route was not handled")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
