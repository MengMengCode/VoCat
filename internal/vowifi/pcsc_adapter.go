package vowifi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"vocat/internal/pcsc"
)

type PCSCBindingResolver func(context.Context, string) (pcsc.Selector, string, error)

// PCSCAdapter uses a directly attached USB smart-card reader as the UICC for
// Wi-Fi Calling. It deliberately exposes no cellular-radio behaviour.
type PCSCAdapter struct {
	service  *pcsc.Service
	resolve  PCSCBindingResolver
	mu       sync.RWMutex
	bindings map[string]string
}

var (
	_ SIMIdentityReader = (*PCSCAdapter)(nil)
	_ SMSCenterReader   = (*PCSCAdapter)(nil)
	_ AKAProvider       = (*PCSCAdapter)(nil)
	_ RadioController   = (*PCSCAdapter)(nil)
)

func NewPCSCAdapter(service *pcsc.Service, resolver PCSCBindingResolver) (*PCSCAdapter, error) {
	if service == nil || resolver == nil {
		return nil, errors.New("vocat: PC/SC service and reader resolver are required")
	}
	return &PCSCAdapter{service: service, resolve: resolver, bindings: make(map[string]string)}, nil
}

func (adapter *PCSCAdapter) ReadIdentity(ctx context.Context, deviceID string) (SIMIdentity, error) {
	selector, pin, err := adapter.resolve(ctx, strings.TrimSpace(deviceID))
	if err != nil {
		return SIMIdentity{}, err
	}
	identity, err := adapter.service.ReadIdentity(ctx, selector, pin)
	if err != nil {
		return SIMIdentity{}, fmt.Errorf("read USB SIM identity: %w", err)
	}
	if len(identity.IMSI) < 5 {
		return SIMIdentity{}, errors.New("vocat: USB SIM reader returned an invalid IMSI")
	}
	adapter.mu.Lock()
	adapter.bindings[identity.ICCID] = strings.TrimSpace(deviceID)
	adapter.mu.Unlock()
	mncLength := identity.MNCLength
	if mncLength != 2 && mncLength != 3 {
		if mcc, mnc, ok := assignedHomePLMN(identity.IMSI); ok {
			return SIMIdentity{ICCID: identity.ICCID, IMSI: identity.IMSI, HomeMCC: mcc, HomeMNC: mnc, SMSC: identity.SMSC}, nil
		}
		return SIMIdentity{}, ErrEC20MNCUnavailable
	}
	if len(identity.IMSI) < 3+mncLength {
		return SIMIdentity{}, errors.New("vocat: USB SIM IMSI is shorter than its EF_AD home PLMN")
	}
	return SIMIdentity{
		ICCID: identity.ICCID, IMSI: identity.IMSI,
		HomeMCC: identity.IMSI[:3], HomeMNC: identity.IMSI[3 : 3+mncLength],
		SMSC: identity.SMSC,
	}, nil
}

func (adapter *PCSCAdapter) ReadSMSCenter(ctx context.Context, deviceID string) (string, error) {
	selector, pin, err := adapter.resolve(ctx, strings.TrimSpace(deviceID))
	if err != nil {
		return "", err
	}
	identity, err := adapter.service.ReadIdentity(ctx, selector, pin)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(identity.SMSC) == "" {
		return "", errors.New("vocat: USB SIM does not expose a service-centre address")
	}
	return identity.SMSC, nil
}

func (adapter *PCSCAdapter) CheckReady(ctx context.Context, identity SIMIdentity) (AKAEvidence, error) {
	selector, pin, err := adapter.resolve(ctx, adapter.deviceID(identity))
	if err != nil {
		return AKAEvidence{}, err
	}
	aid, err := adapter.service.CheckReady(ctx, selector, identity.ICCID, pin)
	if err != nil {
		return AKAEvidence{}, fmt.Errorf("check USB SIM AKA application: %w", err)
	}
	return AKAEvidence{Ready: true, Application: aid}, nil
}

func (adapter *PCSCAdapter) deviceID(identity SIMIdentity) string {
	adapter.mu.RLock()
	deviceID := adapter.bindings[identity.ICCID]
	adapter.mu.RUnlock()
	return deviceID
}

func (adapter *PCSCAdapter) Authenticate(ctx context.Context, identity SIMIdentity, challenge AKAChallenge) (AKAResult, error) {
	selector, pin, err := adapter.resolve(ctx, adapter.deviceID(identity))
	if err != nil {
		return AKAResult{}, err
	}
	result, err := adapter.service.Authenticate(ctx, selector, identity.ICCID, pin, pcsc.AKAChallenge(challenge))
	if err != nil {
		if errors.Is(err, pcsc.ErrAKARejected) {
			return AKAResult{}, errors.Join(ErrEC20AKAMACFailure, err)
		}
		return AKAResult{}, fmt.Errorf("authenticate with USB SIM: %w", err)
	}
	return AKAResult(result), nil
}

func (*PCSCAdapter) Snapshot(context.Context, string) (RadioSnapshot, error) {
	return RadioSnapshot{OperatingMode: 4, PureAirplanePolicy: true}, nil
}
func (*PCSCAdapter) StopCellularData(context.Context, string) error       { return nil }
func (*PCSCAdapter) EnterVoWiFiRFOff(context.Context, string) error       { return nil }
func (*PCSCAdapter) Restore(context.Context, string, RadioSnapshot) error { return nil }
