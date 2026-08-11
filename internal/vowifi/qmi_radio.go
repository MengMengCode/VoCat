package vowifi

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"vocat/internal/device"
)

// QMIManagerRadioDeviceManager is the narrow device.Manager surface needed by
// the native Qualcomm radio lifecycle. Keeping the interface here lets the
// lifecycle be verified without opening a real QMI control device.
type QMIManagerRadioDeviceManager interface {
	Get(string) (device.Device, error)
	Refresh(context.Context, string) (device.Snapshot, error)
	SetFlight(context.Context, string, bool) (device.FlightResult, error)
	SetNetwork(context.Context, string, device.NetworkRequest) (device.NetworkResult, error)
}

type QMIManagerRadioOptions struct {
	// ResolveDeviceID maps the stable configured device id to the live WWAN id
	// owned by device.Manager. When omitted, the id is already a live id.
	ResolveDeviceID func(context.Context, string) (string, error)

	// PureAirplanePolicy distinguishes a user-requested radio-off state from a
	// stale transactional RF-off state left by an interrupted VoWiFi process.
	PureAirplanePolicy func(string) bool
}

// QMIManagerRadio routes every mutating native-410 radio operation through
// device.Manager. device.Manager selects QMI DMS for native WWAN candidates,
// so teardown can never fall back to the unsupported AT+CFUN=1 path.
type QMIManagerRadio struct {
	manager QMIManagerRadioDeviceManager
	options QMIManagerRadioOptions
}

var _ RadioController = (*QMIManagerRadio)(nil)

func NewQMIManagerRadio(
	manager QMIManagerRadioDeviceManager,
	options QMIManagerRadioOptions,
) (*QMIManagerRadio, error) {
	if manager == nil {
		return nil, errors.New("vocat: QMI radio device manager is required")
	}
	return &QMIManagerRadio{manager: manager, options: options}, nil
}

func (radio *QMIManagerRadio) resolve(
	ctx context.Context,
	configuredID string,
) (string, error) {
	configuredID = strings.TrimSpace(configuredID)
	if configuredID == "" {
		return "", errors.New("vocat: QMI radio device id is required")
	}
	resolved := configuredID
	if radio.options.ResolveDeviceID != nil {
		var err error
		resolved, err = radio.options.ResolveDeviceID(ctx, configuredID)
		if err != nil {
			return "", fmt.Errorf("resolve QMI radio device %q: %w", configuredID, err)
		}
	}
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return "", fmt.Errorf("resolve QMI radio device %q: empty live device id", configuredID)
	}
	return resolved, nil
}

func (radio *QMIManagerRadio) nativeDevice(
	ctx context.Context,
	configuredID string,
	requireNetwork bool,
) (string, device.Device, error) {
	deviceID, err := radio.resolve(ctx, configuredID)
	if err != nil {
		return "", device.Device{}, err
	}
	entry, err := radio.manager.Get(deviceID)
	if err != nil {
		return "", device.Device{}, fmt.Errorf("read QMI radio device %q: %w", deviceID, err)
	}
	if !entry.Discovered {
		return "", device.Device{}, fmt.Errorf("vocat: QMI radio device %q is not discovered", deviceID)
	}
	candidateID := strings.TrimSpace(entry.Candidate.ID)
	controlDevice := strings.TrimSpace(entry.Candidate.QMIControl)
	controlBase := filepath.Base(controlDevice)
	if candidateID == "" || !strings.HasPrefix(candidateID, "wwan") ||
		controlDevice == "" || !strings.HasPrefix(controlBase, candidateID+"qmi") {
		return "", device.Device{}, fmt.Errorf(
			"vocat: device %q is not a native WWAN QMI radio; refusing AT fallback",
			deviceID,
		)
	}
	if requireNetwork && strings.TrimSpace(entry.Candidate.NetworkInterface) == "" {
		return "", device.Device{}, fmt.Errorf(
			"vocat: native QMI radio %q has no network interface",
			deviceID,
		)
	}
	return deviceID, entry, nil
}

