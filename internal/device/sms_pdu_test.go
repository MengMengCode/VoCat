package device

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestPrepareSMSSelectsDirectGSM7AndPDUEncodings(t *testing.T) {
	direct, err := prepareSMS("+12 345", "HELLO")
	if err != nil {
		t.Fatalf("prepare direct GSM-7: %v", err)
	}
	if direct.to != "+12345" ||
		direct.encoding != SMSEncodingGSM7Text ||
		direct.prompt != `AT+CMGS="+12345"` ||
		string(direct.payload) != "HELLO" {
		t.Fatalf("direct = %#v", direct)
	}

	gsmPDU, err := prepareSMS("+12345", "@")
	if err != nil {
		t.Fatalf("prepare GSM-7 PDU: %v", err)
	}
	if gsmPDU.encoding != SMSEncodingGSM7PDU ||
		gsmPDU.tpduLength != 11 ||
		string(gsmPDU.payload) != "00210005912143F500000100" {
		t.Fatalf("GSM PDU = %#v", gsmPDU)
	}

	unicode, err := prepareSMS("+12345", "你好")
	if err != nil {
		t.Fatalf("prepare UCS2 PDU: %v", err)
	}
	if unicode.encoding != SMSEncodingUCS2PDU ||
		unicode.tpduLength != 14 ||
		unicode.prompt != "AT+CMGS=14" ||
		string(unicode.payload) != "00210005912143F50008044F60597D" {
		t.Fatalf("UCS2 PDU = %#v", unicode)
	}
}

func TestPrepareAndDecodeTransportIndependentTPDU(t *testing.T) {
	parts, err := PrepareSMSSubmitTPDUs("+12345", "HELLO")
	if err != nil {
		t.Fatalf("PrepareSMSSubmitTPDUs: %v", err)
	}
	if len(parts) != 1 || parts[0].To != "+12345" || len(parts[0].TPDU) == 0 ||
		parts[0].TPDU[0]&0x03 != 1 || parts[0].TPDU[0]&0x20 == 0 {
		t.Fatalf("parts = %#v", parts)
	}
	message, err := DecodeSMSDeliverTPDU([]byte{
		0x04, 0x05, 0x91, 0x21, 0x43, 0xf5, 0x00, 0x00,
		0x42, 0x10, 0x20, 0x30, 0x40, 0x50, 0x00, 0x05,
		0xc8, 0x22, 0x93, 0xf9, 0x04,
	})
	if err != nil || message.From != "+12345" || message.Text != "HELLO" {
		t.Fatalf("DecodeSMSDeliverTPDU = (%#v, %v)", message, err)
	}
}

func TestPrepareSMSSubmitTPDUsNormalizesInternational00Prefix(t *testing.T) {
	parts, err := PrepareSMSSubmitTPDUs("0012345", "HELLO")
	if err != nil {
		t.Fatalf("PrepareSMSSubmitTPDUs: %v", err)
	}
	if len(parts) != 1 || parts[0].To != "+12345" {
		t.Fatalf("parts = %#v", parts)
	}
	message, err := decodeSMSPDU("00" + hex.EncodeToString(parts[0].TPDU))
	if err != nil {
		t.Fatalf("decodeSMSPDU: %v", err)
	}
	if message.To != "+12345" {
		t.Fatalf("decoded recipient = %q", message.To)
	}
}

func TestPrepareSMSRejectsInvalidAndOversizeMessages(t *testing.T) {
	if _, err := prepareSMS(`12"34`, "hello"); !errors.Is(err, ErrSMSInvalidRecipient) {
		t.Fatalf("invalid recipient error = %v", err)
	}
	if _, err := prepareSMS("12345", ""); !errors.Is(err, ErrSMSEmpty) {
		t.Fatalf("empty message error = %v", err)
	}
	if _, err := prepareSMS(
		"12345",
		strings.Repeat("A", 161),
	); !errors.Is(err, ErrSMSTooLong) {
		t.Fatalf("long GSM-7 error = %v", err)
	}
	if _, err := prepareSMS(
		"12345",
		strings.Repeat("你", 71),
	); !errors.Is(err, ErrSMSTooLong) {
		t.Fatalf("long UCS2 error = %v", err)
	}
}

