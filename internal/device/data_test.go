package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/modem"
)

// fakeATHostSession records Close/Status calls for AT host data lifecycle tests.
type fakeATHostSession struct {
	mu           sync.Mutex
	closed       bool
	statusOK     bool
	statusDetail string
	statusErr    error
}

func (session *fakeATHostSession) Status(context.Context) (bool, string, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.statusOK, session.statusDetail, session.statusErr
}

func (session *fakeATHostSession) Close(context.Context) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.closed = true
	return nil
}

func (session *fakeATHostSession) isClosed() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.closed
}

// fakeATHostLink stands in for the platform host data link in manager tests.
type fakeATHostLink struct {
	mu       sync.Mutex
	started  []string
	sessions []*fakeATHostSession
	err      error
}

func (link *fakeATHostLink) Start(_ context.Context, networkInterface string) (atHostDataSession, error) {
	link.mu.Lock()
	defer link.mu.Unlock()
	link.started = append(link.started, networkInterface)
	if link.err != nil {
		return nil, link.err
	}
	session := &fakeATHostSession{statusOK: true, statusDetail: "lease ready"}
	link.sessions = append(link.sessions, session)
	return session, nil
}

func (link *fakeATHostLink) startedInterfaces() []string {
	link.mu.Lock()
	defer link.mu.Unlock()
	return append([]string(nil), link.started...)
}

func (link *fakeATHostLink) lastSession() *fakeATHostSession {
	link.mu.Lock()
	defer link.mu.Unlock()
	if len(link.sessions) == 0 {
		return nil
	}
	return link.sessions[len(link.sessions)-1]
}

// markerPDPProfile is a PDPATProfile whose commands carry a unique marker so
// tests can prove SetNetwork and PrepareRegistration consume the resolved
// profile instead of hardcoded 3GPP strings.
type markerPDPProfile struct{ marker string }

func (profile markerPDPProfile) ConfigureContextCommand(cid int, ipVersion, apn string) (string, error) {
	return fmt.Sprintf("AT+%s=CTX,%d,%s,%s", profile.marker, cid, ipVersion, apn), nil
}

func (profile markerPDPProfile) ConfigureAuthenticationCommand(cid, authCode int, _, _ string) (string, error) {
	return fmt.Sprintf("AT+%s=AUTH,%d,%d", profile.marker, cid, authCode), nil
}

func (profile markerPDPProfile) AttachCommand(enabled bool) string {
	state := 0
	if enabled {
		state = 1
	}
	return fmt.Sprintf("AT+%s=ATT,%d", profile.marker, state)
}

func (profile markerPDPProfile) ActivateCommand(cid int, enabled bool) string {
	state := 0
	if enabled {
		state = 1
	}
	return fmt.Sprintf("AT+%s=ACT,%d,%d", profile.marker, cid, state)
}

// testATDataCandidate mirrors the hardware-gate UZ901 identity: AT-capable
// modem with an ECM host network interface and no QMI control point.
func testATDataCandidate() modem.Candidate {
	return modem.Candidate{
		ID:               "zxic-test-uz901",
		VendorID:         "19d2",
		ProductID:        "0536",
		Manufacturer:     "ALK,Incorporated",
		Product:          "ALK Mobile Boardband",
		NetworkInterface: "enx344b50000000",
		ATPort: modem.Port{
			Path:            "/dev/ttyUSB0",
			Name:            "ttyUSB0",
			InterfaceNumber: 0x02,
			Role:            modem.PortRoleAT,
		},
	}
}

// mutableDiscoverer lets lifecycle tests change the discovered device set
// between Discover calls (plug/unplug simulation).
type mutableDiscoverer struct {
	mu         sync.Mutex
	candidates []modem.Candidate
}

func (discoverer *mutableDiscoverer) Discover(context.Context) ([]modem.Candidate, error) {
	discoverer.mu.Lock()
	defer discoverer.mu.Unlock()
	return append([]modem.Candidate(nil), discoverer.candidates...), nil
}

