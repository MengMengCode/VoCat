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
	ErrNotRegistered       = errors.New("vowifi runtime: device is not registered")
	ErrOperationInProgress = errors.New("vowifi runtime: an operation is already in progress")
	ErrClosed              = errors.New("vowifi runtime: manager is closed")
)

const (
	defaultOperationTimeout = 2 * time.Minute
	defaultRetryInitial     = 2 * time.Second
	defaultRetryMaximum     = 30 * time.Second
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

	mu      sync.Mutex
	closed  bool
	entries map[string]*entry
	wg      sync.WaitGroup
}

type entry struct {
	orchestrator     *vowifi.Orchestrator
	maintenance      bool
	busy             bool
	reconnectPending bool
	disablePending   bool
	desiredEnabled   bool
	autoRetryPending bool
	retryFailures    uint
	operationCancel  context.CancelFunc
	stopWatch        func()
}

// BeginMaintenance temporarily suppresses background enable requests while a
// caller performs an exclusive SIM operation such as switching eSIM profiles.
// Disable requests remain allowed so the current runtime can release QMI/UIM.
func (manager *Manager) BeginMaintenance(deviceID string) error {
	if err := manager.Ensure(manager.ctx, deviceID); err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	item := manager.entries[deviceID]
	if item == nil {
		return ErrNotRegistered
	}
	item.maintenance = true
	return nil
}

// EndMaintenance re-enables ordinary desired-state reconciliation. The caller
// then applies the newly active profile's persisted policy.
func (manager *Manager) EndMaintenance(deviceID string) {
	manager.mu.Lock()
	if item := manager.entries[deviceID]; item != nil {
		item.maintenance = false
	}
	manager.mu.Unlock()
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
		ctx:              ctx,
		cancel:           cancel,
		logger:           options.Logger,
		operationTimeout: options.OperationTimeout,
		retryInitial:     options.RetryInitial,
		retryMaximum:     options.RetryMaximum,
		onState:          options.OnState,
		factory:          options.Factory,
		entries:          make(map[string]*entry),
	}
}

// Ensure registers a runtime for deviceID on demand. This keeps device
// configuration and runtime lifecycle in sync when a modem is added after the
// service has already started.
func (manager *Manager) Ensure(ctx context.Context, deviceID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrClosed
	}
	if _, exists := manager.entries[deviceID]; exists {
		manager.mu.Unlock()
		return nil
	}
	if manager.factory == nil {
		manager.mu.Unlock()
		return ErrNotRegistered
	}

	// The factory is called while holding the manager lock so concurrent status
	// and enable requests cannot create duplicate runtimes for the same device.
	orchestrator, err := manager.factory(ctx, deviceID)
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	if orchestrator == nil {
		manager.mu.Unlock()
		return errors.New("vowifi runtime: factory returned a nil orchestrator")
	}
	state := orchestrator.State()
	if state.DeviceID != deviceID {
		manager.mu.Unlock()
		_ = orchestrator.Close(context.Background())
		return fmt.Errorf(
			"vowifi runtime: factory returned device %q for %q",
			state.DeviceID,
			deviceID,
		)
	}
	states, stopWatch := orchestrator.Subscribe(8)
	manager.entries[deviceID] = &entry{
		orchestrator: orchestrator,
		stopWatch:    stopWatch,
	}
	manager.wg.Add(1)
	manager.mu.Unlock()

	go manager.watch(deviceID, states)
	return nil
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
	if manager.closed {
		manager.mu.Unlock()
		return ErrClosed
	}
	if _, exists := manager.entries[state.DeviceID]; exists {
		manager.mu.Unlock()
		return fmt.Errorf("vowifi runtime: device %q is already registered", state.DeviceID)
	}
	states, stopWatch := orchestrator.Subscribe(8)
	item := &entry{
		orchestrator: orchestrator,
		stopWatch:    stopWatch,
	}
	manager.entries[state.DeviceID] = item
	manager.wg.Add(1)
	manager.mu.Unlock()

	go manager.watch(state.DeviceID, states)
	return nil
}

func (manager *Manager) State(deviceID string) (vowifi.State, error) {
	manager.mu.Lock()
	item := manager.entries[deviceID]
	closed := manager.closed
	manager.mu.Unlock()
	if item == nil {
		if closed {
			return vowifi.State{}, ErrClosed
		}
		if err := manager.Ensure(manager.ctx, deviceID); err != nil {
			return vowifi.State{}, err
		}
		manager.mu.Lock()
		item = manager.entries[deviceID]
		manager.mu.Unlock()
	}
	return item.orchestrator.State(), nil
}

// ModemSMSSyncBlocked reports whether the desired VoWiFi lifecycle still owns
// the modem command path. The HTTP layer consults this signal for PhaseFailed,
// including the small handoff window before an automatic retry is scheduled.
func (manager *Manager) ModemSMSSyncBlocked(deviceID string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	item := manager.entries[deviceID]
	return item != nil && (item.busy || item.autoRetryPending)
}

