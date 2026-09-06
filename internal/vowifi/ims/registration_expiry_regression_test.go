package ims

import (
	"testing"
	"time"
	"vocat/internal/vowifi"
)

func TestRegistrationExpiryUsesMatchingContact(t *testing.T) {
	s := &Session{provider: &Provider{config: Config{SecurityMode: SecurityDisabled, RegistrationExpiry: time.Hour}}, conn: &fakeConn{}, transport: "tcp", identity: identitySet{user: "001010123456789"}, instanceID: "urn:uuid:current", request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{HomeMCC: "001", HomeMNC: "01"}}}
	r := &sipResponse{StatusCode: 200, Headers: map[string][]string{"contact": {`<sip:old@10.0.0.1:5060>;expires=7200;+g.3gpp.smsip;+sip.instance="<urn:uuid:old>",<sip:current@192.0.2.10:5060>;expires=3590;+g.3gpp.smsip;+sip.instance="<urn:uuid:current>"`}}}
	before := time.Now()
	if err := s.applyRegistrationEvidence(r); err != nil {
		t.Fatal(err)
	}
	got := s.expiresAt.Sub(before)
	if got < 3590*time.Second || got > 3591*time.Second {
		t.Fatalf("current Contact grants 3590 seconds but session expiry is %s", got)
	}
}
