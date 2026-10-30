package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"vocat/internal/device"
)

type esimRecoveryWaiter interface {
	WaitESIMRecovery(context.Context, string) error
}

// Return true only when the background waiter takes ownership of smsSyncMu
// and the matching VoWiFi maintenance scope. The caller must not release them.
func (s *Server) waitProfileRecovery(ctx context.Context, waiter esimRecoveryWaiter, configuredID, physicalID string, endMaintenance func()) (bool, error) {
	err := waiter.WaitESIMRecovery(ctx, physicalID)
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		// Recovery may outlive the foreground request. A second deadline must not
		// admit new SIM operations while the first recovery is still cleaning up.
		go s.finishDetachedProfileRecovery(waiter, configuredID, physicalID, endMaintenance)
		return true, err
	}
	return false, err
}

// Keep acquisition cancellable when another profile transition still owns the
// SIM. A disconnected browser must not leave a goroutine parked on Mutex.Lock.
func lockProfileTransition(ctx context.Context, lock *sync.Mutex) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if lock.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// The device manager owns a bounded detached recovery context. Wait for its
// actual completion, including cleanup, rather than releasing maintenance at
// another timeout and letting a new operation race that cleanup.
func (s *Server) finishDetachedProfileRecovery(waiter esimRecoveryWaiter, configuredID, physicalID string, endMaintenance func()) {
	defer s.smsSyncMu.Unlock()
	defer endMaintenance()
	if err := waiter.WaitESIMRecovery(context.Background(), physicalID); err != nil {
		s.stopUnverifiedProfileRuntime(context.Background(), configuredID)
		if s.logger != nil {
			s.logger.Warn("eSIM detached recovery failed; VoWiFi remains stopped", "device_id", configuredID, "error", device.HardwareErrorDetail(err))
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s.restoreProfileSwitchFailureState(ctx, configuredID, physicalID, endMaintenance)
}

// Device configuration is the active runtime cache; per-card preferences stay
// intact. Until a fresh, ready SIM snapshot identifies the active subscription,
// neither the missing-card reconciler nor a leftover old identity may enable it.
func (s *Server) stopUnverifiedProfileRuntime(ctx context.Context, configuredID string) {
	if s.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	config, err := s.store.Device(ctx, configuredID)
	if err == nil {
		config.VoWiFiEnabled, config.NetworkEnabled = false, false
		config.APN = ""
		err = s.store.UpsertDevice(ctx, config)
	}
	if err != nil && s.logger != nil {
		s.logger.Warn("eSIM recovery failed; could not clear unverified runtime policy", "device_id", configuredID, "error", err)
	}
	if s.vowifi != nil {
		_, _ = s.vowifi.RequestEnabled(configuredID, false)
	}
}
