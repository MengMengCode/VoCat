package vowifi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"
)

const defaultCleanupTimeout = 10 * time.Second

type runtimeResources struct {
	cancel       context.CancelFunc
	radio        RadioSnapshot
	radioChanged bool
	tunnel       TunnelSession
	ims          IMSSession
}

// Orchestrator serializes lifecycle mutations while allowing concurrent state
// readers and subscribers. Disable cancels an in-flight Enable before waiting
// for the mutation lock, so a blocked provider cannot deadlock shutdown.
type Orchestrator struct {
	deps    Dependencies
	options Options

	operation chan struct{}

	mu             sync.Mutex
	state          State
	resources      *runtimeResources
	subscribers    map[uint64]chan State
	nextSubscriber uint64
}

func New(deps Dependencies, options Options) (*Orchestrator, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	if options.CleanupTimeout == 0 {
		options.CleanupTimeout = defaultCleanupTimeout
	}
	options.DeviceID = strings.TrimSpace(options.DeviceID)

	now := time.Now().UTC()
	orchestrator := &Orchestrator{
		deps:      deps,
		options:   options,
		operation: make(chan struct{}, 1),
		state: State{
			DeviceID:  strings.TrimSpace(options.DeviceID),
			Phase:     PhaseIdle,
			Sequence:  1,
			UpdatedAt: now,
			Security: SecurityAudit{
				ResponderAUTH: ResponderAUTHUnknown,
			},
		},
		subscribers: make(map[uint64]chan State),
	}
	orchestrator.operation <- struct{}{}
	return orchestrator, nil
}

// State returns a detached snapshot safe for mutation by the caller.
func (orchestrator *Orchestrator) State() State {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	return orchestrator.state.clone()
}

// Subscribe returns the current state immediately and then the newest state on
// every mutation. Slow subscribers lose intermediate snapshots rather than
// blocking the modem lifecycle.
func (orchestrator *Orchestrator) Subscribe(buffer int) (<-chan State, func()) {
	if buffer < 1 {
		buffer = 1
	}
	channel := make(chan State, buffer)

	orchestrator.mu.Lock()
	id := orchestrator.nextSubscriber
	orchestrator.nextSubscriber++
	orchestrator.subscribers[id] = channel
	channel <- orchestrator.state.clone()
	orchestrator.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			orchestrator.mu.Lock()
			if existing, ok := orchestrator.subscribers[id]; ok {
				delete(orchestrator.subscribers, id)
				close(existing)
			}
			orchestrator.mu.Unlock()
		})
	}
	return channel, cancel
}

