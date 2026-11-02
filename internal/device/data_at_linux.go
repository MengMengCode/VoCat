//go:build linux

package device

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Host-side timing for the AT data backend. The first lease budget follows
// the vendor FAQ (DHCP completes within tens of seconds after the PDP is up);
// stop must be quick so disable stays fail-closed.
const (
	atHostFirstLeaseTimeout = 60 * time.Second
	atHostConfigureTimeout  = 15 * time.Second
	atHostStopTimeout       = 10 * time.Second

	// atHostEventPollInterval is how often the non-blocking FIFO reader wakes
	// to re-check the session context while the DHCP hook is idle. It only
	// bounds shutdown latency, not event delivery: a hook write wakes the
	// next read immediately.
	atHostEventPollInterval = 50 * time.Millisecond
)

func newATHostDataLink() atHostDataLink {
	return &linuxATHostDataLink{}
}

type linuxATHostDataLink struct{}

// Start brings the cellular interface up, runs a foreground busybox udhcpc
// whose hook script reports lease events over a FIFO, and returns once the
// first lease is applied. The session owns the DHCP process and the protected
// policy-routing generation until Close.
func (link *linuxATHostDataLink) Start(ctx context.Context, networkInterface string) (atHostDataSession, error) {
	networkInterface = strings.TrimSpace(networkInterface)
	if networkInterface == "" {
		return nil, fmt.Errorf("%w: a network interface is required", ErrDataBackendUnavailable)
	}
	ipCommand, err := lookPathIP("ip")
	if err != nil {
		return nil, fmt.Errorf("%w: install iproute2 to control %s", ErrDataBackendUnavailable, networkInterface)
	}
	busybox, err := exec.LookPath("busybox")
	if err != nil {
		return nil, fmt.Errorf("%w: busybox udhcpc is required for AT host data on %s", ErrDataBackendUnavailable, networkInterface)
	}

	dir, err := os.MkdirTemp("", "vocat-atdata-")
	if err != nil {
		return nil, err
	}
	eventPath := filepath.Join(dir, "events")
	if err := syscall.Mkfifo(eventPath, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("create DHCP event channel: %w", err)
	}
	scriptPath := filepath.Join(dir, "hook.sh")
	scriptText := fmt.Sprintf(`#!/bin/sh
case "$1" in
  bound|renew)
    (umask 077; printf 'event=%%s\nip=%%s\nsubnet=%%s\nrouter=%%s\ndns=%%s\n\n' "$1" "$ip" "$subnet" "$router" "$dns" > %q)
    ;;
  deconfig|released|carrierlost|fail)
    (umask 077; printf 'event=deconfig\n\n' > %q)
    ;;
esac
exit 0
`, eventPath, eventPath)
	if err := os.WriteFile(scriptPath, []byte(scriptText), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &linuxATHostDataSession{
		networkInterface: networkInterface,
		ipCommand:        ipCommand,
		runner:           hostCommandOutput,
		dir:              dir,
		scriptPath:       scriptPath,
		eventPath:        eventPath,
		background:       sessionCtx,
		cancel:           cancel,
		firstLease:       make(chan struct{}),
		done:             make(chan struct{}),
	}

	fail := func(cause error) (atHostDataSession, error) {
		if cleanupErr := session.Close(context.Background()); cleanupErr != nil {
			// Keep an owner for a failed cleanup so the manager can retry it
			// before another data backend takes over this interface.
			return session, errors.Join(cause, cleanupErr)
		}
		return nil, cause
	}

	// A daemon or modem restart can leave the previous generation on the
	// interface; the lease must always start from a clean host state.
	if err := session.cleanupHost(ctx); err != nil {
		return fail(fmt.Errorf("prepare AT host interface: %w", err))
	}
	if result, linkErr := hostCommandOutput(ctx, ipCommand, "link", "set", "dev", networkInterface, "up"); linkErr != nil {
		return fail(fmt.Errorf("set %s up: %w: %s", networkInterface, linkErr, strings.TrimSpace(string(result))))
	}

	// The FIFO reader must exist before udhcpc starts writing events.
	session.wg.Add(1)
	go session.readEvents()

	process, err := startForegroundUDHCPC(sessionCtx, busybox, scriptPath, dir, networkInterface)
	if err != nil {
		return fail(err)
	}
	session.process = process
	session.wg.Add(1)
	go session.watchProcess()

	if err := session.awaitFirstLease(ctx); err != nil {
		session.mu.Lock()
		clean := session.cleanupComplete
		session.mu.Unlock()
		if !clean {
			return session, err
		}
		return nil, err
	}
	return session, nil
}

// awaitFirstLease waits for the first DHCP lease within the lease timeout and
// closes the session on every failure path.
//
// udhcpc without -n retries discovery indefinitely, so a carrier that never
// answers DHCP would block Start forever while the caller holds the device
// operation lock. The bound therefore lives here rather than in the
// caller's context, which may carry no deadline at all.
func (session *linuxATHostDataSession) awaitFirstLease(ctx context.Context) error {
	timeout := session.firstLeaseTimeout
	if timeout <= 0 {
		timeout = atHostFirstLeaseTimeout
	}
	leaseContext, cancelLease := context.WithTimeout(ctx, timeout)
	defer cancelLease()
	if err := session.waitFirstLease(leaseContext); err != nil {
		// Close on every failure path, not only the cancellation branch: a
		// lost lease or an exited DHCP process must not leave the process
		// and its temp directory behind.
		return errors.Join(err, session.Close(context.Background()))
	}
	return nil
}

type atHostProcess interface {
	Wait() error
	Stop(ctx context.Context) error
}

type execHostProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
	once    sync.Once
}

