package qmi

import (
	"encoding/binary"
	"fmt"
)

// ============================================================================
// QMUX Service Types (from QCQMI.h) / QMUX服务类型 (来自QCQMI.h)
// ============================================================================

const (
	ServiceControl uint8 = 0x00 // CTL - Control
	ServiceWDS     uint8 = 0x01 // WDS - Wireless Data Service
	ServiceDMS     uint8 = 0x02 // DMS - Device Management Service
	ServiceNAS     uint8 = 0x03 // NAS - Network Access Stratum
	ServiceQOS     uint8 = 0x04 // QOS - Quality of Service
	ServiceWMS     uint8 = 0x05 // WMS - Wireless Messaging Service
	ServicePDS     uint8 = 0x06 // PDS - Position Determination Service
	ServiceAUTH    uint8 = 0x07 // AUTH - Authentication
	ServiceVOICE   uint8 = 0x09 // VOICE - Voice Service
	ServiceCAT2    uint8 = 0x0A // CAT2 - Card Application Toolkit
	ServiceUIM     uint8 = 0x0B // UIM - User Identity Module
	ServicePBM     uint8 = 0x0C // PBM - Phonebook Manager
	ServiceIMS     uint8 = 0x12 // IMS - IP Multimedia Subsystem Settings
	ServiceWDA     uint8 = 0x1A // WDA - Wireless Data Admin
	ServiceIMSP    uint8 = 0x1F // IMSP - IMS Presence Service
	ServiceWDSIPv6 uint8 = 0x1B // WDS for IPv6 (internal use)
	ServiceIMSA    uint8 = 0x21 // IMSA - IMS Application Service
	ServiceCOEX    uint8 = 0x22 // COEX - Coexistence
)

// ============================================================================
// WDS Message IDs (from QCQMUX.h) / WDS 消息ID (来自QCQMUX.h)
// ============================================================================

const (
	WDSSetEventReport        uint16 = 0x0001 // QMIWDS_SET_EVENT_REPORT_REQ
	WDSEventReportInd        uint16 = 0x0001 // QMIWDS_EVENT_REPORT_IND
	WDSStartNetworkInterface uint16 = 0x0020 // QMIWDS_START_NETWORK_INTERFACE_REQ
	WDSStopNetworkInterface  uint16 = 0x0021 // QMIWDS_STOP_NETWORK_INTERFACE_REQ
	WDSGetPktSrvcStatus      uint16 = 0x0022 // QMIWDS_GET_PKT_SRVC_STATUS_REQ
	WDSGetPktSrvcStatusInd   uint16 = 0x0022 // QMIWDS_GET_PKT_SRVC_STATUS_IND
	WDSGetCurrentChannelRate uint16 = 0x0023 // QMIWDS_GET_CURRENT_CHANNEL_RATE_REQ
	WDSGetPktStatistics      uint16 = 0x0024 // QMIWDS_GET_PKT_STATISTICS_REQ
	WDSGetProfileList        uint16 = 0x002A // QMIWDS_GET_PROFILE_LIST_REQ
	WDSGetProfileSettings    uint16 = 0x002B // QMIWDS_GET_PROFILE_SETTINGS_REQ
	WDSGetDefaultSettings    uint16 = 0x002C // QMIWDS_GET_DEFAULT_SETTINGS_REQ
	WDSGetRuntimeSettings    uint16 = 0x002D // QMIWDS_GET_RUNTIME_SETTINGS_REQ
	WDSSetClientIPFamilyPref uint16 = 0x004D // QMIWDS_SET_CLIENT_IP_FAMILY_PREF_REQ
	WDSBindMuxDataPort       uint16 = 0x00A2 // QMIWDS_BIND_MUX_DATA_PORT_REQ
)

// ============================================================================
// WMS Message IDs
// ============================================================================

