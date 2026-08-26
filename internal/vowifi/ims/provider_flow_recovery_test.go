package ims

import (
	"bufio"
	"context"
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

func TestProtectedReverseSourceIsRejectedDuringSecurityRotation(t *testing.T) {
	session := &Session{
		transport:               "tcp",
		securityActive:          true,
		securityRotationPending: true,
		pcscfIP:                 net.ParseIP("192.0.2.20"),
	}
	if got := session.protectedTCPSourceRejectReason(&net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 15040}); got != "security_rotation_pending" {
		t.Fatalf("protected reverse TCP reject reason = %q, want security_rotation_pending", got)
	}
	session.securityRotationPending = false
	if got := session.protectedTCPSourceRejectReason(&net.TCPAddr{IP: net.ParseIP("192.0.2.21"), Port: 15040}); got != "unexpected_pcscf_ip" {
		t.Fatalf("unexpected reverse TCP reject reason = %q, want unexpected_pcscf_ip", got)
	}
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
		response := testResponse(
			200,
			"OK",
			packet.Request.value("Call-ID"),
			packet.Request.value("CSeq"),
			[]string{"Contact: " + contact + ";expires=600"},
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
		if _, err := connection.Write(testResponse(
			200,
			"OK",
			packet.Request.value("Call-ID"),
			packet.Request.value("CSeq"),
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