// Enable executes one evidence-backed transaction. The order intentionally
// follows the working Linux/QMI path: live identity and home PLMN, AKA
// availability, ePDG derivation, runtime-owned RF off, cellular-data stop,
// country proxy resolution, SWu tunnel, IMS registration, and SMS readiness.
func (orchestrator *Orchestrator) Enable(ctx context.Context) (State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := orchestrator.lockOperation(ctx); err != nil {
		return orchestrator.State(), err
	}
	defer orchestrator.unlockOperation()

	current := orchestrator.State()
	switch current.Phase {
	case PhaseSIMReady, PhaseAccessReady, PhaseTunnelReady, PhaseIMSReady, PhaseSMSReady, PhaseStopping:
		return current, ErrAlreadyEnabled
	}

	now := time.Now().UTC()
	orchestrator.mutate(func(state *State) {
		attempt := state.Attempt + 1
		sequence := state.Sequence
		*state = State{
			DeviceID:   orchestrator.options.DeviceID,
			Phase:      PhaseIdle,
			Enabled:    true,
			Attempt:    attempt,
			Sequence:   sequence,
			StartedAt:  &now,
			UpdatedAt:  now,
			LastReason: "enable_requested",
			Security: SecurityAudit{
				ResponderAUTH: ResponderAUTHUnknown,
			},
		}
	})

	runtimeContext, runtimeCancel := context.WithCancel(context.Background())
	resources := &runtimeResources{cancel: runtimeCancel}
	orchestrator.mu.Lock()
	orchestrator.resources = resources
	orchestrator.mu.Unlock()

	setupContext, stopSetup := mergedContext(ctx, runtimeContext)
	defer stopSetup()

	fail := func(stage Phase, cause error) (State, error) {
		runtimeCancel()
		cleanupErrors := orchestrator.cleanup(resources)
		orchestrator.mu.Lock()
		orchestrator.resources = nil
		orchestrator.mu.Unlock()

		orchestrator.mutate(func(state *State) {
			state.Phase = PhaseFailed
			state.Active = false
			state.TunnelReady = false
			state.IMSReady = false
			state.SMSReady = false
			state.LastErrorClass = classifyError(stage, cause)
			state.LastError = cause.Error()
			state.LastReason = "enable_failed"
			state.CleanupErrors = append([]string(nil), cleanupErrors...)
		})

		stageError := error(&StageError{Stage: stage, Err: cause})
		if len(cleanupErrors) > 0 {
			stageError = errors.Join(
				stageError,
				fmt.Errorf("vowifi cleanup: %s", strings.Join(cleanupErrors, "; ")),
			)
		}
		return orchestrator.State(), stageError
	}

	identity, err := orchestrator.deps.SIM.ReadIdentity(setupContext, orchestrator.options.DeviceID)
	if err != nil {
		return fail(PhaseSIMReady, err)
	}
	if err := identity.validate(); err != nil {
		return fail(PhaseSIMReady, err)
	}
	akaEvidence, err := orchestrator.deps.AKA.CheckReady(setupContext, identity)
	if err != nil {
		return fail(PhaseSIMReady, err)
	}
	if !akaEvidence.Ready {
		return fail(PhaseSIMReady, errors.New("AKA application is not ready"))
	}
	carrierProfile := ResolveCarrierProfile(identity)
	if smsc, smscErrs := orchestrator.readSMSCenter(setupContext); smsc != "" {
		identity.SMSC = smsc
	} else if len(smscErrs) > 0 {
		orchestrator.addWarning(
			"SIM SMS service-centre address is unavailable; IMS receive remains available: " +
				errors.Join(smscErrs...).Error(),
		)
	}
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseSIMReady
		state.ICCID = strings.TrimSpace(identity.ICCID)
		state.IMSI = strings.TrimSpace(identity.IMSI)
		state.SIMReady = true
		state.HomeMCC = strings.TrimSpace(identity.HomeMCC)
		state.HomeMNC = strings.TrimSpace(identity.HomeMNC)
		state.CarrierPLMN = carrierProfile.PLMN
		state.CarrierPresetID = carrierProfile.PresetID
		state.CarrierSource = carrierProfile.Source
		state.IMSIdentitySource = carrierProfile.IMSIdentitySource
		state.LastReason = "sim_and_aka_ready"
	})

	epdg, err := DeriveEPDG(identity)
	if err != nil {
		return fail(PhaseAccessReady, err)
	}
	resources.radio, err = orchestrator.deps.Radio.Snapshot(setupContext, orchestrator.options.DeviceID)
	if err != nil {
		return fail(PhaseAccessReady, err)
	}
	orchestrator.mutate(func(state *State) {
		state.PureAirplanePolicy = resources.radio.PureAirplanePolicy
	})
	// Mark the radio transaction before the first mutating call: a provider
	// may return an error after partially changing the modem.
	resources.radioChanged = true
	// Enter RF-off before reconciling PDP contexts. Some QMI-capable firmware
	// automatically owns CID 1 while the radio is online and rejects a direct
	// CGACT=0 command even though the Linux data interface is down. The adapter
	// selects the modem-specific RF-off form (CFUN=4 for EC20/EC25, CFUN=0 or
	// the equivalent reported CFUN=7 state for native Qualcomm 410). The data
	// stop then acts as a fail-closed verification and removes any context that
	// unexpectedly survived RF-off.
	if err := orchestrator.deps.Radio.EnterVoWiFiRFOff(setupContext, orchestrator.options.DeviceID); err != nil {
		return fail(PhaseAccessReady, err)
	}
	if err := orchestrator.deps.Radio.StopCellularData(setupContext, orchestrator.options.DeviceID); err != nil {
		return fail(PhaseAccessReady, err)
	}

	proxy, err := orchestrator.deps.Proxy.Resolve(setupContext, ProxyRequest{
		DeviceID:    orchestrator.options.DeviceID,
		HomeMCC:     strings.TrimSpace(identity.HomeMCC),
		HomeMNC:     strings.TrimSpace(identity.HomeMNC),
		CountryCode: strings.ToUpper(strings.TrimSpace(identity.HomeCountryCode)),
	})
	if err != nil {
		return fail(PhaseAccessReady, err)
	}
	proxy, err = normalizeProxyRoute(proxy)
	if err != nil {
		return fail(PhaseAccessReady, err)
	}
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseAccessReady
		state.AccessReady = true
		state.EPDG = epdg
		state.EPDGSource = epdgSource(identity)
		state.ProxyMode = proxy.Mode
		state.ProxyID = proxy.ID
		state.LastReason = "epdg_access_ready"
	})

	tunnel, err := orchestrator.deps.Tunnel.Start(setupContext, TunnelRequest{
		DeviceID: orchestrator.options.DeviceID,
		Identity: identity,
		Carrier:  carrierProfile,
		EPDG:     epdg,
		Proxy:    proxy,
		AKA:      orchestrator.deps.AKA,
		Security: TunnelSecurityPolicy{
			AllowMissingResponderAUTH: orchestrator.options.AllowMissingResponderAUTH,
		},
	})
	if err != nil {
		return fail(PhaseTunnelReady, err)
	}
	if tunnel == nil {
		return fail(PhaseTunnelReady, errors.New("tunnel provider returned a nil session"))
	}
	resources.tunnel = tunnel
	tunnelEvidence := tunnel.Evidence()
	if !tunnelEvidence.Established {
		orchestrator.mutate(func(state *State) {
			state.Security = securityAuditFromEvidence(tunnelEvidence)
		})
		return fail(PhaseTunnelReady, ErrTunnelNotEstablished)
	}
	securityAudit, err := orchestrator.validateTunnelEvidence(tunnelEvidence)
	orchestrator.mutate(func(state *State) {
		state.Security = securityAudit
	})
	if err != nil {
		return fail(PhaseTunnelReady, err)
	}
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseTunnelReady
		state.Active = true
		state.TunnelReady = true
		state.TunnelName = strings.TrimSpace(tunnelEvidence.Name)
		state.DataplaneMode = strings.TrimSpace(tunnelEvidence.DataplaneMode)
		state.LastReason = "ipsec_tunnel_ready"
	})
	orchestrator.watchRuntimeTunnel(runtimeContext, resources, tunnel)

	ims, err := orchestrator.deps.IMS.Start(setupContext, IMSRequest{
		DeviceID: orchestrator.options.DeviceID,
		Identity: identity,
		Carrier:  carrierProfile,
		Tunnel:   tunnel,
	})
	if err != nil {
		return fail(PhaseIMSReady, err)
	}
	if ims == nil {
		return fail(PhaseIMSReady, errors.New("IMS provider returned a nil session"))
	}
	resources.ims = ims
	orchestrator.watchRuntimeIMS(runtimeContext, resources, ims)
	imsEvidence := ims.Evidence()
	if !imsEvidence.Registered {
		return fail(PhaseIMSReady, ErrIMSNotRegistered)
	}
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseIMSReady
		state.IMSReady = true
		state.IMSRegistration = strings.TrimSpace(imsEvidence.RegistrationState)
		state.IMSIdentitySource = strings.TrimSpace(imsEvidence.IdentitySource)
		if state.IMSIdentitySource == "" {
			state.IMSIdentitySource = carrierProfile.IMSIdentitySource
		}
		state.LastReason = "ims_registered"
	})
	orchestrator.watchRuntimeIdentity(runtimeContext, resources, identity)

	if number, source, ok := ExtractAssociatedMSISDN(imsEvidence); ok {
		record := PhoneRecord{
			ICCID:     strings.TrimSpace(identity.ICCID),
			Number:    number,
			Source:    source,
			UpdatedAt: time.Now().UTC(),
		}
		if err := orchestrator.deps.Phones.SaveAssociatedNumber(setupContext, record); err != nil {
			orchestrator.addWarning("IMS associated number is valid but could not be persisted: " + err.Error())
		} else {
			orchestrator.mutate(func(state *State) {
				state.PhoneNumber = number
				state.PhoneNumberSource = source
			})
		}
	} else {
		orchestrator.addWarning("IMS did not publish an associated MSISDN; the number was not inferred from IMSI")
	}

	smsEvidence, err := ims.EnableSMS(setupContext)
	if err != nil {
		if orchestrator.options.AllowIMSWithoutSMS {
			orchestrator.addWarning("IMS is registered but SMS capability was not confirmed: " + err.Error())
			orchestrator.mutate(func(state *State) {
				state.LastReason = "ims_registered_sms_unavailable"
				state.LastError = ""
				state.LastErrorClass = ""
				state.CleanupErrors = nil
			})
			return orchestrator.State(), nil
		}
		return fail(PhaseSMSReady, err)
	}
	if !smsEvidence.Ready {
		if orchestrator.options.AllowIMSWithoutSMS {
			orchestrator.addWarning("IMS is registered but SMS capability was not confirmed")
			orchestrator.mutate(func(state *State) {
				state.LastReason = "ims_registered_sms_unavailable"
				state.LastError = ""
				state.LastErrorClass = ""
				state.CleanupErrors = nil
			})
			return orchestrator.State(), nil
		}
		return fail(PhaseSMSReady, ErrSMSNotReady)
	}
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseSMSReady
		state.SMSReady = true
		state.LastReason = "sms_ready"
		state.LastError = ""
		state.LastErrorClass = ""
		state.CleanupErrors = nil
	})
	return orchestrator.State(), nil
}

