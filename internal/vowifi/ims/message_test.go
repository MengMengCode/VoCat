package ims

import (
	"bytes"
	"reflect"
	"testing"
)

func TestParseSIPResponseAndSplitIdentityHeaders(t *testing.T) {
	packet := []byte(
		"SIP/2.0 200 OK\r\n" +
			"Call-ID: register@example\r\n" +
			"CSeq: 2 REGISTER\r\n" +
			"P-Associated-URI: <sip:001010123456789@ims.example>,\r\n" +
			" <tel:+8613800138000>\r\n" +
			"Service-Route: <sip:first.example;lr>\r\n" +
			"Service-Route: <sip:second.example;lr>\r\n" +
			"Content-Length: 4\r\n\r\nbody",
	)
	response, err := parseSIPResponse(packet)
	if err != nil {
		t.Fatalf("parseSIPResponse() error = %v", err)
	}
	if response.StatusCode != 200 || string(response.Body) != "body" {
		t.Fatalf("unexpected response: %#v", response)
	}
	headerEnd := bytes.Index(packet, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		t.Fatal("test SIP response has no header delimiter")
	}
	wantRawHeaders := packet[:headerEnd+len("\r\n\r\n")]
	if string(response.rawHeaders) != string(wantRawHeaders) {
		t.Fatalf("raw headers changed: got %q, want %q", response.rawHeaders, wantRawHeaders)
	}
	identities := splitHeaderValues(response.values("P-Associated-URI"))
	wantIdentities := []string{
		"<sip:001010123456789@ims.example>",
		"<tel:+8613800138000>",
	}
	if !reflect.DeepEqual(identities, wantIdentities) {
		t.Fatalf("identities = %#v, want %#v", identities, wantIdentities)
	}
	wantRoutes := []string{"<sip:first.example;lr>", "<sip:second.example;lr>"}
	if routes := splitHeaderValues(response.values("Service-Route")); !reflect.DeepEqual(routes, wantRoutes) {
		t.Fatalf("routes = %#v, want %#v", routes, wantRoutes)
	}
}

func TestParseSIPResponseRejectsIncompleteBody(t *testing.T) {
	_, err := parseSIPResponse([]byte("SIP/2.0 200 OK\r\nContent-Length: 4\r\n\r\nx"))
	if err == nil {
		t.Fatal("parseSIPResponse() error = nil, want incomplete body error")
	}
}

func TestParseSIPMessageRequestWithBinaryBody(t *testing.T) {
	body := []byte{0x01, 0x2a, 0x00, 0x00}
	packet := append([]byte(
		"MESSAGE sip:user@example.test SIP/2.0\r\n"+
			"Via: SIP/2.0/TCP proxy.example.test;branch=z9hG4bK1\r\n"+
			"From: <sip:gateway@example.test>;tag=a\r\n"+
			"To: <sip:user@example.test>\r\n"+
			"Call-ID: inbound-1\r\n"+
			"CSeq: 1 MESSAGE\r\n"+
			"Content-Type: application/vnd.3gpp.sms\r\n"+
			"Content-Length: 4\r\n\r\n",
	), body...)
	message, err := parseSIPPacket(packet)
	if err != nil {
		t.Fatalf("parseSIPPacket: %v", err)
	}
	if message.Request == nil || message.Request.Method != "MESSAGE" ||
		message.Request.URI != "sip:user@example.test" ||
		!reflect.DeepEqual(message.Request.Body, body) {
		t.Fatalf("message = %#v", message)
	}
	headerEnd := bytes.Index(packet, []byte("\r\n\r\n"))
	if headerEnd < 0 || string(message.Request.rawHeaders) != string(packet[:headerEnd+len("\r\n\r\n")]) {
		t.Fatalf("raw request headers changed: got %q", message.Request.rawHeaders)
	}
}

func TestSplitHeaderValuesPreservesQuotedCommas(t *testing.T) {
	values := splitHeaderValues([]string{
		`"Doe, Jane" <sip:jane@example.test>, <sip:john@example.test>`,
	})
	want := []string{
		`"Doe, Jane" <sip:jane@example.test>`,
		`<sip:john@example.test>`,
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("splitHeaderValues() = %#v, want %#v", values, want)
	}
}
