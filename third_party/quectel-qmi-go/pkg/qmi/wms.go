package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
)

// ============================================================================
// WMS Service Wrapper / WMS 服务封装
// ============================================================================

type WMSService struct {
	client   *Client
	clientID uint8
}

type rawReadMessageValue struct {
	tag    MessageTagType
	hasTag bool
	data   []byte
}

// NewWMSService creates a WMS service wrapper / NewWMSService 创建 WMS 服务封装
func NewWMSService(client *Client) (*WMSService, error) {
	return NewWMSServiceWithContext(context.Background(), client)
}

func NewWMSServiceWithContext(ctx context.Context, client *Client) (*WMSService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceWMS)
	if err != nil {
		return nil, err
	}
	return &WMSService{client: client, clientID: clientID}, nil
}

// Close releases the WMS client ID / Close 释放 WMS 客户端 ID
func (w *WMSService) Close() error {
	return w.client.ReleaseClientID(ServiceWMS, w.clientID)
}

// ============================================================================
// SMS Operations / 短信操作
// ============================================================================

// MessageTagType for listing messages / 用于列出短信的消息标签类型
type MessageTagType uint8

const (
	TagTypeMTRead    MessageTagType = 0x00 // MT: Mobile Terminated (Received) - Read / MT: 移动终端终结（接收）- 已读
	TagTypeMTNotRead MessageTagType = 0x01 // MT: Mobile Terminated (Received) - Not Read / MT: 移动终端终结（接收）- 未读
	TagTypeMOSent    MessageTagType = 0x02 // MO: Mobile Originated (Sent) - Sent / MO: 移动终端发起（发送）- 已发送
	TagTypeMONotSent MessageTagType = 0x03 // MO: Mobile Originated (Sent) - Not Sent / MO: 移动终端发起（发送）- 未发送
)

type MessageMode uint8

const (
	MessageModeCDMA MessageMode = 0x00
	MessageModeGW   MessageMode = 0x01
)

type WMSMessageProtocol uint8

const (
	WMSMessageProtocolCDMA  WMSMessageProtocol = 0x00
	WMSMessageProtocolWCDMA WMSMessageProtocol = 0x01
)

type WMSTransportNetworkRegistration uint8

const (
	WMSTransportNetworkRegistrationNoService      WMSTransportNetworkRegistration = 0x00
	WMSTransportNetworkRegistrationInProcess      WMSTransportNetworkRegistration = 0x01
	WMSTransportNetworkRegistrationFailure        WMSTransportNetworkRegistration = 0x02
	WMSTransportNetworkRegistrationLimitedService WMSTransportNetworkRegistration = 0x03
	WMSTransportNetworkRegistrationFullService    WMSTransportNetworkRegistration = 0x04
)

func (s WMSTransportNetworkRegistration) String() string {
	switch s {
	case WMSTransportNetworkRegistrationNoService:
		return "no-service"
	case WMSTransportNetworkRegistrationInProcess:
		return "in-process"
	case WMSTransportNetworkRegistrationFailure:
		return "failure"
	case WMSTransportNetworkRegistrationLimitedService:
		return "limited-service"
	case WMSTransportNetworkRegistrationFullService:
		return "full-service"
	default:
		return "unknown"
	}
}

// WMSServiceReadyStatus is the modem's SMS service readiness state.  A NAS
// registration does not imply that the WMS storage/application path is ready.
type WMSServiceReadyStatus uint32

const (
	WMSServiceReadyNotReady     WMSServiceReadyStatus = 0
	WMSServiceReady3GPP         WMSServiceReadyStatus = 1
	WMSServiceReady3GPP2        WMSServiceReadyStatus = 2
	WMSServiceReady3GPPAnd3GPP2 WMSServiceReadyStatus = 3
)

func (s WMSServiceReadyStatus) String() string {
	switch s {
	case WMSServiceReadyNotReady:
		return "not-ready"
	case WMSServiceReady3GPP:
		return "3gpp-ready"
	case WMSServiceReady3GPP2:
		return "3gpp2-ready"
	case WMSServiceReady3GPPAnd3GPP2:
		return "3gpp-and-3gpp2-ready"
	default:
		return "unknown"
	}
}

type WMSSMSCAddress struct {
	Type   string
	Digits string
}

type WMSRoute struct {
	MessageType   uint8
	MessageClass  uint8
	StorageType   uint8
	ReceiptAction uint8
}

