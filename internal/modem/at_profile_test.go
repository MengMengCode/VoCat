package modem

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

type markerPDPProfile struct{ marker string }

func (profile markerPDPProfile) ConfigureContextCommand(int, string, string) (string, error) {
	return profile.marker + ":context", nil
}

func (profile markerPDPProfile) ConfigureAuthenticationCommand(int, int, string, string) (string, error) {
	return profile.marker + ":auth", nil
}

func (profile markerPDPProfile) AttachCommand(enabled bool) string {
	if enabled {
		return profile.marker + ":attach-on"
	}
	return profile.marker + ":attach-off"
}

func (profile markerPDPProfile) ActivateCommand(_ int, enabled bool) string {
	if enabled {
		return profile.marker + ":activate-on"
	}
	return profile.marker + ":activate-off"
}

func TestResolveATProfileUsesChipsetFamilyOverlay(t *testing.T) {
	overrides := []ATProfileOverride{{
		Chipset: ChipsetZXIC, PDP: markerPDPProfile{marker: "family"},
	}}
	candidate := Candidate{
		VendorID: "19d2", ProductID: "0536",
		Manufacturer: "not ZXIC", Product: "not ZXIC",
	}
	profile := resolveATProfile(candidate, overrides)
	if got, err := profile.PDP.ConfigureContextCommand(1, "IP", "internet"); err != nil || got != "family:context" {
		t.Fatalf("chipset family overlay command = %q, %v", got, err)
	}

	// A shared vendor ID is insufficient evidence for a chipset override.
	candidate.ProductID = "ffff"
	profile = resolveATProfile(candidate, overrides)
	if got, err := profile.PDP.ConfigureContextCommand(1, "IP", "internet"); err != nil || got == "family:context" {
		t.Fatalf("unverified ZTE product selected overlay: %q, %v", got, err)
	}

	// Firmware-reported identity text never influences chipset detection.
	candidate.VendorID = "2c7c"
	candidate.Manufacturer = "ZXIC"
	profile = resolveATProfile(candidate, overrides)
	if got, err := profile.PDP.ConfigureContextCommand(1, "IP", "internet"); err != nil || got == "family:context" {
		t.Fatalf("non-ZXIC USB identity selected overlay: %q, %v", got, err)
	}
}

func TestResolveATProfileFallsBackForUnknownUSBID(t *testing.T) {
	profile := ResolveATProfile(Candidate{
		VendorID: "ffff", ProductID: "0001",
		Manufacturer: "Quectel", Product: "EC20",
	})
	if profile.APDU == nil || profile.Operator == nil || profile.PDP == nil {
		t.Fatalf("default profile has nil capability: %#v", profile)
	}
	if got := profile.Operator.ScanCommand(); got != "AT+COPS=?" {
		t.Fatalf("generic scan command = %q", got)
	}
	if got, err := profile.PDP.ConfigureContextCommand(1, "IPV4V6", "internet"); err != nil ||
		got != `AT+CGDCONT=1,"IPV4V6","internet"` {
		t.Fatalf("generic PDP command = %q, %v", got, err)
	}

	other := ResolveATProfile(Candidate{VendorID: "abcd", ProductID: "1234"})
	if other.Operator.ScanCommand() != profile.Operator.ScanCommand() {
		t.Fatalf("unknown USB identities resolved differently: %q vs %q", profile.Operator.ScanCommand(), other.Operator.ScanCommand())
	}
}

func TestResolveATProfileLimitsAPDUPreferenceToVerifiedZXIC(t *testing.T) {
	profile := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: "0536"}).APDU
	preference, ok := profile.(APDUFormPreference)
	if !ok {
		t.Fatal("verified ZXIC profile has no APDU form preference")
	}
	for _, command := range []APDUCommand{APDUCSIM, APDUCGLA} {
		form := preference.PreferredForm(command)
		if form != APDUUnquoted {
			t.Fatalf("command %d preference = %v, want unquoted", command, form)
		}
		built, err := profile.Build(command, 1, []byte{0x00, 0xa4}, form)
		if err != nil || strings.Contains(built, `"`) || !strings.HasSuffix(built, ",00A4") {
			t.Fatalf("preferred command = %q, %v", built, err)
		}
	}
	for _, candidate := range []Candidate{{}, {VendorID: "19d2", ProductID: "0601"}, {VendorID: "19d2", ProductID: "ffff"}, {VendorID: "2c7c", ProductID: "0125"}} {
		generic := ResolveATProfile(candidate).APDU
		if _, ok := generic.(APDUFormPreference); ok {
			t.Fatalf("unverified candidate inherited vendor APDU preference: %+v", candidate)
		}
		if command, err := generic.Build(APDUCSIM, 0, []byte{0x00, 0xa4}, APDUQuoted); err != nil || command != `AT+CSIM=4,"00A4"` {
			t.Fatalf("standard quoted command = %q, %v", command, err)
		}
	}
}

