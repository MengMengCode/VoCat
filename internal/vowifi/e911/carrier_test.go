package e911

import (
	"testing"
	"vocat/internal/vowifi"
)

func TestCarrierUsesHomeNetworkAndPrioritizesMVNO(t *testing.T) {
	for _, tt := range []struct{ name, mcc, mnc, spn, want string }{
		{"RedPocket GSMA", "310", "280", "Red Pocket", "att_nsds"},
		{"RedPocket GSMT", "310", "260", "Red Pocket", "account"},
		{"AT&T", "310", "410", "", "att_nsds"},
		{"Tello", "310", "260", "", "account"},
		{"Mint", "310", "240", "", "account"},
		{"US Mobile", "310", "410", "", "account"},
		{"Verizon", "311", "480", "", "phone"},
		{"Visible", "311", "480", "", "phone"},
		{"Google Fi", "310", "260", "", "account"},
		{"unknown US", "313", "999", "", "unsupported"},
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
