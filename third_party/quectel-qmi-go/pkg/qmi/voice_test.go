package qmi

import (
	"encoding/binary"
	"testing"
)

func voiceTLVUint16(tlvType uint8, v uint16) TLV {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func voiceTLVUint32(tlvType uint8, v uint32) TLV {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func TestBuildVoiceIndicationRegistrationTLVs(t *testing.T) {
	tlvs := buildVoiceIndicationRegistrationTLVs(VoiceIndicationRegistration{
		SupplementaryServiceNotificationEvents: true,
		CallNotificationEvents:                 true,
		USSDNotificationEvents:                 true,
	})

	if len(tlvs) != 13 {
		t.Fatalf("expected 13 TLVs, got %d", len(tlvs))
	}
	if tlvs[2].Type != 0x12 || tlvs[2].Value[0] != 0x01 {
		t.Fatalf("unexpected supplementary TLV: %+v", tlvs[2])
	}
	if tlvs[3].Type != 0x13 || tlvs[3].Value[0] != 0x01 {
		t.Fatalf("unexpected call notification TLV: %+v", tlvs[3])
	}
	if tlvs[6].Type != 0x16 || tlvs[6].Value[0] != 0x01 {
		t.Fatalf("unexpected USSD TLV: %+v", tlvs[6])
	}
	if tlvs[0].Value[0] != 0x00 || tlvs[12].Value[0] != 0x00 {
		t.Fatalf("expected disabled flags to be explicitly encoded as 0")
	}
}

func TestParseVoiceSupportedMessagesResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x03, 0x00, 0x20, 0x2F, 0x3A}},
		},
	}

	msgs, err := parseVoiceSupportedMessagesResponse(resp)
	if err != nil {
		t.Fatalf("parseVoiceSupportedMessagesResponse returned error: %v", err)
	}
	if len(msgs) != 3 || msgs[0] != 0x20 || msgs[2] != 0x3A {
		t.Fatalf("unexpected supported messages: %v", msgs)
	}
}

func TestParseVoiceCallIDResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x07}},
		},
	}

	callID, err := parseVoiceCallIDResponse(resp, "dial call")
	if err != nil {
		t.Fatalf("parseVoiceCallIDResponse returned error: %v", err)
	}
	if callID != 0x07 {
		t.Fatalf("unexpected call id: 0x%02X", callID)
	}
}

func TestBuildVoiceBurstDTMFTLV(t *testing.T) {
	tlv := buildVoiceBurstDTMFTLV(0x05, "12#")
	if tlv.Type != 0x01 {
		t.Fatalf("unexpected TLV type: 0x%02X", tlv.Type)
	}
	if tlv.Value[0] != 0x05 || tlv.Value[1] != 0x03 {
		t.Fatalf("unexpected burst DTMF header: %v", tlv.Value[:2])
	}
	if string(tlv.Value[2:]) != "12#" {
		t.Fatalf("unexpected DTMF digits: %q", string(tlv.Value[2:]))
	}
}

func TestBuildVoiceManageCallsTLVs(t *testing.T) {
	tlvs := buildVoiceManageCallsTLVs(VoiceManageCallsRequest{
		ServiceType: 0x03,
		CallID:      0x09,
	})
	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 || tlvs[0].Value[0] != 0x03 {
		t.Fatalf("unexpected manage call service TLV: %+v", tlvs[0])
	}
	if tlvs[1].Type != 0x10 || tlvs[1].Value[0] != 0x09 {
		t.Fatalf("unexpected manage call id TLV: %+v", tlvs[1])
	}
}

func TestBuildVoiceUSSDRequestTLV(t *testing.T) {
	tlv := buildVoiceUSSDRequestTLV(VoiceUSSDRequest{DCS: 0x0F, Data: []byte("*100#")})
	if tlv.Type != 0x01 {
		t.Fatalf("unexpected TLV type: 0x%02X", tlv.Type)
	}
	if tlv.Value[0] != 0x0F || tlv.Value[1] != 0x05 {
		t.Fatalf("unexpected USSD request header: %v", tlv.Value[:2])
	}
	if string(tlv.Value[2:]) != "*100#" {
		t.Fatalf("unexpected USSD payload: %q", string(tlv.Value[2:]))
	}
}

