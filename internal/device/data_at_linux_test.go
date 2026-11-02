//go:build linux

package device

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"vocat/internal/modem"
)

type recordedHostCommand struct {
	name string
	args []string
}

// fakeHostRunner records host network commands so tests can assert which
// protected routes were installed or removed without touching the real system.
type fakeHostRunner struct {
	mu sync.Mutex
	// failNext makes the next command fail, so a test can drive the error
	// path of the route installation.
	failNext bool
	// outputs maps a command prefix to the output the fake command prints,
	// so a test can describe the host routing state a command observes.
	outputs  map[string]string
	commands []recordedHostCommand
}

func (runner *fakeHostRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.commands = append(runner.commands, recordedHostCommand{
		name: name,
		args: append([]string(nil), args...),
	})
	if runner.failNext {
		runner.failNext = false
		return nil, errors.New("fake host command failed")
	}
	joined := strings.Join(append([]string{name}, args...), " ")
	if len(args) >= 2 && args[0] == "rule" && args[1] == "add" {
		fields := make(map[string]string)
		for i := 2; i+1 < len(args); i += 2 {
			fields[args[i]] = args[i+1]
		}
		if runner.outputs == nil {
			runner.outputs = make(map[string]string)
		}
		runner.outputs[name+" -N rule show"] += fmt.Sprintf("%s: from all fwmark %s lookup %s\n", fields["priority"], fields["fwmark"], fields["lookup"])
	}
	for prefix, output := range runner.outputs {
		if strings.HasPrefix(joined, prefix) {
			return []byte(output), nil
		}
	}
	return nil, nil
}

func (runner *fakeHostRunner) failNextCommand() {
	runner.mu.Lock()
	runner.failNext = true
	runner.mu.Unlock()
}

