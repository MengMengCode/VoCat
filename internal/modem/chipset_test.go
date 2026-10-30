package modem

import "testing"

func TestChipsetForUSBRequiresVerifiedIdentity(t *testing.T) {
	cases := []struct {
		name     string
		vendorID string
		product  string
		want     Chipset
	}{
		{"UZ901 lab unit", "19d2", "0536", ChipsetZXIC},
		{"uppercase vendor", "19D2", "0536", ChipsetZXIC},
		{"padded vendor", " 19d2 ", "0536", ChipsetZXIC},
		{"padded product", "19d2", " 0536 ", ChipsetZXIC},
		{"ME3630 RNDIS", "19d2", "0601", ChipsetGeneric},
		{"ME3630 MBIM", "19d2", "0602", ChipsetGeneric},
		{"ME3630 serial", "19d2", "1476", ChipsetGeneric},
		{"unknown ZTE product", "19d2", "ffff", ChipsetGeneric},
		{"empty product", "19d2", "", ChipsetGeneric},
		{"other vendor with same product", "ffff", "0536", ChipsetGeneric},
		{"Quectel", "2c7c", "0125", ChipsetGeneric},
		{"DJI 4G", "2ca3", "4006", ChipsetGeneric},
		{"empty identity", "", "", ChipsetGeneric},
	}
	for _, testCase := range cases {
		if got := ChipsetForUSB(testCase.vendorID, testCase.product); got != testCase.want {
			t.Fatalf("%s: ChipsetForUSB(%q, %q) = %q, want %q",
				testCase.name, testCase.vendorID, testCase.product, got, testCase.want)
		}
	}
}

func TestChipsetForCandidateAndIsZXICFamily(t *testing.T) {
	zxic := Candidate{VendorID: "19d2", ProductID: "0536"}
	if ChipsetForCandidate(zxic) != ChipsetZXIC {
		t.Fatalf("ChipsetForCandidate(zxic) = %q, want %q", ChipsetForCandidate(zxic), ChipsetZXIC)
	}
	if !IsZXICFamily("19d2", "0536") {
		t.Fatal("IsZXICFamily(19d2, 0536) = false, want true")
	}
	if IsZXICFamily("2c7c", "0125") {
		t.Fatal("IsZXICFamily(2c7c, 0125) = true, want false")
	}
	generic := Candidate{VendorID: "2c7c", ProductID: "0125", Manufacturer: "ZXIC", Product: "UZ901"}
	if ChipsetForCandidate(generic) != ChipsetGeneric {
		t.Fatal("manufacturer/product text must not influence chipset family detection")
	}
	generic.VendorID, generic.ProductID = "19d2", "ffff"
	if ChipsetForCandidate(generic) != ChipsetGeneric {
		t.Fatal("unverified ZTE product must not inherit the ZXIC dialect from its strings")
	}
}

// Chipset values are persisted alongside device configuration (the zxic
// device type), so they need stable string identifiers.
func TestChipsetStringIdentifiersAreStable(t *testing.T) {
	if string(ChipsetZXIC) != "zxic" || string(ChipsetGeneric) != "generic" {
		t.Fatalf("chipset identifiers changed: zxic=%q generic=%q", ChipsetZXIC, ChipsetGeneric)
	}
}
