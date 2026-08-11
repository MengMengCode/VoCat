package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func TestParseCLCC(t *testing.T) {
	calls := parseCLCC(modem.Response{Lines: []string{
		`+CLCC: 1,1,4,0,0,"+447700900000",145`,
		`+CLCC: 2,0,0,0,0,"12345",129`,
	}})
	if len(calls) != 2 || calls[0]["number"] != "+447700900000" || calls[1]["state"] != 0 {
		t.Fatalf("parseCLCC = %#v", calls)
	}
}

func TestValidDialNumber(t *testing.T) {
	for _, value := range []string{"+447700900000", "12345", "*100#"} {
		if !validDialNumber(value) {
			t.Errorf("validDialNumber(%q) = false", value)
		}
	}
	for _, value := range []string{"", "+", "12;ATH", "12 34", "abc"} {
		if validDialNumber(value) {
			t.Errorf("validDialNumber(%q) = true", value)
		}
	}
}

func TestEmergencyDialNumber(t *testing.T) {
	for _, value := range []string{"911", "112", "+112", "*31#112", "#31#911", "999", "995", "000", "110", "119", "120"} {
		if !isEmergencyDialNumber(value) {
			t.Errorf("isEmergencyDialNumber(%q) = false", value)
		}
	}
	for _, value := range []string{"+447700900000", "12345", "9110", "*100#"} {
		if isEmergencyDialNumber(value) {
			t.Errorf("isEmergencyDialNumber(%q) = true", value)
		}
	}
}

func TestHandleCallActionRejectsEmergencyDialBeforeCellularTransport(t *testing.T) {
	atCalls := 0
	server := &Server{
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices: fakeDeviceController{atHandler: func(command string) (modem.Response, error) {
			atCalls++
			return modem.Response{Final: "OK"}, nil
		}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/calls/dial", strings.NewReader(`{"number":"911"}`))
	server.handleCallAction(recorder, request, store.Device{ID: "dev1"}, "physical1", "dial")
	assertEmergencyDialRejected(t, recorder)
	if atCalls != 0 {
		t.Fatalf("emergency number reached cellular AT transport %d time(s)", atCalls)
	}
}

func TestHandleCallActionRejectsEmergencyDialBeforeVoWiFiTransport(t *testing.T) {
	controller := &recordingVoWiFiCallController{state: vowifi.State{IMSReady: true}}
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096, vowifi: controller}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/calls/dial", strings.NewReader(`{"number":"112"}`))
	server.handleCallAction(recorder, request, store.Device{ID: "dev1"}, "physical1", "dial")
	assertEmergencyDialRejected(t, recorder)
	if len(controller.dialed) != 0 {
		t.Fatalf("emergency number reached VoWiFi DialCall: %v", controller.dialed)
	}
}

func TestHandleCallActionPreservesOrdinaryCellularDial(t *testing.T) {
	commands := make([]string, 0, 1)
	server := &Server{
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices: fakeDeviceController{atHandler: func(command string) (modem.Response, error) {
			commands = append(commands, command)
			return modem.Response{Final: "OK"}, nil
		}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/calls/dial", strings.NewReader(`{"number":"+447700900000"}`))
	server.handleCallAction(recorder, request, store.Device{ID: "dev1"}, "physical1", "dial")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("ordinary dial status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(commands) != 1 || commands[0] != "ATD+447700900000;" {
		t.Fatalf("ordinary cellular commands = %v", commands)
	}
}

func TestHandleCallActionPreservesOrdinaryVoWiFiDial(t *testing.T) {
	controller := &recordingVoWiFiCallController{state: vowifi.State{IMSReady: true}}
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096, vowifi: controller}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/calls/dial", strings.NewReader(`{"number":"+447700900000"}`))
	server.handleCallAction(recorder, request, store.Device{ID: "dev1"}, "physical1", "dial")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("ordinary VoWiFi dial status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(controller.dialed) != 1 || controller.dialed[0] != "+447700900000" {
		t.Fatalf("ordinary VoWiFi dials = %v", controller.dialed)
	}
}

func TestHandleCallActionSendsNegotiatedVoWiFiDTMF(t *testing.T) {
	controller := &recordingVoWiFiCallController{state: vowifi.State{IMSReady: true}}
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096, vowifi: controller}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/calls/dtmf",
		strings.NewReader(`{"call_id":"call1","digit":"b","duration_ms":160}`),
	)
	server.handleCallAction(recorder, request, store.Device{ID: "dev1"}, "physical1", "dtmf")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("VoWiFi DTMF status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(controller.dtmf) != 1 || controller.dtmf[0].callID != "call1" ||
		controller.dtmf[0].digit != 'B' || controller.dtmf[0].duration != 160*time.Millisecond {
		t.Fatalf("VoWiFi DTMF calls = %#v", controller.dtmf)
	}
}

func TestHandleCallActionRejectsInvalidDTMFBeforeTransport(t *testing.T) {
	controller := &recordingVoWiFiCallController{state: vowifi.State{IMSReady: true}}
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096, vowifi: controller}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/calls/dtmf", strings.NewReader(`{"call_id":"call1","digit":"X"}`))
	server.handleCallAction(recorder, request, store.Device{ID: "dev1"}, "physical1", "dtmf")
	if recorder.Code != http.StatusBadRequest || len(controller.dtmf) != 0 {
		t.Fatalf("invalid DTMF status=%d calls=%#v body=%s", recorder.Code, controller.dtmf, recorder.Body.String())
	}
}

