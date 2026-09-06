package ims

import (
	"context"
	"strings"
	"testing"
	"time"
	"vocat/internal/vowifi"
)

func TestMOSMSUsesDefaultAssociatedIdentity(t *testing.T) {
	s, seen, _ := smsPSITestSession(t, &smsPSITestAKA{recordingAKA: &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}, psi: smsPSITestURI})
	first := "sip:12025550100@msg.example.test"
	current := s.identity.public
	s.mu.Lock()
	s.evidence.AssociatedIdentities = []string{"<" + first + ">", "<" + current + ">"}
	s.mu.Unlock()
	r := smsPSITestSubmit(t, s, seen)
	if publicIdentityURI(r.value("From")) != first || publicIdentityURI(r.value("P-Preferred-Identity")) != first {
		t.Fatalf("MO uses wrong origin: From=%s PPI=%s", r.value("From"), r.value("P-Preferred-Identity"))
	}
	if r.URI != smsPSITestURI {
		t.Fatal("PSI target changed")
	}
}
func TestMOIdentityChangeDoesNotAffectRepliesOrUSSD(t *testing.T) {
	for _, test := range []struct{ name, reply, content, preferred string }{{"reply", "network-call", smsContentType, ""}, {"explicit_identity", "", smsContentType, "current"}, {"ussd", "", "application/vnd.3gpp.ussd+xml", ""}} {
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
