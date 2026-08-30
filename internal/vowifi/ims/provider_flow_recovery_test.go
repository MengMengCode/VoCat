package ims

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestProviderRecoversTCPFlowAfterDisconnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	secondRegister := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveTCPFlowRecovery(listener, secondRegister)
	}()

	provider, err := NewProvider(
		&recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}},
		Config{
			PCSCF:              listener.Addr().String(),
			LocalAddress:       "127.0.0.1",
			Transport:          "tcp",
			TransactionTimeout: 2 * time.Second,
			SecurityMode:       SecurityDisabled,
			Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	session, err := provider.Start(context.Background(), vowifi.IMSRequest{
		DeviceID: "tcp-flow-recovery-test",
		Identity: vowifi.SIMIdentity{
			IMSI:    "001010123456789",
			HomeMCC: "001",
			HomeMNC: "01",
		},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true,
			LocalIPv4:   "127.0.0.1",
			PCSCF:       []string{listener.Addr().String()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-secondRegister:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for recovered TCP REGISTER")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if evidence := session.Evidence(); evidence.Registered && evidence.RegistrationState == "registered" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if evidence := session.Evidence(); !evidence.Registered || evidence.RegistrationState != "registered" {
		t.Fatalf("evidence after TCP flow recovery = %#v", evidence)
	}

	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestNonOutboundTCPFlowDisconnectKeepsRuntimeReady(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()

	refreshContext, refreshCancel := context.WithCancel(context.Background())
	defer refreshCancel()
	ready := make(chan struct{})
	close(ready)
	session := &Session{
		provider: &Provider{config: Config{
			SecurityMode: SecurityDisabled,
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		}},
		request: vowifi.IMSRequest{
			DeviceID: "non-outbound-flow-test",
			Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"},
		},
		transport:           "tcp",
		conn:                client,
		reader:              bufio.NewReader(client),
		outboundFlowReady:   ready,
		outboundFlowChanged: make(chan struct{}),
		outboundState:       outboundRegistrationFallback,
		refreshContext:      refreshContext,
		refreshCancel:       refreshCancel,
		runtimeStarted:      true,
		runtimeStates:       make(chan vowifi.IMSRuntimeStateEvent, 1),
		smsContactConfirmed: true,
		evidence: vowifi.IMSEvidence{
			Registered:        true,
			RegistrationState: "registered",
		},
	}

	session.receiveDone.Add(1)
	go session.readOutboundFlow(client, session.reader)
	_ = peer.Close()

	done := make(chan struct{})
	go func() {
		session.receiveDone.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("outbound flow reader did not stop")
	}

	if _, _, _, err := session.currentOutboundFlow(); err == nil {
		t.Fatal("closed c-flow is still installed")
	}
	session.recoveryMu.Lock()
	recoveryRunning := session.recoveryRunning
	session.recoveryMu.Unlock()
	if recoveryRunning {
		t.Fatal("non-outbound c-flow EOF started immediate recovery")
	}
	select {
	case event := <-session.runtimeStates:
		if !event.IMSReady || !event.SMSReady || event.RegistrationState != "flow_detached" {
			t.Fatalf("runtime state after non-outbound c-flow EOF = %#v", event)
		}
	default:
		t.Fatal("c-flow EOF did not publish runtime state")
	}
}

func TestSIPFlowRecoveryExpiresRegistrationInsteadOfRetrying(t *testing.T) {
	refreshContext, refreshCancel := context.WithCancel(context.Background())
	defer refreshCancel()
	failures := make(chan error, 1)
	session := &Session{
		provider: &Provider{config: Config{
			SecurityMode: SecurityDisabled,
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		}},
		transport:      "tcp",
		refreshContext: refreshContext,
		failures:       failures,
		evidence: vowifi.IMSEvidence{
			Registered:        true,
			RegistrationState: "registered",
		},
		expiresAt: time.Now().Add(-time.Second),
	}

	done := make(chan struct{})
	go func() {
		session.recoverSIPFlow()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expired registration recovery did not stop")
	}

	evidence := session.Evidence()
	if evidence.Registered || evidence.RegistrationState != "refresh_failed" {
		t.Fatalf("evidence after expired registration recovery = %#v", evidence)
	}
	select {
	case err := <-failures:
		if !errors.Is(err, ErrRegistrationExpired) {
			t.Fatalf("recovery failure = %v, want ErrRegistrationExpired", err)
		}
	default:
		t.Fatal("expired registration recovery did not publish a failure")
	}
}

func TestSecurityBootstrapAddsEmptyAuthorizationAfterInitialCSeq(t *testing.T) {
	provider := &Provider{config: Config{SecurityMode: SecurityRequired}}
	session := &Session{
		provider:          provider,
		request:           vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"}},
		identity:          identitySet{domain: "ims.mnc001.mcc001.3gppnetwork.org", public: "sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org", user: "001010123456789"},
		transport:         "tcp",
		conn:              &registerRetransmitConn{},
		endpoint:          pcscfEndpoint{host: "192.0.2.20", port: 5060},
		callID:            "recovery-call",
		fromTag:           "recovery-tag",
		instanceID:        "urn:uuid:recovery",
		securityBootstrap: true,
		securityProposal: securityProposal{
			spiClient:  1001,
			spiServer:  1002,
			portClient: 30001,
			portServer: 30002,
		},
	}

	request, err := session.buildRegister(6, 3600, "", "")
	if err != nil {
		t.Fatalf("buildRegister() error = %v", err)
	}
	if !strings.Contains(string(request), "Authorization: Digest ") ||
		!strings.Contains(string(request), "integrity-protected=no") {
		t.Fatalf("recovery REGISTER omitted empty security authorization:\n%s", request)
	}
}

func TestSuccessfulRegistrationRetainsAuthenticationStateForRefresh(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	provider := &Provider{config: Config{
		SecurityMode:       SecurityDisabled,
		RegistrationExpiry: 10 * time.Minute,
	}}
	auth := &authenticationState{
		challenge: digestChallenge{
			Realm:     "ims.example",
			Nonce:     "nonce-1",
			Algorithm: "AKAv1-MD5",
		},
		response: []byte{1, 2, 3, 4},
		cnonce:   "cnonce-1",
	}
	session := &Session{
		provider:   provider,
		request:    vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"}},
		identity:   identitySet{domain: "ims.example", user: "001010123456789", private: "001010123456789"},
		transport:  "tcp",
		conn:       client,
		instanceID: "urn:uuid:refresh-test",
		auth:       auth,
	}
	response := &sipResponse{
		StatusCode: 200,
		Reason:     "OK",
		Headers: map[string][]string{
			"contact": {"<sip:001010123456789@192.0.2.10>;expires=600"},
		},
	}

	if _, err := session.applyRegistrationEvidence(response); err != nil {
		t.Fatalf("applyRegistrationEvidence() error = %v", err)
	}
	if session.auth != auth {
		t.Fatal("successful registration cleared authentication state")
	}
	if got := string(session.auth.response); got != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("authentication response = %v, want [1 2 3 4]", session.auth.response)
	}
}

func TestInboundFlowSourceRemainsAvailableDuringCFlowRotation(t *testing.T) {
	session := &Session{
		transport:            "tcp",
		serverSecurityActive: true,
		pcscfIP:              net.ParseIP("192.0.2.20"),
		securityAgreement: securityAgreement{selected: securityMechanism{
			portClient: 15040,
		}},
	}
	if got := session.inboundTCPSourceRejectReason(&net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 15040}); got != "" {
		t.Fatalf("inbound TCP reject reason = %q, want accepted during c-flow rotation", got)
	}
	if got := session.inboundTCPSourceRejectReason(&net.TCPAddr{IP: net.ParseIP("192.0.2.21"), Port: 15040}); got != "unexpected_pcscf_ip" {
		t.Fatalf("unexpected inbound TCP reject reason = %q, want unexpected_pcscf_ip", got)
	}
	if got := session.inboundTCPSourceRejectReason(&net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 15041}); got != "unexpected_pcscf_port" {
		t.Fatalf("unexpected inbound TCP reject reason = %q, want unexpected_pcscf_port", got)
	}
}

