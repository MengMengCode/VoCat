package device

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"vocat/internal/modem"
	"vocat/internal/pcsc"
)

type Options struct {
	Discoverer     modem.Discoverer
	Opener         modem.Opener
	Logger         *slog.Logger
	CommandTimeout time.Duration
	LongTimeout    time.Duration
	SMSTimeout     time.Duration
	ScanTimeout    time.Duration
	CardReaders    *pcsc.Service
}

type Manager struct {
	mu                            sync.RWMutex
	esimMu                        sync.Mutex // serializes eSIM card access (list/switch/download)
	esimRecoveryMu                sync.Mutex
	esimRecoveries                map[string]chan struct{}
	esimCacheMu                   sync.RWMutex
	esimCache                     map[string]EsimInfo
	esimActiveName                map[string]string
	discoverer                    modem.Discoverer
	opener                        modem.Opener
	commandTimeout                time.Duration
	longTimeout                   time.Duration
	smsTimeout                    time.Duration
	scanTimeout                   time.Duration
	qmiEUICCOpener                qmiEUICCSessionOpener
	qmiRadioOpener                qmiRadioSessionOpener
	qmiSMSOpener                  qmiSMSSessionOpener
	cardReaders                   *pcsc.Service
	logger                        *slog.Logger
	qmiSMSBackoffMu               sync.Mutex
	qmiSMSBackoffUntil            map[string]time.Time
	nativeQMIRegistrationMu       sync.Mutex
	nativeQMIRegistrationInFlight map[string]struct{}
	started                       bool
	devices                       map[string]*managedDevice
	ussdSessions                  map[string]ussdSession
}

// ussdSession tracks an open USSD dialog on a device so a follow-up Continue or
// Cancel can be routed back to the right modem. The modem owns the actual
// network session; this map only records which device a session id belongs to.
type ussdSession struct {
	deviceID  string
	createdAt time.Time
}

type managedDevice struct {
	opMu              sync.Mutex
	candidate         modem.Candidate
	client            modem.Client
	snapshot          *Snapshot
	lastError         string
	lastUpdated       time.Time
	discovered        bool
	recovering        bool
	preFlightMode     *int
	resetClientOnLock bool
}

func NewManager(options Options) (*Manager, error) {
	if options.Discoverer == nil {
		options.Discoverer = modem.NewSystemDiscoverer()
	}
	if options.Opener == nil {
		options.Opener = modem.SerialOpener{}
	}
	if options.CommandTimeout <= 0 {
		options.CommandTimeout = 3 * time.Second
	}
	if options.LongTimeout <= 0 {
		options.LongTimeout = 45 * time.Second
	}
	if options.SMSTimeout <= 0 {
		// Quectel documents a maximum AT+CMGS response time of 120 seconds.
		options.SMSTimeout = 125 * time.Second
	}
	if options.ScanTimeout <= 0 {
		// AT+COPS=? can take well over a minute while the modem sweeps every band.
		options.ScanTimeout = 150 * time.Second
	}
	return &Manager{
		discoverer:                    options.Discoverer,
		opener:                        options.Opener,
		commandTimeout:                options.CommandTimeout,
		longTimeout:                   options.LongTimeout,
		smsTimeout:                    options.SMSTimeout,
		scanTimeout:                   options.ScanTimeout,
		qmiEUICCOpener:                openQMIEUICCSession,
		qmiRadioOpener:                openQMIRadioSession,
		qmiSMSOpener:                  openQMISMSSession,
		cardReaders:                   options.CardReaders,
		logger:                        options.Logger,
		qmiSMSBackoffUntil:            make(map[string]time.Time),
		nativeQMIRegistrationInFlight: make(map[string]struct{}),
		devices:                       make(map[string]*managedDevice),
		ussdSessions:                  make(map[string]ussdSession),
		esimRecoveries:                make(map[string]chan struct{}),
		esimCache:                     make(map[string]EsimInfo),
		esimActiveName:                make(map[string]string),
	}, nil
}

// logEvent emits secret-neutral, machine-filterable runtime events. The
// service passes its loghub-backed logger here, so these records are available
// through both /api/logs/history and /api/logs/stream. Keep identifiers out of
// the message itself; callers should pass only redacted values as attributes.
func (manager *Manager) logEvent(level slog.Level, message string, attrs ...any) {
	if manager == nil || manager.logger == nil {
		return
	}
	manager.logger.Log(context.Background(), level, message, attrs...)
}

func redactSubscriberID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return value
	}
	return "…" + value[len(value)-4:]
}

func (manager *Manager) Start(ctx context.Context) error {
	manager.mu.Lock()
	if manager.started {
		manager.mu.Unlock()
		return nil
	}
	manager.mu.Unlock()

	if _, err := manager.Discover(ctx); err != nil {
		return err
	}
	manager.mu.Lock()
	manager.started = true
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	manager.started = false
	states := make([]*managedDevice, 0, len(manager.devices))
	for _, state := range manager.devices {
		states = append(states, state)
	}
	manager.mu.Unlock()

	var closeErrors []error
	for _, state := range states {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(closeErrors, err)...)
		}
		state.opMu.Lock()
		if state.client != nil {
			if err := state.client.Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
			state.client = nil
		}
		state.opMu.Unlock()
	}
	return errors.Join(closeErrors...)
}

