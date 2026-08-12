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
	}{
		{name: "O2 three digit", mcc: "262", mnc: "003", preset: "O2_de_26203", source: CarrierSourceBuiltin, plmn: "262003", allowSHA1: true},
		{name: "O2 two digit", mcc: "262", mnc: "03", preset: "O2_de_26203", source: CarrierSourceBuiltin, plmn: "262003", allowSHA1: true},
		{name: "Vodafone UK", mcc: "234", mnc: "15", preset: "Vodafone_uk_23415", source: CarrierSourceBuiltin, plmn: "234015"},
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
		})
	}
}

func TestResolveCarrierProfileFailsClosedForMissingPLMN(t *testing.T) {
	profile := ResolveCarrierProfile(SIMIdentity{IMSI: "262031234567890"})
	if profile.PLMN != "" || profile.PresetID != "" || profile.Source != CarrierSourceFallback {
		t.Fatalf("profile = %#v", profile)
	}
}
