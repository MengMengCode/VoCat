package vowifi

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"vocat/internal/modem"
)

type ec20TranscriptStep struct {
	command   string
	sensitive bool
	lines     []string
	final     string
	err       error
}

type ec20Transcript struct {
	t     *testing.T
	mu    sync.Mutex
	steps []ec20TranscriptStep
	next  int
}

func TestICCIDIdentifierStripsBCDPadding(t *testing.T) {
	for _, test := range []struct {
		wire string
		want string
	}{
		{wire: "8944110069353447454F", want: "8944110069353447454"},
		{wire: "894921007608519523FF", want: "894921007608519523"},
	} {
		response := modem.Response{Lines: []string{"+QCCID: " + test.wire}}
		if got := iccidIdentifier(response, []string{"+CCID:", "+QCCID:"}, 18, 22); got != test.want {
			t.Fatalf("iccidIdentifier(%q) = %q, want %q", test.wire, got, test.want)
		}
	}
}

func (transcript *ec20Transcript) ExecuteAT(
	_ context.Context,
	_ string,
	command string,
) (modem.Response, error) {
	return transcript.execute(command, false)
}

func (transcript *ec20Transcript) ExecuteSensitiveAT(
	_ context.Context,
	_ string,
	command string,
) (modem.Response, error) {
	return transcript.execute(command, true)
}

func (transcript *ec20Transcript) execute(
	command string,
	sensitive bool,
) (modem.Response, error) {
	transcript.t.Helper()
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	if transcript.next >= len(transcript.steps) {
		transcript.t.Fatalf("unexpected EC20 command %q", command)
	}
	step := transcript.steps[transcript.next]
	transcript.next++
	if command != step.command {
		transcript.t.Fatalf(
			"EC20 command %d = %q, want %q",
			transcript.next,
			command,
			step.command,
		)
	}
	if sensitive != step.sensitive {
		transcript.t.Fatalf(
			"EC20 command %q sensitive=%v, want %v",
			command,
			sensitive,
			step.sensitive,
		)
	}
	final := step.final
	if final == "" && step.err == nil {
		final = "OK"
	}
	return modem.Response{
		Command: command,
		Lines:   append([]string(nil), step.lines...),
		Final:   final,
	}, step.err
}

func (transcript *ec20Transcript) assertDone() {
	transcript.t.Helper()
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	if transcript.next != len(transcript.steps) {
		transcript.t.Fatalf(
			"consumed %d/%d EC20 transcript steps",
			transcript.next,
			len(transcript.steps),
		)
	}
}

