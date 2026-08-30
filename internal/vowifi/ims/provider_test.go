package ims

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

type evidenceTunnel struct {
	evidence vowifi.TunnelEvidence
}

type immediateTimeoutError struct{}

func (immediateTimeoutError) Error() string   { return "test timeout" }
func (immediateTimeoutError) Timeout() bool   { return true }
func (immediateTimeoutError) Temporary() bool { return true }

type registerRetransmitConn struct {
	writes   int
	response []byte
}

func (connection *registerRetransmitConn) Read(destination []byte) (int, error) {
	if connection.writes < 2 {
		return 0, immediateTimeoutError{}
	}
	return copy(destination, connection.response), nil
}

func (connection *registerRetransmitConn) Write(source []byte) (int, error) {
	connection.writes++
	return len(source), nil
}

func (*registerRetransmitConn) Close() error { return nil }
func (*registerRetransmitConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 5060}
}
func (*registerRetransmitConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 20), Port: 5060}
}
func (*registerRetransmitConn) SetDeadline(time.Time) error      { return nil }
func (*registerRetransmitConn) SetReadDeadline(time.Time) error  { return nil }
func (*registerRetransmitConn) SetWriteDeadline(time.Time) error { return nil }

type refreshResponseConn struct {
	writes   [][]byte
	response []byte
}

func (connection *refreshResponseConn) Read(destination []byte) (int, error) {
	if len(connection.response) == 0 {
		return 0, io.EOF
	}
	response := connection.response
	connection.response = nil
	return copy(destination, response), nil
}

func (connection *refreshResponseConn) Write(source []byte) (int, error) {
	connection.writes = append(connection.writes, append([]byte(nil), source...))
	packet, err := parseSIPPacket(source)
	if err != nil || packet.Request == nil {
		return 0, fmt.Errorf("parse refresh REGISTER: %w", err)
	}
	connection.response = testResponseForRequest(
		200,
		"OK",
		packet.Request,
		[]string{
			"Contact: " + packet.Request.value("Contact") + ";expires=600",
			"Require: outbound",
		},
	)
	return len(source), nil
}

func (*refreshResponseConn) Close() error { return nil }
func (*refreshResponseConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 5060}
}
func (*refreshResponseConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 5060}
}
func (*refreshResponseConn) SetDeadline(time.Time) error      { return nil }
func (*refreshResponseConn) SetReadDeadline(time.Time) error  { return nil }
func (*refreshResponseConn) SetWriteDeadline(time.Time) error { return nil }

type loopbackRefreshResponseConn struct{ refreshResponseConn }

func (*loopbackRefreshResponseConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30001}
}

func (*loopbackRefreshResponseConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 6060}
}

func (tunnel evidenceTunnel) Evidence() vowifi.TunnelEvidence {
	return tunnel.evidence
}

func (evidenceTunnel) Close(context.Context) error {
	return nil
}

func TestTransportForIdentityUsesPLMNOverride(t *testing.T) {
	t.Parallel()
	config := Config{
		Transport: "tcp",
		TransportByPLMN: map[string]string{
			"23410":  "udp",
			"234010": "udp",
		},
	}

	if got := transportForIdentity(config, vowifi.SIMIdentity{HomeMCC: "234", HomeMNC: "10"}); got != "udp" {
		t.Fatalf("PLMN 234-10 transport = %q, want udp", got)
	}
	if got := transportForIdentity(config, vowifi.SIMIdentity{HomeMCC: "234", HomeMNC: "010"}); got != "udp" {
		t.Fatalf("zero-padded PLMN 234-010 transport = %q, want udp", got)
	}
	if got := transportForIdentity(config, vowifi.SIMIdentity{HomeMCC: "234", HomeMNC: "15"}); got != "tcp" {
		t.Fatalf("non-overridden transport = %q, want tcp", got)
	}
}

func TestTransportForIdentityPreservesLeadingZeroMNCs(t *testing.T) {
	t.Parallel()
	config := Config{
		TransportByPLMN: map[string]string{
			"31001":  "udp",
			"310001": "tcp",
			"31000":  "udp",
			"310000": "tcp",
		},
	}

	for _, test := range []struct {
		mnc  string
		want string
	}{
		{mnc: "01", want: "udp"},
		{mnc: "001", want: "tcp"},
		{mnc: "00", want: "udp"},
		{mnc: "000", want: "tcp"},
	} {
		if got := transportForIdentity(config, vowifi.SIMIdentity{HomeMCC: "310", HomeMNC: test.mnc}); got != test.want {
			t.Errorf("PLMN 310-%s transport = %q, want %q", test.mnc, got, test.want)
		}
	}
}

func TestCarrierProfileSuppliesTransportWithoutCodeMap(t *testing.T) {
	t.Parallel()
	identity := vowifi.SIMIdentity{HomeMCC: "999", HomeMNC: "99"}
	if got := transportForIdentity(Config{Transport: "tcp"}, identity); got != "tcp" {
		t.Fatalf("standard transport = %q, want tcp", got)
	}
	if got := transportForIdentity(Config{
		Transport: "udp", TransportByPLMN: map[string]string{"99999": "tcp"},
	}, identity); got != "tcp" {
		t.Fatalf("explicit configuration did not override: %q", got)
	}
}

