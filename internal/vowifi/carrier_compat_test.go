package vowifi

import (
	"strings"
	"testing"
)

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
	// Cricket MVNO on AT&T network
	cricket := ResolveCarrierProfile(SIMIdentity{
		ICCID: "8901150000000000001", IMSI: "310150000000001",
		HomeMCC: "310", HomeMNC: "150",
	})
	if !strings.Contains(cricket.ID, "cricket") {
		t.Fatalf("Cricket MVNO profile = %#v", cricket)
	}

	// Pure Talk MVNO on AT&T network via GID1
	pureTalk := ResolveCarrierProfile(SIMIdentity{
		IMSI: "310410000000001", HomeMCC: "310", HomeMNC: "410", GID1: "62FFFF",
	})
	if !strings.Contains(pureTalk.ID, "pure-talk") {
		t.Fatalf("Pure Talk MVNO profile = %#v", pureTalk)
	}
}

func TestResolveCarrierProfileUsesAppleGID1Selector(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{
		IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	})
	if !strings.Contains(profile.ID, "giffgaff") || profile.MatchSource != "hplmn+gid1" {
		t.Fatalf("giffgaff profile = %#v", profile)
	}
}

func TestResolveCarrierProfileGiffgaffIMSHeaders(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{
		IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	})
	options := profile.IMSRegisterOptions
	if profile.IMSTransport != "tcp" || options.ContactFormat != IMSContactFormatGSMA {
		t.Fatalf("giffgaff IMS transport/contact profile = %#v", profile)
	}
	if profile.IMSUserAgent != "iOS/18.6.2 iPhone" {
		t.Fatalf("giffgaff User-Agent = %q", profile.IMSUserAgent)
	}
	if options.PAccessNetworkInfo != nil {
		t.Fatalf("giffgaff unexpectedly defines a carrier PANI override = %v", *options.PAccessNetworkInfo)
	}
	if profile.PANIEnabled == nil || !*profile.PANIEnabled || profile.PANICountry != "AUTO" {
		t.Fatalf("giffgaff PANI behavior = enabled=%v country=%q", profile.PANIEnabled, profile.PANICountry)
	}
	if len(options.ContactExtraTags) != 2 || options.ContactExtraTags[0] != "+g.3gpp.mid-call" || options.ContactExtraTags[1] != "+g.3gpp.smsip" {
		t.Fatalf("giffgaff Contact tags = %#v", options.ContactExtraTags)
	}
}

func TestResolveCarrierProfileATT(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{
		ICCID: "8901410000000000001", IMSI: "310410000000001", HomeMCC: "310", HomeMNC: "410",
	})
	if !strings.Contains(profile.ID, "att") {
		t.Fatalf("AT&T profile = %#v", profile)
	}
}

func TestResolveCarrierProfileRedPocketOutranksBroadATTICCID(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{
		ICCID: "8901410000000000001", IMSI: "310170000000001",
		HomeMCC: "310", HomeMNC: "170", SPN: "Red Pocket", GID1: "42FFFF",
	})
	if profile.ID != "ipcc-redpocket-310170" || profile.MatchSource != "hplmn+gid1" {
		t.Fatalf("RedPocket profile = %#v", profile)
	}
}

func TestResolveCarrierProfileStandardHasNoRegisterOverrides(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{HomeMCC: "999", HomeMNC: "99"})
	if profile.ID != CarrierProfileStandard {
		t.Fatalf("profile = %q", profile.ID)
	}
	if profile.IMSRegisterOptions.ExpirySeconds != 0 {
		t.Fatalf("standard expiry = %d", profile.IMSRegisterOptions.ExpirySeconds)
	}
	if profile.IMSRegisterOptions.ContactFormat != "" {
		t.Fatalf("standard contact format = %q", profile.IMSRegisterOptions.ContactFormat)
	}
	if profile.PANIEnabled != nil || profile.PANICountry != "" {
		t.Fatalf("standard PANI behavior = enabled=%v country=%q", profile.PANIEnabled, profile.PANICountry)
	}
	if profile.AllowSMSWithoutContactConfirmation {
		t.Fatal("standard profile should require SMS contact confirmation")
	}
}

func TestMVNOParentNetworkRouting(t *testing.T) {
	// Giffgaff on O2 UK
	giffgaff := ResolveCarrierProfile(SIMIdentity{
		IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	})
	if giffgaff.RouteMCC != "234" || giffgaff.RouteMNC != "10" {
		t.Fatalf("giffgaff Route PLMN = %s-%s, want 234-10", giffgaff.RouteMCC, giffgaff.RouteMNC)
	}

	// VOXI on Vodafone UK
	voxi := ResolveCarrierProfile(SIMIdentity{
		IMSI: "234150000000001", HomeMCC: "234", HomeMNC: "15", SPN: "VOXI",
	})
	if !strings.Contains(voxi.ID, "voxi") || voxi.RouteMCC != "234" || voxi.RouteMNC != "15" {
		t.Fatalf("VOXI profile = %#v", voxi)
	}

	// SMARTY on Three UK
	smarty := ResolveCarrierProfile(SIMIdentity{
		IMSI: "234200000000001", HomeMCC: "234", HomeMNC: "20", SPN: "SMARTY",
	})
	if !strings.Contains(smarty.ID, "smarty") || smarty.RouteMCC != "234" || smarty.RouteMNC != "20" {
		t.Fatalf("SMARTY profile = %#v", smarty)
	}
}

func TestGlobalRoamingProviderResolution(t *testing.T) {
	// Truphone / BetterRoaming global 90143
	truphone := ResolveCarrierProfile(SIMIdentity{
		IMSI: "901430000000001", HomeMCC: "901", HomeMNC: "43",
	})
	if (!strings.Contains(truphone.ID, "truphone") && !strings.Contains(truphone.ID, "1global")) || truphone.EPDG != "epdg.eps.truphone.net" {
		t.Fatalf("Truphone global profile = %#v", truphone)
	}

	// Jersey Telecom 23450 (eSIM Go / 1GLOBAL / RedteaGO host)
	jersey := ResolveCarrierProfile(SIMIdentity{
		IMSI: "234500000000001", HomeMCC: "234", HomeMNC: "50",
	})
	if !strings.Contains(jersey.ID, "jersey-telecom") || jersey.EPDG != "epdg.epc.mnc050.mcc234.pub.3gppnetwork.org" {
		t.Fatalf("Jersey Telecom profile = %#v", jersey)
	}
}

func TestCTExcelMVNOResolution(t *testing.T) {
	ctexcel := ResolveCarrierProfile(SIMIdentity{
		IMSI:    "234330000000001",
		ICCID:   "8944300000000000001",
		SPN:     "CTExcel",
		HomeMCC: "234",
		HomeMNC: "33",
	})
	if ctexcel.ID != "ipcc-ctexcel-23433" {
		t.Fatalf("CTExcel profile ID = %q, want ipcc-ctexcel-23433", ctexcel.ID)
	}
	if ctexcel.IMSDialURIScheme != "sip" || !ctexcel.IMSUserEqPhone {
		t.Fatalf("CTExcel dial URI scheme = %q, userEqPhone = %v", ctexcel.IMSDialURIScheme, ctexcel.IMSUserEqPhone)
	}
}
