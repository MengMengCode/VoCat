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

func physicalConcatExtra(
	t *testing.T,
	storage string,
	fingerprint string,
	occurrenceID string,
	reference int,
	total int,
	sequence int,
) json.RawMessage {
	t.Helper()
	extra, err := json.Marshal(map[string]any{
		"storage":               storage,
		"segment_fingerprint":   fingerprint,
		"segment_occurrence_id": occurrenceID,
		"concat": map[string]any{
			"reference": reference,
			"total":     total,
			"sequence":  sequence,
		},
	})
	if err != nil {
		t.Fatalf("marshal physical concat extra: %v", err)
	}
	return extra
}

func legacyConcatExtra(
	t *testing.T,
	storage string,
	reference int,
	total int,
	sequence int,
	parts map[string]string,
) json.RawMessage {
	t.Helper()
	extra, err := json.Marshal(map[string]any{
		"storage": storage,
		"concat": map[string]any{
			"reference": reference,
			"total":     total,
			"sequence":  sequence,
		},
		"concat_parts":    parts,
		"concat_received": len(parts),
		"concat_complete": len(parts) == total,
	})
	if err != nil {
		t.Fatalf("marshal legacy concat extra: %v", err)
	}
	return extra
}

func insertLegacyConcatRow(
	t *testing.T,
	database *Store,
	messageID string,
	deviceID string,
	modemIMEI string,
	imsi string,
	peer string,
	body string,
	source string,
	partsTotal int,
	at time.Time,
	extra json.RawMessage,
) SMSMessage {
	t.Helper()
	result, err := database.db.ExecContext(context.Background(), `
		INSERT INTO sms_messages (
			message_id, device_id, modem_imei, imsi, peer, direction, body,
			message_time, status, source, parts_total, delivery_state, is_read,
			extra_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'inbound', ?, ?, 'received', ?, ?, '', 0, ?, ?, ?)
	`,
		messageID, deviceID, modemIMEI, imsi, peer, body,
		at.Unix(), source, partsTotal, string(extra), at.Unix(), at.Unix(),
	)
	if err != nil {
		t.Fatalf("insert legacy concat row: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("legacy concat row id: %v", err)
	}
	message, err := database.SMSMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("read legacy concat row: %v", err)
	}
	return message
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

func TestMergeConcatSegmentOccurrenceIDAndFingerprintMustAgree(t *testing.T) {
	first := physicalConcatExtra(t, "ME", "sha256:first", "ME:slot-20:first", 3, 2, 1)
	_, existing, changed, err := mergeConcatSegment(nil, "same body", first)
	if err != nil || !changed {
		t.Fatalf("initial merge changed=%v err=%v", changed, err)
	}
	document, err := decodeJSONObject(existing)
	if err != nil {
		t.Fatal(err)
	}
	occurrenceIDs, _ := document["concat_occurrence_ids"].(map[string]any)
	if occurrenceIDs["1"] != "ME:slot-20:first" {
		t.Fatalf("concat_occurrence_ids = %#v", occurrenceIDs)
	}

	// An exact occurrence id makes an identical rescan idempotent.
	_, _, changed, err = mergeConcatSegment(
		existing,
		"same body",
		physicalConcatExtra(t, "ME", "sha256:first", "ME:slot-20:first", 3, 2, 1),
	)
	if err != nil || changed {
		t.Fatalf("same occurrence changed=%v err=%v", changed, err)
	}
	if _, _, _, err := mergeConcatSegment(
		existing,
		"same body",
		physicalConcatExtra(t, "ME", "sha256:changed", "ME:slot-20:first", 3, 2, 1),
	); err == nil {
		t.Fatal("same occurrence id with a conflicting fingerprint was accepted")
	}

	// Conversely, equal bytes in a different slot are a separate occurrence.
	if _, _, _, err := mergeConcatSegment(
		existing,
		"same body",
		physicalConcatExtra(t, "ME", "sha256:first", "ME:slot-21:first", 3, 2, 1),
	); err == nil {
		t.Fatal("different exact occurrence id was accepted as a redelivery")
	}
}

func TestSaveConcatSMSLegacyFingerprintMatchDoesNotBackfillOccurrenceID(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const (
		imei      = "867394042309830"
		peer      = "+8520000"
		reference = 76
	)
	messageID := StableConcatMessageID("cellular_qmi", imei, "ec20-1", "me:epoch", peer, reference, 2)
	save := func(occurrenceID string) SMSMessage {
		t.Helper()
		saved, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: messageID, DeviceID: "ec20-1", ModemIMEI: imei,
			Peer: peer, Direction: "inbound", Body: "legacy part",
			Status: "received", Source: "cellular_qmi", PartsTotal: 2,
			Extra: physicalConcatExtra(t, "ME", "sha256:legacy-part", occurrenceID, reference, 2, 1),
		})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}

	legacy := save("")
	rescanned := save("ME:slot-40:legacy-part")
	if rescanned.ID != legacy.ID {
		t.Fatalf("legacy fingerprint rescan churned id: got %d, want %d", rescanned.ID, legacy.ID)
	}
	document, err := decodeJSONObject(rescanned.Extra)
	if err != nil {
		t.Fatal(err)
	}
	occurrenceIDs, _ := document["concat_occurrence_ids"].(map[string]any)
	if stored, _ := occurrenceIDs["1"].(string); stored != "" {
		t.Fatalf("legacy row unexpectedly backfilled occurrence id %q", stored)
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

func TestMergeConcatSegmentRejectsMalformedMetadata(t *testing.T) {
	for _, test := range []struct {
		name      string
		reference int
		total     int
		sequence  int
	}{
		{name: "single part concat", reference: 1, total: 1, sequence: 1},
		{name: "zero sequence", reference: 1, total: 2, sequence: 0},
		{name: "sequence over total", reference: 1, total: 2, sequence: 3},
		{name: "missing reference", reference: -1, total: 2, sequence: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := mergeConcatSegment(nil, "part", concatExtra(t, test.reference, test.total, test.sequence)); err == nil {
				t.Fatal("mergeConcatSegment() accepted malformed concat metadata")
			}
		})
	}
}

