package server

import (
	"context"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
)

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

func TestDeviceSummaryTreatsQualcommOfflineAsFlightMode(t *testing.T) {
	t.Parallel()
	got := deviceSummary(device.Device{
		ID:         "wifi-410",
		Discovered: true,
		Snapshot: &device.Snapshot{
			Responsive:    true,
			ModeKnown:     true,
			OperatingMode: 7,
		},
	})
	if got["flight_mode"] != true {
		t.Fatalf("flight_mode = %#v, want true", got["flight_mode"])
	}
}
