//go:build linux

package qmiproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

func writeProxyScript(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qmi-proxy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+contents+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func cleanupChild(t *testing.T, child *proxyChild) {
	t.Helper()
	t.Cleanup(func() {
		if !child.exited() {
			_ = child.command.Process.Kill()
		}
		select {
		case <-child.done:
		case <-time.After(3 * time.Second):
			t.Error("managed proxy was not reaped")
		}
	})
}

func TestExitedChildIsReapedAndFailureOutputIsBounded(t *testing.T) {
	executable := writeProxyScript(t, "printf 'proxy bind failed\\n' >&2; exit 7")
	child, err := startProxy(executable)
	if err != nil {
		t.Fatal(err)
	}
	cleanupChild(t, child)
	select {
	case <-child.done:
	case <-time.After(3 * time.Second):
		t.Fatal("child exit did not trigger Wait")
	}
	if child.err == nil || !strings.Contains(child.failure(), "proxy bind failed") {
		t.Fatalf("missing child failure: %s", child.failure())
	}
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(child.command.Process.Pid, &status, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("child still waitable after supervisor completion: %v", err)
	}
	var output cappedOutput
	data := []byte(strings.Repeat("x", 8192))
	if n, err := output.Write(data); n != len(data) || err != nil || len(output.text) != 4096 {
		t.Fatalf("bounded diagnostic output: n=%d, stored=%d, err=%v", n, len(output.text), err)
	}
}

func TestConcurrentClientsShareOneManagedChild(t *testing.T) {
	manager := newSupervisor()
	var starts atomic.Int32
	manager.dial = func(context.Context, string) error {
		if starts.Load() == 0 {
			return syscall.ECONNREFUSED
		}
		return nil
	}
	executable := writeProxyScript(t, "exec sleep 30")
	manager.start = func(path string) (*proxyChild, error) {
		child, err := startProxy(path)
		if err == nil {
			cleanupChild(t, child)
			starts.Add(1)
		}
		return child, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var group sync.WaitGroup
	errors := make(chan error, 20)
	for index := 0; index < cap(errors); index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			path := "qmi-proxy"
			if index%2 == 0 {
				path = "@qmi-proxy"
			}
			errors <- manager.ensure(ctx, path, executable)
		}(index)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
	if count := starts.Load(); count != 1 {
		t.Fatalf("started %d proxies for concurrent clients", count)
	}
}

func TestAmbiguousDialFailureNeverStartsProxy(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, syscall.ETIMEDOUT, syscall.EACCES} {
		t.Run(failure.Error(), func(t *testing.T) {
			manager := newSupervisor()
			manager.dial = func(context.Context, string) error { return failure }
			manager.start = func(string) (*proxyChild, error) {
				t.Fatal("ambiguous dial failure started a competing proxy")
				return nil, nil
			}
			if err := manager.ensure(context.Background(), "qmi-proxy", "unused"); !errors.Is(err, failure) {
				t.Fatalf("lost dial failure: %v", err)
			}
		})
	}
}

func TestCancelledContextNeverStartsProxy(t *testing.T) {
	manager := newSupervisor()
	manager.start = func(string) (*proxyChild, error) {
		t.Fatal("cancelled request started a proxy")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.ensure(ctx, "qmi-proxy", "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request returned %v", err)
	}
}

func TestFailedChildRestartIsRateLimited(t *testing.T) {
	manager := newSupervisor()
	now := time.Now()
	manager.now = func() time.Time { return now }
	manager.dial = func(context.Context, string) error { return syscall.ECONNREFUSED }
	starts := 0
	executable := writeProxyScript(t, "exit 7")
	manager.start = func(path string) (*proxyChild, error) {
		starts++
		child, err := startProxy(path)
		if err == nil {
			cleanupChild(t, child)
		}
		return child, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.ensure(ctx, "qmi-proxy", executable); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected child exit, got %v", err)
	}
	if err := manager.ensure(ctx, "qmi-proxy", executable); err == nil || !strings.Contains(err.Error(), "restart delayed") {
		t.Fatalf("expected restart cooldown, got %v", err)
	}
	if starts != 1 {
		t.Fatalf("restarted immediately after failure: %d starts", starts)
	}
	now = now.Add(proxyRestartDelay)
	_ = manager.ensure(ctx, "qmi-proxy", executable)
	if starts != 2 {
		t.Fatalf("restart did not resume after cooldown: %d starts", starts)
	}
}

func TestLiveChildIsNotDuplicatedAfterCallerTimeout(t *testing.T) {
	manager := newSupervisor()
	manager.dial = func(context.Context, string) error { return syscall.ECONNREFUSED }
	starts := 0
	executable := writeProxyScript(t, "exec sleep 30")
	manager.start = func(path string) (*proxyChild, error) {
		starts++
		child, err := startProxy(path)
		if err == nil {
			cleanupChild(t, child)
		}
		return child, err
	}
	for index := 0; index < 2; index++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := manager.ensure(ctx, "qmi-proxy", executable)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout, got %v", err)
		}
	}
	if starts != 1 {
		t.Fatalf("duplicated a live child after timeout: %d starts", starts)
	}
}

func TestExistingProxyIsReusedWithoutStartingOrStoppingIt(t *testing.T) {
	name := fmt.Sprintf("vocat-proxy-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	listener, err := net.Listen("unix", socketAddress(name))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	manager := newSupervisor()
	manager.start = func(string) (*proxyChild, error) {
		t.Fatal("started another proxy despite an existing listener")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.ensure(ctx, name, "unused"); err != nil {
		t.Fatal(err)
	}
	if err := probeProxy(ctx, socketAddress(name)); err != nil {
		t.Fatalf("existing proxy was disturbed: %v", err)
	}
}

func TestDependencyCannotAutoSpawnIfSocketDisappearsAfterProbe(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "unmanaged-start")
	executable := writeProxyScript(t, fmt.Sprintf("touch '%s'", marker))
	manager := newSupervisor()
	// Simulate the listener disappearing between a successful managed probe
	// and the dependency's own dial; that second dial must never spawn a child.
	manager.dial = func(context.Context, string) error { return nil }
	manager.start = func(string) (*proxyChild, error) {
		t.Fatal("successful probe unexpectedly started a proxy")
		return nil, nil
	}
	opts := qmi.DefaultClientOptions()
	opts.UseProxy = true
	opts.ProxyPath = fmt.Sprintf("vocat-no-listener-%d-%d", os.Getpid(), time.Now().UnixNano())
	opts.ProxyExecutable = executable
	opts.ProxyFallbackToRaw = true
	opts.Logf = func(qmi.ClientLogLevel, string, ...any) {}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := openClient(ctx, "/dev/null", opts, manager)
	if client != nil {
		_ = client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), managedProxyExecutable) {
		t.Fatalf("expected library auto-start to be disabled: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dependency executed unmanaged proxy: %v", err)
	}
	if _, err := os.Stat(managedProxyExecutable); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("managed shim can unexpectedly exist: %v", err)
	}
}
