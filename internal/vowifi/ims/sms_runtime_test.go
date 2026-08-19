package ims

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

type smsTestAKA struct{ *recordingAKA }

func (smsTestAKA) ReadSMSCenter(context.Context, string) (string, error) {
	return "+447785016005", nil
}

func TestSessionReceivesAndAcknowledgesSMSOverIMS(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))

	received := make(chan ReceivedSMS, 1)
	serverDone := make(chan error, 1)
	readyForClose := make(chan struct{})
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	go func() { serverDone <- serveInboundSMS(listener, nonce, readyForClose) }()
	provider, err := NewProvider(
		smsTestAKA{&recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}},
		Config{
			PCSCF: listener.LocalAddr().String(), LocalAddress: "127.0.0.1",
			Transport: "udp", TransactionTimeout: 3 * time.Second, SecurityMode: SecurityDisabled,
			OnSMS: func(_ context.Context, message ReceivedSMS) error {
				received <- message
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.Start(context.Background(), vowifi.IMSRequest{
		DeviceID: "ec20",
		Identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true, LocalIPv4: "127.0.0.1", PCSCF: []string{listener.LocalAddr().String()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-received:
		if message.From != "+12345" || message.Text != "HELLO" ||
			message.MessageID != "ims:network-deliver-1:42" ||
			message.ServiceCenterTimestamp == nil || message.Timestamp.IsZero() {
			t.Fatalf("received = %#v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for inbound SMS")
	}
	select {
	case <-readyForClose:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the inbound RP-ACK exchange")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSecurityHeaders(t *testing.T) {
	verify := "ipsec-3gpp;alg=hmac-sha-1-96;prot=esp;mod=trans"
	headers := runtimeSecurityHeaders(true, verify)
	want := []string{
		"Security-Verify: " + verify,
		"Require: sec-agree",
		"Proxy-Require: sec-agree",
	}
	if len(headers) != len(want) {
		t.Fatalf("security header count = %d, want %d", len(headers), len(want))
	}
	for index := range want {
		if headers[index] != want[index] {
			t.Fatalf("security header %d = %q, want %q", index, headers[index], want[index])
		}
	}
	if headers := runtimeSecurityHeaders(false, verify); len(headers) != 0 {
		t.Fatalf("disabled security headers = %#v", headers)
	}
}

func TestExtractSMSPayload(t *testing.T) {
	rpdu := []byte{0x01, 0x2a, 0x00, 0x00, 0x03, 0x04, 0x00, 0x00}
	tests := []struct {
		name        string
		request     *sipRequest
		wantSource  string
		wantPayload []byte
	}{
		{
			name: "direct binary",
			request: &sipRequest{Headers: map[string][]string{
				"content-type":              {smsContentType + "; charset=binary"},
				"content-transfer-encoding": {"binary"},
			}, Body: rpdu},
			wantSource:  smsContentType,
			wantPayload: rpdu,
		},
		{
			name:        "multipart base64",
			request:     multipartSMSRequest(t, rpdu),
			wantSource:  "multipart/mixed",
			wantPayload: rpdu,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, source, err := extractSMSPayload(test.request)
			if err != nil {
				t.Fatalf("extractSMSPayload() error = %v", err)
			}
			if source != test.wantSource || !bytes.Equal(payload, test.wantPayload) {
				t.Fatalf("extractSMSPayload() = (%x, %q), want (%x, %q)",
					payload, source, test.wantPayload, test.wantSource)
			}
		})
	}
}

func TestSupportsSMSContentType(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{smsContentType, true},
		{"Application/Vnd.3gpp.Sms; charset=binary", true},
		{`multipart/mixed; boundary="vodafone-boundary"`, true},
		{"multipart/mixed", false},
		{"text/plain", false},
	} {
		if got := supportsSMSContentType(test.value); got != test.want {
			t.Errorf("supportsSMSContentType(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestSupportsUSSIContentType(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{ussiContentType, true},
		{"Application/Vnd.3gpp.Ussd; charset=binary", true},
		{smsContentType, false},
		{"text/plain", false},
	} {
		if got := supportsUSSIContentType(test.value); got != test.want {
			t.Errorf("supportsUSSIContentType(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestEncodeDecodeUSSDBody(t *testing.T) {
	for _, text := range []string{"*100#", "Main menu 中文"} {
		body, dcs, err := encodeUSSDBody(text)
		if err != nil {
			t.Fatalf("encodeUSSDBody(%q) error = %v", text, err)
		}
		if dcs == nil || *dcs != 0x48 {
			t.Fatalf("encodeUSSDBody(%q) dcs = %v, want 0x48", text, dcs)
		}
		decoded := decodeUSSDBody(body, *dcs)
		if decoded != text {
			t.Fatalf("decodeUSSDBody(%q) = %q, want %q", text, decoded, text)
		}
	}
}

func TestExtractUSSDString(t *testing.T) {
	text := "Main menu"
	encoded, dcs, err := encodeUSSDBody(text)
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte{byte(len(encoded) + 1), byte(*dcs)}, encoded...)
	raw, gotDCS, gotText := extractUSSDString(body)
	if gotText != text || gotDCS == nil || *gotDCS != *dcs || !bytes.Equal(raw, body) {
		t.Fatalf("extractUSSDString(%x) = (%q, %v, %q)", body, raw, gotDCS, gotText)
	}

	// A plain raw body without a length/DCS prefix falls back to DCS 0x0F.
	raw, gotDCS, gotText = extractUSSDString([]byte("fallback"))
	if gotDCS == nil || *gotDCS != 0x0F || gotText != "fallback" || !bytes.Equal(raw, []byte("fallback")) {
		t.Fatalf("extractUSSDString fallback = (%q, %v, %q)", raw, gotDCS, gotText)
	}
}

func TestSMSCenterForIdentityUsesExactPLMN(t *testing.T) {
	config := Config{SMSCenterByPLMN: map[string]string{
		"23410":  "+447802000332",
		"234010": "+447802000332",
		"23415":  "+447785016005",
	}}
	for _, test := range []struct {
		mnc  string
		want string
	}{
		{mnc: "10", want: "+447802000332"},
		{mnc: "010", want: "+447802000332"},
		{mnc: "15", want: "+447785016005"},
		{mnc: "30", want: ""},
	} {
		identity := vowifi.SIMIdentity{HomeMCC: "234", HomeMNC: test.mnc}
		if got := smsCenterForIdentity(config, identity); got != test.want {
			t.Errorf("smsCenterForIdentity(234/%s) = %q, want %q", test.mnc, got, test.want)
		}
	}
}

func TestSMSCenterForIdentityFallsBackToCarrierProfile(t *testing.T) {
	for _, test := range []struct {
		mnc  string
		want string
	}{
		{mnc: "10", want: "+447802000332"},
		{mnc: "15", want: "+447785016005"},
		{mnc: "30", want: ""},
	} {
		identity := vowifi.SIMIdentity{HomeMCC: "234", HomeMNC: test.mnc}
		if got := smsCenterForIdentity(Config{}, identity); got != test.want {
			t.Errorf("profile SMSC for 234/%s = %q, want %q", test.mnc, got, test.want)
		}
	}
}

func multipartSMSRequest(t *testing.T, payload []byte) *sipRequest {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary("vodafone-boundary"); err != nil {
		t.Fatal(err)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", smsContentType)
	header.Set("Content-Transfer-Encoding", "base64")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte(base64.StdEncoding.EncodeToString(payload))); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &sipRequest{
		Headers: map[string][]string{
			"content-type": {`multipart/mixed; boundary="vodafone-boundary"`},
		},
		Body: body.Bytes(),
	}
}

func TestSessionSendsSMSOverIMS(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))
	serverDone := make(chan error, 1)
	readyForClose := make(chan struct{})
	statusReceived := make(chan ReceivedSMSStatus, 1)
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	go func() { serverDone <- serveOutboundSMS(listener, nonce, readyForClose) }()
	provider, err := NewProvider(
		smsTestAKA{&recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}},
		Config{
			PCSCF: listener.LocalAddr().String(), LocalAddress: "127.0.0.1",
			Transport: "udp", TransactionTimeout: 3 * time.Second, SecurityMode: SecurityDisabled,
			OnSMSStatus: func(_ context.Context, status ReceivedSMSStatus) error {
				statusReceived <- status
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.Start(context.Background(), vowifi.IMSRequest{
		DeviceID: "ec20",
		Identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true, LocalIPv4: "127.0.0.1", PCSCF: []string{listener.LocalAddr().String()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.(vowifi.SMSSender).SendSMS(context.Background(), vowifi.SMSSubmitRequest{
		Recipient: "+12345", Text: "HELLO",
	})
	if err != nil || !result.AllPartsAccepted || result.PartsAccepted != 1 || result.PartResults[0].SIPCode != 202 {
		t.Fatalf("SendSMS = (%#v, %v)", result, err)
	}
	select {
	case status := <-statusReceived:
		if status.To != "+12345" || status.MessageReference != result.PartResults[0].Reference ||
			status.StatusCode != 0 || status.DeliveryStatus != "delivered" ||
			status.ServiceCenterTimestamp == nil || status.DischargeTimestamp == nil {
			t.Fatalf("SMS status = %#v", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SMS delivery status")
	}
	select {
	case <-readyForClose:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the status-report RP-ACK exchange")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func serveInboundSMS(listener *net.UDPConn, nonce string, readyForClose chan<- struct{}) error {
	packet := make([]byte, 65535)
	count, remote, err := listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err := parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	callID := headers["call-id"]
	if _, err = listener.WriteToUDP(testResponse(401, "Unauthorized", callID, headers["cseq"], []string{
		`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` + nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
	}), remote); err != nil {
		return err
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if _, err = listener.WriteToUDP(testResponse(200, "OK", callID, headers["cseq"], []string{
		"Contact: " + headers["contact"] + ";expires=600",
	}), remote); err != nil {
		return err
	}

	tpdu := []byte{
		0x04, 0x05, 0x91, 0x21, 0x43, 0xf5, 0x00, 0x00,
		0x42, 0x10, 0x20, 0x30, 0x40, 0x50, 0x00, 0x05,
		0xc8, 0x22, 0x93, 0xf9, 0x04,
	}
	rpdu := []byte{0x01, 0x2a, 0x00, 0x00, byte(len(tpdu))}
	rpdu = append(rpdu, tpdu...)
	var messageBody bytes.Buffer
	mimeWriter := multipart.NewWriter(&messageBody)
	if err = mimeWriter.SetBoundary("vodafone-delivery"); err != nil {
		return err
	}
	mimeHeader := make(textproto.MIMEHeader)
	mimeHeader.Set("Content-Type", smsContentType)
	mimeHeader.Set("Content-Transfer-Encoding", "binary")
	mimePart, createErr := mimeWriter.CreatePart(mimeHeader)
	if createErr != nil {
		return createErr
	}
	if _, err = mimePart.Write(rpdu); err != nil {
		return err
	}
	if err = mimeWriter.Close(); err != nil {
		return err
	}
	request := []byte(strings.Join([]string{
		"MESSAGE sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org SIP/2.0",
		"Via: SIP/2.0/UDP " + listener.LocalAddr().String() + ";branch=z9hG4bKdeliver",
		"From: <sip:ipsmgw@example.test>;tag=gw",
		"To: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>",
		"P-Asserted-Identity: <sip:ipsmgw@example.test>",
		"Call-ID: network-deliver-1",
		"CSeq: 1 MESSAGE",
		`Content-Type: multipart/mixed; boundary="vodafone-delivery"`,
		fmt.Sprintf("Content-Length: %d", messageBody.Len()), "", "",
	}, "\r\n"))
	request = append(request, messageBody.Bytes()...)
	if _, err = listener.WriteToUDP(request, remote); err != nil {
		return err
	}

	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	response, err := parseSIPResponse(packet[:count])
	if err != nil || response.StatusCode != 200 {
		return fmt.Errorf("delivery SIP response = (%#v, %v)", response, err)
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	report, err := parseSIPPacket(packet[:count])
	if err != nil || report.Request == nil {
		return fmt.Errorf("delivery report parse: %v", err)
	}
	if report.Request.Method != "MESSAGE" || report.Request.value("In-Reply-To") != "network-deliver-1" ||
		len(report.Request.Body) != 2 || report.Request.Body[0] != 0x02 || report.Request.Body[1] != 0x2a {
		return fmt.Errorf("unexpected delivery report %#v", report.Request)
	}
	if _, err = listener.WriteToUDP(testResponse(200, "OK", report.Request.value("Call-ID"), report.Request.value("CSeq"), nil), remote); err != nil {
		return err
	}
	close(readyForClose)

	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if headers["expires"] != "0" {
		return errors.New("expected deregistration")
	}
	_, err = listener.WriteToUDP(testResponse(200, "OK", callID, headers["cseq"], nil), remote)
	return err
}

func serveOutboundSMS(listener *net.UDPConn, nonce string, readyForClose chan<- struct{}) error {
	packet := make([]byte, 65535)
	count, remote, err := listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err := parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	registerCallID := headers["call-id"]
	if _, err = listener.WriteToUDP(testResponse(401, "Unauthorized", registerCallID, headers["cseq"], []string{
		`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` + nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
	}), remote); err != nil {
		return err
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if _, err = listener.WriteToUDP(testResponse(200, "OK", registerCallID, headers["cseq"], []string{
		"Contact: " + headers["contact"] + ";expires=600",
	}), remote); err != nil {
		return err
	}

	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	firstMessage := append([]byte(nil), packet[:count]...)
	firstRemote := remote.String()
	// Exercise the RFC SIP/UDP non-INVITE transaction retransmission path by
	// deliberately dropping the first MESSAGE request.
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	if remote.String() != firstRemote || !bytes.Equal(packet[:count], firstMessage) {
		return errors.New("outbound MESSAGE retransmission changed transaction bytes or source")
	}
	message, err := parseSIPPacket(packet[:count])
	if err != nil || message.Request == nil {
		return fmt.Errorf("outbound MESSAGE parse: %v", err)
	}
	if message.Request.Method != "MESSAGE" || message.Request.URI != "tel:+447785016005" ||
		strings.ToLower(message.Request.value("Content-Type")) != smsContentType ||
		message.Request.value("Request-Disposition") != "no-fork" ||
		message.Request.value("Allow") != "MESSAGE" {
		return fmt.Errorf("unexpected outbound MESSAGE %#v", message.Request)
	}
	rpdu, err := parseRPDU(message.Request.Body)
	if err != nil || rpdu.messageType != 0 || len(rpdu.tpdu) != 0 {
		// parseRPDU intentionally decodes only network-to-MS RP-DATA; inspect
		// the mandatory MO prefix and TPDU length directly below.
		if err != nil {
			return err
		}
	}
	body := message.Request.Body
	if len(body) < 8 || body[0] != 0x00 || body[2] != 0x00 {
		return fmt.Errorf("invalid MO RP-DATA %x", body)
	}
	destinationLength := int(body[3])
	userLengthIndex := 4 + destinationLength
	if userLengthIndex >= len(body) || int(body[userLengthIndex]) != len(body)-userLengthIndex-1 {
		return fmt.Errorf("invalid MO RP-DATA lengths %x", body)
	}
	tpdu := body[userLengthIndex+1:]
	if len(tpdu) < 2 || tpdu[0]&0x03 != 1 || tpdu[0]&0x20 == 0 || tpdu[1] != body[1] {
		return fmt.Errorf("SMS-SUBMIT did not request a trackable status report: %x", tpdu)
	}
	if _, err = listener.WriteToUDP(testResponse(202, "Accepted", message.Request.value("Call-ID"), message.Request.value("CSeq"), nil), remote); err != nil {
		return err
	}

	statusTPDU := []byte{
		0x02, tpdu[1], 0x05, 0x91, 0x21, 0x43, 0xf5,
		0x42, 0x10, 0x20, 0x30, 0x40, 0x50, 0x00,
		0x42, 0x10, 0x20, 0x30, 0x50, 0x50, 0x00,
		0x00,
	}
	statusRPDU := []byte{0x01, 0x2b, 0x00, 0x00, byte(len(statusTPDU))}
	statusRPDU = append(statusRPDU, statusTPDU...)
	statusRequest := []byte(strings.Join([]string{
		"MESSAGE sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org SIP/2.0",
		"Via: SIP/2.0/UDP " + listener.LocalAddr().String() + ";branch=z9hG4bKstatus",
		"From: <sip:ipsmgw@example.test>;tag=gw",
		"To: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>",
		"P-Asserted-Identity: <sip:ipsmgw@example.test>",
		"Call-ID: network-status-1",
		"CSeq: 2 MESSAGE",
		"Content-Type: application/vnd.3gpp.sms",
		fmt.Sprintf("Content-Length: %d", len(statusRPDU)), "", "",
	}, "\r\n"))
	statusRequest = append(statusRequest, statusRPDU...)
	if _, err = listener.WriteToUDP(statusRequest, remote); err != nil {
		return err
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	statusResponse, err := parseSIPResponse(packet[:count])
	if err != nil || statusResponse.StatusCode != 200 {
		return fmt.Errorf("status SIP response = (%#v, %v)", statusResponse, err)
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	statusACK, err := parseSIPPacket(packet[:count])
	if err != nil || statusACK.Request == nil || statusACK.Request.value("In-Reply-To") != "network-status-1" ||
		len(statusACK.Request.Body) != 2 || statusACK.Request.Body[0] != 0x02 || statusACK.Request.Body[1] != 0x2b {
		return fmt.Errorf("unexpected status RP-ACK %#v (%v)", statusACK.Request, err)
	}
	if _, err = listener.WriteToUDP(testResponse(200, "OK", statusACK.Request.value("Call-ID"), statusACK.Request.value("CSeq"), nil), remote); err != nil {
		return err
	}
	close(readyForClose)

	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if headers["expires"] != "0" {
		return errors.New("expected deregistration")
	}
	_, err = listener.WriteToUDP(testResponse(200, "OK", registerCallID, headers["cseq"], nil), remote)
	return err
}

func TestSessionReceivesUSSIOverIMS(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))

	received := make(chan ReceivedUSSD, 1)
	serverDone := make(chan error, 1)
	readyForClose := make(chan struct{})
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	go func() { serverDone <- serveInboundUSSI(listener, nonce, readyForClose) }()
	provider, err := NewProvider(
		smsTestAKA{&recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}},
		Config{
			PCSCF: listener.LocalAddr().String(), LocalAddress: "127.0.0.1",
			Transport: "udp", TransactionTimeout: 3 * time.Second, SecurityMode: SecurityDisabled,
			OnUSSD: func(_ context.Context, message ReceivedUSSD) error {
				received <- message
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.Start(context.Background(), vowifi.IMSRequest{
		DeviceID: "ec20",
		Identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true, LocalIPv4: "127.0.0.1", PCSCF: []string{listener.LocalAddr().String()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-received:
		if message.Text != "Main menu" || message.From != "sip:ussd@example.test" || message.CallID != "network-ussd-1" {
			t.Fatalf("received = %#v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for inbound USSI")
	}
	select {
	case <-readyForClose:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for USSI MESSAGE acceptance")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func serveInboundUSSI(listener *net.UDPConn, nonce string, readyForClose chan<- struct{}) error {
	packet := make([]byte, 65535)
	count, remote, err := listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err := parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	callID := headers["call-id"]
	if _, err = listener.WriteToUDP(testResponse(401, "Unauthorized", callID, headers["cseq"], []string{
		`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` + nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
	}), remote); err != nil {
		return err
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if _, err = listener.WriteToUDP(testResponse(200, "OK", callID, headers["cseq"], []string{
		"Contact: " + headers["contact"] + ";expires=600",
	}), remote); err != nil {
		return err
	}

	body := buildUSSDBody("Main menu")
	request := []byte(strings.Join([]string{
		"MESSAGE sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org SIP/2.0",
		"Via: SIP/2.0/UDP " + listener.LocalAddr().String() + ";branch=z9hG4bKussd",
		"From: <sip:ussd@example.test>;tag=gw",
		"To: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>",
		"P-Asserted-Identity: <sip:ussd@example.test>",
		"Call-ID: network-ussd-1",
		"CSeq: 1 MESSAGE",
		"Content-Type: application/vnd.3gpp.ussd",
		"Content-Transfer-Encoding: binary",
		fmt.Sprintf("Content-Length: %d", len(body)), "", "",
	}, "\r\n"))
	request = append(request, body...)
	if _, err = listener.WriteToUDP(request, remote); err != nil {
		return err
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	response, err := parseSIPResponse(packet[:count])
	if err != nil || response.StatusCode != 200 {
		return fmt.Errorf("USSI MESSAGE response = (%#v, %v)", response, err)
	}
	close(readyForClose)

	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if headers["expires"] != "0" {
		return errors.New("expected deregistration")
	}
	_, err = listener.WriteToUDP(testResponse(200, "OK", callID, headers["cseq"], nil), remote)
	return err
}

func TestSessionReceivesMalformedSMSBestEffort(t *testing.T) {
	request := &sipRequest{
		Headers: map[string][]string{
			"content-type":              {smsContentType},
			"content-transfer-encoding": {"binary"},
			"call-id":                   {"malformed-test"},
			"p-asserted-identity":       {"<sip:ipsmgw@example.test>"},
		},
		Body: []byte{0x01, 0x2a, 0x00, 0x00, 0x03, 0xff, 0xff, 0xff},
	}

	received := make(chan ReceivedSMS, 1)
	session := &Session{
		provider: &Provider{config: Config{
			Logger: slog.Default(),
			OnSMS: func(_ context.Context, message ReceivedSMS) error {
				received <- message
				return nil
			},
		}},
		request:         vowifi.IMSRequest{DeviceID: "ec20", Identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"}},
		conn:            &fakeConn{},
		transactions:    make(map[sipTransactionKey]chan *sipResponse),
		fromTag:         "tag",
		nextRPReference: 1,
	}

	session.processSMSMessage(request)

	select {
	case message := <-received:
		if message.DecodeError == "" {
			t.Fatal("expected DecodeError to be set")
		}
		if message.RawRPDU == "" || message.RawTPDU == "" {
			t.Fatalf("expected raw payloads to be preserved, got %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for best-effort SMS callback")
	}
}

func TestSessionAllowsSMSWithoutContactConfirmationWhenProfilePermits(t *testing.T) {
	session := &Session{
		provider: &Provider{config: Config{Logger: slog.Default()}},
		request: vowifi.IMSRequest{
			Identity: vowifi.SIMIdentity{HomeMCC: "515", HomeMNC: "66"},
		},
		evidence: vowifi.IMSEvidence{
			Registered:        true,
			RegistrationState: "registered",
		},
		expiresAt: time.Now().Add(time.Hour),
	}

	evidence, err := session.EnableSMS(context.Background())
	if err != nil || !evidence.Ready {
		t.Fatalf("EnableSMS() = (%#v, %v), want ready for DITO profile", evidence, err)
	}
}

func TestSessionRequiresSMSContactConfirmationByDefault(t *testing.T) {
	session := &Session{
		provider: &Provider{config: Config{Logger: slog.Default()}},
		request: vowifi.IMSRequest{
			Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"},
		},
		evidence: vowifi.IMSEvidence{
			Registered:        true,
			RegistrationState: "registered",
		},
		expiresAt: time.Now().Add(time.Hour),
	}

	evidence, err := session.EnableSMS(context.Background())
	if !errors.Is(err, ErrSMSCapabilityNotConfirmed) || evidence.Ready {
		t.Fatalf("EnableSMS() = (%#v, %v), want not-ready", evidence, err)
	}
}

func buildUSSDBody(text string) []byte {
	encoded, dcs, err := encodeUSSDBody(text)
	if err != nil {
		panic(err)
	}
	return append([]byte{byte(len(encoded) + 1), byte(*dcs)}, encoded...)
}

func TestSessionSendsUSSIOverIMS(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))
	serverDone := make(chan error, 1)
	readyForClose := make(chan struct{})
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	go func() { serverDone <- serveOutboundUSSI(listener, nonce, readyForClose) }()
	provider, err := NewProvider(
		smsTestAKA{&recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}},
		Config{
			PCSCF: listener.LocalAddr().String(), LocalAddress: "127.0.0.1",
			Transport: "udp", TransactionTimeout: 3 * time.Second, SecurityMode: SecurityDisabled,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.Start(context.Background(), vowifi.IMSRequest{
		DeviceID: "ec20",
		Identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true, LocalIPv4: "127.0.0.1", PCSCF: []string{listener.LocalAddr().String()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.(vowifi.USSISender).SendUSSI(context.Background(), vowifi.USSISubmitRequest{Code: "*100#"})
	if err != nil {
		t.Fatalf("SendUSSI error = %v", err)
	}
	if result.Status != "final" || result.Text != "Reply" || result.SIPCode != 200 {
		t.Fatalf("SendUSSI result = %#v", result)
	}
	select {
	case <-readyForClose:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for USSI transaction to complete")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func serveOutboundUSSI(listener *net.UDPConn, nonce string, readyForClose chan<- struct{}) error {
	packet := make([]byte, 65535)
	count, remote, err := listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err := parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	registerCallID := headers["call-id"]
	if _, err = listener.WriteToUDP(testResponse(401, "Unauthorized", registerCallID, headers["cseq"], []string{
		`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` + nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
	}), remote); err != nil {
		return err
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if _, err = listener.WriteToUDP(testResponse(200, "OK", registerCallID, headers["cseq"], []string{
		"Contact: " + headers["contact"] + ";expires=600",
	}), remote); err != nil {
		return err
	}

	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	message, err := parseSIPPacket(packet[:count])
	if err != nil || message.Request == nil {
		return fmt.Errorf("outbound MESSAGE parse: %v", err)
	}
	if message.Request.Method != "MESSAGE" ||
		!strings.HasPrefix(message.Request.URI, "sip:") ||
		strings.ToLower(message.Request.value("Content-Type")) != ussiContentType ||
		message.Request.value("Request-Disposition") != "no-fork" ||
		message.Request.value("Allow") != "MESSAGE" {
		return fmt.Errorf("unexpected outbound MESSAGE %#v", message.Request)
	}
	_, _, text := extractUSSDString(message.Request.Body)
	if text != "*100#" {
		return fmt.Errorf("USSI text = %q, want *100#", text)
	}
	replyBody := buildUSSDBody("Reply")
	reply := []byte(strings.Join([]string{
		"SIP/2.0 200 OK",
		"Call-ID: " + message.Request.value("Call-ID"),
		"CSeq: " + message.Request.value("CSeq"),
		"Content-Type: application/vnd.3gpp.ussd",
		"Content-Transfer-Encoding: binary",
		fmt.Sprintf("Content-Length: %d", len(replyBody)), "", "",
	}, "\r\n"))
	reply = append(reply, replyBody...)
	if _, err = listener.WriteToUDP(reply, remote); err != nil {
		return err
	}
	close(readyForClose)

	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	_, headers, err = parseTestRequest(packet[:count])
	if err != nil {
		return err
	}
	if headers["expires"] != "0" {
		return errors.New("expected deregistration")
	}
	_, err = listener.WriteToUDP(testResponse(200, "OK", registerCallID, headers["cseq"], nil), remote)
	return err
}

// fakeConn is a minimal net.Conn useful for tests that only need LocalAddr
// to succeed and do not care about the actual SIP MESSAGE delivery report.
type fakeConn struct{}

func (*fakeConn) Read([]byte) (int, error)         { return 0, errors.New("fakeConn: closed") }
func (*fakeConn) Write(source []byte) (int, error) { return len(source), nil }
func (*fakeConn) Close() error                     { return nil }
func (*fakeConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 5060}
}
func (*fakeConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 20), Port: 5060}
}
func (*fakeConn) SetDeadline(time.Time) error      { return nil }
func (*fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakeConn) SetWriteDeadline(time.Time) error { return nil }
