package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
)

// ============================================================================
// VOICE Service Wrapper / VOICE 服务封装
// ============================================================================

type VOICEService struct {
	client   *Client
	clientID uint8
}

func NewVOICEService(client *Client) (*VOICEService, error) {
	return NewVOICEServiceWithContext(context.Background(), client)
}

func NewVOICEServiceWithContext(ctx context.Context, client *Client) (*VOICEService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceVOICE)
	if err != nil {
		return nil, err
	}
	return &VOICEService{client: client, clientID: clientID}, nil
}

func (v *VOICEService) Close() error {
	return v.client.ReleaseClientID(ServiceVOICE, v.clientID)
}

// ============================================================================
// VOICE Types / VOICE 类型
// ============================================================================

type VoiceCallState uint8
type VoiceCallDirection uint8
type VoiceCallMode uint8
type VoiceUserAction uint8

type VoiceIndicationRegistration struct {
	DTMFEvents                                      bool
	VoicePrivacyEvents                              bool
	SupplementaryServiceNotificationEvents          bool
	CallNotificationEvents                          bool
	HandoverEvents                                  bool
	SpeechCodecEvents                               bool
	USSDNotificationEvents                          bool
	ModificationEvents                              bool
	UUSEvents                                       bool
	AOCEvents                                       bool
	ConferenceEvents                                bool
	ExtendedBurstTypeInternationalInformationEvents bool
	MTPageMissInformationEvents                     bool
}

type VoiceCallInfo struct {
	ID        uint8
	State     VoiceCallState
	Type      uint8
	Direction VoiceCallDirection
	Mode      VoiceCallMode
	Multipart bool
	ALS       uint8
}

type VoiceRemotePartyNumber struct {
	CallID                uint8
	PresentationIndicator uint8
	Number                string
	RawNumber             []byte
}

type VoiceAllCallInfo struct {
	Calls              []VoiceCallInfo
	RemotePartyNumbers []VoiceRemotePartyNumber
}

type VoiceManageCallsRequest struct {
	ServiceType uint8
	CallID      uint8
}

type VoiceSupplementaryServiceRequest struct {
	Action uint8
	Reason uint8
}

type VoiceSupplementaryServiceStatus struct {
	Active      bool
	Provisioned bool
}

type VoiceSupplementaryServiceIndication struct {
	CallID           uint8
	NotificationType uint8
}

type VoiceSupplementaryServiceRequestIndication struct {
	Request                 uint8
	ModifiedByCallControl   bool
	HasInfo                 bool
	ServiceClass            uint8
	HasServiceClass         bool
	Reason                  uint8
	HasReason               bool
	USSData                 *VoiceUSSDPayload
	CallID                  uint8
	HasCallID               bool
	Alpha                   *VoiceUSSDPayload
	DataSource              uint8
	HasDataSource           bool
	FailureCause            uint16
	HasFailureCause         bool
	EncodedDataUTF16        []uint16
	ExtendedServiceClass    uint8
	HasExtendedServiceClass bool
}

type VoiceUSSDRequest struct {
	DCS  uint8
	Data []byte
}

type VoiceUSSDPayload struct {
	DCS  uint8
	Data []byte
	Text string
}

type VoiceUSSDResponse struct {
	FailureCause                             uint16
	HasFailureCause                          bool
	Alpha                                    *VoiceUSSDPayload
	USSData                                  *VoiceUSSDPayload
	CallControlResultType                    uint8
	HasCallControlResultType                 bool
	CallID                                   uint8
	HasCallID                                bool
	CallControlSupplementaryServiceType      uint8
	HasCallControlSupplementaryServiceType   bool
	CallControlSupplementaryServiceReason    uint8
	HasCallControlSupplementaryServiceReason bool
	USSDataUTF16                             []uint16
}

type VoiceUSSDIndication struct {
	UserAction    VoiceUserAction
	HasUserAction bool
	USSData       *VoiceUSSDPayload
	USSDataUTF16  []uint16
}