func (discoverer *mutableDiscoverer) setCandidates(candidates ...modem.Candidate) {
	discoverer.mu.Lock()
	defer discoverer.mu.Unlock()
	discoverer.candidates = append([]modem.Candidate(nil), candidates...)
}

// newStartedATDataTestManager starts a manager whose candidate carries a host
// network interface (AT backend) and whose host data link is injected.
func newStartedATDataTestManager(
	t *testing.T,
	client modem.Client,
	link atHostDataLink,
) (*Manager, string) {
	t.Helper()
	candidate := testATDataCandidate()
	id := candidate.ID
	opener := &staticOpener{client: client}
	manager, err := NewManager(Options{
		Discoverer:     staticDiscoverer{candidates: []modem.Candidate{candidate}},
		Opener:         opener,
		CommandTimeout: time.Second,
		LongTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	manager.atHostDataLink = link
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Stop(context.Background())
	})
	return manager, id
}

func TestSetNetworkATConfiguresPDPThenHostLease(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0", "+CGACT: 2,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
		{command: "AT+CGACT=0,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	result, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	})
	if err != nil {
		t.Fatalf("enable network: %v", err)
	}
	if !result.Enabled || result.Backend != "at" || result.APN != "internet" {
		t.Fatalf("enable result = %#v", result)
	}
	if result.Interface != "enx344b50000000" {
		t.Fatalf("enable interface = %q", result.Interface)
	}
	if started := link.startedInterfaces(); len(started) != 1 || started[0] != "enx344b50000000" {
		t.Fatalf("host link starts = %v", started)
	}
	session := link.lastSession()
	if session == nil || session.isClosed() {
		t.Fatal("host session was not kept open after enable")
	}

	result, err = manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: false, APN: "internet", IPVersion: "IP",
	})
	if err != nil {
		t.Fatalf("disable network: %v", err)
	}
	if result.Enabled || result.Backend != "at" {
		t.Fatalf("disable result = %#v", result)
	}
	if !session.isClosed() {
		t.Fatal("disable did not close the host session")
	}
	client.assertDone(t)
}

