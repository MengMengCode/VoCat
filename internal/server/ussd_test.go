package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type recordingUSSDDevice struct {
	fakeDeviceController
	starts, continues, cancels   int
	physicalID, sessionID, input string
}

func (d *recordingUSSDDevice) USSD(ctx context.Context, id, code string) (device.USSDResult, error) {
	d.starts++
	d.physicalID, d.input = id, code
	return d.fakeDeviceController.USSD(ctx, id, code)
}

func (d *recordingUSSDDevice) ContinueUSSD(ctx context.Context, sessionID, input string) (device.USSDResult, error) {
	d.continues++
	d.sessionID, d.input = sessionID, input
	return d.fakeDeviceController.ContinueUSSD(ctx, sessionID, input)
}

func (d *recordingUSSDDevice) CancelUSSD(ctx context.Context, sessionID string) error {
	d.cancels++
	d.sessionID = sessionID
	return d.fakeDeviceController.CancelUSSD(ctx, sessionID)
}

func newUSSDRoutingServer(t *testing.T, enabled bool) (*Server, *recordingUSSDDevice, *fakeUSSIController) {
	t.Helper()
	db, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	config := store.Device{ID: "saved-1-6", Name: "EC20", ModemIMEI: "860000000000001", VoWiFiEnabled: enabled}
	if err := db.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	d := &recordingUSSDDevice{fakeDeviceController: fakeDeviceController{
		entry:      device.Device{ID: "physical-1-6-1", Discovered: true, Snapshot: &device.Snapshot{IMEI: config.ModemIMEI}},
		ussdResult: device.USSDResult{Status: "awaiting_input", Text: "cellular menu", SessionID: "cellular-token", Continueable: true},
	}}
	ims := &fakeUSSIController{
		fakeVoWiFiController: fakeVoWiFiController{state: vowifi.State{IMSReady: true}},
		expectedDeviceID:     config.ID,
		sendResult:           vowifi.USSISubmitResult{Status: "awaiting_input", Text: "IMS menu", SessionID: "dialog-1", Continueable: true},
	}
	return &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096, store: db, devices: d, vowifi: ims}, d, ims
}

func ussdHTTP(t *testing.T, s *Server, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleDevicePath(rec, httptest.NewRequest(http.MethodPost, "/devices/saved-1-6/actions/ussd", strings.NewReader(body)),
		"saved-1-6", strings.Split("actions/ussd"+action, "/"))
	return rec
}

func TestUSSDWebUsesConfiguredIMSDeviceAndExactDialog(t *testing.T) {
	s, cellular, ims := newUSSDRoutingServer(t, true)
	start := ussdHTTP(t, s, "", `{"command":" #999# "}`)
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	token, _ := decodeData(t, start)["session_id"].(string)
	if token == "" || ims.stateDeviceID != "saved-1-6" || ims.lastDeviceID != "saved-1-6" || ims.lastRequest.Code != "#999#" {
		t.Fatalf("incorrect IMS route: token=%q controller=%+v", token, ims)
	}
	continued := ussdHTTP(t, s, "/continue", `{"sessionId":"`+token+`","input":"1"}`)
	if continued.Code != http.StatusOK || decodeData(t, continued)["session_id"] != token || ims.lastRequest.SessionID != "dialog-1" || ims.lastRequest.Input != "1" || ims.lastRequest.Code != "" {
		t.Fatalf("continue lost binding: %d %s request=%+v", continued.Code, continued.Body.String(), ims.lastRequest)
	}
	ims.sendResult = vowifi.USSISubmitResult{Status: "final", Text: "balance", SessionID: "dialog-1"}
	final := ussdHTTP(t, s, "/continue", `{"session_id":"`+token+`","input":"2"}`)
	if final.Code != http.StatusOK || decodeData(t, final)["session_id"] != "" {
		t.Fatalf("final: %s", final.Body.String())
	}
	if _, err := s.ussdSessionDevice(token); !errors.Is(err, device.ErrUSSDSessionNotFound) {
		t.Fatalf("final token retained: %v", err)
	}
	for _, action := range []string{"/continue", "/cancel"} {
		body := `{"session_id":"` + token + `"}`
		if action == "/continue" {
			body = `{"session_id":"` + token + `","input":"3"}`
		}
		rec := ussdHTTP(t, s, action, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("stale %s: %d %s", action, rec.Code, rec.Body.String())
		}
	}
	if cellular.starts+cellular.continues+cellular.cancels != 0 {
		t.Fatalf("IMS operation reached cellular: %+v", cellular)
	}
}

