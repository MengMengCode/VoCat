package device

import (
	"context"
	"errors"
	"strings"
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

func TestManagerDiscoverRetainsOnlyVerifiedPortForCurrentUSBInstance(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		deferred, reset, newIdentity bool
	}{
		{name: "busy same enumeration"}, {name: "deferred same enumeration", deferred: true},
		{name: "busy new enumeration", reset: true}, {name: "busy new identity", reset: true, newIdentity: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &transcriptClient{steps: []clientStep{{command: "AT", response: okResponse()}}}
			manager, id := newStartedTestManager(t, client)
			if _, err := manager.ExecuteAT(context.Background(), id, "AT"); err != nil {
				t.Fatal(err)
			}
			before, err := manager.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			candidate := before.Candidate
			candidate.ATProbeRequired = true
			candidate.ATProbeBusy = !tc.deferred
			candidate.ATProbeDeferred = tc.deferred
			candidate.Product = "fresh product metadata"
			candidate.ATPort = modem.Port{Path: "/dev/ttyUSB9", Name: "ttyUSB9"}
			candidate.Ports = []modem.Port{before.Candidate.ATPort, candidate.ATPort}
			candidate.DiscoveryIssue = "at_probe_pending"
			if tc.reset {
				candidate.USBGeneration = "new-enumeration"
				candidate.ATPort = before.Candidate.ATPort
			}
			if tc.newIdentity {
				candidate.VendorID = "ffff"
				candidate.ProductID = "0001"
			}
			manager.discoverer = staticDiscoverer{candidates: []modem.Candidate{candidate}}
			items, err := manager.Discover(context.Background())
			if err != nil || len(items) != 1 {
				t.Fatalf("Discover: %v %#v", err, items)
			}
			after := items[0].Candidate
			if after.USBGeneration != candidate.USBGeneration || after.VendorID != candidate.VendorID || after.Product != "fresh product metadata" {
				t.Fatalf("fresh metadata discarded: %+v", after)
			}
			if tc.reset {
				if client.closeCount != 1 || !after.ATProbeRequired {
					t.Fatalf("old session survived reset: closes=%d candidate=%+v", client.closeCount, after)
				}
			} else if client.closeCount != 0 || after.ATPort.OpenPath() != before.Candidate.ATPort.OpenPath() || after.DiscoveryIssue != "" || after.ATProbeRequired {
				t.Fatalf("verified port lost while deferred: closes=%d candidate=%+v", client.closeCount, after)
			}
		})
	}
}

type cancelingPartialDiscoverer struct {
	cancel    context.CancelFunc
	candidate modem.Candidate
}

func (discoverer cancelingPartialDiscoverer) Discover(context.Context) ([]modem.Candidate, error) {
	discoverer.cancel()
	return []modem.Candidate{discoverer.candidate}, context.Canceled
}

func TestManagerDiscoverCancellationDoesNotMarkDevicesOffline(t *testing.T) {
	initial := modem.Candidate{
		ID: "initial", VendorID: "2c7c", ProductID: "0125", USBGeneration: "old",
		ATPort: modem.Port{Path: "/dev/ttyUSB2", Name: "ttyUSB2", Role: modem.PortRoleAT},
	}
	manager, err := NewManager(Options{
		Discoverer:  staticDiscoverer{candidates: []modem.Candidate{initial}},
		Opener:      &staticOpener{},
		CardReaders: pcsc.NewWithBackend(testPCSCBackend{}),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	replacement := initial
	replacement.VendorID = "ffff"
	replacement.ProductID = "0001"
	replacement.USBGeneration = "new"
	replacement.ATPort = modem.Port{Path: "/dev/ttyUSB9", Name: "ttyUSB9"}
	ctx, cancel := context.WithCancel(context.Background())
	manager.discoverer = cancelingPartialDiscoverer{cancel: cancel, candidate: replacement}
	if _, err := manager.Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover error = %v, want context.Canceled", err)
	}

	items := manager.List()
	if len(items) != 1 || !items[0].Discovered ||
		items[0].Candidate.VendorID != initial.VendorID ||
		items[0].Candidate.ProductID != initial.ProductID ||
		items[0].Candidate.USBGeneration != initial.USBGeneration ||
		items[0].Candidate.ATPort.OpenPath() != initial.ATPort.OpenPath() {
		t.Fatalf("device changed after canceled partial discovery: %#v", items)
	}
}

func TestManagerExecuteAPDUSanitizesCommandAndResponse(t *testing.T) {
	const apduHex = "00A40400"
	const quotedCommand = `AT+CSIM=8,"` + apduHex + `"`
	const unquotedCommand = `AT+CSIM=8,` + apduHex
	client := &transcriptClient{steps: []clientStep{
		{
			command:  quotedCommand,
			response: modem.Response{Final: "+CME ERROR: 13", Lines: []string{`+CSIM: 8,"response-` + apduHex + `"`}},
			err:      &modem.CommandError{Command: quotedCommand, Final: "+CME ERROR: 13", Lines: []string{`+CSIM: request-` + apduHex}},
		},
	}}
	manager, id := newStartedTestManager(t, client)

	_, err := manager.ExecuteAPDU(context.Background(), id, modem.APDUCSIM, 0, []byte{0x00, 0xA4, 0x04, 0x00})
	var rejected *modem.APDUCommandRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("ExecuteAPDU error = %v, want APDUCommandRejectedError; transcript=%v steps remaining=%d", err, client.unexpected, len(client.steps))
	}
	for _, secret := range []string{apduHex, quotedCommand, unquotedCommand, "request-", "response-"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("ExecuteAPDU error leaked %q: %v", secret, err)
		}
	}
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{apduHex, quotedCommand, unquotedCommand, "request-", "response-"} {
		if strings.Contains(entry.LastError, secret) {
			t.Fatalf("device LastError leaked %q: %q", secret, entry.LastError)
		}
	}
	client.assertDone(t)
}