func (runner *fakeHostRunner) setOutput(prefix, output string) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.outputs == nil {
		runner.outputs = make(map[string]string)
	}
	runner.outputs[prefix] = output
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
	originalLookPathIP := lookPathIP
	hostCommandOutput = runner.run
	exportProxyDNSDir = t.TempDir()
	// The recorded command stream must not depend on where iproute2 is
	// installed on the test host, and a host without iproute2 must still
	// produce the cleanup commands.
	lookPathIP = func(string) (string, error) { return "/sbin/ip", nil }
	t.Cleanup(func() {
		hostCommandOutput = originalRunner
		exportProxyDNSDir = originalDNSDir
		lookPathIP = originalLookPathIP
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
	if !strings.Contains(renewJoined, "/sbin/ip -4 addr del 192.0.2.10/24 dev enx-test") {
		t.Fatalf("renew retained the previous owned address:\n%s", renewJoined)
	}
	if strings.Contains(renewJoined, "addr flush") {
		t.Fatal("renew flushed unrelated addresses")
	}
	assertNoMainTableRoutes(t, renewLines)
}

// A renewal must not delete and re-add the policy rule. The mark, table and
// priority stay stable for an interface, so the gap between the two
// commands would drop the marked traffic out of the protected table and into
// the host main table.
func TestATHostDataSessionRenewKeepsPolicyRuleInPlace(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	mark, table, priority := exportProxyRouteIdentity("enx-test")
	runner.setOutput("/sbin/ip -N rule show", fmt.Sprintf(
		"0:\tfrom all lookup local\n%d:\tfrom all fwmark 0x%x lookup %d\n",
		priority, mark, table,
	))

	session.handleEventLines(strings.NewReader(
		"event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))

	joined := strings.Join(runner.commandLines(), "\n")
	for _, unwanted := range []string{
		"/sbin/ip rule del priority " + strconv.Itoa(priority),
		"/sbin/ip rule add priority " + strconv.Itoa(priority),
	} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("the policy rule was reinstalled:\n%s", joined)
		}
	}
	if !strings.Contains(joined, "/sbin/ip -4 route replace table "+strconv.Itoa(table)+" 192.0.2.0/24 dev enx-test scope link src 192.0.2.10") {
		t.Fatalf("the protected route was not installed:\n%s", joined)
	}
}

// A lease that changes its prefix leaves the previous connected route
// behind. The stale route must be dropped, while the policy rule stays.
func TestATHostDataSessionRenewDropsStaleConnectedRoute(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	mark, table, priority := exportProxyRouteIdentity("enx-test")
	runner.setOutput("/sbin/ip -N rule show", fmt.Sprintf(
		"0:\tfrom all lookup local\n%d:\tfrom all fwmark 0x%x lookup %d\n",
		priority, mark, table,
	))
	runner.setOutput("/sbin/ip -4 route show table "+strconv.Itoa(table),
		"192.0.2.0/24 dev enx-test scope link src 192.0.2.10\n")

	session.handleEventLines(strings.NewReader(
		"event=renew\nip=198.51.100.20\nsubnet=255.255.255.0\nrouter=198.51.100.1\ndns=192.0.2.53\n\n",
	))

	joined := strings.Join(runner.commandLines(), "\n")
	if !strings.Contains(joined, "/sbin/ip -4 route del table "+strconv.Itoa(table)+" 192.0.2.0/24") {
		t.Fatalf("the stale connected route was not dropped:\n%s", joined)
	}
	if !strings.Contains(joined, "/sbin/ip -4 route replace table "+strconv.Itoa(table)+" 198.51.100.0/24 dev enx-test scope link src 198.51.100.20") {
		t.Fatalf("the new connected route was not installed:\n%s", joined)
	}
	if strings.Contains(joined, "/sbin/ip rule del priority "+strconv.Itoa(priority)) {
		t.Fatalf("the policy rule was removed during the renewal:\n%s", joined)
	}
}

// Policy priorities are not exclusive ownership: another rule at the same
// priority must be preserved while VoCat installs its complete selector.
func TestATHostDataSessionRenewPreservesForeignPolicyRule(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	mark, table, priority := exportProxyRouteIdentity("enx-test")
	runner.setOutput("/sbin/ip -N rule show", fmt.Sprintf("%d: from all fwmark 0x1 lookup %d\n", priority, table+1))
	session.handleEventLines(strings.NewReader("event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n"))
	joined := strings.Join(runner.commandLines(), "\n")
	if strings.Contains(joined, "/sbin/ip rule del priority "+strconv.Itoa(priority)) {
		t.Fatalf("the foreign policy rule was removed:\n%s", joined)
	}
	if !strings.Contains(joined, "/sbin/ip rule add priority "+strconv.Itoa(priority)+" fwmark 0x"+fmt.Sprintf("%x", mark)+" lookup "+strconv.Itoa(table)) {
		t.Fatalf("the owned rule was not installed:\n%s", joined)
	}
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
		"/sbin/ip -4 addr del 192.0.2.10/24 dev enx-test",
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

// A failed renew tears down the protected routes and rule before it
// returns, so the previous lease no longer describes the host path. Keeping
// it would let Status report "connected" from a stale lease and the data
// monitor would never recover the session.
func TestATHostDataSessionFailedRenewDropsStaleLease(t *testing.T) {
	session, runner, _ := newTestATHostSession(t, "enx-test")
	session.handleEventLines(strings.NewReader(
		"event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))

	connected, _, err := session.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after bound: %v", err)
	}
	if !connected {
		t.Fatal("Status after bound reports disconnected")
	}

	// The route installation fails, so the lease must not survive it.
	runner.failNextCommand()
	session.handleEventLines(strings.NewReader(
		"event=renew\nip=192.0.2.11\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))

	connected, detail, err := session.Status(context.Background())
	if connected {
		t.Fatal("Status after failed renew still reports connected")
	}
	if err == nil {
		t.Fatal("Status after failed renew reports no error")
	}
	if detail == "" {
		t.Fatal("Status after failed renew reports no detail")
	}
}

// udhcpc without -n retries discovery indefinitely, so the first-lease wait
// must be bounded even when the caller's context has no deadline. The bound
// lives in awaitFirstLease, not in the caller.
func TestATHostDataSessionFirstLeaseWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, _, _ := newTestATHostSession(t, "enx-test")
		session.firstLeaseTimeout = 20 * time.Millisecond
		started := time.Now()
		err := session.awaitFirstLease(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("awaitFirstLease error = %v, want internal deadline", err)
		}
		if elapsed := time.Since(started); elapsed != session.firstLeaseTimeout {
			t.Fatalf("first lease wait = %s, want %s", elapsed, session.firstLeaseTimeout)
		}
	})
}

// Every failure path of the first-lease wait must close the session: a lost
// lease or an exited DHCP process must not leave the process and its temp
// directory behind.
func TestATHostDataSessionFirstLeaseFailureClosesSession(t *testing.T) {
	session, _, process := newTestATHostSession(t, "enx-test")
	// The DHCP process exits before a lease arrives.
	close(session.done)
	if err := session.awaitFirstLease(context.Background()); err == nil {
		t.Fatal("awaitFirstLease returned no error for an exited DHCP process")
	}
	if !session.closed {
		t.Fatal("session stayed open after the first-lease wait failed")
	}
	if process.stopCount() == 0 {
		t.Fatal("the DHCP process was not stopped after the first-lease wait failed")
	}
}

func TestNetworkStatusReportsATHostSession(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err != nil {
		t.Fatalf("enable network: %v", err)
	}

	status, err := manager.NetworkStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("NetworkStatus: %v", err)
	}
	if status.Backend != "at" || !status.Connected || status.Interface != "enx344b50000000" {
		t.Fatalf("NetworkStatus = %#v, want connected AT session", status)
	}

	session := link.lastSession()
	if session == nil {
		t.Fatal("no host session recorded")
	}
	session.mu.Lock()
	session.statusOK = false
	session.statusDetail = "AT host data lease is not established"
	session.mu.Unlock()

	status, err = manager.NetworkStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("NetworkStatus disconnected: %v", err)
	}
	if status.Connected {
		t.Fatalf("NetworkStatus = %#v, want disconnected after lease loss", status)
	}
	if status.Backend != "at" {
		t.Fatalf("NetworkStatus backend = %q, want at", status.Backend)
	}
}

