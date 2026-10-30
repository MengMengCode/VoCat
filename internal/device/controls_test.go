package device

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

type fakeQMIRadioSession struct {
	mode           qmi.OperatingMode
	getModes       []qmi.OperatingMode
	setModes       []qmi.OperatingMode
	getErr         error
	setErr         error
	closeCount     int
	iccid          string
	iccidErr       error
	imei           string
	imeiErr        error
	openedAIDs     [][]byte
	openChannel    byte
	openErr        error
	closedChannels []byte
	apdus          [][]byte
	apduResponse   []byte
	apduErr        error
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

func (session *fakeQMIRadioSession) GetICCID(context.Context) (string, error) {
	return session.iccid, session.iccidErr
}

func (session *fakeQMIRadioSession) GetIMEI(context.Context) (string, error) {
	return session.imei, session.imeiErr
}

func (session *fakeQMIRadioSession) OpenLogicalChannel(_ context.Context, _ uint8, aid []byte) (byte, error) {
	session.openedAIDs = append(session.openedAIDs, append([]byte(nil), aid...))
	if session.openErr != nil {
		return 0, session.openErr
	}
	if session.openChannel == 0 {
		return 1, nil
	}
	return session.openChannel, nil
}

func (session *fakeQMIRadioSession) CloseLogicalChannel(_ context.Context, _ uint8, channel uint8) error {
	session.closedChannels = append(session.closedChannels, channel)
	return nil
}

func (session *fakeQMIRadioSession) SendAPDU(_ context.Context, _ uint8, _ uint8, command []byte) ([]byte, error) {
	session.apdus = append(session.apdus, append([]byte(nil), command...))
	return append([]byte(nil), session.apduResponse...), session.apduErr
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

// Each QMI open owns a distinct session, while DMS state belongs to the modem.
// Flight-mode changes also launch an asynchronous NAS capability probe; it
// must not close or mutate the foreground operation's fake session.
type flightQMITestModem struct {
	mu          sync.Mutex
	mode        qmi.OperatingMode
	getModes    []qmi.OperatingMode
	setModes    []qmi.OperatingMode
	openedPaths []string
	closeCount  int
}

type flightQMITestSession struct{ modem *flightQMITestModem }

func (modem *flightQMITestModem) open(_ context.Context, path string) (qmiRadioSession, error) {
	modem.mu.Lock()
	modem.openedPaths = append(modem.openedPaths, path)
	modem.mu.Unlock()
	return &flightQMITestSession{modem: modem}, nil
}

func (session *flightQMITestSession) GetOperatingMode(context.Context) (qmi.OperatingMode, error) {
	modem := session.modem
	modem.mu.Lock()
	defer modem.mu.Unlock()
	if len(modem.getModes) > 0 {
		mode := modem.getModes[0]
		modem.getModes = modem.getModes[1:]
		return mode, nil
	}
	return modem.mode, nil
}

func (session *flightQMITestSession) SetOperatingMode(_ context.Context, mode qmi.OperatingMode) error {
	modem := session.modem
	modem.mu.Lock()
	defer modem.mu.Unlock()
	modem.mode = mode
	modem.setModes = append(modem.setModes, mode)
	return nil
}

func (session *flightQMITestSession) Close() error {
	session.modem.mu.Lock()
	session.modem.closeCount++
	session.modem.mu.Unlock()
	return nil
}

func waitForFlightQMIRegistration(t *testing.T, manager *Manager, id string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		manager.nativeQMIRegistrationMu.Lock()
		_, running := manager.nativeQMIRegistrationInFlight[id]
		manager.nativeQMIRegistrationMu.Unlock()
		if !running {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("background NAS capability probe did not finish")
		}
	}
}

func TestSetFlightUsesQMIDMSForNativeWWAN(t *testing.T) {
	manager, atOpener, id := newStartedNativeQMITestManager(t)
	modem := &flightQMITestModem{mode: qmi.ModeOffline}
	manager.qmiRadioOpener = modem.open

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
	waitForFlightQMIRegistration(t, manager, id)
	modem.mu.Lock()
	defer modem.mu.Unlock()
	// Two DMS operations and the background NAS probe each open and close
	// their own session. The unsupported NAS probe changes no radio mode.
	if len(modem.openedPaths) != 3 || modem.closeCount != 3 {
		t.Fatalf("QMI session counts: opened=%d closed=%d, want 3 each", len(modem.openedPaths), modem.closeCount)
	}
	for _, path := range modem.openedPaths {
		if path != "/dev/wwan0qmi0" {
			t.Fatalf("QMI path = %q", path)
		}
	}
	if len(modem.setModes) != 2 || modem.setModes[0] != qmi.ModeOnline || modem.setModes[1] != qmi.ModeLowPower {
		t.Fatalf("QMI modes = %v", modem.setModes)
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

func TestSetFlightWaitsForAsynchronousQMIModeTransition(t *testing.T) {
	manager, atOpener, id := newStartedNativeQMITestManager(t)
	modem := &flightQMITestModem{
		mode:     qmi.ModeShutdown,
		getModes: []qmi.OperatingMode{qmi.ModeShutdown, qmi.ModeShutdown, qmi.ModeOnline},
	}
	manager.qmiRadioOpener = modem.open

	result, err := manager.SetFlight(context.Background(), id, false)
	if err != nil {
		t.Fatalf("disable flight mode: %v", err)
	}
	if !result.Changed || result.PreviousMode != 7 || result.CurrentMode != 1 || result.FlightMode {
		t.Fatalf("result = %#v", result)
	}
	waitForFlightQMIRegistration(t, manager, id)
	modem.mu.Lock()
	defer modem.mu.Unlock()
	if len(modem.setModes) != 1 || modem.setModes[0] != qmi.ModeOnline {
		t.Fatalf("QMI modes = %v", modem.setModes)
	}
	if len(modem.openedPaths) != 2 || modem.closeCount != 2 {
		t.Fatalf("DMS/NAS sessions: opened=%d closed=%d, want 2 each", len(modem.openedPaths), modem.closeCount)
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
