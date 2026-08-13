package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
)

// ============================================================================
// SIM/UIM Status / SIM/UIM状态
// ============================================================================

type SIMStatus uint8

const (
	SIMAbsent        SIMStatus = 0
	SIMNotReady      SIMStatus = 1
	SIMReady         SIMStatus = 2
	SIMPINRequired   SIMStatus = 3
	SIMPUKRequired   SIMStatus = 4
	SIMBlocked       SIMStatus = 5
	SIMNetworkLocked SIMStatus = 6
)

func (s SIMStatus) String() string {
	switch s {
	case SIMAbsent:
		return "absent"
	case SIMNotReady:
		return "not_ready"
	case SIMReady:
		return "ready"
	case SIMPINRequired:
		return "pin_required"
	case SIMPUKRequired:
		return "puk_required"
	case SIMBlocked:
		return "blocked"
	case SIMNetworkLocked:
		return "network_locked"
	default:
		return "unknown"
	}
}

const (
	DMSGetCapabilities uint16 = 0x0020
	DMSGetManufacturer uint16 = 0x0021
	DMSGetModel        uint16 = 0x0022
	/* Defined in frame.go / 在 frame.go 中定义
	DMSGetDeviceRevID         uint16 = 0x0023
	DMSUIMGetState      uint16 = 0x0044
	DMSUIMVerifyPIN     uint16 = 0x0028
	*/
	DMSGetMSISDN           uint16 = 0x0024
	DMSGetPowerState       uint16 = 0x0026
	DMSGetHardwareRevision uint16 = 0x002C
	DMSGetTime             uint16 = 0x002F
	DMSGetPRLVersion       uint16 = 0x0030
	DMSGetActivationState  uint16 = 0x0031
	DMSUIMSetPINProtection uint16 = 0x0027
	DMSUIMUnblockPIN       uint16 = 0x0029
	DMSUIMChangePIN        uint16 = 0x002A
	DMSGetUserLockState    uint16 = 0x0034
	DMSSetUserLockState    uint16 = 0x0035
	DMSSetUserLockCode     uint16 = 0x0036
	DMSReadUserData        uint16 = 0x0037
	DMSWriteUserData       uint16 = 0x0038
	DMSUIMGetICCID         uint16 = 0x003C
	DMSGetBandCapabilities uint16 = 0x0045
	DMSGetFactorySKU       uint16 = 0x0046
	DMSGetSoftwareVersion  uint16 = 0x0051
	DMSGetMACAddress       uint16 = 0x005C
)

const (
	DMSDataServiceCapabilityNone                uint8 = 0
	DMSDataServiceCapabilityCS                  uint8 = 1
	DMSDataServiceCapabilityPS                  uint8 = 2
	DMSDataServiceCapabilitySimultaneousCSPS    uint8 = 3
	DMSDataServiceCapabilityNonSimultaneousCSPS uint8 = 4
)

const (
	DMSSIMCapabilityNotSupported uint8 = 1
	DMSSIMCapabilitySupported    uint8 = 2
)

const (
	DMSRadioInterfaceCDMA20001X uint8 = 1
	DMSRadioInterfaceEVDO       uint8 = 2
	DMSRadioInterfaceGSM        uint8 = 4
	DMSRadioInterfaceUMTS       uint8 = 5
	DMSRadioInterfaceLTE        uint8 = 8
	DMSRadioInterfaceTDS        uint8 = 9
	DMSRadioInterfaceNR5G       uint8 = 10
)

const (
	DMSPowerStateExternalSource   uint8 = 1 << 0
	DMSPowerStateBatteryConnected uint8 = 1 << 1
	DMSPowerStateBatteryCharging  uint8 = 1 << 2
	DMSPowerStateFault            uint8 = 1 << 3
)

const (
	DMSTimeSourceDevice      uint16 = 0
	DMSTimeSourceCDMANetwork uint16 = 1
	DMSTimeSourceHDRNetwork  uint16 = 2
)

const (
	DMSMACTypeWLAN uint32 = 0
	DMSMACTypeBT   uint32 = 1
)

type ActivationState uint16

