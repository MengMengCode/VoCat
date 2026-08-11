package ims

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

type smsTestAKA struct{ *recordingAKA }

func (smsTestAKA) ReadSMSCenter(context.Context, string) (string, error) {
	return "+447785016005", nil
}

type scriptedSMSConn struct {
	recordingSIPConn
	once           sync.Once
	session        *Session
	report         []byte
	reportResponse chan int
}

func (connection *scriptedSMSConn) Write(value []byte) (int, error) {
	count, err := connection.recordingSIPConn.Write(value)
	if err != nil {
		return count, err
	}
	connection.once.Do(func() {
		packet, parseErr := parseSIPPacket(value)
		if parseErr != nil || packet.Request == nil {
			return
		}
		request := packet.Request
		connection.session.dispatchPacket(sipPacket{Response: &sipResponse{
			StatusCode: 202,
			Headers: map[string][]string{
				"call-id": {request.value("Call-ID")},
				"cseq":    {request.value("CSeq")},
			},
		}}, func([]byte) error { return nil })
		if len(connection.report) == 0 {
			return
		}
		reportRequest := smsTestRequest("submit-report", 1, connection.report)
		reportRequest.Headers["in-reply-to"] = []string{request.value("Call-ID")}
		connection.session.handleSIPRequest(reportRequest, func(response []byte) error {
			parsed, responseErr := parseSIPResponse(response)
			if responseErr == nil {
				connection.reportResponse <- parsed.StatusCode
			}
			return responseErr
		})
	})
	return count, nil
}

func newScriptedSMSSession(report []byte, tr1m time.Duration) (*Session, *scriptedSMSConn) {
	connection := &scriptedSMSConn{
		report:         append([]byte(nil), report...),
		reportResponse: make(chan int, 1),
	}
	provider := &Provider{config: Config{TransactionTimeout: time.Second}}
	session := &Session{
		provider: provider,
		request: vowifi.IMSRequest{
			DeviceID: "ec20",
			Identity: vowifi.SIMIdentity{IMSI: "001010123456789", SMSC: "+447785016005"},
		},
		identity:  identitySet{public: "sip:001010123456789@example.test"},
		endpoint:  pcscfEndpoint{host: "127.0.0.1", port: 5060},
		transport: "tcp", conn: connection, fromTag: "local", cseq: 1,
		transactions: make(map[sipTransactionKey]chan *sipResponse),
		smsSubmit:    make(map[byte]*smsSubmitTransaction), smsTR1M: tr1m,
		smsServer:      make(map[smsServerTransactionKey]*smsServerTransaction),
		refreshContext: context.Background(),
		evidence:       vowifi.IMSEvidence{Registered: true}, smsContactConfirmed: true,
	}
	connection.session = session
	return session, connection
}

func smsTestRequest(callID string, cseq uint32, body []byte) *sipRequest {
	return &sipRequest{
		Method: "MESSAGE",
		URI:    "sip:subscriber@example.test",
		Headers: map[string][]string{
			"via":          {fmt.Sprintf("SIP/2.0/UDP 192.0.2.1;branch=z9hG4bK-%s", callID)},
			"from":         {"<sip:ipsmgw@example.test>;tag=gw"},
			"to":           {"<sip:subscriber@example.test>"},
			"call-id":      {callID},
			"cseq":         {fmt.Sprintf("%d MESSAGE", cseq)},
			"content-type": {smsContentType},
		},
		Body: append([]byte(nil), body...),
	}
}