// readSMSCenter resolves the TS-Service-Centre address used to submit SMS over
// IMS. AT+CSCA? through the SIM reader is the standard source; native Qualcomm
// 410 firmware implements only part of TS 27.007, so the AKA provider is
// consulted second because on exactly those devices it reads the same value
// over QMI. Only the failure of every available source leaves submission
// without an address, and IMS receive never depends on one.
func (orchestrator *Orchestrator) readSMSCenter(ctx context.Context) (string, []error) {
	var failures []error
	var attempted []SMSCenterReader
	for _, dependency := range []any{orchestrator.deps.SIM, orchestrator.deps.AKA} {
		reader, ok := dependency.(SMSCenterReader)
		if !ok || containsSMSCenterReader(attempted, reader) {
			continue
		}
		attempted = append(attempted, reader)
		smsc, err := reader.ReadSMSCenter(ctx, orchestrator.options.DeviceID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if smsc = strings.TrimSpace(smsc); smsc != "" {
			return smsc, nil
		}
	}
	return "", failures
}

// containsSMSCenterReader keeps one provider from being probed twice when a
// backend fills both roles, as EC20/EC25 does. Only pointer identity is
// compared: probing a value-typed provider again is cheap, while comparing
// arbitrary dynamic types could panic.
func containsSMSCenterReader(readers []SMSCenterReader, candidate SMSCenterReader) bool {
	value := reflect.ValueOf(candidate)
	if value.Kind() != reflect.Pointer {
		return false
	}
	for _, reader := range readers {
		existing := reflect.ValueOf(reader)
		if existing.Kind() == reflect.Pointer && existing.Pointer() == value.Pointer() {
			return true
		}
	}
	return false
}

// Disable is idempotent. It interrupts setup when necessary and closes IMS,
// tunnel, then restores the captured radio state.
func (orchestrator *Orchestrator) Disable(ctx context.Context) (State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	orchestrator.cancelCurrentRuntime()
	if err := orchestrator.lockOperation(ctx); err != nil {
		return orchestrator.State(), err
	}
	defer orchestrator.unlockOperation()

	orchestrator.mu.Lock()
	resources := orchestrator.resources
	orchestrator.mu.Unlock()
	current := orchestrator.State()
	if resources == nil && current.Phase == PhaseIdle {
		return current, nil
	}

	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseStopping
		state.Enabled = false
		// Stop advertising readiness as soon as disable is accepted. Network
		// cleanup is best-effort and can take several seconds, but callers must
		// not continue to present the old IMS registration as usable.
		state.Active = false
		state.TunnelReady = false
		state.IMSReady = false
		state.SMSReady = false
		state.IMSRegistration = ""
		state.LastReason = "disable_requested"
	})
	if resources != nil && resources.cancel != nil {
		resources.cancel()
	}
	cleanupErrors := orchestrator.cleanup(resources)
	orchestrator.mu.Lock()
	orchestrator.resources = nil
	orchestrator.mu.Unlock()

	if len(cleanupErrors) > 0 {
		cause := fmt.Errorf("%w: %s", ErrCleanupIncomplete, strings.Join(cleanupErrors, "; "))
		orchestrator.mutate(func(state *State) {
			// cleanup() has already released every local resource and restored
			// the radio. A rejected best-effort SIP deregistration is useful
			// diagnostic evidence, but it must not leave a disabled runtime in
			// Failed/Stopping or prevent a later cellular/VoWiFi transition.
			state.Phase = PhaseIdle
			state.Enabled = false
			state.Active = false
			state.SIMReady = false
			state.AccessReady = false
			state.TunnelReady = false
			state.IMSReady = false
			state.SMSReady = false
			state.TunnelName = ""
			state.DataplaneMode = ""
			state.IMSRegistration = ""
			state.LastErrorClass = "cleanup_warning"
			state.LastError = cause.Error()
			state.LastReason = "disabled_with_cleanup_errors"
			state.CleanupErrors = append([]string(nil), cleanupErrors...)
			state.StartedAt = nil
		})
		return orchestrator.State(), cause
	}

	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseIdle
		state.Enabled = false
		state.Active = false
		state.SIMReady = false
		state.AccessReady = false
		state.TunnelReady = false
		state.IMSReady = false
		state.SMSReady = false
		state.TunnelName = ""
		state.DataplaneMode = ""
		state.IMSRegistration = ""
		state.LastErrorClass = ""
		state.LastError = ""
		state.LastReason = "disabled"
		state.CleanupErrors = nil
		state.StartedAt = nil
	})
	return orchestrator.State(), nil
}

