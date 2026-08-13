package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"
)

// ============================================================================
// NAS Registration States / NAS注册状态
// ============================================================================

type RegistrationState uint8

const (
	RegStateNotRegistered RegistrationState = 0
	RegStateRegistered    RegistrationState = 1
	RegStateSearching     RegistrationState = 2
	RegStateDenied        RegistrationState = 3
	RegStateUnknown       RegistrationState = 4
	RegStateRoaming       RegistrationState = 5
)

func (r RegistrationState) String() string {
	switch r {
	case RegStateNotRegistered:
		return "not_registered"
	case RegStateRegistered:
		return "registered"
	case RegStateSearching:
		return "searching"
	case RegStateDenied:
		return "denied"
	case RegStateRoaming:
		return "roaming"
	default:
		return "unknown"
	}
}

// ============================================================================
// NAS Service wrapper / NAS服务包装器
// ============================================================================

const (
	NASSetTechnologyPreference      uint16 = 0x002A
	NASGetTechnologyPreference      uint16 = 0x002B
	NASGetRFBandInfo                uint16 = 0x0031
	NASSetSystemSelectionPreference uint16 = 0x0033
	NASGetSystemSelectionPreference uint16 = 0x0034
	NASGetCellLocationInfo          uint16 = 0x0043
	NASForceNetworkSearch           uint16 = 0x0067
	/* Defined in frame.go / 在 frame.go 中定义
	NASRegisterIndications      uint16 = 0x0003
	NASGetServingSystem  uint16 = 0x0024
	NASGetSignalStrength uint16 = 0x0020
	NASInitiateNetworkRegister  uint16 = 0x0022
	NASAttachDetach             uint16 = 0x0023
	NASPerformNetworkScan       uint16 = 0x0021
	NASGetOperatorName          uint16 = 0x0039
	NASGetPLMNName              uint16 = 0x0044
	NASGetSysInfo        uint16 = 0x004D
	NASGetSignalInfo            uint16 = 0x004F
	NASConfigSignalInfoV2       uint16 = 0x006C
	NASGetNetworkTime           uint16 = 0x007D
	*/
)

const (
	NASTechPreferenceAuto        uint16 = 0
	NASTechPreference3GPP2       uint16 = 1 << 0
	NASTechPreference3GPP        uint16 = 1 << 1
	NASTechPreferenceAMPSOrGSM   uint16 = 1 << 2
	NASTechPreferenceCDMAOrWCDMA uint16 = 1 << 3
	NASTechPreferenceHDR         uint16 = 1 << 4
	NASTechPreferenceLTE         uint16 = 1 << 5
)

const (
	NASPreferenceDurationPermanent     uint8 = 0x00
	NASPreferenceDurationPowerCycle    uint8 = 0x01
	NASPreferenceDurationOneCall       uint8 = 0x02
	NASPreferenceDurationOneCallOrTime uint8 = 0x03
	NASPreferenceDurationInternalCall1 uint8 = 0x04
	NASPreferenceDurationInternalCall2 uint8 = 0x05
	NASPreferenceDurationInternalCall3 uint8 = 0x06
)

const (
	NASRatModePreferenceCDMA1X     uint16 = 1 << 0
	NASRatModePreferenceCDMA1XEVDO uint16 = 1 << 1
	NASRatModePreferenceGSM        uint16 = 1 << 2
	NASRatModePreferenceUMTS       uint16 = 1 << 3
	NASRatModePreferenceLTE        uint16 = 1 << 4
	NASRatModePreferenceTDSCDMA    uint16 = 1 << 5
	NASRatModePreferenceNR5G       uint16 = 1 << 6
)

const (
	NASRoamingPreferenceOff         uint16 = 0x01
	NASRoamingPreferenceNotOff      uint16 = 0x02
	NASRoamingPreferenceNotFlashing uint16 = 0x03
	NASRoamingPreferenceAny         uint16 = 0xFF
)

const (
	NASNetworkSelectionAutomatic uint8 = 0x00
	NASNetworkSelectionManual    uint8 = 0x01
)

const (
	NASChangeDurationPowerCycle uint8 = 0x00
	NASChangeDurationPermanent  uint8 = 0x01
)

const (
	NASServiceDomainPreferenceCSOnly   uint32 = 0x00
	NASServiceDomainPreferencePSOnly   uint32 = 0x01
	NASServiceDomainPreferenceCSPS     uint32 = 0x02
	NASServiceDomainPreferencePSAttach uint32 = 0x03
	NASServiceDomainPreferencePSDetach uint32 = 0x04
)

const (
	NASVoiceDomainPreferenceCSOnly      uint32 = 0x00
	NASVoiceDomainPreferencePSOnly      uint32 = 0x01
	NASVoiceDomainPreferenceCSPreferred uint32 = 0x02
	NASVoiceDomainPreferencePSPreferred uint32 = 0x03
)

// NetworkScanResult represents a network found during scan / NetworkScanResult 代表扫描期间发现的网络
type NetworkScanResult struct {
	MCC         string
	MNC         string
	Status      uint8 // 0: Unknown, 1: Current, 2: Available, 3: Forbidden
	Description string
	RATs        []uint8
}

// SignalInfo contains detailed signal strength information / SignalInfo 包含详细的信号强度信息
type SignalInfo struct {
	// LTE specific
	LTERSRP  int16 // Reference Signal Received Power
	LTERSRQ  int16 // Reference Signal Received Quality
	LTERSSNR int16 // Signal-to-Noise Ratio

	// 5G specific
	NR5GRSRP int16
	NR5GRSRQ int16
	NR5GSINR int16
}

// SysInfo contains system information / SysInfo 包含系统信息
type SysInfo struct {
	CellID uint64
	TAC    uint16 // Tracking Area Code
	LAC    uint16 // Location Area Code
}

// RFBandInfoEntry describes one active RF band/channel tuple.
type RFBandInfoEntry struct {
	RadioInterface  uint8
	ActiveBandClass uint16
	ActiveChannel   uint32
}

// RFBandwidthInfo describes the configured downlink bandwidth for one radio interface.
type RFBandwidthInfo struct {
	RadioInterface uint8
	Bandwidth      uint32
}

// RFBandInfo groups active band/channel and bandwidth information.
type RFBandInfo struct {
	Bands      []RFBandInfoEntry
	Bandwidths []RFBandwidthInfo
}

// TechnologyPreference contains active and persistent RAT preference information.
type TechnologyPreference struct {
	ActivePreference        uint16
	ActiveDuration          uint8
	PersistentPreference    uint16
	HasPersistentPreference bool
}

// ManualNetworkSelection holds manual PLMN selection fields.
type ManualNetworkSelection struct {
	MCC              uint16
	MNC              uint16
	IncludesPCSDigit bool
}

