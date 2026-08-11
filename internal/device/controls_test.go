package device

import (
	"context"
	"errors"
	"testing"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

type fakeQMIRadioSession struct {
	mode       qmi.OperatingMode
	getModes   []qmi.OperatingMode
	setModes   []qmi.OperatingMode
	getErr     error
	setErr     error
	closeCount int
}

func (session *fakeQMIRadioSession) GetOperatingMode(context.Context) (qmi.OperatingMode, error) {
	if len(session.getModes) > 0 {
		mode := session.getModes[0]
		session.getModes = session.getModes[1:]
		return mode, session.getErr
	}
	return session.mode, session.getErr
}

func (session *fakeQMIRadioSession) SetOperatingMode(_ context.Context, mode qmi.OperatingMode) error {
	if session.setErr != nil {
		return session.setErr
	}
	session.setModes = append(session.setModes, mode)
	session.mode = mode
	return nil
}

func (session *fakeQMIRadioSession) Close() error {
	session.closeCount++
	return nil
}

func newStartedNativeQMITestManager(t *testing.T) (*Manager, *staticOpener, string) {
	t.Helper()
	const id = "wwan0"
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
		Opener: opener,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.mu.Lock()
	manager.devices[id].snapshot = &Snapshot{
		DeviceID:      id,
		OperatingMode: 7,
		ModeKnown:     true,
		FlightMode:    true,
		RadioOff:      true,
	}
	manager.mu.Unlock()
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager, opener, id
}

func TestSetFlightUsesQMIDMSForNativeWWAN(t *testing.T) {
	manager, atOpener, id := newStartedNativeQMITestManager(t)
	session := &fakeQMIRadioSession{mode: qmi.ModeOffline}
	var openedPath string
	manager.qmiRadioOpener = func(_ context.Context, path string) (qmiRadioSession, error) {
		openedPath = path
		return session, nil
	}

	disabled, err := manager.SetFlight(context.Background(), id, false)
	if err != nil {
		t.Fatalf("disable flight mode: %v", err)
	}
	if !disabled.Changed || disabled.PreviousMode != 7 || disabled.CurrentMode != 1 ||
		disabled.FlightMode || disabled.RadioOff {
		t.Fatalf("disable result = %#v", disabled)
	}
	enabled, err := manager.SetFlight(context.Background(), id, true)
	if err != nil {
		t.Fatalf("enable flight mode: %v", err)
	}
	if !enabled.Changed || enabled.PreviousMode != 1 || enabled.CurrentMode != 0 ||
		!enabled.FlightMode || !enabled.RadioOff {
		t.Fatalf("enable result = %#v", enabled)
	}
	if openedPath != "/dev/wwan0qmi0" {
		t.Fatalf("QMI path = %q", openedPath)
	}
	if len(session.setModes) != 2 || session.setModes[0] != qmi.ModeOnline || session.setModes[1] != qmi.ModeLowPower {
		t.Fatalf("QMI modes = %v", session.setModes)
	}
	if session.closeCount != 2 {
		t.Fatalf("QMI close count = %d", session.closeCount)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times for native QMI flight mode", atOpener.openCount)
	}
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Snapshot == nil || entry.Snapshot.OperatingMode != 0 || !entry.Snapshot.FlightMode {
		t.Fatalf("snapshot = %#v", entry.Snapshot)
	}
}

func TestSetFlightDoesNotFallBackToUnsupportedATWhenQMIUnavailable(t *testing.T) {
	manager, atOpener, id := newStartedNativeQMITestManager(t)
	wantErr := errors.New("QMI DMS unavailable")
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		return nil, wantErr
	}

	if _, err := manager.SetFlight(context.Background(), id, false); !errors.Is(err, wantErr) {
		t.Fatalf("SetFlight error = %v, want %v", err, wantErr)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times after QMI failure", atOpener.openCount)
	}
}