func TestBuildVoiceConfigQueryTLVsZeroMeansAll(t *testing.T) {
	tlvs := buildVoiceConfigQueryTLVs(VoiceConfigQuery{})
	if len(tlvs) != 9 {
		t.Fatalf("expected 9 TLVs, got %d", len(tlvs))
	}
	for i, tlv := range tlvs {
		wantType := uint8(0x10 + i)
		if tlv.Type != wantType || tlv.Value[0] != 0x01 {
			t.Fatalf("unexpected query TLV at %d: %+v", i, tlv)
		}
	}
}

func TestParseVoiceAllCallInfoResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{
				Type: 0x10,
				Value: []byte{
					0x02,
					0x01, 0x03, 0x00, 0x01, 0x02, 0x00, 0x00,
					0x02, 0x04, 0x01, 0x00, 0x03, 0x01, 0x01,
				},
			},
			{
				Type: 0x11,
				Value: []byte{
					0x01,
					0x02, 0x00, 0x05, '1', '0', '0', '8', '6',
				},
			},
		},
	}

	info, err := parseVoiceAllCallInfoResponse(resp)
	if err != nil {
		t.Fatalf("parseVoiceAllCallInfoResponse returned error: %v", err)
	}
	if len(info.Calls) != 2 || len(info.RemotePartyNumbers) != 1 {
		t.Fatalf("unexpected call info sizes: %+v", info)
	}
	if info.Calls[1].Multipart != true || info.Calls[1].Mode != VoiceCallMode(0x03) {
		t.Fatalf("unexpected second call info: %+v", info.Calls[1])
	}
	if info.RemotePartyNumbers[0].Number != "10086" {
		t.Fatalf("unexpected remote party number: %+v", info.RemotePartyNumbers[0])
	}
}

func TestParseVoiceAllCallStatusIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{
				Type: 0x01,
				Value: []byte{
					0x01,
					0x07, 0x02, 0x00, 0x01, 0x03, 0x00, 0x00,
				},
			},
			{
				Type: 0x10,
				Value: []byte{
					0x01,
					0x07, 0x01, 0x03, '1', '2', '3',
				},
			},
		},
	}

	info, err := ParseVoiceAllCallStatus(packet)
	if err != nil {
		t.Fatalf("ParseVoiceAllCallStatus returned error: %v", err)
	}
	if len(info.Calls) != 1 || info.Calls[0].ID != 0x07 {
		t.Fatalf("unexpected call status info: %+v", info)
	}
	if len(info.RemotePartyNumbers) != 1 || info.RemotePartyNumbers[0].Number != "123" {
		t.Fatalf("unexpected remote party info: %+v", info.RemotePartyNumbers)
	}
}

func TestParseVoiceSupplementaryServiceStatusResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x15, Value: []byte{0x01, 0x00}},
		},
	}

	status, err := parseVoiceSupplementaryServiceStatusResponse(resp)
	if err != nil {
		t.Fatalf("parseVoiceSupplementaryServiceStatusResponse returned error: %v", err)
	}
	if !status.Active || status.Provisioned {
		t.Fatalf("unexpected supplementary status: %+v", status)
	}
}

func TestParseVoiceSupplementaryServiceIndication(t *testing.T) {
	packet := &Packet{TLVs: []TLV{{Type: 0x01, Value: []byte{0x03, 0x07}}}}

	info, err := ParseVoiceSupplementaryServiceIndication(packet)
	if err != nil {
		t.Fatalf("ParseVoiceSupplementaryServiceIndication returned error: %v", err)
	}
	if info.CallID != 0x03 || info.NotificationType != 0x07 {
		t.Fatalf("unexpected supplementary indication: %+v", info)
	}
}

