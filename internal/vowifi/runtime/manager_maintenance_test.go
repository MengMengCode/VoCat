package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

// The first setup waits for cancellation so maintenance races with a real
// lifecycle operation, rather than only with an already idle runtime.
type maintenanceTunnelProvider struct {
	mu       sync.Mutex
	attempts int
	started  chan struct{}
	canceled chan struct{}
}

func (provider *maintenanceTunnelProvider) Start(ctx context.Context, _ vowifi.TunnelRequest) (vowifi.TunnelSession, error) {
	provider.mu.Lock()
	provider.attempts++
	first := provider.attempts == 1
	provider.mu.Unlock()
	if first {
		close(provider.started)
		<-ctx.Done()
		close(provider.canceled)
		return nil, ctx.Err()
	}
	return fakeTunnelSession{}, nil
}

func (provider *maintenanceTunnelProvider) Attempts() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.attempts
}

func waitMaintenanceRuntime(t *testing.T, manager *Manager, deviceID string, ready func(*entry, vowifi.State) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		item := manager.entries[deviceID]
		state := item.orchestrator.State()
		ok := ready(item, state)
		manager.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	state, _ := manager.State(deviceID)
	t.Fatalf("runtime did not reach expected maintenance state: %+v", state)
}

func TestMaintenanceCancelsSetupAndDiscardsQueuedReconnect(t *testing.T) {
	provider := &maintenanceTunnelProvider{started: make(chan struct{}), canceled: make(chan struct{})}
	manager := New(Options{OperationTimeout: time.Second, RetryInitial: 5 * time.Millisecond})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestratorWithTunnel(t, "410", provider)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("setup did not start")
	}
	if _, err := manager.RequestReconnect("410"); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	queued := manager.entries["410"].reconnectPending
	manager.mu.Unlock()
	if !queued {
		t.Fatal("pre-maintenance reconnect was not queued")
	}
	if err := manager.BeginMaintenance("410"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.canceled:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not cancel in-flight setup")
	}
	if _, err := manager.RequestEnabled("410", false); err != nil {
		t.Fatalf("maintenance disable failed: %v", err)
	}
	// Route updates and stale persisted policy must not requeue setup while
	// switching the SIM. They must not be replayed by EndMaintenance either.
	if _, err := manager.RequestReconnect("410"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && !item.desiredEnabled && !item.reconnectPending &&
			!item.autoRetryPending && !state.Enabled && state.Phase == vowifi.PhaseIdle
	})
	if got := provider.Attempts(); got != 1 {
		t.Fatalf("setup attempts during maintenance = %d, want 1", got)
	}
	manager.EndMaintenance("410")
	manager.mu.Lock()
	desired := manager.entries["410"].desiredEnabled
	manager.mu.Unlock()
	if desired {
		t.Fatal("ending maintenance restored the old desired policy")
	}
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && state.Phase == vowifi.PhaseSMSReady
	})
	if got := provider.Attempts(); got != 2 {
		t.Fatalf("setup attempts after explicit target policy = %d, want 2", got)
	}
}

func TestNestedMaintenanceRequiresAllScopesToEnd(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "410")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := manager.BeginMaintenance("410"); err != nil {
			t.Fatal(err)
		}
	}
	manager.EndMaintenance("410")
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestReconnect("410"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("410", false); err != nil {
		t.Fatal(err)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && item.maintenanceDepth == 1 && !item.desiredEnabled &&
			!item.reconnectPending && state.Phase == vowifi.PhaseIdle
	})
	manager.EndMaintenance("410")
	manager.EndMaintenance("410") // An unmatched end must not make the depth negative.
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && item.maintenanceDepth == 0 && state.Phase == vowifi.PhaseSMSReady
	})
}

func TestQueuedWorkerFromOldMaintenanceEpochCannotRun(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "410")); err != nil {
		t.Fatal(err)
	}
	// Capture the state of a worker queued before maintenance, then hold its
	// execution until the entire maintenance scope has finished.
	manager.mu.Lock()
	item := manager.entries["410"]
	item.busy = true
	epoch := item.operationEpoch
	manager.wg.Add(1)
	manager.mu.Unlock()
	if err := manager.BeginMaintenance("410"); err != nil {
		t.Fatal(err)
	}
	manager.EndMaintenance("410")
	// This new target policy clears disablePending while the worker is queued;
	// an epoch check is still required to discard its old operation.
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	var oldOperationCalls atomic.Int32
	manager.runOperations("410", item, func(context.Context, *vowifi.Orchestrator) error {
		oldOperationCalls.Add(1)
		return nil
	}, epoch)
	if got := oldOperationCalls.Load(); got != 0 {
		t.Fatalf("old queued operation ran %d times after maintenance", got)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && state.Phase == vowifi.PhaseSMSReady
	})
}

func TestOldRetryTimerCannotRestartOrClearTargetProfileRetry(t *testing.T) {
	provider := &flakyTunnelProvider{failures: 100}
	manager := New(Options{
		OperationTimeout: time.Second,
		RetryInitial:     100 * time.Millisecond,
		RetryMaximum:     100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestratorWithTunnel(t, "410", provider)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && item.autoRetryPending && state.Phase == vowifi.PhaseFailed
	})
	if err := manager.BeginMaintenance("410"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("410", false); err != nil {
		t.Fatal(err)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && !item.autoRetryPending && state.Phase == vowifi.PhaseIdle
	})
	manager.EndMaintenance("410")
	// Give the new profile a longer retry delay. The old timer will expire
	// first while the new profile is also Failed and wants to reconnect.
	manager.mu.Lock()
	manager.retryInitial = time.Second
	manager.retryMaximum = time.Second
	manager.mu.Unlock()
	if _, err := manager.RequestEnabled("410", true); err != nil {
		t.Fatal(err)
	}
	waitMaintenanceRuntime(t, manager, "410", func(item *entry, state vowifi.State) bool {
		return !item.busy && item.autoRetryPending && state.Phase == vowifi.PhaseFailed
	})
	time.Sleep(200 * time.Millisecond)
	manager.mu.Lock()
	pending := manager.entries["410"].autoRetryPending
	manager.mu.Unlock()
	if !pending {
		t.Fatal("old timer cleared the target profile's pending retry")
	}
	if got := provider.Attempts(); got != 2 {
		t.Fatalf("old timer restarted setup early: attempts=%d, want 2", got)
	}
}