const (
	WMSSetEventReport                        uint16 = 0x0001 // QMIWMS_SET_EVENT_REPORT_REQ
	WMSEventReportInd                        uint16 = 0x0001 // QMIWMS_EVENT_REPORT_IND
	WMSRawSend                               uint16 = 0x0020 // QMIWMS_RAW_SEND_REQ
	WMSRawWrite                              uint16 = 0x0021 // QMIWMS_RAW_WRITE_REQ
	WMSRawRead                               uint16 = 0x0022 // QMIWMS_RAW_READ_REQ
	WMSDelete                                uint16 = 0x0024 // QMIWMS_DELETE_REQ
	WMSListMessages                          uint16 = 0x0031 // QMIWMS_LIST_MESSAGES_REQ
	WMSSMSCAddressInd                        uint16 = 0x0046 // QMIWMS_SMSC_ADDRESS_IND
	WMSTransportNetworkRegistrationStatusInd uint16 = 0x004B // QMIWMS_TRANSPORT_NW_REG_STATUS_IND
)

// ============================================================================
// NAS Message IDs
// ============================================================================

const (
	NASReset                     uint16 = 0x0000 // QMINAS_RESET_REQ
	NASAbort                     uint16 = 0x0001 // QMINAS_ABORT_REQ
	NASSetEventReport            uint16 = 0x0002 // QMINAS_SET_EVENT_REPORT_REQ
	NASEventReportInd            uint16 = 0x0002 // QMINAS_EVENT_REPORT_IND
	NASRegisterIndications       uint16 = 0x0003 // QMINAS_REGISTER_INDICATIONS_REQ
	NASGetSignalStrength         uint16 = 0x0020 // QMINAS_GET_SIGNAL_STRENGTH_REQ
	NASPerformNetworkScan        uint16 = 0x0021 // QMINAS_NETWORK_SCAN_REQ
	NASInitiateNetworkRegister   uint16 = 0x0022 // QMINAS_INITIATE_NETWORK_REGISTER_REQ
	NASAttachDetach              uint16 = 0x0023 // QMINAS_ATTACH_DETACH_REQ
	NASGetServingSystem          uint16 = 0x0024 // QMINAS_GET_SERVING_SYSTEM_REQ
	NASServingSystemInd          uint16 = 0x0024 // QMINAS_SERVING_SYSTEM_IND
	NASGetOperatorName           uint16 = 0x0039 // QMINAS_GET_OPERATOR_NAME_REQ
	NASOperatorNameInd           uint16 = 0x003A // QMINAS_OPERATOR_NAME_IND
	NASGetPLMNName               uint16 = 0x0044 // QMINAS_GET_PLMN_NAME_REQ
	NASGetSysInfo                uint16 = 0x004D // QMINAS_GET_SYS_INFO_REQ
	NASNetworkTimeInd            uint16 = 0x004C // QMINAS_NETWORK_TIME_IND
	NASSysInfoInd                uint16 = 0x004E // QMINAS_SYS_INFO_IND
	NASGetSignalInfo             uint16 = 0x004F // QMINAS_GET_SIGNAL_INFO_REQ
	NASConfigSignalInfo          uint16 = 0x0050 // QMINAS_CONFIG_SIGNAL_INFO_REQ
	NASSignalInfoInd             uint16 = 0x0051 // QMINAS_SIGNAL_INFO_IND
	NASConfigSignalInfoV2        uint16 = 0x006C // QMINAS_CONFIG_SIGNAL_INFO_V2_REQ
	NASNetworkRejectInd          uint16 = 0x0068 // QMINAS_NETWORK_REJECT_IND
	NASGetNetworkTime            uint16 = 0x007D // QMINAS_GET_NETWORK_TIME_REQ
	NASIncrementalNetworkScan    uint16 = 0x0085 // QMINAS_INCREMENTAL_NETWORK_SCAN_REQ
	NASIncrementalNetworkScanInd uint16 = 0x0085 // QMINAS_INCREMENTAL_NETWORK_SCAN_IND
)

// ============================================================================
// DMS Message IDs
// ============================================================================

const (
	DMSGetDeviceSerialNumbers uint16 = 0x0025 // QMIDMS_GET_DEVICE_SERIAL_NUMBERS_REQ
	DMSGetDeviceRevID         uint16 = 0x0023 // QMIDMS_GET_DEVICE_REV_ID_REQ
	DMSUIMGetState            uint16 = 0x0044 // QMIDMS_UIM_GET_STATE_REQ
	DMSUIMGetPINStatus        uint16 = 0x002B // QMIDMS_UIM_GET_PIN_STATUS_REQ
	DMSUIMVerifyPIN           uint16 = 0x0028 // QMIDMS_UIM_VERIFY_PIN_REQ
	DMSSetOperatingMode       uint16 = 0x002E // QMIDMS_SET_OPERATING_MODE_REQ
	DMSGetOperatingMode       uint16 = 0x002D // QMIDMS_GET_OPERATING_MODE_REQ
)