func (orchestrator *Orchestrator) Retry(ctx context.Context) (State, error) {
	if orchestrator.State().Phase != PhaseFailed {
		return orchestrator.State(), ErrRetryRequiresFailure
	}
	return orchestrator.Enable(ctx)
}

func (orchestrator *Orchestrator) Reconnect(ctx context.Context) (State, error) {
	current := orchestrator.State()
	if !current.Enabled && current.Phase == PhaseIdle {
		return current, ErrNotRunning
	}
	// Teardown during a reconnect is best-effort. Disable already releases the
	// local IMS, tunnel, and radio resources, so a non-fatal cleanup error
	// (e.g. the network rejecting SIP deregistration) must not block the
	// rebuild — otherwise the device wedges in PhaseFailed. Only propagate
	// errors that prevented the teardown itself (e.g. the operation lock).
	if _, err := orchestrator.Disable(ctx); err != nil && !errors.Is(err, ErrCleanupIncomplete) {
		return orchestrator.State(), err
	}
	return orchestrator.Enable(ctx)
}

// SendSMS submits through the currently registered IMS session. The lifecycle
// operation lock prevents teardown from closing the session mid-transaction.
func (orchestrator *Orchestrator) SendSMS(
	ctx context.Context,
	request SMSSubmitRequest,
) (SMSSubmitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := orchestrator.lockOperation(ctx); err != nil {
		return SMSSubmitResult{}, err
	}
	defer orchestrator.unlockOperation()
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	state := orchestrator.state.clone()
	ready := state.IMSReady && state.SMSReady
	orchestrator.mu.Unlock()
	if resources == nil || resources.ims == nil || !ready {
		return SMSSubmitResult{}, ErrSMSNotReady
	}
	// The device manager snapshot used by the HTTP layer is refreshed
	// periodically and can still describe the previous SIM for several seconds
	// after a hot swap. Re-read the physical identity while holding the same
	// operation lock that serializes IMS submission and teardown. This closes the
	// check/use gap with controlled profile switches and prevents a message from
	// being submitted on the previous subscriber's still-registered IMS session.
	liveIdentity, err := orchestrator.deps.SIM.ReadIdentity(ctx, orchestrator.options.DeviceID)
	if err != nil {
		return SMSSubmitResult{}, fmt.Errorf("%w: verify live SIM identity before submission: %v", ErrSMSNotReady, err)
	}
	if !sameSMSSubscriber(state.ICCID, state.IMSI, liveIdentity.ICCID, liveIdentity.IMSI) {
		if resources.cancel != nil {
			resources.cancel()
		}
		orchestrator.revokeChangedIdentityRuntimeLocked(resources)
		return SMSSubmitResult{}, errors.Join(ErrSMSNotReady, ErrSIMIdentityChanged)
	}
	sender, ok := resources.ims.(SMSSender)
	if !ok {
		return SMSSubmitResult{}, ErrSMSNotReady
	}
	return sender.SendSMS(ctx, request)
}

