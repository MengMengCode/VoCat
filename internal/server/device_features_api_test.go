package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/exportproxy"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/update"
	"vocat/internal/vowifi"
)

func decodeData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, recorder.Body.String())
	}
	return envelope.Data
}

type recordingVoWiFiInvalidator struct {
	mu            sync.Mutex
	state         vowifi.State
	stateErr      error
	enabled       []bool
	invalidated   []string
	invalidateErr error
	events        *[]string
	guardHeld     bool
}

func (controller *recordingVoWiFiInvalidator) State(string) (vowifi.State, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.state, controller.stateErr
}

func (controller *recordingVoWiFiInvalidator) RequestEnabled(_ string, enabled bool) (vowifi.State, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.enabled = append(controller.enabled, enabled)
	if controller.events != nil {
		*controller.events = append(*controller.events, "quiesce")
	}
	if !enabled && controller.stateErr == nil {
		controller.state.Enabled = false
		controller.state.Active = false
		controller.state.Phase = vowifi.PhaseIdle
	}
	return controller.state, controller.stateErr
}

func (controller *recordingVoWiFiInvalidator) RequestReconnect(string) (vowifi.State, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.state, controller.stateErr
}

func (controller *recordingVoWiFiInvalidator) BeginSubscriberChange(
	_ context.Context,
	deviceID string,
) (func(), error) {
	controller.mu.Lock()
	controller.invalidated = append(controller.invalidated, deviceID)
	if controller.events != nil {
		*controller.events = append(*controller.events, "begin")
	}
	if controller.invalidateErr != nil {
		err := controller.invalidateErr
		controller.mu.Unlock()
		return nil, err
	}
	controller.guardHeld = true
	controller.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			controller.mu.Lock()
			controller.guardHeld = false
			if controller.events != nil {
				*controller.events = append(*controller.events, "release")
			}
			controller.mu.Unlock()
		})
	}, nil
}

type recordingESIMDeviceController struct {
	fakeDeviceController
	events    *[]string
	guard     *recordingVoWiFiInvalidator
	switchErr error
}

func (controller *recordingESIMDeviceController) ESIMSwitchProfile(context.Context, string, string, string) error {
	if controller.guard != nil {
		controller.guard.mu.Lock()
		held := controller.guard.guardHeld
		controller.guard.mu.Unlock()
		if !held {
			return errors.New("subscriber-change guard was not held during switch")
		}
	}
	if controller.events != nil {
		*controller.events = append(*controller.events, "switch")
	}
	return controller.switchErr
}

func (controller *recordingESIMDeviceController) ESIMDisableProfile(context.Context, string, string, string) error {
	if controller.guard != nil {
		controller.guard.mu.Lock()
		held := controller.guard.guardHeld
		controller.guard.mu.Unlock()
		if !held {
			return errors.New("subscriber-change guard was not held during disable")
		}
	}
	if controller.events != nil {
		*controller.events = append(*controller.events, "disable")
	}
	return nil
}

func TestAttachSingleEUICCIdentityFillsProfileGroupMetadataKey(t *testing.T) {
	groups := []map[string]any{{"eid": "", "aidHex": "", "profiles": []any{}}}
	chipInfo := map[string]any{
		"eids": []any{map[string]any{
			"eid": "89086030202200000025000015085962",
			"aid": "A0000005591010FFFFFFFF8900000100",
		}},
	}
	attachSingleEUICCIdentity(groups, chipInfo)
	if groups[0]["eid"] != "89086030202200000025000015085962" {
		t.Fatalf("group EID = %v", groups[0]["eid"])
	}
	if groups[0]["aidHex"] != "A0000005591010FFFFFFFF8900000100" {
		t.Fatalf("group AID = %v", groups[0]["aidHex"])
	}
}

