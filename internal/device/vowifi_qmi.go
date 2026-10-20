package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

func (manager *Manager) withNativeQMIVoWiFiSession(ctx context.Context, id string, fn func(nativeQMIVoWiFiSession) error) error {
	control, native, err := manager.nativeQMIControl(id)
	if err != nil {
		return err
	}
	if !native {
		return errors.New("native QMI control is unavailable")
	}
	session, err := manager.qmiRadioOpener(ctx, control)
	if err != nil {
		return fmt.Errorf("open native QMI control: %w", err)
	}
	defer session.Close()
	qmiSession, ok := session.(nativeQMIVoWiFiSession)
	if !ok {
		return errors.New("native QMI session lacks UIM/NAS support")
	}
	return fn(qmiSession)
}

// ReadNativeQMIIdentity reads the subscription through QMI UIM/DMS. When an AT
// port exists, first wake the SIM interface in RF-off mode before taking the
// QMI lease. Primitive return values keep device independent from VoWiFi.
func (manager *Manager) ReadNativeQMIIdentity(ctx context.Context, id string) (iccid, imsi, imei, mcc, mnc string, err error) {
	// A missing/unpowered card may never answer READ TRANSPARENT. Do not hold
	// the shared QMI lease for the lifecycle's two-minute SIM-ready timeout.
	ctx, cancelIdentity := context.WithTimeout(ctx, 25*time.Second)
	defer cancelIdentity()
	if prepareErr := manager.prepareNativeQMISIMIdentity(ctx, id); prepareErr != nil && manager.logger != nil {
		manager.logger.Debug("native QMI SIM AT preparation unavailable", "category", "hardware", "device_id", id, "error", prepareErr)
	}
	err = manager.withNativeQMIVoWiFiSession(ctx, id, func(session nativeQMIVoWiFiSession) error {
		var recovered bool
		if iccid, recovered, err = readVoWiFiNativeQMIICCID(ctx, session); err != nil {
			control, _, _ := manager.nativeQMIControl(id)
			hotSwapRecovered, recoveryErr := manager.recoverNativeQMIHotSwapSession(ctx, control, session)
			if !hotSwapRecovered {
				return fmt.Errorf("read QMI ICCID: %w", errors.Join(err, recoveryErr))
			}
			readContext, cancelRead := context.WithTimeout(ctx, 3*time.Second)
			iccid, err = session.GetICCID(readContext)
			cancelRead()
			if err != nil {
				return fmt.Errorf("read QMI ICCID after hot-swap recovery: %w", err)
			}
		}
		if recovered && manager.logger != nil {
			manager.logger.Info("native QMI SIM recovered from shutdown with cellular RF disabled", "category", "hardware", "device_id", id)
		}
		if imsi, err = session.GetIMSI(ctx); err != nil {
			return fmt.Errorf("read QMI IMSI: %w", err)
		}
		if imei, err = session.GetIMEI(ctx); err != nil {
			return fmt.Errorf("read QMI IMEI: %w", err)
		}
		if mcc, mnc, err = session.GetNativeMCCMNC(ctx); err != nil {
			return fmt.Errorf("read QMI home PLMN: %w", err)
		}
		return nil
	})
	return
}

type nativeQMISIMStartupSession interface {
	GetICCID(context.Context) (string, error)
	GetOperatingMode(context.Context) (qmi.OperatingMode, error)
	SetOperatingMode(context.Context, qmi.OperatingMode) error
	PowerOnSIM(context.Context, uint8) error
}

