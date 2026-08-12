package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/qmiport"
)

type qmiRadioSession interface {
	GetOperatingMode(context.Context) (qmi.OperatingMode, error)
	SetOperatingMode(context.Context, qmi.OperatingMode) error
	Close() error
}

type qmiDMSICCIDSession interface {
	GetICCID(context.Context) (string, error)
}

type qmiNetworkSelectionSession interface {
	ResetNetworkSelection(context.Context) error
}

type qmiRadioSessionOpener func(context.Context, string) (qmiRadioSession, error)

type productionQMIRadioSession struct {
	client *qmi.Client
	dms    *qmi.DMSService
	nas    *qmi.NASService
	nasErr error
	lease  *qmiport.Lease
}

// openQMIRadioSession controls native WWAN radios through QMI DMS. OpenStick
// 410 firmware rejects AT+CFUN=1 even though the equivalent DMS online request
// is supported, so native WWAN devices must not fall back to the AT path.
func openQMIRadioSession(ctx context.Context, controlDevice string) (qmiRadioSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	openContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lease, err := qmiport.Acquire(openContext, controlDevice)
	if err != nil {
		return nil, err
	}
	opts := qmi.DefaultClientOptions()
	opts.UseProxy = true
	opts.Logf = func(qmi.ClientLogLevel, string, ...any) {}
	client, err := qmi.NewClientWithOptions(openContext, controlDevice, opts)
	if err != nil {
		lease.Release()
		return nil, err
	}
	dms, err := qmi.NewDMSServiceWithContext(openContext, client)
	if err != nil {
		_ = client.Close()
		lease.Release()
		return nil, err
	}
	// NAS is optional for the ordinary radio controls. Some firmware builds
	// expose DMS but reject NAS client allocation; keep the existing radio path
	// usable and report that limitation only to network-selection recovery.
	nas, nasErr := qmi.NewNASServiceWithContext(openContext, client)
	return &productionQMIRadioSession{
		client: client,
		dms:    dms,
		nas:    nas,
		nasErr: nasErr,
		lease:  lease,
	}, nil
}

func (session *productionQMIRadioSession) GetOperatingMode(ctx context.Context) (qmi.OperatingMode, error) {
	return session.dms.GetOperatingMode(ctx)
}

func (session *productionQMIRadioSession) GetICCID(ctx context.Context) (string, error) {
	return session.dms.GetICCID(ctx)
}

// ResetNetworkSelection clears a stale PLMN/band restriction after an eSIM
// profile switch. The modem reports the complete RF capability through DMS;
// copying that mask into NAS avoids carrying an old profile's narrow band
// lock (for example LTE 1/3/5) into a different country or roaming profile.
func (session *productionQMIRadioSession) ResetNetworkSelection(ctx context.Context) error {
	if session == nil || session.nas == nil {
		if session != nil && session.nasErr != nil {
			return session.nasErr
		}
		return errors.New("QMI NAS network-selection recovery is unavailable")
	}
	pref := qmi.SystemSelectionPreference{
		NetworkSelectionPreference:    qmi.NASNetworkSelectionAutomatic,
		HasNetworkSelectionPreference: true,
		ChangeDuration:                qmi.NASChangeDurationPermanent,
		HasChangeDuration:             true,
	}
	if capabilities, err := session.dms.GetBandCapabilities(ctx); err == nil && capabilities != nil && capabilities.HasLTEBandCapability {
		pref.LTEBandPreference = capabilities.LTEBandCapability
		pref.HasLTEBandPreference = true
	}
	if err := session.nas.SetSystemSelectionPreference(ctx, pref); err != nil {
		return fmt.Errorf("restore automatic QMI NAS selection: %w", err)
	}
	// OpenStick firmware accepts the selection update and starts acquisition,
	// but returns OperationNotSupported for the optional force-search command.
	// Treat that firmware quirk as success; the preference update itself is the
	// supported trigger and a later modem-online transition performs the scan.
	if err := session.nas.ForceNetworkSearch(ctx); err != nil {
		qmiErr := qmi.GetQMIError(err)
		if qmiErr == nil || qmiErr.ErrorCode != qmi.QMIErrOpDeviceUnsupported {
			return fmt.Errorf("force QMI NAS network search: %w", err)
		}
	}
	return nil
}

func (session *productionQMIRadioSession) SetOperatingMode(ctx context.Context, mode qmi.OperatingMode) error {
	return session.dms.SetOperatingMode(ctx, mode)
}