type VoiceUSSDNoWaitIndication struct {
	ErrorCode       uint16
	HasErrorCode    bool
	FailureCause    uint16
	HasFailureCause bool
	USSData         *VoiceUSSDPayload
	Alpha           *VoiceUSSDPayload
	USSDataUTF16    []uint16
}

type VoiceConfigQuery struct {
	AutoAnswer            bool
	AirTimer              bool
	RoamTimer             bool
	TTYMode               bool
	PreferredVoiceSO      bool
	AMRStatus             bool
	PreferredVoicePrivacy bool
	NAMIndex              bool
	VoiceDomainPreference bool
}

type VoiceTimerCount struct {
	NAMID   uint8
	Minutes uint32
}

type VoicePreferredVoiceSO struct {
	NAMID                             uint8
	EVRCCapability                    bool
	HomePageVoiceServiceOption        uint16
	HomeOriginationVoiceServiceOption uint16
	RoamOriginationVoiceServiceOption uint16
}

type VoiceAMRStatus struct {
	GSM   bool
	WCDMA uint8
}

type VoiceConfig struct {
	AutoAnswerStatus                 bool
	HasAutoAnswerStatus              bool
	AirTimer                         VoiceTimerCount
	HasAirTimer                      bool
	RoamTimer                        VoiceTimerCount
	HasRoamTimer                     bool
	CurrentTTYMode                   uint8
	HasCurrentTTYMode                bool
	PreferredVoiceSO                 VoicePreferredVoiceSO
	HasPreferredVoiceSO              bool
	CurrentAMRStatus                 VoiceAMRStatus
	HasCurrentAMRStatus              bool
	CurrentVoicePrivacyPreference    uint8
	HasCurrentVoicePrivacyPreference bool
	CurrentVoiceDomainPreference     uint8
	HasCurrentVoiceDomainPreference  bool
}

// ============================================================================
// VOICE Message IDs / VOICE 消息 ID
// ============================================================================

const (
	VOICEIndicationRegister             uint16 = 0x0003
	VOICEGetSupportedMessages           uint16 = 0x001E
	VOICEDialCall                       uint16 = 0x0020
	VOICEEndCall                        uint16 = 0x0021
	VOICEAnswerCall                     uint16 = 0x0022
	VOICEBurstDTMF                      uint16 = 0x0028
	VOICEStartContinuousDTMF            uint16 = 0x0029
	VOICEStopContinuousDTMF             uint16 = 0x002A
	VOICEAllCallStatusInd               uint16 = 0x002E
	VOICEGetAllCallInfo                 uint16 = 0x002F
	VOICEManageCalls                    uint16 = 0x0031
	VOICESupplementaryServiceInd        uint16 = 0x0032
	VOICESetSupplementaryService        uint16 = 0x0033
	VOICEGetCallWaiting                 uint16 = 0x0034
	VOICEOriginateUSSD                  uint16 = 0x003A
	VOICEAnswerUSSD                     uint16 = 0x003B
	VOICECancelUSSD                     uint16 = 0x003C
	VOICEReleaseUSSDInd                 uint16 = 0x003D
	VOICEUSSDInd                        uint16 = 0x003E
	VOICEGetConfig                      uint16 = 0x0041
	VOICESupplementaryServiceRequestInd uint16 = 0x0042
	VOICEOriginateUSSDNoWait            uint16 = 0x0043
)

// ============================================================================
// Public Methods / 对外方法
// ============================================================================

func (v *VOICEService) IndicationRegister(ctx context.Context, cfg VoiceIndicationRegistration) error {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEIndicationRegister, buildVoiceIndicationRegistrationTLVs(cfg))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("voice indication register failed: %w", err)
	}
	return nil
}

func (v *VOICEService) GetSupportedMessages(ctx context.Context) ([]uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEGetSupportedMessages, nil)
	if err != nil {
		return nil, err
	}
	return parseVoiceSupportedMessagesResponse(resp)
}