// WMS route values follow the public Qualcomm/libqmi enumeration. Keeping
// these values in the QMI package prevents callers from having to duplicate
// magic numbers when repairing a modem-side storage route.
const (
	WMSMessageTypePointToPoint uint8 = 0x00

	WMSMessageClass0    uint8 = 0x00
	WMSMessageClass1    uint8 = 0x01
	WMSMessageClass2    uint8 = 0x02
	WMSMessageClass3    uint8 = 0x03
	WMSMessageClassNone uint8 = 0x04
	WMSMessageClassCDMA uint8 = 0x05

	WMSStorageTypeUIM  uint8 = 0x00
	WMSStorageTypeNV   uint8 = 0x01
	WMSStorageTypeNone uint8 = 0xFF

	WMSReceiptActionDiscard        uint8 = 0x00
	WMSReceiptActionStoreAndNotify uint8 = 0x01
	WMSReceiptActionTransferOnly   uint8 = 0x02
	WMSReceiptActionTransferAndAck uint8 = 0x03
	WMSReceiptActionUnknown        uint8 = 0xFF
)

type WMSRouteConfig struct {
	Routes                       []WMSRoute
	TransferStatusReportToClient bool
	HasTransferStatusReport      bool
}

type WMSAck3GPP2Failure struct {
	ErrorClass uint8
	CauseCode  uint8
}

type WMSAck3GPPFailure struct {
	RPCause uint8
	TPCause uint8
}

type WMSAckRequest struct {
	TransactionID uint32
	Protocol      WMSMessageProtocol
	Success       bool
	Failure3GPP2  *WMSAck3GPP2Failure
	Failure3GPP   *WMSAck3GPPFailure
	SMSOnIMS      *bool
}

type WMSAckResult struct {
	FailureCause    uint8
	HasFailureCause bool
}

type WMSSendFromStorageResult struct {
	MessageID              uint16
	HasMessageID           bool
	CDMACauseCode          uint16
	HasCDMACauseCode       bool
	CDMAErrorClass         uint8
	HasCDMAErrorClass      bool
	GSMWCDMARPCause        uint16
	HasGSMWCDMARPCause     bool
	GSMWCDMATPCause        uint8
	HasGSMWCDMATPCause     bool
	DeliveryFailureType    uint8
	HasDeliveryFailureType bool
}

const (
	WMSReset uint16 = 0x0000
	/* Defined in frame.go / 在 frame.go 中定义
	WMSDelete         uint16 = 0x0024
	*/
	WMSGetSupportedMessages                  uint16 = 0x001E
	WMSModifyTag                             uint16 = 0x0023
	WMSGetMessageProtocol                    uint16 = 0x0030
	WMSSetRoutes                             uint16 = 0x0032
	WMSGetRoutes                             uint16 = 0x0033
	WMSGetSMSCAddress                        uint16 = 0x0034
	WMSSendAck                               uint16 = 0x0037
	WMSSendFromStorage                       uint16 = 0x0042
	WMSIndicationRegister                    uint16 = 0x0047
	WMSGetTransportNetworkRegistrationStatus uint16 = 0x004A
	WMSBindSubscription                      uint16 = 0x004C
	WMSGetSubscriptionBinding                uint16 = 0x0058
	WMSGetServiceReady                       uint16 = 0x005C
)

// Reset resets only the WMS service state. It does not reset the modem or
// change RF registration, making it suitable for rebuilding a stale WMS
// client after an eUICC REFRESH/profile switch.
func (w *WMSService) Reset(ctx context.Context) error {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSReset, nil)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("reset WMS service failed: %w", err)
	}
	return nil
}

// BindSubscription binds this WMS client to a subscription. Qualcomm Gobi
// defines 0 as the primary subscription, 1 as secondary and 2 as tertiary.
func (w *WMSService) BindSubscription(ctx context.Context, subscription uint8) error {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSBindSubscription,
		[]TLV{NewTLVUint8(0x01, subscription)})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("bind WMS subscription %d failed: %w", subscription, err)
	}
	return nil
}

// GetSubscriptionBinding returns the subscription bound to this WMS client.
func (w *WMSService) GetSubscriptionBinding(ctx context.Context) (uint8, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSGetSubscriptionBinding, nil)
	if err != nil {
		return 0, err
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("get WMS subscription binding failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 1 {
		return 0, fmt.Errorf("WMS subscription binding TLV missing or too short")
	}
	return tlv.Value[0], nil
}

// GetServiceReadyStatus returns the WMS SMS service readiness state.
func (w *WMSService) GetServiceReadyStatus(ctx context.Context) (WMSServiceReadyStatus, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSGetServiceReady, nil)
	if err != nil {
		return WMSServiceReadyNotReady, err
	}
	if err := resp.CheckResult(); err != nil {
		return WMSServiceReadyNotReady, fmt.Errorf("get WMS service ready status failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x11)
	if tlv == nil || len(tlv.Value) < 4 {
		return WMSServiceReadyNotReady, fmt.Errorf("WMS service ready TLV missing or too short")
	}
	return WMSServiceReadyStatus(binary.LittleEndian.Uint32(tlv.Value[:4])), nil
}

func (w *WMSService) GetSupportedMessages(ctx context.Context) ([]uint8, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSGetSupportedMessages, nil)
	if err != nil {
		return nil, err
	}
	return parseWMSSupportedMessagesResponse(resp)
}