// SystemSelectionPreference models the commonly used NAS system-selection knobs.
type SystemSelectionPreference struct {
	EmergencyMode                              bool
	HasEmergencyMode                           bool
	ModePreference                             uint16
	HasModePreference                          bool
	BandPreference                             uint64
	HasBandPreference                          bool
	CDMAPRLPreference                          uint16
	HasCDMAPRLPreference                       bool
	RoamingPreference                          uint16
	HasRoamingPreference                       bool
	LTEBandPreference                          uint64
	HasLTEBandPreference                       bool
	TDSCDMABandPreference                      uint64
	HasTDSCDMABandPreference                   bool
	NetworkSelectionPreference                 uint8
	HasNetworkSelectionPreference              bool
	ManualNetworkSelection                     ManualNetworkSelection
	HasManualNetworkSelection                  bool
	ChangeDuration                             uint8
	HasChangeDuration                          bool
	ServiceDomainPreference                    uint32
	HasServiceDomainPreference                 bool
	GSMWCDMAAcquisitionOrderPreference         uint32
	HasGSMWCDMAAcquisitionOrderPreference      bool
	AcquisitionOrderPreference                 []uint8
	DisabledModes                              uint16
	HasDisabledModes                           bool
	NetworkSelectionRegistrationRestriction    uint32
	HasNetworkSelectionRegistrationRestriction bool
	UsagePreference                            uint32
	HasUsagePreference                         bool
	VoiceDomainPreference                      uint32
	HasVoiceDomainPreference                   bool
	ExtendedLTEBandPreference                  [4]uint64
	HasExtendedLTEBandPreference               bool
	NR5GSABandPreference                       [8]uint64
	HasNR5GSABandPreference                    bool
	NR5GNSABandPreference                      [8]uint64
	HasNR5GNSABandPreference                   bool
}

// GERANCellLocationInfo contains serving GERAN cell fields.
type GERANCellLocationInfo struct {
	CellID           uint32
	MCC              string
	MNC              string
	LAC              uint16
	ARFCN            uint16
	BaseStationID    uint8
	TimingAdvance    uint32
	HasTimingAdvance bool
	RXLevel          uint16
}

// UMTSCellLocationInfo contains serving UMTS cell fields.
type UMTSCellLocationInfo struct {
	CellID                uint32
	MCC                   string
	MNC                   string
	LAC                   uint16
	UARFCN                uint16
	PrimaryScramblingCode uint16
	RSCP                  int16
	ECIO                  int16
}

// LTECellLocationInfo contains serving LTE cell fields.
type LTECellLocationInfo struct {
	UEInIdle                 bool
	MCC                      string
	MNC                      string
	TAC                      uint16
	GlobalCellID             uint32
	EARFCN                   uint16
	ServingCellID            uint16
	CellReselectionPriority  uint8
	SNonIntraSearchThreshold uint8
	ServingCellLowThreshold  uint8
	SIntraSearchThreshold    uint8
	HasIdleThresholds        bool
	TimingAdvance            uint32
	HasTimingAdvance         bool
}

// NR5GCellLocationInfo contains serving NR5G cell fields.
type NR5GCellLocationInfo struct {
	MCC            string
	MNC            string
	TAC            uint32
	GlobalCellID   uint64
	PhysicalCellID uint16
	RSRQ           int16
	RSRP           int16
	SNR            int16
	ARFCN          uint32
	HasARFCN       bool
}

// CellLocationInfo combines serving-cell details from different RAT families.
type CellLocationInfo struct {
	GERAN *GERANCellLocationInfo
	UMTS  *UMTSCellLocationInfo
	LTE   *LTECellLocationInfo
	NR5G  *NR5GCellLocationInfo
}

func GetLTEDuplexModeFromBandInfo(info *RFBandInfo) string {
	if info == nil {
		return ""
	}
	for _, band := range info.Bands {
		if band.RadioInterface != 0x08 {
			continue
		}
		if duplex := getLTEDuplexModeFromBand(band.ActiveBandClass); duplex != "" {
			return duplex
		}
	}
	return ""
}

func GetLTEDuplexModeFromCellLocation(info *CellLocationInfo) string {
	if info == nil || info.LTE == nil {
		return ""
	}
	return getLTEDuplexModeFromEARFCN(info.LTE.EARFCN)
}

func getLTEDuplexModeFromBand(band uint16) string {
	switch band {
	case 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 46, 47, 48, 50, 51, 53:
		return "TDD"
	case 1, 2, 3, 4, 5, 7, 8, 12, 13, 14, 17, 18, 19, 20, 25, 26, 27, 28, 30, 31, 65, 66, 67, 68, 70, 71:
		return "FDD"
	default:
		return ""
	}
}

func getLTEDuplexModeFromEARFCN(earfcn uint16) string {
	switch {
	case earfcn <= 599:
		return getLTEDuplexModeFromBand(1)
	case earfcn >= 600 && earfcn <= 1199:
		return getLTEDuplexModeFromBand(2)
	case earfcn >= 1200 && earfcn <= 1949:
		return getLTEDuplexModeFromBand(3)
	case earfcn >= 1950 && earfcn <= 2399:
		return getLTEDuplexModeFromBand(4)
	case earfcn >= 2400 && earfcn <= 2649:
		return getLTEDuplexModeFromBand(5)
	case earfcn >= 2750 && earfcn <= 3449:
		return getLTEDuplexModeFromBand(7)
	case earfcn >= 3450 && earfcn <= 3799:
		return getLTEDuplexModeFromBand(8)
	case earfcn >= 37750 && earfcn <= 38249:
		return getLTEDuplexModeFromBand(38)
	case earfcn >= 38250 && earfcn <= 38649:
		return getLTEDuplexModeFromBand(39)
	case earfcn >= 38650 && earfcn <= 39649:
		return getLTEDuplexModeFromBand(40)
	case earfcn >= 39650 && earfcn <= 41589:
		return getLTEDuplexModeFromBand(41)
	default:
		return ""
	}
}

// NetworkTime is one network time source returned by NAS.
type NetworkTime struct {
	Year                      uint16
	Month                     uint8
	Day                       uint8
	Hour                      uint8
	Minute                    uint8
	Second                    uint8
	DayOfWeek                 uint8
	TimezoneOffsetQuarters    int8
	DaylightSavingsAdjustment uint8
	RadioInterface            uint8
}

// NetworkTimeInfo groups 3GPP and 3GPP2 time values.
type NetworkTimeInfo struct {
	ThreeGPP     NetworkTime
	HasThreeGPP  bool
	ThreeGPP2    NetworkTime
	HasThreeGPP2 bool
}

type NASIndicationRegistration struct {
	ServingSystemChanged        bool
	SystemInfo                  bool
	NetworkTime                 bool
	SignalInfo                  bool
	OperatorName                bool
	NetworkReject               bool
	IncrementalNetworkScan      bool
	EventReportSignalThresholds []int8
}

type NASNetworkRegisterMode uint8

const (
	NASNetworkRegisterAutomatic NASNetworkRegisterMode = 0x01
	NASNetworkRegisterManual    NASNetworkRegisterMode = 0x02
)

type NASInitiateNetworkRegisterRequest struct {
	Mode              NASNetworkRegisterMode
	MCC               uint16
	MNC               uint16
	RadioAccessTech   uint8
	IncludesPCSDigit  bool
	ChangeDuration    uint8
	HasChangeDuration bool
}

type NASOperatorNameInfo struct {
	ServiceProviderName string
	OperatorStringName  string
}

type NASPLMNNameRequest struct {
	MCC              uint16
	MNC              uint16
	IncludesPCSDigit bool
}

type NASPLMNNameInfo struct {
	LongName  string
	ShortName string
}

type NASSignalInfoConfigV2 struct {
	LTEEnabled    bool
	LTERSRPDelta  uint8
	LTERSRQDelta  uint8
	LTESNRDelta   uint8
	NR5GEnabled   bool
	NR5GRSRPDelta uint8
	NR5GRSRQDelta uint8
	NR5GSINRDelta uint8
}

