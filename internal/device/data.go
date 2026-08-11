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
	// qmi-network sources its profile as shell syntax for both start and stop.
	// Validate an optional APN on every path so a disable request cannot inject
	// additional profile assignments or commands.
	if apn != "" && !apnPattern.MatchString(apn) {
		return NetworkResult{}, ErrInvalidNetworkAPN
	}
	ipVersion := normalizeIPVersion(request.IPVersion)
	if ipVersion == "" {
		return NetworkResult{}, errors.New("IP version must be IP, IPV6, or IPV4V6")
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
	candidate := manager.candidateFor(state)
	if candidate.QMIControl != "" && candidate.NetworkInterface != "" {
		return setQMINetwork(ctx, candidate, request.Enabled, apn, ipVersion)
	}

	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return NetworkResult{}, err
	}
	if request.Enabled {
		commands := []string{
			fmt.Sprintf(`AT+CGDCONT=1,"%s","%s"`, ipVersion, apn),
			"AT+CGATT=1",
			"AT+CGACT=1,1",
		}
		for _, command := range commands {
			if _, err := manager.command(ctx, client, command); err != nil {
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