// ListMessages lists messages from memory storage / ListMessages 从内存存储中列出消息
// Returns a list of (index, tag) tuples / 返回（索引，标签）元组列表
func (w *WMSService) ListMessages(ctx context.Context, storageType uint8, tagType MessageTagType) ([]struct {
	Index uint32
	Tag   MessageTagType
}, error) {
	// TLV 0x01: Memory Storage Identification / 内存存储识别
	tlvs := []TLV{{Type: 0x01, Value: []byte{storageType}}}

	// TLV 0x11: Message Tag (Some modems require this in 0x11, others in 0x02) / 消息标签（部分调制解调器需要在0x11，其他在0x02）
	tlvs = append(tlvs, TLV{Type: 0x11, Value: []byte{uint8(tagType)}})

	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSListMessages, tlvs)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("list messages failed: %w", err)
	}

	// TLV 0x01: Message List Identification / 消息列表识别
	// Format: count(4), [index(4), tag(1)] * count / 格式：数量(4), [索引(4), 标签(1)] * 数量
	listTLV := FindTLV(resp.TLVs, 0x01)
	if listTLV == nil || len(listTLV.Value) < 4 {
		return nil, nil // No messages / 没有消息
	}

	count := binary.LittleEndian.Uint32(listTLV.Value[0:4])
	var result []struct {
		Index uint32
		Tag   MessageTagType
	}

	offset := 4
	for i := uint32(0); i < count; i++ {
		if offset+5 > len(listTLV.Value) {
			break
		}
		idx := binary.LittleEndian.Uint32(listTLV.Value[offset : offset+4])
		tag := MessageTagType(listTLV.Value[offset+4])
		result = append(result, struct {
			Index uint32
			Tag   MessageTagType
		}{Index: idx, Tag: tag})
		offset += 5
	}

	return result, nil
}

func (w *WMSService) ListMessagesAuto(ctx context.Context, storageType uint8) ([]struct {
	Index uint32
	Tag   MessageTagType
}, error) {
	try := func(tlvs []TLV) ([]struct {
		Index uint32
		Tag   MessageTagType
	}, error) {
		resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSListMessages, tlvs)
		if err != nil {
			return nil, err
		}
		if err := resp.CheckResult(); err != nil {
			return nil, fmt.Errorf("list messages failed: %w", err)
		}

		listTLV := FindTLV(resp.TLVs, 0x01)
		if listTLV == nil || len(listTLV.Value) < 4 {
			return nil, nil
		}

		count := binary.LittleEndian.Uint32(listTLV.Value[0:4])
		var result []struct {
			Index uint32
			Tag   MessageTagType
		}

		offset := 4
		for i := uint32(0); i < count; i++ {
			if offset+5 > len(listTLV.Value) {
				break
			}
			idx := binary.LittleEndian.Uint32(listTLV.Value[offset : offset+4])
			tag := MessageTagType(listTLV.Value[offset+4])
			result = append(result, struct {
				Index uint32
				Tag   MessageTagType
			}{Index: idx, Tag: tag})
			offset += 5
		}
		return result, nil
	}

	storage := TLV{Type: 0x01, Value: []byte{storageType}}
	mode := TLV{Type: 0x10, Value: []byte{uint8(MessageModeGW)}}

	attempts := [][]TLV{
		{storage, {Type: 0x11, Value: []byte{uint8(TagTypeMTNotRead)}}},
		{storage, {Type: 0x11, Value: []byte{uint8(TagTypeMTNotRead)}}, mode},
		{storage, {Type: 0x02, Value: []byte{uint8(TagTypeMTNotRead)}}},
		{storage, {Type: 0x02, Value: []byte{uint8(TagTypeMTNotRead)}}, mode},
		{storage, {Type: 0x11, Value: []byte{uint8(TagTypeMTRead)}}},
		{storage, {Type: 0x11, Value: []byte{uint8(TagTypeMTRead)}}, mode},
	}

	var lastErr error
	for _, tlvs := range attempts {
		msgs, err := try(tlvs)
		if err != nil {
			lastErr = err
			continue
		}
		return msgs, nil
	}
	return nil, lastErr
}