func sameSMSSubscriber(expectedICCID, expectedIMSI, liveICCID, liveIMSI string) bool {
	expectedICCID = strings.TrimSpace(expectedICCID)
	expectedIMSI = strings.TrimSpace(expectedIMSI)
	liveICCID = strings.TrimSpace(liveICCID)
	liveIMSI = strings.TrimSpace(liveIMSI)
	return expectedICCID != "" && expectedIMSI != "" && liveICCID != "" && liveIMSI != "" &&
		strings.EqualFold(expectedICCID, liveICCID) && expectedIMSI == liveIMSI
}

func (orchestrator *Orchestrator) Calls() ([]Call, error) {
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	ready := orchestrator.state.IMSReady
	orchestrator.mu.Unlock()
	if resources == nil || resources.ims == nil || !ready {
		return nil, ErrNotRunning
	}
	controller, ok := resources.ims.(CallController)
	if !ok {
		return nil, ErrNotRunning
	}
	return controller.Calls(), nil
}

func (orchestrator *Orchestrator) DialCall(ctx context.Context, number string) (Call, error) {
	return orchestrator.callAction(ctx, func(controller CallController) (Call, error) {
		return controller.DialCall(ctx, number)
	})
}

func (orchestrator *Orchestrator) AnswerCall(ctx context.Context, id string) (Call, error) {
	return orchestrator.callAction(ctx, func(controller CallController) (Call, error) {
		return controller.AnswerCall(ctx, id)
	})
}

func (orchestrator *Orchestrator) HangupCall(ctx context.Context, id string) error {
	_, err := orchestrator.callAction(ctx, func(controller CallController) (Call, error) {
		return Call{}, controller.HangupCall(ctx, id)
	})
	return err
}

