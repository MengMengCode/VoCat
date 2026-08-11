package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func releasedStorageMessageID(storage string, index int, rawPDU string) string {
	digest := sha256.Sum256([]byte(rawPDU))
	return fmt.Sprintf("modem:%s:%d:%s", storage, index, hex.EncodeToString(digest[:8]))
}

func currentStorageOccurrenceID(source, storage string, index int, rawPDU string) string {
	digest := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(rawPDU))))
	return fmt.Sprintf(
		"modem:%s:0123456789abcdef:%s:%d:%s",
		source,
		storage,
		index,
		hex.EncodeToString(digest[:8]),
	)
}

func currentStorageFingerprint(rawPDU string) string {
	digest := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(rawPDU))))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func legacyStorageExtra(t *testing.T, storage string, index int) json.RawMessage {
	return legacyStorageExtraWithMetadata(t, storage, index, "gsm7_pdu", 0)
}

func legacyStorageExtraWithMetadata(
	t *testing.T,
	storage string,
	index int,
	encoding string,
	dataCodingScheme int,
) json.RawMessage {
	t.Helper()
	extra, err := json.Marshal(map[string]any{
		"storage":            storage,
		"modem_index":        index,
		"storage_status":     "received_unread",
		"encoding":           encoding,
		"data_coding_scheme": dataCodingScheme,
	})
	if err != nil {
		t.Fatalf("marshal legacy storage extra: %v", err)
	}
	return extra
}

func currentStorageExtra(
	t *testing.T,
	storage string,
	index int,
	rawPDU string,
	occurrenceID string,
) json.RawMessage {
	return currentStorageExtraWithMetadata(
		t,
		storage,
		index,
		rawPDU,
		occurrenceID,
		"gsm7_pdu",
		0,
	)
}

func currentStorageExtraWithMetadata(
	t *testing.T,
	storage string,
	index int,
	rawPDU string,
	occurrenceID string,
	encoding string,
	dataCodingScheme int,
) json.RawMessage {
	t.Helper()
	extra, err := json.Marshal(map[string]any{
		"storage":               storage,
		"modem_index":           index,
		"raw_pdu":               rawPDU,
		"segment_occurrence_id": occurrenceID,
		"segment_fingerprint":   currentStorageFingerprint(rawPDU),
		"encoding":              encoding,
		"data_coding_scheme":    dataCodingScheme,
	})
	if err != nil {
		t.Fatalf("marshal current storage extra: %v", err)
	}
	return extra
}

func insertReleasedStorageMessage(
	t *testing.T,
	database *Store,
	messageID string,
	deviceID string,
	modemIMEI string,
	imsi string,
	peer string,
	body string,
	storage string,
	index int,
	at time.Time,
) SMSMessage {
	return insertReleasedStorageMessageWithDirection(
		t,
		database,
		messageID,
		deviceID,
		modemIMEI,
		imsi,
		peer,
		body,
		storage,
		index,
		"inbound",
		at,
	)
}

func insertReleasedStorageMessageWithDirection(
	t *testing.T,
	database *Store,
	messageID string,
	deviceID string,
	modemIMEI string,
	imsi string,
	peer string,
	body string,
	storage string,
	index int,
	direction string,
	at time.Time,
) SMSMessage {
	t.Helper()
	result, err := database.db.ExecContext(context.Background(), `
		INSERT INTO sms_messages (
			message_id, device_id, modem_imei, imsi, peer, direction, body,
			message_time, status, source, parts_total, delivery_state, is_read,
			extra_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'received', 'cellular_at', 1, '', 0, ?, ?, ?)
	`,
		messageID,
		deviceID,
		modemIMEI,
		imsi,
		peer,
		direction,
		body,
		at.Unix(),
		string(legacyStorageExtra(t, storage, index)),
		at.Unix(),
		at.Unix(),
	)
	if err != nil {
		t.Fatalf("insert released storage message: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("released storage message id: %v", err)
	}
	message, err := database.SMSMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("read released storage message: %v", err)
	}
	return message
}