func TestProtectedCFlowReconnectReusesActiveSecurityPorts(t *testing.T) {
	localIP := net.ParseIP("127.0.0.1")
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: localIP})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	clientPort, err := availableProtectedPort(localIP, listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}

	acceptedPort := make(chan int, 1)
	go func() {
		connection, acceptErr := listener.AcceptTCP()
		if acceptErr != nil {
			acceptedPort <- 0
			return
		}
		acceptedPort <- connection.RemoteAddr().(*net.TCPAddr).Port
		_ = connection.Close()
	}()

	session := &Session{
		provider:            &Provider{config: Config{TransactionTimeout: time.Second}},
		transport:           "tcp",
		sipLocalIP:          localIP,
		pcscfIP:             localIP,
		outboundFlowReady:   make(chan struct{}),
		outboundFlowChanged: make(chan struct{}),
		securityProposal: securityProposal{
			portClient: clientPort,
			portServer: clientPort + 1,
		},
		securityAgreement: securityAgreement{selected: securityMechanism{
			portServer: listener.Addr().(*net.TCPAddr).Port,
		}},
		clientSecurityActive: true,
	}
	if err := session.reconnectProtectedSIPFlowLocked(context.Background()); err != nil {
		t.Fatalf("reconnectProtectedSIPFlowLocked() error = %v", err)
	}
	if got := <-acceptedPort; got != clientPort {
		t.Fatalf("protected c-flow source port = %d, want %d", got, clientPort)
	}
	connection, _, _, err := session.currentOutboundFlow()
	if err != nil {
		t.Fatalf("currentOutboundFlow() error = %v", err)
	}
	_ = connection.Close()
}