func (v *VOICEService) DialCall(ctx context.Context, number string) (uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEDialCall, []TLV{NewTLVString(0x01, number)})
	if err != nil {
		return 0, err
	}
	return parseVoiceCallIDResponse(resp, "dial call")
}

func (v *VOICEService) EndCall(ctx context.Context, callID uint8) (uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEEndCall, []TLV{NewTLVUint8(0x01, callID)})
	if err != nil {
		return 0, err
	}
	return parseVoiceCallIDResponse(resp, "end call")
}

func (v *VOICEService) AnswerCall(ctx context.Context, callID uint8) (uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEAnswerCall, []TLV{NewTLVUint8(0x01, callID)})
	if err != nil {
		return 0, err
	}
	return parseVoiceCallIDResponse(resp, "answer call")
}

func (v *VOICEService) BurstDTMF(ctx context.Context, callID uint8, digits string) (uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEBurstDTMF, []TLV{buildVoiceBurstDTMFTLV(callID, digits)})
	if err != nil {
		return 0, err
	}
	return parseVoiceCallIDResponse(resp, "burst dtmf")
}

func (v *VOICEService) StartContinuousDTMF(ctx context.Context, callID uint8, digit uint8) (uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEStartContinuousDTMF, []TLV{buildVoiceStartContinuousDTMFTLV(callID, digit)})
	if err != nil {
		return 0, err
	}
	return parseVoiceCallIDResponse(resp, "start continuous dtmf")
}

func (v *VOICEService) StopContinuousDTMF(ctx context.Context, callID uint8) (uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEStopContinuousDTMF, []TLV{buildVoiceStopContinuousDTMFTLV(callID)})
	if err != nil {
		return 0, err
	}
	return parseVoiceCallIDResponse(resp, "stop continuous dtmf")
}

func (v *VOICEService) GetAllCallInfo(ctx context.Context) (*VoiceAllCallInfo, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEGetAllCallInfo, nil)
	if err != nil {
		return nil, err
	}
	return parseVoiceAllCallInfoResponse(resp)
}

func (v *VOICEService) ManageCalls(ctx context.Context, req VoiceManageCallsRequest) error {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEManageCalls, buildVoiceManageCallsTLVs(req))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("manage calls failed: %w", err)
	}
	return nil
}

func (v *VOICEService) SetSupplementaryService(ctx context.Context, req VoiceSupplementaryServiceRequest) (*VoiceSupplementaryServiceStatus, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICESetSupplementaryService, []TLV{buildVoiceSupplementaryServiceTLV(req)})
	if err != nil {
		return nil, err
	}
	return parseVoiceSupplementaryServiceStatusResponse(resp)
}

func (v *VOICEService) GetCallWaiting(ctx context.Context, serviceClass uint8) (uint8, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEGetCallWaiting, []TLV{NewTLVUint8(0x10, serviceClass)})
	if err != nil {
		return 0, err
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("get call waiting failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 1 {
		return 0, fmt.Errorf("get call waiting response missing service class TLV")
	}
	return tlv.Value[0], nil
}

func (v *VOICEService) OriginateUSSD(ctx context.Context, req VoiceUSSDRequest) (*VoiceUSSDResponse, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEOriginateUSSD, []TLV{buildVoiceUSSDRequestTLV(req)})
	if err != nil {
		return nil, err
	}
	return parseVoiceUSSDResponse(resp, "originate ussd")
}

func (v *VOICEService) AnswerUSSD(ctx context.Context, req VoiceUSSDRequest) error {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEAnswerUSSD, []TLV{buildVoiceUSSDRequestTLV(req)})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("answer ussd failed: %w", err)
	}
	return nil
}

func (v *VOICEService) CancelUSSD(ctx context.Context) error {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICECancelUSSD, nil)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("cancel ussd failed: %w", err)
	}
	return nil
}

func (v *VOICEService) GetConfig(ctx context.Context, query VoiceConfigQuery) (*VoiceConfig, error) {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEGetConfig, buildVoiceConfigQueryTLVs(query))
	if err != nil {
		return nil, err
	}
	return parseVoiceConfigResponse(resp)
}

