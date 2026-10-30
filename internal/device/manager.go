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
	"vocat/internal/remoteproc"
)

type Options struct {
	Discoverer     modem.Discoverer
	Opener         modem.Opener
	CommandTimeout time.Duration
	LongTimeout    time.Duration
	SMSTimeout     time.Duration
	ScanTimeout    time.Duration
	CardReaders    *pcsc.Service
	Logger         *slog.Logger
	// MBNProfileForICCID returns a stored per-card MBN override. An empty
	// result keeps the HPLMN heuristic used after eSIM profile switches.
	MBNProfileForICCID func(context.Context, string) (string, error)
}

type Manager struct {
	mu                 sync.RWMutex
	uiccMu             sync.Mutex // serializes all multi-command UICC/APDU transactions
	esimMu             sync.Mutex // serializes eSIM card access (list/switch/download)
	esimRecoveryMu     sync.Mutex
	esimRecoveries     map[string]*esimRecovery
	esimLastRecoveries map[string]*esimRecovery
	esimSwitchLeases   map[string]*esimSwitchLease
	esimSwitchPending  map[string]struct{}
	esimCacheMu        sync.RWMutex
	esimCache          map[string]EsimInfo
	discoverer         modem.Discoverer
	opener             modem.Opener
	commandTimeout     time.Duration
	longTimeout        time.Duration
	smsTimeout         time.Duration
	scanTimeout        time.Duration
	cardReaders        *pcsc.Service
	logger             *slog.Logger
	mbnProfileForICCID func(context.Context, string) (string, error)

	networkEventsMu         sync.Mutex
	networkEventSubscribers map[chan string]struct{}
	deviceEventsMu          sync.Mutex
	deviceEventSubscribers  map[chan DeviceLifecycleEvent]struct{}
	smsArrivals             chan struct{}

	qmiRadioOpener                qmiRadioSessionOpener
	remoteprocObserver            func(string) (remoteproc.Observation, error)
	qmiDataOpener                 qmiDataSessionOpener
	nativeQMIRegistrationMu       sync.Mutex
	nativeQMIRegistrationInFlight map[string]struct{}
	nativeQMISIMRecoveryMu        sync.Mutex
	nativeQMISIMRecoveries        map[string]time.Time

	started      bool
	devices      map[string]*managedDevice
	ussdSessions map[string]ussdSession
}

// LockUICC and UnlockUICC allow another in-process UICC client (currently the
// VoWiFi AKA adapter) to share the same transaction boundary as eSIM ES10.
// Individual AT commands are already serialized per modem, but a logical-
// channel transaction spans several commands and must not be interleaved.
func (manager *Manager) LockUICC()   { manager.uiccMu.Lock() }
func (manager *Manager) UnlockUICC() { manager.uiccMu.Unlock() }

func (manager *Manager) lockESIM() {
	manager.esimMu.Lock()
	manager.uiccMu.Lock()
}

// lockESIMContext keeps HTTP eSIM reads cancellable when another modem
// operation is slow. A plain Mutex.Lock here used to leave the eSIM page
// spinning forever behind a wedged refresh transaction.
func (manager *Manager) lockESIMContext(ctx context.Context) error {
	if err := lockMutexContext(ctx, &manager.esimMu); err != nil {
		return err
	}
	if err := lockMutexContext(ctx, &manager.uiccMu); err != nil {
		manager.esimMu.Unlock()
		return err
	}
	return nil
}

func lockMutexContext(ctx context.Context, mutex *sync.Mutex) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if mutex.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (manager *Manager) unlockESIM() {
	manager.uiccMu.Unlock()
	manager.esimMu.Unlock()
}

// ussdSession tracks an open USSD dialog on a device so a follow-up Continue or
// Cancel can be routed back to the right modem. The modem owns the actual
// network session; this map only records which device a session id belongs to.
type ussdSession struct {
	deviceID  string
	createdAt time.Time
}

