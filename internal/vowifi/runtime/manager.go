// Package runtime owns the long-lived VoWiFi orchestrators used by the
// service. It keeps HTTP requests short while preserving every evidence-backed
// state transition through the supplied state callback.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"vocat/internal/vowifi"
)

var (
	ErrNotRegistered              = errors.New("vowifi runtime: device is not registered")
	ErrOperationInProgress        = errors.New("vowifi runtime: an operation is already in progress")
	ErrNotQuiesced                = errors.New("vowifi runtime: device is not quiesced")
	ErrSubscriberChangeInProgress = errors.New("vowifi runtime: subscriber change is in progress")
	ErrClosed                     = errors.New("vowifi runtime: manager is closed")
)

const (
	defaultOperationTimeout = 2 * time.Minute
	// Match VoHive's target-state recovery cadence: give the modem/network
	// time to settle after a failed IKE/IMS attempt instead of hammering a
	// registrar that may have just rejected the subscriber.
	defaultRetryInitial = 30 * time.Second
	defaultRetryMaximum = 2 * time.Minute
)

type StateHandler func(context.Context, vowifi.State) error
type OrchestratorFactory func(context.Context, string) (*vowifi.Orchestrator, error)

type Options struct {
	Logger           *slog.Logger
	OperationTimeout time.Duration
	RetryInitial     time.Duration
	RetryMaximum     time.Duration
	OnState          StateHandler
	Factory          OrchestratorFactory
}

type Manager struct {
	ctx              context.Context
	cancel           context.CancelFunc
	logger           *slog.Logger
	operationTimeout time.Duration
	retryInitial     time.Duration
	retryMaximum     time.Duration
	onState          StateHandler
	factory          OrchestratorFactory

	mu                   sync.Mutex
	closed               bool
	entries              map[string]*entry
	subscriberChanges    map[string]uint64
	nextSubscriberChange uint64
	wg                   sync.WaitGroup
}

type entry struct {
	orchestrator     *vowifi.Orchestrator
	busy             bool
	reconnectPending bool
	disablePending   bool
	desiredEnabled   bool
	autoRetryPending bool
	retryFailures    uint
	operationCancel  context.CancelFunc
	stopWatch        func()
	watchCancel      context.CancelFunc
	watchDone        chan struct{}
}

func New(options Options) *Manager {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.OperationTimeout <= 0 {
		options.OperationTimeout = defaultOperationTimeout
	}
	if options.RetryInitial <= 0 {
		options.RetryInitial = defaultRetryInitial
	}
	if options.RetryMaximum <= 0 {
		options.RetryMaximum = defaultRetryMaximum
	}
	if options.RetryMaximum < options.RetryInitial {
		options.RetryMaximum = options.RetryInitial
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		ctx:               ctx,
		cancel:            cancel,
		logger:            options.Logger,
		operationTimeout:  options.OperationTimeout,
		retryInitial:      options.RetryInitial,
		retryMaximum:      options.RetryMaximum,
		onState:           options.OnState,
		factory:           options.Factory,
		entries:           make(map[string]*entry),
		subscriberChanges: make(map[string]uint64),
	}
}

// Ensure registers a runtime for deviceID on demand. This keeps device
// configuration and runtime lifecycle in sync when a modem is added after the
// service has already started.
func (manager *Manager) Ensure(ctx context.Context, deviceID string) error {
	_, err := manager.getOrEnsure(ctx, deviceID)
	return err
}

// getOrEnsure returns the entry it creates while still holding the same lock
// used by subscriber-change barriers. Callers must not perform a second map
// lookup after Ensure: a profile change may legitimately remove that entry as
// soon as Ensure returns.
func (manager *Manager) getOrEnsure(ctx context.Context, deviceID string) (*entry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, ErrClosed
	}
	if manager.subscriberChangeLocked(deviceID) {
		return nil, ErrSubscriberChangeInProgress
	}
	if item := manager.entries[deviceID]; item != nil {
		return item, nil
	}
	if manager.factory == nil {
		return nil, ErrNotRegistered
	}

	// The factory is called while holding the manager lock so concurrent status
	// and enable requests cannot create duplicate runtimes for the same device.
	orchestrator, err := manager.factory(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if orchestrator == nil {
		return nil, errors.New("vowifi runtime: factory returned a nil orchestrator")
	}
	state := orchestrator.State()
	if state.DeviceID != deviceID {
		_ = orchestrator.Close(context.Background())
		return nil, fmt.Errorf(
			"vowifi runtime: factory returned device %q for %q",
			state.DeviceID,
			deviceID,
		)
	}
	states, stopWatch := orchestrator.Subscribe(8)
	watchContext, watchCancel := context.WithCancel(manager.ctx)
	item := &entry{
		orchestrator: orchestrator,
		stopWatch:    stopWatch,
		watchCancel:  watchCancel,
		watchDone:    make(chan struct{}),
	}
	manager.entries[deviceID] = item
	manager.wg.Add(1)
	go manager.watch(watchContext, deviceID, item, states)
	return item, nil
}

