package vowifi

import "testing"

func TestResolveCarrierProfileMatchesVoHivePresets(t *testing.T) {
	cases := []struct {
		name      string
		mcc       string
		mnc       string
		preset    string
		source    string
		plmn      string
		allowSHA1 bool
		epdg      string
		identity  uint8
		modp1024  bool
	}{
		{name: "O2 three digit", mcc: "262", mnc: "003", preset: "O2_de_26203", source: CarrierSourceBuiltin, plmn: "262003", allowSHA1: true},
		{name: "O2 two digit", mcc: "262", mnc: "03", preset: "O2_de_26203", source: CarrierSourceBuiltin, plmn: "262003", allowSHA1: true},
		{name: "Vodafone UK", mcc: "234", mnc: "15", preset: "Vodafone_uk_23415", source: CarrierSourceBuiltin, plmn: "234015"},
		{name: "Globe Philippines", mcc: "515", mnc: "02", preset: "Globe_PH_51502", source: CarrierSourceBuiltin, plmn: "515002", epdg: "weconnect.globe.com.ph", allowSHA1: true, identity: 2, modp1024: true},
		{name: "unknown fallback", mcc: "001", mnc: "01", preset: "001001", source: CarrierSourceFallback, plmn: "001001"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			profile := ResolveCarrierProfile(SIMIdentity{HomeMCC: testCase.mcc, HomeMNC: testCase.mnc})
			if profile.PresetID != testCase.preset || profile.Source != testCase.source || profile.PLMN != testCase.plmn {
				t.Fatalf("profile = %#v", profile)
			}
			if profile.AllowSHA1 != testCase.allowSHA1 {
				t.Fatalf("allow SHA-1 = %t, want %t", profile.AllowSHA1, testCase.allowSHA1)
			}
			if profile.EPDG != testCase.epdg {
				t.Fatalf("ePDG = %q, want %q", profile.EPDG, testCase.epdg)
			}
			if profile.IKEIdentityType != testCase.identity {
				t.Fatalf("IKE identity type = %d, want %d", profile.IKEIdentityType, testCase.identity)
			}
			if profile.UseMODP1024 != testCase.modp1024 {
				t.Fatalf("MODP-1024 = %t, want %t", profile.UseMODP1024, testCase.modp1024)
			}
		})
	}
}

func TestGlobeProfileUsesStaticEPDG(t *testing.T) {
	identity := SIMIdentity{
		ICCID:   "89441000400316034372",
		IMSI:    "515021234567890",
		HomeMCC: "515",
		HomeMNC: "02",
	}
	profile := ResolveCarrierProfile(identity)
	epdg, err := deriveEPDG(identity, profile)
	if err != nil {
		t.Fatalf("deriveEPDG() error = %v", err)
	}
	if epdg != "weconnect.globe.com.ph" {
		t.Fatalf("ePDG = %q", epdg)
	}
	if source := epdgSource(identity, profile); source != "static" {
		t.Fatalf("ePDG source = %q, want static", source)
	}
}

func TestResolveCarrierProfileFailsClosedForMissingPLMN(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{IMSI: "262031234567890"})
	if profile.PLMN != "" || profile.PresetID != "" || profile.Source != CarrierSourceFallback {
		t.Fatalf("profile = %#v", profile)
	}
}
