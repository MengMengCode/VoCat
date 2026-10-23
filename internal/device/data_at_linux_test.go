//go:build linux

package device

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type recordedHostCommand struct {
	name string
	args []string
}

// fakeHostRunner records host network commands so tests can assert which
// protected routes were installed or removed without touching the real system.
type fakeHostRunner struct {
	mu       sync.Mutex
	commands []recordedHostCommand
}

func (runner *fakeHostRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.commands = append(runner.commands, recordedHostCommand{
		name: name,
		args: append([]string(nil), args...),
	})
	return nil, nil
}

func (runner *fakeHostRunner) commandLines() []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	lines := make([]string, 0, len(runner.commands))
	for _, command := range runner.commands {
		lines = append(lines, strings.Join(append([]string{command.name}, command.args...), " "))
	}
	return lines
}

type fakeHostProcess struct {
	mu      sync.Mutex
	stopped int
	waitCh  chan struct{}
	once    sync.Once
}

func newFakeHostProcess() *fakeHostProcess {
	return &fakeHostProcess{waitCh: make(chan struct{})}
}

func (process *fakeHostProcess) Wait() error {
	<-process.waitCh
	return nil
}

func (process *fakeHostProcess) Stop(context.Context) error {
	process.mu.Lock()
	process.stopped++
	process.mu.Unlock()
	process.once.Do(func() { close(process.waitCh) })
	return nil
}

func (process *fakeHostProcess) stopCount() int {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.stopped
}

func newTestATHostSession(t *testing.T, networkInterface string) (*linuxATHostDataSession, *fakeHostRunner, *fakeHostProcess) {
	t.Helper()
	runner := &fakeHostRunner{}
	originalRunner := hostCommandOutput
	originalDNSDir := exportProxyDNSDir
	hostCommandOutput = runner.run
	exportProxyDNSDir = t.TempDir()
	t.Cleanup(func() {
		hostCommandOutput = originalRunner
		exportProxyDNSDir = originalDNSDir
	})
	process := newFakeHostProcess()
	session := &linuxATHostDataSession{
		networkInterface: networkInterface,
		ipCommand:        "/sbin/ip",
		runner:           runner.run,
		process:          process,
		firstLease:       make(chan struct{}),
		done:             make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = session.Close(context.Background())
	})
	return session, runner, process
}

func TestATHostDataSessionRenewsLeaseAndUpdatesProtectedRoute(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	session.handleEventLines(strings.NewReader(
		"event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))

	connected, detail, err := session.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after bound: %v", err)
	}
	if !connected || !strings.Contains(detail, "192.0.2.10") {
		t.Fatalf("Status after bound = %v, %q", connected, detail)
	}

	mark, table, priority := exportProxyRouteIdentity("enx-test")
	boundLines := runner.commandLines()
	joined := strings.Join(boundLines, "\n")
	for _, want := range []string{
		"/sbin/ip -4 addr replace 192.0.2.10/24 dev enx-test",
		"/sbin/ip -4 route replace table " + strconv.Itoa(table) + " 192.0.2.0/24 dev enx-test scope link src 192.0.2.10",
		"/sbin/ip -4 route replace table " + strconv.Itoa(table) + " default via 192.0.2.1 dev enx-test onlink",
		"/sbin/ip rule add priority " + strconv.Itoa(priority) + " fwmark 0x" + fmt.Sprintf("%x", mark) + " lookup " + strconv.Itoa(table),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bound host commands missing %q:\n%s", want, joined)
		}
	}
	assertNoMainTableRoutes(t, boundLines)

	// A renewal with a fresh lease must refresh the same protected generation.
	session.handleEventLines(strings.NewReader(
		"event=renew\nip=192.0.2.20\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))
	renewLines := runner.commandLines()
	renewJoined := strings.Join(renewLines, "\n")
	if !strings.Contains(renewJoined, "/sbin/ip -4 addr replace 192.0.2.20/24 dev enx-test") {
		t.Fatalf("renew did not refresh the address:\n%s", renewJoined)
	}
	if !strings.Contains(renewJoined, "/sbin/ip -4 route replace table "+strconv.Itoa(table)+" default via 192.0.2.1 dev enx-test onlink") {
		t.Fatalf("renew did not refresh the protected default route:\n%s", renewJoined)
	}
	assertNoMainTableRoutes(t, renewLines)
}

func TestATHostDataSessionCloseRemovesOnlyVocatRoutes(t *testing.T) {
	session, runner, process := newTestATHostSession(t, "enx-test")
	session.handleEventLines(strings.NewReader(
		"event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))

	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if process.stopCount() != 1 {
		t.Fatalf("process stops = %d, want 1", process.stopCount())
	}

	_, table, priority := exportProxyRouteIdentity("enx-test")
	joined := strings.Join(runner.commandLines(), "\n")
	for _, want := range []string{
		"/sbin/ip rule del priority " + strconv.Itoa(priority),
		"/sbin/ip -4 route flush table " + strconv.Itoa(table),
		"/sbin/ip -4 addr flush dev enx-test scope global",
		"/sbin/ip link set dev enx-test down",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("close host commands missing %q:\n%s", want, joined)
		}
	}
	assertNoMainTableRoutes(t, runner.commandLines())

	connected, _, err := session.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after close: %v", err)
	}
	if connected {
		t.Fatal("Status after close reports connected")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if process.stopCount() != 1 {
		t.Fatalf("process stops after second Close = %d, want 1", process.stopCount())
	}
}

// assertNoMainTableRoutes fails when any recorded routing command would touch
// the main routing table instead of the protected per-interface table.
func assertNoMainTableRoutes(t *testing.T, lines []string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(line, " route ") {
			continue
		}
		if strings.Contains(line, " route replace ") || strings.Contains(line, " route add ") {
			if !strings.Contains(line, " table ") {
				t.Fatalf("routing command installs into a shared table: %q", line)
			}
		}
		if strings.Contains(line, " route flush ") && !strings.Contains(line, " table ") {
			t.Fatalf("routing command flushes a shared table: %q", line)
		}
		if strings.Contains(line, " route del ") && !strings.Contains(line, " table ") &&
			!strings.Contains(line, " dev ") {
			t.Fatalf("routing command deletes from a shared table: %q", line)
		}
	}
}

func TestATHostDataSessionDeconfigDropsLeaseAndRoutes(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	session.handleEventLines(strings.NewReader(
		"event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))
	session.handleEventLines(strings.NewReader("event=deconfig\n\n"))

	connected, _, err := session.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after deconfig: %v", err)
	}
	if connected {
		t.Fatal("Status after deconfig reports connected")
	}
	_, table, _ := exportProxyRouteIdentity("enx-test")
	joined := strings.Join(runner.commandLines(), "\n")
	if !strings.Contains(joined, "/sbin/ip -4 route flush table "+strconv.Itoa(table)) {
		t.Fatalf("deconfig did not flush the protected table:\n%s", joined)
	}
	assertNoMainTableRoutes(t, runner.commandLines())
}

func TestLinuxATHostDataLinkStartRejectsEmptyInterface(t *testing.T) {
	link := &linuxATHostDataLink{}
	_, err := link.Start(context.Background(), "  ")
	if !errors.Is(err, ErrDataBackendUnavailable) {
		t.Fatalf("Start error = %v, want ErrDataBackendUnavailable", err)
	}
}
