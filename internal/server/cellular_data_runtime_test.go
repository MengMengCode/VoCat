package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
)

type runtimeNetworkSetter struct {
	mu      sync.Mutex
	calls   []bool
	started chan bool
	apply   func(context.Context, device.NetworkRequest) (device.NetworkResult, error)
}

type monitorDeviceController struct {
	fakeDeviceController
	mu          sync.Mutex
	status      device.NetworkStatus
	statusErr   error
	statusCalls int
	setCalls    []device.NetworkRequest
	events      <-chan string
	lifecycle   <-chan device.DeviceLifecycleEvent
}

func (controller *monitorDeviceController) NetworkStatus(context.Context, string) (device.NetworkStatus, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.statusCalls++
	return controller.status, controller.statusErr
}

func (controller *monitorDeviceController) SubscribeNetworkStatusEvents(context.Context) (<-chan string, error) {
	return controller.events, nil
}

func (controller *monitorDeviceController) SubscribeDeviceLifecycleEvents(context.Context) (<-chan device.DeviceLifecycleEvent, error) {
	return controller.lifecycle, nil
}

func (controller *monitorDeviceController) SetNetwork(_ context.Context, _ string, request device.NetworkRequest) (device.NetworkResult, error) {
	controller.mu.Lock()
	controller.setCalls = append(controller.setCalls, request)
	controller.mu.Unlock()
	return device.NetworkResult{Enabled: request.Enabled, Backend: "qmi", Interface: "wwan0"}, nil
}

func (controller *monitorDeviceController) recordedSetCalls() []device.NetworkRequest {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return append([]device.NetworkRequest(nil), controller.setCalls...)
}

func (setter *runtimeNetworkSetter) SetNetwork(
	ctx context.Context,
	_ string,
	request device.NetworkRequest,
) (device.NetworkResult, error) {
	setter.mu.Lock()
	setter.calls = append(setter.calls, request.Enabled)
	setter.mu.Unlock()
	if setter.started != nil {
		setter.started <- request.Enabled
	}
	if setter.apply != nil {
		return setter.apply(ctx, request)
	}
	return device.NetworkResult{Enabled: request.Enabled}, nil
}

func (setter *runtimeNetworkSetter) recordedCalls() []bool {
	setter.mu.Lock()
	defer setter.mu.Unlock()
	return append([]bool(nil), setter.calls...)
}

func TestCellularDataRuntimeCoalescesSupersededDesiredState(t *testing.T) {
	setter := &runtimeNetworkSetter{started: make(chan bool, 4)}
	setter.apply = func(ctx context.Context, request device.NetworkRequest) (device.NetworkResult, error) {
		if request.Enabled {
			<-ctx.Done()
			return device.NetworkResult{}, ctx.Err()
		}
		return device.NetworkResult{Enabled: false}, nil
	}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: true})
	select {
	case enabled := <-setter.started:
		if !enabled {
			t.Fatal("first operation was not enable")
		}
	case <-time.After(time.Second):
		t.Fatal("enable operation did not start")
	}

	disable := runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: false})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := runtime.wait(ctx, "dev1", disable.Revision)
	if err != nil {
		t.Fatalf("wait disable: %v", err)
	}
	if status.Phase != "disabled" || status.Connected || status.DesiredEnabled {
		t.Fatalf("status = %+v", status)
	}
	if calls := setter.recordedCalls(); len(calls) != 2 || !calls[0] || calls[1] {
		t.Fatalf("calls = %v, want [true false]", calls)
	}
}

func TestCellularDataRuntimeRetriesAndReportsFailure(t *testing.T) {
	setter := &runtimeNetworkSetter{}
	setter.apply = func(context.Context, device.NetworkRequest) (device.NetworkResult, error) {
		return device.NetworkResult{}, errors.New("temporary QMI failure")
	}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	requested := runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: true})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := runtime.wait(ctx, "dev1", requested.Revision)
	if err == nil || status.Phase != "failed" || status.LastError == "" {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	if calls := setter.recordedCalls(); len(calls) != 3 {
		t.Fatalf("calls = %v, want three attempts", calls)
	}
}

