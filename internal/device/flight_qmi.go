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

type qmiRadioSessionOpener func(context.Context, string) (qmiRadioSession, error)

type productionQMIRadioSession struct {
	client *qmi.Client
	dms    *qmi.DMSService
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
	return &productionQMIRadioSession{client: client, dms: dms, lease: lease}, nil
}

func (session *productionQMIRadioSession) GetOperatingMode(ctx context.Context) (qmi.OperatingMode, error) {
	return session.dms.GetOperatingMode(ctx)
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
	manager.clearSnapshot(id, state)

	openContext, cancelOpen := manager.withTimeout(ctx, manager.commandTimeout*5)
	session, err := manager.qmiRadioOpener(openContext, controlDevice)
	cancelOpen()
	if err != nil {
		return true, fmt.Errorf("open QMI DMS modem reset: %w", err)
	}
	defer session.Close()
	resetContext, cancelReset := manager.withTimeout(ctx, manager.longTimeout)
	err = session.SetOperatingMode(resetContext, qmi.ModeReset)
	cancelReset()
	if err != nil {
		return true, fmt.Errorf("reset modem through QMI DMS: %w", err)
	}
	return true, nil
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
