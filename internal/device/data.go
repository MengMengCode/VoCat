package device

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"vocat/internal/modem"
)

var apnPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9])?$`)

// qmiDataSession is deliberately independent from the QMI library types so
// the manager can be tested and built on non-Linux hosts. A live session owns
// its WDS client ID and must remain open for as long as the packet call exists.
type qmiDataSession interface {
	Start(context.Context, string, string, string, uint8, uint8) (uint32, error)
	Stop(context.Context, uint32) error
	StopAny(context.Context, bool) error
	Connected(context.Context) (bool, error)
	RawIP(context.Context) (bool, error)
	SetRawIP(context.Context) error
	RuntimeIPv4(context.Context) (qmiIPv4Settings, error)
	Close() error
}

// qmiDataEventSource is an optional capability implemented by native QMI
// sessions. The event channel only carries a wake-up signal; the caller must
// query NetworkStatus before deciding whether recovery is required.
type qmiDataEventSource interface {
	RegisterPacketStatusEvents(context.Context) error
	PacketStatusEvents() <-chan struct{}
}

// DeviceLifecycleEvent reports a physical modem discovery transition. It is
// intentionally separate from packet-service events: a disappearance ends a
// data-session lifecycle immediately and must not be treated as a recoverable
// network probe failure.
type DeviceLifecycleEvent struct {
	ID      string
	Present bool
}

// SubscribeDeviceLifecycleEvents subscribes to hotplug/discovery transitions.
// Subscribers receive a best-effort wake-up and should re-read the current
// device state before starting any new operation.
func (manager *Manager) SubscribeDeviceLifecycleEvents(ctx context.Context) (<-chan DeviceLifecycleEvent, error) {
	if manager == nil {
		return nil, ErrDataBackendUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	events := make(chan DeviceLifecycleEvent, 4)
	manager.deviceEventsMu.Lock()
	if manager.deviceEventSubscribers == nil {
		manager.deviceEventSubscribers = make(map[chan DeviceLifecycleEvent]struct{})
	}
	manager.deviceEventSubscribers[events] = struct{}{}
	manager.deviceEventsMu.Unlock()
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			manager.removeDeviceLifecycleSubscriber(events)
		}()
	}
	return events, nil
}

func (manager *Manager) removeDeviceLifecycleSubscriber(events chan DeviceLifecycleEvent) {
	manager.deviceEventsMu.Lock()
	defer manager.deviceEventsMu.Unlock()
	if _, ok := manager.deviceEventSubscribers[events]; !ok {
		return
	}
	delete(manager.deviceEventSubscribers, events)
	close(events)
}

func (manager *Manager) publishDeviceLifecycleEvent(event DeviceLifecycleEvent) {
	if strings.TrimSpace(event.ID) == "" {
		return
	}
	manager.deviceEventsMu.Lock()
	defer manager.deviceEventsMu.Unlock()
	for events := range manager.deviceEventSubscribers {
		select {
		case events <- event:
		default:
			// A later discovery or monitor pass will re-read state; do not let a
			// slow subscriber block USB discovery for every other device.
		}
	}
}

type qmiDataSessionOpener func(context.Context, string) (qmiDataSession, error)

type qmiIPv4Settings struct {
	Address string
	Prefix  int
	Gateway string
	DNS     []string
	MTU     int
}

// SubscribeNetworkStatusEvents subscribes to modem-side packet-service
// indications. The channel carries the physical device ID and is only a
// wake-up signal; callers must query NetworkStatus before changing state.
// Subscriptions are global so a server can keep one listener while devices
// are added, removed, or remapped.
func (manager *Manager) SubscribeNetworkStatusEvents(ctx context.Context) (<-chan string, error) {
	if manager == nil {
		return nil, ErrDataBackendUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	events := make(chan string, 1)
	manager.networkEventsMu.Lock()
	if manager.networkEventSubscribers == nil {
		manager.networkEventSubscribers = make(map[chan string]struct{})
	}
	manager.networkEventSubscribers[events] = struct{}{}
	manager.networkEventsMu.Unlock()
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			manager.removeNetworkStatusEventSubscriber(events)
		}()
	}
	return events, nil
}

func (manager *Manager) removeNetworkStatusEventSubscriber(events chan string) {
	manager.networkEventsMu.Lock()
	defer manager.networkEventsMu.Unlock()
	if _, ok := manager.networkEventSubscribers[events]; !ok {
		return
	}
	delete(manager.networkEventSubscribers, events)
	close(events)
}

func (manager *Manager) publishNetworkStatusEvent(deviceID string) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return
	}
	manager.networkEventsMu.Lock()
	defer manager.networkEventsMu.Unlock()
	for events := range manager.networkEventSubscribers {
		select {
		case events <- deviceID:
		default:
			// A pending wake-up is enough; the subscriber will query the latest
			// state and the periodic monitor remains the safety net.
		}
	}
}

// ValidAPN reports whether value can safely be used as a modem PDP-context APN.
// An empty value is valid and means that the modem/operator default should be used.
func ValidAPN(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || apnPattern.MatchString(value)
}

func validNetworkCredential(value string) bool {
	if len(value) > 128 || strings.ContainsAny(value, "\r\n\x00\"") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func normalizeNetworkAuthentication(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "NONE":
		return "NONE"
	case "PAP":
		return "PAP"
	case "CHAP":
		return "CHAP"
	case "PAP_OR_CHAP":
		return "PAP_OR_CHAP"
	default:
		return ""
	}
}

func (manager *Manager) SetNetwork(
	ctx context.Context,
	id string,
	request NetworkRequest,
) (NetworkResult, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return NetworkResult{}, err
	}
	apn := strings.TrimSpace(request.APN)
	if request.Enabled && !ValidAPN(apn) {
		return NetworkResult{}, ErrInvalidNetworkAPN
	}
	if !validNetworkCredential(request.Username) || !validNetworkCredential(request.Password) {
		return NetworkResult{}, errors.New("APN username or password contains unsupported characters")
	}
	authentication := normalizeNetworkAuthentication(request.Authentication)
	if authentication == "" {
		return NetworkResult{}, errors.New("authentication type must be NONE, PAP, CHAP, or PAP_OR_CHAP")
	}
	ipVersion := normalizeIPVersion(request.IPVersion)
	if ipVersion == "" {
		return NetworkResult{}, errors.New("IP version must be IP, IPV6, or IPV4V6")
	}

	candidate := manager.candidateFor(state)
	backend := strings.ToLower(strings.TrimSpace(request.Backend))
	if backend == "" {
		if candidate.QMIControl != "" && candidate.NetworkInterface != "" {
			backend = "qmi"
		} else {
			backend = "at"
		}
	}
	if backend != "at" && backend != "qmi" {
		return NetworkResult{}, fmt.Errorf("unsupported cellular data backend %q", request.Backend)
	}
	if backend == "qmi" {
		// QMI WDS owns a different control surface from the serial AT actor. Do
		// not hold opMu while an external QMI transaction is in flight: a stale
		// WDS client may take seconds to time out, and blocking opMu here also
		// blocks SMS synchronization, IMS status and ordinary health probes.
		state.dataMu.Lock()
		defer state.dataMu.Unlock()
		if err := manager.validateActive(id, state); err != nil {
			return NetworkResult{}, err
		}
		candidate = manager.candidateFor(state)
		if request.Enabled {
			if err := manager.regionBlockError(state); err != nil {
				return NetworkResult{}, err
			}
		}
		if candidate.QMIControl == "" || candidate.NetworkInterface == "" {
			return NetworkResult{}, fmt.Errorf("%w: QMI control device and network interface are required", ErrDataBackendUnavailable)
		}
		// OpenStick's native WWAN path must drive registration through QMI NAS.
		// AT+COPS only updates the legacy AT facade on this firmware and can leave
		// NAS in not-registered-searching, which then makes WDS dialing report a
		// generic-no-service call failure.
		if request.Enabled && isNativeQMICandidate(candidate) {
			registrationContext, cancel := context.WithTimeout(ctx, manager.scanTimeout)
			registrationSession, openErr := manager.openNativeQMIRegistration(registrationContext, candidate)
			if openErr != nil {
				cancel()
				manager.setResult(id, state, nil, openErr)
				return NetworkResult{}, fmt.Errorf("prepare native QMI registration: %w", openErr)
			}
			registrationErr := ensureNativeQMIRegistration(
				registrationContext,
				registrationSession,
				qmiRegistrationRequestAutomatic(),
				true,
			)
			_ = registrationSession.Close()
			cancel()
			if registrationErr != nil {
				manager.setResult(id, state, nil, registrationErr)
				return NetworkResult{}, registrationErr
			}
		}
		result, err := manager.setQMINetwork(ctx, state, candidate, request.Enabled, apn, ipVersion, request.Username, request.Password, authentication)
		if err != nil && (request.Username != "" || request.Password != "") {
			// Keep credential-bearing request failures generic. Lower layers may
			// include request fields in diagnostic errors. Preserve the sentinel
			// class so callers can still classify an unavailable backend.
			if errors.Is(err, ErrDataBackendUnavailable) {
				return NetworkResult{}, fmt.Errorf("%w: authenticated QMI cellular data operation failed", ErrDataBackendUnavailable)
			}
			return NetworkResult{}, errors.New("authenticated QMI cellular data operation failed")
		}
		return result, err
	}

	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return NetworkResult{}, err
	}
	if request.Enabled {
		if err := manager.regionBlockError(state); err != nil {
			manager.setResult(id, state, nil, err)
			return NetworkResult{}, err
		}
	}
	candidate = manager.candidateFor(state)

	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return NetworkResult{}, err
	}
	if request.Enabled {
		type networkCommand struct {
			value     string
			sensitive bool
		}
		commands := []networkCommand{
			{value: fmt.Sprintf(`AT+CGDCONT=1,"%s","%s"`, ipVersion, apn)},
		}
		if authentication != "NONE" {
			authCode := map[string]int{"PAP": 1, "CHAP": 2, "PAP_OR_CHAP": 3}[authentication]
			commands = append(commands, networkCommand{
				value:     fmt.Sprintf(`AT+CGAUTH=1,%d,"%s","%s"`, authCode, request.Username, request.Password),
				sensitive: true,
			})
		}
		commands = append(commands,
			networkCommand{value: "AT+CGATT=1"},
			networkCommand{value: "AT+CGACT=1,1"},
		)
		for _, command := range commands {
			var err error
			if command.sensitive {
				_, err = manager.sensitiveCommand(ctx, client, command.value)
			} else {
				_, err = manager.command(ctx, client, command.value)
			}
			if err != nil {
				manager.setResult(id, state, nil, err)
				return NetworkResult{}, err
			}
		}
	} else {
		if _, err := manager.command(ctx, client, "AT+CGACT=0,1"); err != nil {
			manager.setResult(id, state, nil, err)
			return NetworkResult{}, err
		}
	}
	manager.setResult(id, state, nil, nil)
	return NetworkResult{
		Enabled:   request.Enabled,
		Backend:   "at",
		Interface: candidate.NetworkInterface,
		APN:       apn,
		IPVersion: ipVersion,
		Detail:    map[bool]string{true: "PDP context activated", false: "PDP context deactivated"}[request.Enabled],
	}, nil
}

func normalizeIPVersion(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "IP", "IPV4":
		return "IP"
	case "IPV6":
		return "IPV6"
	case "IPV4V6", "IPV6V4":
		return "IPV4V6"
	default:
		return ""
	}
}

// PrepareRegistration writes the PDP context that the modem will use during
// the next EPS attach without starting a host data session. Some roaming SIMs
// are rejected during registration when EC20 firmware restores a stale APN
// after a baseband reboot, even when cellular data is disabled in VoCat.
func (manager *Manager) PrepareRegistration(
	ctx context.Context,
	id string,
	apn string,
	ipVersion string,
) error {
	apn = strings.TrimSpace(apn)
	if apn == "" {
		return nil
	}
	if !ValidAPN(apn) {
		return ErrInvalidNetworkAPN
	}
	ipVersion = normalizeIPVersion(ipVersion)
	if ipVersion == "" {
		return errors.New("IP version must be IP, IPV6, or IPV4V6")
	}
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
	_, err = manager.command(ctx, client, fmt.Sprintf(`AT+CGDCONT=1,"%s","%s"`, ipVersion, apn))
	manager.setResult(id, state, nil, err)
	return err
}

func (manager *Manager) USBNetMode(ctx context.Context, id string) (USBNetMode, error) {
	response, err := manager.ExecuteAT(ctx, id, `AT+QCFG="usbnet"`)
	if err != nil {
		return USBNetMode{}, err
	}
	for _, line := range response.Lines {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(upper, `+QCFG: "USBNET",`) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(upper, `+QCFG: "USBNET",`))
		mode, parseErr := strconv.Atoi(value)
		if parseErr == nil {
			return USBNetMode{Mode: mode, Name: usbNetModeName(mode)}, nil
		}
	}
	return USBNetMode{}, errors.New("modem did not return a valid USB network mode")
}

func (manager *Manager) SetUSBNetMode(ctx context.Context, id string, mode int) (USBNetMode, error) {
	if mode < 0 || mode > 3 {
		return USBNetMode{}, errors.New("USB network mode must be between 0 and 3")
	}
	response, err := manager.ExecuteSensitiveAT(ctx, id, fmt.Sprintf(`AT+QCFG="usbnet",%d`, mode))
	if err != nil {
		return USBNetMode{}, err
	}
	if !response.OK() {
		return USBNetMode{}, &modem.CommandError{Command: response.Command, Final: response.Final, Lines: response.Lines}
	}
	return USBNetMode{Mode: mode, Name: usbNetModeName(mode)}, nil
}

// SetUSBNetModeByPort sets the USB network mode on a device that has only been
// discovered (not yet taken over), addressed by its AT port path. The port must
// belong to a currently discovered candidate, so the endpoint cannot be used to
// open arbitrary host paths.
func (manager *Manager) SetUSBNetModeByPort(
	ctx context.Context,
	atPortPath string,
	mode int,
) (USBNetMode, error) {
	if mode < 0 || mode > 3 {
		return USBNetMode{}, errors.New("USB network mode must be between 0 and 3")
	}
	atPortPath = strings.TrimSpace(atPortPath)
	if atPortPath == "" {
		return USBNetMode{}, errors.New("an AT port path is required")
	}
	manager.mu.RLock()
	var candidate modem.Candidate
	found := false
	for _, state := range manager.devices {
		if state.discovered &&
			(state.candidate.ATPort.OpenPath() == atPortPath || state.candidate.ATPort.Path == atPortPath) {
			candidate = copyCandidate(state.candidate)
			found = true
			break
		}
	}
	manager.mu.RUnlock()
	if !found {
		return USBNetMode{}, fmt.Errorf("no discovered device owns AT port %q", atPortPath)
	}
	client, err := manager.opener.Open(ctx, candidate.ATPort)
	if err != nil {
		return USBNetMode{}, err
	}
	defer func() { _ = client.Close() }()
	commandCtx, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	defer cancel()
	response, err := client.Execute(commandCtx, fmt.Sprintf(`AT+QCFG="usbnet",%d`, mode))
	if err != nil {
		return USBNetMode{}, err
	}
	if !response.OK() {
		return USBNetMode{}, &modem.CommandError{Command: response.Command, Final: response.Final, Lines: response.Lines}
	}
	return USBNetMode{Mode: mode, Name: usbNetModeName(mode)}, nil
}

func usbNetModeName(mode int) string {
	switch mode {
	case 0:
		return "QMI"
	case 1:
		return "ECM"
	case 2:
		return "MBIM"
	case 3:
		return "RNDIS"
	default:
		return "unknown"
	}
}

func (manager *Manager) OperatorSelection(ctx context.Context, id string) (OperatorSelection, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return OperatorSelection{}, err
	}
	candidate := manager.candidateFor(state)
	if isNativeQMICandidate(candidate) {
		state.opMu.Lock()
		defer state.opMu.Unlock()
		if err := manager.validateActive(id, state); err != nil {
			return OperatorSelection{}, err
		}
		return manager.nativeQMIOperatorSelectionLocked(ctx, candidate)
	}
	response, err := manager.ExecuteAT(ctx, id, "AT+COPS?")
	if err != nil {
		return OperatorSelection{}, err
	}
	return parseOperatorSelection(response)
}

func parseOperatorSelection(response modem.Response) (OperatorSelection, error) {
	values := csvValues(valueAfterPrefix(response, "+COPS:"))
	if len(values) < 1 {
		return OperatorSelection{}, errors.New("modem did not return operator selection state")
	}
	result := OperatorSelection{}
	result.Mode, _ = strconv.Atoi(values[0])
	if len(values) > 1 {
		result.Format, _ = strconv.Atoi(values[1])
	}
	if len(values) > 2 {
		result.Operator = strings.Trim(values[2], `"`)
	}
	if len(values) > 3 {
		result.AccessTechnology = accessTechnology(values[3])
	}
	return result, nil
}

