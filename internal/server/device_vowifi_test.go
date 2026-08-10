package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/store"
	"vocat/internal/vowifi"
	vowifiruntime "vocat/internal/vowifi/runtime"
)

type fakeVoWiFiController struct {
	state      vowifi.State
	enabled    []bool
	reconnects int
	err        error
}

func (controller *fakeVoWiFiController) State(string) (vowifi.State, error) {
	return controller.state, controller.err
}

func (controller *fakeVoWiFiController) RequestEnabled(
	_ string,
	enabled bool,
) (vowifi.State, error) {
	controller.enabled = append(controller.enabled, enabled)
	if !enabled && controller.err == nil {
		controller.state.Enabled = false
		controller.state.Active = false
		controller.state.Phase = vowifi.PhaseIdle
	}
	return controller.state, controller.err
}

func (controller *fakeVoWiFiController) RequestReconnect(string) (vowifi.State, error) {
	controller.reconnects++
	return controller.state, controller.err
}

func TestShouldDeferModemSMSSync(t *testing.T) {
	tests := []struct {
		name  string
		state vowifi.State
		err   error
		want  bool
	}{
		{
			name:  "cellular session",
			state: vowifi.State{Phase: vowifi.PhaseIdle},
		},
		{
			name:  "vowifi SIM setup",
			state: vowifi.State{Enabled: true, Phase: vowifi.PhaseSIMReady},
			want:  true,
		},
		{
			name:  "vowifi IMS registration",
			state: vowifi.State{Enabled: true, Phase: vowifi.PhaseIMSReady},
			want:  true,
		},
		{
			name:  "stable vowifi catch-up",
			state: vowifi.State{Enabled: true, Phase: vowifi.PhaseSMSReady, SMSReady: true},
		},
		{
			name:  "failed vowifi cellular fallback",
			state: vowifi.State{Enabled: true, Phase: vowifi.PhaseFailed},
		},
		{
			name:  "unknown vowifi state",
			state: vowifi.State{Enabled: true, Phase: vowifi.PhaseSIMReady},
			err:   context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldDeferModemSMSSync(test.state, test.err); got != test.want {
				t.Fatalf("shouldDeferModemSMSSync() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVoWiFiEnableUpdatesPolicyAndQueuesRuntime(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := store.Device{ID: "ec20", Name: "EC20"}
	if err := database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	controller := &fakeVoWiFiController{
		state: vowifi.State{DeviceID: "ec20", Phase: vowifi.PhaseIdle},
	}
	server := &Server{
		store:               database,
		vowifi:              controller,
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRequestBodyBytes: 4096,
	}
	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/devices/ec20/vowifi",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleVoWiFiEnabled(response, request, config, true)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(controller.enabled) != 1 || !controller.enabled[0] {
		t.Fatalf("queued enables = %#v", controller.enabled)
	}
	stored, err := database.Device(context.Background(), "ec20")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.VoWiFiEnabled {
		t.Fatal("VoWiFi policy was not persisted")
	}
}

func TestVoWiFiRepeatedEnableWhileStartingIsAccepted(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := store.Device{ID: "ec20", Name: "EC20", VoWiFiEnabled: true}
	if err := database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	controller := &fakeVoWiFiController{
		// A reconnect briefly enters Stopping/Enabled=false while the persisted
		// policy remains enabled. Repeating "enable" is still the same intent.
		state: vowifi.State{DeviceID: "ec20", Phase: vowifi.PhaseStopping, Enabled: false},
		err:   vowifiruntime.ErrOperationInProgress,
	}
	server := &Server{
		store:               database,
		vowifi:              controller,
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRequestBodyBytes: 4096,
	}
	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/devices/ec20/vowifi",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleVoWiFiEnabled(response, request, config, true)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	stored, err := database.Device(context.Background(), "ec20")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.VoWiFiEnabled {
		t.Fatal("idempotent enable reverted the desired policy")
	}
}

func TestVoWiFiReconnectRequiresEnabledPolicy(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	controller := &fakeVoWiFiController{}
	server := &Server{
		store:  database,
		vowifi: controller,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/devices/ec20/vowifi/actions/reconnect",
		nil,
	)
	response := httptest.NewRecorder()
	server.handleVoWiFiReconnect(
		response,
		request,
		store.Device{ID: "ec20", Name: "EC20"},
		true,
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if controller.reconnects != 0 {
		t.Fatalf("reconnects = %d", controller.reconnects)
	}
}
