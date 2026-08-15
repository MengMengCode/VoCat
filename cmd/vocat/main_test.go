package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
)

// fakeModemClient is a minimal scripted modem.Client for exercising the region
// enforcement orchestration (flight-mode flips) without hardware.
type fakeModemClient struct {
	steps []fakeStep
	index int
}

type fakeStep struct {
	command string
	lines   []string
}

func (client *fakeModemClient) Execute(_ context.Context, command string) (modem.Response, error) {
	if client.index >= len(client.steps) {
		return modem.Response{}, fmt.Errorf("unexpected command %q", command)
	}
	step := client.steps[client.index]
	client.index++
	if command != step.command {
		return modem.Response{}, fmt.Errorf("command %q, want %q", command, step.command)
	}
	return modem.Response{Command: command, Lines: step.lines, Final: "OK"}, nil
}

func (client *fakeModemClient) WaitURC(context.Context, func(string) bool) (string, error) {
	return "", errors.New("no URC scripted")
}

func (client *fakeModemClient) Close() error { return nil }

func (client *fakeModemClient) assertExhausted(t *testing.T) {
	t.Helper()
	if client.index != len(client.steps) {
		t.Fatalf("consumed %d of %d scripted commands", client.index, len(client.steps))
	}
}

type fakeDiscoverer struct{ candidates []modem.Candidate }

func (discoverer fakeDiscoverer) Discover(context.Context) ([]modem.Candidate, error) {
	return discoverer.candidates, nil
}

type fakeOpener struct{ client modem.Client }

func (opener fakeOpener) Open(context.Context, modem.Port) (modem.Client, error) {
	return opener.client, nil
}

const regionTestDeviceID = "quectel-region-test"

