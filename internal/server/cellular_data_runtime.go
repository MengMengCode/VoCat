package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"vocat/internal/device"
)

type cellularNetworkSetter interface {
	SetNetwork(context.Context, string, device.NetworkRequest) (device.NetworkResult, error)
}

type cellularDataConnectedCallback func(configID, physicalID string, revision uint64)

type cellularDataRuntimeStatus struct {
	DesiredEnabled   bool      `json:"desired_enabled"`
	Connected        bool      `json:"connected"`
	Phase            string    `json:"phase"`
	ModemPhase       string    `json:"modem_phase,omitempty"`
	MaintenancePhase string    `json:"maintenance_phase,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	Revision         uint64    `json:"revision"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type cellularDataSessionPhase string

const (
	cellularDataSessionStarting cellularDataSessionPhase = "starting"
	cellularDataSessionActive   cellularDataSessionPhase = "active"
	cellularDataSessionClosing  cellularDataSessionPhase = "closing"
	cellularDataSessionClosed   cellularDataSessionPhase = "closed"
)

// cellularDataWatchdog is the per-session admission gate for control-plane
// probes. The actual QMI event subscription and fallback ticker are shared by
// the server, but a session owns the right to be probed. Stop deactivates the
// gate immediately; wait drains probes that were already admitted before the
// session entered closing.
type cellularDataWatchdog struct {
	mu         sync.Mutex
	active     bool
	generation uint64
	inFlight   int
	idle       chan struct{}
}

func newCellularDataWatchdog() *cellularDataWatchdog {
	idle := make(chan struct{})
	close(idle)
	return &cellularDataWatchdog{idle: idle}
}

func (watchdog *cellularDataWatchdog) start(generation uint64) {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	watchdog.active = true
	watchdog.generation = generation
	watchdog.mu.Unlock()
}

// stop is intentionally non-blocking. It removes the session from the
// recovery path immediately while the QMI close operation is still allowed to
// complete in the runtime worker.
func (watchdog *cellularDataWatchdog) stop() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	watchdog.active = false
	watchdog.mu.Unlock()
}