func (orchestrator *Orchestrator) SendDTMF(
	ctx context.Context,
	id string,
	digit byte,
	duration time.Duration,
) error {
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	ready := orchestrator.state.IMSReady
	orchestrator.mu.Unlock()
	if resources == nil || resources.ims == nil || !ready {
		return ErrNotRunning
	}
	controller, ok := resources.ims.(CallDTMFController)
	if !ok {
		return ErrNotRunning
	}
	return controller.SendDTMF(ctx, id, digit, duration)
}

func (orchestrator *Orchestrator) CallMedia(ctx context.Context, id string) (CallMedia, error) {
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	ready := orchestrator.state.IMSReady
	orchestrator.mu.Unlock()
	if resources == nil || resources.ims == nil || !ready {
		return nil, ErrNotRunning
	}
	controller, ok := resources.ims.(CallMediaController)
	if !ok {
		return nil, ErrNotRunning
	}
	return controller.CallMedia(ctx, id)
}

func (orchestrator *Orchestrator) callAction(
	ctx context.Context,
	action func(CallController) (Call, error),
) (Call, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := orchestrator.lockOperation(ctx); err != nil {
		return Call{}, err
	}
	defer orchestrator.unlockOperation()
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	ready := orchestrator.state.IMSReady
	orchestrator.mu.Unlock()
	if resources == nil || resources.ims == nil || !ready {
		return Call{}, ErrNotRunning
	}
	controller, ok := resources.ims.(CallController)
	if !ok {
		return Call{}, ErrNotRunning
	}
	return action(controller)
}

func (orchestrator *Orchestrator) Close(ctx context.Context) error {
	_, err := orchestrator.Disable(ctx)
	return err
}

// DeriveEPDG uses an explicitly provided carrier endpoint or the 3GPP standard
// home-PLMN form. It never derives a phone number or MNC length from IMSI.
func DeriveEPDG(identity SIMIdentity) (string, error) {
	if configured := strings.TrimSpace(identity.EPDG); configured != "" {
		if strings.ContainsAny(configured, " \t\r\n/:") || len(configured) > 253 {
			return "", errors.New("vowifi: configured ePDG must be a hostname")
		}
		return strings.ToLower(configured), nil
	}
	if err := identity.validate(); err != nil {
		return "", err
	}
	mnc := strings.TrimSpace(identity.HomeMNC)
	for len(mnc) < 3 {
		mnc = "0" + mnc
	}
	return fmt.Sprintf(
		"epdg.epc.mnc%s.mcc%s.pub.3gppnetwork.org",
		mnc,
		strings.TrimSpace(identity.HomeMCC),
	), nil
}

func epdgSource(identity SIMIdentity) string {
	if strings.TrimSpace(identity.EPDG) != "" {
		return "configured"
	}
	return "derived"
}

func normalizeProxyRoute(route ProxyRoute) (ProxyRoute, error) {
	if route.Mode == "" {
		route.Mode = ProxyModeDirect
	}
	switch route.Mode {
	case ProxyModeDirect:
		route.Address = ""
		route.Username = ""
		route.Password = ""
	case ProxyModeSOCKS5:
		if strings.TrimSpace(route.Address) == "" {
			return ProxyRoute{}, errors.New("vowifi: SOCKS5 proxy address is empty")
		}
	default:
		return ProxyRoute{}, fmt.Errorf("vowifi: unsupported proxy mode %q", route.Mode)
	}
	route.ID = strings.TrimSpace(route.ID)
	route.Address = strings.TrimSpace(route.Address)
	return route, nil
}

func (orchestrator *Orchestrator) validateTunnelEvidence(evidence TunnelEvidence) (SecurityAudit, error) {
	audit := securityAuditFromEvidence(evidence)
	switch evidence.ResponderAUTH {
	case ResponderAUTHVerified:
		return audit, nil
	case ResponderAUTHMissing:
		if !orchestrator.options.AllowMissingResponderAUTH {
			return audit, ErrResponderAUTHRequired
		}
		audit.CompatibilityOverride = true
		audit.HighRisk = true
		audit.Level = AuditLevelHigh
		audit.Code = AuditCodeMissingResponderAUTH
		audit.Message = "IKE responder AUTH was missing and accepted by explicit compatibility policy"
		return audit, nil
	case ResponderAUTHInvalid:
		return audit, fmt.Errorf("%w: responder AUTH is invalid", ErrResponderAUTHRequired)
	default:
		return audit, fmt.Errorf("%w: responder AUTH evidence is unknown", ErrResponderAUTHRequired)
	}
}

func securityAuditFromEvidence(evidence TunnelEvidence) SecurityAudit {
	return SecurityAudit{
		ResponderAUTH: evidence.ResponderAUTH,
		IKEEncryption: strings.TrimSpace(evidence.IKEEncryption),
		IKEIntegrity:  strings.TrimSpace(evidence.IKEIntegrity),
		IKEDHGroup:    strings.TrimSpace(evidence.IKEDHGroup),
		ESPEncryption: strings.TrimSpace(evidence.ESPEncryption),
		ESPIntegrity:  strings.TrimSpace(evidence.ESPIntegrity),
	}
}

