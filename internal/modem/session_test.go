package modem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type transportStep struct {
	write  string
	chunks []string
	result func() (int, error)
}

type transcriptTransport struct {
	mu          sync.Mutex
	steps       []transportStep
	chunks      [][]byte
	readReady   chan struct{}
	closed      bool
	unexpected  error
	writeEvents chan string
	drainErrors []error
	ops         []string
	onRead      func()
	onClose     func()
	readErr     error
}

func (transport *transcriptTransport) Write(payload []byte) (int, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	defer transport.wakeReaderLocked()
	if transport.closed {
		return 0, io.ErrClosedPipe
	}
	transport.ops = append(transport.ops, "write:"+string(payload))
	if len(transport.steps) == 0 {
		transport.unexpected = fmt.Errorf("unexpected write %q", payload)
		return 0, transport.unexpected
	}
	step := transport.steps[0]
	transport.steps = transport.steps[1:]
	if string(payload) != step.write {
		transport.unexpected = fmt.Errorf("write %q, want %q", payload, step.write)
		return 0, transport.unexpected
	}
	if transport.writeEvents != nil {
		transport.writeEvents <- string(payload)
	}
	for _, chunk := range step.chunks {
		transport.chunks = append(transport.chunks, []byte(chunk))
	}
	if step.result != nil {
		transport.mu.Unlock()
		count, err := step.result()
		transport.mu.Lock()
		return count, err
	}
	return len(payload), nil
}

func (transport *transcriptTransport) enqueue(chunks ...string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	defer transport.wakeReaderLocked()
	for _, chunk := range chunks {
		transport.chunks = append(transport.chunks, []byte(chunk))
	}
}

func (transport *transcriptTransport) Read(buffer []byte) (int, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return 0, io.EOF
	}
	if len(transport.chunks) > 0 {
		if transport.onRead != nil {
			transport.onRead()
		}
		count := copy(buffer, transport.chunks[0])
		transport.chunks[0] = transport.chunks[0][count:]
		if len(transport.chunks[0]) == 0 {
			transport.chunks = transport.chunks[1:]
		}
		return count, nil
	}
	if transport.readErr != nil {
		return 0, transport.readErr
	}
	if transport.readReady == nil {
		transport.readReady = make(chan struct{})
	}
	ready := transport.readReady
	transport.mu.Unlock()
	<-ready
	transport.mu.Lock()
	return 0, nil
}

func (transport *transcriptTransport) wakeReaderLocked() {
	if transport.readReady != nil {
		close(transport.readReady)
		transport.readReady = nil
	}
}

func (transport *transcriptTransport) Drain() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.ops = append(transport.ops, "drain")
	if len(transport.drainErrors) == 0 {
		return nil
	}
	err := transport.drainErrors[0]
	transport.drainErrors = transport.drainErrors[1:]
	return err
}

func (transport *transcriptTransport) ResetInputBuffer() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.unexpected = errors.New("a running session must not discard input")
	return transport.unexpected
}

func (transport *transcriptTransport) SetReadTimeout(time.Duration) error { return nil }

func (transport *transcriptTransport) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	defer transport.wakeReaderLocked()
	transport.closed = true
	if transport.onClose != nil {
		transport.onClose()
	}
	return nil
}

func TestSessionWritesCommand(t *testing.T) {
	final := []string{"\r\n+CSQ: 24,99\r\nOK\r\n"}
	for _, test := range []struct {
		name        string
		steps       []transportStep
		drainErrors []error
		wantOps     string
	}{
		{name: "drain before write", steps: []transportStep{{write: "AT+CSQ\r", chunks: final}},
			wantOps: "drain|write:AT+CSQ\r"},
		{name: "interrupted drain", steps: []transportStep{{write: "AT+CSQ\r", chunks: final}}, drainErrors: []error{syscall.EINTR},
			wantOps: "drain|drain|write:AT+CSQ\r"},
		{name: "partial and interrupted writes", steps: []transportStep{
			{write: "AT+CSQ\r", result: func() (int, error) { return 2, nil }},
			{write: "+CSQ\r", result: func() (int, error) { return 1, syscall.EINTR }},
			{write: "CSQ\r", chunks: final},
		}, wantOps: "drain|write:AT+CSQ\r|write:+CSQ\r|write:CSQ\r"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &transcriptTransport{steps: test.steps, drainErrors: test.drainErrors}
			session := newTestSession(t, transport)
			response, err := session.Execute(context.Background(), "AT+CSQ")
			if err != nil || !response.OK() || response.Text() != "+CSQ: 24,99" {
				t.Fatalf("Execute = %#v, %v", response, err)
			}
			if got := strings.Join(transport.ops, "|"); got != test.wantOps {
				t.Fatalf("transport operations = %q, want %q", got, test.wantOps)
			}
		})
	}
}

