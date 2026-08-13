package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
)

// WDA Message IDs / WDA消息ID
const (
	// ServiceWDA is defined in frame.go / ServiceWDA 在 frame.go 中定义

	// WDA message IDs
	WDASetDataFormat     uint16 = 0x0020
	WDAGetDataFormat     uint16 = 0x0021
	WDASetQMAPSettings   uint16 = 0x002B
	WDAGetQMAPSettings   uint16 = 0x002C
	WDASetLoopbackConfig uint16 = 0x002F
)

// Data Format Modes / 数据格式模式
const (
	DataFormatQOSFlowHeader     uint8 = 1 << 0
	DataFormatLinkProtEth       uint8 = 0
	DataFormatLinkProtIP        uint8 = 1 << 1
	DataFormatUlDataAggEnabled  uint8 = 1 << 2
	DataFormatUlDataAggDisabled uint8 = 0
	DataFormatDlDataAggEnabled  uint8 = 1 << 3
	DataFormatDlDataAggDisabled uint8 = 0
	DataFormatNdpSigEnabled     uint8 = 1 << 4 // New Data Path Signature / 新数据路径签名
)

// WDAService implements the QMI WDA service / WDAService 实现 QMI WDA 服务
type WDAService struct {
	client   *Client
	clientID uint8
}

// NewWDAService creates a new WDA client / NewWDAService创建一个新的WDA客户端
func NewWDAService(client *Client) (*WDAService, error) {
	return NewWDAServiceWithContext(context.Background(), client)
}

func NewWDAServiceWithContext(ctx context.Context, client *Client) (*WDAService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceWDA)
	if err != nil {
		return nil, err
	}
	return &WDAService{client: client, clientID: clientID}, nil
}

func (s *WDAService) Close() error {
	return s.client.ReleaseClientID(ServiceWDA, s.clientID)
}

func (s *WDAService) ClientID() uint8 {
	return s.clientID
}

// DataFormat configures the data format for the connection / DataFormat 配置连接的数据格式
type DataFormat struct {
	LinkProtocol      uint32
	UlDataAggregation uint32
	DlDataAggregation uint32
}

type DataFormatDetails struct {
	QOSSetting uint8

	LinkProtocol      uint32
	UlDataAggregation uint32
	DlDataAggregation uint32

	DlMaxDatagrams uint32
	DlMaxSize      uint32

	EndpointType uint32
	EndpointID   uint32
}

// QMAPSettings configures QMAP (Qualcomm Mobile Access Point) parameters / QMAPSettings 配置 QMAP 参数
type QMAPSettings struct {
	InBandFlowControl uint8 // 0x00: Disabled, 0x01: Enabled
}

// LoopbackConfig configures loopback state / LoopbackConfig 配置回环状态
type LoopbackConfig struct {
	State             uint8  // 0x00: Disabled, 0x01: Enabled
	ReplicationFactor uint32 // Number of times to replicate the packet
}