func TestSaveStorageSMSAdoptsExactReleasedOutboundRow(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "测试设备")
	const (
		imei    = "867394042309830"
		peer    = "+8520000"
		storage = "ME"
		index   = 6
		rawPDU  = "001122AABB"
	)
	at := time.Unix(1_700_350_000, 0).UTC()
	legacyMessageID := releasedStorageMessageID(storage, index, rawPDU)
	legacy := insertReleasedStorageMessageWithDirection(
		t,
		database,
		legacyMessageID,
		"ec20-1",
		imei,
		"sim-a",
		peer,
		"submitted message",
		storage,
		index,
		"outbound",
		at,
	)
	occurrenceID := currentStorageOccurrenceID("cellular_at", storage, index, rawPDU)
	adopted, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: occurrenceID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: "sim-b",
		Peer: peer, Direction: "outbound", Body: "submitted message",
		Status: "sent", Source: "cellular_at", PartsTotal: 1, Read: true,
		Extra: currentStorageExtraWithMetadata(
			t,
			storage,
			index,
			rawPDU,
			occurrenceID,
			"ucs2_pdu",
			8,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.ID != legacy.ID || adopted.MessageID != legacyMessageID || adopted.Source != "cellular_at" {
		t.Fatalf("exact released outbound row was not adopted: %#v", adopted)
	}
	if latest, err := database.LatestSMSMessageID(ctx); err != nil || latest != legacy.ID {
		t.Fatalf("outbound adoption cursor = %d, %v; want %d", latest, err, legacy.ID)
	}
}

