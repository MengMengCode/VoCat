package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
)

// fakeDeviceController is a stub DeviceController that resolves one discovered
// device carrying a fixed snapshot, enough to drive the region guards. The scan,
// USSD, and USB-net results are configurable for the feature endpoint tests.
type fakeDeviceController struct {
	entry       device.Device
	atResponse  modem.Response
	atErr       error
	atHandler   func(string) (modem.Response, error)
	scanResult  device.OperatorScanResult
	scanErr     error
	ussdResult  device.USSDResult
	ussdErr     error
	usbNetMode  device.USBNetMode
	usbNetErr   error
	smsMessages []device.SMSMessage
	smsErr      error
}

func (f fakeDeviceController) Discover(context.Context) ([]device.Device, error) {
	return []device.Device{f.entry}, nil
}
func (f fakeDeviceController) List() []device.Device { return []device.Device{f.entry} }
func (f fakeDeviceController) Get(id string) (device.Device, error) {
	if id == f.entry.ID {
		return f.entry, nil
	}
	return device.Device{}, device.ErrNotFound
}
func (f fakeDeviceController) Refresh(context.Context, string) (device.Snapshot, error) {
	return device.Snapshot{}, nil
}

func (f fakeDeviceController) ExecuteAT(_ context.Context, _ string, command string) (modem.Response, error) {
	if f.atHandler != nil {
		return f.atHandler(command)
	}
	return f.atResponse, f.atErr
}
func (f fakeDeviceController) Reboot(context.Context, string) error { return nil }
func (f fakeDeviceController) USSD(context.Context, string, string) (device.USSDResult, error) {
	return f.ussdResult, f.ussdErr
}
func (f fakeDeviceController) ContinueUSSD(context.Context, string, string) (device.USSDResult, error) {
	return f.ussdResult, f.ussdErr
}
func (f fakeDeviceController) CancelUSSD(context.Context, string) error { return f.ussdErr }
func (f fakeDeviceController) SetFlight(context.Context, string, bool) (device.FlightResult, error) {
	return device.FlightResult{}, nil
}
func (f fakeDeviceController) SetNetwork(context.Context, string, device.NetworkRequest) (device.NetworkResult, error) {
	return device.NetworkResult{}, nil
}
func (f fakeDeviceController) USBNetMode(context.Context, string) (device.USBNetMode, error) {
	return device.USBNetMode{}, nil
}
func (f fakeDeviceController) SetUSBNetMode(context.Context, string, int) (device.USBNetMode, error) {
	return device.USBNetMode{}, nil
}
func (f fakeDeviceController) SetUSBNetModeByPort(context.Context, string, int) (device.USBNetMode, error) {
	return f.usbNetMode, f.usbNetErr
}
func (f fakeDeviceController) OperatorSelection(context.Context, string) (device.OperatorSelection, error) {
	return device.OperatorSelection{}, nil
}
func (f fakeDeviceController) SetOperatorSelection(context.Context, string, bool, string, *int) (device.OperatorSelection, error) {
	return device.OperatorSelection{}, nil
}

func (f fakeDeviceController) ReRegisterOperator(context.Context, string) (device.OperatorSelection, error) {
	return device.OperatorSelection{}, nil
}
func (f fakeDeviceController) ScanOperators(context.Context, string) (device.OperatorScanResult, error) {
	return f.scanResult, f.scanErr
}
func (f fakeDeviceController) SendSMS(context.Context, string, string, string) (device.SMSSendResult, error) {
	return device.SMSSendResult{}, nil
}
func (f fakeDeviceController) ListSMS(context.Context, string) (device.SMSListing, error) {
	return device.SMSListing{Messages: append([]device.SMSMessage(nil), f.smsMessages...)}, f.smsErr
}
func (f fakeDeviceController) ReadSMS(context.Context, string, int) (device.SMSMessage, error) {
	return device.SMSMessage{}, nil
}
func (f fakeDeviceController) DeleteSMS(context.Context, string, int) error { return nil }
func (f fakeDeviceController) DeleteSMSFromStorage(context.Context, string, string, int) error {
	return nil
}
func (f fakeDeviceController) ESIMListProfiles(context.Context, string) (device.EsimInfo, error) {
	return device.EsimInfo{}, nil
}
func (f fakeDeviceController) ESIMInventory(context.Context, string) ([]device.EsimInventoryEntry, error) {
	return []device.EsimInventoryEntry{}, nil
}
func (f fakeDeviceController) ESIMSwitchProfile(context.Context, string, string, string) error {
	return nil
}
func (f fakeDeviceController) ESIMDisableProfile(context.Context, string, string, string) error {
	return nil
}
func (f fakeDeviceController) ESIMRenameProfile(context.Context, string, string, string, string) error {
	return nil
}
func (f fakeDeviceController) ESIMDownloadProfile(context.Context, string, device.EsimDownloadParams, func(device.EsimProgress)) (*device.EsimDownloadResult, error) {
	return nil, errors.New("not implemented in test fake")
}
func (f fakeDeviceController) ESIMDeleteProfile(context.Context, string, string, string) (*device.EsimDeleteResult, error) {
	return &device.EsimDeleteResult{}, nil
}
func (f fakeDeviceController) ESIMChipInfo(context.Context, string) (*device.EsimChipInfo, error) {
	return nil, errors.New("not implemented in test fake")
}

func regionTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func blockedRegionServer(t *testing.T, imsi string) *Server {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{
		store:               database,
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices: fakeDeviceController{entry: device.Device{
			ID:         "dev1",
			Discovered: true,
			Snapshot:   &device.Snapshot{DeviceID: "dev1", IMSI: imsi},
		}},
		vowifi: &fakeVoWiFiController{},
	}
}

func TestModemSummaryRegionFields(t *testing.T) {
	t.Parallel()
	blocked := modemSummary(&device.Snapshot{IMSI: "460001234567890"}, "", "")
	if blocked["service_blocked"] != true {
		t.Fatalf("service_blocked = %v, want true", blocked["service_blocked"])
	}
	if blocked["card_mcc"] != "460" || blocked["card_country"] != "中国" {
		t.Fatalf("card_mcc=%v card_country=%v", blocked["card_mcc"], blocked["card_country"])
	}
	if reason, _ := blocked["blocked_reason"].(string); reason == "" {
		t.Fatal("blocked_reason must be set for a blocked card")
	}

	allowed := modemSummary(&device.Snapshot{IMSI: "310260123456789"}, "", "")
	if allowed["service_blocked"] != false || allowed["blocked_reason"] != "" {
		t.Fatalf("allowed card summary = %v / %v", allowed["service_blocked"], allowed["blocked_reason"])
	}
	if allowed["card_mcc"] != "310" || allowed["card_country"] != "美国" {
		t.Fatalf("allowed card_mcc=%v card_country=%v", allowed["card_mcc"], allowed["card_country"])
	}

	empty := modemSummary(nil, "", "")
	if empty["service_blocked"] != false || empty["card_mcc"] != "" {
		t.Fatalf("nil snapshot summary = %v / %v", empty["service_blocked"], empty["card_mcc"])
	}
}

func TestWriteDeviceErrorMapsRegionBlockedTo403(t *testing.T) {
	t.Parallel()
	server := &Server{logger: regionTestLogger()}
	recorder := httptest.NewRecorder()
	server.writeDeviceError(recorder, device.ErrRegionBlocked)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "region_blocked" {
		t.Fatalf("error code = %q, want region_blocked", envelope.Error.Code)
	}
}

func TestCountryNameForMCC(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"460": "中国",
		"461": "中国",
		"454": "中国香港",
		"466": "中国台湾",
		"310": "美国",
		"":    "",
		"999": "",
	}
	for mcc, want := range cases {
		if got := countryNameForMCC(mcc); got != want {
			t.Errorf("countryNameForMCC(%q) = %q, want %q", mcc, got, want)
		}
	}
}

func TestHandleVoWiFiEnabledBlockedRegion(t *testing.T) {
	server := blockedRegionServer(t, "460001234567890")
	request := httptest.NewRequest(http.MethodPatch, "/devices/dev1/vowifi", strings.NewReader(`{"enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	config := store.Device{ID: "dev1"}
	if handled := server.handleVoWiFiEnabled(recorder, request, config, true); !handled {
		t.Fatal("handler did not claim the request")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "region_blocked" {
		t.Fatalf("error code = %q, want region_blocked", envelope.Error.Code)
	}
	// The block must happen before any state change is persisted.
	if _, err := server.store.Device(context.Background(), "dev1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("device config must not be written for a blocked region, err=%v", err)
	}
}

func TestHandleVoWiFiEnabledAllowedRegionPassesGuard(t *testing.T) {
	server := blockedRegionServer(t, "310260123456789")
	// Seed the device so the post-guard UpsertDevice succeeds.
	if err := server.store.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "EC20"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/devices/dev1/vowifi", strings.NewReader(`{"enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	server.handleVoWiFiEnabled(recorder, request, store.Device{ID: "dev1"}, true)
	if recorder.Code == http.StatusForbidden {
		t.Fatalf("allowed region must not be blocked, got 403: %s", recorder.Body.String())
	}
}
