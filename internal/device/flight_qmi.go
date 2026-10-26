package device

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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

type nativeQMIICCIDSession interface {
	GetICCID(context.Context) (string, error)
}

type nativeQMIIMEISession interface {
	GetIMEI(context.Context) (string, error)
}

type nativeQMIEuiccSession interface {
	qmiRadioSession
	OpenLogicalChannel(context.Context, uint8, []byte) (byte, error)
	CloseLogicalChannel(context.Context, uint8, uint8) error
	SendAPDU(context.Context, uint8, uint8, []byte) ([]byte, error)
}

// nativeQMIRefreshSession is implemented by production QMI sessions that can
// participate in the modem's UIM REFRESH state machine.  Keep it separate from
// nativeQMIEuiccSession so transcript fakes and older QMI implementations can
// continue to use the APDU transport without pretending to handle indications.
type nativeQMIRefreshSession interface {
	RegisterUIMRefresh(context.Context) error
	CompleteUIMRefresh(context.Context) error
	AcknowledgeUIMRefresh(context.Context) error
}

type nativeQMIUIMResetSession interface {
	ResetUIM(context.Context) error
}

// nativeQMIProvisioningSession is intentionally optional.  A few older UIM
// implementations expose SIM power control but do not implement the
// provisioning-session command.  Those modems retain the existing profile
// switch recovery behaviour.
type nativeQMIProvisioningSession interface {
	GetUSIMAID(context.Context) ([]byte, error)
	RebindPrimaryGWProvisioning(context.Context, uint8, []byte) error
}

type nativeQMIVoWiFiSession interface {
	qmiRadioSession
	GetICCID(context.Context) (string, error)
	GetIMEI(context.Context) (string, error)
	GetIMSI(context.Context) (string, error)
	GetNativeMCCMNC(context.Context) (string, string, error)
	GetUSIMAID(context.Context) ([]byte, error)
	GetISIMAID(context.Context) ([]byte, error)
	GetServingSystem(context.Context) (*qmi.ServingSystem, error)
	AttachDetach(context.Context, bool) error
	OpenLogicalChannel(context.Context, uint8, []byte) (byte, error)
	CloseLogicalChannel(context.Context, uint8, uint8) error
	SendAPDU(context.Context, uint8, uint8, []byte) ([]byte, error)
	PowerOffSIM(context.Context, uint8) error
	PowerOnSIM(context.Context, uint8) error
}

// nativeQMIControl identifies the QMI control node exposed by native WWAN
// devices. USB serial modems may also advertise a control path, but only the
// wwanN/qmiN pairing is safe to operate through the native QMI path.
func (manager *Manager) nativeQMIControl(id string) (string, bool, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return "", false, err
	}
	candidate := manager.candidateFor(state)
	controlDevice := strings.TrimSpace(candidate.QMIControl)
	deviceID := strings.TrimSpace(candidate.ID)
	if !nativeQMIControlMatches(deviceID, controlDevice) {
		return "", false, nil
	}
	return controlDevice, true, nil
}

type productionQMIRadioSession struct {
	client *qmi.Client
	dms    *qmi.DMSService
	nas    *qmi.NASService
	nasErr error
	catID  uint8
	uimMu  sync.Mutex
	uim    *qmi.UIMService
	lease  *qmiport.Lease
}

// The native WWAN path uses the same QMI NAS client for radio wake-up,
// operator selection, and registration.  Keep these methods optional on the
// qmiRadioSession interface so the older transcript-backed tests and AT-only
// devices do not need to grow a fake NAS implementation.
func (session *productionQMIRadioSession) nasService() (*qmi.NASService, error) {
	if session == nil {
		return nil, errors.New("QMI NAS session is unavailable")
	}
	if session.nas == nil {
		if session.nasErr != nil {
			return nil, session.nasErr
		}
		return nil, errors.New("QMI NAS session is unavailable")
	}
	return session.nas, nil
}

func (session *productionQMIRadioSession) GetServingSystem(ctx context.Context) (*qmi.ServingSystem, error) {
	nas, err := session.nasService()
	if err != nil {
		return nil, err
	}
	return nas.GetServingSystem(ctx)
}

func (session *productionQMIRadioSession) GetSystemSelectionPreference(ctx context.Context) (*qmi.SystemSelectionPreference, error) {
	nas, err := session.nasService()
	if err != nil {
		return nil, err
	}
	return nas.GetSystemSelectionPreference(ctx)
}

