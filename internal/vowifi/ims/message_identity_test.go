package ims

import (
	"bytes"
	"context"
	"fmt"
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
