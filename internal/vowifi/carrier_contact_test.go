package vowifi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuiltinCarrierOverridesLoadIndependently(t *testing.T) {
	rules, err := loadCarrierProfiles(carrierOverridesJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.ID != "lebara-uk-23487" {
			continue
		}
		identity := SIMIdentity{HomeMCC: "234", HomeMNC: "87", SPN: "Lebara"}
		_, _, matched := matchCarrierProfileRule(rule, identity)
		if !matched || !rule.Match.RequireHomePLMN ||
			rule.IMS.RegisterOptions.InstanceIDFallback != IMSInstanceIDFallbackZeroIMEI {
			t.Fatalf("curated Lebara override lost its selector or Contact policy: %#v", rule)
		}
		return
	}
	t.Fatal("Lebara Contact policy is missing from curated carrier overrides")
}

func TestResolveCarrierProfileLebaraContactPolicy(t *testing.T) {
	for _, identity := range []SIMIdentity{
		{HomeMCC: "234", HomeMNC: "87", SPN: "Lebara"},
		{HomeMCC: " 234 ", HomeMNC: " 87 ", SPN: " lEbArA "},
		{HomeMCC: "234", HomeMNC: "087", SPN: "LEBARA"},
	} {
		got := ResolveCarrierProfile(identity)
		want := defaultCarrierProfile()
		want.ID = "lebara-uk-23487"
		want.MatchSource = "hplmn+spn"
		want.RouteMCC = "234"
		want.RouteMNC = identity.HomeMNC
		if identity.HomeMNC != "087" {
			want.RouteMNC = "87"
		}
		want.IMSRegisterOptions = IMSRegisterOptions{
			ContactFormat:      IMSContactFormatStandard,
			ContactUserSource:  IMSContactUserStableInstance,
			InstanceIDFormat:   IMSInstanceIDFormatGSMAIMEI,
			InstanceIDFallback: IMSInstanceIDFallbackZeroIMEI,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Lebara profile for %#v = %#v, want %#v", identity, got, want)
		}
	}
	for _, identity := range []SIMIdentity{
		{HomeMCC: "234", HomeMNC: "87"},
		{HomeMCC: "234", HomeMNC: "87", SPN: "Other"},
		{HomeMCC: "999", HomeMNC: "01", SPN: "Lebara"},
		{SPN: "Lebara"},
		{HomeMCC: "234", SPN: "Lebara"},
		{HomeMNC: "87", SPN: "Lebara"},
	} {
		profile := ResolveCarrierProfile(identity)
		options := profile.IMSRegisterOptions
		if profile.ID == "lebara-uk-23487" || options.ContactUserSource != "" ||
			options.InstanceIDFormat != "" || options.InstanceIDFallback != "" {
			t.Fatalf("Lebara Contact policy leaked to %#v: %#v", identity, profile)
		}
	}
}

