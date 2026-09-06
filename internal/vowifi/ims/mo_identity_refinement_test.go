package ims

import (
	"context"
	"strings"
	"testing"
	"time"
	"vocat/internal/vowifi"
)

func TestMOTemporaryPreferredIdentityCannotBypassPolicy(t *testing.T) {
	for _, usableDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_default", true: "usable_default"}[usableDefault], func(t *testing.T) {
			s, seen, _ := smsPSITestSession(t, &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}})
			current := s.identity.public
			const valid = "sip:12025550100@msg.example.test"
			s.mu.Lock()
			s.evidence.AssociatedIdentities = []string{"<" + current + ">"}
			if usableDefault {
				s.evidence.AssociatedIdentities = []string{"<" + valid + ">", "<" + current + ">"}
			}
			s.mu.Unlock()
			_, err := s.sendSIPMessageWithIdentity(context.Background(), smsPSITestURI, []byte{0, 0}, "", smsContentType, "smsip", current)
			if !usableDefault {
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

func TestMOTemporaryIdentityRequiresUsableDefaultBeforeSend(t *testing.T) {
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
			s, seen, _ := smsPSITestSession(t, &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}})
			s.mu.Lock()
			s.evidence.AssociatedIdentities = tc.associated
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
		})
	}
}

func TestMOConfiguredIdentityRetainsLegacySelection(t *testing.T) {
	// Deliberately identical in spelling to a generated identity: provenance,
	// not an IMSI-shaped URI or operator, must decide the policy.
	const current = "sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org"
	for _, tc := range []struct {
		name       string
		associated []string
	}{
		{"current_second", []string{"<sip:12025550100@msg.example.test>", "<" + current + ">"}},
		{"current_absent", []string{"<sip:12025550100@msg.example.test>"}},
		{"no_associated", nil},
		{"unusable_associated", []string{"not-a-uri"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, seen, _ := smsPSITestSession(t, &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}, current)
			s.mu.Lock()
			s.evidence.AssociatedIdentities = tc.associated
			s.mu.Unlock()
			want, _ := messagePublicIdentity(current, "", tc.associated)
			r := smsPSITestSubmit(t, s, seen)
			if got := publicIdentityURI(r.value("From")); got != want {
				t.Errorf("From=%q want legacy %q", got, want)
			}
			if got := publicIdentityURI(r.value("P-Preferred-Identity")); got != want {
				t.Errorf("PPI=%q want legacy %q", got, want)
			}
		})
	}
}
