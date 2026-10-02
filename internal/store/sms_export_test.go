package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExportSMSUnlimitedStableOrderAndPreservation(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	stamp := time.Unix(1700000000, 0).UTC()
	want := make([]SMSMessage, 1005)
	for i := range want {
		message, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: fmt.Sprintf("message-%04d", i), DeviceID: "old-name", ModemIMEI: "imei-a",
			ICCID: "card-a", IMSI: "imsi-a", LocalPhone: "+44123", Peer: "peer", Direction: "inbound",
			Body: "  exact\r\n" + strings.Repeat("短信🙂<&> ", 100) + "\n\tend  ", Timestamp: stamp,
			Status: "received", Source: "cellular_at", PartsTotal: 3, DeliveryState: "delivered",
			Read: i%2 == 0, Extra: []byte(`{"nested":{"value":"原文"}}`),
			CreatedAt: stamp.Add(-time.Hour), UpdatedAt: stamp.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		want[i] = message
	}
	for pass := 0; pass < 2; pass++ {
		var got []SMSMessage
		err := database.ExportSMSMessages(ctx, SMSFilter{Limit: 1, BeforeID: want[1].ID}, func(m SMSMessage) error {
			got = append(got, m)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("export count = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("pass %d message %d changed or reordered: got %#v, want %#v", pass, i, got[i], want[i])
			}
		}
	}
}

func TestExportSMSFiltersAndConversationOrder(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	stamp := time.Unix(1700000000, 0).UTC()
	fixtures := []SMSMessage{
		{MessageID: "other-hardware", DeviceID: "other", ModemIMEI: "imei-b", ICCID: "card-a", Peer: "peer", Timestamp: stamp},
		{MessageID: "other-card", DeviceID: "old", ModemIMEI: "imei-a", ICCID: "card-b", Peer: "peer", Timestamp: stamp},
		{MessageID: "until", DeviceID: "old", ModemIMEI: "imei-a", ICCID: "card-a", Peer: "peer", Timestamp: stamp.Add(time.Second)},
		{MessageID: "since", DeviceID: "old", ModemIMEI: "imei-a", ICCID: "card-a", Peer: "peer", Timestamp: stamp},
		{MessageID: "before", DeviceID: "old", ModemIMEI: "imei-a", ICCID: "card-a", Peer: "peer", Timestamp: stamp.Add(-time.Second)},
		{MessageID: "other-peer", DeviceID: "old", ModemIMEI: "imei-a", ICCID: "card-a", Peer: "aaa", Timestamp: stamp},
	}
	for _, m := range fixtures {
		m.Direction = "outbound"
		m.IMSI = "imsi-" + m.ICCID
		if _, err := database.SaveSMSMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		filter SMSFilter
		want   []string
	}{
		{"all ordered", SMSFilter{}, []string{"other-peer", "before", "since", "until", "other-card", "other-hardware"}},
		{"half open", SMSFilter{ModemIMEI: "imei-a", ICCID: "card-a", Peer: "peer", Since: stamp, Until: stamp.Add(time.Second)}, []string{"since"}},
		{"device and subscription", SMSFilter{DeviceID: "other", IMSI: "imsi-card-a"}, []string{"other-hardware"}},
		{"empty", SMSFilter{DeviceID: "missing"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			if err := database.ExportSMSMessages(ctx, tc.filter, func(m SMSMessage) error { got = append(got, m.MessageID); return nil }); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("messages = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExportSMSCancellationAndVisitorFailure(t *testing.T) {
	database := openTestStore(t, ":memory:")
	for i := 0; i < 3; i++ {
		if _, err := database.SaveSMSMessage(context.Background(), SMSMessage{DeviceID: "device", Peer: "peer", Direction: "inbound"}); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := errors.New("visitor failed")
	calls := 0
	err := database.ExportSMSMessages(context.Background(), SMSFilter{}, func(SMSMessage) error { calls++; return sentinel })
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("visitor failure: calls=%d err=%v", calls, err)
	}
	for _, during := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if !during {
			cancel()
		}
		calls = 0
		err := database.ExportSMSMessages(ctx, SMSFilter{}, func(SMSMessage) error { calls++; cancel(); return nil })
		cancel()
		if !errors.Is(err, context.Canceled) || (!during && calls != 0) || (during && calls != 1) {
			t.Fatalf("cancellation during=%v: calls=%d err=%v", during, calls, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := database.ListSMSMessages(ctx, SMSFilter{}); err != nil {
		t.Fatal(err)
	}
}

func TestExportSMSSingleSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	reader := openTestStore(t, path)
	writer := openTestStore(t, path)
	stamp := time.Unix(1700000000, 0).UTC()
	for _, id := range []string{"first", "last"} {
		if _, err := writer.SaveSMSMessage(ctx, SMSMessage{MessageID: id, DeviceID: "device", Peer: "peer", Direction: "inbound", Body: "original", Timestamp: stamp}); err != nil {
			t.Fatal(err)
		}
	}
	var got []SMSMessage
	err := reader.ExportSMSMessages(ctx, SMSFilter{}, func(m SMSMessage) error {
		got = append(got, m)
		if len(got) == 1 {
			if _, err := writer.SaveSMSMessage(ctx, SMSMessage{MessageID: "last", DeviceID: "device", Peer: "peer", Direction: "inbound", Body: "updated", Timestamp: stamp}); err != nil {
				return err
			}
			_, err := writer.SaveSMSMessage(ctx, SMSMessage{MessageID: "new", DeviceID: "device", Peer: "peer", Direction: "inbound", Body: "new", Timestamp: stamp})
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].MessageID != "last" || got[1].Body != "original" {
		t.Fatalf("mixed export snapshot: %#v", got)
	}
	got = nil
	if err := reader.ExportSMSMessages(ctx, SMSFilter{}, func(m SMSMessage) error { got = append(got, m); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].Body != "updated" {
		t.Fatalf("subsequent snapshot missed committed writes: %#v", got)
	}
}
