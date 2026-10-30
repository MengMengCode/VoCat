package device

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/modem"
)

// zxicScriptedClient answers the commands the ZXIC platform firmware implements and
// rejects everything else with the vendor's "command not recognized" error,
// exactly like the live unit. Tests assert which commands are issued rather
// than a brittle full ordering, because the snapshot's optional probes evolve
// independently of the identity dialect.
type zxicScriptedClient struct {
	mu        sync.Mutex
	responses map[string][]string
	calls     []string
}

func (client *zxicScriptedClient) Execute(_ context.Context, command string) (modem.Response, error) {
	client.mu.Lock()
	client.calls = append(client.calls, command)
	lines, scripted := client.responses[command]
	client.mu.Unlock()
	if scripted {
		return modem.Response{Lines: lines, Final: "OK"}, nil
	}
	return modem.Response{}, &modem.CommandError{Command: command, Final: "+CME ERROR: 6003"}
}

func (client *zxicScriptedClient) WaitURC(context.Context, func(string) bool) (string, error) {
	return "", context.DeadlineExceeded
}

func (client *zxicScriptedClient) Close() error { return nil }

func (client *zxicScriptedClient) called(command string) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	for _, recorded := range client.calls {
		if strings.EqualFold(strings.TrimSpace(recorded), command) {
			return true
		}
	}
	return false
}

// newZXICSnapshotTestManager starts a manager over one ZXIC candidate whose
// USB identity selects the chipset-family profile.
func newZXICSnapshotTestManager(t *testing.T, client modem.Client) (*Manager, string) {
	t.Helper()
	const id = "zxic-snapshot"
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:        id,
			VendorID:  "19d2",
			ProductID: "0536",
			ATPort: modem.Port{
				Path: "/dev/ttyUSB0",
				Name: "ttyUSB0",
				Role: modem.PortRoleAT,
			},
		}}},
		Opener:         &staticOpener{client: client},
		CommandTimeout: time.Second,
		LongTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager, id
}

// TestManagerRefreshBuildsZXICSnapshot pins the ZXIC snapshot dialect
// verified on the live hardware: identity via the prefixed +CGMI/+CGMM/+CGMR
// probes, ICCID via the AT+ZICCID? query, and none of the commands the
// firmware rejects with +CME ERROR: 6003 (ATI, the AT+CCID family, or
// AT+CLCC) may be issued.
func TestManagerRefreshBuildsZXICSnapshot(t *testing.T) {
	client := &zxicScriptedClient{responses: map[string][]string{
		"AT+CGMI":    {"+CGMI: UZ901_KSBC_JH_VSIM"},
		"AT+CGMM":    {"+CGMM: UZ901"},
		"AT+CGMR":    {"+CGMR: UZ901DW3.0_KSBC_JH_VSIM_SL_V01.01.02P48U06_01"},
		"AT+CPIN?":   {"+CPIN: READY"},
		"AT+ZICCID?": {"+ZICCID: 89441000400317114751"},
		"AT+CIMI":    {"+CIMI: 234159612827125"},
		"AT+CSQ":     {"+CSQ: 20,99"},
		"AT+COPS?":   {`+COPS: 0,2,"46000",7,0`},
		"AT+CEREG?":  {`+CEREG: 2,5,"268A","0BB766C0",7,0`},
		"AT+CGSN":    {"+CGSN: 865240070019472"},
		"AT+CFUN?":   {"+CFUN: 1"},
	}}
	manager, id := newZXICSnapshotTestManager(t, client)

	snapshot, err := manager.Refresh(context.Background(), id)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !snapshot.Responsive || !snapshot.SIMReady || snapshot.SIMStatus != "ready" {
		t.Fatalf("modem/SIM state = %#v", snapshot)
	}
	if snapshot.Manufacturer != "UZ901_KSBC_JH_VSIM" ||
		snapshot.Model != "UZ901" ||
		snapshot.Firmware != "UZ901DW3.0_KSBC_JH_VSIM_SL_V01.01.02P48U06_01" {
		t.Fatalf(
			"identity = manufacturer %q, model %q, firmware %q",
			snapshot.Manufacturer, snapshot.Model, snapshot.Firmware,
		)
	}
	if snapshot.ICCID != "89441000400317114751" {
		t.Fatalf("ICCID = %q", snapshot.ICCID)
	}
	if snapshot.IMSI != "234159612827125" {
		t.Fatalf("IMSI = %q", snapshot.IMSI)
	}
	if snapshot.IMEI != "865240070019472" {
		t.Fatalf("IMEI = %q", snapshot.IMEI)
	}
	if snapshot.OperatorCode != "46000" {
		t.Fatalf("operator code = %q, want 46000 from COPS numeric format", snapshot.OperatorCode)
	}
	if snapshot.RegistrationStatus == 0 {
		t.Fatalf("registration = %#v, want roaming registration from CEREG", snapshot.RegistrationStatus)
	}

	for _, command := range []string{"AT+CGMI", "AT+CGMM", "AT+CGMR", "AT+ZICCID?"} {
		if !client.called(command) {
			t.Fatalf("ZXIC snapshot never issued %s", command)
		}
	}
	// Everything the firmware rejects must stay unsent: issuing them burns
	// the control channel and pollutes the snapshot with vendor errors.
	for _, forbidden := range []string{"ATI", "AT+CCID", "AT+QCCID", "AT+MCCID", "AT+CLCC"} {
		if client.called(forbidden) {
			t.Fatalf("ZXIC snapshot issued %s, which the firmware rejects with +CME ERROR: 6003", forbidden)
		}
	}
}

