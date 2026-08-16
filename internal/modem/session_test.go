package modem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"
)

type transportStep struct {
	write  string
	chunks []string
}

type transcriptTransport struct {
	mu            sync.Mutex
	steps         []transportStep
	chunks        [][]byte
	pendingWrite  string
	pendingChunks []string
	readTimeout   time.Duration
	resetCount    int
	closed        bool
	unexpected    error
	writePartial  bool
	writeEvents   chan string
	drainErrors   []error
	drainCount    int
}

func (transport *transcriptTransport) Write(payload []byte) (int, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return 0, io.ErrClosedPipe
	}
	if transport.pendingWrite != "" {
		if string(payload) != transport.pendingWrite {
			transport.unexpected = fmt.Errorf(
				"partial write %q, want %q",
				payload,
				transport.pendingWrite,
			)
			return 0, transport.unexpected
		}
		for _, chunk := range transport.pendingChunks {
			transport.chunks = append(transport.chunks, []byte(chunk))
		}
		transport.pendingWrite = ""
		transport.pendingChunks = nil
		return len(payload), nil
	}
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
		select {
		case transport.writeEvents <- string(payload):
		default:
		}
	}
	if transport.writePartial && len(payload) > 1 {
		transport.writePartial = false
		count := len(payload) / 2
		transport.pendingWrite = step.write[count:]
		transport.pendingChunks = append([]string(nil), step.chunks...)
		return count, nil
	}
	for _, chunk := range step.chunks {
		transport.chunks = append(transport.chunks, []byte(chunk))
	}
	return len(payload), nil
}

func (transport *transcriptTransport) enqueue(chunks ...string) {
	transport.mu.Lock()
	for _, chunk := range chunks {
		transport.chunks = append(transport.chunks, []byte(chunk))
	}
	transport.mu.Unlock()
}

func (transport *transcriptTransport) Read(buffer []byte) (int, error) {
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return 0, io.EOF
	}
	if len(transport.chunks) > 0 {
		chunk := transport.chunks[0]
		count := copy(buffer, chunk)
		if count == len(chunk) {
			transport.chunks = transport.chunks[1:]
		} else {
			transport.chunks[0] = chunk[count:]
		}
		transport.mu.Unlock()
		return count, nil
	}
	timeout := transport.readTimeout
	transport.mu.Unlock()
	if timeout <= 0 || timeout > 2*time.Millisecond {
		timeout = time.Millisecond
	}
	time.Sleep(timeout)
	return 0, nil
}

func (transport *transcriptTransport) Drain() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.drainCount++
	if len(transport.drainErrors) == 0 {
		return nil
	}
	err := transport.drainErrors[0]
	transport.drainErrors = transport.drainErrors[1:]
	return err
}

func (transport *transcriptTransport) ResetInputBuffer() error {
	transport.mu.Lock()
	transport.chunks = nil
	transport.resetCount++
	transport.mu.Unlock()
	return nil
}

func (transport *transcriptTransport) SetReadTimeout(timeout time.Duration) error {
	transport.mu.Lock()
	transport.readTimeout = timeout
	transport.mu.Unlock()
	return nil
}

func (transport *transcriptTransport) Close() error {
	transport.mu.Lock()
	transport.closed = true
	transport.mu.Unlock()
	return nil
}