func (v *VOICEService) OriginateUSSDNoWait(ctx context.Context, req VoiceUSSDRequest) error {
	resp, err := v.client.SendRequest(ctx, ServiceVOICE, v.clientID, VOICEOriginateUSSDNoWait, []TLV{buildVoiceUSSDRequestTLV(req)})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("originate ussd no wait failed: %w", err)
	}
	return nil
}

func ParseVoiceAllCallStatus(packet *Packet) (*VoiceAllCallInfo, error) {
	return parseVoiceAllCallInfoPacket(packet, 0x01, 0x10, "all call status indication")
}

func ParseVoiceSupplementaryServiceIndication(packet *Packet) (*VoiceSupplementaryServiceIndication, error) {
	tlv := FindTLV(packet.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("supplementary service indication missing info TLV")
	}
	return &VoiceSupplementaryServiceIndication{
		CallID:           tlv.Value[0],
		NotificationType: tlv.Value[1],
	}, nil
}

func ParseVoiceSupplementaryServiceRequestIndication(packet *Packet) (*VoiceSupplementaryServiceRequestIndication, error) {
	info := &VoiceSupplementaryServiceRequestIndication{}
	if tlv := FindTLV(packet.TLVs, 0x01); tlv != nil {
		if len(tlv.Value) < 2 {
			return nil, fmt.Errorf("supplementary service request indication info TLV too short")
		}
		info.Request = tlv.Value[0]
		info.ModifiedByCallControl = tlv.Value[1] != 0
		info.HasInfo = true
	}
	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		info.ServiceClass = tlv.Value[0]
		info.HasServiceClass = true
	}
	if tlv := FindTLV(packet.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		info.Reason = tlv.Value[0]
		info.HasReason = true
	}
	if tlv := FindTLV(packet.TLVs, 0x14); tlv != nil {
		payload, err := parseVoiceUSSDPayloadTLV(tlv)
		if err != nil {
			return nil, err
		}
		info.USSData = payload
	}
	if tlv := FindTLV(packet.TLVs, 0x15); tlv != nil && len(tlv.Value) >= 1 {
		info.CallID = tlv.Value[0]
		info.HasCallID = true
	}
	if tlv := FindTLV(packet.TLVs, 0x16); tlv != nil {
		payload, err := parseVoiceUSSDPayloadTLV(tlv)
		if err != nil {
			return nil, err
		}
		info.Alpha = payload
	}
	if tlv := FindTLV(packet.TLVs, 0x19); tlv != nil && len(tlv.Value) >= 1 {
		info.DataSource = tlv.Value[0]
		info.HasDataSource = true
	}
	if tlv := FindTLV(packet.TLVs, 0x1A); tlv != nil && len(tlv.Value) >= 2 {
		info.FailureCause = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasFailureCause = true
	}
	if tlv := FindTLV(packet.TLVs, 0x21); tlv != nil {
		values, err := parseUint16ArrayWithUint8Length(tlv.Value)
		if err != nil {
			return nil, err
		}
		info.EncodedDataUTF16 = values
	}
	if tlv := FindTLV(packet.TLVs, 0x22); tlv != nil && len(tlv.Value) >= 1 {
		info.ExtendedServiceClass = tlv.Value[0]
		info.HasExtendedServiceClass = true
	}
	return info, nil
}

func ParseVoiceUSSDIndication(packet *Packet) (*VoiceUSSDIndication, error) {
	info := &VoiceUSSDIndication{}
	if tlv := FindTLV(packet.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		info.UserAction = VoiceUserAction(tlv.Value[0])
		info.HasUserAction = true
	}
	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil {
		payload, err := parseVoiceUSSDPayloadTLV(tlv)
		if err != nil {
			return nil, err
		}
		info.USSData = payload
	}
	if tlv := FindTLV(packet.TLVs, 0x11); tlv != nil {
		values, err := parseUint16ArrayWithUint8Length(tlv.Value)
		if err != nil {
			return nil, err
		}
		info.USSDataUTF16 = values
	}
	return info, nil
}

