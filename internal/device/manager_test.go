package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"vocat/internal/modem"
	"vocat/internal/pcsc"
)

type testPCSCBackend struct{ readers []pcsc.Reader }

func (backend testPCSCBackend) Readers(context.Context) ([]pcsc.Reader, error) {
	return append([]pcsc.Reader(nil), backend.readers...), nil
}
func (testPCSCBackend) Open(context.Context, pcsc.Selector) (pcsc.Card, error) {
	return nil, pcsc.ErrNoCard
}

func TestManagerDiscoversWiFiCallingOnlyReaderWithoutATPort(t *testing.T) {
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{}, Opener: &staticOpener{},
		CardReaders: pcsc.NewWithBackend(testPCSCBackend{readers: []pcsc.Reader{{
			Name: "Alcor Link AK9563 00 00", USBPath: "1-3", Product: "AK9563",
		}}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	items := manager.List()
	if len(items) != 1 || items[0].Candidate.HardwareKind != pcsc.HardwareKind || items[0].Candidate.HasATPort() {
		t.Fatalf("discovered readers = %#v", items)
	}
	snapshot, err := manager.Refresh(context.Background(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Responsive || snapshot.SIMReady || snapshot.SIMStatus != "" || !snapshot.FlightMode {
		t.Fatalf("reader snapshot = %#v", snapshot)
	}
}

func TestManagerDiscoverReturnsOnlyCurrentlyPresentDevices(t *testing.T) {
	manager, id := newStartedTestManager(t, nil)
	if devices := manager.List(); len(devices) != 1 || devices[0].ID != id || !devices[0].Discovered {
		t.Fatalf("initial devices = %#v", devices)
	}

	manager.discoverer = staticDiscoverer{}
	present, err := manager.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover after unplug: %v", err)
	}
	if len(present) != 0 {
		t.Fatalf("present devices after unplug = %#v, want none", present)
	}

	// The retained entry is still available to the configured-device dashboard,
	// but is explicitly offline and cannot be offered by fresh discovery.
	retained := manager.List()
	if len(retained) != 1 || retained[0].ID != id || retained[0].Discovered {
		t.Fatalf("retained devices after unplug = %#v", retained)
	}
}

func TestManagerPublishesDeviceLifecycleEvents(t *testing.T) {
	manager, id := newStartedTestManager(t, nil)
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := manager.SubscribeDeviceLifecycleEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}

	manager.discoverer = staticDiscoverer{}
	if _, err := manager.Discover(ctx); err != nil {
		t.Fatalf("Discover after removal: %v", err)
	}
	select {
	case event := <-events:
		if event.ID != id || event.Present {
			t.Fatalf("removal event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for removal event")
	}

	manager.discoverer = staticDiscoverer{candidates: []modem.Candidate{{
		ID:     id,
		ATPort: modem.Port{Path: "/dev/ttyUSB2", Role: modem.PortRoleAT},
	}}}
	if _, err := manager.Discover(ctx); err != nil {
		t.Fatalf("Discover after insertion: %v", err)
	}
	select {
	case event := <-events:
		if event.ID != id || !event.Present {
			t.Fatalf("insertion event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for insertion event")
	}
}

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
		{
			command:  "AT+CCID",
			response: modem.Response{Final: "+CME ERROR: 100"},
			err:      errors.New("CCID unsupported"),
		},
		{command: "AT+QCCID", response: okResponse("+QCCID: 8986001234567890123F")},
		{command: "AT+CIMI", response: okResponse("460001234567890")},
		{command: "AT+CRSM=176,28486,0,0,17", response: okResponse(`+CRSM: 144,0,"00434D4343FFFFFFFFFFFFFFFFFFFFFFFF"`)},
		{command: "AT+CRSM=192,28589,0,0,0", response: okResponse(`+CRSM: 144,0,"620680020004FFFF"`)},
		{command: "AT+CRSM=176,28589,0,0,4", response: okResponse(`+CRSM: 144,0,"00000002"`)},
		{command: "AT+CRSM=192,28478,0,0,0", response: okResponse(`+CRSM: 144,0,"620680020002FFFF"`)},
		{command: "AT+CRSM=176,28478,0,0,2", response: okResponse(`+CRSM: 144,0,"0102"`)},
		{command: "AT+CRSM=192,28479,0,0,0", response: okResponse(`+CRSM: 144,0,"620680020001FFFF"`)},
		{command: "AT+CRSM=176,28479,0,0,1", response: okResponse(`+CRSM: 144,0,"FF"`)},
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
		snapshot.IMSI != "460001234567890" || snapshot.SPN != "CMCC" ||
		snapshot.MNCLength != 2 || snapshot.GID1 != "0102" || snapshot.GID2 != "" {
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

func TestManagerRefreshReadsNativeWWANICCIDThroughQMIUIM(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "ATI", response: okResponse("Qualcomm", "PCIe/MHI WWAN modem", "Revision: native-410")},
		{command: "AT+CPIN?", response: okResponse("+CPIN: READY")},
		{command: "AT+CCID", response: modem.Response{Final: "ERROR"}, err: errors.New("CCID unsupported")},
		{command: "AT+QCCID", response: modem.Response{Final: "ERROR"}, err: errors.New("QCCID unsupported")},
		{command: "AT+CIMI", response: okResponse("234159611274418")},
		{command: "AT+CRSM=176,28486,0,0,17", response: okResponse(`+CRSM: 106,130,""`)},
		{command: "AT+CRSM=192,28589,0,0,0", response: okResponse(`+CRSM: 106,130,""`)},
		{command: "AT+CRSM=192,28478,0,0,0", response: okResponse(`+CRSM: 106,130,""`)},
		{command: "AT+CRSM=192,28479,0,0,0", response: okResponse(`+CRSM: 106,130,""`)},
		{command: "AT+CSQ", response: okResponse("+CSQ: 99,99")},
		{command: `AT+QENG="servingcell"`, response: okResponse(`+QENG: "servingcell","SEARCH"`)},
		{command: "AT+COPS?", response: okResponse("+COPS: 0")},
		{command: "AT+CEREG?", response: okResponse("+CEREG: 0,2")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
		{command: "AT+CNUM", response: okResponse(`+CNUM: "","+8613800138000",145`)},
	}}
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:               "mhi-wwan0",
			Product:          "PCIe/MHI WWAN modem",
			QMIControl:       "/dev/wwan0qmi0",
			NetworkInterface: "wwan0",
			ATPort:           modem.Port{Path: "/dev/wwan0at0", Name: "wwan0at0", Role: modem.PortRoleAT},
		}}},
		Opener: &staticOpener{client: client},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		return &fakeQMIRadioSession{iccid: "89441000400316034372", imei: "861716070416510"}, nil
	}
	if err := manager.SetBackend("mhi-wwan0", "qmi"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := manager.Refresh(context.Background(), "mhi-wwan0")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if snapshot.ICCID != "89441000400316034372" || snapshot.IMEI != "861716070416510" || !snapshot.SIMReady {
		t.Fatalf("native QMI identity = %#v", snapshot)
	}
	client.assertDone(t)
}