func (session *productionQMIRadioSession) SetSystemSelectionPreference(ctx context.Context, pref qmi.SystemSelectionPreference) error {
	nas, err := session.nasService()
	if err != nil {
		return err
	}
	return nas.SetSystemSelectionPreference(ctx, pref)
}

func (session *productionQMIRadioSession) InitiateNetworkRegister(ctx context.Context, req qmi.NASInitiateNetworkRegisterRequest) error {
	nas, err := session.nasService()
	if err != nil {
		return err
	}
	return nas.InitiateNetworkRegister(ctx, req)
}

func (session *productionQMIRadioSession) ForceNetworkSearch(ctx context.Context) error {
	nas, err := session.nasService()
	if err != nil {
		return err
	}
	return nas.ForceNetworkSearch(ctx)
}

func (session *productionQMIRadioSession) AttachDetach(ctx context.Context, attached bool) error {
	nas, err := session.nasService()
	if err != nil {
		return err
	}
	return nas.AttachDetach(ctx, attached)
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
	// NAS is optional for ordinary radio controls. Some firmware exposes DMS
	// but rejects NAS client allocation; keep radio control usable and report
	// that limitation only to native registration/RF queries.
	nas, nasErr := qmi.NewNASServiceWithContext(openContext, client)
	return &productionQMIRadioSession{
		client: client,
		dms:    dms,
		nas:    nas,
		nasErr: nasErr,
		lease:  lease,
	}, nil
}

func (session *productionQMIRadioSession) GetICCID(ctx context.Context) (string, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return "", err
	}
	return uim.GetICCID(ctx)
}

func (session *productionQMIRadioSession) GetCardStatusDetails(ctx context.Context) (*qmi.CardStatusDetails, qmi.SIMStatus, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return nil, qmi.SIMNotReady, err
	}
	return uim.GetCardStatusDetails(ctx)
}

func (session *productionQMIRadioSession) GetIMSI(ctx context.Context) (string, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return "", err
	}
	return uim.GetIMSI(ctx)
}

func (session *productionQMIRadioSession) GetNativeMCCMNC(ctx context.Context) (string, string, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return "", "", err
	}
	return uim.GetNativeMCCMNC(ctx)
}

func (session *productionQMIRadioSession) GetUSIMAID(ctx context.Context) ([]byte, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return nil, err
	}
	return uim.GetUSIMAID(ctx)
}

func (session *productionQMIRadioSession) GetISIMAID(ctx context.Context) ([]byte, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return nil, err
	}
	return uim.GetISIMAID(ctx)
}

func (session *productionQMIRadioSession) PowerOffSIM(ctx context.Context, slot uint8) error {
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	return uim.PowerOffSIM(ctx, slot)
}

func (session *productionQMIRadioSession) PowerOnSIM(ctx context.Context, slot uint8) error {
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	return uim.PowerOnSIM(ctx, slot)
}

func (session *productionQMIRadioSession) ResetUIM(ctx context.Context) error {
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	return uim.Reset(ctx)
}

// RebindPrimaryGWProvisioning restores the primary GSM/LTE provisioning
// session after a physical eUICC profile switch.  Some Qualcomm MHI firmware
// clears this volatile binding when the SIM is power-cycled, despite the card
// and its newly-enabled USIM application being ready.
func (session *productionQMIRadioSession) RebindPrimaryGWProvisioning(ctx context.Context, slot uint8, aid []byte) error {
	if len(aid) == 0 {
		return errors.New("USIM AID is empty")
	}
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	return uim.ChangeProvisioningSession(ctx, qmi.UIMChangeProvisioningSessionRequest{
		SessionType:           qmi.UIMSessionTypePrimaryGWProvisioning,
		Activate:              true,
		Slot:                  &slot,
		ApplicationIdentifier: append([]byte(nil), aid...),
	})
}

func (session *productionQMIRadioSession) uimService(ctx context.Context) (*qmi.UIMService, error) {
	if session == nil || session.client == nil {
		return nil, errors.New("QMI UIM session is unavailable")
	}
	session.uimMu.Lock()
	defer session.uimMu.Unlock()
	if session.uim == nil {
		uim, err := qmi.NewUIMServiceWithContext(ctx, session.client)
		if err != nil {
			return nil, err
		}
		session.uim = uim
	}
	return session.uim, nil
}

func (session *productionQMIRadioSession) OpenLogicalChannel(ctx context.Context, slot uint8, aid []byte) (byte, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return 0, err
	}
	return uim.OpenLogicalChannel(ctx, slot, aid)
}

