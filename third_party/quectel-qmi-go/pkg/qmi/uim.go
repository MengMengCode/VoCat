package qmi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/warthog618/sms/encoding/gsm7"
)

const (
	// UIM Message IDs / UIM消息ID
	UIMReset                uint16 = 0x0000
	UIMGetSupportedMessages uint16 = 0x001E
	UIMReadTransparent      uint16 = 0x0020
	UIMReadRecord           uint16 = 0x0021
	UIMGetFileAttrs         uint16 = 0x0024
	/* Defined in frame.go / 在 frame.go 中定义
	UIMVerifyPIN            uint16 = 0x0026
	*/
	UIMSetPINProtection          uint16 = 0x0025
	UIMUnblockPIN                uint16 = 0x0027
	UIMChangePIN                 uint16 = 0x0028
	UIMRefreshRegister           uint16 = 0x002A
	UIMRefreshComplete           uint16 = 0x002C
	UIMRegisterEvents            uint16 = 0x002E
	UIMPowerOffSIM               uint16 = 0x0030
	UIMPowerOnSIM                uint16 = 0x0031
	UIMStatusChangeInd           uint16 = 0x0032
	UIMRefreshInd                uint16 = 0x0033
	UIMChangeProvisioningSession uint16 = 0x0038
	UIMSessionClosedInd          uint16 = 0x0043
	UIMRefreshRegisterAll        uint16 = 0x0044
	/* Defined in frame.go / 在 frame.go 中定义
	UIMGetCardStatus        uint16 = 0x002F
	*/
	UIMSwitchSlot    uint16 = 0x0046
	UIMGetSlotStatus uint16 = 0x0047
	UIMSlotStatusInd uint16 = 0x0048
)

const (
	UIMEventRegistrationCardStatus         uint32 = 1 << 0
	UIMEventRegistrationSAPConnection      uint32 = 1 << 1
	UIMEventRegistrationExtendedCardStatus uint32 = 1 << 2
	UIMEventRegistrationPhysicalSlotStatus uint32 = 1 << 4
)

const (
	UIMSessionTypePrimaryGWProvisioning   uint8 = 0
	UIMSessionTypePrimary1XProvisioning   uint8 = 1
	UIMSessionTypeSecondaryGWProvisioning uint8 = 2
	UIMSessionTypeSecondary1XProvisioning uint8 = 3
	UIMSessionTypeNonProvisioningSlot1    uint8 = 4
	UIMSessionTypeNonProvisioningSlot2    uint8 = 5
	UIMSessionTypeCardSlot1               uint8 = 6
	UIMSessionTypeCardSlot2               uint8 = 7
	UIMSessionTypeLogicalChannelSlot1     uint8 = 8
	UIMSessionTypeLogicalChannelSlot2     uint8 = 9
	UIMSessionTypeNonProvisioningSlot3    uint8 = 16
	UIMSessionTypeCardSlot3               uint8 = 19
	UIMSessionTypeLogicalChannelSlot3     uint8 = 22
)

const (
	UIMFileTypeTransparent uint8 = 0
	UIMFileTypeCyclic      uint8 = 1
	UIMFileTypeLinearFixed uint8 = 2
	UIMFileTypeDedicated   uint8 = 3
	UIMFileTypeMaster      uint8 = 4
)

const (
	UIMPhysicalCardStateUnknown uint32 = 0
	UIMPhysicalCardStateAbsent  uint32 = 1
	UIMPhysicalCardStatePresent uint32 = 2
)

const (
	UIMSlotStateInactive uint32 = 0
	UIMSlotStateActive   uint32 = 1
)

const (
	UIMCardProtocolUnknown uint32 = 0
	UIMCardProtocolICC     uint32 = 1
	UIMCardProtocolUICC    uint32 = 2
)

const (
	UIMAppTypeUnknown uint8 = 0
	UIMAppTypeSIM     uint8 = 1
	UIMAppTypeUSIM    uint8 = 2
	UIMAppTypeRUIM    uint8 = 3
	UIMAppTypeCSIM    uint8 = 4
	UIMAppTypeISIM    uint8 = 5
)

var (
	uimUSIMAIDPrefix = []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02}
	uimISIMAIDPrefix = []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04}
)

// CardStatus represents the SIM card status / CardStatus代表SIM卡状态

// ============================================================================
// UIM Service wrapper / UIM服务包装器
// ============================================================================

type UIMService struct {
	client   *Client
	clientID uint8
}

type CardStatusDetails struct {
	CardState           uint8
	ErrorCode           uint8
	NumSlot             uint8
	NumApp              uint8
	AppType             uint8
	AppState            uint8
	PersoState          uint8
	PersoFeature        uint8
	PersoRetries        uint8
	PersoUnblockRetries uint8
	AID                 []byte
	PIN1State           PINStatus
	PIN1Retries         uint8
	PUK1Retries         uint8
	PIN2State           PINStatus
	PIN2Retries         uint8
	PUK2Retries         uint8
	UsesUPIN            bool
	UPINState           PINStatus
	UPINRetries         uint8
	UPUKRetries         uint8
}

type uimCardStatusApp struct {
	appType             uint8
	appState            uint8
	persoState          uint8
	persoFeature        uint8
	persoRetries        uint8
	persoUnblockRetries uint8
	aid                 []byte
	pin                 QMIUIM_PIN_STATE
}

type QMIUIM_PIN_STATE struct {
	UnivPIN     uint8
	PIN1State   uint8
	PIN1Retries uint8
	PUK1Retries uint8
	PIN2State   uint8
	PIN2Retries uint8
	PUK2Retries uint8
}

type UIMCardResult struct {
	SW1 uint8
	SW2 uint8
}

type UIMRecordData struct {
	CardResult                   UIMCardResult
	HasCardResult                bool
	Data                         []byte
	AdditionalData               []byte
	ResponseInIndicationToken    uint32
	HasResponseInIndicationToken bool
}

type UIMSecurityAttributes struct {
	Logic      uint8
	Attributes uint16
}

type UIMFileAttributes struct {
	CardResult                   UIMCardResult
	HasCardResult                bool
	FileSize                     uint16
	FileID                       uint16
	FileType                     uint8
	RecordSize                   uint16
	RecordCount                  uint16
	ReadSecurity                 UIMSecurityAttributes
	WriteSecurity                UIMSecurityAttributes
	IncreaseSecurity             UIMSecurityAttributes
	DeactivateSecurity           UIMSecurityAttributes
	ActivateSecurity             UIMSecurityAttributes
	RawData                      []byte
	ResponseInIndicationToken    uint32
	HasResponseInIndicationToken bool
}

type UIMSlotStatusSlot struct {
	PhysicalCardStatus uint32
	PhysicalSlotStatus uint32
	LogicalSlot        uint8
	ICCIDRaw           []byte
	ICCID              string
	CardProtocol       uint32
	ValidApplications  uint8
	ATR                []byte
	HasExtendedInfo    bool
	IsEUICC            bool
	EID                []byte
	HasEID             bool
}

type UIMSlotStatus struct {
	Slots []UIMSlotStatusSlot
}

type UIMRefreshFile struct {
	FileID uint16
	Path   []uint8
}

type SIMMetadata struct {
	NativeMCC    string
	NativeMNC    string
	GID1         string
	GID2         string
	PNN          []PNNRecord
	OPL          []OPLRecord
	ServiceTable *SIMServiceTable
}

type PNNRecord struct {
	Record    int    `json:"record"`
	FullName  string `json:"full_name,omitempty"`
	ShortName string `json:"short_name,omitempty"`
	RawHex    string `json:"raw_hex,omitempty"`
}

type OPLRecord struct {
	Record    int    `json:"record"`
	PLMN      string `json:"plmn,omitempty"`
	LACStart  uint16 `json:"lac_start,omitempty"`
	LACEnd    uint16 `json:"lac_end,omitempty"`
	PNNRecord int    `json:"pnn_record,omitempty"`
	RawHex    string `json:"raw_hex,omitempty"`
}