func TestManagerExecuteAPDUSanitizesMalformedResponse(t *testing.T) {
	const secretResponse = "DEADBEEFCAFEBABE"
	client := &transcriptClient{steps: []clientStep{{
		command: `AT+CSIM=2,"00"`,
		response: modem.Response{
			Final: "OK", Lines: []string{`+CSIM: 4,"` + secretResponse + `ZZ9000"`},
		},
	}}}
	manager, id := newStartedTestManager(t, client)

	_, err := manager.ExecuteAPDU(context.Background(), id, modem.APDUCSIM, 0, []byte{0x00})
	if !errors.Is(err, modem.ErrAPDUResponseInvalid) {
		t.Fatalf("ExecuteAPDU error = %v, want safe APDU parse sentinel", err)
	}
	if strings.Contains(err.Error(), secretResponse) {
		t.Fatalf("ExecuteAPDU error leaked response payload: %v", err)
	}
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(entry.LastError, secretResponse) {
		t.Fatalf("device LastError leaked response payload: %q", entry.LastError)
	}
	client.assertDone(t)
}

func TestManagedDeviceProfileResetsOnUSBIdentityChange(t *testing.T) {
	manager, id := newStartedTestManager(t, nil)
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	initialDialect := state.apduDialect
	if initialDialect == nil || state.atProfile.APDU == nil || state.atProfile.Operator == nil || state.atProfile.PDP == nil {
		t.Fatalf("initial profile/dialect not initialized: profile=%#v dialect=%p", state.atProfile, initialDialect)
	}

	unchanged := state.candidate
	manager.discoverer = staticDiscoverer{candidates: []modem.Candidate{unchanged}}
	if _, err := manager.Discover(context.Background()); err != nil {
		t.Fatalf("Discover unchanged identity: %v", err)
	}
	if state.apduDialect != initialDialect {
		t.Fatal("unchanged USB identity unexpectedly reset the APDU dialect")
	}

	state.atProfile = modem.ATProfile{}
	changed := unchanged
	changed.VendorID, changed.ProductID = "19d2", "0536"
	changed.Manufacturer, changed.Product = "ZXIC", "UZ901"
	manager.discoverer = staticDiscoverer{candidates: []modem.Candidate{changed}}
	if _, err := manager.Discover(context.Background()); err != nil {
		t.Fatalf("Discover changed identity: %v", err)
	}
	if state.apduDialect == nil || state.apduDialect == initialDialect {
		t.Fatal("USB identity change did not replace the APDU dialect")
	}
	if state.atProfile.APDU == nil || state.atProfile.Operator == nil || state.atProfile.PDP == nil {
		t.Fatalf("USB identity change did not resolve a complete profile: %#v", state.atProfile)
	}

	identityChangedDialect := state.apduDialect
	manager.discoverer = staticDiscoverer{}
	if _, err := manager.Discover(context.Background()); err != nil {
		t.Fatalf("Discover removed modem: %v", err)
	}
	manager.discoverer = staticDiscoverer{candidates: []modem.Candidate{changed}}
	if _, err := manager.Discover(context.Background()); err != nil {
		t.Fatalf("Discover reconnected modem: %v", err)
	}
	if state.apduDialect == identityChangedDialect {
		t.Fatal("reconnected hardware reused the prior APDU dialect")
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
		{command: "AT+QCCID", response: modem.Response{Final: "ERROR"}, err: errors.New("QCCID unsupported")},
		{command: "AT+MCCID", response: okResponse("+MCCID: 8986001234567890123F")},

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

func TestSnapshotReadsML307IdentityAndMUESTATS(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "ATI", response: okResponse("CMCC", "ML307A", "ML307A-DSLN-MTSH1S00")},
		{command: "AT+CGMR", response: okResponse("ML307A-DSLN-MTSH1S00")},
		{command: "AT+CPIN?", response: okResponse("+CPIN: READY")},
		{command: "AT+CCID", response: modem.Response{Final: "ERROR"}, err: errors.New("unsupported")},
		{command: "AT+QCCID", response: modem.Response{Final: "ERROR"}, err: errors.New("unsupported")},
		{command: "AT+MCCID", response: okResponse("+MCCID: 89861118216007272115")},
		{command: "AT+CIMI", response: okResponse("460011234567890")},
		{command: "AT+CRSM=176,28486,0,0,17", response: modem.Response{Final: "ERROR"}, err: errors.New("unsupported")},
		{command: "AT+CRSM=192,28589,0,0,0", response: modem.Response{Final: "ERROR"}, err: errors.New("unsupported")},
		{command: "AT+CRSM=192,28478,0,0,0", response: modem.Response{Final: "ERROR"}, err: errors.New("unsupported")},
		{command: "AT+CRSM=192,28479,0,0,0", response: modem.Response{Final: "ERROR"}, err: errors.New("unsupported")},
		{command: "AT+CSQ", response: okResponse("+CSQ: 29,99")},
		{command: `AT+MUESTATS="cell"`, response: okResponse(`+MUESTATS: "scell",4,460,01,1650,,106,-810,-50,-550,80,20.0`)},
		{command: `AT+MUESTATS="sband"`, response: okResponse(`+MUESTATS: "sband",3`)},

		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
		{command: "AT+CEREG?", response: okResponse(`+CEREG: 2,1,"10D4","00595179",7`)},
		{command: "AT+CGSN", response: okResponse("86123456789012M1")},
		{command: "AT+CGSN=1", response: okResponse("+CGSN: +867530912345678")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
		{command: "AT+CNUM", response: okResponse(`+CNUM: "","+8613800138000",145`)},
	}}
	manager := &Manager{commandTimeout: time.Second}
	snapshot, err := manager.readSnapshot(context.Background(), "test", modem.Candidate{Product: "ML307A"}, "", "", nil, client)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if snapshot.Model != "ML307A" || snapshot.Manufacturer != "CMCC" || snapshot.Firmware != "ML307A-DSLN-MTSH1S00" {
		t.Fatalf("modem identity = manufacturer %q model %q firmware %q", snapshot.Manufacturer, snapshot.Model, snapshot.Firmware)
	}
	if snapshot.IMEI != "867530912345678" || snapshot.ICCID != "89861118216007272115" {
		t.Fatalf("identifiers were not parsed")
	}
	if snapshot.AccessTech != "LTE" || snapshot.OperatorCode != "46001" || snapshot.Channel != "1650" || snapshot.Band != "B3" {
		t.Fatalf("network metrics = %#v", snapshot)
	}
	if snapshot.RSRP == nil || *snapshot.RSRP != -81 || snapshot.RSRQ == nil || *snapshot.RSRQ != -5 ||
		snapshot.RSSIDBm == nil || *snapshot.RSSIDBm != -55 || snapshot.SINR == nil || *snapshot.SINR != 8 {
		t.Fatalf("unexpected MUESTATS metrics: %+v", snapshot)
	}
	client.assertDone(t)
}

