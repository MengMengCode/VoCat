package modem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
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

// Session serializes all access to one physical AT port. A timed-out command
// keeps ownership of its response until its final arrives or recovery expires.
type Session struct {
	gate      chan struct{}
	transport Transport
	options   SessionOptions
	readBuf   []byte
	readErr   error
	skipSpace bool
	urcs      []string
	pending   *atTransaction
	poisoned  atomic.Bool

	recoveryDeadline time.Time
	ioDeadline       time.Time

	lifetime  context.Context
	cancel    context.CancelFunc
	received  chan atRead
	readDone  chan struct{}
	ioDone    chan error
	closeOnce sync.Once
	closeErr  error
}

type atRead struct {
	data []byte
	err  error
}

type atTransaction struct {
	command        string
	responsePrefix string
	commandEcho    bool
	remainingEcho  []string
	awaitPrompt    bool
}

// PoisonedClient reports failures that require reopening the port.
// Caller cancellation alone does not poison a session or release its pending reply.
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
	lifetime, cancel := context.WithCancel(context.Background())
	session := &Session{
		transport: transport, options: options,
		gate: make(chan struct{}, 1), lifetime: lifetime, cancel: cancel,
		received: make(chan atRead, 8), readDone: make(chan struct{}),
	}
	go session.readLoop()
	return session, nil
}

// Poisoned reports whether this session must be discarded after a transport
// failure or expired response recovery. It is safe to call concurrently.
func (session *Session) Poisoned() bool {
	return session.poisoned.Load() || session.lifetime.Err() != nil
}

func (session *Session) lock(ctx context.Context) error {
	select {
	case session.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return commandContextError(ctx.Err())
	case <-session.lifetime.Done():
		return ErrSessionClosed
	}
}

func (session *Session) unlock() { <-session.gate }

func (session *Session) Execute(ctx context.Context, command string) (Response, error) {
	command, err := normalizeATCommand(command)
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := session.commandContext(ctx)
	defer cancel()

	if err := session.lock(ctx); err != nil {
		return Response{Command: command}, err
	}
	defer session.unlock()
	return session.executeLocked(ctx, command, nil, false)
}

func (session *Session) ExecutePrompt(ctx context.Context, command string, payload []byte) (Response, error) {
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

	if err := session.lock(ctx); err != nil {
		return Response{Command: command}, err
	}
	defer session.unlock()
	return session.executeLocked(ctx, command, append([]byte(nil), payload...), true)
}

func (session *Session) commandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, session.options.CommandTimeout)
}

func commandContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrCommandTimeout, err)
	}
	return err
}