func ParseVoiceUSSDNoWaitIndication(packet *Packet) (*VoiceUSSDNoWaitIndication, error) {
	info := &VoiceUSSDNoWaitIndication{}
	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
		info.ErrorCode = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasErrorCode = true
	}
	if tlv := FindTLV(packet.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 2 {
		info.FailureCause = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasFailureCause = true
	}
	if tlv := FindTLV(packet.TLVs, 0x12); tlv != nil {
		payload, err := parseVoiceUSSDPayloadTLV(tlv)
		if err != nil {
			return nil, err
		}
		info.USSData = payload
	}
	if tlv := FindTLV(packet.TLVs, 0x13); tlv != nil {
		payload, err := parseVoiceUSSDPayloadTLV(tlv)
		if err != nil {
			return nil, err
		}
		info.Alpha = payload
	}
	if tlv := FindTLV(packet.TLVs, 0x14); tlv != nil {
		values, err := parseUint16ArrayWithUint8Length(tlv.Value)
		if err != nil {
			return nil, err
		}
		info.USSDataUTF16 = values
	}
	return info, nil
}

// ============================================================================
// Internal Helpers / 内部助手
// ============================================================================

func buildVoiceIndicationRegistrationTLVs(cfg VoiceIndicationRegistration) []TLV {
	return []TLV{
		NewTLVUint8(0x10, boolToUint8(cfg.DTMFEvents)),
		NewTLVUint8(0x11, boolToUint8(cfg.VoicePrivacyEvents)),
		NewTLVUint8(0x12, boolToUint8(cfg.SupplementaryServiceNotificationEvents)),
		NewTLVUint8(0x13, boolToUint8(cfg.CallNotificationEvents)),
		NewTLVUint8(0x14, boolToUint8(cfg.HandoverEvents)),
		NewTLVUint8(0x15, boolToUint8(cfg.SpeechCodecEvents)),
		NewTLVUint8(0x16, boolToUint8(cfg.USSDNotificationEvents)),
		NewTLVUint8(0x18, boolToUint8(cfg.ModificationEvents)),
		NewTLVUint8(0x19, boolToUint8(cfg.UUSEvents)),
		NewTLVUint8(0x1A, boolToUint8(cfg.AOCEvents)),
		NewTLVUint8(0x1B, boolToUint8(cfg.ConferenceEvents)),
		NewTLVUint8(0x1C, boolToUint8(cfg.ExtendedBurstTypeInternationalInformationEvents)),
		NewTLVUint8(0x1D, boolToUint8(cfg.MTPageMissInformationEvents)),
	}
}

func buildVoiceBurstDTMFTLV(callID uint8, digits string) TLV {
	buf := make([]byte, 2+len(digits))
	buf[0] = callID
	buf[1] = uint8(len(digits))
	copy(buf[2:], []byte(digits))
	return TLV{Type: 0x01, Value: buf}
}

func buildVoiceStartContinuousDTMFTLV(callID uint8, digit uint8) TLV {
	return TLV{Type: 0x01, Value: []byte{callID, digit}}
}

func buildVoiceStopContinuousDTMFTLV(callID uint8) TLV {
	return TLV{Type: 0x01, Value: []byte{callID}}
}

func buildVoiceManageCallsTLVs(req VoiceManageCallsRequest) []TLV {
	return []TLV{
		NewTLVUint8(0x01, req.ServiceType),
		NewTLVUint8(0x10, req.CallID),
	}
}

func buildVoiceSupplementaryServiceTLV(req VoiceSupplementaryServiceRequest) TLV {
	return TLV{Type: 0x01, Value: []byte{req.Action, req.Reason}}
}

func buildVoiceUSSDRequestTLV(req VoiceUSSDRequest) TLV {
	buf := make([]byte, 2+len(req.Data))
	buf[0] = req.DCS
	buf[1] = uint8(len(req.Data))
	copy(buf[2:], req.Data)
	return TLV{Type: 0x01, Value: buf}
}

