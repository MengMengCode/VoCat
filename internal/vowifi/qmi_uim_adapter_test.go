package vowifi

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type fakeQMIUIMSession struct {
	iccid        string
	aid          []byte
	channel      byte
	sendResponse []byte
	openAID      []byte
	openSlot     uint8
	sendSlot     uint8
	closeSlot    uint8
	closed       bool
}

func (session *fakeQMIUIMSession) GetICCID(context.Context) (string, error) {
	return session.iccid, nil
}

func (session *fakeQMIUIMSession) GetUSIMAID(context.Context) ([]byte, error) {
	return append([]byte(nil), session.aid...), nil
}

func (session *fakeQMIUIMSession) OpenLogicalChannel(
	_ context.Context,
	slot uint8,
	aid []byte,
) (byte, error) {
	session.openSlot = slot
	session.openAID = append([]byte(nil), aid...)
	return session.channel, nil
}

func (session *fakeQMIUIMSession) CloseLogicalChannel(
	_ context.Context,
	slot uint8,
	_ byte,
) error {
	session.closeSlot = slot
	return nil
}

func (session *fakeQMIUIMSession) SendAPDU(
	_ context.Context,
	slot uint8,
	_ byte,
	_ []byte,
) ([]byte, error) {
	session.sendSlot = slot
	return append([]byte(nil), session.sendResponse...), nil
}

func (session *fakeQMIUIMSession) Close() error {
	session.closed = true
	return nil
}

func TestQMIUIMAKAProviderCheckReadyUses410SlotOneAndFullAID(t *testing.T) {
	fullAID := append(append([]byte(nil), usimAID...), 0xff, 0x49)
	session := &fakeQMIUIMSession{
		iccid:   "894921007998876780",
		aid:     fullAID,
		channel: 2,
	}
	provider, err := newQMIUIMAKAProvider("/dev/wwan0qmi0", func(context.Context, string) (qmiUIMSession, error) {
		return session, nil
	})
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	evidence, err := provider.CheckReady(context.Background(), SIMIdentity{ICCID: session.iccid})
	if err != nil {
		t.Fatalf("CheckReady: %v", err)
	}
	if !evidence.Ready || evidence.Application != "USIM" {
		t.Fatalf("evidence = %#v", evidence)
	}
	if session.openSlot != 1 || session.closeSlot != 1 {
		t.Fatalf("slots open=%d close=%d, want 1", session.openSlot, session.closeSlot)
	}
	if !bytes.Equal(session.openAID, fullAID) {
		t.Fatalf("open AID = %x, want %x", session.openAID, fullAID)
	}
	if !session.closed {
		t.Fatal("QMI session was not closed")
	}
}

func TestQMIUIMAKAProviderAuthenticatesAndParsesVector(t *testing.T) {
	response := []byte{0xdb, 0x04, 1, 2, 3, 4, 0x10}
	response = append(response, bytes.Repeat([]byte{0x11}, 16)...)
	response = append(response, 0x10)
	response = append(response, bytes.Repeat([]byte{0x22}, 16)...)
	response = append(response, 0x90, 0x00)
	session := &fakeQMIUIMSession{
		iccid:        "894921007998876780",
		aid:          append(append([]byte(nil), usimAID...), 0xff),
		channel:      3,
		sendResponse: response,
	}
	provider, err := newQMIUIMAKAProvider("/dev/wwan0qmi0", func(context.Context, string) (qmiUIMSession, error) {
		return session, nil
	})
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	result, err := provider.Authenticate(
		context.Background(),
		SIMIdentity{ICCID: session.iccid},
		AKAChallenge{},
	)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !bytes.Equal(result.RES, []byte{1, 2, 3, 4}) || len(result.CK) != 16 || len(result.IK) != 16 {
		t.Fatalf("AKA result lengths RES=%d CK=%d IK=%d", len(result.RES), len(result.CK), len(result.IK))
	}
	if session.sendSlot != 1 || session.closeSlot != 1 {
		t.Fatalf("slots send=%d close=%d, want 1", session.sendSlot, session.closeSlot)
	}
}

func TestQMIUIMAKAProviderRejectsChangedSIM(t *testing.T) {
	session := &fakeQMIUIMSession{iccid: "222", aid: usimAID, channel: 1}
	provider, err := newQMIUIMAKAProvider("/dev/wwan0qmi0", func(context.Context, string) (qmiUIMSession, error) {
		return session, nil
	})
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	_, err = provider.CheckReady(context.Background(), SIMIdentity{ICCID: "111"})
	if !errors.Is(err, ErrEC20IdentityChanged) {
		t.Fatalf("CheckReady error = %v, want ErrEC20IdentityChanged", err)
	}
}
