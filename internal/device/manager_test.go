package device

import (
	"context"
	"errors"
	"testing"

	"vocat/internal/modem"
)

func TestManagerRefreshBuildsEC20Snapshot(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{
			command: "ATI",
			response: okResponse(
				"Quectel",
				"EC20CEFAGR06A04M4G",
				"Revision: EC20CEHCLGR06A04M1G",
			),
		},
		{command: "AT+CPIN?", response: okResponse("+CPIN: READY")},
		{command: "AT+CSQ", response: okResponse("+CSQ: 20,99")},
		{
			command: `AT+QENG="servingcell"`,
			response: okResponse(
				`+QENG: "servingcell","NOCONN","LTE","FDD",460,01,5F1E805,37,1650,3,5,5,8340,-97,-10,-68,15,9`,
			),
		},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,0,"China Mobile",7`)},
		{command: "AT+CEREG?", response: okResponse(`+CEREG: 0,5`)},
		{command: "AT+CGSN", response: okResponse("867123456789012")},
		{
			command:  "AT+CCID",
			response: modem.Response{Final: "+CME ERROR: 100"},
			err:      errors.New("CCID unsupported"),
		},
		{command: "AT+QCCID", response: okResponse("+QCCID: 8986001234567890123F")},
		{command: "AT+CIMI", response: okResponse("460001234567890")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
		{command: "AT+CNUM", response: okResponse(`+CNUM: "","+8613800138000",145`)},
	}}
	manager, id := newStartedTestManager(t, client)

	snapshot, err := manager.Refresh(context.Background(), id)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !snapshot.Responsive || !snapshot.SIMReady || snapshot.SIMStatus != "ready" {
		t.Fatalf("modem/SIM state = %#v", snapshot)
	}
	if snapshot.Manufacturer != "Quectel" ||
		snapshot.Model != "EC20CEFAGR06A04M4G" ||
		snapshot.Firmware != "EC20CEHCLGR06A04M1G" {
		t.Fatalf(
			"identity = manufacturer %q, model %q, firmware %q",
			snapshot.Manufacturer,
			snapshot.Model,
			snapshot.Firmware,
		)
	}
	if snapshot.SignalRaw == nil || *snapshot.SignalRaw != 20 ||
		snapshot.SignalPercent == nil || *snapshot.SignalPercent != 65 ||
		snapshot.RSSIDBm == nil || *snapshot.RSSIDBm != -68 ||
		snapshot.RSRP == nil || *snapshot.RSRP != -97 ||
		snapshot.RSRQ == nil || *snapshot.RSRQ != -10 ||
		snapshot.SINR == nil || *snapshot.SINR != 15 {
		t.Fatalf("signal metrics = %#v", snapshot)
	}
	if snapshot.AccessTech != "LTE" || snapshot.Band != "B3" ||
		snapshot.Channel != "1650" || snapshot.OperatorName != "China Unicom" ||
		snapshot.OperatorCode != "46001" ||
		snapshot.RegistrationStatus != 5 || snapshot.RegistrationSource != "CEREG" {
		t.Fatalf("network = %#v", snapshot)
	}
	if snapshot.IMEI != "867123456789012" ||
		snapshot.ICCID != "8986001234567890123" ||
		snapshot.IMSI != "460001234567890" {
		t.Fatalf("subscriber identifiers = %#v", snapshot)
	}
	if !snapshot.ModeKnown || snapshot.OperatingMode != 1 ||
		snapshot.FlightMode || snapshot.RadioOff {
		t.Fatalf("operating mode = %#v", snapshot)
	}
	if snapshot.Phone.Number != "+8613800138000" ||
		snapshot.Phone.Source != PhoneSourceCNUM {
		t.Fatalf("phone = %#v", snapshot.Phone)
	}

	device, err := manager.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if device.Snapshot == nil || device.Snapshot.Phone.Number != snapshot.Phone.Number {
		t.Fatalf("stored device = %#v", device)
	}
	client.assertDone(t)
}

func TestManagerRefreshBuildsNativeWWANSnapshotFromQualcommATI(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{
			command: "ATI",
			response: okResponse(
				"Manufacturer: QUALCOMM INCORPORATED",
				"Model: 4094",
				"Revision: MPSS.DPM.2.0",
				"IMEI: 866340055929387",
			),
		},
		{command: "AT+CPIN?", response: okResponse("+CPIN: READY")},
		{command: "AT+CSQ", response: okResponse("+CSQ: 15,99")},
		{command: `AT+QENG="servingcell"`, err: errors.New("unsupported")},
		{command: "AT+COPS?", response: okResponse("+COPS: 0")},
		{command: "AT+CEREG?", response: okResponse("+CEREG: 0,0")},
		{command: "AT+CCID", err: errors.New("unsupported")},
		{command: "AT+QCCID", err: errors.New("unsupported")},
		{command: "AT+ICCID", response: okResponse("ICCID: 894921007608519523")},
		{command: "AT+CIMI", response: okResponse("515031234567890")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 0")},
		{command: "AT+CNUM", response: okResponse(`+CNUM: "","+639171234567",145`)},
	}}
	manager, id := newStartedTestManager(t, client)

	snapshot, err := manager.Refresh(context.Background(), id)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if snapshot.Manufacturer != "QUALCOMM INCORPORATED" ||
		snapshot.Model != "4094" || snapshot.Firmware != "MPSS.DPM.2.0" {
		t.Fatalf("identity = %#v", snapshot)
	}
	if snapshot.IMEI != "866340055929387" ||
		snapshot.ICCID != "894921007608519523" ||
		snapshot.IMSI != "515031234567890" {
		t.Fatalf("subscriber identifiers = %#v", snapshot)
	}
	if !snapshot.SIMReady || !snapshot.ModeKnown || snapshot.OperatingMode != 0 ||
		!snapshot.FlightMode || !snapshot.RadioOff {
		t.Fatalf("SIM/radio state = %#v", snapshot)
	}
	client.assertDone(t)
}

func TestParseICCIDIdentifierStripsTwoFillerNibbles(t *testing.T) {
	response := modem.Response{Lines: []string{"+CCID: 894921007608519523FF"}}
	if got := parseICCIDIdentifier(response, []string{"+CCID:", "+QCCID:"}, 18, 22); got != "894921007608519523" {
		t.Fatalf("parseICCIDIdentifier = %q", got)
	}
}

func TestRadioOffModesIncludeQualcommOffline(t *testing.T) {
	t.Parallel()
	for _, mode := range []int{0, 4, 7} {
		if !isRadioOffMode(mode) {
			t.Fatalf("CFUN=%d was not classified as radio off", mode)
		}
	}
	if isRadioOffMode(1) {
		t.Fatal("CFUN=1 was classified as radio off")
	}
}

func TestManagerRequiresStartAndKnownDevice(t *testing.T) {
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{},
		Opener:     &staticOpener{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), "missing"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("before Start error = %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown device error = %v", err)
	}
}

func TestExecuteSensitiveATDoesNotPersistCommandOrModemError(t *testing.T) {
	const secretCommand = `AT+CSIM=78,"00880081221000112233445566778899AABBCCDDEEFF1000112233445566778899AABBCCDDEEFF00"`
	client := &transcriptClient{steps: []clientStep{{
		command: secretCommand,
		err:     &modem.CommandError{Command: secretCommand, Final: "ERROR"},
	}}}
	manager, id := newStartedTestManager(t, client)

	if _, err := manager.ExecuteSensitiveAT(context.Background(), id, secretCommand); err == nil {
		t.Fatal("ExecuteSensitiveAT() error = nil")
	}
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if entry.LastError != "sensitive AT command failed" {
		t.Fatalf("LastError = %q", entry.LastError)
	}
	client.assertDone(t)
}