// acquire admits one monitor callback. The generation check prevents an old
// event from borrowing a newly created session's watchdog.
func (watchdog *cellularDataWatchdog) acquire(generation uint64) (func(), bool) {
	if watchdog == nil {
		return nil, false
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if !watchdog.active || watchdog.generation != generation {
		return nil, false
	}
	if watchdog.inFlight == 0 {
		watchdog.idle = make(chan struct{})
	}
	watchdog.inFlight++
	return func() { watchdog.release() }, true
}

func (watchdog *cellularDataWatchdog) release() {
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.inFlight <= 0 {
		return
	}
	watchdog.inFlight--
	if watchdog.inFlight == 0 {
		close(watchdog.idle)
	}
}

func (watchdog *cellularDataWatchdog) wait(ctx context.Context) error {
	if watchdog == nil {
		return nil
	}
	watchdog.mu.Lock()
	idle := watchdog.idle
	watchdog.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type cellularDataSession struct {
	generation uint64
	physicalID string
	identity   string // current ICCID/Profile identity when known
	phase      cellularDataSessionPhase
	watchdog   *cellularDataWatchdog
}

type cellularDataRuntimeEntry struct {
	physicalID    string
	identity      string
	request       device.NetworkRequest
	status        cellularDataRuntimeStatus
	session       *cellularDataSession
	running       bool
	cancel        context.CancelFunc
	changed       chan struct{}
	probeFailures int
}

// The monitor is deliberately a control-plane/host-plane check. It never
// sends a user-plane request, so a healthy session does not consume carrier
// data merely because VoCat is keeping it alive.
var cellularDataMonitorInterval = time.Minute

const (
	cellularDataMonitorProbeTimeout = 5 * time.Second
	cellularDataMonitorFailureLimit = 2
)

// cellularDataRuntime serializes and coalesces desired data-state changes per
// configured device. Hardware transitions use a server-owned context, so a
// browser closing or navigating away cannot abandon a half-finished QMI WDS
// transaction. A later request cancels the superseded transition and the same
// worker reconciles the newest desired state.
type cellularDataRuntime struct {
	mu                    sync.Mutex
	root                  context.Context
	setter                cellularNetworkSetter
	logger                *slog.Logger
	onConnected           cellularDataConnectedCallback
	entries               map[string]*cellularDataRuntimeEntry
	nextSessionGeneration uint64
}

func newCellularDataRuntime(setter cellularNetworkSetter, logger *slog.Logger, callbacks ...cellularDataConnectedCallback) *cellularDataRuntime {
	var onConnected cellularDataConnectedCallback
	if len(callbacks) > 0 {
		onConnected = callbacks[0]
	}
	return &cellularDataRuntime{
		root: context.Background(), setter: setter, logger: logger, onConnected: onConnected,
		entries: make(map[string]*cellularDataRuntimeEntry),
	}
}

func (s *Server) cellularDataRuntime() *cellularDataRuntime {
	s.cellularDataOnce.Do(func() {
		s.cellularData = newCellularDataRuntime(s.devices, s.logger, func(configID, physicalID string, revision uint64) {
			s.schedulePublicIPDetection(configID, physicalID, revision)
		})
	})
	return s.cellularData
}

func (runtime *cellularDataRuntime) start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.mu.Lock()
	runtime.root = ctx
	runtime.mu.Unlock()
}

func (runtime *cellularDataRuntime) rootContext() context.Context {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.root
}

// StartCellularDataReconciler binds data jobs to the server lifetime and
// reconciles every present configured device after a service restart. Enabled
// devices are restored; disabled devices actively reclaim any modem-side call
// left behind by an older process while host routing remains fail-closed.
func (s *Server) StartCellularDataReconciler(ctx context.Context) {
	if s == nil {
		return
	}
	runtime := s.cellularDataRuntime()
	runtime.start(ctx)
	s.cellularDataMonitorOnce.Do(func() {
		go s.runCellularDataMonitor(ctx)
	})
	s.cellularDataEventOnce.Do(func() {
		go s.runCellularDataEventMonitor(ctx)
	})
	s.cellularDataLifecycleOnce.Do(func() {
		go s.runCellularDataLifecycleMonitor(ctx)
	})
	go func() {
		<-ctx.Done()
		runtime.stopAllWatchdogs()
	}()
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		configs, err := s.store.ListDevices(ctx)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("could not restore cellular data desired state", "error", err)
			}
			return
		}
		for _, config := range configs {
			if config.VoWiFiEnabled {
				continue
			}
			entry, physicalID, present := s.physicalForConfig(config)
			if !present || entry.Snapshot == nil {
				continue
			}
			request := s.cellularNetworkRequest(ctx, config, entry.Snapshot)
			request.Enabled = config.NetworkEnabled
			runtime.requestWithIdentity(config.ID, physicalID, request, strings.TrimSpace(entry.Snapshot.ICCID))
		}
	}()
}

func (s *Server) runCellularDataLifecycleMonitor(ctx context.Context) {
	source, ok := s.devices.(cellularDeviceLifecycleController)
	if !ok {
		return
	}
	events, err := source.SubscribeDeviceLifecycleEvents(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("could not subscribe to device lifecycle events", "error", err)
		}
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if !event.Present {
				s.cellularDataRuntime().closeForPhysicalID(event.ID)
			} else {
				s.reconcileCellularDataForPhysical(ctx, event.ID)
			}
		}
	}
}

func (s *Server) reconcileCellularDataForPhysical(ctx context.Context, physicalID string) {
	if s == nil || s.store == nil {
		return
	}
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		return
	}
	runtime := s.cellularDataRuntime()
	for _, config := range configs {
		if !config.NetworkEnabled || config.VoWiFiEnabled {
			continue
		}
		entry, resolvedPhysicalID, present := s.physicalForConfig(config)
		if !present || resolvedPhysicalID != physicalID || entry.Snapshot == nil {
			continue
		}
		request := s.cellularNetworkRequest(ctx, config, entry.Snapshot)
		request.Enabled = true
		runtime.requestWithIdentity(config.ID, physicalID, request, strings.TrimSpace(entry.Snapshot.ICCID))
	}
}

// runCellularDataMonitor periodically reconciles desired cellular data
// sessions. NetworkStatus is intentionally used instead of HTTP/DNS probes:
// it checks the modem's WDS call and the host-side interface/route without
// generating user-plane traffic. The monitor is a fallback for lost QMI
// indications and for stale sessions that disappear silently.
func (s *Server) runCellularDataMonitor(ctx context.Context) {
	ticker := time.NewTicker(cellularDataMonitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.monitorCellularDataOnce(ctx)
		}
	}
}

