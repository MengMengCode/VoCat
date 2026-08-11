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

func TestManagerInvalidateRebuildsQuiescedRuntimeOnDemand(t *testing.T) {
	created := 0
	manager := New(Options{
		Factory: func(_ context.Context, deviceID string) (*vowifi.Orchestrator, error) {
			created++
			return testOrchestrator(t, deviceID), nil
		},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	if _, err := manager.State("ec20"); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	first := manager.entries["ec20"].orchestrator
	manager.mu.Unlock()
	if err := manager.Invalidate(context.Background(), "ec20"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	manager.mu.Lock()
	remaining := manager.entries["ec20"]
	manager.mu.Unlock()
	if remaining != nil {
		t.Fatal("invalidated runtime remained cached")
	}

	if _, err := manager.State("ec20"); err != nil {
		t.Fatalf("rebuild State: %v", err)
	}
	manager.mu.Lock()
	second := manager.entries["ec20"].orchestrator
	manager.mu.Unlock()
	if created != 2 || second == first {
		t.Fatalf("factory calls = %d, first=%p second=%p", created, first, second)
	}
}

func TestManagerInvalidateRejectsRuntimeThatIsNotQuiesced(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Invalidate(context.Background(), "ec20"); !errors.Is(err, ErrNotQuiesced) {
		t.Fatalf("Invalidate error = %v, want ErrNotQuiesced", err)
	}
	manager.mu.Lock()
	remaining := manager.entries["ec20"]
	manager.mu.Unlock()
	if remaining == nil {
		t.Fatal("active runtime was removed")
	}
}

func TestManagerSubscriberChangeBlocksEveryRuntimeEntryPointUntilRelease(t *testing.T) {
	created := 0
	manager := New(Options{
		Factory: func(_ context.Context, deviceID string) (*vowifi.Orchestrator, error) {
			created++
			return testOrchestrator(t, deviceID), nil
		},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	if _, err := manager.State("ec20"); err != nil {
		t.Fatal(err)
	}
	release, err := manager.BeginSubscriberChange(context.Background(), "ec20")
	if err != nil {
		t.Fatalf("BeginSubscriberChange: %v", err)
	}
	assertBlocked := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrSubscriberChangeInProgress) {
			t.Fatalf("%s error = %v, want ErrSubscriberChangeInProgress", name, err)
		}
	}

	assertBlocked("Ensure", manager.Ensure(context.Background(), "ec20"))
	_, err = manager.State("ec20")
	assertBlocked("State", err)
	_, err = manager.RequestEnabled("ec20", true)
	assertBlocked("RequestEnabled", err)
	_, err = manager.RequestReconnect("ec20")
	assertBlocked("RequestReconnect", err)
	_, err = manager.SendSMS(context.Background(), "ec20", vowifi.SMSSubmitRequest{})
	assertBlocked("SendSMS", err)
	_, err = manager.Calls("ec20")
	assertBlocked("Calls", err)
	_, err = manager.DialCall(context.Background(), "ec20", "+447700900123")
	assertBlocked("DialCall", err)
	_, err = manager.AnswerCall(context.Background(), "ec20", "call-1")
	assertBlocked("AnswerCall", err)
	assertBlocked("HangupCall", manager.HangupCall(context.Background(), "ec20", "call-1"))
	assertBlocked("SendDTMF", manager.SendDTMF(context.Background(), "ec20", "call-1", '1', 100*time.Millisecond))
	_, err = manager.CallMedia(context.Background(), "ec20", "call-1")
	assertBlocked("CallMedia", err)
	candidate := testOrchestrator(t, "ec20")
	assertBlocked("Register", manager.Register(candidate))
	_ = candidate.Close(context.Background())

	if created != 1 {
		t.Fatalf("factory calls while guarded = %d, want 1", created)
	}
	release()
	release()
	if _, err := manager.State("ec20"); err != nil {
		t.Fatalf("State after release: %v", err)
	}
	if created != 2 {
		t.Fatalf("factory calls after release = %d, want 2", created)
	}

	// A stale/idempotent release must not clear a later guard for the device.
	releaseAgain, err := manager.BeginSubscriberChange(context.Background(), "ec20")
	if err != nil {
		t.Fatalf("second BeginSubscriberChange: %v", err)
	}
	release()
	_, err = manager.State("ec20")
	assertBlocked("State under second guard", err)
	releaseAgain()
}

func TestManagerStateRacesSubscriberChangeWithoutReturningRemovedEntry(t *testing.T) {
	manager := New(Options{
		Factory: func(_ context.Context, deviceID string) (*vowifi.Orchestrator, error) {
			return testOrchestrator(t, deviceID), nil
		},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if _, err := manager.State("ec20"); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 200; attempt++ {
		start := make(chan struct{})
		stateResult := make(chan error, 1)
		type beginResult struct {
			release func()
			err     error
		}
		beginResultChannel := make(chan beginResult, 1)
		go func() {
			<-start
			_, err := manager.State("ec20")
			stateResult <- err
		}()
		go func() {
			<-start
			release, err := manager.BeginSubscriberChange(context.Background(), "ec20")
			beginResultChannel <- beginResult{release: release, err: err}
		}()
		close(start)

		begin := <-beginResultChannel
		if begin.err != nil {
			t.Fatalf("attempt %d BeginSubscriberChange: %v", attempt, begin.err)
		}
		stateErr := <-stateResult
		if stateErr != nil && !errors.Is(stateErr, ErrSubscriberChangeInProgress) {
			t.Fatalf("attempt %d State error = %v", attempt, stateErr)
		}
		begin.release()
		if _, err := manager.State("ec20"); err != nil {
			t.Fatalf("attempt %d rebuild State: %v", attempt, err)
		}
	}
}

func TestManagerSubscriberChangeWaitsForOldWatcherToExit(t *testing.T) {
	persistStarted := make(chan struct{})
	allowPersistReturn := make(chan struct{})
	persisted := make(chan struct{})
	manager := New(Options{
		OnState: func(context.Context, vowifi.State) error {
			close(persistStarted)
			<-allowPersistReturn
			close(persisted)
			return nil
		},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}
	<-persistStarted

	type beginResult struct {
		release func()
		err     error
	}
	result := make(chan beginResult, 1)
	go func() {
		release, err := manager.BeginSubscriberChange(context.Background(), "ec20")
		result <- beginResult{release: release, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		blocked := manager.subscriberChangeLocked("ec20")
		manager.mu.Unlock()
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber-change barrier was not installed")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case got := <-result:
		t.Fatalf("BeginSubscriberChange returned before watcher joined: %v", got.err)
	default:
	}
	if _, err := manager.State("ec20"); !errors.Is(err, ErrSubscriberChangeInProgress) {
		t.Fatalf("State while watcher drains = %v, want barrier error", err)
	}

	close(allowPersistReturn)
	got := <-result
	if got.err != nil {
		t.Fatalf("BeginSubscriberChange: %v", got.err)
	}
	select {
	case <-persisted:
	default:
		t.Fatal("subscriber change returned before old persistence completed")
	}
	got.release()
}

func TestManagerSubscriberChangeClearsStaleDesiredPolicyAndRetry(t *testing.T) {
	manager := New(Options{})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	item := manager.entries["ec20"]
	item.desiredEnabled = true
	item.reconnectPending = true
	item.autoRetryPending = true
	item.retryFailures = 4
	manager.mu.Unlock()

	release, err := manager.BeginSubscriberChange(context.Background(), "ec20")
	if err != nil {
		t.Fatalf("BeginSubscriberChange on idle runtime with stale policy: %v", err)
	}
	if item.desiredEnabled || item.reconnectPending || item.autoRetryPending || item.retryFailures != 0 {
		t.Fatalf("stale runtime policy was not cleared: %+v", item)
	}
	release()
}

func TestManagerSubscriberChangeRejectsActiveRuntimeWithoutLeavingBarrier(t *testing.T) {
	manager := New(Options{OperationTimeout: time.Second})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Register(testOrchestrator(t, "ec20")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestEnabled("ec20", true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginSubscriberChange(context.Background(), "ec20"); !errors.Is(err, ErrNotQuiesced) {
		t.Fatalf("BeginSubscriberChange error = %v, want ErrNotQuiesced", err)
	}
	manager.mu.Lock()
	blocked := manager.subscriberChangeLocked("ec20")
	manager.mu.Unlock()
	if blocked {
		t.Fatal("failed subscriber change left the runtime blocked")
	}
	if _, err := manager.State("ec20"); err != nil {
		t.Fatalf("State after rejected subscriber change: %v", err)
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
	if _, err := manager.RequestEnabled("ec20", true); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("non-reconnect operation error = %v, want ErrOperationInProgress", err)
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