func assertEmergencyDialRejected(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("emergency dial status = %d, want 501; body=%s", recorder.Code, recorder.Body.String())
	}
	var response errorEnvelope
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode emergency response: %v", err)
	}
	if response.Error.Code != "emergency_call_unsupported" {
		t.Fatalf("emergency error code = %q", response.Error.Code)
	}
}

func TestCallTransportRequiresIMSReady(t *testing.T) {
	controller := &fakeVoWiFiController{state: vowifi.State{Enabled: true}}
	server := &Server{vowifi: controller}
	if got := server.callTransport("ec20"); got != "cellular" {
		t.Fatalf("callTransport before IMS registration = %q, want cellular", got)
	}
	controller.state.IMSReady = true
	if got := server.callTransport("ec20"); got != "vowifi" {
		t.Fatalf("callTransport with IMS ready = %q, want vowifi", got)
	}
}

func TestResolveVoWiFiCallIDIgnoresTerminalCalls(t *testing.T) {
	controller := &fakeCallController{calls: []vowifi.Call{
		{ID: "failed", State: "failed"},
		{ID: "active", State: "active"},
	}}
	got, err := resolveVoWiFiCallID(controller, "ec20", "", "")
	if err != nil || got != "active" {
		t.Fatalf("resolveVoWiFiCallID() = %q, %v; want active", got, err)
	}
}

type fakeCallController struct {
	calls []vowifi.Call
}

func (controller *fakeCallController) Calls(string) ([]vowifi.Call, error) {
	return controller.calls, nil
}

func (*fakeCallController) DialCall(context.Context, string, string) (vowifi.Call, error) {
	return vowifi.Call{}, nil
}

func (*fakeCallController) AnswerCall(context.Context, string, string) (vowifi.Call, error) {
	return vowifi.Call{}, nil
}

func (*fakeCallController) HangupCall(context.Context, string, string) error { return nil }

type recordingVoWiFiCallController struct {
	state  vowifi.State
	dialed []string
	dtmf   []recordedDTMF
}

type recordedDTMF struct {
	callID   string
	digit    byte
	duration time.Duration
}

func (controller *recordingVoWiFiCallController) State(string) (vowifi.State, error) {
	return controller.state, nil
}

func (controller *recordingVoWiFiCallController) RequestEnabled(string, bool) (vowifi.State, error) {
	return controller.state, nil
}

func (controller *recordingVoWiFiCallController) RequestReconnect(string) (vowifi.State, error) {
	return controller.state, nil
}

func (*recordingVoWiFiCallController) Calls(string) ([]vowifi.Call, error) { return nil, nil }

func (controller *recordingVoWiFiCallController) DialCall(_ context.Context, _ string, number string) (vowifi.Call, error) {
	controller.dialed = append(controller.dialed, number)
	return vowifi.Call{ID: "call1"}, nil
}

func (*recordingVoWiFiCallController) AnswerCall(context.Context, string, string) (vowifi.Call, error) {
	return vowifi.Call{}, nil
}

func (*recordingVoWiFiCallController) HangupCall(context.Context, string, string) error { return nil }

func (controller *recordingVoWiFiCallController) SendDTMF(
	_ context.Context,
	_ string,
	callID string,
	digit byte,
	duration time.Duration,
) error {
	controller.dtmf = append(controller.dtmf, recordedDTMF{callID: callID, digit: digit, duration: duration})
	return nil
}
