package server

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"vocat/internal/device"
	"vocat/internal/store"
)

type revertTestController struct {
	fakeDeviceController
	actions      []string
	switches     []string
	registration int
	currentICCID string
}

func (c *revertTestController) SetNetwork(_ context.Context, _ string, request device.NetworkRequest) (device.NetworkResult, error) {
	c.actions = append(c.actions, fmt.Sprintf("data:%t", request.Enabled))
	return device.NetworkResult{Enabled: request.Enabled}, nil
}

func (c *revertTestController) SetFlight(_ context.Context, _ string, enabled bool) (device.FlightResult, error) {
	c.actions = append(c.actions, fmt.Sprintf("flight:%t", enabled))
	return device.FlightResult{}, nil
}

func (c *revertTestController) SetOperatorSelection(_ context.Context, _ string, automatic bool, _ string, _ *int) (device.OperatorSelection, error) {
	c.actions = append(c.actions, fmt.Sprintf("automatic:%t", automatic))
	return device.OperatorSelection{}, nil
}

func (c *revertTestController) ReRegisterOperator(context.Context, string) (device.OperatorSelection, error) {
	c.actions = append(c.actions, "register")
	return device.OperatorSelection{}, nil
}

func (c *revertTestController) Refresh(context.Context, string) (device.Snapshot, error) {
	c.actions = append(c.actions, "refresh")
	return device.Snapshot{ICCID: c.currentICCID, RegistrationStatus: c.registration, PSAttached: false}, nil
}

func (c *revertTestController) ESIMListProfiles(_ context.Context, _ string) (device.EsimInfo, error) {
	return device.EsimInfo{
		Profiles: []device.EsimProfile{
			{ICCID: "card-initial", AID: "A0000005591010FFFFFFFF8900000100"},
			{ICCID: "card-task", AID: "A0000005591010FFFFFFFF8900000200"},
		},
	}, nil
}

func (c *revertTestController) ESIMSwitchProfile(_ context.Context, _ string, iccid, aid string) error {
	c.switches = append(c.switches, fmt.Sprintf("%s:%s", iccid, aid))
	c.currentICCID = iccid
	c.entry.Snapshot = &device.Snapshot{ICCID: iccid}
	return nil
}

func TestAutomaticTaskRevertToInitialProfile(t *testing.T) {
	s := blockedRegionServer(t, "310260123456789")
	s.vowifi = &fakeVoWiFiController{}

	ctrl := &revertTestController{
		registration: 1,
		currentICCID: "card-initial",
		fakeDeviceController: fakeDeviceController{
			entry: device.Device{
				ID:         "dev1",
				Discovered: true,
				Snapshot:   &device.Snapshot{ICCID: "card-initial"},
			},
		},
	}
	s.devices = ctrl

	ctx := context.Background()
	// Setup initial device and policies
	if err := s.store.UpsertDevice(ctx, store.Device{
		ID:             "dev1",
		Name:           "Modem",
		NetworkEnabled: false,
		VoWiFiEnabled:  true,
	}); err != nil {
		t.Fatal(err)
	}

	initialPolicy := store.CardPolicy{
		ICCID:          "card-initial",
		AirplaneEnabled: true,
		VoWiFiEnabled:  true,
		NetworkEnabled: false,
		Source:         "user",
	}
	if err := s.store.UpsertCardPolicy(ctx, initialPolicy); err != nil {
		t.Fatal(err)
	}

	taskPolicy := store.CardPolicy{
		ICCID:          "card-task",
		AirplaneEnabled: false,
		NetworkEnabled: true,
		Source:         "user",
	}
	if err := s.store.UpsertCardPolicy(ctx, taskPolicy); err != nil {
		t.Fatal(err)
	}

	task := store.AutomaticTask{
		DeviceID:      "dev1",
		ProfileICCID:  "card-task",
		ProfileAID:    "A0000005591010FFFFFFFF8900000200",
		TaskType:      "cellular_attach",
		Environment:   "cellular",
		RevertProfile: true,
		Payload:       []byte(`{}`),
	}

	output, err := s.executeAutomaticTask(ctx, task, func(string) {})
	if err != nil {
		t.Fatalf("executeAutomaticTask failed: %v", err)
	}
	if output != "已注册蜂窝网络，未启用数据连接" {
		t.Fatalf("unexpected output: %q", output)
	}

	// Verify profile switches: first to card-task, then reverted back to card-initial
	wantSwitches := []string{
		"card-task:A0000005591010FFFFFFFF8900000200",
		"card-initial:A0000005591010FFFFFFFF8900000100",
	}
	if !reflect.DeepEqual(ctrl.switches, wantSwitches) {
		t.Fatalf("switches=%v, want %v", ctrl.switches, wantSwitches)
	}
	if ctrl.currentICCID != "card-initial" {
		t.Fatalf("final currentICCID=%s, want card-initial", ctrl.currentICCID)
	}

	// Verify initial policy was restored
	storedDevice, err := s.store.Device(ctx, "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if storedDevice.VoWiFiEnabled != initialPolicy.VoWiFiEnabled || storedDevice.NetworkEnabled != false {
		t.Fatalf("device setting not restored: %+v", storedDevice)
	}

	storedInitialPolicy, err := s.store.CardPolicy(ctx, "card-initial")
	if err != nil {
		t.Fatal(err)
	}
	if storedInitialPolicy.VoWiFiEnabled != initialPolicy.VoWiFiEnabled || storedInitialPolicy.AirplaneEnabled != initialPolicy.AirplaneEnabled {
		t.Fatalf("card-initial policy not restored: %+v", storedInitialPolicy)
	}

	// Verify card-task's own saved policy was also preserved in store
	storedTaskPolicy, err := s.store.CardPolicy(ctx, "card-task")
	if err != nil {
		t.Fatal(err)
	}
	if storedTaskPolicy.NetworkEnabled != taskPolicy.NetworkEnabled {
		t.Fatalf("card-task policy altered: %+v", storedTaskPolicy)
	}
}