func (session *productionQMIRadioSession) Close() error {
	if session == nil {
		return nil
	}
	var closeErrors []error
	if session.dms != nil {
		closeErrors = append(closeErrors, session.dms.Close())
		session.dms = nil
	}
	if session.nas != nil {
		closeErrors = append(closeErrors, session.nas.Close())
		session.nas = nil
	}
	if session.client != nil {
		closeErrors = append(closeErrors, session.client.Close())
		session.client = nil
	}
	if session.lease != nil {
		session.lease.Release()
		session.lease = nil
	}
	return errors.Join(closeErrors...)
}

// resetNativeQMIModemForProfileSwitchLocked performs the native equivalent of
// AT+CFUN=1,1. The caller holds state.opMu. OpenStick 410 firmware accepts QMI
// DMS ModeReset but does not reliably reset its UIM/eUICC state through the AT
// command, which can leave an accepted EnableProfile pending indefinitely.
func (manager *Manager) resetNativeQMIModemForProfileSwitchLocked(
	ctx context.Context,
	id string,
	state *managedDevice,
) (bool, error) {
	controlDevice, native, err := manager.nativeQMIControl(id)
	if err != nil || !native {
		return native, err
	}
	if manager.qmiRadioOpener == nil {
		return true, errors.New("QMI DMS modem reset is unavailable")
	}
	if state.client != nil {
		_ = state.client.Close()
		state.client = nil
	}
	state.preFlightMode = nil
	manager.beginRecovery(id, state)

	openContext, cancelOpen := manager.withTimeout(ctx, manager.commandTimeout*5)
	session, err := manager.qmiRadioOpener(openContext, controlDevice)
	cancelOpen()
	if err != nil {
		return true, fmt.Errorf("open QMI DMS modem reset: %w", err)
	}
	resetContext, cancelReset := manager.withTimeout(ctx, manager.longTimeout)
	err = session.SetOperatingMode(resetContext, qmi.ModeReset)
	cancelReset()
	// ModeReset on the OpenStick 410 completes by leaving DMS in low-power.
	// Close the pre-reset client and reopen QMI before explicitly bringing the
	// radio online; otherwise the modem can remain in a permanent "searching"
	// state with no RF interface after an accepted EnableProfile.
	_ = session.Close()
	if err != nil {
		return true, fmt.Errorf("reset modem through QMI DMS: %w", err)
	}
	onlineSession, err := manager.reopenNativeQMIRadioOnline(ctx, controlDevice)
	if err != nil {
		return true, err
	}
	defer onlineSession.Close()
	if selection, ok := onlineSession.(qmiNetworkSelectionSession); ok {
		if err := selection.ResetNetworkSelection(ctx); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (manager *Manager) reopenNativeQMIRadioOnline(ctx context.Context, controlDevice string) (qmiRadioSession, error) {
	if manager.qmiRadioOpener == nil {
		return nil, errors.New("QMI DMS modem online recovery is unavailable")
	}
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), manager.longTimeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		session, err := manager.qmiRadioOpener(recoveryContext, controlDevice)
		if err == nil {
			setContext, cancelSet := context.WithTimeout(recoveryContext, manager.commandTimeout*2)
			setErr := session.SetOperatingMode(setContext, qmi.ModeOnline)
			cancelSet()
			if setErr == nil {
				readContext, cancelRead := context.WithTimeout(recoveryContext, manager.commandTimeout)
				mode, readErr := session.GetOperatingMode(readContext)
				cancelRead()
				if readErr == nil && mode == qmi.ModeOnline {
					return session, nil
				}
				if readErr != nil {
					lastErr = readErr
				} else {
					lastErr = fmt.Errorf("QMI modem remained in mode %d after online request", mode)
				}
			} else {
				lastErr = setErr
			}
			_ = session.Close()
		} else {
			lastErr = err
		}
		if attempt+1 < 8 {
			timer := time.NewTimer(750 * time.Millisecond)
			select {
			case <-timer.C:
			case <-recoveryContext.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil, fmt.Errorf("restore QMI modem online mode: %w", recoveryContext.Err())
			}
		}
	}
	return nil, fmt.Errorf("restore QMI modem online mode: %w", lastErr)
}

