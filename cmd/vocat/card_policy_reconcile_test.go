package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type policyReconcileDevices struct {
	gate       sync.RWMutex
	mu         sync.Mutex
	entry      device.Device
	flights    []bool
	acquireErr error
	onAcquire  func()
	readers    atomic.Int32
	unsafeRF   atomic.Bool
}

func (d *policyReconcileDevices) Get(string) (device.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry := d.entry
	if entry.Snapshot != nil {
		snapshot := *entry.Snapshot
		entry.Snapshot = &snapshot
	}
	return entry, nil
}

func (d *policyReconcileDevices) List() []device.Device {
	entry, _ := d.Get("")
	return []device.Device{entry}
}

func (d *policyReconcileDevices) AcquireSIMRead(ctx context.Context, _ string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.acquireErr != nil {
		return nil, d.acquireErr
	}
	if !d.gate.TryRLock() {
		return nil, device.ErrESIMRecovering
	}
	d.readers.Add(1)
	if d.onAcquire != nil {
		d.onAcquire()
	}
	return func() { d.readers.Add(-1); d.gate.RUnlock() }, nil
}

func (d *policyReconcileDevices) SetFlight(_ context.Context, _ string, enabled bool) (device.FlightResult, error) {
	if d.readers.Load() == 0 {
		d.unsafeRF.Store(true)
	}
	d.mu.Lock()
	d.flights = append(d.flights, enabled)
	d.mu.Unlock()
	return device.FlightResult{}, nil
}

type policyReconcileWiFi struct {
	mu          sync.Mutex
	maintenance bool
	state       vowifi.State
	enables     []bool
	reconnects  int
	onState     func()
	onRequest   func()
}

func (w *policyReconcileWiFi) InMaintenance(string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maintenance
}

