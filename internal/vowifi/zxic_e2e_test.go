package vowifi

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/modem"
)

// ZXIC platform end-to-end coverage: the real ZXICAdapter drives
// radio/SIM/AKA through an ordered APDU transcript while the existing
// orchestrator fakes stand in for proxy/tunnel/IMS. The tunnel fake performs
// EAP-AKA through the request's AKAProvider, exactly like the real IKE
// provider, so the CCHO/CGLA manager entry points are exercised inside a
// full Orchestrator.Enable.
//
// The transcript is the chipset dialect itself: identity comes from the
// prefixed +CGMI-style probes elsewhere, the ICCID only from AT+ZICCID?, the
// CSIM/CGLA APDUs use the unquoted form the firmware accepts, and none of
// the commands it rejects with vendor +CME ERROR: 6003 (ATI, the AT+CCID
// family, AT+CUAD, AT+CLCC) may be issued.
const (
	zxicE2EIMSI  = "234150123456789"
	zxicE2EICCID = "8944101234567890123"
	zxicE2EUSIM  = "A0000000871002FFFFFFFF8903020000"
)

var zxicE2EChallenge = func() AKAChallenge {
	var challenge AKAChallenge
	for index := range challenge.RAND {
		challenge.RAND[index] = byte(index)
		challenge.AUTN[index] = byte(0xf0 + index)
	}
	return challenge
}()

// zxicE2EExecutor records every adapter event in order on top of the shared
// strict transcript, so tests can assert cross-phase command ordering and the
// fail-closed absence of CFUN=1 / CGACT=1,1.
type zxicE2EExecutor struct {
	*ec20Transcript
	mu     sync.Mutex
	events []string
}

func newZXICE2EExecutor(t *testing.T, steps []ec20TranscriptStep) *zxicE2EExecutor {
	t.Helper()
	return &zxicE2EExecutor{ec20Transcript: &ec20Transcript{t: t, steps: steps}}
}

func (executor *zxicE2EExecutor) ExecuteAT(ctx context.Context, id, command string) (modem.Response, error) {
	executor.record("AT " + command)
	return executor.ec20Transcript.ExecuteAT(ctx, id, command)
}

func (executor *zxicE2EExecutor) ExecuteAPDU(
	ctx context.Context,
	id string,
	command modem.APDUCommand,
	channel int,
	apdu []byte,
) ([]byte, error) {
	executor.record(fmt.Sprintf("APDU CGLA/CSIM=%v ch=%d %X", command, channel, apdu))
	return executor.ec20Transcript.ExecuteAPDU(ctx, id, command, channel, apdu)
}

func (executor *zxicE2EExecutor) record(event string) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.events = append(executor.events, event)
}

func (executor *zxicE2EExecutor) eventSnapshot() []string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]string(nil), executor.events...)
}

func (executor *zxicE2EExecutor) assertAbsent(t *testing.T, forbidden ...string) {
	t.Helper()
	for _, event := range executor.eventSnapshot() {
		for _, needle := range forbidden {
			if strings.Contains(event, needle) {
				t.Fatalf("forbidden command %q in transcript: %s", needle, event)
			}
		}
	}
}

func (executor *zxicE2EExecutor) assertOrder(t *testing.T, first, second string) {
	t.Helper()
	events := executor.eventSnapshot()
	firstIndex, secondIndex := -1, -1
	for index, event := range events {
		if firstIndex < 0 && strings.Contains(event, first) {
			firstIndex = index
		}
		if secondIndex < 0 && strings.Contains(event, second) {
			secondIndex = index
		}
	}
	if firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex {
		t.Fatalf("command order %q (index %d) before %q (index %d) violated: %v",
			first, firstIndex, second, secondIndex, events)
	}
}

// zxicE2EBasicAPDU builds one basic-channel CSIM transcript step in the
// unquoted form the ZXIC firmware accepts.
func zxicE2EBasicAPDU(t *testing.T, apdu []byte, body []byte) ec20TranscriptStep {
	t.Helper()
	profile := modem.ResolveATProfile(modem.Candidate{}).APDU
	command, err := profile.Build(modem.APDUCSIM, 0, apdu, modem.APDUUnquoted)
	if err != nil {
		t.Fatalf("build basic-channel APDU: %v", err)
	}
	response := append(append([]byte(nil), body...), 0x90, 0x00)
	return ec20TranscriptStep{
		command:     command,
		apduCommand: modem.APDUCSIM, channel: 0, apdu: apdu,
		apduForm: modem.APDUUnquoted, sensitive: true,
		lines: []string{fmt.Sprintf(`+CSIM: %d,%X`, len(response)*2, response)},
	}
}