func (session *Session) executeLocked(ctx context.Context, command string, payload []byte, interactive bool) (response Response, err error) {
	response.Command = command
	if session.Poisoned() {
		return response, ErrSessionClosed
	}
	if err := ctx.Err(); err != nil {
		return response, commandContextError(err)
	}
	if err := session.waitIOLocked(ctx); err != nil {
		return response, err
	}
	if err := session.runIOLocked(ctx, func() error { return drainTransport(session.lifetime, session.transport) }); err != nil {
		return response, fmt.Errorf("drain %s: %w", command, err)
	}
	// Never assign an old reply (including idle input and partial trailing
	// lines) to a new command. Flushing cannot isolate bytes still in flight.
	for {
		session.receivePendingLocked()
		if session.pending == nil {
			session.readBuf = bytes.TrimLeft(session.readBuf, "\r\n\x00 \t")
			if len(session.readBuf) == 0 && session.readErr == nil {
				session.recoveryDeadline = time.Time{}
				break
			}
		}
		if err := session.readNextLocked(ctx, nil, nil); err != nil {
			return response, commandContextError(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return response, commandContextError(err)
	}
	started := time.Now()
	defer func() { response.Duration = time.Since(started) }()
	transaction := &atTransaction{
		command: command, responsePrefix: expectedResponsePrefix(command), awaitPrompt: interactive,
	}
	session.pending = transaction
	// Cancellation does not stop the modem. Preserve the caller's deadline and
	// allow known long commands to finish even when it was shorter than usual.
	deadline, _ := ctx.Deadline()
	minimum := max(session.options.CommandTimeout, deadline.Sub(started))
	if interactive {
		minimum = max(minimum, 125*time.Second)
	} else if strings.EqualFold(command, "AT+COPS=?") {
		minimum = max(minimum, 150*time.Second)
	}
	session.recoveryDeadline = started.Add(minimum + 5*session.options.CommandTimeout)
	// Cancellation releases the caller, not a partially written command.
	if err := session.runIOLocked(ctx, func() error {
		if err := writeAll(session.transport, []byte(command+"\r")); err != nil {
			return err
		}
		if interactive {
			return drainTransport(session.lifetime, session.transport)
		}
		return nil
	}); err != nil {
		return response, fmt.Errorf("write %s: %w", command, err)
	}
	for session.pending != nil {
		if err := session.readNextLocked(ctx, &response, payload); err != nil {
			return response, commandContextError(err)
		}
	}
	if !response.OK() {
		return response, &CommandError{
			Command: command,
			Final:   response.Final,
			Lines:   append([]string(nil), response.Lines...),
		}
	}
	if transaction.awaitPrompt {
		return response, fmt.Errorf("%w: %s", ErrPromptNotReceived, command)
	}
	return response, nil
}

func (session *Session) readLoop() {
	defer close(session.readDone)
	buffer := make([]byte, 1024)
	for session.lifetime.Err() == nil {
		count, err := session.transport.Read(buffer)
		if count == 0 && (err == nil || errors.Is(err, syscall.EINTR)) {
			continue
		}
		batch := atRead{data: append([]byte(nil), buffer[:max(count, 0)]...), err: err}
		if err != nil {
			session.poisoned.Store(true)
		}
		select {
		case session.received <- batch:
		case <-session.lifetime.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (session *Session) receivePendingLocked() {
	for {
		select {
		case batch := <-session.received:
			session.readBuf = append(session.readBuf, batch.data...)
			session.readErr = batch.err
		default:
			return
		}
	}
}

// Only one I/O task may run; even after cancellation, later operations wait
// for it before accessing the transaction or issuing another write.
func (session *Session) runIOLocked(ctx context.Context, operation func() error) error {
	done := make(chan error, 1)
	session.ioDone = done
	session.ioDeadline = time.Now().Add(5 * session.options.CommandTimeout)
	if session.recoveryDeadline.After(session.ioDeadline) {
		session.ioDeadline = session.recoveryDeadline
	}
	go func() {
		err := operation()
		if err != nil {
			session.poisoned.Store(true)
		}
		done <- err
	}()
	return session.waitIOLocked(ctx)
}

func (session *Session) waitIOLocked(ctx context.Context) error {
	if session.ioDone == nil {
		return nil
	}
	timer := time.NewTimer(time.Until(session.ioDeadline))
	defer timer.Stop()
	select {
	case err := <-session.ioDone:
		session.ioDone = nil
		return err
	case <-ctx.Done():
		return commandContextError(ctx.Err())
	case <-timer.C:
		select {
		case err := <-session.ioDone:
			session.ioDone = nil
			return err
		default:
			session.poisoned.Store(true)
			return fmt.Errorf("%w: transport I/O never completed", ErrSessionClosed)
		}
	case <-session.lifetime.Done():
		return ErrSessionClosed
	}
}

// A nil response means the caller of the pending transaction has already left.
// Its remaining lines are discarded, but URCs and the final still get processed.
func (session *Session) readNextLocked(ctx context.Context, response *Response, payload []byte) error {
	if err := session.waitIOLocked(ctx); err != nil {
		return err
	}
	var recovery <-chan time.Time
	for {
		if recovery != nil && !time.Now().Before(session.recoveryDeadline) {
			session.poisoned.Store(true)
			return fmt.Errorf("%w: stale response never completed", ErrSessionClosed)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if session.skipSpace && len(session.readBuf) > 0 {
			session.skipSpace = false
			if session.readBuf[0] == ' ' {
				session.readBuf = session.readBuf[1:]
				continue
			}
		}
		if session.pending != nil && session.pending.awaitPrompt && len(session.readBuf) > 0 && session.readBuf[0] == '>' {
			session.readBuf = session.readBuf[1:]
			session.skipSpace = true
			return session.sendPayloadLocked(ctx, response != nil, payload)
		}
		if line, ok := popLine(&session.readBuf); ok {
			if session.pending == nil {
				session.recoveryDeadline = time.Time{}
			}
			session.consumeLineLocked(line, response)
			return nil
		}
		if session.readErr != nil {
			session.poisoned.Store(true)
			return fmt.Errorf("read serial response: %w", session.readErr)
		}
		if response == nil && recovery == nil && (session.pending != nil || len(bytes.Trim(session.readBuf, "\r\n\x00 \t")) != 0) {
			if session.recoveryDeadline.IsZero() {
				session.recoveryDeadline = time.Now().Add(5 * session.options.CommandTimeout)
			}
			timer := time.NewTimer(time.Until(session.recoveryDeadline))
			defer timer.Stop()
			recovery = timer.C
		}
		select {
		case batch := <-session.received:
			session.readBuf = append(session.readBuf, batch.data...)
			session.readErr = batch.err
		case <-ctx.Done():
			continue
		case <-recovery:
			continue
		case <-session.lifetime.Done():
			return ErrSessionClosed
		}
	}
}

func (session *Session) sendPayloadLocked(ctx context.Context, active bool, payload []byte) error {
	if !active || ctx.Err() != nil {
		session.recoveryDeadline = time.Now().Add(5 * session.options.CommandTimeout)
	}
	return session.runIOLocked(ctx, func() error {
		terminator := byte(0x1b) // ESC aborts a prompt whose caller has left.
		if active && ctx.Err() == nil {
			if session.pending.commandEcho {
				session.pending.remainingEcho = strings.FieldsFunc(string(payload), func(r rune) bool { return r == '\r' || r == '\n' })
			}
			if err := writeAll(session.transport, payload); err != nil {
				return fmt.Errorf("write SMS payload: %w", err)
			}
			if ctx.Err() == nil {
				terminator = 0x1a
			}
		}
		if err := writeAll(session.transport, []byte{terminator}); err != nil {
			return fmt.Errorf("terminate SMS payload: %w", err)
		}
		session.pending.awaitPrompt = false
		return drainTransport(session.lifetime, session.transport)
	})
}

func (session *Session) consumeLineLocked(line string, response *Response) {
	pending := session.pending
	if pending != nil && len(pending.remainingEcho) > 0 && line == pending.remainingEcho[0] {
		pending.remainingEcho = pending.remainingEcho[1:]
		return
	}
	line = strings.TrimSpace(strings.Trim(line, "\x00"))
	if line == "" {
		return
	}
	if pending == nil {
		if !isFinalResult(line) {
			session.enqueueURCLocked(line)
		}
		return
	}
	if strings.EqualFold(line, pending.command) {
		pending.commandEcho = true
		return
	}
	callResult := isCallResult(line)
	if callResult || (isURC(line) && !strings.HasPrefix(strings.ToUpper(line), pending.responsePrefix)) {
		session.enqueueURCLocked(line)
		if response != nil {
			response.URCs = append(response.URCs, line)
		}
		if !callResult || !isCallCommand(pending.command) {
			return
		}
	}
	pending.remainingEcho = nil
	if isFinalResult(line) || callResult {
		session.pending = nil
		session.recoveryDeadline = time.Time{}
		if response != nil {
			response.Final = line
		}
	} else if response != nil {
		response.Lines = append(response.Lines, line)
	}
}

// WaitURC waits for an unsolicited result matching predicate. Non-matching URCs
// remain queued for another consumer. It shares Execute's transaction lock.
func (session *Session) WaitURC(ctx context.Context, predicate func(string) bool) (string, error) {
	if predicate == nil {
		return "", errors.New("modem: URC predicate is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := session.lock(ctx); err != nil {
		return "", err
	}
	defer session.unlock()
	if session.Poisoned() {
		return "", ErrSessionClosed
	}
	for {
		for index, line := range session.urcs {
			if predicate(line) {
				session.urcs = append(session.urcs[:index], session.urcs[index+1:]...)
				return line, nil
			}
		}
		if err := session.readNextLocked(ctx, nil, nil); err != nil {
			return "", err
		}
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

// drainTransport retries tcdrain/TCSBRK when interrupted by a signal.
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

func isCallCommand(command string) bool {
	command = strings.ToUpper(strings.TrimSpace(command))
	return command == "ATA" || command == "ATA0" || strings.HasPrefix(command, "ATD")
}

func isCallResult(line string) bool {
	switch strings.ToUpper(strings.TrimSpace(line)) {
	case "NO CARRIER", "BUSY", "NO ANSWER":
		return true
	default:
		return false
	}
}

func isFinalResult(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	return upper == "OK" ||
		upper == "ERROR" ||
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
		"CIEV:",
		"+CIEV:",
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
		if count < 0 || count > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[count:]
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (session *Session) Close() error {
	session.closeOnce.Do(func() {
		session.cancel()
		session.closeErr = session.transport.Close()
	})
	<-session.readDone
	session.gate <- struct{}{}
	defer session.unlock()
	if session.ioDone != nil {
		<-session.ioDone
		session.ioDone = nil
	}
	return session.closeErr
}
