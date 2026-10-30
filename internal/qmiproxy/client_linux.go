//go:build linux

// Package qmiproxy manages the shared libqmi proxy without detaching its children.
package qmiproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/remoteproc"
)

const (
	defaultProxyPath       = "qmi-proxy"
	defaultProxyExecutable = "/usr/libexec/qmi-proxy"
	defaultOpenTimeout     = 5 * time.Second
	proxyProbeTimeout      = 250 * time.Millisecond
	proxyRetryDelay        = 100 * time.Millisecond
	proxyRestartDelay      = 5 * time.Second

	// quectel-qmi-go v0.6.0 has no public switch to disable its Start/Release
	// auto-launch path, and normalizes an empty executable back to its default.
	// A child of /dev/null cannot exist or be executable, so a socket disappearing
	// between our probe and the library's dial cannot launch an unmanaged child.
	managedProxyExecutable = "/dev/null/vocat-managed-qmi-proxy"
)

var processSupervisor = newSupervisor()

type proxyChild struct {
	command *exec.Cmd
	done    chan struct{}
	err     error // Published by closing done after Cmd.Wait has reaped the child.
	stderr  cappedOutput
}

type proxyEntry struct {
	gate      chan struct{}
	child     *proxyChild
	nextStart time.Time
}

type supervisor struct {
	mu      sync.Mutex
	entries map[string]*proxyEntry
	dial    func(context.Context, string) error
	start   func(string) (*proxyChild, error)
	now     func() time.Time
}

func newSupervisor() *supervisor {
	return &supervisor{
		entries: make(map[string]*proxyEntry),
		dial:    probeProxy,
		start:   startProxy,
		now:     time.Now,
	}
}

// OpenClient reuses an existing proxy or starts one owned and reaped by VoCat.
// It never bypasses a failed proxy by opening the modem directly.
func OpenClient(ctx context.Context, path string, opts qmi.ClientOptions) (*qmi.Client, error) {
	if _, err := remoteproc.CheckPort(path); err != nil {
		return nil, err
	}
	return openClient(ctx, path, opts, processSupervisor)
}

func openClient(ctx context.Context, path string, opts qmi.ClientOptions, manager *supervisor) (*qmi.Client, error) {
	if !opts.UseProxy {
		return qmi.NewClientWithOptions(ctx, path, opts)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := opts.ProxyOpenTimeout
	if timeout <= 0 {
		timeout = defaultOpenTimeout
	}
	openContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := manager.ensure(openContext, opts.ProxyPath, findExecutable(opts.ProxyExecutable)); err != nil {
		return nil, err
	}
	opts.ProxyExecutable = managedProxyExecutable
	opts.ProxyFallbackToRaw = false
	return qmi.NewClientWithOptions(ctx, path, opts)
}

func (manager *supervisor) ensure(ctx context.Context, path, executable string) error {
	address := socketAddress(path)
	manager.mu.Lock()
	item := manager.entries[address]
	if item == nil {
		item = &proxyEntry{gate: make(chan struct{}, 1)}
		item.gate <- struct{}{}
		manager.entries[address] = item
	}
	manager.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-item.gate:
	}
	defer func() { item.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := manager.dial(ctx, address)
	if err == nil {
		return nil
	}
	// A timeout can mean a live but overloaded proxy. Starting another process
	// then creates competing bind failures; only an absent listener permits it.
	if !missingListener(err) {
		return fmt.Errorf("connect qmi-proxy %q without starting another process: %w", path, err)
	}
	if item.child == nil || item.child.exited() {
		if remaining := item.nextStart.Sub(manager.now()); remaining > 0 {
			return fmt.Errorf("qmi-proxy restart delayed for %s after a failed start: %w", remaining.Round(time.Millisecond), err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		item.nextStart = manager.now().Add(proxyRestartDelay)
		child, startErr := manager.start(executable)
		if startErr != nil {
			return fmt.Errorf("start managed qmi-proxy %s: %w", executable, startErr)
		}
		item.child = child
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for managed qmi-proxy %q: %w", path, err)
		}
		if err = manager.dial(ctx, address); err == nil {
			return nil
		}
		if item.child.exited() {
			return fmt.Errorf("managed qmi-proxy %s exited before accepting connections (%s): %w", executable, item.child.failure(), err)
		}
		timer := time.NewTimer(proxyRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for managed qmi-proxy %q: %w", path, ctx.Err())
		case <-item.child.done:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (child *proxyChild) exited() bool {
	select {
	case <-child.done:
		return true
	default:
		return false
	}
}

func (child *proxyChild) failure() string {
	if output := strings.TrimSpace(child.stderr.text); output != "" {
		return fmt.Sprintf("%v; %s", child.err, output)
	}
	return fmt.Sprint(child.err)
}

func startProxy(executable string) (*proxyChild, error) {
	child := &proxyChild{command: exec.Command(executable), done: make(chan struct{})}
	child.command.Stdout = io.Discard
	child.command.Stderr = &child.stderr
	child.command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.command.Start(); err != nil {
		return nil, err
	}
	// Release relinquishes the handle but does not reap Unix zombie processes.
	// Every successful Start is paired with exactly one Wait, even if the caller
	// cancels or no later request opens another QMI client.
	go func() {
		child.err = child.command.Wait()
		close(child.done)
	}()
	return child, nil
}

type cappedOutput struct{ text string }

func (output *cappedOutput) Write(data []byte) (int, error) {
	const limit = 4096
	if remaining := limit - len(output.text); remaining > 0 {
		output.text += string(data[:min(len(data), remaining)])
	}
	return len(data), nil
}

func probeProxy(ctx context.Context, address string) error {
	probeContext, cancel := context.WithTimeout(ctx, proxyProbeTimeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(probeContext, "unix", address)
	if err == nil {
		_ = connection.Close()
	}
	return err
}

func missingListener(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func socketAddress(path string) string {
	if path == "" {
		path = defaultProxyPath
	}
	return "\x00" + strings.TrimPrefix(strings.TrimPrefix(path, "\x00"), "@")
}

func findExecutable(configured string) string {
	if configured != "" && configured != defaultProxyExecutable {
		return configured
	}
	if path, err := exec.LookPath("qmi-proxy"); err == nil {
		return path
	}
	for _, path := range []string{defaultProxyExecutable, "/usr/lib/qmi-proxy", "/usr/lib/libqmi-glib/qmi-proxy"} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return path
		}
	}
	return defaultProxyExecutable
}