type SIMServiceTable struct {
	Kind            string `json:"kind,omitempty"`
	RawHex          string `json:"raw_hex,omitempty"`
	EnabledServices []int  `json:"enabled_services,omitempty"`
}

type UIMChangeProvisioningSessionRequest struct {
	SessionType           uint8
	Activate              bool
	Slot                  *uint8
	ApplicationIdentifier []byte
}

type UIMRefreshRegisterRequest struct {
	SessionType           uint8
	ApplicationIdentifier []byte
	RegisterFlag          bool
	VoteForInit           bool
	Files                 []UIMRefreshFile
}

type UIMRefreshCompleteRequest struct {
	SessionType           uint8
	ApplicationIdentifier []byte
	RefreshSuccess        bool
}

type UIMRefreshRegisterAllRequest struct {
	SessionType           uint8
	ApplicationIdentifier []byte
	RegisterFlag          bool
}

type UIMRefreshIndication struct {
	Stage                 uint8
	Mode                  uint8
	SessionType           uint8
	ApplicationIdentifier []byte
	Files                 []UIMRefreshFile
}

// NewUIMService creates a UIM service wrapper / NewUIMService创建一个UIM服务包装器
func NewUIMService(client *Client) (*UIMService, error) {
	return NewUIMServiceWithContext(context.Background(), client)
}

func NewUIMServiceWithContext(ctx context.Context, client *Client) (*UIMService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceUIM)
	if err != nil {
		return nil, err
	}
	return &UIMService{client: client, clientID: clientID}, nil
}

// Close releases the UIM client ID / Close释放UIM客户端ID
func (u *UIMService) Close() error {
	return u.client.ReleaseClientID(ServiceUIM, u.clientID)
}

func (u *UIMService) GetCardStatusDetails(ctx context.Context) (*CardStatusDetails, SIMStatus, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMGetCardStatus, nil)
	if err != nil {
		return nil, SIMAbsent, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, SIMAbsent, fmt.Errorf("UIM get card status failed: %w", err)
	}

	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 15 {
		return nil, SIMNotReady, fmt.Errorf("card status TLV missing or too short")
	}

	v := tlv.Value
	details := &CardStatusDetails{}
	details.NumSlot = v[8]
	details.CardState = v[9]
	details.UPINState = PINStatus(v[10])
	details.UPINRetries = v[11]
	details.UPUKRetries = v[12]
	details.ErrorCode = v[13]
	details.NumApp = v[14]

	apps := parseUIMCardStatusApps(v, details.NumApp)

	var chosen *uimCardStatusApp
	for i := range apps {
		if apps[i].appType == 0x02 {
			chosen = &apps[i]
			break
		}
	}
	if chosen == nil && len(apps) > 0 {
		chosen = &apps[0]
	}
	if chosen != nil {
		details.AppType = chosen.appType
		details.AppState = chosen.appState
		details.PersoState = chosen.persoState
		details.PersoFeature = chosen.persoFeature
		details.PersoRetries = chosen.persoRetries
		details.PersoUnblockRetries = chosen.persoUnblockRetries
		details.AID = chosen.aid
		details.UsesUPIN = chosen.pin.UnivPIN == 1
		details.PIN1State = PINStatus(chosen.pin.PIN1State)
		details.PIN1Retries = chosen.pin.PIN1Retries
		details.PUK1Retries = chosen.pin.PUK1Retries
		details.PIN2State = PINStatus(chosen.pin.PIN2State)
		details.PIN2Retries = chosen.pin.PIN2Retries
		details.PUK2Retries = chosen.pin.PUK2Retries
	}

	status := SIMNotReady
	switch details.CardState {
	case 0x00:
		status = SIMAbsent
	case 0x02:
		status = SIMBlocked
	case 0x01:
		state := details.PIN1State
		verifyRetries := details.PIN1Retries
		unblockRetries := details.PUK1Retries
		if details.UsesUPIN {
			state = details.UPINState
			verifyRetries = details.UPINRetries
			unblockRetries = details.UPUKRetries
		}
		_ = verifyRetries
		_ = unblockRetries
		switch state {
		case PINStatusNotVerified:
			status = SIMPINRequired
		case PINStatusBlocked:
			status = SIMPUKRequired
		case PINStatusPermBlocked:
			status = SIMBlocked
		case PINStatusNotInit, PINStatusVerified, PINStatusDisabled, PINStatusUnblocked, PINStatusChanged:
			status = SIMReady
		default:
			status = SIMNotReady
		}
		if status == SIMReady && (details.PersoState == 1 || details.PersoState == 3 || details.PersoState == 4) {
			status = SIMNetworkLocked
		}
	default:
		status = SIMNotReady
	}

	return details, status, nil
}

func parseUIMCardStatusApps(v []byte, numApp uint8) []uimCardStatusApp {
	offset := 15
	apps := make([]uimCardStatusApp, 0, int(numApp))
	for i := 0; i < int(numApp); i++ {
		if offset+7 > len(v) {
			break
		}
		app := uimCardStatusApp{
			appType:             v[offset],
			appState:            v[offset+1],
			persoState:          v[offset+2],
			persoFeature:        v[offset+3],
			persoRetries:        v[offset+4],
			persoUnblockRetries: v[offset+5],
		}
		aidLen := int(v[offset+6])
		offset += 7
		if offset+aidLen > len(v) {
			break
		}
		if aidLen > 0 {
			app.aid = make([]byte, aidLen)
			copy(app.aid, v[offset:offset+aidLen])
		}
		offset += aidLen
		if offset+7 > len(v) {
			break
		}
		app.pin = QMIUIM_PIN_STATE{
			UnivPIN:     v[offset],
			PIN1State:   v[offset+1],
			PIN1Retries: v[offset+2],
			PUK1Retries: v[offset+3],
			PIN2State:   v[offset+4],
			PIN2Retries: v[offset+5],
			PUK2Retries: v[offset+6],
		}
		offset += 7
		apps = append(apps, app)
	}
	return apps
}

// GetCardStatus queries the UIM card status / GetCardStatus查询UIM卡状态
func (u *UIMService) GetCardStatus(ctx context.Context) (SIMStatus, error) {
	_, st, err := u.GetCardStatusDetails(ctx)
	return st, err
}

func (u *UIMService) GetUSIMAID(ctx context.Context) ([]byte, error) {
	return u.getCardStatusAID(ctx, UIMAppTypeUSIM, uimUSIMAIDPrefix, "USIM")
}

func (u *UIMService) GetISIMAID(ctx context.Context) ([]byte, error) {
	return u.getCardStatusAID(ctx, UIMAppTypeISIM, uimISIMAIDPrefix, "ISIM")
}

func (u *UIMService) getCardStatusAID(ctx context.Context, appType uint8, prefix []byte, label string) ([]byte, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMGetCardStatus, nil)
	if err != nil {
		return nil, err
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("UIM get card status failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 15 {
		return nil, fmt.Errorf("card status TLV missing or too short")
	}
	apps := parseUIMCardStatusApps(tlv.Value, tlv.Value[14])
	for _, app := range apps {
		if app.appType != appType {
			continue
		}
		if len(app.aid) < len(prefix) || !bytes.Equal(app.aid[:len(prefix)], prefix) {
			return nil, fmt.Errorf("UIM %s AID invalid: %X", label, app.aid)
		}
		return append([]byte(nil), app.aid...), nil
	}
	return nil, fmt.Errorf("UIM %s application not found: app_type=0x%02x", label, appType)
}