func TestParseVoiceSupplementaryServiceRequestIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{0x07, 0x01}},
			{Type: 0x10, Value: []byte{0x01}},
			{Type: 0x11, Value: []byte{0x0f}},
			{Type: 0x14, Value: []byte{0x01, 0x05, '*', '1', '0', '0', '#'}},
			{Type: 0x15, Value: []byte{0x09}},
			{Type: 0x16, Value: []byte{0x01, 0x02, 'O', 'K'}},
			{Type: 0x1a, Value: []byte{0x7d, 0x00}},
			{Type: 0x21, Value: []byte{0x02, 0x4f, 0x00, 0x4b, 0x00}},
			{Type: 0x22, Value: []byte{0x03}},
		},
	}

	info, err := ParseVoiceSupplementaryServiceRequestIndication(packet)
	if err != nil {
		t.Fatalf("ParseVoiceSupplementaryServiceRequestIndication returned error: %v", err)
	}
	if !info.HasInfo || info.Request != 0x07 || !info.ModifiedByCallControl {
		t.Fatalf("unexpected supplementary request info: %+v", info)
	}
	if !info.HasServiceClass || info.ServiceClass != 0x01 || !info.HasReason || info.Reason != 0x0f {
		t.Fatalf("unexpected service class/reason: %+v", info)
	}
	if info.USSData == nil || info.USSData.Text != "*100#" {
		t.Fatalf("unexpected USS data: %+v", info.USSData)
	}
	if !info.HasCallID || info.CallID != 0x09 {
		t.Fatalf("unexpected call ID: %+v", info)
	}
	if info.Alpha == nil || info.Alpha.Text != "OK" {
		t.Fatalf("unexpected alpha: %+v", info.Alpha)
	}
	if !info.HasFailureCause || info.FailureCause != 0x007d {
		t.Fatalf("unexpected failure cause: %+v", info)
	}
	if len(info.EncodedDataUTF16) != 2 || info.EncodedDataUTF16[0] != 0x004f {
		t.Fatalf("unexpected encoded data: %+v", info.EncodedDataUTF16)
	}
	if !info.HasExtendedServiceClass || info.ExtendedServiceClass != 0x03 {
		t.Fatalf("unexpected extended service class: %+v", info)
	}
}

func TestParseVoiceUSSDResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			voiceTLVUint16(0x10, 0x0022),
			{Type: 0x11, Value: []byte{0x0F, 0x02, 'O', 'K'}},
			{Type: 0x12, Value: []byte{0x0F, 0x05, '*', '1', '0', '0', '#'}},
			{Type: 0x13, Value: []byte{0x01}},
			{Type: 0x14, Value: []byte{0x09}},
			{Type: 0x15, Value: []byte{0x02, 0x03}},
			{Type: 0x16, Value: []byte{0x02, 0x34, 0x12, 0x78, 0x56}},
		},
	}

	result, err := parseVoiceUSSDResponse(resp, "originate ussd")
	if err != nil {
		t.Fatalf("parseVoiceUSSDResponse returned error: %v", err)
	}
	if !result.HasFailureCause || result.FailureCause != 0x0022 {
		t.Fatalf("unexpected failure cause parse: %+v", result)
	}
	if result.Alpha == nil || result.Alpha.Text != "OK" {
		t.Fatalf("unexpected alpha payload: %+v", result.Alpha)
	}
	if result.USSData == nil || result.USSData.Text != "*100#" {
		t.Fatalf("unexpected USS payload: %+v", result.USSData)
	}
	if !result.HasCallControlResultType || result.CallControlResultType != 0x01 {
		t.Fatalf("unexpected result type parse: %+v", result)
	}
	if !result.HasCallID || result.CallID != 0x09 {
		t.Fatalf("unexpected call ID parse: %+v", result)
	}
	if !result.HasCallControlSupplementaryServiceReason || result.CallControlSupplementaryServiceReason != 0x03 {
		t.Fatalf("unexpected supplementary service parse: %+v", result)
	}
	if len(result.USSDataUTF16) != 2 || result.USSDataUTF16[1] != 0x5678 {
		t.Fatalf("unexpected UTF16 data: %+v", result.USSDataUTF16)
	}
}

func TestParseVoiceUSSDIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{0x02}},
			{Type: 0x10, Value: []byte{0x0F, 0x05, '*', '1', '2', '3', '#'}},
			{Type: 0x11, Value: []byte{0x01, 0x41, 0x00}},
		},
	}

	info, err := ParseVoiceUSSDIndication(packet)
	if err != nil {
		t.Fatalf("ParseVoiceUSSDIndication returned error: %v", err)
	}
	if !info.HasUserAction || info.UserAction != VoiceUserAction(0x02) {
		t.Fatalf("unexpected user action parse: %+v", info)
	}
	if info.USSData == nil || info.USSData.Text != "*123#" {
		t.Fatalf("unexpected USSD indication payload: %+v", info.USSData)
	}
	if len(info.USSDataUTF16) != 1 || info.USSDataUTF16[0] != 0x0041 {
		t.Fatalf("unexpected UTF16 indication payload: %+v", info.USSDataUTF16)
	}
}

func TestParseVoiceUSSDNoWaitIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			voiceTLVUint16(0x10, 0x1001),
			voiceTLVUint16(0x11, 0x2002),
			{Type: 0x12, Value: []byte{0x0F, 0x03, '1', '2', '3'}},
			{Type: 0x13, Value: []byte{0x0F, 0x02, 'O', 'K'}},
			{Type: 0x14, Value: []byte{0x01, 0x42, 0x00}},
		},
	}

	info, err := ParseVoiceUSSDNoWaitIndication(packet)
	if err != nil {
		t.Fatalf("ParseVoiceUSSDNoWaitIndication returned error: %v", err)
	}
	if !info.HasErrorCode || info.ErrorCode != 0x1001 || !info.HasFailureCause || info.FailureCause != 0x2002 {
		t.Fatalf("unexpected no-wait codes: %+v", info)
	}
	if info.USSData == nil || info.USSData.Text != "123" {
		t.Fatalf("unexpected no-wait uss data: %+v", info.USSData)
	}
	if info.Alpha == nil || info.Alpha.Text != "OK" {
		t.Fatalf("unexpected no-wait alpha data: %+v", info.Alpha)
	}
	if len(info.USSDataUTF16) != 1 || info.USSDataUTF16[0] != 0x0042 {
		t.Fatalf("unexpected no-wait UTF16 data: %+v", info.USSDataUTF16)
	}
}

func TestParseVoiceConfigResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x01}},
			{Type: 0x11, Value: []byte{0x02, 0x78, 0x56, 0x34, 0x12}},
			{Type: 0x12, Value: []byte{0x03, 0x21, 0x43, 0x65, 0x87}},
			{Type: 0x13, Value: []byte{0x01}},
			{Type: 0x14, Value: []byte{0x04, 0x01, 0x34, 0x12, 0x78, 0x56, 0xBC, 0x9A}},
			{Type: 0x15, Value: []byte{0x01, 0x02}},
			{Type: 0x16, Value: []byte{0x03}},
			{Type: 0x17, Value: []byte{0x04}},
		},
	}

	cfg, err := parseVoiceConfigResponse(resp)
	if err != nil {
		t.Fatalf("parseVoiceConfigResponse returned error: %v", err)
	}
	if !cfg.HasAutoAnswerStatus || !cfg.AutoAnswerStatus {
		t.Fatalf("unexpected auto answer status: %+v", cfg)
	}
	if !cfg.HasAirTimer || cfg.AirTimer.NAMID != 0x02 || cfg.AirTimer.Minutes != 0x12345678 {
		t.Fatalf("unexpected air timer: %+v", cfg.AirTimer)
	}
	if !cfg.HasPreferredVoiceSO || cfg.PreferredVoiceSO.HomePageVoiceServiceOption != 0x1234 {
		t.Fatalf("unexpected preferred voice SO: %+v", cfg.PreferredVoiceSO)
	}
	if !cfg.HasCurrentAMRStatus || !cfg.CurrentAMRStatus.GSM || cfg.CurrentAMRStatus.WCDMA != 0x02 {
		t.Fatalf("unexpected AMR status: %+v", cfg.CurrentAMRStatus)
	}
	if !cfg.HasCurrentVoicePrivacyPreference || cfg.CurrentVoicePrivacyPreference != 0x03 {
		t.Fatalf("unexpected voice privacy: %+v", cfg)
	}
	if !cfg.HasCurrentVoiceDomainPreference || cfg.CurrentVoiceDomainPreference != 0x04 {
		t.Fatalf("unexpected voice domain preference: %+v", cfg)
	}
}

func TestDispatchVoiceIndications(t *testing.T) {
	c := &Client{eventCh: make(chan Event, 8)}
	cases := []struct {
		msgID uint16
		want  EventType
	}{
		{msgID: VOICEAllCallStatusInd, want: EventVoiceCallStatus},
		{msgID: VOICESupplementaryServiceInd, want: EventVoiceSupplementaryService},
		{msgID: VOICESupplementaryServiceRequestInd, want: EventVoiceSupplementaryServiceRequest},
		{msgID: VOICEUSSDInd, want: EventUSSD},
		{msgID: VOICEReleaseUSSDInd, want: EventVoiceUSSDReleased},
		{msgID: VOICEOriginateUSSDNoWait, want: EventVoiceUSSDNoWaitResult},
	}

	for _, tc := range cases {
		c.dispatchIndication(&Packet{ServiceType: ServiceVOICE, MessageID: tc.msgID, IsIndication: true})
		evt := <-c.eventCh
		if evt.Type != tc.want {
			t.Fatalf("message 0x%04X dispatched as %v, want %v", tc.msgID, evt.Type, tc.want)
		}
	}
}