func TestMergeConcatSegmentRejectsMetadataChange(t *testing.T) {
	_, existing, _, err := mergeConcatSegment(nil, "first", concatExtra(t, 7, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []json.RawMessage{
		concatExtra(t, 8, 2, 2),
		concatExtra(t, 7, 3, 2),
	} {
		if _, _, _, err := mergeConcatSegment(existing, "second", changed); err == nil {
			t.Fatal("mergeConcatSegment() accepted changed reference/total")
		}
	}
}

func TestStableConcatMessageIDScopesBySubscriberPeerReferenceTotal(t *testing.T) {
	a := StableConcatMessageID("cellular_at", "imei-1", "ec20", "45400", "+10086", 7, 2)
	if !isConcatSMSMessageID(a) {
		t.Fatalf("id %q missing concat prefix", a)
	}
	if !strings.Contains(a, ":45400:") {
		t.Fatalf("id %q does not contain its IMSI scope", a)
	}
	if again := StableConcatMessageID("cellular_at", "imei-1", "ec20", "45400", "+10086", 7, 2); again != a {
		t.Fatalf("id unstable: %q vs %q", a, again)
	}
	for _, different := range []string{
		StableConcatMessageID("cellular_at", "imei-1", "ec20", "45401", "+10086", 7, 2), // other IMSI
		StableConcatMessageID("cellular_at", "imei-1", "ec20", "45400", "+10086", 8, 2), // other reference
		StableConcatMessageID("cellular_at", "imei-1", "ec20", "45400", "+10010", 7, 2), // other peer
		StableConcatMessageID("cellular_at", "imei-1", "ec20", "45400", "+10086", 7, 3), // other total
		StableConcatMessageID("ims", "imei-1", "ec20", "45400", "+10086", 7, 2),         // other source
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
	incomplete := StableConcatMessageID("cellular_at", "imei", "ec20", "45400", "peer", 1, 2)
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
			MessageID: StableConcatMessageID("cellular_at", imei, "ec20-1", "45400", "+8520000", 5, 2),
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

func TestSaveConcatSMSScopesAssembliesByIMSI(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const imei = "867394042309830"

	for _, subscriber := range []struct {
		imsi string
		body string
	}{
		{imsi: "45400", body: "SIM A 第一段"},
		{imsi: "45401", body: "SIM B 第一段"},
	} {
		if _, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: StableConcatMessageID("cellular_at", imei, "ec20-1", subscriber.imsi, "+8520000", 5, 2),
			DeviceID:  "ec20-1", ModemIMEI: imei, IMSI: subscriber.imsi,
			Peer: "+8520000", Direction: "inbound", Body: subscriber.body,
			Status: "received", Source: "cellular_at", PartsTotal: 2,
			Extra: concatExtra(t, 5, 2, 1),
		}); err != nil {
			t.Fatalf("SaveSMSMessage(IMSI=%s) error = %v", subscriber.imsi, err)
		}
	}

	messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("stored rows = %d, want separate rows for each IMSI: %+v", len(messages), messages)
	}
	for _, got := range messages {
		if !strings.Contains(got.MessageID, ":"+got.IMSI+":") {
			t.Fatalf("message id %q does not carry row IMSI %q", got.MessageID, got.IMSI)
		}
		if got.IMSI == "45400" && got.Body != "SIM A 第一段" {
			t.Fatalf("SIM A body contaminated: %q", got.Body)
		}
		if got.IMSI == "45401" && got.Body != "SIM B 第一段" {
			t.Fatalf("SIM B body contaminated: %q", got.Body)
		}
	}
}

func TestSaveConcatSMSUpgradesCompleteLegacyRowsInPlace(t *testing.T) {
	for _, test := range []struct {
		storage       string
		currentSource string
	}{
		{storage: "ME", currentSource: "cellular_at"},
		{storage: "SM", currentSource: "cellular_at"},
		{storage: "ME", currentSource: "cellular_qmi"},
		{storage: "SM", currentSource: "cellular_qmi"},
	} {
		t.Run(test.storage+"_to_"+test.currentSource, func(t *testing.T) {
			ctx := context.Background()
			database := openTestStore(t, ":memory:")
			mustSaveDevice(t, database, "ec20-1", "测试设备")
			const (
				imei         = "867394042309830"
				peer         = "+8520000"
				legacySource = "cellular_at"
				reference    = 77
			)
			at := time.Unix(1_700_100_000, 0).UTC()
			legacyMessageID := "concat:cellular_at:867394042309830:+8520000:77:2"
			legacy := insertLegacyConcatRow(
				t,
				database,
				legacyMessageID,
				"ec20-1",
				imei,
				"sim-a",
				peer,
				"part 1part 2",
				legacySource,
				2,
				at,
				legacyConcatExtra(t, test.storage, reference, 2, 2, map[string]string{
					"1": "part 1",
					"2": "part 2",
				}),
			)
			cursorBefore, err := database.LatestSMSMessageID(ctx)
			if err != nil {
				t.Fatal(err)
			}
			subscriberScope := "sim-a"
			incomingIMSI := "sim-a"
			if test.storage == "ME" {
				subscriberScope = "me:epoch-b"
				incomingIMSI = ""
			}
			scopedMessageID := StableConcatMessageID(
				test.currentSource, imei, "ec20-1", subscriberScope, peer, reference, 2,
			)
			save := func(sequence int, body, fingerprint, occurrenceID string) SMSMessage {
				t.Helper()
				saved, saveErr := database.SaveSMSMessage(ctx, SMSMessage{
					MessageID: scopedMessageID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: incomingIMSI,
					Peer: peer, Direction: "inbound", Body: body, Timestamp: at,
					Status: "received", Source: test.currentSource, PartsTotal: 2,
					Extra: physicalConcatExtra(t, test.storage, fingerprint, occurrenceID, reference, 2, sequence),
				})
				if saveErr != nil {
					t.Fatalf("SaveSMSMessage(seq=%d) error = %v", sequence, saveErr)
				}
				return saved
			}

			for sequence, part := range []string{"part 1", "part 2"} {
				slot := part[len(part)-1:]
				saved := save(
					sequence+1,
					part,
					"sha256:part-"+slot,
					test.storage+":slot-"+slot,
				)
				if saved.ID != legacy.ID || saved.MessageID != legacyMessageID ||
					saved.Body != legacy.Body || saved.Source != legacySource {
					t.Fatalf("legacy %s/%s rescan = %#v, want original %#v", test.storage, test.currentSource, saved, legacy)
				}
				cursorAfter, cursorErr := database.LatestSMSMessageID(ctx)
				if cursorErr != nil || cursorAfter != cursorBefore {
					t.Fatalf("cursor after legacy %s/%s rescan = %d, %v; want %d", test.storage, test.currentSource, cursorAfter, cursorErr, cursorBefore)
				}
				rows, listErr := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
				if listErr != nil || len(rows) != 1 {
					t.Fatalf("legacy %s/%s rows after rescan = %#v, %v", test.storage, test.currentSource, rows, listErr)
				}
			}

			upgraded, err := database.SMSMessage(ctx, legacy.ID)
			if err != nil {
				t.Fatal(err)
			}
			document, err := decodeJSONObject(upgraded.Extra)
			if err != nil {
				t.Fatal(err)
			}
			occurrenceIDs, _ := document["concat_occurrence_ids"].(map[string]any)
			fingerprints, _ := document["concat_fingerprints"].(map[string]any)
			if len(occurrenceIDs) != 2 || len(fingerprints) != 2 {
				t.Fatalf("upgraded legacy %s/%s identity maps = occurrences %#v fingerprints %#v", test.storage, test.currentSource, occurrenceIDs, fingerprints)
			}
			repeated := save(1, "part 1", "sha256:part-1", test.storage+":slot-1")
			if repeated.ID != legacy.ID || repeated.MessageID != legacyMessageID || repeated.Source != legacySource {
				t.Fatalf("repeated legacy %s/%s exact rescan = %#v", test.storage, test.currentSource, repeated)
			}
			if cursorAfter, err := database.LatestSMSMessageID(ctx); err != nil || cursorAfter != cursorBefore {
				t.Fatalf("cursor after repeated legacy %s/%s rescan = %d, %v; want %d", test.storage, test.currentSource, cursorAfter, err, cursorBefore)
			}

			// Equal bytes in another physical slot are not the retained row once
			// the legacy sequence has an exact occurrence owner.
			different := save(1, "part 1", "sha256:part-1", test.storage+":slot-99")
			if different.ID == legacy.ID || different.MessageID != scopedMessageID || different.Source != test.currentSource {
				t.Fatalf("different legacy %s/%s occurrence was swallowed: %#v", test.storage, test.currentSource, different)
			}
			rows, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
			if err != nil || len(rows) != 2 {
				t.Fatalf("legacy %s/%s rows after different occurrence = %#v, %v", test.storage, test.currentSource, rows, err)
			}
		})
	}
}

func TestSaveConcatSMSUpgradesLegacyPartialThenRoutesMissingSequenceToScopedRow(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const (
		imei      = "867394042309830"
		peer      = "+8520000"
		source    = "cellular_at"
		reference = 78
	)
	at := time.Unix(1_700_200_000, 0).UTC()
	legacyMessageID := "concat:cellular_at:867394042309830:+8520000:78:2"
	legacy := insertLegacyConcatRow(
		t,
		database,
		legacyMessageID,
		"ec20-1",
		imei,
		"sim-a",
		peer,
		"part 1",
		source,
		2,
		at,
		legacyConcatExtra(t, "ME", reference, 2, 1, map[string]string{"1": "part 1"}),
	)
	cursorBefore, err := database.LatestSMSMessageID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scopedMessageID := StableConcatMessageID(source, imei, "ec20-1", "me:epoch-b", peer, reference, 2)
	save := func(sequence int, body, occurrenceID string) SMSMessage {
		t.Helper()
		saved, saveErr := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: scopedMessageID, DeviceID: "ec20-1", ModemIMEI: imei,
			Peer: peer, Direction: "inbound", Body: body, Timestamp: at,
			Status: "received", Source: source, PartsTotal: 2,
			Extra: physicalConcatExtra(t, "ME", "sha256:"+body, occurrenceID, reference, 2, sequence),
		})
		if saveErr != nil {
			t.Fatalf("SaveSMSMessage(seq=%d) error = %v", sequence, saveErr)
		}
		return saved
	}

	rescanned := save(1, "part 1", "ME:slot-1")
	if rescanned.ID != legacy.ID || rescanned.MessageID != legacyMessageID {
		t.Fatalf("partial legacy rescan = %#v, want original %#v", rescanned, legacy)
	}
	if cursorAfter, err := database.LatestSMSMessageID(ctx); err != nil || cursorAfter != cursorBefore {
		t.Fatalf("partial legacy cursor = %d, %v; want %d", cursorAfter, err, cursorBefore)
	}
	if rows, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"}); err != nil || len(rows) != 1 {
		t.Fatalf("partial legacy rows after rescan = %#v, %v", rows, err)
	}

	missing := save(2, "part 2", "ME:slot-2")
	if missing.ID == legacy.ID || missing.MessageID != scopedMessageID || missing.Body != "part 2" {
		t.Fatalf("missing legacy sequence was merged across subscriber scope: %#v", missing)
	}
	rows, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("partial legacy rows after missing sequence = %#v, %v", rows, err)
	}
	storedLegacy, err := database.SMSMessage(ctx, legacy.ID)
	if err != nil || storedLegacy.Body != "part 1" || storedLegacy.MessageID != legacyMessageID {
		t.Fatalf("partial legacy row changed after safe split: %#v, %v", storedLegacy, err)
	}
}

