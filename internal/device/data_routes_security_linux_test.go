//go:build linux

package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExportProxyRuleRequiresExactIdentity(t *testing.T) {
	const priority, table = 24567, 24567
	const mark = "0x56012345"
	tests := []struct {
		name, line string
		want       bool
	}{
		{"normal", "24567: from all fwmark 0x56012345 lookup 24567", true},
		{"full mask", "24567: from all fwmark 0x56012345/0xffffffff lookup 24567", true},
		{"decimal mark", "24567: from all fwmark 1442915141 lookup 24567", true},
		{"numeric metadata", "24567: from all to all fwmark 0x56012345 lookup 24567 protocol 3", true},
		{"table prefix", "24567: from all fwmark 0x56012345 lookup 245670", false},
		{"inverted", "24567: not from all fwmark 0x56012345 lookup 24567", false},
		{"source selector", "24567: from 192.0.2.0/24 fwmark 0x56012345 lookup 24567", false},
		{"destination selector", "24567: from all to 192.0.2.0/24 fwmark 0x56012345 lookup 24567", false},
		{"interface selector", "24567: from all iif eth0 fwmark 0x56012345 lookup 24567", false},
		{"suppressor", "24567: from all fwmark 0x56012345 lookup 24567 suppress_prefixlength 0", false},
		{"partial mask", "24567: from all fwmark 0x56012345/0xffffff lookup 24567", false},
		{"wrong priority", "24568: from all fwmark 0x56012345 lookup 24567", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := exportProxyRuleMatches(test.line, priority, mark, table); got != test.want {
				t.Fatalf("rule matches=%t, want %t: %s", got, test.want, test.line)
			}
		})
	}
}

func TestEnsureExportProxyRulePreservesUnrelatedRules(t *testing.T) {
	const priority, table = 24567, 24567
	const mark uint32 = 0x56012345
	runner := &fakeHostRunner{}
	runner.setOutput("mock-ip -N rule show", "24567: from 192.0.2.0/24 lookup 123\n")
	original := hostCommandOutput
	hostCommandOutput = runner.run
	t.Cleanup(func() { hostCommandOutput = original })
	if err := ensureExportProxyRule(context.Background(), "mock-ip", priority, mark, table); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commandLines(), "\n")
	if strings.Contains(joined, "rule del") {
		t.Fatalf("deleted a host rule: %s", joined)
	}
	if !strings.Contains(joined, "rule add priority 24567 fwmark 0x56012345 lookup 24567") {
		t.Fatalf("missing cellular rule: %s", joined)
	}
}

func TestEnsureExportProxyRuleRefusesAmbiguousSelector(t *testing.T) {
	runner := &fakeHostRunner{}
	runner.setOutput("mock-ip -N rule show", "24567: not from all fwmark 0x56012345 lookup 24567\n")
	original := hostCommandOutput
	hostCommandOutput = runner.run
	t.Cleanup(func() { hostCommandOutput = original })
	if err := ensureExportProxyRule(context.Background(), "mock-ip", 24567, 0x56012345, 24567); err == nil {
		t.Fatal("accepted conflicting selector")
	}
	if joined := strings.Join(runner.commandLines(), "\n"); strings.Contains(joined, "rule del") || strings.Contains(joined, "rule add") {
		t.Fatalf("changed ambiguous host rule: %s", joined)
	}
}

func TestClearExportProxyRoutesOnlyRemovesOwnedRule(t *testing.T) {
	_, runner, _ := newTestATHostSession(t, "enx-test")
	mark, table, priority := exportProxyRouteIdentity("enx-test")
	runner.setOutput("/sbin/ip -N rule show", fmt.Sprintf("%d: from 192.0.2.0/24 lookup 123\n%d: from all fwmark 0x%x lookup %d\n", priority, priority, mark, table))
	if err := clearExportProxyRouteWithError(context.Background(), "enx-test"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commandLines(), "\n")
	want := fmt.Sprintf("rule del priority %d from all fwmark 0x%x/0xffffffff lookup %d", priority, mark, table)
	if !strings.Contains(joined, want) {
		t.Fatalf("missing complete rule identity: %s", joined)
	}
	if !strings.Contains(joined, fmt.Sprintf("route flush table %d dev enx-test", table)) {
		t.Fatalf("table cleanup omitted device ownership: %s", joined)
	}
}

func TestClearExportProxyRoutesPropagatesFailuresAndToleratesMissingRule(t *testing.T) {
	for _, stage := range []string{"inspect", "delete", "flush", "missing"} {
		t.Run(stage, func(t *testing.T) {
			_, runner, _ := newTestATHostSession(t, "enx-test")
			mark, table, priority := exportProxyRouteIdentity("enx-test")
			original := hostCommandOutput
			hostCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				line := strings.Join(args, " ")
				if line == "-N rule show" {
					if stage == "inspect" {
						return nil, context.DeadlineExceeded
					}
					if stage == "missing" {
						return nil, nil
					}
					return []byte(fmt.Sprintf("%d: from all fwmark 0x%x lookup %d\n", priority, mark, table)), nil
				}
				if stage == "delete" && strings.HasPrefix(line, "rule del") || stage == "flush" && strings.Contains(line, "route flush") {
					return nil, context.DeadlineExceeded
				}
				return runner.run(ctx, name, args...)
			}
			t.Cleanup(func() { hostCommandOutput = original })
			err := clearExportProxyRouteWithError(context.Background(), "enx-test")
			if stage == "missing" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost cleanup failure: %v", err)
			}
		})
	}
}

func TestProtectedIPv4AddressDoesNotCreateMainPrefixRoute(t *testing.T) {
	_, runner, _ := newTestATHostSession(t, "enx-test")
	if _, err := configureExportProxyIPv4(context.Background(), "/sbin/ip", "enx-test", "192.0.2.10", 24, "192.0.2.1", []string{"192.0.2.53"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commandLines(), "\n")
	if !strings.Contains(joined, "addr replace 192.0.2.10/24 dev enx-test noprefixroute") {
		t.Fatalf("address installation permits an implicit main-table route: %s", joined)
	}
}

func TestProtectedRoutingTableRefusesOtherInterfaces(t *testing.T) {
	_, runner, _ := newTestATHostSession(t, "enx-test")
	_, table, _ := exportProxyRouteIdentity("enx-test")
	runner.setOutput(fmt.Sprintf("/sbin/ip -4 route show table %d", table), "default via 198.51.100.1 dev eth0\n")
	if _, err := configureExportProxyIPv4(context.Background(), "/sbin/ip", "enx-test", "192.0.2.10", 24, "192.0.2.1", nil); err == nil {
		t.Fatal("overwrote a table owned by another host interface")
	}
	for _, command := range runner.commandLines() {
		if strings.Contains(command, "addr replace") || strings.Contains(command, "route replace") || strings.Contains(command, "rule add") {
			t.Fatalf("changed host state after a table ownership conflict: %s", command)
		}
	}
}

func TestProtectedRouteInspectionAllowsAbsentTable(t *testing.T) {
	original := hostCommandOutput
	hostCommandOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Error: ipv4: FIB table does not exist.\nDump terminated"), errors.New("exit status 2")
	}
	t.Cleanup(func() { hostCommandOutput = original })
	if routes, err := exportProxyTableCIDRs(context.Background(), "mock-ip", 24567, "enx-test"); err != nil || len(routes) != 0 {
		t.Fatalf("first lease rejected an absent table: %v, %v", routes, err)
	}
}
