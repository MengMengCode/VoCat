package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestProxyResolverUsesICCIDProfileBinding(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "ec20", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertUpstreamProxy(context.Background(), store.UpstreamProxy{
		ID:       "clash",
		Name:     "Clash",
		Addr:     "192.168.2.143:7897",
		Enabled:  true,
		Password: "must-not-be-lost",
		Username: "proxy-user",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDeviceProxyBinding(context.Background(), store.DeviceProxyBinding{
		DeviceID:        "ec20",
		ICCID:           "89441000400128014257",
		ProfileName:     "Vodafone UK",
		UpstreamProxyID: "clash",
	}); err != nil {
		t.Fatal(err)
	}
	route, err := (ProxyResolver{Store: database}).Resolve(
		context.Background(),
		vowifi.ProxyRequest{DeviceID: "ec20", ICCID: "89441000400128014257", HomeMCC: "234", HomeMNC: "15"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if route.Mode != vowifi.ProxyModeSOCKS5 ||
		route.Address != "192.168.2.143:7897" ||
		route.Username != "proxy-user" ||
		route.Password != "must-not-be-lost" {
		t.Fatalf("route = %#v", route)
	}
}

func TestProxyResolverDoesNotLeakBindingToAnotherProfileOnSameDevice(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "ec20", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertUpstreamProxy(context.Background(), store.UpstreamProxy{ID: "proxy", Name: "Proxy", Addr: "127.0.0.1:1080", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDeviceProxyBinding(context.Background(), store.DeviceProxyBinding{DeviceID: "ec20", ICCID: "89441000400128014257", ProfileName: "A", UpstreamProxyID: "proxy"}); err != nil {
		t.Fatal(err)
	}
	route, err := (ProxyResolver{Store: database}).Resolve(context.Background(), vowifi.ProxyRequest{DeviceID: "ec20", ICCID: "89104100000028106378"})
	if err != nil {
		t.Fatal(err)
	}
	if route.Mode != vowifi.ProxyModeDirect {
		t.Fatalf("route = %#v, want direct for unbound ICCID", route)
	}
}

func TestProxyResolverDoesNotUseCountryRuleWithoutDeviceBinding(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertUpstreamProxy(context.Background(), store.UpstreamProxy{
		ID: "legacy", Name: "Legacy", Addr: "127.0.0.1:1080", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertCountryRule(context.Background(), store.CountryRule{
		CountryCode: "GB", CountryName: "United Kingdom", UpstreamProxyID: "legacy", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	route, err := (ProxyResolver{Store: database}).Resolve(
		context.Background(),
		vowifi.ProxyRequest{DeviceID: "ec20", HomeMCC: "234"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if route.Mode != vowifi.ProxyModeDirect {
		t.Fatalf("route = %#v, want direct", route)
	}
}

func TestPhoneStoreRejectsUntrustedSource(t *testing.T) {
	database := testStore(t)
	phones := PhoneStore{Store: database, DeviceID: "ec20"}
	record := vowifi.PhoneRecord{
		ICCID:     "89441000400311061404",
		Number:    "+447700900123",
		Source:    "imsi_guess",
		UpdatedAt: time.Now(),
	}
	if err := phones.SaveAssociatedNumber(context.Background(), record); err == nil {
		t.Fatal("untrusted source was accepted")
	}
	record.Source = vowifi.PhoneSourcePAssociatedURI
	if err := phones.SaveAssociatedNumber(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got, err := database.PhoneAssociation(context.Background(), record.ICCID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != record.Number || got.Source != record.Source {
		t.Fatalf("association = %#v", got)
	}
}

func TestStateProjectorRestoresVerifiedNumber(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:   "ec20",
		Name: "EC20",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertPhoneAssociation(context.Background(), store.PhoneAssociation{
		ICCID:    "89441000400311061404",
		DeviceID: "ec20",
		Number:   "+447700900123",
		Source:   vowifi.PhoneSourcePAssociatedURI,
	}); err != nil {
		t.Fatal(err)
	}
	projector := StateProjector{
		Store: database,
		Devices: staticDeviceReader{
			iccid: "89441000400311061404",
			imsi:  "234159598901845",
		},
	}
	if err := projector.Save(context.Background(), vowifi.State{
		DeviceID:  "ec20",
		Phase:     vowifi.PhaseIdle,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := database.VoWiFiRuntime(context.Background(), "ec20")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.LocalPhone != "+447700900123" ||
		runtime.PhoneNumberSource != vowifi.PhoneSourcePAssociatedURI {
		t.Fatalf("runtime phone = %q (%q)", runtime.LocalPhone, runtime.PhoneNumberSource)
	}
	var tunnel map[string]any
	if err := json.Unmarshal(runtime.Tunnel, &tunnel); err != nil {
		t.Fatal(err)
	}
}

func TestStateProjectorPreservesConcreteDataplaneMode(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:   "ec25",
		Name: "EC25",
	}); err != nil {
		t.Fatal(err)
	}
	projector := StateProjector{Store: database}
	if err := projector.Save(context.Background(), vowifi.State{
		DeviceID:      "ec25",
		Phase:         vowifi.PhaseIMSReady,
		TunnelReady:   true,
		IMSReady:      true,
		TunnelName:    "vocat-swu-ec25",
		DataplaneMode: "userspace",
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := database.VoWiFiRuntime(context.Background(), "ec25")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.DataplaneMode != "userspace" {
		t.Fatalf("dataplane mode = %q, want userspace", runtime.DataplaneMode)
	}
	var tunnel map[string]any
	if err := json.Unmarshal(runtime.Tunnel, &tunnel); err != nil {
		t.Fatal(err)
	}
	if tunnel["dataplane_mode"] != "userspace" {
		t.Fatalf("tunnel dataplane mode = %#v", tunnel["dataplane_mode"])
	}
}

func TestStateProjectorDoesNotAttachOldSessionNumberToNewLiveSIM(t *testing.T) {
	database := testStore(t)
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "ec20", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	projector := StateProjector{
		Store: database,
		Devices: staticDeviceReader{
			iccid: "89104100000028106378",
			imsi:  "310380500712483",
		},
	}
	if err := projector.Save(context.Background(), vowifi.State{
		DeviceID:          "ec20",
		ICCID:             "89441000400128014257",
		IMSI:              "234159608751160",
		Phase:             vowifi.PhaseStopping,
		PhoneNumber:       "+447386083638",
		PhoneNumberSource: vowifi.PhoneSourcePAssociatedURI,
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := database.VoWiFiRuntime(context.Background(), "ec20")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ICCID != "89441000400128014257" || runtime.IMSI != "234159608751160" {
		t.Fatalf("runtime identity = %q/%q", runtime.ICCID, runtime.IMSI)
	}
	if runtime.LocalPhone != "+447386083638" {
		t.Fatalf("runtime phone = %q", runtime.LocalPhone)
	}
}

type staticDeviceReader struct {
	iccid string
	imsi  string
}

func (reader staticDeviceReader) Get(string) (device.Device, error) {
	return device.Device{
		Snapshot: &device.Snapshot{
			ICCID: reader.iccid,
			IMSI:  reader.imsi,
		},
	}, nil
}