func TestCandidateFlowSwitchDoesNotInterruptPreviousTransaction(t *testing.T) {
	previous, previousPeer := net.Pipe()
	defer previous.Close()
	defer previousPeer.Close()
	candidate, candidatePeer := net.Pipe()
	defer candidate.Close()
	defer candidatePeer.Close()
	ready := make(chan struct{})
	close(ready)
	changed := make(chan struct{})
	key := sipTransactionKey{branch: "z9hG4bKprevious-flow", method: "REGISTER", connection: previous}
	responses := make(chan *sipResponse, 1)
	session := &Session{
		transport:           "tcp",
		conn:                previous,
		reader:              bufio.NewReader(previous),
		outboundFlowReady:   ready,
		outboundFlowChanged: changed,
		transactions: map[sipTransactionKey]sipTransaction{
			key: {responses: responses},
		},
	}

	replaced, err := session.installOutboundFlowWithReader(candidate, bufio.NewReader(candidate), false)
	if err != nil {
		t.Fatal(err)
	}
	if replaced != previous {
		t.Fatalf("replaced connection = %p, want %p", replaced, previous)
	}
	select {
	case <-changed:
		t.Fatal("candidate switch interrupted the previous flow")
	default:
	}

	response := &sipResponse{
		StatusCode: 200,
		Headers: map[string][]string{
			"via":  {"SIP/2.0/TCP 127.0.0.1:5060;branch=" + key.branch},
			"cseq": {"9 REGISTER"},
		},
	}
	session.dispatchPacketFrom(sipPacket{Response: response}, outboundFlowName, previous, nil)
	select {
	case got := <-responses:
		if got != response {
			t.Fatalf("transaction response = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("previous-flow response was not delivered")
	}
}

func TestFreshSecurityBootstrapPreservesOldSFlowUntilCandidateSucceeds(t *testing.T) {
	localIP := net.ParseIP("127.0.0.1")
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: localIP})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()

	clientHandle := &fakeIPSecHandle{}
	serverHandle := &fakeIPSecHandle{}
	session := &Session{
		provider: &Provider{config: Config{
			SecurityMode:       SecurityRequired,
			TransactionTimeout: time.Second,
		}},
		transport:           "tcp",
		sipLocalIP:          localIP,
		initialEndpoint:     pcscfEndpoint{host: localIP.String(), port: listener.Addr().(*net.TCPAddr).Port},
		endpoint:            pcscfEndpoint{host: localIP.String(), port: 6060},
		outboundFlowReady:   make(chan struct{}),
		outboundFlowChanged: make(chan struct{}),
		securityProposal: securityProposal{
			spiClient:                1001,
			spiServer:                1002,
			portClient:               30001,
			portServer:               30002,
			integrityAlgorithms:      []string{"hmac-sha-1-96"},
			encryptionAlgorithmsList: []string{"aes-cbc"},
		},
		securityAgreement: securityAgreement{selected: securityMechanism{
			portClient: 40001,
			portServer: 40002,
			spiClient:  2001,
			spiServer:  2002,
			algorithm:  "hmac-sha-1-96",
			encryption: "aes-cbc",
			protocol:   "esp",
			mode:       "trans",
		}},
		clientIPSecHandle:    clientHandle,
		serverIPSecHandle:    serverHandle,
		clientSecurityActive: true,
		serverSecurityActive: true,
		securityActive:       true,
	}

	if err := session.prepareSIPFlowRecoveryLocked(context.Background()); err != nil {
		t.Fatalf("prepareSIPFlowRecoveryLocked() error = %v", err)
	}
	peer := <-accepted
	if peer == nil {
		t.Fatal("fresh security bootstrap did not connect to initial P-CSCF")
	}
	defer peer.Close()
	if clientHandle.closes() != 0 || serverHandle.closes() != 0 {
		t.Fatalf("old SA handles closed before candidate success: client=%d server=%d",
			clientHandle.closes(), serverHandle.closes())
	}
	if session.cFlowSecurityActive() {
		t.Fatal("old c-flow remained active during fresh bootstrap")
	}
	if !session.sFlowSecurityActive() {
		t.Fatal("old s-flow was disabled before candidate success")
	}
	connection, _, _, err := session.currentOutboundFlow()
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
}