func TestProviderCachesSuccessfulTransportPerSIM(t *testing.T) {
	t.Parallel()
	provider := &Provider{transportCache: make(map[string]string)}
	first := vowifi.SIMIdentity{ICCID: "8901000000000000001", HomeMCC: "001", HomeMNC: "01"}
	second := vowifi.SIMIdentity{ICCID: "8901000000000000002", HomeMCC: "001", HomeMNC: "01"}
	firstEndpoint := pcscfEndpoint{host: "192.0.2.10", port: 5060}
	secondEndpoint := pcscfEndpoint{host: "192.0.2.11", port: 5060}
	provider.rememberTransport(first, firstEndpoint, "udp")
	if got := provider.cachedTransport(first, firstEndpoint); got != "udp" {
		t.Fatalf("cached first transport = %q", got)
	}
	if got := provider.cachedTransport(second, firstEndpoint); got != "" {
		t.Fatalf("second SIM inherited cached transport %q", got)
	}
	if got := provider.cachedTransport(first, secondEndpoint); got != "" {
		t.Fatalf("second P-CSCF inherited cached transport %q", got)
	}
}

func TestExplicitPCSCFTransportOverridesProfileAndCache(t *testing.T) {
	t.Parallel()
	identity := vowifi.SIMIdentity{ICCID: "8901000000000000001", HomeMCC: "001", HomeMNC: "01"}
	endpoint := pcscfEndpoint{host: "192.0.2.10", port: 5060}
	provider := &Provider{
		config: Config{
			Transport:       "udp",
			TransportByPLMN: map[string]string{"00101": "udp"},
		},
		transportCache: make(map[string]string),
	}
	provider.rememberTransport(identity, endpoint, "udp")
	if got := provider.transportForEndpoint(identity, endpoint, "tcp"); got != "tcp" {
		t.Fatalf("explicit P-CSCF transport = %q, want tcp", got)
	}
}

func TestUDPRegisterRetransmitsBeforeTransactionTimeout(t *testing.T) {
	t.Parallel()
	connection := &registerRetransmitConn{response: []byte(strings.Join([]string{
		"SIP/2.0 200 OK",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bKretransmit",
		"Call-ID: register-retransmit-test",
		"CSeq: 7 REGISTER",
		"Content-Length: 0",
		"",
		"",
	}, "\r\n"))}
	session := &Session{
		provider: &Provider{config: Config{
			TransactionTimeout: 3 * time.Second,
			Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		}},
		request:   vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"}},
		transport: "udp",
		conn:      connection,
		callID:    "register-retransmit-test",
	}
	request := []byte(strings.Join([]string{
		"REGISTER sip:ims.example SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bKretransmit",
		"Call-ID: register-retransmit-test",
		"CSeq: 7 REGISTER",
		"Content-Length: 0",
		"",
		"",
	}, "\r\n"))
	response, err := session.exchange(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || connection.writes != 2 {
		t.Fatalf("response=%#v writes=%d, want SIP 200 after one retransmission", response, connection.writes)
	}
}

func TestProtectedUDPRegisterResponseUsesUEClientFlow(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	inbound, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer inbound.Close()

	serverDone := make(chan error, 1)
	go func() {
		packet := make([]byte, 2048)
		count, remote, readErr := server.ReadFromUDP(packet)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		request, parseErr := parseSIPPacket(packet[:count])
		if parseErr != nil || request.Request == nil {
			serverDone <- fmt.Errorf("parse REGISTER = (%#v, %v)", request.Request, parseErr)
			return
		}
		_, writeErr := server.WriteToUDP(testResponse(
			200,
			"OK",
			request.Request.value("Call-ID"),
			request.Request.value("CSeq"),
			[]string{"Via: " + request.Request.value("Via")},
		), remote)
		serverDone <- writeErr
	}()

	request := []byte(strings.Join([]string{
		"REGISTER sip:ims.example SIP/2.0",
		"Via: SIP/2.0/UDP " + client.LocalAddr().String() + ";branch=z9hG4bKflow2;rport",
		"From: <sip:user@ims.example>;tag=flow2",
		"To: <sip:user@ims.example>",
		"Call-ID: protected-udp-flow2",
		"CSeq: 7 REGISTER",
		"Content-Length: 0",
		"",
		"",
	}, "\r\n"))
	session := &Session{
		provider: &Provider{config: Config{
			TransactionTimeout: time.Second,
			Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		}},
		transport:            "udp",
		conn:                 client,
		inboundUDP:           inbound,
		serverSecurityActive: true,
		callID:               "protected-udp-flow2",
	}
	response, err := session.exchangeWithRuntimeOnFlow(context.Background(), request, false, client)
	if err != nil {
		t.Fatalf("exchangeWithRuntimeOnFlow() error = %v", err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("REGISTER response status = %d", response.StatusCode)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeConfigValidatesSMSCentersByPLMN(t *testing.T) {
	config, err := normalizeConfig(Config{SMSCenterByPLMN: map[string]string{
		" 23410 ": " +447802000332 ",
	}})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if got := config.SMSCenterByPLMN["23410"]; got != "+447802000332" {
		t.Fatalf("normalized O2 SMSC = %q", got)
	}
	for _, invalid := range []Config{
		{SMSCenterByPLMN: map[string]string{"234": "+447802000332"}},
		{SMSCenterByPLMN: map[string]string{"23410": "not-a-number"}},
	} {
		if _, err := normalizeConfig(invalid); err == nil {
			t.Fatalf("normalizeConfig(%#v) succeeded", invalid.SMSCenterByPLMN)
		}
	}
}

func TestProviderRegisterAKAParseEvidenceAndClose(t *testing.T) {
	for _, test := range []struct {
		name         string
		confirmSMS   bool
		wantSMSReady bool
	}{
		{name: "registrar confirms SMS feature tag", confirmSMS: true, wantSMSReady: true},
		{name: "registrar omits SMS feature tag", confirmSMS: false, wantSMSReady: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatalf("ListenUDP() error = %v", err)
			}
			defer listener.Close()
			if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatalf("SetDeadline() error = %v", err)
			}

			nonceBytes := make([]byte, 32)
			for index := range nonceBytes {
				nonceBytes[index] = byte(index + 1)
			}
			nonce := base64.StdEncoding.EncodeToString(nonceBytes)
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- serveRegistration(listener, nonce, test.confirmSMS)
			}()

			aka := &recordingAKA{
				result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
			}
			provider, err := NewProvider(aka, Config{
				PCSCF:              listener.LocalAddr().String(),
				LocalAddress:       "127.0.0.1",
				Transport:          "udp",
				TransactionTimeout: 3 * time.Second,
				SecurityMode:       SecurityDisabled,
			})
			if err != nil {
				t.Fatalf("NewProvider() error = %v", err)
			}
			session, err := provider.Start(context.Background(), vowifi.IMSRequest{
				DeviceID: "ec20",
				Identity: vowifi.SIMIdentity{
					ICCID:   "8901000000000000000",
					IMSI:    "001010123456789",
					HomeMCC: "001",
					HomeMNC: "01",
				},
				Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
					Established: true,
					LocalIPv4:   "127.0.0.1",
					PCSCF:       []string{listener.LocalAddr().String()},
				}},
			})
			if err != nil {
				t.Fatalf("Provider.Start() error = %v", err)
			}

			evidence := session.Evidence()
			if !evidence.Registered || evidence.LastSIPCode != 200 ||
				evidence.RegistrationState != "registered" {
				t.Fatalf("evidence = %#v", evidence)
			}
			if len(evidence.AssociatedIdentities) != 2 ||
				len(evidence.PAssociatedURI) != 2 ||
				len(evidence.ServiceRoute) != 1 {
				t.Fatalf("parsed evidence = %#v", evidence)
			}
			if evidence.RegisteredContact == "" {
				t.Fatalf("registered contact was not correlated: %#v", evidence)
			}
			concrete, ok := session.(*Session)
			if !ok {
				t.Fatalf("session type = %T", session)
			}
			if err := concrete.refreshOnce(context.Background()); err != nil {
				t.Fatalf("refreshOnce() error = %v", err)
			}
			evidence = session.Evidence()
			if !evidence.Registered || evidence.RegistrationState != "registered" {
				t.Fatalf("evidence after refresh = %#v", evidence)
			}
			number, source, ok := vowifi.ExtractAssociatedMSISDN(evidence)
			if !ok || number != "+8613800138000" || source != vowifi.PhoneSourcePAssociatedURI {
				t.Fatalf("ExtractAssociatedMSISDN() = (%q, %q, %t)", number, source, ok)
			}
			sms, smsErr := session.EnableSMS(context.Background())
			if test.wantSMSReady {
				if smsErr != nil || !sms.Ready {
					t.Fatalf("EnableSMS() = (%#v, %v), want ready", sms, smsErr)
				}
			} else {
				if !errors.Is(smsErr, ErrSMSCapabilityNotConfirmed) || sms.Ready {
					t.Fatalf("EnableSMS() = (%#v, %v), want strict not-ready", sms, smsErr)
				}
			}
			if err := session.Close(context.Background()); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if session.Evidence().Registered {
				t.Fatal("Evidence().Registered = true after Close")
			}
			if err := <-serverDone; err != nil {
				t.Fatalf("registrar error = %v", err)
			}
			if len(aka.challenges) != 1 {
				t.Fatalf("AKA challenge count = %d, want 1", len(aka.challenges))
			}
		})
	}
}

