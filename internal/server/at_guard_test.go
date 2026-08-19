package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
)

func TestValidateATCommandBlocksTrafficMessagingAndDialActions(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"AT+CGATT=1",
		"AT+CGACT=1,1",
		"AT+CGDATA=\"PPP\",1",
		"AT+QNETDEVCTL=1,1,1",
		"AT+QIACT=1",
		"AT+CMGS=42",
		"AT+CMSS=7",
		"AT+CMGC=12",
		"AT+QCMGS=42",
		"AT+CUSD=1,\"*100#\"",
		"ATD12345;",
		"ATA",
		"ATH",
		"AT+CSQ; +CGACT = 1,1",
		"AT+CSQ;+CMSS=7",
		"AT+CSQ;D12345;",
	} {
		if err := validateATCommand(command, false); err == nil {
			t.Errorf("validateATCommand(%q) permitted a guarded mutation", command)
		}
	}
}

func TestValidateATCommandForceBypassesGuard(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"AT+CGATT=1",
		"AT+CFUN=1",
		"AT+CGACT=1,1",
		"AT+CUSD=1,\"*100#\"",
		"ATD12345;",
	} {
		if err := validateATCommand(command, true); err != nil {
			t.Errorf("validateATCommand(%q, true): %v", command, err)
		}
	}
}

func TestValidateATCommandForceKeepsSyntaxChecks(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"A",
		"",
		"AT\r",
		"AT\n",
		string(make([]byte, 513)),
	} {
		if err := validateATCommand(command, true); err == nil {
			t.Errorf("validateATCommand(%q, true) skipped syntax check", command)
		}
	}
}

func TestValidateATCommandAllowsReadOnlyStatusQueries(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"AT",
		"AT+CPIN?",
		"AT+CGATT?",
		"AT+CGACT?",
		"AT+CFUN?",
		"AT+CIMI",
		"AT+CCID",
	} {
		if err := validateATCommand(command, false); err != nil {
			t.Errorf("validateATCommand(%q): %v", command, err)
		}
	}
}

// The AT terminal must present ERROR / +CME ERROR as a normal response, not as
// a 502. Before the CommandError branch was restored, every unsupported or
// SIM-less command was folded into "the device operation failed", hiding the
// real reason from the user.
func TestHandleATSurfacesCommandErrorAsResponse(t *testing.T) {
	controller := fakeDeviceController{
		entry: device.Device{ID: "dev1"},
		atHandler: func(command string) (modem.Response, error) {
			return modem.Response{}, &modem.CommandError{
				Command: command,
				Final:   "+CME ERROR: 10",
				Lines:   []string{"+CME ERROR: 10"},
			}
		},
	}
	server := &Server{devices: controller, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/devices/dev1/actions/at",
		strings.NewReader(`{"cmd":"AT+CPIN?","timeout_ms":5000}`),
	)
	request.Header.Set("Content-Type", "application/json")

	if !server.handleAT(recorder, request, "dev1") {
		t.Fatal("handleAT returned false")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	data := decodeData(t, recorder)
	response, _ := data["response"].(string)
	if !strings.Contains(response, "+CME ERROR: 10") {
		t.Fatalf("response = %q, want +CME ERROR text", response)
	}
}

func TestHandleATMapsNonCommandErrorTo502(t *testing.T) {
	controller := fakeDeviceController{
		entry: device.Device{ID: "dev1"},
		atErr: errors.New("transport wedged"),
	}
	server := &Server{devices: controller, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/devices/dev1/actions/at",
		strings.NewReader(`{"cmd":"AT+CSQ"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	if !server.handleAT(recorder, request, "dev1") {
		t.Fatal("handleAT returned false")
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
}
