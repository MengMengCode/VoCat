package server

import (
	"context"
	"errors"
	"testing"

	"vocat/internal/device"
	"vocat/internal/store"
)

type failedSwitchRecoveryController struct {
	flightTrackingController
	liveSnapshot device.Snapshot
	refreshErr   error
}

func TestUncertainSwitchRestoresLiveCardPolicyInsteadOfOldWiFiIntent(t *testing.T) {
	for _, tc := range []struct {
		name              string
		oldWiFi, liveWiFi bool
		liveICCID         string
		readErr           error
	}{
		{name: "new card disables old WiFi", oldWiFi: true, liveICCID: "new-card"},
		{name: "new card enables its own WiFi", liveWiFi: true, liveICCID: "new-card"},
		{name: "unknown identity stays stopped", oldWiFi: true},
		{name: "failed live read cannot use stale policy", oldWiFi: true, liveICCID: "new-card", readErr: errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(context.Background(), ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "dev1", VoWiFiEnabled: tc.oldWiFi, APN: "old.apn"}); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertCardPolicy(context.Background(), store.CardPolicy{ICCID: "new-card", VoWiFiEnabled: tc.liveWiFi, AirplaneEnabled: true, APN: "new.apn"}); err != nil {
				t.Fatal(err)
			}
			controller := &failedSwitchRecoveryController{liveSnapshot: device.Snapshot{ICCID: tc.liveICCID}, refreshErr: tc.readErr}
			wifi := &fakeEsimVoWiFiController{}
			s := &Server{store: db, devices: controller, vowifi: wifi, logger: regionTestLogger()}
			s.restoreProfileSwitchFailureState(context.Background(), "dev1", "dev1")
			config, err := db.Device(context.Background(), "dev1")
			if err != nil {
				t.Fatal(err)
			}
			known := tc.liveICCID != "" && tc.readErr == nil
			wantWiFi := known && tc.liveWiFi
			if wifi.enabled != wantWiFi || config.VoWiFiEnabled != wantWiFi || config.NetworkEnabled || !controller.lastFlightState {
				t.Fatalf("old intent reused: config=%+v wifi=%v", config, wifi.enabled)
			}
			if known && config.APN != "new.apn" {
				t.Fatal("active card APN was not restored")
			}
			if !known && config.APN != "" {
				t.Fatal("unknown card inherited previous APN")
			}
		})
	}
}

func (c *failedSwitchRecoveryController) Refresh(context.Context, string) (device.Snapshot, error) {
	return c.liveSnapshot, c.refreshErr
}

func TestRejectedProfileSwitchPreservesRFOffPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		knownIdentity bool
		knownPolicy   bool
		airplane      bool
		refreshErr    error
		liveICCID     string
		wantFlight    bool
	}{
		{name: "protected card with WiFi disabled", knownIdentity: true, knownPolicy: true, airplane: true, wantFlight: true},
		{name: "unknown live identity", wantFlight: true},
		{name: "unknown card policy", knownIdentity: true, wantFlight: true},
		{name: "stale cellular identity with failed live read", knownIdentity: true, knownPolicy: true, refreshErr: errors.New("identity unavailable"), wantFlight: true},
		{name: "stale cellular policy with different active card", knownIdentity: true, knownPolicy: true, liveICCID: "8900000000000000002", wantFlight: true},
		{name: "explicit cellular policy", knownIdentity: true, knownPolicy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(context.Background(), ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "dev1"}); err != nil {
				t.Fatal(err)
			}
			entry := device.Device{ID: "dev1"}
			if tc.knownIdentity {
				entry.Snapshot = &device.Snapshot{ICCID: "8900000000000000001"}
			}
			if tc.knownPolicy {
				if err := db.UpsertCardPolicy(context.Background(), store.CardPolicy{
					ICCID: entry.Snapshot.ICCID, AirplaneEnabled: tc.airplane,
				}); err != nil {
					t.Fatal(err)
				}
			}
			controller := &failedSwitchRecoveryController{
				flightTrackingController: flightTrackingController{fakeDeviceController: fakeDeviceController{entry: entry}},
				refreshErr:               tc.refreshErr,
			}
			if entry.Snapshot != nil {
				controller.liveSnapshot = *entry.Snapshot
			}
			if tc.liveICCID != "" {
				controller.liveSnapshot.ICCID = tc.liveICCID
			}
			s := &Server{store: db, devices: controller, logger: regionTestLogger()}
			s.restoreProfileSwitchFailureState(context.Background(), "dev1", "dev1")
			if controller.lastFlightState != tc.wantFlight {
				t.Fatalf("restored flight = %v, want %v", controller.lastFlightState, tc.wantFlight)
			}
		})
	}
}
