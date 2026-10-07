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

type isimFallbackTranscript struct {
	*ec20Transcript
	uiccMu sync.Mutex
}

func (transcript *isimFallbackTranscript) LockUICC()   { transcript.uiccMu.Lock() }
func (transcript *isimFallbackTranscript) UnlockUICC() { transcript.uiccMu.Unlock() }

func (transcript *isimFallbackTranscript) assertLocked(command string) {
	if command != "AT+CCID" && command != "AT+CUAD" && transcript.uiccMu.TryLock() {
		transcript.uiccMu.Unlock()
		transcript.t.Fatalf("ISIM discovery/authentication was not protected by the UICC lock: %s", command)
	}
}

func (transcript *isimFallbackTranscript) ExecuteAT(ctx context.Context, id, command string) (modem.Response, error) {
	transcript.assertLocked(command)
	return transcript.ec20Transcript.ExecuteAT(ctx, id, command)
}

func (transcript *isimFallbackTranscript) ExecuteSensitiveAT(ctx context.Context, id, command string) (modem.Response, error) {
	transcript.assertLocked(command)
	return transcript.ec20Transcript.ExecuteSensitiveAT(ctx, id, command)
}

func TestEC20ISIMStrictDiscoversFullAIDAndUsesCompatibleTransport(t *testing.T) {
	const fullISIM = "A0000000871004FFFFFFFF8903020000"
	const fullUSIM = "A0000000871002FFFFFFFF8903020000"
	identity := SIMIdentity{ICCID: "8901000000000000001", IMSI: "310280000000001"}
	openFailure := &modem.CommandError{Command: `AT+CCHO="` + fullISIM + `"`, Final: "ERROR"}
	for _, test := range []struct {
		name       string
		fallback   bool
		knownBasic bool
		response   []byte
	}{
		{name: "full_AID_CCHO_succeeds", response: successfulUSIMResponse()},
		{name: "CCHO_rejected_uses_CSIM", fallback: true, response: successfulUSIMResponse()},
		{name: "CSIM_preserves_synchronization_failure", fallback: true, response: synchronizationFailureUSIMResponse()},
		{name: "preserves_known_CSIM_transport", fallback: true, knownBasic: true, response: successfulUSIMResponse()},
	} {
		t.Run(test.name, func(t *testing.T) {
			var challenge AKAChallenge
			authAPDU := strings.ToUpper(hex.EncodeToString(buildUSIMAuthenticateAPDU(challenge)))
			csimAuth := fmt.Sprintf(`AT+CSIM=%d,"%s"`, len(authAPDU), authAPDU)
			encodedResponse := strings.ToUpper(hex.EncodeToString(test.response))
			selectISIM := `AT+CSIM=42,"00A4040410` + fullISIM + `"`
			steps := []ec20TranscriptStep{
				{command: "AT+CUAD", err: errors.New("CUAD unsupported"), final: "+CME ERROR: 13"},
				{command: `AT+CSIM=16,"00A40004023F0000"`, lines: []string{`+CSIM: 4,"9000"`}},
				{command: `AT+CSIM=16,"00A40004022F0000"`, lines: []string{`+CSIM: 4,"9000"`}},
				{command: `AT+CSIM=10,"00B2010400"`, lines: []string{`+CSIM: 44,"61124F10` + fullUSIM + `9000"`}},
				{command: `AT+CSIM=10,"00B2020400"`, lines: []string{`+CSIM: 44,"61124F10` + fullISIM + `9000"`}},
				{command: "AT+CCID", lines: []string{"+CCID: " + identity.ICCID}},
			}
			if test.fallback {
				if !test.knownBasic {
					steps = append(steps, ec20TranscriptStep{command: `AT+CCHO="` + fullISIM + `"`, err: openFailure, final: "ERROR"})
				}
				steps = append(steps,
					ec20TranscriptStep{command: selectISIM, lines: []string{`+CSIM: 4,"9000"`}},
					ec20TranscriptStep{command: csimAuth, sensitive: true, lines: []string{fmt.Sprintf(`+CSIM: %d,"%s"`, len(encodedResponse), encodedResponse)}},
				)
			} else {
				steps = append(steps,
					ec20TranscriptStep{command: `AT+CCHO="` + fullISIM + `"`, lines: []string{"+CCHO: 2"}},
					ec20TranscriptStep{command: fmt.Sprintf(`AT+CGLA=2,%d,"%s"`, len(authAPDU), authAPDU), sensitive: true, lines: []string{fmt.Sprintf(`+CGLA: %d,"%s"`, len(encodedResponse), encodedResponse)}},
					ec20TranscriptStep{command: "AT+CCHC=2"},
				)
			}
			// 第二次鉴权复用已验证的完整 AID 和访问方式，不重复 CUAD/目录探测。
			steps = append(steps, ec20TranscriptStep{command: "AT+CCID", lines: []string{"+CCID: " + identity.ICCID}})
			if test.fallback {
				steps = append(steps,
					ec20TranscriptStep{command: selectISIM, lines: []string{`+CSIM: 4,"9000"`}},
					ec20TranscriptStep{command: csimAuth, sensitive: true, lines: []string{fmt.Sprintf(`+CSIM: %d,"%s"`, len(encodedResponse), encodedResponse)}},
				)
			} else {
				steps = append(steps,
					ec20TranscriptStep{command: `AT+CCHO="` + fullISIM + `"`, lines: []string{"+CCHO: 2"}},
					ec20TranscriptStep{command: fmt.Sprintf(`AT+CGLA=2,%d,"%s"`, len(authAPDU), authAPDU), sensitive: true, lines: []string{fmt.Sprintf(`+CGLA: %d,"%s"`, len(encodedResponse), encodedResponse)}},
					ec20TranscriptStep{command: "AT+CCHC=2"},
				)
			}
			transcript := &isimFallbackTranscript{ec20Transcript: &ec20Transcript{t: t, steps: steps}}
			adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			adapter.bindings[identity.ICCID] = ec20SIMBinding{deviceID: "ec20-1", iccid: identity.ICCID, imsi: identity.IMSI, aid: fullUSIM, application: "USIM", basicChannel: test.knownBasic}
			for attempt := 0; attempt < 2; attempt++ {
				result, err := adapter.AuthenticateWithPreference(context.Background(), identity, challenge, "isim_strict")
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(test.response, synchronizationFailureUSIMResponse()) {
					if !result.SynchronizationFailure || len(result.AUTS) != 14 {
						t.Fatalf("invalid synchronization failure: %#v", result)
					}
				} else if !bytes.Equal(result.RES, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
					t.Fatalf("invalid authentication response: %#v", result)
				}
			}
			binding, err := adapter.bindingFor(identity)
			if err != nil || binding.aid != fullISIM || binding.application != "ISIM" || binding.basicChannel != test.fallback {
				t.Fatalf("ISIM binding changed unexpectedly: %#v, %v", binding, err)
			}
			transcript.assertDone()
		})
	}
}

