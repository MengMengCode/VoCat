package qmi

import (
	"encoding/binary"
	"strings"
	"testing"
)

func wmsTLVUint16(tlvType uint8, v uint16) TLV {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func wmsTLVUint32(tlvType uint8, v uint32) TLV {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func TestBuildRawWriteMessageTLVs(t *testing.T) {
	pdu := []byte{0x11, 0x22, 0x33}
	tlvs := buildRawWriteMessageTLVs(0x01, 0x06, pdu)

	if len(tlvs) != 1 {
		t.Fatalf("expected 1 TLV, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 {
		t.Fatalf("unexpected TLV type: 0x%02X", tlvs[0].Type)
	}
	if len(tlvs[0].Value) != 7 {
		t.Fatalf("unexpected TLV length: %d", len(tlvs[0].Value))
	}
	if tlvs[0].Value[0] != 0x01 || tlvs[0].Value[1] != 0x06 {
		t.Fatalf("unexpected storage/format bytes: %v", tlvs[0].Value[:2])
	}
	if got := binary.LittleEndian.Uint16(tlvs[0].Value[2:4]); got != uint16(len(pdu)) {
		t.Fatalf("unexpected PDU length: %d", got)
	}
	if got := tlvs[0].Value[4:]; len(got) != len(pdu) || got[2] != 0x33 {
		t.Fatalf("unexpected PDU bytes: %v", got)
	}
}

func TestParseRawWriteMessageResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			wmsTLVUint32(0x01, 0x12345678),
		},
	}

	index, err := parseRawWriteMessageResponse(resp)
	if err != nil {
		t.Fatalf("parseRawWriteMessageResponse returned error: %v", err)
	}
	if index != 0x12345678 {
		t.Fatalf("unexpected memory index: 0x%X", index)
	}
}

func TestParseRawReadMessageValueTrimsUntaggedPadding(t *testing.T) {
	pdu := []byte{0x07, 0x91, 0x44, 0x87, 0x20}
	val := append([]byte{0x06, byte(len(pdu)), 0x00}, pdu...)
	val = append(val, 0x00, 0x00, 0x00, 0x00)

	got, err := parseRawReadMessageValue(val)
	if err != nil {
		t.Fatalf("parseRawReadMessageValue() error = %v", err)
	}
	if string(got.data) != string(pdu) || got.hasTag {
		t.Fatalf("got data=%x hasTag=%v, want data=%x hasTag=false", got.data, got.hasTag, pdu)
	}
}

func TestParseRawReadMessageValueTrimsTaggedPadding(t *testing.T) {
	pdu := []byte{0x07, 0x91, 0x44, 0x87, 0x20}
	val := append([]byte{byte(TagTypeMTNotRead), 0x06, byte(len(pdu)), 0x00}, pdu...)
	val = append(val, 0x00, 0x00, 0x00, 0x00)

	got, err := parseRawReadMessageValue(val)
	if err != nil {
		t.Fatalf("parseRawReadMessageValue() error = %v", err)
	}
	if string(got.data) != string(pdu) || !got.hasTag || got.tag != TagTypeMTNotRead {
		t.Fatalf("got data=%x hasTag=%v tag=%v, want data=%x hasTag=true tag=%v", got.data, got.hasTag, got.tag, pdu, TagTypeMTNotRead)
	}
}

func TestParseGetMessageProtocolResponse(t *testing.T) {
	cases := []struct {
		name     string
		value    byte
		expected WMSMessageProtocol
	}{
		{name: "CDMA", value: 0x00, expected: WMSMessageProtocolCDMA},
		{name: "WCDMA", value: 0x01, expected: WMSMessageProtocolWCDMA},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &Packet{
				TLVs: []TLV{
					successResultTLV(),
					{Type: 0x01, Value: []byte{tc.value}},
				},
			}

			protocol, err := parseGetMessageProtocolResponse(resp)
			if err != nil {
				t.Fatalf("parseGetMessageProtocolResponse returned error: %v", err)
			}
			if protocol != tc.expected {
				t.Fatalf("unexpected protocol: %v", protocol)
			}
		})
	}
}