func TestCellularDataRuntimeCallsConnectedCallback(t *testing.T) {
	setter := &runtimeNetworkSetter{}
	connected := make(chan struct {
		configID   string
		physicalID string
		revision   uint64
	}, 1)
	runtime := newCellularDataRuntime(setter, nil, func(configID, physicalID string, revision uint64) {
		connected <- struct {
			configID   string
			physicalID string
			revision   uint64
		}{configID: configID, physicalID: physicalID, revision: revision}
	})
	runtime.start(context.Background())
	requested := runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: true})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := runtime.wait(ctx, "dev1", requested.Revision)
	if err != nil || !status.Connected {
		t.Fatalf("connected status = %+v, err = %v", status, err)
	}
	select {
	case event := <-connected:
		if event.configID != "dev1" || event.physicalID != "physical1" || event.revision != requested.Revision {
			t.Fatalf("callback event = %+v, want dev1/physical1/revision %d", event, requested.Revision)
		}
	case <-time.After(time.Second):
		t.Fatal("connected callback was not invoked")
	}
}

func TestCellularDataRuntimeInvalidateDropsObservedConnection(t *testing.T) {
	setter := &runtimeNetworkSetter{}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	requested := runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: true})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connected, err := runtime.wait(ctx, "dev1", requested.Revision)
	if err != nil || !connected.Connected {
		t.Fatalf("initial status = %+v, err = %v", connected, err)
	}

	invalidated := runtime.invalidate("dev1", true, "", "")
	if invalidated.Connected || !invalidated.DesiredEnabled || invalidated.Phase != "recovering" {
		t.Fatalf("invalidated status = %+v", invalidated)
	}
	if invalidated.Revision <= connected.Revision || !runtime.isCurrent("dev1", invalidated.Revision, "recovering") {
		t.Fatalf("invalidated generation was not current: before=%+v after=%+v", connected, invalidated)
	}
}

func TestCellularDataRuntimeTracksModemRebootSeparately(t *testing.T) {
	setter := &runtimeNetworkSetter{}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())

	rebooting := runtime.invalidateWithModemPhase("dev1", true, "recovering", "", "rebooting")
	if rebooting.Phase != "recovering" || rebooting.ModemPhase != "rebooting" || rebooting.Connected {
		t.Fatalf("rebooting status = %+v, want recovering/rebooting and disconnected", rebooting)
	}

	requested := runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: true})
	status, err := runtime.wait(context.Background(), "dev1", requested.Revision)
	if err != nil || status.Phase != "connected" || status.ModemPhase != "" {
		t.Fatalf("post-reboot status = %+v, err = %v, want connected without modem phase", status, err)
	}
}

func TestCellularDataMonitorReconnectsAfterTwoControlPlaneFailures(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "physical-1", Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		Interface: "wwan0", DeviceBackend: "qmi", NetworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	controller := &monitorDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "physical-1", Discovered: true,
			Snapshot: &device.Snapshot{ICCID: "89441000000000000001", SIMReady: true, PSAttached: true},
		}},
		status: device.NetworkStatus{Backend: "qmi", Interface: "wwan0", Detail: "QMI packet data session is disconnected"},
	}
	server := &Server{store: database, devices: controller, logger: regionTestLogger()}
	runtime := server.cellularDataRuntime()
	runtime.start(ctx)
	initial := runtime.request("physical-1", "physical-1", device.NetworkRequest{Enabled: true, Backend: "qmi", APN: "internet", IPVersion: "IPV4V6"})
	waitContext, cancel := context.WithTimeout(ctx, time.Second)
	if status, waitErr := runtime.wait(waitContext, "physical-1", initial.Revision); waitErr != nil || !status.Connected {
		cancel()
		t.Fatalf("initial session = %+v, err = %v", status, waitErr)
	}
	cancel()

	server.monitorCellularDataOnce(ctx)
	if calls := controller.recordedSetCalls(); len(calls) != 1 {
		t.Fatalf("reconnected after first failed probe: calls = %d, want 1", len(calls))
	}
	server.monitorCellularDataOnce(ctx)
	waitContext, cancel = context.WithTimeout(ctx, time.Second)
	status, waitErr := runtime.wait(waitContext, "physical-1", initial.Revision+1)
	cancel()
	if waitErr != nil || !status.Connected || status.Phase != "connected" {
		t.Fatalf("reconnected session = %+v, err = %v", status, waitErr)
	}
	if calls := controller.recordedSetCalls(); len(calls) != 2 || !calls[1].Enabled {
		t.Fatalf("set network calls = %+v, want initial enable plus one reconnect", calls)
	}
}

