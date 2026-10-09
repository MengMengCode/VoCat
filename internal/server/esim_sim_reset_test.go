package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"vocat/internal/device"
	"vocat/internal/loghub"
	"vocat/internal/store"
)

type simResetTestController struct {
	fakeDeviceController
	mu            sync.Mutex
	inventoryErr  error
	afterResetErr error
	resetErr      error
	resets        int
	vowifi        *fakeEsimVoWiFiController
	onReset       func(context.Context) error
}

func (controller *simResetTestController) ESIMInventory(ctx context.Context, _ string) ([]device.EsimInventoryEntry, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.inventoryErr != nil {
		return nil, controller.inventoryErr
	}
	return []device.EsimInventoryEntry{{Info: device.EsimInfo{EID: "test-eid"}}}, nil
}

func (controller *simResetTestController) ResetSIM(ctx context.Context, _ string) error {
	if controller.vowifi.enabled {
		return errors.New("SIM reset began before VoWiFi stopped")
	}
	controller.mu.Lock()
	controller.resets++
	controller.mu.Unlock()
	if controller.onReset != nil {
		if err := controller.onReset(ctx); err != nil {
			return err
		}
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.resetErr != nil {
		return controller.resetErr
	}
	controller.inventoryErr = controller.afterResetErr
	return nil
}

func newSIMResetTestServer(t *testing.T, controller *simResetTestController) (*Server, *loghub.Hub) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "configured", Name: "test", DeviceType: store.DeviceTypePCIeEC20EC25, VoWiFiEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	hub := loghub.New(slog.NewTextHandler(io.Discard, nil), 100)
	controller.vowifi = &fakeEsimVoWiFiController{enabled: true}
	return &Server{store: database, devices: controller, vowifi: controller.vowifi, logger: slog.New(hub)}, hub
}

func TestOrdinarySIMRequestsStayEmptyWithoutResetOrLogs(t *testing.T) {
	controller := &simResetTestController{inventoryErr: device.ErrNoEUICC}
	server, hub := newSIMResetTestServer(t, controller)
	for range 4 {
		recorder := httptest.NewRecorder()
		server.handleESIM(recorder, httptest.NewRequest(http.MethodGet, "/esim", nil), nil, "physical", true, "configured")
		if recorder.Code != http.StatusOK || decodeData(t, recorder)["chipInfo"] != nil {
			t.Fatalf("ordinary SIM response = %d %s", recorder.Code, recorder.Body.String())
		}
	}
	if controller.resets != 0 || !controller.vowifi.enabled {
		t.Fatalf("ordinary SIM interrupted: resets=%d, VoWiFi=%v", controller.resets, controller.vowifi.enabled)
	}
	if logs := hub.History(100, slog.LevelDebug, ""); len(logs) != 0 {
		t.Fatalf("ordinary SIM produced logs: %+v", logs)
	}
}

func TestESIMSIMResetFailureIsAttemptedOnceAndRestoresVoWiFi(t *testing.T) {
	for _, test := range []struct {
		name          string
		resetErr      error
		afterResetErr error
		wantSuccess   bool
	}{
		{name: "recovered", wantSuccess: true},
		{name: "still_missing", afterResetErr: device.ErrESIMManagementUnavailable},
		{name: "not_detected_after_reset", afterResetErr: device.ErrNoEUICC},
		{name: "reset_rejected", resetErr: errors.New("reset rejected")},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &simResetTestController{
				inventoryErr: device.ErrESIMManagementUnavailable, resetErr: test.resetErr, afterResetErr: test.afterResetErr,
			}
			server, hub := newSIMResetTestServer(t, controller)
			info, err := server.readESIMInventory(context.Background(), "configured", "physical")
			if (err == nil) != test.wantSuccess {
				t.Fatalf("success = %v, want %v; error=%v", err == nil, test.wantSuccess, err)
			}
			if test.wantSuccess && len(info) != 1 {
				t.Fatalf("restored inventory = %+v", info)
			}
			if test.afterResetErr == device.ErrNoEUICC && errors.Is(err, device.ErrNoEUICC) {
				t.Fatal("known recovery failure was hidden as an ordinary SIM")
			}
			for range 3 {
				_, _ = server.readESIMInventory(context.Background(), "configured", "physical")
			}
			if controller.resets != 1 || !controller.vowifi.enabled {
				t.Fatalf("resets=%d, restored VoWiFi=%v", controller.resets, controller.vowifi.enabled)
			}
			logs := hub.History(100, slog.LevelWarn, "eSIM SIM reset recovery failed")
			if !test.wantSuccess && len(logs) != 1 {
				t.Fatalf("repeated recovery logs: %+v", logs)
			}
		})
	}
}

func TestESIMSIMResetConcurrentReadersShareOneRecovery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	controller := &simResetTestController{inventoryErr: device.ErrESIMManagementUnavailable}
	controller.onReset = func(context.Context) error {
		close(entered)
		<-release
		return nil
	}
	server, _ := newSIMResetTestServer(t, controller)
	var readers sync.WaitGroup
	errorsSeen := make(chan error, 5)
	read := func() {
		defer readers.Done()
		_, err := server.readESIMInventory(context.Background(), "configured", "physical")
		errorsSeen <- err
	}
	readers.Add(1)
	go read()
	<-entered
	for range 4 {
		readers.Add(1)
		go read()
	}
	close(release)
	readers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if controller.resets != 1 {
		t.Fatalf("concurrent readers performed %d resets", controller.resets)
	}
}

func TestESIMSIMResetCompletesAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := &simResetTestController{inventoryErr: device.ErrESIMManagementUnavailable}
	controller.onReset = func(recoveryCtx context.Context) error {
		cancel()
		return recoveryCtx.Err()
	}
	server, _ := newSIMResetTestServer(t, controller)
	if _, err := server.readESIMInventory(ctx, "configured", "physical"); err != nil {
		t.Fatalf("committed recovery canceled with browser: %v", err)
	}
	if !controller.vowifi.enabled {
		t.Fatal("VoWiFi was not restored")
	}
}

func TestESIMSIMResetRestoresLatestSavedVoWiFiSetting(t *testing.T) {
	controller := &simResetTestController{inventoryErr: device.ErrESIMManagementUnavailable}
	server, _ := newSIMResetTestServer(t, controller)
	controller.onReset = func(ctx context.Context) error {
		config, err := server.store.Device(ctx, "configured")
		if err != nil {
			return err
		}
		config.VoWiFiEnabled = false
		return server.store.UpsertDevice(ctx, config)
	}
	if _, err := server.readESIMInventory(context.Background(), "configured", "physical"); err != nil {
		t.Fatal(err)
	}
	if controller.vowifi.enabled {
		t.Fatal("recovery overrode the newly disabled VoWiFi setting")
	}
}
