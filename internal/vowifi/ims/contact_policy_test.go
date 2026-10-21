package ims

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func lebaraContactTestRequest() vowifi.IMSRequest {
	return vowifi.IMSRequest{DeviceID: "pcsc-reader-1", Identity: vowifi.SIMIdentity{
		IMSI: "234870123456789", HomeMCC: "234", HomeMNC: "87", SPN: "Lebara",
	}}
}

func contactPolicyTestURI(t *testing.T, contact string) string {
	t.Helper()
	uri, _, ok := strings.Cut(strings.TrimPrefix(contact, "<"), ">")
	if !ok {
		t.Fatalf("Contact has no name-addr URI: %q", contact)
	}
	return uri
}

func contactPolicyTestInstance(t *testing.T, contact string) string {
	t.Helper()
	_, rest, ok := strings.Cut(contact, `+sip.instance="<`)
	if !ok {
		t.Fatalf("Contact has no instance: %q", contact)
	}
	instance, _, ok := strings.Cut(rest, `>"`)
	if !ok {
		t.Fatalf("Contact has malformed instance: %q", contact)
	}
	return instance
}

func TestLebaraRegisterContactPolicyPreservesIdentitiesAndLifecycle(t *testing.T) {
	request := lebaraContactTestRequest()
	session := newStableInstanceTestSession(t, request)
	wantContact := session.buildContact(session.contactAddress(), session.imsRegisterOptions())
	uri := contactPolicyTestURI(t, wantContact)
	user, _, ok := strings.Cut(strings.TrimPrefix(uri, "sip:"), "@")
	if !ok || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(user) {
		t.Fatalf("Lebara Contact must use an opaque stable UUID user: %q", uri)
	}
	if want := "sip:" + user + "@" + session.contactAddress() + ";transport=tcp"; uri != want {
		t.Fatalf("Contact URI = %q, want %q", uri, want)
	}
	if got := contactPolicyTestInstance(t, wantContact); got != "urn:gsma:imei:00000000-000000-0" {
		t.Fatalf("no-modem instance = %q", got)
	}
	for _, tag := range []string{";+g.3gpp.smsip", ";audio", "urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel", "urn%3Aurn-7%3A3gpp-service.ims.icsi.sms"} {
		if !strings.Contains(wantContact, tag) {
			t.Errorf("Contact omits existing service capability %q: %s", tag, wantContact)
		}
	}

	private := "234870123456789@ims.mnc087.mcc234.3gppnetwork.org"
	public := "sip:" + private
	if session.identity.private != private || session.identity.public != public {
		t.Fatalf("Contact policy changed IMS identities: %#v", session.identity)
	}
	authorization := `Digest username="` + private + `", realm="ims.mnc087.mcc234.3gppnetwork.org", nonce="test", uri="sip:ims.mnc087.mcc234.3gppnetwork.org", response="test"`
	for index, phase := range []struct {
		name          string
		expires       int
		authorization string
	}{
		{"initial", 3600, ""},
		{"authenticated", 3600, authorization},
		{"refresh", 1800, authorization},
		{"deregister", 0, authorization},
	} {
		t.Run(phase.name, func(t *testing.T) {
			packet, err := session.buildRegister(uint32(index+1), phase.expires, "Authorization", phase.authorization)
			if err != nil {
				t.Fatal(err)
			}
			start, headers, err := parseTestRequest(packet)
			if err != nil {
				t.Fatal(err)
			}
			if headers["contact"] != wantContact {
				t.Fatalf("%s changed Contact: %q, want %q", phase.name, headers["contact"], wantContact)
			}
			if start != "REGISTER sip:ims.mnc087.mcc234.3gppnetwork.org SIP/2.0" || headers["to"] != "<"+public+">" || headers["from"] != "<"+public+">;tag="+session.fromTag {
				t.Fatalf("REGISTER identities changed: %q, headers %#v", start, headers)
			}
			if headers["authorization"] != phase.authorization {
				t.Errorf("Authorization = %q, want %q", headers["authorization"], phase.authorization)
			}
			if headers["expires"] != fmt.Sprint(phase.expires) || headers["cseq"] != fmt.Sprintf("%d REGISTER", index+1) {
				t.Errorf("REGISTER lifecycle headers = %#v", headers)
			}
		})
	}
	if next := newStableInstanceTestSession(t, request); next.buildContact(next.contactAddress(), next.imsRegisterOptions()) != wantContact {
		t.Error("Contact changed across independent providers/connections")
	}
	request.DeviceID = "pcsc-reader-2"
	other := newStableInstanceTestSession(t, request)
	otherContact := other.buildContact(other.contactAddress(), other.imsRegisterOptions())
	if contactPolicyTestURI(t, otherContact) == uri {
		t.Error("different readers share the same opaque Contact user")
	}
	if contactPolicyTestInstance(t, otherContact) != contactPolicyTestInstance(t, wantContact) {
		t.Error("no-modem compatibility instance must be independent of the opaque Contact user")
	}
}