// runCellularDataEventMonitor reacts to QMI WDS packet-service indications.
// The event is only a fast wake-up: the normal live status query still owns
// the decision, and the periodic monitor remains the fallback for firmware
// that does not emit usable indications.
func (s *Server) runCellularDataEventMonitor(ctx context.Context) {
	source, ok := s.devices.(cellularNetworkEventController)
	if !ok {
		return
	}
	events, err := source.SubscribeNetworkStatusEvents(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("could not subscribe to QMI packet status events", "error", err)
		}
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case physicalID, ok := <-events:
			if !ok {
				return
			}
			s.monitorCellularDataEvent(ctx, physicalID)
		}
	}
}

// monitorCellularDataOnce is split from the ticker loop so the recovery
// policy can be exercised deterministically in unit tests.
func (s *Server) monitorCellularDataOnce(ctx context.Context) {
	s.monitorCellularData(ctx, "", false)
}

func (s *Server) monitorCellularDataEvent(ctx context.Context, physicalID string) {
	s.monitorCellularData(ctx, strings.TrimSpace(physicalID), true)
}

func (s *Server) monitorCellularData(ctx context.Context, physicalID string, immediateFailure bool) {
	if s == nil || s.store == nil || s.devices == nil {
		return
	}
	observer, ok := s.devices.(cellularNetworkStatusController)
	if !ok {
		return
	}
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("could not list devices for cellular data monitor", "error", err)
		}
		return
	}
	runtime := s.cellularDataRuntime()
	for _, config := range configs {
		if ctx.Err() != nil {
			return
		}
		if !config.NetworkEnabled || config.VoWiFiEnabled {
			continue
		}
		status, _, present := runtime.monitorCandidate(config.ID)
		if !present || !status.DesiredEnabled || status.Revision == 0 || status.ModemPhase == "rebooting" || status.MaintenancePhase != "" {
			continue
		}
		if status.Phase != "connected" && status.Phase != "failed" {
			continue
		}

		entry, resolvedPhysicalID, physicalPresent := s.physicalForConfig(config)
		if !physicalPresent || entry.Snapshot == nil {
			// A USB disappearance is handled by the normal device lifecycle. Do
			// not count it as a data failure or reset a modem that is not present.
			runtime.closeIfPhysicalAbsent(config.ID)
			continue
		}
		if physicalID != "" && resolvedPhysicalID != physicalID {
			continue
		}
		request := s.cellularNetworkRequest(ctx, config, entry.Snapshot)
		request.Enabled = true
		identity := strings.TrimSpace(entry.Snapshot.ICCID)
		if runtime.sessionIdentityChanged(config.ID, status.Revision, identity) {
			runtime.invalidateWithMaintenancePhase(config.ID, true, "recovering", "active ICCID/Profile changed", "profile_changed")
			runtime.requestWithIdentity(config.ID, resolvedPhysicalID, request, identity)
			continue
		}
		backend := strings.ToLower(strings.TrimSpace(request.Backend))
		if backend != "" && backend != "qmi" {
			// NetworkStatus observes the process-owned QMI session. AT-backed
			// sessions have no equivalent live observer yet.
			continue
		}
		releaseWatchdog, admitted := runtime.acquireWatchdog(config.ID, status.Revision)
		if !admitted {
			continue
		}

		probeContext, cancel := context.WithTimeout(ctx, cellularDataMonitorProbeTimeout)
		observed, probeErr := observer.NetworkStatus(probeContext, resolvedPhysicalID)
		cancel()
		if ctx.Err() != nil {
			releaseWatchdog()
			return
		}
		if errors.Is(probeErr, device.ErrDataOperationInProgress) ||
			errors.Is(probeErr, device.ErrDataBackendUnavailable) {
			releaseWatchdog()
			continue
		}
		if probeErr == nil && observed.Connected {
			runtime.recordProbeSuccess(config.ID, status.Revision)
			releaseWatchdog()
			continue
		}

		detail := strings.TrimSpace(observed.Detail)
		if detail == "" && probeErr != nil {
			detail = probeErr.Error()
		}
		if detail == "" {
			detail = "cellular data session is disconnected"
		}
		candidate, reconnect := runtime.recordProbeFailure(config.ID, status.Revision, detail, immediateFailure)
		if !reconnect {
			releaseWatchdog()
			continue
		}
		if _, accepted := runtime.requestIfCurrent(config.ID, candidate.Revision, "recovering", resolvedPhysicalID, request); accepted {
			if s.logger != nil {
				s.logger.Warn("cellular data monitor is reconnecting", "device_id", config.ID, "interface", config.Interface, "reason", detail)
			}
		}
		releaseWatchdog()
	}
}

func (runtime *cellularDataRuntime) signalLocked(entry *cellularDataRuntimeEntry) {
	close(entry.changed)
	entry.changed = make(chan struct{})
}

