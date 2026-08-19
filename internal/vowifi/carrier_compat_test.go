package vowifi

import (
	"strings"
	"testing"
)

func TestAssignedRoutePLMNUsesNarrowCardAndSubscriptionMatches(t *testing.T) {
	tests := []struct {
		name         string
		iccid        string
		imsi         string
		wantMCC      string
		wantMNC      string
		wantAssigned bool
	}{
		{name: "XeSIM Lebara route", iccid: "8944160000000000001", imsi: "204047000000001", wantMCC: "234", wantMNC: "15", wantAssigned: true},
		{name: "CTExcel initial route", iccid: "8944300000000000001", imsi: "234336000000001", wantMCC: "234", wantMNC: "30", wantAssigned: true},
		{name: "XeSIM ICCID without matching subscription", iccid: "8944160000000000001", imsi: "204041000000001"},
		{name: "similar ICCID must not match", iccid: "8944100000000000001", imsi: "204047000000001"},
		{name: "generic EE SIM must not match CTExcel", iccid: "8944110000000000000", imsi: "234336000000001"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mcc, mnc, assigned := AssignedRoutePLMN(test.iccid, test.imsi)
			if mcc != test.wantMCC || mnc != test.wantMNC || assigned != test.wantAssigned {
				t.Fatalf("AssignedRoutePLMN() = %q/%q,%v, want %q/%q,%v", mcc, mnc, assigned, test.wantMCC, test.wantMNC, test.wantAssigned)
			}
		})
	}
}

func TestApplyAssignedCarrierRoutePreservesAuthenticationPLMN(t *testing.T) {
	identity := applyAssignedCarrierRoute(SIMIdentity{
		ICCID: "8944300000000000001", IMSI: "234336000000001",
		HomeMCC: "234", HomeMNC: "33",
	})
	if identity.HomeMCC != "234" || identity.HomeMNC != "33" {
		t.Fatalf("authentication PLMN = %s/%s, want 234/33", identity.HomeMCC, identity.HomeMNC)
	}
	if identity.EPDG != "epdg.epc.mnc030.mcc234.pub.3gppnetwork.org" {
		t.Fatalf("route ePDG = %q", identity.EPDG)
	}
}

func TestIsATT310280RequiresMatchingPLMNAndIMSI(t *testing.T) {
	if !IsATT310280(SIMIdentity{IMSI: "310280000000001", HomeMCC: "310", HomeMNC: "280"}) {
		t.Fatal("AT&T 310/280 identity was not recognized")
	}
	for _, identity := range []SIMIdentity{
		{IMSI: "310410000000001", HomeMCC: "310", HomeMNC: "280"},
		{IMSI: "310280000000001", HomeMCC: "310", HomeMNC: "28"},
		{IMSI: "310280000000001", HomeMCC: "311", HomeMNC: "280"},
	} {
		if IsATT310280(identity) {
			t.Fatalf("unrelated identity matched AT&T 310/280: %#v", identity)
		}
	}
}

func TestResolveCarrierProfileUsesStandardDefault(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{
		IMSI: "999010000000001", HomeMCC: "999", HomeMNC: "01",
	})
	if profile.ID != CarrierProfileStandard || profile.MatchSource != "standard" {
		t.Fatalf("default profile = %#v", profile)
	}
	if profile.IKEProposal != IKEProposalModern || !profile.AdvertiseEAPOnly ||
		profile.IMSIdentityProfile != IMSProfileStandard || profile.IMSRegisterProfile != IMSProfileStandard {
		t.Fatalf("default profile lost standard capabilities: %#v", profile)
	}
}

func TestResolveCarrierProfilePrefersConstrainedMVNO(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{
		ICCID: "8944160000000000001", IMSI: "204047000000001",
		HomeMCC: "204", HomeMNC: "04", SPN: "Lebara",
	})
	if profile.ID != "xesim-lebara-vodafone-uk" || profile.RouteMCC != "234" || profile.RouteMNC != "15" {
		t.Fatalf("MVNO profile = %#v", profile)
	}
	if profile.MatchSource != "hplmn+imsi+iccid" {
		t.Fatalf("MVNO match source = %q", profile.MatchSource)
	}
}

func TestResolveCarrierProfileUsesAlternativeMVNOSelectors(t *testing.T) {
	tests := []struct {
		name     string
		identity SIMIdentity
		source   string
	}{
		{
			name:     "Apple GID1 selector",
			identity: SIMIdentity{IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF"},
			source:   "hplmn+gid1",
		},
		{
			name:     "Android SPN selector",
			identity: SIMIdentity{IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", SPN: "GiffGaff"},
			source:   "hplmn+spn",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := ResolveCarrierProfile(test.identity)
			if profile.ID != "giffgaff-o2-uk" || profile.MatchSource != test.source {
				t.Fatalf("giffgaff profile = %#v", profile)
			}
			if profile.SMSCenter != "+447802002606" || profile.IMSTransport != "udp" || !profile.IMSUserEqPhone {
				t.Fatalf("giffgaff IMS settings = %#v", profile)
			}
		})
	}

	generic := ResolveCarrierProfile(SIMIdentity{
		IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10",
	})
	if generic.ID != "o2-uk" || generic.SMSCenter != "+447802000332" {
		t.Fatalf("generic O2 profile = %#v", generic)
	}
}

func TestEEHostedProfileDoesNotClaimCTExcelBrand(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{
		ICCID: "8944300000000000001", IMSI: "234336000000001",
		HomeMCC: "234", HomeMNC: "33",
	})
	if profile.ID != "ee-uk-hosted-23433" || profile.RouteMCC != "234" || profile.RouteMNC != "30" {
		t.Fatalf("EE-hosted profile = %#v", profile)
	}
}

