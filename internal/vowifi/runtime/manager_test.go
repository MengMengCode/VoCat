package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

type fakeSIM struct{}

func (fakeSIM) ReadIdentity(context.Context, string) (vowifi.SIMIdentity, error) {
	return vowifi.SIMIdentity{
		ICCID:           "8944100000000000000",
		HomeMCC:         "234",
		HomeMNC:         "15",
		HomeCountryCode: "GB",
	}, nil
}

type fakeAKA struct{}

func (fakeAKA) CheckReady(context.Context, vowifi.SIMIdentity) (vowifi.AKAEvidence, error) {
	return vowifi.AKAEvidence{Ready: true, Application: "USIM"}, nil
}

func (fakeAKA) Authenticate(context.Context, vowifi.SIMIdentity, vowifi.AKAChallenge) (vowifi.AKAResult, error) {
	return vowifi.AKAResult{}, nil
}

type fakeRadio struct{}

func (fakeRadio) Snapshot(context.Context, string) (vowifi.RadioSnapshot, error) {
	return vowifi.RadioSnapshot{CellularDataEnabled: true, OperatingMode: 1}, nil
}
func (fakeRadio) StopCellularData(context.Context, string) error { return nil }
func (fakeRadio) EnterVoWiFiRFOff(context.Context, string) error { return nil }
func (fakeRadio) Restore(context.Context, string, vowifi.RadioSnapshot) error {
	return nil
}

type fakeProxy struct{}

func (fakeProxy) Resolve(context.Context, vowifi.ProxyRequest) (vowifi.ProxyRoute, error) {
	return vowifi.ProxyRoute{Mode: vowifi.ProxyModeDirect}, nil
}

type fakeTunnelProvider struct{}
type fakeTunnelSession struct{}

func (fakeTunnelProvider) Start(context.Context, vowifi.TunnelRequest) (vowifi.TunnelSession, error) {
	return fakeTunnelSession{}, nil
}
func (fakeTunnelSession) Evidence() vowifi.TunnelEvidence {
	return vowifi.TunnelEvidence{
		Established:   true,
		Name:          "xfrm-test",
		ResponderAUTH: vowifi.ResponderAUTHVerified,
	}
}
func (fakeTunnelSession) Close(context.Context) error { return nil }

type flakyTunnelProvider struct {
	mu       sync.Mutex
	attempts int
	failures int
}

func (provider *flakyTunnelProvider) Start(
	context.Context,
	vowifi.TunnelRequest,
) (vowifi.TunnelSession, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.attempts++
	if provider.attempts <= provider.failures {
		return nil, errors.New("temporary tunnel failure")
	}
	return fakeTunnelSession{}, nil
}

func (provider *flakyTunnelProvider) Attempts() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.attempts
}

type fakeIMSProvider struct{}
type fakeIMSSession struct{}

func (fakeIMSProvider) Start(context.Context, vowifi.IMSRequest) (vowifi.IMSSession, error) {
	return fakeIMSSession{}, nil
}
func (fakeIMSSession) Evidence() vowifi.IMSEvidence {
	return vowifi.IMSEvidence{
		Registered:        true,
		RegistrationState: "registered",
		AssociatedMSISDN:  "+447700900123",
	}
}
func (fakeIMSSession) EnableSMS(context.Context) (vowifi.SMSEvidence, error) {
	return vowifi.SMSEvidence{Ready: true}, nil
}
func (fakeIMSSession) Close(context.Context) error { return nil }

type fakePhones struct{}

func (fakePhones) SaveAssociatedNumber(context.Context, vowifi.PhoneRecord) error {
	return nil
}