func TestSetNetworkATDeactivatesActiveContextBeforeReconfigure(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,1")},
		{command: "AT+CGACT=0,1", response: okResponse()},
		{command: `AT+CGDCONT=1,"IP","carrier.apn"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "carrier.apn", IPVersion: "IP",
	}); err != nil {
		t.Fatalf("enable network: %v", err)
	}
	client.assertDone(t)
}

func TestSetNetworkATRollsBackOnDHCPFailure(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
		{command: "AT+CGACT=0,1", response: okResponse()},
	}}
	link := &fakeATHostLink{err: errors.New("host lease unavailable")}
	manager, id := newStartedATDataTestManager(t, client, link)
	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	})
	if err == nil || !strings.Contains(err.Error(), "host lease unavailable") {
		t.Fatalf("enable error = %v, want host lease failure", err)
	}
	if started := link.startedInterfaces(); len(started) != 1 {
		t.Fatalf("host link starts = %v, want one attempt", started)
	}
	client.assertDone(t)
}

func TestSetNetworkATDisableIsFailClosed(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
		{command: "AT+CGACT=0,1", err: &modem.CommandError{Command: "AT+CGACT=0,1", Final: "ERROR"}},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err != nil {
		t.Fatalf("enable network: %v", err)
	}
	session := link.lastSession()
	if session == nil {
		t.Fatal("no host session after enable")
	}

	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: false, APN: "internet", IPVersion: "IP",
	})
	if err == nil {
		t.Fatal("disable error = nil, want modem deactivate failure")
	}
	if !session.isClosed() {
		t.Fatal("fail-closed disable did not close the host session before the modem error")
	}
	client.assertDone(t)
}

func TestSetNetworkATRejectsMissingInterface(t *testing.T) {
	client := &transcriptClient{}
	link := &fakeATHostLink{}
	manager, id := newStartedTestManager(t, client)
	manager.atHostDataLink = link
	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IP",
	})
	if !errors.Is(err, ErrDataBackendUnavailable) {
		t.Fatalf("error = %v, want ErrDataBackendUnavailable", err)
	}
	if started := link.startedInterfaces(); len(started) != 0 {
		t.Fatalf("host link starts = %v, want none", started)
	}
	client.assertDone(t)
}

// A device that lost its host interface must still be able to deactivate its
// PDP context: the interface and the platform link are only required to
// bring the data path up, so the disable path may not demand them.
func TestSetNetworkATDisableDoesNotRequireHostInterface(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT=0,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.candidate.NetworkInterface = ""

	result, err := manager.SetNetwork(context.Background(), id, NetworkRequest{Enabled: false})
	if err != nil {
		t.Fatalf("SetNetwork disable without a host interface: %v", err)
	}
	if result.Enabled {
		t.Fatal("SetNetwork disable reports enabled")
	}
	if result.Backend != "at" {
		t.Fatalf("disable backend = %q, want %q", result.Backend, "at")
	}
	client.assertDone(t)
}

// A recovery re-enable must not leave the previous DHCP process and its
// protected routes alive alongside the new lease: the old session would
// keep its udhcpc running, and its later deconfig would flush the
// per-interface table the new lease shares.
func TestSetNetworkATEnableClosesPreviousHostSession(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
		// The first enable activated context 1, so the recovery probe finds
		// it active and has to deactivate it before reconfiguring.
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,1")},
		{command: "AT+CGACT=0,1", response: okResponse()},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)

	// The first enable leaves a live host session behind.
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err != nil {
		t.Fatalf("first enable: %v", err)
	}
	first := link.lastSession()
	if first == nil {
		t.Fatal("first enable started no host session")
	}
	if first.isClosed() {
		t.Fatal("first host session is closed right after the enable")
	}

	// The recovery re-enable must tear the old session down first.
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err != nil {
		t.Fatalf("recovery enable: %v", err)
	}
	if !first.isClosed() {
		t.Fatal("recovery enable left the previous host session open")
	}
	if started := link.startedInterfaces(); len(started) != 2 {
		t.Fatalf("host link starts = %v, want two", started)
	}
}

func TestSetNetworkATUsesResolvedPDPProfile(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: "AT+MKPDP=CTX,1,IPV4V6,internet", response: okResponse()},
		{command: "AT+MKPDP=AUTH,1,2", response: okResponse()},
		{command: "AT+MKPDP=ATT,1", response: okResponse()},
		{command: "AT+MKPDP=ACT,1,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.atProfile.PDP = markerPDPProfile{marker: "MKPDP"}
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
		Username: "user", Password: "secret", Authentication: "CHAP",
	}); err != nil {
		t.Fatalf("enable network: %v", err)
	}
	client.assertDone(t)
}

func TestPrepareRegistrationUsesResolvedPDPProfile(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+MKPDP=CTX,1,IP,apn.example", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.atProfile.PDP = markerPDPProfile{marker: "MKPDP"}
	if err := manager.PrepareRegistration(context.Background(), id, "apn.example", "IP"); err != nil {
		t.Fatalf("PrepareRegistration: %v", err)
	}
	if started := link.startedInterfaces(); len(started) != 0 {
		t.Fatalf("host link starts = %v, want none for registration preparation", started)
	}
	client.assertDone(t)
}

func TestSetNetworkATBackendActivatesAndDeactivatesPDP(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
		{command: "AT+CGACT=0,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	result, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	})
	if err != nil {
		t.Fatalf("enable network: %v", err)
	}
	if !result.Enabled || result.Backend != "at" || result.APN != "internet" {
		t.Fatalf("enable result = %#v", result)
	}
	result, err = manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: false, APN: "internet", IPVersion: "IP",
	})
	if err != nil {
		t.Fatalf("disable network: %v", err)
	}
	if result.Enabled {
		t.Fatalf("disable result = %#v", result)
	}
	client.assertDone(t)
}

func TestSetNetworkATBackendAppliesPAPCredentials(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","giffgaff.com"`, response: okResponse()},
		{command: `AT+CGAUTH=1,1,"gg","p"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "giffgaff.com", IPVersion: "IPV4V6",
		Username: "gg", Password: "p", Authentication: "PAP",
	}); err != nil {
		t.Fatalf("enable authenticated network: %v", err)
	}
	client.assertDone(t)
}

func TestSetNetworkDoesNotExposeAPNCredentialsInErrorsOrState(t *testing.T) {
	const username = "private-user"
	const password = "private-password"
	command := `AT+CGAUTH=1,1,"` + username + `","` + password + `"`
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","giffgaff.com"`, response: okResponse()},
		{command: command, err: &modem.CommandError{Command: command, Final: "ERROR"}},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "giffgaff.com", IPVersion: "IPV4V6",
		Username: username, Password: password, Authentication: "PAP",
	})
	if err == nil {
		t.Fatal("SetNetwork() error = nil")
	}
	if strings.Contains(err.Error(), username) || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "AT+CGAUTH") {
		t.Fatalf("SetNetwork() exposed credentials: %q", err)
	}
	entry, getErr := manager.Get(id)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if strings.Contains(entry.LastError, username) || strings.Contains(entry.LastError, password) || strings.Contains(entry.LastError, "AT+CGAUTH") {
		t.Fatalf("device state exposed credentials: %q", entry.LastError)
	}
	client.assertDone(t)
}