func newExecHostProcess(cmd *exec.Cmd) *execHostProcess {
	process := &execHostProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		process.waitErr = cmd.Wait()
		close(process.done)
	}()
	return process
}

func (process *execHostProcess) Wait() error {
	<-process.done
	return process.waitErr
}

func (process *execHostProcess) Stop(ctx context.Context) error {
	if process.cmd.Process != nil {
		_ = process.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-process.done:
		return nil
	case <-ctx.Done():
		if process.cmd.Process != nil {
			_ = process.cmd.Process.Kill()
		}
		<-process.done
		return ctx.Err()
	}
}

func startForegroundUDHCPC(
	ctx context.Context,
	busybox, scriptPath, dir, networkInterface string,
) (atHostProcess, error) {
	command := exec.CommandContext(ctx, busybox, "udhcpc", "-f", "-i", networkInterface, "-s", scriptPath)
	logFile, err := os.Create(filepath.Join(dir, "udhcpc.log"))
	if err == nil {
		command.Stdout = logFile
		command.Stderr = logFile
		// exec.Cmd.Wait does not close a file the caller supplied, and the
		// child keeps its own copy of the descriptor. Closing it here keeps
		// a long-running daemon from collecting one descriptor per session.
		defer func() { _ = logFile.Close() }()
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start udhcpc on %s: %w", networkInterface, err)
	}
	return newExecHostProcess(command), nil
}

type atDHCPLease struct {
	Address string
	Prefix  int
	Gateway string
	DNS     []string
}

type linuxATHostDataSession struct {
	networkInterface  string
	ipCommand         string
	runner            func(context.Context, string, ...string) ([]byte, error)
	process           atHostProcess
	dir               string
	scriptPath        string
	eventPath         string
	background        context.Context
	cancel            context.CancelFunc
	firstLeaseTimeout time.Duration

	// eventMu serializes lease-event application against the process-exit
	// cleanup. Without it, a delayed bound event could finish after
	// watchProcess tore the routes and lease down, resurrecting a session
	// whose DHCP process has already exited.
	eventMu       sync.Mutex
	eventsStopped bool
	// ownedAddresses includes an address as soon as configuration is attempted,
	// including failures after addr replace. Only these addresses are removed.
	ownedAddresses map[string]struct{}

	mu              sync.Mutex
	lease           *atDHCPLease
	lastErr         error
	closed          bool
	closing         bool
	cleanupComplete bool
	closeAttempt    *atHostCloseAttempt
	firstLease      chan struct{}
	firstOnce       sync.Once
	done            chan struct{}
	wg              sync.WaitGroup
}