type NASNetworkRejectInfo struct {
	RadioInterface uint8
	RejectCause    uint32
	PLMN           string
}

type NASIncrementalNetworkScanInfo struct {
	ScanComplete bool
	Results      []NetworkScanResult
}

type NASService struct {
	client   *Client
	clientID uint8
}

// NewNASService creates a NAS service wrapper / NewNASService创建一个NAS服务包装器
func NewNASService(client *Client) (*NASService, error) {
	return NewNASServiceWithContext(context.Background(), client)
}

func NewNASServiceWithContext(ctx context.Context, client *Client) (*NASService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceNAS)
	if err != nil {
		return nil, err
	}
	return &NASService{client: client, clientID: clientID}, nil
}

// Close releases the NAS client ID / Close释放NAS客户端ID
func (n *NASService) Close() error {
	return n.client.ReleaseClientID(ServiceNAS, n.clientID)
}

func (n *NASService) ClientID() uint8 {
	return n.clientID
}

// ServingSystem contains network registration info / ServingSystem包含网络注册信息
type ServingSystem struct {
	RegistrationState RegistrationState
	PSAttached        bool
	RadioInterface    uint8 // common QMI values: 0=none, 4=GSM, 5=UMTS, 8=LTE, 10=NR5G / 常见 QMI 值：0=无, 4=GSM, 5=UMTS, 8=LTE, 10=NR5G
	MCC               uint16
	MNC               uint16
}

// GetServingSystem queries the current serving system / GetServingSystem查询当前服务系统
func (n *NASService) GetServingSystem(ctx context.Context) (*ServingSystem, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetServingSystem, nil)
	if err != nil {
		return nil, err
	}
	return parseServingSystemPacket(resp, true)
}

func ParseServingSystemIndication(packet *Packet) (*ServingSystem, error) {
	return parseServingSystemPacket(packet, false)
}

// IsRegistered checks if we're registered on the network / IsRegistered检查我们是否已在网络上注册
func (n *NASService) IsRegistered(ctx context.Context) (bool, error) {
	ss, err := n.GetServingSystem(ctx)
	if err != nil {
		return false, err
	}
	return (ss.RegistrationState == RegStateRegistered || ss.RegistrationState == RegStateRoaming) && ss.PSAttached, nil
}

// SignalStrength contains signal quality info / SignalStrength包含信号质量信息
type SignalStrength struct {
	RSSI int8  // dBm
	ECIO int16 // dB * 10 (for UMTS) / dB * 10 (用于UMTS)
	RSRP int16 // dBm (for LTE) / dBm (用于LTE)
	RSRQ int8  // dB (for LTE) / dB (用于LTE)
	SNR  int16 // dB * 10 (for LTE) / dB * 10 (用于LTE)
}

// GetSignalStrength queries current signal strength / GetSignalStrength查询当前信号强度
func (n *NASService) GetSignalStrength(ctx context.Context) (*SignalStrength, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetSignalStrength, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get signal strength failed: %w", err)
	}

	sig := &SignalStrength{}

	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		sig.RSSI = int8(tlv.Value[0])
	}
	if tlv := FindTLV(resp.TLVs, 0x16); tlv != nil && len(tlv.Value) >= 1 {
		sig.RSRQ = int8(tlv.Value[0])
	}
	if tlv := FindTLV(resp.TLVs, 0x17); tlv != nil && len(tlv.Value) >= 2 {
		sig.SNR = int16(binary.LittleEndian.Uint16(tlv.Value))
	}
	if tlv := FindTLV(resp.TLVs, 0x18); tlv != nil && len(tlv.Value) >= 2 {
		sig.RSRP = int16(binary.LittleEndian.Uint16(tlv.Value))
	}

	return sig, nil
}

func parseServingSystemPacket(packet *Packet, checkResult bool) (*ServingSystem, error) {
	if checkResult {
		if err := packet.CheckResult(); err != nil {
			return nil, fmt.Errorf("get serving system failed: %w", err)
		}
	}

	ss := &ServingSystem{}

	// TLV 0x01: Serving system / TLV 0x01: 服务系统
	if tlv := FindTLV(packet.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 3 {
		ss.RegistrationState = RegistrationState(tlv.Value[0])
		ss.PSAttached = tlv.Value[2] == 1

		if len(tlv.Value) >= 6 {
			numIfaces := int(tlv.Value[4])
			if numIfaces > 0 && len(tlv.Value) >= 5+numIfaces {
				ss.RadioInterface = tlv.Value[5]
			}
		}
	}

	// TLV 0x10: Roaming Indicator (0x00 = Roaming ON, 0x01 = Roaming OFF)
	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		if tlv.Value[0] == 0x00 && ss.RegistrationState == RegStateRegistered {
			ss.RegistrationState = RegStateRoaming
		}
	}

	// TLV 0x12: Current PLMN / TLV 0x12: 当前PLMN
	if tlv := FindTLV(packet.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 4 {
		ss.MCC = binary.LittleEndian.Uint16(tlv.Value[0:2])
		ss.MNC = binary.LittleEndian.Uint16(tlv.Value[2:4])
	}

	return ss, nil
}

// RegisterIndications enables NAS unsolicited indications / RegisterIndications启用NAS主动指示
func (n *NASService) RegisterIndications() error {
	cfg := NASIndicationRegistration{
		ServingSystemChanged:        true,
		SystemInfo:                  true,
		NetworkTime:                 true,
		SignalInfo:                  true,
		OperatorName:                true,
		NetworkReject:               true,
		IncrementalNetworkScan:      true,
		EventReportSignalThresholds: []int8{-60, -85},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return n.RegisterIndicationsWithConfig(ctx, cfg)
}

// RegisterIndicationsWithConfig enables NAS indications via Register Indications (0x0003)
// and optionally configures Event Report thresholds (0x0002).
func (n *NASService) RegisterIndicationsWithConfig(ctx context.Context, cfg NASIndicationRegistration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASRegisterIndications, buildNASRegisterIndicationsTLVs(cfg))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("register nas indications failed: %w", err)
	}

	if len(cfg.EventReportSignalThresholds) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	th := make([]byte, 0, len(cfg.EventReportSignalThresholds))
	for _, v := range cfg.EventReportSignalThresholds {
		th = append(th, byte(v))
	}
	tlvs := []TLV{
		{Type: 0x10, Value: append([]byte{0x01, uint8(len(cfg.EventReportSignalThresholds))}, th...)},
	}

	resp, err = n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASSetEventReport, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

func buildNASRegisterIndicationsTLVs(cfg NASIndicationRegistration) []TLV {
	tlvs := make([]TLV, 0, 7)
	if cfg.ServingSystemChanged {
		tlvs = append(tlvs, NewTLVUint8(0x10, 0x01))
	}
	if cfg.SystemInfo {
		tlvs = append(tlvs, NewTLVUint8(0x13, 0x01))
	}
	if cfg.NetworkTime {
		tlvs = append(tlvs, NewTLVUint8(0x17, 0x01))
	}
	if cfg.SignalInfo {
		tlvs = append(tlvs, NewTLVUint8(0x18, 0x01))
	}
	if cfg.OperatorName {
		tlvs = append(tlvs, NewTLVUint8(0x19, 0x01))
	}
	if cfg.NetworkReject {
		tlvs = append(tlvs, NewTLVUint8(0x1A, 0x01))
	}
	if cfg.IncrementalNetworkScan {
		tlvs = append(tlvs, NewTLVUint8(0x1B, 0x01))
	}
	return tlvs
}

// InitiateNetworkRegister starts network registration selection.
func (n *NASService) InitiateNetworkRegister(ctx context.Context, req NASInitiateNetworkRegisterRequest) error {
	tlvs := buildNASInitiateNetworkRegisterTLVs(req)
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASInitiateNetworkRegister, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("initiate network register failed: %w", err)
	}
	return nil
}

