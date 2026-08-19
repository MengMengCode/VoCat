package device

import (
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

func TestDecodeAlphanumericTPAddress(t *testing.T) {
	// "TEST" encoded as 4 GSM-7 septets packed into 4 bytes (non-standard septet count format: length=4).
	cursor := &pduCursor{data: []byte{0x04, 0xd0, 0xd4, 0xe2, 0x94, 0x0a}}
	address, err := readTPAddress(cursor)
	if err != nil {
		t.Fatalf("readTPAddress error = %v", err)
	}
	if address != "TEST" {
		t.Fatalf("readTPAddress = %q, want TEST", address)
	}
	if cursor.index != len(cursor.data) {
		t.Fatalf("cursor did not consume all bytes: %d/%d", cursor.index, len(cursor.data))
	}
}

func TestDecodeAlphanumericTPAddressStandard3GPP(t *testing.T) {
	// "Google" (6 chars) encoded per 3GPP TS 23.040 §9.1.2.5:
	// length = 0x0B (11 useful semi-octets), TOA = 0xD0 (Alphanumeric),
	// 6 bytes payload: C7 F7 FB CC 2E 03
	cursor := &pduCursor{data: []byte{0x0b, 0xd0, 0xc7, 0xf7, 0xfb, 0xcc, 0x2e, 0x03}}
	address, err := readTPAddress(cursor)
	if err != nil {
		t.Fatalf("readTPAddress standard 3GPP error = %v", err)
	}
	if address != "Google" {
		t.Fatalf("readTPAddress standard 3GPP = %q, want Google", address)
	}
	if cursor.index != len(cursor.data) {
		t.Fatalf("cursor did not consume all bytes: %d/%d", cursor.index, len(cursor.data))
	}

	// "TEST" (4 chars) with standard 3GPP semi-octets (length = 0x08, 8 semi-octets -> 4 bytes)
	cursorTest := &pduCursor{data: []byte{0x08, 0xd0, 0xd4, 0xe2, 0x94, 0x0a}}
	addressTest, err := readTPAddress(cursorTest)
	if err != nil {
		t.Fatalf("readTPAddress standard 3GPP TEST error = %v", err)
	}
	if addressTest != "TEST" {
		t.Fatalf("readTPAddress standard 3GPP TEST = %q, want TEST", addressTest)
	}
}

func TestDecodeDeliverPDUWithAlphanumericSender(t *testing.T) {
	// SMS-DELIVER with alphanumeric originator "VoCat" and empty user data.
	// SMSC length=0, first octet=0x04, OA length=0x05, OA TON=0xD0,
	// OA bytes pack "VoCat" (5 septets -> 5 bytes), PID=0x00, DCS=0x00,
	// SCTS=7 bytes, UDL=0x00.
	message, err := decodeSMSPDU("000405D0D6F7304C0700004210203040500000")
	if err != nil {
		t.Fatalf("decodeSMSPDU error = %v", err)
	}
	if message.From != "VoCat" {
		t.Fatalf("From = %q, want VoCat", message.From)
	}
	if message.Direction != SMSDirectionReceived {
		t.Fatalf("Direction = %q", message.Direction)
	}
}

func TestDecode8BitPDUShowsHexPayload(t *testing.T) {
	// SMS-DELIVER with no SMSC, from +12345, DCS=0xF5 (8-bit data,
	// alphabet bits 0x0c), UDL=3. User data bytes are 0xAA 0xBB 0xCC.
	// Built from the GSM-7 deliver vector by swapping the DCS to 0xF5
	// and replacing the user data with three raw binary bytes.
	message, err := decodeSMSPDU(
		"000405912143F500F54210203040500003AABBCC",
	)
	if err != nil {
		t.Fatalf("decode 8-bit: %v", err)
	}
	if message.Encoding != SMSEncoding8BitPDU ||
		message.Text != "AABBCC" ||
		message.RawUserData != "AABBCC" {
		t.Fatalf("8-bit message = %#v", message)
	}
}
