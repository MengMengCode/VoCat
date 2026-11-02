//go:build linux

package device

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run explicitly with unshare --net and VOCAT_TEST_AT_NETNS=1. These tests
// execute real iproute2/DHCP commands and refuse the initial network namespace.
func requireATNetworkNamespace(t *testing.T) string {
	t.Helper()
	if os.Getenv("VOCAT_TEST_AT_NETNS") != "1" {
		t.Skip("requires an isolated network namespace")
	}
	self, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.Readlink("/proc/1/ns/net")
	if err != nil || self == initial {
		t.Fatal("refusing network smoke test in the initial namespace")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Fatal(err)
	}
	originalDNSDir := exportProxyDNSDir
	exportProxyDNSDir = t.TempDir()
	t.Cleanup(func() { exportProxyDNSDir = originalDNSDir })
	return ip
}

func netnsIP(t *testing.T, command string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestATNetworkNamespaceLeaseLifecycle(t *testing.T) {
	ip := requireATNetworkNamespace(t)
	const iface = "vocat-at-test0"
	netnsIP(t, ip, "link", "add", iface, "type", "dummy")
	t.Cleanup(func() { _ = exec.Command(ip, "link", "del", iface).Run() })
	netnsIP(t, ip, "link", "set", "dev", iface, "up")
	// An unrelated address and rule must survive renewals and teardown.
	netnsIP(t, ip, "-4", "addr", "add", "198.18.0.2/24", "dev", iface)
	mark, table, priority := exportProxyRouteIdentity(iface)
	netnsIP(t, ip, "rule", "add", "priority", strconv.Itoa(priority), "from", "198.51.100.0/24", "lookup", "main")
	t.Cleanup(func() {
		_ = exec.Command(ip, "rule", "del", "priority", strconv.Itoa(priority), "from", "198.51.100.0/24", "lookup", "main").Run()
	})
	mainBefore := netnsIP(t, ip, "-4", "route", "show", "table", "main")
	session := &linuxATHostDataSession{
		networkInterface: iface, ipCommand: ip, runner: hostCommandOutput,
		firstLease: make(chan struct{}), done: make(chan struct{}),
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	values := map[string]string{"event": "bound", "ip": "192.0.2.10", "subnet": "255.255.255.0", "router": "192.0.2.1", "dns": "192.0.2.53"}
	session.handleLeaseEvent(values)
	if connected, _, err := session.Status(context.Background()); err != nil || !connected {
		t.Fatalf("bound lease: connected=%t err=%v", connected, err)
	}
	values["event"], values["ip"] = "renew", "192.0.2.20"
	session.handleLeaseEvent(values)
	if connected, _, err := session.Status(context.Background()); err != nil || !connected {
		t.Fatalf("renewed lease: connected=%t err=%v", connected, err)
	}
	addresses := netnsIP(t, ip, "-4", "addr", "show", "dev", iface)
	if strings.Contains(addresses, "192.0.2.10/") || !strings.Contains(addresses, "192.0.2.20/24") || !strings.Contains(addresses, "198.18.0.2/24") {
		t.Fatalf("incorrect address ownership after renew: %s", addresses)
	}
	if mainAfter := netnsIP(t, ip, "-4", "route", "show", "table", "main"); mainAfter != mainBefore {
		t.Fatalf("lease changed the main table: before=%q after=%q", mainBefore, mainAfter)
	}
	rules := netnsIP(t, ip, "-N", "rule", "show")
	if !strings.Contains(rules, "from 198.51.100.0/24") || !exportProxyRuleMatches(rules, priority, fmt.Sprintf("0x%x", mark), table) {
		t.Fatalf("missing foreign or protected rule: %s", rules)
	}
	route := netnsIP(t, ip, "-4", "route", "get", "203.0.113.1", "mark", fmt.Sprintf("0x%x", mark))
	if !strings.Contains(route, "dev "+iface) || !strings.Contains(route, "src 192.0.2.20") {
		t.Fatalf("marked traffic uses the wrong lease: %s", route)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Close(canceled); err != nil {
		t.Fatal(err)
	}
	addresses = netnsIP(t, ip, "-4", "addr", "show", "dev", iface)
	rules = netnsIP(t, ip, "-N", "rule", "show")
	if strings.Contains(addresses, "192.0.2.") || !strings.Contains(addresses, "198.18.0.2/24") ||
		!strings.Contains(rules, "from 198.51.100.0/24") || exportProxyRuleMatches(rules, priority, fmt.Sprintf("0x%x", mark), table) {
		t.Fatalf("canceled Close left owned resources or removed foreign ones: addresses=%s rules=%s", addresses, rules)
	}
}

func TestATNetworkNamespaceDHCPStartClose(t *testing.T) {
	ip := requireATNetworkNamespace(t)
	busybox, err := exec.LookPath("busybox")
	if err != nil {
		t.Fatal(err)
	}
	applets, err := exec.Command(busybox, "--list").Output()
	if err != nil || !strings.Contains("\n"+string(applets), "\nudhcpd\n") {
		t.Skip("BusyBox udhcpd is required for the isolated DHCP server")
	}
	const clientInterface, serverInterface = "vocat-dhcp0", "vocat-dhcp1"
	netnsIP(t, ip, "link", "add", clientInterface, "type", "veth", "peer", "name", serverInterface)
	t.Cleanup(func() { _ = exec.Command(ip, "link", "del", clientInterface).Run() })
	for _, iface := range []string{clientInterface, serverInterface} {
		netnsIP(t, ip, "link", "set", "dev", iface, "up")
	}
	netnsIP(t, ip, "-4", "addr", "add", "192.0.2.1/24", "dev", serverInterface)
	dir := t.TempDir()
	config := filepath.Join(dir, "udhcpd.conf")
	// The server shares this isolated namespace. Advertise a distinct gateway:
	// Linux correctly rejects a nexthop assigned locally to the server's veth.
	text := fmt.Sprintf("start 192.0.2.10\nend 192.0.2.20\ninterface %s\npidfile %s\nlease_file %s\noption subnet 255.255.255.0\noption router 192.0.2.254\noption dns 192.0.2.1\noption lease 120\n", serverInterface, filepath.Join(dir, "server.pid"), filepath.Join(dir, "leases"))
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	server := exec.Command(busybox, "udhcpd", "-f", config)
	serverLogPath := filepath.Join(dir, "server.log")
	serverLog, err := os.Create(serverLogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer serverLog.Close()
	server.Stdout, server.Stderr = serverLog, serverLog
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Process.Kill(); _ = server.Wait() })
	// Keep client diagnostics outside the session directory, which failed
	// startup correctly removes during rollback.
	clientLogPath := filepath.Join(dir, "client.log")
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	wrapper := fmt.Sprintf("#!/bin/sh\nexec %q \"$@\" >> %q 2>&1\n", busybox, clientLogPath)
	if err := os.WriteFile(filepath.Join(bin, "busybox"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := (&linuxATHostDataLink{}).Start(ctx, clientInterface)
	if session != nil {
		t.Cleanup(func() { _ = session.Close(context.Background()) })
	}
	if err != nil {
		if output, readErr := os.ReadFile(serverLogPath); readErr == nil {
			t.Logf("DHCP server: %s", output)
		}
		if output, readErr := os.ReadFile(clientLogPath); readErr == nil {
			t.Logf("DHCP client: %s", output)
		}
		t.Fatalf("real udhcpc setup: %v", err)
	}
	if connected, detail, err := session.Status(ctx); err != nil || !connected {
		t.Fatalf("DHCP lease status: connected=%t detail=%s err=%v", connected, detail, err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if addresses := netnsIP(t, ip, "-4", "addr", "show", "dev", clientInterface); strings.Contains(addresses, "inet ") {
		t.Fatalf("DHCP address remains after Close: %s", addresses)
	}
}