// RawReadMessage reads a raw SMS PDU / RawReadMessage 读取原始短信 PDU
func (w *WMSService) RawReadMessage(ctx context.Context, storageType uint8, index uint32) ([]byte, error) {
	// TLV 0x01: Memory Storage Identification / 内存存储识别
	buf := make([]byte, 5)
	buf[0] = storageType
	binary.LittleEndian.PutUint32(buf[1:5], index)

	tlvs := []TLV{
		{Type: 0x01, Value: buf},
		{Type: 0x10, Value: []byte{0x01}}, // Message Mode: GW (0x01) / 消息模式：GW (0x01)
	}

	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSRawRead, tlvs)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("read message failed: %w", err)
	}

	// TLV 0x01: Raw Message Identification / 原始消息识别
	// Variations:
	// 1. [Format(1)], [Length(2)], [Data(N)]
	// 2. [Tag(1)], [Format(1)], [Length(2)], [Data(N)]
	msgTLV := FindTLV(resp.TLVs, 0x01)
	if msgTLV == nil || len(msgTLV.Value) < 3 {
		return nil, fmt.Errorf("response missing raw message TLV or too short")
	}

	parsed, err := parseRawReadMessageValue(msgTLV.Value)
	if err != nil {
		return nil, err
	}
	return parsed.data, nil
}

func parseRawReadMessageValue(val []byte) (rawReadMessageValue, error) {
	if len(val) < 3 {
		return rawReadMessageValue{}, fmt.Errorf("raw message TLV too short")
	}

	// Untagged format: [Format(1)], [Length(2)], [Data(N)].
	length := int(binary.LittleEndian.Uint16(val[1:3]))
	if length <= len(val)-3 {
		return rawReadMessageValue{
			data: append([]byte(nil), val[3:3+length]...),
		}, nil
	}

	// Tagged format: [Tag(1)], [Format(1)], [Length(2)], [Data(N)].
	if len(val) >= 4 {
		length = int(binary.LittleEndian.Uint16(val[2:4]))
		if isKnownMessageTagType(val[0]) && length <= len(val)-4 {
			return rawReadMessageValue{
				tag:    MessageTagType(val[0]),
				hasTag: true,
				data:   append([]byte(nil), val[4:4+length]...),
			}, nil
		}
	}

	return rawReadMessageValue{
		data: append([]byte(nil), val[3:]...),
	}, nil
}

func isKnownMessageTagType(v byte) bool {
	switch MessageTagType(v) {
	case TagTypeMTRead, TagTypeMTNotRead, TagTypeMOSent, TagTypeMONotSent:
		return true
	default:
		return false
	}
}

func (w *WMSService) RawReadMessageMeta(ctx context.Context, storageType uint8, index uint32) (MessageTagType, bool, []byte, error) {
	buf := make([]byte, 5)
	buf[0] = storageType
	binary.LittleEndian.PutUint32(buf[1:5], index)

	tlvs := []TLV{
		{Type: 0x01, Value: buf},
		{Type: 0x10, Value: []byte{0x01}},
	}

	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSRawRead, tlvs)
	if err != nil {
		return 0, false, nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return 0, false, nil, fmt.Errorf("read message failed: %w", err)
	}

	msgTLV := FindTLV(resp.TLVs, 0x01)
	if msgTLV == nil || len(msgTLV.Value) < 3 {
		return 0, false, nil, fmt.Errorf("response missing raw message TLV or too short")
	}

	parsed, err := parseRawReadMessageValue(msgTLV.Value)
	if err != nil {
		return 0, false, nil, err
	}
	return parsed.tag, parsed.hasTag, parsed.data, nil
}

func (w *WMSService) DeleteMessage(ctx context.Context, storageType uint8, index uint32) error {
	return w.DeleteMessageByIndex(ctx, storageType, index, MessageModeGW)
}