func TestSessionParsesResponses(t *testing.T) {
	for _, test := range []struct {
		name, command, text, final, urc string
		chunks                          []string
	}{
		{name: "interleaved URC and split line", command: "AT+CSQ", text: "+CSQ: 24,99", final: "OK", urc: `+CMTI: "SM",7`,
			chunks: []string{"\r\nAT+CSQ\r", "\n+CMTI: \"SM\",7\r\n+CS", "Q: 24,99\r\nOK\r", "\n"}},
		{name: "expected registration", command: "AT+CEREG?", text: "+CEREG: 0,5", final: "OK", chunks: []string{"\r\n+CEREG: 0,5\r\nOK\r\n"}},
		{name: "asynchronous CUSD", command: `AT+CUSD=1,"*100#",15`, final: "OK", urc: `+CUSD: 0,"004F004B",72`, chunks: []string{"\r\n+CUSD: 0,\"004F004B\",72\r\nOK\r\n"}},
		{name: "command error", command: "AT+CPIN?", final: "+CME ERROR: 10", chunks: []string{"\r\n+CME ERROR: 10\r\n"}},
		{name: "dial final", command: "ATD12345;", final: "NO CARRIER", urc: "NO CARRIER", chunks: []string{"NO CARRIER\r\n"}},
		{name: "answer final", command: "ATA", final: "BUSY", urc: "BUSY", chunks: []string{"BUSY\r\n"}},
		{name: "query during call", command: "AT+CFUN?", text: "+CFUN: 1", final: "OK", urc: "NO CARRIER", chunks: []string{"NO CARRIER\r\n+CFUN: 1\r\nOK\r\n"}},
		{name: "hangup waits for OK", command: "ATH", final: "OK", urc: "NO ANSWER", chunks: []string{"NO ANSWER\r\nOK\r\n"}},
		{name: "ML307 call indication", command: "ATI", text: "ML307A", final: "OK", urc: "CIEV: CALL 1", chunks: []string{"CIEV: CALL 1\r\nML307A\r\nOK\r\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &transcriptTransport{steps: []transportStep{{write: test.command + "\r", chunks: test.chunks}}}
			session := newTestSession(t, transport)
			response, err := session.Execute(context.Background(), test.command)
			if response.Text() != test.text || response.Final != test.final || session.Poisoned() {
				t.Fatalf("Execute = %#v, %v", response, err)
			}
			var commandErr *CommandError
			if test.final == "OK" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &commandErr) || commandErr.Command != test.command || commandErr.Final != test.final {
				t.Fatalf("command error = %v", err)
			}
			if test.urc == "" {
				if len(response.URCs) != 0 {
					t.Fatalf("unexpected URCs: %q", response.URCs)
				}
			} else {
				if len(response.URCs) != 1 || response.URCs[0] != test.urc {
					t.Fatalf("URCs = %q, want %q", response.URCs, test.urc)
				}
				urc, err := session.WaitURC(context.Background(), func(line string) bool { return line == test.urc })
				if err != nil || urc != test.urc {
					t.Fatalf("WaitURC = %q, %v", urc, err)
				}
			}
		})
	}
}