// A cold-booted 410 can report DMS shutting-down and UIM no-ATR indefinitely.
// Repeating EF_ICCID reads cannot initialize that SIM. Recover this precise
// power state into low-power mode and power on slot 1, without ever requesting
// DMS online, cycling a working SIM, changing a profile, or entering a PIN.
// Other read failures retain their original error and do not trigger recovery.
func readVoWiFiNativeQMIICCID(ctx context.Context, session nativeQMISIMStartupSession) (string, bool, error) {
	readContext, cancelRead := context.WithTimeout(ctx, 3*time.Second)
	iccid, identityErr := session.GetICCID(readContext)
	cancelRead()
	if identityErr == nil || ctx.Err() != nil {
		return iccid, false, identityErr
	}
	mode, err := session.GetOperatingMode(ctx)
	if err != nil || mode != qmi.ModeShutdown {
		return "", false, identityErr
	}
	recoveryContext, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := session.SetOperatingMode(recoveryContext, qmi.ModeLowPower); err != nil {
		return "", false, fmt.Errorf("leave QMI shutdown with RF disabled: %w", err)
	}
	// Verify the requested mode before addressing the SIM. Neither shutdown
	// nor generic 'radio off' is sufficient evidence that UIM is available.
	for {
		mode, err = session.GetOperatingMode(recoveryContext)
		if err != nil {
			return "", false, fmt.Errorf("verify QMI low-power SIM recovery: %w", err)
		}
		if mode == qmi.ModeLowPower {
			break
		}
		if mode != qmi.ModeShutdown {
			return "", false, errors.New("QMI SIM recovery did not reach low-power mode")
		}
		if err := waitNativeQMIRecovery(recoveryContext, 250*time.Millisecond); err != nil {
			return "", false, fmt.Errorf("wait for QMI low-power SIM recovery: %w", err)
		}
	}
	if err := session.PowerOnSIM(recoveryContext, 1); err != nil {
		return "", false, fmt.Errorf("power on QMI SIM after shutdown: %w", err)
	}
	for {
		iccid, err = session.GetICCID(recoveryContext)
		if err == nil && iccid != "" {
			mode, err = session.GetOperatingMode(recoveryContext)
			if err != nil {
				return "", false, fmt.Errorf("verify RF state after QMI SIM recovery: %w", err)
			}
			if mode != qmi.ModeLowPower {
				return "", false, errors.New("QMI low-power mode was not preserved after SIM recovery")
			}
			return iccid, true, nil
		}
		if waitErr := waitNativeQMIRecovery(recoveryContext, 250*time.Millisecond); waitErr != nil {
			return "", false, fmt.Errorf("wait for QMI SIM identity after shutdown: %w", errors.Join(waitErr, err))
		}
	}
}

func (manager *Manager) ProbeNativeQMIApplication(ctx context.Context, id, preference string) (aid []byte, application string, err error) {
	err = manager.withNativeQMIVoWiFiSession(ctx, id, func(session nativeQMIVoWiFiSession) error {
		if strings.EqualFold(strings.TrimSpace(preference), "isim_strict") {
			aid, err = session.GetISIMAID(ctx)
			application = "ISIM"
			return err
		}
		if aid, err = session.GetUSIMAID(ctx); err == nil {
			application = "USIM"
			return nil
		}
		aid, err = session.GetISIMAID(ctx)
		application = "ISIM"
		return err
	})
	return
}

func (manager *Manager) AuthenticateNativeQMI(ctx context.Context, id string, aid, apdu []byte) (response []byte, err error) {
	err = manager.withNativeQMIVoWiFiSession(ctx, id, func(session nativeQMIVoWiFiSession) error {
		channel, openErr := openNativeQMIChannelWithRecovery(ctx, session, 1, aid)
		if openErr != nil {
			return fmt.Errorf("open QMI UIM logical channel: %w", openErr)
		}
		command := append([]byte(nil), apdu...)
		response, err = session.SendAPDU(ctx, 1, channel, command)
		// ISO/IEC 7816-4 procedure bytes are transport-level continuation,
		// not an AKA rejection. QMI exposes the raw status words, so follow
		// 61xx/9Fxx with GET RESPONSE and retry 6Cxx with the advised Le while
		// the same logical channel is still open.
		for step := 0; err == nil && step < 4 && len(response) >= 2; step++ {
			sw1, sw2 := response[len(response)-2], response[len(response)-1]
			switch sw1 {
			case 0x61, 0x9f:
				response, err = session.SendAPDU(ctx, 1, channel, []byte{0x00, 0xc0, 0x00, 0x00, sw2})
			case 0x6c:
				if len(command) < 5 {
					step = 4
					continue
				}
				command[len(command)-1] = sw2
				response, err = session.SendAPDU(ctx, 1, channel, command)
			default:
				step = 4
			}
		}
		// 请求取消后仍释放本次打开的通道，清理最多等待两秒。
		closeContext, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		closeErr := session.CloseLogicalChannel(closeContext, 1, channel)
		cancelClose()
		return errors.Join(err, closeErr)
	})
	return
}