func TestATDataSessionClosesOnDeviceDisconnect(t *testing.T) {
	discoverer := &mutableDiscoverer{}
	discoverer.setCandidates(testATDataCandidate())
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	manager, err := NewManager(Options{
		Discoverer:     discoverer,
		Opener:         &staticOpener{client: client},
		CommandTimeout: time.Second,
		LongTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	link := &fakeATHostLink{}
	manager.atHostDataLink = link
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	id := testATDataCandidate().ID
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err != nil {
		t.Fatalf("enable network: %v", err)
	}
	session := link.lastSession()
	if session == nil || session.isClosed() {
		t.Fatal("no live host session after enable")
	}

	// The modem disappears: Discover now returns an empty device set, so the
	// stale-device cleanup must tear the host session down too.
	discoverer.setCandidates()
	if _, err := manager.Discover(context.Background()); err != nil {
		t.Fatalf("Discover after unplug: %v", err)
	}
	if !session.isClosed() {
		t.Fatal("device disconnect did not close the AT host session")
	}
	status, err := manager.NetworkStatus(context.Background(), id)
	if err == nil && status.Connected {
		t.Fatalf("NetworkStatus after disconnect = %#v, want no live session", status)
	}
}

func TestNetworkStatusReturnsOperationInProgressDuringATTransition(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, client, link)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err != nil {
		t.Fatalf("enable network: %v", err)
	}

	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	// A live lease teardown holds dataMu; concurrent status probes must fail
	// fast as in-progress instead of observing a half-cleared state.
	state.dataMu.Lock()
	_, statusErr := manager.NetworkStatus(context.Background(), id)
	state.dataMu.Unlock()
	if !errors.Is(statusErr, ErrDataOperationInProgress) {
		t.Fatalf("NetworkStatus error = %v, want ErrDataOperationInProgress", statusErr)
	}
}

// Shutdown must not depend on a DHCP writer ever opening the FIFO. The reader
// used to park in open(2) when it lost the race with the writer, so Close hung
// forever in wg.Wait() while holding the device operation lock.
func TestATHostDataSessionReadEventsStopsWithoutWriter(t *testing.T) {
	eventPath := filepath.Join(t.TempDir(), "events")
	if err := syscall.Mkfifo(eventPath, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &linuxATHostDataSession{
		eventPath:  eventPath,
		background: ctx,
		firstLease: make(chan struct{}),
		done:       make(chan struct{}),
	}
	finished := make(chan struct{})
	session.wg.Add(1)
	go func() {
		session.readEvents()
		close(finished)
	}()

	// Give the reader time to reach the point where the old code blocked in
	// open(2) with no writer in sight, then cancel the session.
	time.Sleep(atHostEventPollInterval * 2)
	cancel()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("readEvents did not stop after the session context was cancelled")
	}
}

// The non-blocking reader must still parse hook records written to the FIFO.
func TestATHostDataSessionReadEventsAppliesFIFORecord(t *testing.T) {
	eventPath := filepath.Join(t.TempDir(), "events")
	if err := syscall.Mkfifo(eventPath, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	// Hold the FIFO open read-write so the writer's open(2) never blocks
	// regardless of how far the session reader has progressed.
	anchor, err := os.OpenFile(eventPath, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("anchor FIFO: %v", err)
	}
	defer func() { _ = anchor.Close() }()

	session, _, _ := newTestATHostSession(t, "enx-test")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	session.eventPath = eventPath
	session.background = ctx
	session.wg.Add(1)
	go session.readEvents()

	writer, err := os.OpenFile(eventPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open event writer: %v", err)
	}
	if _, err := writer.WriteString(
		"event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	); err != nil {
		t.Fatalf("write event: %v", err)
	}
	_ = writer.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		connected, _, statusErr := session.Status(context.Background())
		if statusErr == nil && connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("FIFO bound event never applied: connected=%v err=%v", connected, statusErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A bound event that arrives after the DHCP process-exit cleanup must not
// rebuild the routes and lease. Lease events and process-exit cleanup are
// serialized by eventMu/eventsStopped.
func TestATHostDataSessionIgnoresLeaseEventAfterProcessExit(t *testing.T) {
	session, runner, process := newTestATHostSession(t, "enx-test")
	session.handleEventLines(strings.NewReader(
		"event=bound\nip=192.0.2.10\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))
	if connected, _, _ := session.Status(context.Background()); !connected {
		t.Fatal("session did not establish the initial lease")
	}

	// The DHCP process exits; watchProcess tears the lease down.
	session.wg.Add(1)
	go session.watchProcess()
	if err := process.Stop(context.Background()); err != nil {
		t.Fatalf("stop process: %v", err)
	}
	select {
	case <-session.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchProcess did not finish after the process exited")
	}

	mark := len(runner.commandLines())
	// A delayed bound event is now ignored.
	session.handleEventLines(strings.NewReader(
		"event=bound\nip=192.0.2.20\nsubnet=255.255.255.0\nrouter=192.0.2.1\ndns=192.0.2.53\n\n",
	))
	if connected, _, _ := session.Status(context.Background()); connected {
		t.Fatal("a lease event after process exit resurrected the session")
	}
	if lines := runner.commandLines(); len(lines) != mark {
		t.Fatalf("a lease event after process exit issued host commands:\n%s", strings.Join(lines[mark:], "\n"))
	}
}

// Once a failed replacement setup clears the AT session, an AT-only device
// must keep reporting the intended AT backend as disconnected. Falling
// through to QMI would return ErrDataBackendUnavailable, which the server
// monitor skips, so the watchdog would never retry the recovery.
func TestNetworkStatusATOnlyDeviceReportsDisconnectedAfterFailedEnable(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
		{command: "AT+CGACT=0,1", response: okResponse()},
	}}
	link := &fakeATHostLink{err: errors.New("host lease unavailable")}
	manager, id := newStartedATDataTestManager(t, client, link)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err == nil {
		t.Fatal("enable error = nil, want host lease failure")
	}

	status, err := manager.NetworkStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("NetworkStatus after failed enable: %v", err)
	}
	if status.Backend != "at" {
		t.Fatalf("NetworkStatus backend = %q, want at", status.Backend)
	}
	if status.Connected {
		t.Fatalf("NetworkStatus = %#v, want disconnected after failed enable", status)
	}
	client.assertDone(t)
}

// Switching to the QMI backend must tear the AT host session down first.
// Both backends own the same host interface: a surviving udhcpc would keep
// reapplying its lease, and its later deconfig would flush the address and
// routes the QMI session owns.
func TestSetNetworkQMIClosesResidualATHostSession(t *testing.T) {
	candidate := testATDataCandidate()
	candidate.QMIControl = "/dev/cdc-wdm-vocat-test"
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	link := &fakeATHostLink{}
	manager, err := NewManager(Options{
		Discoverer:     staticDiscoverer{candidates: []modem.Candidate{candidate}},
		Opener:         &staticOpener{client: client},
		CommandTimeout: time.Second,
		LongTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	manager.atHostDataLink = link
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	id := candidate.ID
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6", Backend: "at",
	}); err != nil {
		t.Fatalf("enable network: %v", err)
	}
	session := link.lastSession()
	if session == nil || session.isClosed() {
		t.Fatal("no live AT host session after enable")
	}

	// The QMI path runs real iproute2 commands, so the lookup fails and the
	// QMI transaction stops before it touches the host. The AT session must
	// already be gone: the switch is fail-closed regardless of the outcome.
	originalLookPathIP := lookPathIP
	lookPathIP = func(string) (string, error) {
		return "", errors.New("iproute2 is not installed")
	}
	t.Cleanup(func() { lookPathIP = originalLookPathIP })

	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6", Backend: "qmi",
	}); err == nil {
		t.Fatal("QMI enable error = nil, want an unavailable backend")
	}
	if !session.isClosed() {
		t.Fatal("switching to QMI left the AT host session running")
	}

	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.dataMu.Lock()
	defer state.dataMu.Unlock()
	if state.atHostSession != nil {
		t.Fatal("switching to QMI left the AT host session referenced")
	}
	if state.dataBackend == "at" {
		t.Fatal("switching to QMI left the AT backend marker in place")
	}
}

// countOpenDescriptorsFor counts the descriptors this process holds on a
// path. exec.Cmd.Wait does not close a file the caller supplied, so a
// long-running daemon would collect one descriptor per session.
func countOpenDescriptorsFor(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot inspect the open descriptors: %v", err)
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue
		}
		if target == path {
			count++
		}
	}
	return count
}

// The udhcpc log descriptor belongs to the child process. The parent must
// release its copy, otherwise every recovery re-enable leaks one.
func TestStartForegroundUDHCPCDoesNotKeepLogDescriptor(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "udhcpc.log")
	process, err := startForegroundUDHCPC(
		context.Background(), "/bin/sh", "/dev/null", dir, "enx-test",
	)
	if err != nil {
		t.Fatalf("start udhcpc: %v", err)
	}
	t.Cleanup(func() { _ = process.Stop(context.Background()) })
	if open := countOpenDescriptorsFor(t, logPath); open != 0 {
		t.Fatalf("the parent keeps %d descriptors open on %s", open, logPath)
	}
}

// A failed start must release the log descriptor too.
func TestStartForegroundUDHCPCClosesLogDescriptorOnStartFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "udhcpc.log")
	if _, err := startForegroundUDHCPC(
		context.Background(), filepath.Join(dir, "missing-busybox"), "/dev/null", dir, "enx-test",
	); err == nil {
		t.Fatal("start udhcpc error = nil, want a startup failure")
	}
	if open := countOpenDescriptorsFor(t, logPath); open != 0 {
		t.Fatalf("the parent keeps %d descriptors open on %s", open, logPath)
	}
}
