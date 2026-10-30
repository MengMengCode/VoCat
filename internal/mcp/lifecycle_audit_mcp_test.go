package mcp

import (
	"context"
	"errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
	"vocat/internal/device"
)

type lifecycleAuditDevices struct {
	fakeDevices
	lists int
}

func (d *lifecycleAuditDevices) ESIMListProfiles(context.Context, string) (device.EsimInfo, error) {
	d.lists++
	return device.EsimInfo{}, errors.New("SIM is leased by active VoWiFi")
}

func TestAuditMCPSwitchDelegatesBeforeAnyHardwareAccess(t *testing.T) {
	devices := &lifecycleAuditDevices{fakeDevices: fakeDevices{entries: []device.Device{onlineEntry()}}}
	s, _ := newTestServer(t, devices, nil)
	var called int
	s.options.SwitchESIM = func(_ context.Context, id, iccid, aid string) error {
		called++
		if id != configuredID || iccid != "8985000000000000002" || aid != "" {
			t.Fatalf("unexpected configured-device operation arguments: %q %q %q", id, iccid, aid)
		}
		return nil
	}
	_, result, err := s.esimSwitchProfile(context.Background(), nil, esimSwitchInput{
		DeviceID: configuredID, ICCID: "8985000000000000002",
	})
	if err != nil || called != 1 || result.SwitchedTo != "8985000000000000002" {
		t.Fatalf("switch callback not used: calls=%d result=%+v error=%v", called, result, err)
	}
	if devices.lists != 0 || len(devices.switches) != 0 {
		t.Fatalf("MCP touched hardware before the configured operation: lists=%d switches=%+v", devices.lists, devices.switches)
	}
}

func TestAuditMCPSwitchFailsClosedWithoutConfiguredOperation(t *testing.T) {
	devices := &fakeDevices{entries: []device.Device{onlineEntry()}}
	s, _ := newTestServer(t, devices, nil)
	if _, _, err := s.esimSwitchProfile(context.Background(), nil, esimSwitchInput{
		DeviceID: configuredID, ICCID: "8985000000000000002", AID: "A0000005591010",
	}); err == nil {
		t.Fatal("missing operation callback allowed raw device switching")
	}
	if len(devices.switches) != 0 {
		t.Fatalf("direct low-level switch occurred: %+v", devices.switches)
	}
}

func TestAuditMCPSwitchRegistrationRequiresConfiguredOperation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "without callback", true: "with callback"}[enabled], func(t *testing.T) {
			s, database := newTestServer(t, &fakeDevices{}, nil)
			options := s.options
			options.Store = database
			if enabled {
				options.SwitchESIM = func(context.Context, string, string, string) error { return nil }
			}
			s, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			clientTransport, serverTransport := sdk.NewInMemoryTransports()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go func() { _ = s.sdk.Run(ctx, serverTransport) }()
			client := sdk.NewClient(&sdk.Implementation{Name: "audit", Version: "test"}, nil)
			session, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range tools.Tools {
				found = found || tool.Name == "esim_switch_profile"
			}
			if found != enabled {
				t.Fatalf("switch tool exposed=%v, callback configured=%v", found, enabled)
			}
		})
	}
}