func TestSaveStorageSMSAdoptsReleasedRowsInPlace(t *testing.T) {
	// This SMS-DELIVER decodes to HELLO with SCTS 2024-01-02 03:04:05 UTC.
	// QMI WMS can return the same message as a direct TPDU without the AT
	// SMSC-length octet, so the released raw digest is necessarily different.
	const (
		atFullPDU     = "000405912143F500004210203040500005C82293F904"
		qmiDirectTPDU = "0405912143F500004210203040500005C82293F904"
	)
	for _, test := range []struct {
		storage       string
		currentSource string
		legacyRaw     string
		currentRaw    string
	}{
		{
			storage:       "ME",
			currentSource: "cellular_at",
			legacyRaw:     atFullPDU,
			currentRaw:    strings.ToLower(atFullPDU),
		},
		{
			storage:       "SM",
			currentSource: "cellular_at",
			legacyRaw:     atFullPDU,
			currentRaw:    strings.ToLower(atFullPDU),
		},
		{
			storage:       "ME",
			currentSource: "cellular_qmi",
			legacyRaw:     atFullPDU,
			currentRaw:    qmiDirectTPDU,
		},
		{
			storage:       "SM",
			currentSource: "cellular_qmi",
			legacyRaw:     atFullPDU,
			currentRaw:    qmiDirectTPDU,
		},
	} {
		t.Run(test.storage+"_to_"+test.currentSource, func(t *testing.T) {
			ctx := context.Background()
			database := openTestStore(t, ":memory:")
			mustSaveDevice(t, database, "ec20-1", "测试设备")
			const (
				imei  = "867394042309830"
				peer  = "+8520000"
				index = 7
			)
			at := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
			legacyMessageID := releasedStorageMessageID(test.storage, index, test.legacyRaw)
			legacy := insertReleasedStorageMessage(
				t,
				database,
				legacyMessageID,
				"ec20-1",
				imei,
				"sim-a",
				peer,
				"HELLO",
				test.storage,
				index,
				at,
			)
			cursorBefore, err := database.LatestSMSMessageID(ctx)
			if err != nil {
				t.Fatal(err)
			}
			occurrenceID := currentStorageOccurrenceID(
				test.currentSource,
				test.storage,
				index,
				test.currentRaw,
			)
			if test.currentSource == "cellular_qmi" {
				descriptor := currentStorageSMSDescriptor{
					storage: test.storage, index: index, rawPDU: test.currentRaw,
				}
				for _, candidate := range legacyStorageMessageIDCandidates(descriptor) {
					if candidate == legacyMessageID {
						t.Fatal("QMI representation unexpectedly matched the released AT digest; prefix fallback was not exercised")
					}
				}
			}
			incomingIMSI := "sim-b"
			if test.storage == "SM" {
				incomingIMSI = "sim-a"
			}
			save := func(messageID, rawPDU string, timestamp time.Time, read bool) SMSMessage {
				t.Helper()
				saved, saveErr := database.SaveSMSMessage(ctx, SMSMessage{
					MessageID: messageID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: incomingIMSI,
					Peer: peer, Direction: "inbound", Body: "HELLO", Timestamp: timestamp,
					Status: "received", Source: test.currentSource, PartsTotal: 1, Read: read,
					Extra: currentStorageExtra(t, test.storage, index, rawPDU, messageID),
				})
				if saveErr != nil {
					t.Fatal(saveErr)
				}
				return saved
			}

			adopted := save(occurrenceID, test.currentRaw, at, false)
			if adopted.ID != legacy.ID || adopted.MessageID != legacyMessageID ||
				adopted.Source != "cellular_at" || adopted.IMSI != "sim-a" {
				t.Fatalf("adopted storage row = %#v, want released row %#v", adopted, legacy)
			}
			if cursorAfter, cursorErr := database.LatestSMSMessageID(ctx); cursorErr != nil || cursorAfter != cursorBefore {
				t.Fatalf("cursor after adoption = %d, %v; want %d", cursorAfter, cursorErr, cursorBefore)
			}
			if fresh, listErr := database.ListInboundSMSAfterID(ctx, cursorBefore, 10); listErr != nil || len(fresh) != 0 {
				t.Fatalf("adoption advanced inbound cursor: %#v, %v", fresh, listErr)
			}
			if rows, listErr := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"}); listErr != nil || len(rows) != 1 {
				t.Fatalf("rows after adoption = %#v, %v", rows, listErr)
			}
			document, err := decodeJSONObject(adopted.Extra)
			if err != nil {
				t.Fatal(err)
			}
			if document["segment_occurrence_id"] != occurrenceID ||
				document["segment_fingerprint"] != currentStorageFingerprint(test.currentRaw) ||
				document["raw_pdu"] != test.currentRaw {
				t.Fatalf("backfilled storage identity = %#v", document)
			}

			repeated := save(occurrenceID, test.currentRaw, at.Add(24*time.Hour), false)
			if repeated.ID != legacy.ID || repeated.MessageID != legacyMessageID || repeated.Source != "cellular_at" {
				t.Fatalf("repeated storage scan = %#v, want released row", repeated)
			}

			// Once the released row has an exact owner, equal bytes under a
			// different occurrence id must create the current durable row.
			differentID := strings.Replace(
				occurrenceID,
				"0123456789abcdef",
				"fedcba9876543210",
				1,
			)
			different := save(differentID, test.currentRaw, at, false)
			if different.ID == legacy.ID || different.MessageID != differentID || different.Source != test.currentSource {
				t.Fatalf("different occurrence was adopted: %#v", different)
			}
			if rows, listErr := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"}); listErr != nil || len(rows) != 2 {
				t.Fatalf("rows after different occurrence = %#v, %v", rows, listErr)
			}

			// The current id now exists. Its repeat must take the normal upsert
			// path instead of falling back to the released owner.
			updated := save(differentID, test.currentRaw, at, true)
			if updated.ID != different.ID || updated.ID == legacy.ID || !updated.Read {
				t.Fatalf("current occurrence repeat bypassed normal upsert: %#v", updated)
			}

			differentRaw := "FFEEDDCCBBAA"
			differentPduID := currentStorageOccurrenceID(
				test.currentSource,
				test.storage,
				index,
				differentRaw,
			)
			differentPDU := save(differentPduID, differentRaw, at, false)
			if differentPDU.ID == legacy.ID || differentPDU.MessageID != differentPduID ||
				differentPDU.Source != test.currentSource {
				t.Fatalf("different PDU was adopted: %#v", differentPDU)
			}
			if rows, listErr := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"}); listErr != nil || len(rows) != 3 {
				t.Fatalf("rows after different PDU = %#v, %v", rows, listErr)
			}
		})
	}
}

