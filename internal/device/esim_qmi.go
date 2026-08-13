package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/qmiport"
)

const qmiEUICCSlot uint8 = 1

const qmiErrorInjectTimeout uint16 = 0x0050

type qmiEUICCSession interface {
	GetICCID(context.Context) (string, error)
	OpenLogicalChannel(context.Context, uint8, []byte) (byte, error)
	CloseLogicalChannel(context.Context, uint8, byte) error
	SendAPDU(context.Context, uint8, byte, []byte) ([]byte, error)
	Reset(context.Context) error
	PowerOffSIM(context.Context, uint8) error
	PowerOnSIM(context.Context, uint8) error
	Close() error
}

type qmiEUICCSessionOpener func(context.Context, string) (qmiEUICCSession, error)

type qmiEUICCChannelBackend struct {
	manager       *Manager
	deviceID      string
	controlDevice string
	session       qmiEUICCSession
	slot          uint8
	channel       byte
}

// nativeQMIControl selects QMI-UIM only for Linux WWAN-class devices such as
// the OpenStick 410. USB EC20/EC25 devices retain their proven AT+CSIM path.
func (manager *Manager) nativeQMIControl(id string) (string, bool, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return "", false, err
	}
	candidate := manager.candidateFor(state)
	controlDevice := strings.TrimSpace(candidate.QMIControl)
	deviceID := strings.TrimSpace(candidate.ID)
	base := filepath.Base(controlDevice)
	if controlDevice == "" || deviceID == "" ||
		!strings.HasPrefix(deviceID, "wwan") ||
		!strings.HasPrefix(base, deviceID+"qmi") {
		return "", false, nil
	}
	return controlDevice, true, nil
}

func (manager *Manager) openQMIEuiccOnceAID(
	ctx context.Context,
	deviceID string,
	controlDevice string,
	aidHex string,
) (*euiccChannel, error) {
	aidHex = strings.ToUpper(strings.TrimSpace(aidHex))
	aid, err := hex.DecodeString(aidHex)
	if err != nil || len(aid) == 0 || len(aid) > 255 {
		return nil, fmt.Errorf("esim: invalid ISD-R AID %q", aidHex)
	}
	if manager.qmiEUICCOpener == nil {
		return nil, errors.New("esim: QMI-UIM eUICC transport is unavailable")
	}

	openContext, cancelOpen := context.WithTimeout(ctx, csimAPDUTimeout)
	defer cancelOpen()
	session, err := manager.qmiEUICCOpener(openContext, controlDevice)
	if err != nil {
		return nil, fmt.Errorf("esim: open QMI-UIM session: %w", err)
	}
	channel, err := session.OpenLogicalChannel(openContext, qmiEUICCSlot, aid)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("esim: open QMI-UIM ISD-R channel: %w", err)
	}
	return &euiccChannel{backend: &qmiEUICCChannelBackend{
		manager:       manager,
		deviceID:      strings.TrimSpace(deviceID),
		controlDevice: strings.TrimSpace(controlDevice),
		session:       session,
		slot:          qmiEUICCSlot,
		channel:       channel,
	}}, nil
}

func isQMIUIMInjectTimeout(err error) bool {
	qmiErr := qmi.GetQMIError(err)
	return qmiErr != nil &&
		qmiErr.Service == qmi.ServiceUIM &&
		qmiErr.MessageID == qmi.UIMOpenLogicalChannel &&
		qmiErr.ErrorCode == qmiErrorInjectTimeout
}

// recoverQMIEuiccChannel clears a wedged UIM command and power-cycles the card.
// OpenStick 410 can leave ordinary USIM reads working while every ISD-R channel
// open returns INJECT_TIMEOUT after EnableProfile. A UIM reset plus slot power
// cycle is narrower and faster than rebooting Linux, while still forcing the
// baseband to discard the stale eUICC channel and subscriber cache.
func (manager *Manager) recoverQMIEuiccChannel(ctx context.Context, id string) error {
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return err
	}
	return manager.recoverQMIEuiccChannelLocked(ctx, id, state)
}