func TestSetNetworkRejectsUnsafeAPNBeforeOpeningModem(t *testing.T) {
	client := &transcriptClient{}
	manager, id := newStartedTestManager(t, client)
	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: `internet";AT+CFUN=0`, IPVersion: "IP",
	})
	if !errors.Is(err, ErrInvalidNetworkAPN) {
		t.Fatalf("error = %v, want ErrInvalidNetworkAPN", err)
	}
	client.assertDone(t)
}

func TestUSBNetModeReadAndGuardedWrite(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="usbnet"`, response: okResponse(`+QCFG: "usbnet",0`)},
		{command: `AT+QCFG="usbnet",1`, response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)
	mode, err := manager.USBNetMode(context.Background(), id)
	if err != nil {
		t.Fatalf("read USB mode: %v", err)
	}
	if mode.Mode != 0 || mode.Name != "QMI" {
		t.Fatalf("USB mode = %#v", mode)
	}
	mode, err = manager.SetUSBNetMode(context.Background(), id, 1)
	if err != nil {
		t.Fatalf("write USB mode: %v", err)
	}
	if mode.Mode != 1 || mode.Name != "ECM" {
		t.Fatalf("new USB mode = %#v", mode)
	}
	client.assertDone(t)
}

func TestOperatorSelectionManualAndAutomatic(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+COPS=1,2,"46000",7`, response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 1,2,"46000",7`)},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	act := 7
	selection, err := manager.SetOperatorSelection(context.Background(), id, false, "46000", &act)
	if err != nil {
		t.Fatalf("manual selection: %v", err)
	}
	if selection.Mode != 1 || selection.Format != 2 || selection.Operator != "46000" || selection.AccessTechnology != "LTE" {
		t.Fatalf("manual selection = %#v", selection)
	}
	selection, err = manager.SetOperatorSelection(context.Background(), id, true, "", nil)
	if err != nil {
		t.Fatalf("automatic selection: %v", err)
	}
	if selection.Mode != 0 || selection.Operator != "46001" {
		t.Fatalf("automatic selection = %#v", selection)
	}
	client.assertDone(t)
}

func TestSetOperatorSelectionUsesATForZXICCandidate(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+COPS=1,2,"23415",7`, response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 1,2,"23415",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", modem.ResolveATProfile(modem.Candidate{
		VendorID: "19d2", ProductID: "0536",
	}).Operator)

	act := 7
	selection, err := manager.SetOperatorSelection(context.Background(), id, false, "23415", &act)
	if err != nil {
		t.Fatalf("SetOperatorSelection: %v", err)
	}
	if selection.Mode != 1 || selection.Format != 2 || selection.Operator != "23415" || selection.AccessTechnology != "LTE" {
		t.Fatalf("selection = %+v", selection)
	}
	client.assertDone(t)
}