func serveTCPFlowRecovery(listener net.Listener, secondRegister chan<- struct{}) error {
	for connectionNumber := 0; connectionNumber < 2; connectionNumber++ {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			_ = connection.Close()
			return err
		}
		reader := bufio.NewReader(connection)
		packet, err := readSIPPacket(reader)
		if err != nil {
			_ = connection.Close()
			return err
		}
		if packet.Request == nil || packet.Request.Method != "REGISTER" {
			_ = connection.Close()
			return fmt.Errorf("request on recovered flow = %#v", packet.Request)
		}
		contact := packet.Request.value("Contact")
		if !strings.Contains(contact, "<sip:") {
			_ = connection.Close()
			return fmt.Errorf("REGISTER contact = %q", contact)
		}
		response := testResponseForRequest(
			200,
			"OK",
			packet.Request,
			[]string{
				"Contact: " + contact + ";expires=600",
				"Require: outbound",
			},
		)
		if _, err := connection.Write(response); err != nil {
			_ = connection.Close()
			return err
		}
		if connectionNumber == 0 {
			_ = connection.Close()
			continue
		}
		close(secondRegister)
		// Keep the replacement flow alive long enough for Close to send the
		// normal Expires: 0 deregistration.
		packet, err = readSIPPacket(reader)
		if err != nil {
			_ = connection.Close()
			return err
		}
		if packet.Request == nil || packet.Request.Method != "REGISTER" ||
			packet.Request.value("Expires") != "0" {
			_ = connection.Close()
			return fmt.Errorf("deregistration request = %#v", packet.Request)
		}
		if _, err := connection.Write(testResponseForRequest(
			200,
			"OK",
			packet.Request,
			nil,
		)); err != nil {
			_ = connection.Close()
			return err
		}
		_ = connection.Close()
		return nil
	}
	return fmt.Errorf("TCP flow recovery did not accept a replacement flow")
}