func TestSaveConcatSMSLegacySMRequiresSameSubscriber(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const (
		imei          = "867394042309830"
		peer          = "+8520000"
		legacySource  = "cellular_at"
		currentSource = "cellular_qmi"
		reference     = 80
	)
	at := time.Unix(1_700_250_000, 0).UTC()
	legacyMessageID := "concat:cellular_at:867394042309830:+8520000:80:2"
	legacy := insertLegacyConcatRow(
		t,
		database,
		legacyMessageID,
		"ec20-1",
		imei,
		"sim-a",
		peer,
		"same part",
		legacySource,
		2,
		at,
		legacyConcatExtra(t, "SM", reference, 2, 1, map[string]string{"1": "same part"}),
	)
	scopedMessageID := StableConcatMessageID(currentSource, imei, "ec20-1", "sim-b", peer, reference, 2)
	saved, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: scopedMessageID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: "sim-b",
		Peer: peer, Direction: "inbound", Body: "same part", Timestamp: at,
		Status: "received", Source: currentSource, PartsTotal: 2,
		Extra: physicalConcatExtra(t, "SM", "sha256:same-part", "SM:slot-1:same-part", reference, 2, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == legacy.ID || saved.MessageID != scopedMessageID || saved.IMSI != "sim-b" {
		t.Fatalf("cross-subscriber legacy SM segment reused old row: %#v", saved)
	}
	rows, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("legacy SM rows across subscribers = %#v, %v", rows, err)
	}
	storedLegacy, err := database.SMSMessage(ctx, legacy.ID)
	if err != nil || storedLegacy.IMSI != "sim-a" || storedLegacy.MessageID != legacyMessageID {
		t.Fatalf("legacy SM owner changed: %#v, %v", storedLegacy, err)
	}
}

