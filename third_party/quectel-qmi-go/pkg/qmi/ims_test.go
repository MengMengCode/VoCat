package qmi

import (
	"encoding/binary"
	"testing"
)

func imsTLVUint32(tlvType uint8, v uint32) TLV {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func boolPtr(v bool) *bool {
	return &v
}

func imsCallModePtr(v IMSCallModePreference) *IMSCallModePreference {
	return &v
}

func TestBuildIMSARegisterIndicationsTLVs(t *testing.T) {
	tlvs := buildIMSARegisterIndicationsTLVs(IMSAIndicationRegistration{
		RegistrationStatusChanged: true,
		ServicesStatusChanged:     false,
	})

	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x10 || tlvs[0].Value[0] != 0x01 {
		t.Fatalf("unexpected registration indication TLV: %+v", tlvs[0])
	}
	if tlvs[1].Type != 0x11 || tlvs[1].Value[0] != 0x00 {
		t.Fatalf("unexpected services indication TLV: %+v", tlvs[1])
	}
}

func TestBuildIMSABindTLVs(t *testing.T) {
	tlvs := buildIMSABindTLVs(0x11223344)
	if len(tlvs) != 1 || tlvs[0].Type != 0x10 {
		t.Fatalf("unexpected IMSA bind TLVs: %+v", tlvs)
	}
	if got := binary.LittleEndian.Uint32(tlvs[0].Value); got != 0x11223344 {
		t.Fatalf("unexpected IMSA bind value: 0x%X", got)
	}
}

func TestParseIMSARegistrationStatusResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x11, Value: []byte{0x34, 0x12}},
			imsTLVUint32(0x12, uint32(IMSARegistrationStateRegistered)),
			{Type: 0x13, Value: []byte("registered")},
			imsTLVUint32(0x14, uint32(IMSARegistrationTechnologyWWAN)),
		},
	}

	info, err := parseIMSARegistrationStatusResponse(resp)
	if err != nil {
		t.Fatalf("parseIMSARegistrationStatusResponse returned error: %v", err)
	}
	if !info.HasStatus || info.Status != IMSARegistrationStateRegistered {
		t.Fatalf("unexpected status: %+v", info)
	}
	if !info.HasErrorCode || info.ErrorCode != 0x1234 {
		t.Fatalf("unexpected error code: %+v", info)
	}
	if !info.HasErrorMessage || info.ErrorMessage != "registered" {
		t.Fatalf("unexpected error message: %+v", info)
	}
	if !info.HasTechnology || info.Technology != IMSARegistrationTechnologyWWAN {
		t.Fatalf("unexpected technology: %+v", info)
	}
}

func TestParseIMSARegistrationStatusChanged(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x10, Value: []byte{0x02, 0x00}},
			imsTLVUint32(0x11, uint32(IMSARegistrationStateRegistering)),
			{Type: 0x12, Value: []byte("registering")},
			imsTLVUint32(0x13, uint32(IMSARegistrationTechnologyWLAN)),
		},
	}

	info, err := ParseIMSARegistrationStatusChanged(packet)
	if err != nil {
		t.Fatalf("ParseIMSARegistrationStatusChanged returned error: %v", err)
	}
	if info.Status != IMSARegistrationStateRegistering || info.ErrorCode != 0x0002 {
		t.Fatalf("unexpected IMSA registration indication parse: %+v", info)
	}
}

func TestParseIMSAServicesStatusResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			imsTLVUint32(0x10, uint32(IMSAServiceAvailabilityAvailable)),
			imsTLVUint32(0x11, uint32(IMSAServiceAvailabilityLimited)),
			imsTLVUint32(0x12, uint32(IMSAServiceAvailabilityUnavailable)),
			imsTLVUint32(0x13, uint32(IMSARegistrationTechnologyWWAN)),
			imsTLVUint32(0x14, uint32(IMSARegistrationTechnologyWLAN)),
			imsTLVUint32(0x15, uint32(IMSARegistrationTechnologyInterworkingWLAN)),
			imsTLVUint32(0x16, uint32(IMSAServiceAvailabilityAvailable)),
			imsTLVUint32(0x17, uint32(IMSARegistrationTechnologyWWAN)),
			imsTLVUint32(0x18, uint32(IMSAServiceAvailabilityLimited)),
			imsTLVUint32(0x19, uint32(IMSARegistrationTechnologyWLAN)),
		},
	}

	info, err := parseIMSAServicesStatusResponse(resp)
	if err != nil {
		t.Fatalf("parseIMSAServicesStatusResponse returned error: %v", err)
	}
	if !info.HasSMSServiceStatus || info.SMSServiceStatus != IMSAServiceAvailabilityAvailable {
		t.Fatalf("unexpected SMS service status: %+v", info)
	}
	if !info.HasVoiceTechnology || info.VoiceTechnology != IMSARegistrationTechnologyWLAN {
		t.Fatalf("unexpected voice technology: %+v", info)
	}
	if !info.HasVideoShareTechnology || info.VideoShareTechnology != IMSARegistrationTechnologyWLAN {
		t.Fatalf("unexpected video share technology: %+v", info)
	}
}

func TestParseIMSAServicesStatusChanged(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			imsTLVUint32(0x10, uint32(IMSAServiceAvailabilityLimited)),
			imsTLVUint32(0x14, uint32(IMSARegistrationTechnologyWWAN)),
		},
	}

	info, err := ParseIMSAServicesStatusChanged(packet)
	if err != nil {
		t.Fatalf("ParseIMSAServicesStatusChanged returned error: %v", err)
	}
	if !info.HasSMSServiceStatus || info.SMSServiceStatus != IMSAServiceAvailabilityLimited {
		t.Fatalf("unexpected IMSA services indication: %+v", info)
	}
	if !info.HasVoiceTechnology || info.VoiceTechnology != IMSARegistrationTechnologyWWAN {
		t.Fatalf("unexpected IMSA services indication tech: %+v", info)
	}
}

