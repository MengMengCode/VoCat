package modem

import "testing"

// Only the verified UZ901 USB identity receives its nonstandard identity
// commands. Other products sharing ZTE's vendor ID retain the default probes.
func TestResolveATProfileZXICIdentityCommands(t *testing.T) {
	standard := ResolveATProfile(Candidate{})
	if commands := standard.Identity.DeviceInfoCommands(); len(commands) != 1 || commands[0] != "ATI" {
		t.Fatalf("standard DeviceInfoCommands = %v, want [ATI]", commands)
	}
	if commands := standard.Identity.ICCIDCommands(); len(commands) != 3 ||
		commands[0] != "AT+CCID" || commands[1] != "AT+QCCID" || commands[2] != "AT+MCCID" {
		t.Fatalf("standard ICCIDCommands = %v, want CCID/QCCID/MCCID", commands)
	}
	if !standard.Identity.SupportsCLCC() {
		t.Fatal("standard profile must keep CLCC polling")
	}

	zxic := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: "0536"})
	if commands := zxic.Identity.DeviceInfoCommands(); len(commands) != 3 ||
		commands[0] != "AT+CGMI" || commands[1] != "AT+CGMM" || commands[2] != "AT+CGMR" {
		t.Fatalf("ZXIC DeviceInfoCommands = %v, want CGMI/CGMM/CGMR", commands)
	}
	if commands := zxic.Identity.ICCIDCommands(); len(commands) != 1 || commands[0] != "AT+ZICCID?" {
		t.Fatalf("ZXIC ICCIDCommands = %v, want [AT+ZICCID?]", commands)
	}
	if zxic.Identity.SupportsCLCC() {
		t.Fatal("ZXIC firmware returns +CME ERROR: 6003 for AT+CLCC; polling must be gated off")
	}

	for _, productID := range []string{"0601", "0602", "1476", "ffff", ""} {
		otherZTE := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: productID})
		if commands := otherZTE.Identity.DeviceInfoCommands(); len(commands) != 1 || commands[0] != "ATI" {
			t.Fatalf("ZTE product %q DeviceInfoCommands = %v, want [ATI]", productID, commands)
		}
		if commands := otherZTE.Identity.ICCIDCommands(); len(commands) != 3 || commands[0] != "AT+CCID" {
			t.Fatalf("ZTE product %q ICCIDCommands = %v, want standard fallback", productID, commands)
		}
		if !otherZTE.Identity.SupportsCLCC() {
			t.Fatalf("ZTE product %q must retain standard call-list capability", productID)
		}
	}

	// Non-ZXIC vendors never inherit the override.
	quectel := ResolveATProfile(Candidate{VendorID: "2c7c", ProductID: "0125", Manufacturer: "ZXIC"})
	if commands := quectel.Identity.DeviceInfoCommands(); len(commands) != 1 || commands[0] != "ATI" {
		t.Fatalf("non-ZXIC DeviceInfoCommands = %v, want [ATI]", commands)
	}
}