// ForceNetworkSearch asks the modem to restart network search.
func (n *NASService) ForceNetworkSearch(ctx context.Context) error {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASForceNetworkSearch, nil)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("force network search failed: %w", err)
	}
	return nil
}

func buildNASInitiateNetworkRegisterTLVs(req NASInitiateNetworkRegisterRequest) []TLV {
	tlvs := []TLV{{Type: 0x01, Value: []byte{uint8(req.Mode)}}}
	if req.Mode == NASNetworkRegisterManual {
		buf := make([]byte, 5)
		binary.LittleEndian.PutUint16(buf[0:2], req.MCC)
		binary.LittleEndian.PutUint16(buf[2:4], req.MNC)
		buf[4] = req.RadioAccessTech
		tlvs = append(tlvs, TLV{Type: 0x10, Value: buf})
	}
	if req.Mode != NASNetworkRegisterManual && req.RadioAccessTech != 0 {
		tlvs = append(tlvs, NewTLVUint8(0x10, req.RadioAccessTech))
	}
	if req.HasChangeDuration {
		tlvs = append(tlvs, NewTLVUint8(0x11, req.ChangeDuration))
	}
	if req.IncludesPCSDigit {
		tlvs = append(tlvs, NewTLVUint8(0x12, 0x01))
	}
	return tlvs
}

// AttachDetach controls PS attach state.
func (n *NASService) AttachDetach(ctx context.Context, attached bool) error {
	value := uint8(0x00)
	if attached {
		value = 0x01
	}
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASAttachDetach, []TLV{{Type: 0x10, Value: []byte{value}}})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("attach detach failed: %w", err)
	}
	return nil
}

// GetSignalInfo gets detailed signal information (LTE/5G) / GetSignalInfo 获取详细信号信息 (LTE/5G)
func (s *NASService) GetSignalInfo(ctx context.Context) (*SignalInfo, error) {
	resp, err := s.client.SendRequest(ctx, ServiceNAS, s.clientID, NASGetSignalInfo, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, err
	}
	return parseSignalInfoPacket(resp)
}

// GetSysInfo gets system information including Cell ID / GetSysInfo 获取系统信息，包括 Cell ID
func (s *NASService) GetSysInfo(ctx context.Context) (*SysInfo, error) {
	resp, err := s.client.SendRequest(ctx, ServiceNAS, s.clientID, NASGetSysInfo, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, err
	}

	return ParseSysInfoIndication(resp)
}

func ParseSysInfoIndication(packet *Packet) (*SysInfo, error) {
	info := &SysInfo{}

	if tlv := FindTLV(packet.TLVs, 0x19); tlv != nil && len(tlv.Value) >= 16 {
		info.CellID = uint64(binary.LittleEndian.Uint32(tlv.Value[12:16]))
		if len(tlv.Value) >= 29 {
			info.TAC = binary.LittleEndian.Uint16(tlv.Value[27:29])
		}
	}

	return info, nil
}

// GetOperatorName returns current operator display names.
func (n *NASService) GetOperatorName(ctx context.Context) (*NASOperatorNameInfo, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetOperatorName, nil)
	if err != nil {
		return nil, err
	}
	return parseOperatorNamePacket(resp, true)
}

func ParseOperatorNameIndication(packet *Packet) (*NASOperatorNameInfo, error) {
	return parseOperatorNamePacket(packet, false)
}

// GetPLMNName resolves PLMN long/short names for the given PLMN.
func (n *NASService) GetPLMNName(ctx context.Context, req NASPLMNNameRequest) (*NASPLMNNameInfo, error) {
	buf := make([]byte, 5)
	binary.LittleEndian.PutUint16(buf[0:2], req.MCC)
	binary.LittleEndian.PutUint16(buf[2:4], req.MNC)
	if req.IncludesPCSDigit {
		buf[4] = 1
	}
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetPLMNName, []TLV{{Type: 0x01, Value: buf}})
	if err != nil {
		return nil, err
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get plmn name failed: %w", err)
	}
	info := &NASPLMNNameInfo{}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		longLen := int(tlv.Value[0])
		if len(tlv.Value) >= 1+longLen {
			info.LongName = string(tlv.Value[1 : 1+longLen])
		}
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		shortLen := int(tlv.Value[0])
		if len(tlv.Value) >= 1+shortLen {
			info.ShortName = string(tlv.Value[1 : 1+shortLen])
		}
	}
	return info, nil
}

// ConfigSignalInfoV2 sets signal-info indication reporting behavior.
func (n *NASService) ConfigSignalInfoV2(ctx context.Context, cfg NASSignalInfoConfigV2) error {
	tlvs := make([]TLV, 0, 2)
	if cfg.LTEEnabled {
		tlvs = append(tlvs, TLV{
			Type: 0x10,
			Value: []byte{
				0x01,
				cfg.LTERSRPDelta,
				cfg.LTERSRQDelta,
				cfg.LTESNRDelta,
			},
		})
	}
	if cfg.NR5GEnabled {
		tlvs = append(tlvs, TLV{
			Type: 0x11,
			Value: []byte{
				0x01,
				cfg.NR5GRSRPDelta,
				cfg.NR5GRSRQDelta,
				cfg.NR5GSINRDelta,
			},
		})
	}
	if len(tlvs) == 0 {
		return fmt.Errorf("config signal info v2 requires at least one RAT config")
	}

	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASConfigSignalInfoV2, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("config signal info v2 failed: %w", err)
	}
	return nil
}

// PerformNetworkScan scans for available networks / PerformNetworkScan 扫描可用网络
func (s *NASService) PerformNetworkScan(ctx context.Context) ([]NetworkScanResult, error) {
	resp, err := s.client.SendRequest(ctx, ServiceNAS, s.clientID, NASPerformNetworkScan, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, err
	}

	return parseNetworkScanResults(resp), nil
}

// GetRFBandInfo returns current active band/channel details.
func (n *NASService) GetRFBandInfo(ctx context.Context) (*RFBandInfo, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetRFBandInfo, nil)
	if err != nil {
		return nil, err
	}
	return parseRFBandInfoResponse(resp)
}

// GetTechnologyPreference returns the active and persistent technology preference.
func (n *NASService) GetTechnologyPreference(ctx context.Context) (*TechnologyPreference, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetTechnologyPreference, nil)
	if err != nil {
		return nil, err
	}
	return parseTechnologyPreferenceResponse(resp)
}

// SetTechnologyPreference updates the active technology preference.
func (n *NASService) SetTechnologyPreference(ctx context.Context, pref TechnologyPreference) error {
	tlvs := buildTechnologyPreferenceTLVs(pref)
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASSetTechnologyPreference, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("set technology preference failed: %w", err)
	}
	return nil
}