func TestUSSDWebAndTelegramDoNotFallBackFromVoWiFi(t *testing.T) {
	for _, entry := range []string{"web", "telegram"} {
		for _, failure := range []string{"missing runtime", "missing capability", "state error", "not registered", "send not ready", "send timeout", "send rejected"} {
			t.Run(entry+"/"+failure, func(t *testing.T) {
				s, cellular, ims := newUSSDRoutingServer(t, true)
				switch failure {
				case "missing runtime":
					s.vowifi = nil
				case "missing capability":
					s.vowifi = &fakeVoWiFiController{}
				case "state error":
					ims.err = errors.New("config lookup failed")
				case "not registered":
					ims.state = vowifi.State{Phase: vowifi.PhaseTunnelReady}
				case "send not ready":
					ims.sendErr = vowifi.ErrUSSINotReady
				case "send timeout":
					ims.sendErr = context.DeadlineExceeded
				case "send rejected":
					ims.sendErr = errors.New("SIP 403")
				}
				if entry == "web" {
					rec := ussdHTTP(t, s, "", `{"command":"#999#"}`)
					if rec.Code == http.StatusOK {
						t.Fatalf("unexpected success: %s", rec.Body.String())
					}
					if !strings.HasPrefix(failure, "send ") || failure == "send not ready" {
						if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ussi_not_ready") {
							t.Fatalf("missing readiness error: %d %s", rec.Code, rec.Body.String())
						}
					}
				} else {
					if _, err := (&telegramBot{server: s}).executeUSSDCommand(context.Background(), "saved-1-6", "#999#"); err == nil {
						t.Fatal("unexpected Telegram success")
					}
				}
				if cellular.starts != 0 || ims.sendCalled > 1 {
					t.Fatalf("retried or switched transport: cellular=%d IMS=%d", cellular.starts, ims.sendCalled)
				}
			})
		}
	}
}