func TestRefreshTemporary503PreservesRegistrationEvidence(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveRefreshFailure(listener, nonce, 503)
	}()

	provider, err := NewProvider(
		&recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}},
		Config{
			PCSCF:              listener.LocalAddr().String(),
			LocalAddress:       "127.0.0.1",
			Transport:          "udp",
			TransactionTimeout: 3 * time.Second,
			SecurityMode:       SecurityDisabled,
		},
	)
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	session, err := provider.Start(context.Background(), vowifi.IMSRequest{
		Identity: vowifi.SIMIdentity{
			IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01",
		},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true,
			LocalIPv4:   "127.0.0.1",
			PCSCF:       []string{listener.LocalAddr().String()},
		}},
	})
	if err != nil {
		t.Fatalf("Provider.Start() error = %v", err)
	}
	concrete := session.(*Session)
	err = concrete.refreshOnce(context.Background())
	if err == nil {
		t.Fatal("refreshOnce() error = nil, want SIP rejection")
	}
	if status, ok := registrationRejectionStatus(err); !ok || status != 503 {
		t.Fatalf("registrationRejectionStatus() = (%d, %t), want (503, true)", status, ok)
	}
	if retryAfter, ok := registrationRetryAfter(err); ok || retryAfter != 0 {
		t.Fatalf("registrationRetryAfter() = (%s, %t), want (0s, false)", retryAfter, ok)
	}
	evidence := session.Evidence()
	if !evidence.Registered || evidence.RegistrationState != "refresh_retrying" {
		t.Fatalf("evidence after temporary refresh rejection = %#v", evidence)
	}
	if delay := registrationRefreshRetryDelay(err, 0); delay != defaultRegistrationRetry {
		t.Fatalf("registrationRefreshRetryDelay() = %s, want %s", delay, defaultRegistrationRetry)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("registrar error = %v", err)
	}
}