func (w *WMSService) DeleteMessageByIndex(ctx context.Context, storageType uint8, index uint32, mode MessageMode) error {
	attempts := [][]TLV{
		{NewTLVUint8(0x01, storageType), NewTLVUint32(0x10, index), NewTLVUint8(0x12, uint8(mode))},
		{NewTLVUint8(0x01, storageType), NewTLVUint32(0x02, index), NewTLVUint8(0x04, uint8(mode))},
	}

	var lastErr error
	for _, tlvs := range attempts {
		resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSDelete, tlvs)
		if err != nil {
			lastErr = err
			continue
		}
		if err := resp.CheckResult(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (w *WMSService) DeleteMessagesByTag(ctx context.Context, storageType uint8, tag MessageTagType, mode MessageMode) error {
	attempts := [][]TLV{
		{NewTLVUint8(0x01, storageType), NewTLVUint8(0x11, uint8(tag)), NewTLVUint8(0x12, uint8(mode))},
		{NewTLVUint8(0x01, storageType), NewTLVUint8(0x03, uint8(tag)), NewTLVUint8(0x04, uint8(mode))},
	}

	var lastErr error
	for _, tlvs := range attempts {
		resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSDelete, tlvs)
		if err != nil {
			lastErr = err
			continue
		}
		if err := resp.CheckResult(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// ModifyMessageTag modifies the tag of a message (e.g. mark as read) / ModifyMessageTag 修改消息标签 (例如标记为已读)
func (w *WMSService) ModifyMessageTag(ctx context.Context, storageType uint8, index uint32, newTag MessageTagType) error {
	modeTLV := TLV{Type: 0x10, Value: []byte{0x01}}

	bufCombined := make([]byte, 6)
	bufCombined[0] = storageType
	binary.LittleEndian.PutUint32(bufCombined[1:5], index)
	bufCombined[5] = uint8(newTag)

	bufInfo := make([]byte, 5)
	bufInfo[0] = storageType
	binary.LittleEndian.PutUint32(bufInfo[1:5], index)

	attempts := [][]TLV{
		{{Type: 0x01, Value: bufCombined}, modeTLV},
		{{Type: 0x01, Value: bufCombined}},
		{NewTLVUint8(0x01, uint8(newTag)), {Type: 0x03, Value: bufInfo}, modeTLV},
		{NewTLVUint8(0x01, uint8(newTag)), {Type: 0x03, Value: bufInfo}},
		{NewTLVUint8(0x01, uint8(newTag)), NewTLVUint32(0x02, 0), {Type: 0x03, Value: bufInfo}, modeTLV},
	}

	var lastErr error
	for _, tlvs := range attempts {
		resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSModifyTag, tlvs)
		if err != nil {
			lastErr = err
			continue
		}
		if err := resp.CheckResult(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// GetSMSCAddress gets the SMS center address / GetSMSCAddress 获取短信中心地址
func (w *WMSService) GetSMSCAddress(ctx context.Context) (string, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSGetSMSCAddress, nil)
	if err != nil {
		return "", err
	}

	if err := resp.CheckResult(); err != nil {
		return "", err
	}

	// TLV 0x01: SMSC Address / 短信中心地址
	// Type(3 bytes for type/length) + Address...
	// Usually: [Type(3 chars string)] [Length(1)] [Digits...]
	// But QMI spec says:
	// string SMSCAddressType (max 3)
	// string SMSCAddress (max 20)
	// Let's look for TLV 0x01
	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil {
		return "", fmt.Errorf("SMSC address TLV not found")
	}

	// Parse as string for simplicity, though it might be binary encoded digits
	// Typically ASCII for type, and ASCII digits for address
	return string(tlv.Value), nil
}

// RegisterEventReport enables indications for new messages / RegisterEventReport 开启新消息指示
func (w *WMSService) RegisterEventReport(ctx context.Context) error {
	// TLV 0x10: New MT Message Indicator (1 = enable) / 新 MT 消息指示器 (1 = 启用)
	tlvs := []TLV{NewTLVUint8(0x10, 0x01)}

	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSSetEventReport, tlvs)
	if err != nil {
		return err
	}

	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("register event report failed: %w", err)
	}
	return nil
}

// RawWriteMessage writes a raw SMS PDU into modem storage and returns the memory index.
func (w *WMSService) RawWriteMessage(ctx context.Context, storageType uint8, format uint8, pdu []byte) (uint32, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSRawWrite, buildRawWriteMessageTLVs(storageType, format, pdu))
	if err != nil {
		return 0, err
	}
	return parseRawWriteMessageResponse(resp)
}

// GetMessageProtocol returns the current WMS message protocol.
func (w *WMSService) GetMessageProtocol(ctx context.Context) (WMSMessageProtocol, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSGetMessageProtocol, nil)
	if err != nil {
		return 0, err
	}
	return parseGetMessageProtocolResponse(resp)
}

// SetRoutes updates the modem-side SMS routing table.
func (w *WMSService) SetRoutes(ctx context.Context, routes []WMSRoute, transferStatusReportToClient bool) error {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSSetRoutes, buildSetRoutesTLVs(routes, transferStatusReportToClient))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("set routes failed: %w", err)
	}
	return nil
}

// GetRoutes returns the current modem-side SMS routing table.
func (w *WMSService) GetRoutes(ctx context.Context) (*WMSRouteConfig, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSGetRoutes, nil)
	if err != nil {
		return nil, err
	}
	return parseGetRoutesResponse(resp)
}