func TestBuildSetRoutesTLVs(t *testing.T) {
	tlvs := buildSetRoutesTLVs([]WMSRoute{
		{MessageType: 0x00, MessageClass: 0x01, StorageType: 0x02, ReceiptAction: 0x03},
		{MessageType: 0x04, MessageClass: 0x05, StorageType: 0x06, ReceiptAction: 0x07},
	}, true)

	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 {
		t.Fatalf("unexpected route list TLV type: 0x%02X", tlvs[0].Type)
	}
	if got := binary.LittleEndian.Uint16(tlvs[0].Value[0:2]); got != 2 {
		t.Fatalf("unexpected route count: %d", got)
	}
	if tlvs[0].Value[2] != 0x00 || tlvs[0].Value[5] != 0x03 || tlvs[0].Value[6] != 0x04 || tlvs[0].Value[9] != 0x07 {
		t.Fatalf("unexpected route encoding: %v", tlvs[0].Value)
	}
	if tlvs[1].Type != 0x10 || tlvs[1].Value[0] != 0x01 {
		t.Fatalf("unexpected transfer status TLV: %+v", tlvs[1])
	}
}

func TestParseGetRoutesResponse(t *testing.T) {
	routeList := []byte{
		0x02, 0x00,
		0x00, 0x01, 0x02, 0x03,
		0x04, 0x05, 0x06, 0x07,
	}
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: routeList},
			{Type: 0x10, Value: []byte{0x01}},
		},
	}

	config, err := parseGetRoutesResponse(resp)
	if err != nil {
		t.Fatalf("parseGetRoutesResponse returned error: %v", err)
	}
	if len(config.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(config.Routes))
	}
	if config.Routes[1].StorageType != 0x06 || config.Routes[1].ReceiptAction != 0x07 {
		t.Fatalf("unexpected route decode: %+v", config.Routes[1])
	}
	if !config.HasTransferStatusReport || !config.TransferStatusReportToClient {
		t.Fatalf("unexpected transfer status config: %+v", config)
	}
}

func TestParseGetRoutesResponseWithoutRouteListReturnsEmptySlice(t *testing.T) {
	resp := &Packet{TLVs: []TLV{successResultTLV()}}

	config, err := parseGetRoutesResponse(resp)
	if err != nil {
		t.Fatalf("parseGetRoutesResponse returned error: %v", err)
	}
	if len(config.Routes) != 0 {
		t.Fatalf("expected empty routes, got %+v", config.Routes)
	}
}

func TestBuildSendAckTLVsSuccess(t *testing.T) {
	ims := true
	tlvs := buildSendAckTLVs(WMSAckRequest{
		TransactionID: 0x01020304,
		Protocol:      WMSMessageProtocolWCDMA,
		Success:       true,
		SMSOnIMS:      &ims,
	})

	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 {
		t.Fatalf("unexpected info TLV type: 0x%02X", tlvs[0].Type)
	}
	if got := binary.LittleEndian.Uint32(tlvs[0].Value[0:4]); got != 0x01020304 {
		t.Fatalf("unexpected transaction ID: 0x%X", got)
	}
	if tlvs[0].Value[4] != 0x01 || tlvs[0].Value[5] != 0x01 {
		t.Fatalf("unexpected protocol/success bytes: %v", tlvs[0].Value[4:6])
	}
	if tlvs[1].Type != 0x12 || tlvs[1].Value[0] != 0x01 {
		t.Fatalf("unexpected SMS on IMS TLV: %+v", tlvs[1])
	}
}

func TestBuildSendAckTLVsFailure(t *testing.T) {
	tlvs := buildSendAckTLVs(WMSAckRequest{
		TransactionID: 0x99,
		Protocol:      WMSMessageProtocolCDMA,
		Success:       false,
		Failure3GPP2:  &WMSAck3GPP2Failure{ErrorClass: 0x05, CauseCode: 0x06},
		Failure3GPP:   &WMSAck3GPPFailure{RPCause: 0x07, TPCause: 0x08},
	})

	if len(tlvs) != 3 {
		t.Fatalf("expected 3 TLVs, got %d", len(tlvs))
	}
	if tlvs[1].Type != 0x10 || tlvs[1].Value[0] != 0x05 || tlvs[1].Value[1] != 0x06 {
		t.Fatalf("unexpected 3GPP2 failure TLV: %+v", tlvs[1])
	}
	if tlvs[2].Type != 0x11 || tlvs[2].Value[0] != 0x07 || tlvs[2].Value[1] != 0x08 {
		t.Fatalf("unexpected 3GPP failure TLV: %+v", tlvs[2])
	}
}