func TestRegisterContactExplicitInstancePolicies(t *testing.T) {
	for _, test := range []struct {
		name     string
		imei     string
		format   string
		fallback string
		want     string
	}{
		{"real_IMEI", "353024112557010", "gsma-imei", "zero-imei", "urn:gsma:imei:35302411-255701-0"},
		{"trimmed_IMEI", " 353024112557010\n", "gsma-imei", "uuid", "urn:gsma:imei:35302411-255701-0"},
		{"missing_IMEI_zero", "", "gsma-imei", "zero-imei", "urn:gsma:imei:00000000-000000-0"},
		{"invalid_IMEI_zero", "35302411255701x", "gsma-imei", "zero-imei", "urn:gsma:imei:00000000-000000-0"},
		{"missing_IMEI_UUID", "", "gsma-imei", "uuid", "urn:uuid:"},
		{"invalid_IMEI_UUID", "3530241125570100", "gsma-imei", "uuid", "urn:uuid:"},
		{"explicit_UUID_with_IMEI", "353024112557010", "uuid", "uuid", "urn:uuid:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := lebaraContactTestRequest()
			request.Identity.IMEI = test.imei
			session := newStableInstanceTestSession(t, request)
			options := vowifi.IMSRegisterOptions{ContactUserSource: "public-identity", InstanceIDFormat: test.format, InstanceIDFallback: test.fallback}
			contact := session.buildContact(session.contactAddress(), options)
			got := contactPolicyTestInstance(t, contact)
			if (test.want == "urn:uuid:" && !strings.HasPrefix(got, test.want)) || (test.want != "urn:uuid:" && got != test.want) {
				t.Fatalf("instance = %q, want %q", got, test.want)
			}
			if !strings.HasPrefix(contactPolicyTestURI(t, contact), "sip:"+session.identity.user+"@") {
				t.Errorf("explicit public-identity user source was not respected: %s", contact)
			}
		})
	}
}

func TestLebaraRealIMEIContactKeepsOpaqueUserStable(t *testing.T) {
	request := lebaraContactTestRequest()
	request.Identity.IMEI = "353024112557010"
	first := newStableInstanceTestSession(t, request)
	contact := first.buildContact(first.contactAddress(), first.imsRegisterOptions())
	if got := contactPolicyTestInstance(t, contact); got != "urn:gsma:imei:35302411-255701-0" {
		t.Fatalf("real modem instance = %q", got)
	}
	request.DeviceID = "renamed-reader"
	request.Identity.IMSI = "234870987654321"
	next := newStableInstanceTestSession(t, request)
	if got := contactPolicyTestURI(t, next.buildContact(next.contactAddress(), next.imsRegisterOptions())); got != contactPolicyTestURI(t, contact) {
		t.Fatalf("stable hardware changed opaque Contact: %q, want %q", got, contactPolicyTestURI(t, contact))
	}
}

