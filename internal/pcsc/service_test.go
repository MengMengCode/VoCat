package pcsc

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
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

func TestSelectMFDoesNotRequestFCP(t *testing.T) {
	card := &scriptedCard{replies: []scriptedReply{{sw: 0x9000}}}
	if err := selectMF(context.Background(), card); err != nil {
		t.Fatalf("selectMF: %v", err)
	}
	want := []byte{0x00, 0xA4, 0x00, 0x0C, 0x02, 0x3F, 0x00}
	if len(card.calls) != 1 || !bytes.Equal(card.calls[0], want) {
		t.Fatalf("SELECT MF APDU = % X, want % X", card.calls, want)
	}
}

func TestSelectMFDoesNotTreatFCPPrefixAsSuccess(t *testing.T) {
	card := &scriptedCard{replies: []scriptedReply{{sw: 0x622F}}}
	err := selectMF(context.Background(), card)
	if err == nil || !strings.Contains(err.Error(), "status 622F") {
		t.Fatalf("selectMF warning error = %v", err)
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

func TestMatchReaderRecoversUSBMovesWithoutSwitchingReaders(t *testing.T) {
	const name = "Alcor Link AK9563 00 00"
	moved := Reader{Name: name, USBPath: "2-1"}
	other := Reader{Name: "Other Reader 00 00", USBPath: "1-1"}
	placeholder := Reader{Name: "USB smart card reader", USBPath: "1-1", DiscoveryIssue: "pcsc_service_unavailable"}
	tests := []struct {
		name     string
		readers  []Reader
		selector Selector
		want     Reader
		found    bool
	}{
		{"moved reader after old port occupant", []Reader{other, moved}, Selector{USBPath: "1-1", ReaderName: name}, moved, true},
		{"moved reader before old port occupant", []Reader{moved, other}, Selector{USBPath: "1-1", ReaderName: name}, moved, true},
		{"old port occupied while named reader absent", []Reader{other}, Selector{USBPath: "1-1", ReaderName: name}, Reader{}, false},
		{"exact pair disambiguates duplicate names", []Reader{moved, {Name: name, USBPath: "1-1"}}, Selector{USBPath: "2-1", ReaderName: name}, moved, true},
		{"duplicate names after port move", []Reader{moved, {Name: name, USBPath: "3-1"}}, Selector{USBPath: "1-1", ReaderName: name}, Reader{}, false},
		{"legacy path only", []Reader{other, moved}, Selector{USBPath: "2-1"}, moved, true},
		{"path shared by multiple slots", []Reader{moved, {Name: "Alcor Link AK9563 00 01", USBPath: "2-1"}}, Selector{USBPath: "2-1"}, Reader{}, false},
		{"different slot is a different name", []Reader{{Name: "Alcor Link AK9563 00 01", USBPath: "1-1"}}, Selector{USBPath: "1-1", ReaderName: name}, Reader{}, false},
		{"unique name only", []Reader{other, moved}, Selector{ReaderName: name}, moved, true},
		{"surrounding whitespace preserves native name", []Reader{{Name: " " + name + " ", USBPath: "2-1"}}, Selector{ReaderName: " " + name}, Reader{Name: " " + name + " ", USBPath: "2-1"}, true},
		{"missing name permits path fallback", []Reader{{USBPath: "1-1"}}, Selector{USBPath: "1-1", ReaderName: name}, Reader{USBPath: "1-1"}, true},
		{"diagnostic USB placeholder", []Reader{placeholder}, Selector{USBPath: "1-1", ReaderName: name}, placeholder, true},
		{"ready named reader outranks stale placeholder", []Reader{placeholder, moved}, Selector{USBPath: "1-1", ReaderName: name}, moved, true},
		{"empty selector", []Reader{moved}, Selector{}, Reader{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, found := MatchReader(test.readers, test.selector)
			if found != test.found || got != test.want {
				t.Fatalf("MatchReader() = %#v, %v, want %#v, %v", got, found, test.want, test.found)
			}
		})
	}
}

type selectionReaderBackend struct {
	readers []Reader
	opened  bool
}

func (backend *selectionReaderBackend) Readers(context.Context) ([]Reader, error) {
	return backend.readers, nil
}

func (backend *selectionReaderBackend) Open(context.Context, Selector) (Card, error) {
	backend.opened = true
	return nil, errors.New("unexpected card open")
}

func TestSnapshotUsesNamedReaderAfterUSBMove(t *testing.T) {
	const name = "Alcor Link AK9563 00 00"
	backend := &selectionReaderBackend{readers: []Reader{
		{Name: "Other Reader 00 00", USBPath: "1-1", CardPresent: true},
		{Name: name, USBPath: "2-1"},
	}}
	service := NewWithBackend(backend)
	snapshot, err := service.Snapshot(context.Background(), Selector{USBPath: "1-1", ReaderName: name}, "")
	if !errors.Is(err, ErrNoCard) || snapshot.Reader.Name != name || snapshot.Reader.USBPath != "2-1" || backend.opened {
		t.Fatalf("Snapshot() = %#v, %v, card opened %v", snapshot, err, backend.opened)
	}

	backend.readers = backend.readers[:1]
	_, err = service.Snapshot(context.Background(), Selector{USBPath: "1-1", ReaderName: name}, "")
	if !errors.Is(err, ErrReaderNotFound) || backend.opened {
		t.Fatalf("absent named reader = %v, card opened %v", err, backend.opened)
	}
}

type scriptedBackend struct {
	card Card
}

func (b *scriptedBackend) Readers(context.Context) ([]Reader, error) {
	return []Reader{{Name: "Test Reader", USBPath: "1-1", CardPresent: true}}, nil
}

func (b *scriptedBackend) Open(context.Context, Selector) (Card, error) {
	return b.card, nil
}

func TestSelectAKAApplicationPreferences(t *testing.T) {
	usimRecord := []byte{0x61, 0x12, 0x4F, 0x10, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}
	isimRecord := []byte{0x61, 0x12, 0x4F, 0x10, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}

	t.Run("prefers USIM by default", func(t *testing.T) {
		card := &scriptedCard{
			replies: []scriptedReply{
				{sw: 0x9000},                   // select MF
				{sw: 0x9000},                   // select EF_DIR
				{data: usimRecord, sw: 0x9000}, // record 1: USIM
				{data: isimRecord, sw: 0x9000}, // record 2: ISIM
				{sw: 0x6A83},                   // record 3: end of file
				{sw: 0x9000},                   // select application
			},
		}
		aid, app, err := selectAKAApplication(context.Background(), card, "")
		if err != nil {
			t.Fatalf("selectAKAApplication: %v", err)
		}
		if app != "USIM" || !strings.HasPrefix(strings.ToUpper(hex.EncodeToString(aid)), usimAIDPrefix) {
			t.Fatalf("app = %s, aid = %X", app, aid)
		}
	})

	t.Run("selects ISIM when preference is isim_strict", func(t *testing.T) {
		card := &scriptedCard{
			replies: []scriptedReply{
				{sw: 0x9000},                   // select MF
				{sw: 0x9000},                   // select EF_DIR
				{data: usimRecord, sw: 0x9000}, // record 1: USIM
				{data: isimRecord, sw: 0x9000}, // record 2: ISIM
				{sw: 0x6A83},                   // record 3: end of file
				{sw: 0x9000},                   // select application
			},
		}
		aid, app, err := selectAKAApplication(context.Background(), card, "isim_strict")
		if err != nil {
			t.Fatalf("selectAKAApplication: %v", err)
		}
		if app != "ISIM" || !strings.HasPrefix(strings.ToUpper(hex.EncodeToString(aid)), isimAIDPrefix) {
			t.Fatalf("app = %s, aid = %X", app, aid)
		}
	})

	t.Run("returns ErrISIMUnavailable when isim_strict card lacks ISIM", func(t *testing.T) {
		card := &scriptedCard{
			replies: []scriptedReply{
				{sw: 0x9000},                   // select MF
				{sw: 0x9000},                   // select EF_DIR
				{data: usimRecord, sw: 0x9000}, // record 1: USIM
				{sw: 0x6A83},                   // record 2: end of file
			},
		}
		_, _, err := selectAKAApplication(context.Background(), card, "isim_strict")
		if !errors.Is(err, ErrISIMUnavailable) {
			t.Fatalf("want ErrISIMUnavailable, got %v", err)
		}
	})
}

func TestAuthenticateWithPreference(t *testing.T) {
	usimRecord := []byte{0x61, 0x12, 0x4F, 0x10, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}
	isimRecord := []byte{0x61, 0x12, 0x4F, 0x10, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}
	iccidBytes := []byte{0x98, 0x10, 0x32, 0x54, 0x76, 0x98, 0x10, 0x32, 0x54, 0xF6}

	akaSuccess := []byte{0xDB, 0x08, 1, 2, 3, 4, 5, 6, 7, 8, 0x10}
	akaSuccess = append(akaSuccess, bytes.Repeat([]byte{0xAA}, 16)...)
	akaSuccess = append(akaSuccess, 0x10)
	akaSuccess = append(akaSuccess, bytes.Repeat([]byte{0xBB}, 16)...)

	card := &scriptedCard{
		replies: []scriptedReply{
			{sw: 0x9000},                   // select MF (readICCID)
			{sw: 0x9000},                   // select EF_ICCID
			{data: iccidBytes, sw: 0x9000}, // read binary EF_ICCID
			{sw: 0x9000},                   // select MF (selectAKAApplication)
			{sw: 0x9000},                   // select EF_DIR
			{data: usimRecord, sw: 0x9000}, // record 1: USIM
			{data: isimRecord, sw: 0x9000}, // record 2: ISIM
			{sw: 0x6A83},                   // record 3: end of EF_DIR
			{sw: 0x9000},                   // select application (ISIM)
			{data: akaSuccess, sw: 0x9000}, // authenticate APDU
		},
	}

	service := NewWithBackend(&scriptedBackend{card: card})
	res, err := service.AuthenticateWithPreference(
		context.Background(),
		Selector{USBPath: "1-1"},
		"8901234567890123456",
		"",
		AKAChallenge{},
		"isim_strict",
	)
	if err != nil {
		t.Fatalf("AuthenticateWithPreference: %v", err)
	}
	if len(res.RES) != 8 || len(res.CK) != 16 || len(res.IK) != 16 {
		t.Fatalf("unexpected AKA result: %#v", res)
	}

	// Verify the selected application was ISIM (A0000000871004...)
	// APDU index 8 is SELECT application: 00 A4 04 04 10 <AID> 00
	if len(card.calls) < 9 {
		t.Fatalf("card received only %d calls", len(card.calls))
	}
	selectAPDU := card.calls[8]
	if !bytes.Contains(selectAPDU, []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04}) {
		t.Fatalf("expected ISIM AID in SELECT APDU, got: % X", selectAPDU)
	}
}
