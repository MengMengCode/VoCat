package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"vocat/internal/store"
)

// After a restart, an SMS stored after Open but before a dispatcher's first
// tick (the modem sync loop can run first) must still be notified, while
// history from before the restart must not be replayed.
func TestSMSNotificationDeliversStartupWindowArrival(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vocat.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		DeviceID: "device-1", Peer: "+12025550123", Direction: "inbound",
		Body: "history before restart", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{store: database, logger: regionTestLogger()}
	if err := database.UpsertNotificationSetting(ctx, store.NotificationSetting{
		Channel:         "webhook",
		Enabled:         true,
		Config:          []byte(`{"urls":["https://example.com/hook"]}`),
		SensitiveFields: store.DefaultNotificationSensitiveFields("webhook"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		DeviceID: "device-1", Peer: "+12025550123", Direction: "inbound",
		Body: "arrived during startup", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var delivered []string
	sender := func(_ context.Context, _ string, _ map[string]any, notification smsNotification) error {
		delivered = append(delivered, notification.Content)
		return nil
	}
	server.deliverSMSNotificationsOnce(ctx, "webhook", &smsNotificationCursor{}, sender)
	if len(delivered) != 1 || delivered[0] != "arrived during startup" {
		t.Fatalf("delivered = %q, want only the startup-window SMS", delivered)
	}
}