// VerifyPIN verifies the PIN code / VerifyPIN 验证 PIN 码
func (u *UIMService) VerifyPIN(ctx context.Context, pinID uint8, pin string) error {
	var tlvs []TLV

	// TLV 0x01: PIN Info / PIN 信息
	// PIN ID (1) + PIN Len (1) + PIN Value
	pinBytes := []byte(pin)
	buf := make([]byte, 2+len(pinBytes))
	buf[0] = pinID
	buf[1] = uint8(len(pinBytes))
	copy(buf[2:], pinBytes)
	tlvs = append(tlvs, TLV{Type: 0x01, Value: buf})

	// TLV 0x02: Session Info / 会话信息 (Optional, default usually works)
	// AidLen (1) + Aid...
	// For simplicity, we omit session info assuming default primary session

	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMVerifyPIN, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

// SetPINProtection enables or disables PIN protection / SetPINProtection 启用或禁用 PIN 保护
func (u *UIMService) SetPINProtection(ctx context.Context, pinID uint8, enabled bool, pin string) error {
	var tlvs []TLV

	// TLV 0x01: PIN Info / PIN 信息
	pinBytes := []byte(pin)
	buf := make([]byte, 2+1+len(pinBytes)) // ID + Enable + Len + PIN
	buf[0] = pinID
	if enabled {
		buf[1] = 1
	} else {
		buf[1] = 0
	}
	buf[2] = uint8(len(pinBytes))
	copy(buf[3:], pinBytes)
	tlvs = append(tlvs, TLV{Type: 0x01, Value: buf})

	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMSetPINProtection, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

// ChangePIN changes the PIN code / ChangePIN 修改 PIN 码
func (u *UIMService) ChangePIN(ctx context.Context, pinID uint8, oldPIN, newPIN string) error {
	var tlvs []TLV

	// TLV 0x01: PIN Info / PIN 信息
	oldBytes := []byte(oldPIN)
	newBytes := []byte(newPIN)
	buf := make([]byte, 1+1+len(oldBytes)+1+len(newBytes))

	buf[0] = pinID
	buf[1] = uint8(len(oldBytes))
	copy(buf[2:], oldBytes)
	buf[2+len(oldBytes)] = uint8(len(newBytes))
	copy(buf[3+len(oldBytes):], newBytes)

	tlvs = append(tlvs, TLV{Type: 0x01, Value: buf})

	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMChangePIN, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

// UnblockPIN unblocks the PIN using PUK / UnblockPIN 使用 PUK 解锁 PIN
func (u *UIMService) UnblockPIN(ctx context.Context, pinID uint8, puk, newPIN string) error {
	var tlvs []TLV

	// TLV 0x01: Unblock Info / 解锁信息
	pukBytes := []byte(puk)
	newBytes := []byte(newPIN)
	buf := make([]byte, 1+1+len(pukBytes)+1+len(newBytes))

	buf[0] = pinID
	buf[1] = uint8(len(pukBytes))
	copy(buf[2:], pukBytes)
	buf[2+len(pukBytes)] = uint8(len(newBytes))
	copy(buf[3+len(pukBytes):], newBytes)

	tlvs = append(tlvs, TLV{Type: 0x01, Value: buf})

	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMUnblockPIN, tlvs)
	if err != nil {
		return err
	}
	return resp.CheckResult()
}

func buildOpenLogicalChannelTLVs(slot uint8, aid []byte) []TLV {
	value := append([]byte{byte(len(aid))}, aid...)
	return []TLV{
		{Type: 0x10, Value: value},
		{Type: 0x01, Value: []byte{slot}},
	}
}

func buildUIMSessionTLV(sessionType uint8, aid []byte) TLV {
	value := make([]byte, 2+len(aid))
	value[0] = sessionType
	value[1] = uint8(len(aid))
	copy(value[2:], aid)
	return TLV{Type: 0x01, Value: value}
}

func buildUIMFileTLV(fileID uint16, path []uint8) TLV {
	value := make([]byte, 3+len(path))
	binary.LittleEndian.PutUint16(value[0:2], fileID)
	value[2] = uint8(len(path))
	copy(value[3:], path)
	return TLV{Type: 0x02, Value: value}
}

func buildCloseLogicalChannelTLVs(slot uint8, channel uint8) []TLV {
	return []TLV{
		{Type: 0x01, Value: []byte{slot}},
		{Type: 0x11, Value: []byte{channel}},
		{Type: 0x13, Value: []byte{0x01}},
	}
}

func buildSendAPDUTLVs(slot uint8, channel uint8, command []byte) []TLV {
	length := len(command)
	value := make([]byte, 2+len(command))
	binary.LittleEndian.PutUint16(value[0:2], uint16(length))
	copy(value[2:], command)
	return []TLV{
		{Type: 0x10, Value: []byte{channel}},
		{Type: 0x02, Value: value},
		{Type: 0x01, Value: []byte{slot}},
	}
}

func uimBoolToByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func buildChangeProvisioningSessionTLVs(req UIMChangeProvisioningSessionRequest) []TLV {
	tlvs := []TLV{
		{Type: 0x01, Value: []byte{req.SessionType, uimBoolToByte(req.Activate)}},
	}

	if req.Slot != nil || len(req.ApplicationIdentifier) > 0 {
		aid := append([]byte(nil), req.ApplicationIdentifier...)
		value := make([]byte, 2+len(aid))
		if req.Slot != nil {
			value[0] = *req.Slot
		}
		value[1] = uint8(len(aid))
		copy(value[2:], aid)
		tlvs = append(tlvs, TLV{Type: 0x10, Value: value})
	}

	return tlvs
}

func buildRefreshRegisterInfoTLV(req UIMRefreshRegisterRequest) (TLV, error) {
	if len(req.Files) > 0xFFFF {
		return TLV{}, fmt.Errorf("too many refresh files: %d", len(req.Files))
	}

	totalLen := 4
	for i, file := range req.Files {
		if len(file.Path) > 0xFF {
			return TLV{}, fmt.Errorf("refresh file path too long at index %d: %d", i, len(file.Path))
		}
		totalLen += 3 + len(file.Path)
	}

	value := make([]byte, totalLen)
	value[0] = uimBoolToByte(req.RegisterFlag)
	value[1] = uimBoolToByte(req.VoteForInit)
	binary.LittleEndian.PutUint16(value[2:4], uint16(len(req.Files)))

	offset := 4
	for _, file := range req.Files {
		binary.LittleEndian.PutUint16(value[offset:offset+2], file.FileID)
		value[offset+2] = uint8(len(file.Path))
		offset += 3
		copy(value[offset:offset+len(file.Path)], file.Path)
		offset += len(file.Path)
	}

	return TLV{Type: 0x02, Value: value}, nil
}

func buildRefreshCompleteInfoTLV(req UIMRefreshCompleteRequest) TLV {
	return TLV{Type: 0x02, Value: []byte{uimBoolToByte(req.RefreshSuccess)}}
}

func buildRefreshRegisterAllInfoTLV(req UIMRefreshRegisterAllRequest) TLV {
	return TLV{Type: 0x02, Value: []byte{uimBoolToByte(req.RegisterFlag)}}
}

func parseSupportedMessagesTLV(resp *Packet) ([]uint8, error) {
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

func parseRefreshEventTLV(value []byte) (*UIMRefreshIndication, error) {
	if len(value) < 6 {
		return nil, fmt.Errorf("refresh event TLV too short")
	}

	info := &UIMRefreshIndication{
		Stage:       value[0],
		Mode:        value[1],
		SessionType: value[2],
	}

	offset := 3
	aidLen := int(value[offset])
	offset++
	if offset+aidLen+2 > len(value) {
		return nil, fmt.Errorf("refresh event TLV truncated in application identifier")
	}
	if aidLen > 0 {
		info.ApplicationIdentifier = append([]byte(nil), value[offset:offset+aidLen]...)
	}
	offset += aidLen

	fileCount := int(binary.LittleEndian.Uint16(value[offset : offset+2]))
	offset += 2
	info.Files = make([]UIMRefreshFile, 0, fileCount)

	for i := 0; i < fileCount; i++ {
		if offset+3 > len(value) {
			return nil, fmt.Errorf("refresh event file entry %d truncated", i)
		}
		fileID := binary.LittleEndian.Uint16(value[offset : offset+2])
		pathLen := int(value[offset+2])
		offset += 3
		if offset+pathLen > len(value) {
			return nil, fmt.Errorf("refresh event file path %d truncated", i)
		}
		file := UIMRefreshFile{FileID: fileID}
		if pathLen > 0 {
			file.Path = append([]byte(nil), value[offset:offset+pathLen]...)
		}
		offset += pathLen
		info.Files = append(info.Files, file)
	}

	if offset != len(value) {
		return nil, fmt.Errorf("refresh event TLV has %d trailing bytes", len(value)-offset)
	}

	return info, nil
}

func wrapUIMNotSupported(operation string, err error) error {
	if qe := GetQMIError(err); qe != nil && (qe.ErrorCode == QMIErrNotSupported || qe.ErrorCode == QMIErrInvalidQmiCmd) {
		return &NotSupportedError{Operation: operation}
	}
	return err
}

func parseUIMCardResult(tlv *TLV) (UIMCardResult, bool, error) {
	if tlv == nil {
		return UIMCardResult{}, false, nil
	}
	if len(tlv.Value) < 2 {
		return UIMCardResult{}, false, fmt.Errorf("UIM card result TLV too short")
	}
	return UIMCardResult{
		SW1: tlv.Value[0],
		SW2: tlv.Value[1],
	}, true, nil
}

func decodeUIMDigits(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	isASCII := true
	for _, b := range data {
		if b < '0' || b > '9' {
			isASCII = false
			break
		}
	}
	if isASCII {
		return string(data)
	}
	return decodeSwappedBCD(data)
}

func parseSlotStatusTLVs(tlvs []TLV) (*UIMSlotStatus, error) {
	statusTLV := FindTLV(tlvs, 0x10)
	if statusTLV == nil || len(statusTLV.Value) < 1 {
		return nil, fmt.Errorf("slot status TLV missing or too short")
	}

	slots := make([]UIMSlotStatusSlot, 0, int(statusTLV.Value[0]))
	offset := 1
	for i := 0; i < int(statusTLV.Value[0]); i++ {
		if offset+10 > len(statusTLV.Value) {
			return nil, fmt.Errorf("slot status entry %d truncated", i)
		}
		slot := UIMSlotStatusSlot{
			PhysicalCardStatus: binary.LittleEndian.Uint32(statusTLV.Value[offset : offset+4]),
			PhysicalSlotStatus: binary.LittleEndian.Uint32(statusTLV.Value[offset+4 : offset+8]),
			LogicalSlot:        statusTLV.Value[offset+8],
		}
		iccidLen := int(statusTLV.Value[offset+9])
		offset += 10
		if offset+iccidLen > len(statusTLV.Value) {
			return nil, fmt.Errorf("slot status ICCID entry %d truncated", i)
		}
		if iccidLen > 0 {
			slot.ICCIDRaw = append([]byte(nil), statusTLV.Value[offset:offset+iccidLen]...)
			slot.ICCID = decodeUIMDigits(slot.ICCIDRaw)
		}
		offset += iccidLen
		slots = append(slots, slot)
	}

	if infoTLV := FindTLV(tlvs, 0x11); infoTLV != nil && len(infoTLV.Value) >= 1 {
		offset = 1
		count := int(infoTLV.Value[0])
		for i := 0; i < count; i++ {
			if offset+7 > len(infoTLV.Value) {
				return nil, fmt.Errorf("slot extended info entry %d truncated", i)
			}
			atrLen := int(infoTLV.Value[offset+5])
			if offset+7+atrLen > len(infoTLV.Value) {
				return nil, fmt.Errorf("slot extended ATR entry %d truncated", i)
			}
			if i >= len(slots) {
				slots = append(slots, UIMSlotStatusSlot{})
			}
			slots[i].CardProtocol = binary.LittleEndian.Uint32(infoTLV.Value[offset : offset+4])
			slots[i].ValidApplications = infoTLV.Value[offset+4]
			if atrLen > 0 {
				slots[i].ATR = append([]byte(nil), infoTLV.Value[offset+6:offset+6+atrLen]...)
			}
			slots[i].IsEUICC = infoTLV.Value[offset+6+atrLen] != 0
			slots[i].HasExtendedInfo = true
			offset += 7 + atrLen
		}
	}

	if eidTLV := FindTLV(tlvs, 0x12); eidTLV != nil && len(eidTLV.Value) >= 1 {
		offset = 1
		count := int(eidTLV.Value[0])
		for i := 0; i < count; i++ {
			if offset+1 > len(eidTLV.Value) {
				return nil, fmt.Errorf("slot EID entry %d truncated", i)
			}
			eidLen := int(eidTLV.Value[offset])
			offset++
			if offset+eidLen > len(eidTLV.Value) {
				return nil, fmt.Errorf("slot EID data entry %d truncated", i)
			}
			if i >= len(slots) {
				slots = append(slots, UIMSlotStatusSlot{})
			}
			if eidLen > 0 {
				slots[i].EID = append([]byte(nil), eidTLV.Value[offset:offset+eidLen]...)
				slots[i].HasEID = true
			}
			offset += eidLen
		}
	}

	return &UIMSlotStatus{Slots: slots}, nil
}

func parseGetSlotStatusResponse(resp *Packet) (*UIMSlotStatus, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, wrapUIMNotSupported("get slot status", err)
	}
	return parseSlotStatusTLVs(resp.TLVs)
}

// ParseUIMSlotStatusIndication parses UIM slot status indication (0x0048).
func ParseUIMSlotStatusIndication(packet *Packet) (*UIMSlotStatus, error) {
	if packet == nil {
		return nil, fmt.Errorf("slot status indication packet is nil")
	}
	return parseSlotStatusTLVs(packet.TLVs)
}

// ParseUIMRefreshIndication parses UIM refresh indication (0x0033).
func ParseUIMRefreshIndication(packet *Packet) (*UIMRefreshIndication, error) {
	if packet == nil {
		return nil, fmt.Errorf("refresh indication packet is nil")
	}
	tlv := FindTLV(packet.TLVs, 0x10)
	if tlv == nil {
		return nil, fmt.Errorf("refresh indication TLV not found")
	}
	return parseRefreshEventTLV(tlv.Value)
}

func parseReadRecordResponse(resp *Packet) (*UIMRecordData, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, wrapUIMNotSupported("read record", err)
	}

	result := &UIMRecordData{}
	cardResult, hasCardResult, err := parseUIMCardResult(FindTLV(resp.TLVs, 0x10))
	if err != nil {
		return nil, err
	}
	result.CardResult = cardResult
	result.HasCardResult = hasCardResult

	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil {
		if len(tlv.Value) < 2 {
			return nil, fmt.Errorf("read record result TLV too short")
		}
		length := int(binary.LittleEndian.Uint16(tlv.Value[0:2]))
		if len(tlv.Value) < 2+length {
			return nil, fmt.Errorf("read record result truncated")
		}
		result.Data = append([]byte(nil), tlv.Value[2:2+length]...)
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil {
		if len(tlv.Value) < 2 {
			return nil, fmt.Errorf("additional read result TLV too short")
		}
		length := int(binary.LittleEndian.Uint16(tlv.Value[0:2]))
		if len(tlv.Value) < 2+length {
			return nil, fmt.Errorf("additional read result truncated")
		}
		result.AdditionalData = append([]byte(nil), tlv.Value[2:2+length]...)
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 4 {
		result.ResponseInIndicationToken = binary.LittleEndian.Uint32(tlv.Value[0:4])
		result.HasResponseInIndicationToken = true
	}
	return result, nil
}

func parseGetFileAttributesResponse(resp *Packet) (*UIMFileAttributes, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, wrapUIMNotSupported("get file attributes", err)
	}

	result := &UIMFileAttributes{}
	cardResult, hasCardResult, err := parseUIMCardResult(FindTLV(resp.TLVs, 0x10))
	if err != nil {
		return nil, err
	}
	result.CardResult = cardResult
	result.HasCardResult = hasCardResult

	tlv := FindTLV(resp.TLVs, 0x11)
	if tlv == nil || len(tlv.Value) < 26 {
		return nil, fmt.Errorf("file attributes TLV missing or too short")
	}

	result.FileSize = binary.LittleEndian.Uint16(tlv.Value[0:2])
	result.FileID = binary.LittleEndian.Uint16(tlv.Value[2:4])
	result.FileType = tlv.Value[4]
	result.RecordSize = binary.LittleEndian.Uint16(tlv.Value[5:7])
	result.RecordCount = binary.LittleEndian.Uint16(tlv.Value[7:9])
	result.ReadSecurity = UIMSecurityAttributes{Logic: tlv.Value[9], Attributes: binary.LittleEndian.Uint16(tlv.Value[10:12])}
	result.WriteSecurity = UIMSecurityAttributes{Logic: tlv.Value[12], Attributes: binary.LittleEndian.Uint16(tlv.Value[13:15])}
	result.IncreaseSecurity = UIMSecurityAttributes{Logic: tlv.Value[15], Attributes: binary.LittleEndian.Uint16(tlv.Value[16:18])}
	result.DeactivateSecurity = UIMSecurityAttributes{Logic: tlv.Value[18], Attributes: binary.LittleEndian.Uint16(tlv.Value[19:21])}
	result.ActivateSecurity = UIMSecurityAttributes{Logic: tlv.Value[21], Attributes: binary.LittleEndian.Uint16(tlv.Value[22:24])}

	rawLen := int(binary.LittleEndian.Uint16(tlv.Value[24:26]))
	if len(tlv.Value) < 26+rawLen {
		return nil, fmt.Errorf("file attributes raw data truncated")
	}
	if rawLen > 0 {
		result.RawData = append([]byte(nil), tlv.Value[26:26+rawLen]...)
	}
	if tokenTLV := FindTLV(resp.TLVs, 0x12); tokenTLV != nil && len(tokenTLV.Value) >= 4 {
		result.ResponseInIndicationToken = binary.LittleEndian.Uint32(tokenTLV.Value[0:4])
		result.HasResponseInIndicationToken = true
	}
	return result, nil
}

func parseOpenLogicalChannelResponse(resp *Packet) (byte, error) {
	if err := resp.CheckResult(); err != nil {
		return 0, wrapUIMNotSupported("open logical channel", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 1 {
		return 0, fmt.Errorf("logical channel TLV missing or too short")
	}
	return tlv.Value[0], nil
}

func parseCloseLogicalChannelResponse(resp *Packet) error {
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("close logical channel", err)
	}
	return nil
}

func parseSendAPDUResponse(resp *Packet) ([]byte, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, wrapUIMNotSupported("send APDU", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 2 {
		return nil, fmt.Errorf("APDU response TLV missing or too short")
	}
	responseLen := int(binary.LittleEndian.Uint16(tlv.Value[0:2]))
	if len(tlv.Value) < 2+responseLen {
		return nil, fmt.Errorf("APDU response truncated")
	}
	return append([]byte(nil), tlv.Value[2:2+responseLen]...), nil
}

// OpenLogicalChannel opens a logical channel on the target slot and selects the given AID.
func (u *UIMService) OpenLogicalChannel(ctx context.Context, slot uint8, aid []byte) (byte, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMOpenLogicalChannel, buildOpenLogicalChannelTLVs(slot, aid))
	if err != nil {
		return 0, err
	}
	return parseOpenLogicalChannelResponse(resp)
}

// CloseLogicalChannel closes the given logical channel on the target slot.
func (u *UIMService) CloseLogicalChannel(ctx context.Context, slot uint8, channel uint8) error {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMCloseLogicalChannel, buildCloseLogicalChannelTLVs(slot, channel))
	if err != nil {
		return err
	}
	return parseCloseLogicalChannelResponse(resp)
}

// SendAPDU transmits a raw APDU on the given logical channel and slot.
func (u *UIMService) SendAPDU(ctx context.Context, slot uint8, channel uint8, command []byte) ([]byte, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMSendAPDU, buildSendAPDUTLVs(slot, channel, command))
	if err != nil {
		return nil, err
	}
	return parseSendAPDUResponse(resp)
}

// GetSlotStatus returns physical/logical slot mapping and optional eUICC details.
func (u *UIMService) GetSlotStatus(ctx context.Context) (*UIMSlotStatus, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMGetSlotStatus, nil)
	if err != nil {
		return nil, err
	}
	return parseGetSlotStatusResponse(resp)
}