func TestSessionExecutePromptQueuesURCsAndReturnsCMGS(t *testing.T) {
	const pdu = "00010005912143F50008044F60597D"
	transport := &transcriptTransport{steps: []transportStep{
		{write: "AT+CMGS=14\r", chunks: []string{"\r", "\nAT+CMGS=14\r\n+CMTI: \"SM\",7\r", "\n>", " "}},
		{write: pdu},
		{write: "\x1a", chunks: []string{"\r\n" + pdu + "\r\n+CMGS: 42\r\n", "+CMTI: \"SM\",8\r\nOK\r\n"}},
	}}
	session := newTestSession(t, transport)
	response, err := session.ExecutePrompt(context.Background(), "AT+CMGS=14", []byte(pdu))
	if err != nil || response.Text() != "+CMGS: 42" || !response.OK() || len(response.URCs) != 2 {
		t.Fatalf("ExecutePrompt = %#v, %v", response, err)
	}
	for _, wanted := range []string{`+CMTI: "SM",7`, `+CMTI: "SM",8`} {
		line, err := session.WaitURC(context.Background(), func(line string) bool { return line == wanted })
		if err != nil || line != wanted {
			t.Fatalf("WaitURC(%q) = %q, %v", wanted, line, err)
		}
	}
}

func TestSessionExecutePromptSerializesConcurrentCommand(t *testing.T) {
	events := make(chan string, 4)
	transport := &transcriptTransport{writeEvents: events, steps: []transportStep{
		{write: "AT+CMGS=5\r"},
		{write: "HELLO"},
		{write: "\x1a", chunks: []string{"\r\n+CMGS: 9\r\nOK\r\n"}},
		{write: "AT+CSQ\r", chunks: []string{"\r\n+CSQ: 20,99\r\nOK\r\n"}},
	}}
	session := newTestSession(t, transport)
	promptResult, normalResult := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := session.ExecutePrompt(context.Background(), "AT+CMGS=5", []byte("HELLO"))
		promptResult <- err
	}()
	if first := <-events; first != "AT+CMGS=5\r" {
		t.Fatalf("first write = %q", first)
	}
	go func() {
		_, err := session.Execute(context.Background(), "AT+CSQ")
		normalResult <- err
	}()
	transport.enqueue("\r\n> ")
	for _, err := range []error{<-promptResult, <-normalResult} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"HELLO", "\x1a", "AT+CSQ\r"} {
		if got := <-events; got != want {
			t.Fatalf("write = %q, want %q", got, want)
		}
	}
}

func TestSessionPayloadEchoDoesNotReplaceFinal(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		echo, fail    bool
	}{
		{name: "OK without echo", payload: "OK"},
		{name: "OK with echo", payload: "OK", echo: true},
		{name: "multiline echo", payload: "ERROR\nHELLO", echo: true},
		{name: "echo with whitespace", payload: " OK", echo: true},
		{name: "error after echo", payload: "OK", echo: true, fail: true},
		{name: "error without echo", payload: "ERROR", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, prompt := `AT+CMGS="12345"`, "\r\n> "
			final, text := "OK", "+CMGS: 9"
			if test.fail {
				final, text = "ERROR", ""
			}
			reply := "\r\n" + text + "\r\n" + final + "\r\n"
			if test.echo {
				prompt = command + "\r\n> "
				reply = strings.ReplaceAll(test.payload, "\n", "\r\n") + reply
			}
			transport := &transcriptTransport{steps: []transportStep{
				{write: command + "\r", chunks: []string{prompt}},
				{write: test.payload},
				{write: "\x1a", chunks: []string{reply}},
			}}
			response, err := newTestSession(t, transport).ExecutePrompt(context.Background(), command, []byte(test.payload))
			if (err != nil) != test.fail || response.Final != final || response.Text() != text {
				t.Fatalf("prompt response = %#v, %v", response, err)
			}
		})
	}
}

