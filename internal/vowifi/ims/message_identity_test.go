package ims

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestMessagePublicIdentity(t *testing.T) {
	const current = "sip:001010123456789@ims.example"
	const called = "sip:Alice@ims.example"
	tests := []struct {
		name       string
		preferred  string
		associated []string
		want       string
		source     string
	}{
		{"called identity replaces barred registration identity", "<" + called + ">", []string{"<tel:+64221234567>", "<" + called + ">"}, called, "called_party"},
		{"called identity preferred even when current is associated", called, []string{current, called}, called, "called_party"},
		{"missing called identity retains associated current", "", []string{called, current}, current, "associated_current"},
		{"unassociated called identity retains associated current", "sip:other@ims.example", []string{current}, current, "associated_current"},
		{"barred current uses first associated identity", "", []string{"<tel:+64221234567>", called}, "tel:+64221234567", "associated_default"},
		{"unassociated called identity uses default", "sip:other@ims.example", []string{called}, called, "associated_default"},
		{"missing associated list keeps compatibility", called, nil, current, "configured_fallback"},
		{"empty associated list keeps compatibility", called, []string{""}, current, "configured_fallback"},
		{"SIP userinfo is case sensitive", "sip:alice@ims.example", []string{current, called}, current, "associated_current"},
		{"scheme and host are case insensitive", "SIP:Alice@IMS.EXAMPLE", []string{current, called}, called, "called_party"},
		{"SIPS is distinct from SIP", "sips:Alice@ims.example", []string{current, called}, current, "associated_current"},
		{"quoted comma and header parameters", `"Doe, Alice" <` + called + `>;x=1`, []string{current, `"Doe, Alice" <` + called + `>;x=2`}, called, "called_party"},
		{"multiple associated values", called, []string{"<" + current + ">, <" + called + ">"}, called, "called_party"},
		{"TEL identity", "<tel:+64221234567>", []string{current, "<tel:+64221234567>"}, "tel:+64221234567", "called_party"},
		{"URI parameters preserved", "<" + called + ";user=phone>", []string{current, "<" + called + ";user=phone>"}, called + ";user=phone", "called_party"},
		{"bare URI parameters preserved", called + ";user=phone", []string{current, "<" + called + ";user=phone>"}, called + ";user=phone", "called_party"},
		{"URI parameters not discarded", called + ";user=phone", []string{current, called}, current, "associated_current"},
		{"multiple called identities rejected", current + "," + called, []string{current, called}, current, "associated_current"},
		{"malformed called identity rejected", "<" + called, []string{current, called}, current, "associated_current"},
		{"header injection rejected", "<" + called + ">\r\nX: y", []string{current, called}, current, "associated_current"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := messagePublicIdentity(current, tt.preferred, tt.associated)
			if got != tt.want || source != tt.source {
				t.Fatalf("identity = (%q, %q), want (%q, %q)", got, source, tt.want, tt.source)
			}
		})
	}
}

type identityTestConn struct {
	fakeConn
	onWrite func([]byte) (int, error)
}

func (conn *identityTestConn) Write(packet []byte) (int, error) { return conn.onWrite(packet) }