func TestSaveConcatSMSLegacyBodyFallbackRequiresStableTimestamp(t *testing.T) {
	base := time.Unix(1_700_300_000, 0).UTC()
	for _, test := range []struct {
		name     string
		incoming time.Time
	}{
		{name: "missing timestamp"},
		{name: "distant timestamp", incoming: base.Add(10 * time.Minute)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database := openTestStore(t, ":memory:")
			mustSaveDevice(t, database, "ec20-1", "测试设备")
			const (
				imei      = "867394042309830"
				peer      = "+8520000"
				source    = "cellular_at"
				reference = 79
			)
			legacy := insertLegacyConcatRow(
				t,
				database,
				"concat:cellular_at:867394042309830:+8520000:79:2",
				"ec20-1",
				imei,
				"sim-a",
				peer,
				"same body",
				source,
				2,
				base,
				legacyConcatExtra(t, "ME", reference, 2, 1, map[string]string{"1": "same body"}),
			)
			scopedMessageID := StableConcatMessageID(source, imei, "ec20-1", "me:epoch-b", peer, reference, 2)
			saved, err := database.SaveSMSMessage(ctx, SMSMessage{
				MessageID: scopedMessageID, DeviceID: "ec20-1", ModemIMEI: imei,
				Peer: peer, Direction: "inbound", Body: "same body", Timestamp: test.incoming,
				Status: "received", Source: source, PartsTotal: 2,
				Extra: physicalConcatExtra(t, "ME", "sha256:same-body", "ME:slot-9", reference, 2, 1),
			})
			if err != nil {
				t.Fatal(err)
			}
			if saved.ID == legacy.ID || saved.MessageID != scopedMessageID {
				t.Fatalf("unreliable body-only legacy match reused old row: %#v", saved)
			}
			rows, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
			if err != nil || len(rows) != 2 {
				t.Fatalf("rows after unreliable legacy match = %#v, %v", rows, err)
			}
		})
	}
}

