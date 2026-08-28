package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"vocat/internal/modem"
)

// USSD dialog states derived from the +CUSD <n> result code.
const (
	ussdStatusFinal         = "final"          // 0: no further user action required
	ussdStatusAwaitingInput = "awaiting_input" // 1: network expects more input
	ussdStatusTerminated    = "terminated"     // 2: terminated by the network
	ussdStatusFailed        = "failed"         // 3/4/5: local answer, unsupported, or timeout
)

// USSD begins a USSD dialog. When the network expects more input the result
// carries an "awaiting_input" status and a session id that can be continued or
// cancelled; otherwise the dialog is complete and self-contained.
func (manager *Manager) USSD(
	ctx context.Context,
	id string,
	code string,
) (USSDResult, error) {
	code = strings.TrimSpace(code)
	if !validServiceCode(code) {
		return USSDResult{}, errors.New("invalid USSD service code")
	}
	result, err := manager.runUSSD(ctx, id, fmt.Sprintf(`AT+CUSD=1,"%s",15`, code))
	if err != nil {
		return result, err
	}
	result.Code = code
	if result.Status == ussdStatusAwaitingInput {
		result.SessionID = manager.openUSSDSession(id)
		result.Continueable = true
	}
	return result, nil
}

// ContinueUSSD sends follow-up input on an open USSD dialog.
func (manager *Manager) ContinueUSSD(
	ctx context.Context,
	sessionID string,
	input string,
) (USSDResult, error) {
	deviceID, err := manager.ussdSessionDevice(sessionID)
	if err != nil {
		return USSDResult{}, err
	}
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 182 || strings.ContainsAny(input, "\"\r\n") {
		return USSDResult{}, errors.New("invalid USSD input")
	}
	result, err := manager.runUSSD(ctx, deviceID, fmt.Sprintf(`AT+CUSD=1,"%s",15`, input))
	if err != nil {
		return result, err
	}
	result.Code = input
	if result.Status == ussdStatusAwaitingInput {
		result.SessionID = sessionID
		result.Continueable = true
	} else {
		manager.dropUSSDSession(sessionID)
	}
	return result, nil
}

