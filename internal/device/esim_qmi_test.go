package device

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"vocat/internal/modem"
)

type fakeDeviceQMIUIMSession struct {
	iccid       string
	getICCIDErr error
	openSlot    uint8
	openAID     []byte
	channel     byte
	sendSlots   []uint8
	sendChannel []byte
	sendAPDUs   [][]byte
	responses   [][]byte
	closeSlot   uint8
	closed      bool
}

func (session *fakeDeviceQMIUIMSession) GetICCID(context.Context) (string, error) {
	return session.iccid, session.getICCIDErr
}

func (session *fakeDeviceQMIUIMSession) OpenLogicalChannel(
	_ context.Context,
	slot uint8,
	aid []byte,
) (byte, error) {
	session.openSlot = slot
	session.openAID = append([]byte(nil), aid...)
	return session.channel, nil
}

func (session *fakeDeviceQMIUIMSession) CloseLogicalChannel(
	_ context.Context,
	slot uint8,
	_ byte,
) error {
	session.closeSlot = slot
	return nil
}

func (session *fakeDeviceQMIUIMSession) SendAPDU(
	_ context.Context,
	slot uint8,
	channel byte,
	apdu []byte,
) ([]byte, error) {
	session.sendSlots = append(session.sendSlots, slot)
	session.sendChannel = append(session.sendChannel, channel)
	session.sendAPDUs = append(session.sendAPDUs, append([]byte(nil), apdu...))
	if len(session.responses) == 0 {
		return nil, errors.New("unexpected QMI-UIM APDU")
	}
	response := append([]byte(nil), session.responses[0]...)
	session.responses = session.responses[1:]
	return response, nil
}

func (session *fakeDeviceQMIUIMSession) Close() error {
	session.closed = true
	return nil
}

func TestNativeWWANEuiccUsesQMIUIMLogicalChannel(t *testing.T) {
	const id = "wwan0"
	client := &transcriptClient{}
	opener := &staticOpener{client: client}
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:               id,
			Product:          "410 WiFi stick",
			QMIControl:       "/dev/wwan0qmi0",
			NetworkInterface: "wwan0",
			ATPort: modem.Port{
				Path: "/dev/wwan0at0",
				Name: "wwan0at0",
				Role: modem.PortRoleAT,
			},
		}}},
		Opener: opener,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	session := &fakeDeviceQMIUIMSession{
		channel: 3,
		responses: [][]byte{
			{0x01, 0x02, 0x61, 0x02},
			{0x03, 0x04, 0x90, 0x00},
		},
	}
	var openedPath string
	manager.qmiEUICCOpener = func(
		_ context.Context,
		controlDevice string,
	) (qmiEUICCSession, error) {
		openedPath = controlDevice
		return session, nil
	}

	channel, err := manager.openEuiccAID(context.Background(), id, isdRAID)
	if err != nil {
		t.Fatalf("openEuiccAID: %v", err)
	}
	payload, status, err := channel.transmit(
		context.Background(),
		[]byte{0x80, 0xE2, 0x91, 0x00, 0x00},
		0x80,
	)
	if err != nil {
		t.Fatalf("transmit: %v", err)
	}
	channel.close(context.Background())

	if openedPath != "/dev/wwan0qmi0" {
		t.Fatalf("QMI-UIM path = %q", openedPath)
	}
	wantAID := []byte{0xA0, 0x00, 0x00, 0x05, 0x59, 0x10, 0x10, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}
	if session.openSlot != 1 || !bytes.Equal(session.openAID, wantAID) {
		t.Fatalf("open slot/AID = %d/%X", session.openSlot, session.openAID)
	}
	if status != 0x9000 || !bytes.Equal(payload, []byte{1, 2, 3, 4}) {
		t.Fatalf("response = %X/%04X", payload, status)
	}
	if len(session.sendAPDUs) != 2 || session.sendAPDUs[0][0] != 0x80 || session.sendAPDUs[1][0] != 0x80 {
		t.Fatalf("QMI APDUs = %X", session.sendAPDUs)
	}
	for index := range session.sendSlots {
		if session.sendSlots[index] != 1 || session.sendChannel[index] != 3 {
			t.Fatalf("send[%d] slot/channel = %d/%d", index, session.sendSlots[index], session.sendChannel[index])
		}
	}
	if session.closeSlot != 1 || !session.closed {
		t.Fatalf("QMI session close = slot %d closed %v", session.closeSlot, session.closed)
	}
	if opener.openCount != 0 {
		t.Fatalf("AT opener used %d times for native QMI eUICC", opener.openCount)
	}
	client.assertDone(t)
}

func TestVerifySwitchedICCIDUsesNativeQMIUIM(t *testing.T) {
	const (
		id       = "wwan0"
		expected = "89492026266006792824"
	)
	opener := &staticOpener{client: &transcriptClient{}}
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:               id,
			Product:          "410 WiFi stick",
			QMIControl:       "/dev/wwan0qmi0",
			NetworkInterface: "wwan0",
			ATPort: modem.Port{
				Path: "/dev/wwan0at0",
				Name: "wwan0at0",
				Role: modem.PortRoleAT,
			},
		}}},
		Opener:         opener,
		CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	session := &fakeDeviceQMIUIMSession{iccid: expected}
	manager.qmiEUICCOpener = func(context.Context, string) (qmiEUICCSession, error) {
		return session, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.verifySwitchedICCID(ctx, id, expected); err != nil {
		t.Fatalf("verifySwitchedICCID: %v", err)
	}
	if !session.closed {
		t.Fatal("QMI-UIM verification session was not closed")
	}
	if opener.openCount != 0 {
		t.Fatalf("AT opener used %d times for native QMI ICCID verification", opener.openCount)
	}
}
