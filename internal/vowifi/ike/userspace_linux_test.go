//go:build linux

package ike

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateUserspaceRoutesAllowsOnlyNegotiatedEndpoints(t *testing.T) {
	t.Parallel()
	config := userspaceRouteTestConfig()
	if err := validateUserspaceRoutes(config); err != nil {
		t.Fatalf("valid negotiated route: %v", err)
	}

	config.PCSCF = []net.IP{net.IPv4(203, 0, 113, 10)}
	if err := validateUserspaceRoutes(config); err == nil {
		t.Fatal("P-CSCF outside responder selector was accepted")
	}
}

func TestValidateUserspaceRoutesRequiresMatchingAddressFamily(t *testing.T) {
	t.Parallel()
	config := userspaceRouteTestConfig()
	config.PCSCF = []net.IP{net.ParseIP("2001:db8::20")}
	config.ResponderSelectors = []trafficSelector{{
		StartPort: 0,
		EndPort:   65535,
		StartIP:   net.ParseIP("2001:db8::"),
		EndIP:     net.ParseIP("2001:db8::ffff"),
	}}
	if err := validateUserspaceRoutes(config); err == nil {
		t.Fatal("IPv6 P-CSCF without an assigned inner IPv6 address was accepted")
	}
}

func TestResponderSelectorRoutePrefixesCoverDynamicMediaWithoutBroadening(t *testing.T) {
	t.Parallel()
	selectors := []trafficSelector{
		{
			StartPort: 0,
			EndPort:   65535,
			StartIP:   net.IPv4(10, 64, 0, 0),
			EndIP:     net.IPv4(10, 127, 255, 255),
		},
		{
			StartPort: 49152,
			EndPort:   65535,
			StartIP:   net.IPv4(192, 0, 2, 17),
			EndIP:     net.IPv4(192, 0, 2, 23),
		},
	}
	prefixes, err := selectorRoutePrefixes(selectors, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []net.IP{
		net.IPv4(10, 88, 9, 7), // dynamically negotiated RTP endpoint
		net.IPv4(192, 0, 2, 17),
		net.IPv4(192, 0, 2, 23),
	} {
		if !routePrefixesContain(prefixes, address) {
			t.Fatalf("negotiated address %s is not covered by routes %v", address, prefixes)
		}
	}
	for _, address := range []net.IP{
		net.IPv4(10, 128, 0, 1),
		net.IPv4(192, 0, 2, 24),
		net.IPv4(198, 51, 100, 1),
	} {
		if routePrefixesContain(prefixes, address) {
			t.Fatalf("address outside negotiated selectors %s was covered by routes %v", address, prefixes)
		}
	}
}

func TestResponderSelectorDefaultRouteRemainsRepresentable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		ipv6       bool
		start, end net.IP
		want       string
	}{
		{name: "IPv4", start: net.IPv4zero, end: net.IPv4bcast, want: "0.0.0.0/0"},
		{name: "IPv6", ipv6: true, start: net.IPv6zero, end: net.IP(strings.Repeat("\xff", 16)), want: "::/0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefixes, err := selectorRoutePrefixes([]trafficSelector{{
				StartPort: 0, EndPort: 65535, StartIP: test.start, EndIP: test.end,
			}}, test.ipv6)
			if err != nil {
				t.Fatal(err)
			}
			if len(prefixes) != 1 || prefixes[0] != test.want {
				t.Fatalf("default selector routes = %v, want [%s]", prefixes, test.want)
			}
		})
	}
	table, priority := userspaceRoutingIdentifiers(0x01020304)
	failTable, failPriority := userspaceFailClosedRoutingIdentifiers(table, priority)
	if failTable == table || failPriority != priority+1 || failPriority >= 32766 {
		t.Fatalf("fail-closed fallback identifiers = %d/%d after %d/%d", failTable, failPriority, table, priority)
	}
}

