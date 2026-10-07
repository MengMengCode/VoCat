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