func TestProtectedUDPResponseUsesUEClientPair(t *testing.T) {
	pcscfServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen on P-CSCF server port: %v", err)
	}
	defer pcscfServer.Close()
	ueClient, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, pcscfServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial from UE client port: %v", err)
	}
	defer ueClient.Close()
	ueServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen on UE server port: %v", err)
	}
	defer ueServer.Close()
	pcscfClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen on P-CSCF client port: %v", err)
	}
	defer pcscfClient.Close()

	session := &Session{
		conn:           ueClient,
		protectedUDP:   ueServer,
		fromTag:        "local",
		securityActive: true,
		securityAgreement: securityAgreement{selected: securityMechanism{
			portClient: pcscfClient.LocalAddr().(*net.UDPAddr).Port,
		}},
	}
	session.receiveDone.Add(1)
	go session.readProtectedUDP()

	request := []byte(strings.Join([]string{
		"OPTIONS sip:subscriber@example.test SIP/2.0",
		"Via: SIP/2.0/UDP " + pcscfClient.LocalAddr().String() + ";branch=z9hG4bK-protected-options",
		"From: <sip:pcscf@example.test>;tag=pcscf",
		"To: <sip:subscriber@example.test>",
		"Call-ID: protected-options",
		"CSeq: 1 OPTIONS",
		"Content-Length: 0",
		"",
		"",
	}, "\r\n"))
	if _, err := pcscfClient.WriteToUDP(request, ueServer.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("send protected OPTIONS: %v", err)
	}

	if err := pcscfServer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set P-CSCF server deadline: %v", err)
	}
	responseBuffer := make([]byte, 65535)
	count, responseSource, err := pcscfServer.ReadFromUDP(responseBuffer)
	if err != nil {
		t.Fatalf("read protected response on P-CSCF server port: %v", err)
	}
	response, err := parseSIPResponse(responseBuffer[:count])
	if err != nil {
		t.Fatalf("parse protected response: %v", err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("protected response status = %d, want 200", response.StatusCode)
	}
	wantSource := ueClient.LocalAddr().(*net.UDPAddr)
	if responseSource.Port != wantSource.Port || !responseSource.IP.Equal(wantSource.IP) {
		t.Fatalf("protected response source = %v, want UE client %v", responseSource, wantSource)
	}

	if err := pcscfClient.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set P-CSCF client deadline: %v", err)
	}
	if count, source, err := pcscfClient.ReadFromUDP(responseBuffer); err == nil {
		t.Fatalf("unexpected %d-byte response on P-CSCF client port from %v", count, source)
	} else if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("check P-CSCF client port: %v", err)
	}

	if err := ueServer.Close(); err != nil {
		t.Fatalf("close UE server port: %v", err)
	}
	session.receiveDone.Wait()
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

func TestSessionWaitsForRPErrorBeforeRejectingSMS(t *testing.T) {
	session, connection := newScriptedSMSSession([]byte{0x05, 0x00, 0x01, 0x15}, time.Second)
	result, err := session.SendSMS(context.Background(), vowifi.SMSSubmitRequest{
		Recipient: "+12345", Text: "HELLO",
	})
	if !errors.Is(err, ErrSMSRejected) {
		t.Fatalf("SendSMS error = %v, want ErrSMSRejected", err)
	}
	if result.PartsAccepted != 0 || result.AllPartsAccepted || result.SubmissionStatus != "rejected" ||
		len(result.PartResults) != 1 || result.PartResults[0].Accepted {
		t.Fatalf("SendSMS result = %#v", result)
	}
	select {
	case status := <-connection.reportResponse:
		if status != 200 {
			t.Fatalf("submit-report SIP status = %d, want 200", status)
		}
	default:
		t.Fatal("submit-report SIP response was not sent")
	}
}

func TestSessionSubmitReportTimeoutIsUnknown(t *testing.T) {
	session, _ := newScriptedSMSSession(nil, 20*time.Millisecond)
	result, err := session.SendSMS(context.Background(), vowifi.SMSSubmitRequest{
		Recipient: "+12345", Text: "HELLO",
	})
	if !errors.Is(err, ErrSMSSubmitReportTimeout) {
		t.Fatalf("SendSMS error = %v, want ErrSMSSubmitReportTimeout", err)
	}
	if result.PartsAccepted != 0 || result.AllPartsAccepted || result.SubmissionStatus != "unknown" ||
		len(result.PartResults) != 1 || result.PartResults[0].SubmissionStatus != "submit_report_unknown" {
		t.Fatalf("SendSMS result = %#v", result)
	}
}