func TestEC20ISIMFallbackDoesNotDowngradeOrHideFailures(t *testing.T) {
	const fullISIM = "A0000000871004FFFFFFFF8903020000"
	const secret = "authentication material must not appear in errors"
	identity := SIMIdentity{ICCID: "8901000000000000001", IMSI: "310280000000001"}
	for _, test := range []struct {
		name        string
		application string
		openErr     error
		selectSW    string
		authSW      string
		authErr     error
		wantErr     error
	}{
		{name: "ISIM_absent", application: "ISIM", selectSW: "6A82", wantErr: ErrEC20ApplicationAbsent},
		{name: "transport_timeout", application: "ISIM", openErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		{name: "request_canceled", application: "ISIM", openErr: context.Canceled, wantErr: context.Canceled},
		{name: "USIM_failure_unchanged", application: "USIM", wantErr: ErrEC20ApplicationAbsent},
		{name: "MAC_failure_preserved", application: "ISIM", selectSW: "9000", authSW: "9862", wantErr: ErrEC20AKAMACFailure},
		{name: "sensitive_error_redacted", application: "ISIM", selectSW: "9000", authErr: errors.New(secret), wantErr: ErrEC20AKACommand},
	} {
		t.Run(test.name, func(t *testing.T) {
			aid := fullISIM
			preference := "isim_strict"
			if test.application == "USIM" {
				aid = usimAIDPrefix
				preference = ""
			}
			openErr := test.openErr
			if openErr == nil {
				openErr = &modem.CommandError{Command: `AT+CCHO="` + aid + `"`, Final: "ERROR"}
			}
			steps := []ec20TranscriptStep{
				{command: "AT+CCID", lines: []string{"+CCID: " + identity.ICCID}},
				{command: `AT+CCHO="` + aid + `"`, err: openErr, final: "ERROR"},
			}
			if test.selectSW != "" {
				steps = append(steps, ec20TranscriptStep{command: `AT+CSIM=42,"00A4040410` + fullISIM + `"`, lines: []string{`+CSIM: 4,"` + test.selectSW + `"`}})
			}
			if test.authSW != "" || test.authErr != nil {
				authAPDU := strings.ToUpper(hex.EncodeToString(buildUSIMAuthenticateAPDU(AKAChallenge{})))
				steps = append(steps, ec20TranscriptStep{
					command: fmt.Sprintf(`AT+CSIM=%d,"%s"`, len(authAPDU), authAPDU), sensitive: true,
					lines: []string{`+CSIM: 4,"` + test.authSW + `"`}, err: test.authErr,
				})
			}
			transcript := &isimFallbackTranscript{ec20Transcript: &ec20Transcript{t: t, steps: steps}}
			adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			adapter.bindings[identity.ICCID] = ec20SIMBinding{deviceID: "ec20-1", iccid: identity.ICCID, imsi: identity.IMSI, aid: aid, application: test.application}
			_, err = adapter.AuthenticateWithPreference(context.Background(), identity, AKAChallenge{}, preference)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("authentication error = %v, want %v", err, test.wantErr)
			}
			if test.selectSW == "9000" && errors.Is(err, ErrEC20ApplicationAbsent) {
				t.Fatalf("selected application must not be classified as absent: %v", err)
			}
			if test.authErr != nil && !errors.Is(err, openErr) {
				t.Fatalf("authentication error lost the CCHO cause: %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("authentication error exposed sensitive details: %v", err)
			}
			binding, _ := adapter.bindingFor(identity)
			if binding.application != test.application {
				t.Fatalf("application was silently changed: %#v", binding)
			}
			transcript.assertDone()
		})
	}
}
