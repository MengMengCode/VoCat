package vowifi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// NativeQMIController is implemented by device.Manager. It exposes only the
// QMI UIM/DMS/NAS primitives needed by VoWiFi and keeps transport ownership in
// the device layer.
type NativeQMIController interface {
	ReadNativeQMIIdentity(context.Context, string) (iccid, imsi, imei, mcc, mnc string, err error)
	ProbeNativeQMIApplication(context.Context, string, string) ([]byte, string, error)
	AuthenticateNativeQMI(context.Context, string, []byte, []byte) ([]byte, error)
	NativeQMIRadioSnapshot(context.Context, string) (mode int, psAttached bool, err error)
	StopNativeQMICellularData(context.Context, string) error
	SetNativeQMIRadioOff(context.Context, string, bool) error
}

type NativeQMIAdapter struct {
	controller         NativeQMIController
	pureAirplanePolicy func(string) bool
	mu                 sync.Mutex
	bindings           map[string]nativeQMIBinding
}

type nativeQMIBinding struct {
	deviceID    string
	iccid       string
	imsi        string
	aid         []byte
	application string
}

var _ SIMIdentityReader = (*NativeQMIAdapter)(nil)
var _ PreferredAKAProvider = (*NativeQMIAdapter)(nil)
var _ SMSCenterReader = (*NativeQMIAdapter)(nil)
var _ RadioController = (*NativeQMIAdapter)(nil)

func NewNativeQMIAdapter(controller NativeQMIController, purePolicy func(string) bool) (*NativeQMIAdapter, error) {
	if controller == nil {
		return nil, errors.New("vocat: native QMI controller is required")
	}
	return &NativeQMIAdapter{controller: controller, pureAirplanePolicy: purePolicy, bindings: make(map[string]nativeQMIBinding)}, nil
}

func (adapter *NativeQMIAdapter) ReadIdentity(ctx context.Context, deviceID string) (SIMIdentity, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return SIMIdentity{}, errors.New("vocat: native QMI device ID is required")
	}
	iccid, imsi, imei, mcc, mnc, err := adapter.controller.ReadNativeQMIIdentity(ctx, deviceID)
	if err != nil {
		return SIMIdentity{}, err
	}
	identity := applyAssignedCarrierRoute(SIMIdentity{ICCID: strings.TrimSpace(iccid), IMSI: strings.TrimSpace(imsi), IMEI: strings.TrimSpace(imei), HomeMCC: strings.TrimSpace(mcc), HomeMNC: strings.TrimSpace(mnc)})
	if reader, ok := adapter.controller.(SIMMetadataReader); ok {
		if metadata, metadataErr := reader.ReadSIMMetadata(ctx, deviceID); metadataErr == nil {
			identity.SPN = strings.TrimSpace(metadata.SPN)
			identity.GID1 = strings.TrimSpace(metadata.GID1)
			identity.GID2 = strings.TrimSpace(metadata.GID2)
			identity = applyAssignedCarrierRoute(identity)
		}
	}
	if err := identity.validate(); err != nil {
		return SIMIdentity{}, err
	}
	adapter.mu.Lock()
	adapter.bindings[identity.ICCID] = nativeQMIBinding{deviceID: deviceID, iccid: identity.ICCID, imsi: identity.IMSI}
	adapter.mu.Unlock()
	return identity, nil
}

func (adapter *NativeQMIAdapter) ReadSMSCenter(ctx context.Context, deviceID string) (string, error) {
	reader, ok := adapter.controller.(SMSCenterReader)
	if !ok {
		return "", errors.New("vocat: native QMI controller does not expose an SMS service-centre reader")
	}
	return reader.ReadSMSCenter(ctx, deviceID)
}

func (adapter *NativeQMIAdapter) binding(identity SIMIdentity) (nativeQMIBinding, error) {
	adapter.mu.Lock()
	binding, ok := adapter.bindings[strings.TrimSpace(identity.ICCID)]
	adapter.mu.Unlock()
	if !ok {
		return nativeQMIBinding{}, errors.New("vocat: native QMI SIM identity is not bound to a device")
	}
	if binding.imsi != strings.TrimSpace(identity.IMSI) {
		return nativeQMIBinding{}, ErrEC20IdentityChanged
	}
	return binding, nil
}