func TestEC20AdapterCSIMFallbackSupportsSuccessAndSynchronizationFailure(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name    string
		apdu    []byte
		wantErr error
		assert  func(*testing.T, AKAResult)
	}{
		{
			name: "success",
			apdu: successfulUSIMResponse(),
			assert: func(t *testing.T, result AKAResult) {
				t.Helper()
				if result.SynchronizationFailure {
					t.Fatal("successful result was marked as synchronization failure")
				}
				if !bytes.Equal(result.RES, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
					t.Fatalf("RES = %x", result.RES)
				}
				if len(result.CK) != 16 || len(result.IK) != 16 {
					t.Fatalf("CK/IK lengths = %d/%d", len(result.CK), len(result.IK))
				}
			},
		},
		{
			name: "synchronization_failure",
			apdu: synchronizationFailureUSIMResponse(),
			assert: func(t *testing.T, result AKAResult) {
				t.Helper()
				if !result.SynchronizationFailure {
					t.Fatal("AUTS result was not marked as synchronization failure")
				}
				if len(result.AUTS) != 14 {
					t.Fatalf("AUTS length = %d", len(result.AUTS))
				}
				if len(result.RES) != 0 || len(result.CK) != 0 || len(result.IK) != 0 {
					t.Fatal("synchronization failure exposed a success vector")
				}
			},
		},
		{
			name:    "mac_failure_9862",
			apdu:    []byte{0x98, 0x62},
			wantErr: ErrEC20AKAMACFailure,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var challenge AKAChallenge
			for index := range challenge.RAND {
				challenge.RAND[index] = byte(index)
				challenge.AUTN[index] = byte(0xf0 + index)
			}
			authAPDU := buildUSIMAuthenticateAPDU(challenge)
			authCommand := fmt.Sprintf(
				`AT+CSIM=%d,"%s"`,
				len(authAPDU)*2,
				strings.ToUpper(hex.EncodeToString(authAPDU)),
			)
			encodedResponse := strings.ToUpper(hex.EncodeToString(test.apdu))

			transcript := &ec20Transcript{
				t: t,
				steps: append(
					identityTranscriptSteps("234150123456789"),
					ec20TranscriptStep{
						command: "AT+CCID",
						lines:   []string{"+CCID: 8944101234567890123"},
					},
					ec20TranscriptStep{
						command: "AT+CUAD",
						lines: []string{
							`+CUAD: 22,"61094F07A0000000871002"`,
						},
					},
					ec20TranscriptStep{
						command: `AT+CCHO="A0000000871002"`,
						err:     errors.New("unsupported"),
						final:   "ERROR",
					},
					// The SELECT response requests GET RESPONSE. This is the
					// behavior observed on EC20 basic-channel firmware.
					ec20TranscriptStep{
						command: `AT+CSIM=24,"00A4040407A0000000871002"`,
						lines:   []string{`+CSIM: 4,"613A"`},
					},
					ec20TranscriptStep{
						command: `AT+CSIM=10,"00C000003A"`,
						lines:   []string{`+CSIM: 4,"9000"`},
					},
					ec20TranscriptStep{
						command: "AT+CCID",
						lines:   []string{"+CCID: 8944101234567890123"},
					},
					ec20TranscriptStep{
						command: `AT+CSIM=24,"00A4040407A0000000871002"`,
						lines:   []string{`+CSIM: 4,"9000"`},
					},
					ec20TranscriptStep{
						command:   authCommand,
						sensitive: true,
						lines: []string{fmt.Sprintf(
							`+CSIM: %d,"%s"`,
							len(encodedResponse),
							encodedResponse,
						)},
					},
				),
			}
			adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			identity, err := adapter.ReadIdentity(context.Background(), "ec20-1")
			if err != nil {
				t.Fatalf("ReadIdentity: %v", err)
			}
			evidence, err := adapter.CheckReady(context.Background(), identity)
			if err != nil {
				t.Fatalf("CheckReady: %v", err)
			}
			if !evidence.Ready || evidence.Application != "USIM" {
				t.Fatalf("AKA evidence = %#v", evidence)
			}
			result, err := adapter.Authenticate(
				context.Background(),
				identity,
				challenge,
			)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Authenticate error = %v, want %v", err, test.wantErr)
				}
				transcript.assertDone()
				return
			}
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			test.assert(t, result)
			transcript.assertDone()
		})
	}
}