// GetSystemSelectionPreference returns the modem system-selection policy.
func (n *NASService) GetSystemSelectionPreference(ctx context.Context) (*SystemSelectionPreference, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetSystemSelectionPreference, nil)
	if err != nil {
		return nil, err
	}
	return parseSystemSelectionPreferenceResponse(resp)
}

// SetSystemSelectionPreference updates one or more system-selection policy fields.
func (n *NASService) SetSystemSelectionPreference(ctx context.Context, pref SystemSelectionPreference) error {
	tlvs, err := buildSystemSelectionPreferenceTLVs(pref)
	if err != nil {
		return err
	}
	if len(tlvs) == 0 {
		return fmt.Errorf("set system selection preference requires at least one field")
	}

	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASSetSystemSelectionPreference, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("set system selection preference failed: %w", err)
	}
	return nil
}

// GetCellLocationInfo returns serving-cell details for the current RAT.
func (n *NASService) GetCellLocationInfo(ctx context.Context) (*CellLocationInfo, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetCellLocationInfo, nil)
	if err != nil {
		return nil, err
	}
	return parseCellLocationInfoResponse(resp)
}

// GetNetworkTime returns network-provided time values from 3GPP/3GPP2.
func (n *NASService) GetNetworkTime(ctx context.Context) (*NetworkTimeInfo, error) {
	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASGetNetworkTime, nil)
	if err != nil {
		return nil, err
	}
	return parseNetworkTimeResponse(resp)
}

// ============================================================================
// Internal helpers / 内部助手
// ============================================================================

func buildTechnologyPreferenceTLVs(pref TechnologyPreference) []TLV {
	buf := make([]byte, 3)
	binary.LittleEndian.PutUint16(buf[0:2], pref.ActivePreference)
	buf[2] = pref.ActiveDuration
	return []TLV{{Type: 0x01, Value: buf}}
}

func buildSystemSelectionPreferenceTLVs(pref SystemSelectionPreference) ([]TLV, error) {
	var tlvs []TLV

	if pref.HasEmergencyMode {
		tlvs = append(tlvs, NewTLVUint8(0x10, boolToUint8(pref.EmergencyMode)))
	}
	if pref.HasModePreference {
		tlvs = append(tlvs, NewTLVUint16(0x11, pref.ModePreference))
	}
	if pref.HasBandPreference {
		tlvs = append(tlvs, newTLVUint64(0x12, pref.BandPreference))
	}
	if pref.HasCDMAPRLPreference {
		tlvs = append(tlvs, NewTLVUint16(0x13, pref.CDMAPRLPreference))
	}
	if pref.HasRoamingPreference {
		tlvs = append(tlvs, NewTLVUint16(0x14, pref.RoamingPreference))
	}
	if pref.HasLTEBandPreference {
		tlvs = append(tlvs, newTLVUint64(0x15, pref.LTEBandPreference))
	}
	if pref.HasTDSCDMABandPreference {
		tlvs = append(tlvs, newTLVUint64(0x1D, pref.TDSCDMABandPreference))
	}
	if pref.HasNetworkSelectionPreference || pref.HasManualNetworkSelection {
		mode := pref.NetworkSelectionPreference
		var mcc uint16
		var mnc uint16
		if pref.HasManualNetworkSelection {
			mode = NASNetworkSelectionManual
			mcc = pref.ManualNetworkSelection.MCC
			mnc = pref.ManualNetworkSelection.MNC
		}
		buf := make([]byte, 5)
		buf[0] = mode
		binary.LittleEndian.PutUint16(buf[1:3], mcc)
		binary.LittleEndian.PutUint16(buf[3:5], mnc)
		tlvs = append(tlvs, TLV{Type: 0x16, Value: buf})
	}
	if pref.HasChangeDuration {
		tlvs = append(tlvs, NewTLVUint8(0x17, pref.ChangeDuration))
	}
	if pref.HasServiceDomainPreference {
		tlvs = append(tlvs, NewTLVUint32(0x18, pref.ServiceDomainPreference))
	}
	if pref.HasGSMWCDMAAcquisitionOrderPreference {
		tlvs = append(tlvs, NewTLVUint32(0x19, pref.GSMWCDMAAcquisitionOrderPreference))
	}
	if pref.HasManualNetworkSelection {
		tlvs = append(tlvs, NewTLVUint8(0x1A, boolToUint8(pref.ManualNetworkSelection.IncludesPCSDigit)))
	}
	if len(pref.AcquisitionOrderPreference) > 0 {
		buf := make([]byte, 1+len(pref.AcquisitionOrderPreference))
		buf[0] = uint8(len(pref.AcquisitionOrderPreference))
		copy(buf[1:], pref.AcquisitionOrderPreference)
		tlvs = append(tlvs, TLV{Type: 0x1E, Value: buf})
	}
	if pref.HasNetworkSelectionRegistrationRestriction {
		tlvs = append(tlvs, NewTLVUint32(0x1F, pref.NetworkSelectionRegistrationRestriction))
	}
	if pref.HasUsagePreference {
		tlvs = append(tlvs, NewTLVUint32(0x21, pref.UsagePreference))
	}
	if pref.HasVoiceDomainPreference {
		tlvs = append(tlvs, NewTLVUint32(0x23, pref.VoiceDomainPreference))
	}
	if pref.HasExtendedLTEBandPreference {
		tlv, err := newTLVUint64Sequence(0x24, pref.ExtendedLTEBandPreference[:])
		if err != nil {
			return nil, err
		}
		tlvs = append(tlvs, tlv)
	}
	if pref.HasNR5GSABandPreference {
		tlv, err := newTLVUint64Sequence(0x2F, pref.NR5GSABandPreference[:])
		if err != nil {
			return nil, err
		}
		tlvs = append(tlvs, tlv)
	}
	if pref.HasNR5GNSABandPreference {
		tlv, err := newTLVUint64Sequence(0x30, pref.NR5GNSABandPreference[:])
		if err != nil {
			return nil, err
		}
		tlvs = append(tlvs, tlv)
	}

	return tlvs, nil
}

func parseSignalInfoPacket(packet *Packet) (*SignalInfo, error) {
	info := &SignalInfo{}

	if tlv := FindTLV(packet.TLVs, 0x14); tlv != nil && len(tlv.Value) >= 6 {
		info.LTERSRQ = int16(int8(tlv.Value[1]))
		info.LTERSRP = int16(binary.LittleEndian.Uint16(tlv.Value[2:4]))
		info.LTERSSNR = int16(binary.LittleEndian.Uint16(tlv.Value[4:6]))
	}

	if tlv := FindTLV(packet.TLVs, 0x17); tlv != nil && len(tlv.Value) >= 6 {
		info.NR5GRSRP = int16(binary.LittleEndian.Uint16(tlv.Value[2:4]))
		info.NR5GRSRQ = int16(binary.LittleEndian.Uint16(tlv.Value[0:2]))
		info.NR5GSINR = int16(binary.LittleEndian.Uint16(tlv.Value[4:6]))
	}

	return info, nil
}

func ParseSignalInfoIndication(packet *Packet) (*SignalInfo, error) {
	return parseSignalInfoPacket(packet)
}

