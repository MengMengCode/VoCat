package vowifi

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
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
			{command: "AT+CFUN=1"},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
			{
				command: "AT+CGACT?",
				lines:   []string{"+CGACT: 1,0", "+CGACT: 2,0"},
			},
			{command: "AT+CGACT=1,1"},
			{
				command: "AT+CGACT?",
				lines:   []string{"+CGACT: 1,1", "+CGACT: 2,0"},
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

func TestEC20AdapterUsesConfiguredRFOffMode(t *testing.T) {
	t.Parallel()
	transcript := &ec20Transcript{
		t: t,
		steps: []ec20TranscriptStep{
			{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
			{command: "AT+CFUN=0"},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 0"}},
		},
	}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{
		RFOffMode: func(context.Context, string) (int, error) {
			return 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.EnterVoWiFiRFOff(
		context.Background(),
		"wifi-410",
	); err != nil {
		t.Fatalf("EnterVoWiFiRFOff: %v", err)
	}
	transcript.assertDone()
}

func TestEC20AdapterAcceptsQualcommOfflineMode(t *testing.T) {
	t.Parallel()
	transcript := &ec20Transcript{
		t: t,
		steps: []ec20TranscriptStep{
			{command: "AT+CFUN?", lines: []string{"+CFUN: 7"}},
			{command: "AT+CFUN?", lines: []string{"+CFUN: 7"}},
		},
	}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{
		RFOffMode: func(context.Context, string) (int, error) {
			return 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.EnterVoWiFiRFOff(
		context.Background(),
		"wifi-410",
	); err != nil {
		t.Fatalf("EnterVoWiFiRFOff: %v", err)
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

func TestEC20AdapterReadSMSCenterPreservesInternationalTON(t *testing.T) {
	t.Parallel()
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{
		{command: "AT+CSCA?", lines: []string{`+CSCA: "447785016005",145`}},
	}}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.ReadSMSCenter(context.Background(), "wifi-410")
	if err != nil {
		t.Fatalf("ReadSMSCenter: %v", err)
	}
	if got != "+447785016005" {
		t.Fatalf("SMSC = %q, want international +447785016005", got)
	}
	transcript.assertDone()
}

func TestEC20AdapterReadSMSCenterKeepsNationalTON(t *testing.T) {
	t.Parallel()
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{
		{command: "AT+CSCA?", lines: []string{`+CSCA: "12345",129`}},
	}}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.ReadSMSCenter(context.Background(), "ec20")
	if err != nil {
		t.Fatalf("ReadSMSCenter: %v", err)
	}
	if got != "12345" {
		t.Fatalf("SMSC = %q, want national 12345", got)
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
		{command: "ATI", lines: []string{"Manufacturer: QUALCOMM", "IMEI: 867530912345678"}},
	}
}

func TestEC20AdapterReadIdentitySupportsNative410ICCID(t *testing.T) {
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{
		{command: "AT+CPIN?", lines: []string{"+CPIN: READY"}},
		{command: "AT+CIMI", lines: []string{"515661234567890"}},
		{command: "AT+CCID", err: errors.New("unsupported"), final: "ERROR"},
		{command: "AT+QCCID", err: errors.New("unsupported"), final: "ERROR"},
		{command: "AT+ICCID", lines: []string{"ICCID: 89441000400316034372"}},
		{command: "ATI", lines: []string{"Manufacturer: QUALCOMM", "IMEI: 864284040123456"}},
		{command: "AT+CRSM=176,28589,0,0,4", lines: []string{`+CRSM: 144,0,"00000002"`}},
	}}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.ReadIdentity(context.Background(), "wwan0")
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if identity.ICCID != "89441000400316034372" || identity.IMEI != "864284040123456" {
		t.Fatalf("identity = %#v", identity)
	}
	transcript.assertDone()
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