func TestCellularDataMonitorDoesNotReclaimATBackend(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "physical-1", Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		Interface: "wwan0", DeviceBackend: "at", NetworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	controller := &monitorDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "physical-1", Discovered: true,
			Snapshot: &device.Snapshot{ICCID: "89441000000000000001", SIMReady: true, PSAttached: true},
		}},
		status: device.NetworkStatus{Backend: "qmi", Interface: "wwan0"},
	}
	server := &Server{store: database, devices: controller, logger: regionTestLogger()}
	runtime := server.cellularDataRuntime()
	runtime.start(ctx)
	initial := runtime.request("physical-1", "physical-1", device.NetworkRequest{Enabled: true, Backend: "at"})
	waitContext, cancel := context.WithTimeout(ctx, time.Second)
	if _, waitErr := runtime.wait(waitContext, "physical-1", initial.Revision); waitErr != nil {
		cancel()
		t.Fatal(waitErr)
	}
	cancel()

	server.monitorCellularDataOnce(ctx)
	if calls := controller.recordedSetCalls(); len(calls) != 1 {
		t.Fatalf("AT backend was reclaimed by QMI monitor: calls = %d, want 1", len(calls))
	}
}

func TestCellularDataEventMonitorReconnectsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "physical-1", Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		Interface: "wwan0", DeviceBackend: "qmi", NetworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	qmiEvents := make(chan string, 1)
	controller := &monitorDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "physical-1", Discovered: true,
			Snapshot: &device.Snapshot{ICCID: "89441000000000000001", SIMReady: true, PSAttached: true},
		}},
		status: device.NetworkStatus{Backend: "qmi", Interface: "wwan0", Detail: "QMI packet data session is disconnected"},
		events: qmiEvents,
	}
	server := &Server{store: database, devices: controller, logger: regionTestLogger()}
	runtime := server.cellularDataRuntime()
	runtime.start(ctx)
	initial := runtime.request("physical-1", "physical-1", device.NetworkRequest{Enabled: true, Backend: "qmi", APN: "internet", IPVersion: "IPV4V6"})
	waitContext, waitCancel := context.WithTimeout(ctx, time.Second)
	if status, waitErr := runtime.wait(waitContext, "physical-1", initial.Revision); waitErr != nil || !status.Connected {
		waitCancel()
		t.Fatalf("initial session = %+v, err = %v", status, waitErr)
	}
	waitCancel()

	go server.runCellularDataEventMonitor(ctx)
	qmiEvents <- "physical-1"
	waitContext, waitCancel = context.WithTimeout(ctx, time.Second)
	status, waitErr := runtime.wait(waitContext, "physical-1", initial.Revision+1)
	waitCancel()
	if waitErr != nil || !status.Connected || status.Phase != "connected" {
		t.Fatalf("event-triggered recovery = %+v, err = %v", status, waitErr)
	}
	if calls := controller.recordedSetCalls(); len(calls) != 2 || !calls[1].Enabled {
		t.Fatalf("set network calls = %+v, want initial enable plus immediate reconnect", calls)
	}
}

