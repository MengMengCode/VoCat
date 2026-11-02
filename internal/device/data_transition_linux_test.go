//go:build linux

package device

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Production QMI Close consumes its transport even if releasing the client ID
// fails; methods cannot be called on that session after Close returns.
type terminalQMIErrorSession struct {
	*fakeQMIDataSession
	closeErr error
	stops    int
}

func (session *terminalQMIErrorSession) Stop(ctx context.Context, handle uint32) error {
	session.stops++
	if session.closed {
		return errors.New("attempted to reuse a closed QMI transport")
	}
	return session.fakeQMIDataSession.Stop(ctx, handle)
}

func (session *terminalQMIErrorSession) Close() error {
	session.closed = true
	return session.closeErr
}

func TestATTakeoverStopsOnlyOwnedWDSAndItsEventWatch(t *testing.T) {
	runner := mockTransitionQMIHostCleanup(t)
	manager, id := newStartedATDataTestManager(t, &transcriptClient{steps: transitionATSteps()}, &fakeATHostLink{})
	state, _ := manager.lookup(id)
	owned := &fakeQMIDataSession{}
	state.dataSession, state.dataSessionHandle, state.dataSessionControl = owned, 123, "/dev/cdc-wdm-fixture"
	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	state.dataEventCancel = cancelWatch
	manager.qmiDataOpener = func(context.Context, string) (qmiDataSession, error) {
		t.Error("takeover opened an unrelated QMI session")
		return nil, errors.New("unexpected QMI open")
	}
	if _, err := manager.SetNetwork(context.Background(), id, transitionATRequest()); err != nil {
		t.Fatal(err)
	}
	if !owned.closed || owned.stopHandle != 123 || state.dataSession != nil || state.dataSessionHandle != 0 ||
		state.dataSessionControl != "" || state.dataEventCancel != nil || watchContext.Err() == nil {
		t.Fatal("AT takeover retained the previous WDS owner or its event watch")
	}
	commands := strings.Join(runner.commandLines(), "\n")
	if !strings.Contains(commands, "addr flush dev enx344b50000000 scope global") ||
		!strings.Contains(commands, "link set dev enx344b50000000 down") {
		t.Fatal("AT takeover retained the previous QMI host path")
	}
}

func TestATTakeoverDoesNotStartWhenOwnedWDSStopFails(t *testing.T) {
	mockTransitionQMIHostCleanup(t)
	stopErr := errors.New("injected WDS stop failure")
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, &transcriptClient{}, link)
	state, _ := manager.lookup(id)
	owned := &fakeQMIDataSession{stopErr: stopErr}
	state.dataSession, state.dataSessionHandle, state.dataSessionControl = owned, 123, "/dev/cdc-wdm-fixture"
	if _, err := manager.SetNetwork(context.Background(), id, transitionATRequest()); !errors.Is(err, stopErr) {
		t.Fatalf("takeover error = %v, want WDS stop failure", err)
	}
	if owned.closed || state.dataSession != owned || len(link.startedInterfaces()) != 0 {
		t.Fatal("failed WDS teardown lost its owner or started an overlapping AT lease")
	}
}

func TestATTakeoverDoesNotReuseQMITransportAfterCloseError(t *testing.T) {
	mockTransitionQMIHostCleanup(t)
	closeErr := errors.New("injected WDS client-ID release failure")
	owned := &terminalQMIErrorSession{fakeQMIDataSession: &fakeQMIDataSession{}, closeErr: closeErr}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, &transcriptClient{steps: transitionATSteps()}, link)
	state, _ := manager.lookup(id)
	state.dataSession, state.dataSessionHandle, state.dataSessionControl = owned, 123, "/dev/cdc-wdm-fixture"
	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	state.dataEventCancel = cancelWatch
	if _, err := manager.SetNetwork(context.Background(), id, transitionATRequest()); !errors.Is(err, closeErr) {
		t.Fatalf("takeover lost the terminal Close error: %v", err)
	}
	if state.dataSession != nil || state.dataSessionHandle != 0 || state.dataSessionControl != "" ||
		state.dataEventCancel != nil || watchContext.Err() == nil || len(link.startedInterfaces()) != 0 {
		t.Fatal("terminal Close retained its consumed owner or allowed overlapping startup")
	}
	if _, err := manager.SetNetwork(context.Background(), id, transitionATRequest()); err != nil {
		t.Fatalf("retry attempted to use the consumed QMI session: %v", err)
	}
	if owned.stops != 1 || len(link.startedInterfaces()) != 1 {
		t.Fatal("retry reused the closed QMI transport instead of starting a clean AT lease")
	}
}

func TestATDisableReleasesOwnedQMIHostPath(t *testing.T) {
	runner := mockTransitionQMIHostCleanup(t)
	client := &transcriptClient{steps: []clientStep{{command: "AT+CGACT=0,1", response: okResponse()}}}
	manager, id := newStartedATDataTestManager(t, client, &fakeATHostLink{})
	state, _ := manager.lookup(id)
	owned := &fakeQMIDataSession{}
	state.dataSession, state.dataSessionHandle, state.dataSessionControl = owned, 123, "/dev/cdc-wdm-fixture"
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{Enabled: false, Backend: "at"}); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.commandLines(), "\n")
	if !owned.closed || owned.stopHandle != 123 || state.dataSession != nil ||
		!strings.Contains(commands, "addr flush dev enx344b50000000 scope global") ||
		!strings.Contains(commands, "link set dev enx344b50000000 down") {
		t.Fatal("AT disable retained the previous QMI host path or its WDS owner")
	}
	client.assertDone(t)
}

func mockTransitionQMIHostCleanup(t *testing.T) *fakeHostRunner {
	t.Helper()
	oldLookup, oldRunner, oldDNSDir := lookPathIP, hostCommandOutput, exportProxyDNSDir
	runner := &fakeHostRunner{}
	lookPathIP = func(string) (string, error) { return "/fake/ip", nil }
	hostCommandOutput = runner.run
	exportProxyDNSDir = t.TempDir()
	t.Cleanup(func() {
		lookPathIP, hostCommandOutput, exportProxyDNSDir = oldLookup, oldRunner, oldDNSDir
	})
	return runner
}

func TestExplicitATFailureRetainsExpectedBackendOnDualControlDevice(t *testing.T) {
	client := &transcriptClient{steps: append(transitionATSteps(), clientStep{
		command: "AT+CGACT=0,1", response: okResponse(),
	})}
	manager, id := newStartedATDataTestManager(t, client, &fakeATHostLink{err: errors.New("DHCP unavailable")})
	state, _ := manager.lookup(id)
	manager.mu.Lock()
	state.candidate.QMIControl = "/dev/cdc-wdm-fixture"
	manager.mu.Unlock()
	if _, err := manager.SetNetwork(context.Background(), id, transitionATRequest()); err == nil {
		t.Fatal("expected host setup failure")
	}
	status, err := manager.NetworkStatus(context.Background(), id)
	if err != nil || status.Backend != "at" || status.Connected {
		t.Fatalf("explicit AT failure was observed as another backend: %+v, %v", status, err)
	}
}