// ============================================================================
// UIM Message IDs
// ============================================================================

const (
	UIMVerifyPIN           uint16 = 0x0026 // QMIUIM_VERIFY_PIN_REQ
	UIMGetCardStatus       uint16 = 0x002F // QMIUIM_GET_CARD_STATUS_REQ
	UIMOpenLogicalChannel  uint16 = 0x0042 // QMIUIM_OPEN_LOGICAL_CHANNEL_REQ
	UIMCloseLogicalChannel uint16 = 0x003F // QMIUIM_CLOSE_LOGICAL_CHANNEL_REQ
	UIMSendAPDU            uint16 = 0x003B // QMIUIM_SEND_APDU_REQ
)

// ============================================================================
// CTL (Control Service) Message IDs
// ============================================================================

const (
	CTLGetVersionInfo    uint16 = 0x0021 // QMICTL_GET_VERSION_REQ
	CTLGetClientID       uint16 = 0x0022 // QMICTL_GET_CLIENT_ID_REQ
	CTLReleaseClientID   uint16 = 0x0023 // QMICTL_RELEASE_CLIENT_ID_REQ
	CTLSetDataFormat     uint16 = 0x0026 // QMICTL_SET_DATA_FORMAT_REQ
	CTLRevokeClientIDInd uint16 = 0x0024 // QMICTL_REVOKE_CLIENT_ID_IND
	CTLInternalProxyOpen uint16 = 0xFF00 // libqmi qmi-proxy internal open request
	TLVProxyDevicePath   uint8  = 0x01   // CTLInternalProxyOpen device path TLV
)

// ============================================================================
// Connection Status Constants
// ============================================================================

const (
	PktDataUnknown        uint8 = 0x00
	PktDataDisconnected   uint8 = 0x01
	PktDataConnected      uint8 = 0x02
	PktDataSuspended      uint8 = 0x03
	PktDataAuthenticating uint8 = 0x04
)

// IP Family Constants / IP族常量
const (
	IpFamilyV4 uint8 = 0x04
	IpFamilyV6 uint8 = 0x06
)

// ============================================================================
// QMUX Header Structure (matches C struct exactly) / QMUX头结构 (与C结构完全匹配)
// ============================================================================

// QmuxHeader represents the 6-byte QMUX header / QmuxHeader代表6字节QMUX头
// Offset 0: IFType (always 0x01) / 偏移0: 接口类型 (QMUX始终为0x01)
// Offset 1-2: Length (little-endian, total length after IFType) / 偏移1-2: 长度 (小端序，IFType之后的总长度)
// Offset 3: ControlFlags (0x00 for normal, 0x80 for service) / 偏移3: 控制标志 (0x00为普通，0x80为服务)
// Offset 4: ServiceType / 偏移4: 服务类型
// Offset 5: ClientID / 偏移5: 客户端ID
type QmuxHeader struct {
	IFType       uint8
	Length       uint16
	ControlFlags uint8
	ServiceType  uint8
	ClientID     uint8
}

const QmuxHeaderSize = 6

func (h *QmuxHeader) Marshal() []byte {
	buf := make([]byte, QmuxHeaderSize)
	buf[0] = 0x01 // IFType is always 0x01 for QMUX
	binary.LittleEndian.PutUint16(buf[1:3], h.Length)
	buf[3] = h.ControlFlags
	buf[4] = h.ServiceType
	buf[5] = h.ClientID
	return buf
}

func UnmarshalQmuxHeader(data []byte) (*QmuxHeader, error) {
	if len(data) < QmuxHeaderSize {
		return nil, fmt.Errorf("data too short for QMUX header: %d", len(data))
	}
	if data[0] != 0x01 {
		return nil, fmt.Errorf("invalid IFType: 0x%02x", data[0])
	}
	return &QmuxHeader{
		IFType:       data[0],
		Length:       binary.LittleEndian.Uint16(data[1:3]),
		ControlFlags: data[3],
		ServiceType:  data[4],
		ClientID:     data[5],
	}, nil
}

