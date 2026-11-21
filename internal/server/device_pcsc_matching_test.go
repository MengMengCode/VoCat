package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/pcsc"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type pcscMappingDevices struct {
	fakeDeviceController
	entries []device.Device
}

func (devices pcscMappingDevices) List() []device.Device { return devices.entries }

func (devices pcscMappingDevices) Discover(context.Context) ([]device.Device, error) {
	return devices.entries, nil
}

func (devices pcscMappingDevices) Get(id string) (device.Device, error) {
	for _, entry := range devices.entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return device.Device{}, device.ErrNotFound
}

func newPCSCMappingServer(t *testing.T, entries []device.Device) (*Server, store.Device) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := store.Device{
		ID: "reader", Name: "SIM Reader", DeviceType: store.DeviceTypeUSBSIMReader,
		ControlDevice: "Alcor Link AK9563 00 00", USBPath: "1-1",
	}
	if err := database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	config, err = database.Device(context.Background(), config.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		store: database, logger: regionTestLogger(), devices: pcscMappingDevices{entries: entries},
		vowifi: &fakeVoWiFiController{state: vowifi.State{
			DeviceID: config.ID, ICCID: "8944100000000000001", Enabled: true, Active: true,
			SIMReady: true, TunnelReady: true, IMSReady: true, SMSReady: true, Phase: vowifi.PhaseSMSReady,
		}},
	}, config
}

func TestPCSCDeviceSummaryFollowsReaderAfterUSBPortChange(t *testing.T) {
	moved := device.Device{
		ID: "pcsc-new-port", Discovered: true,
		Candidate: modem.Candidate{
			HardwareKind: pcsc.HardwareKind, ReaderName: "Alcor Link AK9563 00 00", USBPath: "2-1",
		},
		Snapshot: &device.Snapshot{Responsive: true, SIMReady: true, Model: "AK9563", ICCID: "8944100000000000001"},
	}
	for _, test := range []struct {
		name  string
		other device.Device
	}{
		{
			name: "old port contains another reader",
			other: device.Device{ID: "pcsc-old-port", Discovered: true, Candidate: modem.Candidate{
				HardwareKind: pcsc.HardwareKind, ReaderName: "Another Reader 00 00", USBPath: "1-1",
			}},
		},
		{
			name: "old port contains a modem without AT",
			other: device.Device{ID: "usb-old-port", Discovered: true, LastError: device.ErrNoATPort.Error(), Candidate: modem.Candidate{
				HardwareKind: "usb", USBPath: "1-1",
			}},
		},
		{
			name: "logical ID is reused by another reader",
			other: device.Device{ID: "reader", Discovered: true, Candidate: modem.Candidate{
				HardwareKind: pcsc.HardwareKind, ReaderName: "Another Reader 00 00", USBPath: "1-1",
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, config := newPCSCMappingServer(t, []device.Device{test.other, moved})
			entry, id, present := server.physicalForConfig(config)
			if !present || id != moved.ID {
				t.Fatalf("physical reader = %q, present %v, want %q", id, present, moved.ID)
			}
			overview := server.configuredDeviceOverview(config, entry, present)
			if overview["healthy"] != true || overview["control_online"] != true || overview["lifecycle_phase"] != "ready" || overview["lifecycle_reason"] != "" {
				t.Fatalf("moved reader overview = %#v", overview)
			}
			response := httptest.NewRecorder()
			server.handleDevices(response, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("device list status = %d: %s", response.Code, response.Body.String())
			}
			rows := decodeData(t, response)["devices"].([]any)
			row := rows[0].(map[string]any)
			if row["control_online"] != true || row["healthy"] != true || row["lifecycle_phase"] != "ready" {
				t.Fatalf("moved reader list row = %#v", row)
			}
			runtime := row["vowifi_runtime"].(map[string]any)
			if runtime["phase"] != string(vowifi.PhaseSMSReady) {
				t.Fatalf("moved reader runtime = %#v", runtime)
			}
			response = httptest.NewRecorder()
			server.handleDiscoveredDevices(response, httptest.NewRequest(http.MethodGet, "/api/devices/discovered", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("discovery status = %d: %s", response.Code, response.Body.String())
			}
			for _, value := range decodeData(t, response)["devices"].([]any) {
				row := value.(map[string]any)
				wantConfigured := row["discovery_key"] == moved.ID
				if row["configured"] != wantConfigured {
					t.Fatalf("discovery row has wrong configured state: %#v", row)
				}
				if wantConfigured && row["configured_id"] != config.ID {
					t.Fatalf("moved reader has wrong configuration binding: %#v", row)
				}
			}
		})
	}
}

func TestPCSCDeviceMappingRejectsMissingAndAmbiguousReaders(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []device.Device
	}{
		{name: "reader absent but old port is occupied", entries: []device.Device{{
			ID: "old-port", Discovered: true, Candidate: modem.Candidate{
				HardwareKind: pcsc.HardwareKind, ReaderName: "Another Reader 00 00", USBPath: "1-1",
			},
		}}},
		{name: "same reader name at two new ports", entries: []device.Device{
			{ID: "reader-a", Discovered: true, Candidate: modem.Candidate{
				HardwareKind: pcsc.HardwareKind, ReaderName: "Alcor Link AK9563 00 00", USBPath: "2-1",
			}},
			{ID: "reader-b", Discovered: true, Candidate: modem.Candidate{
				HardwareKind: pcsc.HardwareKind, ReaderName: "Alcor Link AK9563 00 00", USBPath: "3-1",
			}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, config := newPCSCMappingServer(t, test.entries)
			if _, id, present := server.physicalForConfig(config); present || id != "" {
				t.Fatalf("unresolved reader matched %q", id)
			}
			response := httptest.NewRecorder()
			server.handleDiscoveredDevices(response, httptest.NewRequest(http.MethodGet, "/api/devices/discovered", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("discovery status = %d: %s", response.Code, response.Body.String())
			}
			for _, value := range decodeData(t, response)["devices"].([]any) {
				row := value.(map[string]any)
				if row["configured"] != false || row["configured_id"] != "" {
					t.Fatalf("unresolved discovery row marked configured: %#v", row)
				}
			}
		})
	}
}