func TestEC20AdapterDiscoversFullUSIMAIDFromEFDIRWhenCUADFails(t *testing.T) {
	t.Parallel()
	const fullAID = "A0000000871002FFFFFFFF8903020000"
	record := "61184F10" + fullAID + "50045553494D"
	encodedResponse := strings.ToUpper(hex.EncodeToString(successfulUSIMResponse()))
	var challenge AKAChallenge
	for index := range challenge.RAND {
		challenge.RAND[index] = byte(index)
		challenge.AUTN[index] = byte(0xf0 + index)
	}
	authAPDU := buildUSIMAuthenticateAPDU(challenge)
	authCommand := fmt.Sprintf(
		`AT+CSIM=%d,"%s"`,
		len(authAPDU)*2,
		strings.ToUpper(hex.EncodeToString(authAPDU)),
	)
	selectApplication := `AT+CSIM=42,"00A4040410` + fullAID + `"`
	transcript := &ec20Transcript{
		t: t,
		steps: append(
			identityTranscriptStepsWithoutEFAD("310280000000001"),
			[]ec20TranscriptStep{
				{command: "AT+CCID", lines: []string{"+CCID: 8944101234567890123"}},
				{command: "AT+CUAD", err: errors.New("+CME ERROR: 13"), final: "+CME ERROR: 13"},
				{command: `AT+CSIM=16,"00A40004023F0000"`, lines: []string{`+CSIM: 4,"9000"`}},
				{command: `AT+CSIM=16,"00A40004022F0000"`, lines: []string{`+CSIM: 4,"9000"`}},
				{command: `AT+CSIM=10,"00B2010400"`, lines: []string{`+CSIM: 4,"6C1A"`}},
				{command: `AT+CSIM=10,"00B201041A"`, lines: []string{fmt.Sprintf(`+CSIM: %d,"%s9000"`, len(record)+4, record)}},
				{command: `AT+CCHO="` + fullAID + `"`, err: errors.New("unsupported"), final: "ERROR"},
				{command: selectApplication, lines: []string{`+CSIM: 4,"9000"`}},
				{command: "AT+CCID", lines: []string{"+CCID: 8944101234567890123"}},
				{command: selectApplication, lines: []string{`+CSIM: 4,"9000"`}},
				{command: authCommand, sensitive: true, lines: []string{fmt.Sprintf(`+CSIM: %d,"%s"`, len(encodedResponse), encodedResponse)}},
			}...,
		),
	}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.ReadIdentity(context.Background(), "ec20-1")
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if _, err := adapter.CheckReady(context.Background(), identity); err != nil {
		t.Fatalf("CheckReady: %v", err)
	}
	if _, err := adapter.Authenticate(context.Background(), identity, challenge); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	transcript.assertDone()
}

func TestEC20AdapterLogicalChannelAuthenticateFollowsGetResponse(
	t *testing.T,
) {
	t.Parallel()
	var challenge AKAChallenge
	for index := range challenge.RAND {
		challenge.RAND[index] = byte(index)
		challenge.AUTN[index] = byte(0xf0 + index)
	}
	authAPDU := buildUSIMAuthenticateAPDU(challenge)
	authCommand := fmt.Sprintf(
		`AT+CGLA=1,%d,"%s"`,
		len(authAPDU)*2,
		strings.ToUpper(hex.EncodeToString(authAPDU)),
	)
	chainedResponse := logicalChainedUSIMResponse()
	if len(chainedResponse)-2 != 0x35 {
		t.Fatalf(
			"test response body length = %d, want 0x35",
			len(chainedResponse)-2,
		)
	}
	encodedResponse := strings.ToUpper(hex.EncodeToString(chainedResponse))

	transcript := &ec20Transcript{
		t: t,
		steps: append(
			identityTranscriptSteps("234150123456789"),
			ec20TranscriptStep{
				command: "AT+CCID",
				lines:   []string{"+CCID: 8944101234567890123"},
			},
			ec20TranscriptStep{
				command: "AT+CUAD",
				lines: []string{
					`+CUAD: 22,"61094F07A0000000871002"`,
				},
			},
			ec20TranscriptStep{
				command: `AT+CCHO="A0000000871002"`,
				lines:   []string{"+CCHO: 1"},
			},
			ec20TranscriptStep{command: "AT+CCHC=1"},
			ec20TranscriptStep{
				command: "AT+CCID",
				lines:   []string{"+CCID: 8944101234567890123"},
			},
			ec20TranscriptStep{
				command: `AT+CCHO="A0000000871002"`,
				lines:   []string{"+CCHO: 1"},
			},
			ec20TranscriptStep{
				command:   authCommand,
				sensitive: true,
				lines:     []string{`+CGLA: 4,"6135"`},
			},
			ec20TranscriptStep{
				command:   `AT+CGLA=1,10,"00C0000035"`,
				sensitive: true,
				lines: []string{fmt.Sprintf(
					`+CGLA: %d,"%s"`,
					len(encodedResponse),
					encodedResponse,
				)},
			},
			ec20TranscriptStep{command: "AT+CCHC=1"},
		),
	}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.ReadIdentity(context.Background(), "ec20-1")
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if _, err := adapter.CheckReady(context.Background(), identity); err != nil {
		t.Fatalf("CheckReady: %v", err)
	}
	result, err := adapter.Authenticate(
		context.Background(),
		identity,
		challenge,
	)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !bytes.Equal(result.RES, []byte{1, 2, 3, 4, 5, 6, 7, 8}) ||
		len(result.CK) != 16 ||
		len(result.IK) != 16 {
		t.Fatalf(
			"AKA result RES=%x CK=%d IK=%d",
			result.RES,
			len(result.CK),
			len(result.IK),
		)
	}
	transcript.assertDone()
}