// recoverQMIEuiccChannelLocked is the same UIM/eUICC recovery operation for
// callers that already own state.opMu (for example the native QMI SMS scan).
// Keeping the lock held across the power cycle prevents a concurrent profile
// switch or identity read from racing the card reset.
func (manager *Manager) recoverQMIEuiccChannelLocked(
	ctx context.Context,
	id string,
	state *managedDevice,
) error {
	if state == nil {
		return errors.New("esim: missing device state for QMI-UIM recovery")
	}
	controlDevice := strings.TrimSpace(manager.candidateFor(state).QMIControl)
	if controlDevice == "" || manager.qmiEUICCOpener == nil {
		return errors.New("esim: QMI-UIM recovery is unavailable")
	}

	openContext, cancelOpen := manager.withTimeout(ctx, manager.commandTimeout*5)
	session, err := manager.qmiEUICCOpener(openContext, controlDevice)
	cancelOpen()
	if err != nil {
		return fmt.Errorf("esim: open QMI-UIM recovery session: %w", err)
	}
	defer session.Close()

	resetContext, cancelReset := manager.withTimeout(ctx, manager.commandTimeout*2)
	resetErr := session.Reset(resetContext)
	cancelReset()
	offContext, cancelOff := manager.withTimeout(ctx, manager.commandTimeout*2)
	offErr := session.PowerOffSIM(offContext, qmiEUICCSlot)
	cancelOff()
	if offErr != nil {
		return fmt.Errorf("esim: QMI-UIM recovery could not power off slot %d: %w", qmiEUICCSlot, errors.Join(resetErr, offErr))
	}

	timer := time.NewTimer(250 * time.Millisecond)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
	}
	// Power-on must run even if the HTTP caller disconnected after power-off.
	powerOnContext, cancelPowerOn := context.WithTimeout(context.WithoutCancel(ctx), manager.commandTimeout*2)
	powerOnErr := session.PowerOnSIM(powerOnContext, qmiEUICCSlot)
	cancelPowerOn()
	if powerOnErr != nil {
		return fmt.Errorf("esim: QMI-UIM recovery could not power on slot %d: %w", qmiEUICCSlot, powerOnErr)
	}
	// UIM power-cycling does not make the AT control plane disappear. Preserve
	// the last responsive modem snapshot; a profile switch will separately mark
	// the full modem reset as recovering and refresh its subscriber identity.

	readyContext, cancelReady := manager.withTimeout(context.WithoutCancel(ctx), manager.longTimeout)
	defer cancelReady()
	var lastErr error
	for {
		iccid, readErr := session.GetICCID(readyContext)
		if readErr == nil && strings.TrimSpace(iccid) != "" {
			return nil
		}
		if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = errors.New("QMI-UIM returned an empty ICCID")
		}
		wait := time.NewTimer(time.Second)
		select {
		case <-readyContext.Done():
			if !wait.Stop() {
				<-wait.C
			}
			return fmt.Errorf("esim: QMI-UIM did not become ready after recovery: %w", errors.Join(resetErr, lastErr, readyContext.Err()))
		case <-wait.C:
		}
	}
}