func TestSubmitReportRequiresMatchingInReplyToAndRPReference(t *testing.T) {
	transaction := &smsSubmitTransaction{callID: "outbound-call", reports: make(chan rpMessage, 1)}
	session := &Session{
		fromTag:   "local",
		smsSubmit: map[byte]*smsSubmitTransaction{0x2a: transaction},
		smsServer: make(map[smsServerTransactionKey]*smsServerTransaction),
	}
	tests := []struct {
		name      string
		callID    string
		body      []byte
		inReplyTo string
	}{
		{name: "missing In-Reply-To", callID: "report-missing", body: []byte{0x03, 0x2a}},
		{name: "wrong In-Reply-To", callID: "report-wrong-call", body: []byte{0x03, 0x2a}, inReplyTo: "other-call"},
		{name: "wrong RP reference", callID: "report-wrong-ref", body: []byte{0x03, 0x2b}, inReplyTo: "outbound-call"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := smsTestRequest(test.callID, 1, test.body)
			if test.inReplyTo != "" {
				request.Headers["in-reply-to"] = []string{test.inReplyTo}
			}
			var status int
			session.handleSIPRequest(request, func(response []byte) error {
				parsed, err := parseSIPResponse(response)
				if err == nil {
					status = parsed.StatusCode
				}
				return err
			})
			if status != 488 {
				t.Fatalf("SIP status = %d, want 488", status)
			}
		})
	}
	valid := smsTestRequest("report-valid", 1, []byte{0x03, 0x2a})
	valid.Headers["in-reply-to"] = []string{"outbound-call"}
	var status int
	session.handleSIPRequest(valid, func(response []byte) error {
		parsed, err := parseSIPResponse(response)
		if err == nil {
			status = parsed.StatusCode
		}
		return err
	})
	if status != 200 {
		t.Fatalf("valid submit-report SIP status = %d, want 200", status)
	}
	select {
	case report := <-transaction.reports:
		if report.messageType != 3 || report.reference != 0x2a {
			t.Fatalf("queued report = %#v", report)
		}
	default:
		t.Fatal("matching submit report was not queued")
	}
}

func TestDuplicateInboundSMSOnlyDeliversOnce(t *testing.T) {
	delivered := make(chan ReceivedSMS, 2)
	session := &Session{
		provider: &Provider{config: Config{OnSMS: func(_ context.Context, message ReceivedSMS) error {
			delivered <- message
			return nil
		}}},
		request:   vowifi.IMSRequest{DeviceID: "ec20", Identity: vowifi.SIMIdentity{IMSI: "001010123456789"}},
		fromTag:   "local",
		smsServer: make(map[smsServerTransactionKey]*smsServerTransaction),
	}
	tpdu := []byte{
		0x04, 0x05, 0x91, 0x21, 0x43, 0xf5, 0x00, 0x00,
		0x42, 0x10, 0x20, 0x30, 0x40, 0x50, 0x00, 0x05,
		0xc8, 0x22, 0x93, 0xf9, 0x04,
	}
	rpdu := append([]byte{0x01, 0x2a, 0x00, 0x00, byte(len(tpdu))}, tpdu...)
	request := smsTestRequest("duplicate-delivery", 1, rpdu)
	// Keep the delivery-report target empty so the unit test does not need a
	// transport; the business callback remains the behavior under test.
	request.Headers["from"] = []string{";tag=gw"}
	var responses [][]byte
	var responsesMu sync.Mutex
	respond := func(response []byte) error {
		responsesMu.Lock()
		responses = append(responses, append([]byte(nil), response...))
		responsesMu.Unlock()
		return nil
	}
	start := make(chan struct{})
	var requests sync.WaitGroup
	requests.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			defer requests.Done()
			<-start
			session.handleSIPRequest(request, respond)
		}()
	}
	close(start)
	requests.Wait()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound delivery")
	}
	select {
	case duplicate := <-delivered:
		t.Fatalf("duplicate business delivery = %#v", duplicate)
	case <-time.After(50 * time.Millisecond):
	}
	responsesMu.Lock()
	defer responsesMu.Unlock()
	if len(responses) != 2 || string(responses[0]) != string(responses[1]) {
		t.Fatalf("duplicate SIP responses differ: %q / %q", responses[0], responses[1])
	}
}

