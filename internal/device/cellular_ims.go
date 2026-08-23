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

// CellularIMSStatus is the Quectel baseband IMS override and reported VoLTE
// capability. Mode is the persistent module configuration; Registered is kept
// for API compatibility with PR #77 and mirrors the modem's VoLTE capability.
type CellularIMSStatus struct {
	Supported    bool            `json:"supported"`
	Mode         CellularIMSMode `json:"mode"`
	Configured   bool            `json:"configured"`
	Registered   bool            `json:"registered"`
	VoLTECapable bool            `json:"volteCapable"`
	CSKnown      bool            `json:"csKnown"`
	CSRegistered bool            `json:"csRegistered"`
	Changed      bool            `json:"changed,omitempty"`
	Rebooting    bool            `json:"rebooting,omitempty"`
}

type CellularIMSMode string

const (
	CellularIMSModeMBNDefault    CellularIMSMode = "mbn_default"
	CellularIMSModeForceEnabled  CellularIMSMode = "force_enabled"
	CellularIMSModeForceDisabled CellularIMSMode = "force_disabled"
)

func (mode CellularIMSMode) qcfgValue() (int, error) {
	switch mode {
	case CellularIMSModeMBNDefault:
		return 0, nil
	case CellularIMSModeForceEnabled:
		return 1, nil
	case CellularIMSModeForceDisabled:
		return 2, nil
	default:
		return 0, fmt.Errorf("invalid cellular IMS mode %q", mode)
	}
}

func cellularIMSModeFromQCFG(value int) (CellularIMSMode, error) {
	switch value {
	case 0:
		return CellularIMSModeMBNDefault, nil
	case 1:
		return CellularIMSModeForceEnabled, nil
	case 2:
		return CellularIMSModeForceDisabled, nil
	default:
		return "", fmt.Errorf("unsupported cellular IMS mode %d", value)
	}
}

var cellularIMSLine = regexp.MustCompile(`(?i)^\+QCFG:\s*"ims"\s*,\s*([012])(?:\s*,\s*([01]))?\s*$`)
var cellularCSLine = regexp.MustCompile(`(?i)^\+CREG:\s*(?:\d+\s*,\s*)?([0-9]+)(?:\s*,.*)?$`)

func parseCellularCSRegistration(lines []string) (registered, known bool) {
	for _, line := range lines {
		matches := cellularCSLine.FindStringSubmatch(strings.TrimSpace(line))
		if matches == nil {
			continue
		}
		status, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}
		return status == 1 || status == 5, true
	}
	return false, false
}

func parseCellularIMSStatus(lines []string) (CellularIMSStatus, error) {
	for _, line := range lines {
		matches := cellularIMSLine.FindStringSubmatch(strings.TrimSpace(line))
		if matches == nil {
			continue
		}
		configured, _ := strconv.Atoi(matches[1])
		mode, err := cellularIMSModeFromQCFG(configured)
		if err != nil {
			return CellularIMSStatus{}, err
		}
		registered := 0
		if len(matches) > 2 && matches[2] != "" {
			registered, _ = strconv.Atoi(matches[2])
		}
		return CellularIMSStatus{
			Supported: true, Mode: mode, Configured: configured == 1,
			Registered: registered == 1, VoLTECapable: registered == 1,
		}, nil
	}
	return CellularIMSStatus{}, errors.New("modem did not return a Quectel IMS status")
}

func (manager *Manager) CellularIMS(ctx context.Context, id string) (CellularIMSStatus, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return CellularIMSStatus{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return CellularIMSStatus{}, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return CellularIMSStatus{}, err
	}
	status, err := manager.readCellularIMS(ctx, client)
	if err == nil {
		if response, csErr := manager.command(ctx, client, "AT+CREG?"); csErr == nil {
			status.CSRegistered, status.CSKnown = parseCellularCSRegistration(response.Lines)
		}
	}
	manager.setResult(id, state, nil, err)
	return status, err
}

// SetCellularIMS changes the persistent Quectel IMS override. A full modem
// restart is issued only when the configured value changes; this is essential
// because QCFG may report the new setting before the baseband has loaded it.
func (manager *Manager) SetCellularIMS(ctx context.Context, id string, mode CellularIMSMode) (CellularIMSStatus, error) {
	target, err := mode.qcfgValue()
	if err != nil {
		return CellularIMSStatus{}, err
	}
	state, err := manager.lookup(id)
	if err != nil {
		return CellularIMSStatus{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return CellularIMSStatus{}, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return CellularIMSStatus{}, err
	}
	status, err := manager.readCellularIMS(ctx, client)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return CellularIMSStatus{}, err
	}
	if status.Mode == mode {
		manager.setResult(id, state, nil, nil)
		return status, nil
	}
	if _, err = manager.command(ctx, client, fmt.Sprintf(`AT+QCFG="ims",%d`, target)); err != nil {
		manager.setResult(id, state, nil, err)
		return CellularIMSStatus{}, err
	}
	status, err = manager.readCellularIMS(ctx, client)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return CellularIMSStatus{}, err
	}
	if status.Mode != mode {
		err = errors.New("modem did not retain the requested IMS setting")
		manager.setResult(id, state, nil, err)
		return CellularIMSStatus{}, err
	}
	status.Changed = true
	status.Rebooting = true
	// A CFUN reset destroys every WDS client and packet-data handle. Discard the
	// owned session before the reset so the restore path cannot use a handle
	// from the previous baseband generation.
	state.dataMu.Lock()
	invalidateQMINetworkSession(state, manager.candidateFor(state))
	state.dataMu.Unlock()
	rebootCtx, cancel := manager.withTimeout(ctx, manager.longTimeout)
	_, err = client.Execute(rebootCtx, "AT+CFUN=1,1")
	cancel()
	if closeErr := client.Close(); err == nil {
		err = closeErr
	}
	state.client = nil
	state.preFlightMode = nil
	manager.clearSnapshot(id, state)
	manager.setResult(id, state, nil, err)
	return status, err
}

func (manager *Manager) readCellularIMS(ctx context.Context, client modem.Client) (CellularIMSStatus, error) {
	response, err := manager.command(ctx, client, `AT+QCFG="ims"`)
	if err != nil {
		return CellularIMSStatus{}, fmt.Errorf("query cellular IMS: %w", err)
	}
	status, err := parseCellularIMSStatus(response.Lines)
	if err != nil {
		return CellularIMSStatus{}, err
	}
	return status, nil
}