func TestRegisterContactExplicitUUIDOverridesLegacyGSMA(t *testing.T) {
	request := vowifi.IMSRequest{DeviceID: "reader", Identity: vowifi.SIMIdentity{
		IMEI: "353024112557010", IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	}}
	session := newStableInstanceTestSession(t, request)
	legacyContact := session.buildContact(session.contactAddress(), session.imsRegisterOptions())
	if instance := contactPolicyTestInstance(t, legacyContact); instance != "urn:gsma:imei:353024112557010-0" {
		t.Fatalf("legacy GSMA representation changed: %q", instance)
	}
	dialog := session.dialogContactHeader()
	options := vowifi.IMSRegisterOptions{
		ContactFormat: vowifi.IMSContactFormatGSMA, ContactUserSource: "stable-instance",
		InstanceIDFormat: "uuid", InstanceIDFallback: "uuid",
	}
	contact := session.buildContact(session.contactAddress(), options)
	const uuid = "eddf02e1-07af-5305-bad0-a7ce44dd536d"
	if instance := contactPolicyTestInstance(t, contact); instance != "urn:uuid:"+uuid {
		t.Fatalf("explicit UUID policy used legacy GSMA instance: %q", instance)
	}
	if uri := contactPolicyTestURI(t, contact); uri != "sip:"+uuid+"@"+session.contactAddress()+";transport=tcp" {
		t.Fatalf("explicit opaque user did not override address-only GSMA layout: %q", uri)
	}
	if got := session.dialogContactHeader(); got != dialog {
		t.Fatalf("REGISTER policy changed ordinary dialog Contact: %q, want %q", got, dialog)
	}
}

func TestLebaraRegisterPolicyPreservesOrdinaryDialogContact(t *testing.T) {
	for _, imei := range []string{"", "353024112557010"} {
		t.Run("IMEI_"+imei, func(t *testing.T) {
			request := lebaraContactTestRequest()
			request.Identity.IMEI = imei
			lebara := newStableInstanceTestSession(t, request)
			request.Identity.SPN = "Other"
			baseline := newStableInstanceTestSession(t, request)
			if got, want := lebara.dialogContactHeader(), baseline.dialogContactHeader(); got != want {
				t.Fatalf("carrier REGISTER policy changed dialog Contact: %q, want %q", got, want)
			}
			if !strings.Contains(lebara.dialogContactHeader(), "+sip.instance=\"<urn:uuid:") {
				t.Fatalf("ordinary dialog instance changed to REGISTER-only compatibility fallback: %s", lebara.dialogContactHeader())
			}
		})
	}
}