// SendAck sends a delivery/report acknowledgment for a previously received SMS.
func (w *WMSService) SendAck(ctx context.Context, req WMSAckRequest) (*WMSAckResult, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSSendAck, buildSendAckTLVs(req))
	if err != nil {
		return nil, err
	}
	return parseSendAckResponse(resp)
}

// SendFromStorage sends a message that already exists in modem storage.
func (w *WMSService) SendFromStorage(ctx context.Context, storageType uint8, index uint32, mode MessageMode, smsOnIMS bool) (*WMSSendFromStorageResult, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSSendFromStorage, buildSendFromStorageTLVs(storageType, index, mode, smsOnIMS))
	if err != nil {
		return nil, err
	}
	return parseSendFromStorageResponse(resp)
}

// IndicationRegister toggles transport network registration indications.
func (w *WMSService) IndicationRegister(ctx context.Context, reportTransportNetworkRegistration bool) error {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSIndicationRegister, []TLV{NewTLVUint8(0x11, boolToUint8(reportTransportNetworkRegistration))})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("indication register failed: %w", err)
	}
	return nil
}

// GetTransportNetworkRegistrationStatus returns the WMS transport network registration state.
func (w *WMSService) GetTransportNetworkRegistrationStatus(ctx context.Context) (WMSTransportNetworkRegistration, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSGetTransportNetworkRegistrationStatus, nil)
	if err != nil {
		return 0, err
	}
	return parseTransportNetworkRegistrationStatusResponse(resp)
}

// SendRawMessage sends a raw PDU / SendRawMessage 发送原始 PDU
// format: 0x06 (GSM/WCDMA), 0x00 (CDMA) / 格式：0x06 (GSM/WCDMA), 0x00 (CDMA)
func (w *WMSService) SendRawMessage(ctx context.Context, format uint8, pdu []byte) error {
	// TLV 0x01: Raw Message Write / 原始消息写入
	// Format: format(1), length(2), data(...) / 格式：格式(1)，长度(2)，数据(...)
	buf := make([]byte, 3+len(pdu))
	buf[0] = format
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(pdu)))
	copy(buf[3:], pdu)

	tlvs := []TLV{{Type: 0x01, Value: buf}}

	resp, err := w.client.SendRequest(ctx, ServiceWMS, w.clientID, WMSRawSend, tlvs)
	if err != nil {
		return err
	}

	if err := resp.CheckResult(); err != nil {
		if summary := summarizeRawSendResponse(resp); summary != "" {
			return fmt.Errorf("send message failed: %w (%s)", err, summary)
		}
		return fmt.Errorf("send message failed: %w", err)
	}
	return nil
}

func buildRawWriteMessageTLVs(storageType uint8, format uint8, pdu []byte) []TLV {
	buf := make([]byte, 4+len(pdu))
	buf[0] = storageType
	buf[1] = format
	binary.LittleEndian.PutUint16(buf[2:4], uint16(len(pdu)))
	copy(buf[4:], pdu)
	return []TLV{{Type: 0x01, Value: buf}}
}

func buildSetRoutesTLVs(routes []WMSRoute, transferStatusReportToClient bool) []TLV {
	buf := make([]byte, 2+len(routes)*4)
	binary.LittleEndian.PutUint16(buf[0:2], uint16(len(routes)))
	offset := 2
	for _, route := range routes {
		buf[offset] = route.MessageType
		buf[offset+1] = route.MessageClass
		buf[offset+2] = route.StorageType
		buf[offset+3] = route.ReceiptAction
		offset += 4
	}

	return []TLV{
		{Type: 0x01, Value: buf},
		NewTLVUint8(0x10, boolToUint8(transferStatusReportToClient)),
	}
}

func buildSendAckTLVs(req WMSAckRequest) []TLV {
	info := make([]byte, 6)
	binary.LittleEndian.PutUint32(info[0:4], req.TransactionID)
	info[4] = uint8(req.Protocol)
	info[5] = boolToUint8(req.Success)

	tlvs := []TLV{{Type: 0x01, Value: info}}
	if !req.Success && req.Failure3GPP2 != nil {
		tlvs = append(tlvs, TLV{Type: 0x10, Value: []byte{req.Failure3GPP2.ErrorClass, req.Failure3GPP2.CauseCode}})
	}
	if !req.Success && req.Failure3GPP != nil {
		tlvs = append(tlvs, TLV{Type: 0x11, Value: []byte{req.Failure3GPP.RPCause, req.Failure3GPP.TPCause}})
	}
	if req.SMSOnIMS != nil {
		tlvs = append(tlvs, NewTLVUint8(0x12, boolToUint8(*req.SMSOnIMS)))
	}
	return tlvs
}