func serveRegistration(listener *net.UDPConn, nonce string, confirmSMS bool) error {
	var callID string
	var pani string
	for step := 0; step < 4; step++ {
		packet := make([]byte, 65535)
		count, remote, err := listener.ReadFromUDP(packet)
		if err != nil {
			return err
		}
		startLine, headers, err := parseTestRequest(packet[:count])
		if err != nil {
			return err
		}
		if !strings.HasPrefix(startLine, "REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0") {
			return fmt.Errorf("unexpected start line %q", startLine)
		}
		for _, forbidden := range []string{
			"p-visited-network-id",
			"p-preferred-identity",
		} {
			if headers[forbidden] != "" {
				return fmt.Errorf(
					"REGISTER unexpectedly included %s: %q",
					forbidden,
					headers[forbidden],
				)
			}
		}
		currentPANI := headers["p-access-network-info"]
		if err := validateTestPANI(currentPANI); err != nil {
			return fmt.Errorf("REGISTER PANI: %w", err)
		}
		if step == 0 {
			pani = currentPANI
		} else if currentPANI != pani {
			return fmt.Errorf("REGISTER PANI changed from %q to %q", pani, currentPANI)
		}
		if !strings.Contains(headers["allow"], "MESSAGE") ||
			!strings.Contains(string(packet[:count]), "Accept-Contact: *;+g.3gpp.smsip") {
			return fmt.Errorf("REGISTER omitted SMS-over-IMS capability: Allow=%q", headers["allow"])
		}
		if step == 0 {
			if headers["authorization"] != "" {
				return errors.New("initial REGISTER unexpectedly authenticated")
			}
			callID = headers["call-id"]
			response := testResponseForHeaders(
				401,
				"Unauthorized",
				headers,
				[]string{
					`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` +
						nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
				},
			)
			if _, err := listener.WriteToUDP(response, remote); err != nil {
				return err
			}
			continue
		}
		if headers["call-id"] != callID {
			return errors.New("Call-ID changed within registration")
		}
		if step == 1 {
			if headers["authorization"] == "" {
				return errors.New("authenticated REGISTER omitted Authorization")
			}
			if err := verifyTestAuthorization(headers["authorization"], nonce); err != nil {
				return err
			}
			contact := headers["contact"]
			extraContacts := []string(nil)
			if !confirmSMS {
				contact = strings.Replace(contact, ";+g.3gpp.smsip", "", 1)
				extraContacts = append(
					extraContacts,
					"Contact: <sip:other@127.0.0.1:5099;transport=udp>;+g.3gpp.smsip;expires=600",
				)
			}
			responseHeaders := []string{
				"P-Associated-URI: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>, <tel:+8613800138000>",
				"Contact: " + contact + ";expires=600",
				"Service-Route: <sip:route.ims.example;lr>",
			}
			responseHeaders = append(responseHeaders, extraContacts...)
			response := testResponseForHeaders(
				200,
				"OK",
				headers,
				responseHeaders,
			)
			if _, err := listener.WriteToUDP(response, remote); err != nil {
				return err
			}
			continue
		}
		if step == 2 {
			if headers["expires"] == "0" {
				return errors.New("refresh REGISTER used zero expiry")
			}
			if headers["authorization"] == "" {
				return errors.New("refresh REGISTER omitted cached Authorization")
			}
			if err := verifyTestAuthorization(headers["authorization"], nonce); err != nil {
				return fmt.Errorf("refresh Authorization: %w", err)
			}
			contact := headers["contact"]
			extraContacts := []string(nil)
			if !confirmSMS {
				contact = strings.Replace(contact, ";+g.3gpp.smsip", "", 1)
				extraContacts = append(
					extraContacts,
					"Contact: <sip:other@127.0.0.1:5099;transport=udp>;+g.3gpp.smsip;expires=600",
				)
			}
			responseHeaders := []string{
				"P-Associated-URI: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>, <tel:+8613800138000>",
				"Contact: " + contact + ";expires=600",
				"Service-Route: <sip:route.ims.example;lr>",
			}
			responseHeaders = append(responseHeaders, extraContacts...)
			response := testResponseForHeaders(
				200,
				"OK",
				headers,
				responseHeaders,
			)
			if _, err := listener.WriteToUDP(response, remote); err != nil {
				return err
			}
			continue
		}
		if headers["expires"] != "0" {
			return fmt.Errorf("deregister Expires = %q, want 0", headers["expires"])
		}
		if _, err := listener.WriteToUDP(
			testResponseForHeaders(200, "OK", headers, nil),
			remote,
		); err != nil {
			return err
		}
	}
	return nil
}

func TestSessionPAccessNetworkInfoIsStableAndUEProvided(t *testing.T) {
	defaultPANI := "IEEE-802.11;i-wlan-node-id=" + defaultPANIWLANNode
	if err := validateTestPANI(defaultPANI); err != nil {
		t.Fatal(err)
	}
	if got := ueProvidedPANI(" IEEE-802.11;i-wlan-node-id=aabbccddeeff;network-provided "); got != "IEEE-802.11;i-wlan-node-id=aabbccddeeff" {
		t.Fatalf("ueProvidedPANI() = %q", got)
	}
	if got := ueProvidedPANI("network-provided"); got != "" {
		t.Fatalf("marker-only PANI = %q, want empty", got)
	}
	if got := (&Session{pani: "IEEE-802.11;i-wlan-node-id=" + defaultPANIWLANNode, paniResolved: true}).pAccessNetworkInfo(); got != "IEEE-802.11;i-wlan-node-id="+defaultPANIWLANNode {
		t.Fatalf("session PANI = %q, want default WLAN node", got)
	}
}