func TestLebaraRegisterContactPolicyStaysStableAcrossProfileReload(t *testing.T) {
	request := lebaraContactTestRequest()
	session := newStableInstanceTestSession(t, request)
	initialPacket, err := session.buildRegister(1, 3600, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, initialHeaders, err := parseTestRequest(initialPacket)
	if err != nil {
		t.Fatal(err)
	}
	emptyProfiles := t.TempDir()
	t.Cleanup(func() {
		if err := vowifi.LoadCarrierProfileDirectory(emptyProfiles); err != nil {
			t.Errorf("clear installed carrier profiles: %v", err)
		}
	})
	profileDir := t.TempDir()
	profile := `{"version":1,"profiles":[{"id":"test-reloaded-lebara-contact","match":{"home_plmns":["23487"],"require_home_plmn":true,"spns":["Lebara"]},"ims":{"register_options":{"contact_format":"standard","contact_user_source":"public-identity","instance_id_format":"uuid","instance_id_fallback":"uuid"}}}]}`
	if err := os.WriteFile(filepath.Join(profileDir, "contact.json"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vowifi.LoadCarrierProfileDirectory(profileDir); err != nil {
		t.Fatal(err)
	}
	for index, expires := range []int{3600, 0} {
		packet, err := session.buildRegister(uint32(index+2), expires, "", "")
		if err != nil {
			t.Fatal(err)
		}
		_, headers, err := parseTestRequest(packet)
		if err != nil {
			t.Fatal(err)
		}
		if got := headers["contact"]; got != initialHeaders["contact"] {
			t.Fatalf("profile reload changed existing registration Contact: %q, want %q", got, initialHeaders["contact"])
		}
	}
	response := &sipResponse{StatusCode: 200, Headers: map[string][]string{"contact": {initialHeaders["contact"] + ";expires=600"}}}
	if err := session.applyRegistrationEvidence(response); err != nil {
		t.Fatal(err)
	}
	if got := session.Evidence().RegisteredContact; got != response.Headers["contact"][0] {
		t.Fatalf("profile reload stopped matching active registration Contact: %q", got)
	}
	next := newStableInstanceTestSession(t, request)
	nextPacket, err := next.buildRegister(1, 3600, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, nextHeaders, err := parseTestRequest(nextPacket)
	if err != nil {
		t.Fatal(err)
	}
	if nextHeaders["contact"] == initialHeaders["contact"] || !strings.HasPrefix(contactPolicyTestURI(t, nextHeaders["contact"]), "sip:"+next.identity.user+"@") || !strings.HasPrefix(contactPolicyTestInstance(t, nextHeaders["contact"]), "urn:uuid:") {
		t.Fatalf("new session did not adopt reloaded Contact policy: %q", nextHeaders["contact"])
	}
}

func TestLebaraRegistrationEvidenceSelectsOwnContact(t *testing.T) {
	for _, test := range []struct {
		name          string
		foreignFirst  bool
		stripInstance bool
		stripSMS      bool
		zeroExpiry    bool
		foreignOnly   bool
	}{
		{name: "foreign_before", foreignFirst: true},
		{name: "foreign_after"},
		{name: "URI_echo_without_instance", foreignFirst: true, stripInstance: true},
		{name: "foreign_SMS_tag_not_confirmation", foreignFirst: true, stripSMS: true},
		{name: "own_zero_expiry", foreignFirst: true, zeroExpiry: true},
		{name: "foreign_only_is_not_confirmation", foreignOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newStableInstanceTestSession(t, lebaraContactTestRequest())
			session.provider.config.RegistrationExpiry = time.Hour
			contact := session.buildContact(session.contactAddress(), session.imsRegisterOptions())
			instance := contactPolicyTestInstance(t, contact)
			own := contact
			if test.stripInstance {
				own = strings.Replace(own, `;+sip.instance="<`+instance+`>"`, "", 1)
			}
			if test.stripSMS {
				own = strings.ReplaceAll(own, ";+g.3gpp.smsip", "")
			}
			seconds := 3590
			if test.zeroExpiry {
				seconds = 0
			}
			own += fmt.Sprintf(";expires=%d", seconds)
			foreign := `<sip:11111111-1111-5111-8111-111111111111@` + session.contactAddress() + `;transport=tcp>;+sip.instance="<` + instance + `>";+g.3gpp.smsip;expires=7200`
			contacts := []string{own, foreign}
			if test.foreignFirst {
				contacts = []string{foreign, own}
			}
			if test.foreignOnly {
				contacts = []string{foreign}
			}
			response := &sipResponse{StatusCode: 200, Headers: map[string][]string{
				"contact": contacts,
				"expires": {"3600"},
			}}
			before := time.Now()
			err := session.applyRegistrationEvidence(response)
			if test.zeroExpiry {
				if !errors.Is(err, ErrRegistrationRejected) || session.Evidence().Registered {
					t.Fatalf("own zero expiry must reject registration: err=%v, evidence=%#v", err, session.Evidence())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.foreignOnly {
				if got := session.Evidence().RegisteredContact; got != "" {
					t.Fatalf("another binding sharing zero IMEI was selected: %q", got)
				}
			} else {
				if got := session.Evidence().RegisteredContact; got != own {
					t.Fatalf("selected Contact = %q, want %q", got, own)
				}
				if got := session.expiresAt.Sub(before); got < 3590*time.Second || got > 3591*time.Second {
					t.Errorf("registration grant = %s, want own 3590 seconds", got)
				}
			}
			sms, err := session.EnableSMS(context.Background())
			if test.stripSMS || test.foreignOnly {
				if !errors.Is(err, ErrSMSCapabilityNotConfirmed) || sms.Ready {
					t.Fatalf("foreign Contact granted SMS readiness: %#v, %v", sms, err)
				}
			} else if err != nil || !sms.Ready {
				t.Fatalf("own echoed Contact did not confirm SMS: %#v, %v", sms, err)
			}
		})
	}
}

type contactPolicyAddressConn struct {
	*fakeConn
	local net.Addr
}

func (connection *contactPolicyAddressConn) LocalAddr() net.Addr { return connection.local }

func TestRegistrationContactCorrelationAcceptsOnlyBindingEvidence(t *testing.T) {
	for _, name := range []string{
		"rewritten_URI_uppercase_UUID",
		"expanded_IPv6",
		"URI_parameter_order_first",
		"URI_parameter_order_last",
		"user_case_is_significant_with_zero_IMEI",
		"instance_in_display_name",
		"instance_in_pub_gruu",
		"empty_instance",
		"ambiguous_instance",
		"ambiguous_instance_with_own_URI",
	} {
		t.Run(name, func(t *testing.T) {
			request := vowifi.IMSRequest{DeviceID: "reader", Identity: vowifi.SIMIdentity{
				IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01",
			}}
			if name == "expanded_IPv6" || strings.HasPrefix(name, "URI_parameter_order_") || name == "user_case_is_significant_with_zero_IMEI" {
				request = lebaraContactTestRequest()
			}
			session := newStableInstanceTestSession(t, request)
			if name == "expanded_IPv6" {
				session.conn = &contactPolicyAddressConn{fakeConn: &fakeConn{}, local: &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 5060}}
			}
			if name == "empty_instance" {
				// Older internal callers can construct a Session without an instance.
				session.instanceID = ""
			}
			own := session.buildContact(session.contactAddress(), session.imsRegisterOptions()) + ";expires=600"
			instance := contactPolicyTestInstance(t, own)
			foreign := `<sip:rewritten@198.51.100.20:5060;transport=tcp>;+g.3gpp.smsip;expires=7200`
			contacts, wanted := []string{foreign}, ""
			switch name {
			case "rewritten_URI_uppercase_UUID":
				foreign += `;+sip.instance="<` + strings.ToUpper(instance) + `>"`
				contacts, wanted = []string{foreign}, foreign
			case "expanded_IPv6":
				own = strings.Replace(own, "[2001:db8::1]", "[2001:0DB8:0000:0000:0000:0000:0000:0001]", 1)
				contacts, wanted = []string{own}, own
			case "URI_parameter_order_first", "URI_parameter_order_last":
				parameters := ";ob;TRANSPORT=TCP>"
				if name == "URI_parameter_order_last" {
					parameters = ";TRANSPORT=TCP;ob>"
				}
				own = strings.Replace(own, ";transport=tcp>", parameters, 1)
				contacts, wanted = []string{own}, own
			case "user_case_is_significant_with_zero_IMEI":
				uri := contactPolicyTestURI(t, own)
				own = strings.Replace(own, uri, strings.ToUpper(uri), 1)
				contacts = []string{own}
			case "instance_in_display_name":
				contacts = []string{`"<` + instance + `>" ` + foreign}
			case "instance_in_pub_gruu":
				contacts = []string{foreign + `;pub-gruu="sip:gruu@example.test;opaque=` + instance + `"`}
			case "empty_instance":
				contacts = []string{foreign + `;+sip.instance="<>"`}
			case "ambiguous_instance", "ambiguous_instance_with_own_URI":
				foreign += `;+sip.instance="<` + instance + `>"`
				other := strings.Replace(foreign, "198.51.100.20", "198.51.100.21", 1)
				contacts = []string{foreign, other}
				if name == "ambiguous_instance_with_own_URI" {
					// Even a URI-only echo disambiguates shared nonzero instances.
					own = strings.Replace(own, `;+sip.instance="<`+instance+`>"`, "", 1)
					contacts, wanted = []string{foreign, own, other}, own
				}
			}
			response := &sipResponse{StatusCode: 200, Headers: map[string][]string{"contact": contacts}}
			if err := session.applyRegistrationEvidence(response); err != nil {
				t.Fatal(err)
			}
			if got := session.Evidence().RegisteredContact; got != wanted {
				t.Fatalf("selected Contact = %q, want %q", got, wanted)
			}
			sms, err := session.EnableSMS(context.Background())
			if wanted == "" {
				if !errors.Is(err, ErrSMSCapabilityNotConfirmed) || sms.Ready {
					t.Fatalf("unmatched binding confirmed SMS: %#v, %v", sms, err)
				}
			} else if err != nil || !sms.Ready {
				t.Fatalf("matched binding did not confirm SMS: %#v, %v", sms, err)
			}
		})
	}
}

func TestLegacyGSMARegistrationAcceptsAddedTransportWithoutInstance(t *testing.T) {
	session := newStableInstanceTestSession(t, vowifi.IMSRequest{
		DeviceID: "legacy-gsma-reader",
		Identity: vowifi.SIMIdentity{IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF"},
	})
	if session.imsRegisterOptions().ContactFormat != vowifi.IMSContactFormatGSMA {
		t.Fatal("fixture must select the existing GSMA profile")
	}
	contact := "<sip:" + session.contactAddress() + ";transport=tcp>;+g.3gpp.smsip;expires=600"
	if err := session.applyRegistrationEvidence(&sipResponse{StatusCode: 200, Headers: map[string][]string{"contact": {contact}}}); err != nil {
		t.Fatal(err)
	}
	if session.Evidence().RegisteredContact != contact {
		t.Fatal("legacy GSMA registrar's explicit transport prevented Contact correlation")
	}
	if sms, err := session.EnableSMS(context.Background()); err != nil || !sms.Ready {
		t.Fatalf("legacy GSMA Contact did not confirm SMS: %#v, %v", sms, err)
	}
}

func TestLebaraContactProtectedRegistrationRefreshAndDeregister(t *testing.T) {
	localIP, remoteIP := net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.2")
	initial, err := net.ListenTCP("tcp", &net.TCPAddr{IP: remoteIP})
	if err != nil {
		t.Skipf("secondary loopback address is unavailable: %v", err)
	}
	defer initial.Close()
	protected, err := net.ListenTCP("tcp", &net.TCPAddr{IP: remoteIP})
	if err != nil {
		t.Fatal(err)
	}
	defer protected.Close()
	for _, listener := range []*net.TCPListener{initial, protected} {
		if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	ueClientPort, err := availableProtectedPort(localIP, 0)
	if err != nil {
		t.Fatal(err)
	}
	ueServerPort, err := availableProtectedPort(localIP, ueClientPort)
	if err != nil {
		t.Fatal(err)
	}
	pcscfClientPort, err := availableProtectedPort(remoteIP, protected.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	res := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveLebaraContactRegistrar(initial, protected, pcscfClientPort, ueServerPort, nonce, res)
	}()
	installer := &fakeIPSecInstaller{}
	provider, err := NewProvider(&recordingAKA{result: vowifi.AKAResult{
		RES: res,
		CK:  []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		IK:  []byte{16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31},
	}}, Config{
		PCSCF: initial.Addr().String(), LocalAddress: localIP.String(), Transport: "tcp",
		SecurityMode: SecurityRequired, TransactionTimeout: 3 * time.Second,
		IPSecInstaller: installer, ProtectedClientPort: ueClientPort, ProtectedServerPort: ueServerPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := lebaraContactTestRequest()
	request.Tunnel = evidenceTunnel{evidence: vowifi.TunnelEvidence{
		Established: true, LocalIPv4: localIP.String(), PCSCF: []string{initial.Addr().String()},
	}}
	registered, err := provider.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("protected registration failed: %v", err)
	}
	defer registered.Close(context.Background())
	session := registered.(*Session)
	if err := session.refreshOnce(context.Background()); err != nil {
		t.Fatalf("protected refresh failed: %v", err)
	}
	if evidence := registered.Evidence(); !evidence.Registered || !evidence.SecurityVerified || evidence.RegisteredContact == "" {
		t.Fatalf("protected registration evidence = %#v", evidence)
	}
	if sms, err := registered.EnableSMS(context.Background()); err != nil || !sms.Ready {
		t.Fatalf("SMS capability after protected refresh = %#v, %v", sms, err)
	}
	if err := registered.Close(context.Background()); err != nil {
		t.Fatalf("protected deregistration failed: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("protected registrar: %v", err)
	}
	if installer.handle == nil || installer.handle.closes() != 1 {
		t.Fatalf("IPsec cleanup handle = %#v", installer.handle)
	}
}

// This registrar accepts only the carrier-specific Contact on every REGISTER,
// while verifying that AKA authentication still uses the private IMS identity.
func serveLebaraContactRegistrar(initial, protected *net.TCPListener, pcscfClientPort, ueServerPort int, nonce string, res []byte) error {
	initialConn, err := initial.AcceptTCP()
	if err != nil {
		return err
	}
	defer initialConn.Close()
	_ = initialConn.SetDeadline(time.Now().Add(5 * time.Second))
	packet, err := readTestTCPRequest(bufio.NewReader(initialConn))
	if err != nil {
		return err
	}
	_, initialHeaders, err := parseTestRequest(packet)
	if err != nil {
		return err
	}
	contact := initialHeaders["contact"]
	wantContactURI := regexp.MustCompile(`^<sip:[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}@127\.0\.0\.1:` + strconv.Itoa(ueServerPort) + `;transport=tcp>`)
	if !wantContactURI.MatchString(contact) || !strings.Contains(contact, `+sip.instance="<urn:gsma:imei:00000000-000000-0>"`) {
		return fmt.Errorf("initial Lebara Contact = %q", contact)
	}
	offers := splitHeaderValues([]string{initialHeaders["security-client"]})
	if len(offers) == 0 {
		return errors.New("missing Security-Client")
	}
	proposal, err := parseSecurityMechanism(offers[0])
	if err != nil {
		return err
	}
	clientSPI, serverSPI := nonCollidingServerSPIs(proposal.spiClient, proposal.spiServer)
	securityServer := fmt.Sprintf("ipsec-3gpp;q=0.100;alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=aes-cbc;spi-c=%d;spi-s=%d;port-c=%d;port-s=%d", clientSPI, serverSPI, pcscfClientPort, protected.Addr().(*net.TCPAddr).Port)
	const realm = "ims.mnc087.mcc234.3gppnetwork.org"
	const private = "234870123456789@" + realm
	if _, err := initialConn.Write(testResponse(401, "Unauthorized", initialHeaders["call-id"], initialHeaders["cseq"], []string{
		`WWW-Authenticate: Digest realm="` + realm + `", nonce="` + nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
		"Security-Server: " + securityServer,
	})); err != nil {
		return err
	}
	_ = initialConn.Close()
	protectedConn, err := protected.AcceptTCP()
	if err != nil {
		return err
	}
	defer protectedConn.Close()
	_ = protectedConn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(protectedConn)
	for index, expires := range []string{"3600", "3600", "0"} {
		packet, err := readTestTCPRequest(reader)
		if err != nil {
			return err
		}
		start, headers, err := parseTestRequest(packet)
		if err != nil {
			return err
		}
		if start != "REGISTER sip:"+realm+" SIP/2.0" || headers["contact"] != contact || headers["expires"] != expires || headers["from"] != initialHeaders["from"] || headers["to"] != "<sip:"+private+">" {
			return fmt.Errorf("protected REGISTER %d changed identity/Contact/lifetime: %q, headers %#v", index, start, headers)
		}
		if headers["security-verify"] != securityServer || headers["security-client"] != initialHeaders["security-client"] {
			return fmt.Errorf("protected REGISTER %d changed security agreement", index)
		}
		directives, err := testDigestDirectives(headers["authorization"])
		if err != nil {
			return err
		}
		wantResponse := digestResponse(private, realm, res, "REGISTER", "sip:"+realm, nonce, directives["nc"], directives["cnonce"], "auth")
		if directives["username"] != private || directives["realm"] != realm || directives["response"] != wantResponse || directives["nc"] != fmt.Sprintf("%08x", index+1) || directives["integrity-protected"] != "yes" {
			return fmt.Errorf("protected REGISTER %d changed AKA authorization: %#v", index, directives)
		}
		headers200 := []string{"Contact: " + contact + ";expires=600"}
		if expires == "0" {
			headers200 = nil
		}
		if _, err := protectedConn.Write(testResponse(200, "OK", headers["call-id"], headers["cseq"], headers200)); err != nil {
			return err
		}
	}
	return nil
}