func (radio *QMIManagerRadio) Snapshot(
	ctx context.Context,
	configuredID string,
) (RadioSnapshot, error) {
	deviceID, entry, err := radio.nativeDevice(ctx, configuredID, false)
	if err != nil {
		return RadioSnapshot{}, err
	}

	var current device.Snapshot
	if entry.Snapshot == nil || !entry.Snapshot.ModeKnown {
		current, err = radio.manager.Refresh(ctx, deviceID)
		if err != nil {
			return RadioSnapshot{}, fmt.Errorf("refresh QMI radio device %q: %w", deviceID, err)
		}
	} else {
		current = *entry.Snapshot
	}
	if !current.ModeKnown || current.OperatingMode < 0 {
		return RadioSnapshot{}, fmt.Errorf("vocat: QMI radio device %q has no operating-mode evidence", deviceID)
	}

	pureAirplanePolicy := false
	if radio.options.PureAirplanePolicy != nil {
		pureAirplanePolicy = radio.options.PureAirplanePolicy(configuredID)
	}
	restoreMode := current.OperatingMode
	if !pureAirplanePolicy && isVoWiFiRFOffMode(restoreMode) {
		// No durable checkpoint survives a process restart. Without an explicit
		// airplane policy, a radio-off snapshot is residue from VoWiFi and the
		// safe teardown target is QMI DMS online.
		restoreMode = 1
	}
	return RadioSnapshot{
		// Native data teardown is deliberately fail-closed. VoWiFi does not
		// reactivate a previous qmi-network session during cleanup.
		CellularDataEnabled: false,
		OperatingMode:       restoreMode,
		PureAirplanePolicy:  pureAirplanePolicy,
	}, nil
}

func (radio *QMIManagerRadio) EnterVoWiFiRFOff(
	ctx context.Context,
	configuredID string,
) error {
	deviceID, _, err := radio.nativeDevice(ctx, configuredID, false)
	if err != nil {
		return err
	}
	result, err := radio.manager.SetFlight(ctx, deviceID, true)
	if err != nil {
		return fmt.Errorf("enter QMI VoWiFi RF-off mode: %w", err)
	}
	if !result.FlightMode && !result.RadioOff {
		return fmt.Errorf(
			"vocat: QMI radio remained online after RF-off request (mode %d)",
			result.CurrentMode,
		)
	}
	return nil
}

func (radio *QMIManagerRadio) StopCellularData(
	ctx context.Context,
	configuredID string,
) error {
	deviceID, _, err := radio.nativeDevice(ctx, configuredID, true)
	if err != nil {
		return err
	}
	result, err := radio.manager.SetNetwork(ctx, deviceID, device.NetworkRequest{
		Enabled:   false,
		IPVersion: "IP",
	})
	if err != nil {
		return fmt.Errorf("stop QMI cellular data: %w", err)
	}
	if result.Enabled {
		return errors.New("vocat: QMI cellular data remained enabled")
	}
	return nil
}

func (radio *QMIManagerRadio) Restore(
	ctx context.Context,
	configuredID string,
	snapshot RadioSnapshot,
) error {
	if snapshot.OperatingMode < 0 {
		return errors.New("vocat: invalid QMI radio snapshot")
	}
	deviceID, _, err := radio.nativeDevice(ctx, configuredID, false)
	if err != nil {
		return err
	}
	wantRadioOff := snapshot.PureAirplanePolicy || isVoWiFiRFOffMode(snapshot.OperatingMode)
	result, err := radio.manager.SetFlight(ctx, deviceID, wantRadioOff)
	if err != nil {
		return fmt.Errorf("restore QMI radio state: %w", err)
	}
	if wantRadioOff {
		if !result.FlightMode && !result.RadioOff {
			return fmt.Errorf(
				"vocat: QMI radio did not restore RF-off state (mode %d)",
				result.CurrentMode,
			)
		}
		return nil
	}
	if result.FlightMode || result.RadioOff || result.CurrentMode != 1 {
		return fmt.Errorf(
			"vocat: QMI radio did not restore online state (mode %d)",
			result.CurrentMode,
		)
	}
	return nil
}