// SwitchSlot remaps a logical slot to a target physical slot.
func (u *UIMService) SwitchSlot(ctx context.Context, logicalSlot uint8, physicalSlot uint32) error {
	tlvs := []TLV{
		NewTLVUint8(0x01, logicalSlot),
		NewTLVUint32(0x02, physicalSlot),
	}
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMSwitchSlot, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("switch slot", err)
	}
	return nil
}

// RegisterEvents enables UIM indications and returns the modem-accepted mask when present.
func (u *UIMService) RegisterEvents(ctx context.Context, mask uint32) (uint32, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMRegisterEvents, []TLV{NewTLVUint32(0x01, mask)})
	if err != nil {
		return 0, err
	}
	if err := resp.CheckResult(); err != nil {
		return 0, wrapUIMNotSupported("register UIM events", err)
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 4 {
		return binary.LittleEndian.Uint32(tlv.Value[0:4]), nil
	}
	return mask, nil
}

// GetSupportedMessages returns UIM message IDs supported by the modem.
func (u *UIMService) GetSupportedMessages(ctx context.Context) ([]uint8, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMGetSupportedMessages, nil)
	if err != nil {
		return nil, err
	}
	if err := resp.CheckResult(); err != nil {
		return nil, wrapUIMNotSupported("get supported messages", err)
	}
	return parseSupportedMessagesTLV(resp)
}