func TestResolveATProfileUsesGenericCapabilitiesForZXIC(t *testing.T) {
	zxic := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: "0536"})
	if zxic.APDU == nil || zxic.Operator == nil || zxic.PDP == nil {
		t.Fatalf("ZXIC profile has nil capability: %#v", zxic)
	}
	for _, candidate := range []Candidate{
		{VendorID: "19d2", ProductID: "ffff", Manufacturer: "Quectel", Product: "EC20"},
		{VendorID: "ffff", ProductID: "0001"},
	} {
		profile := ResolveATProfile(candidate)
		zxicCommand, zxicErr := zxic.PDP.ConfigureContextCommand(1, "IPV4V6", "internet")
		genericCommand, genericErr := profile.PDP.ConfigureContextCommand(1, "IPV4V6", "internet")
		if zxicErr != nil || genericErr != nil || zxicCommand != genericCommand {
			t.Fatalf("ZXIC capability differs from generic: %q (%v) vs %q (%v)", zxicCommand, zxicErr, genericCommand, genericErr)
		}
	}
}

func TestATProfileOverlayChangesOnlySelectedCapability(t *testing.T) {
	profile := resolveATProfile(Candidate{VendorID: "19d2", ProductID: "0536"}, []ATProfileOverride{{
		Chipset: ChipsetZXIC, PDP: markerPDPProfile{marker: "overlay"},
	}})
	if got := profile.Operator.ScanCommand(); got != "AT+COPS=?" {
		t.Fatalf("unselected operator capability changed: %q", got)
	}
	if got, err := profile.PDP.ConfigureContextCommand(1, "IP", "internet"); err != nil || got != "overlay:context" {
		t.Fatalf("selected PDP capability = %q, %v", got, err)
	}
	standard := ResolveATProfile(Candidate{})
	standardAPDU, err := standard.APDU.Build(APDUCSIM, 0, []byte{0x00, 0xA4}, APDUQuoted)
	if err != nil {
		t.Fatal(err)
	}
	overlayAPDU, err := profile.APDU.Build(APDUCSIM, 0, []byte{0x00, 0xA4}, APDUQuoted)
	if err != nil || overlayAPDU != standardAPDU {
		t.Fatalf("unselected APDU capability changed: %q vs %q (%v)", overlayAPDU, standardAPDU, err)
	}
}

func TestStandardPDPProfileBuilds3GPPCommands(t *testing.T) {
	profile := ResolveATProfile(Candidate{}).PDP
	tests := []struct {
		name string
		got  string
		err  error
		want string
	}{
		{name: "context", want: `AT+CGDCONT=1,"IPV4V6","internet"`},
		{name: "authentication", want: `AT+CGAUTH=1,3,"user","pass"`},
		{name: "attach", got: profile.AttachCommand(true), want: "AT+CGATT=1"},
		{name: "detach", got: profile.AttachCommand(false), want: "AT+CGATT=0"},
		{name: "activate", got: profile.ActivateCommand(1, true), want: "AT+CGACT=1,1"},
		{name: "deactivate", got: profile.ActivateCommand(1, false), want: "AT+CGACT=0,1"},
	}
	if tests[0].got, tests[0].err = profile.ConfigureContextCommand(1, "IPV4V6", "internet"); tests[0].err != nil {
		t.Fatalf("ConfigureContextCommand: %v", tests[0].err)
	}
	if tests[1].got, tests[1].err = profile.ConfigureAuthenticationCommand(1, 3, "user", "pass"); tests[1].err != nil {
		t.Fatalf("ConfigureAuthenticationCommand: %v", tests[1].err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err != nil || test.got != test.want {
				t.Fatalf("command = %q, err = %v, want %q", test.got, test.err, test.want)
			}
		})
	}
}

func TestATProfileCapabilitiesAreSafeForConcurrentUse(t *testing.T) {
	profile := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: "0536"})
	scanResponse := Response{Lines: []string{`+COPS: (1,"Carrier (LTE)","Short","46001",7)`}}
	selectionResponse := Response{Lines: []string{`+COPS: 1,2,"46001",7`}}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := profile.APDU.Build(APDUCSIM, 0, []byte{0x00, 0xA4}, APDUQuoted); err != nil {
				errorsFound <- err
			}
			if len(profile.Operator.ParseScan(scanResponse)) != 1 {
				errorsFound <- errors.New("concurrent operator scan parse returned wrong record count")
			}
			if _, err := profile.Operator.ParseSelection(selectionResponse); err != nil {
				errorsFound <- err
			}
			if _, err := profile.PDP.ConfigureAuthenticationCommand(1, 3, "user", "pass"); err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent profile operation: %v", err)
	}
}