func (manager *Manager) setNativeQMIFlight(
	ctx context.Context,
	id string,
	state *managedDevice,
	enabled bool,
) (FlightResult, bool, error) {
	controlDevice, native, err := manager.nativeQMIControl(id)
	if err != nil {
		return FlightResult{}, true, err
	}
	if !native {
		return FlightResult{}, false, nil
	}
	if manager.qmiRadioOpener == nil {
		return FlightResult{}, true, errors.New("QMI DMS radio control is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	openContext, cancelOpen := manager.withTimeout(ctx, manager.commandTimeout*5)
	session, err := manager.qmiRadioOpener(openContext, controlDevice)
	cancelOpen()
	if err != nil {
		return FlightResult{}, true, fmt.Errorf("open QMI DMS radio control: %w", err)
	}
	defer session.Close()

	readContext, cancelRead := manager.withTimeout(ctx, manager.commandTimeout)
	previousQMI, err := session.GetOperatingMode(readContext)
	cancelRead()
	if err != nil {
		return FlightResult{}, true, fmt.Errorf("read QMI operating mode: %w", err)
	}
	previous := qmiModeAsCFUN(previousQMI)
	targetQMI := previousQMI
	if enabled {
		if !isQMIRadioOffMode(previousQMI) {
			targetQMI = qmi.ModeLowPower
		}
	} else if previousQMI != qmi.ModeOnline {
		targetQMI = qmi.ModeOnline
	}
	changed := targetQMI != previousQMI
	if changed {
		setContext, cancelSet := manager.withTimeout(ctx, manager.commandTimeout)
		err = session.SetOperatingMode(setContext, targetQMI)
		cancelSet()
		if err != nil {
			return FlightResult{
				PreviousMode: previous,
				CurrentMode:  previous,
				FlightMode:   isQMIRadioOffMode(previousQMI),
				RadioOff:     isQMIRadioOffMode(previousQMI),
			}, true, fmt.Errorf("set QMI operating mode: %w", err)
		}
	}
	currentQMI, err := manager.waitForQMIRadioState(ctx, session, enabled, targetQMI)
	if err != nil {
		currentRadioOff := isQMIRadioOffMode(currentQMI)
		return FlightResult{
			PreviousMode: previous,
			CurrentMode:  qmiModeAsCFUN(currentQMI),
			Changed:      changed,
			FlightMode:   currentRadioOff,
			RadioOff:     currentRadioOff,
		}, true, err
	}
	current := qmiModeAsCFUN(currentQMI)
	currentRadioOff := isQMIRadioOffMode(currentQMI)
	manager.updateSnapshotMode(id, state, current)
	return FlightResult{
		PreviousMode: previous,
		CurrentMode:  current,
		Changed:      changed,
		FlightMode:   currentRadioOff,
		RadioOff:     currentRadioOff,
	}, true, nil
}

func (manager *Manager) waitForQMIRadioState(
	ctx context.Context,
	session qmiRadioSession,
	radioOff bool,
	fallback qmi.OperatingMode,
) (qmi.OperatingMode, error) {
	verifyTimeout := manager.commandTimeout * 2
	if verifyTimeout < 5*time.Second {
		verifyTimeout = 5 * time.Second
	}
	verifyContext, cancel := manager.withTimeout(ctx, verifyTimeout)
	defer cancel()
	current := fallback
	var lastErr error
	for {
		mode, err := session.GetOperatingMode(verifyContext)
		if err == nil {
			current = mode
			lastErr = nil
			if qmiModeMatchesFlight(mode, radioOff) {
				return mode, nil
			}
		} else {
			lastErr = err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-verifyContext.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if lastErr != nil {
				return current, fmt.Errorf("verify QMI operating mode: %w", lastErr)
			}
			return current, fmt.Errorf(
				"QMI operating mode did not reach requested radio state (mode %d): %w",
				current,
				verifyContext.Err(),
			)
		case <-timer.C:
		}
	}
}

func qmiModeMatchesFlight(mode qmi.OperatingMode, radioOff bool) bool {
	if radioOff {
		return isQMIRadioOffMode(mode)
	}
	return mode == qmi.ModeOnline
}

func isQMIRadioOffMode(mode qmi.OperatingMode) bool {
	switch mode {
	case qmi.ModeLowPower, qmi.ModeOffline, qmi.ModeShutdown, qmi.ModePersistLow, qmi.ModeOnlyLowPower:
		return true
	default:
		return false
	}
}

// FlightResult and Snapshot historically expose AT+CFUN values. Preserve that
// API contract while sourcing the real radio state from QMI DMS.
func qmiModeAsCFUN(mode qmi.OperatingMode) int {
	switch mode {
	case qmi.ModeOnline:
		return 1
	case qmi.ModeLowPower, qmi.ModePersistLow:
		return 0
	case qmi.ModeOffline, qmi.ModeShutdown, qmi.ModeOnlyLowPower:
		return 7
	default:
		return 1
	}
}