// Reset resets UIM service state.
func (u *UIMService) Reset(ctx context.Context) error {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMReset, nil)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("reset UIM service", err)
	}
	return nil
}

// PowerOffSIM powers off the SIM card in the specified slot.
func (u *UIMService) PowerOffSIM(ctx context.Context, slot uint8) error {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMPowerOffSIM, []TLV{NewTLVUint8(0x01, slot)})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("power off SIM", err)
	}
	return nil
}

// PowerOnSIM powers on the SIM card in the specified slot.
func (u *UIMService) PowerOnSIM(ctx context.Context, slot uint8) error {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMPowerOnSIM, []TLV{NewTLVUint8(0x01, slot)})
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("power on SIM", err)
	}
	return nil
}

// ChangeProvisioningSession changes UIM provisioning session state.
func (u *UIMService) ChangeProvisioningSession(ctx context.Context, req UIMChangeProvisioningSessionRequest) error {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMChangeProvisioningSession, buildChangeProvisioningSessionTLVs(req))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("change provisioning session", err)
	}
	return nil
}

// RefreshRegister registers refresh files and voting preference.
func (u *UIMService) RefreshRegister(ctx context.Context, req UIMRefreshRegisterRequest) error {
	infoTLV, err := buildRefreshRegisterInfoTLV(req)
	if err != nil {
		return err
	}
	tlvs := []TLV{
		buildUIMSessionTLV(req.SessionType, req.ApplicationIdentifier),
		infoTLV,
	}
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMRefreshRegister, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("refresh register", err)
	}
	return nil
}

// RefreshComplete notifies the modem that refresh handling is finished.
func (u *UIMService) RefreshComplete(ctx context.Context, req UIMRefreshCompleteRequest) error {
	tlvs := []TLV{
		buildUIMSessionTLV(req.SessionType, req.ApplicationIdentifier),
		buildRefreshCompleteInfoTLV(req),
	}
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMRefreshComplete, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("refresh complete", err)
	}
	return nil
}