func buildVoiceConfigQueryTLVs(query VoiceConfigQuery) []TLV {
	if isZeroVoiceConfigQuery(query) {
		query = VoiceConfigQuery{
			AutoAnswer:            true,
			AirTimer:              true,
			RoamTimer:             true,
			TTYMode:               true,
			PreferredVoiceSO:      true,
			AMRStatus:             true,
			PreferredVoicePrivacy: true,
			NAMIndex:              true,
			VoiceDomainPreference: true,
		}
	}

	var tlvs []TLV
	if query.AutoAnswer {
		tlvs = append(tlvs, NewTLVUint8(0x10, 1))
	}
	if query.AirTimer {
		tlvs = append(tlvs, NewTLVUint8(0x11, 1))
	}
	if query.RoamTimer {
		tlvs = append(tlvs, NewTLVUint8(0x12, 1))
	}
	if query.TTYMode {
		tlvs = append(tlvs, NewTLVUint8(0x13, 1))
	}
	if query.PreferredVoiceSO {
		tlvs = append(tlvs, NewTLVUint8(0x14, 1))
	}
	if query.AMRStatus {
		tlvs = append(tlvs, NewTLVUint8(0x15, 1))
	}
	if query.PreferredVoicePrivacy {
		tlvs = append(tlvs, NewTLVUint8(0x16, 1))
	}
	if query.NAMIndex {
		tlvs = append(tlvs, NewTLVUint8(0x17, 1))
	}
	if query.VoiceDomainPreference {
		tlvs = append(tlvs, NewTLVUint8(0x18, 1))
	}
	return tlvs
}

func isZeroVoiceConfigQuery(query VoiceConfigQuery) bool {
	return !query.AutoAnswer &&
		!query.AirTimer &&
		!query.RoamTimer &&
		!query.TTYMode &&
		!query.PreferredVoiceSO &&
		!query.AMRStatus &&
		!query.PreferredVoicePrivacy &&
		!query.NAMIndex &&
		!query.VoiceDomainPreference
}

func parseVoiceSupportedMessagesResponse(resp *Packet) ([]uint8, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get supported messages failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("supported messages response missing list TLV")
	}
	count := int(binary.LittleEndian.Uint16(tlv.Value[0:2]))
	if len(tlv.Value) < 2+count {
		return nil, fmt.Errorf("supported messages TLV truncated: need %d, have %d", 2+count, len(tlv.Value))
	}
	out := make([]uint8, count)
	copy(out, tlv.Value[2:2+count])
	return out, nil
}

