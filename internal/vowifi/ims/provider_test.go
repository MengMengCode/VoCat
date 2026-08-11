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

func TestIMSIdentitiesFailClosedWithoutISIMOverrides(t *testing.T) {
	identity := vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"}
	if _, err := deriveIdentities(identity, Config{RequireExplicitIdentities: true}); err == nil || !strings.Contains(err.Error(), "explicit") {
		t.Fatalf("deriveIdentities() error = %v, want explicit identity requirement", err)
	}
	if _, err := deriveIdentities(identity, Config{PrivateIdentity: "impi@example.net", RequireExplicitIdentities: true}); err == nil {
		t.Fatal("deriveIdentities() accepted only one explicit IMS identity")
	}

	explicit, err := deriveIdentities(identity, Config{
		PrivateIdentity:           "impi@example.net",
		PublicIdentity:            "sip:+12025550123@example.net",
		RequireExplicitIdentities: true,
	})
	if err != nil {
		t.Fatalf("deriveIdentities(explicit) error = %v", err)
	}
	if explicit.private != "impi@example.net" || explicit.public != "sip:+12025550123@example.net" {
		t.Fatalf("explicit identities = %#v", explicit)
	}
	uppercase, err := deriveIdentities(identity, Config{
		PrivateIdentity: "impi@example.net",
		PublicIdentity:  "SIP:+12025550123@example.net",
	})
	if err != nil {
		t.Fatalf("deriveIdentities(uppercase SIP scheme) error = %v", err)
	}
	if uppercase.user != "+12025550123" {
		t.Fatalf("uppercase SIP public user = %q", uppercase.user)
	}

	derived, err := deriveIdentities(identity, Config{})
	if err != nil {
		t.Fatalf("deriveIdentities(explicit opt-in) error = %v", err)
	}
	if derived.private != "001010123456789@ims.mnc001.mcc001.3gppnetwork.org" ||
		derived.public != "sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org" {
		t.Fatalf("opt-in derived identities = %#v", derived)
	}
}

func TestSIPInstanceUUIDIsStablePerEquipment(t *testing.T) {
	first, err := sipInstanceUUID(vowifi.IMSRequest{
		DeviceID: "wwan0",
		Identity: vowifi.SIMIdentity{IMEI: "867394042309830"},
	})
	if err != nil {
		t.Fatalf("sipInstanceUUID(first) error = %v", err)
	}
	second, err := sipInstanceUUID(vowifi.IMSRequest{
		DeviceID: "renamed-device",
		Identity: vowifi.SIMIdentity{IMEI: "867394042309830"},
	})
	if err != nil {
		t.Fatalf("sipInstanceUUID(second) error = %v", err)
	}
	if first != second {
		t.Fatalf("same equipment instance UUID changed: %q != %q", first, second)
	}
	different, err := sipInstanceUUID(vowifi.IMSRequest{
		DeviceID: "wwan0",
		Identity: vowifi.SIMIdentity{IMEI: "867394042309831"},
	})
	if err != nil {
		t.Fatalf("sipInstanceUUID(different) error = %v", err)
	}
	if different == first {
		t.Fatalf("different equipment reused instance UUID %q", first)
	}
	if len(first) != 36 || first[14] != '5' || !strings.ContainsRune("89ab", rune(first[19])) {
		t.Fatalf("instance UUID %q is not RFC 4122 version 5", first)
	}

	fallbackOne, err := sipInstanceUUID(vowifi.IMSRequest{DeviceID: "WWAN0"})
	if err != nil {
		t.Fatalf("sipInstanceUUID(fallback one) error = %v", err)
	}
	fallbackTwo, err := sipInstanceUUID(vowifi.IMSRequest{DeviceID: "wwan0"})
	if err != nil {
		t.Fatalf("sipInstanceUUID(fallback two) error = %v", err)
	}
	if fallbackOne != fallbackTwo {
		t.Fatalf("device fallback instance UUID changed: %q != %q", fallbackOne, fallbackTwo)
	}
}

func (tunnel evidenceTunnel) Evidence() vowifi.TunnelEvidence {
	return tunnel.evidence
}

func (evidenceTunnel) Close(context.Context) error {
	return nil
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
					SMSC:    "+12025550100",
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