func testOrchestrator(t *testing.T, id string) *vowifi.Orchestrator {
	t.Helper()
	orchestrator, err := vowifi.New(vowifi.Dependencies{
		SIM:    fakeSIM{},
		AKA:    fakeAKA{},
		Radio:  fakeRadio{},
		Proxy:  fakeProxy{},
		Tunnel: fakeTunnelProvider{},
		IMS:    fakeIMSProvider{},
		Phones: fakePhones{},
	}, vowifi.Options{DeviceID: id})
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func TestManagerCancelUSSIUnavailableNeverReportsSuccess(t *testing.T) {
	manager := New(Options{})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.CancelUSSI(context.Background(), "missing", "dialog-1"); !errors.Is(err, vowifi.ErrUSSINotReady) {
		t.Fatalf("unregistered cancel = %v", err)
	}
	orchestrator := testOrchestrator(t, "saved")
	if err := manager.Register(orchestrator); err != nil {
		t.Fatal(err)
	}
	if err := manager.CancelUSSI(context.Background(), "saved", "dialog-1"); !errors.Is(err, vowifi.ErrUSSINotReady) {
		t.Fatalf("idle cancel = %v", err)
	}
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.CancelUSSI(context.Background(), "saved", "dialog-1"); !errors.Is(err, vowifi.ErrUSSINotReady) {
		t.Fatalf("unsupported cancel = %v", err)
	}
}

func testOrchestratorWithTunnel(
	t *testing.T,
	id string,
	tunnel vowifi.TunnelProvider,
) *vowifi.Orchestrator {
	t.Helper()
	orchestrator, err := vowifi.New(vowifi.Dependencies{
		SIM:    fakeSIM{},
		AKA:    fakeAKA{},
		Radio:  fakeRadio{},
		Proxy:  fakeProxy{},
		Tunnel: tunnel,
		IMS:    fakeIMSProvider{},
		Phones: fakePhones{},
	}, vowifi.Options{DeviceID: id})
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func TestManagerRunsAndPublishesEnable(t *testing.T) {
	var mu sync.Mutex
	var states []vowifi.State
	manager := New(Options{
		OperationTimeout: time.Second,
		OnState: func(_ context.Context, state vowifi.State) error {
			mu.Lock()
			states = append(states, state)
			mu.Unlock()
			return nil
		},
	})
	t.Cleanup(func() {
		_ = manager.Close(context.Background())
	})
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := manager.State("ec20")
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == vowifi.PhaseSMSReady {
			if state.PhoneNumber != "+447700900123" {
				t.Fatalf("phone number = %q", state.PhoneNumber)
			}
			mu.Lock()
			published := len(states)
			mu.Unlock()
			if published > 0 {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("enable did not finish")
}

func TestManagerRetriesEnabledPolicyUntilReady(t *testing.T) {
	provider := &flakyTunnelProvider{failures: 2}
	manager := New(Options{
		OperationTimeout: time.Second,
		RetryInitial:     5 * time.Millisecond,
		RetryMaximum:     10 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestratorWithTunnel(t, "ec20", provider)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := manager.State("ec20")
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == vowifi.PhaseSMSReady {
			if attempts := provider.Attempts(); attempts != 3 {
				t.Fatalf("tunnel attempts = %d, want 3", attempts)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("VoWiFi did not become ready after retries; attempts=%d", provider.Attempts())
}

func TestManagerStopsAutomaticRetryWhenPolicyIsDisabled(t *testing.T) {
	provider := &flakyTunnelProvider{failures: 100}
	manager := New(Options{
		OperationTimeout: time.Second,
		RetryInitial:     100 * time.Millisecond,
		RetryMaximum:     100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestratorWithTunnel(t, "ec20", provider)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		pending := manager.entries["ec20"].autoRetryPending
		manager.mu.Unlock()
		if pending {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := manager.RequestEnabled("ec20", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if attempts := provider.Attempts(); attempts != 1 {
		t.Fatalf("tunnel attempts after disable = %d, want 1", attempts)
	}
	state, err := manager.State("ec20")
	if err != nil {
		t.Fatal(err)
	}
	if state.Enabled || state.Phase != vowifi.PhaseIdle {
		t.Fatalf("state after disabling retry policy = %+v", state)
	}
}

func TestManagerAcceptsRepeatedEnableWhileBusy(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := manager.startOperation("ec20", false, func(context.Context, *vowifi.Orchestrator) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatalf("repeated desired state was rejected: %v", err)
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := manager.State("ec20")
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == vowifi.PhaseSMSReady {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("desired enable was not reconciled after the active operation")
}

func TestManagerReEnablesWhenSwitchChangesDuringDisable(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatal(err)
	}
	waitForPhase := func(want vowifi.Phase) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			state, err := manager.State("ec20")
			if err != nil {
				t.Fatal(err)
			}
			if state.Phase == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("phase did not become %s", want)
	}
	waitForPhase(vowifi.PhaseSMSReady)

	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := manager.startOperation("ec20", false, func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
		close(started)
		<-release
		_, err := orchestrator.Disable(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	manager.mu.Lock()
	manager.entries["ec20"].desiredEnabled = false
	manager.mu.Unlock()
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatalf("enable while disable is active: %v", err)
	}
	close(release)
	waitForPhase(vowifi.PhaseSMSReady)
}

func TestManagerRejectsUnknownDevice(t *testing.T) {
	manager := New(Options{})
	t.Cleanup(func() {
		_ = manager.Close(context.Background())
	})
	if _, err := manager.RequestEnabled("missing", true); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("error = %v", err)
	}
}

func TestManagerCreatesRuntimeOnDemand(t *testing.T) {
	created := 0
	manager := New(Options{
		Factory: func(_ context.Context, deviceID string) (*vowifi.Orchestrator, error) {
			created++
			return testOrchestrator(t, deviceID), nil
		},
	})
	t.Cleanup(func() {
		_ = manager.Close(context.Background())
	})

	state, err := manager.State("hot-added-ec20")
	if err != nil {
		t.Fatal(err)
	}
	if state.DeviceID != "hot-added-ec20" || created != 1 {
		t.Fatalf("state = %#v, created = %d", state, created)
	}
	if _, err := manager.RequestEnabled("hot-added-ec20", true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.State("hot-added-ec20"); err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("factory calls = %d", created)
	}
}

func TestManagerCoalescesReconnectWhileLifecycleOperationIsBusy(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		busy := manager.entries["ec20"].busy
		manager.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(time.Millisecond)
	}
	before := manager.entries["ec20"].orchestrator.State()
	if before.Phase != vowifi.PhaseSMSReady {
		t.Fatalf("initial phase = %s", before.Phase)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := manager.startOperation("ec20", false, func(context.Context, *vowifi.Orchestrator) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := manager.RequestReconnect("ec20"); err != nil {
		t.Fatalf("queued reconnect error = %v", err)
	}
	// Repeated route changes collapse into the same pending reconnect.
	if _, err := manager.RequestReconnect("ec20"); err != nil {
		t.Fatalf("second queued reconnect error = %v", err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		close(release)
		t.Fatalf("repeated desired enable was rejected: %v", err)
	}
	manager.mu.Lock()
	pending := manager.entries["ec20"].reconnectPending
	manager.mu.Unlock()
	if !pending {
		t.Fatal("reconnect was not queued")
	}
	close(release)

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		busy := manager.entries["ec20"].busy
		manager.mu.Unlock()
		state := manager.entries["ec20"].orchestrator.State()
		if !busy && state.Phase == vowifi.PhaseSMSReady && state.Sequence > before.Sequence {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queued reconnect did not run after the active operation")
}

func TestManagerUnregisterStopsWorkerAndRemovesDevice(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20-to-delete")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.State("ec20-to-delete"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Unregister(context.Background(), "ec20-to-delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.State("ec20-to-delete"); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("expected ErrNotRegistered, got %v", err)
	}
	if err := manager.Unregister(context.Background(), "ec20-to-delete"); err != nil {
		t.Fatal(err)
	}
}
