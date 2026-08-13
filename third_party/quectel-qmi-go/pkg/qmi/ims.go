package qmi

import (
	"context"
	"fmt"
)

// ============================================================================
// IMS Service Wrapper / IMS 服务封装
// ============================================================================

type IMSService struct {
	client   *Client
	clientID uint8
}

func NewIMSService(client *Client) (*IMSService, error) {
	clientID, err := client.AllocateClientID(ServiceIMS)
	if err != nil {
		return nil, err
	}
	return &IMSService{client: client, clientID: clientID}, nil
}

func (i *IMSService) Close() error {
	return i.client.ReleaseClientID(ServiceIMS, i.clientID)
}

// ============================================================================
// IMS Types / IMS 类型
// ============================================================================

type IMSCallModePreference uint8

const (
	IMSCallModePreferenceNone         IMSCallModePreference = 0x00
	IMSCallModePreferenceCellular     IMSCallModePreference = 0x01
	IMSCallModePreferenceWiFi         IMSCallModePreference = 0x02
	IMSCallModePreferenceWiFiOnly     IMSCallModePreference = 0x03
	IMSCallModePreferenceCellularOnly IMSCallModePreference = 0x04
	IMSCallModePreferenceIMS          IMSCallModePreference = 0x05
)

type IMSServicesEnabledSettings struct {
	VoiceOverLTEEnabled      bool
	HasVoiceOverLTEEnabled   bool
	VideoTelephonyEnabled    bool
	HasVideoTelephonyEnabled bool
	VoiceWiFiEnabled         bool
	HasVoiceWiFiEnabled      bool
	CallModePreference       IMSCallModePreference
	HasCallModePreference    bool
	IMSServiceEnabled        bool
	HasIMSServiceEnabled     bool
	UTServiceEnabled         bool
	HasUTServiceEnabled      bool
	SMSServiceEnabled        bool
	HasSMSServiceEnabled     bool
	USSDServiceEnabled       bool
	HasUSSDServiceEnabled    bool
	PresenceEnabled          bool
	HasPresenceEnabled       bool
	AutoconfigEnabled        bool
	HasAutoconfigEnabled     bool
	XDMClientEnabled         bool
	HasXDMClientEnabled      bool
	RCSEnabled               bool
	HasRCSEnabled            bool
	CarrierConfigEnabled     bool
	HasCarrierConfigEnabled  bool
}

type IMSServicesEnabledSettingsUpdate struct {
	VoiceOverLTEEnabled   *bool
	VideoTelephonyEnabled *bool
	VoiceWiFiEnabled      *bool
	CallModePreference    *IMSCallModePreference
	IMSServiceEnabled     *bool
	UTServiceEnabled      *bool
	SMSServiceEnabled     *bool
	USSDServiceEnabled    *bool
	PresenceEnabled       *bool
	AutoconfigEnabled     *bool
	XDMClientEnabled      *bool
	RCSEnabled            *bool
	CarrierConfigEnabled  *bool
}

// ============================================================================
// IMS Message IDs / IMS 消息 ID
// ============================================================================

const (
	IMSSetServicesEnabledSetting uint16 = 0x008F
	IMSGetServicesEnabledSetting uint16 = 0x0090
	IMSSettingsChangedInd        uint16 = 0x0091
	IMSBindRequest               uint16 = 0x0098
)

// ============================================================================
// Public Methods / 对外方法
// ============================================================================

func (i *IMSService) Bind(ctx context.Context, binding uint32) error {
	resp, err := i.client.SendRequest(ctx, ServiceIMS, i.clientID, IMSBindRequest, buildIMSBindTLVs(binding))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("ims bind failed: %w", err)
	}
	return nil
}

func (i *IMSService) GetServicesEnabledSetting(ctx context.Context) (*IMSServicesEnabledSettings, error) {
	resp, err := i.client.SendRequest(ctx, ServiceIMS, i.clientID, IMSGetServicesEnabledSetting, nil)
	if err != nil {
		return nil, err
	}
	return parseIMSGetServicesEnabledSettingResponse(resp)
}

func (i *IMSService) SetServicesEnabledSetting(ctx context.Context, update IMSServicesEnabledSettingsUpdate) error {
	resp, err := i.client.SendRequest(ctx, ServiceIMS, i.clientID, IMSSetServicesEnabledSetting, buildIMSSetServicesEnabledSettingTLVs(update))
	if err != nil {
		return err
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("set ims services enabled setting failed: %w", err)
	}
	return nil
}

func ParseIMSServicesEnabledSetting(packet *Packet) (*IMSServicesEnabledSettings, error) {
	if FindTLV(packet.TLVs, 0x02) != nil {
		return parseIMSGetServicesEnabledSettingResponse(packet)
	}
	return parseIMSSettingsChangedIndication(packet)
}