func TestAutomaticTaskDoNotRevertWhenDisabled(t *testing.T) {
	s := blockedRegionServer(t, "310260123456789")
	s.vowifi = nil

	ctrl := &revertTestController{
		registration: 1,
		currentICCID: "card-initial",
		fakeDeviceController: fakeDeviceController{
			entry: device.Device{
				ID:         "dev1",
				Discovered: true,
				Snapshot:   &device.Snapshot{ICCID: "card-initial"},
			},
		},
	}
	s.devices = ctrl

	ctx := context.Background()
	_ = s.store.UpsertDevice(ctx, store.Device{ID: "dev1", Name: "Modem"})
	_ = s.store.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "card-initial", Source: "user"})
	_ = s.store.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "card-task", NetworkEnabled: true, Source: "user"})

	task := store.AutomaticTask{
		DeviceID:      "dev1",
		ProfileICCID:  "card-task",
		ProfileAID:    "A0000005591010FFFFFFFF8900000200",
		TaskType:      "cellular_attach",
		Environment:   "cellular",
		RevertProfile: false, // Do NOT revert
		Payload:       []byte(`{}`),
	}

	output, err := s.executeAutomaticTask(ctx, task, func(string) {})
	if err != nil {
		t.Fatalf("executeAutomaticTask failed: %v", err)
	}
	if output != "已注册蜂窝网络，未启用数据连接" {
		t.Fatalf("unexpected output: %q", output)
	}

	// Only switched once to card-task, did not revert
	wantSwitches := []string{"card-task:A0000005591010FFFFFFFF8900000200"}
	if !reflect.DeepEqual(ctrl.switches, wantSwitches) {
		t.Fatalf("switches=%v, want %v", ctrl.switches, wantSwitches)
	}
	if ctrl.currentICCID != "card-task" {
		t.Fatalf("final currentICCID=%s, want card-task", ctrl.currentICCID)
	}
}

func TestAutomaticTaskRevertsEvenOnTaskFailure(t *testing.T) {
	s := blockedRegionServer(t, "310260123456789")
	s.vowifi = nil

	ctrl := &revertTestController{
		registration: 3, // Registration denied -> task failure
		currentICCID: "card-initial",
		fakeDeviceController: fakeDeviceController{
			entry: device.Device{
				ID:         "dev1",
				Discovered: true,
				Snapshot:   &device.Snapshot{ICCID: "card-initial"},
			},
		},
	}
	s.devices = ctrl

	ctx := context.Background()
	_ = s.store.UpsertDevice(ctx, store.Device{ID: "dev1", Name: "Modem"})
	_ = s.store.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "card-initial", Source: "user"})
	_ = s.store.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "card-task", Source: "user"})

	task := store.AutomaticTask{
		DeviceID:      "dev1",
		ProfileICCID:  "card-task",
		ProfileAID:    "A0000005591010FFFFFFFF8900000200",
		TaskType:      "cellular_attach",
		Environment:   "cellular",
		RevertProfile: true,
		Payload:       []byte(`{}`),
	}

	_, err := s.executeAutomaticTask(ctx, task, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected registration denied error, got %v", err)
	}

	// Must have reverted back to card-initial even though task failed
	wantSwitches := []string{
		"card-task:A0000005591010FFFFFFFF8900000200",
		"card-initial:A0000005591010FFFFFFFF8900000100",
	}
	if !reflect.DeepEqual(ctrl.switches, wantSwitches) {
		t.Fatalf("switches=%v, want %v", ctrl.switches, wantSwitches)
	}
	if ctrl.currentICCID != "card-initial" {
		t.Fatalf("final currentICCID=%s, want card-initial", ctrl.currentICCID)
	}
}

func TestAutomaticTaskSameProfileDoesNotSwitch(t *testing.T) {
	s := blockedRegionServer(t, "310260123456789")
	s.vowifi = nil

	ctrl := &revertTestController{
		registration: 1,
		currentICCID: "card-same",
		fakeDeviceController: fakeDeviceController{
			entry: device.Device{
				ID:         "dev1",
				Discovered: true,
				Snapshot:   &device.Snapshot{ICCID: "card-same"},
			},
		},
	}
	s.devices = ctrl

	ctx := context.Background()
	_ = s.store.UpsertDevice(ctx, store.Device{ID: "dev1", Name: "Modem"})
	_ = s.store.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "card-same", Source: "user"})

	task := store.AutomaticTask{
		DeviceID:      "dev1",
		ProfileICCID:  "card-same",
		TaskType:      "cellular_attach",
		Environment:   "cellular",
		RevertProfile: true,
		Payload:       []byte(`{}`),
	}

	_, err := s.executeAutomaticTask(ctx, task, func(string) {})
	if err != nil {
		t.Fatalf("executeAutomaticTask failed: %v", err)
	}

	// No switches because profile was already active
	if len(ctrl.switches) != 0 {
		t.Fatalf("expected 0 switches, got %v", ctrl.switches)
	}
}