// Status reports whether the protected host lease is currently established.
func (session *linuxATHostDataSession) Status(context.Context) (bool, string, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return false, "AT host data session is closed", nil
	}
	if session.lease == nil {
		if session.lastErr != nil {
			return false, "AT host data lease is not established", session.lastErr
		}
		return false, "AT host data lease is not established", nil
	}
	return true, fmt.Sprintf(
		"host lease %s/%d on %s",
		session.lease.Address, session.lease.Prefix, session.networkInterface,
	), nil
}

type atHostCloseAttempt struct {
	done chan struct{}
	err  error
}

// Close stops DHCP and removes owned host resources with a bounded independent
// context. Concurrent callers share one attempt; cleanup failures can be retried.
func (session *linuxATHostDataSession) Close(context.Context) error {
	session.mu.Lock()
	if session.cleanupComplete {
		session.mu.Unlock()
		return nil
	}
	if session.closing {
		attempt := session.closeAttempt
		session.mu.Unlock()
		<-attempt.done
		return attempt.err
	}
	stopProcess := !session.closed
	session.closed, session.closing = true, true
	attempt := &atHostCloseAttempt{done: make(chan struct{})}
	session.closeAttempt = attempt
	session.mu.Unlock()

	// Cancel in-flight host commands before waiting for eventMu. Cleanup uses
	// its own bounded context even when the caller's deadline has expired.
	if session.cancel != nil {
		session.cancel()
	}
	session.stopLeaseEvents()
	var closeErr error
	if stopProcess && session.process != nil {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), atHostStopTimeout)
		closeErr = session.process.Stop(stopCtx)
		cancelStop()
	}
	session.wg.Wait()
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), atHostStopTimeout)
	session.eventMu.Lock()
	closeErr = errors.Join(closeErr, session.cleanupHost(cleanupContext))
	session.eventMu.Unlock()
	cancelCleanup()
	if session.dir != "" {
		closeErr = errors.Join(closeErr, os.RemoveAll(session.dir))
	}

	session.mu.Lock()
	session.closing = false
	session.cleanupComplete = closeErr == nil
	attempt.err = closeErr
	close(attempt.done)
	session.mu.Unlock()
	return closeErr
}

// cleanupHost only removes addresses installed by this session. The interface
// can contain addresses supplied by its host administrator or another service.
// The caller holds eventMu once event processing has started.
func (session *linuxATHostDataSession) cleanupHost(ctx context.Context) error {
	cleanupErr := session.clearLeaseHost(ctx)
	if session.runner != nil && session.ipCommand != "" {
		result, err := session.runner(ctx, session.ipCommand, "link", "set", "dev", session.networkInterface, "down")
		if err != nil && !missingATHostInterface(ctx, result, err) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("disable AT host interface: %w: %s", err, strings.TrimSpace(string(result))))
		}
	}
	return cleanupErr
}

func (session *linuxATHostDataSession) clearLeaseHost(ctx context.Context) error {
	cleanupErr := clearExportProxyRouteWithError(ctx, session.networkInterface)
	for address := range session.ownedAddresses {
		cleanupErr = errors.Join(cleanupErr, session.removeOwnedAddress(ctx, address))
	}
	return cleanupErr
}

func missingATHostInterface(ctx context.Context, output []byte, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return strings.Contains(strings.ToLower(string(output)), "cannot find device")
}

func (session *linuxATHostDataSession) removeOwnedAddress(ctx context.Context, address string) error {
	if session.runner == nil || session.ipCommand == "" {
		return nil
	}
	result, err := session.runner(ctx, session.ipCommand, "-4", "addr", "del", address, "dev", session.networkInterface)
	if err != nil {
		message := strings.ToLower(string(result))
		alreadyAbsent := strings.Contains(message, "cannot assign requested address") || missingATHostInterface(ctx, result, err)
		if ctx.Err() != nil || !alreadyAbsent {
			return fmt.Errorf("remove AT host address: %w: %s", err, strings.TrimSpace(string(result)))
		}
	}
	delete(session.ownedAddresses, address)
	return nil
}