func TestSaveConcatSMSUsesExactMEOccurrenceAcrossSubscriberEpochs(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const (
		imei      = "867394042309830"
		peer      = "+8520000"
		reference = 73
	)
	epochA := StableConcatMessageID("cellular_qmi", imei, "ec20-1", "me:epoch-a", peer, reference, 2)
	epochB := StableConcatMessageID("cellular_qmi", imei, "ec20-1", "me:epoch-b", peer, reference, 2)
	base := time.Unix(1_700_000_000, 0).UTC()

	save := func(messageID, imsi, body, fingerprint, occurrenceID string, sequence int, at time.Time) SMSMessage {
		t.Helper()
		saved, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: messageID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: imsi,
			Peer: peer, Direction: "inbound", Body: body, Timestamp: at,
			Status: "received", Source: "cellular_qmi", PartsTotal: 2,
			Extra: physicalConcatExtra(t, "ME", fingerprint, occurrenceID, reference, 2, sequence),
		})
		if err != nil {
			t.Fatalf("SaveSMSMessage(id=%q seq=%d) error = %v", messageID, sequence, err)
		}
		return saved
	}

	original := save(epochA, "sim-a", "shared part 1", "sha256:same-pdu", "ME:slot-20:same-pdu", 1, base)
	retained := save(epochB, "", "shared part 1", "sha256:same-pdu", "ME:slot-20:same-pdu", 1, base.Add(24*time.Hour))
	if retained.ID != original.ID || retained.MessageID != epochA || retained.IMSI != "sim-a" {
		t.Fatalf("retained ME segment = %#v, want original row %#v", retained, original)
	}
	if messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"}); err != nil || len(messages) != 1 {
		t.Fatalf("rows after retained segment = %#v, %v; want only original", messages, err)
	}

	// A different physical segment is not absorbed by the old epoch. It can
	// start and complete an independent occurrence in SIM B's subscriber epoch.
	second := save(epochB, "", "SIM B part 2", "sha256:sim-b-2", "ME:slot-21:sim-b-2", 2, base.Add(24*time.Hour+time.Minute))
	if second.ID == original.ID || second.MessageID != epochB || second.Body != "SIM B part 2" {
		t.Fatalf("new SIM B segment = %#v, want a separate epoch-B row", second)
	}
	// SIM B's sequence one has byte-identical PDU content to SIM A's sequence
	// one, but a different modem slot occurrence. It must complete epoch B rather
	// than being swallowed by fingerprint-only cross-epoch dedupe.
	complete := save(epochB, "", "shared part 1", "sha256:same-pdu", "ME:slot-22:same-pdu", 1, base.Add(24*time.Hour+2*time.Minute))
	if complete.MessageID != epochB || complete.Body != "shared part 1SIM B part 2" ||
		!ConcatSMSReadyToNotify(complete.MessageID, complete.Extra) {
		t.Fatalf("completed SIM B occurrence = %#v", complete)
	}

	messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("cross-epoch ME rows = %#v, want original A plus new B occurrence", messages)
	}
	byID := make(map[string]SMSMessage, len(messages))
	for _, message := range messages {
		byID[message.MessageID] = message
	}
	if byID[epochA].Body != "shared part 1" || byID[epochA].IMSI != "sim-a" ||
		byID[epochB].Body != "shared part 1SIM B part 2" {
		t.Fatalf("cross-epoch ME assemblies = %#v", messages)
	}
}

