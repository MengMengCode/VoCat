package vowifi

import (
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

func (s *isimFallbackTranscript) LockUICC()   { s.uiccMu.Lock() }
func (s *isimFallbackTranscript) UnlockUICC() { s.uiccMu.Unlock() }
func (s *isimFallbackTranscript) assertLocked(command string) {
	if command != "AT+CCID" && command != "AT+CUAD" && s.uiccMu.TryLock() {
		s.uiccMu.Unlock()
		s.t.Fatalf("UICC transaction not locked: %s", command)
	}
}
func (s *isimFallbackTranscript) ExecuteAT(ctx context.Context, id, command string) (modem.Response, error) {
	s.assertLocked(command)
	return s.ec20Transcript.ExecuteAT(ctx, id, command)
}
func (s *isimFallbackTranscript) ExecuteSensitiveAT(ctx context.Context, id, command string) (modem.Response, error) {
	s.assertLocked(command)
	return s.ec20Transcript.ExecuteSensitiveAT(ctx, id, command)
}

func TestEC20ISIMStrictDiscoveryAndTransport(t *testing.T) {
	const fullISIM = "A0000000871004FFFFFFFF8903020000"
	const fullUSIM = "A0000000871002FFFFFFFF8903020000"
	identity := SIMIdentity{ICCID: "8901000000000000001", IMSI: "310280000000001"}
	for _, test := range []struct {
		name                                                    string
		knownBasic, fallback, selectFail, authFail, syncFailure bool
	}{
		{name: "logical"},
		{name: "CCHO_rejected", fallback: true},
		{name: "known_basic", knownBasic: true, fallback: true},
		{name: "select_failure_keeps_binding", knownBasic: true, fallback: true, selectFail: true},
		{name: "auth_failure_keeps_binding", fallback: true, authFail: true},
		{name: "synchronization_failure", fallback: true, syncFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// CUAD 只列出 USIM，完整 ISIM AID 必须从 EF_DIR 获取。
			steps := []ec20TranscriptStep{
				{command: "AT+CUAD", lines: []string{`+CUAD: "61124F10` + fullUSIM + `"`}},
				{command: `AT+CSIM=16,"00A40004023F0000"`, lines: []string{`+CSIM: 4,"9000"`}},
				{command: `AT+CSIM=16,"00A40004022F0000"`, lines: []string{`+CSIM: 4,"9000"`}},
				{command: `AT+CSIM=10,"00B2010400"`, lines: []string{`+CSIM: 44,"61124F10` + fullUSIM + `9000"`}},
				{command: `AT+CSIM=10,"00B2020400"`, lines: []string{`+CSIM: 44,"61124F10` + fullISIM + `9000"`}},
			}
			authAPDU := strings.ToUpper(hex.EncodeToString(buildUSIMAuthenticateAPDU(AKAChallenge{})))
			response := successfulUSIMResponse()
			if test.syncFailure {
				response = synchronizationFailureUSIMResponse()
			}
			encoded := strings.ToUpper(hex.EncodeToString(response))
			attempts := 2
			if test.selectFail || test.authFail {
				attempts = 1
			}
			for attempt := 0; attempt < attempts; attempt++ {
				steps = append(steps, ec20TranscriptStep{command: "AT+CCID", lines: []string{"+CCID: " + identity.ICCID}})
				if test.fallback {
					if !test.knownBasic && attempt == 0 {
						command := `AT+CCHO="` + fullISIM + `"`
						steps = append(steps, ec20TranscriptStep{command: command, err: &modem.CommandError{Command: command, Final: "ERROR"}, final: "ERROR"})
					}
					sw := "9000"
					if test.selectFail {
						sw = "6A82"
					}
					steps = append(steps, ec20TranscriptStep{command: `AT+CSIM=42,"00A4040410` + fullISIM + `"`, lines: []string{`+CSIM: 4,"` + sw + `"`}})
					if !test.selectFail {
						step := ec20TranscriptStep{command: fmt.Sprintf(`AT+CSIM=%d,"%s"`, len(authAPDU), authAPDU), sensitive: true, lines: []string{fmt.Sprintf(`+CSIM: %d,"%s"`, len(encoded), encoded)}}
						if test.authFail {
							step.err = errors.New("secret authentication payload")
						}
						steps = append(steps, step)
					}
				} else {
					steps = append(steps,
						ec20TranscriptStep{command: `AT+CCHO="` + fullISIM + `"`, lines: []string{"+CCHO: 2"}},
						ec20TranscriptStep{command: fmt.Sprintf(`AT+CGLA=2,%d,"%s"`, len(authAPDU), authAPDU), sensitive: true, lines: []string{fmt.Sprintf(`+CGLA: %d,"%s"`, len(encoded), encoded)}},
						ec20TranscriptStep{command: "AT+CCHC=2"},
					)
				}
			}
			transcript := &isimFallbackTranscript{ec20Transcript: &ec20Transcript{t: t, steps: steps}}
			adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			original := ec20SIMBinding{deviceID: "ec20-1", iccid: identity.ICCID, imsi: identity.IMSI, aid: fullUSIM, application: "USIM", basicChannel: test.knownBasic}
			adapter.bindings[identity.ICCID] = original
			for attempt := 0; attempt < attempts; attempt++ {
				result, err := adapter.AuthenticateWithPreference(context.Background(), identity, AKAChallenge{}, "isim_strict")
				if test.selectFail {
					if err == nil {
						t.Fatal("select failure was not returned")
					}
				} else if test.authFail {
					if !errors.Is(err, ErrEC20AKACommand) || errors.Is(err, ErrEC20ApplicationAbsent) || strings.Contains(err.Error(), "secret") {
						t.Fatalf("auth error = %v", err)
					}
				} else if err != nil || result.SynchronizationFailure != test.syncFailure {
					t.Fatalf("authentication = %#v, %v", result, err)
				}
			}
			binding, _ := adapter.bindingFor(identity)
			if test.selectFail || test.authFail {
				if binding != original {
					t.Fatalf("failed authentication changed binding: %#v", binding)
				}
			} else if binding.aid != fullISIM || binding.application != "ISIM" || binding.basicChannel != test.fallback {
				t.Fatalf("incorrect cached binding: %#v", binding)
			}
			transcript.assertDone()
		})
	}
}
