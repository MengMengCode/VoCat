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

// ReadNativeQMIIdentity supplies the live subscription identity without using
// an AT port. The primitive return values intentionally keep device independent
// from the VoWiFi package while satisfying its narrow controller interface.
func (manager *Manager) ReadNativeQMIIdentity(ctx context.Context, id string) (iccid, imsi, imei, mcc, mnc string, err error) {
	err = manager.withNativeQMIVoWiFiSession(ctx, id, func(session nativeQMIVoWiFiSession) error {
		if iccid, err = session.GetICCID(ctx); err != nil {
			return fmt.Errorf("read QMI ICCID: %w", err)
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
		closeErr := session.CloseLogicalChannel(ctx, 1, channel)
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
	if powerErr := session.PowerOffSIM(ctx, slot); powerErr != nil {
		return 0, errors.Join(err, fmt.Errorf("power off QMI UIM slot %d: %w", slot, powerErr))
	}
	if waitErr := waitNativeQMIRecovery(ctx, 3*time.Second); waitErr != nil {
		return 0, errors.Join(err, waitErr)
	}
	if powerErr := session.PowerOnSIM(ctx, slot); powerErr != nil {
		return 0, errors.Join(err, fmt.Errorf("power on QMI UIM slot %d: %w", slot, powerErr))
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
	if err := uim.PowerOffSIM(ctx, 1); err != nil {
		return true, err
	}
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-time.After(3 * time.Second):
	}
	if err := uim.PowerOnSIM(ctx, 1); err != nil {
		return true, err
	}
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-time.After(time.Second):
	}
	return true, nil
}