func routePrefixesContain(prefixes []string, address net.IP) bool {
	for _, value := range prefixes {
		_, prefix, err := net.ParseCIDR(value)
		if err == nil && prefix.Contains(address) {
			return true
		}
	}
	return false
}

func TestUserspaceRulePriorityAlwaysPrecedesMainRoute(t *testing.T) {
	t.Parallel()
	for _, spi := range []uint32{
		0,
		1,
		32767,
		0x01020304,
		0x80000000,
		0xffffffff,
	} {
		table, priority := userspaceRoutingIdentifiers(spi)
		if table <= 255 {
			t.Fatalf("SPI %08x produced reserved routing table %d", spi, table)
		}
		if priority < 10000 || priority >= 30000 || priority >= 32766 {
			t.Fatalf(
				"SPI %08x produced unsafe rule priority %d",
				spi,
				priority,
			)
		}
	}
}

func TestUserspaceRouteSetupAndCleanupStayFailClosedAtEveryStep(t *testing.T) {
	t.Parallel()
	temporary := t.TempDir()
	logPath := filepath.Join(temporary, "ip.log")
	scriptPath := filepath.Join(temporary, "ip")
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"-j\" ]; then echo '[]'; exit 0; fi\n" +
		"if [ \"$2\" = \"rule\" ] && [ \"$3\" = \"show\" ]; then exit 0; fi\n" +
		"printf '%s\\n' \"$*\" >> " + strconv.Quote(logPath) + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := userspaceRouteTestConfig()
	config.Name = "vocat-order-test"
	config.InboundSPI = 0x01020304
	handle := &linuxUserspaceHandle{ipCommand: scriptPath, config: config}
	if err := handle.configure(context.Background()); err != nil {
		t.Fatalf("configure with recording ip command: %v", err)
	}
	if err := handle.cleanupNetwork(context.Background()); err != nil {
		t.Fatalf("cleanup with recording ip command: %v", err)
	}
	encoded, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(encoded)
	table, priority := userspaceRoutingIdentifiers(config.InboundSPI)
	failTable, failPriority := userspaceFailClosedRoutingIdentifiers(table, priority)
	// The link is enabled first because a route naming a down device is
	// rejected on the 410's 5.15 kernel ("Device for nexthop is not up"). The
	// fail-closed invariant is unchanged: both rules are in place before the
	// inner address makes the interface usable.
	assertTextOrder(t, log, []string{
		"link set dev " + config.Name + " mtu 1380 up",
		"route add table " + strconv.FormatUint(uint64(failTable), 10) + " unreachable default",
		"route add table " + strconv.FormatUint(uint64(table), 10) + " 10.0.0.0/8 dev " + config.Name,
		"rule add priority " + strconv.FormatUint(uint64(failPriority), 10),
		"rule add priority " + strconv.FormatUint(uint64(priority), 10),
		"address add 10.132.116.34/32 dev " + config.Name,
	})
	assertTextOrder(t, log, []string{
		"rule delete priority " + strconv.FormatUint(uint64(priority), 10),
		"link set dev " + config.Name + " down",
		"address delete 10.132.116.34/32 dev " + config.Name,
		"route delete table " + strconv.FormatUint(uint64(table), 10) + " 10.0.0.0/8 dev " + config.Name,
		"rule delete priority " + strconv.FormatUint(uint64(failPriority), 10),
		"route delete table " + strconv.FormatUint(uint64(failTable), 10) + " unreachable default",
	})
}

