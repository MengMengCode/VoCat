package modem

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestATAPDUDialectRetriesUnquotedAfterCommandError(t *testing.T) {
	dialect := &ATAPDUDialect{}
	profile := standardAPDUProfile{}
	var commands []string
	responseBytes, err := dialect.Execute(context.Background(), profile, APDUCSIM, 0, []byte{0x00, 0xA4}, func(_ context.Context, command string) (Response, error) {
		commands = append(commands, command)
		if len(commands) == 1 {
			return Response{Final: "ERROR"}, &CommandError{Command: command, Final: "ERROR"}
		}
		if command != `AT+CSIM=4,00A4` {
			return Response{}, errors.New("unexpected APDU command shape")
		}
		return Response{Lines: []string{`+CSIM: 4,9000`}, Final: "OK"}, nil
	})
	if err != nil {
		t.Fatal("APDU execution failed")
	}
	if !reflect.DeepEqual(commands, []string{`AT+CSIM=4,"00A4"`, `AT+CSIM=4,00A4`}) {
		t.Fatal("APDU command sequence did not use one quoted rejection followed by one unquoted retry")
	}
	if !reflect.DeepEqual(responseBytes, []byte{0x90, 0x00}) {
		t.Fatal("APDU response bytes did not include the expected status word")
	}
}