func TestDeliveryReportUsesAssociatedIdentityOnWire(t *testing.T) {
	const registered = "sip:001010123456789@ims.example"
	const defaultIdentity = "sip:default@ims.example"
	const called = "sip:Alice@ims.example"
	for _, report := range [][]byte{{0x02, 0x2a}, buildRPError(0x2a, 95)} {
		t.Run(fmt.Sprintf("report_%x", report[0]), func(t *testing.T) {
			conn := &identityTestConn{}
			session := &Session{
				provider: &Provider{config: Config{TransactionTimeout: time.Second}},
				identity: identitySet{public: registered},
				evidence: vowifi.IMSEvidence{AssociatedIdentities: []string{defaultIdentity, called}},
				conn:     conn, transport: "tcp", fromTag: "test-tag", cseq: 1,
				transactions: make(map[sipTransactionKey]chan *sipResponse),
			}
			var sent []*sipRequest
			conn.onWrite = func(packet []byte) (int, error) {
				parsed, err := parseSIPPacket(packet)
				if err != nil || parsed.Request == nil {
					return 0, fmt.Errorf("parse outgoing MESSAGE: %v", err)
				}
				request := parsed.Request
				sent = append(sent, request)
				session.dispatchPacket(sipPacket{Response: &sipResponse{
					StatusCode: 202,
					Headers: map[string][]string{
						"call-id": {request.value("Call-ID")},
						"cseq":    {request.value("CSeq")},
					},
				}}, nil)
				return len(packet), nil
			}
			inbound := &sipRequest{Headers: map[string][]string{
				"p-asserted-identity": {"<sip:ipsmgw@example.test>"},
				"from":                {"<sip:other-gateway@example.test>;tag=gw"},
				"p-called-party-id":   {"<SIP:Alice@IMS.EXAMPLE>"},
				"call-id":             {"inbound-sms"},
			}}
			if err := session.sendDeliveryReport(inbound, report); err != nil {
				t.Fatal(err)
			}
			ack := sent[0]
			if ack.value("From") != "<"+called+">;tag=test-tag" || ack.value("P-Preferred-Identity") != "<"+called+">" {
				t.Fatalf("wrong delivery report identity: %v", ack.Headers)
			}
			if ack.URI != "sip:ipsmgw@example.test" || ack.value("To") != "<sip:ipsmgw@example.test>" || ack.value("In-Reply-To") != "inbound-sms" || !bytes.Equal(ack.Body, report) {
				t.Fatalf("delivery report routing/correlation/payload changed: %#v", ack)
			}
			// A per-message choice must not leak into MO SMS or USSI, and a
			// refreshed registration must immediately affect subsequent requests.
			for _, contentType := range []string{smsContentType, ussiContentType} {
				if _, err := session.sendSIPMessageWith(context.Background(), "sip:service@example.test", []byte("test"), "", contentType, ""); err != nil {
					t.Fatal(err)
				}
				message := sent[len(sent)-1]
				if message.value("From") != "<"+defaultIdentity+">;tag=test-tag" || message.value("P-Preferred-Identity") != "<"+defaultIdentity+">" || message.value("Content-Type") != contentType {
					t.Fatalf("wrong originating MESSAGE identity/content type: %v", message.Headers)
				}
			}
			session.evidence.AssociatedIdentities = []string{registered}
			if err := session.sendDeliveryReport(inbound, report); err != nil {
				t.Fatal(err)
			}
			if message := sent[len(sent)-1]; message.value("P-Preferred-Identity") != "<"+registered+">" {
				t.Fatalf("stale associated identity after refresh: %v", message.Headers)
			}
			if session.identity.public != registered {
				t.Fatal("MESSAGE selection changed the REGISTER identity")
			}
		})
	}
}

func TestMOSMSIdentityProvenanceOnWire(t *testing.T) {
	// The same URI spelling is REGISTER-only when generated, but remains an
	// eligible originating identity when explicitly configured.
	const current = "sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org"
	const networkDefault = "sip:12025550100@msg.example.test"
	for _, tc := range []struct {
		name, configured, want string
	}{
		{"generated", "", networkDefault},
		{"configured", current, current},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, seen, _ := smsPSITestSession(t, &smsPSITestAKA{recordingAKA: &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}, psi: smsPSITestURI}, tc.configured)
			if s.identity.public != current {
				t.Fatalf("fixture identity = %q, want %q", s.identity.public, current)
			}
			s.mu.Lock()
			s.evidence.AssociatedIdentities = []string{"<" + networkDefault + ">", "<" + current + ">"}
			s.mu.Unlock()
			r := smsPSITestSubmit(t, s, seen)
			if publicIdentityURI(r.value("From")) != tc.want || publicIdentityURI(r.value("P-Preferred-Identity")) != tc.want {
				t.Fatalf("wrong origin: From=%q PPI=%q", r.value("From"), r.value("P-Preferred-Identity"))
			}
			if r.URI != smsPSITestURI {
				t.Fatal("PSI target changed")
			}
		})
	}
}