// ============================================================================
// CTL Service Header (6 bytes, different from regular services) / CTL服务头 (6字节，不同于普通服务)
// ============================================================================

// CTLHeader is used for Control Service (Service 0) / CTLHeader用于控制服务 (服务0)
// Offset 0: ControlFlags (0x00 for request, 0x01 for response, 0x02 for indication) / 偏移0: 控制标志 (0x00请求, 0x01响应, 0x02指示)
// Offset 1: TransactionID (1 byte for CTL!) / 偏移1: 事务ID (CTL服务仅1字节!)
// Offset 2-3: MessageID (little-endian) / 偏移2-3: 消息ID (小端序)
// Offset 4-5: Length (little-endian) / 偏移4-5: 长度 (小端序)
type CTLHeader struct {
	ControlFlags  uint8
	TransactionID uint8
	MessageID     uint16
	Length        uint16
}

const CTLHeaderSize = 6

func (h *CTLHeader) Marshal() []byte {
	buf := make([]byte, CTLHeaderSize)
	buf[0] = h.ControlFlags
	buf[1] = h.TransactionID
	binary.LittleEndian.PutUint16(buf[2:4], h.MessageID)
	binary.LittleEndian.PutUint16(buf[4:6], h.Length)
	return buf
}

func UnmarshalCTLHeader(data []byte) (*CTLHeader, error) {
	if len(data) < CTLHeaderSize {
		return nil, fmt.Errorf("data too short for CTL header: %d", len(data))
	}
	return &CTLHeader{
		ControlFlags:  data[0],
		TransactionID: data[1],
		MessageID:     binary.LittleEndian.Uint16(data[2:4]),
		Length:        binary.LittleEndian.Uint16(data[4:6]),
	}, nil
}

// ============================================================================
// Service Header (7 bytes, for services other than CTL) / 服务头 (7字节，用于除CTL外的服务)
// ============================================================================

// ServiceHeader is used for all services except Control (Service 0) / ServiceHeader用于除控制服务(Service 0)外的所有服务
// Offset 0: ControlFlags / 偏移0: 控制标志
// Offset 1-2: TransactionID (2 bytes, little-endian) / 偏移1-2: 事务ID (2字节，小端序)
// Offset 3-4: MessageID (little-endian) / 偏移3-4: 消息ID (小端序)
// Offset 5-6: Length (little-endian) / 偏移5-6: 长度 (小端序)
type ServiceHeader struct {
	ControlFlags  uint8
	TransactionID uint16
	MessageID     uint16
	Length        uint16
}

const ServiceHeaderSize = 7

func (h *ServiceHeader) Marshal() []byte {
	buf := make([]byte, ServiceHeaderSize)
	buf[0] = h.ControlFlags
	binary.LittleEndian.PutUint16(buf[1:3], h.TransactionID)
	binary.LittleEndian.PutUint16(buf[3:5], h.MessageID)
	binary.LittleEndian.PutUint16(buf[5:7], h.Length)
	return buf
}

func UnmarshalServiceHeader(data []byte) (*ServiceHeader, error) {
	if len(data) < ServiceHeaderSize {
		return nil, fmt.Errorf("data too short for Service header: %d", len(data))
	}
	return &ServiceHeader{
		ControlFlags:  data[0],
		TransactionID: binary.LittleEndian.Uint16(data[1:3]),
		MessageID:     binary.LittleEndian.Uint16(data[3:5]),
		Length:        binary.LittleEndian.Uint16(data[5:7]),
	}, nil
}

// ============================================================================
// TLV (Type-Length-Value) Structure
// ============================================================================

// TLV represents a single Type-Length-Value entry / TLV 代表单个 Type-Length-Value 条目
type TLV struct {
	Type  uint8
	Value []byte
}

// TLVMeta contains a TLV type/length pair without exposing the raw payload.
type TLVMeta struct {
	Type   uint8
	Length int
}

const TLVHeaderSize = 3 // 1 byte type + 2 bytes length / 1字节类型 + 2字节长度

