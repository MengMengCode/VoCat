//go:build linux

package ike

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestXFRMInstallerLeavesDummyMTUUnchangedInCompatibilityMode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ip.log")
	ipCommand := filepath.Join(dir, "ip")
	t.Setenv("VOCAT_TEST_IP_LOG", logPath)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$VOCAT_TEST_IP_LOG\"\n"
	if err := os.WriteFile(ipCommand, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := userspaceRouteTestConfig()
	config.Name = "vocat-xfrm-test"
	config.OuterLocal = net.IPv4(192, 0, 2, 10)
	config.OuterRemote = net.IPv4(192, 0, 2, 20)
	config.InboundSPI = 1
	config.OutboundSPI = 2
	config.MTUCompatibility = true
	ctx := context.Background()
	handle, err := (linuxXFRMInstaller{ipCommand: ipCommand}).Install(ctx, config)
	if err != nil {
		t.Fatalf("install native XFRM tunnel: %v", err)
	}
	if err := handle.Close(ctx); err != nil {
		t.Fatalf("close native XFRM tunnel: %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "link set dev " + config.Name + " up"; !slices.Contains(strings.Split(string(commands), "\n"), want) {
		t.Fatalf("missing %q in XFRM commands:\n%s", want, commands)
	}
}
