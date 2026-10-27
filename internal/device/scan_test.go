package device

import (
	"context"
	"errors"
	"strings"
	"testing"

	"vocat/internal/modem"
)

type testOperatorProfile struct {
	scanCommand       string
	queryCommand      string
	manualCommand     string
	automaticCommand  string
	deregisterCommand string
	formatCommand     string
	manualPLMN        string
	manualAccessTech  int
	checkManualInput  bool
	scanResponse      string
	selectionResponse string
	records           []modem.ATOperatorRecord
	selection         modem.ATOperatorSelection
	selectionErr      error
}

func (profile testOperatorProfile) ScanCommand() string           { return profile.scanCommand }
func (profile testOperatorProfile) SelectionQueryCommand() string { return profile.queryCommand }
func (profile testOperatorProfile) ManualSelectionCommand(plmn string, accessTechnology *int) (string, error) {
	if profile.checkManualInput &&
		(plmn != profile.manualPLMN || accessTechnology == nil || *accessTechnology != profile.manualAccessTech) {
		return "", errors.New("unexpected manual operator selection arguments")
	}
	return profile.manualCommand, nil
}
func (profile testOperatorProfile) AutomaticSelectionCommand() string {
	return profile.automaticCommand
}
func (profile testOperatorProfile) DeregisterCommand() string           { return profile.deregisterCommand }
func (profile testOperatorProfile) SetOperatorFormatCommand(int) string { return profile.formatCommand }
func (profile testOperatorProfile) ParseScan(response modem.Response) []modem.ATOperatorRecord {
	if profile.scanResponse != "" && !strings.Contains(response.Text(), profile.scanResponse) {
		return nil
	}
	return append([]modem.ATOperatorRecord(nil), profile.records...)
}
func (profile testOperatorProfile) ParseSelection(response modem.Response) (modem.ATOperatorSelection, error) {
	if profile.selectionResponse != "" && !strings.Contains(response.Text(), profile.selectionResponse) {
		return modem.ATOperatorSelection{}, errors.New("unexpected operator selection response")
	}
	return profile.selection, profile.selectionErr
}

func configureTestOperatorProfile(
	t *testing.T,
	manager *Manager,
	id, vendorID, productID string,
	profile modem.OperatorATProfile,
) {
	t.Helper()
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.opMu.Lock()
	manager.mu.Lock()
	state.candidate.VendorID = vendorID
	state.candidate.ProductID = productID
	state.atProfile.Operator = profile
	manager.mu.Unlock()
	state.opMu.Unlock()
}

func TestScanOperatorsParsesZXICCOPSResponse(t *testing.T) {
	// This is a representative standard 3GPP COPS transcript, not a captured
	// UZ901 hardware response; the repository has no sanitized device capture.
	client := &transcriptClient{steps: []clientStep{{
		command: "AT+COPS=?",
		response: okResponse(
			`+COPS: (2,"Roam Mobile","Roam","23415",7),(1,"Example Network","EX","99999",2),,(0-4),(0-2)`,
		),
	}}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", modem.ResolveATProfile(modem.Candidate{
		VendorID: "19d2", ProductID: "0536",
	}).Operator)

	result, err := manager.ScanOperators(context.Background(), id)
	if err != nil {
		t.Fatalf("ScanOperators: %v", err)
	}
	if result.Status != "complete" || len(result.Operators) != 2 {
		t.Fatalf("scan result = %+v", result)
	}
	first := result.Operators[0]
	if first.Status != "current" || first.Numeric != "23415" || first.Act != "LTE" {
		t.Fatalf("first operator = %+v, want current 23415 LTE", first)
	}
	second := result.Operators[1]
	if second.Status != "available" || second.Numeric != "99999" || second.Act != "UTRAN" {
		t.Fatalf("second operator = %+v, want available 99999 UTRAN", second)
	}
	client.assertDone(t)
}

func TestScanOperatorsUsesResolvedOperatorProfile(t *testing.T) {
	profile := testOperatorProfile{
		scanCommand:  "AT+XOPS=scan",
		scanResponse: "+XOPS: opaque vendor response",
		records: []modem.ATOperatorRecord{{
			StatusCode: "2", LongName: "Profile Network", ShortName: "PN",
			Numeric: "99998", AccessTechnologyCode: "7",
		}},
	}
	client := &transcriptClient{steps: []clientStep{{command: "AT+XOPS=scan", response: okResponse("+XOPS: opaque vendor response")}}}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", profile)

	result, err := manager.ScanOperators(context.Background(), id)
	if err != nil {
		t.Fatalf("ScanOperators: %v", err)
	}
	if len(result.Operators) != 1 || result.Operators[0].Numeric != "99998" ||
		result.Operators[0].Name != "Profile Network" || result.Operators[0].Act != "LTE" {
		t.Fatalf("scan result did not use profile parser: %+v", result)
	}
	client.assertDone(t)
}

type cancelOperatorScanClient struct {
	cancel   context.CancelFunc
	commands []string
}

func (client *cancelOperatorScanClient) Execute(ctx context.Context, command string) (modem.Response, error) {
	client.commands = append(client.commands, command)
	if command != "AT+COPS=?" {
		return modem.Response{}, errors.New("unexpected command during canceled operator scan")
	}
	client.cancel()
	return modem.Response{}, context.Canceled
}

func (*cancelOperatorScanClient) WaitURC(context.Context, func(string) bool) (string, error) {
	return "", errors.New("unexpected URC wait")
}

func (*cancelOperatorScanClient) Close() error { return nil }

func TestScanOperatorsCancellationDoesNotChangeSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &cancelOperatorScanClient{cancel: cancel}
	manager, id := newStartedTestManager(t, client)
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", modem.ResolveATProfile(modem.Candidate{
		VendorID: "19d2", ProductID: "0536",
	}).Operator)

	result, err := manager.ScanOperators(ctx, id)
	if !errors.Is(err, context.Canceled) || result.Status != "failed" {
		t.Fatalf("ScanOperators = %+v, %v; want failed canceled scan", result, err)
	}
	if len(client.commands) != 1 || client.commands[0] != "AT+COPS=?" {
		t.Fatalf("canceled scan changed selection state with commands %v", client.commands)
	}
}

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
		{imsi: "234330000000001", wantPLMN: "23433", wantCountry: "GB"},
		{imsi: "234150000000001", wantPLMN: "23415", wantCountry: "GB"},
		{imsi: "234870000000001", wantPLMN: "23487", wantCountry: "GB"},
		{imsi: "454000000000001", wantPLMN: "45400", wantCountry: "HK"},
		{imsi: "310260000000001", wantPLMN: "310260", wantCountry: "US"},
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

func TestCarrierForSIMDisplaysDITOForRoamingSponsorIdentity(t *testing.T) {
	for _, identity := range []CarrierIdentity{
		{IMSI: "204047616000001", ICCID: "89636626000000000001"},
		{IMSI: "204047616000002", ICCID: "89636626000000000002"},
		{IMSI: "515661015000001", ICCID: "89636626000000000001"},
	} {
		plmn, name, country, ok := CarrierForSIM(identity)
		if !ok || plmn != "51566" || name != "DITO" || country != "PH" {
			t.Fatalf("DITO identity = (%q, %q, %q, %v)", plmn, name, country, ok)
		}
	}

	plmn, name, country, ok := CarrierForSIM(CarrierIdentity{
		IMSI: "204047616000001", ICCID: "8931440400000000000",
	})
	if !ok || plmn != "20404" || name == "DITO" || country != "NL" {
		t.Fatalf("unrelated Vodafone identity was relabelled: (%q, %q, %q, %v)", plmn, name, country, ok)
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