func TestSaveStorageSMSRejectsUnsafeLegacyOwners(t *testing.T) {
	base := time.Unix(1_700_500_000, 0).UTC()
	for _, test := range []struct {
		name         string
		storage      string
		incomingIMSI string
		index        int
		rawPDU       string
		body         string
		timestamp    time.Time
		encoding     string
		dcs          int
	}{
		{
			name:         "SM subscriber changed",
			storage:      "SM",
			incomingIMSI: "sim-b",
			index:        7,
			rawPDU:       "001122AABB",
			body:         "retained message",
			timestamp:    base,
		},
		{
			name:         "slot changed",
			storage:      "ME",
			incomingIMSI: "sim-b",
			index:        8,
			rawPDU:       "001122AABB",
			body:         "retained message",
			timestamp:    base,
		},
		{
			name:         "PDU and body changed",
			storage:      "ME",
			incomingIMSI: "sim-b",
			index:        7,
			rawPDU:       "FFEEDDCCBB",
			body:         "different message",
			timestamp:    base,
		},
		{
			name:         "same body but nearby SCTS",
			storage:      "ME",
			incomingIMSI: "sim-b",
			index:        7,
			rawPDU:       "FFEEDDCCBB",
			body:         "retained message",
			timestamp:    base.Add(2 * time.Minute),
		},
		{
			name:         "same SCTS but encoding changed",
			storage:      "ME",
			incomingIMSI: "sim-b",
			index:        7,
			rawPDU:       "FFEEDDCCBB",
			body:         "retained message",
			timestamp:    base,
			encoding:     "ucs2_pdu",
		},
		{
			name:         "same SCTS but data coding scheme changed",
			storage:      "ME",
			incomingIMSI: "sim-b",
			index:        7,
			rawPDU:       "FFEEDDCCBB",
			body:         "retained message",
			timestamp:    base,
			dcs:          8,
		},
		{
			name:         "timestamp unavailable",
			storage:      "ME",
			incomingIMSI: "sim-b",
			index:        7,
			rawPDU:       "001122AABB",
			body:         "retained message",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database := openTestStore(t, ":memory:")
			mustSaveDevice(t, database, "ec20-1", "测试设备")
			const (
				imei      = "867394042309830"
				peer      = "+8520000"
				legacyRaw = "0791001122AABB"
			)
			legacyMessageID := releasedStorageMessageID(test.storage, 7, legacyRaw)
			legacy := insertReleasedStorageMessage(
				t,
				database,
				legacyMessageID,
				"ec20-1",
				imei,
				"sim-a",
				peer,
				"retained message",
				test.storage,
				7,
				base,
			)
			occurrenceID := currentStorageOccurrenceID("cellular_qmi", test.storage, test.index, test.rawPDU)
			encoding := test.encoding
			if encoding == "" {
				encoding = "gsm7_pdu"
			}
			saved, err := database.SaveSMSMessage(ctx, SMSMessage{
				MessageID: occurrenceID, DeviceID: "ec20-1", ModemIMEI: imei, IMSI: test.incomingIMSI,
				Peer: peer, Direction: "inbound", Body: test.body, Timestamp: test.timestamp,
				Status: "received", Source: "cellular_qmi", PartsTotal: 1,
				Extra: currentStorageExtraWithMetadata(
					t,
					test.storage,
					test.index,
					test.rawPDU,
					occurrenceID,
					encoding,
					test.dcs,
				),
			})
			if err != nil {
				t.Fatal(err)
			}
			if saved.ID == legacy.ID || saved.MessageID != occurrenceID || saved.Source != "cellular_qmi" {
				t.Fatalf("unsafe legacy owner was adopted: %#v", saved)
			}
			rows, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
			if err != nil || len(rows) != 2 {
				t.Fatalf("rows after rejected legacy owner = %#v, %v", rows, err)
			}
			storedLegacy, err := database.SMSMessage(ctx, legacy.ID)
			if err != nil || storedLegacy.MessageID != legacyMessageID || storedLegacy.Source != "cellular_at" {
				t.Fatalf("released row changed after rejection: %#v, %v", storedLegacy, err)
			}
		})
	}
}

func TestLegacyStorageMessageIDCandidatesNormalizeHexCase(t *testing.T) {
	descriptor := currentStorageSMSDescriptor{storage: "ME", index: 3, rawPDU: " aabb "}
	want := releasedStorageMessageID("ME", 3, "AABB")
	for _, candidate := range legacyStorageMessageIDCandidates(descriptor) {
		if candidate == want {
			return
		}
	}
	t.Fatalf("legacy candidates for normalized hex did not include %q", want)
}

func TestCurrentStorageOccurrenceIDLooksCurrent(t *testing.T) {
	id := currentStorageOccurrenceID("cellular_qmi", "ME", 7, "AABB")
	parts := strings.Split(id, ":")
	if len(parts) != 6 || parts[0] != "modem" || parts[1] != "cellular_qmi" ||
		parts[3] != "ME" || parts[4] != strconv.Itoa(7) {
		t.Fatalf("current occurrence id = %q", id)
	}
}
