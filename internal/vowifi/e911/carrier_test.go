package e911

import (
	"testing"
	"vocat/internal/vowifi"
)

func TestCarrierUsesHomeNetworkAndPrioritizesMVNO(t *testing.T) {
	for _, tt := range []struct{ name, mcc, mnc, spn, want string }{
		{"RedPocket GSMA", "310", "280", "Red Pocket", "att_nsds"},
		{"RedPocket GSMT", "310", "260", "Red Pocket", "msp_websheet"},
		{"AT&T", "310", "410", "", "att_nsds"},
		{"Tello", "310", "260", "", "msp_websheet"},
		{"Mint", "310", "240", "", "msp_websheet"},
		{"US Mobile", "310", "410", "", "att_nsds"},
		{"US Mobile", "310", "260", "", "msp_websheet"},
		{"US Mobile", "311", "480", "", "msp_websheet"},
		{"Verizon", "311", "480", "", "msp_websheet"},
		{"Visible", "311", "480", "", "msp_websheet"},
		{"Google Fi", "310", "260", "", "msp_websheet"},
		{"unknown US", "313", "999", "", "ts43_websheet"},
		{"foreign SIM roaming in US", "234", "15", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Detect(vowifi.SIMIdentity{HomeMCC: tt.mcc, HomeMNC: tt.mnc, SPN: tt.spn}, tt.name)
			if got.Method != tt.want {
				t.Fatalf("Detect() = %#v, want method %s", got, tt.want)
			}
		})
	}
}

func TestMVNOKeepsBrandAndValidationEvidence(t *testing.T) {
	for _, network := range []struct{ mcc, mnc, want string }{{"310", "410", "att"}, {"310", "260", "tmobile"}, {"311", "480", "verizon"}} {
		carrier := Detect(vowifi.SIMIdentity{HomeMCC: network.mcc, HomeMNC: network.mnc}, "US Mobile")
		if carrier.ID != "usmobile" || carrier.Network != network.want || carrier.PortalURL != "https://app.usmobile.com/" || carrier.Verified {
			t.Fatalf("incorrect multi-network brand routing/evidence: %#v", carrier)
		}
	}
}

func TestAddressRequiresValidUSFields(t *testing.T) {
	for _, a := range []Address{
		{Street: "1 Main St", City: "City", State: "xx", ZIP: "12345"},
		{Street: "1 Main St", City: "City", State: "OR", ZIP: "9740"},
		{Street: "1 Main St\nOther", City: "City", State: "OR", ZIP: "97403"},
		{City: "City", State: "OR", ZIP: "97403"},
	} {
		if _, err := a.Normalize(); err == nil {
			t.Fatalf("accepted invalid address %#v", a)
		}
	}
	a, err := (Address{Street: " 1 Main St ", City: "Eugene", State: "or", ZIP: "97403-1234"}).Normalize()
	if err != nil || a.Street != "1 Main St" || a.State != "OR" {
		t.Fatalf("normalized = %#v, %v", a, err)
	}
}