type managedDevice struct {
	profileSwitchMu      sync.RWMutex // excludes ordinary snapshot/auto hot-swap work during a profile transaction
	opMu                 sync.Mutex
	dataMu               sync.Mutex
	dataSession          qmiDataSession
	dataSessionHandle    uint32
	dataSessionControl   string
	dataEventCancel      context.CancelFunc
	candidate            modem.Candidate
	atProfile            modem.ATProfile
	apduDialect          *modem.ATAPDUDialect
	profileVendorID      string
	profileProductID     string
	backend              string
	esimTransport        string
	lastICCID            string
	client               modem.Client
	snapshot             *Snapshot
	lastError            string
	lastUpdated          time.Time
	discovered           bool
	preFlightMode        *int
	resetClientOnLock    bool
	remoteprocOwner      string
	remoteprocGeneration uint64
	resetATProfileOnLock bool
	simPIN               string
}

func NewManager(options Options) (*Manager, error) {
	if options.Discoverer == nil {
		options.Discoverer = modem.NewSystemDiscoverer()
	}
	smsArrivals := make(chan struct{}, 1)
	if options.Opener == nil {
		options.Opener = modem.SerialOpener{
			SessionOptions: modem.SessionOptions{OnURC: signalSMSArrival(smsArrivals)},
		}
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
	if options.CardReaders == nil {
		options.CardReaders = pcsc.New()
	}
	return &Manager{
		discoverer:         options.Discoverer,
		opener:             options.Opener,
		commandTimeout:     options.CommandTimeout,
		longTimeout:        options.LongTimeout,
		smsTimeout:         options.SMSTimeout,
		scanTimeout:        options.ScanTimeout,
		cardReaders:        options.CardReaders,
		logger:             options.Logger,
		mbnProfileForICCID: options.MBNProfileForICCID,

		qmiRadioOpener:                openQMIRadioSession,
		qmiDataOpener:                 openQMIDataSession,
		nativeQMIRegistrationInFlight: make(map[string]struct{}),
		networkEventSubscribers:       make(map[chan string]struct{}),
		deviceEventSubscribers:        make(map[chan DeviceLifecycleEvent]struct{}),
		smsArrivals:                   smsArrivals,

		devices:        make(map[string]*managedDevice),
		ussdSessions:   make(map[string]ussdSession),
		esimRecoveries: make(map[string]*esimRecovery),
		esimCache:      make(map[string]EsimInfo),
	}, nil
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
		state.dataMu.Lock()
		invalidateQMINetworkSession(state, manager.candidateFor(state))
		state.dataMu.Unlock()
	}
	return errors.Join(closeErrors...)
}

