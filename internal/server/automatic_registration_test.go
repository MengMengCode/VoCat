package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
)

// Exercise the real task orchestration with a modem that records every action.
type registrationTaskController struct {
	fakeDeviceController
	actions      []string
	registration int
	stopErr      error
}

func (c *registrationTaskController) SetNetwork(_ context.Context, _ string, request device.NetworkRequest) (device.NetworkResult, error) {
	c.actions = append(c.actions, fmt.Sprintf("data:%t", request.Enabled))
	if !request.Enabled && c.stopErr != nil {
		return device.NetworkResult{}, c.stopErr
	}
	return device.NetworkResult{Enabled: request.Enabled}, nil
}
func (c *registrationTaskController) SetFlight(_ context.Context, _ string, enabled bool) (device.FlightResult, error) {
	c.actions = append(c.actions, fmt.Sprintf("flight:%t", enabled))
	return device.FlightResult{}, nil
}
func (c *registrationTaskController) SetOperatorSelection(_ context.Context, _ string, automatic bool, _ string, _ *int) (device.OperatorSelection, error) {
	c.actions = append(c.actions, fmt.Sprintf("automatic:%t", automatic))
	return device.OperatorSelection{}, nil
}
func (c *registrationTaskController) ReRegisterOperator(context.Context, string) (device.OperatorSelection, error) {
	c.actions = append(c.actions, "register")
	return device.OperatorSelection{}, nil
}
func (c *registrationTaskController) Refresh(context.Context, string) (device.Snapshot, error) {
	c.actions = append(c.actions, "refresh")
	return device.Snapshot{ICCID: "test-card", RegistrationStatus: c.registration, PSAttached: false}, nil
}
func (c *registrationTaskController) SendSMS(context.Context, string, string, string) (device.SMSSendResult, error) {
	c.actions = append(c.actions, "sms")
	return device.SMSSendResult{}, errors.New("unexpected SMS")
}

func TestCellularRegistrationTaskExecution(t *testing.T) {
	for _, tc := range []struct {
		name         string
		registration int
		timeout      time.Duration
		stopErr      error
		wantErr      string
	}{
		{name: "home", registration: 1, timeout: 10 * time.Second},
		{name: "roaming", registration: 5, timeout: 10 * time.Second},
		{name: "denied", registration: 3, timeout: time.Second, wantErr: "registration was denied"},
		{name: "timeout", registration: 2, timeout: 50 * time.Millisecond, wantErr: "deadline exceeded"},
		{name: "cannot stop data", registration: 1, timeout: 10 * time.Second, stopErr: errors.New("stop failed"), wantErr: "stop cellular data before registration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := blockedRegionServer(t, "310260123456789")
			s.vowifi = nil
			controller := &registrationTaskController{registration: tc.registration, stopErr: tc.stopErr,
				fakeDeviceController: fakeDeviceController{entry: device.Device{ID: "dev1", Discovered: true, Snapshot: &device.Snapshot{ICCID: "test-card"}}},
			}
			controller.atHandler = func(command string) (response modem.Response, err error) {
				controller.actions = append(controller.actions, "at:"+command)
				return
			}
			s.devices = controller
			original := store.CardPolicy{ICCID: "test-card", AirplaneEnabled: true, Source: "user"}
			if err := s.store.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "Modem"}); err != nil {
				t.Fatal(err)
			}
			if err := s.store.UpsertCardPolicy(context.Background(), original); err != nil {
				t.Fatal(err)
			}
			original, _ = s.store.CardPolicy(context.Background(), "test-card")
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			output, err := s.executeAutomaticTask(ctx, store.AutomaticTask{DeviceID: "dev1", ProfileICCID: "test-card", TaskType: "cellular_attach", Environment: "cellular", Payload: []byte(`{}`)}, func(string) {})
			if tc.wantErr == "" {
				if err != nil || output != "已注册蜂窝网络，未启用数据连接" {
					t.Fatalf("output=%q error=%v", output, err)
				}
				want := []string{"data:false", "flight:false", "automatic:true", "register", "refresh", "refresh", "data:false", "flight:true"}
				if !reflect.DeepEqual(controller.actions, want) {
					t.Fatalf("actions=%v, want %v", controller.actions, want)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%v, want %q", err, tc.wantErr)
			}
			for _, action := range controller.actions {
				if action == "data:true" || action == "sms" || strings.HasPrefix(action, "at:") {
					t.Fatalf("unexpected action %s", action)
				}
				if tc.stopErr != nil && action == "register" {
					t.Fatal("registered despite failing to stop data")
				}
			}
			restored, err := s.store.CardPolicy(context.Background(), "test-card")
			if err != nil || restored.NetworkEnabled != original.NetworkEnabled || restored.AirplaneEnabled != original.AirplaneEnabled || restored.VoWiFiEnabled != original.VoWiFiEnabled || restored.Source != original.Source {
				t.Fatalf("restored=%+v error=%v", restored, err)
			}
		})
	}
}

func TestCellularRegistrationTaskValidation(t *testing.T) {
	s := blockedRegionServer(t, "310260123456789")
	for _, tc := range []struct {
		deviceType, environment string
		wantError               bool
	}{
		{store.DeviceTypePCIeEC20EC25, "cellular", false},
		{store.DeviceTypePCIeEC20EC25, "vowifi", true},
		{store.DeviceTypePCIeEC20EC25, "none", true},
		{store.DeviceTypeUSBSIMReader, "cellular", true},
	} {
		if err := s.store.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "Modem", DeviceType: tc.deviceType}); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"name":"Register","device_id":"dev1","profile_iccid":"test-card","task_type":"cellular_attach","environment":%q,"interval_days":1,"start_date":"2026-10-02","run_time":"12:00","timezone":"UTC","payload":{}}`, tc.environment)
		req := httptest.NewRequest("POST", "/api/automatic-tasks", strings.NewReader(body))
		task, err := s.decodeAutomaticTask(req, 0)
		if (err != nil) != tc.wantError {
			t.Fatalf("device=%s env=%s error=%v", tc.deviceType, tc.environment, err)
		}
		if err == nil {
			if _, err := s.store.SaveAutomaticTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.executeAutomaticTask(context.Background(), store.AutomaticTask{TaskType: "cellular_attach", Environment: "vowifi"}, func(string) {}); err == nil {
		t.Fatal("invalid environment reached execution")
	}
}
