package store

import (
	"context"
	"testing"
)

func TestSMSInsertedSignalsNewRow(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	inserted := database.SMSInserted()
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		DeviceID: "device-1", Peer: "+12025550123", Direction: "inbound", Body: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inserted:
	default:
		t.Fatal("new SMS did not wake notification consumers")
	}
}