func (manager *Manager) Register(orchestrator *vowifi.Orchestrator) error {
	if orchestrator == nil {
		return errors.New("vowifi runtime: orchestrator is nil")
	}
	state := orchestrator.State()
	if state.DeviceID == "" {
		return errors.New("vowifi runtime: orchestrator device ID is empty")
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	if manager.subscriberChangeLocked(state.DeviceID) {
		return ErrSubscriberChangeInProgress
	}
	if _, exists := manager.entries[state.DeviceID]; exists {
		return fmt.Errorf("vowifi runtime: device %q is already registered", state.DeviceID)
	}
	states, stopWatch := orchestrator.Subscribe(8)
	watchContext, watchCancel := context.WithCancel(manager.ctx)
	item := &entry{
		orchestrator: orchestrator,
		stopWatch:    stopWatch,
		watchCancel:  watchCancel,
		watchDone:    make(chan struct{}),
	}
	manager.entries[state.DeviceID] = item
	manager.wg.Add(1)
	go manager.watch(watchContext, state.DeviceID, item, states)
	return nil
}

func (manager *Manager) State(deviceID string) (vowifi.State, error) {
	item, err := manager.getOrEnsure(manager.ctx, deviceID)
	if err != nil {
		return vowifi.State{}, err
	}
	manager.mu.Lock()
	if err := manager.validateEntryLocked(deviceID, item); err != nil {
		manager.mu.Unlock()
		return vowifi.State{}, err
	}
	state := item.orchestrator.State()
	manager.mu.Unlock()
	return state, nil
}

// BeginSubscriberChange atomically blocks new runtime users and removes the
// quiesced runtime for deviceID. The returned release is idempotent and must be
// held until the physical SIM mutation (or config deletion) has completed.
// While the guard is held, no status, enable, SMS, or call request may recreate
// an orchestrator with the old subscriber identity.
func (manager *Manager) BeginSubscriberChange(
	ctx context.Context,
	deviceID string,
) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil, ErrClosed
	}
	if manager.subscriberChangeLocked(deviceID) {
		manager.mu.Unlock()
		return nil, ErrSubscriberChangeInProgress
	}
	manager.nextSubscriberChange++
	if manager.nextSubscriberChange == 0 {
		manager.nextSubscriberChange++
	}
	token := manager.nextSubscriberChange
	manager.subscriberChanges[deviceID] = token
	watchDone, err := manager.invalidateLocked(ctx, deviceID, true)
	if err != nil {
		delete(manager.subscriberChanges, deviceID)
		manager.mu.Unlock()
		return nil, err
	}
	manager.mu.Unlock()

	if err := waitForWatch(ctx, watchDone); err != nil {
		manager.releaseSubscriberChange(deviceID, token)
		return nil, fmt.Errorf("join device %q runtime watcher: %w", deviceID, err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			manager.releaseSubscriberChange(deviceID, token)
		})
	}, nil
}

// Invalidate removes one cached orchestrator after its subscriber session has
// been quiesced. The next Ensure call rebuilds the runtime through Factory, so
// no SIM identity or carrier configuration from the previous card is reused.
// Subscriber-changing callers should use BeginSubscriberChange so that this
// standalone invalidation cannot be followed by an old-identity rebuild.
func (manager *Manager) Invalidate(ctx context.Context, deviceID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrClosed
	}
	watchDone, err := manager.invalidateLocked(ctx, deviceID, false)
	manager.mu.Unlock()
	if err != nil {
		return err
	}
	if err := waitForWatch(ctx, watchDone); err != nil {
		return fmt.Errorf("join device %q runtime watcher: %w", deviceID, err)
	}
	return nil
}