const (
	ActivationStateNotActivated       ActivationState = 0x00
	ActivationStateActivated          ActivationState = 0x01
	ActivationStateConnecting         ActivationState = 0x02
	ActivationStateConnected          ActivationState = 0x03
	ActivationStateOTASPAuthenticated ActivationState = 0x04
	ActivationStateOTASPNAM           ActivationState = 0x05
	ActivationStateOTASPMDN           ActivationState = 0x06
	ActivationStateOTASPIMSI          ActivationState = 0x07
	ActivationStateOTASPPRL           ActivationState = 0x08
	ActivationStateOTASPSPC           ActivationState = 0x09
	ActivationStateOTASPCommitted     ActivationState = 0x0A
)

func (s ActivationState) String() string {
	switch s {
	case ActivationStateNotActivated:
		return "not_activated"
	case ActivationStateActivated:
		return "activated"
	case ActivationStateConnecting:
		return "connecting"
	case ActivationStateConnected:
		return "connected"
	case ActivationStateOTASPAuthenticated:
		return "otasp_authenticated"
	case ActivationStateOTASPNAM:
		return "otasp_nam"
	case ActivationStateOTASPMDN:
		return "otasp_mdn"
	case ActivationStateOTASPIMSI:
		return "otasp_imsi"
	case ActivationStateOTASPPRL:
		return "otasp_prl"
	case ActivationStateOTASPSPC:
		return "otasp_spc"
	case ActivationStateOTASPCommitted:
		return "otasp_committed"
	default:
		return "unknown"
	}
}

// ============================================================================
// Operating Mode / 操作模式
// ============================================================================

type OperatingMode uint8

const (
	ModeOnline       OperatingMode = 0x00
	ModeLowPower     OperatingMode = 0x01
	ModeFactoryTest  OperatingMode = 0x02
	ModeOffline      OperatingMode = 0x03
	ModeReset        OperatingMode = 0x04
	ModeShutdown     OperatingMode = 0x05
	ModePersistLow   OperatingMode = 0x06
	ModeOnlyLowPower OperatingMode = 0x07
)

// ============================================================================
// DMS Service wrapper / DMS服务包装器
// ============================================================================

type DMSService struct {
	client   *Client
	clientID uint8
}

// NewDMSService creates a DMS service wrapper / NewDMSService创建一个DMS服务包装器
func NewDMSService(client *Client) (*DMSService, error) {
	return NewDMSServiceWithContext(context.Background(), client)
}

func NewDMSServiceWithContext(ctx context.Context, client *Client) (*DMSService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceDMS)
	if err != nil {
		return nil, err
	}
	return &DMSService{client: client, clientID: clientID}, nil
}

// Close releases the DMS client ID / Close释放DMS客户端ID
func (d *DMSService) Close() error {
	return d.client.ReleaseClientID(ServiceDMS, d.clientID)
}

func (d *DMSService) ClientID() uint8 {
	return d.clientID
}

type NotSupportedError struct {
	Operation string
}

func (e *NotSupportedError) Error() string {
	if e.Operation == "" {
		return "not supported"
	}
	return e.Operation + ": not supported"
}

type PINStatus uint8

const (
	PINStatusNotInit     PINStatus = 0
	PINStatusNotVerified PINStatus = 1
	PINStatusVerified    PINStatus = 2
	PINStatusDisabled    PINStatus = 3
	PINStatusBlocked     PINStatus = 4
	PINStatusPermBlocked PINStatus = 5
	PINStatusUnblocked   PINStatus = 6
	PINStatusChanged     PINStatus = 7
)

type PINInfo struct {
	Status             PINStatus
	VerifyRetriesLeft  uint8
	UnblockRetriesLeft uint8
}

func (d *DMSService) GetPINStatus(ctx context.Context) (*PINInfo, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMGetPINStatus, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		if qe := GetQMIError(err); qe != nil && qe.ErrorCode == QMIErrNotSupported {
			return nil, &NotSupportedError{Operation: "get PIN status"}
		}
		return nil, fmt.Errorf("UIM get PIN status failed: %w", err)
	}

	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 3 {
		return &PINInfo{
			Status:             PINStatus(tlv.Value[0]),
			VerifyRetriesLeft:  tlv.Value[1],
			UnblockRetriesLeft: tlv.Value[2],
		}, nil
	}

	return nil, fmt.Errorf("no PIN status in response")
}

