package main

import (
	"context"
	"errors"
	"testing"

	"vocat/internal/device"
)

type startupFlightSetter struct {
	errors []error
	calls  int
	id     string
}

func (setter *startupFlightSetter) SetFlight(
	_ context.Context,
	id string,
	enabled bool,
) (device.FlightResult, error) {
	setter.calls++
	setter.id = id
	if !enabled {
		return device.FlightResult{}, errors.New("expected flight mode to be enabled")
	}
	if setter.calls <= len(setter.errors) {
		return device.FlightResult{}, setter.errors[setter.calls-1]
	}
	return device.FlightResult{CurrentMode: 4, FlightMode: true, RadioOff: true}, nil
}

func TestProtectVoWiFiStartupRadioRetriesTransientFailure(t *testing.T) {
	transient := errors.New("modem is reopening")
	setter := &startupFlightSetter{errors: []error{transient, transient}}
	if err := protectVoWiFiStartupRadioWithRetry(
		context.Background(), setter, "quectel-1", 3, 0,
	); err != nil {
		t.Fatalf("protect startup radio: %v", err)
	}
	if setter.calls != 3 || setter.id != "quectel-1" {
		t.Fatalf("SetFlight calls = %d, id = %q", setter.calls, setter.id)
	}
}

func TestProtectVoWiFiStartupRadioReturnsLastFailure(t *testing.T) {
	first := errors.New("first")
	last := errors.New("last")
	setter := &startupFlightSetter{errors: []error{first, last}}
	err := protectVoWiFiStartupRadioWithRetry(
		context.Background(), setter, "quectel-1", 2, 0,
	)
	if !errors.Is(err, last) || setter.calls != 2 {
		t.Fatalf("protect startup radio = %v after %d calls", err, setter.calls)
	}
}