func TestPhysicalMatchesConfigRejectsDuplicateAndroidSerialAlias(t *testing.T) {
	config := store.Device{
		ID:        "EC20",
		ATPort:    "/dev/serial/by-id/usb-Android_Android-if02-port0",
		USBPath:   "/sys/bus/usb/devices/1-6",
		ModemIMEI: "111111111111111",
	}
	newModem := device.Device{
		ID: "quectel-0125-1-5",
		Candidate: modem.Candidate{
			USBPath: "/sys/bus/usb/devices/1-5",
			ATPort: modem.Port{
				Path:       "/dev/ttyUSB6",
				StablePath: config.ATPort,
			},
		},
		Snapshot: &device.Snapshot{IMEI: "222222222222222"},
	}
	if physicalMatchesConfig(newModem, config) {
		t.Fatal("different modem matched through a duplicated Android by-id alias")
	}

	movedOriginal := newModem
	movedOriginal.Snapshot = &device.Snapshot{IMEI: config.ModemIMEI}
	if !physicalMatchesConfig(movedOriginal, config) {
		t.Fatal("same IMEI should follow the modem to a different USB port")
	}
}

func TestFindDiscoveredDevicePrefersPhysicalIdentityOverSerialAlias(t *testing.T) {
	alias := "/dev/serial/by-id/usb-Android_Android-if02-port0"
	devices := []device.Device{
		{ID: "old", Candidate: modem.Candidate{USBPath: "/sys/bus/usb/devices/1-6", ATPort: modem.Port{StablePath: alias}}},
		{ID: "new", Candidate: modem.Candidate{USBPath: "/sys/bus/usb/devices/1-5", ATPort: modem.Port{StablePath: alias}}},
	}
	selected := findDiscoveredDevice(devices, deviceConfigPayload{
		USBPath: "/sys/bus/usb/devices/1-5",
		ATPort:  alias,
	})
	if selected == nil || selected.ID != "new" {
		t.Fatalf("selected = %#v, want new physical USB device", selected)
	}
}