func TestSetOperatorSelectionUsesResolvedOperatorProfile(t *testing.T) {
	profile := testOperatorProfile{
		manualCommand:     "AT+XOPS=manual",
		queryCommand:      "AT+XOPS?",
		manualPLMN:        "99998",
		manualAccessTech:  7,
		checkManualInput:  true,
		selectionResponse: "+XOPS: custom locked selection",
		selection: modem.ATOperatorSelection{
			Mode: 1, Format: 2, Operator: "99998", AccessTechnologyCode: "7",
		},
	}
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+XOPS=manual", response: okResponse()},
		{command: "AT+XOPS?", response: okResponse("+XOPS: custom locked selection")},
	}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", profile)

	act := 7
	selection, err := manager.SetOperatorSelection(context.Background(), id, false, "99998", &act)
	if err != nil {
		t.Fatalf("SetOperatorSelection: %v", err)
	}
	if selection.Mode != 1 || selection.Operator != "99998" || selection.AccessTechnology != "LTE" {
		t.Fatalf("selection did not use profile parser: %+v", selection)
	}
	client.assertDone(t)
}

func TestOperatorSelectionUsesResolvedOperatorProfile(t *testing.T) {
	profile := testOperatorProfile{
		queryCommand:      "AT+XOPS?",
		selectionResponse: "+XOPS: custom selection",
		selection: modem.ATOperatorSelection{
			Mode: 1, Format: 2, Operator: "99998", AccessTechnologyCode: "7",
		},
	}
	client := &transcriptClient{steps: []clientStep{{
		command: "AT+XOPS?", response: okResponse("+XOPS: custom selection"),
	}}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", profile)

	selection, err := manager.OperatorSelection(context.Background(), id)
	if err != nil {
		t.Fatalf("OperatorSelection: %v", err)
	}
	if selection.Mode != 1 || selection.Operator != "99998" || selection.AccessTechnology != "LTE" {
		t.Fatalf("selection did not use profile parser: %+v", selection)
	}
	client.assertDone(t)
}

func TestSetOperatorSelectionAutomaticUsesResolvedOperatorProfile(t *testing.T) {
	profile := testOperatorProfile{
		queryCommand:      "AT+XOPS?",
		automaticCommand:  "AT+XOPS=automatic",
		deregisterCommand: "AT+XOPS=detach",
		selectionResponse: "+XOPS: custom automatic selection",
		selection: modem.ATOperatorSelection{
			Mode: 0, Format: 2, Operator: "99998", AccessTechnologyCode: "7",
		},
	}
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+XOPS=detach", response: okResponse()},
		{command: "AT+XOPS=automatic", response: okResponse()},
		{command: "AT+XOPS?", response: okResponse("+XOPS: custom automatic selection")},
	}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", profile)

	selection, err := manager.SetOperatorSelection(context.Background(), id, true, "", nil)
	if err != nil {
		t.Fatalf("SetOperatorSelection automatic: %v", err)
	}
	if selection.Mode != 0 || selection.Operator != "99998" || selection.AccessTechnology != "LTE" {
		t.Fatalf("automatic selection did not use profile flow: %+v", selection)
	}
	client.assertDone(t)
}