func TestPAccessNetworkInfoUsesDefaultNodeAndConditionalCountry(t *testing.T) {
	cases := []struct {
		name     string
		identity vowifi.SIMIdentity
		want     string
	}{
		{
			name:     "standard without PANI country format",
			identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"},
			want:     "IEEE-802.11;i-wlan-node-id=" + defaultPANIWLANNode,
		},
		{
			name:     "giffgaff with IPCC PANI country format",
			identity: vowifi.SIMIdentity{IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF"},
			want:     "IEEE-802.11;country=GB;i-wlan-node-id=" + defaultPANIWLANNode,
		},
		{
			name:     "AT&T without PANI country format",
			identity: vowifi.SIMIdentity{IMSI: "310410000000001", HomeMCC: "310", HomeMNC: "410"},
			want:     "IEEE-802.11;i-wlan-node-id=" + defaultPANIWLANNode,
		},
		{
			name:     "VOXI without PANI country format",
			identity: vowifi.SIMIdentity{IMSI: "234150000000001", HomeMCC: "234", HomeMNC: "15", SPN: "VOXI"},
			want:     "IEEE-802.11;i-wlan-node-id=" + defaultPANIWLANNode,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := resolveSessionPAccessNetworkInfo(test.identity, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if got != test.want {
				t.Fatalf("PANI = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAppendPaniCountryModes(t *testing.T) {
	base := "IEEE-802.11;i-wlan-node-id=" + defaultPANIWLANNode
	identity := vowifi.SIMIdentity{HomeMCC: "234"}

	if got := appendPaniCountry(base, identity, vowifi.CarrierProfile{}, slog.Default()); got != base {
		t.Fatalf("empty PANI country = %q, want %q", got, base)
	}
	if got := appendPaniCountry(base, identity, vowifi.CarrierProfile{PANICountry: "GB"}, slog.Default()); got != "IEEE-802.11;country=GB;i-wlan-node-id="+defaultPANIWLANNode {
		t.Fatalf("fixed PANI country = %q", got)
	}
	if got := appendPaniCountry(base, identity, vowifi.CarrierProfile{PANICountry: "AUTO"}, slog.Default()); got != "IEEE-802.11;country=GB;i-wlan-node-id="+defaultPANIWLANNode {
		t.Fatalf("automatic PANI country = %q", got)
	}

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	got := appendPaniCountry(base, vowifi.SIMIdentity{}, vowifi.CarrierProfile{ID: "test-auto", PANICountry: "AUTO"}, logger)
	if got != base || !strings.Contains(logs.String(), "IMS PANI country code could not be derived") {
		t.Fatalf("failed automatic PANI country = %q, logs = %q", got, logs.String())
	}
}

func TestIMSProfileUserAgentUsesUnifiedHeaderValue(t *testing.T) {
	t.Setenv(imsUserAgentOverrideEnv, "")
	giffgaff := &Session{request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
		IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	}}}
	if got := giffgaff.imsUserAgent(); got != "iOS/18.6.2 iPhone" {
		t.Fatalf("giffgaff IMS User-Agent = %q", got)
	}
	standard := &Session{request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
		IMSI: "999010000000001", HomeMCC: "999", HomeMNC: "01",
	}}}
	if got := standard.imsUserAgent(); got != "iOS/18.6.2 iPhone" {
		t.Fatalf("standard IMS User-Agent fallback = %q", got)
	}
}

func TestIMSUserAgentEnvironmentOverridesOnlyDefaultFallback(t *testing.T) {
	t.Setenv(imsUserAgentOverrideEnv, "configured-default-agent")
	standard := &Session{request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
		HomeMCC: "999", HomeMNC: "01",
	}}}
	if got := standard.imsUserAgent(); got != "configured-default-agent" {
		t.Fatalf("environment IMS User-Agent fallback = %q", got)
	}

	giffgaff := &Session{request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
		IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	}}}
	if got := giffgaff.imsUserAgent(); got != "iOS/18.6.2 iPhone" {
		t.Fatalf("profile IMS User-Agent with environment fallback = %q", got)
	}
}

func TestIMSUserAgentProfileOverridesProviderAndEnvironment(t *testing.T) {
	t.Setenv(imsUserAgentOverrideEnv, "environment-agent")
	session := &Session{
		provider: &Provider{config: Config{UserAgent: "device-agent"}},
		request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
			IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
		}},
	}
	if got := session.imsUserAgent(); got != "iOS/18.6.2 iPhone" {
		t.Fatalf("profile IMS User-Agent = %q", got)
	}
}

func TestIMSUserAgentProviderOverridesEnvironment(t *testing.T) {
	t.Setenv(imsUserAgentOverrideEnv, "environment-agent")
	session := &Session{
		provider: &Provider{config: Config{UserAgent: "device-agent"}},
		request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
			HomeMCC: "999", HomeMNC: "01",
		}},
	}
	if got := session.imsUserAgent(); got != "device-agent" {
		t.Fatalf("provider IMS User-Agent = %q", got)
	}
}

func TestBuildRegisterUsesImplementationCapabilityHeaders(t *testing.T) {
	session := &Session{
		provider: &Provider{config: Config{UserAgent: "test-agent"}},
		request:  vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "999", HomeMNC: "01"}},
		identity: identitySet{
			public: "sip:001010123456789@ims.example",
			user:   "001010123456789",
			domain: "ims.example",
		},
		transport:     "tcp",
		conn:          &registerRetransmitConn{},
		callID:        "capability-test",
		fromTag:       "capability-tag",
		instanceID:    "urn:uuid:capability-test",
		outboundState: outboundRegistrationRequested,
		outboundRegID: "1",
	}

	request, err := session.buildRegister(1, 3600, "", "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(request)
	if !strings.Contains(text, "Via: SIP/2.0/TCP ") ||
		!strings.Contains(text, ";rport;keep\r\n") {
		t.Fatalf("REGISTER Via keep negotiation = %q", text)
	}
	if !strings.Contains(text, "Supported: path, outbound\r\n") || strings.Contains(text, "gruu") {
		t.Fatalf("REGISTER capability header = %q", text)
	}
	if !strings.Contains(text, "Allow: REGISTER, INVITE, ACK, CANCEL, BYE, OPTIONS, MESSAGE, PRACK, UPDATE\r\n") ||
		strings.Contains(text, "SUBSCRIBE") || strings.Contains(text, "NOTIFY") {
		t.Fatalf("REGISTER Allow header = %q", text)
	}
}

