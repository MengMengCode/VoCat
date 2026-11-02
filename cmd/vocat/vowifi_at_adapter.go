package main

import (
	"context"
	"sync"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type atVoWiFiAdapter interface {
	vowifiDeviceAdapter
	vowifi.SMSCenterReader
	ReadSMSCenterPSI(context.Context, string) (string, error)
}

func voWiFiAdapterForDevice(deviceType string, lookup func(context.Context) (device.Device, error), ec20, zxic atVoWiFiAdapter, pcsc, native vowifiDeviceAdapter) vowifiDeviceAdapter {
	switch deviceType {
	case store.DeviceTypeUSBSIMReader:
		return pcsc
	case store.DeviceTypeWiFi410:
		return native
	default:
		return newResolvedATVoWiFiAdapter(deviceType, lookup, ec20, zxic)
	}
}

// Resolve at operation time: persisted pre-ZXIC devices retain their old type,
// and an orchestrator may be created before its modem is discovered.
type resolvedATVoWiFiAdapter struct {
	mu                   sync.Mutex
	lookup               func(context.Context) (device.Device, error)
	selected, ec20, zxic atVoWiFiAdapter
}

func newResolvedATVoWiFiAdapter(deviceType string, lookup func(context.Context) (device.Device, error), ec20, zxic atVoWiFiAdapter) *resolvedATVoWiFiAdapter {
	selected := ec20
	if deviceType == store.DeviceTypeZXIC {
		selected = zxic
	}
	return &resolvedATVoWiFiAdapter{lookup: lookup, selected: selected, ec20: ec20, zxic: zxic}
}

func (adapter *resolvedATVoWiFiAdapter) current(ctx context.Context) atVoWiFiAdapter {
	entry, err := adapter.lookup(ctx)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if err == nil && entry.Discovered {
		adapter.selected = adapter.ec20
		if modem.ChipsetForUSB(entry.Candidate.VendorID, entry.Candidate.ProductID) == modem.ChipsetZXIC {
			adapter.selected = adapter.zxic
		}
	}
	return adapter.selected
}

func (adapter *resolvedATVoWiFiAdapter) ReadIdentity(ctx context.Context, id string) (vowifi.SIMIdentity, error) {
	return adapter.current(ctx).ReadIdentity(ctx, id)
}
func (adapter *resolvedATVoWiFiAdapter) CheckReady(ctx context.Context, identity vowifi.SIMIdentity) (vowifi.AKAEvidence, error) {
	return adapter.current(ctx).CheckReady(ctx, identity)
}
func (adapter *resolvedATVoWiFiAdapter) Authenticate(ctx context.Context, identity vowifi.SIMIdentity, challenge vowifi.AKAChallenge) (vowifi.AKAResult, error) {
	return adapter.current(ctx).Authenticate(ctx, identity, challenge)
}
func (adapter *resolvedATVoWiFiAdapter) AuthenticateWithPreference(ctx context.Context, identity vowifi.SIMIdentity, challenge vowifi.AKAChallenge, preference string) (vowifi.AKAResult, error) {
	return adapter.current(ctx).AuthenticateWithPreference(ctx, identity, challenge, preference)
}
func (adapter *resolvedATVoWiFiAdapter) Snapshot(ctx context.Context, id string) (vowifi.RadioSnapshot, error) {
	return adapter.current(ctx).Snapshot(ctx, id)
}
func (adapter *resolvedATVoWiFiAdapter) StopCellularData(ctx context.Context, id string) error {
	return adapter.current(ctx).StopCellularData(ctx, id)
}
func (adapter *resolvedATVoWiFiAdapter) EnterVoWiFiRFOff(ctx context.Context, id string) error {
	return adapter.current(ctx).EnterVoWiFiRFOff(ctx, id)
}
func (adapter *resolvedATVoWiFiAdapter) Restore(ctx context.Context, id string, snapshot vowifi.RadioSnapshot) error {
	return adapter.current(ctx).Restore(ctx, id, snapshot)
}
func (adapter *resolvedATVoWiFiAdapter) ReadSMSCenter(ctx context.Context, id string) (string, error) {
	return adapter.current(ctx).ReadSMSCenter(ctx, id)
}
func (adapter *resolvedATVoWiFiAdapter) ReadSMSCenterPSI(ctx context.Context, id string) (string, error) {
	return adapter.current(ctx).ReadSMSCenterPSI(ctx, id)
}