func (runtime *cellularDataRuntime) request(
	configID string,
	physicalID string,
	request device.NetworkRequest,
) cellularDataRuntimeStatus {
	return runtime.requestWithIdentity(configID, physicalID, request, "")
}

func (runtime *cellularDataRuntime) requestWithIdentity(
	configID string,
	physicalID string,
	request device.NetworkRequest,
	identity string,
) cellularDataRuntimeStatus {
	runtime.mu.Lock()
	status := runtime.requestLocked(configID, physicalID, request, strings.TrimSpace(identity))
	runtime.mu.Unlock()
	return status
}

func (runtime *cellularDataRuntime) requestLocked(
	configID string,
	physicalID string,
	request device.NetworkRequest,
	identity string,
) cellularDataRuntimeStatus {
	entry := runtime.entries[configID]
	if entry == nil {
		entry = &cellularDataRuntimeEntry{
			status:  cellularDataRuntimeStatus{Phase: "unknown"},
			changed: make(chan struct{}),
		}
		runtime.entries[configID] = entry
	}
	entry.physicalID = physicalID
	entry.request = request
	if identity == "" {
		identity = entry.identity
	}
	if identity != "" {
		entry.identity = identity
	}
	if identity != "" && entry.session != nil {
		entry.session.identity = identity
	}
	entry.status.DesiredEnabled = request.Enabled
	entry.status.Revision++
	entry.status.LastError = ""
	entry.status.ModemPhase = ""
	entry.status.MaintenancePhase = ""
	entry.probeFailures = 0
	entry.status.UpdatedAt = time.Now().UTC()
	if request.Enabled {
		if entry.session == nil || entry.session.phase == cellularDataSessionClosed {
			entry.session = runtime.newSessionLocked(physicalID, entry.identity)
		} else if entry.session.phase == cellularDataSessionClosing {
			// A new enable request may arrive while the old QMI stop is still
			// returning. Keep the old session in closing until the worker has
			// observed that operation's completion; run() will then create the
			// new session before replaying the enable request.
			if identity != "" {
				entry.session.identity = identity
			}
		}
		entry.status.Phase = "starting"
	} else {
		runtime.beginClosingLocked(entry)
		entry.status.Phase = "stopping"
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if !entry.running {
		entry.running = true
		go runtime.run(configID, entry)
	}
	runtime.signalLocked(entry)
	return entry.status
}

func (runtime *cellularDataRuntime) newSessionLocked(physicalID, identity string) *cellularDataSession {
	runtime.nextSessionGeneration++
	return &cellularDataSession{
		generation: runtime.nextSessionGeneration,
		physicalID: physicalID,
		identity:   strings.TrimSpace(identity),
		phase:      cellularDataSessionStarting,
		watchdog:   newCellularDataWatchdog(),
	}
}

func (runtime *cellularDataRuntime) beginClosingLocked(entry *cellularDataRuntimeEntry) {
	if entry == nil || entry.session == nil {
		return
	}
	entry.session.phase = cellularDataSessionClosing
	entry.session.watchdog.stop()
}

func (runtime *cellularDataRuntime) activateSessionLocked(entry *cellularDataRuntimeEntry, physicalID, identity string) {
	if entry == nil {
		return
	}
	if entry.session == nil || entry.session.phase == cellularDataSessionClosing || entry.session.phase == cellularDataSessionClosed {
		if identity == "" {
			identity = entry.identity
		}
		entry.session = runtime.newSessionLocked(physicalID, identity)
	}
	entry.session.physicalID = physicalID
	if identity != "" {
		entry.session.identity = identity
		entry.identity = identity
	}
	entry.session.phase = cellularDataSessionActive
	entry.session.watchdog.start(entry.session.generation)
}

func (runtime *cellularDataRuntime) closeSessionLocked(entry *cellularDataRuntimeEntry) {
	if entry == nil || entry.session == nil {
		return
	}
	entry.session.watchdog.stop()
	entry.session.phase = cellularDataSessionClosed
}

func (runtime *cellularDataRuntime) stopAllWatchdogs() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	for _, entry := range runtime.entries {
		runtime.beginClosingLocked(entry)
	}
}