// CancelUSSD terminates an open USSD dialog with AT+CUSD=2. A session abort
// returns OK without a +CUSD result, so unlike start/continue it does not wait
// for an unsolicited response.
func (manager *Manager) CancelUSSD(ctx context.Context, sessionID string) error {
	deviceID, err := manager.ussdSessionDevice(sessionID)
	if err != nil {
		return err
	}
	defer manager.dropUSSDSession(sessionID)
	state, err := manager.lookup(deviceID)
	if err != nil {
		return err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(deviceID, state); err != nil {
		return err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(deviceID, state, nil, err)
		return err
	}
	commandCtx, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	_, err = client.Execute(commandCtx, "AT+CUSD=2")
	cancel()
	manager.setResult(deviceID, state, nil, err)
	return err
}

// runUSSD issues one CUSD command and waits for the +CUSD unsolicited result.
func (manager *Manager) runUSSD(
	ctx context.Context,
	id string,
	command string,
) (USSDResult, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return USSDResult{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return USSDResult{}, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return USSDResult{}, err
	}

	commandCtx, cancelCommand := manager.withTimeout(ctx, manager.commandTimeout)
	_, err = client.Execute(commandCtx, command)
	cancelCommand()
	if err != nil {
		manager.setResult(id, state, nil, err)
		return USSDResult{}, err
	}
	waitCtx, cancelWait := manager.withTimeout(ctx, manager.longTimeout)
	defer cancelWait()
	line, err := client.WaitURC(waitCtx, func(line string) bool {
		return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+CUSD:")
	})
	if err != nil {
		manager.setResult(id, state, nil, err)
		return USSDResult{}, err
	}
	result, err := parseUSSDResponse(line)
	manager.setResult(id, state, nil, err)
	return result, err
}

func (manager *Manager) openUSSDSession(deviceID string) string {
	var token [8]byte
	_, _ = rand.Read(token[:])
	id := hex.EncodeToString(token[:])
	manager.mu.Lock()
	manager.ussdSessions[id] = ussdSession{deviceID: deviceID, createdAt: time.Now().UTC()}
	manager.mu.Unlock()
	return id
}

func (manager *Manager) ussdSessionDevice(sessionID string) (string, error) {
	manager.mu.RLock()
	session, ok := manager.ussdSessions[strings.TrimSpace(sessionID)]
	manager.mu.RUnlock()
	if !ok {
		return "", ErrUSSDSessionNotFound
	}
	return session.deviceID, nil
}

func (manager *Manager) dropUSSDSession(sessionID string) {
	manager.mu.Lock()
	delete(manager.ussdSessions, strings.TrimSpace(sessionID))
	manager.mu.Unlock()
}

// parseUSSDResponse parses a +CUSD unsolicited result line, capturing the dialog
// state byte, the decoded text, and the data coding scheme.
func parseUSSDResponse(line string) (USSDResult, error) {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+CUSD:") {
		return USSDResult{}, errors.New("invalid CUSD response")
	}
	values := csvValues(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
	if len(values) < 2 {
		return USSDResult{}, errors.New("CUSD response has no text")
	}
	code, _ := strconv.Atoi(strings.TrimSpace(values[0]))
	payload := strings.Trim(values[1], `"`)
	var dcs *int
	if len(values) >= 3 {
		if value, err := strconv.Atoi(values[2]); err == nil {
			dcs = intPointer(value)
		}
	}
	text := payload
	if (dcs != nil && *dcs == 72) || looksLikeUCS2(payload) {
		if decoded := decodeUCS2(payload); decoded != "" {
			text = decoded
		}
	}
	return USSDResult{
		Text:   text,
		Raw:    line,
		DCS:    dcs,
		Status: ussdStatusFromCode(code),
	}, nil
}

func ussdStatusFromCode(code int) string {
	switch code {
	case 0:
		return ussdStatusFinal
	case 1:
		return ussdStatusAwaitingInput
	case 2:
		return ussdStatusTerminated
	default:
		return ussdStatusFailed
	}
}

func looksLikeUCS2(value string) bool {
	if len(value) < 4 || len(value)%4 != 0 {
		return false
	}
	if _, err := hex.DecodeString(value); err != nil {
		return false
	}
	return strings.HasPrefix(value, "00") ||
		strings.IndexFunc(value, func(character rune) bool {
			return character >= 'A' && character <= 'F' ||
				character >= 'a' && character <= 'f'
		}) >= 0
}

func decodeUCS2(value string) string {
	if len(value) < 4 || len(value)%4 != 0 {
		return ""
	}
	units := make([]uint16, 0, len(value)/4)
	for index := 0; index < len(value); index += 4 {
		unit, err := strconv.ParseUint(value[index:index+4], 16, 16)
		if err != nil {
			return ""
		}
		units = append(units, uint16(unit))
	}
	return string(utf16.Decode(units))
}

func validServiceCode(value string) bool {
	if value == "" || len(value) > 40 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character != '*' && character != '#' && character != '+' {
				return false
			}
		}
	}
	return true
}

func (manager *Manager) SetFlight(
	ctx context.Context,
	id string,
	enabled bool,
) (FlightResult, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return FlightResult{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return FlightResult{}, err
	}
	if manager.candidateFor(state).HardwareKind == "pcsc" {
		return FlightResult{PreviousMode: 4, CurrentMode: 4, FlightMode: true, RadioOff: true}, nil
	}
	if result, handled, err := manager.setNativeQMIFlight(ctx, id, state, enabled); handled {
		manager.setResult(id, state, nil, err)
		return result, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return FlightResult{}, err
	}
	previous, err := manager.readOperatingMode(ctx, client)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return FlightResult{}, err
	}
	target := previous
	if enabled {
		if !isRadioOffMode(previous) {
			saved := previous
			state.preFlightMode = &saved
			target = 4
		}
	} else if isRadioOffMode(previous) {
		target = 1
		if state.preFlightMode != nil && !isRadioOffMode(*state.preFlightMode) {
			target = *state.preFlightMode
		}
	}

	changed := target != previous
	current := previous
	currentKnown := false
	if changed {
		if enabled {
			// Entering RF-off tears down the packet service. Forget the previous
			// WDS generation before issuing CFUN so a later enable starts cleanly.
			state.dataMu.Lock()
			invalidateQMINetworkSession(state, manager.candidateFor(state))
			state.dataMu.Unlock()
		}
		if _, err := manager.command(
			ctx,
			client,
			fmt.Sprintf("AT+CFUN=%d", target),
		); err != nil {
			_, recoveredMode, recoveryErr := manager.recoverOperatingModeAfterTransportLoss(
				ctx, id, state, client, target, err,
			)
			if recoveryErr != nil {
				manager.setResult(id, state, nil, recoveryErr)
				return FlightResult{
					PreviousMode: previous,
					CurrentMode:  previous,
					FlightMode:   isRadioOffMode(previous),
					RadioOff:     isRadioOffMode(previous),
				}, recoveryErr
			}
			current = recoveredMode
			currentKnown = true
		}
	}
	if !currentKnown {
		current, err = manager.readOperatingMode(ctx, client)
		if err != nil {
			recoveredClient, recoveredMode, recoveryErr := manager.recoverOperatingModeAfterTransportLoss(
				ctx, id, state, client, target, err,
			)
			if recoveryErr != nil {
				manager.setResult(id, state, nil, recoveryErr)
				return FlightResult{
					PreviousMode: previous,
					CurrentMode:  target,
					Changed:      changed,
					FlightMode:   isRadioOffMode(target),
					RadioOff:     isRadioOffMode(target),
				}, recoveryErr
			}
			client = recoveredClient
			current = recoveredMode
		}
	}
	if !enabled && !isRadioOffMode(current) {
		state.preFlightMode = nil
	}
	manager.updateSnapshotMode(id, state, current)
	manager.setResult(id, state, nil, nil)
	return FlightResult{
		PreviousMode: previous,
		CurrentMode:  current,
		Changed:      changed,
		FlightMode:   isRadioOffMode(current),
		RadioOff:     isRadioOffMode(current),
	}, nil
}