func (orchestrator *Orchestrator) cleanup(resources *runtimeResources) []string {
	if resources == nil {
		return nil
	}
	var cleanupErrors []string
	if resources.ims != nil {
		if err := orchestrator.cleanupCall(resources.ims.Close); err != nil {
			cleanupErrors = append(cleanupErrors, "close IMS: "+err.Error())
		}
		resources.ims = nil
	}
	if resources.tunnel != nil {
		if err := orchestrator.cleanupCall(resources.tunnel.Close); err != nil {
			cleanupErrors = append(cleanupErrors, "close tunnel: "+err.Error())
		}
		resources.tunnel = nil
	}
	if resources.radioChanged {
		if err := orchestrator.cleanupCall(func(ctx context.Context) error {
			return orchestrator.deps.Radio.Restore(ctx, orchestrator.options.DeviceID, resources.radio)
		}); err != nil {
			cleanupErrors = append(cleanupErrors, "restore radio: "+err.Error())
		}
		resources.radioChanged = false
	}
	return cleanupErrors
}

func (orchestrator *Orchestrator) cleanupCall(call func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), orchestrator.options.CleanupTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- call(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Providers receive the same deadline and should normally return on it.
		// The outer select is a final containment boundary: a defective network
		// close must never prevent the following tunnel/radio cleanup.
		return ctx.Err()
	}
}

func (orchestrator *Orchestrator) cancelCurrentRuntime() {
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	orchestrator.mu.Unlock()
	if resources != nil && resources.cancel != nil {
		resources.cancel()
	}
}

func (orchestrator *Orchestrator) watchRuntimeTunnel(
	runtimeContext context.Context,
	resources *runtimeResources,
	tunnel TunnelSession,
) {
	notifier, ok := tunnel.(RuntimeFailureNotifier)
	if !ok {
		return
	}
	orchestrator.watchRuntimeFailure(
		runtimeContext,
		resources,
		notifier,
		"tunnel_runtime",
		"runtime_tunnel_failed",
	)
}

func (orchestrator *Orchestrator) watchRuntimeIMS(
	runtimeContext context.Context,
	resources *runtimeResources,
	ims IMSSession,
) {
	notifier, ok := ims.(RuntimeFailureNotifier)
	if !ok {
		return
	}
	orchestrator.watchRuntimeFailure(
		runtimeContext,
		resources,
		notifier,
		"ims_runtime",
		"runtime_ims_failed",
	)
}

func (orchestrator *Orchestrator) watchRuntimeIdentity(
	runtimeContext context.Context,
	resources *runtimeResources,
	identity SIMIdentity,
) {
	interval := orchestrator.options.IdentityCheckInterval
	if interval <= 0 {
		return
	}
	wantICCID := strings.TrimSpace(identity.ICCID)
	wantIMSI := strings.TrimSpace(identity.IMSI)
	wantProfile := ResolveCarrierProfile(identity)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runtimeContext.Done():
				return
			case <-ticker.C:
			}
			checkContext, cancel := context.WithTimeout(runtimeContext, interval)
			current, err := orchestrator.deps.SIM.ReadIdentity(
				checkContext,
				orchestrator.options.DeviceID,
			)
			cancel()
			if err != nil {
				// A transient read failure is not proof that the subscriber changed.
				// Existing tunnel/IMS failure monitors remain authoritative.
				continue
			}
			gotICCID := strings.TrimSpace(current.ICCID)
			gotIMSI := strings.TrimSpace(current.IMSI)
			gotProfile := ResolveCarrierProfile(current)
			if (wantICCID != "" && gotICCID != "" && !strings.EqualFold(wantICCID, gotICCID)) ||
				(wantIMSI != "" && gotIMSI != "" && wantIMSI != gotIMSI) ||
				(wantProfile.PLMN != "" && gotProfile.PLMN != "" && wantProfile.PLMN != gotProfile.PLMN) {
				orchestrator.revokeChangedIdentityRuntime(resources)
				return
			}
		}
	}()
}

func (orchestrator *Orchestrator) revokeChangedIdentityRuntime(resources *runtimeResources) {
	if resources == nil {
		return
	}
	if resources.cancel != nil {
		resources.cancel()
	}
	if err := orchestrator.lockOperation(context.Background()); err != nil {
		return
	}
	defer orchestrator.unlockOperation()
	orchestrator.revokeChangedIdentityRuntimeLocked(resources)
}

