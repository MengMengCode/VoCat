package qmi

import (
	"reflect"
	"testing"
)

func TestIMSDCMServiceIDExceedsCurrentQMUXServiceRange(t *testing.T) {
	const imsdcmServiceID = 0x302

	if imsdcmServiceID <= 0xFF {
		t.Fatalf("expected IMSDCM service ID to exceed uint8, got 0x%X", imsdcmServiceID)
	}
}

func TestCurrentQMUXClientUses8BitServiceIdentifiers(t *testing.T) {
	if got := reflect.TypeOf(QmuxHeader{}.ServiceType).Kind(); got != reflect.Uint8 {
		t.Fatalf("QmuxHeader.ServiceType kind = %v, want uint8", got)
	}
	if got := reflect.TypeOf(Packet{}.ServiceType).Kind(); got != reflect.Uint8 {
		t.Fatalf("Packet.ServiceType kind = %v, want uint8", got)
	}
	if got := reflect.TypeOf(Event{}.ServiceID).Kind(); got != reflect.Uint8 {
		t.Fatalf("Event.ServiceID kind = %v, want uint8", got)
	}

	var c Client
	if got := reflect.TypeOf(c.clientIDs).Key().Kind(); got != reflect.Uint8 {
		t.Fatalf("Client.clientIDs key kind = %v, want uint8", got)
	}

	sendRequestType := reflect.TypeOf((*Client).SendRequest)
	if got := sendRequestType.In(2).Kind(); got != reflect.Uint8 {
		t.Fatalf("SendRequest service arg kind = %v, want uint8", got)
	}
	if got := sendRequestType.In(3).Kind(); got != reflect.Uint8 {
		t.Fatalf("SendRequest clientID arg kind = %v, want uint8", got)
	}

	allocateType := reflect.TypeOf((*Client).AllocateClientID)
	if got := allocateType.In(1).Kind(); got != reflect.Uint8 {
		t.Fatalf("AllocateClientID service arg kind = %v, want uint8", got)
	}

	releaseType := reflect.TypeOf((*Client).ReleaseClientID)
	if got := releaseType.In(1).Kind(); got != reflect.Uint8 {
		t.Fatalf("ReleaseClientID service arg kind = %v, want uint8", got)
	}
}

func TestCurrentFramingIsQMUXOnly(t *testing.T) {
	packet := Packet{
		ServiceType: ServiceIMS,
		ClientID:    0x01,
		MessageID:   0x1234,
	}

	raw := packet.Marshal()
	if len(raw) < QmuxHeaderSize {
		t.Fatalf("marshal returned %d bytes, want at least %d", len(raw), QmuxHeaderSize)
	}
	if raw[0] != 0x01 {
		t.Fatalf("QMUX marker = 0x%02X, want 0x01", raw[0])
	}

	raw[0] = 0x02
	if _, err := UnmarshalQmuxHeader(raw[:QmuxHeaderSize]); err == nil {
		t.Fatal("expected QRTR-style marker to be rejected by current QMUX header parser")
	}
}
