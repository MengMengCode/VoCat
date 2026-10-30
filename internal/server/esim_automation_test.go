package server

import (
	"context"
	"errors"
	"testing"

	"vocat/internal/device"
	"vocat/internal/store"
)

type automationSwitchWiFi struct {
	fakeEsimVoWiFiController
	maintenance bool
	begin, end  int
}

func (w *automationSwitchWiFi) BeginMaintenance(string) error {
	w.maintenance = true
	w.begin++
	return nil
}

func (w *automationSwitchWiFi) EndMaintenance(string) {
	w.maintenance = false
	w.end++
}

type automationSwitchDevices struct {
	fakeDeviceController
	wifi        *automationSwitchWiFi
	flight      bool
	switched    bool
	switchICCID string
	server      *Server
}

func (d *automationSwitchDevices) SetFlight(_ context.Context, _ string, enabled bool) (device.FlightResult, error) {
	d.flight = enabled
	return device.FlightResult{}, nil
}

func (d *automationSwitchDevices) ESIMSwitchProfile(_ context.Context, _ string, iccid, aid string) error {
	if !d.wifi.maintenance || d.wifi.enabled || !d.flight {
		return errors.New("SIM switch bypassed maintenance, VoWiFi shutdown or RF-off")
	}
	if d.server.smsSyncMu.TryLock() {
		d.server.smsSyncMu.Unlock()
		return errors.New("SIM switch did not serialize with modem SMS synchronization")
	}
	d.switched, d.switchICCID = true, iccid
	return nil
}

func TestAutomationESIMSwitchCoordinatesLifecycleAndRestoresTargetPolicy(t *testing.T) {
	for _, targetWiFi := range []bool{false, true} {
		ctx := context.Background()
		db, err := store.Open(ctx, ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := db.UpsertDevice(ctx, store.Device{ID: "dev1", Name: "dev1", VoWiFiEnabled: true, APN: "old.apn"}); err != nil {
			t.Fatal(err)
		}
		const target = "8900000000000000002"
		if err := db.UpsertCardPolicy(ctx, store.CardPolicy{
			ICCID: target, VoWiFiEnabled: targetWiFi, AirplaneEnabled: true, APN: "target.apn",
		}); err != nil {
			t.Fatal(err)
		}
		wifi := &automationSwitchWiFi{fakeEsimVoWiFiController: fakeEsimVoWiFiController{enabled: true}}
		devices := &automationSwitchDevices{wifi: wifi, fakeDeviceController: fakeDeviceController{
			entry: device.Device{ID: "dev1", Discovered: true, Snapshot: &device.Snapshot{ICCID: "old-card"}},
		}}
		s := &Server{store: db, devices: devices, vowifi: wifi, logger: regionTestLogger()}
		devices.server = s
		if err := s.SwitchESIMProfile(ctx, "dev1", target, ""); err != nil {
			t.Fatal(err)
		}
		config, err := db.Device(ctx, "dev1")
		if err != nil {
			t.Fatal(err)
		}
		if !devices.switched || devices.switchICCID != target || !devices.flight ||
			wifi.begin != 1 || wifi.end != 1 || wifi.maintenance || wifi.enabled != targetWiFi ||
			config.APN != "target.apn" || config.VoWiFiEnabled != targetWiFi || config.NetworkEnabled {
			t.Fatalf("target policy/lifecycle mismatch: config=%+v wifi=%+v devices=%+v", config, wifi, devices)
		}
		if s.cellularDataRuntime().status("dev1", false).Phase != "disabled" {
			t.Fatal("data runtime remained in profile transition")
		}
		if !s.smsSyncMu.TryLock() {
			t.Fatal("SMS synchronization remained locked after switch")
		}
		s.smsSyncMu.Unlock()
	}
}
