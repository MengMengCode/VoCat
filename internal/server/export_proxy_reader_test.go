package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/store"
)

func TestExportProxyRejectsUSBSIMReader(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "reader-1", Name: "USB SIM Reader", DeviceType: store.DeviceTypeUSBSIMReader,
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database}
	response := httptest.NewRecorder()
	if !server.rejectUnsupportedExportProxyDevice(response, context.Background(), "reader-1") {
		t.Fatal("reader was accepted as an export-proxy device")
	}
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
