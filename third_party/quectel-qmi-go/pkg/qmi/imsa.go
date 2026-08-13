package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
)

// ============================================================================
// IMSA Service Wrapper / IMSA 服务封装
// ============================================================================

type IMSAService struct {
	client   *Client
	clientID uint8
}

func NewIMSAService(client *Client) (*IMSAService, error) {
	clientID, err := client.AllocateClientID(ServiceIMSA)
	if err != nil {
		return nil, err
	}
	return &IMSAService{client: client, clientID: clientID}, nil
}

func (i *IMSAService) Close() error {
	return i.client.ReleaseClientID(ServiceIMSA, i.clientID)
}

// ============================================================================
// IMSA Types / IMSA 类型
// ============================================================================

type IMSARegistrationState uint32
type IMSAServiceAvailability uint32
type IMSARegistrationTechnology uint32

const (
	IMSARegistrationStateNotRegistered     IMSARegistrationState = 0x00
	IMSARegistrationStateRegistering       IMSARegistrationState = 0x01
	IMSARegistrationStateRegistered        IMSARegistrationState = 0x02
	IMSARegistrationStateLimitedRegistered IMSARegistrationState = 0x03
)

const (
	IMSAServiceAvailabilityUnavailable IMSAServiceAvailability = 0x00
	IMSAServiceAvailabilityLimited     IMSAServiceAvailability = 0x01
	IMSAServiceAvailabilityAvailable   IMSAServiceAvailability = 0x02
)

const (
	IMSARegistrationTechnologyWLAN             IMSARegistrationTechnology = 0x00
	IMSARegistrationTechnologyWWAN             IMSARegistrationTechnology = 0x01
	IMSARegistrationTechnologyInterworkingWLAN IMSARegistrationTechnology = 0x02
)

type IMSARegistrationStatus struct {
	Status          IMSARegistrationState
	HasStatus       bool
	ErrorCode       uint16
	HasErrorCode    bool
	ErrorMessage    string
	HasErrorMessage bool
	Technology      IMSARegistrationTechnology
	HasTechnology   bool
}

type IMSAServicesStatus struct {
	SMSServiceStatus               IMSAServiceAvailability
	HasSMSServiceStatus            bool
	VoiceServiceStatus             IMSAServiceAvailability
	HasVoiceServiceStatus          bool
	VideoTelephonyServiceStatus    IMSAServiceAvailability
	HasVideoTelephonyServiceStatus bool
	SMSTechnology                  IMSARegistrationTechnology
	HasSMSTechnology               bool
	VoiceTechnology                IMSARegistrationTechnology
	HasVoiceTechnology             bool
	VideoTelephonyTechnology       IMSARegistrationTechnology
	HasVideoTelephonyTechnology    bool
	UETASServiceStatus             IMSAServiceAvailability
	HasUETASServiceStatus          bool
	UETASTechnology                IMSARegistrationTechnology
	HasUETASTechnology             bool
	VideoShareServiceStatus        IMSAServiceAvailability
	HasVideoShareServiceStatus     bool
	VideoShareTechnology           IMSARegistrationTechnology
	HasVideoShareTechnology        bool
}

type IMSAIndicationRegistration struct {
	RegistrationStatusChanged bool
	ServicesStatusChanged     bool
}

// ============================================================================
// IMSA Message IDs / IMSA 消息 ID
// ============================================================================

const (
	IMSAGetIMSRegistrationStatus  uint16 = 0x0020
	IMSAGetIMSServicesStatus      uint16 = 0x0021
	IMSARegisterIndications       uint16 = 0x0022
	IMSARegistrationStatusChanged uint16 = 0x0023
	IMSAServicesStatusChanged     uint16 = 0x0024
	IMSABindRequest               uint16 = 0x0033
)

// ============================================================================
// Public Methods / 对外方法
// ============================================================================

func (i *IMSAService) Bind(ctx context.Context, binding uint32) error {
	resp, err := i.client.SendRequest(ctx, ServiceIMSA, i.clientID, IMSABindRequest, buildIMSABindTLVs(binding))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("imsa bind failed: %w", err)
	}
	return nil
}

func (i *IMSAService) GetIMSRegistrationStatus(ctx context.Context) (*IMSARegistrationStatus, error) {
	resp, err := i.client.SendRequest(ctx, ServiceIMSA, i.clientID, IMSAGetIMSRegistrationStatus, nil)
	if err != nil {
		return nil, err
	}
	return parseIMSARegistrationStatusResponse(resp)
}

func (i *IMSAService) GetIMSServicesStatus(ctx context.Context) (*IMSAServicesStatus, error) {
	resp, err := i.client.SendRequest(ctx, ServiceIMSA, i.clientID, IMSAGetIMSServicesStatus, nil)
	if err != nil {
		return nil, err
	}
	return parseIMSAServicesStatusResponse(resp)
}

func (i *IMSAService) RegisterIndications(ctx context.Context, cfg IMSAIndicationRegistration) error {
	resp, err := i.client.SendRequest(ctx, ServiceIMSA, i.clientID, IMSARegisterIndications, buildIMSARegisterIndicationsTLVs(cfg))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("imsa register indications failed: %w", err)
	}
	return nil
}