// zxicE2ETunnel performs EAP-AKA through TunnelRequest.AKA like the real IKE
// provider, then optionally fails tunnel establishment.
type zxicE2ETunnel struct {
	environment *fakeEnvironment
	failStart   bool
}

func (tunnel zxicE2ETunnel) Start(ctx context.Context, request TunnelRequest) (TunnelSession, error) {
	if err := tunnel.environment.record(ctx, "tunnel.start"); err != nil {
		return nil, err
	}
	result, err := request.AKA.Authenticate(ctx, request.Identity, zxicE2EChallenge)
	if err != nil {
		return nil, fmt.Errorf("EAP-AKA through the tunnel: %w", err)
	}
	if len(result.RES) == 0 {
		return nil, errors.New("tunnel EAP-AKA produced no RES")
	}
	if tunnel.failStart {
		return nil, errors.New("tunnel establishment failed")
	}
	tunnel.environment.mu.Lock()
	tunnel.environment.tunnelRequests = append(tunnel.environment.tunnelRequests, request)
	tunnel.environment.mu.Unlock()
	return &fakeTunnelSession{environment: tunnel.environment}, nil
}

func newZXICVoWiFiTestOrchestrator(
	t *testing.T,
	environment *fakeEnvironment,
	executor *zxicE2EExecutor,
	tunnel TunnelProvider,
) (*Orchestrator, *ZXICAdapter) {
	t.Helper()
	adapter, err := NewZXICAdapter(executor, ATModemAdapterOptions{})
	if err != nil {
		t.Fatalf("NewZXICAdapter: %v", err)
	}
	orchestrator, err := New(Dependencies{
		SIM:    adapter,
		AKA:    adapter,
		Radio:  adapter,
		Proxy:  fakeProxy{environment},
		Tunnel: tunnel,
		IMS:    fakeIMSProvider{environment},
		Phones: fakePhones{environment},
	}, Options{DeviceID: "zxic-family", CleanupTimeout: time.Second})
	if err != nil {
		t.Fatalf("New orchestrator: %v", err)
	}
	return orchestrator, adapter
}