func parseOperatorNamePacket(packet *Packet, checkResult bool) (*NASOperatorNameInfo, error) {
	if checkResult {
		if err := packet.CheckResult(); err != nil {
			return nil, fmt.Errorf("get operator name failed: %w", err)
		}
	}
	info := &NASOperatorNameInfo{}
	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		info.ServiceProviderName = string(tlv.Value[1:])
	}
	if tlv := FindTLV(packet.TLVs, 0x13); tlv != nil {
		info.OperatorStringName = string(tlv.Value)
	}
	return info, nil
}

func parseNetworkScanResults(packet *Packet) []NetworkScanResult {
	var results []NetworkScanResult

	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
		n := int(binary.LittleEndian.Uint16(tlv.Value[0:2]))
		offset := 2
		for i := 0; i < n; i++ {
			if len(tlv.Value)-offset < 6 {
				break
			}
			mcc := binary.LittleEndian.Uint16(tlv.Value[offset : offset+2])
			mnc := binary.LittleEndian.Uint16(tlv.Value[offset+2 : offset+4])
			status := tlv.Value[offset+4]
			descLen := int(tlv.Value[offset+5])
			offset += 6
			if len(tlv.Value)-offset < descLen {
				break
			}
			desc := ""
			if descLen > 0 {
				desc = string(tlv.Value[offset : offset+descLen])
				offset += descLen
			}
			results = append(results, NetworkScanResult{
				MCC:         fmt.Sprintf("%03d", mcc),
				MNC:         fmt.Sprintf("%03d", mnc),
				Status:      status,
				Description: desc,
			})
		}
	}

	if tlv := FindTLV(packet.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 2 {
		n := int(binary.LittleEndian.Uint16(tlv.Value[0:2]))
		offset := 2
		for i := 0; i < n; i++ {
			if len(tlv.Value)-offset < 5 {
				break
			}
			mcc := fmt.Sprintf("%03d", binary.LittleEndian.Uint16(tlv.Value[offset:offset+2]))
			mnc := fmt.Sprintf("%03d", binary.LittleEndian.Uint16(tlv.Value[offset+2:offset+4]))
			rat := tlv.Value[offset+4]
			offset += 5
			for j := range results {
				if results[j].MCC == mcc && results[j].MNC == mnc {
					results[j].RATs = append(results[j].RATs, rat)
					break
				}
			}
		}
	}

	return results
}

func parseRFBandInfoResponse(resp *Packet) (*RFBandInfo, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get RF band information failed: %w", err)
	}

	info := &RFBandInfo{}

	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		count := int(tlv.Value[0])
		offset := 1
		info.Bands = make([]RFBandInfoEntry, 0, count)
		for i := 0; i < count; i++ {
			if offset+7 > len(tlv.Value) {
				break
			}
			info.Bands = append(info.Bands, RFBandInfoEntry{
				RadioInterface:  tlv.Value[offset],
				ActiveBandClass: binary.LittleEndian.Uint16(tlv.Value[offset+1 : offset+3]),
				ActiveChannel:   binary.LittleEndian.Uint32(tlv.Value[offset+3 : offset+7]),
			})
			offset += 7
		}
	} else if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		count := int(tlv.Value[0])
		offset := 1
		info.Bands = make([]RFBandInfoEntry, 0, count)
		for i := 0; i < count; i++ {
			if offset+5 > len(tlv.Value) {
				break
			}
			info.Bands = append(info.Bands, RFBandInfoEntry{
				RadioInterface:  tlv.Value[offset],
				ActiveBandClass: binary.LittleEndian.Uint16(tlv.Value[offset+1 : offset+3]),
				ActiveChannel:   uint32(binary.LittleEndian.Uint16(tlv.Value[offset+3 : offset+5])),
			})
			offset += 5
		}
	}

	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 1 {
		count := int(tlv.Value[0])
		offset := 1
		info.Bandwidths = make([]RFBandwidthInfo, 0, count)
		for i := 0; i < count; i++ {
			if offset+5 > len(tlv.Value) {
				break
			}
			info.Bandwidths = append(info.Bandwidths, RFBandwidthInfo{
				RadioInterface: tlv.Value[offset],
				Bandwidth:      binary.LittleEndian.Uint32(tlv.Value[offset+1 : offset+5]),
			})
			offset += 5
		}
	}

	if len(info.Bands) == 0 && len(info.Bandwidths) == 0 {
		return nil, fmt.Errorf("no RF band information TLVs in response")
	}
	return info, nil
}

func parseTechnologyPreferenceResponse(resp *Packet) (*TechnologyPreference, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get technology preference failed: %w", err)
	}

	info := &TechnologyPreference{}
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 3 {
		info.ActivePreference = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.ActiveDuration = tlv.Value[2]
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
		info.PersistentPreference = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasPersistentPreference = true
	}
	return info, nil
}