func (manager *Manager) Discover(ctx context.Context) ([]Device, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	candidates, err := manager.discoverer.Discover(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(candidates))

	manager.mu.Lock()
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" {
			continue
		}
		seen[candidate.ID] = struct{}{}
		state := manager.devices[candidate.ID]
		if state == nil {
			manager.devices[candidate.ID] = &managedDevice{
				candidate:  candidate,
				discovered: true,
			}
			continue
		}
		if state.candidate.ATPort.OpenPath() != candidate.ATPort.OpenPath() {
			state.resetClientOnLock = true
		}
		state.candidate = candidate
		state.discovered = true
	}
	var stale []*managedDevice
	for id, state := range manager.devices {
		if _, ok := seen[id]; ok {
			continue
		}
		state.discovered = false
		stale = append(stale, state)
	}
	manager.mu.Unlock()

	for _, state := range stale {
		state.opMu.Lock()
		if state.client != nil {
			_ = state.client.Close()
			state.client = nil
			manager.logEvent(slog.LevelWarn, "control-plane AT transport discarded",
				"category", "control_plane", "event", "at_transport_discarded",
				"device_id", state.candidate.ID, "reason", "device_disappeared")
		}
		state.opMu.Unlock()
	}
	manager.resetChangedClients()
	return manager.List(), nil
}

func (manager *Manager) resetChangedClients() {
	manager.mu.Lock()
	states := make([]*managedDevice, 0, len(manager.devices))
	for _, state := range manager.devices {
		if state.resetClientOnLock {
			states = append(states, state)
			state.resetClientOnLock = false
		}
	}
	manager.mu.Unlock()
	for _, state := range states {
		state.opMu.Lock()
		if state.client != nil {
			_ = state.client.Close()
			state.client = nil
		}
		state.opMu.Unlock()
	}
}

func (manager *Manager) List() []Device {
	manager.mu.RLock()
	result := make([]Device, 0, len(manager.devices))
	for id, state := range manager.devices {
		result = append(result, copyDevice(id, state))
	}
	manager.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (manager *Manager) Get(id string) (Device, error) {
	manager.mu.RLock()
	state := manager.devices[id]
	if state == nil {
		manager.mu.RUnlock()
		return Device{}, ErrNotFound
	}
	result := copyDevice(id, state)
	manager.mu.RUnlock()
	return result, nil
}

func copyDevice(id string, state *managedDevice) Device {
	var snapshot *Snapshot
	if state.snapshot != nil {
		value := *state.snapshot
		value.Warnings = append([]string(nil), value.Warnings...)
		snapshot = &value
	}
	return Device{
		ID:          id,
		Candidate:   copyCandidate(state.candidate),
		Snapshot:    snapshot,
		LastError:   state.lastError,
		Discovered:  state.discovered,
		Recovering:  state.recovering,
		LastUpdated: state.lastUpdated,
	}
}

func copyCandidate(candidate modem.Candidate) modem.Candidate {
	candidate.Ports = append([]modem.Port(nil), candidate.Ports...)
	return candidate
}

func (manager *Manager) lookup(id string) (*managedDevice, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.started {
		return nil, ErrNotStarted
	}
	state := manager.devices[id]
	if state == nil || !state.discovered {
		return nil, ErrNotFound
	}
	return state, nil
}

func (manager *Manager) clientLocked(
	ctx context.Context,
	state *managedDevice,
	candidate modem.Candidate,
) (modem.Client, error) {
	if state.client != nil {
		if poisoned, ok := state.client.(modem.PoisonedClient); ok && poisoned.Poisoned() {
			// The cached session hit a transport-fatal error (a failed
			// write/drain/read or a closed serial line); the underlying fd is
			// wedged and every subsequent command reuses the corpse, so the
			// device stays stuck on EIO forever. Discard it and reopen so the
			// next AT/CSIM call self-heals. AT-level failures (CommandError,
			// command timeout) do not poison — those leave a healthy transport
			// that reopening would only destroy over a transient +CME ERROR.
			_ = state.client.Close()
			state.client = nil
		} else {
			return state.client, nil
		}
	}
	if !candidate.HasATPort() {
		return nil, ErrNoATPort
	}
	client, err := manager.opener.Open(ctx, candidate.ATPort)
	if err != nil {
		manager.logEvent(slog.LevelWarn, "control-plane AT transport open failed",
			"category", "control_plane", "event", "at_transport_open_failed",
			"device_id", candidate.ID, "at_path", candidate.ATPort.OpenPath(), "error", err)
		return nil, err
	}
	state.client = client
	manager.logEvent(slog.LevelInfo, "control-plane AT transport opened",
		"category", "control_plane", "event", "at_transport_opened",
		"device_id", candidate.ID, "at_path", candidate.ATPort.OpenPath())
	return client, nil
}

func (manager *Manager) setResult(
	id string,
	state *managedDevice,
	snapshot *Snapshot,
	err error,
) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.devices[id] != state {
		return
	}
	if snapshot != nil && (err == nil || state.snapshot == nil) {
		value := *snapshot
		value.Warnings = append([]string(nil), snapshot.Warnings...)
		state.snapshot = &value
		state.lastUpdated = snapshot.UpdatedAt
	}
	if err != nil {
		state.lastError = err.Error()
		manager.logEvent(slog.LevelWarn, "device operation failed",
			"category", "device", "event", "operation_failed",
			"device_id", id, "recovering", state.recovering, "error", err)
	} else {
		state.lastError = ""
		if snapshot != nil {
			state.recovering = false
		}
	}
}

