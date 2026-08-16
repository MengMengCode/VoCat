package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func concatExtra(t *testing.T, reference, total, sequence int) json.RawMessage {
	t.Helper()
	extra, err := json.Marshal(map[string]any{
		"concat": map[string]any{"reference": reference, "total": total, "sequence": sequence},
	})
	if err != nil {
		t.Fatalf("marshal concat extra: %v", err)
	}
	return extra
}

func TestMergeConcatSegmentJoinsOutOfOrderInSequenceOrder(t *testing.T) {
	// UCS-2 long SMS (the customer case) whose segments arrive 2, 1, 3.
	var body string
	var extra json.RawMessage
	var changed, complete bool

	body, extra, changed, err := mergeConcatSegment(extra, "中段", concatExtra(t, 9, 3, 2))
	if err != nil || !changed {
		t.Fatalf("segment 2: body=%q changed=%v err=%v", body, changed, err)
	}
	if body != "中段" {
		t.Fatalf("after segment 2 body = %q, want partial %q", body, "中段")
	}

	body, extra, changed, err = mergeConcatSegment(extra, "前段", concatExtra(t, 9, 3, 1))
	if err != nil || !changed {
		t.Fatalf("segment 1: body=%q changed=%v err=%v", body, changed, err)
	}
	if body != "前段中段" {
		t.Fatalf("after segment 1 body = %q, want %q", body, "前段中段")
	}

	body, extra, changed, err = mergeConcatSegment(extra, "尾段", concatExtra(t, 9, 3, 3))
	if err != nil || !changed {
		t.Fatalf("segment 3: body=%q changed=%v err=%v", body, changed, err)
	}
	if body != "前段中段尾段" {
		t.Fatalf("complete body = %q, want %q", body, "前段中段尾段")
	}
	document, err := decodeJSONObject(extra)
	if err != nil {
		t.Fatalf("decode merged extra: %v", err)
	}
	complete, _ = document["concat_complete"].(bool)
	if !complete {
		t.Fatalf("concat_complete = %v, want true; extra=%s", complete, extra)
	}
	if got := numberAsInt(document["concat_received"]); got != 3 {
		t.Fatalf("concat_received = %d, want 3", got)
	}
}