type operatorProfileSnapshotClient struct {
	commands        []string
	servingCellLine string
	operatorLines   []string
}

func (client *operatorProfileSnapshotClient) Execute(ctx context.Context, command string) (modem.Response, error) {
	if err := ctx.Err(); err != nil {
		return modem.Response{}, err
	}
	client.commands = append(client.commands, command)
	var lines []string
	switch command {
	case "ATI":
		lines = []string{"ZXIC", "UZ901", "firmware"}
	case "AT+CPIN?":
		lines = []string{"+CPIN: READY"}
	case "AT+CCID":
		lines = []string{"+CCID: 89860012345678901234"}
	case "AT+CIMI":
		lines = []string{"234151234567890"}
	case "AT+CRSM=176,28486,0,0,17", "AT+CRSM=192,28589,0,0,0", "AT+CRSM=192,28478,0,0,0", "AT+CRSM=192,28479,0,0,0":
		lines = []string{`+CRSM: 106,130,""`}
	case "AT+CSQ":
		lines = []string{"+CSQ: 20,99"}
	case `AT+QENG="servingcell"`:
		if client.servingCellLine != "" {
			lines = []string{client.servingCellLine}
		}
	case "AT+CEREG?":
		lines = []string{"+CEREG: 0,1"}
	case "AT+CGSN":
		lines = []string{"867530912345678"}
	case "AT+CFUN?":
		lines = []string{"+CFUN: 1"}
	case "AT+CNUM":
		lines = []string{`+CNUM: "",,145`}
	case "AT+XOPS?":
		lines = append([]string(nil), client.operatorLines...)
		if lines == nil {
			lines = []string{"+XOPS: custom selection"}
		}
	}
	return modem.Response{Command: command, Lines: lines, Final: "OK"}, nil
}

