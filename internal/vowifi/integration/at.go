package integration

import (
	"context"
	"errors"
	"strings"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
)

type ATDeviceController interface {
	Get(string) (device.Device, error)
	List() []device.Device
	ExecuteAT(context.Context, string, string) (modem.Response, error)
	ExecuteSensitiveAT(context.Context, string, string) (modem.Response, error)
}

// ATMapper lets runtime IDs remain stable configuration IDs even when Linux
// re-enumerates a physical modem under a discovery-derived ID.
type ATMapper struct {
	Store   *store.Store
	Devices ATDeviceController
}

type uiccLocker interface {
	LockUICC()
	UnlockUICC()
}

func (mapper ATMapper) LockUICC() {
	if locker, ok := mapper.Devices.(uiccLocker); ok {
		locker.LockUICC()
	}
}

func (mapper ATMapper) UnlockUICC() {
	if locker, ok := mapper.Devices.(uiccLocker); ok {
		locker.UnlockUICC()
	}
}

func (mapper ATMapper) Get(configuredID string) (device.Device, error) {
	physicalID, err := mapper.resolve(context.Background(), configuredID)
	if err != nil {
		return device.Device{}, err
	}
	return mapper.Devices.Get(physicalID)
}

func (mapper ATMapper) ExecuteAT(
	ctx context.Context,
	configuredID string,
	command string,
) (modem.Response, error) {
	physicalID, err := mapper.resolve(ctx, configuredID)
	if err != nil {
		return modem.Response{}, err
	}
	return mapper.Devices.ExecuteAT(ctx, physicalID, command)
}

func (mapper ATMapper) ExecuteSensitiveAT(
	ctx context.Context,
	configuredID string,
	command string,
) (modem.Response, error) {
	physicalID, err := mapper.resolve(ctx, configuredID)
	if err != nil {
		return modem.Response{}, err
	}
	return mapper.Devices.ExecuteSensitiveAT(ctx, physicalID, command)
}

func (mapper ATMapper) resolve(
	ctx context.Context,
	configuredID string,
) (string, error) {
	if mapper.Store == nil || mapper.Devices == nil {
		return "", errors.New("vowifi AT mapper is not configured")
	}
	config, err := mapper.Store.Device(ctx, configuredID)
	if err != nil {
		return "", err
	}
	// Score every physical candidate before choosing one. Returning the first
	// partial match made two EC20s on the same hub vulnerable to iteration order:
	// a stale USB path on one configured row could win before the other
	// candidate's AT port, QMI node, or live IMEI was considered. The result was
	// two logical devices issuing APDUs to the same SIM.
	bestID := ""
	bestScore := 0
	for _, entry := range mapper.Devices.List() {
		if !entry.Discovered {
			continue
		}
		score := physicalMatchScore(configuredID, config, entry)
		if score > bestScore {
			bestID = entry.ID
			bestScore = score
		}
	}
	if bestID != "" {
		return bestID, nil
	}
	return "", device.ErrNotFound
}

func physicalMatchScore(configuredID string, config store.Device, entry device.Device) int {
	candidate := entry.Candidate
	score := 0
	// A live modem identity is the strongest evidence and must override stale
	// Linux node names or a USB topology saved before devices were rearranged.
	if config.ModemIMEI != "" && entry.Snapshot != nil &&
		strings.EqualFold(strings.TrimSpace(config.ModemIMEI), strings.TrimSpace(entry.Snapshot.IMEI)) {
		score += 10000
	}
	if config.ATPort != "" &&
		(config.ATPort == candidate.ATPort.Path || config.ATPort == candidate.ATPort.OpenPath()) {
		score += 300
	}
	if config.ControlDevice != "" &&
		(config.ControlDevice == candidate.QMIControl || config.ControlDevice == candidate.ATPort.OpenPath()) {
		score += 300
	}
	if config.USBPath != "" && config.USBPath == candidate.USBPath {
		score += 100
	}
	// Discovery IDs are not persistent user IDs. Treat an exact text match only
	// as a weak hint so it cannot override physical identity evidence.
	if entry.ID == configuredID {
		score += 25
	}
	return score
}