// GetSIMStatus queries the SIM card status / GetSIMStatus查询SIM卡状态
func (d *DMSService) GetSIMStatus(ctx context.Context) (SIMStatus, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMGetState, nil)
	if err != nil {
		return SIMAbsent, err
	}

	if err := resp.CheckResult(); err != nil {
		if qe := GetQMIError(err); qe != nil && qe.ErrorCode == QMIErrNotSupported {
			uim, uerr := NewUIMService(d.client)
			if uerr != nil {
				return SIMAbsent, &NotSupportedError{Operation: "get SIM status"}
			}
			defer uim.Close()
			return uim.GetCardStatus(ctx)
		}
		return SIMAbsent, fmt.Errorf("UIM get state failed: %w", err)
	}

	// TLV 0x01: UIM state / TLV 0x01: UIM状态
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		state := tlv.Value[0]
		switch state {
		case 0x00:
			return SIMReady, nil // UIM initialization completed / UIM初始化完成
		case 0x01:
			pin, err := d.GetPINStatus(ctx)
			if err == nil {
				switch pin.Status {
				case PINStatusNotVerified:
					return SIMPINRequired, nil
				case PINStatusBlocked:
					return SIMPUKRequired, nil
				case PINStatusPermBlocked:
					return SIMBlocked, nil
				}
			}
			return SIMPINRequired, nil // UIM is locked / UIM被锁定
		case 0x02:
			return SIMAbsent, nil // UIM not present / UIM不在位
		default:
			return SIMNotReady, nil
		}
	}

	return SIMAbsent, fmt.Errorf("no UIM state in response")
}

// VerifyPIN verifies the SIM PIN / VerifyPIN验证SIM PIN
func (d *DMSService) VerifyPIN(ctx context.Context, pin string) error {
	if len(pin) == 0 || len(pin) > 8 {
		return fmt.Errorf("invalid PIN length")
	}

	// TLV 0x01: PIN ID (1) and PIN value / TLV 0x01: PIN ID (1) 和 PIN值
	tlvData := append([]byte{0x01, uint8(len(pin))}, []byte(pin)...)
	tlvs := []TLV{{Type: 0x01, Value: tlvData}}

	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMVerifyPIN, tlvs)
	if err != nil {
		return err
	}

	if err := resp.CheckResult(); err != nil {
		if retrytlv := FindTLV(resp.TLVs, 0x10); retrytlv != nil && len(retrytlv.Value) >= 1 {
			return fmt.Errorf("PIN verification failed, %d retries left: %w", retrytlv.Value[0], err)
		}
		return fmt.Errorf("PIN verification failed: %w", err)
	}

	return nil
}

// GetOperatingMode queries the current operating mode / GetOperatingMode查询当前操作模式
func (d *DMSService) GetOperatingMode(ctx context.Context) (OperatingMode, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetOperatingMode, nil)
	if err != nil {
		return ModeOnline, err
	}

	if err := resp.CheckResult(); err != nil {
		return ModeOnline, fmt.Errorf("get operating mode failed: %w", err)
	}

	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		return OperatingMode(tlv.Value[0]), nil
	}

	return ModeOnline, fmt.Errorf("no mode in response")
}

// SetOperatingMode changes the modem operating mode / SetOperatingMode更改modem操作模式
// Use ModeOnline to turn radio on, ModeLowPower to turn off, ModeReset to reboot modem / 使用ModeOnline打开射频，ModeLowPower关闭，ModeReset重启modem
func (d *DMSService) SetOperatingMode(ctx context.Context, mode OperatingMode) error {
	tlvs := []TLV{NewTLVUint8(0x01, uint8(mode))}

	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSSetOperatingMode, tlvs)
	if err != nil {
		return err
	}

	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("set operating mode failed: %w", err)
	}
	return nil
}

