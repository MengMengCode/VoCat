package ims

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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

func TestRefreshFailureRevokesRegistrationEvidence(t *testing.T) {
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
		serverDone <- serveRefreshFailure(listener, nonce)
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
	if err := concrete.refreshOnce(context.Background()); err == nil {
		t.Fatal("refreshOnce() error = nil, want SIP rejection")
	}
	evidence := session.Evidence()
	if evidence.Registered || evidence.RegistrationState != "refresh_failed" {
		t.Fatalf("evidence after failed refresh = %#v", evidence)
	}
	if sms, err := session.EnableSMS(context.Background()); sms.Ready || !errors.Is(err, vowifi.ErrIMSNotRegistered) {
		t.Fatalf("EnableSMS() = (%#v, %v), want IMS not registered", sms, err)
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
			"p-access-network-info",
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
		if step == 0 {
			if headers["authorization"] != "" {
				return errors.New("initial REGISTER unexpectedly authenticated")
			}
			callID = headers["call-id"]
			response := testResponse(
				401,
				"Unauthorized",
				callID,
				headers["cseq"],
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
			response := testResponse(
				200,
				"OK",
				callID,
				headers["cseq"],
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
			if headers["authorization"] != "" {
				return errors.New("refresh reused the one-time AKAv1 RES")
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
			response := testResponse(
				200,
				"OK",
				callID,
				headers["cseq"],
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
			testResponse(200, "OK", callID, headers["cseq"], nil),
			remote,
		); err != nil {
			return err
		}
	}
	return nil
}

func TestO2GermanyInitialRegisterMatchesSupportedIMSProfile(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	identity := vowifi.SIMIdentity{
		IMSI:    "262030123456789",
		HomeMCC: "262",
		HomeMNC: "03",
	}
	identities, err := deriveIdentities(identity, Config{})
	if err != nil {
		t.Fatalf("deriveIdentities() error = %v", err)
	}
	session := &Session{
		provider: &Provider{config: Config{
			SecurityMode: SecurityRequired,
			UserAgent:    "vocat-test",
		}},
		request:    vowifi.IMSRequest{Identity: identity},
		identity:   identities,
		endpoint:   pcscfEndpoint{host: "pcscf.example", port: 5060},
		transport:  "tcp",
		conn:       client,
		callID:     "o2-test",
		fromTag:    "tag",
		instanceID: "urn:uuid:test",
		securityProposal: securityProposal{
			spiClient:  101,
			spiServer:  102,
			portClient: 5062,
			portServer: 5063,
			encryption: "null",
		},
	}

	packet, err := session.buildRegister(1, 3600, "", "")
	if err != nil {
		t.Fatalf("buildRegister() error = %v", err)
	}
	_, headers, err := parseTestRequest(packet)
	if err != nil {
		t.Fatalf("parseTestRequest() error = %v", err)
	}
	if got, want := headers["security-client"], "ipsec-3gpp;q=1.000;alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=null;spi-c=0000000101;spi-s=0000000102;port-c=5062;port-s=5063"; got != want {
		t.Fatalf("Security-Client = %q, want %q", got, want)
	}
	if headers["proxy-require"] != "sec-agree" || !strings.Contains(headers["authorization"], "integrity-protected=no") {
		t.Fatalf("initial O2 headers omitted standardized sec-agree/IMS-AKA fields: %#v", headers)
	}
	if got, want := headers["p-preferred-identity"], "<"+identities.public+">"; got != want {
		t.Fatalf("P-Preferred-Identity = %q, want %q", got, want)
	}
	for name, token := range map[string]string{
		"supported": "sec-agree",
		"allow":     "MESSAGE",
	} {
		if !strings.Contains(headers[name], token) {
			t.Fatalf("%s = %q, want token %q", name, headers[name], token)
		}
	}
}

func TestATT310280DeriveIdentitiesUsesISIMDomains(t *testing.T) {
	identities, err := deriveIdentities(vowifi.SIMIdentity{
		IMSI: "310280229187733", HomeMCC: "310", HomeMNC: "280",
	}, Config{})
	if err != nil {
		t.Fatalf("deriveIdentities() error = %v", err)
	}
	if identities.domain != "one.att.net" ||
		identities.private != "310280229187733@private.att.net" ||
		identities.public != "sip:310280229187733@one.att.net" {
		t.Fatalf("AT&T identities = %#v", identities)
	}
}

func TestATT310280InitialRegisterMatchesProvisionedProfile(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	identity := vowifi.SIMIdentity{
		IMSI: "310280229187733", HomeMCC: "310", HomeMNC: "280",
	}
	identities, err := deriveIdentities(identity, Config{})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{
		provider:   &Provider{config: Config{SecurityMode: SecurityRequired, UserAgent: "vocat/1"}},
		request:    vowifi.IMSRequest{Identity: identity},
		identity:   identities,
		endpoint:   pcscfEndpoint{host: "pcscf.example", port: 5060},
		transport:  "tcp",
		conn:       client,
		callID:     "att-test",
		fromTag:    "tag",
		instanceID: "urn:uuid:test",
		securityProposal: securityProposal{
			spiClient: 1546543, spiServer: 1546542,
			portClient: 32773, portServer: 6000,
			integrityAlgorithms:      []string{"hmac-sha-1-96"},
			encryptionAlgorithmsList: []string{"aes-cbc"},
		},
	}
	packet, err := session.buildRegister(1, 3600, "", "")
	if err != nil {
		t.Fatalf("buildRegister() error = %v", err)
	}
	request := string(packet)
	for _, want := range []string{
		"REGISTER sip:one.att.net SIP/2.0",
		"Expires: 18400",
		"Supported: path,sec-agree,gruu",
		"User-Agent: SimAdmin VoWiFi",
		`+g.3gpp.accesstype="wlan1";audio;+g.3gpp.smsip`,
		"P-Preferred-Identity: <sip:310280229187733@one.att.net>",
		`P-Visited-Network-ID: "one.att.net"`,
		"P-Access-Network-Info: IEEE-802.11;i-wlan-node-id=000000000000;network-provided",
		"Cellular-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=3102800000000;cell-info-age=0",
		"Accept-Contact: *;+g.3gpp.smsip",
		"Security-Client: ipsec-3gpp; alg=hmac-sha-1-96; ealg=aes-cbc; prot=esp; mod=trans; spi-c=1546543; spi-s=1546542; port-c=32773; port-s=6000",
		`username="310280229187733@private.att.net"`,
		`uri="sip:one.att.net"`,
	} {
		if !strings.Contains(request, want) {
			t.Fatalf("AT&T REGISTER omits %q:\n%s", want, request)
		}
	}
}

func serveRefreshFailure(listener *net.UDPConn, nonce string) error {
	var callID string
	for step := 0; step < 3; step++ {
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
			callID = headers["call-id"]
			if _, err := listener.WriteToUDP(
				testResponse(
					401,
					"Unauthorized",
					callID,
					headers["cseq"],
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
				testResponse(
					200,
					"OK",
					callID,
					headers["cseq"],
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
		if headers["authorization"] != "" {
			return errors.New("refresh reused the one-time AKAv1 RES")
		}
		if _, err := listener.WriteToUDP(
			testResponse(503, "Service Unavailable", callID, headers["cseq"], nil),
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