func TestATAPDUDialectUsesZXICPreferredFormWithoutReplayingSIMFailure(t *testing.T) {
	profile := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: "0536"}).APDU
	for _, commandKind := range []APDUCommand{APDUCSIM, APDUCGLA} {
		t.Run(apduCommandName(commandKind), func(t *testing.T) {
			dialect := &ATAPDUDialect{}
			apdu := []byte{0x00, 0x70, 0x00, 0x00, 0x01}
			want, _ := profile.Build(commandKind, 1, apdu, APDUUnquoted)
			calls := 0
			_, err := dialect.Execute(context.Background(), profile, commandKind, 1, apdu, func(_ context.Context, command string) (Response, error) {
				calls++
				if command != want {
					t.Fatal("ZXIC did not start with its verified unquoted form")
				}
				return Response{Final: "+CME ERROR: 13"}, &CommandError{Command: command, Final: "+CME ERROR: 13"}
			})
			var rejected *APDUCommandRejectedError
			if calls != 1 || !errors.As(err, &rejected) || rejected.Final != "+CME ERROR: 13" {
				t.Fatalf("preferred-form SIM failure was replayed or replaced: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestATAPDUDialectLearnsQuotedAfterPreferredFormRejected(t *testing.T) {
	profile := ResolveATProfile(Candidate{VendorID: "19d2", ProductID: "0536"}).APDU
	dialect := &ATAPDUDialect{}
	calls := 0
	for range 2 {
		_, err := dialect.Execute(context.Background(), profile, APDUCSIM, 0, []byte{0x00, 0xA4}, func(_ context.Context, command string) (Response, error) {
			calls++
			if calls == 1 {
				if command != `AT+CSIM=4,00A4` {
					t.Fatal("first exchange ignored the profile preference")
				}
				return Response{Final: "ERROR"}, &CommandError{Command: command, Final: "ERROR"}
			}
			if command != `AT+CSIM=4,"00A4"` {
				t.Fatal("quoted fallback was not retained after success")
			}
			return Response{Final: "OK", Lines: []string{`+CSIM: 4,"9000"`}}, nil
		})
		if err != nil {
			t.Fatalf("quoted fallback failed: %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("preferred form fallback used %d exchanges, want 3", calls)
	}
}

func TestATAPDUDialectDoesNotReplayCMEFailure(t *testing.T) {
	for _, commandKind := range []APDUCommand{APDUCSIM, APDUCGLA} {
		for _, final := range []string{"+CME ERROR: 0", "+CME ERROR: 13", "+CME ERROR: 14", "+CME ERROR: 6004", "+CME ERROR: 9999"} {
			for _, withError := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/commandError=%v", apduCommandName(commandKind), final, withError), func(t *testing.T) {
					dialect := &ATAPDUDialect{}
					calls := 0
					// MANAGE CHANNEL changes card state. A failed card exchange must
					// not be replayed merely to test a different quoting form.
					_, err := dialect.Execute(context.Background(), standardAPDUProfile{}, commandKind, 1, []byte{0x00, 0x70, 0x00, 0x00, 0x01}, func(_ context.Context, command string) (Response, error) {
						calls++
						if calls > 1 {
							return Response{Final: "ERROR"}, &CommandError{Command: command, Final: "ERROR"}
						}
						if withError {
							return Response{Final: final}, &CommandError{Command: command, Final: final, Lines: []string{"private-response"}}
						}
						return Response{Final: final}, nil
					})
					var rejected *APDUCommandRejectedError
					if !errors.As(err, &rejected) || rejected.Final != final {
						t.Fatalf("first card failure was replaced: %v", err)
					}
					if calls != 1 || dialect.learned[0] || dialect.learned[1] {
						t.Fatalf("card failure replayed or learned a quoting form: calls=%d learned=%v", calls, dialect.learned)
					}
					if strings.Contains(err.Error(), "0070000001") || strings.Contains(err.Error(), "private-response") {
						t.Fatal("card failure exposed command or response data")
					}
				})
			}
		}
	}
}

func TestATAPDUDialectDoesNotReplayErrorWithAPDUResponse(t *testing.T) {
	for _, test := range []struct {
		name          string
		responseLines []string
		errorLines    []string
	}{
		{name: "response data", responseLines: []string{`+CSIM: 6,"019000"`}},
		{name: "error data", errorLines: []string{`+CGLA: 4,"9000"`}},
		{name: "malformed response", responseLines: []string{`+CSIM: invalid-private-response`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialect := &ATAPDUDialect{}
			calls := 0
			_, err := dialect.Execute(context.Background(), standardAPDUProfile{}, APDUCSIM, 0, []byte{0x00, 0x70, 0x00, 0x00, 0x01}, func(_ context.Context, command string) (Response, error) {
				calls++
				return Response{Final: "ERROR", Lines: test.responseLines}, &CommandError{Command: command, Final: "ERROR", Lines: test.errorLines}
			})
			if !errors.Is(err, ErrAPDUCommandRejected) || calls != 1 {
				t.Fatalf("ERROR carrying an APDU response must not replay: calls=%d err=%v", calls, err)
			}
			if strings.Contains(err.Error(), "private-response") || strings.Contains(err.Error(), "0070000001") {
				t.Fatal("uncertain APDU response exposed command or response data")
			}
		})
	}
}

func TestATAPDUDialectLearnsCSIMAndCGLAIndependently(t *testing.T) {
	dialect := &ATAPDUDialect{}
	profile := standardAPDUProfile{}
	var commands []string
	responses := []struct {
		command string
		final   string
		line    string
	}{
		{command: `AT+CSIM=4,"00A4"`, final: "ERROR"},
		{command: `AT+CSIM=4,00A4`, final: "OK", line: `+CSIM: 4,9000`},
		{command: `AT+CGLA=2,4,"00A4"`, final: "OK", line: `+CGLA: 4,"9000"`},
		{command: `AT+CSIM=4,00A4`, final: "OK", line: `+CSIM: 4,"9000"`},
		{command: `AT+CGLA=2,4,"00A4"`, final: "OK", line: `+CGLA: 4,9000`},
	}
	responseIndex := 0
	for _, commandKind := range []APDUCommand{APDUCSIM, APDUCGLA, APDUCSIM, APDUCGLA} {
		_, err := dialect.Execute(context.Background(), profile, commandKind, 2, []byte{0x00, 0xA4}, func(_ context.Context, command string) (Response, error) {
			current := responses[responseIndex]
			responseIndex++
			commands = append(commands, command)
			if command != current.command {
				return Response{}, errors.New("unexpected command shape")
			}
			response := Response{Final: current.final}
			if current.line != "" {
				response.Lines = []string{current.line}
			}
			if current.final == "ERROR" {
				return response, &CommandError{Command: command, Final: current.final}
			}
			return response, nil
		})
		if err != nil {
			t.Fatalf("Execute(%s) failed", apduCommandName(commandKind))
		}
	}
	if responseIndex != len(responses) {
		t.Fatalf("consumed %d response steps, want %d", responseIndex, len(responses))
	}
	want := []string{
		`AT+CSIM=4,"00A4"`, `AT+CSIM=4,00A4`,
		`AT+CGLA=2,4,"00A4"`,
		`AT+CSIM=4,00A4`, `AT+CGLA=2,4,"00A4"`,
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatal("CSIM and CGLA did not retain independent APDU quoting forms")
	}
}

func TestATAPDUDialectDoesNotRetryAfterTimeout(t *testing.T) {
	dialect := &ATAPDUDialect{}
	commands := 0
	_, err := dialect.Execute(context.Background(), standardAPDUProfile{}, APDUCSIM, 0, []byte{0x00}, func(context.Context, string) (Response, error) {
		commands++
		return Response{}, errors.Join(context.DeadlineExceeded, &CommandError{Command: `AT+CSIM=2,"00"`, Final: "ERROR"})
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("APDU timeout did not preserve the deadline error class")
	}
	if commands != 1 {
		t.Fatalf("execute called %d times after timeout, want 1", commands)
	}
}

func TestATAPDUDialectDoesNotRetryAfterCancellation(t *testing.T) {
	dialect := &ATAPDUDialect{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands := 0
	_, err := dialect.Execute(ctx, standardAPDUProfile{}, APDUCSIM, 0, []byte{0x00}, func(_ context.Context, command string) (Response, error) {
		commands++
		cancel()
		return Response{Final: "ERROR"}, &CommandError{Command: command, Final: "ERROR"}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("APDU cancellation did not preserve the cancellation error class")
	}
	if commands != 1 {
		t.Fatalf("execute called %d times after cancellation, want 1", commands)
	}
}

func TestATAPDUDialectDoesNotRetryAfterIOOrParseError(t *testing.T) {
	t.Run("I/O error", func(t *testing.T) {
		dialect := &ATAPDUDialect{}
		commands := 0
		secret := "transport failure with private APDU CAFEBABE"
		_, err := dialect.Execute(context.Background(), standardAPDUProfile{}, APDUCSIM, 0, []byte{0x00}, func(context.Context, string) (Response, error) {
			commands++
			return Response{}, errors.New(secret)
		})
		if !errors.Is(err, ErrAPDUExecutionFailed) || strings.Contains(err.Error(), secret) {
			t.Fatal("APDU transport failure was not sanitized or preserved its error class")
		}
		if commands != 1 {
			t.Fatalf("execute called %d times after I/O error, want 1", commands)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		dialect := &ATAPDUDialect{}
		commands := 0
		const secret = "CAFEBABE"
		_, err := dialect.Execute(context.Background(), standardAPDUProfile{}, APDUCSIM, 0, []byte{0x00}, func(context.Context, string) (Response, error) {
			commands++
			return Response{Final: "OK", Lines: []string{`+CSIM: 8,"` + secret + `ZZ9000"`}}, nil
		})
		if !errors.Is(err, ErrAPDUResponseInvalid) || strings.Contains(err.Error(), secret) {
			t.Fatal("APDU parse failure was not sanitized")
		}
		if commands != 1 {
			t.Fatalf("execute called %d times after parse error, want 1", commands)
		}
	})
}

func TestATAPDUDialectDoesNotRetryLearnedShape(t *testing.T) {
	dialect := &ATAPDUDialect{}
	profile := standardAPDUProfile{}
	command := `AT+CSIM=4,"00A4"`
	if _, err := dialect.Execute(context.Background(), profile, APDUCSIM, 0, []byte{0x00, 0xA4}, func(context.Context, string) (Response, error) {
		return Response{Final: "OK", Lines: []string{`+CSIM: 4,9000`}}, nil
	}); err != nil {
		t.Fatal("initial APDU command failed")
	}
	var commands []string
	_, err := dialect.Execute(context.Background(), profile, APDUCSIM, 0, []byte{0x00, 0xA4}, func(_ context.Context, actual string) (Response, error) {
		commands = append(commands, actual)
		return Response{Final: "+CME ERROR: 13"}, &CommandError{Command: actual, Final: "+CME ERROR: 13"}
	})
	var rejected *APDUCommandRejectedError
	if !errors.As(err, &rejected) || rejected.Command != APDUCSIM || rejected.Final != "+CME ERROR: 13" {
		t.Fatal("learned APDU shape did not return a sanitized command rejection")
	}
	if !reflect.DeepEqual(commands, []string{command}) {
		t.Fatal("learned APDU shape was retried")
	}
}

func TestATAPDUDialectConcurrentCallsAreSafe(t *testing.T) {
	const calls = 16
	dialect := &ATAPDUDialect{}
	var wait sync.WaitGroup
	var commandMu sync.Mutex
	commands := make([]string, 0, calls)
	errorsFound := make(chan error, calls)
	start := make(chan struct{})
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := dialect.Execute(context.Background(), standardAPDUProfile{}, APDUCSIM, 0, []byte{0x00, 0xA4}, func(_ context.Context, command string) (Response, error) {
				commandMu.Lock()
				commands = append(commands, command)
				commandMu.Unlock()
				return Response{Final: "OK", Lines: []string{`+CSIM: 4,9000`}}, nil
			})
			if err != nil {
				errorsFound <- err
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Error("concurrent APDU execution failed")
		}
	}
	if len(commands) != calls {
		t.Fatalf("execute callback count = %d, want %d", len(commands), calls)
	}
	for _, command := range commands {
		if command != `AT+CSIM=4,"00A4"` {
			t.Fatal("concurrent APDU calls did not use the learned quoting shape")
		}
	}
}

func TestParseAPDUResponseQuotedAndUnquoted(t *testing.T) {
	profile := standardAPDUProfile{}
	tests := []struct {
		name     string
		command  APDUCommand
		response Response
		want     []byte
	}{
		{
			name:     "quoted length in hex characters",
			command:  APDUCSIM,
			response: Response{Lines: []string{`+CSIM: 12,"DEADBEEF9000"`}},
			want:     []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x90, 0x00},
		},
		{
			name:     "unquoted length in bytes",
			command:  APDUCGLA,
			response: Response{Lines: []string{`+CGLA: 6,DEADBEEF9000`}},
			want:     []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x90, 0x00},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := profile.Parse(test.command, test.response)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatal("APDU response parser returned an error or unexpected response bytes")
			}
		})
	}
}

func TestParseCCHOChannelPrefixedAndBare(t *testing.T) {
	tests := []struct {
		line string
		want int
		ok   bool
	}{
		{line: "+CCHO: 1", want: 1, ok: true},
		{line: "19", want: 19, ok: true},
		{line: "0", ok: false},
		{line: "20", ok: false},
		{line: "channel 1", ok: false},
		{line: "+CCHO: 1,2", ok: false},
	}
	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			got, err := ParseCCHOChannel(Response{Lines: []string{test.line}})
			if test.ok && (err != nil || got != test.want) {
				t.Fatalf("ParseCCHOChannel = %d, %v, want %d", got, err, test.want)
			}
			if !test.ok && err == nil {
				t.Fatalf("ParseCCHOChannel(%q) = %d, want error", test.line, got)
			}
		})
	}
}