// RadioPower turns the radio on or off / RadioPower打开或关闭射频
func (d *DMSService) RadioPower(ctx context.Context, on bool) error {
	if on {
		return d.SetOperatingMode(ctx, ModeOnline)
	}
	return d.SetOperatingMode(ctx, ModeLowPower)
}

// SetPINProtection enables or disables PIN protection / SetPINProtection 启用或禁用 PIN 保护
func (s *DMSService) SetPINProtection(ctx context.Context, pinID uint8, enabled bool, pin string) error {
	var tlvs []TLV

	// TLV 0x01: PIN Protection Info / PIN 保护信息
	// 1 byte PIN ID + 1 byte Enable/Disable (0/1) + PIN Length + PIN
	pinBytes := []byte(pin)
	buf := make([]byte, 2+1+len(pinBytes))
	buf[0] = pinID
	if enabled {
		buf[1] = 1
	} else {
		buf[1] = 0
	}
	buf[2] = uint8(len(pinBytes))
	copy(buf[3:], pinBytes)
	tlvs = append(tlvs, TLV{Type: 0x01, Value: buf})

	resp, err := s.client.SendRequest(ctx, ServiceDMS, s.clientID, DMSUIMSetPINProtection, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

// ChangePIN changes the PIN code / ChangePIN 修改 PIN 码
func (s *DMSService) ChangePIN(ctx context.Context, pinID uint8, oldPIN, newPIN string) error {
	var tlvs []TLV

	// TLV 0x01: PIN Info / PIN 信息
	// 1 byte PIN ID + Old PIN Length + Old PIN + New PIN Length + New PIN
	oldBytes := []byte(oldPIN)
	newBytes := []byte(newPIN)
	buf := make([]byte, 1+1+len(oldBytes)+1+len(newBytes))

	buf[0] = pinID
	buf[1] = uint8(len(oldBytes))
	copy(buf[2:], oldBytes)
	buf[2+len(oldBytes)] = uint8(len(newBytes))
	copy(buf[3+len(oldBytes):], newBytes)

	tlvs = append(tlvs, TLV{Type: 0x01, Value: buf})

	resp, err := s.client.SendRequest(ctx, ServiceDMS, s.clientID, DMSUIMChangePIN, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

// UnblockPIN unblocks the PIN using PUK / UnblockPIN 使用 PUK 解锁 PIN
func (s *DMSService) UnblockPIN(ctx context.Context, pinID uint8, puk, newPIN string) error {
	var tlvs []TLV

	// TLV 0x01: Unblock Info / 解锁信息
	// 1 byte PIN ID + PUK Length + PUK + New PIN Length + New PIN
	pukBytes := []byte(puk)
	newBytes := []byte(newPIN)
	buf := make([]byte, 1+1+len(pukBytes)+1+len(newBytes))

	buf[0] = pinID
	buf[1] = uint8(len(pukBytes))
	copy(buf[2:], pukBytes)
	buf[2+len(pukBytes)] = uint8(len(newBytes))
	copy(buf[3+len(pukBytes):], newBytes)

	tlvs = append(tlvs, TLV{Type: 0x01, Value: buf})

	resp, err := s.client.SendRequest(ctx, ServiceDMS, s.clientID, DMSUIMUnblockPIN, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

// ============================================================================
// Device Info / 设备信息
// ============================================================================

// DeviceInfo contains modem identification information / DeviceInfo包含modem识别信息
type DeviceInfo struct {
	Manufacturer     string
	Model            string
	Revision         string
	HardwareRevision string
	SoftwareVersion  string
	MSISDN           string
	FactorySKU       string
	IMEI             string
	ESN              string
	MEID             string
}

// DeviceCapabilities contains modem-wide capabilities reported by DMS.
type DeviceCapabilities struct {
	MaxTxChannelRate      uint32
	MaxRxChannelRate      uint32
	DataServiceCapability uint8
	SIMCapability         uint8
	RadioInterfaces       []uint8
}

// PowerStateInfo contains the modem power source and battery state flags.
type PowerStateInfo struct {
	Flags              uint8
	BatteryLevel       uint8
	ExternalSource     bool
	BatteryConnected   bool
	BatteryCharging    bool
	PowerFaultDetected bool
}

// TimeInfo contains the raw DMS device/system/user time counters.
type TimeInfo struct {
	DeviceTimeCount uint64
	TimeSource      uint16
	SystemTime      uint64
	HasSystemTime   bool
	UserTime        uint64
	HasUserTime     bool
}

// PRLVersionInfo contains PRL version metadata.
type PRLVersionInfo struct {
	Version              uint16
	PRLOnlyPreference    bool
	HasPRLOnlyPreference bool
}

// MACAddressInfo contains a raw MAC address plus a normalized printable string.
type MACAddressInfo struct {
	Type          uint32
	Address       []byte
	AddressString string
}

// BandCapabilities contains the modem-reported band masks and extended band lists.
type BandCapabilities struct {
	BandCapability            uint64
	HasBandCapability         bool
	LTEBandCapability         uint64
	HasLTEBandCapability      bool
	ExtendedLTEBandCapability []uint16
	NR5GBandCapability        []uint16
}

// GetDeviceSerialNumbers retrieves IMEI and other serial numbers / GetDeviceSerialNumbers检索IMEI和其他序列号
func (d *DMSService) GetDeviceSerialNumbers(ctx context.Context) (*DeviceInfo, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetDeviceSerialNumbers, nil)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get serial numbers failed: %w", err)
	}

	info := &DeviceInfo{}

	// TLV 0x10: ESN
	// TLV 0x11: IMEI
	// TLV 0x12: MEID
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		info.ESN = string(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil {
		info.IMEI = string(tlv.Value)
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil {
		info.MEID = string(tlv.Value)
	}
	return info, nil
}

func (d *DMSService) getStringValue(ctx context.Context, messageID uint16, operation string) (string, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, messageID, nil)
	if err != nil {
		return "", err
	}

	if err := resp.CheckResult(); err != nil {
		return "", fmt.Errorf("%s failed: %w", operation, err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil {
		return "", fmt.Errorf("%s response missing value TLV", operation)
	}
	return string(tlv.Value), nil
}

// GetIMSI retrieves the generic IMSI from DMS service directly / GetIMSI 直接从 DMS 服务检索通用 IMSI
func (d *DMSService) GetIMSI(ctx context.Context) (string, error) {
	// 0x0043 is standard QMI_DMS_GET_IMSI_REQ
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, 0x0043, nil)
	if err != nil {
		return "", err
	}

	if err := resp.CheckResult(); err != nil {
		return "", fmt.Errorf("DMS get IMSI failed: %w", err)
	}

	// TLV 0x01: IMSI String / TLV 0x01: IMSI 字符串
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) > 0 {
		return string(tlv.Value), nil
	}

	return "", fmt.Errorf("no IMSI in response")
}

// GetDeviceRevision retrieves firmware revision / GetDeviceRevision检索固件版本
func (d *DMSService) GetDeviceRevision(ctx context.Context) (string, string, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetDeviceRevID, nil)
	if err != nil {
		return "", "", err
	}

	if err := resp.CheckResult(); err != nil {
		return "", "", fmt.Errorf("get revision failed: %w", err)
	}

	var revision, bootVersion string

	// TLV 0x01: Device revision / TLV 0x01: 设备版本
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil {
		revision = string(tlv.Value)
	}

	// TLV 0x10: Boot version / TLV 0x10: Boot版本
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		bootVersion = string(tlv.Value)
	}

	return revision, bootVersion, nil
}

// GetManufacturer retrieves the modem manufacturer string.
func (d *DMSService) GetManufacturer(ctx context.Context) (string, error) {
	return d.getStringValue(ctx, DMSGetManufacturer, "get manufacturer")
}

// GetModel retrieves the modem model string.
func (d *DMSService) GetModel(ctx context.Context) (string, error) {
	return d.getStringValue(ctx, DMSGetModel, "get model")
}

// GetHardwareRevision retrieves the hardware revision string.
func (d *DMSService) GetHardwareRevision(ctx context.Context) (string, error) {
	return d.getStringValue(ctx, DMSGetHardwareRevision, "get hardware revision")
}

// GetSoftwareVersion retrieves the software version string.
func (d *DMSService) GetSoftwareVersion(ctx context.Context) (string, error) {
	return d.getStringValue(ctx, DMSGetSoftwareVersion, "get software version")
}

// GetMSISDN retrieves the MSISDN string when available from the network/SIM configuration.
func (d *DMSService) GetMSISDN(ctx context.Context) (string, error) {
	return d.getStringValue(ctx, DMSGetMSISDN, "get MSISDN")
}

// GetFactorySKU retrieves the factory SKU string.
func (d *DMSService) GetFactorySKU(ctx context.Context) (string, error) {
	return d.getStringValue(ctx, DMSGetFactorySKU, "get factory SKU")
}

// GetICCID retrieves ICCID via DMS UIM helper support when available.
func (d *DMSService) GetICCID(ctx context.Context) (string, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMGetICCID, nil)
	if err != nil {
		return "", err
	}

	if err := resp.CheckResult(); err != nil {
		return "", fmt.Errorf("DMS get ICCID failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) == 0 {
		return "", fmt.Errorf("no ICCID in response")
	}
	return string(tlv.Value), nil
}

// GetCapabilities retrieves device-wide data/SIM/radio capability information.
func (d *DMSService) GetCapabilities(ctx context.Context) (*DeviceCapabilities, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetCapabilities, nil)
	if err != nil {
		return nil, err
	}
	return parseDeviceCapabilitiesResponse(resp)
}

// GetPowerState retrieves the current power-source and battery state flags.
func (d *DMSService) GetPowerState(ctx context.Context) (*PowerStateInfo, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetPowerState, nil)
	if err != nil {
		return nil, err
	}
	return parsePowerStateResponse(resp)
}

// GetTime retrieves the raw device/system/user time counters reported by the modem.
func (d *DMSService) GetTime(ctx context.Context) (*TimeInfo, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetTime, nil)
	if err != nil {
		return nil, err
	}
	return parseTimeResponse(resp)
}

