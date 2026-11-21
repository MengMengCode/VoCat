package integration

import (
	"context"
	"errors"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/pcsc"
	"vocat/internal/store"
)

type fakeATDevices struct {
	entries     []device.Device
	executedID  string
	sensitiveID string
	apduID      string
	apduCommand modem.APDUCommand
	apduChannel int
	apdu        []byte
}

func (devices *fakeATDevices) Get(id string) (device.Device, error) {
	for _, entry := range devices.entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return device.Device{}, device.ErrNotFound
}

func (devices *fakeATDevices) List() []device.Device {
	return append([]device.Device(nil), devices.entries...)
}

func (devices *fakeATDevices) ExecuteAT(
	_ context.Context,
	id string,
	_ string,
) (modem.Response, error) {
	devices.executedID = id
	return modem.Response{Final: "OK"}, nil
}

func (devices *fakeATDevices) ExecuteSensitiveAT(
	_ context.Context,
	id string,
	_ string,
) (modem.Response, error) {
	devices.sensitiveID = id
	return modem.Response{Final: "OK"}, nil
}

func (devices *fakeATDevices) ExecuteAPDU(
	_ context.Context,
	id string,
	command modem.APDUCommand,
	channel int,
	apdu []byte,
) ([]byte, error) {
	devices.apduID = id
	devices.apduCommand = command
	devices.apduChannel = channel
	devices.apdu = append([]byte(nil), apdu...)
	return []byte{0x90, 0x00}, nil
}

func TestATMapperResolvesConfiguredIDByStableATPath(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:     "living-room",
		Name:   "EC20",
		ATPort: "/dev/serial/by-id/usb-ec20-if02",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &fakeATDevices{entries: []device.Device{{
		ID:         "usb-1-2",
		Discovered: true,
		Candidate: modem.Candidate{
			ATPort: modem.Port{
				Path:       "/dev/ttyUSB2",
				StablePath: "/dev/serial/by-id/usb-ec20-if02",
			},
		},
	}}}
	mapper := ATMapper{Store: database, Devices: devices}
	if _, err := mapper.ExecuteAT(
		context.Background(),
		"living-room",
		"AT",
	); err != nil {
		t.Fatal(err)
	}
	if devices.executedID != "usb-1-2" {
		t.Fatalf("ExecuteAT physical ID = %q", devices.executedID)
	}
	if _, err := mapper.ExecuteSensitiveAT(
		context.Background(),
		"living-room",
		"AT+CSIM=1",
	); err != nil {
		t.Fatal(err)
	}
	if devices.sensitiveID != "usb-1-2" {
		t.Fatalf("ExecuteSensitiveAT physical ID = %q", devices.sensitiveID)
	}
}

func TestATMapperExecuteAPDU(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:     "living-room",
		Name:   "ZXIC",
		ATPort: "/dev/serial/by-id/usb-zxic-if02",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &fakeATDevices{entries: []device.Device{{
		ID:         "usb-1-2",
		Discovered: true,
		Candidate: modem.Candidate{ATPort: modem.Port{
			Path:       "/dev/ttyUSB2",
			StablePath: "/dev/serial/by-id/usb-zxic-if02",
		}},
	}}}
	mapper := ATMapper{Store: database, Devices: devices}
	apdu := []byte{0x00, 0x88, 0x00, 0x81}
	response, err := mapper.ExecuteAPDU(context.Background(), "living-room", modem.APDUCGLA, 2, apdu)
	if err != nil {
		t.Fatalf("ExecuteAPDU: %v", err)
	}
	if devices.apduID != "usb-1-2" || devices.apduCommand != modem.APDUCGLA ||
		devices.apduChannel != 2 || string(devices.apdu) != string(apdu) {
		t.Fatalf("ExecuteAPDU forwarded ID=%q command=%d channel=%d APDU=%X", devices.apduID, devices.apduCommand, devices.apduChannel, devices.apdu)
	}
	if string(response) != string([]byte{0x90, 0x00}) {
		t.Fatalf("ExecuteAPDU response = %X", response)
	}
}

func TestATMapperScoresAllCandidatesBeforeUsingStaleUSBPath(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:            "ec20_1",
		Name:          "EC20 1",
		ATPort:        "/dev/ttyUSB2",
		ControlDevice: "/dev/cdc-wdm0",
		// Simulate metadata left from a formerly swapped hub mapping.
		USBPath:   "/sys/bus/usb/devices/1-6",
		ModemIMEI: "111111111111111",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &fakeATDevices{entries: []device.Device{
		{
			ID:         "quectel-0125-1-6",
			Discovered: true,
			Candidate: modem.Candidate{
				USBPath:    "/sys/bus/usb/devices/1-6",
				QMIControl: "/dev/cdc-wdm1",
				ATPort:     modem.Port{Path: "/dev/ttyUSB6"},
			},
		},
		{
			ID:         "quectel-0306-1-5",
			Discovered: true,
			Candidate: modem.Candidate{
				USBPath:    "/sys/bus/usb/devices/1-5",
				QMIControl: "/dev/cdc-wdm0",
				ATPort:     modem.Port{Path: "/dev/ttyUSB2"},
			},
		},
	}}
	mapper := ATMapper{Store: database, Devices: devices}
	if _, err := mapper.ExecuteAT(context.Background(), "ec20_1", "AT+CIMI"); err != nil {
		t.Fatal(err)
	}
	if devices.executedID != "quectel-0306-1-5" {
		t.Fatalf("ExecuteAT physical ID = %q, want coherent AT/QMI candidate", devices.executedID)
	}
}