// revokeChangedIdentityRuntimeLocked tears down resources for the previous
// subscriber. The caller must own orchestrator.operation.
func (orchestrator *Orchestrator) revokeChangedIdentityRuntimeLocked(resources *runtimeResources) {
	orchestrator.mu.Lock()
	current := orchestrator.resources == resources
	orchestrator.mu.Unlock()
	if !current {
		return
	}
	cleanupErrors := orchestrator.cleanup(resources)
	orchestrator.mu.Lock()
	if orchestrator.resources == resources {
		orchestrator.resources = nil
	}
	orchestrator.mu.Unlock()
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseIdle
		state.Enabled = false
		state.Active = false
		state.SIMReady = false
		state.AccessReady = false
		state.TunnelReady = false
		state.IMSReady = false
		state.SMSReady = false
		state.IMSRegistration = ""
		state.HomeMCC = ""
		state.HomeMNC = ""
		state.CarrierPLMN = ""
		state.CarrierPresetID = ""
		state.CarrierSource = ""
		state.EPDGSource = ""
		state.IMSIdentitySource = ""
		state.EPDG = ""
		state.ProxyMode = ""
		state.ProxyID = ""
		state.LastErrorClass = "sim_identity_changed"
		state.LastError = ErrSIMIdentityChanged.Error()
		state.LastReason = "runtime_sim_identity_changed"
		state.CleanupErrors = append([]string(nil), cleanupErrors...)
		state.StartedAt = nil
	})
}

func (orchestrator *Orchestrator) watchRuntimeFailure(
	runtimeContext context.Context,
	resources *runtimeResources,
	notifier RuntimeFailureNotifier,
	errorClass string,
	reason string,
) {
	failures := notifier.Failures()
	if failures == nil {
		return
	}
	go func() {
		select {
		case <-runtimeContext.Done():
			return
		case cause := <-failures:
			if cause == nil {
				cause = errors.New("VoWiFi runtime session stopped")
			}
			// Interrupt any still-running IMS setup before waiting for the
			// serialized lifecycle lock.
			if resources.cancel != nil {
				resources.cancel()
			}
			if err := orchestrator.lockOperation(context.Background()); err != nil {
				return
			}
			defer orchestrator.unlockOperation()

			orchestrator.mu.Lock()
			current := orchestrator.resources == resources
			orchestrator.mu.Unlock()
			if !current {
				return
			}
			cleanupErrors := orchestrator.cleanup(resources)
			orchestrator.mu.Lock()
			if orchestrator.resources == resources {
				orchestrator.resources = nil
			}
			orchestrator.mu.Unlock()
			orchestrator.mutate(func(state *State) {
				state.Phase = PhaseFailed
				state.Active = false
				state.TunnelReady = false
				state.IMSReady = false
				state.SMSReady = false
				state.LastErrorClass = errorClass
				state.LastError = cause.Error()
				state.LastReason = reason
				state.CleanupErrors = append([]string(nil), cleanupErrors...)
			})
		}
	}()
}

func (orchestrator *Orchestrator) lockOperation(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-orchestrator.operation:
		return nil
	}
}

func (orchestrator *Orchestrator) unlockOperation() {
	orchestrator.operation <- struct{}{}
}

func (orchestrator *Orchestrator) mutate(change func(*State)) {
	orchestrator.mu.Lock()
	change(&orchestrator.state)
	orchestrator.state.Sequence++
	orchestrator.state.UpdatedAt = time.Now().UTC()
	snapshot := orchestrator.state.clone()
	for _, subscriber := range orchestrator.subscribers {
		select {
		case subscriber <- snapshot:
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- snapshot:
			default:
			}
		}
	}
	orchestrator.mu.Unlock()
}

func (orchestrator *Orchestrator) addWarning(warning string) {
	orchestrator.mutate(func(state *State) {
		state.Warnings = append(state.Warnings, warning)
	})
}

func classifyError(stage Phase, err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case isTimeoutError(err):
		return "network_timeout"
	case errors.Is(err, ErrInvalidIdentity):
		return "sim_identity"
	case errors.Is(err, ErrEAPAuthenticationRejected):
		return "eap_authentication_rejected"
	case errors.Is(err, ErrResponderAUTHRequired):
		return "responder_auth"
	case errors.Is(err, ErrTunnelNotEstablished):
		return "tunnel"
	case errors.Is(err, ErrIMSNotRegistered):
		return "ims_registration"
	case errors.Is(err, ErrSMSNotReady):
		return "sms"
	default:
		return string(stage)
	}
}

func isTimeoutError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func mergedContext(caller context.Context, runtime context.Context) (context.Context, func()) {
	merged, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(runtime, cancel)
	return merged, func() {
		stop()
		cancel()
	}
}