func (manager *Manager) Discover(ctx context.Context) ([]Device, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates, modemErr := manager.discoverer.Discover(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errors.Is(modemErr, context.Canceled) || errors.Is(modemErr, context.DeadlineExceeded) {
		return nil, modemErr
	}
	readers, readerErr := manager.cardReaders.Readers(ctx)
	if readerErr == nil {
		for _, reader := range readers {
			candidates = append(candidates, modem.Candidate{
				ID: pcsc.DeviceID(reader), HardwareKind: pcsc.HardwareKind,
				ReaderName: reader.Name, USBPath: reader.USBPath,
				VendorID: reader.VendorID, ProductID: reader.ProductID,
				Manufacturer: reader.Manufacturer, Product: reader.Product,
				DiscoveryIssue: reader.DiscoveryIssue,
			})
		}
	}
	if modemErr != nil && readerErr != nil && !errors.Is(readerErr, pcsc.ErrUnsupported) && !errors.Is(readerErr, pcsc.ErrUnavailable) {
		return nil, errors.Join(modemErr, readerErr)
	}
	if modemErr != nil && len(candidates) == 0 {
		return nil, modemErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(candidates))

	type discoveryEvent struct {
		connected bool
		candidate modem.Candidate
	}
	events := make([]discoveryEvent, 0)
	manager.mu.Lock()
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" {
			continue
		}
		state := manager.devices[candidate.ID]
		if state != nil && state.discovered {
			candidate = preserveVerifiedATPort(state.candidate, candidate)
		}
		seen[candidate.ID] = struct{}{}
		if state == nil {
			manager.devices[candidate.ID] = &managedDevice{
				candidate:        candidate,
				atProfile:        modem.ResolveATProfile(candidate),
				apduDialect:      &modem.ATAPDUDialect{},
				profileVendorID:  strings.ToLower(strings.TrimSpace(candidate.VendorID)),
				profileProductID: strings.ToLower(strings.TrimSpace(candidate.ProductID)),
				discovered:       true,
			}
			events = append(events, discoveryEvent{connected: true, candidate: candidate})
			continue
		}
		reconnected := !state.discovered
		usbIdentityChanged := !sameUSBIdentity(state.candidate, candidate)
		endpointChanged := usbIdentityChanged || state.candidate.USBGeneration != candidate.USBGeneration ||
			state.candidate.ATPort.OpenPath() != candidate.ATPort.OpenPath() ||
			state.candidate.QMIControl != candidate.QMIControl
		if reconnected {
			events = append(events, discoveryEvent{connected: true, candidate: candidate})
		} else if endpointChanged {
			// A brief USB reset can disappear and reappear entirely between two
			// discovery passes. The Linux device number still changes, so publish
			// a synthetic disconnect/connect pair to restart dependent runtimes.
			events = append(events,
				discoveryEvent{candidate: state.candidate},
				discoveryEvent{connected: true, candidate: candidate},
			)
		}
		if reconnected || endpointChanged {
			state.resetClientOnLock = true
		}
		if reconnected || usbIdentityChanged {
			state.resetATProfileOnLock = true
		}
		state.candidate = candidate
		state.discovered = true
	}
	var stale []*managedDevice
	for id, state := range manager.devices {
		if _, ok := seen[id]; ok {
			continue
		}
		if state.discovered {
			events = append(events, discoveryEvent{candidate: state.candidate})
		}
		state.discovered = false
		stale = append(stale, state)
	}
	manager.mu.Unlock()
	if manager.logger != nil {
		for _, event := range events {
			message := "hardware disconnected"
			if event.connected {
				message = "hardware connected"
			}
			manager.logger.Info(message,
				"event", "hardware.discovery",
				"device_id", event.candidate.ID,
				"hardware_kind", event.candidate.HardwareKind,
				"vendor_id", event.candidate.VendorID,
				"product_id", event.candidate.ProductID,
			)
		}
	}
	for _, event := range events {
		manager.publishDeviceLifecycleEvent(DeviceLifecycleEvent{
			ID:      event.candidate.ID,
			Present: event.connected,
		})
	}

	for _, state := range stale {
		state.opMu.Lock()
		if state.client != nil {
			_ = state.client.Close()
			state.client = nil
		}
		state.opMu.Unlock()
		state.dataMu.Lock()
		invalidateQMINetworkSession(state, manager.candidateFor(state))
		state.dataMu.Unlock()
	}
	manager.resetChangedClients()

	// List retains previously discovered devices so configured hardware can be
	// rendered as offline after it is unplugged. Discover, however, is a fresh
	// physical scan and must only return devices that are present now. Returning
	// the retained entries here allowed an unplugged modem to be selected and
	// added again from the device discovery screen.
	devices := manager.List()
	present := devices[:0]
	for _, entry := range devices {
		if entry.Discovered {
			present = append(present, entry)
		}
	}
	return present, nil
}