func TestSessionIdleInputStaysUnsolicited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &transcriptTransport{steps: []transportStep{{write: "AT+CEREG?\r", chunks: []string{"+CEREG: 0,1\r\nOK\r\n"}}}}
		session := newTestSession(t, transport)
		transport.enqueue("+CEREG: 5\r\n")
		synctest.Wait()
		response, err := session.Execute(context.Background(), "AT+CEREG?")
		if err != nil || response.Text() != "+CEREG: 0,1" {
			t.Fatalf("idle URC leaked into query: %#v, %v", response, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		line, err := session.WaitURC(ctx, func(line string) bool { return line == "+CEREG: 5" })
		if err != nil || line != "+CEREG: 5" {
			t.Fatalf("idle URC was lost: %q, %v", line, err)
		}
		if _, err := session.WaitURC(ctx, func(string) bool { return false }); !errors.Is(err, context.DeadlineExceeded) || session.Poisoned() {
			t.Fatalf("idle wait = %v, poisoned = %t", err, session.Poisoned())
		}
	})
}

func TestSessionReportsIdleURCWithoutCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &transcriptTransport{}
		var mu sync.Mutex
		var seen []string
		session := newTestSessionWithOptions(t, transport, SessionOptions{OnURC: func(line string) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, line)
		}})
		transport.enqueue("\r\n+CMTI: \"SM\",3\r\n", "+CMTI: \"ME\"")
		synctest.Wait()
		mu.Lock()
		got := append([]string(nil), seen...)
		mu.Unlock()
		if len(got) != 1 || got[0] != `+CMTI: "SM",3` {
			t.Fatalf("idle URCs = %q, want only the complete +CMTI line", got)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if line, err := session.WaitURC(ctx, func(line string) bool { return strings.HasPrefix(line, "+CMTI:") }); err != nil || line != `+CMTI: "SM",3` {
			t.Fatalf("idle URC was not queued: %q, %v", line, err)
		}
	})
}

func TestSessionCancelsAndClosesBlockedWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		releaseWrite := sync.OnceFunc(func() { close(release) })
		transport := &transcriptTransport{onClose: releaseWrite, steps: []transportStep{
			{write: "AT+CSQ\r", result: func() (int, error) { return 2, nil }},
			{write: "+CSQ\r", result: func() (int, error) { close(started); <-release; return 0, io.ErrClosedPipe }},
		}}
		session := newTestSession(t, transport)
		t.Cleanup(releaseWrite)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := session.Execute(ctx, "AT+CSQ"); done <- err }()
		<-started
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled write = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation waited for the blocked write")
		}
		waitStarted := time.Now()
		time.Sleep(2 * time.Second)
		retryCtx, cancelRetry := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := session.Execute(retryCtx, "AT")
		cancelRetry()
		if !errors.Is(err, ErrSessionClosed) || !session.Poisoned() ||
			time.Since(waitStarted) != 6*time.Second || session.ioDone == nil || session.pending == nil {
			t.Fatalf("blocked I/O recovery = %v after %s, poisoned = %t", err, time.Since(waitStarted), session.Poisoned())
		}
		go func() { done <- session.Close() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not interrupt the blocked write")
		}
	})
}

func TestSessionCompletedIOWinsExpiredDeadline(t *testing.T) {
	session := newTestSession(t, &transcriptTransport{})
	session.ioDeadline = time.Now().Add(-time.Second)
	for range 32 {
		session.ioDone = make(chan error, 1)
		session.ioDone <- nil
		if err := session.waitIOLocked(context.Background()); err != nil || session.Poisoned() || session.ioDone != nil {
			t.Fatalf("completed I/O = %v, poisoned = %t", err, session.Poisoned())
		}
	}
}

func TestSessionRejectsUnsafeInput(t *testing.T) {
	session := newTestSession(t, &transcriptTransport{})
	if _, err := session.Execute(context.Background(), "AT\rAT+CFUN=0"); err == nil {
		t.Fatal("expected command delimiter rejection")
	}
	if _, err := session.ExecutePrompt(context.Background(), "AT+CSQ", []byte("payload")); err == nil {
		t.Fatal("expected non-CMGS prompt command rejection")
	}
	if _, err := session.ExecutePrompt(context.Background(), "AT+CMGS=1", []byte{'A', 0x1a}); err == nil {
		t.Fatal("expected Ctrl-Z payload rejection")
	}
}

