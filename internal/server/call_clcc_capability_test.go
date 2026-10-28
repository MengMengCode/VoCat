package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
)

// clccCapabilityController adds the optional capability probe to the shared
// transcript controller, mirroring device.Manager.
type clccCapabilityController struct {
	fakeDeviceController
	supportsCLCC bool
}

func (controller clccCapabilityController) SupportsCLCC(context.Context, string) (bool, error) {
	return controller.supportsCLCC, nil
}

// The ZXIC platform firmware rejects AT+CLCC with vendor +CME ERROR: 6003.
// The call list endpoint must answer with an explicit capability error
// instead of spending a control-channel round trip on a command the dialect
// declares unsupported.
func TestHandleCallsReportsUnsupportedCLCCWithoutIssuingAT(t *testing.T) {
	issued := []string{}
	controller := clccCapabilityController{
		supportsCLCC: false,
		fakeDeviceController: fakeDeviceController{
			entry: device.Device{
				ID:        "physical-1",
				Candidate: modem.Candidate{VendorID: "19d2", ProductID: "0536"},
			},
			atHandler: func(command string) (modem.Response, error) {
				issued = append(issued, command)
				return modem.Response{Final: "OK"}, nil
			},
		},
	}
	server := &Server{devices: controller}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/devices/device-1/calls", nil)

	if !server.handleCalls(recorder, request, store.Device{ID: "device-1"}, "physical-1") {
		t.Fatal("handleCalls did not handle the request")
	}
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotImplemented)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error.Code != "clcc_unsupported" {
		t.Fatalf("error code = %q, want clcc_unsupported", envelope.Error.Code)
	}
	if len(issued) != 0 {
		t.Fatalf("issued AT commands = %v, want none for firmware without AT+CLCC", issued)
	}
}

// Firmware that implements AT+CLCC keeps the historical call-list flow.
func TestHandleCallsUsesCLCCForCapableFirmware(t *testing.T) {
	controller := clccCapabilityController{
		supportsCLCC: true,
		fakeDeviceController: fakeDeviceController{
			entry: device.Device{ID: "physical-1"},
			atHandler: func(command string) (modem.Response, error) {
				if command != "AT+CLCC" {
					t.Fatalf("command = %q, want AT+CLCC", command)
				}
				return modem.Response{
					Lines: []string{`+CLCC: 1,1,4,0,0,"+447700900000",145`},
					Final: "OK",
				}, nil
			},
		},
	}
	server := &Server{devices: controller}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/devices/device-1/calls", nil)

	if !server.handleCalls(recorder, request, store.Device{ID: "device-1"}, "physical-1") {
		t.Fatal("handleCalls did not handle the request")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}

// Controllers that cannot report the capability (test fakes, SIM readers)
// keep the historical polling behavior.
func TestSupportsCLCCDefaultsToPollingWithoutCapability(t *testing.T) {
	server := &Server{devices: fakeDeviceController{}}
	if !server.supportsCLCC(context.Background(), "unknown") {
		t.Fatal("supportsCLCC without a capability provider = false, want true")
	}
	supported, err := (clccCapabilityController{supportsCLCC: true}).
		SupportsCLCC(context.Background(), "x")
	if err != nil || !supported {
		t.Fatalf("capability provider lost its answer: supported=%v err=%v", supported, err)
	}
}

// A capability provider that cannot answer in time must not block the
// caller: the server keeps the historical behavior and lets the modem answer.
func TestSupportsCLCCFallsBackWhenTheProviderFails(t *testing.T) {
	server := &Server{devices: failingCLCCCapabilityController{}}
	if !server.supportsCLCC(context.Background(), "unknown") {
		t.Fatal("supportsCLCC = false, want true when the provider fails")
	}
}

type failingCLCCCapabilityController struct {
	fakeDeviceController
}

func (failingCLCCCapabilityController) SupportsCLCC(context.Context, string) (bool, error) {
	return false, context.DeadlineExceeded
}
