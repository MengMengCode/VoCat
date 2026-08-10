package vowifi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/qmiport"
)

const qmiUIMSIMAuthSlot uint8 = 1

var usimAID = []byte{0xa0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02}

// qmiUIMSession is deliberately narrower than qmi.UIMService so the AKA
// path can be tested without a modem and cannot accidentally change radio,
// network, profile, or SMS state.
type qmiUIMSession interface {
	GetICCID(context.Context) (string, error)
	GetUSIMAID(context.Context) ([]byte, error)
	OpenLogicalChannel(context.Context, uint8, []byte) (byte, error)
	CloseLogicalChannel(context.Context, uint8, byte) error
	SendAPDU(context.Context, uint8, byte, []byte) ([]byte, error)
	Close() error
}

type qmiUIMSessionOpener func(context.Context, string) (qmiUIMSession, error)

// QMIUIMAKAProvider performs USIM AKA over the Qualcomm QMI-UIM service. It
// is used by native OpenStick/410 devices whose WWAN AT firmware rejects
// AT+CSIM even though the same UICC is fully available through QMI.
type QMIUIMAKAProvider struct {
	controlDevice string
	openSession   qmiUIMSessionOpener
	mu            sync.Mutex
}

var _ AKAProvider = (*QMIUIMAKAProvider)(nil)

func NewQMIUIMAKAProvider(controlDevice string) (*QMIUIMAKAProvider, error) {
	return newQMIUIMAKAProvider(controlDevice, openQMIUIMSession)
}

func newQMIUIMAKAProvider(
	controlDevice string,
	opener qmiUIMSessionOpener,
) (*QMIUIMAKAProvider, error) {
	controlDevice = strings.TrimSpace(controlDevice)
	if controlDevice == "" {
		return nil, errors.New("vocat: QMI-UIM control device is required")
	}
	if opener == nil {
		return nil, errors.New("vocat: QMI-UIM session opener is required")
	}
	return &QMIUIMAKAProvider{
		controlDevice: controlDevice,
		openSession:   opener,
	}, nil
}

func (provider *QMIUIMAKAProvider) CheckReady(
	ctx context.Context,
	identity SIMIdentity,
) (AKAEvidence, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()

	session, err := provider.session(ctx)
	if err != nil {
		return AKAEvidence{}, err
	}
	defer session.Close()
	if err := verifyQMIUIMIdentity(ctx, session, identity); err != nil {
		return AKAEvidence{}, err
	}
	channel, _, err := openQMIUSIMChannel(ctx, session)
	if err != nil {
		return AKAEvidence{}, errors.Join(ErrEC20ApplicationAbsent, err)
	}
	if err := closeQMIUIMChannel(session, channel); err != nil {
		return AKAEvidence{}, err
	}
	return AKAEvidence{Ready: true, Application: "USIM"}, nil
}

func (provider *QMIUIMAKAProvider) Authenticate(
	ctx context.Context,
	identity SIMIdentity,
	challenge AKAChallenge,
) (AKAResult, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()

	session, err := provider.session(ctx)
	if err != nil {
		return AKAResult{}, err
	}
	defer session.Close()
	if err := verifyQMIUIMIdentity(ctx, session, identity); err != nil {
		return AKAResult{}, err
	}
	channel, _, err := openQMIUSIMChannel(ctx, session)
	if err != nil {
		return AKAResult{}, errors.Join(ErrEC20ApplicationAbsent, err)
	}
	raw, commandErr := transmitQMIUIMAPDU(
		ctx,
		session,
		channel,
		buildUSIMAuthenticateAPDU(challenge),
	)
	closeErr := closeQMIUIMChannel(session, channel)
	if commandErr != nil {
		if closeErr != nil {
			return AKAResult{}, errors.Join(commandErr, closeErr)
		}
		return AKAResult{}, commandErr
	}
	if closeErr != nil {
		return AKAResult{}, closeErr
	}
	return parseUSIMAuthenticateResponse(raw)
}

func (provider *QMIUIMAKAProvider) session(ctx context.Context) (qmiUIMSession, error) {
	if provider == nil || provider.openSession == nil {
		return nil, errors.New("vocat: QMI-UIM provider is not configured")
	}
	session, err := provider.openSession(ctx, provider.controlDevice)
	if err != nil {
		return nil, fmt.Errorf("vocat: open QMI-UIM session: %w", err)
	}
	return session, nil
}

func verifyQMIUIMIdentity(
	ctx context.Context,
	session qmiUIMSession,
	identity SIMIdentity,
) error {
	want := strings.TrimSpace(identity.ICCID)
	if want == "" {
		return ErrEC20IdentityChanged
	}
	got, err := session.GetICCID(ctx)
	if err != nil {
		return fmt.Errorf("vocat: read QMI-UIM ICCID: %w", err)
	}
	if strings.TrimSpace(got) != want {
		return ErrEC20IdentityChanged
	}
	return nil
}