func TestATMapperPrefersLiveIMEIOverAllStalePaths(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:            "ec20_1",
		Name:          "EC20 1",
		ATPort:        "/dev/ttyUSB2",
		ControlDevice: "/dev/cdc-wdm0",
		USBPath:       "/sys/bus/usb/devices/1-5",
		ModemIMEI:     "222222222222222",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &fakeATDevices{entries: []device.Device{
		{
			ID:         "old-paths",
			Discovered: true,
			Candidate: modem.Candidate{
				USBPath:    "/sys/bus/usb/devices/1-5",
				QMIControl: "/dev/cdc-wdm0",
				ATPort:     modem.Port{Path: "/dev/ttyUSB2"},
			},
			Snapshot: &device.Snapshot{IMEI: "111111111111111"},
		},
		{
			ID:         "live-imei",
			Discovered: true,
			Candidate: modem.Candidate{
				USBPath:    "/sys/bus/usb/devices/2-3",
				QMIControl: "/dev/cdc-wdm4",
				ATPort:     modem.Port{Path: "/dev/ttyUSB10"},
			},
			Snapshot: &device.Snapshot{IMEI: "222222222222222"},
		},
	}}
	mapper := ATMapper{Store: database, Devices: devices}
	if _, err := mapper.ExecuteAT(context.Background(), "ec20_1", "AT+CIMI"); err != nil {
		t.Fatal(err)
	}
	if devices.executedID != "live-imei" {
		t.Fatalf("ExecuteAT physical ID = %q, want live IMEI candidate", devices.executedID)
	}
}

func TestATMapperResolvesPCSCReaderAfterUSBPortMove(t *testing.T) {
	const name = "Alcor Link AK9563 00 00"
	config := store.Device{
		ID: "configured-reader", Name: "SIM reader", DeviceType: store.DeviceTypeUSBSIMReader,
		DeviceBackend: pcsc.HardwareKind, ControlDevice: name, USBPath: "1-1",
	}
	oldPort := device.Device{ID: config.ID, Discovered: true, Candidate: modem.Candidate{
		HardwareKind: pcsc.HardwareKind, ReaderName: "Other Reader 00 00", USBPath: "1-1",
	}}
	moved := device.Device{ID: "reader-new-port", Discovered: true, Candidate: modem.Candidate{
		HardwareKind: pcsc.HardwareKind, ReaderName: name, USBPath: "2-1",
	}}
	for _, entries := range [][]device.Device{{oldPort, moved}, {moved, oldPort}} {
		database := testStore(t)
		if err := database.UpsertDevice(context.Background(), config); err != nil {
			t.Fatal(err)
		}
		devices := &fakeATDevices{entries: entries}
		mapper := ATMapper{Store: database, Devices: devices}
		entry, err := mapper.Get(config.ID)
		if err != nil || entry.ID != moved.ID {
			t.Fatalf("Get() = %#v, %v, want moved reader", entry, err)
		}
		if _, err := mapper.ExecuteAPDU(context.Background(), config.ID, modem.APDUCGLA, 0, []byte{0x00, 0xA4}); err != nil {
			t.Fatal(err)
		}
		if devices.apduID != moved.ID {
			t.Fatalf("APDU physical ID = %q, want %q", devices.apduID, moved.ID)
		}
	}
}

func TestMatchPhysicalDevicePCSCRejectsConflictsAndAmbiguity(t *testing.T) {
	const name = "Alcor Link AK9563 00 00"
	config := store.Device{
		ID: "configured-reader", DeviceType: store.DeviceTypeUSBSIMReader,
		ControlDevice: name, USBPath: "1-1",
	}
	reader := func(id, readerName, path string) device.Device {
		return device.Device{ID: id, Discovered: true, Candidate: modem.Candidate{
			HardwareKind: pcsc.HardwareKind, ReaderName: readerName, USBPath: path,
		}}
	}
	tests := []struct {
		name    string
		config  store.Device
		entries []device.Device
		wantID  string
	}{
		{"conflicting reader at old port and ID", config, []device.Device{reader(config.ID, "Other Reader 00 00", "1-1")}, ""},
		{"non-PCSC device at old port and ID", config, []device.Device{{ID: config.ID, Discovered: true, Candidate: modem.Candidate{USBPath: "1-1"}}}, ""},
		{"ambiguous names ignore path-derived ID hint", config, []device.Device{reader(config.ID, name, "2-1"), reader("other", name, "3-1")}, ""},
		{"exact pair among duplicate names", config, []device.Device{reader("other", name, "2-1"), reader("paired", name, "1-1")}, "paired"},
		{"name-based move with legacy backend marker", store.Device{ID: config.ID, DeviceBackend: pcsc.HardwareKind, ControlDevice: name, USBPath: "1-1"}, []device.Device{reader("moved", name, "2-1")}, "moved"},
		{"legacy path-only reader", store.Device{ID: config.ID, DeviceType: store.DeviceTypeUSBSIMReader, USBPath: "1-1"}, []device.Device{reader("legacy", "Other Reader 00 00", "1-1")}, "legacy"},
		{"legacy ID-only reader", store.Device{ID: config.ID, DeviceType: store.DeviceTypeUSBSIMReader}, []device.Device{reader(config.ID, name, "2-1")}, config.ID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MatchPhysicalDevice(test.config, test.entries)
			if test.wantID == "" {
				if !errors.Is(err, device.ErrNotFound) {
					t.Fatalf("MatchPhysicalDevice() = %#v, %v, want not found", got, err)
				}
				return
			}
			if err != nil || got.ID != test.wantID {
				t.Fatalf("MatchPhysicalDevice() = %#v, %v, want %q", got, err, test.wantID)
			}
		})
	}
}