func TestEC20AdapterReadsExplicitHomePLMNAndKnownAssignmentFallback(
	t *testing.T,
) {
	t.Parallel()
	tests := []struct {
		name  string
		steps []ec20TranscriptStep
	}{
		{
			name:  "EF_AD",
			steps: identityTranscriptSteps("234150123456789"),
		},
		{
			name: "assigned HPLMN when EF_AD omits MNC length",
			steps: append(
				identityTranscriptStepsWithoutEFAD("234150123456789"),
				ec20TranscriptStep{
					command: "AT+CRSM=176,28589,0,0,4",
					err:     errors.New("not available"),
					final:   "ERROR",
				},
				ec20TranscriptStep{
					command: "AT+CRSM=176,28589,0,0,0",
					err:     errors.New("not available"),
					final:   "ERROR",
				},
			),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transcript := &ec20Transcript{t: t, steps: test.steps}
			adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			identity, err := adapter.ReadIdentity(context.Background(), "ec20-1")
			if err != nil {
				t.Fatalf("ReadIdentity: %v", err)
			}
			if identity.HomeMCC != "234" || identity.HomeMNC != "15" {
				t.Fatalf(
					"home PLMN = %s/%s, want 234/15",
					identity.HomeMCC,
					identity.HomeMNC,
				)
			}
			transcript.assertDone()
		})
	}
}

func TestAssignedHomePLMNIncludesLebaraUKCores(t *testing.T) {
	tests := map[string]string{
		"204040123456789": "204/04",
		"234150123456789": "234/15",
		"234870123456789": "234/87",
		"310280000000001": "310/280",
	}
	for imsi, want := range tests {
		mcc, mnc, ok := assignedHomePLMN(imsi)
		if got := mcc + "/" + mnc; !ok || got != want {
			t.Errorf("assignedHomePLMN(%q) = %q, %v; want %q", imsi, got, ok, want)
		}
	}
}

func TestEC20AdapterTreatsATT310280AsThreeDigitMNC(t *testing.T) {
	t.Parallel()
	transcript := &ec20Transcript{t: t, steps: identityTranscriptStepsWithoutEFAD("310280000000001")}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.ReadIdentity(context.Background(), "ec20-1")
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if identity.HomeMCC != "310" || identity.HomeMNC != "280" {
		t.Fatalf("home PLMN = %s/%s, want 310/280", identity.HomeMCC, identity.HomeMNC)
	}
	transcript.assertDone()
}