func TestHandleOperatorScanReturnsOperators(t *testing.T) {
	server := &Server{
		logger: regionTestLogger(),
		devices: fakeDeviceController{scanResult: device.OperatorScanResult{
			Status: "complete",
			Operators: []device.ScannedOperator{
				{Status: "current", Name: "China Mobile", Numeric: "46000", Act: "LTE"},
				{Status: "available", Name: "China Unicom", Numeric: "46001", Act: "LTE"},
			},
		}},
	}
	recorder := httptest.NewRecorder()
	server.handleOperatorScan(recorder, httptest.NewRequest(http.MethodGet, "/scan", nil), "dev1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	data := decodeData(t, recorder)
	if data["status"] != "complete" {
		t.Fatalf("scan status = %v", data["status"])
	}
	candidates, ok := data["candidates"].([]any)
	if !ok || len(candidates) != 2 {
		t.Fatalf("candidates = %v", data["candidates"])
	}
	first := candidates[0].(map[string]any)
	if first["plmn"] != "46000" || first["status"] != "current" || first["operatorName"] != "China Mobile" {
		t.Fatalf("first candidate = %v", first)
	}
}

func TestHandleOperatorScanStreamEmitsTerminalEvent(t *testing.T) {
	server := &Server{
		logger: regionTestLogger(),
		devices: fakeDeviceController{scanResult: device.OperatorScanResult{
			Status:    "complete",
			Operators: []device.ScannedOperator{{Status: "current", Name: "CMCC", Numeric: "46000"}},
		}},
	}
	recorder := httptest.NewRecorder()
	server.handleOperatorScanStream(recorder, httptest.NewRequest(http.MethodGet, "/scan/stream", nil), "dev1")
	body := recorder.Body.String()
	if !strings.Contains(body, "event: operator_scan") {
		t.Fatalf("expected operator_scan events, got %q", body)
	}
	if !strings.Contains(body, `"status":"running"`) || !strings.Contains(body, `"status":"complete"`) {
		t.Fatalf("expected running then complete, got %q", body)
	}
}

func TestHandleUSSDContinueAndCancel(t *testing.T) {
	server := &Server{
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices: fakeDeviceController{ussdResult: device.USSDResult{
			Status: "awaiting_input", Text: "Main menu", SessionID: "abc123", Continueable: true,
		}},
	}
	request := httptest.NewRequest(http.MethodPost, "/continue", strings.NewReader(`{"session_id":"abc123","input":"1"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleUSSDContinue(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("continue status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	data := decodeData(t, recorder)
	result, _ := data["result"].(map[string]any)
	if result["status"] != "awaiting_input" || data["session_id"] != "abc123" {
		t.Fatalf("continue data = %v", data)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/cancel", strings.NewReader(`{"session_id":"abc123"}`))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRec := httptest.NewRecorder()
	server.handleUSSDCancel(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body=%s", cancelRec.Code, cancelRec.Body.String())
	}
}

func TestHandleUSSDContinueRequiresSession(t *testing.T) {
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096, devices: fakeDeviceController{}}
	request := httptest.NewRequest(http.MethodPost, "/continue", strings.NewReader(`{"input":"1"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleUSSDContinue(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing session status = %d, want 400", recorder.Code)
	}
}

func TestHandleCardPoliciesListsAll(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertCardPolicy(context.Background(), store.CardPolicy{
		ICCID: "89860001", NetworkEnabled: true, IPVersion: "IPV4V6", Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database, logger: regionTestLogger()}
	recorder := httptest.NewRecorder()
	server.handleCardPolicies(recorder, httptest.NewRequest(http.MethodGet, "/api/cards/policies", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0]["iccid"] != "89860001" {
		t.Fatalf("policies = %v", envelope.Data)
	}
}

func TestHandleESIMShapes(t *testing.T) {
	server := &Server{logger: regionTestLogger()}

	overview := httptest.NewRecorder()
	server.handleESIM(overview, httptest.NewRequest(http.MethodGet, "/esim", nil), []string{}, "dev1", "dev1", false)
	if overview.Code != http.StatusOK {
		t.Fatalf("overview status = %d", overview.Code)
	}
	if data := decodeData(t, overview); data["chipInfo"] != nil {
		t.Fatalf("overview chipInfo = %v, want nil (empty state)", data["chipInfo"])
	}

	profiles := httptest.NewRecorder()
	server.handleESIM(profiles, httptest.NewRequest(http.MethodGet, "/esim/profiles", nil), []string{"profiles"}, "dev1", "dev1", false)
	if profiles.Code != http.StatusOK {
		t.Fatalf("profiles status = %d", profiles.Code)
	}

	notif := httptest.NewRecorder()
	server.handleESIM(notif, httptest.NewRequest(http.MethodGet, "/esim/notifications", nil), []string{"notifications"}, "dev1", "dev1", false)
	if notif.Code != http.StatusOK {
		t.Fatalf("notifications status = %d", notif.Code)
	}

	// Download is a GET+SSE endpoint, so POST is rejected.
	downloadPost := httptest.NewRecorder()
	server.handleESIM(downloadPost, httptest.NewRequest(http.MethodPost, "/esim/actions/download", nil), []string{"actions", "download"}, "dev1", "dev1", false)
	if downloadPost.Code != http.StatusMethodNotAllowed {
		t.Fatalf("download POST status = %d, want 405", downloadPost.Code)
	}

	// Download with no device manager reports 503.
	download := httptest.NewRecorder()
	server.handleESIM(download, httptest.NewRequest(http.MethodGet, "/esim/actions/download?smdp=rsp.example.com", nil), []string{"actions", "download"}, "dev1", "dev1", false)
	if download.Code != http.StatusServiceUnavailable {
		t.Fatalf("download (no device) status = %d, want 503", download.Code)
	}

	// Switch with no physical modem present reports 503.
	absent := httptest.NewRecorder()
	server.handleESIM(absent, httptest.NewRequest(http.MethodPost, "/esim/actions/switch", strings.NewReader(`{"iccid":"8900000000000000001"}`)), []string{"actions", "switch"}, "dev1", "dev1", false)
	if absent.Code != http.StatusServiceUnavailable {
		t.Fatalf("switch (no device) status = %d, want 503", absent.Code)
	}

	// Switch happy path: a present device + fake controller switches by ICCID.
	present := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096, devices: fakeDeviceController{}}
	swOK := httptest.NewRecorder()
	swReq := httptest.NewRequest(http.MethodPost, "/esim/actions/switch", strings.NewReader(`{"iccid":"8900000000000000001","aid_hex":"A0"}`))
	swReq.Header.Set("Content-Type", "application/json")
	present.handleESIM(swOK, swReq, []string{"actions", "switch"}, "dev1", "dev1", true)
	if swOK.Code != http.StatusOK {
		t.Fatalf("switch happy-path status = %d, body=%s", swOK.Code, swOK.Body.String())
	}
	if data := decodeData(t, swOK); data["status"] != "switched" || data["verified"] != true {
		t.Fatalf("switch data = %v", data)
	}

	// Disable happy path routes the active profile to ES10c DisableProfile.
	disableOK := httptest.NewRecorder()
	disableReq := httptest.NewRequest(http.MethodPost, "/esim/actions/disable", strings.NewReader(`{"iccid":"8900000000000000001","aid_hex":"A0000005591010FFFFFFFF8900000100"}`))
	disableReq.Header.Set("Content-Type", "application/json")
	present.handleESIM(disableOK, disableReq, []string{"actions", "disable"}, "dev1", "dev1", true)
	if disableOK.Code != http.StatusOK {
		t.Fatalf("disable happy-path status = %d, body=%s", disableOK.Code, disableOK.Body.String())
	}
	if data := decodeData(t, disableOK); data["status"] != "disabled" || data["recovering"] != true {
		t.Fatalf("disable data = %v", data)
	}

	// Rename happy path routes PATCH to ES10c SetNickname support.
	renameOK := httptest.NewRecorder()
	renameReq := httptest.NewRequest(http.MethodPatch, "/esim/profiles/8900000000000000001", strings.NewReader(`{"name":"Test profile","aid_hex":"A0000005591010FFFFFFFF8900000100"}`))
	renameReq.Header.Set("Content-Type", "application/json")
	present.handleESIM(renameOK, renameReq, []string{"profiles", "8900000000000000001"}, "dev1", "dev1", true)
	if renameOK.Code != http.StatusOK {
		t.Fatalf("rename happy-path status = %d, body=%s", renameOK.Code, renameOK.Body.String())
	}
	if data := decodeData(t, renameOK); data["status"] != "renamed" || data["name"] != "Test profile" {
		t.Fatalf("rename data = %v", data)
	}

	// Download on a present device but with no smdp address reports 400.
	dlNoSmdp := httptest.NewRecorder()
	present.handleESIM(dlNoSmdp, httptest.NewRequest(http.MethodGet, "/esim/actions/download", nil), []string{"actions", "download"}, "dev1", "dev1", true)
	if dlNoSmdp.Code != http.StatusBadRequest {
		t.Fatalf("download (no smdp) status = %d, want 400", dlNoSmdp.Code)
	}
}

func TestEsimSwitchStopsVoWiFiAndPersistsDisabledPolicy(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := store.Device{ID: "dev1", Name: "410", VoWiFiEnabled: true}
	if err := database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	controller := &recordingVoWiFiInvalidator{state: vowifi.State{
		DeviceID: "dev1",
		Enabled:  true,
		Active:   true,
		Phase:    vowifi.PhaseAccessReady,
	}, events: &events}
	server := &Server{
		store:               database,
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices:             &recordingESIMDeviceController{events: &events, guard: controller},
		vowifi:              controller,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/esim/actions/switch",
		strings.NewReader(`{"iccid":"8900000000000000001","aid_hex":"A0"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleESIM(response, request, []string{"actions", "switch"}, "dev1", "dev1", true)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if len(controller.enabled) != 1 || controller.enabled[0] {
		t.Fatalf("VoWiFi requests = %v, want one disable", controller.enabled)
	}
	if len(controller.invalidated) != 1 || controller.invalidated[0] != "dev1" {
		t.Fatalf("invalidated runtimes = %v", controller.invalidated)
	}
	if got := strings.Join(events, ","); got != "quiesce,begin,switch,release" {
		t.Fatalf("profile switch order = %q", got)
	}
	stored, err := database.Device(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.VoWiFiEnabled {
		t.Fatal("target card's default VoWiFi policy should remain disabled")
	}
}

func TestEsimSwitchFailureRestoresVoWiFiPolicy(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := store.Device{ID: "dev1", Name: "410", VoWiFiEnabled: true}
	if err := database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	oldICCID := "8900000000000000002"
	if err := database.UpsertCardPolicy(context.Background(), store.CardPolicy{
		ICCID: oldICCID, VoWiFiEnabled: true, AirplaneEnabled: true, Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	entry := device.Device{
		ID:         "dev1",
		Discovered: true,
		Snapshot:   &device.Snapshot{DeviceID: "dev1", ICCID: oldICCID, IMSI: "310260123456789"},
	}
	controller := &recordingVoWiFiInvalidator{state: vowifi.State{
		DeviceID: "dev1", Enabled: true, Active: true, Phase: vowifi.PhaseAccessReady,
	}, events: new([]string)}
	devices := &recordingESIMDeviceController{
		fakeDeviceController: fakeDeviceController{entry: entry},
		guard:                controller,
		switchErr:            errors.New("enable profile rejected"),
	}
	server := &Server{
		store:               database,
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices:             devices,
		vowifi:              controller,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/esim/actions/switch",
		strings.NewReader(`{"iccid":"8900000000000000001"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleESIM(response, request, []string{"actions", "switch"}, "dev1", "dev1", true)
	if response.Code == http.StatusOK {
		t.Fatalf("failed switch unexpectedly succeeded: %s", response.Body.String())
	}
	stored, err := database.Device(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.VoWiFiEnabled {
		t.Fatal("failed switch permanently disabled the old VoWiFi device policy")
	}
	policy, err := database.CardPolicy(context.Background(), oldICCID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.VoWiFiEnabled || !policy.AirplaneEnabled {
		t.Fatalf("old card policy was not restored: %+v", policy)
	}
	if len(controller.enabled) < 2 || controller.enabled[0] || !controller.enabled[len(controller.enabled)-1] {
		t.Fatalf("VoWiFi requests did not stop then restore the session: %v", controller.enabled)
	}
}

func TestEsimSwitchFailsClosedWithoutSubscriberGuardCapability(t *testing.T) {
	events := []string{}
	server := &Server{
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices:             &recordingESIMDeviceController{events: &events},
		vowifi: &fakeVoWiFiController{state: vowifi.State{
			DeviceID: "dev1", Phase: vowifi.PhaseIdle,
		}},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/esim/actions/switch",
		strings.NewReader(`{"iccid":"8900000000000000001"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleESIM(response, request, []string{"actions", "switch"}, "dev1", "dev1", true)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if len(events) != 0 {
		t.Fatalf("physical switch ran without subscriber guard: %v", events)
	}
}

func TestEsimDisableHoldsSubscriberGuardAndClearsWriteDeadline(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "dev1", Name: "410", VoWiFiEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	controller := &recordingVoWiFiInvalidator{state: vowifi.State{
		DeviceID: "dev1", Enabled: true, Active: true, Phase: vowifi.PhaseSMSReady,
	}, events: &events}
	server := &Server{
		store: database, logger: regionTestLogger(), maxRequestBodyBytes: 4096,
		devices: &recordingESIMDeviceController{events: &events, guard: controller}, vowifi: controller,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/esim/actions/disable",
		strings.NewReader(`{"iccid":"8900000000000000001","aid_hex":"A0"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := &recordingDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	server.handleESIM(response, request, []string{"actions", "disable"}, "dev1", "dev1", true)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := strings.Join(events, ","); got != "quiesce,begin,disable,release" {
		t.Fatalf("profile disable order = %q", got)
	}
	if len(response.deadlines) != 1 || !response.deadlines[0].IsZero() {
		t.Fatalf("write deadlines = %#v, want one cleared deadline", response.deadlines)
	}
}

func TestDeleteDeviceKeepsConfigWhenSubscriberGuardFailsAndClearsWriteDeadline(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "dev1", Name: "410", VoWiFiEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	controller := &recordingVoWiFiInvalidator{
		state:         vowifi.State{DeviceID: "dev1", Enabled: true, Active: true, Phase: vowifi.PhaseSMSReady},
		invalidateErr: errors.New("close failed"),
	}
	server := &Server{store: database, logger: regionTestLogger(), vowifi: controller}
	request := httptest.NewRequest(http.MethodDelete, "/api/devices/dev1", nil)
	response := &recordingDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	server.handleDevicePath(response, request, "dev1", nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, err := database.Device(context.Background(), "dev1"); err != nil {
		t.Fatalf("device config was deleted after invalidation failure: %v", err)
	}
	if len(response.deadlines) != 1 || !response.deadlines[0].IsZero() {
		t.Fatalf("write deadlines = %#v, want one cleared deadline", response.deadlines)
	}
}

func TestDeleteDeviceHoldsSubscriberGuardThroughDeletion(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "dev1", Name: "410", VoWiFiEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	controller := &recordingVoWiFiInvalidator{
		state: vowifi.State{
			DeviceID: "dev1", Enabled: true, Active: true, Phase: vowifi.PhaseSMSReady,
		},
		events: &events,
	}
	server := &Server{store: database, logger: regionTestLogger(), vowifi: controller}
	request := httptest.NewRequest(http.MethodDelete, "/api/devices/dev1", nil)
	response := httptest.NewRecorder()
	server.handleDevicePath(response, request, "dev1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := strings.Join(events, ","); got != "quiesce,begin,release" {
		t.Fatalf("device deletion guard order = %q", got)
	}
	if _, err := database.Device(context.Background(), "dev1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Device after DELETE error = %v, want store.ErrNotFound", err)
	}
}

func TestHandleFixUSBNet(t *testing.T) {
	server := &Server{
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices:             fakeDeviceController{usbNetMode: device.USBNetMode{Mode: 0, Name: "QMI"}},
	}
	request := httptest.NewRequest(http.MethodPost, "/fix-usbnet", strings.NewReader(`{"at_port":"/dev/ttyUSB2","mode":0}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleFixUSBNet(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if data := decodeData(t, recorder); data["mode"] != float64(0) || data["name"] != "QMI" {
		t.Fatalf("fix-usbnet data = %v", data)
	}
}

func TestHandleUpdateApplyIsSafeNoop(t *testing.T) {
	server := &Server{logger: regionTestLogger()}
	recorder := httptest.NewRecorder()
	server.handleUpdateApply(recorder, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if data := decodeData(t, recorder); data["applied"] != false {
		t.Fatalf("update apply must be a no-op, got %v", data)
	}
}

func TestHandleUpdateCheckUsesTrustedRepository(t *testing.T) {
	server := &Server{
		logger:           regionTestLogger(),
		updateRepository: update.DefaultRepository,
		updateCheck: func(_ context.Context, repo, token, current string) (update.CheckResult, error) {
			if repo != update.DefaultRepository || token != "token" || current == "" {
				t.Fatalf("check arguments = %q, %q, %q", repo, token, current)
			}
			return update.CheckResult{
				Available:    true,
				Current:      current,
				Latest:       "9.9.9",
				ReleaseNotes: "release notes",
			}, nil
		},
		updateToken: "token",
	}
	recorder := httptest.NewRecorder()
	server.handleUpdateCheck(recorder, httptest.NewRequest(http.MethodGet, "/check", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	data := decodeData(t, recorder)
	if data["available"] != true || data["version"] != "9.9.9" || data["repository"] != update.DefaultRepository {
		t.Fatalf("check data = %#v", data)
	}
}

func TestHandleUpdateApplyInstallsFromTrustedRepository(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.SetAdmin(context.Background(), "admin", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	tokenHash := []byte("active-session")
	if err := database.CreateSession(
		context.Background(), 1, tokenHash, []byte("csrf"), time.Now().Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store:            database,
		logger:           regionTestLogger(),
		updateRepository: update.DefaultRepository,
		updateApply: func(_ context.Context, _ *slog.Logger, options update.Options, restart bool) (update.CheckResult, error) {
			if options.Repo != update.DefaultRepository || restart {
				t.Fatalf("apply options = %#v, restart = %v", options, restart)
			}
			return update.CheckResult{Applied: true, Latest: "9.9.9"}, nil
		},
	}
	recorder := httptest.NewRecorder()
	server.handleUpdateApply(recorder, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	data := decodeData(t, recorder)
	if data["applied"] != true || data["version"] != "9.9.9" || data["reauthentication_required"] != true {
		t.Fatalf("apply data = %#v", data)
	}
	if _, err := database.SessionByTokenHash(context.Background(), tokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session must be revoked after update, got %v", err)
	}
	expired := map[string]bool{}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.MaxAge < 0 {
			expired[cookie.Name] = true
		}
	}
	if !expired[sessionCookieName] || !expired[csrfCookieName] {
		t.Fatalf("auth cookies were not expired: %#v", recorder.Result().Cookies())
	}
}

func TestE911WebsheetProvisioningFailsClosed(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{
		store:               database,
		logger:              regionTestLogger(),
		websheets:           newWebsheetManager(),
		maxRequestBodyBytes: 4096,
	}

	createRec := httptest.NewRecorder()
	server.handleE911Websheet(createRec, httptest.NewRequest(http.MethodPost, "/e911", nil), store.Device{ID: "dev1"})
	if createRec.Code != http.StatusNotImplemented {
		t.Fatalf("create status = %d, want 501; body=%s", createRec.Code, createRec.Body.String())
	}
	var response errorEnvelope
	if err := json.NewDecoder(createRec.Body).Decode(&response); err != nil {
		t.Fatalf("decode unsupported response: %v", err)
	}
	if response.Error.Code != "e911_provisioning_unavailable" {
		t.Fatalf("error code = %q", response.Error.Code)
	}
	if _, err := database.AppSetting(context.Background(), "e911_address:dev1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsupported provisioning must not persist an address, got %v", err)
	}
}

func TestLegacyE911WebsheetPathsFailClosed(t *testing.T) {
	server := &Server{logger: regionTestLogger(), websheets: newWebsheetManager()}
	recorder := httptest.NewRecorder()
	server.handleWebsheet(recorder, httptest.NewRequest(http.MethodGet, "/websheets/legacy?token=old", nil))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("legacy websheet status = %d, want 501", recorder.Code)
	}
}

// readSSEEvent reads one Server-Sent-Events frame ("event:"/"data:" lines
// terminated by a blank line) and returns the event name and data payload.
func readSSEEvent(reader *bufio.Reader) (string, []byte, error) {
	var event string
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if event != "" || data != nil {
				return event, data, nil
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			event = rest
		} else if rest, ok := strings.CutPrefix(line, "data: "); ok {
			data = append(data, rest...)
		}
	}
}

// awaitOverviewNetworkEnabled reads overview SSE events until one reports the
// requested network_enabled value, or the stream ends / the request times out.
func awaitOverviewNetworkEnabled(reader *bufio.Reader, want bool) error {
	for {
		event, data, err := readSSEEvent(reader)
		if err != nil {
			return err
		}
		if event != "overview" {
			continue
		}
		var overview struct {
			NetworkEnabled bool `json:"network_enabled"`
		}
		if err := json.Unmarshal(data, &overview); err != nil {
			return err
		}
		if overview.NetworkEnabled == want {
			return nil
		}
	}
}

// The overview SSE stream must reflect edits made after it opened. Before the
// fix it rebuilt every tick from the config snapshot captured when the stream
// opened, so toggling roaming data off was immediately overwritten by the stale
// "on" snapshot and the switch flapped. This test opens the stream with roaming
// data on, turns it off in the store, and requires the stream to keep reporting
// the new "off" state.
func TestHandleOverviewStreamReflectsConfigChanges(t *testing.T) {
	previousInterval := overviewStreamInterval
	overviewStreamInterval = 10 * time.Millisecond
	t.Cleanup(func() { overviewStreamInterval = previousInterval })

	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertAppSetting(ctx, store.AppSetting{
		Key:   developer.EnabledSettingKey,
		Value: []byte(`{"enabled":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(ctx, store.Device{ID: "dev1", Name: "Test device", NetworkEnabled: true}); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: database, logger: regionTestLogger(), developerEnabled: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		config, err := database.Device(r.Context(), "dev1")
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		server.handleOverviewStream(w, r, config, device.Device{}, false)
	})
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)

	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, testServer.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)

	// The stream opens with roaming data enabled.
	if err := awaitOverviewNetworkEnabled(reader, true); err != nil {
		t.Fatalf("initial overview never reported network_enabled=true: %v", err)
	}

	// Turn roaming data off; the very next ticks must report the new state
	// instead of replaying the stale enabled snapshot.
	config, err := database.Device(ctx, "dev1")
	if err != nil {
		t.Fatal(err)
	}
	config.NetworkEnabled = false
	if err := database.UpsertDevice(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err := awaitOverviewNetworkEnabled(reader, false); err != nil {
		t.Fatalf("overview kept replaying stale network_enabled=true after the edit: %v", err)
	}
}

// Turning roaming data off must be refused while an enabled export proxy is
// bound to the device; the user has to disable that binding first.
func TestHandleCellularDataRejectsDisableWhileExportProxyActive(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertAppSetting(ctx, store.AppSetting{
		Key: developer.EnabledSettingKey, Value: json.RawMessage(`{"enabled":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	deviceConfig := store.Device{ID: "modem-1", Name: "modem-1", Interface: "wwan0", NetworkEnabled: true}
	if err := database.UpsertDevice(ctx, deviceConfig); err != nil {
		t.Fatal(err)
	}
	// Seed an already-enabled export proxy bound to the device. New only logs a
	// warning when the Linux-only listener cannot start on this platform, so the
	// enabled config still loads and the interlock sees it.
	seeded, err := json.Marshal([]exportproxy.Config{{
		ID: "proxy-1", Name: "proxy-1", DeviceID: "modem-1", Interface: "wwan0",
		Mode: "socks5", ListenHost: "127.0.0.1", ListenPort: 1080, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: exportproxy.SettingKey, Value: seeded, Sensitive: true}); err != nil {
		t.Fatal(err)
	}
	proxyManager, err := exportproxy.New(ctx, database, regionTestLogger(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxyManager.Close() })
	server := &Server{
		store:               database,
		logger:              regionTestLogger(),
		developerEnabled:    true,
		exportProxy:         proxyManager,
		devices:             fakeDeviceController{},
		maxRequestBodyBytes: 1 << 20,
	}

	patchOff := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPatch, "/api/devices/modem-1/cellular-data", strings.NewReader(`{"enabled":false}`))
		request.Header.Set("Content-Type", "application/json")
		if !server.handleCellularData(recorder, request, deviceConfig, "physical-1") {
			t.Fatal("handleCellularData did not handle the request")
		}
		return recorder
	}

	// While the export proxy is enabled, turning roaming data off is rejected and
	// the stored config keeps roaming data on.
	recorder := patchOff()
	if recorder.Code != http.StatusConflict {
		t.Fatalf("disable with active proxy status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "export_proxy_active" {
		t.Fatalf("error code = %q, body = %s", failure.Error.Code, recorder.Body)
	}
	stored, err := database.Device(ctx, "modem-1")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.NetworkEnabled {
		t.Fatal("roaming data was turned off despite the active export proxy")
	}

	// Once the binding is disabled, the same request goes through.
	proxies, err := proxyManager.Configs()
	if err != nil || len(proxies) != 1 {
		t.Fatalf("configs = %+v, %v", proxies, err)
	}
	disabled := proxies[0]
	disabled.Enabled = false
	if _, err := proxyManager.Update(ctx, disabled.ID, disabled); err != nil {
		t.Fatal(err)
	}
	recorder = patchOff()
	if recorder.Code != http.StatusOK {
		t.Fatalf("disable after proxy off status = %d, body = %s", recorder.Code, recorder.Body)
	}
	stored, err = database.Device(ctx, "modem-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.NetworkEnabled {
		t.Fatal("roaming data was not turned off after the export proxy was disabled")
	}
}
