package vowifi

import (
	"context"
	"errors"
	"fmt"
	"net"
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
// follows the working Linux/QMI path: snapshot and disable cellular RF first,
// then read the live identity/home PLMN, verify AKA availability, derive ePDG,
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

	// A failed attempt deliberately retains the original radio checkpoint and
	// keeps CFUN=4. Automatic retries must rebuild only the Wi-Fi/IKE/IMS layers;
	// restoring CFUN=1 between attempts can briefly register on a visited network
	// and trigger roaming/welcome SMS messages. Explicit Disable is the only path
	// that restores the pre-VoWiFi radio mode.
	orchestrator.mu.Lock()
	retained := orchestrator.resources
	orchestrator.mu.Unlock()
	runtimeContext, runtimeCancel := context.WithCancel(context.Background())
	resources := &runtimeResources{cancel: runtimeCancel}
	if current.Phase == PhaseFailed && retained != nil && retained.radioChanged {
		resources.radio = retained.radio
		resources.radioChanged = true
	}
	orchestrator.mu.Lock()
	orchestrator.resources = resources
	orchestrator.mu.Unlock()

	setupContext, stopSetup := mergedContext(ctx, runtimeContext)
	defer stopSetup()
	var err error

	fail := func(stage Phase, cause error) (State, error) {
		runtimeCancel()
		cleanupErrors := orchestrator.cleanupSessions(resources)
		orchestrator.mu.Lock()
		if resources.radioChanged {
			orchestrator.resources = resources
		} else {
			orchestrator.resources = nil
		}
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

	if !resources.radioChanged {
		resources.radio, err = orchestrator.deps.Radio.Snapshot(setupContext, orchestrator.options.DeviceID)
		if err != nil {
			return fail(PhaseAccessReady, err)
		}
		orchestrator.mutate(func(state *State) {
			state.PureAirplanePolicy = resources.radio.PureAirplanePolicy
		})
		// Mark the transaction before the mutating call: a provider may return
		// an error after partially changing the modem.
		resources.radioChanged = true
	}
	// RF-off is established before any SIM/AKA probing. Those operations are
	// local UICC APDUs and remain available in CFUN=4; no serving-cell attach is
	// required or permitted during VoWiFi setup.
	if err := orchestrator.deps.Radio.EnterVoWiFiRFOff(setupContext, orchestrator.options.DeviceID); err != nil {
		return fail(PhaseAccessReady, err)
	}
	if err := orchestrator.deps.Radio.StopCellularData(setupContext, orchestrator.options.DeviceID); err != nil {
		return fail(PhaseAccessReady, err)
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
	if reader, ok := orchestrator.deps.SIM.(SMSCenterReader); ok {
		if smsc, smscErr := reader.ReadSMSCenter(setupContext, orchestrator.options.DeviceID); smscErr == nil {
			identity.SMSC = strings.TrimSpace(smsc)
		} else {
			orchestrator.addWarning("SIM SMS service-centre address is unavailable; IMS receive remains available: " + smscErr.Error())
		}
	}
	carrierProfile := ResolveCarrierProfile(identity)
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseSIMReady
		state.ICCID = strings.TrimSpace(identity.ICCID)
		state.IMSI = strings.TrimSpace(identity.IMSI)
		state.SIMReady = true
		state.HomeMCC = strings.TrimSpace(identity.HomeMCC)
		state.HomeMNC = strings.TrimSpace(identity.HomeMNC)
		state.CarrierProfile = carrierProfile.ID
		state.CarrierProfileFrom = carrierProfile.MatchSource
		state.LastReason = "sim_and_aka_ready"
	})

	epdg, err := DeriveEPDG(identity)
	if err != nil {
		return fail(PhaseAccessReady, err)
	}
	proxy, err := orchestrator.deps.Proxy.Resolve(setupContext, ProxyRequest{
		DeviceID:    orchestrator.options.DeviceID,
		ICCID:       strings.TrimSpace(identity.ICCID),
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
		state.ProxyMode = proxy.Mode
		state.ProxyID = proxy.ID
		state.LastReason = "epdg_access_ready"
	})

	tunnel, err := orchestrator.deps.Tunnel.Start(setupContext, TunnelRequest{
		DeviceID: orchestrator.options.DeviceID,
		Identity: identity,
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
		state.LastReason = "ims_registered"
	})

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
	if ctx == nil {
		ctx = context.Background()
	}
	if err := orchestrator.lockOperation(ctx); err != nil {
		return orchestrator.State(), err
	}
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	orchestrator.mu.Unlock()
	if resources != nil && resources.cancel != nil {
		resources.cancel()
	}
	cleanupErrors := orchestrator.cleanupSessions(resources)
	orchestrator.mutate(func(state *State) {
		state.Phase = PhaseFailed
		state.Enabled = true
		state.Active = false
		state.TunnelReady = false
		state.IMSReady = false
		state.SMSReady = false
		state.LastReason = "reconnect_requested"
		state.CleanupErrors = append([]string(nil), cleanupErrors...)
	})
	orchestrator.unlockOperation()
	// Keep the radio checkpoint and CFUN=4 across a reconnect. Re-enabling RF
	// for even a short window defeats airplane-first VoWiFi behavior.
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
	ready := orchestrator.state.IMSReady && orchestrator.state.SMSReady
	orchestrator.mu.Unlock()
	if resources == nil || resources.ims == nil || !ready {
		return SMSSubmitResult{}, ErrSMSNotReady
	}
	sender, ok := resources.ims.(SMSSender)
	if !ok {
		return SMSSubmitResult{}, ErrSMSNotReady
	}
	return sender.SendSMS(ctx, request)
}

// SendUSSI submits a USSD dialog turn through the currently registered IMS
// session. USSI only requires IMS registration — it does not depend on the
// +g.3gpp.smsip contact being confirmed, so the readiness gate is IMSReady
// alone (unlike SendSMS which also requires SMSReady).
func (orchestrator *Orchestrator) SendUSSI(
	ctx context.Context,
	request USSISubmitRequest,
) (USSISubmitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := orchestrator.lockOperation(ctx); err != nil {
		return USSISubmitResult{}, err
	}
	defer orchestrator.unlockOperation()
	orchestrator.mu.Lock()
	resources := orchestrator.resources
	ready := orchestrator.state.IMSReady
	orchestrator.mu.Unlock()
	if resources == nil || resources.ims == nil || !ready {
		return USSISubmitResult{}, ErrUSSINotReady
	}
	sender, ok := resources.ims.(USSISender)
	if !ok {
		return USSISubmitResult{}, ErrUSSINotReady
	}
	return sender.SendUSSI(ctx, request)
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
	profile := ResolveCarrierProfile(identity)
	if profile.EPDG != "" {
		return profile.EPDG, nil
	}
	if profile.RouteMCC != "" {
		return standardEPDGHostname(profile.RouteMCC, profile.RouteMNC), nil
	}
	if err := identity.validate(); err != nil {
		return "", err
	}
	return standardEPDGHostname(identity.HomeMCC, identity.HomeMNC), nil
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
	cleanupErrors := orchestrator.cleanupSessions(resources)
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

// cleanupSessions releases network-layer resources without restoring cellular
// RF. It is used while VoWiFi remains the desired policy, including failed
// automatic retries and manual reconnects.
func (orchestrator *Orchestrator) cleanupSessions(resources *runtimeResources) []string {
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
			cleanupErrors := orchestrator.cleanupSessions(resources)
			orchestrator.mu.Lock()
			if orchestrator.resources == resources {
				if resources.radioChanged {
					orchestrator.resources = resources
				} else {
					orchestrator.resources = nil
				}
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
