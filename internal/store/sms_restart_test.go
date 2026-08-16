package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestLongSMSReassemblySurvivesServiceRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vocat.db")
	const (
		deviceID = "dajiang"
		imei     = "867394042309830"
		peer     = "+447700900123"
	)
	messageID := StableConcatMessageID("ims", imei, deviceID, peer, 27, 2)

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustSaveDevice(t, database, deviceID, "大疆")
	first, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: messageID, DeviceID: deviceID, ModemIMEI: imei, IMSI: "23433",
		Peer: peer, Direction: "inbound", Body: "第一段：安全提醒，",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(), Status: "received", Source: "ims",
		PartsTotal: 2, Extra: concatExtra(t, 27, 2, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ConcatSMSReadyToNotify(first.MessageID, first.Extra) {
		t.Fatal("partial message must not be ready before restart")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	second, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: messageID, DeviceID: deviceID, ModemIMEI: imei, IMSI: "23433",
		Peer: peer, Direction: "inbound", Body: "第二段：请通过官方渠道核实。",
		Timestamp: time.Unix(1_700_000_030, 0).UTC(), Status: "received", Source: "ims",
		PartsTotal: 2, Extra: concatExtra(t, 27, 2, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Body != "第一段：安全提醒，第二段：请通过官方渠道核实。" ||
		!ConcatSMSReadyToNotify(second.MessageID, second.Extra) {
		t.Fatalf("reassembled message after restart = %#v", second)
	}
	messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != second.ID {
		t.Fatalf("stored messages after restart = %#v, want one merged row", messages)
	}
	redelivered, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: messageID, DeviceID: deviceID, ModemIMEI: imei, IMSI: "23433",
		Peer: peer, Direction: "inbound", Body: "第二段：请通过官方渠道核实。",
		Status: "received", Source: "ims", PartsTotal: 2, Extra: concatExtra(t, 27, 2, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if redelivered.ID != second.ID || redelivered.Body != second.Body {
		t.Fatalf("redelivery duplicated or changed message: %#v", redelivered)
	}
}

func TestMultipartDeliveryReportsSurviveServiceRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vocat.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustSaveDevice(t, database, "dajiang", "大疆")
	extra := json.RawMessage(`{"transport":"ims","part_results":[{"reference":51},{"reference":52}]}`)
	sent, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "ims-submit-restart", DeviceID: "dajiang", IMSI: "23433",
		Peer: "+447700900123", Direction: "outbound", Body: "multipart",
		Status: "accepted_by_ims", Source: "ims", PartsTotal: 2,
		DeliveryState: "accepted_by_ims", Read: true, Extra: extra,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	first, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		DeviceID: "dajiang", IMSI: "23433", Peer: "+447700900123", Source: "ims",
		MessageReference: 51, StatusCode: 0, DeliveryState: "delivered",
	})
	if err != nil || first.ID != sent.ID || first.DeliveryState != "pending_delivery_report" {
		t.Fatalf("first report after restart = (%#v, %v)", first, err)
	}
	second, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		DeviceID: "dajiang", IMSI: "23433", Peer: "+447700900123", Source: "ims",
		MessageReference: 52, StatusCode: 0, DeliveryState: "delivered",
	})
	if err != nil || second.ID != sent.ID || second.DeliveryState != "delivered" {
		t.Fatalf("second report after restart = (%#v, %v)", second, err)
	}
}