func (w *policyReconcileWiFi) State(string) (vowifi.State, error) {
	if w.onState != nil {
		w.onState()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state, nil
}

func (w *policyReconcileWiFi) RequestEnabled(_ string, enabled bool) (vowifi.State, error) {
	if w.onRequest != nil {
		w.onRequest()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enables = append(w.enables, enabled)
	return w.state, nil
}

func (w *policyReconcileWiFi) RequestReconnect(string) (vowifi.State, error) {
	if w.onRequest != nil {
		w.onRequest()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reconnects++
	return w.state, nil
}

func newPolicyReconcileFixture(t *testing.T) (*cardPolicyReconciler, *policyReconcileDevices, *policyReconcileWiFi) {
	t.Helper()
	db := newRegionTestStore(t)
	if err := db.UpsertDevice(context.Background(), store.Device{
		ID: "dev1", Name: "dev1", DeviceType: store.DeviceTypeWiFi410,
		ATPort: "/dev/policy-test", APN: "current.apn",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCardPolicy(context.Background(), store.CardPolicy{
		ICCID: "old-card", VoWiFiEnabled: true, AirplaneEnabled: true, APN: "old.apn",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCardPolicy(context.Background(), store.CardPolicy{
		ICCID: "new-card", AirplaneEnabled: true, APN: "new.apn",
	}); err != nil {
		t.Fatal(err)
	}
	d := &policyReconcileDevices{entry: device.Device{
		ID: "physical1", Discovered: true,
		Candidate: modem.Candidate{ATPort: modem.Port{Path: "/dev/policy-test"}},
		Snapshot:  &device.Snapshot{Responsive: true, SIMReady: true, ICCID: "old-card", FlightMode: true},
	}}
	w := &policyReconcileWiFi{}
	r := &cardPolicyReconciler{
		logger: regionTestLogger(), database: db, devices: d, wifi: w,
		observedCards: make(map[string]string),
	}
	return r, d, w
}

func TestCardPolicyReconcileSkipsMaintenanceAndBlockedSIM(t *testing.T) {
	for _, blockedBy := range []string{"runtime maintenance", "SIM writer"} {
		t.Run(blockedBy, func(t *testing.T) {
			r, d, w := newPolicyReconcileFixture(t)
			if blockedBy == "runtime maintenance" {
				w.maintenance = true
			} else {
				d.acquireErr = device.ErrESIMRecovering
			}
			r.reconcile(context.Background())
			config, err := r.database.Device(context.Background(), "dev1")
			if err != nil {
				t.Fatal(err)
			}
			if config.VoWiFiEnabled || config.APN != "current.apn" || len(w.enables) != 0 || w.reconnects != 0 || len(d.flights) != 0 {
				t.Fatalf("old card policy applied during %s: config=%+v wifi=%+v", blockedBy, config, w)
			}
			if d.readers.Load() != 0 {
				t.Fatal("SIM read lease leaked")
			}
		})
	}
}

func TestCardPolicyReconcileReadsIdentityAndConfigAfterSIMLease(t *testing.T) {
	r, d, w := newPolicyReconcileFixture(t)
	d.onAcquire = func() {
		// The mapping pass saw old-card. A completed switch before read-lease
		// acquisition must replace both that snapshot and its saved intent.
		d.mu.Lock()
		d.entry.Snapshot = &device.Snapshot{Responsive: true, SIMReady: true, ICCID: "new-card", FlightMode: true}
		d.mu.Unlock()
		config, err := r.database.Device(context.Background(), "dev1")
		if err != nil {
			t.Fatal(err)
		}
		config.VoWiFiEnabled = false
		config.APN = "new.apn"
		config.Name = "after switch"
		if err := r.database.UpsertDevice(context.Background(), config); err != nil {
			t.Fatal(err)
		}
	}
	r.reconcile(context.Background())
	config, err := r.database.Device(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if config.VoWiFiEnabled || config.APN != "new.apn" || config.Name != "after switch" || len(w.enables) != 0 || w.reconnects != 0 {
		t.Fatalf("reused pre-lease card/config: config=%+v wifi=%+v", config, w)
	}
}

func TestCardPolicyReconcileDrainsReaderBeforeProfileWriter(t *testing.T) {
	r, d, w := newPolicyReconcileFixture(t)
	d.entry.Snapshot.FlightMode = false
	stateRead := make(chan struct{})
	allowState := make(chan struct{})
	var releaseState sync.Once
	t.Cleanup(func() { releaseState.Do(func() { close(allowState) }) })
	w.onState = func() { close(stateRead); <-allowState }
	var unsafeRequest atomic.Bool
	w.onRequest = func() {
		if d.readers.Load() == 0 {
			unsafeRequest.Store(true)
		}
	}
	done := make(chan struct{})
	go func() { r.reconcile(context.Background()); close(done) }()
	select {
	case <-stateRead:
	case <-time.After(time.Second):
		t.Fatal("reconciler did not reach its lifecycle decision")
	}
	writerStarted := make(chan struct{})
	writerEntered := make(chan struct{})
	go func() {
		close(writerStarted)
		d.gate.Lock()
		close(writerEntered)
		d.gate.Unlock()
	}()
	<-writerStarted
	select {
	case <-writerEntered:
		t.Fatal("profile writer entered before the reader's RF/lifecycle actions")
	case <-time.After(20 * time.Millisecond):
	}
	releaseState.Do(func() { close(allowState) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader did not complete")
	}
	select {
	case <-writerEntered:
	case <-time.After(time.Second):
		t.Fatal("reader did not release the profile gate")
	}
	if unsafeRequest.Load() || d.unsafeRF.Load() || len(w.enables) != 1 || !w.enables[0] || len(d.flights) != 1 || !d.flights[0] {
		t.Fatal("policy lifecycle/RF decision escaped its SIM read lease")
	}
}

func TestCardPolicyReconcileMissingIdentityCannotEnableIdleRuntime(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured bool
		state      vowifi.State
		snapshot   *device.Snapshot
		wantRetry  int
	}{
		{name: "disabled intent with old active runtime", state: vowifi.State{Enabled: true, Active: true, ICCID: "old-card"}},
		{name: "idle retains old ICCID", configured: true, state: vowifi.State{ICCID: "old-card", Phase: vowifi.PhaseIdle}},
		{name: "failed setup keeps its existing retry", configured: true, state: vowifi.State{Enabled: true, ICCID: "old-card", Phase: vowifi.PhaseFailed}},
		{name: "unresponsive old snapshot", snapshot: &device.Snapshot{ICCID: "old-card", SIMReady: true}},
		{name: "not ready old snapshot", snapshot: &device.Snapshot{ICCID: "old-card", Responsive: true}},
		{name: "active permitted runtime sees removal", configured: true, state: vowifi.State{Enabled: true, Active: true, ICCID: "old-card", Phase: vowifi.PhaseSMSReady}, wantRetry: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d, w := newPolicyReconcileFixture(t)
			d.entry.Snapshot = tc.snapshot
			w.state = tc.state
			config, err := r.database.Device(context.Background(), "dev1")
			if err != nil {
				t.Fatal(err)
			}
			config.VoWiFiEnabled = tc.configured
			if err := r.database.UpsertDevice(context.Background(), config); err != nil {
				t.Fatal(err)
			}
			r.reconcile(context.Background())
			stored, err := r.database.Device(context.Background(), "dev1")
			if err != nil {
				t.Fatal(err)
			}
			if stored.VoWiFiEnabled != tc.configured || stored.APN != "current.apn" || len(w.enables) != 0 || w.reconnects != tc.wantRetry || len(d.flights) != 0 {
				t.Fatalf("missing/stale identity changed intent: config=%+v reconnects=%d", stored, w.reconnects)
			}
		})
	}
}

func TestCardPolicyReconcilePhysicalRemovalStopsOldSessionAndReplugResumes(t *testing.T) {
	r, d, w := newPolicyReconcileFixture(t)
	config, err := r.database.Device(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	config.VoWiFiEnabled = true
	if err := r.database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	w.state = vowifi.State{Enabled: true, Active: true, ICCID: "old-card", Phase: vowifi.PhaseSMSReady}
	r.observedCards["dev1"] = "old-card"
	d.entry.Discovered = false
	r.reconcile(context.Background())
	stored, err := r.database.Device(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.VoWiFiEnabled || len(w.enables) != 1 || w.enables[0] || w.reconnects != 0 || r.observedCards["dev1"] != "missing" {
		t.Fatalf("physical removal did not retire only the old session: config=%+v calls=%v", stored, w.enables)
	}
	// Disable finishes asynchronously, preserving the ICCID for diagnostics.
	// The same card reappears ready and must use its saved policy again.
	w.state = vowifi.State{ICCID: "old-card", Phase: vowifi.PhaseIdle}
	d.entry.Discovered = true
	r.reconcile(context.Background())
	if len(w.enables) != 2 || !w.enables[1] || w.reconnects != 0 || r.observedCards["dev1"] != "old-card" {
		t.Fatalf("same-card replug did not resume its saved policy: calls=%v reconnects=%d", w.enables, w.reconnects)
	}
}

func TestCardPolicyReconcilePhysicalRemovalDoesNotInterruptMaintenance(t *testing.T) {
	r, d, w := newPolicyReconcileFixture(t)
	d.entry.Discovered = false
	w.maintenance = true
	w.state = vowifi.State{Enabled: true, Active: true, ICCID: "old-card"}
	r.observedCards["dev1"] = "old-card"
	r.reconcile(context.Background())
	if len(w.enables) != 0 || w.reconnects != 0 || r.observedCards["dev1"] != "old-card" {
		t.Fatal("temporary disappearance interrupted profile maintenance")
	}
}