// RefreshRegisterAll registers all files for refresh handling.
func (u *UIMService) RefreshRegisterAll(ctx context.Context, req UIMRefreshRegisterAllRequest) error {
	tlvs := []TLV{
		buildUIMSessionTLV(req.SessionType, req.ApplicationIdentifier),
		buildRefreshRegisterAllInfoTLV(req),
	}
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMRefreshRegisterAll, tlvs)
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return wrapUIMNotSupported("refresh register all", err)
	}
	return nil
}

// ReadRecord reads one record from a record-oriented EF using the default primary GW session.
func (u *UIMService) ReadRecord(ctx context.Context, fileID uint16, path []uint8, recordNumber uint16, recordLength uint16) (*UIMRecordData, error) {
	return u.ReadRecordWithSession(ctx, UIMSessionTypePrimaryGWProvisioning, fileID, path, recordNumber, recordLength)
}

// ReadRecordWithSession reads one record from a record-oriented EF using an explicit UIM session.
func (u *UIMService) ReadRecordWithSession(ctx context.Context, sessionType uint8, fileID uint16, path []uint8, recordNumber uint16, recordLength uint16) (*UIMRecordData, error) {
	tlvs := []TLV{
		buildUIMSessionTLV(sessionType, nil),
		buildUIMFileTLV(fileID, path),
		{Type: 0x03, Value: []byte{
			byte(recordNumber), byte(recordNumber >> 8),
			byte(recordLength), byte(recordLength >> 8),
		}},
	}
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMReadRecord, tlvs)
	if err != nil {
		return nil, err
	}
	return parseReadRecordResponse(resp)
}

// GetFileAttributes retrieves metadata for a SIM/USIM file using the default primary GW session.
func (u *UIMService) GetFileAttributes(ctx context.Context, fileID uint16, path []uint8) (*UIMFileAttributes, error) {
	return u.GetFileAttributesWithSession(ctx, UIMSessionTypePrimaryGWProvisioning, fileID, path)
}

// GetFileAttributesWithSession retrieves metadata for a SIM/USIM file using an explicit UIM session.
func (u *UIMService) GetFileAttributesWithSession(ctx context.Context, sessionType uint8, fileID uint16, path []uint8) (*UIMFileAttributes, error) {
	tlvs := []TLV{
		buildUIMSessionTLV(sessionType, nil),
		buildUIMFileTLV(fileID, path),
	}
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMGetFileAttrs, tlvs)
	if err != nil {
		return nil, err
	}
	return parseGetFileAttributesResponse(resp)
}

// ReadTransparent reads a transparent file from the SIM card / ReadTransparent 从 SIM 卡读取透明文件
// fileID: e.g. 0x2FE2 for ICCID, 0x6F07 for IMSI
func (u *UIMService) ReadTransparent(ctx context.Context, fileID uint16, path []uint8) ([]byte, error) {
	return u.ReadTransparentWithSession(ctx, UIMSessionTypePrimaryGWProvisioning, fileID, path)
}

func (u *UIMService) ReadTransparentWithSession(ctx context.Context, sessionType uint8, fileID uint16, path []uint8) ([]byte, error) {
	var tlvs []TLV

	tlvs = append(tlvs, buildUIMSessionTLV(sessionType, nil))
	tlvs = append(tlvs, buildUIMFileTLV(fileID, path))

	// TLV 0x03: Read Information (Optional but good practice)
	// Offset (2) + Length (2)
	// 0, 0 means read entire file
	bufRead := make([]byte, 4)
	binary.LittleEndian.PutUint16(bufRead[0:2], 0)
	binary.LittleEndian.PutUint16(bufRead[2:4], 0)
	tlvs = append(tlvs, TLV{Type: 0x03, Value: bufRead})

	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMReadTransparent, tlvs)
	if err != nil {
		return nil, err
	}

	if err := resp.CheckResult(); err != nil {
		return nil, err
	}

	// TLV 0x11: Read Result (Content) - quectel-CM uses 0x11
	// Format: ContentLen (2) + Content...
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil {
		if len(tlv.Value) < 2 {
			return nil, fmt.Errorf("read result too short")
		}
		contentLen := binary.LittleEndian.Uint16(tlv.Value[0:2])
		if len(tlv.Value) < int(2+contentLen) {
			return nil, fmt.Errorf("read result truncated")
		}
		return tlv.Value[2 : 2+contentLen], nil
	}

	// Fallback to 0x10 just in case
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		return tlv.Value, nil
	}

	return nil, nil
}

func (u *UIMService) GetICCID(ctx context.Context) (string, error) {
	data, err := u.ReadTransparentWithSession(ctx, 0x06, 0x2FE2, []byte{0x00, 0x3F})
	if err != nil {
		data, err = u.ReadTransparentWithSession(ctx, 0x06, 0x2FE2, []byte{})
		if err != nil {
			return "", err
		}
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty ICCID")
	}
	return decodeSwappedBCD(data), nil
}

func (u *UIMService) GetIMSI(ctx context.Context) (string, error) {
	data, err := u.ReadTransparentWithSession(ctx, 0x00, 0x6F07, []byte{0x00, 0x3F, 0xFF, 0x7F})
	if err != nil {
		data, err = u.ReadTransparentWithSession(ctx, 0x00, 0x6F07, []byte{0x20, 0x7F})
		if err != nil {
			data, err = u.ReadTransparentWithSession(ctx, 0x00, 0x6F07, []byte{})
			if err != nil {
				return "", err
			}
		}
	}
	if len(data) <= 1 {
		return "", fmt.Errorf("invalid IMSI length")
	}
	bcd := data[1:]
	if int(data[0]) <= len(data)-1 {
		bcd = data[1 : 1+int(data[0])]
	}
	imsi := decodeSwappedBCD(bcd)
	if imsi == "" {
		return "", fmt.Errorf("empty IMSI")
	}

	// 核心修复: 3GPP TS 31.102 规范说明 EF_IMSI 文件的第一个字节低 4 位
	// 并非 IMSI 实际数字，而是奇偶校验/身份类型前缀（通常为 0x01 或 0x09）。
	// decodeSwappedBCD 毫无差别地将该 nibble (1 或 9) 放到了第一位输出。
	// 这会导致正常的譬如 "234..." 被加上 9 前缀变成 "9234..." ！
	// 故必须切掉错误解析出的第一位。
	if len(imsi) > 0 {
		imsi = imsi[1:]
	}

	return imsi, nil
}

func (u *UIMService) GetNativeSPN(ctx context.Context) (string, error) {
	data, err := u.readSIMTransparentFallback(ctx, 0x6F46)
	if err != nil {
		return "", err
	}
	return decodeEFSPN(data)
}

func (u *UIMService) GetSIMMetadata(ctx context.Context) (*SIMMetadata, error) {
	meta := &SIMMetadata{}
	if gid, err := u.GetGID1(ctx); err == nil {
		meta.GID1 = gid
	}
	if gid, err := u.GetGID2(ctx); err == nil {
		meta.GID2 = gid
	}
	if st, err := u.GetSIMServiceTable(ctx); err == nil {
		meta.ServiceTable = st
	}
	if pnn, err := u.GetPNNRecords(ctx); err == nil {
		meta.PNN = pnn
	}
	if opl, err := u.GetOPLRecords(ctx); err == nil {
		meta.OPL = opl
	}
	if mcc, mnc, ok := nativeMCCMNCFromOPLRecords(meta.OPL); ok {
		meta.NativeMCC = mcc
		meta.NativeMNC = mnc
	} else if mcc, mnc, err := u.getNativeMCCMNCFromIMSI(ctx); err == nil {
		meta.NativeMCC = mcc
		meta.NativeMNC = mnc
	}
	if meta.NativeMCC == "" && meta.NativeMNC == "" && meta.GID1 == "" && meta.GID2 == "" && meta.ServiceTable == nil && len(meta.PNN) == 0 && len(meta.OPL) == 0 {
		return meta, fmt.Errorf("sim_metadata_empty")
	}
	return meta, nil
}

