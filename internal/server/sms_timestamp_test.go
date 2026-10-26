package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func TestSyncModemSMSNormalizesDITOBacklogAndRescans(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const deviceID, imei = "dito-ec20", "867394042309830"
	if err := database.UpsertDevice(ctx, store.Device{
		ID: deviceID, Name: "DITO", DeviceType: store.DeviceTypePCIeEC20EC25,
		ModemIMEI: imei, SMSEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		scts string
		want string
	}{
		{"62012231806400", "2026-10-22T05:08:46Z"},
		{"62012281950100", "2026-10-22T10:59:10Z"},
		{"62012222750000", "2026-10-22T14:57:00Z"},
		{"62012231806423", "2026-10-22T05:08:46Z"}, // Correct +08:00 already present.
	}
	var messages []device.SMSMessage
	for index, fixture := range fixtures {
		tpdu, err := hex.DecodeString("0405912143F50000" + fixture.scts + "05C82293F904")
		if err != nil {
			t.Fatal(err)
		}
		message, err := device.DecodeSMSDeliverTPDU(tpdu)
		if err != nil {
			t.Fatal(err)
		}
		message.Index, message.Storage, message.StorageStatus = index+1, "ME", device.SMSStatusReceivedUnread
		messages = append(messages, message)
	}
	snapshot := &device.Snapshot{
		DeviceID: deviceID, IMEI: imei, ICCID: "89636624000000000001",
		IMSI: "515660000000001", MNCLength: 2,
		// The SPN may not be available during AT catch-up scans. Also verify
		// that a visited foreign operator does not select the timestamp zone.
		OperatorCode: "23415",
	}
	server := &Server{
		store: database, logger: regionTestLogger(),
		devices: fakeDeviceController{
			entry:       device.Device{ID: deviceID, Discovered: true, Snapshot: snapshot},
			smsMessages: messages,
		},
	}
	// Model an old, still-readable modem SMS stored before the fix. Its PDU
	// identity must remain stable while the previously skewed time is corrected.
	old, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		MessageID: modemSMSMessageID(messages[0], imei, deviceID, messages[0].From, ""),
		DeviceID:  deviceID, ModemIMEI: imei, ICCID: snapshot.ICCID, IMSI: snapshot.IMSI,
		Peer: messages[0].From, Direction: "inbound", Body: messages[0].Text,
		Timestamp: *messages[0].ServiceCenterTimestamp, Source: "cellular_at",
	})
	if err != nil {
		t.Fatal(err)
	}
	for scan := 0; scan < 2; scan++ {
		server.syncModemSMS(ctx, deviceID)
		stored, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: deviceID})
		if err != nil || len(stored) != len(fixtures) {
			t.Fatalf("scan %d: messages=%#v error=%v", scan, stored, err)
		}
		bySlot := make(map[int]store.SMSMessage)
		for _, saved := range stored {
			var metadata struct {
				Slot          int                            `json:"modem_index"`
				ServiceCenter *time.Time                     `json:"service_center_timestamp"`
				Correction    *vowifi.SMSTimestampCorrection `json:"service_center_timestamp_correction"`
			}
			if err := json.Unmarshal(saved.Extra, &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Slot < 1 || metadata.Slot > len(fixtures) {
				t.Fatalf("unexpected modem slot in metadata: %s", saved.Extra)
			}
			bySlot[metadata.Slot] = saved
			want, _ := time.Parse(time.RFC3339, fixtures[metadata.Slot-1].want)
			if !saved.Timestamp.Equal(want) {
				t.Errorf("scan %d, slot %d: timestamp=%v want=%v", scan, metadata.Slot, saved.Timestamp, want)
			}
			if metadata.Slot < 4 {
				if metadata.ServiceCenter == nil || !metadata.ServiceCenter.Equal(want) || metadata.Correction == nil ||
					!metadata.Correction.OriginalTimestamp.Equal(*messages[metadata.Slot-1].ServiceCenterTimestamp) {
					t.Errorf("slot %d lost original/corrected SCTS evidence: %s", metadata.Slot, saved.Extra)
				}
			}
			if metadata.Slot == 4 && metadata.Correction != nil {
				t.Errorf("valid nonzero timezone was corrected: %s", saved.Extra)
			}
		}
		if bySlot[1].ID != old.ID {
			t.Errorf("rescan duplicated the existing SMS: id=%d, want %d", bySlot[1].ID, old.ID)
		}
		if bySlot[1].Timestamp.Equal(bySlot[2].Timestamp) || bySlot[2].Timestamp.Equal(bySlot[3].Timestamp) {
			t.Error("backlog SCTS values collapsed to one arrival time")
		}
	}
}
