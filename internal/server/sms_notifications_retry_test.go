package server

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/auth"
	"vocat/internal/store"
)

// TestSMSNotificationFailureDoesNotStallLaterMessages verifies that a provider
// permanently rejecting one SMS does not stall every later notification for
// that channel and is not retried forever: the dispatcher retries with a
// bounded exponential backoff and then moves past the poisoned message so later
// ones are still delivered.
func TestSMSNotificationFailureDoesNotStallLaterMessages(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authService, err := auth.New(database, auth.Options{SessionTTL: time.Hour, BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Store:               database,
		Auth:                authService,
		Assets:              fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}},
		MaxRequestBodyBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertNotificationSetting(ctx, store.NotificationSetting{
		Channel:         "webhook",
		Enabled:         true,
		Config:          []byte(`{"urls":["https://example.com/hook"]}`),
		SensitiveFields: store.DefaultNotificationSensitiveFields("webhook"),
	}); err != nil {
		t.Fatal(err)
	}

	firstAttempts := 0
	delivered := 0
	sender := func(_ context.Context, _ string, _ map[string]any, notification smsNotification) error {
		if notification.Content == "first" {
			firstAttempts++
			return errors.New("provider rejected the notification")
		}
		delivered++
		return nil
	}

	// The first tick initializes the channel cursor to the current max id, so
	// run it before inserting the messages the dispatcher should deliver.
	state := &smsNotificationCursor{}
	server.deliverSMSNotificationsOnce(ctx, "webhook", state, sender)

	for _, body := range []string{"first", "second"} {
		if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
			DeviceID:  "device-1",
			Peer:      "+12025550123",
			Direction: "inbound",
			Body:      body,
			Timestamp: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	for tick := 0; tick < 40 && delivered == 0; tick++ {
		server.deliverSMSNotificationsOnce(ctx, "webhook", state, sender)
	}

	if delivered == 0 {
		t.Fatalf("the later notification was never delivered; a single failing message stalled the channel (first message attempted %d times)", firstAttempts)
	}
	if firstAttempts > smsNotificationMaxAttempts+1 {
		t.Fatalf("the failing notification was retried %d times instead of backing off and giving up", firstAttempts)
	}
}