// SetDataFormat sets the data format (e.g. Raw IP) / SetDataFormat设置数据格式 (例如 原始IP)
func (s *WDAService) SetDataFormat(ctx context.Context, format DataFormat) error {
	var endpointTLV *TLV
	if current, err := s.GetDataFormatDetails(ctx); err == nil {
		if current.EndpointType != 0 && current.EndpointID != 0 {
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint32(buf[0:4], current.EndpointType)
			binary.LittleEndian.PutUint32(buf[4:8], current.EndpointID)
			endpointTLV = &TLV{Type: 0x17, Value: buf}
		}
	}

	bufLink := make([]byte, 4)
	binary.LittleEndian.PutUint32(bufLink, format.LinkProtocol)

	bufUl := make([]byte, 4)
	binary.LittleEndian.PutUint32(bufUl, format.UlDataAggregation)

	bufDl := make([]byte, 4)
	binary.LittleEndian.PutUint32(bufDl, format.DlDataAggregation)

	baseTLVs := []TLV{
		{Type: 0x10, Value: []byte{0x00}},
		{Type: 0x11, Value: bufLink},
		{Type: 0x12, Value: bufUl},
		{Type: 0x13, Value: bufDl},
	}

	attempts := [][]TLV{
		baseTLVs,
	}
	if endpointTLV != nil {
		attempts = append([][]TLV{append(append([]TLV{}, baseTLVs...), *endpointTLV)}, attempts...)
	}

	var lastErr error
	for _, tlvs := range attempts {
		resp, err := s.client.SendRequest(ctx, ServiceWDA, s.clientID, WDASetDataFormat, tlvs)
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

// GetDataFormat gets the current data format configuration / GetDataFormat 获取当前的数据格式配置
func (s *WDAService) GetDataFormat(ctx context.Context) (*DataFormat, error) {
	d, err := s.GetDataFormatDetails(ctx)
	if err != nil {
		return nil, err
	}
	return &DataFormat{
		LinkProtocol:      d.LinkProtocol,
		UlDataAggregation: d.UlDataAggregation,
		DlDataAggregation: d.DlDataAggregation,
	}, nil
}

func (s *WDAService) GetDataFormatDetails(ctx context.Context) (*DataFormatDetails, error) {
	resp, err := s.client.SendRequest(ctx, ServiceWDA, s.clientID, WDAGetDataFormat, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, err
	}

	format := &DataFormatDetails{}

	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		format.QOSSetting = tlv.Value[0]
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 4 {
		format.LinkProtocol = binary.LittleEndian.Uint32(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 4 {
		format.UlDataAggregation = binary.LittleEndian.Uint32(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 4 {
		format.DlDataAggregation = binary.LittleEndian.Uint32(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x15); tlv != nil && len(tlv.Value) >= 4 {
		format.DlMaxDatagrams = binary.LittleEndian.Uint32(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x16); tlv != nil && len(tlv.Value) >= 4 {
		format.DlMaxSize = binary.LittleEndian.Uint32(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x17); tlv != nil && len(tlv.Value) >= 4 {
		format.EndpointType = binary.LittleEndian.Uint32(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x18); tlv != nil && len(tlv.Value) >= 4 {
		format.EndpointID = binary.LittleEndian.Uint32(tlv.Value)
	}

	return format, nil
}

// SetQMAPSettings configures QMAP settings / SetQMAPSettings 配置 QMAP 设置
func (s *WDAService) SetQMAPSettings(ctx context.Context, settings QMAPSettings) error {
	var tlvs []TLV

	// TLV 0x10: In-Band Flow Control / 带内流控
	buf := make([]byte, 1)
	buf[0] = settings.InBandFlowControl
	tlvs = append(tlvs, TLV{Type: 0x10, Value: buf})

	resp, err := s.client.SendRequest(ctx, ServiceWDA, s.clientID, WDASetQMAPSettings, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

// GetQMAPSettings gets current QMAP settings / GetQMAPSettings 获取当前 QMAP 设置
func (s *WDAService) GetQMAPSettings(ctx context.Context) (*QMAPSettings, error) {
	resp, err := s.client.SendRequest(ctx, ServiceWDA, s.clientID, WDAGetQMAPSettings, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, err
	}

	settings := &QMAPSettings{}

	// TLV 0x10: In-Band Flow Control / 带内流控
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		settings.InBandFlowControl = tlv.Value[0]
	}

	return settings, nil
}

// SetLoopbackConfig configures loopback mode (diagnostic use) / SetLoopbackConfig 配置回环模式 (诊断用途)
func (s *WDAService) SetLoopbackConfig(ctx context.Context, config LoopbackConfig) error {
	var tlvs []TLV

	// TLV 0x01: Loopback State / 回环状态
	bufState := make([]byte, 1)
	bufState[0] = config.State
	tlvs = append(tlvs, TLV{Type: 0x01, Value: bufState})

	// TLV 0x10: Replication Factor / 复制因子
	if config.ReplicationFactor > 0 {
		bufFactor := make([]byte, 4)
		binary.LittleEndian.PutUint32(bufFactor, config.ReplicationFactor)
		tlvs = append(tlvs, TLV{Type: 0x10, Value: bufFactor})
	}

	resp, err := s.client.SendRequest(ctx, ServiceWDA, s.clientID, WDASetLoopbackConfig, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		if qe := GetQMIError(err); qe != nil && qe.ErrorCode == QMIErrInvalidQmiCmd {
			return &NotSupportedError{Operation: "loopback"}
		}
		return fmt.Errorf("set loopback config failed: %w", err)
	}
	return nil
}

// DataFormatMode constants for Link Protocol (TLV 0x11) / Link Protocol (TLV 0x11) 的 DataFormatMode 常量
const (
	LinkProtocolEthernet uint32 = 0x01 // Sometime 0x02? Need to verify spec vs modem. / 有时是0x02? 需要针对modem验证规范。
	LinkProtocolIP       uint32 = 0x02
)

// Actually, looking at QCQMUX.h isn't super clear on values. / 实际上，查看QCQMUX.h关于值的说明并不是很清楚。
// Standard QMI: / 标准QMI:
// 0x01: QMI_WDA_LINK_LAYER_PROTOCOL_802_3 (Ethernet) / 0x01: QMI_WDA_LINK_LAYER_PROTOCOL_802_3 (以太网)
// 0x02: QMI_WDA_LINK_LAYER_PROTOCOL_RAW_IP (IP) / 0x02: QMI_WDA_LINK_LAYER_PROTOCOL_RAW_IP (IP)