func (t *TLV) Marshal() []byte {
	buf := make([]byte, TLVHeaderSize+len(t.Value))
	buf[0] = t.Type
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(t.Value)))
	copy(buf[3:], t.Value)
	return buf
}

func UnmarshalTLV(data []byte) (*TLV, int, error) {
	if len(data) < TLVHeaderSize {
		return nil, 0, fmt.Errorf("data too short for TLV header")
	}
	t := data[0]
	l := binary.LittleEndian.Uint16(data[1:3])
	if len(data) < int(TLVHeaderSize)+int(l) {
		return nil, 0, fmt.Errorf("TLV value truncated: need %d, have %d", l, len(data)-TLVHeaderSize)
	}
	return &TLV{
		Type:  t,
		Value: data[TLVHeaderSize : TLVHeaderSize+int(l)],
	}, TLVHeaderSize + int(l), nil
}

// ParseTLVs parses multiple TLVs from a byte slice / 从字节切片解析多个TLV
func ParseTLVs(data []byte) ([]TLV, error) {
	var tlvs []TLV
	offset := 0
	for offset < len(data) {
		if len(data)-offset < TLVHeaderSize {
			allZero := true
			for _, b := range data[offset:] {
				if b != 0x00 {
					allZero = false
					break
				}
			}
			if allZero {
				break
			}
		}
		tlv, consumed, err := UnmarshalTLV(data[offset:])
		if err != nil {
			return tlvs, err
		}
		tlvs = append(tlvs, *tlv)
		offset += consumed
	}
	return tlvs, nil
}

// FindTLV finds a TLV by type in a slice / 在切片中查找指定类型的TLV
func FindTLV(tlvs []TLV, tlvType uint8) *TLV {
	for i := range tlvs {
		if tlvs[i].Type == tlvType {
			return &tlvs[i]
		}
	}
	return nil
}

// ============================================================================
// QMI Packet (unified representation)
// ============================================================================

// Packet represents a complete QMI message / Packet代表一个完整的QMI消息
type Packet struct {
	ServiceType   uint8
	ClientID      uint8
	TransactionID uint16 // For CTL, only lower 8 bits used / 对于CTL，仅使用低8位
	MessageID     uint16
	IsIndication  bool // True if this is an unsolicited indication / 如果是不请自来的指示消息则为真
	TLVs          []TLV
}

// Marshal serializes the packet to bytes / 将数据包序列化为字节
func (p *Packet) Marshal() []byte {
	// Serialize TLVs
	var tlvBytes []byte
	for _, t := range p.TLVs {
		tlvBytes = append(tlvBytes, t.Marshal()...)
	}

	var body []byte
	if p.ServiceType == ServiceControl {
		// CTL uses 6-byte header / CTL使用6字节头
		ctlH := CTLHeader{
			ControlFlags:  0x00, // Request
			TransactionID: uint8(p.TransactionID & 0xFF),
			MessageID:     p.MessageID,
			Length:        uint16(len(tlvBytes)),
		}
		body = append(ctlH.Marshal(), tlvBytes...)
	} else {
		// Regular services use 7-byte header / 普通服务使用7字节头
		svcH := ServiceHeader{
			ControlFlags:  0x00,
			TransactionID: p.TransactionID,
			MessageID:     p.MessageID,
			Length:        uint16(len(tlvBytes)),
		}
		body = append(svcH.Marshal(), tlvBytes...)
	}

	// QMUX header
	// Length = LengthField(2) + CtlFlags(1) + QMIType(1) + ClientId(1) + SDU
	// This matches the C version where QMIHdr.Length = (TotalPacketSize - 1) / 这与C版本匹配，其中QMIHdr.Length = (总包大小 - 1)
	qmuxH := QmuxHeader{
		IFType:       0x01,
		Length:       uint16(len(body) + 5), // +5 for Length, CtlFlags, ServiceType, ClientID
		ControlFlags: 0x00,
		ServiceType:  p.ServiceType,
		ClientID:     p.ClientID,
	}

	return append(qmuxH.Marshal(), body...)
}

