package modem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Transport interface {
	io.ReadWriteCloser
	Drain() error
	ResetInputBuffer() error
	SetReadTimeout(time.Duration) error
}

type SessionOptions struct {
	ReadTimeout    time.Duration
	CommandTimeout time.Duration
	MaxURCs        int
}

func (options SessionOptions) withDefaults() SessionOptions {
	if options.ReadTimeout <= 0 {
		options.ReadTimeout = 100 * time.Millisecond
	}
	if options.CommandTimeout <= 0 {
		options.CommandTimeout = 3 * time.Second
	}
	if options.MaxURCs <= 0 {
		options.MaxURCs = 256
	}
	return options
}

// Session serializes commands for one physical AT port. Reading is intentionally
// performed under the same mutex as writing: this prevents two callers from
// consuming each other's responses while still allowing interleaved URCs to be
// separated and queued.
type Session struct {
	mu        sync.Mutex
	transport Transport
	options   SessionOptions
	readBuf   []byte
	urcs      []string
	closed    bool
	poisoned  bool
}

// PoisonedClient is implemented by Session. A poisoned session has hit a
// transport-fatal error (a failed write/drain/read or a closed serial line);
// the underlying fd is wedged and every subsequent command reuses the corpse.
// AT-level failures (CommandError) do not poison. A command deadline that has
// to close a blocked transport does poison, because the fd was wedged and must
// be reopened before the next operation.
type PoisonedClient interface {
	Poisoned() bool
}

func NewSession(transport Transport, options SessionOptions) (*Session, error) {
	if transport == nil {
		return nil, errors.New("modem: transport is required")
	}
	options = options.withDefaults()
	if err := transport.SetReadTimeout(options.ReadTimeout); err != nil {
		return nil, fmt.Errorf("set serial read timeout: %w", err)
	}
	return &Session{
		transport: transport,
		options:   options,
	}, nil
}

// Poisoned reports whether this session has hit a transport-fatal error and
// should be discarded rather than reused. It is safe to call concurrently.
func (session *Session) Poisoned() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.poisoned || session.closed
}

func (session *Session) Execute(ctx context.Context, command string) (Response, error) {
	command, err := normalizeATCommand(command)
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := session.commandContext(ctx)
	defer cancel()

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return Response{}, ErrSessionClosed
	}
	return session.executeLocked(ctx, command)
}

// ExecutePrompt executes the controlled two-phase AT+CMGS transaction. It does
// not release the session mutex between the command, the '>' prompt, the
// payload terminator, and the final result.
func (session *Session) ExecutePrompt(
	ctx context.Context,
	command string,
	payload []byte,
) (Response, error) {
	command, err := normalizeATCommand(command)
	if err != nil {
		return Response{}, err
	}
	if !strings.HasPrefix(strings.ToUpper(command), "AT+CMGS=") {
		return Response{}, errors.New("modem: prompt command must be AT+CMGS")
	}
	if len(payload) > 8192 {
		return Response{}, errors.New("modem: prompt payload exceeds 8192 bytes")
	}
	if bytes.IndexByte(payload, 0x1a) >= 0 || bytes.IndexByte(payload, 0x1b) >= 0 {
		return Response{}, errors.New("modem: prompt payload contains a terminator")
	}
	ctx, cancel := session.commandContext(ctx)
	defer cancel()

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return Response{}, ErrSessionClosed
	}
	return session.executePromptLocked(ctx, command, payload)
}

func (session *Session) commandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok || session.options.CommandTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, session.options.CommandTimeout)
}

func (session *Session) executeLocked(ctx context.Context, command string) (Response, error) {
	started := time.Now()
	response := Response{Command: command}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	// Drain the transport before writing the command. Serial transports wait
	// for any pending output here (a no-op after a synchronous command), while
	// WWAN transports discard bytes left over from a previous command that
	// timed out; without this, a late reply (e.g. a slow CGSN response) would
	// be mis-parsed as this command's output.
	if err := drainTransport(ctx, session.transport); err != nil {
		session.poisonLocked()
		return response, fmt.Errorf("drain %s: %w", command, err)
	}
	if err := writeAll(session.transport, []byte(command+"\r")); err != nil {
		session.poisonLocked()
		return response, fmt.Errorf("write %s: %w", command, err)
	}
	return session.readFinalLocked(ctx, started, command, "", response)
}

// poisonLocked marks the session unusable after a transport-fatal error. Held
// under session.mu by the caller; idempotent.
func (session *Session) poisonLocked() {
	session.poisoned = true
}