func TestDistinctInboundSMSDeliveriesProceedIndependently(t *testing.T) {
	connection := &recordingSIPConn{}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	callbackReferences := make(chan int, 3)
	session := &Session{
		provider: &Provider{config: Config{
			TransactionTimeout: time.Second,
			OnSMS: func(_ context.Context, message ReceivedSMS) error {
				callbackReferences <- message.RPReference
				if message.RPReference == 0x2a {
					close(firstEntered)
					<-releaseFirst
				}
				return nil
			},
		}},
		request: vowifi.IMSRequest{
			DeviceID: "ec20",
			Identity: vowifi.SIMIdentity{IMSI: "001010123456789"},
		},
		identity:     identitySet{public: "sip:001010123456789@example.test", domain: "example.test"},
		endpoint:     pcscfEndpoint{host: "127.0.0.1", port: 5060},
		transport:    "tcp",
		conn:         connection,
		fromTag:      "local",
		cseq:         1,
		transactions: make(map[sipTransactionKey]chan *sipResponse),
		smsServer:    make(map[smsServerTransactionKey]*smsServerTransaction),
	}
	tpdu := []byte{
		0x04, 0x05, 0x91, 0x21, 0x43, 0xf5, 0x00, 0x00,
		0x42, 0x10, 0x20, 0x30, 0x40, 0x50, 0x00, 0x05,
		0xc8, 0x22, 0x93, 0xf9, 0x04,
	}
	requestFor := func(callID string, reference byte) *sipRequest {
		rpdu := append([]byte{0x01, reference, 0x00, 0x00, byte(len(tpdu))}, tpdu...)
		return smsTestRequest(callID, 1, rpdu)
	}
	first := requestFor("concurrent-delivery-first", 0x2a)
	second := requestFor("concurrent-delivery-second", 0x2b)

	session.handleSIPRequest(first, func([]byte) error { return nil })
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first inbound SMS did not enter callback")
	}
	session.handleSIPRequest(second, func([]byte) error { return nil })
	select {
	case reference := <-callbackReferences:
		if reference != 0x2a {
			t.Fatalf("first callback reference = %d, want 42", reference)
		}
	case <-time.After(time.Second):
		t.Fatal("first callback reference was not observed")
	}
	select {
	case reference := <-callbackReferences:
		if reference != 0x2b {
			t.Fatalf("second callback reference = %d, want 43", reference)
		}
	case <-time.After(time.Second):
		t.Fatal("second inbound SMS was blocked behind the first callback")
	}

	// A retransmission of the second SIP server transaction must replay its
	// cached response without delivering the RP-DATA again.
	session.handleSIPRequest(second, func([]byte) error { return nil })
	select {
	case reference := <-callbackReferences:
		t.Fatalf("duplicate SIP transaction redelivered RP reference %d", reference)
	case <-time.After(25 * time.Millisecond):
	}

	secondReport := waitForSMSDeliveryReport(t, connection, 0, 0x2b)
	completeSMSDeliveryReport(session, secondReport)
	close(releaseFirst)
	firstReport := waitForSMSDeliveryReport(t, connection, 1, 0x2a)
	completeSMSDeliveryReport(session, firstReport)

	deadline := time.Now().Add(time.Second)
	for {
		session.transactionsMu.Lock()
		pending := len(session.transactions)
		session.transactionsMu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery-report SIP transactions still pending: %d", pending)
		}
		time.Sleep(time.Millisecond)
	}
	if writes := connection.captured(); len(writes) != 2 {
		t.Fatalf("delivery-report writes = %d, want 2", len(writes))
	}
}

func waitForSMSDeliveryReport(
	t *testing.T,
	connection *recordingSIPConn,
	index int,
	reference byte,
) *sipRequest {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		writes := connection.captured()
		if len(writes) > index {
			packet, err := parseSIPPacket(writes[index])
			if err != nil || packet.Request == nil {
				t.Fatalf("parse delivery-report write %d: %v", index, err)
			}
			want := []byte{0x02, reference}
			if string(packet.Request.Body) != string(want) {
				t.Fatalf("delivery-report body %d = %x, want %x", index, packet.Request.Body, want)
			}
			return packet.Request
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for delivery-report write %d", index)
		}
		time.Sleep(time.Millisecond)
	}
}

func completeSMSDeliveryReport(session *Session, request *sipRequest) {
	session.dispatchPacket(sipPacket{Response: &sipResponse{
		StatusCode: 200,
		Headers: map[string][]string{
			"call-id": {request.value("Call-ID")},
			"cseq":    {request.value("CSeq")},
		},
	}}, func([]byte) error {
		return nil
	})
}