func TestCellularDataLifecycleMonitorClosesRemovedPhysicalSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "physical-1", Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		Interface: "wwan0", DeviceBackend: "qmi", NetworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	lifecycle := make(chan device.DeviceLifecycleEvent, 1)
	controller := &monitorDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "physical-1", Discovered: true,
			Snapshot: &device.Snapshot{ICCID: "89441000000000000001", SIMReady: true, PSAttached: true},
		}},
		lifecycle: lifecycle,
	}
	server := &Server{store: database, devices: controller, logger: regionTestLogger()}
	runtime := server.cellularDataRuntime()
	runtime.start(ctx)
	initial := runtime.requestWithIdentity("physical-1", "physical-1", device.NetworkRequest{Enabled: true, Backend: "qmi"}, "89441000000000000001")
	waitContext, waitCancel := context.WithTimeout(ctx, time.Second)
	if status, waitErr := runtime.wait(waitContext, "physical-1", initial.Revision); waitErr != nil || !status.Connected {
		waitCancel()
		t.Fatalf("initial session = %+v, err = %v", status, waitErr)
	}
	waitCancel()
	go server.runCellularDataLifecycleMonitor(ctx)
	lifecycle <- device.DeviceLifecycleEvent{ID: "physical-1", Present: false}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := runtime.status("physical-1", true)
		if status.MaintenancePhase == "device_disconnected" && status.Phase == "failed" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("removed physical did not close session: %+v", runtime.status("physical-1", true))
}

func TestCellularDataRuntimeInvalidateStopsSupersededWorkerWithoutReplay(t *testing.T) {
	setter := &runtimeNetworkSetter{started: make(chan bool, 4)}
	setter.apply = func(ctx context.Context, request device.NetworkRequest) (device.NetworkResult, error) {
		<-ctx.Done()
		return device.NetworkResult{}, ctx.Err()
	}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: true})
	select {
	case <-setter.started:
	case <-time.After(time.Second):
		t.Fatal("enable operation did not start")
	}
	invalidated := runtime.invalidate("dev1", true, "recovering", "")
	time.Sleep(100 * time.Millisecond)
	if calls := setter.recordedCalls(); len(calls) != 1 {
		t.Fatalf("superseded operation replayed during reboot: calls=%v", calls)
	}
	status := runtime.status("dev1", true)
	if status.Revision != invalidated.Revision || status.Phase != "recovering" || status.Connected {
		t.Fatalf("status = %+v", status)
	}
}

func TestCellularDataRuntimeInvalidateDuringRetryBackoffDoesNotReplay(t *testing.T) {
	setter := &runtimeNetworkSetter{started: make(chan bool, 4)}
	returned := make(chan struct{}, 1)
	setter.apply = func(context.Context, device.NetworkRequest) (device.NetworkResult, error) {
		returned <- struct{}{}
		return device.NetworkResult{}, errors.New("temporary QMI failure")
	}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: true})
	select {
	case <-setter.started:
	case <-time.After(time.Second):
		t.Fatal("enable operation did not start")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("first attempt did not return")
	}
	// The worker is now in its first retry backoff. Invalidate the generation
	// while it is waiting and ensure it cannot replay the superseded request.
	time.Sleep(50 * time.Millisecond)
	invalidated := runtime.invalidate("dev1", true, "recovering", "")
	time.Sleep(100 * time.Millisecond)
	if calls := setter.recordedCalls(); len(calls) != 1 {
		t.Fatalf("superseded retry replayed during backoff: calls=%v", calls)
	}
	status := runtime.status("dev1", true)
	if status.Revision != invalidated.Revision || status.Phase != "recovering" || status.Connected {
		t.Fatalf("status = %+v", status)
	}
}

func TestCellularDataRuntimeLiveObservationInvalidatesStaleConnectedState(t *testing.T) {
	runtime := newCellularDataRuntime(&runtimeNetworkSetter{}, nil)
	runtime.mu.Lock()
	runtime.entries["dev1"] = &cellularDataRuntimeEntry{
		status:  cellularDataRuntimeStatus{DesiredEnabled: true, Connected: true, Phase: "connected", Revision: 4},
		changed: make(chan struct{}),
	}
	runtime.mu.Unlock()

	status := runtime.observe("dev1", true, false, nil)
	if status.Connected || status.Phase != "failed" {
		t.Fatalf("live disconnected observation = %+v, want failed and disconnected", status)
	}
}

