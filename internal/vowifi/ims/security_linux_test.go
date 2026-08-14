//go:build linux

package ims

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunIPCommandExplainsMissingKernelXFRM(t *testing.T) {
	directory := t.TempDir()
	command := directory + "/ip"
	script := "#!/bin/sh\necho 'Cannot open netlink socket: Protocol not supported' >&2\nexit 1\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runIPCommand(context.Background(), command, xfrmOperation{description: "test state"})
	if err == nil || !strings.Contains(err.Error(), "kmod-ipsec") || !strings.Contains(err.Error(), "CONFIG_XFRM_USER") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinuxIPSecInstallerLifecycle(t *testing.T) {
	if os.Getenv("VOCAT_NETNS_TEST") != "1" {
		t.Skip("set VOCAT_NETNS_TEST=1 inside an isolated Linux network namespace")
	}
	handle, err := (linuxIPSecInstaller{ipCommand: "ip"}).Install(
		context.Background(),
		testIPSecSAConfig(),
	)
	if err != nil {
		t.Fatalf("install ipsec-3gpp XFRM set: %v", err)
	}
	states, err := exec.Command("ip", "xfrm", "state").CombinedOutput()
	if err != nil {
		t.Fatalf("list XFRM states: %v: %s", err, states)
	}
	if count := strings.Count(string(states), "src 10.0.0.2 dst 10.0.0.3"); count != 2 {
		t.Fatalf("outbound XFRM state count = %d: %s", count, states)
	}
	if count := strings.Count(string(states), "src 10.0.0.3 dst 10.0.0.2"); count != 2 {
		t.Fatalf("inbound XFRM state count = %d: %s", count, states)
	}
	policies, err := exec.Command("ip", "xfrm", "policy").CombinedOutput()
	if err != nil {
		t.Fatalf("list XFRM policies: %v: %s", err, policies)
	}
	if count := strings.Count(string(policies), "sport 40666 dport 50600"); count != 2 {
		t.Fatalf("UE-client policy count = %d: %s", count, policies)
	}

	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handle.Close(closeContext); err != nil {
		t.Fatalf("close ipsec-3gpp XFRM set: %v", err)
	}
	states, err = exec.Command("ip", "xfrm", "state").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(states)) != "" {
		t.Fatalf("XFRM states survived Close: %s", states)
	}
	policies, err = exec.Command("ip", "xfrm", "policy").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(policies)) != "" {
		t.Fatalf("XFRM policies survived Close: %s", policies)
	}
}