func TestSaveConcatSMSDoesNotDedupeSMPhysicalSegmentAcrossSubscriberEpochs(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const (
		imei      = "867394042309830"
		peer      = "+8520000"
		reference = 74
	)

	var saved []SMSMessage
	for _, subscriber := range []string{"sim-a", "sim-b"} {
		message, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: StableConcatMessageID("cellular_qmi", imei, "ec20-1", subscriber, peer, reference, 2),
			DeviceID:  "ec20-1", ModemIMEI: imei, IMSI: subscriber,
			Peer: peer, Direction: "inbound", Body: "same SIM-stored segment",
			Status: "received", Source: "cellular_qmi", PartsTotal: 2,
			Extra: physicalConcatExtra(t, "SM", "sha256:same-sm-pdu", "SM:slot-1:same-pdu", reference, 2, 1),
		})
		if err != nil {
			t.Fatalf("SaveSMSMessage(IMSI=%s) error = %v", subscriber, err)
		}
		saved = append(saved, message)
	}
	if saved[0].ID == saved[1].ID || saved[0].MessageID == saved[1].MessageID {
		t.Fatalf("SM segments crossed subscriber epochs: %#v", saved)
	}
	messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil || len(messages) != 2 {
		t.Fatalf("SM rows = %#v, %v; want one per subscriber", messages, err)
	}
}