func ParseIMSARegistrationStatusChanged(packet *Packet) (*IMSARegistrationStatus, error) {
	return parseIMSARegistrationStatusPacket(packet, false)
}

func ParseIMSAServicesStatusChanged(packet *Packet) (*IMSAServicesStatus, error) {
	return parseIMSAServicesStatusPacket(packet, false)
}

// ============================================================================
// Internal Helpers / 内部助手
// ============================================================================

func buildIMSARegisterIndicationsTLVs(cfg IMSAIndicationRegistration) []TLV {
	return []TLV{
		NewTLVUint8(0x10, boolToUint8(cfg.RegistrationStatusChanged)),
		NewTLVUint8(0x11, boolToUint8(cfg.ServicesStatusChanged)),
	}
}

func buildIMSABindTLVs(binding uint32) []TLV {
	return []TLV{NewTLVUint32(0x10, binding)}
}

func parseIMSARegistrationStatusResponse(resp *Packet) (*IMSARegistrationStatus, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get ims registration status failed: %w", err)
	}
	return parseIMSARegistrationStatusPacket(resp, true)
}

func parseIMSARegistrationStatusPacket(packet *Packet, isResponse bool) (*IMSARegistrationStatus, error) {
	statusTLVType := uint8(0x11)
	errorCodeTLVType := uint8(0x10)
	errorMessageTLVType := uint8(0x12)
	technologyTLVType := uint8(0x13)
	if isResponse {
		statusTLVType = 0x12
		errorCodeTLVType = 0x11
		errorMessageTLVType = 0x13
		technologyTLVType = 0x14
	}

	out := &IMSARegistrationStatus{}
	if tlv := FindTLV(packet.TLVs, statusTLVType); tlv != nil && len(tlv.Value) >= 4 {
		out.Status = IMSARegistrationState(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasStatus = true
	}
	if tlv := FindTLV(packet.TLVs, errorCodeTLVType); tlv != nil && len(tlv.Value) >= 2 {
		out.ErrorCode = binary.LittleEndian.Uint16(tlv.Value[0:2])
		out.HasErrorCode = true
	}
	if tlv := FindTLV(packet.TLVs, errorMessageTLVType); tlv != nil {
		out.ErrorMessage = string(tlv.Value)
		out.HasErrorMessage = true
	}
	if tlv := FindTLV(packet.TLVs, technologyTLVType); tlv != nil && len(tlv.Value) >= 4 {
		out.Technology = IMSARegistrationTechnology(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasTechnology = true
	}
	if !out.HasStatus {
		return nil, fmt.Errorf("ims registration status TLV missing")
	}
	return out, nil
}

func parseIMSAServicesStatusResponse(resp *Packet) (*IMSAServicesStatus, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get ims services status failed: %w", err)
	}
	return parseIMSAServicesStatusPacket(resp, true)
}

func parseIMSAServicesStatusPacket(packet *Packet, _ bool) (*IMSAServicesStatus, error) {
	out := &IMSAServicesStatus{}

	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 4 {
		out.SMSServiceStatus = IMSAServiceAvailability(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasSMSServiceStatus = true
	}
	if tlv := FindTLV(packet.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 4 {
		out.VoiceServiceStatus = IMSAServiceAvailability(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasVoiceServiceStatus = true
	}
	if tlv := FindTLV(packet.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 4 {
		out.VideoTelephonyServiceStatus = IMSAServiceAvailability(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasVideoTelephonyServiceStatus = true
	}
	if tlv := FindTLV(packet.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 4 {
		out.SMSTechnology = IMSARegistrationTechnology(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasSMSTechnology = true
	}
	if tlv := FindTLV(packet.TLVs, 0x14); tlv != nil && len(tlv.Value) >= 4 {
		out.VoiceTechnology = IMSARegistrationTechnology(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasVoiceTechnology = true
	}
	if tlv := FindTLV(packet.TLVs, 0x15); tlv != nil && len(tlv.Value) >= 4 {
		out.VideoTelephonyTechnology = IMSARegistrationTechnology(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasVideoTelephonyTechnology = true
	}
	if tlv := FindTLV(packet.TLVs, 0x16); tlv != nil && len(tlv.Value) >= 4 {
		out.UETASServiceStatus = IMSAServiceAvailability(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasUETASServiceStatus = true
	}
	if tlv := FindTLV(packet.TLVs, 0x17); tlv != nil && len(tlv.Value) >= 4 {
		out.UETASTechnology = IMSARegistrationTechnology(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasUETASTechnology = true
	}
	if tlv := FindTLV(packet.TLVs, 0x18); tlv != nil && len(tlv.Value) >= 4 {
		out.VideoShareServiceStatus = IMSAServiceAvailability(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasVideoShareServiceStatus = true
	}
	if tlv := FindTLV(packet.TLVs, 0x19); tlv != nil && len(tlv.Value) >= 4 {
		out.VideoShareTechnology = IMSARegistrationTechnology(binary.LittleEndian.Uint32(tlv.Value[0:4]))
		out.HasVideoShareTechnology = true
	}
	return out, nil
}