// recoverOperatingModeAfterTransportLoss handles modems that commit CFUN and
// then briefly disappear from USB before returning the final AT response. It
// is deliberately limited to poisoned transports and only reports success
// after a fresh session reads back the requested mode.
func (manager *Manager) recoverOperatingModeAfterTransportLoss(
	ctx context.Context,
	id string,
	state *managedDevice,
	client modem.Client,
	target int,
	commandErr error,
) (modem.Client, int, error) {
	poisoned, ok := client.(modem.PoisonedClient)
	if !ok || !poisoned.Poisoned() {
		return nil, 0, commandErr
	}

	_ = client.Close()
	state.client = nil

	recoveryCtx, cancel := manager.withTimeout(ctx, manager.longTimeout)
	defer cancel()
	lastErr := commandErr
	for {
		manager.rediscoverCandidateForRecovery(recoveryCtx, id, state)
		reopened, err := manager.clientLocked(recoveryCtx, state, manager.candidateFor(state))
		if err == nil {
			mode, readErr := manager.readOperatingMode(recoveryCtx, reopened)
			if readErr == nil && mode == target {
				return reopened, mode, nil
			}
			if readErr != nil {
				lastErr = readErr
			} else {
				lastErr = fmt.Errorf("modem operating mode is %d, want %d", mode, target)
			}
			if poisoned, ok := reopened.(modem.PoisonedClient); ok && poisoned.Poisoned() {
				_ = reopened.Close()
				state.client = nil
			}
		} else {
			lastErr = err
		}

		select {
		case <-recoveryCtx.Done():
			return nil, 0, fmt.Errorf(
				"%w; confirm AT+CFUN=%d after transport recovery: %v",
				commandErr, target, lastErr,
			)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (manager *Manager) rediscoverCandidateForRecovery(
	ctx context.Context,
	id string,
	state *managedDevice,
) {
	candidates, err := manager.discoverer.Discover(ctx)
	if err != nil {
		return
	}
	for _, candidate := range candidates {
		if candidate.ID != id {
			continue
		}
		manager.mu.Lock()
		if manager.devices[id] == state {
			state.candidate = candidate
			state.discovered = true
		}
		manager.mu.Unlock()
		return
	}
}

func (manager *Manager) readOperatingMode(
	ctx context.Context,
	client modem.Client,
) (int, error) {
	response, err := manager.command(ctx, client, "AT+CFUN?")
	if err != nil {
		return 0, err
	}
	mode, ok := parseCFUN(response)
	if !ok {
		return 0, errors.New("modem did not return a valid +CFUN value")
	}
	return mode, nil
}

func (manager *Manager) updateSnapshotMode(
	id string,
	state *managedDevice,
	mode int,
) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.devices[id] != state || state.snapshot == nil {
		return
	}
	state.snapshot.OperatingMode = mode
	state.snapshot.ModeKnown = true
	state.snapshot.FlightMode = isRadioOffMode(mode)
	state.snapshot.RadioOff = state.snapshot.FlightMode
}
