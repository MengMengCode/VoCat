//go:build linux

package device

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestATHostFirstLeaseFailureReturnsWithoutWaitingForTimeout(t *testing.T) {
	for _, failure := range []string{"invalid lease", "host configuration"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session, runner, _ := newTestATHostSession(t, "enx-test")
				values := map[string]string{"event": "bound", "ip": "192.0.2.10", "subnet": "255.255.255.0"}
				if failure == "invalid lease" {
					values["subnet"] = "invalid"
				} else {
					runner.failNextCommand()
				}
				session.handleLeaseEvent(values)
				started := time.Now()
				if err := session.awaitFirstLease(context.Background()); err == nil {
					t.Fatal("failed first lease was accepted")
				}
				if time.Since(started) != 0 {
					t.Fatalf("first-lease failure waited %s", time.Since(started))
				}
			})
		})
	}
}

func TestATHostCloseUsesIndependentCleanupAfterCallerDeadline(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	session.background, session.cancel = context.WithCancel(context.Background())
	session.handleEventLines(strings.NewReader("event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n"))
	original := hostCommandOutput
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return runner.run(ctx, name, args...)
	}
	hostCommandOutput, session.runner = run, run
	t.Cleanup(func() { hostCommandOutput = original })
	session.wg.Add(1)
	go session.watchProcess()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := session.Close(expired); err != nil {
		t.Fatalf("Close expired caller: %v", err)
	}
	joined := strings.Join(runner.commandLines(), "\n")
	if !strings.Contains(joined, "addr del 192.0.2.10/24 dev enx-test") || !strings.Contains(joined, "link set dev enx-test down") {
		t.Fatalf("cleanup did not run after caller deadline: %s", joined)
	}
	if strings.Contains(joined, "addr flush") {
		t.Fatal("cleanup removed unrelated host addresses")
	}
}

func TestATHostConcurrentCloseWaitsAndCleanupFailureCanRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, runner, process := newTestATHostSession(t, "enx-test")
		original := hostCommandOutput
		cause := errors.New("injected interface cleanup failure")
		entered, resume := make(chan struct{}), make(chan struct{})
		var once sync.Once
		fail := true
		run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if strings.Join(args, " ") == "link set dev enx-test down" && fail {
				once.Do(func() { close(entered) })
				<-resume
				fail = false
				return nil, cause
			}
			return runner.run(ctx, name, args...)
		}
		hostCommandOutput, session.runner = run, run
		t.Cleanup(func() { hostCommandOutput = original })
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { first <- session.Close(context.Background()) }()
		<-entered
		go func() { second <- session.Close(context.Background()) }()
		synctest.Wait()
		select {
		case <-second:
			t.Fatal("concurrent Close returned before cleanup finished")
		default:
		}
		close(resume)
		for _, done := range []chan error{first, second} {
			if err := <-done; !errors.Is(err, cause) {
				t.Fatalf("cleanup error lost: %v", err)
			}
		}
		if err := session.Close(context.Background()); err != nil {
			t.Fatalf("cleanup retry: %v", err)
		}
		if process.stopCount() != 1 {
			t.Fatalf("DHCP stopped %d times", process.stopCount())
		}
	})
}

func TestATHostFailedAddressChangeRetainsCleanupOwnership(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	session.handleEventLines(strings.NewReader("event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n"))
	original := hostCommandOutput
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "route replace table") {
			return nil, errors.New("injected route installation failure")
		}
		return runner.run(ctx, name, args...)
	}
	hostCommandOutput = run
	session.handleEventLines(strings.NewReader("event=renew\nip=198.51.100.20\nsubnet=255.255.255.0\nrouter=198.51.100.1\ndns=192.0.2.53\n\n"))
	hostCommandOutput = original
	connected, _, err := session.Status(context.Background())
	if connected || err == nil {
		t.Fatal("partially applied renewal was reported connected")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commandLines(), "\n")
	for _, address := range []string{"192.0.2.10/24", "198.51.100.20/24"} {
		if !strings.Contains(joined, "addr del "+address+" dev enx-test") {
			t.Fatalf("lost cleanup ownership for %s: %s", address, joined)
		}
	}
}

// USB unplug removes the interface before its session is invalidated. Missing
// hardware is already clean; permission failures must still remain visible.
func TestATHostCloseAcceptsAnAlreadyRemovedInterface(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	session.ownedAddresses = map[string]struct{}{"192.0.2.10/24": {}}
	original := hostCommandOutput
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "addr del") || strings.Contains(joined, "link set") {
			return []byte(`Cannot find device "enx-test"`), errors.New("exit status 1")
		}
		return runner.run(ctx, name, args...)
	}
	hostCommandOutput, session.runner = run, run
	t.Cleanup(func() { hostCommandOutput = original })
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("cleanup of removed interface: %v", err)
	}
	if !session.cleanupComplete || len(session.ownedAddresses) != 0 {
		t.Fatal("removed interface kept stale cleanup ownership")
	}
}