// WaitForStableModem prevents cold-boot consumers from opening an EC20 during
// its provisional first enumeration. Some modules enumerate, reset once while
// their baseband finishes booting, and then reappear with the same tty names.
// Once any usable modem has appeared, its complete endpoint signature must stay
// unchanged for stableFor before startup proceeds. A host with no modem still
// starts after initialWait so the management UI remains available.
func (manager *Manager) WaitForStableModem(
	ctx context.Context,
	stableFor time.Duration,
	pollInterval time.Duration,
	initialWait time.Duration,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if stableFor <= 0 {
		return nil
	}
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	if initialWait < 0 {
		initialWait = 0
	}
	started := time.Now()
	everSeen := false
	stableSince := time.Time{}
	stableSignature := ""
	for {
		devices, err := manager.Discover(ctx)
		if err != nil && ctx.Err() == nil {
			stableSince = time.Time{}
			stableSignature = ""
		} else if err != nil {
			return err
		} else {
			signature := stableModemSignature(devices)
			if signature == "" {
				stableSince = time.Time{}
				stableSignature = ""
			} else {
				everSeen = true
				if signature != stableSignature {
					stableSignature = signature
					stableSince = time.Now()
				} else if time.Since(stableSince) >= stableFor {
					return nil
				}
			}
		}
		if !everSeen && time.Since(started) >= initialWait {
			return nil
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func stableModemSignature(devices []Device) string {
	parts := make([]string, 0, len(devices))
	for _, entry := range devices {
		candidate := entry.Candidate
		if !entry.Discovered || candidate.HardwareKind == pcsc.HardwareKind ||
			candidate.DiscoveryIssue != "" || !candidate.HasATPort() {
			continue
		}
		parts = append(parts, strings.Join([]string{
			entry.ID,
			candidate.USBGeneration,
			candidate.ATPort.OpenPath(),
			candidate.QMIControl,
		}, "|"))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func (manager *Manager) resetChangedClients() {
	type pendingReset struct {
		state        *managedDevice
		resetProfile bool
	}
	manager.mu.Lock()
	states := make([]pendingReset, 0, len(manager.devices))
	for _, state := range manager.devices {
		if state.resetClientOnLock {
			states = append(states, pendingReset{state: state, resetProfile: state.resetATProfileOnLock})
			state.resetClientOnLock = false
			state.resetATProfileOnLock = false
		}
	}
	manager.mu.Unlock()
	for _, pending := range states {
		state := pending.state
		state.opMu.Lock()
		if state.client != nil {
			_ = state.client.Close()
			state.client = nil
		}
		candidate := manager.candidateFor(state)
		if pending.resetProfile || state.apduDialect == nil || state.atProfile.APDU == nil ||
			state.profileVendorID != strings.ToLower(strings.TrimSpace(candidate.VendorID)) ||
			state.profileProductID != strings.ToLower(strings.TrimSpace(candidate.ProductID)) {
			setATProfileLocked(state, candidate)
		}
		state.opMu.Unlock()
		state.dataMu.Lock()
		invalidateQMINetworkSession(state, manager.candidateFor(state))
		state.dataMu.Unlock()
	}
}

func sameUSBIdentity(left, right modem.Candidate) bool {
	return strings.EqualFold(strings.TrimSpace(left.VendorID), strings.TrimSpace(right.VendorID)) &&
		strings.EqualFold(strings.TrimSpace(left.ProductID), strings.TrimSpace(right.ProductID))
}

// A busy/deferred probe is not a new device observation. Keep only the
// verified port on the same enumeration; fresh USB metadata must still reach
// endpointChanged so a reset cannot retain old AT/WDS file descriptors.
func preserveVerifiedATPort(previous, current modem.Candidate) modem.Candidate {
	if (!current.ATProbeBusy && !current.ATProbeDeferred) || previous.ATProbeRequired ||
		!previous.HasATPort() || !sameUSBIdentity(previous, current) || previous.USBGeneration != current.USBGeneration {
		return current
	}
	ports := current.Ports
	if len(ports) == 0 && current.HasATPort() {
		ports = []modem.Port{current.ATPort}
	}
	for _, port := range ports {
		if port.OpenPath() != previous.ATPort.OpenPath() && (port.Name == "" || port.Name != previous.ATPort.Name) {
			continue
		}
		port.Role = previous.ATPort.Role
		current.ATPort = port
		current.ATProbeRequired = false
		if current.DiscoveryIssue == "at_port_missing" || current.DiscoveryIssue == "at_probe_pending" {
			current.DiscoveryIssue = previous.DiscoveryIssue
		}
		break
	}
	return current
}

func setATProfileLocked(state *managedDevice, candidate modem.Candidate) {
	state.atProfile = modem.ResolveATProfile(candidate)
	state.apduDialect = &modem.ATAPDUDialect{}
	state.profileVendorID = strings.ToLower(strings.TrimSpace(candidate.VendorID))
	state.profileProductID = strings.ToLower(strings.TrimSpace(candidate.ProductID))
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
	observe := manager.remoteprocObserver
	if observe == nil {
		observe = remoteproc.CheckPort
	}
	observation, healthErr := observe(candidate.ATPort.OpenPath())
	if healthErr != nil {
		if state.client != nil {
			_ = state.client.Close()
			state.client = nil
		}
		manager.mu.Lock()
		if state.snapshot != nil {
			state.snapshot.Responsive = false
		}
		manager.mu.Unlock()
		return nil, healthErr
	}
	if observation.Path != "" && (state.remoteprocOwner != observation.Path || state.remoteprocGeneration != observation.Generation) {
		if state.client != nil {
			_ = state.client.Close()
			state.client = nil
		}
		state.preFlightMode = nil
		state.remoteprocOwner = observation.Path
		state.remoteprocGeneration = observation.Generation
	}
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
		return nil, err
	}
	state.client = client
	return client, nil
}

func (manager *Manager) setResult(
	id string,
	state *managedDevice,
	snapshot *Snapshot,
	err error,
) {
	manager.mu.Lock()
	if manager.devices[id] != state {
		manager.mu.Unlock()
		return
	}
	previousError := state.lastError
	var previousSnapshot *Snapshot
	if state.snapshot != nil {
		value := *state.snapshot
		previousSnapshot = &value
	}
	if snapshot != nil {
		value := *snapshot
		value.Warnings = append([]string(nil), snapshot.Warnings...)
		state.snapshot = &value
		state.lastUpdated = snapshot.UpdatedAt
	}
	if err != nil {
		state.lastError = err.Error()
	} else {
		state.lastError = ""
	}
	shouldLog := err != nil && manager.logger != nil && previousError != err.Error()
	registrationChanged := snapshot != nil && manager.logger != nil &&
		(previousSnapshot == nil ||
			previousSnapshot.RegistrationStatus != snapshot.RegistrationStatus ||
			previousSnapshot.OperatorCode != snapshot.OperatorCode ||
			previousSnapshot.AccessTech != snapshot.AccessTech ||
			previousSnapshot.PSAttached != snapshot.PSAttached ||
			previousSnapshot.SIMStatus != snapshot.SIMStatus)
	backend := state.backend
	hardwareKind := state.candidate.HardwareKind
	manager.mu.Unlock()
	if shouldLog {
		manager.logger.Warn(
			"hardware operation failed",
			"device_id", id,
			"backend", backend,
			"hardware_kind", hardwareKind,
			"error", HardwareErrorDetail(err),
		)
	}
	if registrationChanged {
		manager.logger.Info(
			"cellular registration state changed",
			"category", "network",
			"event", "network.registration",
			"device_id", id,
			"sim_status", snapshot.SIMStatus,
			"registration_status", snapshot.RegistrationStatus,
			"registration_source", snapshot.RegistrationSource,
			"operator", snapshot.OperatorName,
			"operator_code", snapshot.OperatorCode,
			"access_technology", snapshot.AccessTech,
			"packet_service_attached", snapshot.PSAttached,
		)
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
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := manager.lookup(id)
	if err != nil {
		return Snapshot{}, err
	}
	// A snapshot can perform its own SIM power-cycle. It must not compete
	// with a committed eSIM REFRESH/reset, including detached recovery after
	// the initiating request has already failed. Only the transaction's own
	// bounded verification/refresh context may cross this maintenance gate.
	unlockProfileRead, err := manager.lockProfileSwitchRead(ctx, id, state)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlockProfileRead()
	if err := lockMutexContext(ctx, &state.opMu); err != nil {
		return Snapshot{}, err
	}
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return Snapshot{}, err
	}
	candidate := manager.candidateFor(state)
	if candidate.HardwareKind == pcsc.HardwareKind {
		return manager.refreshCardReader(ctx, id, state, candidate)
	}
	backend := manager.backendFor(state)
	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return Snapshot{}, err
	}
	previousICCID := state.lastICCID
	var previousSnapshot *Snapshot
	manager.mu.RLock()
	if state.snapshot != nil {
		copy := *state.snapshot
		previousSnapshot = &copy
	}
	manager.mu.RUnlock()
	snapshot, err := manager.readSnapshotWithOperatorProfile(
		ctx, id, candidate, backend, previousICCID, previousSnapshot, client,
		operatorProfileForState(state, candidate),
	)
	if err == nil && strings.TrimSpace(snapshot.ICCID) != "" {
		state.lastICCID = strings.TrimSpace(snapshot.ICCID)
	}
	manager.setResult(id, state, &snapshot, err)
	return snapshot, err
}

func (manager *Manager) refreshCardReader(ctx context.Context, id string, state *managedDevice, candidate modem.Candidate) (Snapshot, error) {
	result := Snapshot{
		DeviceID: id, Port: candidate.ReaderName, Responsive: true,
		Manufacturer: candidate.Manufacturer, Model: candidate.Product,
		AccessTech: "Wi-Fi", RegistrationSource: "pcsc", OperatingMode: 4,
		ModeKnown: true, FlightMode: true, RadioOff: true, UpdatedAt: time.Now().UTC(),
	}
	previousICCID := state.lastICCID
	card, err := manager.cardReaders.Snapshot(ctx, pcsc.Selector{USBPath: candidate.USBPath, ReaderName: candidate.ReaderName}, state.simPIN)
	if err != nil {
		switch {
		case errors.Is(err, pcsc.ErrNoCard):
			result.SIMStatus = ""
			err = nil
		case errors.Is(err, pcsc.ErrPINRequired), errors.Is(err, pcsc.ErrPINTriesLow), errors.Is(err, pcsc.ErrPINRejected):
			result.SIMStatus = "SIM PIN"
			result.Warnings = []string{err.Error()}
			err = nil
		default:
			manager.setResult(id, state, &result, err)
			return result, err
		}
	} else {
		result.SIMStatus = "READY"
		result.SIMReady = true
		result.ICCID = card.Identity.ICCID
		result.IMSI = card.Identity.IMSI
		result.SPN = card.Identity.SPN
		result.MNCLength = card.Identity.MNCLength
		result.SIMChanged = previousICCID != "" && !strings.EqualFold(previousICCID, result.ICCID)
		state.lastICCID = result.ICCID
	}
	manager.setResult(id, state, &result, err)
	return result, err
}

// SetSIMPin updates the in-memory PIN used for protected USIM files and AKA.
// It is deliberately never retained in runtime snapshots or logs.
func (manager *Manager) SetSIMPin(id, pin string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.devices[id]
	if state == nil || !state.discovered {
		return ErrNotFound
	}
	state.simPIN = strings.TrimSpace(pin)
	return nil
}

// SetBackend selects which control plane supplies registration and data state.
// AT remains available in either mode for UICC, RF, SMS, voice and diagnostics.
func (manager *Manager) SetBackend(id, backend string) error {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend != "at" && backend != "qmi" && backend != "pcsc" {
		return fmt.Errorf("unsupported device backend %q", backend)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.devices[id]
	if state == nil || !state.discovered {
		return ErrNotFound
	}
	state.backend = backend
	return nil
}

func (manager *Manager) backendFor(state *managedDevice) string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return state.backend
}

// SetESIMTransport selects the control path used for eUICC APDU operations.
// It is intentionally independent from the registration/data backend: an EC20
// can use QMI for cellular state while using AT+CSIM for its eUICC.
// An empty value clears the override and falls back to the selected backend.
func (manager *Manager) SetESIMTransport(id, transport string) error {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport != "" && transport != "at" && transport != "qmi" && transport != "pcsc" && transport != "none" {
		return fmt.Errorf("unsupported eSIM transport %q", transport)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.devices[id]
	if state == nil || !state.discovered {
		return ErrNotFound
	}
	state.esimTransport = transport
	return nil
}

func (manager *Manager) esimTransportFor(state *managedDevice) string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if state.esimTransport != "" {
		return state.esimTransport
	}
	return state.backend
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
	if err := lockMutexContext(ctx, &state.opMu); err != nil {
		return modem.Response{}, err
	}
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
	// An unsupported CLCC query does not indicate an unhealthy control channel:
	// 3GPP "operation not supported" (ERROR: 4) and the ZXIC vendor
	// "command not recognized" (6003) are both expected on firmware without a
	// call-list implementation.
	var commandErr *modem.CommandError
	final := ""
	if errors.As(err, &commandErr) {
		final = strings.TrimSpace(commandErr.Final)
	}
	if !strings.EqualFold(strings.TrimSpace(command), "AT+CLCC") ||
		final != "+CME ERROR: 4" && final != "+CME ERROR: 6003" {
		manager.setResult(id, state, nil, err)
	}
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

// ExecuteAPDU sends one APDU through the device-owned profile and learned
// command dialect. The payload always stays inside the sensitive AT boundary.
func (manager *Manager) ExecuteAPDU(
	ctx context.Context,
	id string,
	command modem.APDUCommand,
	channel int,
	apdu []byte,
) ([]byte, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := lockMutexContext(ctx, &state.opMu); err != nil {
		return nil, err
	}
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return nil, err
	}
	candidate := manager.candidateFor(state)
	if state.apduDialect == nil || state.atProfile.APDU == nil ||
		state.profileVendorID != strings.ToLower(strings.TrimSpace(candidate.VendorID)) ||
		state.profileProductID != strings.ToLower(strings.TrimSpace(candidate.ProductID)) {
		setATProfileLocked(state, candidate)
	}
	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		err = sanitizeManagerAPDUError(err)
		manager.setResult(id, state, nil, err)
		return nil, err
	}
	return manager.executeAPDULocked(ctx, id, state, client, command, channel, apdu)
}

// executeAPDULocked requires state.opMu and reuses the Manager's single
// per-device profile and dialect for eSIM and VoWiFi callers.
func (manager *Manager) executeAPDULocked(
	ctx context.Context,
	id string,
	state *managedDevice,
	client modem.Client,
	command modem.APDUCommand,
	channel int,
	apdu []byte,
) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	candidate := manager.candidateFor(state)
	if state.apduDialect == nil || state.atProfile.APDU == nil ||
		state.profileVendorID != strings.ToLower(strings.TrimSpace(candidate.VendorID)) ||
		state.profileProductID != strings.ToLower(strings.TrimSpace(candidate.ProductID)) {
		setATProfileLocked(state, candidate)
	}
	apduCtx, cancel := manager.withTimeout(ctx, csimAPDUTimeout)
	defer cancel()
	result, err := state.apduDialect.Execute(
		apduCtx,
		state.atProfile.APDU,
		command,
		channel,
		apdu,
		func(commandCtx context.Context, commandText string) (modem.Response, error) {
			return manager.sensitiveAPDUCommand(commandCtx, client, commandText)
		},
	)
	if err != nil {
		err = sanitizeManagerAPDUError(err)
	}
	manager.setResult(id, state, nil, err)
	return result, err
}

// sensitiveAPDUCommand is the sensitive boundary for APDUs. Unlike the
// generic helper it lets the dialect inspect a definite modem rejection; the
// dialect sanitizes every result before it can reach callers or device state.
func (manager *Manager) sensitiveAPDUCommand(
	ctx context.Context,
	client modem.Client,
	command string,
) (modem.Response, error) {
	commandCtx, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	defer cancel()
	return client.Execute(commandCtx, command)
}

func sanitizeManagerAPDUError(err error) error {
	if err == nil {
		return nil
	}
	var rejected *modem.APDUCommandRejectedError
	if errors.As(err, &rejected) {
		return rejected
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, modem.ErrCommandTimeout):
		return modem.ErrCommandTimeout
	case errors.Is(err, modem.ErrSessionClosed):
		return modem.ErrSessionClosed
	case errors.Is(err, modem.ErrATPortBusy):
		return modem.ErrATPortBusy
	case errors.Is(err, ErrNoATPort):
		return ErrNoATPort
	case errors.Is(err, modem.ErrAPDUCommandRejected):
		return modem.ErrAPDUCommandRejected
	case errors.Is(err, modem.ErrAPDUResponseInvalid):
		return modem.ErrAPDUResponseInvalid
	case errors.Is(err, modem.ErrAPDUExecutionFailed):
		return modem.ErrAPDUExecutionFailed
	default:
		return modem.ErrAPDUExecutionFailed
	}
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
	if err := manager.requireOnlineModemRestart(ctx, client); err != nil {
		manager.setResult(id, state, nil, err)
		return err
	}
	state.dataMu.Lock()
	invalidateQMINetworkSession(state, manager.candidateFor(state))
	state.dataMu.Unlock()
	commandCtx, cancel := manager.withTimeout(ctx, manager.longTimeout)
	defer cancel()
	_, err = client.Execute(commandCtx, "AT+CFUN=1,1")
	if !isCommandInterrupted(err) {
		if closeErr := client.Close(); err == nil {
			err = closeErr
		}
		state.client = nil
	}
	state.preFlightMode = nil
	manager.clearSnapshot(id, state)
	manager.setResult(id, state, nil, err)
	return err
}