func TestBuildIMSBindTLVs(t *testing.T) {
	tlvs := buildIMSBindTLVs(0x55667788)
	if len(tlvs) != 1 || tlvs[0].Type != 0x01 {
		t.Fatalf("unexpected IMS bind TLVs: %+v", tlvs)
	}
	if got := binary.LittleEndian.Uint32(tlvs[0].Value); got != 0x55667788 {
		t.Fatalf("unexpected IMS bind value: 0x%X", got)
	}
}

func TestBuildIMSSetServicesEnabledSettingTLVsFalseAndUnset(t *testing.T) {
	tlvs := buildIMSSetServicesEnabledSettingTLVs(IMSServicesEnabledSettingsUpdate{
		VoiceOverLTEEnabled: boolPtr(false),
		PresenceEnabled:     boolPtr(true),
		CallModePreference:  imsCallModePtr(IMSCallModePreferenceWiFiOnly),
	})

	if len(tlvs) != 3 {
		t.Fatalf("expected 3 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x10 || tlvs[0].Value[0] != 0x00 {
		t.Fatalf("expected explicit false VoLTE TLV, got %+v", tlvs[0])
	}
	if tlvs[1].Type != 0x15 || tlvs[1].Value[0] != uint8(IMSCallModePreferenceWiFiOnly) {
		t.Fatalf("unexpected call mode TLV: %+v", tlvs[1])
	}
	if tlvs[2].Type != 0x1E || tlvs[2].Value[0] != 0x01 {
		t.Fatalf("unexpected presence TLV: %+v", tlvs[2])
	}
}

func TestParseIMSGetServicesEnabledSettingResponse(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x11, Value: []byte{0x01}},
			{Type: 0x12, Value: []byte{0x00}},
			{Type: 0x15, Value: []byte{0x01}},
			{Type: 0x18, Value: []byte{0x01}},
			{Type: 0x19, Value: []byte{0x00}},
			{Type: 0x1A, Value: []byte{0x01}},
			{Type: 0x1C, Value: []byte{0x01}},
		},
	}

	info, err := ParseIMSServicesEnabledSetting(packet)
	if err != nil {
		t.Fatalf("ParseIMSServicesEnabledSetting returned error: %v", err)
	}
	if !info.HasVoiceOverLTEEnabled || !info.VoiceOverLTEEnabled {
		t.Fatalf("unexpected voice over LTE parse: %+v", info)
	}
	if !info.HasVideoTelephonyEnabled || info.VideoTelephonyEnabled {
		t.Fatalf("unexpected video telephony parse: %+v", info)
	}
	if !info.HasVoiceWiFiEnabled || !info.VoiceWiFiEnabled {
		t.Fatalf("unexpected voice wifi parse: %+v", info)
	}
	if info.HasPresenceEnabled || info.HasCallModePreference {
		t.Fatalf("response should not synthesize absent fields: %+v", info)
	}
}

func TestParseIMSSettingsChangedIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x10, Value: []byte{0x01}},
			{Type: 0x11, Value: []byte{0x01}},
			{Type: 0x14, Value: []byte{0x00}},
			{Type: 0x18, Value: []byte{0x01}},
			{Type: 0x1E, Value: []byte{0x01}},
			{Type: 0x1F, Value: []byte{0x00}},
			{Type: 0x20, Value: []byte{0x01}},
			{Type: 0x21, Value: []byte{0x00}},
			{Type: 0x25, Value: []byte{0x01}},
		},
	}

	info, err := ParseIMSServicesEnabledSetting(packet)
	if err != nil {
		t.Fatalf("ParseIMSServicesEnabledSetting returned error: %v", err)
	}
	if !info.HasPresenceEnabled || !info.PresenceEnabled {
		t.Fatalf("unexpected presence parse: %+v", info)
	}
	if !info.HasXDMClientEnabled || !info.XDMClientEnabled {
		t.Fatalf("unexpected xdm parse: %+v", info)
	}
	if !info.HasCarrierConfigEnabled || !info.CarrierConfigEnabled {
		t.Fatalf("unexpected carrier config parse: %+v", info)
	}
}

func TestParseIMSPGetEnablerStateResponse(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			imsTLVUint32(0x10, uint32(IMSPEnablerStateRegistered)),
		},
	}

	state, err := parseIMSPGetEnablerStateResponse(packet)
	if err != nil {
		t.Fatalf("parseIMSPGetEnablerStateResponse returned error: %v", err)
	}
	if state != IMSPEnablerStateRegistered {
		t.Fatalf("unexpected IMSP state: %v", state)
	}
}

func TestDispatchIMSIndications(t *testing.T) {
	c := &Client{eventCh: make(chan Event, 8)}
	cases := []struct {
		service uint8
		msgID   uint16
		want    EventType
	}{
		{service: ServiceIMSA, msgID: IMSARegistrationStatusChanged, want: EventIMSRegistrationStatus},
		{service: ServiceIMSA, msgID: IMSAServicesStatusChanged, want: EventIMSServicesStatus},
		{service: ServiceIMS, msgID: IMSSettingsChangedInd, want: EventIMSSettingsChanged},
	}

	for _, tc := range cases {
		c.dispatchIndication(&Packet{ServiceType: tc.service, MessageID: tc.msgID, IsIndication: true})
		evt := <-c.eventCh
		if evt.Type != tc.want {
			t.Fatalf("service 0x%02X msg 0x%04X dispatched as %v, want %v", tc.service, tc.msgID, evt.Type, tc.want)
		}
	}
}