type nativeQMIChannelRecoverySession interface {
	OpenLogicalChannel(context.Context, uint8, []byte) (byte, error)
	PowerOffSIM(context.Context, uint8) error
	PowerOnSIM(context.Context, uint8) error
}

// OpenStick 410 can leave the physical UICC powered but unable to allocate a
// logical channel after a SIM hot-swap. A UIM service reset alone does not
// clear that state; cycling the affected physical slot does. Recover only the
// precise QMI InsufficientResources response, then retry the original AID once.
func openNativeQMIChannelWithRecovery(
	ctx context.Context,
	session nativeQMIChannelRecoverySession,
	slot uint8,
	aid []byte,
) (byte, error) {
	channel, err := session.OpenLogicalChannel(ctx, slot, aid)
	if err == nil || !isQMIInsufficientResources(err) {
		return channel, err
	}
	if resetter, ok := session.(nativeQMIUIMResetSession); ok {
		_ = resetter.ResetUIM(ctx)
	}
	if powerErr := cycleNativeQMISIMPower(ctx, session, slot, 3*time.Second); powerErr != nil {
		return 0, errors.Join(err, powerErr)
	}
	if waitErr := waitNativeQMIRecovery(ctx, 5*time.Second); waitErr != nil {
		return 0, errors.Join(err, waitErr)
	}
	return session.OpenLogicalChannel(ctx, slot, aid)
}

func isQMIInsufficientResources(err error) bool {
	qmiErr := qmi.GetQMIError(err)
	return qmiErr != nil && qmiErr.Service == qmi.ServiceUIM && qmiErr.ErrorCode == 0x0044
}

var waitNativeQMIRecovery = func(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (manager *Manager) NativeQMIRadioSnapshot(ctx context.Context, id string) (mode int, psAttached bool, err error) {
	err = manager.withNativeQMIVoWiFiSession(ctx, id, func(session nativeQMIVoWiFiSession) error {
		qmiMode, modeErr := session.GetOperatingMode(ctx)
		if modeErr != nil {
			return modeErr
		}
		mode = qmiModeAsCFUN(qmiMode)
		serving, servingErr := session.GetServingSystem(ctx)
		if servingErr == nil && serving != nil {
			psAttached = serving.PSAttached
		}
		// An RF-off modem commonly rejects NAS serving-system queries; DMS mode
		// remains sufficient evidence and data cannot be attached while RF is off.
		if servingErr != nil && !isQMIRadioOffMode(qmiMode) {
			return servingErr
		}
		return nil
	})
	return
}

func (manager *Manager) StopNativeQMICellularData(ctx context.Context, id string) error {
	return manager.withNativeQMIVoWiFiSession(ctx, id, func(session nativeQMIVoWiFiSession) error {
		serving, err := session.GetServingSystem(ctx)
		if err != nil {
			return nil
		}
		if serving == nil || !serving.PSAttached {
			return nil
		}
		if err := session.AttachDetach(ctx, false); err != nil {
			return err
		}
		deadline := time.NewTicker(250 * time.Millisecond)
		defer deadline.Stop()
		for attempt := 0; attempt < 12; attempt++ {
			current, readErr := session.GetServingSystem(ctx)
			if readErr == nil && (current == nil || !current.PSAttached) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
			}
		}
		return errors.New("native QMI packet service remained attached")
	})
}

func (manager *Manager) SetNativeQMIRadioOff(ctx context.Context, id string, off bool) error {
	_, err := manager.SetFlight(ctx, id, off)
	return err
}

func (manager *Manager) powerCycleNativeQMISIM(ctx context.Context, id string) (bool, error) {
	control, native, err := manager.nativeQMIControl(id)
	if err != nil || !native {
		return native, err
	}
	session, err := manager.qmiRadioOpener(ctx, control)
	if err != nil {
		return true, err
	}
	defer session.Close()
	uim, ok := session.(nativeQMIVoWiFiSession)
	if !ok {
		return true, errors.New("native QMI session lacks SIM power control")
	}
	if resetter, ok := session.(nativeQMIUIMResetSession); ok {
		_ = resetter.ResetUIM(ctx)
	}
	if err := cycleNativeQMISIMPower(ctx, uim, 1, 3*time.Second); err != nil {
		return true, err
	}
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-time.After(time.Second):
	}
	return true, nil
}