func TestParseSPNASCIIAndUCS2(t *testing.T) {
	if got := parseSPN(okResponse(`+CRSM: 144,0,"004C6562617261FFFFFFFFFFFFFFFFFFFF"`)); got != "Lebara" {
		t.Fatalf("ASCII SPN = %q", got)
	}
	if got := parseSPN(okResponse(`+CRSM: 144,0,"0080004C00650062006100720061FFFF"`)); got != "Lebara" {
		t.Fatalf("UCS2 SPN = %q", got)
	}
	if got := parseSPN(okResponse(`+CRSM: 106,130,""`)); got != "" {
		t.Fatalf("failed CRSM SPN = %q", got)
	}
}

func TestParseICCIDIdentifierStripsTwoFillerNibbles(t *testing.T) {
	response := modem.Response{Lines: []string{"+CCID: 894921007608519523FF"}}
	if got := parseICCIDIdentifier(response, []string{"+CCID:", "+QCCID:"}, 18, 22); got != "894921007608519523" {
		t.Fatalf("parseICCIDIdentifier = %q", got)
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

func TestManagerBackendSelectionIsExplicit(t *testing.T) {
	manager, id := newStartedTestManager(t, &transcriptClient{})
	if err := manager.SetBackend(id, "qmi"); err != nil {
		t.Fatal(err)
	}
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.backendFor(state); got != "qmi" {
		t.Fatalf("backend = %q, want qmi", got)
	}
	if err := manager.SetBackend(id, "mbim"); err == nil {
		t.Fatal("unsupported backend was accepted")
	}
}

func TestManagerForcesRFOffBeforeInspectingChangedSIMNetwork(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "ATI", response: okResponse("Quectel", "EC20", "Revision: test")},
		{command: "AT+CPIN?", response: okResponse("+CPIN: READY")},
		{command: "AT+CCID", response: okResponse("+CCID: 8900000000000000002")},
		// This must precede CIMI, signal, serving-cell and operator queries.
		{command: "AT+CFUN=4", response: okResponse()},
		{command: "AT+CIMI", response: okResponse("234150000000002")},
		{command: "AT+CRSM=176,28486,0,0,17", response: okResponse(`+CRSM: 144,0,"004C6562617261FFFFFFFFFFFFFFFFFFFF"`)},
		{command: "AT+CRSM=192,28589,0,0,0", response: okResponse(`+CRSM: 144,0,"620680020004FFFF"`)},
		{command: "AT+CRSM=176,28589,0,0,4", response: okResponse(`+CRSM: 144,0,"00000002"`)},
		{command: "AT+CRSM=192,28478,0,0,0", response: okResponse(`+CRSM: 144,0,"620680020001FFFF"`)},
		{command: "AT+CRSM=176,28478,0,0,1", response: okResponse(`+CRSM: 144,0,"FF"`)},
		{command: "AT+CRSM=192,28479,0,0,0", response: okResponse(`+CRSM: 144,0,"620680020001FFFF"`)},
		{command: "AT+CRSM=176,28479,0,0,1", response: okResponse(`+CRSM: 144,0,"FF"`)},
		{command: "AT+CSQ", response: okResponse("+CSQ: 99,99")},
		{command: `AT+QENG="servingcell"`, response: okResponse(`+QENG: "servingcell","SEARCH"`)},
		{command: "AT+COPS?", response: okResponse("+COPS: 0")},
		{command: "AT+CEREG?", response: okResponse("+CEREG: 0,0")},
		{command: "AT+CGSN", response: okResponse("867123456789012")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
		{command: "AT+CNUM", response: okResponse(`+CNUM: "","+447700900002",145`)},
	}}
	manager, id := newStartedTestManager(t, client)
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.lastICCID = "8900000000000000001"

	snapshot, err := manager.Refresh(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.SIMChanged || !snapshot.FlightMode || snapshot.OperatingMode != 4 {
		t.Fatalf("changed SIM snapshot = %#v", snapshot)
	}
	client.assertDone(t)
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