// Interrupted commands still own their late replies; reopening the port would
// let those replies complete an unrelated command in the new session.
func isCommandInterrupted(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, modem.ErrCommandTimeout)
}

// softResetForProfileSwitch resets the baseband SIM stack using a soft CFUN sequence
// (AT+CFUN=0 -> AT+CFUN=4) instead of rebooting the entire hardware module (AT+CFUN=1,1).
// This causes the baseband to reload the new eSIM profile files within ~1-2 seconds
// without disconnecting USB/PCIe or dropping serial communication ports.
func (manager *Manager) softResetForProfileSwitch(ctx context.Context, id string) error {
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	if err := lockMutexContext(ctx, &state.opMu); err != nil {
		return err
	}
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return err
	}
	return manager.softResetSIMLocked(ctx, id, state, client)
}

// softResetSIMLocked 要求持有 opMu，使模式验证与 SIM 重置不能被其他模组操作插入。
func (manager *Manager) softResetSIMLocked(ctx context.Context, id string, state *managedDevice, client modem.Client) error {
	state.dataMu.Lock()
	invalidateQMINetworkSession(state, manager.candidateFor(state))
	state.dataMu.Unlock()
	commandCtx, cancel := context.WithTimeout(ctx, manager.commandTimeout)
	defer cancel()

	// 1. Cycle SIM interface to minimum functionality / clear cached SIM files
	if manager.logger != nil {
		manager.logger.Info("recovering SIM with cellular RF disabled", "category", "hardware", "device_id", id,
			"recovery_source", "sim_reset", "target_cfun", 4)
	}
	if _, err := client.Execute(commandCtx, "AT+CFUN=0"); err != nil {
		// 超时或取消时不能确认 CFUN=0 是否已生效，仍尝试恢复到 RF 关闭的工作模式。
		if isCommandInterrupted(err) {
			reloadCtx, cancelReload := context.WithTimeout(context.WithoutCancel(ctx), manager.commandTimeout)
			_, reloadErr := client.Execute(reloadCtx, "AT+CFUN=4")
			cancelReload()
			err = errors.Join(err, reloadErr)
		}
		err = fmt.Errorf("power down SIM for profile recovery: %w", err)
		manager.clearSnapshot(id, state)
		manager.setResult(id, state, nil, err)
		return err
	}

	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}

	// 2. Reload the USIM without enabling RF. Snapshots can be nil or partial
	// during REFRESH; their zero-value FlightMode does not authorize CFUN=1.
	// CFUN=0 成功后必须独立完成重新上电，不能因页面取消或原命令超时留在最低功能模式。
	reloadCtx, cancelReload := context.WithTimeout(context.WithoutCancel(ctx), manager.commandTimeout)
	defer cancelReload()
	_, err := client.Execute(reloadCtx, "AT+CFUN=4")
	err = errors.Join(ctx.Err(), err)

	manager.clearSnapshot(id, state)
	manager.setResult(id, state, nil, err)
	return err
}

func (manager *Manager) clearSnapshot(id string, state *managedDevice) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.devices[id] == state {
		state.snapshot = nil
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

// sensitiveCommand executes an AT command containing credentials or other
// authentication material. Modem errors commonly echo the complete command,
// so neither the returned error nor the retained device state may wrap it.
func (manager *Manager) sensitiveCommand(
	ctx context.Context,
	client modem.Client,
	command string,
) (modem.Response, error) {
	commandCtx, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	defer cancel()
	response, err := client.Execute(commandCtx, command)
	if err != nil {
		return response, errors.New("sensitive modem command failed")
	}
	return response, nil
}