// GetPRLVersion retrieves the PRL version and optional PRL-only preference flag.
func (d *DMSService) GetPRLVersion(ctx context.Context) (*PRLVersionInfo, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetPRLVersion, nil)
	if err != nil {
		return nil, err
	}
	return parsePRLVersionResponse(resp)
}

// GetActivationState retrieves the current service activation state.
func (d *DMSService) GetActivationState(ctx context.Context) (ActivationState, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetActivationState, nil)
	if err != nil {
		return ActivationStateNotActivated, err
	}
	return parseActivationStateResponse(resp)
}

// GetUserLockState retrieves whether the user lock is enabled.
func (d *DMSService) GetUserLockState(ctx context.Context) (bool, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetUserLockState, nil)
	if err != nil {
		return false, err
	}
	return parseUserLockStateResponse(resp)
}

// SetUserLockState enables or disables the device user lock using the current 4-digit lock code.
func (d *DMSService) SetUserLockState(ctx context.Context, enabled bool, lockCode string) error {
	if len(lockCode) != 4 {
		return fmt.Errorf("lock code must be exactly 4 characters")
	}
	tlvValue := make([]byte, 1+len(lockCode))
	tlvValue[0] = boolToUint8(enabled)
	copy(tlvValue[1:], []byte(lockCode))
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSSetUserLockState, []TLV{{Type: 0x01, Value: tlvValue}})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("set user lock state failed: %w", err)
	}
	return nil
}