func TestParseSendAckResponseAckNotSentKeepsFailureCause(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			qmiErrorResultTLV(QMIErrCallFailed),
			{Type: 0x10, Value: []byte{0x02}},
		},
	}

	result, err := parseSendAckResponse(resp)
	if err == nil {
		t.Fatal("expected parseSendAckResponse to return error")
	}
	if result == nil || !result.HasFailureCause || result.FailureCause != 0x02 {
		t.Fatalf("unexpected ACK result: %+v", result)
	}
}

func TestBuildSendFromStorageTLVs(t *testing.T) {
	tlvs := buildSendFromStorageTLVs(0x01, 0x12345678, MessageModeGW, true)

	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x01 {
		t.Fatalf("unexpected info TLV type: 0x%02X", tlvs[0].Type)
	}
	if tlvs[0].Value[0] != 0x01 || tlvs[0].Value[5] != uint8(MessageModeGW) {
		t.Fatalf("unexpected storage/mode bytes: %v", tlvs[0].Value)
	}
	if got := binary.LittleEndian.Uint32(tlvs[0].Value[1:5]); got != 0x12345678 {
		t.Fatalf("unexpected memory index: 0x%X", got)
	}
	if tlvs[1].Type != 0x10 || tlvs[1].Value[0] != 0x01 {
		t.Fatalf("unexpected SMS on IMS TLV: %+v", tlvs[1])
	}
}

func TestParseSendFromStorageResponseSuccess(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			wmsTLVUint16(0x10, 0x4321),
		},
	}

	result, err := parseSendFromStorageResponse(resp)
	if err != nil {
		t.Fatalf("parseSendFromStorageResponse returned error: %v", err)
	}
	if !result.HasMessageID || result.MessageID != 0x4321 {
		t.Fatalf("unexpected message ID result: %+v", result)
	}
}

func TestParseSendFromStorageResponseWMSCauseCode(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			qmiErrorResultTLV(QMIErrCallFailed),
			wmsTLVUint16(0x11, 0x1001),
			{Type: 0x12, Value: []byte{0x02}},
			{Type: 0x13, Value: []byte{0x34, 0x12, 0x05}},
			{Type: 0x14, Value: []byte{0x07}},
		},
	}

	result, err := parseSendFromStorageResponse(resp)
	if err == nil {
		t.Fatal("expected parseSendFromStorageResponse to return error")
	}
	if !strings.Contains(err.Error(), "rp_cause=0x1234") || !strings.Contains(err.Error(), "tp_cause=0x05") {
		t.Fatalf("expected error summary to include GSM causes, got: %v", err)
	}
	if result == nil || !result.HasCDMACauseCode || result.CDMACauseCode != 0x1001 {
		t.Fatalf("unexpected CDMA cause result: %+v", result)
	}
	if !result.HasCDMAErrorClass || result.CDMAErrorClass != 0x02 {
		t.Fatalf("unexpected CDMA error class result: %+v", result)
	}
	if !result.HasGSMWCDMARPCause || result.GSMWCDMARPCause != 0x1234 || !result.HasGSMWCDMATPCause || result.GSMWCDMATPCause != 0x05 {
		t.Fatalf("unexpected GSM/WCDMA cause result: %+v", result)
	}
	if !result.HasDeliveryFailureType || result.DeliveryFailureType != 0x07 {
		t.Fatalf("unexpected delivery failure result: %+v", result)
	}
}

func TestSummarizeRawSendResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			wmsTLVUint16(0x01, 0x4321),
			wmsTLVUint16(0x10, 0x1001),
			{Type: 0x11, Value: []byte{0x02}},
			{Type: 0x12, Value: []byte{0x34, 0x12, 0x05}},
			{Type: 0x13, Value: []byte{0x07}},
		},
	}

	summary := summarizeRawSendResponse(resp)
	wantFragments := []string{
		"msg_id=0x4321",
		"cdma_cause=0x1001",
		"cdma_class=0x02",
		"rp_cause=0x1234",
		"tp_cause=0x05",
		"delivery_failure_type=0x07",
	}
	for _, want := range wantFragments {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q missing fragment %q", summary, want)
		}
	}
}

