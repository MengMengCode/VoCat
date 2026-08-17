package server

import (
	"context"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func TestFillConfigFromPhysicalClassifiesDJI4G(t *testing.T) {
	config := store.Device{DeviceType: store.DeviceTypePCIeEC20EC25}
	entry := device.Device{Candidate: modem.Candidate{
		VendorID:  "2ca3",
		ProductID: "4006",
	}}

	fillConfigFromPhysical(&config, entry)

	if config.DeviceType != store.DeviceTypeDJI4G {
		t.Fatalf("device type = %q, want %q", config.DeviceType, store.DeviceTypeDJI4G)
	}
	if got := discoveredDeviceType(entry.Candidate); got != store.DeviceTypeDJI4G {
		t.Fatalf("discovered device type = %q, want %q", got, store.DeviceTypeDJI4G)
	}
}

func TestConfiguredDeviceSummaryIgnoresVoWiFiRuntimeFromPreviousSIM(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "ec20_1", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertVoWiFiRuntime(context.Background(), store.VoWiFiRuntime{
		DeviceID:          "ec20_1",
		Phase:             "stopping",
		ICCID:             "89441000400128014257",
		IMSI:              "234159608751160",
		TunnelReady:       true,
		IMSReady:          true,
		SMSReady:          true,
		LocalPhone:        "+447386083638",
		PhoneNumberSource: "ims_p_associated_uri",
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: database}
	entry := &device.Device{ID: "physical", Snapshot: &device.Snapshot{
		ICCID: "89104100000028106378",
		IMSI:  "310380500712483",
	}}
	got := s.configuredDeviceSummary(store.Device{ID: "ec20_1"}, entry)
	if got["vowifi_active"] != false {
		t.Fatalf("vowifi_active = %#v", got["vowifi_active"])
	}
	if got["local_phone"] == "+447386083638" {
		t.Fatalf("old phone leaked into current SIM summary: %#v", got)
	}
	runtime, ok := got["vowifi_runtime"].(map[string]any)
	if !ok || runtime["phase"] != "idle" || runtime["iccid"] != "89104100000028106378" {
		t.Fatalf("runtime = %#v", got["vowifi_runtime"])
	}
}

func TestConfiguredDeviceSummaryPrefersLiveVoWiFiStateOverStoredShutdownState(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "ec20_1", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertVoWiFiRuntime(context.Background(), store.VoWiFiRuntime{
		DeviceID:   "ec20_1",
		Phase:      "idle",
		ICCID:      "89104100000028106378",
		LastReason: "disabled",
		UpdatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	live := vowifi.State{
		DeviceID:    "ec20_1",
		Phase:       vowifi.PhaseTunnelReady,
		Enabled:     true,
		Active:      true,
		ICCID:       "89104100000028106378",
		SIMReady:    true,
		AccessReady: true,
		TunnelReady: true,
		LastReason:  "ipsec_tunnel_ready",
		UpdatedAt:   time.Now().UTC(),
	}
	s := &Server{store: database, vowifi: &fakeVoWiFiController{state: live}}
	entry := &device.Device{ID: "physical", Snapshot: &device.Snapshot{ICCID: live.ICCID}}
	got := s.configuredDeviceSummary(store.Device{ID: "ec20_1", VoWiFiEnabled: true}, entry)
	runtime, ok := got["vowifi_runtime"].(map[string]any)
	if !ok || runtime["phase"] != string(vowifi.PhaseTunnelReady) || runtime["enabled"] != true {
		t.Fatalf("runtime = %#v", got["vowifi_runtime"])
	}
	if got["vowifi_active"] != true {
		t.Fatalf("vowifi_active = %#v", got["vowifi_active"])
	}
}

func TestConfiguredDeviceSummaryMarksIdleRuntimeAsNotInUse(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	s := &Server{
		store: database,
		vowifi: &fakeVoWiFiController{state: vowifi.State{
			DeviceID:   "ec20_1",
			Phase:      vowifi.PhaseIdle,
			Enabled:    false,
			LastReason: "disabled",
			UpdatedAt:  time.Now().UTC(),
		}},
	}
	got := s.configuredDeviceSummary(store.Device{ID: "ec20_1", VoWiFiEnabled: true}, nil)
	runtime := got["vowifi_runtime"].(map[string]any)
	if runtime["enabled"] != false || got["vowifi_active"] != false {
		t.Fatalf("summary = %#v", got)
	}
}

func TestConfiguredDeviceOverviewAlwaysUsesLiveDiscoveredATPort(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	s := &Server{store: database}
	config := store.Device{ID: "ec20_1", ATPort: "/dev/ttyUSB9"}
	entry := device.Device{Candidate: modem.Candidate{
		ATPort: modem.Port{Path: "/dev/ttyUSB2", Role: modem.PortRoleAT},
	}}

	connected := s.configuredDeviceOverview(config, entry, true)
	if got := connected["at_port"]; got != "/dev/ttyUSB2" {
		t.Fatalf("connected AT port = %#v, want live /dev/ttyUSB2", got)
	}

	offline := s.configuredDeviceOverview(config, entry, false)
	if got := offline["at_port"]; got != "" {
		t.Fatalf("offline AT port = %#v, want empty instead of stored port", got)
	}
}

func TestSnapshotHasSIMDoesNotTreatUnknownStatusAsInserted(t *testing.T) {
	for _, snapshot := range []*device.Snapshot{
		{IMEI: "867123456789012"},
		{IMEI: "867123456789012", SIMStatus: "unknown"},
		{IMEI: "867123456789012", SIMStatus: "not_inserted"},
	} {
		if snapshotHasSIM(snapshot) {
			t.Fatalf("snapshot was reported with a SIM: %#v", snapshot)
		}
	}
	for _, snapshot := range []*device.Snapshot{
		{SIMStatus: "pin_required"},
		{ICCID: "89441000400128014257"},
		{SIMReady: true},
	} {
		if !snapshotHasSIM(snapshot) {
			t.Fatalf("snapshot was reported without a SIM: %#v", snapshot)
		}
	}
}
