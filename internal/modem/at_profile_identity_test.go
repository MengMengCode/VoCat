package modem

import "testing"

// The ZXIC platform firmware has no ATI, no AT+CCID family, and no +CLCC;
// identity probes must use the platform commands documented in the vendor
// manual and verified on the live hardware (prefixed +CGMI/+CGMM/+CGMR and
// the +ZICCID query). The override keys on the chipset family, so every
// ZXIC product ID inherits it.
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

	// Family semantics: an unlisted ZXIC product ID inherits the platform
	// dialect instead of silently falling back to commands the firmware
	// does not implement.
	otherZXIC := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: "ffff"})
	if commands := otherZXIC.Identity.DeviceInfoCommands(); len(commands) != 3 ||
		commands[0] != "AT+CGMI" {
		t.Fatalf("other ZXIC product DeviceInfoCommands = %v, want the family override", commands)
	}
	if otherZXIC.Identity.SupportsCLCC() {
		t.Fatal("other ZXIC product must inherit SupportsCLCC=false")
	}

	// Non-ZXIC vendors never inherit the override.
	quectel := ResolveATProfile(Candidate{VendorID: "2c7c", ProductID: "0125", Manufacturer: "ZXIC"})
	if commands := quectel.Identity.DeviceInfoCommands(); len(commands) != 1 || commands[0] != "ATI" {
		t.Fatalf("non-ZXIC DeviceInfoCommands = %v, want [ATI]", commands)
	}
}