func TestCellularDataRuntimeLiveObservationDoesNotOverwriteRecovery(t *testing.T) {
	runtime := newCellularDataRuntime(&runtimeNetworkSetter{}, nil)
	runtime.mu.Lock()
	runtime.entries["dev1"] = &cellularDataRuntimeEntry{
		status:  cellularDataRuntimeStatus{DesiredEnabled: true, Connected: false, Phase: "recovering", Revision: 5},
		running: true,
		changed: make(chan struct{}),
	}
	runtime.mu.Unlock()

	status := runtime.observe("dev1", true, true, nil)
	if status.Connected || status.Phase != "recovering" {
		t.Fatalf("live observation during recovery = %+v, want unchanged recovery", status)
	}
}

func TestCellularDataRuntimeLiveProbeErrorInvalidatesConnectedState(t *testing.T) {
	runtime := newCellularDataRuntime(&runtimeNetworkSetter{}, nil)
	runtime.mu.Lock()
	runtime.entries["dev1"] = &cellularDataRuntimeEntry{
		status:  cellularDataRuntimeStatus{DesiredEnabled: true, Connected: true, Phase: "connected", Revision: 6},
		changed: make(chan struct{}),
	}
	runtime.mu.Unlock()

	status := runtime.observe("dev1", true, false, errors.New("stale WDS client"))
	if status.Connected || status.Phase != "failed" || status.LastError != "stale WDS client" {
		t.Fatalf("probe error observation = %+v, want failed and disconnected", status)
	}
}

func TestCellularDataRuntimeBusyProbeRetainsTransitionState(t *testing.T) {
	runtime := newCellularDataRuntime(&runtimeNetworkSetter{}, nil)
	runtime.mu.Lock()
	runtime.entries["dev1"] = &cellularDataRuntimeEntry{
		status:  cellularDataRuntimeStatus{DesiredEnabled: true, Connected: true, Phase: "connected", Revision: 7},
		changed: make(chan struct{}),
	}
	runtime.mu.Unlock()

	status := runtime.observe("dev1", true, false, device.ErrDataOperationInProgress)
	if !status.Connected || status.Phase != "connected" {
		t.Fatalf("busy probe observation = %+v, want previous terminal state", status)
	}
}

func TestCellularDataRuntimeNewRequestSupersedesRecoveryGeneration(t *testing.T) {
	setter := &runtimeNetworkSetter{}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	recovering := runtime.invalidate("dev1", true, "recovering", "")
	disabling := runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: false})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := runtime.wait(ctx, "dev1", disabling.Revision)
	if err != nil || status.Phase != "disabled" {
		t.Fatalf("disable status = %+v, err = %v", status, err)
	}
	if runtime.isCurrent("dev1", recovering.Revision, "recovering") {
		t.Fatal("old recovery generation remained current")
	}
	if _, accepted := runtime.requestIfCurrent(
		"dev1", recovering.Revision, "recovering", "physical1", device.NetworkRequest{Enabled: true},
	); accepted {
		t.Fatal("stale recovery request was accepted after the user disabled data")
	}
}