func (session *productionQMIRadioSession) CloseLogicalChannel(ctx context.Context, slot, channel uint8) error {
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	return uim.CloseLogicalChannel(ctx, slot, channel)
}

func (session *productionQMIRadioSession) SendAPDU(ctx context.Context, slot, channel uint8, command []byte) ([]byte, error) {
	uim, err := session.uimService(ctx)
	if err != nil {
		return nil, err
	}
	return uim.SendAPDU(ctx, slot, channel, command)
}

// RegisterUIMRefresh mirrors the terminal registration used by libqmi for a
// physical card slot.  EnableProfile(refresh=true) may cause the eUICC to issue
// a proactive REFRESH; without a registered terminal the card remains CAT busy
// after the profile has changed and rejects the next profile operation.
func (session *productionQMIRadioSession) RegisterUIMRefresh(ctx context.Context) error {
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	if err := uim.RefreshRegisterAll(ctx, qmi.UIMRefreshRegisterAllRequest{
		SessionType:  qmi.UIMSessionTypeCardSlot1,
		RegisterFlag: true,
	}); err != nil {
		return err
	}
	if session.catID == 0 {
		clientID, err := session.client.AllocateClientIDWithContext(ctx, qmi.ServiceCAT2)
		if err != nil {
			return fmt.Errorf("allocate QMI CAT2 client: %w", err)
		}
		session.catID = clientID
	}
	configuration, configErr := session.client.SendRequest(ctx, qmi.ServiceCAT2, session.catID, 0x002E, nil)
	if configErr == nil && configuration.CheckResult() == nil {
		if modeTLV := qmi.FindTLV(configuration.TLVs, 0x10); modeTLV != nil && len(modeTLV.Value) > 0 {
			slog.Info("QMI CAT2 configuration", "mode", modeTLV.Value[0])
		}
	}
	response, err := session.client.SendRequest(ctx, qmi.ServiceCAT2, session.catID, 0x0001, []qmi.TLV{
		// Claim the raw proactive-command events implemented by this CAT2
		// generation (bits 0..22 and 24..25). A profile can leave any STK
		// command pending, not only REFRESH, and SGP.22 forbids profile changes
		// while that proactive session is unanswered.
		{Type: 0x10, Value: []byte{0xFF, 0xFF, 0x7F, 0x03}},
		// Slot mask bit 0 selects slot 1.
		{Type: 0x12, Value: []byte{0x01}},
	})
	if err != nil {
		return fmt.Errorf("register QMI CAT2 refresh: %w", err)
	}
	if err := response.CheckResult(); err != nil {
		return fmt.Errorf("register QMI CAT2 refresh: %w", err)
	}
	for _, tlv := range response.TLVs {
		if tlv.Type >= 0x10 && tlv.Type <= 0x12 {
			slog.Info("QMI CAT2 registration response", "tlv", fmt.Sprintf("0x%02X", tlv.Type), "value", fmt.Sprintf("%X", tlv.Value))
		}
	}
	return nil
}

