package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
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

// ReadSMSCenter reads the SIM-provisioned service-centre address through the
// modem's read-only AT interface. Native QMI devices still expose a companion
// AT port, and SMS-over-IMS needs this value to address RP-DATA submissions.
func (mapper ATMapper) ReadSMSCenter(ctx context.Context, configuredID string) (string, error) {
	response, err := mapper.ExecuteAT(ctx, configuredID, "AT+CSCA?")
	if err != nil {
		return "", fmt.Errorf("read SMS service centre: %w", err)
	}
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+CSCA:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "+CSCA:"))
		if comma := strings.IndexByte(value, ','); comma >= 0 {
			value = value[:comma]
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		digits := strings.TrimPrefix(value, "+")
		if len(digits) < 3 || len(digits) > 20 {
			break
		}
		valid := true
		for _, digit := range digits {
			if digit < '0' || digit > '9' {
				valid = false
				break
			}
		}
		if valid {
			return value, nil
		}
		break
	}
	return "", errors.New("modem returned no valid SMS service-centre address")
}

// ReadSIMMetadata reuses the device manager's per-ICCID EF cache. VoWiFi
// identity discovery therefore gains Android-style SPN/GID MVNO selectors
// without issuing duplicate APDUs on every reconnect.
func (mapper ATMapper) ReadSIMMetadata(ctx context.Context, configuredID string) (vowifi.SIMMetadata, error) {
	physicalID, err := mapper.resolve(ctx, configuredID)
	if err != nil {
		return vowifi.SIMMetadata{}, err
	}
	entry, err := mapper.Devices.Get(physicalID)
	if err != nil {
		return vowifi.SIMMetadata{}, err
	}
	if entry.Snapshot == nil {
		return vowifi.SIMMetadata{}, nil
	}
	return vowifi.SIMMetadata{
		SPN:  strings.TrimSpace(entry.Snapshot.SPN),
		GID1: strings.TrimSpace(entry.Snapshot.GID1),
		GID2: strings.TrimSpace(entry.Snapshot.GID2),
	}, nil
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