// invalidateLocked closes and removes a quiesced entry. When subscriberChange
// is true, a desired enable left in the narrow Ensure-to-startOperation window
// is stale policy rather than an active session and is cleared before removal.
func (manager *Manager) invalidateLocked(
	ctx context.Context,
	deviceID string,
	subscriberChange bool,
) (<-chan struct{}, error) {
	item := manager.entries[deviceID]
	if item == nil {
		return nil, nil
	}
	state := item.orchestrator.State()
	if item.busy || (!subscriberChange && item.desiredEnabled) || state.Enabled || state.Active ||
		(state.Phase != vowifi.PhaseIdle && state.Phase != vowifi.PhaseFailed) {
		return nil, fmt.Errorf("%w: phase=%s", ErrNotQuiesced, state.Phase)
	}
	if subscriberChange {
		item.desiredEnabled = false
		item.reconnectPending = false
		item.disablePending = false
		item.autoRetryPending = false
		item.retryFailures = 0
	}
	if err := item.orchestrator.Close(ctx); err != nil {
		return nil, fmt.Errorf("close device %q runtime before invalidation: %w", deviceID, err)
	}
	delete(manager.entries, deviceID)
	if item.watchCancel != nil {
		item.watchCancel()
		item.watchCancel = nil
	}
	if item.stopWatch != nil {
		item.stopWatch()
		item.stopWatch = nil
	}
	return item.watchDone, nil
}

func (manager *Manager) subscriberChangeLocked(deviceID string) bool {
	_, blocked := manager.subscriberChanges[deviceID]
	return blocked
}

func (manager *Manager) releaseSubscriberChange(deviceID string, token uint64) {
	manager.mu.Lock()
	if manager.subscriberChanges[deviceID] == token {
		delete(manager.subscriberChanges, deviceID)
	}
	manager.mu.Unlock()
}

func waitForWatch(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RequestEnabled queues an enable or disable transaction and returns
// immediately. Callers observe progress through State; provider errors are
// persisted in the orchestrator state instead of being lost with an HTTP
// request context.
func (manager *Manager) RequestEnabled(deviceID string, enabled bool) (vowifi.State, error) {
	item, err := manager.getOrEnsure(manager.ctx, deviceID)
	if err != nil {
		return vowifi.State{}, err
	}
	manager.mu.Lock()
	if err := manager.validateEntryLocked(deviceID, item); err != nil {
		manager.mu.Unlock()
		return vowifi.State{}, err
	}
	item.desiredEnabled = enabled
	if item.busy {
		manager.logger.Info(
			"VoWiFi desired state updated while lifecycle operation is active",
			"device_id", deviceID,
			"enabled", enabled,
		)
		// The switch is a desired-state control, not a one-shot command. A user
		// can change it again while a slow IKE/IMS transaction is still winding
		// down. Accept the newest value and let runOperations reconcile the
		// runtime after the current operation completes. Returning busy here used
		// to let the database and runtime diverge (configured on, runtime idle).
		if enabled {
			item.disablePending = false
		} else {
			item.disablePending = true
		}
		cancel := item.operationCancel
		state := item.orchestrator.State()
		manager.mu.Unlock()
		if !enabled && cancel != nil {
			cancel()
		}
		return state, nil
	}
	manager.mu.Unlock()
	return manager.startOperation(deviceID, false, func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
		if enabled {
			_, err := orchestrator.Enable(ctx)
			return err
		}
		_, err := orchestrator.Disable(ctx)
		return err
	})
}

func (manager *Manager) RequestReconnect(deviceID string) (vowifi.State, error) {
	item, err := manager.getOrEnsure(manager.ctx, deviceID)
	if err != nil {
		return vowifi.State{}, err
	}
	manager.mu.Lock()
	if err := manager.validateEntryLocked(deviceID, item); err != nil {
		manager.mu.Unlock()
		return vowifi.State{}, err
	}
	item.desiredEnabled = true
	manager.mu.Unlock()
	return manager.startOperation(deviceID, true, func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
		_, err := orchestrator.Reconnect(ctx)
		return err
	})
}