// RequestEnabled queues an enable or disable transaction and returns
// immediately. Callers observe progress through State; provider errors are
// persisted in the orchestrator state instead of being lost with an HTTP
// request context.
func (manager *Manager) RequestEnabled(deviceID string, enabled bool) (vowifi.State, error) {
	if err := manager.Ensure(manager.ctx, deviceID); err != nil {
		return vowifi.State{}, err
	}
	manager.mu.Lock()
	item := manager.entries[deviceID]
	if item.maintenance && enabled {
		state := item.orchestrator.State()
		manager.mu.Unlock()
		return state, nil
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
	if err := manager.Ensure(manager.ctx, deviceID); err != nil {
		return vowifi.State{}, err
	}
	manager.mu.Lock()
	if item := manager.entries[deviceID]; item != nil {
		item.desiredEnabled = true
	}
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
	if err := manager.Ensure(ctx, deviceID); err != nil {
		return vowifi.SMSSubmitResult{}, err
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return vowifi.SMSSubmitResult{}, ErrClosed
	}
	item := manager.entries[deviceID]
	manager.mu.Unlock()
	if item == nil {
		return vowifi.SMSSubmitResult{}, ErrNotRegistered
	}
	return item.orchestrator.SendSMS(ctx, request)
}

func (manager *Manager) SendUSSI(
	ctx context.Context,
	deviceID string,
	request vowifi.USSISubmitRequest,
) (vowifi.USSISubmitResult, error) {
	if err := manager.Ensure(ctx, deviceID); err != nil {
		return vowifi.USSISubmitResult{}, err
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return vowifi.USSISubmitResult{}, ErrClosed
	}
	item := manager.entries[deviceID]
	manager.mu.Unlock()
	if item == nil {
		return vowifi.USSISubmitResult{}, ErrNotRegistered
	}
	return item.orchestrator.SendUSSI(ctx, request)
}

func (manager *Manager) Calls(deviceID string) ([]vowifi.Call, error) {
	if err := manager.Ensure(manager.ctx, deviceID); err != nil {
		return nil, err
	}
	manager.mu.Lock()
	item := manager.entries[deviceID]
	manager.mu.Unlock()
	if item == nil {
		return nil, ErrNotRegistered
	}
	return item.orchestrator.Calls()
}

func (manager *Manager) DialCall(ctx context.Context, deviceID, number string) (vowifi.Call, error) {
	if err := manager.Ensure(ctx, deviceID); err != nil {
		return vowifi.Call{}, err
	}
	manager.mu.Lock()
	item := manager.entries[deviceID]
	manager.mu.Unlock()
	if item == nil {
		return vowifi.Call{}, ErrNotRegistered
	}
	return item.orchestrator.DialCall(ctx, number)
}

func (manager *Manager) AnswerCall(ctx context.Context, deviceID, id string) (vowifi.Call, error) {
	if err := manager.Ensure(ctx, deviceID); err != nil {
		return vowifi.Call{}, err
	}
	manager.mu.Lock()
	item := manager.entries[deviceID]
	manager.mu.Unlock()
	if item == nil {
		return vowifi.Call{}, ErrNotRegistered
	}
	return item.orchestrator.AnswerCall(ctx, id)
}

func (manager *Manager) HangupCall(ctx context.Context, deviceID, id string) error {
	if err := manager.Ensure(ctx, deviceID); err != nil {
		return err
	}
	manager.mu.Lock()
	item := manager.entries[deviceID]
	manager.mu.Unlock()
	if item == nil {
		return ErrNotRegistered
	}
	return item.orchestrator.HangupCall(ctx, id)
}

func (manager *Manager) CallMedia(ctx context.Context, deviceID, id string) (vowifi.CallMedia, error) {
	if err := manager.Ensure(ctx, deviceID); err != nil {
		return nil, err
	}
	manager.mu.Lock()
	item := manager.entries[deviceID]
	manager.mu.Unlock()
	if item == nil {
		return nil, ErrNotRegistered
	}
	return item.orchestrator.CallMedia(ctx, id)
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
		shouldRetry := item.desiredEnabled && state.Phase == vowifi.PhaseFailed
		if !shouldRetry && state.Phase != vowifi.PhaseFailed {
			item.retryFailures = 0
		}
		if shouldRetry {
			// Mark the retry pending before releasing the lifecycle lock. This
			// closes the handoff window in which AT+CMGL could otherwise start
			// after busy becomes false but before the retry is visible.
			manager.scheduleAutoRetryLocked(deviceID, item)
		}
		manager.mu.Unlock()
		return
	}
}

func (manager *Manager) scheduleAutoRetry(deviceID string, item *entry) {
	manager.mu.Lock()
	manager.scheduleAutoRetryLocked(deviceID, item)
	manager.mu.Unlock()
}

func (manager *Manager) scheduleAutoRetryLocked(deviceID string, item *entry) {
	if manager.closed || item.busy || item.autoRetryPending || !item.desiredEnabled {
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
		if manager.closed || manager.entries[deviceID] != item || !item.desiredEnabled {
			manager.mu.Unlock()
			return
		}
		state := item.orchestrator.State()
		if state.Phase != vowifi.PhaseFailed {
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
			_, err := orchestrator.Retry(ctx)
			return err
		})
	}()
}

func (manager *Manager) watch(deviceID string, states <-chan vowifi.State) {
	defer manager.wg.Done()
	for {
		select {
		case <-manager.ctx.Done():
			return
		case state, ok := <-states:
			if !ok {
				return
			}
			if state.Phase == vowifi.PhaseFailed {
				manager.mu.Lock()
				item := manager.entries[deviceID]
				manager.mu.Unlock()
				if item != nil {
					manager.scheduleAutoRetry(deviceID, item)
				}
			} else if state.Phase == vowifi.PhaseSMSReady || !state.Enabled {
				manager.mu.Lock()
				if item := manager.entries[deviceID]; item != nil {
					item.retryFailures = 0
				}
				manager.mu.Unlock()
			}
			if manager.onState == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(manager.ctx, 5*time.Second)
			err := manager.onState(ctx, state)
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