func TestParseTransportNetworkRegistrationStatusResponse(t *testing.T) {
	cases := []struct {
		name     string
		value    byte
		expected WMSTransportNetworkRegistration
	}{
		{name: "NoService", value: 0x00, expected: WMSTransportNetworkRegistrationNoService},
		{name: "InProcess", value: 0x01, expected: WMSTransportNetworkRegistrationInProcess},
		{name: "Limited", value: 0x03, expected: WMSTransportNetworkRegistrationLimitedService},
		{name: "Full", value: 0x04, expected: WMSTransportNetworkRegistrationFullService},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &Packet{
				TLVs: []TLV{
					successResultTLV(),
					{Type: 0x10, Value: []byte{tc.value}},
				},
			}

			status, err := parseTransportNetworkRegistrationStatusResponse(resp)
			if err != nil {
				t.Fatalf("parseTransportNetworkRegistrationStatusResponse returned error: %v", err)
			}
			if status != tc.expected {
				t.Fatalf("unexpected status: %v", status)
			}
		})
	}
}

func TestParseTransportNetworkRegistrationStatusResponseMissingTLV(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
		},
	}

	if _, err := parseTransportNetworkRegistrationStatusResponse(resp); err == nil {
		t.Fatal("expected missing TLV error")
	}
}

func TestParseTransportNetworkRegistrationStatusResponseShortTLV(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{}},
		},
	}

	if _, err := parseTransportNetworkRegistrationStatusResponse(resp); err == nil {
		t.Fatal("expected short TLV error")
	}
}

func TestParseWMSSMSCAddressIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{'I', 'P', 'V', 0x05, '1', '0', '0', '8', '6'}},
		},
	}

	info, err := ParseWMSSMSCAddressIndication(packet)
	if err != nil {
		t.Fatalf("ParseWMSSMSCAddressIndication returned error: %v", err)
	}
	if info == nil || info.Type != "IPV" || info.Digits != "10086" {
		t.Fatalf("unexpected SMSC indication payload: %+v", info)
	}
}

func TestParseWMSSMSCAddressIndicationTruncated(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{'I', 'P', 'V', 0x05, '1', '0'}},
		},
	}

	if _, err := ParseWMSSMSCAddressIndication(packet); err == nil {
		t.Fatal("expected ParseWMSSMSCAddressIndication to fail on truncated payload")
	}
}

func TestParseWMSTransportNetworkRegistrationStatusIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{byte(WMSTransportNetworkRegistrationLimitedService)}},
		},
	}

	status, err := ParseWMSTransportNetworkRegistrationStatusIndication(packet)
	if err != nil {
		t.Fatalf("ParseWMSTransportNetworkRegistrationStatusIndication returned error: %v", err)
	}
	if status != WMSTransportNetworkRegistrationLimitedService {
		t.Fatalf("unexpected transport network registration indication: %v", status)
	}
}

func TestParseWMSTransportNetworkRegistrationStatusIndicationMissingTLV(t *testing.T) {
	if _, err := ParseWMSTransportNetworkRegistrationStatusIndication(&Packet{}); err == nil {
		t.Fatal("expected missing TLV error")
	}
}

func TestParseWMSSupportedMessagesResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x10, Value: []byte{0x03, 0x00, 0x01, 0x46, 0x4B}},
		},
	}

	msgs, err := parseWMSSupportedMessagesResponse(resp)
	if err != nil {
		t.Fatalf("parseWMSSupportedMessagesResponse returned error: %v", err)
	}
	if len(msgs) != 3 || msgs[0] != 0x01 || msgs[1] != 0x46 || msgs[2] != 0x4B {
		t.Fatalf("unexpected supported messages: %v", msgs)
	}
}

func TestDispatchWMSIndications(t *testing.T) {
	c := &Client{eventCh: make(chan Event, 8)}
	cases := []struct {
		msgID uint16
		want  EventType
	}{
		{msgID: WMSEventReportInd, want: EventNewMessage},
		{msgID: WMSSMSCAddressInd, want: EventWMSSMSCAddress},
		{msgID: WMSTransportNetworkRegistrationStatusInd, want: EventWMSTransportNetworkRegistrationStatus},
		{msgID: 0x0044, want: EventUnknown},
	}

	for _, tc := range cases {
		c.dispatchIndication(&Packet{ServiceType: ServiceWMS, MessageID: tc.msgID, IsIndication: true})
		evt := <-c.eventCh
		if evt.Type != tc.want {
			t.Fatalf("WMS msg 0x%04X dispatched as %v, want %v", tc.msgID, evt.Type, tc.want)
		}
	}
}