func parseSystemSelectionPreferenceResponse(resp *Packet) (*SystemSelectionPreference, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get system selection preference failed: %w", err)
	}

	info := &SystemSelectionPreference{}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		info.EmergencyMode = tlv.Value[0] != 0
		info.HasEmergencyMode = true
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 2 {
		info.ModePreference = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasModePreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 8 {
		info.BandPreference = binary.LittleEndian.Uint64(tlv.Value[0:8])
		info.HasBandPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 2 {
		info.CDMAPRLPreference = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasCDMAPRLPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x14); tlv != nil && len(tlv.Value) >= 2 {
		info.RoamingPreference = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasRoamingPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x15); tlv != nil && len(tlv.Value) >= 8 {
		info.LTEBandPreference = binary.LittleEndian.Uint64(tlv.Value[0:8])
		info.HasLTEBandPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x1A); tlv != nil && len(tlv.Value) >= 8 {
		info.TDSCDMABandPreference = binary.LittleEndian.Uint64(tlv.Value[0:8])
		info.HasTDSCDMABandPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x16); tlv != nil && len(tlv.Value) >= 1 {
		info.NetworkSelectionPreference = tlv.Value[0]
		info.HasNetworkSelectionPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x18); tlv != nil && len(tlv.Value) >= 4 {
		info.ServiceDomainPreference = binary.LittleEndian.Uint32(tlv.Value[0:4])
		info.HasServiceDomainPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x19); tlv != nil && len(tlv.Value) >= 4 {
		info.GSMWCDMAAcquisitionOrderPreference = binary.LittleEndian.Uint32(tlv.Value[0:4])
		info.HasGSMWCDMAAcquisitionOrderPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x1B); tlv != nil && len(tlv.Value) >= 5 {
		info.ManualNetworkSelection = ManualNetworkSelection{
			MCC:              binary.LittleEndian.Uint16(tlv.Value[0:2]),
			MNC:              binary.LittleEndian.Uint16(tlv.Value[2:4]),
			IncludesPCSDigit: tlv.Value[4] != 0,
		}
		info.HasManualNetworkSelection = true
	}
	if tlv := FindTLV(resp.TLVs, 0x1C); tlv != nil && len(tlv.Value) >= 1 {
		count := int(tlv.Value[0])
		if len(tlv.Value) >= 1+count {
			info.AcquisitionOrderPreference = append([]uint8(nil), tlv.Value[1:1+count]...)
		}
	}
	if tlv := FindTLV(resp.TLVs, 0x1D); tlv != nil && len(tlv.Value) >= 4 {
		info.NetworkSelectionRegistrationRestriction = binary.LittleEndian.Uint32(tlv.Value[0:4])
		info.HasNetworkSelectionRegistrationRestriction = true
	}
	if tlv := FindTLV(resp.TLVs, 0x1F); tlv != nil && len(tlv.Value) >= 4 {
		info.UsagePreference = binary.LittleEndian.Uint32(tlv.Value[0:4])
		info.HasUsagePreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x20); tlv != nil && len(tlv.Value) >= 4 {
		info.VoiceDomainPreference = binary.LittleEndian.Uint32(tlv.Value[0:4])
		info.HasVoiceDomainPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x22); tlv != nil && len(tlv.Value) >= 2 {
		info.DisabledModes = binary.LittleEndian.Uint16(tlv.Value[0:2])
		info.HasDisabledModes = true
	}
	if tlv := FindTLV(resp.TLVs, 0x23); tlv != nil {
		values, err := parseUint64Sequence(tlv.Value, 4)
		if err != nil {
			return nil, err
		}
		copy(info.ExtendedLTEBandPreference[:], values)
		info.HasExtendedLTEBandPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x2C); tlv != nil {
		values, err := parseUint64Sequence(tlv.Value, 8)
		if err != nil {
			return nil, err
		}
		copy(info.NR5GSABandPreference[:], values)
		info.HasNR5GSABandPreference = true
	}
	if tlv := FindTLV(resp.TLVs, 0x2D); tlv != nil {
		values, err := parseUint64Sequence(tlv.Value, 8)
		if err != nil {
			return nil, err
		}
		copy(info.NR5GNSABandPreference[:], values)
		info.HasNR5GNSABandPreference = true
	}

	return info, nil
}

func parseCellLocationInfoResponse(resp *Packet) (*CellLocationInfo, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get cell location info failed: %w", err)
	}

	info := &CellLocationInfo{}

	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 18 {
		mcc, mnc := decodeBCDPLMN(tlv.Value[4:7])
		geran := &GERANCellLocationInfo{
			CellID:        binary.LittleEndian.Uint32(tlv.Value[0:4]),
			MCC:           mcc,
			MNC:           mnc,
			LAC:           binary.LittleEndian.Uint16(tlv.Value[7:9]),
			ARFCN:         binary.LittleEndian.Uint16(tlv.Value[9:11]),
			BaseStationID: tlv.Value[11],
			RXLevel:       binary.LittleEndian.Uint16(tlv.Value[16:18]),
		}
		timingAdvance := binary.LittleEndian.Uint32(tlv.Value[12:16])
		if timingAdvance != 0xFFFFFFFF {
			geran.TimingAdvance = timingAdvance
			geran.HasTimingAdvance = true
		}
		info.GERAN = geran
	}

	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 15 {
		mcc, mnc := decodeBCDPLMN(tlv.Value[2:5])
		info.UMTS = &UMTSCellLocationInfo{
			CellID:                uint32(binary.LittleEndian.Uint16(tlv.Value[0:2])),
			MCC:                   mcc,
			MNC:                   mnc,
			LAC:                   binary.LittleEndian.Uint16(tlv.Value[5:7]),
			UARFCN:                binary.LittleEndian.Uint16(tlv.Value[7:9]),
			PrimaryScramblingCode: binary.LittleEndian.Uint16(tlv.Value[9:11]),
			RSCP:                  int16(binary.LittleEndian.Uint16(tlv.Value[11:13])),
			ECIO:                  int16(binary.LittleEndian.Uint16(tlv.Value[13:15])),
		}
	}

	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 18 {
		mcc, mnc := decodeBCDPLMN(tlv.Value[1:4])
		lte := &LTECellLocationInfo{
			UEInIdle:                 tlv.Value[0] != 0,
			MCC:                      mcc,
			MNC:                      mnc,
			TAC:                      binary.LittleEndian.Uint16(tlv.Value[4:6]),
			GlobalCellID:             binary.LittleEndian.Uint32(tlv.Value[6:10]),
			EARFCN:                   binary.LittleEndian.Uint16(tlv.Value[10:12]),
			ServingCellID:            binary.LittleEndian.Uint16(tlv.Value[12:14]),
			CellReselectionPriority:  tlv.Value[14],
			SNonIntraSearchThreshold: tlv.Value[15],
			ServingCellLowThreshold:  tlv.Value[16],
			SIntraSearchThreshold:    tlv.Value[17],
		}
		if lte.UEInIdle {
			lte.HasIdleThresholds = true
		}
		info.LTE = lte
	}

	if tlv := FindTLV(resp.TLVs, 0x17); tlv != nil && len(tlv.Value) >= 4 {
		if info.UMTS == nil {
			info.UMTS = &UMTSCellLocationInfo{}
		}
		info.UMTS.CellID = binary.LittleEndian.Uint32(tlv.Value[0:4])
	}

	if tlv := FindTLV(resp.TLVs, 0x1E); tlv != nil && len(tlv.Value) >= 4 {
		if info.LTE == nil {
			info.LTE = &LTECellLocationInfo{}
		}
		timingAdvance := binary.LittleEndian.Uint32(tlv.Value[0:4])
		if timingAdvance != 0xFFFFFFFF {
			info.LTE.TimingAdvance = timingAdvance
			info.LTE.HasTimingAdvance = true
		}
	}

	if tlv := FindTLV(resp.TLVs, 0x2E); tlv != nil && len(tlv.Value) >= 4 {
		if info.NR5G == nil {
			info.NR5G = &NR5GCellLocationInfo{}
		}
		info.NR5G.ARFCN = binary.LittleEndian.Uint32(tlv.Value[0:4])
		info.NR5G.HasARFCN = true
	}

	if tlv := FindTLV(resp.TLVs, 0x2F); tlv != nil && len(tlv.Value) >= 20 {
		mcc, mnc := decodeBCDPLMN(tlv.Value[0:3])
		if info.NR5G == nil {
			info.NR5G = &NR5GCellLocationInfo{}
		}
		info.NR5G.MCC = mcc
		info.NR5G.MNC = mnc
		info.NR5G.TAC = decodeUint24(tlv.Value[3:6])
		info.NR5G.GlobalCellID = binary.LittleEndian.Uint64(tlv.Value[6:14])
		info.NR5G.PhysicalCellID = binary.LittleEndian.Uint16(tlv.Value[14:16])
		info.NR5G.RSRQ = int16(binary.LittleEndian.Uint16(tlv.Value[16:18]))
		info.NR5G.RSRP = int16(binary.LittleEndian.Uint16(tlv.Value[18:20]))
		if len(tlv.Value) >= 22 {
			info.NR5G.SNR = int16(binary.LittleEndian.Uint16(tlv.Value[20:22]))
		}
	}

	if info.GERAN == nil && info.UMTS == nil && info.LTE == nil && info.NR5G == nil {
		return nil, fmt.Errorf("no cell location TLVs in response")
	}

	return info, nil
}

func parseNetworkTimePacket(packet *Packet, checkResult bool) (*NetworkTimeInfo, error) {
	if checkResult {
		if err := packet.CheckResult(); err != nil {
			return nil, fmt.Errorf("get network time failed: %w", err)
		}
	}

	info := &NetworkTimeInfo{}
	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil {
		value, err := parseNetworkTimeTLV(tlv)
		if err != nil {
			return nil, err
		}
		info.ThreeGPP2 = value
		info.HasThreeGPP2 = true
	}
	if tlv := FindTLV(packet.TLVs, 0x11); tlv != nil {
		value, err := parseNetworkTimeTLV(tlv)
		if err != nil {
			return nil, err
		}
		info.ThreeGPP = value
		info.HasThreeGPP = true
	}

	if !info.HasThreeGPP && !info.HasThreeGPP2 {
		return nil, fmt.Errorf("no network time TLV in response")
	}
	return info, nil
}