func TestProfileSwitchRecoveryUsesQMIModemResetForNativeWWAN(t *testing.T) {
	manager, atOpener, id := newStartedNativeQMITestManager(t)
	session := &fakeQMIRadioSession{mode: qmi.ModeOnline}
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		return session, nil
	}

	if err := manager.rebootForProfileSwitch(context.Background(), id); err != nil {
		t.Fatalf("rebootForProfileSwitch() error = %v", err)
	}
	if len(session.setModes) != 1 || session.setModes[0] != qmi.ModeReset {
		t.Fatalf("QMI reset modes = %v", session.setModes)
	}
	if session.closeCount != 1 {
		t.Fatalf("QMI close count = %d", session.closeCount)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times for native profile-switch reset", atOpener.openCount)
	}
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Snapshot == nil {
		t.Fatal("last responsive snapshot was discarded during native modem recovery")
	}
	if !entry.Recovering {
		t.Fatal("native modem reset was not exposed as recovering")
	}
}

func TestSetFlightWaitsForAsynchronousQMIModeTransition(t *testing.T) {
	manager, atOpener, id := newStartedNativeQMITestManager(t)
	session := &fakeQMIRadioSession{
		mode:     qmi.ModeShutdown,
		getModes: []qmi.OperatingMode{qmi.ModeShutdown, qmi.ModeShutdown, qmi.ModeOnline},
	}
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		return session, nil
	}

	result, err := manager.SetFlight(context.Background(), id, false)
	if err != nil {
		t.Fatalf("disable flight mode: %v", err)
	}
	if !result.Changed || result.PreviousMode != 7 || result.CurrentMode != 1 || result.FlightMode {
		t.Fatalf("result = %#v", result)
	}
	if len(session.setModes) != 1 || session.setModes[0] != qmi.ModeOnline {
		t.Fatalf("QMI modes = %v", session.setModes)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times during QMI transition", atOpener.openCount)
	}
}

func TestSetFlightPreservesRawCFUNZero(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 0")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 0")},
	}}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.SetFlight(context.Background(), id, true)
	if err != nil {
		t.Fatalf("SetFlight: %v", err)
	}
	if result.Changed || result.PreviousMode != 0 || result.CurrentMode != 0 ||
		!result.FlightMode || !result.RadioOff {
		t.Fatalf("result = %#v", result)
	}
	client.assertDone(t)
}

func TestSetFlightRestoresPreviousFunctionalMode(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
		{command: "AT+CFUN=4", response: okResponse()},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
		{command: "AT+CFUN=1", response: okResponse()},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
	}}
	manager, id := newStartedTestManager(t, client)

	enabled, err := manager.SetFlight(context.Background(), id, true)
	if err != nil {
		t.Fatalf("enable flight mode: %v", err)
	}
	if !enabled.Changed || enabled.PreviousMode != 1 ||
		enabled.CurrentMode != 4 || !enabled.FlightMode {
		t.Fatalf("enable result = %#v", enabled)
	}
	disabled, err := manager.SetFlight(context.Background(), id, false)
	if err != nil {
		t.Fatalf("disable flight mode: %v", err)
	}
	if !disabled.Changed || disabled.PreviousMode != 4 ||
		disabled.CurrentMode != 1 || disabled.FlightMode {
		t.Fatalf("disable result = %#v", disabled)
	}
	client.assertDone(t)
}

func TestUSSDWaitsForAndDecodesCUSD(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{{
			command:  `AT+CUSD=1,"*100#",15`,
			response: okResponse(),
		}},
		urcs: []string{`+CUSD: 0,"004F004B",72`},
	}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.USSD(context.Background(), id, "*100#")
	if err != nil {
		t.Fatalf("USSD: %v", err)
	}
	if result.Text != "OK" || result.Code != "*100#" ||
		result.DCS == nil || *result.DCS != 72 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := manager.USSD(context.Background(), id, "*100#\rAT"); err == nil {
		t.Fatal("expected invalid service code rejection")
	}
	client.assertDone(t)
}