func openQMIUSIMChannel(
	ctx context.Context,
	session qmiUIMSession,
) (byte, []byte, error) {
	resolved, resolveErr := session.GetUSIMAID(ctx)
	candidates := make([][]byte, 0, 2)
	if resolveErr == nil && bytes.HasPrefix(resolved, usimAID) {
		candidates = append(candidates, append([]byte(nil), resolved...))
	}
	if len(candidates) == 0 || !bytes.Equal(candidates[0], usimAID) {
		candidates = append(candidates, append([]byte(nil), usimAID...))
	}
	var errs []error
	if resolveErr != nil {
		errs = append(errs, fmt.Errorf("resolve USIM AID: %w", resolveErr))
	}
	for _, aid := range candidates {
		channel, err := session.OpenLogicalChannel(ctx, qmiUIMSIMAuthSlot, aid)
		if err == nil {
			return channel, aid, nil
		}
		errs = append(errs, err)
	}
	return 0, nil, fmt.Errorf("vocat: open QMI-UIM USIM channel: %w", errors.Join(errs...))
}

func transmitQMIUIMAPDU(
	ctx context.Context,
	session qmiUIMSession,
	channel byte,
	apdu []byte,
) ([]byte, error) {
	if channel == 0 || len(apdu) == 0 || len(apdu) > 261 {
		return nil, ErrEC20AKACommand
	}
	var collected []byte
	current := append([]byte(nil), apdu...)
	for exchange := 0; exchange < 4; exchange++ {
		raw, err := session.SendAPDU(
			ctx,
			qmiUIMSIMAuthSlot,
			channel,
			current,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: QMI-UIM exchange failed", ErrEC20AKACommand)
		}
		body, status, err := splitAPDUStatus(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEC20AKAResponse, err)
		}
		collected = append(collected, body...)
		sw1 := byte(status >> 8)
		if sw1 != 0x61 && sw1 != 0x9f {
			collected = append(collected, byte(status>>8), byte(status))
			return collected, nil
		}
		current = []byte{0x00, 0xc0, 0x00, 0x00, byte(status)}
	}
	return nil, fmt.Errorf(
		"%w: QMI-UIM response chaining exceeded limit",
		ErrEC20AKAResponse,
	)
}

func closeQMIUIMChannel(session qmiUIMSession, channel byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), channelCleanupTimeout)
	defer cancel()
	if err := session.CloseLogicalChannel(ctx, qmiUIMSIMAuthSlot, channel); err != nil {
		return fmt.Errorf("vocat: close QMI-UIM logical channel: %w", err)
	}
	return nil
}

type productionQMIUIMSession struct {
	client *qmi.Client
	uim    *qmi.UIMService
	lease  *qmiport.Lease
}

func openQMIUIMSession(
	ctx context.Context,
	controlDevice string,
) (qmiUIMSession, error) {
	openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lease, err := qmiport.Acquire(openCtx, controlDevice)
	if err != nil {
		return nil, err
	}
	opts := qmi.DefaultClientOptions()
	// qmicli and eUICC management share this native control port. qmi-proxy
	// provides QMUX fan-out while qmiport prevents the old 410 WWAN driver from
	// removing DATA5_CNTL between short-lived AKA requests.
	opts.UseProxy = true
	// QMI diagnostics must never include authentication APDUs or key material.
	opts.Logf = func(qmi.ClientLogLevel, string, ...any) {}
	client, err := qmi.NewClientWithOptions(openCtx, controlDevice, opts)
	if err != nil {
		lease.Release()
		return nil, err
	}
	uim, err := qmi.NewUIMServiceWithContext(openCtx, client)
	if err != nil {
		_ = client.Close()
		lease.Release()
		return nil, err
	}
	return &productionQMIUIMSession{client: client, uim: uim, lease: lease}, nil
}

func (session *productionQMIUIMSession) GetICCID(ctx context.Context) (string, error) {
	return session.uim.GetICCID(ctx)
}

func (session *productionQMIUIMSession) GetUSIMAID(ctx context.Context) ([]byte, error) {
	return session.uim.GetUSIMAID(ctx)
}

func (session *productionQMIUIMSession) OpenLogicalChannel(
	ctx context.Context,
	slot uint8,
	aid []byte,
) (byte, error) {
	return session.uim.OpenLogicalChannel(ctx, slot, aid)
}

func (session *productionQMIUIMSession) CloseLogicalChannel(
	ctx context.Context,
	slot uint8,
	channel byte,
) error {
	return session.uim.CloseLogicalChannel(ctx, slot, channel)
}

func (session *productionQMIUIMSession) SendAPDU(
	ctx context.Context,
	slot uint8,
	channel byte,
	apdu []byte,
) ([]byte, error) {
	return session.uim.SendAPDU(ctx, slot, channel, apdu)
}

func (session *productionQMIUIMSession) Close() error {
	if session == nil {
		return nil
	}
	var errs []error
	if session.uim != nil {
		errs = append(errs, session.uim.Close())
	}
	if session.client != nil {
		errs = append(errs, session.client.Close())
	}
	if session.lease != nil {
		session.lease.Release()
		session.lease = nil
	}
	return errors.Join(errs...)
}