func TestResolveCarrierProfileNormalizesMNCWidth(t *testing.T) {
	for _, mnc := range []string{"03", "003"} {
		profile := ResolveCarrierProfile(SIMIdentity{HomeMCC: "262", HomeMNC: mnc})
		if profile.ID != "o2-germany" || profile.AdvertiseEAPOnly || profile.IMSIPSecEncryption != "null" {
			t.Errorf("O2 Germany MNC %q profile = %#v", mnc, profile)
		}
	}
}

func TestEPDGDNSClientSubnetComesFromCarrierProfileData(t *testing.T) {
	if got := EPDGDNSClientSubnet("EPDG.EPC.MNC002.MCC262.PUB.3GPPNETWORK.ORG."); got != "109.192.0.0/24" {
		t.Fatalf("Vodafone Germany DNS client subnet = %q", got)
	}
	if got := EPDGDNSClientSubnet("epdg.epc.mnc015.mcc234.pub.3gppnetwork.org"); got != "" {
		t.Fatalf("ordinary ePDG received geographic DNS fallback %q", got)
	}
}

func TestResolveCarrierProfileDITOPhilippinesUsesLegacyIKE(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{HomeMCC: "515", HomeMNC: "66"})
	if profile.ID != "dito-philippines" {
		t.Fatalf("DITO profile = %#v", profile)
	}
	if profile.IKEProposal != IKEProposalLegacy {
		t.Fatalf("DITO IKE proposal = %q, want %q", profile.IKEProposal, IKEProposalLegacy)
	}
	if !profile.AllowSMSWithoutContactConfirmation {
		t.Fatalf("DITO profile should allow SMS without contact confirmation")
	}
}

func TestResolveCarrierProfileATTRegisterOptions(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{IMSI: "310280000000001", HomeMCC: "310", HomeMNC: "280"})
	if profile.ID != "att-us" {
		t.Fatalf("AT&T profile = %#v", profile)
	}
	if profile.IMSRegisterOptions.ContactFormat != IMSContactFormatATT {
		t.Fatalf("AT&T contact format = %q, want %q", profile.IMSRegisterOptions.ContactFormat, IMSContactFormatATT)
	}
	if profile.IMSRegisterOptions.ExpirySeconds != 18400 {
		t.Fatalf("AT&T expiry = %d, want 18400", profile.IMSRegisterOptions.ExpirySeconds)
	}
	if profile.IMSRegisterOptions.UserAgent != "SimAdmin VoWiFi" {
		t.Fatalf("AT&T user agent = %q", profile.IMSRegisterOptions.UserAgent)
	}
	if profile.IMSRegisterOptions.PVisitedNetworkID != "one.att.net" {
		t.Fatalf("AT&T P-Visited-Network-ID = %q", profile.IMSRegisterOptions.PVisitedNetworkID)
	}
	if len(profile.IMSRegisterOptions.AcceptContactTags) != 2 {
		t.Fatalf("AT&T Accept-Contact tags = %v", profile.IMSRegisterOptions.AcceptContactTags)
	}
}

func TestResolveCarrierProfileO2GermanyRegisterOptions(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{HomeMCC: "262", HomeMNC: "03"})
	if profile.ID != "o2-germany" {
		t.Fatalf("O2 Germany profile = %#v", profile)
	}
	if profile.IMSRegisterOptions.ContactFormat != "" {
		t.Fatalf("O2 Germany contact format = %q, want empty", profile.IMSRegisterOptions.ContactFormat)
	}
	if profile.IMSRegisterOptions.SupportedHeader == nil || !strings.Contains(*profile.IMSRegisterOptions.SupportedHeader, "sec-agree") {
		t.Fatalf("O2 Germany Supported header = %v", profile.IMSRegisterOptions.SupportedHeader)
	}
	if profile.IMSRegisterOptions.AllowHeader == nil || !strings.Contains(*profile.IMSRegisterOptions.AllowHeader, "MESSAGE") {
		t.Fatalf("O2 Germany Allow header = %v", profile.IMSRegisterOptions.AllowHeader)
	}
	if !profile.IMSRegisterOptions.PPreferredIdentity {
		t.Fatal("O2 Germany should add P-Preferred-Identity")
	}
}

func TestResolveCarrierProfileStandardHasNoRegisterOverrides(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{HomeMCC: "001", HomeMNC: "01"})
	if profile.ID != CarrierProfileStandard {
		t.Fatalf("profile = %q", profile.ID)
	}
	if profile.IMSRegisterOptions.ExpirySeconds != 0 {
		t.Fatalf("standard expiry = %d", profile.IMSRegisterOptions.ExpirySeconds)
	}
	if profile.IMSRegisterOptions.ContactFormat != "" {
		t.Fatalf("standard contact format = %q", profile.IMSRegisterOptions.ContactFormat)
	}
	if profile.IMSRegisterOptions.SupportedHeader != nil {
		t.Fatalf("standard supported header = %v", *profile.IMSRegisterOptions.SupportedHeader)
	}
	if profile.AllowSMSWithoutContactConfirmation {
		t.Fatal("standard profile should require SMS contact confirmation")
	}
}