func (session *Session) executePromptLocked(
	ctx context.Context,
	command string,
	payload []byte,
) (Response, error) {
	started := time.Now()
	response := Response{Command: command}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if err := writeAll(session.transport, []byte(command+"\r")); err != nil {
		session.poisonLocked()
		return response, fmt.Errorf("write %s: %w", command, err)
	}
	if err := drainTransport(ctx, session.transport); err != nil {
		session.poisonLocked()
		return response, fmt.Errorf("drain %s: %w", command, err)
	}
	if err := session.waitPromptLocked(ctx, command, &response); err != nil {
		response.Duration = time.Since(started)
		return response, session.normalizeReadError(command, err)
	}
	if err := ctx.Err(); err != nil {
		session.abortPromptLocked()
		response.Duration = time.Since(started)
		return response, err
	}
	if err := writeAll(session.transport, payload); err != nil {
		session.poisonLocked()
		session.abortPromptLocked()
		response.Duration = time.Since(started)
		return response, fmt.Errorf("write %s payload: %w", command, err)
	}
	if err := writeAll(session.transport, []byte{0x1a}); err != nil {
		session.poisonLocked()
		session.abortPromptLocked()
		response.Duration = time.Since(started)
		return response, fmt.Errorf("terminate %s payload: %w", command, err)
	}
	if err := drainTransport(ctx, session.transport); err != nil {
		session.poisonLocked()
		response.Duration = time.Since(started)
		return response, fmt.Errorf("drain %s payload: %w", command, err)
	}
	return session.readFinalLocked(ctx, started, command, string(payload), response)
}

