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

func TestCarrierForIMSIHandlesTwoAndThreeDigitMNCs(t *testing.T) {
	tests := []struct {
		imsi        string
		wantPLMN    string
		wantCountry string
	}{
		{imsi: "234336570710174", wantPLMN: "23433", wantCountry: "GB"},
		{imsi: "234159609054263", wantPLMN: "23415", wantCountry: "GB"},
		{imsi: "234870123456789", wantPLMN: "23487", wantCountry: "GB"},
		{imsi: "310260123456789", wantPLMN: "310260", wantCountry: "US"},
	}
	for _, item := range tests {
		plmn, name, country, ok := CarrierForIMSI(item.imsi)
		if !ok || plmn != item.wantPLMN || name == "" || country != item.wantCountry {
			t.Errorf("CarrierForIMSI(%q) = (%q, %q, %q, %v), want PLMN %q and country %q", item.imsi, plmn, name, country, ok, item.wantPLMN, item.wantCountry)
		}
	}
}
