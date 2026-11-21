package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/pcsc"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type ATDeviceController interface {
	Get(string) (device.Device, error)
	List() []device.Device
	ExecuteAT(context.Context, string, string) (modem.Response, error)
	ExecuteSensitiveAT(context.Context, string, string) (modem.Response, error)
	ExecuteAPDU(context.Context, string, modem.APDUCommand, int, []byte) ([]byte, error)
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

func (mapper ATMapper) ExecuteAPDU(
	ctx context.Context,
	configuredID string,
	command modem.APDUCommand,
	channel int,
	apdu []byte,
) ([]byte, error) {
	physicalID, err := mapper.resolve(ctx, configuredID)
	if err != nil {
		return nil, err
	}
	return mapper.Devices.ExecuteAPDU(ctx, physicalID, command, channel, apdu)
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

// DeviceLister is the read side ResolvePhysicalID needs.
type DeviceLister interface {
	List() []device.Device
}

// ResolvePhysicalID maps a configured device ID to the live physical device
// ID using the same scored matching as the AT paths. Transports beyond the
// HTTP API (for example the MCP tool surface) resolve device addresses
// through this so every consumer picks the same modem.
func ResolvePhysicalID(
	ctx context.Context,
	database *store.Store,
	devices DeviceLister,
	configuredID string,
) (string, error) {
	if database == nil || devices == nil {
		return "", errors.New("physical device resolver is not configured")
	}
	config, err := database.Device(ctx, configuredID)
	if err != nil {
		return "", err
	}
	entry, err := MatchPhysicalDevice(config, devices.List())
	if err != nil {
		return "", err
	}
	return entry.ID, nil
}

// MatchPhysicalDevice scores physical identity evidence without changing the
// saved configuration.
func MatchPhysicalDevice(config store.Device, entries []device.Device) (device.Device, error) {
	// Score every physical candidate before choosing one. Returning the first
	// partial match made two EC20s on the same hub vulnerable to iteration order:
	// a stale USB path on one configured row could win before the other
	// candidate's AT port, QMI node, or live IMEI was considered. The result was
	// two logical devices issuing APDUs to the same SIM.
	var best device.Device
	bestScore := 0
	ambiguous := false
	readerConfig := config.DeviceBackend == pcsc.HardwareKind ||
		store.NormalizeDeviceType(config.DeviceType) == store.DeviceTypeUSBSIMReader
	for _, entry := range entries {
		if !entry.Discovered {
			continue
		}
		score := physicalMatchScore(config.ID, config, entry)
		if score > bestScore {
			best = entry
			bestScore = score
			ambiguous = false
		} else if readerConfig && score > 0 && score == bestScore {
			ambiguous = true
		}
	}
	if ambiguous {
		return device.Device{}, fmt.Errorf("%w: physical reader match is ambiguous", device.ErrNotFound)
	}
	if bestScore > 0 {
		return best, nil
	}
	return device.Device{}, device.ErrNotFound
}

func (mapper ATMapper) resolve(
	ctx context.Context,
	configuredID string,
) (string, error) {
	return ResolvePhysicalID(ctx, mapper.Store, mapper.Devices, configuredID)
}

func physicalMatchScore(configuredID string, config store.Device, entry device.Device) int {
	candidate := entry.Candidate
	if config.DeviceBackend == pcsc.HardwareKind ||
		store.NormalizeDeviceType(config.DeviceType) == store.DeviceTypeUSBSIMReader {
		if candidate.HardwareKind != pcsc.HardwareKind {
			return 0
		}
		selector := pcsc.Selector{USBPath: config.USBPath, ReaderName: config.ControlDevice}
		if strings.TrimSpace(selector.USBPath) == "" && strings.TrimSpace(selector.ReaderName) == "" && entry.ID == configuredID {
			return 25
		}
		return pcsc.ReaderMatchScore(pcsc.Reader{
			Name: candidate.ReaderName, USBPath: candidate.USBPath,
			DiscoveryIssue: candidate.DiscoveryIssue,
		}, selector)
	}
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