func TestUserspaceCleanupRetainsGuardAfterSafetyBarrierFailure(t *testing.T) {
	t.Parallel()
	temporary := t.TempDir()
	logPath := filepath.Join(temporary, "ip.log")
	scriptPath := filepath.Join(temporary, "ip")
	writeRecorder := func(failLinkDown bool) {
		t.Helper()
		failure := ""
		if failLinkDown {
			failure = "case \"$*\" in *\"link set dev vocat-failure-test down\"*) exit 42;; esac\n"
		}
		script := "#!/bin/sh\n" +
			"if [ \"$2\" = \"-j\" ]; then echo '[]'; exit 0; fi\n" +
			"if [ \"$2\" = \"rule\" ] && [ \"$3\" = \"show\" ]; then exit 0; fi\n" +
			"printf '%s\\n' \"$*\" >> " + strconv.Quote(logPath) + "\n" + failure
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRecorder(true)
	config := userspaceRouteTestConfig()
	config.Name = "vocat-failure-test"
	config.InboundSPI = 0x11223344
	handle := &linuxUserspaceHandle{ipCommand: scriptPath, config: config}
	if err := handle.configure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := handle.cleanupNetwork(context.Background()); err == nil {
		t.Fatal("cleanup succeeded despite injected link-down failure")
	}
	encoded, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(encoded)
	table, priority := userspaceRoutingIdentifiers(config.InboundSPI)
	failTable, failPriority := userspaceFailClosedRoutingIdentifiers(table, priority)
	if strings.Contains(log, "rule delete priority "+strconv.FormatUint(uint64(failPriority), 10)) ||
		strings.Contains(log, "route delete table "+strconv.FormatUint(uint64(failTable), 10)+" unreachable default") {
		t.Fatalf("cleanup removed fail-closed guard after safety failure:\n%s", log)
	}
	writeRecorder(false)
	if err := handle.cleanupNetwork(context.Background()); err != nil {
		t.Fatalf("retry retained cleanup: %v", err)
	}
	encoded, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log = string(encoded)
	if !strings.Contains(log, "rule delete priority "+strconv.FormatUint(uint64(failPriority), 10)) ||
		!strings.Contains(log, "route delete table "+strconv.FormatUint(uint64(failTable), 10)+" unreachable default") {
		t.Fatalf("cleanup retry did not remove retained guard:\n%s", log)
	}
}

func TestLinuxUserspaceConcurrentCloseWaitsAndRetriesRetainedGuard(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	runContext, cancelRun := context.WithCancel(context.Background())
	firstCleanupStarted := make(chan struct{})
	releaseFirstCleanup := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var overlapped atomic.Bool
	invocations := make(chan string, 4)

	handle := &linuxUserspaceHandle{
		tun:        reader,
		runContext: runContext,
		cancel:     cancelRun,
		cleanup: []ipCleanupCommand{
			{
				operation: "disable TUN interface",
				arguments: []string{"link", "set", "dev", "vocat-concurrent-test", "down"},
				phase:     cleanupLinkDown,
			},
			{
				operation: "remove fail-closed fallback rule",
				arguments: []string{"-4", "rule", "delete", "priority", "20001"},
				phase:     cleanupFailRule,
			},
			{
				operation: "remove fail-closed route",
				arguments: []string{"-4", "route", "delete", "table", "40001", "unreachable", "default"},
				phase:     cleanupFailRoute,
			},
		},
	}
	handle.cleanupExecutor = func(ctx context.Context, arguments ...string) ([]byte, error) {
		if active.Add(1) > 1 {
			overlapped.Store(true)
		}
		defer active.Add(-1)
		invocations <- strings.Join(arguments, " ")
		if calls.Add(1) != 1 {
			return nil, nil
		}
		close(firstCleanupStarted)
		select {
		case <-releaseFirstCleanup:
			return []byte("injected link-down failure"), errors.New("exit status 42")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- handle.Close(context.Background())
	}()
	select {
	case <-firstCleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("first Close did not enter cleanup")
	}

	// Calling Close synchronously guarantees the retry is active while the
	// first cleanup is blocked. Its deadline releases the injected failure;
	// the retry must then acquire closeMu, consume the retained guard commands,
	// and close the TUN.
	retryContext, cancelRetry := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelRetry()
	go func() {
		<-retryContext.Done()
		close(releaseFirstCleanup)
	}()
	secondErr := handle.Close(retryContext)
	if secondErr != nil {
		t.Fatalf("second Close did not complete retained cleanup: %v", secondErr)
	}

	select {
	case firstErr := <-firstResult:
		if firstErr == nil || !strings.Contains(firstErr.Error(), "injected link-down failure") {
			t.Fatalf("first Close error = %v, want injected cleanup failure", firstErr)
		}
	case <-time.After(time.Second):
		t.Fatal("first Close remained blocked after cleanup release")
	}
	if overlapped.Load() {
		t.Fatal("concurrent Close calls executed cleanup commands at the same time")
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("cleanup command calls = %d, want initial failure plus three-command retry", got)
	}

	wantInvocations := []string{
		"link set dev vocat-concurrent-test down",
		"link set dev vocat-concurrent-test down",
		"-4 rule delete priority 20001",
		"-4 route delete table 40001 unreachable default",
	}
	for index, want := range wantInvocations {
		select {
		case got := <-invocations:
			if got != want {
				t.Fatalf("cleanup invocation %d = %q, want %q", index, got, want)
			}
		default:
			t.Fatalf("cleanup invocation %d missing, want %q", index, want)
		}
	}
	if len(handle.cleanup) != 0 {
		t.Fatalf("successful retry retained cleanup commands: %+v", handle.cleanup)
	}
	if _, err := reader.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("TUN descriptor remained open after successful retry: %v", err)
	}
}

func assertTextOrder(t *testing.T, text string, fragments []string) {
	t.Helper()
	previous := -1
	for _, fragment := range fragments {
		index := strings.Index(text[previous+1:], fragment)
		if index < 0 {
			t.Fatalf("command log omitted %q:\n%s", fragment, text)
		}
		index += previous + 1
		if index <= previous {
			t.Fatalf("command %q is out of order:\n%s", fragment, text)
		}
		previous = index
	}
}

func userspaceRouteTestConfig() ChildSAConfig {
	return ChildSAConfig{
		InnerLocalIPv4: net.IPv4(10, 132, 116, 34),
		PCSCF:          []net.IP{net.IPv4(10, 127, 192, 82)},
		InitiatorSelectors: []trafficSelector{{
			StartPort: 0,
			EndPort:   65535,
			StartIP:   net.IPv4(10, 132, 116, 34),
			EndIP:     net.IPv4(10, 132, 116, 34),
		}},
		ResponderSelectors: []trafficSelector{{
			StartPort: 0,
			EndPort:   65535,
			StartIP:   net.IPv4(10, 0, 0, 0),
			EndIP:     net.IPv4(10, 255, 255, 255),
		}},
	}
}

type blockingNATTRelay struct{}

func (blockingNATTRelay) SendESP(ctx context.Context, _ []byte) error {
	return ctx.Err()
}

func (blockingNATTRelay) ReceiveESP(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestLinuxUserspaceTUNReaderStopsOnCancel(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	runContext, cancel := context.WithCancel(context.Background())
	handle := &linuxUserspaceHandle{
		tun:        reader,
		runContext: runContext,
		cancel:     cancel,
	}
	handle.wait.Add(1)
	done := make(chan struct{})
	go func() {
		handle.copyTUNToRelay()
		close(done)
	}()
	handle.cancelRun()

	select {
	case <-done:
		_ = reader.Close()
	case <-time.After(time.Second):
		// Release the old blocking implementation before failing the test.
		_ = reader.Close()
		<-done
		t.Fatal("TUN reader did not stop after runtime cancellation")
	}
}

func TestLinuxUserspaceInstallerLifecycle(t *testing.T) {
	if os.Getenv("VOCAT_NETNS_TEST") != "1" {
		t.Skip("set VOCAT_NETNS_TEST=1 inside an isolated Linux network namespace")
	}
	config := userspaceRouteTestConfig()
	config.Name = "vocat-swu-test"
	config.InboundSPI = 0x01020304
	config.OutboundSPI = 0x05060708
	config.Encryption = "aes-cbc-128"
	config.Integrity = "hmac-sha1-96"
	config.InboundEncKey = make([]byte, 16)
	config.InboundAuthKey = make([]byte, 20)
	config.OutboundEncKey = make([]byte, 16)
	config.OutboundAuthKey = make([]byte, 20)
	config.UDPEncapsulation = true
	config.Relay = blockingNATTRelay{}

	tableID, priorityID := userspaceRoutingIdentifiers(config.InboundSPI)
	failTableID, _ := userspaceFailClosedRoutingIdentifiers(tableID, priorityID)
	table := strconv.FormatUint(uint64(tableID), 10)
	failTable := strconv.FormatUint(uint64(failTableID), 10)
	if output, err := exec.Command(
		"ip", "-4", "route", "add",
		"table", table, "unreachable", "default",
	).CombinedOutput(); err != nil {
		t.Fatalf("preoccupy route table: %v: %s", err, output)
	}
	if conflicting, err := (linuxUserspaceInstaller{ipCommand: "ip"}).Install(
		context.Background(),
		config,
	); err == nil {
		_ = conflicting.Close(context.Background())
		t.Fatal("installer accepted a preoccupied routing table")
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "delete",
		"table", table, "unreachable", "default",
	).CombinedOutput(); err != nil {
		t.Fatalf("release preoccupied route table: %v: %s", err, output)
	}

	handle, err := (linuxUserspaceInstaller{ipCommand: "ip"}).Install(
		context.Background(),
		config,
	)
	if err != nil {
		t.Fatalf("install user-space CHILD_SA: %v", err)
	}
	if mode := handle.(DataplaneEvidence).DataplaneMode(); mode != "userspace" {
		t.Fatalf("dataplane mode = %q", mode)
	}
	if output, err := exec.Command("ip", "link", "show", "dev", config.Name).CombinedOutput(); err != nil {
		t.Fatalf("TUN interface was not created: %v: %s", err, output)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "show", "table", table,
	).CombinedOutput(); err != nil ||
		!strings.Contains(string(output), "10.0.0.0/8") {
		t.Fatalf("negotiated selector routes are missing: %v: %s", err, output)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "show", "table", failTable,
	).CombinedOutput(); err != nil || !strings.Contains(string(output), "unreachable default") {
		t.Fatalf("fail-closed fallback route is missing: %v: %s", err, output)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "get", "10.88.9.7",
		"from", "10.132.116.34",
	).CombinedOutput(); err != nil ||
		!strings.Contains(string(output), "dev "+config.Name) ||
		!strings.Contains(string(output), "table "+table) {
		t.Fatalf(
			"dynamic RTP route did not use the TUN: %v: %s",
			err,
			output,
		)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "get", "10.127.192.82",
		"from", "10.132.116.34",
	).CombinedOutput(); err != nil ||
		!strings.Contains(string(output), "dev "+config.Name) ||
		!strings.Contains(string(output), "table "+table) {
		t.Fatalf(
			"P-CSCF route did not win before the main table: %v: %s",
			err,
			output,
		)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "get", "198.51.100.1",
		"from", "10.132.116.34",
	).CombinedOutput(); err == nil {
		t.Fatalf(
			"non-P-CSCF inner traffic escaped the unreachable default: %s",
			output,
		)
	}

	expectedFailure := errors.New("test relay stopped")
	concrete := handle.(*linuxUserspaceHandle)
	concrete.fail(expectedFailure)
	select {
	case failure := <-concrete.Failures():
		if !errors.Is(failure, expectedFailure) {
			t.Fatalf("runtime failure = %v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal failure was not published")
	}

	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handle.Close(closeContext); err != nil {
		t.Fatalf("close user-space CHILD_SA: %v", err)
	}
	if err := exec.Command("ip", "link", "show", "dev", config.Name).Run(); err == nil {
		t.Fatal("TUN interface survived Close")
	}
	output, err := exec.Command("ip", "-4", "rule", "show").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "from 10.132.116.34") {
		t.Fatalf("source rule survived Close: %s", output)
	}
}

var _ NATTPacketRelay = blockingNATTRelay{}
