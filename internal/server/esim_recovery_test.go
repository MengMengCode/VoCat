package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/remoteproc"
	"vocat/internal/store"
)

type recoveringSwitchController struct {
	flightTrackingController
	entered        chan struct{}
	done           chan struct{}
	recoveryErr    error
	refreshEntered chan struct{}
	allowRefresh   chan struct{}
}

func (c *recoveringSwitchController) Refresh(ctx context.Context, id string) (device.Snapshot, error) {
	if c.refreshEntered != nil {
		c.refreshEntered <- struct{}{}
		select {
		case <-c.allowRefresh:
		case <-ctx.Done():
			return device.Snapshot{}, ctx.Err()
		}
	}
	return c.flightTrackingController.Refresh(ctx, id)
}

func (c *recoveringSwitchController) WaitESIMRecovery(ctx context.Context, _ string) error {
	c.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.recoveryErr
	}
}

func TestUncertainSwitchKeepsMaintenanceUntilRecoveryActuallyEnds(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovered", true: "recovery failed"}[failed], func(t *testing.T) {
			db, err := store.Open(context.Background(), ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "dev1", VoWiFiEnabled: true}); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertCardPolicy(context.Background(), store.CardPolicy{ICCID: "live-card", VoWiFiEnabled: true, AirplaneEnabled: true}); err != nil {
				t.Fatal(err)
			}
			original := errors.New("EnableProfile transport outcome uncertain")
			c := &recoveringSwitchController{flightTrackingController: flightTrackingController{switchErr: original}, entered: make(chan struct{}, 2), done: make(chan struct{})}
			c.entry.Snapshot = &device.Snapshot{ICCID: "live-card"}
			if failed {
				c.recoveryErr = errors.New("SIM recovery failed")
			}
			var release sync.Once
			defer release.Do(func() { close(c.done) })
			wifi := &automationSwitchWiFi{fakeEsimVoWiFiController: fakeEsimVoWiFiController{enabled: true}}
			s := &Server{store: db, devices: c, vowifi: wifi, logger: regionTestLogger()}
			result := make(chan error, 1)
			go func() {
				_, err := s.switchESIMProfile(context.Background(), "dev1", "dev1", "target", "")
				result <- err
			}()
			select {
			case <-c.entered:
			case <-time.After(time.Second):
				t.Fatal("did not wait for recovery")
			}
			if !wifi.maintenance || wifi.end != 0 || wifi.enabled {
				t.Fatal("VoWiFi resumed during recovery")
			}
			if s.smsSyncMu.TryLock() {
				s.smsSyncMu.Unlock()
				t.Fatal("SMS scanning admitted during recovery")
			}
			release.Do(func() { close(c.done) })
			select {
			case err := <-result:
				if !errors.Is(err, original) {
					t.Fatalf("uncertain switch became success: %v", err)
				}
				if failed && !errors.Is(err, c.recoveryErr) {
					t.Fatalf("recovery failure lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("switch did not finish")
			}
			if wifi.maintenance || wifi.end != 1 || wifi.enabled == failed {
				t.Fatalf("post-recovery state: %+v", wifi)
			}
			if failed {
				config, err := db.Device(context.Background(), "dev1")
				if err != nil || config.VoWiFiEnabled || config.NetworkEnabled {
					t.Fatalf("unverified runtime policy retained: %+v %v", config, err)
				}
				policy, err := db.CardPolicy(context.Background(), "live-card")
				if err != nil || !policy.VoWiFiEnabled {
					t.Fatal("recovery failure overwrote saved card preference")
				}
			}
			if !s.smsSyncMu.TryLock() {
				t.Fatal("transition lock leaked")
			}
			s.smsSyncMu.Unlock()
		})
	}
}

func TestFailureRestorationKeepsMaintenanceUntilLivePolicyIsPrepared(t *testing.T) {
	db, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "dev1", VoWiFiEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCardPolicy(context.Background(), store.CardPolicy{ICCID: "new-card", AirplaneEnabled: true}); err != nil {
		t.Fatal(err)
	}
	c := &recoveringSwitchController{refreshEntered: make(chan struct{}, 1), allowRefresh: make(chan struct{})}
	c.entry.Snapshot = &device.Snapshot{ICCID: "new-card"}
	var release sync.Once
	defer release.Do(func() { close(c.allowRefresh) })
	wifi := &automationSwitchWiFi{maintenance: true}
	s := &Server{store: db, devices: c, vowifi: wifi, logger: regionTestLogger()}
	finished := make(chan struct{})
	go func() {
		s.restoreProfileSwitchFailureState(context.Background(), "dev1", "dev1", func() { wifi.EndMaintenance("dev1") })
		close(finished)
	}()
	select {
	case <-c.refreshEntered:
	case <-time.After(time.Second):
		t.Fatal("no live identity read")
	}
	if !wifi.maintenance || wifi.end != 0 {
		t.Fatal("old policy admitted while live identity was unknown")
	}
	release.Do(func() { close(c.allowRefresh) })
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("restore did not finish")
	}
	if wifi.maintenance || wifi.end != 1 || wifi.enabled {
		t.Fatalf("incorrect live policy restoration: %+v", wifi)
	}
}

func TestRecoveryWaitTimeoutTransfersOwnershipUntilBackgroundCompletion(t *testing.T) {
	c := &recoveringSwitchController{entered: make(chan struct{}, 2), done: make(chan struct{}), recoveryErr: errors.New("recovery failed")}
	var release sync.Once
	defer release.Do(func() { close(c.done) })
	s := &Server{logger: regionTestLogger()}
	s.smsSyncMu.Lock()
	var ends atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transferred, err := s.waitProfileRecovery(ctx, c, "dev1", "dev1", func() { ends.Add(1) })
	if !transferred || !errors.Is(err, context.Canceled) {
		t.Fatalf("handoff=%v err=%v", transferred, err)
	}
	for range 2 {
		select {
		case <-c.entered:
		case <-time.After(time.Second):
			t.Fatal("background waiter did not start")
		}
	}
	if ends.Load() != 0 {
		t.Fatal("maintenance released at HTTP timeout")
	}
	if s.smsSyncMu.TryLock() {
		s.smsSyncMu.Unlock()
		t.Fatal("transition released at HTTP timeout")
	}
	release.Do(func() { close(c.done) })
	deadline := time.Now().Add(time.Second)
	for !s.smsSyncMu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("background recovery leaked transition lock")
		}
		time.Sleep(time.Millisecond)
	}
	s.smsSyncMu.Unlock()
	if ends.Load() != 1 {
		t.Fatalf("maintenance ended %d times", ends.Load())
	}
}

func TestWaitingProfileTransitionHonorsCancellation(t *testing.T) {
	var lock sync.Mutex
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := lockProfileTransition(ctx, &lock); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock error: %v", err)
	}
}

func TestRecoveryErrorsDescribeBusyAndUnavailableDevices(t *testing.T) {
	s := &Server{logger: regionTestLogger()}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{device.ErrESIMRecovering, http.StatusConflict, "esim_recovering"},
		{remoteproc.ErrUnavailable, http.StatusServiceUnavailable, "modem_unavailable"},
	} {
		w := httptest.NewRecorder()
		s.writeDeviceError(w, tc.err)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("response: %d %s", w.Code, w.Body)
		}
	}
}