func TestOutboundRegistrationStateRequiresRegistrarConfirmation(t *testing.T) {
	session := &Session{
		provider: &Provider{config: Config{
			SecurityMode:       SecurityDisabled,
			RegistrationExpiry: time.Hour,
		}},
		request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"}},
		identity: identitySet{
			public: "sip:001010123456789@ims.example",
			user:   "001010123456789",
			domain: "ims.example",
		},
		transport:     "tcp",
		conn:          &registerRetransmitConn{},
		instanceID:    "urn:uuid:outbound-test",
		outboundState: outboundRegistrationRequested,
		outboundRegID: "1",
	}

	confirmed, err := session.applyRegistrationEvidence(&sipResponse{
		StatusCode: 200,
		Headers: map[string][]string{
			"contact": {`<sip:001010123456789@192.0.2.10>;expires=600;+sip.instance="<urn:uuid:outbound-test>"`},
			"require": {"outbound"},
		},
	})
	if err != nil {
		t.Fatalf("confirmed applyRegistrationEvidence() error = %v", err)
	}
	if !confirmed.outboundRequested || !confirmed.outboundConfirmed || confirmed.outboundRegID != "1" ||
		!session.outboundRegistrationConfirmed() {
		t.Fatalf("confirmed outbound state = %#v", confirmed)
	}
	request, err := session.buildRegister(3, 600, "", "")
	if err != nil {
		t.Fatalf("buildRegister() after confirmation error = %v", err)
	}
	text := string(request)
	if !strings.Contains(text, "Supported: path, outbound") || !strings.Contains(text, ";reg-id=1") {
		t.Fatalf("confirmed REGISTER omitted outbound state: %q", text)
	}

	session.setOutboundRegistrationState(outboundRegistrationRequested)
	unconfirmed, err := session.applyRegistrationEvidence(&sipResponse{
		StatusCode: 200,
		Headers: map[string][]string{
			"contact": {`<sip:001010123456789@192.0.2.10>;expires=600;+sip.instance="<urn:uuid:outbound-test>"`},
		},
	})
	if err != nil {
		t.Fatalf("unconfirmed applyRegistrationEvidence() error = %v", err)
	}
	if !unconfirmed.outboundRequested || unconfirmed.outboundConfirmed || unconfirmed.outboundRegID != "1" ||
		session.outboundRegistrationRequested() || session.outboundRegistrationConfirmed() {
		t.Fatalf("unconfirmed outbound state = %#v", unconfirmed)
	}
	request, err = session.buildRegister(3, 600, "", "")
	if err != nil {
		t.Fatalf("buildRegister() after fallback error = %v", err)
	}
	text = string(request)
	if strings.Contains(text, "Supported: path, outbound") || strings.Contains(text, ";reg-id=1") {
		t.Fatalf("fallback REGISTER still advertised outbound: %q", text)
	}
}

func TestRefreshDelayUsesTemporaryTwentyMinuteInterval(t *testing.T) {
	if got := refreshDelay(40 * time.Minute); got != temporaryRegistrationRefreshInterval {
		t.Fatalf("refreshDelay() = %s, want %s", got, temporaryRegistrationRefreshInterval)
	}
	if got, want := refreshDelay(10*time.Minute), 10*time.Minute-registrationSafetyMargin; got != want {
		t.Fatalf("refreshDelay() near expiry = %s, want %s", got, want)
	}
	if got := refreshDelay(3 * time.Second); got != 100*time.Millisecond {
		t.Fatalf("refreshDelay() at safety margin = %s, want 100ms", got)
	}
}

func TestRefreshRegistrationUsesTheCapturedOutboundFlow(t *testing.T) {
	connection := &refreshResponseConn{}
	session := &Session{
		provider: &Provider{config: Config{
			SecurityMode:       SecurityDisabled,
			RegistrationExpiry: time.Hour,
		}},
		request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"}},
		identity: identitySet{
			public: "sip:001010123456789@ims.example",
			user:   "001010123456789",
			domain: "ims.example",
		},
		transport:     "tcp",
		conn:          connection,
		reader:        bufio.NewReader(connection),
		callID:        "refresh-flow-test",
		fromTag:       "refresh-tag",
		instanceID:    "urn:uuid:refresh-flow-test",
		outboundState: outboundRegistrationConfirmed,
		outboundRegID: "1",
		evidence: vowifi.IMSEvidence{
			Registered:        true,
			RegistrationState: "registered",
		},
		expiresAt: time.Now().Add(time.Hour),
	}

	if err := session.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refreshOnce() error = %v", err)
	}
	if len(connection.writes) != 1 {
		t.Fatalf("refresh REGISTER writes = %d, want 1", len(connection.writes))
	}
	text := string(connection.writes[0])
	if !strings.Contains(text, "Supported: path, outbound\r\n") ||
		!strings.Contains(text, ";reg-id=1") {
		t.Fatalf("refresh REGISTER omitted confirmed outbound state: %q", text)
	}
}

