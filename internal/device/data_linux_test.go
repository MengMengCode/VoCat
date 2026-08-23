//go:build linux

package device

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

type fakeQMIDataSession struct {
	closed                  bool
	stopHandle              uint32
	stopAnyCalls            int
	stopErr                 error
	rawIP                   bool
	setRawIPCalls           int
	packetStatusEvents      chan struct{}
	registerPacketEventErr  error
	registerPacketEventCall int
}

func (*fakeQMIDataSession) Start(context.Context, string, string, string, uint8, uint8) (uint32, error) {
	return 0, nil
}
func (session *fakeQMIDataSession) Stop(_ context.Context, handle uint32) error {
	session.stopHandle = handle
	return session.stopErr
}
func (session *fakeQMIDataSession) StopAny(context.Context, bool) error {
	session.stopAnyCalls++
	return session.stopErr
}
func (session *fakeQMIDataSession) RegisterPacketStatusEvents(context.Context) error {
	session.registerPacketEventCall++
	if session.packetStatusEvents == nil {
		session.packetStatusEvents = make(chan struct{}, 1)
	}
	return session.registerPacketEventErr
}
func (session *fakeQMIDataSession) PacketStatusEvents() <-chan struct{} {
	return session.packetStatusEvents
}
func (*fakeQMIDataSession) Connected(context.Context) (bool, error) { return false, nil }
func (session *fakeQMIDataSession) RawIP(context.Context) (bool, error) {
	return session.rawIP, nil
}
func (session *fakeQMIDataSession) SetRawIP(context.Context) error {
	session.rawIP = true
	session.setRawIPCalls++
	return nil
}
func (*fakeQMIDataSession) RuntimeIPv4(context.Context) (qmiIPv4Settings, error) {
	return qmiIPv4Settings{}, errors.New("not configured")
}
func (session *fakeQMIDataSession) Close() error {
	session.closed = true
	return nil
}

func TestInvalidateQMINetworkSessionClosesOwnedSessionAndRemovesLegacyState(t *testing.T) {
	control := "/dev/cdc-wdm-vocat-test-" + strconv.Itoa(os.Getpid())
	path := legacyQMINetworkStatePath(control)
	if err := os.WriteFile(path, []byte("CID=7\nPDH=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	session := &fakeQMIDataSession{}
	state := &managedDevice{dataSession: session, dataSessionHandle: 123, dataSessionControl: control}
	invalidateQMINetworkSession(state, modem.Candidate{QMIControl: control})
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state still exists: %v", err)
	}
	if !session.closed || state.dataSession != nil || state.dataSessionHandle != 0 {
		t.Fatalf("owned session was not discarded: closed=%v state=%+v", session.closed, state)
	}
}

func TestStoppedQMIDataError(t *testing.T) {
	for _, err := range []error{
		nil,
		&qmi.OutOfCallError{Operation: "stop"},
		&qmi.QMIError{ErrorCode: qmi.QMIErrInvalidID},
		fmt.Errorf("wrapped: %w", &qmi.QMIError{ErrorCode: qmi.QMIErrNoEffect}),
	} {
		if !stoppedQMIDataError(err) {
			t.Fatalf("%v should be idempotent", err)
		}
	}
	if stoppedQMIDataError(context.DeadlineExceeded) {
		t.Fatal("timeout must not be treated as an already-stopped session")
	}
}

func TestStopQMIDataSessionUsesOwnedHandle(t *testing.T) {
	session := &fakeQMIDataSession{}
	state := &managedDevice{dataSession: session, dataSessionHandle: 456, dataSessionControl: "/dev/cdc-wdm0"}
	manager := &Manager{}
	err := manager.stopQMIDataSession(context.Background(), state, modem.Candidate{QMIControl: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	if session.stopHandle != 456 || !session.closed || state.dataSession != nil {
		t.Fatalf("owned stop did not clear session: handle=%d closed=%v", session.stopHandle, session.closed)
	}
}

func TestStopQMIDataSessionReclaimsUnknownModemCall(t *testing.T) {
	session := &fakeQMIDataSession{stopErr: &qmi.OutOfCallError{Operation: "stop"}}
	manager := &Manager{qmiDataOpener: func(context.Context, string) (qmiDataSession, error) {
		return session, nil
	}}
	err := manager.stopQMIDataSession(context.Background(), &managedDevice{}, modem.Candidate{QMIControl: "/dev/cdc-wdm0"})
	if err != nil {
		t.Fatal(err)
	}
	if session.stopAnyCalls != 1 || !session.closed {
		t.Fatalf("unknown session was not reclaimed: stopAny=%d closed=%v", session.stopAnyCalls, session.closed)
	}
}

func TestPrepareQMIDataFormatEnablesRawIPOnModemAndKernel(t *testing.T) {
	rawIPPath := t.TempDir() + "/raw_ip"
	if err := os.WriteFile(rawIPPath, []byte("N\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &fakeQMIDataSession{}
	if err := prepareQMIDataFormat(context.Background(), session, "/bin/true", "wwan-test", rawIPPath); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(rawIPPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "Y" || !session.rawIP || session.setRawIPCalls != 1 {
		t.Fatalf("raw-ip not synchronized: kernel=%q modem=%t setCalls=%d", content, session.rawIP, session.setRawIPCalls)
	}
}

func TestPrepareQMIDataFormatLeavesMatchingRawIPUndisturbed(t *testing.T) {
	rawIPPath := t.TempDir() + "/raw_ip"
	if err := os.WriteFile(rawIPPath, []byte("Y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &fakeQMIDataSession{rawIP: true}
	if err := prepareQMIDataFormat(context.Background(), session, "/does/not/exist", "wwan-test", rawIPPath); err != nil {
		t.Fatal(err)
	}
	if session.setRawIPCalls != 0 {
		t.Fatalf("matching format was rewritten %d times", session.setRawIPCalls)
	}
}

func TestQMIDataEventWatcherPublishesDeviceID(t *testing.T) {
	manager := &Manager{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := manager.SubscribeNetworkStatusEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeQMIDataSession{}
	state := &managedDevice{}
	manager.enableQMIDataEvents(ctx, state, "physical-1", session)
	if session.registerPacketEventCall != 1 {
		t.Fatalf("register calls = %d, want 1", session.registerPacketEventCall)
	}
	if state.dataEventCancel == nil {
		t.Fatal("QMI event watcher was not started")
	}
	session.packetStatusEvents <- struct{}{}
	select {
	case deviceID := <-events:
		if deviceID != "physical-1" {
			t.Fatalf("event device ID = %q, want physical-1", deviceID)
		}
	case <-time.After(time.Second):
		t.Fatal("QMI packet event was not published")
	}
	state.dataEventCancel()
}