func (u *UIMService) GetGID1(ctx context.Context) (string, error) {
	data, err := u.readSIMTransparentFallback(ctx, 0x6F3E)
	if err != nil {
		return "", err
	}
	return simRawHex(data), nil
}

func (u *UIMService) GetGID2(ctx context.Context) (string, error) {
	data, err := u.readSIMTransparentFallback(ctx, 0x6F3F)
	if err != nil {
		return "", err
	}
	return simRawHex(data), nil
}

func (u *UIMService) GetSIMServiceTable(ctx context.Context) (*SIMServiceTable, error) {
	data, err := u.readSIMTransparentFallback(ctx, 0x6F38)
	if err != nil {
		return nil, err
	}
	if st := decodeSIMServiceTable("UST", data); st != nil {
		return st, nil
	}
	return decodeSIMServiceTable("SST", data), nil
}

func (u *UIMService) GetPNNRecords(ctx context.Context) ([]PNNRecord, error) {
	return u.readPNNRecords(ctx, 0x6FC5)
}

func (u *UIMService) GetOPLRecords(ctx context.Context) ([]OPLRecord, error) {
	return u.readOPLRecords(ctx, 0x6FC6)
}

func (u *UIMService) readSIMTransparentFallback(ctx context.Context, fileID uint16) ([]byte, error) {
	data, err := u.ReadTransparentWithSession(ctx, 0x00, fileID, []byte{0x00, 0x3F, 0xFF, 0x7F})
	if err == nil {
		return data, nil
	}
	data, err = u.ReadTransparentWithSession(ctx, 0x00, fileID, []byte{0x20, 0x7F})
	if err == nil {
		return data, nil
	}
	return u.ReadTransparentWithSession(ctx, 0x00, fileID, []byte{})
}

type uimRecordLayout struct {
	path           []byte
	recordSize     uint16
	recordCount    uint16
	firstRecord    []byte
	hasFirstRecord bool
}

func (u *UIMService) readRecordAtPath(ctx context.Context, fileID uint16, path []byte, record uint16, length uint16) ([]byte, error) {
	data, err := u.ReadRecordWithSession(ctx, 0x00, fileID, path, record, length)
	if err != nil || data == nil {
		return nil, err
	}
	return data.Data, nil
}

func (u *UIMService) recordLayout(ctx context.Context, fileID uint16, fallbackLength uint16) (uimRecordLayout, error) {
	paths := [][]byte{{0x00, 0x3F, 0xFF, 0x7F}, {0x20, 0x7F}, {}}
	var lastErr error
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return uimRecordLayout{}, err
		}
		attrs, err := u.GetFileAttributesWithSession(ctx, 0x00, fileID, path)
		if err == nil && attrs != nil && attrs.RecordSize > 0 && attrs.RecordCount > 0 {
			return uimRecordLayout{
				path:        append([]byte(nil), path...),
				recordSize:  attrs.RecordSize,
				recordCount: attrs.RecordCount,
			}, nil
		}
		lastErr = err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return uimRecordLayout{}, err
		}
		data, err := u.readRecordAtPath(ctx, fileID, path, 1, fallbackLength)
		if err == nil {
			return uimRecordLayout{
				path:           append([]byte(nil), path...),
				recordSize:     fallbackLength,
				recordCount:    32,
				firstRecord:    append([]byte(nil), data...),
				hasFirstRecord: true,
			}, nil
		}
		lastErr = err
	}
	return uimRecordLayout{}, lastErr
}

func (u *UIMService) readPNNRecords(ctx context.Context, fileID uint16) ([]PNNRecord, error) {
	layout, err := u.recordLayout(ctx, fileID, 64)
	if err != nil {
		return nil, err
	}
	records := make([]PNNRecord, 0)
	for i := uint16(1); i <= layout.recordCount && i <= 32; i++ {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		var data []byte
		if i == 1 && layout.hasFirstRecord {
			data = append([]byte(nil), layout.firstRecord...)
		} else {
			data, err = u.readRecordAtPath(ctx, fileID, layout.path, i, layout.recordSize)
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return records, ctxErr
			}
			return records, err
		}
		if len(trimSPNPadding(data)) == 0 {
			break
		}
		if rec, ok := decodePNNRecord(int(i), data); ok {
			records = append(records, rec)
		}
	}
	return records, nil
}

func (u *UIMService) readOPLRecords(ctx context.Context, fileID uint16) ([]OPLRecord, error) {
	layout, err := u.recordLayout(ctx, fileID, 8)
	if err != nil {
		return nil, err
	}
	if layout.recordSize < 8 {
		layout.recordSize = 8
	}
	records := make([]OPLRecord, 0)
	for i := uint16(1); i <= layout.recordCount && i <= 32; i++ {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		var data []byte
		if i == 1 && layout.hasFirstRecord {
			data = append([]byte(nil), layout.firstRecord...)
		} else {
			data, err = u.readRecordAtPath(ctx, fileID, layout.path, i, layout.recordSize)
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return records, ctxErr
			}
			return records, err
		}
		if len(trimSPNPadding(data)) == 0 {
			break
		}
		if rec, ok := decodeOPLRecord(int(i), data); ok {
			records = append(records, rec)
		}
	}
	return records, nil
}

func (u *UIMService) GetNativeMCCMNC(ctx context.Context) (mcc string, mnc string, err error) {
	if opl, oplErr := u.GetOPLRecords(ctx); oplErr == nil {
		if mcc, mnc, ok := nativeMCCMNCFromOPLRecords(opl); ok {
			return mcc, mnc, nil
		}
	}
	return u.getNativeMCCMNCFromIMSI(ctx)
}

func (u *UIMService) getNativeMCCMNCFromIMSI(ctx context.Context) (mcc string, mnc string, err error) {
	// 1. 获取 IMSI
	imsi, err := u.GetIMSI(ctx)
	if err != nil {
		return "", "", fmt.Errorf("failed to get IMSI: %w", err)
	}
	if len(imsi) < 5 {
		return "", "", fmt.Errorf("invalid IMSI length: %s", imsi)
	}

	// 2. 尝试读取 EF_AD (0x6FAD) 获取 MNC 长度
	mncLen := 2 // 默认安全回退到 2 位
	adData, adErr := u.ReadTransparentWithSession(ctx, 0x00, 0x6FAD, []byte{0x00, 0x3F, 0xFF, 0x7F})
	if adErr != nil {
		adData, adErr = u.ReadTransparentWithSession(ctx, 0x00, 0x6FAD, []byte{0x20, 0x7F})
		if adErr != nil {
			adData, _ = u.ReadTransparentWithSession(ctx, 0x00, 0x6FAD, []byte{})
		}
	}

	// EF_AD 文件如果存在且长度足够，第 4 字节（索引为 3）存放了 MNC 的长度
	if len(adData) >= 4 {
		// 第 4 字节规定了 MNC 位数 (0x02 或 0x03)
		if adData[3] == 0x02 || adData[3] == 0x03 {
			mncLen = int(adData[3])
		}
	}

	if len(imsi) < 3+mncLen {
		return "", "", fmt.Errorf("invalid IMSI length %d for MNC length %d", len(imsi), mncLen)
	}

	mcc = imsi[0:3]
	mnc = imsi[3 : 3+mncLen]

	return mcc, mnc, nil
}

func decodeEFSPN(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("EF_SPN data empty")
	}
	name := data
	if len(name) > 1 {
		name = name[1:]
	}
	name = trimSPNPadding(name)
	if len(name) == 0 {
		return "", fmt.Errorf("EF_SPN name empty")
	}

	var (
		decoded string
		err     error
	)
	switch name[0] {
	case 0x80:
		decoded, err = decodeSPNUCS2(name[1:])
	case 0x81:
		decoded, err = decodeSPNCompressedUCS2(name, 1)
	case 0x82:
		decoded, err = decodeSPNCompressedUCS2(name, 2)
	default:
		if isPrintableASCII(name) {
			decoded = string(name)
		} else {
			decoded, err = decodeSPNGSM(name)
		}
	}
	if err != nil {
		return "", err
	}
	decoded = strings.TrimSpace(strings.ReplaceAll(decoded, "\x00", ""))
	if decoded == "" {
		return "", fmt.Errorf("EF_SPN name empty")
	}
	return decoded, nil
}