func TestUSSDCellularRouteRetainsPhysicalIDAndSession(t *testing.T) {
	for _, entry := range []string{"web", "telegram"} {
		t.Run(entry, func(t *testing.T) {
			s, cellular, ims := newUSSDRoutingServer(t, false)
			if entry == "web" {
				rec := ussdHTTP(t, s, "", `{"command":"#999#"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("start: %s", rec.Body.String())
				}
			} else if _, err := (&telegramBot{server: s}).executeUSSDCommand(context.Background(), "saved-1-6", "#999#"); err != nil {
				t.Fatal(err)
			}
			if cellular.physicalID != "physical-1-6-1" || cellular.starts != 1 || ims.sendCalled != 0 {
				t.Fatalf("incorrect route: cellular=%+v IMS=%+v", cellular, ims)
			}
			if _, err := s.continueUSSD(context.Background(), "cellular-token", "1"); err != nil {
				t.Fatal(err)
			}
			if err := s.cancelUSSD(context.Background(), "cellular-token"); err != nil {
				t.Fatal(err)
			}
			if cellular.sessionID != "cellular-token" || cellular.continues != 1 || cellular.cancels != 1 || ims.cancelCalled != 0 {
				t.Fatalf("incorrect session route: %+v", cellular)
			}
		})
	}
}

func TestUSSDCancelFailureKeepsExactIMSBindingForRetry(t *testing.T) {
	s, cellular, ims := newUSSDRoutingServer(t, true)
	token := s.openUSSDSessionWithNetwork("saved-1-6", "dialog-1")
	ims.cancelErr = errors.New("BYE rejected")
	rec := ussdHTTP(t, s, "/cancel", `{"session_id":"`+token+`"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("false cancellation success: %s", rec.Body.String())
	}
	if _, err := s.ussdSessionDevice(token); err != nil {
		t.Fatalf("cancel failure discarded token: %v", err)
	}
	if ims.cancelDeviceID != "saved-1-6" || ims.cancelSessionID != "dialog-1" {
		t.Fatalf("incorrect cancel target: %+v", ims)
	}
	ims.cancelErr = nil
	rec = ussdHTTP(t, s, "/cancel", `{"session_id":"`+token+`"}`)
	if rec.Code != http.StatusOK || ims.cancelCalled != 2 {
		t.Fatalf("cancel retry: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := s.ussdSessionDevice(token); !errors.Is(err, device.ErrUSSDSessionNotFound) {
		t.Fatalf("successful cancel retained token: %v", err)
	}
	if cellular.cancels != 0 {
		t.Fatal("IMS cancel reached cellular")
	}
}

func TestUSSIReplyFailureNeverChangesTransportOrDialog(t *testing.T) {
	for _, failure := range []string{"registration lost", "send not ready", "timeout", "wrong dialog"} {
		t.Run(failure, func(t *testing.T) {
			s, cellular, ims := newUSSDRoutingServer(t, true)
			token := s.openUSSDSessionWithNetwork("saved-1-6", "dialog-1")
			switch failure {
			case "registration lost":
				ims.state.IMSReady = false
			case "send not ready":
				ims.sendErr = vowifi.ErrUSSINotReady
			case "timeout":
				ims.sendErr = context.DeadlineExceeded
			case "wrong dialog":
				ims.sendResult.SessionID = "dialog-2"
			}
			if _, err := s.continueUSSD(context.Background(), token, "1"); err == nil {
				t.Fatal("unexpected success")
			}
			if s.ussdNetworkSession(token) != "dialog-1" || cellular.continues != 0 || ims.sendCalled > 1 {
				t.Fatalf("lost binding: cellular=%+v IMS=%+v", cellular, ims)
			}
		})
	}
}

func TestUSSIRejectsInteractiveResultWithoutNetworkDialog(t *testing.T) {
	s, cellular, ims := newUSSDRoutingServer(t, true)
	ims.sendResult.SessionID = ""
	if _, err := (&telegramBot{server: s}).executeUSSDCommand(context.Background(), "saved-1-6", "#999#"); err == nil {
		t.Fatal("opened a token without a network dialog")
	}
	if cellular.starts != 0 {
		t.Fatal("invalid IMS result retried on cellular")
	}
}

func TestUSSDRejectsInvalidCodeBeforeEitherTransport(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, code := range []string{"", "AT+CUSD=2", "#999#\r\nInjected: value", strings.Repeat("1", 41)} {
			s, cellular, ims := newUSSDRoutingServer(t, enabled)
			config, err := s.store.Device(context.Background(), "saved-1-6")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.startUSSD(context.Background(), config, "physical-1-6-1", code); !errors.Is(err, errInvalidUSSDRequest) {
				t.Fatalf("code %q: %v", code, err)
			}
			if cellular.starts != 0 || ims.sendCalled != 0 {
				t.Fatalf("invalid code was submitted: %q", code)
			}
		}
	}
}

func TestUSSDLocalInformationUsesConfiguredRuntime(t *testing.T) {
	s, cellular, ims := newUSSDRoutingServer(t, true)
	if err := s.store.UpsertDeviceRuntime(context.Background(), store.DeviceRuntime{DeviceID: "saved-1-6", IMEI: "860000000000001", Firmware: "test-firmware"}); err != nil {
		t.Fatal(err)
	}
	config, err := s.store.Device(context.Background(), "saved-1-6")
	if err != nil {
		t.Fatal(err)
	}
	config.ModemIMEI = ""
	for code, want := range map[string]string{"*#06#": "IMEI: 860000000000001", "*#0000#": "Software Version: test-firmware"} {
		result, err := s.startUSSD(context.Background(), config, "physical-1-6-1", code)
		if err != nil || result.Status != "final" || result.Text != want {
			t.Fatalf("local code %s: %+v %v", code, result, err)
		}
	}
	if cellular.starts != 0 || ims.sendCalled != 0 {
		t.Fatal("local information sent a network request")
	}
}

func TestUSSICancelUnavailablePreservesToken(t *testing.T) {
	for _, failure := range []string{"runtime missing", "unsupported", "not ready"} {
		t.Run(failure, func(t *testing.T) {
			s, cellular, ims := newUSSDRoutingServer(t, true)
			token := s.openUSSDSessionWithNetwork("saved-1-6", "dialog-1")
			switch failure {
			case "runtime missing":
				s.vowifi = nil
			case "unsupported":
				s.vowifi = &fakeVoWiFiController{}
			case "not ready":
				ims.cancelErr = vowifi.ErrUSSINotReady
			}
			rec := ussdHTTP(t, s, "/cancel", `{"session_id":"`+token+`"}`)
			if rec.Code != http.StatusServiceUnavailable || s.ussdNetworkSession(token) != "dialog-1" || cellular.cancels != 0 {
				t.Fatalf("cancel lost state: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUSSIConfirmedMissingDialogReleasesToken(t *testing.T) {
	for _, action := range []string{"continue", "cancel"} {
		t.Run(action, func(t *testing.T) {
			s, cellular, ims := newUSSDRoutingServer(t, true)
			token := s.openUSSDSessionWithNetwork("saved-1-6", "dialog-1")
			ims.sendErr, ims.cancelErr = vowifi.ErrUSSISessionNotFound, vowifi.ErrUSSISessionNotFound
			var err error
			if action == "continue" {
				_, err = s.continueUSSD(context.Background(), token, "1")
			} else {
				err = s.cancelUSSD(context.Background(), token)
			}
			if !errors.Is(err, vowifi.ErrUSSISessionNotFound) {
				t.Fatalf("error = %v", err)
			}
			if _, err := s.ussdSessionDevice(token); !errors.Is(err, device.ErrUSSDSessionNotFound) {
				t.Fatalf("confirmed closed token retained: %v", err)
			}
			if cellular.continues+cellular.cancels != 0 {
				t.Fatal("closed IMS session switched to cellular")
			}
		})
	}
}

func TestUSSIResultTerminalStatusCannotOpenAnotherSession(t *testing.T) {
	for _, status := range []string{"final", "failed", "terminated"} {
		result := ussdResultFromUSSI(vowifi.USSISubmitResult{Status: status, Continueable: true})
		if result.Status != status || result.Continueable || result.SessionID != "" {
			t.Fatalf("terminal result became interactive: %+v", result)
		}
	}
}