func (manager *Manager) SetOperatorSelection(
	ctx context.Context,
	id string,
	automatic bool,
	plmn string,
	accessTechnologyValue *int,
) (OperatorSelection, error) {
	result := OperatorSelection{Mode: 0}
	command := ""
	if !automatic {
		plmn = strings.TrimSpace(plmn)
		if len(plmn) < 5 || len(plmn) > 6 || strings.IndexFunc(plmn, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return OperatorSelection{}, errors.New("operator PLMN must contain 5 or 6 digits")
		}
		// Mode 1 is a real manual lock. Mode 4 is only a manual attempt with
		// automatic fallback; using it made a rejected registration silently
		// return to COPS=0 while the UI incorrectly reported a successful lock.
		command = fmt.Sprintf(`AT+COPS=1,2,"%s"`, plmn)
		actName := ""
		if accessTechnologyValue != nil {
			if *accessTechnologyValue < 0 || *accessTechnologyValue > 9 {
				return OperatorSelection{}, errors.New("invalid operator access technology")
			}
			command += fmt.Sprintf(",%d", *accessTechnologyValue)
			actName = accessTechnology(strconv.Itoa(*accessTechnologyValue))
		}
		result = OperatorSelection{Mode: 1, Format: 2, Operator: plmn, AccessTechnology: actName}
	}
	state, err := manager.lookup(id)
	if err != nil {
		return OperatorSelection{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return OperatorSelection{}, err
	}
	candidate := manager.candidateFor(state)
	if isNativeQMICandidate(candidate) {
		selection, err := manager.setNativeQMIOperatorSelectionLocked(
			ctx,
			candidate,
			automatic,
			plmn,
			accessTechnologyValue,
		)
		manager.setResult(id, state, nil, err)
		return selection, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return OperatorSelection{}, err
	}
	// Manual PLMN selection makes the modem search for and register on the
	// requested network, which can take tens of seconds — far longer than the
	// normal command timeout. Use the same deadline budget as operator scan so
	// the lock is not aborted while registration is still in progress.
	lockCtx, cancel := manager.withTimeout(ctx, manager.scanTimeout)
	defer cancel()
	if automatic {
		result, err = restoreAutomaticOperatorSelection(lockCtx, client)
		manager.setResult(id, state, nil, err)
		return result, err
	}
	response, err := client.Execute(lockCtx, command)
	if err != nil || !response.OK() {
		if err == nil {
			err = &modem.CommandError{Command: response.Command, Final: response.Final, Lines: response.Lines}
		}
		rollbackOperatorSelection(manager, client)
		wrapped := fmt.Errorf("manual operator selection failed and automatic selection was restored: %w", err)
		manager.setResult(id, state, nil, wrapped)
		return OperatorSelection{}, wrapped
	}
	actual, err := queryOperatorSelection(lockCtx, client)
	if err != nil {
		rollbackOperatorSelection(manager, client)
		manager.setResult(id, state, nil, err)
		return OperatorSelection{}, fmt.Errorf("verify manual operator selection: %w", err)
	}
	if actual.Mode != 1 || actual.Operator != plmn {
		rollbackOperatorSelection(manager, client)
		err := fmt.Errorf("network %s did not accept registration; automatic selection was restored (modem reported mode=%d operator=%q)", plmn, actual.Mode, actual.Operator)
		manager.setResult(id, state, nil, err)
		return OperatorSelection{}, err
	}
	result = actual
	manager.setResult(id, state, nil, nil)
	return result, nil
}

func queryOperatorSelection(ctx context.Context, client modem.Client) (OperatorSelection, error) {
	response, err := client.Execute(ctx, "AT+COPS?")
	if err != nil {
		return OperatorSelection{}, err
	}
	if !response.OK() {
		return OperatorSelection{}, &modem.CommandError{Command: response.Command, Final: response.Final, Lines: response.Lines}
	}
	return parseOperatorSelection(response)
}

// restoreAutomaticOperatorSelection clears both a manual PLMN latch and an
// old RAT-only scan restriction. The latter is important on EC20 modules:
// COPS=0 alone can remain effectively LTE-only after an earlier lock, unlike a
// phone's normal automatic GSM/WCDMA/LTE acquisition policy.
func restoreAutomaticOperatorSelection(ctx context.Context, client modem.Client) (OperatorSelection, error) {
	// Older firmware may not implement nwscanmode; COPS auto is still useful in
	// that case, so this compatibility reset is best effort.
	_, _ = client.Execute(ctx, `AT+QCFG="nwscanmode",0,1`)
	_, _ = client.Execute(ctx, "AT+COPS=2")
	response, err := client.Execute(ctx, "AT+COPS=0")
	if err != nil {
		return OperatorSelection{}, err
	}
	if !response.OK() {
		return OperatorSelection{}, &modem.CommandError{Command: response.Command, Final: response.Final, Lines: response.Lines}
	}
	actual, err := queryOperatorSelection(ctx, client)
	if err != nil {
		return OperatorSelection{}, fmt.Errorf("verify automatic operator selection: %w", err)
	}
	if actual.Mode != 0 {
		return OperatorSelection{}, fmt.Errorf("modem did not enter automatic operator selection (mode=%d operator=%q)", actual.Mode, actual.Operator)
	}
	return actual, nil
}

func rollbackOperatorSelection(manager *Manager, client modem.Client) {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), manager.longTimeout)
	defer cancel()
	_, _ = restoreAutomaticOperatorSelection(rollbackCtx, client)
}