func (adapter *NativeQMIAdapter) verify(ctx context.Context, binding nativeQMIBinding) error {
	iccid, imsi, _, _, _, err := adapter.controller.ReadNativeQMIIdentity(ctx, binding.deviceID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(iccid) != binding.iccid || strings.TrimSpace(imsi) != binding.imsi {
		return ErrEC20IdentityChanged
	}
	return nil
}

func (adapter *NativeQMIAdapter) CheckReady(ctx context.Context, identity SIMIdentity) (AKAEvidence, error) {
	binding, err := adapter.binding(identity)
	if err != nil {
		return AKAEvidence{}, err
	}
	if err := adapter.verify(ctx, binding); err != nil {
		return AKAEvidence{}, err
	}
	aid, application, err := adapter.controller.ProbeNativeQMIApplication(ctx, binding.deviceID, "")
	if err != nil {
		return AKAEvidence{}, fmt.Errorf("%w: %v", ErrEC20ApplicationAbsent, err)
	}
	binding.aid, binding.application = append([]byte(nil), aid...), application
	adapter.mu.Lock()
	adapter.bindings[binding.iccid] = binding
	adapter.mu.Unlock()
	return AKAEvidence{Ready: true, Application: application}, nil
}

func (adapter *NativeQMIAdapter) Authenticate(ctx context.Context, identity SIMIdentity, challenge AKAChallenge) (AKAResult, error) {
	return adapter.AuthenticateWithPreference(ctx, identity, challenge, "")
}

func (adapter *NativeQMIAdapter) AuthenticateWithPreference(ctx context.Context, identity SIMIdentity, challenge AKAChallenge, preference string) (AKAResult, error) {
	binding, err := adapter.binding(identity)
	if err != nil {
		return AKAResult{}, err
	}
	strictISIM := strings.EqualFold(strings.TrimSpace(preference), "isim_strict")
	if len(binding.aid) == 0 || (strictISIM && binding.application != "ISIM") {
		aid, application, probeErr := adapter.controller.ProbeNativeQMIApplication(ctx, binding.deviceID, preference)
		if probeErr != nil {
			return AKAResult{}, fmt.Errorf("%w: %v", ErrEC20ApplicationAbsent, probeErr)
		}
		binding.aid, binding.application = append([]byte(nil), aid...), application
		adapter.mu.Lock()
		adapter.bindings[binding.iccid] = binding
		adapter.mu.Unlock()
	}
	if strictISIM && binding.application != "ISIM" {
		return AKAResult{}, ErrEC20ApplicationAbsent
	}
	if err := adapter.verify(ctx, binding); err != nil {
		return AKAResult{}, err
	}
	raw, err := adapter.controller.AuthenticateNativeQMI(ctx, binding.deviceID, binding.aid, buildUSIMAuthenticateAPDU(challenge))
	if err != nil {
		return AKAResult{}, fmt.Errorf("%w: %v", ErrEC20AKACommand, err)
	}
	return parseUSIMAuthenticateResponse(raw)
}

func (adapter *NativeQMIAdapter) Snapshot(ctx context.Context, deviceID string) (RadioSnapshot, error) {
	mode, attached, err := adapter.controller.NativeQMIRadioSnapshot(ctx, deviceID)
	if err != nil {
		return RadioSnapshot{}, err
	}
	pure := false
	if adapter.pureAirplanePolicy != nil {
		pure = adapter.pureAirplanePolicy(deviceID)
	}
	return RadioSnapshot{CellularDataEnabled: attached, OperatingMode: mode, PureAirplanePolicy: pure}, nil
}

func (adapter *NativeQMIAdapter) StopCellularData(ctx context.Context, deviceID string) error {
	return adapter.controller.StopNativeQMICellularData(ctx, deviceID)
}

func (adapter *NativeQMIAdapter) EnterVoWiFiRFOff(ctx context.Context, deviceID string) error {
	return adapter.controller.SetNativeQMIRadioOff(ctx, deviceID, true)
}

func (adapter *NativeQMIAdapter) Restore(ctx context.Context, deviceID string, snapshot RadioSnapshot) error {
	// A user VoWiFi policy is fail-closed. Otherwise restore whether the modem
	// was online, never a packet context that was detached for VoWiFi.
	off := snapshot.PureAirplanePolicy || snapshot.OperatingMode != 1
	return adapter.controller.SetNativeQMIRadioOff(ctx, deviceID, off)
}
