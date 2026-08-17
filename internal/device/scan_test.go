package device

import (
	"testing"

	"vocat/internal/modem"
)

func TestParseOperatorScanNormalizesMainlandCarrierNamesByPLMN(t *testing.T) {
	response := modem.Response{Lines: []string{
		`+COPS: (1,"CMCC","CMCC","46000",7),(1,"wrong modem name","CU","46001",7),(1,"","CT","46011",7),(1,"CBN","CBN","46015",7)`,
	}}
	operators := parseOperatorScan(response)
	if len(operators) != 4 {
		t.Fatalf("operators = %#v", operators)
	}
	want := []string{"China Mobile", "China Unicom", "China Telecom", "China Broadnet"}
	for index := range want {
		if operators[index].Name != want[index] {
			t.Fatalf("operator %d name = %q, want %q", index, operators[index].Name, want[index])
		}
	}
}

func TestCarrierNameForPLMNUsesGlobalDatabase(t *testing.T) {
	if got := carrierNameForPLMN("23415", "stale modem name"); got != "Vodafone" {
		t.Fatalf("carrier name = %q", got)
	}
	if got := carrierNameForPLMN("26202", ""); got != "Vodafone" {
		t.Fatalf("German carrier name = %q", got)
	}
	if got := carrierNameForPLMN("310260", ""); got != "T-Mobile - US" {
		t.Fatalf("US carrier name = %q", got)
	}
	if got := carrierNameForPLMN("99999", "Test Network"); got != "Test Network" {
		t.Fatalf("unknown carrier fallback = %q", got)
	}
}

func TestCarrierForPLMNReturnsCountryCode(t *testing.T) {
	tests := map[string]string{
		"23415":  "GB",
		"23487":  "GB",
		"26202":  "DE",
		"310260": "US",
		"22201":  "IT",
		"72405":  "BR",
		"46015":  "CN",
	}
	for plmn, wantCountry := range tests {
		name, country, ok := CarrierForPLMN(plmn)
		if !ok || name == "" || country != wantCountry {
			t.Errorf("CarrierForPLMN(%q) = (%q, %q, %v), want a name and country %q", plmn, name, country, ok, wantCountry)
		}
	}
}

func TestCountryForMCCUsesEmbeddedCountryIndex(t *testing.T) {
	tests := map[string]string{
		"234": "GB",
		"262": "DE",
		"310": "US",
		"460": "CN",
	}
	for mcc, want := range tests {
		if got, ok := CountryForMCC(mcc); !ok || got != want {
			t.Errorf("CountryForMCC(%q) = (%q, %v), want %q", mcc, got, ok, want)
		}
	}
	for _, invalid := range []string{"", "23", "999", "abcd"} {
		if got, ok := CountryForMCC(invalid); ok || got != "" {
			t.Errorf("CountryForMCC(%q) = (%q, %v), want unknown", invalid, got, ok)
		}
	}
}

func TestMCCsByCountryReturnsCompleteIndependentGrouping(t *testing.T) {
	grouped := MCCsByCountry()
	if got := grouped["GB"]; len(got) != 2 || got[0] != "234" || got[1] != "235" {
		t.Fatalf("GB MCCs = %#v", got)
	}
	grouped["GB"][0] = "999"
	if country, ok := CountryForMCC("234"); !ok || country != "GB" {
		t.Fatalf("mutating returned grouping changed embedded index: (%q, %v)", country, ok)
	}
}

func TestCarrierForIMSIHandlesTwoAndThreeDigitMNCs(t *testing.T) {
	tests := []struct {
		imsi        string
		wantPLMN    string
		wantCountry string
	}{
		{imsi: "234336570710174", wantPLMN: "23433", wantCountry: "GB"},
		{imsi: "234159609054263", wantPLMN: "23415", wantCountry: "GB"},
		{imsi: "234870123456789", wantPLMN: "23487", wantCountry: "GB"},
		{imsi: "454006395879502", wantPLMN: "45400", wantCountry: "HK"},
		{imsi: "310260123456789", wantPLMN: "310260", wantCountry: "US"},
	}
	for _, item := range tests {
		plmn, name, country, ok := CarrierForIMSI(item.imsi)
		if !ok || plmn != item.wantPLMN || name == "" || country != item.wantCountry {
			t.Errorf("CarrierForIMSI(%q) = (%q, %q, %q, %v), want PLMN %q and country %q", item.imsi, plmn, name, country, ok, item.wantPLMN, item.wantCountry)
		}
	}
}

func TestCarrierForSIMUsesAndroidGIDRuleBeforePLMNFallback(t *testing.T) {
	plmn, name, country, ok := CarrierForSIM(CarrierIdentity{
		IMSI: "454006395879502", ICCID: "89852350126077295027",
		SPN: "Saily", GID1: "536E617065", GID2: "536E617065000012", MNCLength: 2,
	})
	if !ok || plmn != "45400" || name != "Webbing" || country != "HK" {
		t.Fatalf("CarrierForSIM exact rule = (%q, %q, %q, %v)", plmn, name, country, ok)
	}

	plmn, name, country, ok = CarrierForSIM(CarrierIdentity{
		IMSI: "454006395879502", SPN: "Saily", MNCLength: 2,
	})
	if !ok || plmn != "45400" || name != "1O1O / csl / Club Sim" || country != "HK" {
		t.Fatalf("CarrierForSIM generic fallback = (%q, %q, %q, %v)", plmn, name, country, ok)
	}
}

func TestCarrierForSIMRecognizesGiffgaffWithoutRelabelingGenericO2(t *testing.T) {
	for _, identity := range []CarrierIdentity{
		{IMSI: "234100000000001", GID1: "508FFFFF", MNCLength: 2},
		{IMSI: "234100000000001", SPN: "GiffGaff", MNCLength: 2},
	} {
		plmn, name, country, ok := CarrierForSIM(identity)
		if !ok || plmn != "23410" || name != "giffgaff" || country != "GB" {
			t.Fatalf("giffgaff identity = (%q, %q, %q, %v)", plmn, name, country, ok)
		}
	}

	_, name, _, ok := CarrierForSIM(CarrierIdentity{
		IMSI: "234100000000001", MNCLength: 2,
	})
	if !ok || name == "giffgaff" {
		t.Fatalf("generic O2 SIM was mislabeled as giffgaff: (%q, %v)", name, ok)
	}
}
