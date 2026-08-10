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

type qmiEUICCSession interface {
	GetICCID(context.Context) (string, error)
	OpenLogicalChannel(context.Context, uint8, []byte) (byte, error)
	CloseLogicalChannel(context.Context, uint8, byte) error
	SendAPDU(context.Context, uint8, byte, []byte) ([]byte, error)
	Close() error
}

type qmiEUICCSessionOpener func(context.Context, string) (qmiEUICCSession, error)

type qmiEUICCChannelBackend struct {
	session qmiEUICCSession
	slot    uint8
	channel byte
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
		session: session,
		slot:    qmiEUICCSlot,
		channel: channel,
	}}, nil
}

func (backend *qmiEUICCChannelBackend) exchange(
	ctx context.Context,
	apdu []byte,
) ([]byte, int, error) {
	if backend == nil || backend.session == nil || backend.channel == 0 || len(apdu) == 0 {
		return nil, 0, errors.New("esim: invalid QMI-UIM eUICC channel")
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