func parseNetworkTimeResponse(resp *Packet) (*NetworkTimeInfo, error) {
	return parseNetworkTimePacket(resp, true)
}

func ParseNetworkTimeIndication(packet *Packet) (*NetworkTimeInfo, error) {
	if packet == nil {
		return nil, fmt.Errorf("network time indication packet is nil")
	}

	// NAS_NETWORK_TIME_IND uses split TLVs:
	// 0x01 universal time, 0x10 timezone, 0x11 DST, 0x12 radio interface.
	if tlv := FindTLV(packet.TLVs, 0x01); tlv != nil {
		value, err := parseNetworkTimeIndicationTLV(tlv)
		if err != nil {
			return nil, err
		}
		if tz := FindTLV(packet.TLVs, 0x10); tz != nil {
			if len(tz.Value) < 1 {
				return nil, fmt.Errorf("network time timezone TLV too short: %d", len(tz.Value))
			}
			value.TimezoneOffsetQuarters = int8(tz.Value[0])
		}
		if dst := FindTLV(packet.TLVs, 0x11); dst != nil {
			if len(dst.Value) < 1 {
				return nil, fmt.Errorf("network time daylight savings TLV too short: %d", len(dst.Value))
			}
			value.DaylightSavingsAdjustment = dst.Value[0]
		}
		if rat := FindTLV(packet.TLVs, 0x12); rat != nil {
			if len(rat.Value) < 1 {
				return nil, fmt.Errorf("network time radio interface TLV too short: %d", len(rat.Value))
			}
			value.RadioInterface = rat.Value[0]
		}
		return &NetworkTimeInfo{
			ThreeGPP:    value,
			HasThreeGPP: true,
		}, nil
	}

	// Keep compatibility with modems that emit GetNetworkTime response-style
	// TLVs on the indication path.
	if networkTimeResponseTLVPresent(packet) {
		return parseNetworkTimePacket(packet, false)
	}
	return nil, fmt.Errorf("network time indication TLV not found")
}

func ParseNetworkRejectIndication(packet *Packet) (*NASNetworkRejectInfo, error) {
	if packet == nil {
		return nil, fmt.Errorf("network reject indication packet is nil")
	}
	info := &NASNetworkRejectInfo{}
	if tlv := FindTLV(packet.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 5 {
		info.RadioInterface = tlv.Value[0]
		info.RejectCause = binary.LittleEndian.Uint32(tlv.Value[1:5])
	}
	if tlv := FindTLV(packet.TLVs, 0x11); tlv != nil {
		info.PLMN = string(tlv.Value)
	}
	return info, nil
}

func ParseIncrementalNetworkScanIndication(packet *Packet) (*NASIncrementalNetworkScanInfo, error) {
	if packet == nil {
		return nil, fmt.Errorf("incremental network scan indication packet is nil")
	}
	info := &NASIncrementalNetworkScanInfo{
		Results: parseNetworkScanResults(packet),
	}
	if tlv := FindTLV(packet.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 1 {
		info.ScanComplete = tlv.Value[0] != 0
	}
	return info, nil
}

func parseNetworkTimeTLV(tlv *TLV) (NetworkTime, error) {
	if tlv == nil {
		return NetworkTime{}, fmt.Errorf("network time TLV is nil")
	}
	if len(tlv.Value) < 11 {
		return NetworkTime{}, fmt.Errorf("network time TLV too short: %d", len(tlv.Value))
	}
	return NetworkTime{
		Year:                      binary.LittleEndian.Uint16(tlv.Value[0:2]),
		Month:                     tlv.Value[2],
		Day:                       tlv.Value[3],
		Hour:                      tlv.Value[4],
		Minute:                    tlv.Value[5],
		Second:                    tlv.Value[6],
		DayOfWeek:                 tlv.Value[7],
		TimezoneOffsetQuarters:    int8(tlv.Value[8]),
		DaylightSavingsAdjustment: tlv.Value[9],
		RadioInterface:            tlv.Value[10],
	}, nil
}

func parseNetworkTimeIndicationTLV(tlv *TLV) (NetworkTime, error) {
	if tlv == nil {
		return NetworkTime{}, fmt.Errorf("network time indication TLV is nil")
	}
	if len(tlv.Value) < 8 {
		return NetworkTime{}, fmt.Errorf("network time universal time TLV too short: %d", len(tlv.Value))
	}
	value := NetworkTime{
		Year:      binary.LittleEndian.Uint16(tlv.Value[0:2]),
		Month:     tlv.Value[2],
		Day:       tlv.Value[3],
		Hour:      tlv.Value[4],
		Minute:    tlv.Value[5],
		Second:    tlv.Value[6],
		DayOfWeek: tlv.Value[7],
	}
	if len(tlv.Value) >= 11 {
		value.TimezoneOffsetQuarters = int8(tlv.Value[8])
		value.DaylightSavingsAdjustment = tlv.Value[9]
		value.RadioInterface = tlv.Value[10]
	}
	return value, nil
}

func networkTimeResponseTLVPresent(packet *Packet) bool {
	for _, tlvType := range []uint8{0x10, 0x11} {
		if tlv := FindTLV(packet.TLVs, tlvType); tlv != nil && len(tlv.Value) >= 11 {
			return true
		}
	}
	return false
}

func parseUint64Sequence(value []byte, count int) ([]uint64, error) {
	if len(value) < count*8 {
		return nil, fmt.Errorf("uint64 sequence too short: need %d, have %d", count*8, len(value))
	}
	out := make([]uint64, count)
	for i := 0; i < count; i++ {
		offset := i * 8
		out[i] = binary.LittleEndian.Uint64(value[offset : offset+8])
	}
	return out, nil
}

func newTLVUint64(t uint8, v uint64) TLV {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)
	return TLV{Type: t, Value: buf}
}

func newTLVUint64Sequence(t uint8, values []uint64) (TLV, error) {
	if len(values) == 0 {
		return TLV{}, fmt.Errorf("uint64 sequence cannot be empty")
	}
	buf := make([]byte, 0, len(values)*8)
	for _, v := range values {
		part := make([]byte, 8)
		binary.LittleEndian.PutUint64(part, v)
		buf = append(buf, part...)
	}
	return TLV{Type: t, Value: buf}, nil
}

func boolToUint8(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}

func decodeUint24(b []byte) uint32 {
	if len(b) < 3 {
		return 0
	}
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

func decodeBCDPLMN(plmn []byte) (string, string) {
	if len(plmn) < 3 {
		return "", ""
	}

	mcc1 := plmn[0] & 0x0F
	mcc2 := (plmn[0] >> 4) & 0x0F
	mcc3 := plmn[1] & 0x0F
	mnc3 := (plmn[1] >> 4) & 0x0F
	mnc1 := plmn[2] & 0x0F
	mnc2 := (plmn[2] >> 4) & 0x0F

	mcc := fmt.Sprintf("%d%d%d", mcc1, mcc2, mcc3)
	if mnc3 == 0x0F {
		return mcc, fmt.Sprintf("%d%d", mnc1, mnc2)
	}
	return mcc, fmt.Sprintf("%d%d%d", mnc1, mnc2, mnc3)
}