func (*operatorProfileSnapshotClient) WaitURC(context.Context, func(string) bool) (string, error) {
	return "", errors.New("unexpected URC wait")
}

func (*operatorProfileSnapshotClient) Close() error { return nil }

func TestRefreshUsesResolvedOperatorProfile(t *testing.T) {
	client := &operatorProfileSnapshotClient{}
	manager, id := newStartedTestManager(t, client)
	profile := testOperatorProfile{
		queryCommand: "AT+XOPS?",
		selection: modem.ATOperatorSelection{
			Mode: 1, Format: 2, Operator: "99998", AccessTechnologyCode: "7",
		},
	}
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", profile)

	snapshot, err := manager.Refresh(context.Background(), id)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if snapshot.OperatorCode != "99998" || snapshot.AccessTech != "LTE" {
		t.Fatalf("snapshot did not use profile parser: code=%q accessTech=%q", snapshot.OperatorCode, snapshot.AccessTech)
	}
	for _, command := range client.commands {
		if command == "AT+COPS?" {
			t.Fatal("Refresh used hard-coded AT+COPS? instead of the operator profile")
		}
	}
	profileQuerySeen := false
	for _, command := range client.commands {
		if command == "AT+XOPS?" {
			profileQuerySeen = true
			break
		}
	}
	if !profileQuerySeen {
		t.Fatalf("Refresh commands do not contain profile query: %v", client.commands)
	}
}

func TestRefreshRetainsServingPLMNWhenOperatorProfileParseFails(t *testing.T) {
	client := &operatorProfileSnapshotClient{
		servingCellLine: `+QENG: "servingcell","NOCONN","LTE","FDD",460,01,5F1E805,37,1650,3,5,5,8340,-97,-10,-68,15,9`,
		operatorLines:   []string{"+XOPS: malformed selection"},
	}
	manager, id := newStartedTestManager(t, client)
	profile := testOperatorProfile{
		queryCommand: "AT+XOPS?",
		selectionErr: errors.New("operator profile response unavailable"),
	}
	configureTestOperatorProfile(t, manager, id, "19d2", "0536", profile)

	snapshot, err := manager.Refresh(context.Background(), id)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if snapshot.OperatorCode != "46001" {
		t.Fatalf("operator code = %q, want serving PLMN fallback 46001", snapshot.OperatorCode)
	}
}

func TestManagerRefreshReadsNativeWWANICCIDThroughQMIUIM(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "ATI", response: okResponse("Qualcomm", "PCIe/MHI WWAN modem", "Revision: native-410")},
		{command: "AT+CPIN?", response: okResponse("+CPIN: READY")},
		{command: "AT+CCID", response: modem.Response{Final: "ERROR"}, err: errors.New("CCID unsupported")},
		{command: "AT+QCCID", response: modem.Response{Final: "ERROR"}, err: errors.New("QCCID unsupported")},
		{command: "AT+MCCID", response: modem.Response{Final: "ERROR"}, err: errors.New("MCCID unsupported")},
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