func TestSessionExpiredTransactionKeepsWireOwnership(t *testing.T) {
	for _, test := range []struct {
		name                             string
		prompt, submitted, payloadCancel bool
		timeout, waitURC, echo           bool
		idle                             time.Duration
	}{
		{name: "query canceled"},
		{name: "query timeout via WaitURC", timeout: true, waitURC: true},
		{name: "late prompt canceled", prompt: true},
		{name: "late prompt after long idle", prompt: true, idle: 3 * time.Minute},
		{name: "late prompt timeout via WaitURC", prompt: true, timeout: true, waitURC: true},
		{name: "payload canceled", prompt: true, payloadCancel: true},
		{name: "payload echo canceled", prompt: true, payloadCancel: true, echo: true},
		{name: "submitted SMS canceled", prompt: true, submitted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				command := "AT+CFUN?"
				if test.prompt {
					command = "AT+CMGS=5"
				}
				first := transportStep{write: command + "\r"}
				if test.submitted || test.payloadCancel {
					first.chunks = []string{"\r\n> "}
				} else if !test.timeout {
					first.result = func() (int, error) { cancel(); return len(first.write), nil }
				}
				body := "HELLO"
				if test.echo {
					body = "ERROR\nHELLO"
					first.chunks = []string{command + "\r\n> "}
				}
				transport := &transcriptTransport{}
				steps := []transportStep{first}
				if !test.prompt && !test.timeout {
					steps[0].result = func() (int, error) { cancel(); return 2, nil }
					steps = append(steps, transportStep{write: first.write[2:]})
				}
				lateReply := []string{"\r\nlate reply\r\nOK\r\n+CFUN: STA", "LE\r\nOK\r\nRING\r\n"}
				if test.submitted || test.payloadCancel {
					payload := transportStep{write: body}
					if test.echo {
						payload.chunks = []string{"\r\n" + strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"}
					}
					if test.payloadCancel {
						payload.result = func() (int, error) { cancel(); return len(body), nil }
					}
					steps = append(steps, payload)
				}
				if test.submitted {
					steps = append(steps, transportStep{write: "\x1a", result: func() (int, error) { cancel(); return 1, nil }})
				} else if test.prompt {
					escape := transportStep{write: "\x1b"}
					if !test.payloadCancel {
						escape.result = func() (int, error) {
							time.AfterFunc(time.Millisecond, func() { transport.enqueue(lateReply...) })
							return 1, nil
						}
					}
					steps = append(steps, escape)
				}
				steps = append(steps, transportStep{write: "AT+CPIN?\r", chunks: []string{"\r\n+CPIN: READY\r\nOK\r\n"}})
				transport.steps = steps
				session := newTestSession(t, transport)
				var response Response
				var err error
				if test.prompt {
					response, err = session.ExecutePrompt(ctx, command, []byte(body))
				} else {
					response, err = session.Execute(ctx, command)
				}
				wantErr := context.Canceled
				if test.timeout {
					wantErr = ErrCommandTimeout
				}
				if !errors.Is(err, wantErr) || response.Final != "" || session.Poisoned() {
					t.Fatalf("expired transaction = %#v, %v; poisoned = %t", response, err, session.Poisoned())
				}
				blockedCtx, cancelBlocked := context.WithTimeout(context.Background(), time.Millisecond)
				_, err = session.Execute(blockedCtx, "AT")
				cancelBlocked()
				if !errors.Is(err, ErrCommandTimeout) {
					t.Fatalf("command before old final = %v", err)
				}
				if test.prompt && !test.submitted && !test.payloadCancel {
					transport.enqueue("\r\n> ")
				} else {
					transport.enqueue(lateReply...)
				}
				time.Sleep(test.idle)
				if test.waitURC {
					urcCtx, cancelURC := context.WithTimeout(context.Background(), time.Second)
					line, err := session.WaitURC(urcCtx, func(line string) bool { return line == "RING" })
					cancelURC()
					if err != nil || line != "RING" {
						t.Fatalf("WaitURC while draining old response = %q, %v", line, err)
					}
				}
				response, err = session.Execute(context.Background(), "AT+CPIN?")
				if err != nil || !response.OK() || response.Text() != "+CPIN: READY" {
					t.Fatalf("query after late final = %#v, %v", response, err)
				}
			})
		})
	}
}