func TestSMSServerTransactionCacheIsBounded(t *testing.T) {
	session := &Session{
		fromTag:   "local",
		smsServer: make(map[smsServerTransactionKey]*smsServerTransaction),
	}
	for index := 0; index < maxSMSServerTransactions+32; index++ {
		request := smsTestRequest(fmt.Sprintf("bounded-%d", index), 1, []byte{0x03, byte(index)})
		session.handleSIPRequest(request, func([]byte) error { return nil })
	}
	session.smsServerMu.Lock()
	defer session.smsServerMu.Unlock()
	if len(session.smsServer) != maxSMSServerTransactions {
		t.Fatalf("SMS server transaction cache size = %d, want %d", len(session.smsServer), maxSMSServerTransactions)
	}
}

func TestParseRPDURejectsTrailingBytes(t *testing.T) {
	if _, err := parseRPDU([]byte{0x03, 0x2a, 0x00}); err == nil {
		t.Fatal("parseRPDU accepted trailing RP-ACK byte")
	}
	if _, err := parseRPDU([]byte{0x01, 0x2a, 0x00, 0x00, 0x01, 0xaa, 0xbb}); err == nil {
		t.Fatal("parseRPDU accepted trailing RP-DATA byte")
	}
}

func TestReservedOrSpareRPMessageTypeReturnsCause97(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		reference byte
	}{
		{name: "reserved MTI", body: []byte{0x06, 0x2a}, reference: 0x2a},
		{name: "non-zero spare bits", body: []byte{0x09, 0x2b}, reference: 0x2b},
		{name: "MS-to-network RP-DATA", body: []byte{0x00, 0x2c, 0x00, 0x00, 0x01, 0xaa}, reference: 0x2c},
		{name: "MS-to-network RP-ACK", body: []byte{0x02, 0x2d}, reference: 0x2d},
		{name: "MS-to-network RP-ERROR", body: []byte{0x04, 0x2e, 0x01, 95}, reference: 0x2e},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := &recordingSIPConn{}
			session := &Session{
				provider:     &Provider{config: Config{TransactionTimeout: 5 * time.Millisecond}},
				identity:     identitySet{public: "sip:subscriber@example.test"},
				endpoint:     pcscfEndpoint{host: "127.0.0.1", port: 5060},
				transport:    "tcp",
				conn:         connection,
				fromTag:      "local",
				cseq:         1,
				transactions: make(map[sipTransactionKey]chan *sipResponse),
			}
			request := smsTestRequest("invalid-rp-message", 1, test.body)
			session.processSMSMessage(request)
			writes := connection.captured()
			if len(writes) != 1 {
				t.Fatalf("delivery-report writes = %d, want 1", len(writes))
			}
			packet, err := parseSIPPacket(writes[0])
			if err != nil || packet.Request == nil {
				t.Fatalf("parse RP-ERROR MESSAGE = (%#v, %v)", packet, err)
			}
			want := []byte{0x04, test.reference, 0x01, 97}
			if string(packet.Request.Body) != string(want) {
				t.Fatalf("RP-ERROR body = %x, want %x", packet.Request.Body, want)
			}
		})
	}
}

func TestEncodeRPAddressRejectsCharactersInsteadOfCleaningThem(t *testing.T) {
	for _, value := range []string{"foo123", "+12-34", "12 34", "++123", "123+"} {
		if _, err := encodeRPAddress(value); !errors.Is(err, ErrSMSCUnavailable) {
			t.Errorf("encodeRPAddress(%q) error = %v, want ErrSMSCUnavailable", value, err)
		}
	}
}

func TestRegistrationExpiryUsesMatchedContact(t *testing.T) {
	response := &sipResponse{Headers: map[string][]string{
		"contact": {
			"<sip:other@192.0.2.2>;expires=0",
			"<sip:subscriber@192.0.2.1>;expires=600",
		},
		"expires": {"900"},
	}}
	contacts := splitHeaderValues(response.values("Contact"))
	matched := contacts[1]
	if got := registrationExpiry(response, matched, time.Minute); got != 10*time.Minute {
		t.Fatalf("registrationExpiry = %v, want 10m", got)
	}
	if got := registrationExpiry(response, "<sip:subscriber@192.0.2.1>", time.Minute); got != 15*time.Minute {
		t.Fatalf("global registrationExpiry = %v, want 15m", got)
	}
}

type emptySMSCenterAKA struct{ *recordingAKA }

func (emptySMSCenterAKA) ReadSMSCenter(context.Context, string) (string, error) { return "", nil }

type changingSMSCenterAKA struct {
	*recordingAKA
	calls int
}