func TestPrepareMultipartGSM7Uses153SeptetsAndSharedUDH(t *testing.T) {
	const reference = 0x7a
	text := strings.Repeat("A", 161)
	parts, err := prepareSMSPartsWithReference("+12345", text, reference)
	if err != nil {
		t.Fatalf("prepare multipart GSM-7: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	for index, part := range parts {
		message, decodeErr := decodeSMSPDU(string(part.payload))
		if decodeErr != nil {
			t.Fatalf("decode part %d: %v", index+1, decodeErr)
		}
		wantText := strings.Repeat("A", 153)
		if index == 1 {
			wantText = strings.Repeat("A", 8)
		}
		if message.Text != wantText ||
			message.Concat == nil ||
			message.Concat.Reference != reference ||
			message.Concat.Total != 2 ||
			message.Concat.Sequence != index+1 ||
			part.part != index+1 ||
			part.total != 2 ||
			part.encoding != SMSEncodingGSM7PDU {
			t.Fatalf("part %d = prepared %#v, decoded %#v", index+1, part, message)
		}
	}
}

func TestPrepareMultipartGSM7DoesNotSplitExtensionEscape(t *testing.T) {
	text := strings.Repeat("A", 152) + "^" + strings.Repeat("B", 10)
	parts, err := prepareSMSPartsWithReference("+12345", text, 7)
	if err != nil {
		t.Fatalf("prepare multipart extension: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	first, err := decodeSMSPDU(string(parts[0].payload))
	if err != nil {
		t.Fatalf("decode first part: %v", err)
	}
	second, err := decodeSMSPDU(string(parts[1].payload))
	if err != nil {
		t.Fatalf("decode second part: %v", err)
	}
	if first.Text != strings.Repeat("A", 152) ||
		second.Text != "^"+strings.Repeat("B", 10) {
		t.Fatalf("split text = %q + %q", first.Text, second.Text)
	}
}

func TestPrepareMultipartUCS2DoesNotSplitSurrogatePair(t *testing.T) {
	const reference = 0x52
	text := strings.Repeat("你", 66) + "😀" + strings.Repeat("好", 4)
	parts, err := prepareSMSPartsWithReference("+12345", text, reference)
	if err != nil {
		t.Fatalf("prepare multipart UCS2: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	first, err := decodeSMSPDU(string(parts[0].payload))
	if err != nil {
		t.Fatalf("decode first part: %v", err)
	}
	second, err := decodeSMSPDU(string(parts[1].payload))
	if err != nil {
		t.Fatalf("decode second part: %v", err)
	}
	if first.Text != strings.Repeat("你", 66) ||
		second.Text != "😀"+strings.Repeat("好", 4) {
		t.Fatalf("split text = %q + %q", first.Text, second.Text)
	}
	for index, message := range []SMSMessage{first, second} {
		if message.Concat == nil ||
			message.Concat.Reference != reference ||
			message.Concat.Total != 2 ||
			message.Concat.Sequence != index+1 {
			t.Fatalf("concat part %d = %#v", index+1, message.Concat)
		}
	}
}

func TestPrepareMultipartRejectsMoreThan255Parts(t *testing.T) {
	if _, err := prepareSMSPartsWithReference(
		"12345",
		strings.Repeat("A", 153*255+1),
		1,
	); !errors.Is(err, ErrSMSTooLong) {
		t.Fatalf("oversize multipart GSM-7 error = %v", err)
	}
	if _, err := prepareSMSPartsWithReference(
		"12345",
		strings.Repeat("你", 67*255+1),
		1,
	); !errors.Is(err, ErrSMSTooLong) {
		t.Fatalf("oversize multipart UCS2 error = %v", err)
	}
}

func TestDecodeDeliverPDUHandlesGSM7AndUCS2(t *testing.T) {
	gsm, err := decodeSMSPDU(
		"000405912143F500004210203040500005C82293F904",
	)
	if err != nil {
		t.Fatalf("decode GSM-7: %v", err)
	}
	if gsm.Direction != SMSDirectionReceived ||
		gsm.From != "+12345" ||
		gsm.Text != "HELLO" ||
		gsm.Encoding != SMSEncodingGSM7PDU {
		t.Fatalf("GSM message = %#v", gsm)
	}
	if gsm.ServiceCenterTimestamp == nil ||
		gsm.ServiceCenterTimestamp.Year() != 2024 ||
		gsm.ServiceCenterTimestamp.Month() != 1 ||
		gsm.ServiceCenterTimestamp.Day() != 2 ||
		gsm.ServiceCenterTimestamp.Hour() != 3 ||
		gsm.ServiceCenterTimestamp.Minute() != 4 ||
		gsm.ServiceCenterTimestamp.Second() != 5 {
		t.Fatalf("timestamp = %v", gsm.ServiceCenterTimestamp)
	}

	ucs2, err := decodeSMSPDU(
		"004405912143F50008421020304050000A0500037A02014F60597D",
	)
	if err != nil {
		t.Fatalf("decode UCS2: %v", err)
	}
	if ucs2.Text != "你好" ||
		ucs2.Encoding != SMSEncodingUCS2PDU ||
		ucs2.Concat == nil ||
		ucs2.Concat.Reference != 0x7a ||
		ucs2.Concat.Total != 2 ||
		ucs2.Concat.Sequence != 1 {
		t.Fatalf("UCS2 message = %#v", ucs2)
	}
}

func TestDecodeSubmitAndStatusReportPDU(t *testing.T) {
	submit, err := decodeSMSPDU("00010005912143F500000100")
	if err != nil {
		t.Fatalf("decode submit: %v", err)
	}
	if submit.Direction != SMSDirectionSubmitted ||
		submit.To != "+12345" ||
		submit.Text != "@" {
		t.Fatalf("submit = %#v", submit)
	}

	report, err := decodeSMSPDU(
		"00022A05912143F5421020304050004210203050500000",
	)
	if err != nil {
		t.Fatalf("decode status report: %v", err)
	}
	if report.Direction != SMSDirectionStatusReport ||
		report.To != "+12345" ||
		report.MessageReference == nil ||
		*report.MessageReference != 42 ||
		report.StatusCode == nil ||
		*report.StatusCode != 0 ||
		report.DeliveryStatus != "delivered" {
		t.Fatalf("status report = %#v", report)
	}
}

func TestParseCMGLPreservesUndecodableRecord(t *testing.T) {
	messages := parseCMGL(okResponse(
		"+CMGL: 9,0,,4",
		"NOT-A-PDU",
	))
	if len(messages) != 1 ||
		messages[0].Index != 9 ||
		messages[0].RawPDU != "NOT-A-PDU" ||
		messages[0].DecodeError == "" {
		t.Fatalf("messages = %#v", messages)
	}
}
