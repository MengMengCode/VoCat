package pcsc

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type scriptedReply struct {
	data []byte
	sw   uint16
}

type scriptedCard struct {
	replies []scriptedReply
	calls   [][]byte
}

type unavailableReaderBackend struct{}

func (unavailableReaderBackend) Readers(context.Context) ([]Reader, error) {
	return []Reader{{Name: "ACR38", USBPath: "2-1", DiscoveryIssue: "pcsc_service_unavailable"}}, nil
}

func (unavailableReaderBackend) Open(context.Context, Selector) (Card, error) {
	return nil, errors.New("Open must not be called for a diagnostic-only reader")
}

func (card *scriptedCard) Transmit(_ context.Context, command []byte) ([]byte, uint16, error) {
	card.calls = append(card.calls, append([]byte(nil), command...))
	if len(card.replies) == 0 {
		return nil, 0, errors.New("unexpected APDU")
	}
	reply := card.replies[0]
	card.replies = card.replies[1:]
	return append([]byte(nil), reply.data...), reply.sw, nil
}

func (*scriptedCard) Close() error { return nil }

func TestDecodeIdentifiers(t *testing.T) {
	if got := decodeSwappedBCD([]byte{0x98, 0x10, 0x32, 0x54, 0xF6}, false); got != "890123456" {
		t.Fatalf("ICCID BCD = %q", got)
	}
	imsi, err := decodeIMSI([]byte{0x08, 0x19, 0x32, 0x54, 0x76, 0x98, 0x10, 0x32, 0x54})
	if err != nil {
		t.Fatal(err)
	}
	if imsi != "123456789012345" {
		t.Fatalf("IMSI = %q", imsi)
	}
}

func TestVerifyPINRefusesLowAttemptCount(t *testing.T) {
	card := &scriptedCard{replies: []scriptedReply{{sw: 0x63C2}}}
	err := verifyPIN(context.Background(), card, "1234")
	if !errors.Is(err, ErrPINTriesLow) {
		t.Fatalf("error = %v", err)
	}
	if len(card.calls) != 1 {
		t.Fatalf("APDU calls = %d, PIN must not be submitted", len(card.calls))
	}
}

func TestParseAKAResponse(t *testing.T) {
	data := []byte{0xDB, 0x08, 1, 2, 3, 4, 5, 6, 7, 8, 0x10}
	data = append(data, bytes.Repeat([]byte{0xAA}, 16)...)
	data = append(data, 0x10)
	data = append(data, bytes.Repeat([]byte{0xBB}, 16)...)
	result, err := parseAKAResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RES) != 8 || len(result.CK) != 16 || len(result.IK) != 16 || result.SynchronizationFailure {
		t.Fatalf("unexpected AKA result: %#v", result)
	}

	syncResult, err := parseAKAResponse(append([]byte{0xDC, 0x0E}, bytes.Repeat([]byte{0xCC}, 14)...))
	if err != nil {
		t.Fatal(err)
	}
	if !syncResult.SynchronizationFailure || len(syncResult.AUTS) != 14 {
		t.Fatalf("unexpected sync result: %#v", syncResult)
	}
}

func TestDeviceIDUsesStableUSBPath(t *testing.T) {
	a := DeviceID(Reader{Name: "reader 00 00", USBPath: "1-3"})
	b := DeviceID(Reader{Name: "renamed reader", USBPath: "1-3"})
	if a != b || a == "" {
		t.Fatalf("device IDs = %q, %q", a, b)
	}
}

func TestSnapshotRejectsPhysicalReaderUntilPCSCDIsReady(t *testing.T) {
	service := NewWithBackend(unavailableReaderBackend{})
	snapshot, err := service.Snapshot(context.Background(), Selector{USBPath: "2-1"}, "")
	if !errors.Is(err, ErrUnavailable) || snapshot.Reader.USBPath != "2-1" {
		t.Fatalf("Snapshot() = %#v, %v", snapshot, err)
	}
}