// ============================================================================
// Internal Helpers / 内部助手
// ============================================================================

func buildIMSSetServicesEnabledSettingTLVs(update IMSServicesEnabledSettingsUpdate) []TLV {
	var tlvs []TLV
	if update.VoiceOverLTEEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x10, boolToUint8(*update.VoiceOverLTEEnabled)))
	}
	if update.VideoTelephonyEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x11, boolToUint8(*update.VideoTelephonyEnabled)))
	}
	if update.VoiceWiFiEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x14, boolToUint8(*update.VoiceWiFiEnabled)))
	}
	if update.CallModePreference != nil {
		tlvs = append(tlvs, NewTLVUint8(0x15, uint8(*update.CallModePreference)))
	}
	if update.IMSServiceEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x18, boolToUint8(*update.IMSServiceEnabled)))
	}
	if update.UTServiceEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x19, boolToUint8(*update.UTServiceEnabled)))
	}
	if update.SMSServiceEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x1A, boolToUint8(*update.SMSServiceEnabled)))
	}
	if update.USSDServiceEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x1C, boolToUint8(*update.USSDServiceEnabled)))
	}
	if update.PresenceEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x1E, boolToUint8(*update.PresenceEnabled)))
	}
	if update.AutoconfigEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x1F, boolToUint8(*update.AutoconfigEnabled)))
	}
	if update.XDMClientEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x20, boolToUint8(*update.XDMClientEnabled)))
	}
	if update.RCSEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x21, boolToUint8(*update.RCSEnabled)))
	}
	if update.CarrierConfigEnabled != nil {
		tlvs = append(tlvs, NewTLVUint8(0x25, boolToUint8(*update.CarrierConfigEnabled)))
	}
	return tlvs
}

func buildIMSBindTLVs(binding uint32) []TLV {
	return []TLV{NewTLVUint32(0x01, binding)}
}

func parseIMSGetServicesEnabledSettingResponse(resp *Packet) (*IMSServicesEnabledSettings, error) {
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("get ims services enabled setting failed: %w", err)
	}
	out := &IMSServicesEnabledSettings{}
	setIMSBoolField(out, FindTLV(resp.TLVs, 0x11), &out.VoiceOverLTEEnabled, &out.HasVoiceOverLTEEnabled)
	setIMSBoolField(out, FindTLV(resp.TLVs, 0x12), &out.VideoTelephonyEnabled, &out.HasVideoTelephonyEnabled)
	setIMSBoolField(out, FindTLV(resp.TLVs, 0x15), &out.VoiceWiFiEnabled, &out.HasVoiceWiFiEnabled)
	setIMSBoolField(out, FindTLV(resp.TLVs, 0x18), &out.IMSServiceEnabled, &out.HasIMSServiceEnabled)
	setIMSBoolField(out, FindTLV(resp.TLVs, 0x19), &out.UTServiceEnabled, &out.HasUTServiceEnabled)
	setIMSBoolField(out, FindTLV(resp.TLVs, 0x1A), &out.SMSServiceEnabled, &out.HasSMSServiceEnabled)
	setIMSBoolField(out, FindTLV(resp.TLVs, 0x1C), &out.USSDServiceEnabled, &out.HasUSSDServiceEnabled)
	return out, nil
}

func parseIMSSettingsChangedIndication(packet *Packet) (*IMSServicesEnabledSettings, error) {
	out := &IMSServicesEnabledSettings{}
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x10), &out.VoiceOverLTEEnabled, &out.HasVoiceOverLTEEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x11), &out.VideoTelephonyEnabled, &out.HasVideoTelephonyEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x14), &out.VoiceWiFiEnabled, &out.HasVoiceWiFiEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x18), &out.IMSServiceEnabled, &out.HasIMSServiceEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x19), &out.UTServiceEnabled, &out.HasUTServiceEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x1A), &out.SMSServiceEnabled, &out.HasSMSServiceEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x1C), &out.USSDServiceEnabled, &out.HasUSSDServiceEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x1E), &out.PresenceEnabled, &out.HasPresenceEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x1F), &out.AutoconfigEnabled, &out.HasAutoconfigEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x20), &out.XDMClientEnabled, &out.HasXDMClientEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x21), &out.RCSEnabled, &out.HasRCSEnabled)
	setIMSBoolField(out, FindTLV(packet.TLVs, 0x25), &out.CarrierConfigEnabled, &out.HasCarrierConfigEnabled)
	return out, nil
}

func setIMSBoolField(_ *IMSServicesEnabledSettings, tlv *TLV, target *bool, has *bool) {
	if tlv == nil || len(tlv.Value) < 1 {
		return
	}
	*target = tlv.Value[0] != 0
	*has = true
}