// CompleteUIMRefresh consumes refresh indications on the same QMI client that
// registered for them.  Qualcomm requires RefreshComplete only for START
// indications whose mode is not RESET; RESET is completed by the modem itself.
func (session *productionQMIRadioSession) CompleteUIMRefresh(ctx context.Context) error {
	if session == nil || session.client == nil {
		return errors.New("QMI UIM refresh session is unavailable")
	}
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	refreshCompleted := false
	uimEnded := false
	catEnded := false
	for {
		select {
		case <-ctx.Done():
			// Some firmware handles a RESET internally and never forwards an
			// indication to this client.  A missing indication is therefore not
			// a failed profile commit.
			return nil
		case event, ok := <-session.client.Events():
			if !ok {
				return nil
			}
			if event.ServiceID == qmi.ServiceCAT2 && event.MessageID == 0x0001 {
				for _, eventTLV := range event.Packet.TLVs {
					slog.Info("QMI CAT2 event", "tlv", fmt.Sprintf("0x%02X", eventTLV.Type), "length", len(eventTLV.Value))
				}
				if tlv := qmi.FindTLV(event.Packet.TLVs, 0x19); tlv != nil && len(tlv.Value) >= 4 {
					mode := uint16(tlv.Value[0]) | uint16(tlv.Value[1])<<8
					stage := uint16(tlv.Value[2]) | uint16(tlv.Value[3])<<8
					slog.Info("QMI CAT2 profile refresh", "stage", stage, "mode", mode)
					if stage == 3 {
						return errors.New("QMI CAT2 refresh ended with failure")
					}
				}
				// UIM refresh completion is not a CAT terminal response. Qualcomm
				// delivers the raw proactive command in a command-specific TLV; send
				// a response carrying that command's reference ID. Unsupported UI STK
				// commands receive the standards-defined "beyond terminal
				// capabilities" result, which still closes the proactive session.
				for _, commandTLV := range event.Packet.TLVs {
					if !isRawCATCommandTLV(commandTLV.Type) {
						continue
					}
					ref, terminalResponse, commandType, responseOK := catProactiveTerminalResponse(commandTLV.Value)
					if !responseOK {
						continue
					}
					if err := session.sendCATTerminalResponse(ctx, ref, terminalResponse); err != nil {
						return err
					}
					slog.Info("QMI CAT2 terminal response sent", "reference", ref, "command", fmt.Sprintf("0x%02X", commandType))
					break
				}
				if tlv := qmi.FindTLV(event.Packet.TLVs, 0x1A); tlv != nil && len(tlv.Value) > 0 {
					// Older MDM8916 CAT2 firmware encodes this enum in one byte;
					// newer interface descriptions model it as a 32-bit value.
					reason := uint32(tlv.Value[0])
					if len(tlv.Value) >= 4 {
						reason |= uint32(tlv.Value[1])<<8 | uint32(tlv.Value[2])<<16 | uint32(tlv.Value[3])<<24
					}
					slog.Info("QMI CAT2 proactive session ended", "reason", reason)
					catEnded = true
					if uimEnded {
						return nil
					}
				}
				continue
			}
			if event.Type != qmi.EventUIMRefresh {
				continue
			}
			info, parseErr := qmi.ParseUIMRefreshIndication(event.Packet)
			if parseErr != nil {
				return parseErr
			}
			const (
				refreshStageWaitForOK = uint8(0)
				refreshStageStart     = uint8(1)
				refreshStageSuccess   = uint8(2)
				refreshStageFailure   = uint8(3)
				refreshModeReset      = uint8(0)
			)
			slog.Info("QMI UIM profile refresh", "stage", info.Stage, "mode", info.Mode)
			switch info.Stage {
			case refreshStageWaitForOK:
				// Registration without a vote advances on its own. Keep the UIM
				// client alive for the subsequent START and END indications.
				continue
			case refreshStageStart:
				if info.Mode == refreshModeReset || refreshCompleted {
					continue
				}
				// libqmi intentionally uses CARD_SLOT_1 here rather than echoing
				// the provisioning session from the indication.
				_ = uim.RefreshComplete(ctx, qmi.UIMRefreshCompleteRequest{
					SessionType:    qmi.UIMSessionTypeCardSlot1,
					RefreshSuccess: true,
				})
				refreshCompleted = true
				continue
			case refreshStageSuccess:
				uimEnded = true
				if catEnded {
					return nil
				}
				continue
			case refreshStageFailure:
				return errors.New("QMI UIM refresh ended with failure")
			default:
				continue
			}
		}
	}
}

func (session *productionQMIRadioSession) sendCATTerminalResponse(ctx context.Context, reference uint32, terminalResponse []byte) error {
	value := make([]byte, 0, 6+len(terminalResponse))
	value = binary.LittleEndian.AppendUint32(value, reference)
	value = binary.LittleEndian.AppendUint16(value, uint16(len(terminalResponse)))
	value = append(value, terminalResponse...)
	response, err := session.client.SendRequest(ctx, qmi.ServiceCAT2, session.catID, 0x0021, []qmi.TLV{
		{Type: 0x01, Value: value},
		{Type: 0x10, Value: []byte{0x01}}, // CAT slot 1 (not a slot mask)
	})
	if err != nil {
		return fmt.Errorf("send QMI CAT2 refresh terminal response: %w", err)
	}
	if err := response.CheckResult(); err != nil {
		return fmt.Errorf("send QMI CAT2 refresh terminal response: %w", err)
	}
	return nil
}