// ReRegisterOperator detaches from the network and reapplies the modem's
// current automatic/manual selection. This is intentionally different from a
// passive refresh: it forces a new registration attempt without changing the
// user's lock policy.
func (manager *Manager) ReRegisterOperator(ctx context.Context, id string) (OperatorSelection, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return OperatorSelection{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return OperatorSelection{}, err
	}
	candidate := manager.candidateFor(state)
	if isNativeQMICandidate(candidate) {
		selection, err := manager.reRegisterNativeQMIOperatorLocked(ctx, candidate)
		manager.setResult(id, state, nil, err)
		return selection, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return OperatorSelection{}, err
	}
	longCtx, cancel := manager.withTimeout(ctx, manager.scanTimeout)
	defer cancel()

	current, err := queryOperatorSelection(longCtx, client)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return OperatorSelection{}, err
	}
	manual := current.Mode == 1 || current.Mode == 4
	if manual && !decimalPLMN(current.Operator) {
		response, formatErr := client.Execute(longCtx, "AT+COPS=3,2")
		if formatErr != nil || !response.OK() {
			if formatErr == nil {
				formatErr = &modem.CommandError{Command: response.Command, Final: response.Final, Lines: response.Lines}
			}
			manager.setResult(id, state, nil, formatErr)
			return OperatorSelection{}, formatErr
		}
		current, err = queryOperatorSelection(longCtx, client)
		if err != nil {
			manager.setResult(id, state, nil, err)
			return OperatorSelection{}, err
		}
		manual = current.Mode == 1 || current.Mode == 4
	}

	if !manual {
		result, restoreErr := restoreAutomaticOperatorSelection(longCtx, client)
		manager.setResult(id, state, nil, restoreErr)
		return result, restoreErr
	}
	desired := ""
	if manual {
		if !decimalPLMN(current.Operator) {
			return OperatorSelection{}, errors.New("current manual operator is not available as a numeric PLMN")
		}
		desired = fmt.Sprintf(`AT+COPS=1,2,"%s"`, current.Operator)
		if code, ok := accessTechnologyCode(current.AccessTechnology); ok {
			desired += fmt.Sprintf(",%d", code)
		}
	}
	for _, command := range []string{"AT+COPS=2", desired} {
		response, executeErr := client.Execute(longCtx, command)
		if executeErr != nil {
			manager.setResult(id, state, nil, executeErr)
			return OperatorSelection{}, executeErr
		}
		if !response.OK() {
			executeErr = &modem.CommandError{Command: response.Command, Final: response.Final, Lines: response.Lines}
			manager.setResult(id, state, nil, executeErr)
			return OperatorSelection{}, executeErr
		}
	}
	result, err := queryOperatorSelection(longCtx, client)
	manager.setResult(id, state, nil, err)
	if err != nil {
		return OperatorSelection{}, err
	}
	return result, nil
}

func decimalPLMN(value string) bool {
	value = strings.TrimSpace(value)
	return (len(value) == 5 || len(value) == 6) && strings.IndexFunc(value, func(r rune) bool {
		return r < '0' || r > '9'
	}) < 0
}

func accessTechnologyCode(name string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "GSM":
		return 0, true
	case "UTRAN":
		return 2, true
	case "EDGE":
		return 3, true
	case "HSDPA":
		return 4, true
	case "HSUPA":
		return 5, true
	case "HSPA":
		return 6, true
	case "LTE":
		return 7, true
	case "NR5G":
		return 9, true
	default:
		return 0, false
	}
}