func parseVoiceCallIDResponse(resp *Packet, operation string) (uint8, error) {
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("%s failed: %w", operation, err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 1 {
		return 0, fmt.Errorf("%s response missing call id TLV", operation)
	}
	return tlv.Value[0], nil
}

func parseVoiceAllCallInfoResponse(resp *Packet) (*VoiceAllCallInfo, error) {
	return parseVoiceAllCallInfoPacket(resp, 0x10, 0x11, "get all call info")
}

func parseVoiceAllCallInfoPacket(packet *Packet, callTLVType uint8, remoteTLVType uint8, operation string) (*VoiceAllCallInfo, error) {
	if resultTLV := FindTLV(packet.TLVs, 0x02); resultTLV != nil {
		if err := packet.CheckResult(); err != nil {
			return nil, fmt.Errorf("%s failed: %w", operation, err)
		}
	}

	info := &VoiceAllCallInfo{
		Calls:              make([]VoiceCallInfo, 0),
		RemotePartyNumbers: make([]VoiceRemotePartyNumber, 0),
	}
	if tlv := FindTLV(packet.TLVs, callTLVType); tlv != nil {
		calls, err := parseVoiceCallInfoArray(tlv.Value)
		if err != nil {
			return nil, err
		}
		info.Calls = calls
	}
	if tlv := FindTLV(packet.TLVs, remoteTLVType); tlv != nil {
		numbers, err := parseVoiceRemotePartyNumberArray(tlv.Value)
		if err != nil {
			return nil, err
		}
		info.RemotePartyNumbers = numbers
	}
	return info, nil
}

func parseVoiceSupplementaryServiceStatusResponse(resp *Packet) (*VoiceSupplementaryServiceStatus, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("set supplementary service failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x15)
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("set supplementary service response missing status TLV")
	}
	return &VoiceSupplementaryServiceStatus{
		Active:      tlv.Value[0] != 0,
		Provisioned: tlv.Value[1] != 0,
	}, nil
}

func parseVoiceUSSDResponse(resp *Packet, operation string) (*VoiceUSSDResponse, error) {
	result := &VoiceUSSDResponse{}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
		result.FailureCause = binary.LittleEndian.Uint16(tlv.Value[0:2])
		result.HasFailureCause = true
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil {
		payload, err := parseVoiceUSSDPayloadTLV(tlv)
		if err != nil {
			return nil, err
		}
		result.Alpha = payload
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil {
		payload, err := parseVoiceUSSDPayloadTLV(tlv)
		if err != nil {
			return nil, err
		}
		result.USSData = payload
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 1 {
		result.CallControlResultType = tlv.Value[0]
		result.HasCallControlResultType = true
	}
	if tlv := FindTLV(resp.TLVs, 0x14); tlv != nil && len(tlv.Value) >= 1 {
		result.CallID = tlv.Value[0]
		result.HasCallID = true
	}
	if tlv := FindTLV(resp.TLVs, 0x15); tlv != nil && len(tlv.Value) >= 1 {
		result.CallControlSupplementaryServiceType = tlv.Value[0]
		result.HasCallControlSupplementaryServiceType = true
		if len(tlv.Value) >= 2 {
			result.CallControlSupplementaryServiceReason = tlv.Value[1]
			result.HasCallControlSupplementaryServiceReason = true
		}
	}
	if tlv := FindTLV(resp.TLVs, 0x16); tlv != nil {
		values, err := parseUint16ArrayWithUint8Length(tlv.Value)
		if err != nil {
			return nil, err
		}
		result.USSDataUTF16 = values
	}

	if err := resp.CheckResult(); err != nil {
		return result, fmt.Errorf("%s failed: %w", operation, err)
	}
	return result, nil
}

func parseVoiceConfigResponse(resp *Packet) (*VoiceConfig, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get voice config failed: %w", err)
	}

	cfg := &VoiceConfig{}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		cfg.AutoAnswerStatus = tlv.Value[0] != 0
		cfg.HasAutoAnswerStatus = true
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 5 {
		cfg.AirTimer = VoiceTimerCount{
			NAMID:   tlv.Value[0],
			Minutes: binary.LittleEndian.Uint32(tlv.Value[1:5]),
		}
		cfg.HasAirTimer = true
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 5 {
		cfg.RoamTimer = VoiceTimerCount{
			NAMID:   tlv.Value[0],
			Minutes: binary.LittleEndian.Uint32(tlv.Value[1:5]),
		}
		cfg.HasRoamTimer = true
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 1 {
		cfg.CurrentTTYMode = tlv.Value[0]
		cfg.HasCurrentTTYMode = true
	}
	if tlv := FindTLV(resp.TLVs, 0x14); tlv != nil && len(tlv.Value) >= 8 {
		cfg.PreferredVoiceSO = VoicePreferredVoiceSO{
			NAMID:                             tlv.Value[0],
			EVRCCapability:                    tlv.Value[1] != 0,
			HomePageVoiceServiceOption:        binary.LittleEndian.Uint16(tlv.Value[2:4]),
			HomeOriginationVoiceServiceOption: binary.LittleEndian.Uint16(tlv.Value[4:6]),
			RoamOriginationVoiceServiceOption: binary.LittleEndian.Uint16(tlv.Value[6:8]),
		}
		cfg.HasPreferredVoiceSO = true
	}
	if tlv := FindTLV(resp.TLVs, 0x15); tlv != nil && len(tlv.Value) >= 2 {
		cfg.CurrentAMRStatus = VoiceAMRStatus{
			GSM:   tlv.Value[0] != 0,
			WCDMA: tlv.Value[1],
		}
		cfg.HasCurrentAMRStatus = true
	}
	if tlv := FindTLV(resp.TLVs, 0x16); tlv != nil && len(tlv.Value) >= 1 {
		cfg.CurrentVoicePrivacyPreference = tlv.Value[0]
		cfg.HasCurrentVoicePrivacyPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x17); tlv != nil && len(tlv.Value) >= 1 {
		cfg.CurrentVoiceDomainPreference = tlv.Value[0]
		cfg.HasCurrentVoiceDomainPreference = true
	}
	return cfg, nil
}

func parseVoiceCallInfoArray(value []byte) ([]VoiceCallInfo, error) {
	if len(value) < 1 {
		return nil, fmt.Errorf("voice call info array too short")
	}
	count := int(value[0])
	expected := 1 + count*7
	if len(value) < expected {
		return nil, fmt.Errorf("voice call info array truncated: need %d, have %d", expected, len(value))
	}
	items := make([]VoiceCallInfo, 0, count)
	offset := 1
	for i := 0; i < count; i++ {
		items = append(items, VoiceCallInfo{
			ID:        value[offset],
			State:     VoiceCallState(value[offset+1]),
			Type:      value[offset+2],
			Direction: VoiceCallDirection(value[offset+3]),
			Mode:      VoiceCallMode(value[offset+4]),
			Multipart: value[offset+5] != 0,
			ALS:       value[offset+6],
		})
		offset += 7
	}
	return items, nil
}

func parseVoiceRemotePartyNumberArray(value []byte) ([]VoiceRemotePartyNumber, error) {
	if len(value) < 1 {
		return nil, fmt.Errorf("voice remote party number array too short")
	}
	count := int(value[0])
	offset := 1
	items := make([]VoiceRemotePartyNumber, 0, count)
	for i := 0; i < count; i++ {
		if offset+3 > len(value) {
			return nil, fmt.Errorf("voice remote party number array truncated at item %d", i)
		}
		length := int(value[offset+2])
		if offset+3+length > len(value) {
			return nil, fmt.Errorf("voice remote party number item %d truncated", i)
		}
		raw := append([]byte(nil), value[offset+3:offset+3+length]...)
		items = append(items, VoiceRemotePartyNumber{
			CallID:                value[offset],
			PresentationIndicator: value[offset+1],
			Number:                string(raw),
			RawNumber:             raw,
		})
		offset += 3 + length
	}
	return items, nil
}

func parseVoiceUSSDPayloadTLV(tlv *TLV) (*VoiceUSSDPayload, error) {
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("voice ussd payload TLV too short")
	}
	length := int(tlv.Value[1])
	if len(tlv.Value) < 2+length {
		return nil, fmt.Errorf("voice ussd payload TLV truncated: need %d, have %d", 2+length, len(tlv.Value))
	}
	data := append([]byte(nil), tlv.Value[2:2+length]...)
	return &VoiceUSSDPayload{
		DCS:  tlv.Value[0],
		Data: data,
		Text: string(data),
	}, nil
}

func parseUint16ArrayWithUint8Length(value []byte) ([]uint16, error) {
	if len(value) < 1 {
		return nil, fmt.Errorf("uint16 array value too short")
	}
	count := int(value[0])
	expected := 1 + count*2
	if len(value) < expected {
		return nil, fmt.Errorf("uint16 array truncated: need %d, have %d", expected, len(value))
	}
	out := make([]uint16, count)
	offset := 1
	for i := 0; i < count; i++ {
		out[i] = binary.LittleEndian.Uint16(value[offset : offset+2])
		offset += 2
	}
	return out, nil
}