func (manager *Manager) candidateFor(state *managedDevice) modem.Candidate {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return copyCandidate(state.candidate)
}

func (manager *Manager) validateActive(
	id string,
	state *managedDevice,
) error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.started {
		return ErrNotStarted
	}
	current := manager.devices[id]
	if current != state || !state.discovered {
		return ErrNotFound
	}
	return nil
}

func (manager *Manager) Refresh(ctx context.Context, id string) (Snapshot, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return Snapshot{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return Snapshot{}, err
	}
	candidate := manager.candidateFor(state)
	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return Snapshot{}, err
	}
	snapshot, err := manager.readSnapshot(ctx, id, candidate, client)
	manager.setResult(id, state, &snapshot, err)
	return snapshot, err
}

func (manager *Manager) ExecuteAT(
	ctx context.Context,
	id string,
	command string,
) (modem.Response, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return modem.Response{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return modem.Response{}, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return modem.Response{}, err
	}
	commandCtx, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	defer cancel()
	response, err := client.Execute(commandCtx, command)
	manager.setResult(id, state, nil, err)
	return response, err
}

// ExecuteSensitiveAT runs an AT command whose payload contains short-lived
// authentication material. The original transport error is returned to the
// caller, but it is never retained in the device snapshot because a
// modem.CommandError may include the full command.
func (manager *Manager) ExecuteSensitiveAT(
	ctx context.Context,
	id string,
	command string,
) (modem.Response, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return modem.Response{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return modem.Response{}, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(
			id,
			state,
			nil,
			errors.New("sensitive AT command could not open the modem"),
		)
		return modem.Response{}, err
	}
	commandCtx, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	defer cancel()
	response, err := client.Execute(commandCtx, command)
	recordedErr := err
	if err != nil {
		recordedErr = errors.New("sensitive AT command failed")
	}
	manager.setResult(id, state, nil, recordedErr)
	return response, err
}

func (manager *Manager) Reboot(ctx context.Context, id string) error {
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return err
	}
	commandCtx, cancel := manager.withTimeout(ctx, manager.longTimeout)
	defer cancel()
	_, err = client.Execute(commandCtx, "AT+CFUN=1,1")
	if closeErr := client.Close(); err == nil {
		err = closeErr
	}
	state.client = nil
	state.preFlightMode = nil
	manager.beginRecovery(id, state)
	manager.setResult(id, state, nil, err)
	return err
}

// rebootForProfileSwitch is the post-EnableProfile modem reset. After the eUICC
// marks a new profile active, the modem keeps the old SIM cached and lands in
// SIM failure (-CME 13) until it is bounced. ESIMSwitchProfile has already
// released opMu by the time it calls this, so the reset is safe to take the
// lock. This mirrors Reboot but is separate so the call site can't recurse into
// a guarded-reset path.
func (manager *Manager) rebootForProfileSwitch(ctx context.Context, id string) error {
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return err
	}
	if handled, resetErr := manager.resetNativeQMIModemForProfileSwitchLocked(ctx, id, state); handled {
		manager.setResult(id, state, nil, resetErr)
		return resetErr
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return err
	}
	commandCtx, cancel := manager.withTimeout(ctx, manager.longTimeout)
	defer cancel()
	_, err = client.Execute(commandCtx, "AT+CFUN=1,1")
	if closeErr := client.Close(); err == nil {
		err = closeErr
	}
	state.client = nil
	state.preFlightMode = nil
	manager.beginRecovery(id, state)
	manager.setResult(id, state, nil, err)
	return err
}

func (manager *Manager) beginRecovery(id string, state *managedDevice) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.devices[id] == state {
		state.recovering = true
	}
}

func (manager *Manager) finishRecovery(id string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if state := manager.devices[id]; state != nil {
		state.recovering = false
	}
}

func (manager *Manager) withTimeout(
	ctx context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func (manager *Manager) command(
	ctx context.Context,
	client modem.Client,
	command string,
) (modem.Response, error) {
	commandCtx, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	defer cancel()
	response, err := client.Execute(commandCtx, command)
	if err != nil {
		return response, fmt.Errorf("%s: %w", command, err)
	}
	return response, nil
}