// closeForPhysicalID is the terminal path for a physical disappearance. The
// modem manager has already lost the control device, so waiting for QMI stop
// would only add latency. The logical session is closed immediately and a
// later discovery event creates a fresh session on the reconnected device.
func (runtime *cellularDataRuntime) closeForPhysicalID(physicalID string) {
	physicalID = strings.TrimSpace(physicalID)
	if physicalID == "" {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	for _, entry := range runtime.entries {
		if entry.physicalID != physicalID || entry.session == nil || entry.status.MaintenancePhase == "device_disconnected" {
			continue
		}
		if entry.cancel != nil {
			entry.cancel()
		}
		runtime.closeSessionLocked(entry)
		entry.status.Connected = false
		entry.status.Phase = "failed"
		if !entry.status.DesiredEnabled {
			entry.status.Phase = "disabled"
		}
		entry.status.MaintenancePhase = "device_disconnected"
		entry.status.LastError = "physical modem is no longer present"
		entry.status.Revision++
		entry.status.UpdatedAt = time.Now().UTC()
		runtime.signalLocked(entry)
	}
}

func (runtime *cellularDataRuntime) closeIfPhysicalAbsent(configID string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil || entry.session == nil || entry.status.MaintenancePhase == "device_disconnected" {
		return
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	runtime.closeSessionLocked(entry)
	entry.status.Connected = false
	entry.status.Phase = "failed"
	if !entry.status.DesiredEnabled {
		entry.status.Phase = "disabled"
	}
	entry.status.MaintenancePhase = "device_disconnected"
	entry.status.LastError = "physical modem is no longer present"
	entry.status.Revision++
	entry.status.UpdatedAt = time.Now().UTC()
	runtime.signalLocked(entry)
}

func (runtime *cellularDataRuntime) requestIfCurrent(
	configID string,
	revision uint64,
	phase string,
	physicalID string,
	request device.NetworkRequest,
) (cellularDataRuntimeStatus, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil || entry.status.Revision != revision || entry.status.Phase != phase {
		return cellularDataRuntimeStatus{}, false
	}
	identity := entry.identity
	if entry.session != nil && entry.session.identity != "" {
		identity = entry.session.identity
	}
	return runtime.requestLocked(configID, physicalID, request, identity), true
}

// monitorCandidate returns the last accepted desired request and its current
// generation. The monitor must use the generation to avoid reconnecting a
// session after a newer user action (for example, an explicit disable).
func (runtime *cellularDataRuntime) monitorCandidate(configID string) (cellularDataRuntimeStatus, device.NetworkRequest, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil {
		return cellularDataRuntimeStatus{}, device.NetworkRequest{}, false
	}
	return entry.status, entry.request, true
}

func (runtime *cellularDataRuntime) acquireWatchdog(configID string, revision uint64) (func(), bool) {
	runtime.mu.Lock()
	entry := runtime.entries[configID]
	if entry == nil || entry.status.Revision != revision || entry.session == nil ||
		entry.session.phase != cellularDataSessionActive || entry.status.MaintenancePhase != "" {
		runtime.mu.Unlock()
		return nil, false
	}
	session := entry.session
	runtime.mu.Unlock()
	return session.watchdog.acquire(session.generation)
}

func (runtime *cellularDataRuntime) sessionIdentityChanged(configID string, revision uint64, identity string) bool {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil || entry.status.Revision != revision || entry.session == nil {
		return false
	}
	if entry.session.identity == "" {
		entry.session.identity = identity
		entry.identity = identity
		return false
	}
	return !strings.EqualFold(entry.session.identity, identity)
}

func (runtime *cellularDataRuntime) recordProbeSuccess(configID string, revision uint64) cellularDataRuntimeStatus {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil || entry.status.Revision != revision || entry.running ||
		(entry.status.Phase != "connected" && entry.status.Phase != "failed") ||
		entry.session == nil || entry.session.phase != cellularDataSessionActive || entry.status.MaintenancePhase != "" {
		if entry == nil {
			return cellularDataRuntimeStatus{}
		}
		return entry.status
	}
	changed := entry.probeFailures != 0 || entry.status.LastError != "" || entry.status.Phase != "connected" || !entry.status.Connected
	entry.probeFailures = 0
	entry.status.Connected = true
	entry.status.Phase = "connected"
	entry.status.LastError = ""
	entry.status.UpdatedAt = time.Now().UTC()
	if changed {
		runtime.signalLocked(entry)
	}
	return entry.status
}

// recordProbeFailure debounces live observations. Periodic probes require two
// consecutive failures; an explicit QMI packet-service indication is already
// a strong modem-side signal and can transition the same generation directly
// to recovering after the confirming NetworkStatus query.
func (runtime *cellularDataRuntime) recordProbeFailure(configID string, revision uint64, detail string, immediate bool) (cellularDataRuntimeStatus, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil || entry.status.Revision != revision || entry.running || !entry.status.DesiredEnabled ||
		entry.status.ModemPhase == "rebooting" || entry.status.MaintenancePhase != "" ||
		(entry.status.Phase != "connected" && entry.status.Phase != "failed") ||
		entry.session == nil || entry.session.phase != cellularDataSessionActive {
		if entry == nil {
			return cellularDataRuntimeStatus{}, false
		}
		return entry.status, false
	}
	entry.probeFailures++
	entry.status.LastError = detail
	entry.status.UpdatedAt = time.Now().UTC()
	if immediate || entry.status.Phase == "failed" || entry.probeFailures >= cellularDataMonitorFailureLimit {
		entry.status.Phase = "recovering"
		entry.status.Connected = false
		runtime.signalLocked(entry)
		return entry.status, true
	}
	runtime.signalLocked(entry)
	return entry.status, false
}

// invalidate replaces the current runtime generation without starting a new
// hardware operation. It is used when another control operation (for example
// CFUN reboot) is known to destroy the packet call. Replacing the entry, rather
// than only changing its fields, also prevents a cancelled older worker from
// replaying its request while the modem is still rebooting.
func (runtime *cellularDataRuntime) invalidate(
	configID string,
	desired bool,
	phase string,
	lastError string,
) cellularDataRuntimeStatus {
	return runtime.invalidateWithModemPhase(configID, desired, phase, lastError, "")
}

func (runtime *cellularDataRuntime) invalidateWithModemPhase(
	configID string,
	desired bool,
	phase string,
	lastError string,
	modemPhase string,
) cellularDataRuntimeStatus {
	return runtime.invalidateWithPhases(configID, desired, phase, lastError, modemPhase, "")
}

func (runtime *cellularDataRuntime) invalidateWithMaintenancePhase(
	configID string,
	desired bool,
	phase string,
	lastError string,
	maintenancePhase string,
) cellularDataRuntimeStatus {
	return runtime.invalidateWithPhases(configID, desired, phase, lastError, "", maintenancePhase)
}

func (runtime *cellularDataRuntime) invalidateWithPhases(
	configID string,
	desired bool,
	phase string,
	lastError string,
	modemPhase string,
	maintenancePhase string,
) cellularDataRuntimeStatus {
	runtime.mu.Lock()
	var previousWatchdog *cellularDataWatchdog
	if previous := runtime.entries[configID]; previous != nil && previous.session != nil {
		previousWatchdog = previous.session.watchdog
	}
	status := runtime.invalidateLocked(configID, desired, phase, lastError, modemPhase, maintenancePhase)
	runtime.mu.Unlock()
	if previousWatchdog != nil {
		waitContext, cancel := context.WithTimeout(context.Background(), cellularDataMonitorProbeTimeout)
		_ = previousWatchdog.wait(waitContext)
		cancel()
	}
	return status
}

func (runtime *cellularDataRuntime) invalidateLocked(
	configID string,
	desired bool,
	phase string,
	lastError string,
	modemPhase string,
	maintenancePhase string,
) cellularDataRuntimeStatus {
	previous := runtime.entries[configID]
	revision := uint64(1)
	physicalID := ""
	identity := ""
	request := device.NetworkRequest{Enabled: desired}
	if previous != nil {
		revision = previous.status.Revision + 1
		physicalID = previous.physicalID
		identity = previous.identity
		if previous.session != nil && previous.session.identity != "" {
			identity = previous.session.identity
		}
		request = previous.request
		request.Enabled = desired
		if previous.cancel != nil {
			previous.cancel()
		}
		runtime.closeSessionLocked(previous)
		close(previous.changed)
		if modemPhase == "" && phase != "failed" && phase != "disabled" {
			modemPhase = previous.status.ModemPhase
		}
	}
	if phase == "" {
		if desired {
			phase = "recovering"
		} else {
			phase = "disabled"
		}
	}
	entry := &cellularDataRuntimeEntry{
		physicalID: physicalID,
		identity:   identity,
		request:    request,
		status: cellularDataRuntimeStatus{
			DesiredEnabled:   desired,
			Connected:        false,
			Phase:            phase,
			ModemPhase:       modemPhase,
			MaintenancePhase: maintenancePhase,
			LastError:        lastError,
			Revision:         revision,
			UpdatedAt:        time.Now().UTC(),
		},
		changed: make(chan struct{}),
	}
	runtime.entries[configID] = entry
	return entry.status
}

func (runtime *cellularDataRuntime) invalidateIfCurrent(
	configID string,
	revision uint64,
	currentPhase string,
	desired bool,
	nextPhase string,
	lastError string,
) (cellularDataRuntimeStatus, bool) {
	runtime.mu.Lock()
	entry := runtime.entries[configID]
	if entry == nil || entry.status.Revision != revision || entry.status.Phase != currentPhase {
		runtime.mu.Unlock()
		return cellularDataRuntimeStatus{}, false
	}
	var previousWatchdog *cellularDataWatchdog
	if entry.session != nil {
		previousWatchdog = entry.session.watchdog
	}
	status := runtime.invalidateLocked(configID, desired, nextPhase, lastError, "", "")
	runtime.mu.Unlock()
	if previousWatchdog != nil {
		waitContext, cancel := context.WithTimeout(context.Background(), cellularDataMonitorProbeTimeout)
		_ = previousWatchdog.wait(waitContext)
		cancel()
	}
	return status, true
}

func (runtime *cellularDataRuntime) isCurrent(configID string, revision uint64, phase string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	return entry != nil && entry.status.Revision == revision && entry.status.Phase == phase
}

func (runtime *cellularDataRuntime) run(configID string, entry *cellularDataRuntimeEntry) {
	for {
		runtime.mu.Lock()
		current := runtime.entries[configID]
		if current != entry {
			runtime.mu.Unlock()
			return
		}
		if entry.status.MaintenancePhase != "" {
			entry.running = false
			entry.cancel = nil
			runtime.mu.Unlock()
			return
		}
		if entry.request.Enabled && entry.session != nil && entry.session.phase == cellularDataSessionClosing {
			// The previous disable operation has returned. The old session is
			// now closed and a new logical session may own the next enable.
			runtime.closeSessionLocked(entry)
			entry.session = runtime.newSessionLocked(entry.physicalID, entry.identity)
		}
		revision := entry.status.Revision
		physicalID := entry.physicalID
		request := entry.request
		var closingWatchdog *cellularDataWatchdog
		if !request.Enabled && entry.session != nil && entry.session.phase == cellularDataSessionClosing {
			closingWatchdog = entry.session.watchdog
		}
		root := runtime.root
		operationTimeout := 75 * time.Second
		if !request.Enabled {
			operationTimeout = 30 * time.Second
		}
		operationContext, cancel := context.WithTimeout(root, operationTimeout)
		entry.cancel = cancel
		runtime.mu.Unlock()
		if closingWatchdog != nil {
			drainContext, drainCancel := context.WithTimeout(operationContext, cellularDataMonitorProbeTimeout)
			_ = closingWatchdog.wait(drainContext)
			drainCancel()
		}

		var result device.NetworkResult
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			result, err = runtime.setter.SetNetwork(operationContext, physicalID, request)
			if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			if attempt == 2 {
				break
			}
			delay := time.Duration(2+attempt*3) * time.Second
			select {
			case <-operationContext.Done():
				err = operationContext.Err()
				attempt = 3
			case <-time.After(delay):
				runtime.mu.Lock()
				if runtime.entries[configID] == entry && entry.status.Revision == revision {
					entry.status.Phase = "recovering"
					entry.status.UpdatedAt = time.Now().UTC()
					runtime.signalLocked(entry)
				}
				runtime.mu.Unlock()
			}
		}
		cancel()

		runtime.mu.Lock()
		if runtime.entries[configID] != entry {
			runtime.mu.Unlock()
			return
		}
		entry.cancel = nil
		if entry.status.Revision != revision {
			// A newer desired state arrived while the old operation was running.
			runtime.mu.Unlock()
			continue
		}
		entry.running = false
		entry.status.UpdatedAt = time.Now().UTC()
		if err != nil {
			entry.status.Phase = "failed"
			entry.status.LastError = err.Error()
			// Only a completed setup is reported as connected. QMI disable and
			// failed setup paths remove host routes before returning errors.
			entry.status.Connected = false
			if request.Enabled && entry.session != nil && entry.session.phase == cellularDataSessionActive {
				// A failed recovery keeps ownership of the same data-session
				// watchdog so a later event or periodic probe can retry it. A
				// failed initial start never arms a watchdog.
			} else {
				runtime.closeSessionLocked(entry)
			}
			if runtime.logger != nil && !errors.Is(err, context.Canceled) {
				runtime.logger.Warn("cellular data reconcile failed", "device_id", configID, "desired_enabled", request.Enabled, "error", err)
			}
		} else {
			entry.status.Connected = result.Enabled
			entry.status.LastError = ""
			if result.Enabled {
				runtime.activateSessionLocked(entry, physicalID, entry.identity)
				entry.status.Phase = "connected"
			} else {
				runtime.closeSessionLocked(entry)
				entry.status.Phase = "disabled"
			}
		}
		runtime.signalLocked(entry)
		runtime.mu.Unlock()
		if err == nil && result.Enabled && runtime.onConnected != nil {
			runtime.onConnected(configID, physicalID, revision)
		}
		return
	}
}

func (runtime *cellularDataRuntime) status(configID string, desired bool) cellularDataRuntimeStatus {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil {
		return cellularDataRuntimeStatus{DesiredEnabled: desired, Phase: "unknown"}
	}
	status := entry.status
	status.DesiredEnabled = desired
	return status
}

// observe folds a live modem/interface sample into a terminal runtime state.
// It never overwrites a start/stop/recovery operation that is still running.
func (runtime *cellularDataRuntime) observe(configID string, desired, connected bool, observationErr error) cellularDataRuntimeStatus {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	entry := runtime.entries[configID]
	if entry == nil {
		phase := "disabled"
		if desired {
			phase = "unknown"
		}
		entry = &cellularDataRuntimeEntry{
			status:  cellularDataRuntimeStatus{DesiredEnabled: desired, Phase: phase, Revision: 1},
			changed: make(chan struct{}),
		}
		runtime.entries[configID] = entry
	}
	entry.status.DesiredEnabled = desired
	if entry.running || entry.status.MaintenancePhase != "" || entry.status.Phase == "starting" || entry.status.Phase == "stopping" || entry.status.Phase == "recovering" {
		return entry.status
	}
	entry.status.UpdatedAt = time.Now().UTC()
	if observationErr != nil {
		// A concurrent start/stop owns the authoritative transition. Other probe
		// errors (lost device, stale WDS client, timeout) invalidate an old
		// connected result instead of silently retaining it.
		if errors.Is(observationErr, device.ErrDataOperationInProgress) {
			return entry.status
		}
		entry.status.Connected = false
		entry.status.LastError = observationErr.Error()
		if desired {
			entry.status.Phase = "failed"
		} else {
			entry.status.Phase = "disabled"
		}
		runtime.signalLocked(entry)
		return entry.status
	}
	entry.status.Connected = desired && connected
	entry.status.LastError = ""
	if !desired {
		runtime.closeSessionLocked(entry)
		entry.status.Phase = "disabled"
	} else if connected {
		if entry.session == nil || entry.session.phase == cellularDataSessionClosed {
			entry.session = runtime.newSessionLocked(entry.physicalID, entry.identity)
		}
		entry.session.phase = cellularDataSessionActive
		entry.session.watchdog.start(entry.session.generation)
		entry.status.Phase = "connected"
	} else {
		entry.status.Phase = "failed"
		entry.status.LastError = "live QMI packet session or host route is not ready"
	}
	runtime.signalLocked(entry)
	return entry.status
}

func (runtime *cellularDataRuntime) wait(
	ctx context.Context,
	configID string,
	revision uint64,
) (cellularDataRuntimeStatus, error) {
	for {
		runtime.mu.Lock()
		entry := runtime.entries[configID]
		if entry == nil {
			runtime.mu.Unlock()
			return cellularDataRuntimeStatus{}, errors.New("cellular data operation disappeared")
		}
		status := entry.status
		changed := entry.changed
		terminal := status.Revision >= revision && (status.Phase == "connected" || status.Phase == "disabled" || status.Phase == "failed")
		runtime.mu.Unlock()
		if terminal {
			if status.Phase == "failed" {
				return status, errors.New(status.LastError)
			}
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-changed:
		}
	}
}

func (s *Server) applyCellularData(
	ctx context.Context,
	configID string,
	physicalID string,
	request device.NetworkRequest,
) (device.NetworkResult, error) {
	if !request.Enabled {
		s.clearPublicIP(configID)
	}
	runtime := s.cellularDataRuntime()
	identity := ""
	if s.devices != nil {
		if entry, err := s.devices.Get(physicalID); err == nil && entry.Snapshot != nil {
			identity = strings.TrimSpace(entry.Snapshot.ICCID)
		}
	}
	requested := runtime.requestWithIdentity(configID, physicalID, request, identity)
	status, err := runtime.wait(ctx, configID, requested.Revision)
	return device.NetworkResult{Enabled: status.Connected}, err
}