func TestSessionRetriesInterruptedDrain(t *testing.T) {
	transport := &transcriptTransport{
		steps: []transportStep{{
			write:  "AT+CSQ\r",
			chunks: []string{"\r\nAT+CSQ\r\n+CSQ: 24,99\r\nOK\r\n"},
		}},
		drainErrors: []error{syscall.EINTR},
	}
	session, err := NewSession(transport, SessionOptions{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	response, err := session.Execute(context.Background(), "AT+CSQ")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if response.Final != "OK" {
		t.Fatalf("response final = %q", response.Final)
	}
	if transport.drainCount != 2 {
		t.Fatalf("Drain() calls = %d, want 2", transport.drainCount)
	}
}

func TestSessionSeparatesInterleavedURCs(t *testing.T) {
	transport := &transcriptTransport{steps: []transportStep{{
		write: "AT+CSQ\r",
		chunks: []string{
			"\r\nAT+CSQ\r\n+CMTI: \"SM\",7\r\n",
			"+CSQ: 24,99\r\nOK\r\n",
		},
	}}}
	session := newTestSession(t, transport)
	response, err := session.Execute(context.Background(), "AT+CSQ")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := response.Text(); got != "+CSQ: 24,99" {
		t.Fatalf("response = %q", got)
	}
	if len(response.URCs) != 1 || response.URCs[0] != `+CMTI: "SM",7` {
		t.Fatalf("URCs = %#v", response.URCs)
	}
	urc, err := session.WaitURC(context.Background(), func(line string) bool {
		return line == `+CMTI: "SM",7`
	})
	if err != nil || urc == "" {
		t.Fatalf("WaitURC = %q, %v", urc, err)
	}
}

func TestSessionKeepsExpectedRegistrationLineInResponse(t *testing.T) {
	transport := &transcriptTransport{steps: []transportStep{{
		write:  "AT+CEREG?\r",
		chunks: []string{"\r\n+CEREG: 0,5\r\nOK\r\n"},
	}}}
	session := newTestSession(t, transport)
	response, err := session.Execute(context.Background(), "AT+CEREG?")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if response.Text() != "+CEREG: 0,5" || len(response.URCs) != 0 {
		t.Fatalf("response = %#v", response)
	}
}

func TestSessionQueuesCUSDThatArrivesBeforeOK(t *testing.T) {
	transport := &transcriptTransport{steps: []transportStep{{
		write:  "AT+CUSD=1,\"*100#\",15\r",
		chunks: []string{"\r\n+CUSD: 0,\"004F004B\",72\r\nOK\r\n"},
	}}}
	session := newTestSession(t, transport)
	response, err := session.Execute(context.Background(), `AT+CUSD=1,"*100#",15`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(response.URCs) != 1 {
		t.Fatalf("URCs = %#v", response.URCs)
	}
	urc, err := session.WaitURC(context.Background(), func(line string) bool {
		return len(line) >= 6 && line[:6] == "+CUSD:"
	})
	if err != nil || urc != `+CUSD: 0,"004F004B",72` {
		t.Fatalf("WaitURC = %q, %v", urc, err)
	}
}

func TestSessionReturnsTypedCommandError(t *testing.T) {
	transport := &transcriptTransport{steps: []transportStep{{
		write:  "AT+CPIN?\r",
		chunks: []string{"\r\n+CME ERROR: 10\r\n"},
	}}}
	session := newTestSession(t, transport)
	_, err := session.Execute(context.Background(), "AT+CPIN?")
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || commandErr.Final != "+CME ERROR: 10" {
		t.Fatalf("error = %#v", err)
	}
}

func TestSessionTimeoutResetsInputAndRejectsCommandInjection(t *testing.T) {
	transport := &transcriptTransport{steps: []transportStep{{write: "AT\r"}}}
	session, err := NewSession(transport, SessionOptions{
		ReadTimeout:    time.Millisecond,
		CommandTimeout: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Execute(context.Background(), "AT")
	if !errors.Is(err, ErrCommandTimeout) {
		t.Fatalf("error = %v", err)
	}
	if transport.resetCount != 1 {
		t.Fatalf("reset count = %d", transport.resetCount)
	}
	if _, err := session.Execute(context.Background(), "AT\rAT+CFUN=0"); err == nil {
		t.Fatal("expected command delimiter rejection")
	}
}

func TestSessionHandlesPartialWrites(t *testing.T) {
	transport := &transcriptTransport{
		writePartial: true,
		steps: []transportStep{{
			write:  "AT+CSQ\r",
			chunks: []string{"\r\n+CSQ: 1,99\r\nOK\r\n"},
		}},
	}
	session := newTestSession(t, transport)
	response, err := session.Execute(context.Background(), "AT+CSQ")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if response.Text() != "+CSQ: 1,99" {
		t.Fatalf("response = %#v", response)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.pendingWrite != "" || len(transport.steps) != 0 ||
		transport.unexpected != nil {
		t.Fatalf(
			"unfinished transcript: pending=%q steps=%d err=%v",
			transport.pendingWrite,
			len(transport.steps),
			transport.unexpected,
		)
	}
}

func TestSessionExecutePromptQueuesURCsAndReturnsCMGS(t *testing.T) {
	const pdu = "00010005912143F50008044F60597D"
	transport := &transcriptTransport{steps: []transportStep{
		{
			write: "AT+CMGS=14\r",
			chunks: []string{
				"\r\nAT+CMGS=14\r\n+CMTI: \"SM\",7\r\n> ",
			},
		},
		{write: pdu},
		{
			write: string([]byte{0x1a}),
			chunks: []string{
				"\r\n" + pdu + "\r\n+CMGS: 42\r\n",
				"+CMTI: \"SM\",8\r\nOK\r\n",
			},
		},
	}}
	session := newTestSession(t, transport)
	response, err := session.ExecutePrompt(
		context.Background(),
		"AT+CMGS=14",
		[]byte(pdu),
	)
	if err != nil {
		t.Fatalf("ExecutePrompt: %v", err)
	}
	if response.Text() != "+CMGS: 42" || !response.OK() {
		t.Fatalf("response = %#v", response)
	}
	if len(response.URCs) != 2 {
		t.Fatalf("URCs = %#v", response.URCs)
	}
	for _, wanted := range []string{`+CMTI: "SM",7`, `+CMTI: "SM",8`} {
		line, waitErr := session.WaitURC(
			context.Background(),
			func(line string) bool { return line == wanted },
		)
		if waitErr != nil || line != wanted {
			t.Fatalf("WaitURC(%q) = %q, %v", wanted, line, waitErr)
		}
	}
}

func TestSessionExecutePromptTimeoutDoesNotWritePayload(t *testing.T) {
	transport := &transcriptTransport{
		steps: []transportStep{{write: "AT+CMGS=5\r"}},
	}
	session, err := NewSession(transport, SessionOptions{
		ReadTimeout:    time.Millisecond,
		CommandTimeout: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.ExecutePrompt(
		context.Background(),
		"AT+CMGS=5",
		[]byte("001122"),
	)
	if !errors.Is(err, ErrCommandTimeout) {
		t.Fatalf("error = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.resetCount != 1 || len(transport.steps) != 0 ||
		transport.unexpected != nil {
		t.Fatalf(
			"transport = reset %d, steps %d, error %v",
			transport.resetCount,
			len(transport.steps),
			transport.unexpected,
		)
	}
}

func TestSessionExecutePromptSerializesConcurrentCommand(t *testing.T) {
	events := make(chan string, 4)
	transport := &transcriptTransport{
		writeEvents: events,
		steps: []transportStep{
			{write: "AT+CMGS=\"12345\"\r"},
			{write: "HELLO"},
			{
				write:  string([]byte{0x1a}),
				chunks: []string{"\r\n+CMGS: 9\r\nOK\r\n"},
			},
			{
				write:  "AT+CSQ\r",
				chunks: []string{"\r\n+CSQ: 20,99\r\nOK\r\n"},
			},
		},
	}
	session := newTestSession(t, transport)
	promptResult := make(chan error, 1)
	go func() {
		_, err := session.ExecutePrompt(
			context.Background(),
			`AT+CMGS="12345"`,
			[]byte("HELLO"),
		)
		promptResult <- err
	}()
	if first := <-events; first != "AT+CMGS=\"12345\"\r" {
		t.Fatalf("first write = %q", first)
	}

	normalStarted := make(chan struct{})
	normalResult := make(chan error, 1)
	go func() {
		close(normalStarted)
		_, err := session.Execute(context.Background(), "AT+CSQ")
		normalResult <- err
	}()
	<-normalStarted
	transport.enqueue("\r\n> ")

	if err := <-promptResult; err != nil {
		t.Fatalf("ExecutePrompt: %v", err)
	}
	if err := <-normalResult; err != nil {
		t.Fatalf("concurrent Execute: %v", err)
	}
	writes := []string{<-events, <-events, <-events}
	want := []string{"HELLO", string([]byte{0x1a}), "AT+CSQ\r"}
	for index := range want {
		if writes[index] != want[index] {
			t.Fatalf("write[%d] = %q, want %q", index, writes[index], want[index])
		}
	}
}

func TestSessionExecutePromptRejectsUnsafeInput(t *testing.T) {
	transport := &transcriptTransport{}
	session := newTestSession(t, transport)
	if _, err := session.ExecutePrompt(
		context.Background(),
		"AT+CSQ",
		[]byte("payload"),
	); err == nil {
		t.Fatal("expected non-CMGS prompt command rejection")
	}
	if _, err := session.ExecutePrompt(
		context.Background(),
		"AT+CMGS=1",
		[]byte{'A', 0x1a},
	); err == nil {
		t.Fatal("expected Ctrl-Z payload rejection")
	}
}

func newTestSession(t *testing.T, transport Transport) *Session {
	t.Helper()
	session, err := NewSession(transport, SessionOptions{
		ReadTimeout:    time.Millisecond,
		CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}
