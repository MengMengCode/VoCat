package device

import (
	"context"
	"errors"
	"testing"
)

type fakeQMIProvisioningSession struct {
	aid        []byte
	aidErr     error
	rebindErr  error
	rebindSlot uint8
	rebindAID  []byte
}

func (session *fakeQMIProvisioningSession) GetUSIMAID(context.Context) ([]byte, error) {
	return append([]byte(nil), session.aid...), session.aidErr
}

func (session *fakeQMIProvisioningSession) RebindPrimaryGWProvisioning(_ context.Context, slot uint8, aid []byte) error {
	session.rebindSlot = slot
	session.rebindAID = append([]byte(nil), aid...)
	return session.rebindErr
}

func TestRebindNativeQMIPrimaryGWProvisioning(t *testing.T) {
	session := &fakeQMIProvisioningSession{aid: []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02}}
	if err := rebindNativeQMIPrimaryGWProvisioning(context.Background(), session); err != nil {
		t.Fatalf("rebind provisioning session: %v", err)
	}
	if session.rebindSlot != 1 {
		t.Fatalf("slot = %d, want 1", session.rebindSlot)
	}
	if string(session.rebindAID) != string(session.aid) {
		t.Fatalf("AID = %X, want %X", session.rebindAID, session.aid)
	}
}

func TestRebindNativeQMIPrimaryGWProvisioningWrapsFailure(t *testing.T) {
	want := errors.New("UIM command rejected")
	session := &fakeQMIProvisioningSession{aid: []byte{0xA0}, rebindErr: want}
	if err := rebindNativeQMIPrimaryGWProvisioning(context.Background(), session); !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}