func (backend *qmiEUICCChannelBackend) exchange(
	ctx context.Context,
	apdu []byte,
) ([]byte, int, error) {
	if backend == nil || backend.manager == nil || backend.session == nil ||
		backend.deviceID == "" || backend.channel == 0 || len(apdu) == 0 {
		return nil, 0, errors.New("esim: invalid QMI-UIM eUICC channel")
	}
	state, err := backend.manager.lookup(backend.deviceID)
	if err != nil {
		return nil, 0, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := backend.manager.validateActive(backend.deviceID, state); err != nil {
		return nil, 0, err
	}
	if current := strings.TrimSpace(backend.manager.candidateFor(state).QMIControl); current != backend.controlDevice {
		return nil, 0, errors.New("esim: QMI-UIM control device changed while the channel was open")
	}
	response, err := backend.session.SendAPDU(
		ctx,
		backend.slot,
		backend.channel,
		append([]byte(nil), apdu...),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("esim: QMI-UIM APDU exchange failed: %w", err)
	}
	if len(response) < 2 {
		return nil, 0, errors.New("esim: QMI-UIM returned a short APDU response")
	}
	status := int(response[len(response)-2])<<8 | int(response[len(response)-1])
	return append([]byte(nil), response[:len(response)-2]...), status, nil
}

func (backend *qmiEUICCChannelBackend) close(ctx context.Context) error {
	if backend == nil || backend.session == nil {
		return nil
	}
	closeContext, cancel := context.WithTimeout(ctx, csimAPDUTimeout)
	defer cancel()
	logicalErr := backend.session.CloseLogicalChannel(
		closeContext,
		backend.slot,
		backend.channel,
	)
	sessionErr := backend.session.Close()
	backend.session = nil
	return errors.Join(logicalErr, sessionErr)
}

type productionDeviceQMIUIMSession struct {
	client *qmi.Client
	uim    *qmi.UIMService
	lease  *qmiport.Lease
}

func openQMIEUICCSession(
	ctx context.Context,
	controlDevice string,
) (qmiEUICCSession, error) {
	openContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lease, err := qmiport.Acquire(openContext, controlDevice)
	if err != nil {
		return nil, err
	}
	opts := qmi.DefaultClientOptions()
	// The host also uses qmicli for registration snapshots. Route every active
	// QMI client through qmi-proxy while qmiport keeps the kernel WWAN endpoint
	// alive, so responses cannot be consumed by competing raw readers.
	opts.UseProxy = true
	// ES10 APDUs can contain profile metadata and download material. Never emit
	// raw QMI frames or APDU bodies through the library logger.
	opts.Logf = func(qmi.ClientLogLevel, string, ...any) {}
	client, err := qmi.NewClientWithOptions(openContext, controlDevice, opts)
	if err != nil {
		lease.Release()
		return nil, err
	}
	uim, err := qmi.NewUIMServiceWithContext(openContext, client)
	if err != nil {
		_ = client.Close()
		lease.Release()
		return nil, err
	}
	return &productionDeviceQMIUIMSession{client: client, uim: uim, lease: lease}, nil
}

func (session *productionDeviceQMIUIMSession) OpenLogicalChannel(
	ctx context.Context,
	slot uint8,
	aid []byte,
) (byte, error) {
	return session.uim.OpenLogicalChannel(ctx, slot, aid)
}

func (session *productionDeviceQMIUIMSession) GetICCID(ctx context.Context) (string, error) {
	return session.uim.GetICCID(ctx)
}

func (session *productionDeviceQMIUIMSession) CloseLogicalChannel(
	ctx context.Context,
	slot uint8,
	channel byte,
) error {
	return session.uim.CloseLogicalChannel(ctx, slot, channel)
}

func (session *productionDeviceQMIUIMSession) SendAPDU(
	ctx context.Context,
	slot uint8,
	channel byte,
	apdu []byte,
) ([]byte, error) {
	return session.uim.SendAPDU(ctx, slot, channel, apdu)
}

func (session *productionDeviceQMIUIMSession) Reset(ctx context.Context) error {
	return session.uim.Reset(ctx)
}

func (session *productionDeviceQMIUIMSession) PowerOffSIM(ctx context.Context, slot uint8) error {
	return session.uim.PowerOffSIM(ctx, slot)
}

func (session *productionDeviceQMIUIMSession) PowerOnSIM(ctx context.Context, slot uint8) error {
	return session.uim.PowerOnSIM(ctx, slot)
}

func (session *productionDeviceQMIUIMSession) Close() error {
	if session == nil {
		return nil
	}
	var closeErrors []error
	if session.uim != nil {
		closeErrors = append(closeErrors, session.uim.Close())
	}
	if session.client != nil {
		closeErrors = append(closeErrors, session.client.Close())
	}
	if session.lease != nil {
		session.lease.Release()
		session.lease = nil
	}
	return errors.Join(closeErrors...)
}
