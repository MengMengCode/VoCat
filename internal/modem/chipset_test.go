package modem

import "testing"

// The chipset family keys on the USB vendor ID: every ZXIC-platform product
// shares the vendor's AT dialect (no ATI, no +CCID family, no +CLCC), while
// each retail module still exposes its own product ID. Family matching must
// not depend on the product ID or on the manufacturer strings the firmware
// reports, which are frequently placeholders.
func TestChipsetForUSBKeysOnVendorFamily(t *testing.T) {
	cases := []struct {
		name     string
		vendorID string
		product  string
		want     Chipset
	}{
		{"UZ901 lab unit", "19d2", "0536", ChipsetZXIC},
		{"uppercase vendor", "19D2", "0536", ChipsetZXIC},
		{"padded vendor", " 19d2 ", "0536", ChipsetZXIC},
		{"other ZXIC product", "19d2", "ffff", ChipsetZXIC},
		{"empty product", "19d2", "", ChipsetZXIC},
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
}

// Chipset values are persisted alongside device configuration (the zxic
// device type), so they need stable string identifiers.
func TestChipsetStringIdentifiersAreStable(t *testing.T) {
	if string(ChipsetZXIC) != "zxic" || string(ChipsetGeneric) != "generic" {
		t.Fatalf("chipset identifiers changed: zxic=%q generic=%q", ChipsetZXIC, ChipsetGeneric)
	}
}