func TestProtectedRefreshOffersCandidateSecurityWithoutReplacingActiveSet(t *testing.T) {
	connection := &loopbackRefreshResponseConn{}
	activeProposal := securityProposal{
		spiClient:                1001,
		spiServer:                1002,
		portClient:               30001,
		portServer:               30002,
		integrityAlgorithms:      []string{"hmac-sha-1-96"},
		encryptionAlgorithmsList: []string{"aes-cbc"},
	}
	session := &Session{
		provider: &Provider{config: Config{
			SecurityMode:       SecurityRequired,
			RegistrationExpiry: time.Hour,
		}},
		request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"}},
		identity: identitySet{
			public: "sip:001010123456789@ims.example",
			user:   "001010123456789",
			domain: "ims.example",
		},
		transport:        "tcp",
		conn:             connection,
		reader:           bufio.NewReader(connection),
		sipLocalIP:       net.ParseIP("127.0.0.1"),
		callID:           "protected-refresh",
		fromTag:          "protected-refresh-tag",
		instanceID:       "urn:uuid:protected-refresh",
		securityProposal: activeProposal,
		securityAgreement: securityAgreement{
			selected:    securityMechanism{portServer: 6060},
			verifyValue: "ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc",
		},
		securityActive: true,
		evidence: vowifi.IMSEvidence{
			Registered:        true,
			RegistrationState: "registered",
		},
		expiresAt: time.Now().Add(time.Hour),
	}

	if err := session.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refreshOnce() error = %v", err)
	}
	if len(connection.writes) != 1 {
		t.Fatalf("refresh REGISTER writes = %d, want 1", len(connection.writes))
	}
	packet, err := parseSIPPacket(connection.writes[0])
	if err != nil || packet.Request == nil {
		t.Fatalf("parse refresh REGISTER = (%#v, %v)", packet.Request, err)
	}
	offers := splitHeaderValues(packet.Request.values("Security-Client"))
	if len(offers) == 0 {
		t.Fatal("protected refresh omitted Security-Client")
	}
	candidate, err := parseSecurityMechanism(offers[0])
	if err != nil {
		t.Fatal(err)
	}
	if candidate.portServer != activeProposal.portServer {
		t.Fatalf("candidate port-s = %d, want %d", candidate.portServer, activeProposal.portServer)
	}
	if candidate.portClient == activeProposal.portClient ||
		candidate.spiClient == activeProposal.spiClient ||
		candidate.spiServer == activeProposal.spiServer {
		t.Fatalf("candidate security values were not rotated: %#v", candidate)
	}
	if session.securityProposal.spiClient != activeProposal.spiClient ||
		session.securityProposal.spiServer != activeProposal.spiServer ||
		session.securityProposal.portClient != activeProposal.portClient ||
		session.securityProposal.portServer != activeProposal.portServer {
		t.Fatalf("direct 200 replaced active proposal: %#v", session.securityProposal)
	}
}

func TestSipInstanceIDUsesGSMAFormWhenIMEIIsAvailable(t *testing.T) {
	identity := vowifi.SIMIdentity{IMEI: "353024112557010"}
	if got := sipInstanceID(identity, "00000000-0000-4000-8000-000000000001"); got != "urn:gsma:imei:35302411-255701-0" {
		t.Fatalf("sipInstanceID() = %q", got)
	}
	if got := sipInstanceID(vowifi.SIMIdentity{IMEI: "not-an-imei"}, "00000000-0000-4000-8000-000000000001"); got != "urn:uuid:00000000-0000-4000-8000-000000000001" {
		t.Fatalf("sipInstanceID() fallback = %q", got)
	}
}

