//go:build linux

package device

import (
	"bufio"
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
	ipCommand, err := exec.LookPath("ip")
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

	// A daemon or modem restart can leave the previous generation on the
	// interface; the lease must always start from a clean host state.
	session.cleanupHost(ctx)
	if result, linkErr := hostCommandOutput(ctx, ipCommand, "link", "set", "dev", networkInterface, "up"); linkErr != nil {
		_ = session.Close(ctx)
		return nil, fmt.Errorf("set %s up: %w: %s", networkInterface, linkErr, strings.TrimSpace(string(result)))
	}

	// The FIFO reader must exist before udhcpc starts writing events.
	session.wg.Add(1)
	go session.readEvents()

	process, err := startForegroundUDHCPC(sessionCtx, busybox, scriptPath, dir, networkInterface)
	if err != nil {
		_ = session.Close(ctx)
		return nil, err
	}
	session.process = process
	session.wg.Add(1)
	go session.watchProcess()

	if err := session.waitFirstLease(ctx); err != nil {
		return nil, err
	}
	return session, nil
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
	networkInterface string
	ipCommand        string
	runner           func(context.Context, string, ...string) ([]byte, error)
	process          atHostProcess
	dir              string
	scriptPath       string
	eventPath        string
	background       context.Context
	cancel           context.CancelFunc

	mu         sync.Mutex
	lease      *atDHCPLease
	lastErr    error
	closed     bool
	firstLease chan struct{}
	firstOnce  sync.Once
	done       chan struct{}
	wg         sync.WaitGroup
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

// Close stops the DHCP process, waits for it to exit, and removes every host
// path this session created (protected routes, addresses, DNS file).
func (session *linuxATHostDataSession) Close(ctx context.Context) error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	session.mu.Unlock()

	if session.cancel != nil {
		session.cancel()
	}
	if session.process != nil {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), atHostStopTimeout)
		_ = session.process.Stop(stopCtx)
		cancelStop()
	}
	// Unblock a FIFO reader parked in open(2) by completing one writer
	// handshake, then let the loop observe the cancelled context.
	if session.eventPath != "" {
		if writer, err := os.OpenFile(session.eventPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_, _ = writer.Write([]byte("\n\n"))
			_ = writer.Close()
		}
	}
	session.wg.Wait()

	cleanupContext := ctx
	var cancelCleanup context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		cleanupContext, cancelCleanup = context.WithTimeout(context.Background(), atHostStopTimeout)
		defer cancelCleanup()
	}
	session.cleanupHost(cleanupContext)
	if session.dir != "" {
		_ = os.RemoveAll(session.dir)
	}
	return nil
}

// cleanupHost removes the protected routing generation and the interface
// address. It never touches the main routing table.
func (session *linuxATHostDataSession) cleanupHost(ctx context.Context) {
	clearExportProxyRoute(ctx, session.networkInterface)
	if session.runner == nil || session.ipCommand == "" {
		return
	}
	_, _ = session.runner(ctx, session.ipCommand, "-4", "addr", "flush", "dev", session.networkInterface, "scope", "global")
	_, _ = session.runner(ctx, session.ipCommand, "link", "set", "dev", session.networkInterface, "down")
}

func (session *linuxATHostDataSession) readEvents() {
	defer session.wg.Done()
	for {
		if session.background.Err() != nil {
			return
		}
		file, err := os.OpenFile(session.eventPath, os.O_RDONLY, 0)
		if err != nil {
			return
		}
		var lines []string
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		_ = file.Close()
		if session.background.Err() != nil {
			return
		}
		if len(lines) > 0 {
			session.handleEventLines(strings.NewReader(strings.Join(lines, "\n") + "\n"))
		}
	}
}

func (session *linuxATHostDataSession) watchProcess() {
	defer session.wg.Done()
	_ = session.process.Wait()
	// An unexpected DHCP exit (interface down, carrier loss) drops the lease
	// and the protected routes; Status then reports disconnected so the
	// monitor can recover.
	session.applyEvent(map[string]string{"event": "deconfig"})
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
		session.applyEvent(values)
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
		configureContext, cancel := context.WithTimeout(base, atHostConfigureTimeout)
		detail, err := configureExportProxyIPv4(
			configureContext, session.ipCommand, session.networkInterface,
			address.String(), ones, gateway, dns,
		)
		cancel()
		if err != nil {
			session.setLeaseError(err)
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
		_ = detail
	case "deconfig":
		cleanupContext, cancel := context.WithTimeout(base, atHostConfigureTimeout)
		clearExportProxyRoute(cleanupContext, session.networkInterface)
		if session.runner != nil && session.ipCommand != "" {
			_, _ = session.runner(cleanupContext, session.ipCommand, "-4", "addr", "flush", "dev", session.networkInterface, "scope", "global")
		}
		cancel()
		session.mu.Lock()
		session.lease = nil
		session.mu.Unlock()
	}
}

func (session *linuxATHostDataSession) setLeaseError(err error) {
	session.mu.Lock()
	session.lastErr = err
	session.mu.Unlock()
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
		_ = session.Close(context.Background())
		return fmt.Errorf("wait for AT host data lease on %s: %w", session.networkInterface, ctx.Err())
	}
}