// zxicE2EEnableTranscript scripts the ZXIC platform through the full
// successful enable: RF-off, PDP stop, SIM identity, EF_DIR application
// discovery over the unquoted basic channel, AKA readiness, and the
// tunnel-driven AKA exchange. When akaFails is set, the CGLA APDU is
// rejected so the tunnel's EAP-AKA attempt fails.
func zxicE2EEnableTranscript(t *testing.T, akaFails bool) []ec20TranscriptStep {
	t.Helper()
	authAPDU := buildUSIMAuthenticateAPDU(zxicE2EChallenge)
	profile := modem.ResolveATProfile(modem.Candidate{}).APDU
	cglACommand, err := profile.Build(modem.APDUCGLA, 2, authAPDU, modem.APDUUnquoted)
	if err != nil {
		t.Fatalf("build unquoted CGLA command: %v", err)
	}
	akaStep := ec20TranscriptStep{
		command:     cglACommand,
		apduCommand: modem.APDUCGLA, channel: 2, apdu: authAPDU,
		apduForm: modem.APDUUnquoted, sensitive: true,
		lines: []string{fmt.Sprintf(`+CGLA: %d,%X`, len(successfulUSIMResponse())*2, successfulUSIMResponse())},
	}
	if akaFails {
		akaStep.lines = nil
		akaStep.err = &modem.APDUCommandRejectedError{Command: modem.APDUCGLA, Final: "ERROR"}
	}
	// EF_DIR record 1 carries one application: the tagged USIM AID.
	efDirRecord := append([]byte{0x61, 0x12, 0x4f, 0x10}, mustHex(t, zxicE2EUSIM)...)
	return []ec20TranscriptStep{
		// radio.snapshot
		{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
		{command: "AT+CGACT?", lines: []string{"+CGACT: 1,1", "+CGACT: 2,0"}},
		// radio.rf_off
		{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
		{command: "AT+CFUN=4", lines: []string{"OK"}},
		{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
		// radio.stop_data
		{command: "AT+CGACT?", lines: []string{"+CGACT: 1,1"}},
		{command: "AT+CGACT=0,1", lines: []string{"OK"}},
		{command: "AT+CGACT?", lines: []string{"+CGACT: 1,0"}},
		// sim.identity
		{command: "AT+CPIN?", lines: []string{"+CPIN: READY"}},
		{command: "AT+CIMI", lines: []string{zxicE2EIMSI}},
		{command: "AT+ZICCID?", lines: []string{"+ZICCID: " + zxicE2EICCID}},
		{command: "AT+CGSN", lines: []string{"867530912345678"}},
		{command: "AT+CRSM=176,28589,0,0,4", lines: []string{`+CRSM: 144,0,"00000002"`}},
		// aka.ready: live ICCID check, EF_DIR application discovery over the
		// basic channel, then the logical-channel probe.
		{command: "AT+ZICCID?", lines: []string{"+ZICCID: " + zxicE2EICCID}},
		zxicE2EBasicAPDU(t, []byte{0x00, 0xa4, 0x00, 0x04, 0x02, 0x3f, 0x00, 0x00}, nil),
		zxicE2EBasicAPDU(t, []byte{0x00, 0xa4, 0x00, 0x04, 0x02, 0x2f, 0x00, 0x00}, nil),
		zxicE2EBasicAPDU(t, []byte{0x00, 0xb2, 0x01, 0x04, 0x00}, efDirRecord),
		{command: `AT+CCHO="` + zxicE2EUSIM + `"`, lines: []string{"+CCHO: 2"}},
		{command: "AT+CCHC=2", lines: []string{"OK"}},
		// SMS-over-IMS addressing read before the tunnel is built
		{command: "AT+CSCA?", lines: []string{`+CSCA: "+447785016005",145`}},
		// aka.authenticate driven by the tunnel (EAP-AKA)
		{command: "AT+ZICCID?", lines: []string{"+ZICCID: " + zxicE2EICCID}},
		{command: `AT+CCHO="` + zxicE2EUSIM + `"`, lines: []string{"+CCHO: 2"}},
		akaStep,
		{command: "AT+CCHC=2", lines: []string{"OK"}},
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex %q: %v", value, err)
	}
	return decoded
}

func TestZXICVoWiFiEnableUsesATDialectAndLeavesCellularDataOff(t *testing.T) {
	environment := newFakeEnvironment()
	executor := newZXICE2EExecutor(t, zxicE2EEnableTranscript(t, false))
	orchestrator, _ := newZXICVoWiFiTestOrchestrator(
		t, environment, executor, zxicE2ETunnel{environment: environment},
	)

	state, err := orchestrator.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if state.Phase != PhaseSMSReady || !state.Active || !state.TunnelReady || !state.IMSReady || !state.SMSReady {
		t.Fatalf("Enable() state = %+v", state)
	}
	executor.assertDone()

	// Evidence order across the whole stack: RF off, PDP stop, SIM identity,
	// AKA exchange (CCHO then manager APDU), then tunnel/IMS readiness.
	events := executor.eventSnapshot()
	if !containsEvent(events, "AT+CFUN=4") {
		t.Fatalf("missing CFUN=4 in events: %v", events)
	}
	if !containsEvent(events, "AT+ZICCID?") {
		t.Fatalf("missing the ZICCID ICCID query in events: %v", events)
	}
	executor.assertOrder(t, "AT+CFUN=4", "AT+CGACT=0,1")
	executor.assertOrder(t, "AT+CGACT=0,1", "AT+CPIN?")
	executor.assertOrder(t, "AT+CPIN?", "APDU CGLA/CSIM=1 ch=0")
	executor.assertOrder(t, "APDU CGLA/CSIM=1 ch=0", `AT+CCHO="`)
	executor.assertOrder(t, "AT AT+CSCA?", "APDU CGLA/CSIM=2 ch=2")
	if last := events[len(events)-1]; last != "AT AT+CCHC=2" {
		t.Fatalf("last event = %q, want the AKA channel closed after the CGLA exchange", last)
	}
	if calls := environment.callsSnapshot(); !equalStrings(calls, []string{
		"proxy.resolve",
		"tunnel.start",
		"tunnel.evidence",
		"ims.start",
		"ims.evidence",
		"phone.save",
		"ims.sms",
	}) {
		t.Fatalf("provider calls = %v, want proxy→tunnel→IMS order", calls)
	}

	// A successful enable must leave RF off and must never start billable
	// cellular data.
	executor.assertAbsent(t, "AT+CFUN=1", "AT+CGACT=1,1")

	// None of the commands the platform firmware rejects with vendor
	// +CME ERROR: 6003 may be spent on the control channel.
	executor.assertAbsent(t, "AT ATI", "AT+CCID", "AT+QCCID", "AT+MCCID", "AT+CUAD", "AT+CLCC")
}

func TestZXICVoWiFiFailureDoesNotRestoreRFOrCellularData(t *testing.T) {
	t.Run("AKA failure inside the tunnel", func(t *testing.T) {
		environment := newFakeEnvironment()
		executor := newZXICE2EExecutor(t, zxicE2EEnableTranscript(t, true))
		orchestrator, _ := newZXICVoWiFiTestOrchestrator(
			t, environment, executor, zxicE2ETunnel{environment: environment},
		)

		if _, err := orchestrator.Enable(context.Background()); err == nil {
			t.Fatal("Enable() error = nil, want AKA failure")
		} else {
			// Failure text must stay scrubbed: no challenge material, no APDU
			// hex, no subscriber/device identities.
			upperError := strings.ToUpper(err.Error())
			for _, needle := range []string{
				strings.ToUpper(hex.EncodeToString(zxicE2EChallenge.RAND[:])),
				strings.ToUpper(hex.EncodeToString(zxicE2EChallenge.AUTN[:])),
				strings.ToUpper(hex.EncodeToString(buildUSIMAuthenticateAPDU(zxicE2EChallenge))),
				zxicE2EIMSI,
				zxicE2EICCID,
				"867530912345678",
			} {
				if needle != "" && strings.Contains(upperError, needle) {
					t.Fatalf("AKA failure leaked sensitive material %q: %q", needle, err)
				}
			}
		}
		executor.assertDone()
		executor.assertAbsent(t, "AT+CFUN=1", "AT+CGACT=1,1")
		if calls := environment.callCount("ims.start"); calls != 0 {
			t.Fatalf("ims.start calls = %d, want none after AKA failure", calls)
		}
		if calls := environment.callCount("tunnel.close"); calls != 0 {
			t.Fatalf("tunnel.close calls = %d, want none for an unestablished tunnel", calls)
		}
	})

	t.Run("tunnel establishment failure", func(t *testing.T) {
		environment := newFakeEnvironment()
		executor := newZXICE2EExecutor(t, zxicE2EEnableTranscript(t, false))
		orchestrator, _ := newZXICVoWiFiTestOrchestrator(
			t, environment, executor, zxicE2ETunnel{environment: environment, failStart: true},
		)

		if _, err := orchestrator.Enable(context.Background()); err == nil {
			t.Fatal("Enable() error = nil, want tunnel failure")
		}
		executor.assertDone()
		executor.assertAbsent(t, "AT+CFUN=1", "AT+CGACT=1,1")
		if calls := environment.callCount("ims.start"); calls != 0 {
			t.Fatalf("ims.start calls = %d, want none after tunnel failure", calls)
		}
	})
}

func TestZXICVoWiFiDisableAcceptsVerifiedRFOffInventoryRefusal(t *testing.T) {
	steps := zxicE2EEnableTranscript(t, false)
	// Exercise the RF-off refusal in StopCellularData as well as Restore.
	steps = append(steps[:5], append([]ec20TranscriptStep{
		{command: "AT+CGACT?", err: zxicVendorBusy("AT+CGACT?")},
		{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
		{command: "AT+CGACT?", err: zxicVendorBusy("AT+CGACT?")},
		{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
	}, steps[8:]...)...)
	steps = append(steps,
		ec20TranscriptStep{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
		ec20TranscriptStep{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
		ec20TranscriptStep{command: "AT+CGACT?", err: zxicVendorBusy("AT+CGACT?")},
		ec20TranscriptStep{command: "AT+CGACT?", err: zxicVendorBusy("AT+CGACT?")},
	)
	environment := newFakeEnvironment()
	executor := newZXICE2EExecutor(t, steps)
	orchestrator, adapter := newZXICVoWiFiTestOrchestrator(t, environment, executor, zxicE2ETunnel{environment: environment})
	adapter.options.PureAirplanePolicy = func(string) bool { return true }
	if state, err := orchestrator.Enable(context.Background()); err != nil || state.Phase != PhaseSMSReady {
		t.Fatalf("Enable = %+v, %v", state, err)
	}
	if state, err := orchestrator.Disable(context.Background()); err != nil || state.Phase != PhaseIdle || len(state.CleanupErrors) != 0 {
		t.Fatalf("Disable = %+v, %v", state, err)
	}
	adapter.mu.Lock()
	_, retained := adapter.checkpoints["zxic-family"]
	adapter.mu.Unlock()
	if retained {
		t.Fatal("radio checkpoint retained after cleanup")
	}
	executor.assertDone()
	executor.assertAbsent(t, "AT+CFUN=1", "AT+CGACT=1,1")
}

func containsEvent(events []string, needle string) bool {
	for _, event := range events {
		if strings.Contains(event, needle) {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