// SetUserLockCode changes the current 4-digit user lock code.
func (d *DMSService) SetUserLockCode(ctx context.Context, oldCode, newCode string) error {
	if len(oldCode) != 4 || len(newCode) != 4 {
		return fmt.Errorf("old and new lock code must both be exactly 4 characters")
	}
	tlvValue := make([]byte, 8)
	copy(tlvValue[0:4], []byte(oldCode))
	copy(tlvValue[4:8], []byte(newCode))
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSSetUserLockCode, []TLV{{Type: 0x01, Value: tlvValue}})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("set user lock code failed: %w", err)
	}
	return nil
}

// ReadUserData reads the modem's user-data blob.
func (d *DMSService) ReadUserData(ctx context.Context) ([]byte, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSReadUserData, nil)
	if err != nil {
		return nil, err
	}
	return parsePrefixedBytesResponse(resp, 0x01, 2, "read user data")
}

// WriteUserData writes the modem's user-data blob.
func (d *DMSService) WriteUserData(ctx context.Context, data []byte) error {
	if len(data) > 0xFFFF {
		return fmt.Errorf("user data too large: %d", len(data))
	}
	tlvValue := make([]byte, 2+len(data))
	binary.LittleEndian.PutUint16(tlvValue[0:2], uint16(len(data)))
	copy(tlvValue[2:], data)
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSWriteUserData, []TLV{{Type: 0x01, Value: tlvValue}})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("write user data failed: %w", err)
	}
	return nil
}