func TestCarrierContactPolicyJSONRoundTrip(t *testing.T) {
	for _, options := range []carrierProfileRegisterOptions{
		{},
		{ContactUserSource: " PUBLIC-IDENTITY ", InstanceIDFormat: " UUID ", InstanceIDFallback: " UUID "},
		{ContactUserSource: " STABLE-INSTANCE ", InstanceIDFormat: " GSMA-IMEI ", InstanceIDFallback: " UUID "},
		{ContactUserSource: " STABLE-INSTANCE ", InstanceIDFormat: " GSMA-IMEI ", InstanceIDFallback: " ZERO-IMEI "},
	} {
		encoded, err := json.Marshal(carrierProfileDocument{
			Version: CarrierProfileSchemaVersion,
			Profiles: []carrierProfileRule{{
				ID:    "contact-policy",
				Match: carrierProfileMatch{HomePLMNs: []string{"99901"}, RequireHomePLMN: true},
				IMS:   carrierProfileIMS{RegisterOptions: options},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		rules, err := loadCarrierProfiles(encoded)
		if err != nil {
			t.Fatalf("load policy %#v: %v", options, err)
		}
		if !reflect.DeepEqual(rules[0].IMS.RegisterOptions, options) || !rules[0].Match.RequireHomePLMN {
			t.Fatalf("JSON policy did not survive round trip: %s", encoded)
		}
		got := applyCarrierProfileRule(defaultCarrierProfile(), rules[0], "hplmn", SIMIdentity{}).IMSRegisterOptions
		want := IMSRegisterOptions{}
		if options.ContactUserSource == " PUBLIC-IDENTITY " {
			want.ContactUserSource = IMSContactUserPublicIdentity
			want.InstanceIDFormat = IMSInstanceIDFormatUUID
			want.InstanceIDFallback = IMSInstanceIDFallbackUUID
		} else if options.ContactUserSource != "" {
			want.ContactUserSource = IMSContactUserStableInstance
			want.InstanceIDFormat = IMSInstanceIDFormatGSMAIMEI
			want.InstanceIDFallback = IMSInstanceIDFallbackUUID
			if options.InstanceIDFallback == " ZERO-IMEI " {
				want.InstanceIDFallback = IMSInstanceIDFallbackZeroIMEI
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resolved Contact policy = %#v, want %#v", got, want)
		}
	}
}

func TestCarrierContactPolicyRejectsInvalidConfiguration(t *testing.T) {
	for _, options := range []carrierProfileRegisterOptions{
		{ContactUserSource: "username"},
		{InstanceIDFormat: "imei"},
		{InstanceIDFallback: "zero"},
		{InstanceIDFallback: IMSInstanceIDFallbackZeroIMEI},
		{InstanceIDFormat: IMSInstanceIDFormatUUID, InstanceIDFallback: IMSInstanceIDFallbackZeroIMEI},
		{ContactUserSource: "stable-instance\r\nInjected: yes"},
	} {
		encoded, err := json.Marshal(carrierProfileDocument{
			Version: CarrierProfileSchemaVersion,
			Profiles: []carrierProfileRule{{
				ID: "invalid-contact-policy", Match: carrierProfileMatch{HomePLMNs: []string{"99901"}},
				IMS: carrierProfileIMS{RegisterOptions: options},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadCarrierProfiles(encoded); err == nil {
			t.Fatalf("invalid Contact policy accepted: %s", encoded)
		}
	}
	if _, err := loadCarrierProfiles([]byte(`{"version":1,"profiles":[{"id":"missing-home-plmn","match":{"require_home_plmn":true,"spns":["Lebara"]}}]}`)); err == nil {
		t.Fatal("require_home_plmn without home_plmns accepted")
	}
}

func TestCarrierContactPolicyExternalDirectory(t *testing.T) {
	externalCarrierProfiles.RLock()
	previous := append([]carrierProfileRule(nil), externalCarrierProfiles.rules...)
	externalCarrierProfiles.RUnlock()
	t.Cleanup(func() {
		externalCarrierProfiles.Lock()
		externalCarrierProfiles.rules = previous
		externalCarrierProfiles.Unlock()
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "contact-policy.json")
	encoded := []byte(`{"version":1,"profiles":[{"id":"installed-lebara-contact-policy","match":{"home_plmns":["23487"],"require_home_plmn":true,"spns":["Lebara"]},"ims":{"register_options":{"contact_format":" STANDARD ","contact_user_source":" PUBLIC-IDENTITY ","instance_id_format":" UUID ","instance_id_fallback":" UUID "}}}]}`)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadCarrierProfileDirectory(dir); err != nil {
		t.Fatal(err)
	}
	identity := SIMIdentity{HomeMCC: "234", HomeMNC: "87", SPN: "Lebara"}
	got := ResolveCarrierProfile(identity)
	want := IMSRegisterOptions{
		ContactFormat: IMSContactFormatStandard, ContactUserSource: IMSContactUserPublicIdentity,
		InstanceIDFormat: IMSInstanceIDFormatUUID, InstanceIDFallback: IMSInstanceIDFallbackUUID,
	}
	if got.ID != "installed-lebara-contact-policy" || !reflect.DeepEqual(got.IMSRegisterOptions, want) {
		t.Fatalf("installed Contact policy did not override builtin: %#v", got)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"profiles":[{"id":"invalid","match":{"home_plmns":["23487"]},"ims":{"register_options":{"instance_id_fallback":"zero-imei"}}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadCarrierProfileDirectory(dir); err == nil {
		t.Fatal("invalid external Contact policy was accepted")
	}
	if after := ResolveCarrierProfile(identity); !reflect.DeepEqual(after, got) {
		t.Fatalf("failed directory reload discarded installed Contact policy: %#v", after)
	}
}

func TestRequireHomePLMNPreservesLegacySelectorMatching(t *testing.T) {
	match := carrierProfileMatch{HomePLMNs: []string{"23487"}, SPNs: []string{"Lebara"}}
	identity := SIMIdentity{SPN: "Lebara"}
	if _, _, ok := matchCarrierProfile(match, identity); !ok {
		t.Fatal("legacy SPN matching before home PLMN discovery changed")
	}
	match.RequireHomePLMN = true
	if _, _, ok := matchCarrierProfile(match, identity); ok {
		t.Fatal("strict home PLMN selector matched without a home PLMN")
	}
}