func (manager *Manager) SendSMS(
	ctx context.Context,
	deviceID string,
	request vowifi.SMSSubmitRequest,
) (vowifi.SMSSubmitResult, error) {
	item, err := manager.getOrEnsure(ctx, deviceID)
	if err != nil {
		return vowifi.SMSSubmitResult{}, err
	}
	manager.mu.Lock()
	if err := manager.validateEntryLocked(deviceID, item); err != nil {
		manager.mu.Unlock()
		return vowifi.SMSSubmitResult{}, err
	}
	manager.mu.Unlock()
	return item.orchestrator.SendSMS(ctx, request)
}

func (manager *Manager) Calls(deviceID string) ([]vowifi.Call, error) {
	item, err := manager.getOrEnsure(manager.ctx, deviceID)
	if err != nil {
		return nil, err
	}
	manager.mu.Lock()
	err = manager.validateEntryLocked(deviceID, item)
	manager.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return item.orchestrator.Calls()
}

func (manager *Manager) DialCall(ctx context.Context, deviceID, number string) (vowifi.Call, error) {
	item, err := manager.getOrEnsure(ctx, deviceID)
	if err != nil {
		return vowifi.Call{}, err
	}
	manager.mu.Lock()
	err = manager.validateEntryLocked(deviceID, item)
	manager.mu.Unlock()
	if err != nil {
		return vowifi.Call{}, err
	}
	return item.orchestrator.DialCall(ctx, number)
}

func (manager *Manager) AnswerCall(ctx context.Context, deviceID, id string) (vowifi.Call, error) {
	item, err := manager.getOrEnsure(ctx, deviceID)
	if err != nil {
		return vowifi.Call{}, err
	}
	manager.mu.Lock()
	err = manager.validateEntryLocked(deviceID, item)
	manager.mu.Unlock()
	if err != nil {
		return vowifi.Call{}, err
	}
	return item.orchestrator.AnswerCall(ctx, id)
}

func (manager *Manager) HangupCall(ctx context.Context, deviceID, id string) error {
	item, err := manager.getOrEnsure(ctx, deviceID)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	err = manager.validateEntryLocked(deviceID, item)
	manager.mu.Unlock()
	if err != nil {
		return err
	}
	return item.orchestrator.HangupCall(ctx, id)
}

func (manager *Manager) SendDTMF(
	ctx context.Context,
	deviceID string,
	id string,
	digit byte,
	duration time.Duration,
) error {
	item, err := manager.getOrEnsure(ctx, deviceID)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	err = manager.validateEntryLocked(deviceID, item)
	manager.mu.Unlock()
	if err != nil {
		return err
	}
	return item.orchestrator.SendDTMF(ctx, id, digit, duration)
}

func (manager *Manager) CallMedia(ctx context.Context, deviceID, id string) (vowifi.CallMedia, error) {
	item, err := manager.getOrEnsure(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	manager.mu.Lock()
	err = manager.validateEntryLocked(deviceID, item)
	manager.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return item.orchestrator.CallMedia(ctx, id)
}

func (manager *Manager) validateEntryLocked(deviceID string, item *entry) error {
	if manager.closed {
		return ErrClosed
	}
	if manager.subscriberChangeLocked(deviceID) {
		return ErrSubscriberChangeInProgress
	}
	if item == nil || manager.entries[deviceID] != item {
		return ErrNotRegistered
	}
	return nil
}

func (manager *Manager) startOperation(
	deviceID string,
	coalesceReconnect bool,
	operation func(context.Context, *vowifi.Orchestrator) error,
) (vowifi.State, error) {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return vowifi.State{}, ErrClosed
	}
	if manager.subscriberChangeLocked(deviceID) {
		manager.mu.Unlock()
		return vowifi.State{}, ErrSubscriberChangeInProgress
	}
	item := manager.entries[deviceID]
	if item == nil {
		manager.mu.Unlock()
		return vowifi.State{}, ErrNotRegistered
	}
	if item.busy {
		state := item.orchestrator.State()
		if coalesceReconnect {
			// Route changes and repeated reconnect clicks only need the latest
			// result. Keep one pending reconnect behind the active lifecycle
			// operation instead of rejecting the request or running two modem/
			// tunnel transactions concurrently.
			item.reconnectPending = true
			manager.mu.Unlock()
			return state, nil
		}
		manager.mu.Unlock()
		return state, ErrOperationInProgress
	}
	item.busy = true
	manager.wg.Add(1)
	manager.mu.Unlock()

	manager.logger.Debug("VoWiFi lifecycle operation queued", "device_id", deviceID)
	go manager.runOperations(deviceID, item, operation)
	return item.orchestrator.State(), nil
}