func newRegionTestManager(t *testing.T, client modem.Client) *device.Manager {
	t.Helper()
	manager, err := device.NewManager(device.Options{
		Discoverer: fakeDiscoverer{candidates: []modem.Candidate{{
			ID:      regionTestDeviceID,
			Product: "EC20",
			ATPort:  modem.Port{Path: "/dev/ttyUSB2", Role: modem.PortRoleAT},
		}}},
		Opener:         fakeOpener{client: client},
		CommandTimeout: time.Second,
		LongTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager
}

func newRegionTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func regionTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnforceCardRegionForcesAirplaneAndPersistsPolicy(t *testing.T) {
	client := &fakeModemClient{steps: []fakeStep{
		{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
		{command: "AT+CFUN=4"},
		{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
	}}
	manager := newRegionTestManager(t, client)
	database := newRegionTestStore(t)

	snapshot := &device.Snapshot{
		DeviceID: regionTestDeviceID,
		SIMReady: true,
		IMSI:     "460001234567890",
		ICCID:    "89860012345678901234",
	}
	enforceCardRegion(context.Background(), regionTestLogger(), database, manager, regionTestDeviceID, snapshot)
	client.assertExhausted(t)

	policy, err := database.CardPolicy(context.Background(), snapshot.ICCID)
	if err != nil {
		t.Fatalf("CardPolicy: %v", err)
	}
	if policy.Source != cardPolicySourceRegionBlock {
		t.Fatalf("policy source = %q, want %q", policy.Source, cardPolicySourceRegionBlock)
	}
	if policy.NetworkEnabled || policy.VoWiFiEnabled || !policy.AirplaneEnabled {
		t.Fatalf("policy switches = %#v, want all service off and airplane on", policy)
	}
}

func TestEnforceCardRegionSkipsRadioWhenAlreadyOff(t *testing.T) {
	client := &fakeModemClient{}
	manager := newRegionTestManager(t, client)
	database := newRegionTestStore(t)

	snapshot := &device.Snapshot{
		DeviceID:   regionTestDeviceID,
		SIMReady:   true,
		IMSI:       "461001234567890",
		ICCID:      "89860012345678901234",
		FlightMode: true,
	}
	enforceCardRegion(context.Background(), regionTestLogger(), database, manager, regionTestDeviceID, snapshot)
	client.assertExhausted(t)

	if _, err := database.CardPolicy(context.Background(), snapshot.ICCID); err != nil {
		t.Fatalf("expected a persisted block policy even with the radio already off: %v", err)
	}
}

func TestEnforceCardRegionLiftsBlockForAllowedSIM(t *testing.T) {
	client := &fakeModemClient{}
	manager := newRegionTestManager(t, client)
	database := newRegionTestStore(t)

	if err := database.UpsertCardPolicy(context.Background(), store.CardPolicy{
		ICCID:           "89860012345678901234",
		AirplaneEnabled: true,
		IPVersion:       "IPV4V6",
		Source:          cardPolicySourceRegionBlock,
	}); err != nil {
		t.Fatalf("seed block policy: %v", err)
	}

	snapshot := &device.Snapshot{
		DeviceID:   regionTestDeviceID,
		SIMReady:   true,
		IMSI:       "310260123456789",
		ICCID:      "89012601234567890123",
		FlightMode: true,
	}
	enforceCardRegion(context.Background(), regionTestLogger(), database, manager, regionTestDeviceID, snapshot)
	client.assertExhausted(t)

	if _, err := database.CardPolicy(context.Background(), "89860012345678901234"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected the auto block policy to be cleared, got err=%v", err)
	}
}

func TestEnforceCardRegionLeavesAllowedSIMWithoutPriorBlockAlone(t *testing.T) {
	client := &fakeModemClient{}
	manager := newRegionTestManager(t, client)
	database := newRegionTestStore(t)

	snapshot := &device.Snapshot{
		DeviceID: regionTestDeviceID,
		SIMReady: true,
		IMSI:     "310260123456789",
		ICCID:    "89012601234567890123",
	}
	enforceCardRegion(context.Background(), regionTestLogger(), database, manager, regionTestDeviceID, snapshot)
	client.assertExhausted(t)
}

func TestEnforceCardRegionIgnoresUnknownOrNotReadySIM(t *testing.T) {
	client := &fakeModemClient{}
	manager := newRegionTestManager(t, client)
	database := newRegionTestStore(t)

	// Not ready: no action at all.
	notReady := &device.Snapshot{DeviceID: regionTestDeviceID, SIMReady: false, IMSI: "460001234567890"}
	enforceCardRegion(context.Background(), regionTestLogger(), database, manager, regionTestDeviceID, notReady)

	// Ready but IMSI unknown: hold state, neither block nor lift.
	unknown := &device.Snapshot{DeviceID: regionTestDeviceID, SIMReady: true, IMSI: ""}
	enforceCardRegion(context.Background(), regionTestLogger(), database, manager, regionTestDeviceID, unknown)

	client.assertExhausted(t)
	policies, err := database.ListCardPolicies(context.Background())
	if err != nil {
		t.Fatalf("ListCardPolicies: %v", err)
	}
	if len(policies) != 0 {
		t.Fatalf("expected no card policies, got %d", len(policies))
	}
}

func TestProvisionedDeviceTypeRecognizesNativeWWAN(t *testing.T) {
	native := modem.Candidate{
		HardwareKind: "wwan",
		USBPath:      "/sys/devices/pci0000:00/0000:00:00.0/wwan/wwan0",
		QMIControl:   "/dev/wwan0qmi0",
		ATPort:       modem.Port{Path: "/dev/wwan0at0"},
	}
	if got := provisionedDeviceType(native); got != store.DeviceTypeWiFi410 {
		t.Fatalf("native WWAN type = %q, want %q", got, store.DeviceTypeWiFi410)
	}

	usb := modem.Candidate{
		USBPath:    "/sys/bus/usb/devices/1-6",
		QMIControl: "/dev/cdc-wdm0",
		ATPort:     modem.Port{Path: "/dev/ttyUSB2"},
	}
	if got := provisionedDeviceType(usb); got != store.DeviceTypePCIeEC20EC25 {
		t.Fatalf("USB modem type = %q, want %q", got, store.DeviceTypePCIeEC20EC25)
	}
}
