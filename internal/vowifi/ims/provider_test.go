package ims

import (
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
	provider.rememberTransport(first, "udp")
	if got := provider.cachedTransport(first); got != "udp" {
		t.Fatalf("cached first transport = %q", got)
	}
	if got := provider.cachedTransport(second); got != "" {
		t.Fatalf("second SIM inherited cached transport %q", got)
	}
}

func TestUDPRegisterRetransmitsBeforeTransactionTimeout(t *testing.T) {
	t.Parallel()
	connection := &registerRetransmitConn{response: []byte(strings.Join([]string{
		"SIP/2.0 200 OK",
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
	response, err := session.exchange(context.Background(), []byte("REGISTER test"), 7)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || connection.writes != 2 {
		t.Fatalf("response=%#v writes=%d, want SIP 200 after one retransmission", response, connection.writes)
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
	giffgaff := &Session{request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
		IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	}}}
	if got := giffgaff.imsUserAgent(); got != "iOS/18.6.2 iPhone" {
		t.Fatalf("giffgaff IMS User-Agent = %q", got)
	}
	if options := giffgaff.imsRegisterOptions(); options.AllowHeader != nil || options.SupportedHeader != nil {
		t.Fatalf("giffgaff REGISTER capability overrides leaked from business headers: %#v", options)
	}

	standard := &Session{request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
		IMSI: "999010000000001", HomeMCC: "999", HomeMNC: "01",
	}}}
	if got := standard.imsUserAgent(); got != "vocat/1" {
		t.Fatalf("standard IMS User-Agent fallback = %q", got)
	}
}

func TestSipInstanceIDUsesGSMAFormWhenIMEIIsAvailable(t *testing.T) {
	identity := vowifi.SIMIdentity{IMEI: "353024112557010"}
	if got := sipInstanceID(identity, "00000000-0000-4000-8000-000000000001"); got != "urn:gsma:imei:353024112557010-0" {
		t.Fatalf("sipInstanceID() = %q", got)
	}
	if got := sipInstanceID(vowifi.SIMIdentity{IMEI: "not-an-imei"}, "00000000-0000-4000-8000-000000000001"); got != "urn:uuid:00000000-0000-4000-8000-000000000001" {
		t.Fatalf("sipInstanceID() fallback = %q", got)
	}
}

func TestGSMAContactFormatUsesAddressAndDeviceInstance(t *testing.T) {
	session := &Session{
		identity:   identitySet{user: "234105776448519"},
		transport:  "tcp",
		instanceID: "urn:gsma:imei:353024112557010-0",
	}
	got := session.buildContact("[2001:db8::1]:49686", vowifi.IMSRegisterOptions{
		ContactFormat:    vowifi.IMSContactFormatGSMA,
		ContactExtraTags: []string{"+g.3gpp.mid-call", "+g.3gpp.smsip"},
	})
	want := `<sip:[2001:db8::1]:49686>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.smsip;+sip.instance="<urn:gsma:imei:353024112557010-0>"`
	if got != want {
		t.Fatalf("GSMA Contact = %q, want %q", got, want)
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