func TestSipInstanceIDUsesStableUUIDForICCIDFallback(t *testing.T) {
	identity := vowifi.SIMIdentity{
		IMEI:  "not-an-imei",
		ICCID: "89 4410 0000 0000 0000 0",
	}
	first := sipInstanceID(identity, "00000000-0000-4000-8000-000000000001")
	second := sipInstanceID(identity, "00000000-0000-4000-8000-000000000002")
	if first != second {
		t.Fatalf("ICCID-derived instance changed with fallback UUID: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "urn:uuid:") || len(first) != len("urn:uuid:")+36 {
		t.Fatalf("ICCID-derived instance has invalid UUID URN: %q", first)
	}
	if other := sipInstanceID(vowifi.SIMIdentity{ICCID: "8944100000000000001"}, ""); other == first {
		t.Fatalf("different ICCIDs produced the same instance: %q", first)
	}
}

func validateTestPANI(value string) error {
	const accessType = "IEEE-802.11"
	if !strings.HasPrefix(value, accessType+";") {
		return fmt.Errorf("value %q does not start with %q", value, accessType+";")
	}
	if strings.Contains(strings.ToLower(value), "network-provided") {
		return fmt.Errorf("UE PANI incorrectly claims network-provided provenance: %q", value)
	}
	var nodeValue, country string
	for _, parameter := range strings.Split(strings.TrimPrefix(value, accessType+";"), ";") {
		key, parameterValue, ok := strings.Cut(parameter, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "i-wlan-node-id":
			nodeValue = strings.TrimSpace(parameterValue)
		case "country":
			country = strings.TrimSpace(parameterValue)
		}
	}
	if nodeValue == "" {
		return fmt.Errorf("i-wlan-node-id is missing: %q", value)
	}
	node, err := hex.DecodeString(nodeValue)
	if err != nil || len(node) != 6 {
		return fmt.Errorf("i-wlan-node-id must be 12 hexadecimal digits: %q", value)
	}
	if strings.EqualFold(nodeValue, defaultPANIWLANNode) {
		if country != "" && len(country) != 2 {
			return fmt.Errorf("country must be an ISO alpha-2 code: %q", value)
		}
		return nil
	}
	if node[0]&0x03 != 0x02 {
		return fmt.Errorf("i-wlan-node-id must be a locally administered unicast identifier: %q", value)
	}
	return nil
}

func serveRefreshFailure(listener *net.UDPConn, nonce string, rejectionStatus int) error {
	for step := 0; step < 4; step++ {
		packet := make([]byte, 65535)
		count, remote, err := listener.ReadFromUDP(packet)
		if err != nil {
			return err
		}
		_, headers, err := parseTestRequest(packet[:count])
		if err != nil {
			return err
		}
		if step == 0 {
			if _, err := listener.WriteToUDP(
				testResponseForHeaders(
					401,
					"Unauthorized",
					headers,
					[]string{
						`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` +
							nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
					},
				),
				remote,
			); err != nil {
				return err
			}
			continue
		}
		if step == 1 {
			if headers["authorization"] == "" {
				return errors.New("authenticated REGISTER omitted Authorization")
			}
			if _, err := listener.WriteToUDP(
				testResponseForHeaders(
					200,
					"OK",
					headers,
					[]string{
						"P-Associated-URI: <tel:+8613800138000>",
						"Contact: " + headers["contact"] + ";expires=600",
					},
				),
				remote,
			); err != nil {
				return err
			}
			continue
		}
		if headers["authorization"] == "" {
			return errors.New("refresh REGISTER omitted cached Authorization")
		}
		if step == 2 {
			reason := "Service Unavailable"
			if rejectionStatus != 503 {
				reason = "Forbidden"
			}
			if _, err := listener.WriteToUDP(
				testResponseForHeaders(rejectionStatus, reason, headers, nil),
				remote,
			); err != nil {
				return err
			}
			if rejectionStatus != 503 {
				return nil
			}
			continue
		}
		if headers["expires"] != "0" {
			return fmt.Errorf("deregister Expires = %q, want 0", headers["expires"])
		}
		if _, err := listener.WriteToUDP(
			testResponseForHeaders(200, "OK", headers, nil),
			remote,
		); err != nil {
			return err
		}
	}
	return nil
}

func parseTestRequest(packet []byte) (string, map[string]string, error) {
	text := strings.ReplaceAll(string(packet), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return "", nil, errors.New("short SIP request")
	}
	headers := make(map[string]string)
	for _, line := range lines[1:] {
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		if !found {
			return "", nil, fmt.Errorf("malformed request header %q", line)
		}
		headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	return lines[0], headers, nil
}

func verifyTestAuthorization(value string, nonce string) error {
	scheme, parameters, found := strings.Cut(value, " ")
	if !found || scheme != "Digest" {
		return errors.New("invalid Authorization scheme")
	}
	directives, err := parseAuthDirectives(parameters)
	if err != nil {
		return err
	}
	expected := digestResponse(
		"001010123456789@ims.mnc001.mcc001.3gppnetwork.org",
		"ims.mnc001.mcc001.3gppnetwork.org",
		[]byte{1, 2, 3, 4, 5, 6, 7, 8},
		"REGISTER",
		"sip:ims.mnc001.mcc001.3gppnetwork.org",
		nonce,
		directives["nc"],
		directives["cnonce"],
		directives["qop"],
	)
	if directives["response"] != expected {
		return fmt.Errorf("digest response = %q, want %q", directives["response"], expected)
	}
	if directives["algorithm"] != "AKAv1-MD5" || directives["qop"] != "auth" {
		return fmt.Errorf("digest directives = %#v", directives)
	}
	return nil
}

func TestRegistrationRejectionErrorIncludesSafeDiagnostics(t *testing.T) {
	err := registrationRejectionError(&sipResponse{
		StatusCode: 403,
		Reason:     "Forbidden\r\nignored",
		Headers: map[string][]string{
			"reason":           {`SIP;cause=403;text="not provisioned"`},
			"warning":          {`399 pcscf "subscriber barred"`},
			"www-authenticate": {`Digest nonce="must-not-leak"`},
		},
	}, "authenticated")
	if !errors.Is(err, ErrRegistrationRejected) {
		t.Fatalf("error does not wrap ErrRegistrationRejected: %v", err)
	}
	if status, ok := registrationRejectionStatus(err); !ok || status != 403 {
		t.Fatalf("registrationRejectionStatus() = (%d, %t), want (403, true)", status, ok)
	}
	message := err.Error()
	for _, expected := range []string{
		"authenticated REGISTER was rejected",
		"SIP 403 Forbidden ignored",
		`Reason: SIP;cause=403;text="not provisioned"`,
		`Warning: 399 pcscf "subscriber barred"`,
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("error %q does not contain %q", message, expected)
		}
	}
	if strings.Contains(message, "must-not-leak") {
		t.Fatalf("error leaked an authentication header: %q", message)
	}
}

func testResponse(
	status int,
	reason string,
	callID string,
	cseq string,
	extraHeaders []string,
) []byte {
	lines := []string{
		"SIP/2.0 " + strconv.Itoa(status) + " " + reason,
		"Call-ID: " + callID,
		"CSeq: " + cseq,
	}
	lines = append(lines, extraHeaders...)
	lines = append(lines, "Content-Length: 0", "", "")
	return []byte(strings.Join(lines, "\r\n"))
}

func testResponseForRequest(
	status int,
	reason string,
	request *sipRequest,
	extraHeaders []string,
) []byte {
	headers := append([]string{"Via: " + request.value("Via")}, extraHeaders...)
	return testResponse(
		status,
		reason,
		request.value("Call-ID"),
		request.value("CSeq"),
		headers,
	)
}

func testResponseForHeaders(
	status int,
	reason string,
	requestHeaders map[string]string,
	extraHeaders []string,
) []byte {
	headers := append([]string{"Via: " + requestHeaders["via"]}, extraHeaders...)
	return testResponse(
		status,
		reason,
		requestHeaders["call-id"],
		requestHeaders["cseq"],
		headers,
	)
}
