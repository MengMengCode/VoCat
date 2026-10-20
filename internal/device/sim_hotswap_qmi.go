package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
	"vocat/internal/modem"
)

type nativeQMIHotSwapSession interface {
	qmiRadioSession
	GetCardStatusDetails(context.Context) (*qmi.CardStatusDetails, qmi.SIMStatus, error)
	PowerOffSIM(context.Context, uint8) error
	PowerOnSIM(context.Context, uint8) error
}

type nativeQMISIMPowerSession interface {
	PowerOffSIM(context.Context, uint8) error
	PowerOnSIM(context.Context, uint8) error
}

// DMS low-power alone can leave the 410 at CFUN=0 (SIM interface disabled).
// CFUN=4 keeps cellular RF off while making the SIM interface available. Never
// issue CFUN=0 or CFUN=1 here, and keep AT access outside the QMI lease.
func (manager *Manager) prepareNativeQMISIMInterface(ctx context.Context, client modem.Client) (bool, error) {
	response, err := manager.command(ctx, client, "AT+CFUN?")
	if err != nil {
		return false, err
	}
	mode, known := parseCFUN(response)
	if !known || mode != 0 {
		return false, nil
	}
	if _, err := manager.command(ctx, client, "AT+CFUN=4"); err != nil {
		return false, err
	}
	response, err = manager.command(ctx, client, "AT+CFUN?")
	if err != nil {
		return false, err
	}
	if mode, known = parseCFUN(response); !known || mode != 4 {
		return false, errors.New("native SIM interface did not reach RF-off CFUN=4")
	}
	return true, waitNativeQMIRecovery(ctx, time.Second)
}

func (manager *Manager) prepareNativeQMISIMIdentity(ctx context.Context, id string) error {
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	candidate := manager.candidateFor(state)
	if !isNativeQMICandidate(candidate) || !candidate.HasATPort() {
		return nil // Preserve support for QMI-only devices without an AT port.
	}
	if err := lockMutexContext(ctx, &state.opMu); err != nil {
		return err
	}
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return err
	}
	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		return err
	}
	_, err = manager.prepareNativeQMISIMInterface(ctx, client)
	return err
}

// A timed-out power-off request may already have reached the modem. Always
// issue power-on after any attempted power-off, even on cancellation/error.
func cycleNativeQMISIMPower(ctx context.Context, session nativeQMISIMPowerSession, slot uint8, delay time.Duration) error {
	offErr := session.PowerOffSIM(ctx, slot)
	var waitErr error
	if offErr == nil {
		waitErr = waitNativeQMIRecovery(ctx, delay)
	}
	restoreContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	onErr := session.PowerOnSIM(restoreContext, slot)
	cancel()
	if offErr != nil {
		offErr = fmt.Errorf("power off QMI SIM slot %d: %w", slot, offErr)
	}
	if onErr != nil {
		onErr = fmt.Errorf("restore power to QMI SIM slot %d: %w", slot, onErr)
	}
	return errors.Join(offErr, waitErr, onErr)
}

func (manager *Manager) recoverNativeQMIHotSwap(ctx context.Context, candidate modem.Candidate) (bool, error) {
	if manager.qmiRadioOpener == nil {
		return false, nil
	}
	session, err := manager.qmiRadioOpener(ctx, candidate.QMIControl)
	if err != nil {
		return false, err
	}
	defer session.Close()
	return manager.recoverNativeQMIHotSwapSession(ctx, candidate.QMIControl, session)
}

// A 410 may latch UIM error 6 after a physical swap, even after reinsertion.
// CFUN cycling does not clear it. Inspect card status before touching power:
// absent, present, PIN/PUK locked and unrelated electrical errors are left alone.
// The QMI session owns the control-node lease throughout this transaction, so
// it cannot interrupt another UIM logical-channel transaction.
func (manager *Manager) recoverNativeQMIHotSwapSession(ctx context.Context, control string, radio qmiRadioSession) (bool, error) {
	session, ok := radio.(nativeQMIHotSwapSession)
	if !ok {
		return false, nil
	}
	details, _, err := session.GetCardStatusDetails(ctx)
	if err != nil {
		return false, err
	}
	if details == nil || details.CardState != 2 || (details.ErrorCode != 1 && details.ErrorCode != 3 && details.ErrorCode != 6) {
		return false, nil
	}
	// A removed card can keep reporting error 6. Limit power cycles, while
	// continuing cheap status probes so a normally inserted card appears fast.
	manager.nativeQMISIMRecoveryMu.Lock()
	if time.Since(manager.nativeQMISIMRecoveries[control]) < 15*time.Second {
		manager.nativeQMISIMRecoveryMu.Unlock()
		return false, nil
	}
	if manager.nativeQMISIMRecoveries == nil {
		manager.nativeQMISIMRecoveries = make(map[string]time.Time)
	}
	manager.nativeQMISIMRecoveries[control] = time.Now()
	manager.nativeQMISIMRecoveryMu.Unlock()
	ctx, cancelRecovery := context.WithTimeout(ctx, 12*time.Second)
	defer cancelRecovery()
	if manager.logger != nil {
		manager.logger.Info("recovering native QMI SIM after hot-swap", "category", "hardware", "control", control, "card_error", details.ErrorCode)
	}

	// The reinserted card has not yet been identified or had its policy checked.
	// Establish and verify RF-off before reinitializing the physical slot.
	mode, err := session.GetOperatingMode(ctx)
	if err != nil {
		return false, err
	}
	if mode != qmi.ModeLowPower {
		if err := session.SetOperatingMode(ctx, qmi.ModeLowPower); err != nil {
			return false, fmt.Errorf("disable RF before SIM recovery: %w", err)
		}
		mode, err = session.GetOperatingMode(ctx)
		if err != nil || mode != qmi.ModeLowPower {
			return false, errors.Join(errors.New("SIM recovery requires verified QMI low-power mode"), err)
		}
	}
	if details.ErrorCode == 1 {
		// An interrupted earlier transaction can leave slot power off. Do not
		// power it down again: repair the orphaned power-off directly.
		if err := session.PowerOnSIM(ctx, 1); err != nil {
			return false, fmt.Errorf("restore powered-down QMI SIM: %w", err)
		}
	} else if err := cycleNativeQMISIMPower(ctx, session, 1, 3*time.Second); err != nil {
		return false, err
	}
	for {
		details, _, err = session.GetCardStatusDetails(ctx)
		if err != nil {
			return false, err
		}
		if details != nil && details.CardState == 1 && details.NumApp > 0 &&
			(details.AppState == qmi.UIMAppStateReady || details.PIN1State == qmi.PINStatusNotVerified || details.PIN1State == qmi.PINStatusBlocked) {
			mode, err = session.GetOperatingMode(ctx)
			if err != nil || mode != qmi.ModeLowPower {
				return false, errors.Join(errors.New("QMI low-power mode was not preserved after hot-swap recovery"), err)
			}
			if manager.logger != nil {
				manager.logger.Info("native QMI hot-swapped SIM recovered with cellular RF disabled", "category", "hardware", "control", control)
			}
			return true, nil
		}
		// Power-on is acknowledged before ATR/application initialization ends.
		// In particular, transient no-ATR/power-down is not a final result.
		if details != nil && details.CardState == 0 {
			return false, nil
		}
		if err := waitNativeQMIRecovery(ctx, 250*time.Millisecond); err != nil {
			if details != nil {
				return false, fmt.Errorf("wait for QMI SIM initialization (state=%d card_error=%d app_state=%d): %w", details.CardState, details.ErrorCode, details.AppState, err)
			}
			return false, err
		}
	}
}
