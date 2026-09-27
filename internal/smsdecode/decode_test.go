package smsdecode

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Captured Hong Kong MMS notification (WAP Push, application/vnd.wap.mms-message).
// Subject charset is UTF-8 (MIBEnum 106). The operator MMSC URL is internal and
// is not part of the displayed preview.
const capturedMMSHex = "E906226170706C69636174696F6E2F766E642E7761702E6D6D732D6D65737361676500AF848C82987A66746E2D33636362363433312D326562392D343936312D626137632D303232393238356161663462008D92890C802341444343313832323200961F2AEAE881BDE588B0E3808C34E5A4A7E89789E58FA3E3808DEFBC9DE58187E5AEA2E69C8DE59183E4BABA008A808E03037F8485046AB74D90880481021C1F83687474703A2F2F6D6D7363343A383030322F7A66746E2D33636362363433312D326562392D343936312D626137632D3032323932383561616634622F00"

const capturedMMSSubject = "聽到「4大藉口」＝假客服呃人"

func TestDecodeDisplayTextExtractsMMSNotificationSubject(t *testing.T) {
	payload, err := hex.DecodeString(capturedMMSHex)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := DecodeDisplayText(payload)
	if !ok || text != capturedMMSSubject {
		t.Fatalf("DecodeDisplayText = (%q, %v), want %q", text, ok, capturedMMSSubject)
	}
}

func TestDecodeHexDisplayTextAcceptsCapturedPayload(t *testing.T) {
	text, ok := DecodeHexDisplayText(strings.ToLower(capturedMMSHex))
	if !ok || text != capturedMMSSubject {
		t.Fatalf("DecodeHexDisplayText = (%q, %v)", text, ok)
	}
}

func TestDecodeHexDisplayTextRejectsPlainTextAndShortHex(t *testing.T) {
	for _, value := range []string{
		"HELLO",
		"AABBCC",
		capturedMMSSubject,
		"000405912143F5",
	} {
		if text, ok := DecodeHexDisplayText(value); ok {
			t.Fatalf("DecodeHexDisplayText(%q) = %q, want reject", value, text)
		}
	}
}

func TestDecodeDisplayTextWellKnownMMSContentType(t *testing.T) {
	payload, err := hex.DecodeString(capturedMMSHex)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the 34-byte string Content-Type + X-Wap-Application-Id with the
	// well-known short form 0xBE (application/vnd.wap.mms-message) and 0xAF 0x84.
	rewritten := []byte{payload[0], payload[1], 0x03, 0xbe, 0xaf, 0x84}
	rewritten = append(rewritten, payload[37:]...)
	text, ok := DecodeDisplayText(rewritten)
	if !ok || text != capturedMMSSubject {
		t.Fatalf("well-known content-type = (%q, %v)", text, ok)
	}
}

func TestDecodeDisplayTextTruncatedBeforeSubjectIsRejected(t *testing.T) {
	payload, err := hex.DecodeString(capturedMMSHex)
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := DecodeDisplayText(payload[:20]); ok {
		t.Fatalf("truncated payload decoded as %q", text)
	}
}

func TestDecodeDisplayTextUsesSubjectFromPartialMMSBody(t *testing.T) {
	payload, err := hex.DecodeString(capturedMMSHex)
	if err != nil {
		t.Fatal(err)
	}
	// First concat segment typically carries the WSP headers and the Subject
	// while Content-Location spills into segment 2.
	text, ok := DecodeDisplayText(payload[:160])
	if !ok || text != capturedMMSSubject {
		t.Fatalf("partial MMS = (%q, %v)", text, ok)
	}
}

func TestDecodeDisplayTextEmptySubjectNotification(t *testing.T) {
	// TID, Push, headersLen=3, well-known MMS type, X-Wap-Application-Id=mms.ua,
	// then m-notification-ind with no Subject.
	payload := []byte{0x01, 0x06, 0x03, 0xbe, 0xaf, 0x84, mmsMessageType, mmsNotificationInd}
	text, ok := DecodeDisplayText(payload)
	if !ok || text != "MMS" {
		t.Fatalf("empty subject = (%q, %v)", text, ok)
	}
}

func TestDecodeServiceIndicationXML(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><si><indication href="http://example.com">You have 1 new alert</indication></si>`)
	payload := encodeWSPPush("text/vnd.wap.si", body)
	text, ok := DecodeDisplayText(payload)
	if !ok || text != "You have 1 new alert" {
		t.Fatalf("XML SI = (%q, %v)", text, ok)
	}
}

func TestDecodeServiceIndicationWBXML(t *testing.T) {
	// WBXML SI: version 1.2, public ID 5, UTF-8, empty string table, inline
	// indication text "Bank OTP 123456".
	body := []byte{
		0x02, 0x05, 0x6a, 0x00,
		0x45, 0xc6, 0x08, 0x01,
		0x03, 'B', 'a', 'n', 'k', ' ', 'O', 'T', 'P', ' ', '1', '2', '3', '4', '5', '6', 0x00,
		0x01, 0x01,
	}
	payload := encodeWSPPush("application/vnd.wap.sic", body)
	text, ok := DecodeDisplayText(payload)
	if !ok || text != "Bank OTP 123456" {
		t.Fatalf("WBXML SI = (%q, %v)", text, ok)
	}
}

func TestDecodeDisplayTextIgnoresUnrelatedBinary(t *testing.T) {
	if text, ok := DecodeDisplayText([]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}); ok {
		t.Fatalf("binary payload decoded as %q", text)
	}
}

func encodeWSPPush(contentType string, body []byte) []byte {
	headers := append(append([]byte(nil), contentType...), 0)
	payload := []byte{0x10, wspPDUPush, byte(len(headers))}
	payload = append(payload, headers...)
	return append(payload, body...)
}