func TestSetOperatorSelectionAutomaticKeepsQuectelScanModeResetForEveryModem(t *testing.T) {
	// The RAT-scan reset is a best-effort compatibility command. Quectel-based
	// modules are also sold under other USB identities (for example the DJI
	// 4G module), so narrowing it to one vendor ID would silently drop the
	// LTE-only lock recovery for those devices.
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"99998",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", modem.ResolveATProfile(modem.Candidate{
		VendorID: "19d2", ProductID: "0536",
	}).Operator)

	selection, err := manager.SetOperatorSelection(context.Background(), id, true, "", nil)
	if err != nil {
		t.Fatalf("SetOperatorSelection automatic: %v", err)
	}
	if selection.Mode != 0 || selection.Operator != "99998" {
		t.Fatalf("automatic selection = %+v", selection)
	}
	client.assertDone(t)
}

func TestReRegisterOperatorUsesResolvedOperatorProfile(t *testing.T) {
	profile := testOperatorProfile{
		queryCommand:      "AT+XOPS?",
		manualCommand:     "AT+XOPS=manual",
		deregisterCommand: "AT+XOPS=detach",
		manualPLMN:        "99998",
		manualAccessTech:  7,
		checkManualInput:  true,
		selectionResponse: "+XOPS:",
		selection: modem.ATOperatorSelection{
			Mode: 1, Format: 2, Operator: "99998", AccessTechnologyCode: "7",
		},
	}
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+XOPS?", response: okResponse("+XOPS: current selection")},
		{command: "AT+XOPS=detach", response: okResponse()},
		{command: "AT+XOPS=manual", response: okResponse()},
		{command: "AT+XOPS?", response: okResponse("+XOPS: reapplied selection")},
	}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", profile)

	selection, err := manager.ReRegisterOperator(context.Background(), id)
	if err != nil {
		t.Fatalf("ReRegisterOperator: %v", err)
	}
	if selection.Mode != 1 || selection.Operator != "99998" || selection.AccessTechnology != "LTE" {
		t.Fatalf("re-registration did not use profile flow: %+v", selection)
	}
	client.assertDone(t)
}

func TestOperatorSelectionRejectsAutomaticFallbackAsSuccess(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+COPS=1,2,"46000",7`, response: okResponse()},
		{command: "AT+COPS?", response: okResponse("+COPS: 0")},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	act := 7
	if _, err := manager.SetOperatorSelection(context.Background(), id, false, "46000", &act); err == nil {
		t.Fatal("manual selection must fail when readback returned automatic mode")
	}
	client.assertDone(t)
}

func TestOperatorSelectionCommandFailureRestoresAutomaticMode(t *testing.T) {
	selectionErr := errors.New("+CME ERROR: 30")
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+COPS=1,2,"46000",7`, err: selectionErr},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	act := 7
	_, err := manager.SetOperatorSelection(context.Background(), id, false, "46000", &act)
	if !errors.Is(err, selectionErr) {
		t.Fatalf("error = %v, want wrapped selection error", err)
	}
	client.assertDone(t)
}

func TestReRegisterOperatorReappliesAutomaticMode(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	selection, err := manager.ReRegisterOperator(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != 0 || selection.Operator != "46001" {
		t.Fatalf("selection = %#v", selection)
	}
	client.assertDone(t)
}

func TestReRegisterOperatorPreservesManualLock(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+COPS?", response: okResponse(`+COPS: 1,2,"46003",7`)},
		{command: "AT+COPS=2", response: okResponse()},
		{command: `AT+COPS=1,2,"46003",7`, response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 1,2,"46003",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	selection, err := manager.ReRegisterOperator(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != 1 || selection.Operator != "46003" || selection.AccessTechnology != "LTE" {
		t.Fatalf("selection = %#v", selection)
	}
	client.assertDone(t)
}

func TestReRegisterOperatorRecoversDeregisteredModeWithAutomaticSelection(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+COPS?", response: okResponse(`+COPS: 2`)},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	selection, err := manager.ReRegisterOperator(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != 0 || selection.Operator != "46001" {
		t.Fatalf("selection = %#v", selection)
	}
	client.assertDone(t)
}