func TestEC20AdapterRadioTransactionRestoresCFUNAndPDPContexts(
	t *testing.T,
) {
	t.Parallel()
	transcript := &ec20Transcript{
		t: t,
		steps: []ec20TranscriptStep{
			{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
			{
				command: "AT+CGACT?",
				lines:   []string{"+CGACT: 1,1", "+CGACT: 2,0"},
			},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
			{command: "AT+CFUN=4"},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
			{
				command: "AT+CGACT?",
				lines:   []string{"+CGACT: 1,0", "+CGACT: 2,0"},
			},
			{
				command: "AT+CGACT?",
				lines:   []string{"+CGACT: 1,0", "+CGACT: 2,0"},
			},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
			{
				command: "AT+CGACT?",
				lines:   []string{"+CGACT: 1,0", "+CGACT: 2,0"},
			},
			{
				command: "AT+CGACT?",
				lines:   []string{"+CGACT: 1,0", "+CGACT: 2,0"},
			},
		},
	}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{
		PureAirplanePolicy:  func(string) bool { return true },
		RestoreCellularData: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := adapter.Snapshot(context.Background(), "ec20-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snapshot.CellularDataEnabled ||
		snapshot.OperatingMode != 1 ||
		!snapshot.PureAirplanePolicy {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := adapter.EnterVoWiFiRFOff(
		context.Background(),
		"ec20-1",
	); err != nil {
		t.Fatalf("EnterVoWiFiRFOff: %v", err)
	}
	if err := adapter.StopCellularData(
		context.Background(),
		"ec20-1",
	); err != nil {
		t.Fatalf("StopCellularData: %v", err)
	}
	if err := adapter.Restore(
		context.Background(),
		"ec20-1",
		snapshot,
	); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	transcript.assertDone()
}

func TestEC20AdapterNeverStartsCellularDataByDefault(t *testing.T) {
	t.Parallel()
	transcript := &ec20Transcript{
		t: t,
		steps: []ec20TranscriptStep{
			{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
			{command: "AT+CGACT?", lines: []string{"+CGACT: 1,1"}},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
			{command: "AT+CGACT?", lines: []string{"+CGACT: 1,1"}},
			{command: "AT+CGACT=0,1"},
			{command: "AT+CGACT?", lines: []string{"+CGACT: 1,0"}},
		},
	}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := adapter.Snapshot(context.Background(), "ec20-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := adapter.Restore(context.Background(), "ec20-1", snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	transcript.assertDone()
}

func identityTranscriptSteps(imsi string) []ec20TranscriptStep {
	return append(
		identityTranscriptStepsWithoutEFAD(imsi),
		ec20TranscriptStep{
			command: "AT+CRSM=176,28589,0,0,4",
			lines:   []string{`+CRSM: 144,0,"00000002"`},
		},
	)
}

func identityTranscriptStepsWithoutEFAD(imsi string) []ec20TranscriptStep {
	return []ec20TranscriptStep{
		{command: "AT+CPIN?", lines: []string{"+CPIN: READY"}},
		{command: "AT+CIMI", lines: []string{imsi}},
		{
			command: "AT+CCID",
			lines:   []string{"+CCID: 8944101234567890123"},
		},
		{command: "AT+CGSN", lines: []string{"867530912345678"}},
	}
}

func successfulUSIMResponse() []byte {
	res := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	ck := bytes.Repeat([]byte{0x11}, 16)
	ik := bytes.Repeat([]byte{0x22}, 16)
	kc := bytes.Repeat([]byte{0x33}, 8)
	value := []byte{byte(len(res))}
	value = append(value, res...)
	value = append(value, byte(len(ck)))
	value = append(value, ck...)
	value = append(value, byte(len(ik)))
	value = append(value, ik...)
	value = append(value, byte(len(kc)))
	value = append(value, kc...)
	raw := []byte{0xdb}
	raw = append(raw, value...)
	return append(raw, 0x90, 0x00)
}

func logicalChainedUSIMResponse() []byte {
	res := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	ck := bytes.Repeat([]byte{0x11}, 16)
	ik := bytes.Repeat([]byte{0x22}, 16)
	kc := bytes.Repeat([]byte{0x33}, 8)
	value := []byte{byte(len(res))}
	value = append(value, res...)
	value = append(value, byte(len(ck)))
	value = append(value, ck...)
	value = append(value, byte(len(ik)))
	value = append(value, ik...)
	value = append(value, byte(len(kc)))
	value = append(value, kc...)
	raw := []byte{0xdb}
	raw = append(raw, value...)
	return append(raw, 0x90, 0x00)
}

func synchronizationFailureUSIMResponse() []byte {
	auts := make([]byte, 14)
	for index := range auts {
		auts[index] = byte(0xa0 + index)
	}
	raw := []byte{0xdc, byte(len(auts))}
	raw = append(raw, auts...)
	return append(raw, 0x90, 0x00)
}

func TestCollectApplicationAIDsSkipsCUADPadding(t *testing.T) {
	t.Parallel()
	response := modem.Response{Lines: []string{
		`+CUAD: "61184F10A0000000871002FFFFFFFF890302000050045553494DFFFFFFFFFFFFFFFFFFFFFFFF""61184F10A0000000871004FFFFFFFF890302000050044953494DFFFFFFFFFFFFFFFFFFFFFFFF"`,
		`"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"`,
	}}
	data, err := parseCUADData(response)
	if err != nil {
		t.Fatal(err)
	}
	aids := collectApplicationAIDs(data)
	want := []string{
		"A0000000871002FFFFFFFF8903020000",
		"A0000000871004FFFFFFFF8903020000",
	}
	if !reflect.DeepEqual(aids, want) {
		t.Fatalf("AIDs = %v, want %v", aids, want)
	}
}

func TestEC20AdapterISIMStrictUsesCUADFullAID(t *testing.T) {
	var challenge AKAChallenge
	for index := range challenge.RAND {
		challenge.RAND[index] = byte(index)
		challenge.AUTN[index] = byte(0xf0 + index)
	}
	authAPDU := buildUSIMAuthenticateAPDU(challenge)
	authCommand := fmt.Sprintf(
		`AT+CGLA=1,%d,"%s"`,
		len(authAPDU)*2,
		strings.ToUpper(hex.EncodeToString(authAPDU)),
	)
	encodedResponse := strings.ToUpper(hex.EncodeToString(successfulUSIMResponse()))
	fullISIM := "A0000000871004FFFFFFFF8903020000"
	cuad := `61184F10A0000000871002FFFFFFFF890302000050045553494D61184F10A0000000871004FFFFFFFF890302000050044953494D`
	transcript := &ec20Transcript{
		t: t,
		steps: []ec20TranscriptStep{
			{command: "AT+CPIN?", lines: []string{"+CPIN: READY"}},
			{command: "AT+CIMI", lines: []string{"310280000000001"}},
			{command: "AT+CCID", lines: []string{"+CCID: 8901000000000000001"}},
			{command: "AT+CGSN", lines: []string{"860000000000001"}},
			{command: "AT+CUAD", lines: []string{`+CUAD: "` + cuad + `"`}},
			{command: "AT+CCID", lines: []string{"+CCID: 8901000000000000001"}},
			{command: `AT+CCHO="` + fullISIM + `"`, lines: []string{"+CCHO: 1"}},
			{
				command:   authCommand,
				sensitive: true,
				lines: []string{fmt.Sprintf(
					`+CGLA: %d,"%s"`,
					len(encodedResponse),
					encodedResponse,
				)},
			},
			{command: "AT+CCHC=1"},
		},
	}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.ReadIdentity(context.Background(), "ec20-1")
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	result, err := adapter.AuthenticateWithPreference(context.Background(), identity, challenge, "isim_strict")
	if err != nil {
		t.Fatalf("AuthenticateWithPreference: %v", err)
	}
	if !bytes.Equal(result.RES, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("RES = %x", result.RES)
	}
	transcript.assertDone()
}

// This executor exercises the real adapter boundary, without a serial device.
type ec20PSIExecutor struct {
	mu  sync.Mutex
	run func(context.Context, string, string) (modem.Response, error)
}

func (e *ec20PSIExecutor) LockUICC()   { e.mu.Lock() }
func (e *ec20PSIExecutor) UnlockUICC() { e.mu.Unlock() }
func (e *ec20PSIExecutor) ExecuteAT(ctx context.Context, id, command string) (modem.Response, error) {
	return e.run(ctx, id, command)
}

func TestEC20ReadSMSCenterPSINoCacheAndLocks(t *testing.T) {
	e := &ec20PSIExecutor{}
	adapter, err := NewEC20Adapter(e, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	e.run = func(gotCtx context.Context, id, command string) (modem.Response, error) {
		if gotCtx != ctx || id != "device" {
			t.Fatal("context or trimmed device ID not forwarded")
		}
		if command != `AT+CRSM=178,28645,1,4,0,,"3F007F10"` {
			t.Fatalf("unexpected command %q", command)
		}
		if adapter.apduMu.TryLock() {
			adapter.apduMu.Unlock()
			t.Fatal("APDU lock not held")
		}
		if e.mu.TryLock() {
			e.mu.Unlock()
			t.Fatal("UICC lock not held")
		}
		uris := []string{"sip:first.example", "sip:second.example"}
		if calls >= len(uris) {
			t.Fatal("unexpected extra command")
		}
		uri := uris[calls]
		calls++
		return modem.Response{Final: "OK", Lines: []string{`+CRSM: 144,0,"` + hex.EncodeToString(append([]byte{0x80, byte(len(uri))}, []byte(uri)...)) + `"`}}, nil
	}
	for _, want := range []string{"sip:first.example", "sip:second.example"} {
		got, err := adapter.ReadSMSCenterPSI(ctx, " device ")
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if calls != 2 {
		t.Fatalf("read calls = %d; PSI must not be cached", calls)
	}
	if !adapter.apduMu.TryLock() {
		t.Fatal("APDU lock leaked")
	}
	adapter.apduMu.Unlock()
	if !e.mu.TryLock() {
		t.Fatal("UICC lock leaked")
	}
	e.mu.Unlock()
}

func TestEC20ReadSMSCenterPSIFailures(t *testing.T) {
	const good = `+CRSM: 144,0,"8003736970"`
	transportErr := errors.New("test transport failed")
	tests := []struct {
		name  string
		lines []string
		final string
		err   error
	}{
		{"transport", nil, "", transportErr},
		{"card_status", []string{`+CRSM: 106,130`}, "OK", nil},
		{"invalid_status", []string{`+CRSM: x,0,"8003736970"`}, "OK", nil},
		{"no_data", []string{`+CRSM: 144,0`}, "OK", nil},
		{"invalid_hex", []string{`+CRSM: 144,0,"XYZ"`}, "OK", nil},
		{"odd_hex", []string{`+CRSM: 144,0,"800"`}, "OK", nil},
		{"malformed_record", []string{`+CRSM: 144,0,"8008736970"`}, "OK", nil},
		{"missing_response", nil, "OK", nil},
		{"duplicate_response", []string{good, good}, "OK", nil},
		{"failed_final", []string{good}, "ERROR", nil},
		{"missing_final", []string{good}, "", nil},
		{"nonzero_success_sw2", []string{`+CRSM: 144,1,"8003736970"`}, "OK", nil},
		{"response_pending", []string{`+CRSM: 159,3,"8003736970"`}, "OK", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &ec20PSIExecutor{run: func(context.Context, string, string) (modem.Response, error) {
				return modem.Response{Lines: tt.lines, Final: tt.final}, tt.err
			}}
			adapter, err := NewEC20Adapter(e, EC20AdapterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := adapter.ReadSMSCenterPSI(context.Background(), "device")
			if err == nil || got != "" {
				t.Fatalf("got %q, %v; want rejection", got, err)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("lost transport error: %v", err)
			}
			if !adapter.apduMu.TryLock() {
				t.Fatal("APDU lock leaked on error")
			}
			adapter.apduMu.Unlock()
			if !e.mu.TryLock() {
				t.Fatal("UICC lock leaked on error")
			}
			e.mu.Unlock()
		})
	}
}

func TestEC20ReadSMSCenterPSIRejectsInvalidRequest(t *testing.T) {
	e := &ec20PSIExecutor{run: func(context.Context, string, string) (modem.Response, error) {
		t.Fatal("invalid request issued AT command")
		return modem.Response{}, nil
	}}
	adapter, err := NewEC20Adapter(e, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ReadSMSCenterPSI(context.Background(), " \t"); err == nil {
		t.Fatal("accepted empty device ID")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.ReadSMSCenterPSI(ctx, "device"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want canceled", err)
	}
}

func TestParseEC20SMSCenterPSIRecord(t *testing.T) {
	const uri = "sip:smsc.example.test"
	short := append([]byte{0x80, byte(len(uri))}, []byte(uri)...)
	tests := []struct {
		name   string
		record []byte
		want   string
	}{
		{"short", short, uri},
		{"padding", append(append([]byte{}, short...), 0xff, 0xff), uri},
		// SIM BER-TLV permits definite long forms; tolerate non-minimal encodings.
		{"long81", append([]byte{0x80, 0x81, byte(len(uri))}, []byte(uri)...), uri},
		{"long82", append([]byte{0x80, 0x82, 0, byte(len(uri))}, []byte(uri)...), uri},
		{"long_value", append([]byte{0x80, 0x81, 128}, bytes.Repeat([]byte{'a'}, 128)...), string(bytes.Repeat([]byte{'a'}, 128))},
		{"empty_record", nil, ""},
		{"padding_only", []byte{0xff, 0xff}, ""},
		{"empty_uri", []byte{0x80, 0}, ""},
		{"missing_length", []byte{0x80}, ""},
		{"truncated_value", []byte{0x80, 4, 's'}, ""},
		{"truncated_long_length", []byte{0x80, 0x82, 0}, ""},
		{"truncated_long_value", []byte{0x80, 0x82, 1, 0, 's'}, ""},
		{"indefinite_length", []byte{0x80, 0x80, 's', 0, 0}, ""},
		{"reserved_length", []byte{0x80, 0xff}, ""},
		{"excessive_length_octets", []byte{0x80, 0x84, 0xff, 0xff, 0xff, 0xff}, ""},
		{"wrong_tag", append([]byte{0x81, byte(len(uri))}, []byte(uri)...), ""},
		{"constructed_tag", append([]byte{0xa0, byte(len(uri))}, []byte(uri)...), ""},
		{"bare_uri", []byte(uri), ""},
		{"duplicate_uri", append(append([]byte{}, short...), short...), ""},
		{"unknown_tlv_after_uri", append(append([]byte{}, short...), 0x81, 0), ""},
		{"data_after_padding", append(append([]byte{}, short...), 0xff, 0x80, 0), ""},
		{"leading_padding", append([]byte{0xff}, short...), ""},
		{"zero_padding", append(append([]byte{}, short...), 0), ""},
		{"all_spaces", []byte{0x80, 2, ' ', ' '}, ""},
		{"nul_in_value", []byte{0x80, 3, 's', 0, 'p'}, ""},
		{"newline_in_value", []byte{0x80, 3, 's', '\n', 'p'}, ""},
		{"padding_in_value", []byte{0x80, 3, 's', 0xff, 'p'}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEC20SMSCenterPSIRecord(tt.record)
			if tt.want == "" {
				if err == nil || got != "" {
					t.Fatalf("got %q, %v; want rejection", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestEC20ReadSMSCenterPSITranscript(t *testing.T) {
	// Synthetic URI; the command and 125-byte / tag 80 layout match the live probe.
	const uri = "sip:smsc.ims.mnc001.mcc001.3gppnetwork.org;lr=1"
	record := append([]byte{0x80, byte(len(uri))}, []byte(uri)...)
	record = append(record, bytes.Repeat([]byte{0xff}, 125-len(record))...)
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{{
		command: `AT+CRSM=178,28645,1,4,0,,"3F007F10"`,
		lines:   []string{`+CRSM: 144,0,"` + hex.EncodeToString(record) + `"`},
	}}}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.ReadSMSCenterPSI(context.Background(), "test-device")
	if err != nil || got != uri {
		t.Fatalf("got %q, %v; want %q", got, err, uri)
	}
	transcript.assertDone()
}