func TestCellularDataWatchdogStopDisarmsImmediatelyAndDrainsProbe(t *testing.T) {
	watchdog := newCellularDataWatchdog()
	watchdog.start(7)
	release, ok := watchdog.acquire(7)
	if !ok {
		t.Fatal("active watchdog did not admit probe")
	}
	watchdog.stop()
	if _, ok := watchdog.acquire(7); ok {
		t.Fatal("stopped watchdog admitted a new probe")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- watchdog.wait(context.Background()) }()
	select {
	case err := <-waitDone:
		t.Fatalf("watchdog drained before in-flight probe released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("wait for watchdog drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not drain in-flight probe")
	}
}

func TestCellularDataRuntimeDisableStopsSessionWatchdogBeforeQMIStop(t *testing.T) {
	setter := &runtimeNetworkSetter{}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	enabled := runtime.requestWithIdentity("dev1", "physical1", device.NetworkRequest{Enabled: true}, "iccid-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if status, err := runtime.wait(ctx, "dev1", enabled.Revision); err != nil || !status.Connected {
		t.Fatalf("enable status = %+v, err = %v", status, err)
	}

	runtime.mu.Lock()
	activeSession := runtime.entries["dev1"].session
	runtime.mu.Unlock()
	if activeSession == nil || activeSession.phase != cellularDataSessionActive {
		t.Fatalf("session = %+v, want active", activeSession)
	}
	disabled := runtime.request("dev1", "physical1", device.NetworkRequest{Enabled: false})
	if _, ok := activeSession.watchdog.acquire(activeSession.generation); ok {
		t.Fatal("watchdog remained armed after entering closing")
	}
	if status, err := runtime.wait(ctx, "dev1", disabled.Revision); err != nil || status.Phase != "disabled" {
		t.Fatalf("disable status = %+v, err = %v", status, err)
	}
	runtime.mu.Lock()
	finalSession := runtime.entries["dev1"].session
	runtime.mu.Unlock()
	if finalSession == nil || finalSession.phase != cellularDataSessionClosed {
		t.Fatalf("final session = %+v, want closed", finalSession)
	}
}

func TestCellularDataRuntimePhysicalRemovalEndsWatchdogAndReconnectCreatesSession(t *testing.T) {
	setter := &runtimeNetworkSetter{}
	runtime := newCellularDataRuntime(setter, nil)
	runtime.start(context.Background())
	initial := runtime.requestWithIdentity("dev1", "physical1", device.NetworkRequest{Enabled: true}, "iccid-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if status, err := runtime.wait(ctx, "dev1", initial.Revision); err != nil || !status.Connected {
		t.Fatalf("initial status = %+v, err = %v", status, err)
	}
	runtime.closeForPhysicalID("physical1")
	removed := runtime.status("dev1", true)
	if removed.MaintenancePhase != "device_disconnected" || removed.Phase != "failed" || removed.Connected {
		t.Fatalf("removed status = %+v", removed)
	}
	runtime.mu.Lock()
	removedSession := runtime.entries["dev1"].session
	runtime.mu.Unlock()
	if removedSession == nil || removedSession.phase != cellularDataSessionClosed {
		t.Fatalf("removed session = %+v, want closed", removedSession)
	}

	reconnected := runtime.requestWithIdentity("dev1", "physical2", device.NetworkRequest{Enabled: true}, "iccid-2")
	if status, err := runtime.wait(ctx, "dev1", reconnected.Revision); err != nil || !status.Connected {
		t.Fatalf("reconnected status = %+v, err = %v", status, err)
	}
	runtime.mu.Lock()
	newSession := runtime.entries["dev1"].session
	runtime.mu.Unlock()
	if newSession == nil || newSession.generation == removedSession.generation || newSession.identity != "iccid-2" {
		t.Fatalf("new session = %+v, want a new ICCID-bound session", newSession)
	}
}

func TestCellularDataRuntimeDetectsProfileIdentityChange(t *testing.T) {
	runtime := newCellularDataRuntime(&runtimeNetworkSetter{}, nil)
	runtime.start(context.Background())
	requested := runtime.requestWithIdentity("dev1", "physical1", device.NetworkRequest{Enabled: true}, "iccid-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if status, err := runtime.wait(ctx, "dev1", requested.Revision); err != nil || !status.Connected {
		t.Fatalf("initial status = %+v, err = %v", status, err)
	}
	if !runtime.sessionIdentityChanged("dev1", requested.Revision, "iccid-2") {
		t.Fatal("profile identity change was not detected")
	}
	runtime.invalidateWithMaintenancePhase("dev1", true, "recovering", "active ICCID/Profile changed", "profile_changed")
	status := runtime.status("dev1", true)
	if status.MaintenancePhase != "profile_changed" || status.Phase != "recovering" || status.Connected {
		t.Fatalf("identity-change status = %+v", status)
	}
}
