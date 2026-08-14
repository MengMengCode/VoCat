package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/device"
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
			name:  "stopping while disabled",
			state: vowifi.State{Enabled: false, Phase: vowifi.PhaseStopping},
			want:  true,
		},
		{
			name:  "unknown stopping state",
			state: vowifi.State{Enabled: false, Phase: vowifi.PhaseStopping},
			err:   context.Canceled,
		},
		{
			name: "idle after radio restore failure",
			state: vowifi.State{
				Enabled: false, Phase: vowifi.PhaseIdle,
				CleanupErrors: []string{"close IMS: timeout", "restore radio: operation failed"},
			},
			want: true,
		},
		{
			name: "failed after radio restore failure",
			state: vowifi.State{
				Enabled: true, Phase: vowifi.PhaseFailed,
				CleanupErrors: []string{"restore radio: modem rejected request"},
			},
			want: true,
		},
		{
			name: "idle after non-radio cleanup failure",
			state: vowifi.State{
				Enabled: false, Phase: vowifi.PhaseIdle,
				CleanupErrors: []string{"close IMS: timeout", "close tunnel: timeout"},
			},
		},
		{
			name: "failed after non-radio cleanup failure",
			state: vowifi.State{
				Enabled: true, Phase: vowifi.PhaseFailed,
				CleanupErrors: []string{"close IMS: timeout"},
			},
		},
		{
			name:  "vowifi SIM setup",
			state: vowifi.State{Enabled: true, Phase: vowifi.PhaseSIMReady},
			want:  true,
		},
		{
			name: "stable IMS without optional SMS capability",
			state: vowifi.State{
				Enabled: true, Phase: vowifi.PhaseIMSReady, IMSReady: true,
				LastReason: "ims_registered_sms_unavailable",
			},
		},
		{
			name:  "transient IMS before SMS negotiation",
			state: vowifi.State{Enabled: true, Phase: vowifi.PhaseIMSReady, IMSReady: true},
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
		{
			name: "subscriber change barrier",
			err:  vowifiruntime.ErrSubscriberChangeInProgress,
			want: true,
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

func TestShouldDeferNative410SMSCatchUp(t *testing.T) {
	tests := []struct {
		name       string
		deviceType string
		liveID     string
		state      vowifi.State
		err        error
		want       bool
	}{
		{
			name:       "native 410 live IMS SMS owns delivery",
			deviceType: store.DeviceTypeWiFi410,
			liveID:     "wwan0",
			state:      vowifi.State{Enabled: true, Phase: vowifi.PhaseSMSReady, SMSReady: true},
			want:       true,
		},
		{
			name:       "native 410 stable IMS without SMS keeps RF off",
			deviceType: store.DeviceTypeWiFi410,
			liveID:     "wwan0",
			state: vowifi.State{
				Enabled: true, Phase: vowifi.PhaseIMSReady,
				LastReason: "ims_registered_sms_unavailable",
			},
			want: true,
		},
		{
			name:       "native 410 failure restored radio",
			deviceType: store.DeviceTypeWiFi410,
			liveID:     "wwan0",
			state:      vowifi.State{Enabled: true, Phase: vowifi.PhaseFailed},
		},
		{
			name:       "EC20 keeps stable AT catch-up",
			deviceType: store.DeviceTypePCIeEC20EC25,
			liveID:     "0125:ec20",
			state:      vowifi.State{Enabled: true, Phase: vowifi.PhaseSMSReady, SMSReady: true},
		},
		{
			name:       "disabled native 410 uses cellular WMS",
			deviceType: store.DeviceTypeWiFi410,
			liveID:     "wwan0",
			state:      vowifi.State{Phase: vowifi.PhaseIdle},
		},
		{
			name:       "unknown runtime does not assume RF ownership",
			deviceType: store.DeviceTypeWiFi410,
			liveID:     "wwan0",
			state:      vowifi.State{Enabled: true, Phase: vowifi.PhaseSMSReady},
			err:        context.Canceled,
		},
		{
			name:       "live native WWAN overrides stale USB type",
			deviceType: store.DeviceTypePCIeEC20EC25,
			liveID:     "wwan0",
			state:      vowifi.State{Enabled: true, Phase: vowifi.PhaseSMSReady, SMSReady: true},
			want:       true,
		},
		{
			name:       "live USB backend overrides stale 410 type",
			deviceType: store.DeviceTypeWiFi410,
			liveID:     "0125:ec20",
			state:      vowifi.State{Enabled: true, Phase: vowifi.PhaseSMSReady, SMSReady: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := store.Device{DeviceType: test.deviceType}
			if got := shouldDeferNative410SMSCatchUp(config, test.liveID, test.state, test.err); got != test.want {
				t.Fatalf("shouldDeferNative410SMSCatchUp() = %v, want %v", got, test.want)
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

func TestVoWiFiEnablePersistsActiveCardPolicy(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := store.Device{ID: "ec20", Name: "EC20"}
	if err := database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	iccid := "8986001234567890123"
	devices := fakeDeviceController{entry: device.Device{
		ID:         "ec20",
		Discovered: true,
		Snapshot:   &device.Snapshot{DeviceID: "ec20", ICCID: iccid, IMSI: "310260123456789"},
	}}
	controller := &fakeVoWiFiController{state: vowifi.State{DeviceID: "ec20", Phase: vowifi.PhaseIdle}}
	server := &Server{
		store:               database,
		devices:             devices,
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
	policy, err := database.CardPolicy(context.Background(), iccid)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.VoWiFiEnabled || !policy.AirplaneEnabled || policy.NetworkEnabled {
		t.Fatalf("active card policy = %+v, want VoWiFi+airplane on and network off", policy)
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