func buildSendFromStorageTLVs(storageType uint8, index uint32, mode MessageMode, smsOnIMS bool) []TLV {
	info := make([]byte, 6)
	info[0] = storageType
	binary.LittleEndian.PutUint32(info[1:5], index)
	info[5] = uint8(mode)
	return []TLV{
		{Type: 0x01, Value: info},
		NewTLVUint8(0x10, boolToUint8(smsOnIMS)),
	}
}

func parseRawWriteMessageResponse(resp *Packet) (uint32, error) {
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("raw write message failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil {
		return 0, fmt.Errorf("raw write response missing memory index TLV")
	}
	if len(tlv.Value) < 4 {
		return 0, fmt.Errorf("raw write memory index TLV too short: %d", len(tlv.Value))
	}
	return binary.LittleEndian.Uint32(tlv.Value[0:4]), nil
}

func parseGetMessageProtocolResponse(resp *Packet) (WMSMessageProtocol, error) {
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("get message protocol failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil {
		return 0, fmt.Errorf("message protocol TLV not found")
	}
	if len(tlv.Value) < 1 {
		return 0, fmt.Errorf("message protocol TLV too short: %d", len(tlv.Value))
	}
	return WMSMessageProtocol(tlv.Value[0]), nil
}

func parseGetRoutesResponse(resp *Packet) (*WMSRouteConfig, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get routes failed: %w", err)
	}

	config := &WMSRouteConfig{Routes: make([]WMSRoute, 0)}
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil {
		routes, err := parseRouteList(tlv.Value)
		if err != nil {
			return nil, err
		}
		config.Routes = routes
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		if len(tlv.Value) < 1 {
			return nil, fmt.Errorf("transfer status report TLV too short: %d", len(tlv.Value))
		}
		config.TransferStatusReportToClient = tlv.Value[0] != 0
		config.HasTransferStatusReport = true
	}
	return config, nil
}

func parseSendAckResponse(resp *Packet) (*WMSAckResult, error) {
	result := &WMSAckResult{}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		if len(tlv.Value) < 1 {
			return nil, fmt.Errorf("ack failure cause TLV too short: %d", len(tlv.Value))
		}
		result.FailureCause = tlv.Value[0]
		result.HasFailureCause = true
	}

	if err := resp.CheckResult(); err != nil {
		return result, fmt.Errorf("send ack failed: %w", err)
	}
	return result, nil
}

