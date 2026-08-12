package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

const testProfileICCID = "89441000400128014257"

func newProfileBindingTestServer(t *testing.T) (*Server, *store.Store, *fakeVoWiFiController) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "ec20", Name: "EC20", VoWiFiEnabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, upstream := range []store.UpstreamProxy{
		{ID: "route-1", Name: "Route 1", Addr: "127.0.0.1:1080", Enabled: true},
		{ID: "route-2", Name: "Route 2", Addr: "127.0.0.1:1081", Enabled: true},
	} {
		if err := database.UpsertUpstreamProxy(context.Background(), upstream); err != nil {
			t.Fatal(err)
		}
	}
	controller := &fakeVoWiFiController{state: vowifi.State{DeviceID: "ec20", ICCID: testProfileICCID, Enabled: true}}
	return &Server{store: database, vowifi: controller, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), maxRequestBodyBytes: 16 << 10}, database, controller
}

func profileBindingRequest(t *testing.T, server *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/upstream-proxy-profile-bindings", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleProfileProxyBindings(response, request)
	return response
}

func TestProfileProxyBindingPersistsAndReconnectsOnlyCurrentICCID(t *testing.T) {
	server, database, controller := newProfileBindingTestServer(t)
	response := profileBindingRequest(t, server, http.MethodPost, `{
		"upstream_proxy_id":"route-1",
		"bindings":[
			{"device_id":"ec20","iccid":"89441000400128014257","profile_name":"Vodafone UK","state_text":"Enabled"},
			{"device_id":"ec20","iccid":"89104100000028106378","profile_name":"TIM"}
		]
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body = %s", response.Code, response.Body.String())
	}
	binding, err := database.DeviceProxyBinding(context.Background(), testProfileICCID)
	if err != nil || binding.UpstreamProxyID != "route-1" || binding.ProfileName != "Vodafone UK" {
		t.Fatalf("binding = %+v, %v", binding, err)
	}
	if controller.reconnects != 1 {
		t.Fatalf("reconnects = %d, want only the current ICCID to reconnect", controller.reconnects)
	}

	response = profileBindingRequest(t, server, http.MethodDelete, `{"upstream_proxy_id":"route-1","iccids":["89441000400128014257","89104100000028106378"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", response.Code, response.Body.String())
	}
	if _, err := database.DeviceProxyBinding(context.Background(), testProfileICCID); err != store.ErrNotFound {
		t.Fatalf("binding after delete error = %v, want ErrNotFound", err)
	}
	if controller.reconnects != 2 {
		t.Fatalf("reconnects after delete = %d, want 2", controller.reconnects)
	}
}

func TestProfileProxyBindingRejectsSameICCIDOnDifferentProxy(t *testing.T) {
	server, database, _ := newProfileBindingTestServer(t)
	first := profileBindingRequest(t, server, http.MethodPost, `{"upstream_proxy_id":"route-1","bindings":[{"device_id":"ec20","iccid":"89441000400128014257","profile_name":"Profile"}]}`)
	if first.Code != http.StatusOK {
		t.Fatalf("initial bind status = %d, body = %s", first.Code, first.Body.String())
	}
	second := profileBindingRequest(t, server, http.MethodPost, `{"upstream_proxy_id":"route-2","bindings":[{"device_id":"ec20","iccid":"89441000400128014257","profile_name":"Profile"}]}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("rebind status = %d, want 409, body = %s", second.Code, second.Body.String())
	}
	binding, err := database.DeviceProxyBinding(context.Background(), testProfileICCID)
	if err != nil || binding.UpstreamProxyID != "route-1" {
		t.Fatalf("binding after rejected rebind = %+v, %v", binding, err)
	}
}

func TestProfileProxyBindingSupportsPCSCReader(t *testing.T) {
	server, database, controller := newProfileBindingTestServer(t)
	readerICCID := "89104100000028106378"
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "reader-1", Name: "USB SIM Reader", DeviceType: store.DeviceTypeUSBSIMReader,
	}); err != nil {
		t.Fatal(err)
	}
	controller.state = vowifi.State{DeviceID: "reader-1", ICCID: readerICCID, Enabled: true}
	response := profileBindingRequest(t, server, http.MethodPost, `{
		"upstream_proxy_id":"route-1",
		"bindings":[{"device_id":"reader-1","iccid":"89104100000028106378","profile_name":"Reader Profile"}]
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body = %s", response.Code, response.Body.String())
	}
	binding, err := database.DeviceProxyBinding(context.Background(), readerICCID)
	if err != nil || binding.DeviceID != "reader-1" || binding.UpstreamProxyID != "route-1" {
		t.Fatalf("reader binding = %+v, %v", binding, err)
	}
	if controller.reconnects != 1 {
		t.Fatalf("reader reconnects = %d, want 1", controller.reconnects)
	}
}
