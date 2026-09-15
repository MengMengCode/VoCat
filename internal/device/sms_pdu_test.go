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

func TestDecodeDeliverPDUWithShortStandardAlphanumericSender(t *testing.T) {
	// TP-OA length is expressed in useful semi-octets. The three-character
	// sender "OKX" therefore has length 6, even though it contains 3 septets.
	// A previous short-address heuristic interpreted 6 as the character count
	// and swallowed PID, DCS, and timestamp bytes into the sender address.
	text := "Your OKX verification code is: 123456"
	textSeptets, ok := encodeGSM7(text)
	if !ok {
		t.Fatal("test text is not GSM-7 encodable")
	}
	pdu := []byte{0x00, 0x04, 0x06, 0xd0}
	pdu = append(pdu, packSeptets([]byte{'O', 'K', 'X'}, 0)...)
	pdu = append(pdu,
		0x00, 0x00, // PID and GSM-7 DCS.
		0x62, 0x80, 0x20, 0x91, 0x40, 0x95, 0x00, // 2026-08-02 19:04:59 UTC.
		byte(len(textSeptets)),
	)
	pdu = append(pdu, packSeptets(textSeptets, 0)...)

	message, err := decodeSMSPDU(hex.EncodeToString(pdu))
	if err != nil {
		t.Fatalf("decode short alphanumeric sender: %v", err)
	}
	if message.From != "OKX" || message.Text != text ||
		message.Encoding != SMSEncodingGSM7PDU || message.DataCodingScheme != 0 {
		t.Fatalf("message = %#v", message)
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

func TestDecodeSIMDataDownloadMarksBinaryPayload(t *testing.T) {
	// O2 SMS-PP data download: PID 0x7F, 8-bit class-2 DCS 0xF6 and
	// UDH IEI 0x70 (UICC toolkit security header).
	tpdu, err := hex.DecodeString("440C919471071610007FF6629041718111403D02700000381516001212B201000D5F284696D1470A06A44E649D62B3BC7B6A11D49874DBE86C379BD4A87805BDA5ED2FF2DE9416A43640832306C159E1")
	if err != nil {
		t.Fatal(err)
	}
	message, err := DecodeSMSDeliverTPDU(tpdu)
	if err != nil {
		t.Fatalf("decode SIM data download: %v", err)
	}
	if !message.SIMDataDownload || message.ProtocolID != 0x7f ||
		message.DataCodingScheme != 0xf6 || message.Encoding != SMSEncoding8BitPDU {
		t.Fatalf("SIM data download = %#v", message)
	}
}

func TestSIMDataDownloadRequiresPIDAndClass2DCS(t *testing.T) {
	for _, test := range []struct {
		pid, dcs byte
		want     bool
	}{
		{pid: 0x7f, dcs: 0xf6, want: true},
		{pid: 0x7f, dcs: 0x16, want: true},
		{pid: 0x00, dcs: 0xf6, want: false},
		{pid: 0x7f, dcs: 0xf5, want: false},
		{pid: 0x7f, dcs: 0x06, want: false},
	} {
		if got := isSIMDataDownload(test.pid, test.dcs); got != test.want {
			t.Fatalf("isSIMDataDownload(0x%02X, 0x%02X) = %v, want %v", test.pid, test.dcs, got, test.want)
		}
	}
}

func TestDecodeUserDataUnderstandsDCSGroups(t *testing.T) {
	septets, ok := encodeGSM7("HELLO")
	if !ok {
		t.Fatal("encode GSM-7 test text")
	}
	packed := packSeptets(septets, 0)
	for _, dcs := range []byte{0x00, 0xc8, 0xd0, 0xf0} {
		message := SMSMessage{}
		if err := decodeUserData(packed, 0, dcs, len(septets), &message); err != nil {
			t.Fatalf("decode DCS 0x%02X: %v", dcs, err)
		}
		if message.Text != "HELLO" || message.Encoding != SMSEncodingGSM7PDU {
			t.Fatalf("DCS 0x%02X message = %#v", dcs, message)
		}
	}

	ucs2 := []byte{0x4f, 0x60, 0x59, 0x7d}
	for _, dcs := range []byte{0x08, 0xe0} {
		message := SMSMessage{}
		if err := decodeUserData(ucs2, 0, dcs, len(ucs2), &message); err != nil {
			t.Fatalf("decode DCS 0x%02X: %v", dcs, err)
		}
		if message.Text != "你好" || message.Encoding != SMSEncodingUCS2PDU {
			t.Fatalf("DCS 0x%02X message = %#v", dcs, message)
		}
	}
}

func TestDecodeGSM7NationalLanguageTables(t *testing.T) {
	// National language locking shift IEI 0x25, Turkish table 1. In that
	// locking table septet 0x07 is the dotless i (ı), rather than default ì.
	header := []byte{0x03, 0x25, 0x01, 0x01}
	headerSeptets := (len(header)*8 + 6) / 7
	data := packSeptets([]byte{0x07}, headerSeptets*7)
	copy(data, header)

	message := SMSMessage{}
	if err := decodeUserData(data, 0x40, 0x00, headerSeptets+1, &message); err != nil {
		t.Fatalf("decode Turkish locking table: %v", err)
	}
	if message.Text != "ı" || message.Encoding != SMSEncodingGSM7PDU {
		t.Fatalf("message = %#v", message)
	}
}

func TestDecode8BitTextEncodingsAndPreservesBinary(t *testing.T) {
	tests := []struct {
		name     string
		payload  []byte
		wantText string
		encoding SMSEncoding
	}{
		{name: "UTF-8", payload: []byte("验证码 123456"), wantText: "验证码 123456", encoding: SMSEncodingUTF8PDU},
		{name: "GB18030", payload: []byte{0xd1, 0xe9, 0xd6, 0xa4, 0xc2, 0xeb}, wantText: "验证码", encoding: SMSEncodingGB18030},
		{name: "Latin-1", payload: []byte{'C', 'a', 'f', 0xe9}, wantText: "Café", encoding: SMSEncodingLatin1},
		{name: "binary", payload: []byte{0xaa, 0xbb, 0xcc}, wantText: "AABBCC", encoding: SMSEncoding8BitPDU},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := SMSMessage{}
			if err := decodeUserData(test.payload, 0, 0x04, len(test.payload), &message); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if message.Text != test.wantText || message.Encoding != test.encoding {
				t.Fatalf("message = %#v", message)
			}
		})
	}
}

func TestDecodePortAddressed8BitSMSRemainsBinary(t *testing.T) {
	header := []byte{0x04, 0x04, 0x02, 0x0b, 0x84}
	data := append(append([]byte(nil), header...), []byte("plain-looking payload")...)
	message := SMSMessage{}
	if err := decodeUserData(data, 0x40, 0x04, len(data), &message); err != nil {
		t.Fatalf("decode: %v", err)
	}
	payload := data[len(header):]
	if message.Text != strings.ToUpper(hex.EncodeToString(payload)) ||
		message.Encoding != SMSEncoding8BitPDU {
		t.Fatalf("message = %#v", message)
	}
}