// drainTransport retries tcdrain/TCSBRK when the kernel interrupts it with a
// signal. go.bug.st/serial already retries EINTR for Read, but its Linux
// Drain implementation currently returns the transient error directly.
func drainTransport(ctx context.Context, transport Transport) error {
	for {
		err := transport.Drain()
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (session *Session) readFinalLocked(
	ctx context.Context,
	started time.Time,
	command string,
	payloadEcho string,
	response Response,
) (Response, error) {
	expectedPrefix := expectedResponsePrefix(command)
	for {
		line, err := session.readLineLocked(ctx)
		if err != nil {
			response.Duration = time.Since(started)
			return response, session.normalizeReadError(command, err)
		}
		line = strings.TrimSpace(strings.Trim(line, "\x00"))
		if line == "" || strings.EqualFold(line, command) ||
			(payloadEcho != "" && line == payloadEcho) {
			continue
		}
		if isFinalResult(line) {
			response.Final = line
			response.Duration = time.Since(started)
			if response.OK() {
				return response, nil
			}
			return response, &CommandError{
				Command: command,
				Final:   line,
				Lines:   append([]string(nil), response.Lines...),
			}
		}
		if isURC(line) && !strings.HasPrefix(strings.ToUpper(line), expectedPrefix) {
			response.URCs = append(response.URCs, line)
			session.enqueueURCLocked(line)
			continue
		}
		response.Lines = append(response.Lines, line)
	}
}

func (session *Session) waitPromptLocked(
	ctx context.Context,
	command string,
	response *Response,
) error {
	expectedPrefix := expectedResponsePrefix(command)
	for {
		if index := promptIndex(session.readBuf); index >= 0 {
			prefix := string(session.readBuf[:index])
			session.readBuf = session.readBuf[index+1:]
			for len(session.readBuf) > 0 &&
				(session.readBuf[0] == ' ' || session.readBuf[0] == '\t') {
				session.readBuf = session.readBuf[1:]
			}
			for _, line := range strings.FieldsFunc(prefix, func(character rune) bool {
				return character == '\r' || character == '\n'
			}) {
				if err := session.consumePromptLineLocked(
					command,
					expectedPrefix,
					line,
					response,
				); err != nil {
					return err
				}
			}
			return nil
		}
		if line, ok := popLine(&session.readBuf); ok {
			if err := session.consumePromptLineLocked(
				command,
				expectedPrefix,
				line,
				response,
			); err != nil {
				return err
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		buffer := make([]byte, 1024)
		count, err := readTransportContext(ctx, session.transport, buffer)
		if count > 0 {
			session.readBuf = append(session.readBuf, buffer[:count]...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && session.closed {
				return ErrSessionClosed
			}
			session.poisonLocked()
			return fmt.Errorf("read serial prompt: %w", err)
		}
	}
}

func promptIndex(buffer []byte) int {
	for index, character := range buffer {
		if character != '>' {
			continue
		}
		if index == 0 || buffer[index-1] == '\r' || buffer[index-1] == '\n' {
			return index
		}
	}
	return -1
}

func (session *Session) consumePromptLineLocked(
	command string,
	expectedPrefix string,
	line string,
	response *Response,
) error {
	line = strings.TrimSpace(strings.Trim(line, "\x00"))
	if line == "" || strings.EqualFold(line, command) {
		return nil
	}
	if isFinalResult(line) {
		response.Final = line
		if response.OK() {
			return fmt.Errorf("%w: %s", ErrPromptNotReceived, command)
		}
		return &CommandError{
			Command: command,
			Final:   line,
			Lines:   append([]string(nil), response.Lines...),
		}
	}
	if isURC(line) && !strings.HasPrefix(strings.ToUpper(line), expectedPrefix) {
		response.URCs = append(response.URCs, line)
		session.enqueueURCLocked(line)
		return nil
	}
	response.Lines = append(response.Lines, line)
	return nil
}

func (session *Session) normalizeReadError(command string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		_ = session.transport.ResetInputBuffer()
		session.readBuf = nil
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %s", ErrCommandTimeout, command)
		}
	}
	return err
}

func (session *Session) abortPromptLocked() {
	_ = writeAll(session.transport, []byte{0x1b})
	_ = session.transport.Drain()
	_ = session.transport.ResetInputBuffer()
	session.readBuf = nil
}

// WaitURC waits for an unsolicited result matching predicate. Non-matching URCs
// remain queued for another consumer.
func (session *Session) WaitURC(
	ctx context.Context,
	predicate func(string) bool,
) (string, error) {
	if predicate == nil {
		return "", errors.New("modem: URC predicate is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return "", ErrSessionClosed
	}

	for index, line := range session.urcs {
		if predicate(line) {
			session.urcs = append(session.urcs[:index], session.urcs[index+1:]...)
			return line, nil
		}
	}
	for {
		line, err := session.readLineLocked(ctx)
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(strings.Trim(line, "\x00"))
		if line == "" {
			continue
		}
		if predicate(line) {
			return line, nil
		}
		session.enqueueURCLocked(line)
	}
}

func (session *Session) enqueueURCLocked(line string) {
	if len(session.urcs) >= session.options.MaxURCs {
		copy(session.urcs, session.urcs[1:])
		session.urcs[len(session.urcs)-1] = line
		return
	}
	session.urcs = append(session.urcs, line)
}

func (session *Session) readLineLocked(ctx context.Context) (string, error) {
	for {
		if line, ok := popLine(&session.readBuf); ok {
			return line, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		buffer := make([]byte, 1024)
		count, err := readTransportContext(ctx, session.transport, buffer)
		if count > 0 {
			session.readBuf = append(session.readBuf, buffer[:count]...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && session.closed {
				return "", ErrSessionClosed
			}
			session.poisonLocked()
			return "", fmt.Errorf("read serial response: %w", err)
		}
	}
}

// readTransportContext checks cancellation around one bounded transport read.
// It must not close a transport from another goroutine: some serial libraries
// wait in Close for the active reader while that reader is itself stuck in a
// blocking read(2), deadlocking the session and every caller queued behind it.
// Linux serial transports therefore keep their fd non-blocking and enforce the
// configured read timeout with poll(2).
func readTransportContext(
	ctx context.Context,
	transport Transport,
	buffer []byte,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count, err := transport.Read(buffer)
	if contextErr := ctx.Err(); contextErr != nil {
		return 0, contextErr
	}
	return count, err
}

func popLine(buffer *[]byte) (string, bool) {
	data := *buffer
	for index, character := range data {
		if character != '\r' && character != '\n' {
			continue
		}
		line := string(data[:index])
		next := index + 1
		for next < len(data) && (data[next] == '\r' || data[next] == '\n') {
			next++
		}
		*buffer = data[next:]
		return line, true
	}
	return "", false
}

func normalizeATCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("modem: AT command is empty")
	}
	if len(command) > 512 {
		return "", errors.New("modem: AT command exceeds 512 bytes")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		return "", errors.New("modem: AT command contains a control delimiter")
	}
	if !strings.HasPrefix(strings.ToUpper(command), "AT") {
		return "", errors.New("modem: command must start with AT")
	}
	return command, nil
}

func expectedResponsePrefix(command string) string {
	upper := strings.ToUpper(strings.TrimSpace(command))
	if strings.HasPrefix(upper, "AT+CUSD=") {
		// +CUSD is asynchronous even when it arrives before the command's OK.
		return "\x00"
	}
	body := strings.TrimPrefix(upper, "AT")
	if body == "" || body == "I" {
		return "\x00"
	}
	end := len(body)
	for index, character := range body {
		if character == '?' || character == '=' || character == ',' {
			end = index
			break
		}
	}
	name := body[:end]
	if name == "" {
		return "\x00"
	}
	return name + ":"
}

func isFinalResult(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	return upper == "OK" ||
		upper == "ERROR" ||
		upper == "NO CARRIER" ||
		upper == "BUSY" ||
		upper == "NO ANSWER" ||
		strings.HasPrefix(upper, "+CME ERROR:") ||
		strings.HasPrefix(upper, "+CMS ERROR:")
}

func isURC(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	if upper == "RING" ||
		upper == "RDY" ||
		upper == "CALL READY" ||
		upper == "SMS READY" ||
		upper == "PB DONE" {
		return true
	}
	for _, prefix := range []string{
		"+CMTI:",
		"+CMT:",
		"+CDS:",
		"+CREG:",
		"+CGREG:",
		"+CEREG:",
		"+CUSD:",
		"+CLIP:",
		"+CRING:",
		"+QIND:",
		"+QIURC:",
		"+QSIMSTAT:",
		"+QUSIM:",
		"+QNWINFO:",
	} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		count, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		if count > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[count:]
	}
	return nil
}

func (session *Session) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	session.closed = true
	return session.transport.Close()
}