func (manager *Manager) runOperations(
	deviceID string,
	item *entry,
	operation func(context.Context, *vowifi.Orchestrator) error,
) {
	defer manager.wg.Done()
	manager.logger.Debug("VoWiFi lifecycle worker started", "device_id", deviceID)
	for {
		ctx, cancel := context.WithTimeout(manager.ctx, manager.operationTimeout)
		manager.mu.Lock()
		if item.disablePending {
			item.disablePending = false
			item.reconnectPending = false
			operation = func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
				_, err := orchestrator.Disable(ctx)
				return err
			}
		}
		item.operationCancel = cancel
		manager.mu.Unlock()
		manager.logger.Debug("VoWiFi lifecycle operation executing", "device_id", deviceID)
		err := operation(ctx, item.orchestrator)
		cancel()
		if err != nil &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, vowifi.ErrAlreadyEnabled) {
			manager.logger.Warn(
				"VoWiFi operation failed",
				"device_id", deviceID,
				"error", err,
			)
		}
		state := item.orchestrator.State()
		manager.mu.Lock()
		item.operationCancel = nil
		if manager.closed {
			item.busy = false
			manager.mu.Unlock()
			return
		}
		if item.disablePending {
			item.disablePending = false
			item.reconnectPending = false
			manager.mu.Unlock()
			operation = func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
				_, err := orchestrator.Disable(ctx)
				return err
			}
			continue
		}
		if item.reconnectPending {
			item.reconnectPending = false
			manager.mu.Unlock()

			// Read the route only when this runs. If the user bound, unbound,
			// then rebound while busy, this reconnect uses the final persisted
			// binding instead of replaying stale intermediate routes.
			operation = func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
				_, err := orchestrator.Reconnect(ctx)
				return err
			}
			continue
		}
		// Reconcile a switch change that arrived while the previous lifecycle
		// operation was busy. Keep using the same worker so enable/disable can
		// never overlap on the modem or tunnel resources.
		if item.desiredEnabled && !state.Enabled {
			manager.mu.Unlock()
			operation = func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
				_, err := orchestrator.Enable(ctx)
				return err
			}
			continue
		}
		if !item.desiredEnabled && state.Enabled {
			manager.mu.Unlock()
			operation = func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
				_, err := orchestrator.Disable(ctx)
				return err
			}
			continue
		}
		item.busy = false
		shouldRetry := item.desiredEnabled && (state.Phase == vowifi.PhaseFailed ||
			(state.Phase == vowifi.PhaseIdle && state.LastReason == "runtime_sim_identity_changed"))
		if !shouldRetry && state.Phase != vowifi.PhaseFailed {
			item.retryFailures = 0
		}
		manager.mu.Unlock()
		if shouldRetry {
			manager.scheduleAutoRetry(deviceID, item)
		}
		return
	}
}