func TestMOIdentityChangeDoesNotAffectRepliesOrUSSD(t *testing.T) {
	for _, test := range []struct {
		name, reply, content, preferred string
	}{
		{"reply", "network-call", smsContentType, ""},
		{"explicit_identity", "", smsContentType, "current"},
		{"ussd", "", ussiContentType, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			// An eligible explicitly configured identity is not the generated
			// REGISTER-only identity. Replies/USSD still exercise generated sessions.
			configured := ""
			if test.preferred != "" {
				configured = "sip:subscriber@msg.example.test"
			}
			s, seen, _ := smsPSITestSession(t, &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}, configured)
			current := s.identity.public
			s.mu.Lock()
			s.evidence.AssociatedIdentities = []string{"<sip:12025550100@msg.example.test>", "<" + current + ">"}
			s.mu.Unlock()
			preferred := test.preferred
			if preferred == "current" {
				preferred = current
			}
			_, e := s.sendSIPMessageWithIdentity(context.Background(), smsPSITestURI, []byte{3, 0}, test.reply, test.content, "smsip", preferred)
			if e != nil {
				t.Fatal(e)
			}
			select {
			case r := <-seen:
				if !strings.Contains(r.value("From"), current) || !strings.Contains(r.value("P-Preferred-Identity"), current) {
					t.Fatal("non-MO identity changed")
				}
			case <-time.After(time.Second):
				t.Fatal("no message")
			}
		})
	}
}

func TestMOTemporaryPreferredIdentityCannotBypassPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		usableDefault bool
	}{
		{"no_default", false},
		{"usable_default", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, seen, _ := smsPSITestSession(t, &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}})
			current := s.identity.public
			const valid = "sip:12025550100@msg.example.test"
			s.mu.Lock()
			s.evidence.AssociatedIdentities = []string{"<" + current + ">"}
			if tc.usableDefault {
				s.evidence.AssociatedIdentities = []string{"<" + valid + ">", "<" + current + ">"}
			}
			s.mu.Unlock()
			_, err := s.sendSIPMessageWithIdentity(context.Background(), smsPSITestURI, []byte{0, 0}, "", smsContentType, "smsip", current)
			if !tc.usableDefault {
				if err == nil {
					t.Error("explicit preferred temporary identity bypassed error")
				}
				select {
				case <-seen:
					t.Error("sent forbidden MO MESSAGE")
				case <-time.After(30 * time.Millisecond):
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-seen:
				if publicIdentityURI(r.value("From")) != valid || publicIdentityURI(r.value("P-Preferred-Identity")) != valid {
					t.Errorf("preferred temporary leaked: %v", r.Headers)
				}
			case <-time.After(time.Second):
				t.Fatal("no MESSAGE")
			}
		})
	}
}

func TestOriginatingSMSPublicIdentityRejectsUnusableDefault(t *testing.T) {
	const temporary = "sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org"
	const valid = "sip:12025550100@msg.example.test"
	for _, tc := range []struct {
		name       string
		associated []string
	}{
		{"absent", nil},
		{"same_temporary", []string{"<" + temporary + ">"}},
		{"temporary_uri_parameter", []string{"<" + temporary + ";user=phone>"}},
		{"temporary_escaped_digit", []string{"<sip:%3001010123456789@ims.mnc001.mcc001.3gppnetwork.org>"}},
		{"same_temporary_case", []string{"<SIP:001010123456789@IMS.MNC001.MCC001.3GPPNETWORK.ORG>"}},
		{"temporary_first_not_second", []string{"<" + temporary + ">, <" + valid + ">"}},
		{"invalid_first_not_second", []string{"garbage", "<" + valid + ">"}},
		{"invalid_sip", []string{"<sip:a@@example.test>"}},
		{"invalid_tel", []string{"<tel:not-a-number>"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := originatingSMSPublicIdentity(temporary, tc.associated)
			if err == nil || got != "" || !strings.Contains(err.Error(), "public identity") {
				t.Fatalf("identity = %q, %v; want rejection", got, err)
			}
		})
	}
}

func TestMOTemporaryIdentityWithoutDefaultNeverSends(t *testing.T) {
	s, seen, _ := smsPSITestSession(t, &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}})
	s.mu.Lock()
	s.evidence.AssociatedIdentities = nil
	s.mu.Unlock()
	result, err := s.SendSMS(context.Background(), vowifi.SMSSubmitRequest{Recipient: "+12025550123", Text: "OFFLINE TEST"})
	if err == nil || !strings.Contains(err.Error(), "public identity") {
		t.Errorf("want identity error, got %v", err)
	}
	if result.PartsAttempted != 0 {
		t.Errorf("attempted %d parts without usable default", result.PartsAttempted)
	}
	select {
	case r := <-seen:
		t.Errorf("sent forbidden MO MESSAGE: From=%q", r.value("From"))
	case <-time.After(30 * time.Millisecond):
	}
}