func TestSaveConcatSMSPreservesFirstSubscriberAttributionWhenReinserted(t *testing.T) {
	for _, test := range []struct {
		name         string
		existingIMSI string
		incomingIMSI string
		wantIMSI     string
	}{
		{
			name:         "unattributed first segment remains unattributed",
			existingIMSI: "",
			incomingIMSI: "sim-b",
			wantIMSI:     "",
		},
		{
			name:         "attributed first segment keeps original subscriber",
			existingIMSI: "sim-a",
			incomingIMSI: "",
			wantIMSI:     "sim-a",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database := openTestStore(t, ":memory:")
			mustSaveDevice(t, database, "ec20-1", "测试设备")
			const (
				imei      = "867394042309830"
				peer      = "+8520000"
				reference = 75
			)
			messageID := StableConcatMessageID(
				"cellular_qmi", imei, "ec20-1", "me:epoch", peer, reference, 2,
			)

			first, err := database.SaveSMSMessage(ctx, SMSMessage{
				MessageID: messageID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: test.existingIMSI,
				Peer: peer, Direction: "inbound", Body: "part 1",
				Status: "received", Source: "cellular_qmi", PartsTotal: 2,
				Extra: physicalConcatExtra(t, "ME", "sha256:part-1", "ME:slot-30:part-1", reference, 2, 1),
			})
			if err != nil {
				t.Fatal(err)
			}
			completed, err := database.SaveSMSMessage(ctx, SMSMessage{
				MessageID: messageID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: test.incomingIMSI,
				Peer: peer, Direction: "inbound", Body: "part 2",
				Status: "received", Source: "cellular_qmi", PartsTotal: 2,
				Extra: physicalConcatExtra(t, "ME", "sha256:part-2", "ME:slot-31:part-2", reference, 2, 2),
			})
			if err != nil {
				t.Fatal(err)
			}
			if completed.ID <= first.ID {
				t.Fatalf("concat row was not reinserted: first=%d completed=%d", first.ID, completed.ID)
			}
			if completed.IMSI != test.wantIMSI {
				t.Fatalf("completed IMSI = %q, want first attribution %q", completed.IMSI, test.wantIMSI)
			}
		})
	}
}

func TestMarkSMSReadDoesNotReassembleConcatBody(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const (
		imei = "867394042309830"
		imsi = "45400"
	)
	messageID := StableConcatMessageID("cellular_at", imei, "ec20-1", imsi, "+8520000", 9, 2)

	var complete SMSMessage
	for sequence, body := range []string{"第一段", "第二段"} {
		saved, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: messageID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: imsi,
			Peer: "+8520000", Direction: "inbound", Body: body,
			Status: "received", Source: "cellular_at", PartsTotal: 2,
			Extra: concatExtra(t, 9, 2, sequence+1),
		})
		if err != nil {
			t.Fatalf("SaveSMSMessage(seq=%d) error = %v", sequence+1, err)
		}
		complete = saved
	}
	if complete.Body != "第一段第二段" {
		t.Fatalf("assembled body before mark read = %q", complete.Body)
	}

	if err := database.MarkSMSRead(ctx, complete.ID); err != nil {
		t.Fatalf("MarkSMSRead() error = %v", err)
	}
	marked, err := database.SMSMessage(ctx, complete.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !marked.Read {
		t.Fatal("message remained unread")
	}
	if marked.ID != complete.ID {
		t.Fatalf("mark read changed durable id: got %d, want %d", marked.ID, complete.ID)
	}
	if marked.Body != complete.Body {
		t.Fatalf("mark read changed concat body: got %q, want %q", marked.Body, complete.Body)
	}
	if string(marked.Extra) != string(complete.Extra) {
		t.Fatalf("mark read changed concat metadata: got %s, want %s", marked.Extra, complete.Extra)
	}
}