func (manager *Manager) scheduleAutoRetry(deviceID string, item *entry) {
	manager.mu.Lock()
	if manager.closed || manager.subscriberChangeLocked(deviceID) ||
		manager.entries[deviceID] != item || item.busy || item.autoRetryPending ||
		!item.desiredEnabled {
		manager.mu.Unlock()
		return
	}
	delay := manager.retryInitial
	for attempt := uint(0); attempt < item.retryFailures && delay < manager.retryMaximum; attempt++ {
		if delay > manager.retryMaximum/2 {
			delay = manager.retryMaximum
			break
		}
		delay *= 2
	}
	if delay > manager.retryMaximum {
		delay = manager.retryMaximum
	}
	item.retryFailures++
	item.autoRetryPending = true
	manager.wg.Add(1)
	manager.mu.Unlock()

	manager.logger.Info(
		"VoWiFi automatic retry scheduled",
		"device_id", deviceID,
		"retry_in", delay,
	)
	go func() {
		defer manager.wg.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-manager.ctx.Done():
			return
		case <-timer.C:
		}

		manager.mu.Lock()
		item.autoRetryPending = false
		if manager.closed || manager.subscriberChangeLocked(deviceID) ||
			manager.entries[deviceID] != item || !item.desiredEnabled {
			manager.mu.Unlock()
			return
		}
		state := item.orchestrator.State()
		identityRebuild := state.Phase == vowifi.PhaseIdle &&
			state.LastReason == "runtime_sim_identity_changed"
		if state.Phase != vowifi.PhaseFailed && !identityRebuild {
			if state.Phase != vowifi.PhaseStopping {
				item.retryFailures = 0
			}
			manager.mu.Unlock()
			return
		}
		if item.busy {
			manager.mu.Unlock()
			return
		}
		item.busy = true
		manager.wg.Add(1)
		manager.mu.Unlock()

		go manager.runOperations(deviceID, item, func(ctx context.Context, orchestrator *vowifi.Orchestrator) error {
			var err error
			if identityRebuild {
				_, err = orchestrator.Enable(ctx)
			} else {
				_, err = orchestrator.Retry(ctx)
			}
			return err
		})
	}()
}

func (manager *Manager) watch(
	ctx context.Context,
	deviceID string,
	item *entry,
	states <-chan vowifi.State,
) {
	defer manager.wg.Done()
	defer close(item.watchDone)
	var lastPhase vowifi.Phase
	lastReason := ""
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case state, ok := <-states:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			manager.mu.Lock()
			current := !manager.closed && !manager.subscriberChangeLocked(deviceID) &&
				manager.entries[deviceID] == item
			manager.mu.Unlock()
			if !current {
				return
			}
			if state.Phase != lastPhase || state.LastReason != lastReason {
				manager.logger.Info("VoWiFi state transition",
					"category", "vowifi", "event", "state_transition",
					"device_id", deviceID, "phase", state.Phase,
					"sequence", state.Sequence, "attempt", state.Attempt,
					"enabled", state.Enabled, "active", state.Active,
					"reason", state.LastReason)
				lastPhase = state.Phase
				lastReason = state.LastReason
			}
			if state.Phase == vowifi.PhaseSIMReady {
				manager.logger.Info(
					"VoWiFi carrier profile resolved",
					"device_id", deviceID,
					"plmn", state.CarrierPLMN,
					"preset_id", state.CarrierPresetID,
					"profile_source", state.CarrierSource,
				)
			}
			if state.Phase == vowifi.PhaseAccessReady {
				manager.logger.Info(
					"VoWiFi access profile resolved",
					"device_id", deviceID,
					"epdg_source", state.EPDGSource,
					"identity_source", state.IMSIdentitySource,
				)
			}
			if state.Phase == vowifi.PhaseIMSReady {
				manager.logger.Info(
					"VoWiFi IMS profile confirmed",
					"device_id", deviceID,
					"preset_id", state.CarrierPresetID,
					"identity_source", state.IMSIdentitySource,
				)
			}
			if state.Phase == vowifi.PhaseFailed ||
				(state.Phase == vowifi.PhaseIdle && state.LastReason == "runtime_sim_identity_changed") {
				manager.scheduleAutoRetry(deviceID, item)
			} else if state.Phase == vowifi.PhaseSMSReady || !state.Enabled {
				manager.mu.Lock()
				if manager.entries[deviceID] == item && !manager.subscriberChangeLocked(deviceID) {
					item.retryFailures = 0
				}
				manager.mu.Unlock()
			}
			if manager.onState == nil {
				continue
			}
			persistContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := manager.onState(persistContext, state)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				manager.logger.Error(
					"persist VoWiFi state",
					"device_id", deviceID,
					"phase", state.Phase,
					"error", err,
				)
			}
		}
	}
}

func (manager *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	manager.cancel()
	items := make([]*entry, 0, len(manager.entries))
	for _, item := range manager.entries {
		items = append(items, item)
	}
	manager.mu.Unlock()

	var closeErrors []error
	for _, item := range items {
		if item.stopWatch != nil {
			item.stopWatch()
		}
		if err := item.orchestrator.Close(ctx); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}

	done := make(chan struct{})
	go func() {
		manager.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		closeErrors = append(closeErrors, ctx.Err())
	case <-done:
	}
	return errors.Join(closeErrors...)
}
