package device

import (
	"bytes"
	"testing"
)

func TestPositiveIntegerEncodingRoundTripsFullUint64Range(t *testing.T) {
	for _, value := range []uint64{0, 1, 127, 128, 255, 256, ^uint64(0)} {
		encoded := encodePositiveInteger(value)
		decoded, ok := decodePositiveInteger(encoded)
		if !ok || decoded != value {
			t.Errorf("round trip %d: encoded=%X decoded=%d ok=%t", value, encoded, decoded, ok)
		}
	}
}

func testNotificationMetadata(t *testing.T, sequence byte, event []byte, address, iccid string) []byte {
	t.Helper()
	iccidBCD, err := encodeICCID(iccid)
	if err != nil {
		t.Fatal(err)
	}
	return derConstruct(0xBF2F,
		derEncode(0x80, []byte{sequence}),
		derEncode(0x81, event),
		derEncode(0x0C, []byte(address)),
		derEncode(0x5A, iccidBCD),
	)
}

func TestParsePendingNotifications(t *testing.T) {
	installMetadata := testNotificationMetadata(t, 7, []byte{7, 0x80}, "install.example.com", "8944476500017228672")
	install := derConstruct(0xBF37, derConstruct(0xBF27, installMetadata))
	deleteMetadata := testNotificationMetadata(t, 9, []byte{4, 0x10}, "delete.example.com", "89441000400128014257")
	deleted := derConstruct(0x30, deleteMetadata, derEncode(0x5F37, []byte{1, 2, 3}))

	notifications, err := parsePendingNotifications(derConstruct(0xBF2B, derConstruct(0xA0, install, deleted)))
	if err != nil {
		t.Fatalf("parsePendingNotifications: %v", err)
	}
	if len(notifications) != 2 {
		t.Fatalf("notifications = %#v", notifications)
	}
	// Results are grouped by receiver, then sorted by sequence number.
	if got := notifications[0]; got.SequenceNumber != 9 || got.Event != "delete" ||
		got.Address != "delete.example.com" || got.ICCID != "89441000400128014257" || !bytes.Equal(got.raw, deleted) {
		t.Fatalf("delete notification = %#v, raw=%X", got, got.raw)
	}
	if got := notifications[1]; got.SequenceNumber != 7 || got.Event != "install" ||
		got.Address != "install.example.com" || got.ICCID != "8944476500017228672" || !bytes.Equal(got.raw, install) {
		t.Fatalf("install notification = %#v, raw=%X", got, got.raw)
	}

	metadata, err := parseNotificationMetadataList(derConstruct(0xBF28, derConstruct(0xA0, installMetadata, deleteMetadata)))
	if err != nil || len(metadata) != 2 {
		t.Fatalf("parseNotificationMetadataList = %#v, %v", metadata, err)
	}
	if metadata[0].SequenceNumber != 9 || metadata[0].Event != "delete" || len(metadata[0].raw) != 0 {
		t.Fatalf("listed metadata = %#v", metadata[0])
	}
}

func TestNotificationRequestsAndRemoveResult(t *testing.T) {
	if got := buildListNotificationsRequest(); !bytes.Equal(got, []byte{0xBF, 0x28, 0x00}) {
		t.Fatalf("list request = %X", got)
	}
	if got := buildRetrieveNotificationsRequest(nil); !bytes.Equal(got, []byte{0xBF, 0x2B, 0x00}) {
		t.Fatalf("retrieve all request = %X", got)
	}
	sequenceNumber := uint64(128)
	wantRetrieve := []byte{0xBF, 0x2B, 0x04, 0x80, 0x02, 0x00, 0x80}
	if got := buildRetrieveNotificationsRequest(&sequenceNumber); !bytes.Equal(got, wantRetrieve) {
		t.Fatalf("retrieve request = %X, want %X", got, wantRetrieve)
	}
	wantRemove := []byte{0xBF, 0x30, 0x04, 0x80, 0x02, 0x00, 0x80}
	if got := buildRemoveNotificationRequest(sequenceNumber); !bytes.Equal(got, wantRemove) {
		t.Fatalf("remove request = %X, want %X", got, wantRemove)
	}
	if err := removeNotificationResult([]byte{0xBF, 0x30, 0x03, 0x80, 0x01, 0x00}); err != nil {
		t.Fatalf("removeNotificationResult(ok): %v", err)
	}
	if err := removeNotificationResult([]byte{0xBF, 0x30, 0x03, 0x80, 0x01, 0x7F}); err == nil {
		t.Fatal("undefinedError response was accepted")
	}
}

func TestParsePendingNotificationsRejectsMalformedMetadata(t *testing.T) {
	missingAddress := derConstruct(0x30, derConstruct(0xBF2F,
		derEncode(0x80, []byte{1}),
		derEncode(0x81, []byte{4, 0x10}),
	))
	if _, err := parsePendingNotifications(derConstruct(0xBF2B, missingAddress)); err == nil {
		t.Fatal("notification without receiver address was accepted")
	}
}