// GetMACAddress retrieves a device MAC address for the requested MAC type.
func (d *DMSService) GetMACAddress(ctx context.Context, macType uint32) (*MACAddressInfo, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetMACAddress, []TLV{NewTLVUint32(0x01, macType)})
	if err != nil {
		return nil, err
	}
	return parseMACAddressResponse(resp, macType)
}

// GetBandCapabilities retrieves the modem-supported band masks and extended band lists.
func (d *DMSService) GetBandCapabilities(ctx context.Context) (*BandCapabilities, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetBandCapabilities, nil)
	if err != nil {
		return nil, err
	}
	return parseBandCapabilitiesResponse(resp)
}

func parseDeviceCapabilitiesResponse(resp *Packet) (*DeviceCapabilities, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get capabilities failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) < 11 {
		return nil, fmt.Errorf("capabilities TLV missing or too short")
	}

	info := &DeviceCapabilities{
		MaxTxChannelRate:      binary.LittleEndian.Uint32(tlv.Value[0:4]),
		MaxRxChannelRate:      binary.LittleEndian.Uint32(tlv.Value[4:8]),
		DataServiceCapability: tlv.Value[8],
		SIMCapability:         tlv.Value[9],
	}

	radioCount := int(tlv.Value[10])
	if len(tlv.Value) < 11+radioCount {
		return nil, fmt.Errorf("radio interface list truncated")
	}
	if radioCount > 0 {
		info.RadioInterfaces = append([]uint8(nil), tlv.Value[11:11+radioCount]...)
	}
	return info, nil
}

func parsePowerStateResponse(resp *Packet) (*PowerStateInfo, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get power state failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("power state TLV missing or too short")
	}

	flags := tlv.Value[0]
	return &PowerStateInfo{
		Flags:              flags,
		BatteryLevel:       tlv.Value[1],
		ExternalSource:     flags&DMSPowerStateExternalSource != 0,
		BatteryConnected:   flags&DMSPowerStateBatteryConnected != 0,
		BatteryCharging:    flags&DMSPowerStateBatteryCharging != 0,
		PowerFaultDetected: flags&DMSPowerStateFault != 0,
	}, nil
}

func parseTimeResponse(resp *Packet) (*TimeInfo, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get time failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) < 8 {
		return nil, fmt.Errorf("device time TLV missing or too short")
	}

	info := &TimeInfo{
		DeviceTimeCount: parseUint48LE(tlv.Value[0:6]),
		TimeSource:      binary.LittleEndian.Uint16(tlv.Value[6:8]),
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 8 {
		info.SystemTime = binary.LittleEndian.Uint64(tlv.Value[0:8])
		info.HasSystemTime = true
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 8 {
		info.UserTime = binary.LittleEndian.Uint64(tlv.Value[0:8])
		info.HasUserTime = true
	}
	return info, nil
}

func parsePRLVersionResponse(resp *Packet) (*PRLVersionInfo, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get PRL version failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("PRL version TLV missing or too short")
	}

	info := &PRLVersionInfo{
		Version: binary.LittleEndian.Uint16(tlv.Value[0:2]),
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		info.PRLOnlyPreference = tlv.Value[0] != 0
		info.HasPRLOnlyPreference = true
	}
	return info, nil
}