func parseSendFromStorageResponse(resp *Packet) (*WMSSendFromStorageResult, error) {
	result := &WMSSendFromStorageResult{}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		if len(tlv.Value) < 2 {
			return nil, fmt.Errorf("message ID TLV too short: %d", len(tlv.Value))
		}
		result.MessageID = binary.LittleEndian.Uint16(tlv.Value[0:2])
		result.HasMessageID = true
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil {
		if len(tlv.Value) < 2 {
			return nil, fmt.Errorf("CDMA cause TLV too short: %d", len(tlv.Value))
		}
		result.CDMACauseCode = binary.LittleEndian.Uint16(tlv.Value[0:2])
		result.HasCDMACauseCode = true
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil {
		if len(tlv.Value) < 1 {
			return nil, fmt.Errorf("CDMA error class TLV too short: %d", len(tlv.Value))
		}
		result.CDMAErrorClass = tlv.Value[0]
		result.HasCDMAErrorClass = true
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil {
		if len(tlv.Value) < 3 {
			return nil, fmt.Errorf("GSM/WCDMA cause TLV too short: %d", len(tlv.Value))
		}
		result.GSMWCDMARPCause = binary.LittleEndian.Uint16(tlv.Value[0:2])
		result.HasGSMWCDMARPCause = true
		result.GSMWCDMATPCause = tlv.Value[2]
		result.HasGSMWCDMATPCause = true
	}
	if tlv := FindTLV(resp.TLVs, 0x14); tlv != nil {
		if len(tlv.Value) < 1 {
			return nil, fmt.Errorf("delivery failure type TLV too short: %d", len(tlv.Value))
		}
		result.DeliveryFailureType = tlv.Value[0]
		result.HasDeliveryFailureType = true
	}

	if err := resp.CheckResult(); err != nil {
		if summary := summarizeSendFromStorageResult(result); summary != "" {
			return result, fmt.Errorf("send from storage failed: %w (%s)", err, summary)
		}
		return result, fmt.Errorf("send from storage failed: %w", err)
	}
	return result, nil
}

func summarizeRawSendResponse(resp *Packet) string {
	if resp == nil {
		return ""
	}
	parts := make([]string, 0, 5)
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 2 {
		parts = append(parts, fmt.Sprintf("msg_id=0x%04x", binary.LittleEndian.Uint16(tlv.Value[0:2])))
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
		parts = append(parts, fmt.Sprintf("cdma_cause=0x%04x", binary.LittleEndian.Uint16(tlv.Value[0:2])))
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		parts = append(parts, fmt.Sprintf("cdma_class=0x%02x", tlv.Value[0]))
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 3 {
		rp := binary.LittleEndian.Uint16(tlv.Value[0:2])
		tp := tlv.Value[2]
		parts = append(parts, fmt.Sprintf("rp_cause=0x%04x", rp))
		parts = append(parts, fmt.Sprintf("tp_cause=0x%02x", tp))
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 1 {
		parts = append(parts, fmt.Sprintf("delivery_failure_type=0x%02x", tlv.Value[0]))
	}
	return strings.Join(parts, ",")
}

func summarizeSendFromStorageResult(result *WMSSendFromStorageResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, 6)
	if result.HasMessageID {
		parts = append(parts, fmt.Sprintf("msg_id=0x%04x", result.MessageID))
	}
	if result.HasCDMACauseCode {
		parts = append(parts, fmt.Sprintf("cdma_cause=0x%04x", result.CDMACauseCode))
	}
	if result.HasCDMAErrorClass {
		parts = append(parts, fmt.Sprintf("cdma_class=0x%02x", result.CDMAErrorClass))
	}
	if result.HasGSMWCDMARPCause {
		parts = append(parts, fmt.Sprintf("rp_cause=0x%04x", result.GSMWCDMARPCause))
	}
	if result.HasGSMWCDMATPCause {
		parts = append(parts, fmt.Sprintf("tp_cause=0x%02x", result.GSMWCDMATPCause))
	}
	if result.HasDeliveryFailureType {
		parts = append(parts, fmt.Sprintf("delivery_failure_type=0x%02x", result.DeliveryFailureType))
	}
	return strings.Join(parts, ",")
}

func parseTransportNetworkRegistrationStatusResponse(resp *Packet) (WMSTransportNetworkRegistration, error) {
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("get transport network registration status failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil {
		return 0, fmt.Errorf("transport network registration status TLV not found")
	}
	if len(tlv.Value) < 1 {
		return 0, fmt.Errorf("transport network registration status TLV too short: %d", len(tlv.Value))
	}
	return WMSTransportNetworkRegistration(tlv.Value[0]), nil
}

func ParseWMSSMSCAddressIndication(packet *Packet) (*WMSSMSCAddress, error) {
	tlv := FindTLV(packet.TLVs, 0x01)
	if tlv == nil {
		return nil, fmt.Errorf("smsc address TLV not found")
	}
	return parseWMSSMSCAddressValue(tlv.Value)
}

func ParseWMSTransportNetworkRegistrationStatusIndication(packet *Packet) (WMSTransportNetworkRegistration, error) {
	tlv := FindTLV(packet.TLVs, 0x01)
	if tlv == nil {
		return 0, fmt.Errorf("transport network registration indication TLV not found")
	}
	if len(tlv.Value) < 1 {
		return 0, fmt.Errorf("transport network registration indication TLV too short: %d", len(tlv.Value))
	}
	return WMSTransportNetworkRegistration(tlv.Value[0]), nil
}

func parseWMSSupportedMessagesResponse(resp *Packet) ([]uint8, error) {
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

func parseWMSSMSCAddressValue(value []byte) (*WMSSMSCAddress, error) {
	if len(value) < 4 {
		return nil, fmt.Errorf("smsc address TLV too short: %d", len(value))
	}
	digitsLen := int(value[3])
	if len(value) < 4+digitsLen {
		return nil, fmt.Errorf("smsc address TLV truncated: need %d, have %d", 4+digitsLen, len(value))
	}
	return &WMSSMSCAddress{
		Type:   string(value[:3]),
		Digits: string(value[4 : 4+digitsLen]),
	}, nil
}

func parseRouteList(value []byte) ([]WMSRoute, error) {
	if len(value) < 2 {
		return nil, fmt.Errorf("route list TLV too short: %d", len(value))
	}

	count := int(binary.LittleEndian.Uint16(value[0:2]))
	expected := 2 + count*4
	if len(value) < expected {
		return nil, fmt.Errorf("route list TLV truncated: need %d, have %d", expected, len(value))
	}

	routes := make([]WMSRoute, 0, count)
	offset := 2
	for i := 0; i < count; i++ {
		routes = append(routes, WMSRoute{
			MessageType:   value[offset],
			MessageClass:  value[offset+1],
			StorageType:   value[offset+2],
			ReceiptAction: value[offset+3],
		})
		offset += 4
	}
	return routes, nil
}