// UnmarshalPacket parses a complete QMI packet from bytes / 从字节解析完整的QMI数据包
func UnmarshalPacket(data []byte) (*Packet, error) {
	qmuxH, err := UnmarshalQmuxHeader(data)
	if err != nil {
		return nil, err
	}

	expectedTotal := int(qmuxH.Length) + 1
	if expectedTotal < QmuxHeaderSize {
		return nil, fmt.Errorf("invalid QMUX length: %d", qmuxH.Length)
	}
	if len(data) < expectedTotal {
		return nil, fmt.Errorf("packet truncated: need %d, have %d", expectedTotal, len(data))
	}
	if len(data) > expectedTotal {
		data = data[:expectedTotal]
	}

	p := &Packet{
		ServiceType: qmuxH.ServiceType,
		ClientID:    qmuxH.ClientID,
	}

	body := data[QmuxHeaderSize:]

	if qmuxH.ServiceType == ServiceControl {
		if len(body) < CTLHeaderSize {
			return nil, fmt.Errorf("body too short for CTL header")
		}
		ctlH, err := UnmarshalCTLHeader(body)
		if err != nil {
			return nil, err
		}
		p.TransactionID = uint16(ctlH.TransactionID)
		p.MessageID = ctlH.MessageID
		// CTL: 0x01 = Response, 0x02 = Indication
		p.IsIndication = (ctlH.ControlFlags & 0x02) != 0

		tlvData := body[CTLHeaderSize:]
		if int(ctlH.Length) > len(tlvData) {
			return nil, fmt.Errorf("CTL TLV data truncated: need %d, have %d", ctlH.Length, len(tlvData))
		}
		p.TLVs, err = ParseTLVs(tlvData[:ctlH.Length])
		if err != nil {
			return nil, err
		}
	} else {
		if len(body) < ServiceHeaderSize {
			return nil, fmt.Errorf("body too short for Service header")
		}
		svcH, err := UnmarshalServiceHeader(body)
		if err != nil {
			return nil, err
		}
		p.TransactionID = svcH.TransactionID
		p.MessageID = svcH.MessageID
		// Services: 0x02 = Response, 0x04 = Indication
		// Many modems use bit 2 (0x04) for indications
		p.IsIndication = (svcH.ControlFlags & 0x04) != 0

		tlvData := body[ServiceHeaderSize:]
		if int(svcH.Length) > len(tlvData) {
			return nil, fmt.Errorf("service TLV data truncated: need %d, have %d", svcH.Length, len(tlvData))
		}
		p.TLVs, err = ParseTLVs(tlvData[:svcH.Length])
		if err != nil {
			return nil, err
		}
	}

	return p, nil
}

// GetResultCode extracts the result code from TLV 0x02 / 从TLV 0x02提取结果代码
func (p *Packet) GetResultCode() (result uint16, err uint16, ok bool) {
	tlv := FindTLV(p.TLVs, 0x02)
	if tlv == nil || len(tlv.Value) < 4 {
		return 0, 0, false
	}
	result = binary.LittleEndian.Uint16(tlv.Value[0:2])
	err = binary.LittleEndian.Uint16(tlv.Value[2:4])
	return result, err, true
}

// IsSuccess checks if the response indicates success / 检查响应是否表示成功
func (p *Packet) IsSuccess() bool {
	result, _, ok := p.GetResultCode()
	return ok && result == 0
}

// CheckResult checks for QMI error and returns it as a Go error / 检查QMI错误并将其作为Go错误返回
func (p *Packet) CheckResult() error {
	result, errCode, ok := p.GetResultCode()
	if !ok {
		return fmt.Errorf("response missing result TLV")
	}
	if result != 0 {
		return &QMIError{
			Service:   p.ServiceType,
			MessageID: p.MessageID,
			Result:    result,
			ErrorCode: errCode,
		}
	}
	return nil
}

// ============================================================================
// Helper functions for building TLVs / 用于构建TLV的辅助函数
// ============================================================================

func NewTLVUint8(t uint8, v uint8) TLV {
	return TLV{Type: t, Value: []byte{v}}
}

func NewTLVUint16(t uint8, v uint16) TLV {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, v)
	return TLV{Type: t, Value: buf}
}

func NewTLVUint32(t uint8, v uint32) TLV {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return TLV{Type: t, Value: buf}
}

func NewTLVString(t uint8, s string) TLV {
	return TLV{Type: t, Value: []byte(s)}
}