func trimSPNPadding(data []byte) []byte {
	end := len(data)
	for end > 0 && (data[end-1] == 0xFF || data[end-1] == 0x00) {
		end--
	}
	return data[:end]
}

func simRawHex(data []byte) string {
	data = trimSPNPadding(data)
	if len(data) == 0 {
		return ""
	}
	return strings.ToUpper(hex.EncodeToString(data))
}

func decodePNNRecord(record int, data []byte) (PNNRecord, bool) {
	raw := simRawHex(data)
	data = trimSPNPadding(data)
	if len(data) == 0 {
		return PNNRecord{}, false
	}
	out := PNNRecord{Record: record, RawHex: raw}
	for i := 0; i+2 <= len(data); {
		tag := data[i]
		length := int(data[i+1])
		i += 2
		if i+length > len(data) {
			break
		}
		value := data[i : i+length]
		i += length
		switch tag {
		case 0x43:
			if name, err := decodePNNNetworkName(value); err == nil {
				out.FullName = name
			}
		case 0x45:
			if name, err := decodePNNNetworkName(value); err == nil {
				out.ShortName = name
			}
		}
	}
	return out, out.FullName != "" || out.ShortName != "" || out.RawHex != ""
}

func decodeOPLRecord(record int, data []byte) (OPLRecord, bool) {
	raw := simRawHex(data)
	data = trimSPNPadding(data)
	if len(data) < 8 {
		return OPLRecord{}, false
	}
	out := OPLRecord{
		Record:    record,
		PLMN:      decodeOPLPLMN(data[:3]),
		LACStart:  binary.BigEndian.Uint16(data[3:5]),
		LACEnd:    binary.BigEndian.Uint16(data[5:7]),
		PNNRecord: int(data[7]),
		RawHex:    raw,
	}
	return out, out.PLMN != "" || out.RawHex != ""
}

func nativeMCCMNCFromOPLRecords(records []OPLRecord) (mcc string, mnc string, ok bool) {
	for _, rec := range records {
		plmn := strings.TrimSpace(rec.PLMN)
		if len(plmn) != 5 && len(plmn) != 6 {
			continue
		}
		if !isExactDecimalPLMN(plmn) {
			continue
		}
		return plmn[:3], plmn[3:], true
	}
	return "", "", false
}

func isExactDecimalPLMN(plmn string) bool {
	for i := 0; i < len(plmn); i++ {
		if plmn[i] < '0' || plmn[i] > '9' {
			return false
		}
	}
	return true
}

func decodeSIMServiceTable(kind string, data []byte) *SIMServiceTable {
	raw := simRawHex(data)
	data = trimSPNPadding(data)
	if len(data) == 0 {
		return nil
	}
	enabled := make([]int, 0)
	for i, b := range data {
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) != 0 {
				enabled = append(enabled, i*8+bit+1)
			}
		}
	}
	return &SIMServiceTable{Kind: kind, RawHex: raw, EnabledServices: enabled}
}

func decodeSIMAlphaIdentifier(data []byte) (string, error) {
	data = trimSPNPadding(data)
	if len(data) == 0 {
		return "", fmt.Errorf("SIM alpha identifier empty")
	}
	switch data[0] {
	case 0x80:
		return decodeSPNUCS2(data[1:])
	case 0x81:
		return decodeSPNCompressedUCS2(data, 1)
	case 0x82:
		return decodeSPNCompressedUCS2(data, 2)
	default:
		if isPrintableASCII(data) {
			return strings.TrimSpace(string(data)), nil
		}
		return decodeSPNGSM(data)
	}
}

func decodePNNNetworkName(data []byte) (string, error) {
	data = trimSPNPadding(data)
	if len(data) == 0 {
		return "", fmt.Errorf("PNN network name empty")
	}
	if len(data) >= 2 {
		info := data[0]
		coding := (info >> 4) & 0x07
		payload := data[1:]
		switch coding {
		case 0:
			return decodePackedGSM7(payload, int(info&0x07))
		case 1:
			return decodeSPNUCS2(payload)
		}
	}
	return decodeSIMAlphaIdentifier(data)
}

func decodePackedGSM7(data []byte, spareBits int) (string, error) {
	if spareBits < 0 || spareBits > 7 {
		spareBits = 0
	}
	septets := (len(data)*8 - spareBits) / 7
	if septets <= 0 {
		return "", fmt.Errorf("GSM7 payload empty")
	}
	unpacked := make([]byte, 0, septets)
	buf := 0
	bits := 0
	for _, b := range data {
		buf |= int(b) << bits
		bits += 8
		for bits >= 7 && len(unpacked) < septets {
			unpacked = append(unpacked, byte(buf&0x7F))
			buf >>= 7
			bits -= 7
		}
	}
	decoded, err := decodeSPNGSM(unpacked)
	if err != nil {
		return "", err
	}
	decoded = strings.TrimSpace(strings.ReplaceAll(decoded, "\x00", ""))
	if decoded == "" {
		return "", fmt.Errorf("GSM7 network name empty")
	}
	return decoded, nil
}

func decodeOPLPLMN(data []byte) string {
	if len(data) < 3 {
		return ""
	}
	nibbles := []byte{data[0] & 0x0F, data[0] >> 4, data[1] & 0x0F, data[2] & 0x0F, data[2] >> 4, data[1] >> 4}
	var b strings.Builder
	for _, n := range nibbles {
		if n == 0x0F {
			b.WriteByte('x')
			continue
		}
		if n > 9 {
			return ""
		}
		b.WriteByte('0' + n)
	}
	return strings.TrimRight(b.String(), "x")
}

func decodeSPNUCS2(data []byte) (string, error) {
	data = trimSPNPadding(data)
	if len(data) == 0 {
		return "", fmt.Errorf("UCS2 SPN empty")
	}
	if len(data)%2 != 0 {
		return "", fmt.Errorf("UCS2 SPN odd length: %d", len(data))
	}
	runes := make([]uint16, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		runes = append(runes, binary.BigEndian.Uint16(data[i:i+2]))
	}
	return string(utf16.Decode(runes)), nil
}

func decodeSPNCompressedUCS2(data []byte, baseBytes int) (string, error) {
	if len(data) < 2+baseBytes {
		return "", fmt.Errorf("compressed UCS2 SPN too short")
	}
	count := int(data[1])
	payloadStart := 2 + baseBytes
	payload := data[payloadStart:]
	if count < len(payload) {
		payload = payload[:count]
	}

	var base uint16
	if baseBytes == 1 {
		base = uint16(data[2]) << 7
	} else {
		base = binary.BigEndian.Uint16(data[2:4])
	}

	var b strings.Builder
	for _, c := range payload {
		if c < 0x80 {
			decoded, err := decodeSPNGSM([]byte{c})
			if err != nil {
				return "", err
			}
			b.WriteString(decoded)
			continue
		}
		b.WriteRune(rune(base + uint16(c&0x7F)))
	}
	return b.String(), nil
}

func decodeSPNGSM(data []byte) (string, error) {
	decoded, err := gsm7.Decode(data)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(decoded) {
		return "", fmt.Errorf("GSM SPN decoded invalid UTF-8")
	}
	return string(decoded), nil
}

func isPrintableASCII(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b < 0x20 || b > 0x7E {
			return false
		}
	}
	return true
}

func decodeSwappedBCD(data []byte) string {
	out := make([]byte, 0, len(data)*2)
	for _, b := range data {
		low := b & 0x0F
		high := (b >> 4) & 0x0F

		if low <= 9 {
			out = append(out, '0'+byte(low))
		}
		if high <= 9 {
			out = append(out, '0'+byte(high))
		}
	}
	return string(out)
}
