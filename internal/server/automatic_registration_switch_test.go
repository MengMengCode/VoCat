package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
)

type registrationSwitchController struct {
	registrationTaskController
	server       *Server
	switchErr    error
	flightErr    error
	cancelFlight context.CancelFunc
	switched     bool
}

func (c *registrationSwitchController) SetFlight(ctx context.Context, id string, enabled bool) (device.FlightResult, error) {
	result, err := c.registrationTaskController.SetFlight(ctx, id, enabled)
	if c.cancelFlight != nil {
		c.cancelFlight()
	}
	if c.flightErr != nil {
		return result, c.flightErr
	}
	return result, err
}

func (c *registrationSwitchController) ESIMSwitchProfile(ctx context.Context, id, iccid, aid string) error {
	c.switched = true
	config, err := c.server.store.Device(ctx, id)
	if err != nil {
		return err
	}
	if config.NetworkEnabled {
		return errors.New("data intent still enabled at profile switch")
	}
	if len(c.actions) < 2 || c.actions[0] != "data:false" || c.actions[1] != "flight:true" {
		return errors.New("profile switched before data stop and airplane mode")
	}
	if c.switchErr != nil {
		return c.switchErr
	}
	c.entry.Snapshot = &device.Snapshot{ICCID: iccid}
	// Simulate device reappearance during the switch. It must not restore data.
	c.server.reconcileCellularDataForPhysical(ctx, id)
	return nil
}

func TestCellularRegistrationDisablesDataBeforeProfileSwitch(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		stopErr, switchErr, flightErr                error
		initiallyDisabled, cancelFlight, wantEnabled bool
		wantError                                    string
		wantSwitch                                   bool
	}{
		{name: "flight fails restores enabled intent", flightErr: errors.New("flight failed"), wantError: "flight failed", wantEnabled: true},
		{name: "flight fails preserves disabled intent", flightErr: errors.New("flight failed"), initiallyDisabled: true, wantError: "flight failed"},
		{name: "flight cancellation still restores intent", flightErr: context.Canceled, cancelFlight: true, wantError: "context canceled", wantEnabled: true},
		{name: "device reappears during switch", wantSwitch: true},
		{name: "stop fails", stopErr: errors.New("stop failed"), wantError: "stop cellular data before profile switch"},
		{name: "switch fails", switchErr: errors.New("switch rejected"), wantError: "switch rejected", wantSwitch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := blockedRegionServer(t, "310260123456789")
			c := &registrationSwitchController{server: s, switchErr: tc.switchErr, registrationTaskController: registrationTaskController{stopErr: tc.stopErr, fakeDeviceController: fakeDeviceController{entry: device.Device{ID: "dev1", Discovered: true, Snapshot: &device.Snapshot{ICCID: "old-card"}}}}}
			s.devices = c
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c.flightErr = tc.flightErr
			if tc.cancelFlight {
				c.cancelFlight = cancel
			}
			if err := s.store.UpsertDevice(ctx, store.Device{ID: "dev1", Name: "Modem", NetworkEnabled: !tc.initiallyDisabled}); err != nil {
				t.Fatal(err)
			}
			// Even if the target card normally uses data, it must not connect while switching.
			if err := s.store.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "test-card", NetworkEnabled: true, Source: "user"}); err != nil {
				t.Fatal(err)
			}
			config, _, _, err := s.ensureAutomaticTaskProfile(ctx, store.AutomaticTask{DeviceID: "dev1", ProfileICCID: "test-card", TaskType: "cellular_attach", Environment: "cellular"}, func(string) {})
			if tc.wantError == "" {
				if err != nil || config.NetworkEnabled {
					t.Fatalf("config=%+v error=%v", config, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error=%v want %s", err, tc.wantError)
			}
			if c.switched != tc.wantSwitch {
				t.Fatalf("switched=%v", c.switched)
			}
			for _, action := range c.actions {
				if action == "data:true" {
					t.Fatal("data connected during switch")
				}
			}
			stored, err := s.store.Device(context.Background(), "dev1")
			if err != nil || stored.NetworkEnabled != tc.wantEnabled {
				t.Fatalf("data intent: %+v, %v, want enabled=%v", stored, err, tc.wantEnabled)
			}
			status := s.cellularDataRuntime().status("dev1", stored.NetworkEnabled)
			if status.DesiredEnabled != tc.wantEnabled || status.MaintenancePhase != "" {
				t.Fatalf("runtime intent=%+v, want enabled=%v", status, tc.wantEnabled)
			}
			policy, err := s.store.CardPolicy(context.Background(), "test-card")
			if err != nil || !policy.NetworkEnabled || policy.Source != "user" {
				t.Fatalf("target saved policy changed: %+v, %v", policy, err)
			}
		})
	}
}