func TestSessionBoundsStaleResponseRecovery(t *testing.T) {
	for _, test := range []struct {
		name, command, reply string
		timeout, recovery    time.Duration
		waitURC              bool
	}{
		{"original deadline", "AT", "", 20 * time.Second, 25 * time.Second, false},
		{"scan with short caller", "AT+COPS=?", "", time.Second, 155 * time.Second, true},
		{"prompt with short caller", "AT+CMGS=5", "", time.Second, 130 * time.Second, false},
		{"unterminated tail", "AT", "OK\r\npartial", time.Second, 5 * time.Second, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
				defer cancel()
				step := transportStep{write: test.command + "\r", chunks: []string{test.reply}}
				if test.reply == "" {
					step.result = func() (int, error) { cancel(); return len(step.write), nil }
				}
				session := newTestSession(t, &transcriptTransport{steps: []transportStep{step}})
				var err error
				if strings.HasPrefix(test.command, "AT+CMGS=") {
					_, err = session.ExecutePrompt(ctx, test.command, []byte("HELLO"))
				} else {
					_, err = session.Execute(ctx, test.command)
				}
				if test.reply == "" && !errors.Is(err, context.Canceled) || test.reply != "" && err != nil {
					t.Fatalf("initial command = %v", err)
				}
				for range 2 {
					retryCtx, cancelRetry := context.WithTimeout(context.Background(), time.Second)
					_, err = session.Execute(retryCtx, "AT")
					cancelRetry()
					if !errors.Is(err, ErrCommandTimeout) || session.Poisoned() {
						t.Fatalf("short retry = %v, poisoned = %t", err, session.Poisoned())
					}
				}
				recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), test.recovery)
				defer cancelRecovery()
				if test.waitURC {
					_, err = session.WaitURC(recoveryCtx, func(string) bool { return false })
				} else {
					_, err = session.Execute(recoveryCtx, "AT")
				}
				if !errors.Is(err, ErrSessionClosed) || !session.Poisoned() || time.Since(started) != test.recovery {
					t.Fatalf("recovery = %v after %s, poisoned = %t; want %s", err, time.Since(started), session.Poisoned(), test.recovery)
				}
			})
		})
	}
}

func TestSessionKeepsBytesReadDuringCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	transport := &transcriptTransport{onRead: func() {
		reads++
		if reads == 2 {
			cancel()
		}
	}, steps: []transportStep{
		{write: "AT+CFUN?\r", chunks: []string{"\r\n+CFUN: 1\r\n", "OK\r\n"}},
		{write: "AT+CPIN?\r", chunks: []string{"\r\n+CPIN: READY\r\nOK\r\n"}},
	}}
	session := newTestSession(t, transport)
	first, err := session.Execute(ctx, "AT+CFUN?")
	// The reader may prefetch both chunks before the caller parses the first.
	firstText := first.Text()
	if !errors.Is(err, context.Canceled) || firstText != "" && firstText != "+CFUN: 1" || first.Final != "" {
		t.Fatalf("canceled response = %#v, %v", first, err)
	}
	next, err := session.Execute(context.Background(), "AT+CPIN?")
	if err != nil || !next.OK() || next.Text() != "+CPIN: READY" || first.Text() != firstText {
		t.Fatalf("next response = %#v, %v; previous response = %#v", next, err, first)
	}
}

func TestSessionConsumesBufferedTailBeforeNextCommand(t *testing.T) {
	for _, tail := range []string{"\x00 \t", "+CFUN: STA"} {
		t.Run(tail, func(t *testing.T) {
			chunks := []string{"+CFUN: 1\r\nOK\r\n" + tail}
			if tail == "+CFUN: STA" {
				chunks = append(chunks, "LE\r\nOK\r\n")
			}
			transport := &transcriptTransport{steps: []transportStep{
				{write: "AT+CFUN?\r", chunks: chunks},
				{write: "AT+CPIN?\r", chunks: []string{"+CPIN: READY\r\nOK\r\n"}},
			}}
			session := newTestSession(t, transport)
			for _, query := range []struct{ command, text string }{
				{"AT+CFUN?", "+CFUN: 1"}, {"AT+CPIN?", "+CPIN: READY"},
			} {
				response, err := session.Execute(context.Background(), query.command)
				if err != nil || !response.OK() || response.Text() != query.text {
					t.Fatalf("response with old tail = %#v, %v; want %q", response, err, query.text)
				}
			}
		})
	}
}

