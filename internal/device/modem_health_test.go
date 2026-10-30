package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"vocat/internal/remoteproc"
)

func TestModemCrashStopsATAndRecoveryOpensFreshSession(t *testing.T) {
	first := &transcriptClient{steps: []clientStep{{command: "ATI", response: okResponse("old modem")}}}
	manager, id := newStartedTestManager(t, first)
	observation := remoteproc.Observation{Path: "modem", State: "running", Generation: 1}
	var healthErr error
	manager.remoteprocObserver = func(string) (remoteproc.Observation, error) { return observation, healthErr }
	if _, err := manager.ExecuteAT(context.Background(), id, "ATI"); err != nil {
		t.Fatal(err)
	}
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	state.snapshot = &Snapshot{Responsive: true, SIMReady: true}
	manager.mu.Unlock()
	healthErr = remoteproc.ErrUnavailable
	observation.State, observation.Generation = "crashed", 2
	for range 2 {
		if _, err := manager.ExecuteAT(context.Background(), id, "ATI"); !errors.Is(err, remoteproc.ErrUnavailable) {
			t.Fatalf("command reached crashed modem: %v", err)
		}
	}
	first.assertDone(t)
	if first.closeCount != 1 {
		t.Fatalf("stale session closed %d times, want 1", first.closeCount)
	}
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Snapshot.Responsive || entry.LastError == "" {
		t.Fatalf("missing hardware failure state: %+v", entry)
	}
	second := &transcriptClient{steps: []clientStep{{command: "ATI", response: okResponse("recovered modem")}}}
	manager.opener.(*staticOpener).client = second
	healthErr = nil
	observation.State = "running"
	if _, err := manager.ExecuteAT(context.Background(), id, "ATI"); err != nil {
		t.Fatal(err)
	}
	second.assertDone(t)
	if manager.opener.(*staticOpener).openCount != 2 {
		t.Fatal("recovery reused stale AT session")
	}
}

func TestProfileVerificationATWaitHonorsRecoveryDeadline(t *testing.T) {
	client := &transcriptClient{}
	manager, id := newStartedTestManager(t, client)
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := manager.ExecuteAT(ctx, id, "AT+CCID"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("verification waited beyond its context: %v", err)
	}
	client.assertDone(t)
}