func TestMergeConcatSegmentKeepsURLContiguous(t *testing.T) {
	// A GSM-7 tracking link split mid-token (the OFCA "garbled" report) must
	// reassemble with no break.
	first := "https://ofca.gov.hk/track?tok=ab"
	second := "cdef1234&lang=zh"
	_, extra, _, err := mergeConcatSegment(nil, first, concatExtra(t, 4, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	body, _, _, err := mergeConcatSegment(extra, second, concatExtra(t, 4, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	want := "https://ofca.gov.hk/track?tok=abcdef1234&lang=zh"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestMergeConcatSegmentRedeliveryIsIdempotent(t *testing.T) {
	_, extra, _, err := mergeConcatSegment(nil, "甲", concatExtra(t, 3, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	// A modem rescan redelivers the identical segment: no change, no growth.
	body, extra2, changed, err := mergeConcatSegment(extra, "甲", concatExtra(t, 3, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("redelivered segment reported changed=true; body=%q", body)
	}
	if body != "甲" {
		t.Fatalf("body = %q, want %q", body, "甲")
	}
	if string(extra2) == "" {
		t.Fatal("merged extra lost on idempotent redelivery")
	}
}

func TestMergeConcatSegmentNormalizesCumulativeIMSPart(t *testing.T) {
	first := strings.Repeat("安全提醒", 17)
	want := first + "请通过官方渠道核实。"
	_, extra, _, err := mergeConcatSegment(nil, first, concatExtra(t, 8, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	body, normalized, changed, err := mergeConcatSegment(extra, want, concatExtra(t, 8, 2, 2))
	if err != nil || !changed {
		t.Fatalf("cumulative segment: body=%q changed=%v err=%v", body, changed, err)
	}
	if body != want {
		t.Fatalf("body = %q, want cumulative text once %q", body, want)
	}

	// Redelivering the cumulative wire representation must compare equal to the
	// normalized stored representation and must not churn the durable row id.
	body, _, changed, err = mergeConcatSegment(normalized, want, concatExtra(t, 8, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	if changed || body != want {
		t.Fatalf("redelivery: body=%q changed=%v, want %q/false", body, changed, want)
	}
}

func TestMergeConcatSegmentNormalizesCumulativeIMSPartOutOfOrder(t *testing.T) {
	first := strings.Repeat("甲", 67)
	want := first + "尾段"
	_, extra, _, err := mergeConcatSegment(nil, want, concatExtra(t, 12, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	body, _, changed, err := mergeConcatSegment(extra, first, concatExtra(t, 12, 2, 1))
	if err != nil || !changed {
		t.Fatalf("out-of-order segment: body=%q changed=%v err=%v", body, changed, err)
	}
	if body != want {
		t.Fatalf("body = %q, want cumulative text once %q", body, want)
	}
}

func TestMergeConcatSegmentKeepsEqualRepeatedPart(t *testing.T) {
	_, extra, _, err := mergeConcatSegment(nil, "重复", concatExtra(t, 13, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	body, _, _, err := mergeConcatSegment(extra, "重复", concatExtra(t, 13, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	if body != "重复重复" {
		t.Fatalf("body = %q, want intentional equal segments preserved", body)
	}
}

func TestMergeConcatSegmentWithoutHeaderPassesThrough(t *testing.T) {
	extra, err := json.Marshal(map[string]any{"encoding": "gsm7"})
	if err != nil {
		t.Fatal(err)
	}
	body, _, changed, err := mergeConcatSegment(nil, "plain", extra)
	if err != nil || !changed || body != "plain" {
		t.Fatalf("body=%q changed=%v err=%v, want passthrough", body, changed, err)
	}
}

func TestStableConcatMessageIDScopesByPeerReferenceTotal(t *testing.T) {
	a := StableConcatMessageID("cellular_at", "imei-1", "ec20", "+10086", 7, 2)
	if !isConcatSMSMessageID(a) {
		t.Fatalf("id %q missing concat prefix", a)
	}
	if again := StableConcatMessageID("cellular_at", "imei-1", "ec20", "+10086", 7, 2); again != a {
		t.Fatalf("id unstable: %q vs %q", a, again)
	}
	for _, different := range []string{
		StableConcatMessageID("cellular_at", "imei-1", "ec20", "+10086", 8, 2), // other reference
		StableConcatMessageID("cellular_at", "imei-1", "ec20", "+10010", 7, 2), // other peer
		StableConcatMessageID("cellular_at", "imei-1", "ec20", "+10086", 7, 3), // other total
		StableConcatMessageID("ims", "imei-1", "ec20", "+10086", 7, 2),         // other source
	} {
		if different == a {
			t.Fatalf("id %q collides across distinct concat groups", a)
		}
	}
}

func TestConcatSMSReadyToNotify(t *testing.T) {
	if !ConcatSMSReadyToNotify("modem:SM:3:abcd", json.RawMessage(`{}`)) {
		t.Fatal("plain message should always be ready")
	}
	incomplete := StableConcatMessageID("cellular_at", "imei", "ec20", "peer", 1, 2)
	if ConcatSMSReadyToNotify(incomplete, json.RawMessage(`{"concat_complete":false}`)) {
		t.Fatal("incomplete long SMS must not notify")
	}
	if ConcatSMSReadyToNotify(incomplete, json.RawMessage(`not-json`)) {
		t.Fatal("unparseable concat extra must not notify")
	}
	if !ConcatSMSReadyToNotify(incomplete, json.RawMessage(`{"concat_complete":true}`)) {
		t.Fatal("complete long SMS should notify")
	}
}

func TestSaveConcatSMSFoldsSegmentsIntoOneRow(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const imei = "867394042309830"
	base := time.Unix(1_700_000_000, 0).UTC()

	save := func(sequence int, text string, at time.Time) SMSMessage {
		t.Helper()
		saved, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: StableConcatMessageID("cellular_at", imei, "ec20-1", "+8520000", 5, 2),
			DeviceID:  "ec20-1", ModemIMEI: imei, IMSI: "45400",
			Peer: "+8520000", Direction: "inbound", Body: text,
			Timestamp: at, Status: "received", Source: "cellular_at",
			PartsTotal: 2,
			Extra:      concatExtra(t, 5, 2, sequence),
		})
		if err != nil {
			t.Fatalf("SaveSMSMessage(seq=%d) error = %v", sequence, err)
		}
		return saved
	}

	first := save(1, "【检测】您的结果为", base)
	if first.PartsTotal != 2 {
		t.Fatalf("PartsTotal = %d, want 2", first.PartsTotal)
	}
	if ConcatSMSReadyToNotify(first.MessageID, first.Extra) {
		t.Fatal("first segment alone should not be ready to notify")
	}

	second := save(2, "合格，请查收报告", base.Add(30*time.Second))
	if second.ID <= first.ID {
		t.Fatalf("completed row id = %d, want a fresh id greater than %d", second.ID, first.ID)
	}
	if second.Body != "【检测】您的结果为合格，请查收报告" {
		t.Fatalf("merged body = %q", second.Body)
	}
	if !second.Timestamp.Equal(base) {
		t.Fatalf("merged timestamp = %v, want earliest segment %v", second.Timestamp, base)
	}
	if !ConcatSMSReadyToNotify(second.MessageID, second.Extra) {
		t.Fatal("completed long SMS should be ready to notify")
	}

	// Exactly one stored row represents the whole long SMS.
	messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("stored rows = %d, want 1 merged row: %+v", len(messages), messages)
	}

	// The completed row re-enters after the earlier partial id, so the Telegram
	// id-cursor surfaces it once, complete.
	fresh, err := database.ListInboundSMSAfterID(ctx, first.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].ID != second.ID || !strings.Contains(fresh[0].Body, "合格") {
		t.Fatalf("ListInboundSMSAfterID = %+v, want the completed row", fresh)
	}

	// A modem rescan redelivers an already-folded segment: no write, no id churn.
	rescan := save(1, "【检测】您的结果为", base)
	if rescan.ID != second.ID {
		t.Fatalf("rescan churned the row id: got %d, want stable %d", rescan.ID, second.ID)
	}
	if rescan.Body != second.Body {
		t.Fatalf("rescan changed body to %q", rescan.Body)
	}
	if after, err := database.ListInboundSMSAfterID(ctx, second.ID, 10); err != nil || len(after) != 0 {
		t.Fatalf("rescan produced new rows: %+v, %v", after, err)
	}
	if count, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"}); err != nil || len(count) != 1 {
		t.Fatalf("rows after rescan = %d, %v; want still 1", len(count), err)
	}
}
