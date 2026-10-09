package device

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ResetSIM 仅重载已经关闭射频的 AT 模组 SIM，不切换 profile，也不执行在线模组重启。
// 调用者必须先停止 VoWiFi；UICC 锁防止重置与其他卡操作交错。
func (manager *Manager) ResetSIM(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := manager.lockESIMContext(ctx); err != nil {
		return err
	}
	defer manager.unlockESIM()
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	candidate := manager.candidateFor(state)
	if !candidate.HasATPort() || isNativeQMICandidate(candidate) {
		return errors.New("SIM reset requires an AT modem")
	}
	if manager.esimRecoveryActive(id) {
		return errESIMRecovering
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return err
	}
	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		return err
	}
	mode, err := manager.readOperatingMode(ctx, client)
	if err != nil {
		return err
	}
	if mode != 4 {
		return errors.New("SIM reset requires cellular RF to be disabled (CFUN=4)")
	}
	if err := manager.softResetSIMLocked(ctx, id, state, client); err != nil {
		return err
	}
	var lastErr error
	for {
		response, err := manager.command(ctx, client, "AT+CPIN?")
		if err == nil {
			status, ready := parseCPIN(response)
			if ready {
				mode, err := manager.readOperatingMode(ctx, client)
				if err != nil {
					return err
				}
				if mode != 4 {
					return errors.New("SIM reset did not preserve CFUN=4")
				}
				return nil
			}
			lastErr = fmt.Errorf("SIM is not ready: %s", status)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for SIM after reset: %w", errors.Join(ctx.Err(), lastErr))
		case <-time.After(500 * time.Millisecond):
		}
	}
}
