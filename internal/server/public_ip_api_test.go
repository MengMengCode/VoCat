package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/exportproxy"
	"vocat/internal/store"
)

type publicIPDeviceController struct{ fakeDeviceController }

func (controller *publicIPDeviceController) SetNetwork(_ context.Context, _ string, request device.NetworkRequest) (device.NetworkResult, error) {
	return device.NetworkResult{Enabled: request.Enabled}, nil
}

func TestPublicIPCacheFollowsCurrentICCID(t *testing.T) {
	server := &Server{publicIPs: make(map[string]cachedPublicIP)}
	want := exportproxy.PublicIPInfo{IP: "203.0.113.8", CountryCode: "GB"}
	server.savePublicIP("ec20", "8944100001", want)

	got, ok := server.loadPublicIP("ec20", "8944100001")
	if !ok || got != want {
		t.Fatalf("loadPublicIP() = (%+v, %v), want (%+v, true)", got, ok, want)
	}

	if _, ok := server.loadPublicIP("ec20", "8944100002"); ok {
		t.Fatal("cache survived an ICCID change")
	}
	if _, ok := server.loadPublicIP("ec20", "8944100001"); ok {
		t.Fatal("stale cache was not deleted after an ICCID change")
	}
}

func TestPublicIPGetClearsCacheWhenCellularDataIsDisabled(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	enabled, err := json.Marshal(map[string]bool{"enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: developer.EnabledSettingKey, Value: enabled}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database, developerEnabled: true, publicIPs: make(map[string]cachedPublicIP)}
	server.savePublicIP("ec20", "8944100001", exportproxy.PublicIPInfo{IP: "203.0.113.8", CountryCode: "GB"})

	request := httptest.NewRequest(http.MethodGet, "/api/devices/ec20/network/public-ip", nil)
	recorder := httptest.NewRecorder()
	server.handleCellularPublicIP(recorder, request, store.Device{ID: "ec20", NetworkEnabled: false}, "8944100001")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", recorder.Code, recorder.Body)
	}
	var response struct {
		Data publicIPResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Detected || response.Data.IP != "" || response.Data.CountryCode != "" {
		t.Fatalf("disabled response = %+v, want empty undetected result", response.Data)
	}
	if _, ok := server.loadPublicIP("ec20", "8944100001"); ok {
		t.Fatal("public IP cache survived disabled GET")
	}
}

func TestPublicIPCacheClearsWhileModemIsResetting(t *testing.T) {
	server := &Server{publicIPs: make(map[string]cachedPublicIP)}
	server.savePublicIP("ec20", "8944100001", exportproxy.PublicIPInfo{IP: "203.0.113.8", CountryCode: "GB"})
	if _, ok := server.loadPublicIP("ec20", ""); ok {
		t.Fatal("cache survived a missing live ICCID")
	}
}

func TestOverviewPublicIPIncludesCurrentCachedSession(t *testing.T) {
	server := &Server{publicIPs: make(map[string]cachedPublicIP)}
	server.savePublicIP("ec20", "8944100001", exportproxy.PublicIPInfo{
		IP: "203.0.113.8", CountryCode: "GB", Region: "England", City: "London", Organization: "Example carrier",
	})
	entry := &device.Device{Snapshot: &device.Snapshot{ICCID: "8944100001"}}
	result := server.overviewPublicIP(store.Device{ID: "ec20", NetworkEnabled: true}, entry, true)
	if result["detected"] != true || result["ip"] != "203.0.113.8" || result["country_code"] != "GB" {
		t.Fatalf("overview public IP = %#v, want current cached session", result)
	}
	if result["region"] != "England" || result["city"] != "London" || result["organization"] != "Example carrier" {
		t.Fatalf("overview public IP metadata = %#v, want cached location and organization", result)
	}

	off := server.overviewPublicIP(store.Device{ID: "ec20", NetworkEnabled: false}, entry, true)
	if off["detected"] != false || off["ip"] != nil {
		t.Fatalf("disabled overview public IP = %#v, want undetected", off)
	}
}

func TestCellularConnectedAutomaticallyDetectsPublicIP(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	enabled, err := json.Marshal(map[string]bool{"enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: developer.EnabledSettingKey, Value: enabled}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20", Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		Interface: "wwan0", NetworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	controller := &publicIPDeviceController{fakeDeviceController: fakeDeviceController{entry: device.Device{
		ID:       "physical-1",
		Snapshot: &device.Snapshot{DeviceID: "physical-1", SIMReady: true, ICCID: "89441000000000000001"},
	}}}
	called := make(chan string, 1)
	server := &Server{
		store: database, developerEnabled: true, devices: controller,
		logger: regionTestLogger(), publicIPs: make(map[string]cachedPublicIP),
		lookupPublicIP: func(_ context.Context, networkInterface string) (exportproxy.PublicIPInfo, error) {
			called <- networkInterface
			return exportproxy.PublicIPInfo{IP: "203.0.113.8", CountryCode: "GB"}, nil
		},
	}
	runtime := server.cellularDataRuntime()
	runtime.start(ctx)
	requested := runtime.request("ec20", "physical-1", device.NetworkRequest{Enabled: true})
	waitContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if status, waitErr := runtime.wait(waitContext, "ec20", requested.Revision); waitErr != nil || !status.Connected {
		t.Fatalf("connected status = %+v, err = %v", status, waitErr)
	}
	select {
	case networkInterface := <-called:
		if networkInterface != "wwan0" {
			t.Fatalf("lookup interface = %q, want wwan0", networkInterface)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic public IP lookup was not started")
	}
	cacheDeadline := time.Now().Add(time.Second)
	for time.Now().Before(cacheDeadline) {
		if info, ok := server.loadPublicIP("ec20", "89441000000000000001"); ok && info.IP == "203.0.113.8" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("automatic public IP result was not saved")
}