func parseActivationStateResponse(resp *Packet) (ActivationState, error) {
	if err := resp.CheckResult(); err != nil {
		return ActivationStateNotActivated, fmt.Errorf("get activation state failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) < 2 {
		return ActivationStateNotActivated, fmt.Errorf("activation state TLV missing or too short")
	}
	return ActivationState(binary.LittleEndian.Uint16(tlv.Value[0:2])), nil
}

func parseUserLockStateResponse(resp *Packet) (bool, error) {
	if err := resp.CheckResult(); err != nil {
		return false, fmt.Errorf("get user lock state failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil || len(tlv.Value) < 1 {
		return false, fmt.Errorf("user lock state TLV missing or too short")
	}
	return tlv.Value[0] != 0, nil
}

func parseMACAddressResponse(resp *Packet, macType uint32) (*MACAddressInfo, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get MAC address failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 1 {
		return nil, fmt.Errorf("MAC address TLV missing or too short")
	}
	length := int(tlv.Value[0])
	if len(tlv.Value) < 1+length {
		return nil, fmt.Errorf("MAC address TLV truncated")
	}
	address := append([]byte(nil), tlv.Value[1:1+length]...)
	return &MACAddressInfo{
		Type:          macType,
		Address:       address,
		AddressString: net.HardwareAddr(address).String(),
	}, nil
}

func parseUint48LE(b []byte) uint64 {
	if len(b) < 6 {
		return 0
	}
	return uint64(b[0]) |
		uint64(b[1])<<8 |
		uint64(b[2])<<16 |
		uint64(b[3])<<24 |
		uint64(b[4])<<32 |
		uint64(b[5])<<40
}

func parseBandCapabilitiesResponse(resp *Packet) (*BandCapabilities, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get band capabilities failed: %w", err)
	}

	info := &BandCapabilities{}

	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 8 {
		info.BandCapability = binary.LittleEndian.Uint64(tlv.Value[0:8])
		info.HasBandCapability = true
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 8 {
		info.LTEBandCapability = binary.LittleEndian.Uint64(tlv.Value[0:8])
		info.HasLTEBandCapability = true
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil {
		values, err := parseUint16Array(tlv.Value)
		if err != nil {
			return nil, fmt.Errorf("parse extended LTE band capability: %w", err)
		}
		info.ExtendedLTEBandCapability = values
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil {
		values, err := parseUint16Array(tlv.Value)
		if err != nil {
			return nil, fmt.Errorf("parse NR5G band capability: %w", err)
		}
		info.NR5GBandCapability = values
	}

	if !info.HasBandCapability && !info.HasLTEBandCapability && len(info.ExtendedLTEBandCapability) == 0 && len(info.NR5GBandCapability) == 0 {
		return nil, fmt.Errorf("no band capability TLVs in response")
	}

	return info, nil
}

func parsePrefixedBytesResponse(resp *Packet, tlvType uint8, prefixSize int, operation string) ([]byte, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("%s failed: %w", operation, err)
	}

	tlv := FindTLV(resp.TLVs, tlvType)
	if tlv == nil || len(tlv.Value) < prefixSize {
		return nil, fmt.Errorf("%s TLV missing or too short", operation)
	}

	var length int
	switch prefixSize {
	case 1:
		length = int(tlv.Value[0])
	case 2:
		length = int(binary.LittleEndian.Uint16(tlv.Value[0:2]))
	default:
		return nil, fmt.Errorf("unsupported prefix size %d", prefixSize)
	}
	if len(tlv.Value) < prefixSize+length {
		return nil, fmt.Errorf("%s TLV truncated", operation)
	}
	return append([]byte(nil), tlv.Value[prefixSize:prefixSize+length]...), nil
}

func parseUint16Array(value []byte) ([]uint16, error) {
	if len(value) < 2 {
		return nil, fmt.Errorf("array TLV too short")
	}
	count := int(binary.LittleEndian.Uint16(value[0:2]))
	if len(value) < 2+count*2 {
		return nil, fmt.Errorf("array TLV truncated: need %d bytes, have %d", 2+count*2, len(value))
	}

	out := make([]uint16, 0, count)
	offset := 2
	for i := 0; i < count; i++ {
		out = append(out, binary.LittleEndian.Uint16(value[offset:offset+2]))
		offset += 2
	}
	return out, nil
}
