package ims

import (
	"strings"
	"testing"
)

func TestProtectedReregisterRetainsAuthorizationIdentity(t *testing.T) {
	s := &Session{provider: &Provider{config: Config{SecurityMode: SecurityRequired}}, conn: &fakeConn{}, transport: "tcp", identity: identitySet{user: "001010123456789", private: "001010123456789@ims.example.test", public: "sip:001010123456789@ims.example.test", domain: "ims.example.test"}, endpoint: pcscfEndpoint{host: "192.0.2.20", port: 5060}, securityActive: true, instanceID: "urn:uuid:test", callID: "test", fromTag: "test"}
	for _, expires := range []int{0, 3600} {
		b, err := s.buildRegister(3, expires, "", "")
		if err != nil {
			t.Fatal(err)
		}
		p, err := parseSIPPacket(b)
		if err != nil {
			t.Fatal(err)
		}
		a := p.Request.value("Authorization")
		if !strings.Contains(a, `username="001010123456789@ims.example.test"`) || !strings.Contains(a, "integrity-protected=yes") {
			t.Errorf("protected REGISTER expires=%d missing private identity/integrity indication; Authorization=%q", expires, a)
		}
	}
}