func TestSnapshotKeepsME3630ATIIdentity(t *testing.T) {
	client := &zxicScriptedClient{responses: map[string][]string{
		"ATI":     {"Manufacturer:GOSUNCNWELINK", "Model:ME3630", "Revision:ME3630U1AV1.0B04"},
		"AT+CCID": {"+CCID: 89441000400317114751"},
	}}
	manager := &Manager{commandTimeout: time.Second}
	snapshot, err := manager.readSnapshot(context.Background(), "me3630", modem.Candidate{
		VendorID: "19d2", ProductID: "0601", Product: "ME3630",
	}, "at", "", nil, client)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Firmware != "ME3630U1AV1.0B04" || snapshot.ICCID != "89441000400317114751" {
		t.Fatalf("standard ZTE identity was lost: %+v", snapshot)
	}
	if !client.called("ATI") || !client.called("AT+CCID") || client.called("AT+ZICCID?") {
		t.Fatalf("ME3630 used vendor override: %v", client.calls)
	}
}

func TestZXICIdentityAcceptsBareResponsesWithoutURCs(t *testing.T) {
	client := &zxicScriptedClient{responses: map[string][]string{
		"AT+CGMI": {"RDY", "AT+CGMI", "+CEREG: 1", "UZ901_KSBC_JH_VSIM"},
		"AT+CGMM": {"RING", "^SYSSTART", "UZ901", "OK"},
		"AT+CGMR": {"PB DONE", "+QIURC: ready", "UZ901_V01.02", "NO CARRIER"},
	}}
	manager := &Manager{commandTimeout: time.Second}
	var snapshot Snapshot
	profile := modem.ResolveATProfile(modem.Candidate{VendorID: "19d2", ProductID: "0536"})
	if err := manager.probeIdentity(context.Background(), client, profile.Identity, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Manufacturer != "UZ901_KSBC_JH_VSIM" || snapshot.Model != "UZ901" || snapshot.Firmware != "UZ901_V01.02" {
		t.Fatalf("bare identity = %+v", snapshot)
	}
}

func TestIdentityValueRejectsUnrelatedLines(t *testing.T) {
	for _, lines := range [][]string{
		{"AT+CGMM", "RING", "RDY", "SMS READY", "CALL READY", "PB DONE", "OK"},
		{"+CEREG: 1", "^SYSSTART", "%READY", "$QIND: 1"},
		{"unsolicited bare text", "UZ901"},
	} {
		if got := identityValue(modem.Response{Lines: lines}, "AT+CGMM"); got != "" {
			t.Fatalf("identityValue(%v) = %q, want no identity", lines, got)
		}
	}
	if got := identityValue(modem.Response{Lines: []string{"unrelated", "+CGMM: UZ901"}}, "AT+CGMM"); got != "UZ901" {
		t.Fatalf("prefixed identity did not take priority: %q", got)
	}
}

// TestExecuteATToleratesUnsupportedCLCC pins the control-channel health rule:
// a CLCC rejection — 3GPP "operation not supported" (ERROR: 4) or the ZXIC
// vendor "command not recognized" (6003) — must not poison the device's
// last error, while the error is still returned to the caller.
func TestExecuteATToleratesUnsupportedCLCC(t *testing.T) {
	for _, final := range []string{"+CME ERROR: 4", "+CME ERROR: 6003"} {
		client := &transcriptClient{steps: []clientStep{
			{command: "AT+CLCC", err: &modem.CommandError{Command: "AT+CLCC", Final: final}},
		}}
		manager, id := newStartedTestManager(t, client)
		if _, err := manager.ExecuteAT(context.Background(), id, "AT+CLCC"); err == nil {
			t.Fatalf("ExecuteAT(%s) error = nil, want modem rejection", final)
		}
		entry, getErr := manager.Get(id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if entry.LastError != "" {
			t.Fatalf("LastError = %q after %s, want suppressed", entry.LastError, final)
		}
	}
}