// readEvents drains the DHCP hook records from the event FIFO.
//
// The FIFO is opened read-write and non-blocking so shutdown can never park in
// open(2): a reader that lost the race against the DHCP writer used to block
// forever, and Close then hung in wg.Wait() while holding the device
// operation lock. Read-write keeps the FIFO from ever reporting EOF, and the
// EAGAIN poll loop observes the session context between attempts.
func (session *linuxATHostDataSession) readEvents() {
	defer session.wg.Done()
	background := session.background
	if background == nil {
		background = context.Background()
	}
	fd, err := syscall.Open(session.eventPath, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer func() { _ = syscall.Close(fd) }()

	buffer := make([]byte, 4096)
	var pending []byte
	values := make(map[string]string)
	for {
		if background.Err() != nil {
			return
		}
		read, readErr := syscall.Read(fd, buffer)
		if read > 0 {
			pending = append(pending, buffer[:read]...)
			for {
				index := bytes.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				line := strings.TrimSpace(string(pending[:index]))
				pending = pending[index+1:]
				if line == "" {
					// A blank line terminates a hook record.
					if len(values) > 0 {
						session.handleLeaseEvent(values)
						values = make(map[string]string)
					}
					continue
				}
				if key, value, found := strings.Cut(line, "="); found {
					values[strings.TrimSpace(key)] = strings.TrimSpace(value)
				}
			}
			continue
		}
		if readErr == syscall.EINTR {
			continue
		}
		if readErr != nil && readErr != syscall.EAGAIN && readErr != syscall.EWOULDBLOCK {
			return
		}
		if !session.waitForEventPoll(background) {
			return
		}
	}
}

// waitForEventPoll sleeps for one poll interval, returning false as soon as
// the session context is cancelled so the reader can exit.
func (session *linuxATHostDataSession) waitForEventPoll(background context.Context) bool {
	timer := time.NewTimer(atHostEventPollInterval)
	defer timer.Stop()
	select {
	case <-background.Done():
		return false
	case <-timer.C:
		return true
	}
}

// handleLeaseEvent applies a hook record unless the lease events have been
// stopped. It is the only path from event parsing to the host state, so the
// stop flag and event application are atomic with respect to each other.
func (session *linuxATHostDataSession) handleLeaseEvent(values map[string]string) {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	if session.eventsStopped {
		return
	}
	session.applyEvent(values)
}

// stopLeaseEvents permanently disables lease-event application. It is called
// by Close and by the DHCP process watcher before the process-exit cleanup, so
// a delayed bound/renew event can never rebuild the routes that cleanup
// removed.
func (session *linuxATHostDataSession) stopLeaseEvents() {
	session.eventMu.Lock()
	session.eventsStopped = true
	session.eventMu.Unlock()
}

func (session *linuxATHostDataSession) watchProcess() {
	defer session.wg.Done()
	_ = session.process.Wait()
	// An unexpected DHCP exit (interface down, carrier loss) drops the lease
	// and the protected routes; Status then reports disconnected so the
	// monitor can recover. Stop lease events first and hold eventMu for the
	// cleanup, so a bound event racing the exit cannot resurrect the session.
	session.stopLeaseEvents()
	session.eventMu.Lock()
	session.applyEvent(map[string]string{"event": "deconfig"})
	session.eventMu.Unlock()
	close(session.done)
}

// handleEventLines parses hook-script records (key=value lines separated by a
// blank line) and applies them in order.
func (session *linuxATHostDataSession) handleEventLines(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	values := make(map[string]string)
	flush := func() {
		if len(values) == 0 {
			return
		}
		session.handleLeaseEvent(values)
		values = make(map[string]string)
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			flush()
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	flush()
}

func (session *linuxATHostDataSession) applyEvent(values map[string]string) {
	base := session.background
	if base == nil {
		base = context.Background()
	}
	switch strings.ToLower(values["event"]) {
	case "bound", "renew":
		address := net.ParseIP(values["ip"]).To4()
		maskIP := net.ParseIP(values["subnet"]).To4()
		if address == nil || maskIP == nil {
			session.setLeaseError(errors.New("DHCP returned no valid IPv4 address/subnet"))
			return
		}
		ones, bits := net.IPMask(maskIP).Size()
		if bits != 32 || ones < 0 {
			session.setLeaseError(errors.New("DHCP returned an invalid IPv4 subnet"))
			return
		}
		gateway := ""
		if routers := strings.Fields(values["router"]); len(routers) > 0 {
			if net.ParseIP(routers[0]).To4() == nil {
				session.setLeaseError(errors.New("DHCP returned an invalid IPv4 gateway"))
				return
			}
			gateway = routers[0]
		}
		dns := strings.Fields(values["dns"])
		ownedAddress := fmt.Sprintf("%s/%d", address.String(), ones)
		if session.ownedAddresses == nil {
			session.ownedAddresses = make(map[string]struct{})
		}
		configureContext, cancel := context.WithTimeout(base, atHostConfigureTimeout)
		var err error
		// Delete the old address before adding its replacement: on Linux,
		// removing an IPv4 primary can also delete same-subnet secondaries.
		// Installing the replacement first could therefore delete the new lease.
		for previous := range session.ownedAddresses {
			if previous != ownedAddress {
				err = errors.Join(err, session.removeOwnedAddress(configureContext, previous))
			}
		}
		if err == nil {
			// The helper can fail after addr replace. Track the attempted address
			// so deconfig/Close can still undo a partially applied configuration.
			session.ownedAddresses[ownedAddress] = struct{}{}
			_, err = configureExportProxyIPv4(
				configureContext, session.ipCommand, session.networkInterface,
				address.String(), ones, gateway, dns,
			)
		}
		cancel()
		if err != nil {
			// The failed configure already tore down the protected routes
			// and rule, so the previous lease no longer describes the host
			// path. Keeping it would let Status report "connected" from a
			// stale lease and the monitor would never recover the session.
			session.mu.Lock()
			session.lease = nil
			session.lastErr = err
			session.mu.Unlock()
			session.firstOnce.Do(func() { close(session.firstLease) })
			return
		}
		session.mu.Lock()
		session.lease = &atDHCPLease{
			Address: address.String(),
			Prefix:  ones,
			Gateway: gateway,
			DNS:     dns,
		}
		session.lastErr = nil
		session.mu.Unlock()
		session.firstOnce.Do(func() { close(session.firstLease) })
	case "deconfig":
		// This also runs after Close has canceled background. A deconfiguration
		// must still attempt bounded host cleanup and report any failure.
		cleanupContext, cancel := context.WithTimeout(context.Background(), atHostConfigureTimeout)
		err := session.clearLeaseHost(cleanupContext)
		cancel()
		session.mu.Lock()
		session.lease = nil
		session.lastErr = err
		session.mu.Unlock()
	}
}

func (session *linuxATHostDataSession) setLeaseError(err error) {
	session.mu.Lock()
	session.lastErr = err
	session.mu.Unlock()
	session.firstOnce.Do(func() { close(session.firstLease) })
}

func (session *linuxATHostDataSession) waitFirstLease(ctx context.Context) error {
	select {
	case <-session.firstLease:
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.lease != nil {
			return nil
		}
		if session.lastErr != nil {
			return fmt.Errorf("apply first AT host data lease: %w", session.lastErr)
		}
		return errors.New("AT host data session lost its first lease")
	case <-session.done:
		return errors.New("AT host data DHCP process exited before providing a lease")
	case <-ctx.Done():
		session.mu.Lock()
		leaseErr := session.lastErr
		session.mu.Unlock()
		return fmt.Errorf("wait for AT host data lease on %s: %w", session.networkInterface, errors.Join(ctx.Err(), leaseErr))
	}
}