// catProactiveTerminalResponse extracts a raw CAT command carried as
// {reference:uint32LE, length:uint16LE, BER-TLV command} and creates the
// standards-shaped terminal response. VoCat has no interactive STK UI, so
// commands other than REFRESH/MORE TIME are explicitly reported unsupported.
func catProactiveTerminalResponse(raw []byte) (uint32, []byte, byte, bool) {
	if len(raw) < 8 {
		return 0, nil, 0, false
	}
	reference := binary.LittleEndian.Uint32(raw[:4])
	commandLength := int(binary.LittleEndian.Uint16(raw[4:6]))
	if commandLength <= 0 || commandLength > len(raw)-6 {
		return 0, nil, 0, false
	}
	command := raw[6 : 6+commandLength]
	if len(command) < 2 || command[0] != 0xD0 {
		return 0, nil, 0, false
	}
	bodyLength, lengthBytes, ok := catBERLength(command[1:])
	if !ok || 1+lengthBytes+bodyLength > len(command) {
		return 0, nil, 0, false
	}
	body := command[1+lengthBytes : 1+lengthBytes+bodyLength]
	for offset := 0; offset < len(body); {
		tag := body[offset]
		offset++
		length, consumed, ok := catBERLength(body[offset:])
		if !ok || offset+consumed+length > len(body) {
			return 0, nil, 0, false
		}
		offset += consumed
		value := body[offset : offset+length]
		offset += length
		if tag&0x7F != 0x01 || len(value) < 3 {
			continue
		}
		result := byte(0x30)                      // command beyond terminal capabilities
		if value[1] == 0x01 || value[1] == 0x02 { // REFRESH or MORE TIME
			result = 0x00 // command performed successfully
		}
		terminalResponse := []byte{
			0x81, 0x03, value[0], value[1], value[2], // command details
			0x82, 0x02, 0x82, 0x81, // terminal -> UICC
			0x83, 0x01, result,
		}
		return reference, terminalResponse, value[1], true
	}
	return 0, nil, 0, false
}

func catRefreshTerminalResponse(raw []byte) (uint32, []byte, bool) {
	reference, response, commandType, ok := catProactiveTerminalResponse(raw)
	return reference, response, ok && commandType == 0x01
}

func isRawCATCommandTLV(tag byte) bool {
	switch tag {
	case 0x10, 0x11, 0x12, 0x13, 0x14, 0x17, 0x18,
		0x47, 0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x4D, 0x4E, 0x4F,
		0x51, 0x52, 0x53, 0x54, 0x66, 0x6A:
		return true
	default:
		return false
	}
}

func catBERLength(raw []byte) (length int, consumed int, ok bool) {
	if len(raw) == 0 {
		return 0, 0, false
	}
	switch raw[0] {
	case 0x81:
		if len(raw) < 2 {
			return 0, 0, false
		}
		return int(raw[1]), 2, true
	case 0x82:
		if len(raw) < 3 {
			return 0, 0, false
		}
		return int(raw[1])<<8 | int(raw[2]), 3, true
	default:
		if raw[0]&0x80 != 0 {
			return 0, 0, false
		}
		return int(raw[0]), 1, true
	}
}

// AcknowledgeUIMRefresh is a recovery vote for a refresh that predates this
// QMI client. Qualcomm documents RefreshComplete as harmless when no vote is
// pending; it lets a newly started service release a stale CAT-busy condition
// left by an interrupted LPA/terminal transaction.
func (session *productionQMIRadioSession) AcknowledgeUIMRefresh(ctx context.Context) error {
	uim, err := session.uimService(ctx)
	if err != nil {
		return err
	}
	return uim.RefreshComplete(ctx, qmi.UIMRefreshCompleteRequest{
		SessionType:    qmi.UIMSessionTypeCardSlot1,
		RefreshSuccess: true,
	})
}

func (session *productionQMIRadioSession) GetIMEI(ctx context.Context) (string, error) {
	if session == nil || session.dms == nil {
		return "", errors.New("QMI DMS identity session is unavailable")
	}
	info, err := session.dms.GetDeviceSerialNumbers(ctx)
	if err != nil {
		return "", err
	}
	return info.IMEI, nil
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
	session.uimMu.Lock()
	if session.uim != nil {
		closeErrors = append(closeErrors, session.uim.Close())
		session.uim = nil
	}
	session.uimMu.Unlock()
	if session.dms != nil {
		closeErrors = append(closeErrors, session.dms.Close())
		session.dms = nil
	}
	if session.nas != nil {
		closeErrors = append(closeErrors, session.nas.Close())
		session.nas = nil
	}
	if session.client != nil && session.catID != 0 {
		closeErrors = append(closeErrors, session.client.ReleaseClientID(qmi.ServiceCAT2, session.catID))
		session.catID = 0
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
	if !enabled && !currentRadioOff {
		// DMS Online is only the radio half of the recovery. Continue with a
		// background NAS registration/PS-attach reconcile after the flight-mode
		// transition without holding the radio QMI session open.
		manager.startNativeQMIRegistrationReconcile(id)
	}
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
