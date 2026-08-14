package vowifi

import "testing"

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
