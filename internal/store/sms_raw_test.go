package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSMSRawArchiveSurvivesRestartRescanAndSlotReuse(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sms.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	record := SMSRawRecord{DeviceID: "ec20", ModemIMEI: "imei", ICCID: "card-a", IMSI: "imsi-a",
		Storage: "ME", Slot: 7, RawPDU: "aabb", RawUserData: "BB", DecodeError: "truncated"}
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	before, err := db.ListSMSRawRecords(ctx, "ec20", 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("archive: %v, %v", before, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// A decoder upgrade can update parser status, but a different active SIM
	// must not relabel an existing physical record.
	record.ICCID, record.IMSI, record.DecodeError = "card-b", "imsi-b", ""
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	rescanned, err := db.ListSMSRawRecords(ctx, "ec20", 10)
	if err != nil || len(rescanned) != 1 || rescanned[0].ID != before[0].ID ||
		rescanned[0].FirstSeen != before[0].FirstSeen || rescanned[0].ICCID != "card-a" ||
		rescanned[0].IMSI != "imsi-a" || rescanned[0].DecodeError != "" || rescanned[0].RawPDU != "AABB" {
		t.Fatalf("rescan: %#v, %v", rescanned, err)
	}
	record.RawPDU = "CCDD"
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Storage = "SM"
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	all, err := db.ListSMSRawRecords(ctx, "ec20", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("slot reuse/storage separation: %#v, %v", all, err)
	}
	if filtered, err := db.ListSMSRawRecords(ctx, "other", 10); err != nil || len(filtered) != 0 {
		t.Fatalf("device filter: %v, %v", filtered, err)
	}
}

func TestConcatDecodeErrorsSurviveOtherSegmentsAndClearAfterRecovery(t *testing.T) {
	first := json.RawMessage(`{"concat":{"reference":1,"total":2,"sequence":1},"decode_error":"truncated"}`)
	second := json.RawMessage(`{"concat":{"reference":1,"total":2,"sequence":2}}`)
	_, extra, _, err := mergeConcatSegment(nil, "", first)
	if err != nil {
		t.Fatal(err)
	}
	body, extra, _, err := mergeConcatSegment(extra, "second", second)
	if err != nil || body != "second" || SMSDecodeError(extra) != "part 1: truncated" {
		t.Fatalf("lost first segment's error: %q %s %v", body, extra, err)
	}
	recovered := json.RawMessage(`{"concat":{"reference":1,"total":2,"sequence":1}}`)
	body, extra, changed, err := mergeConcatSegment(extra, "first ", recovered)
	if err != nil || !changed || body != "first second" || SMSDecodeError(extra) != "" {
		t.Fatalf("recovery: %q %s changed=%t err=%v", body, extra, changed, err)
	}
}