func (reader *changingSMSCenterAKA) ReadSMSCenter(context.Context, string) (string, error) {
	reader.calls++
	if reader.calls == 1 {
		return "+447785016005", nil
	}
	return "", nil
}

func TestEnableSMSRequiresAvailableSMSC(t *testing.T) {
	session := &Session{
		provider:            &Provider{aka: emptySMSCenterAKA{&recordingAKA{}}},
		request:             vowifi.IMSRequest{DeviceID: "ec20"},
		evidence:            vowifi.IMSEvidence{Registered: true},
		smsContactConfirmed: true,
		expiresAt:           time.Now().Add(time.Minute),
	}
	evidence, err := session.EnableSMS(context.Background())
	if !errors.Is(err, ErrSMSCUnavailable) || evidence.Ready {
		t.Fatalf("EnableSMS = (%#v, %v), want not ready ErrSMSCUnavailable", evidence, err)
	}
}

func TestEnableSMSDoesNotCacheModemSMSC(t *testing.T) {
	reader := &changingSMSCenterAKA{recordingAKA: &recordingAKA{}}
	session := &Session{
		provider:            &Provider{aka: reader},
		request:             vowifi.IMSRequest{DeviceID: "ec20"},
		evidence:            vowifi.IMSEvidence{Registered: true},
		smsContactConfirmed: true,
		expiresAt:           time.Now().Add(time.Minute),
	}
	if evidence, err := session.EnableSMS(context.Background()); err != nil || !evidence.Ready {
		t.Fatalf("first EnableSMS = (%#v, %v), want ready", evidence, err)
	}
	if evidence, err := session.EnableSMS(context.Background()); !errors.Is(err, ErrSMSCUnavailable) || evidence.Ready {
		t.Fatalf("second EnableSMS = (%#v, %v), want fresh empty SMSC", evidence, err)
	}
	if session.request.Identity.SMSC != "" {
		t.Fatalf("EnableSMS cached modem SMSC %q", session.request.Identity.SMSC)
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
	if !strings.Contains(headers["allow"], "MESSAGE") {
		return fmt.Errorf("REGISTER Allow omitted MESSAGE: %q", headers["allow"])
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
	request := []byte(strings.Join([]string{
		"MESSAGE sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org SIP/2.0",
		"Via: SIP/2.0/UDP " + listener.LocalAddr().String() + ";branch=z9hG4bKdeliver",
		"From: <sip:ipsmgw@example.test>;tag=gw",
		"To: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>",
		"P-Asserted-Identity: <sip:ipsmgw@example.test>",
		"Call-ID: network-deliver-1",
		"CSeq: 1 MESSAGE",
		"Content-Type: application/vnd.3gpp.sms",
		fmt.Sprintf("Content-Length: %d", len(rpdu)), "", "",
	}, "\r\n"))
	request = append(request, rpdu...)
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
	message, err := parseSIPPacket(packet[:count])
	if err != nil || message.Request == nil {
		return fmt.Errorf("outbound MESSAGE parse: %v", err)
	}
	if message.Request.Method != "MESSAGE" || message.Request.URI != "tel:+447785016005" ||
		strings.ToLower(message.Request.value("Content-Type")) != smsContentType {
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
	submitReport := []byte{0x03, body[1]}
	submitRequest := []byte(strings.Join([]string{
		"MESSAGE sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org SIP/2.0",
		"Via: SIP/2.0/UDP " + listener.LocalAddr().String() + ";branch=z9hG4bKsubmit",
		"From: <sip:ipsmgw@example.test>;tag=gw",
		"To: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>",
		"Call-ID: network-submit-1",
		"CSeq: 2 MESSAGE",
		"In-Reply-To: " + message.Request.value("Call-ID"),
		"Content-Type: application/vnd.3gpp.sms",
		fmt.Sprintf("Content-Length: %d", len(submitReport)), "", "",
	}, "\r\n"))
	submitRequest = append(submitRequest, submitReport...)
	if _, err = listener.WriteToUDP(submitRequest, remote); err != nil {
		return err
	}
	count, remote, err = listener.ReadFromUDP(packet)
	if err != nil {
		return err
	}
	submitResponse, err := parseSIPResponse(packet[:count])
	if err != nil || submitResponse.StatusCode != 200 {
		return fmt.Errorf("submit-report SIP response = (%#v, %v)", submitResponse, err)
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