func TestSessionPoisonsTransportFailures(t *testing.T) {
	for _, operation := range []string{"read", "write", "drain", "payload"} {
		t.Run(operation, func(t *testing.T) {
			transport := &transcriptTransport{}
			command := transportStep{write: "AT\r"}
			switch operation {
			case "drain":
				transport.drainErrors = []error{syscall.EIO}
			case "read":
				command.result = func() (int, error) {
					transport.mu.Lock()
					transport.readErr = syscall.EIO
					transport.mu.Unlock()
					return len(command.write), nil
				}
				transport.steps = []transportStep{command}
			case "write":
				command.result = func() (int, error) { return 1, syscall.EIO }
				transport.steps = []transportStep{command}
			case "payload":
				transport.steps = []transportStep{
					{write: "AT+CMGS=5\r", chunks: []string{"\r\n> "}},
					{write: "HELLO", result: func() (int, error) { return 2, syscall.EIO }},
				}
			}
			session := newTestSession(t, transport)
			var err error
			if operation == "payload" {
				_, err = session.ExecutePrompt(context.Background(), "AT+CMGS=5", []byte("HELLO"))
			} else {
				_, err = session.Execute(context.Background(), "AT")
			}
			if !errors.Is(err, syscall.EIO) || !session.Poisoned() {
				t.Fatalf("transport failure = %v; poisoned = %t", err, session.Poisoned())
			}
			if _, err := session.Execute(context.Background(), "AT"); !errors.Is(err, ErrSessionClosed) {
				t.Fatalf("command after transport failure = %v", err)
			}
		})
	}
}

func TestSessionKeepsFinalBeforeEOF(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &transcriptTransport{}
		transport.steps = []transportStep{{write: "AT\r", chunks: []string{"OK\r\n"}, result: func() (int, error) {
			transport.mu.Lock()
			transport.readErr = io.EOF
			transport.mu.Unlock()
			return 3, nil
		}}}
		session := newTestSession(t, transport)
		response, err := session.Execute(context.Background(), "AT")
		synctest.Wait()
		if err != nil || !response.OK() || !session.Poisoned() {
			t.Fatalf("final before EOF = %#v, %v; poisoned = %t", response, err, session.Poisoned())
		}
	})
}

func newTestSession(t *testing.T, transport *transcriptTransport) *Session {
	t.Helper()
	return newTestSessionWithOptions(t, transport, SessionOptions{})
}

func newTestSessionWithOptions(t *testing.T, transport *transcriptTransport, options SessionOptions) *Session {
	t.Helper()
	options.ReadTimeout, options.CommandTimeout = time.Millisecond, time.Second
	session, err := NewSession(transport, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		transport.mu.Lock()
		defer transport.mu.Unlock()
		if transport.unexpected != nil || len(transport.steps) != 0 {
			t.Errorf("unfinished transcript: steps=%d err=%v", len(transport.steps), transport.unexpected)
		}
	})
	return session
}

func TestSessionCloseReleasesATPortLeaseOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttyUSB0")
	release, ok := tryAcquireATPort(path)
	if !ok {
		t.Fatal("initial lease acquisition failed")
	}

	var hookCalls int
	session := newTestSession(t, &transcriptTransport{})
	session.setCloseHook(func() {
		hookCalls++
		release()
	})
	if err := session.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if hookCalls != 1 {
		t.Fatalf("close hook called %d times, want 1", hookCalls)
	}

	if nextRelease, nextOK := tryAcquireATPort(path); !nextOK {
		t.Fatal("lease remained held after Session.Close")
	} else {
		nextRelease()
	}
}
