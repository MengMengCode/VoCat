//go:build linux

package device

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vocat/internal/modem"
)

func TestSetNetworkQMIRejectsUnsafeAPNWhenDisabled(t *testing.T) {
	capturePath := installFakeQMINetworkTools(t)
	manager, id := newStartedQMITestManager(t)

	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled:   false,
		APN:       "internet\nMALICIOUS=1",
		IPVersion: "IP",
	})
	if !errors.Is(err, ErrInvalidNetworkAPN) {
		profile, _ := os.ReadFile(capturePath)
		t.Fatalf("error = %v, want ErrInvalidNetworkAPN before qmi-network; captured profile = %q", err, profile)
	}
	if profile, readErr := os.ReadFile(capturePath); readErr == nil {
		t.Fatalf("unsafe APN reached qmi-network profile sink: %q", profile)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("inspect qmi-network profile capture: %v", readErr)
	}
}

func TestSetNetworkQMIPreservesEmptyAndValidAPNs(t *testing.T) {
	for _, testCase := range []struct {
		name string
		apn  string
		want string
	}{
		{name: "empty", apn: "", want: ""},
		{name: "valid", apn: " ims.example-1 ", want: "APN=ims.example-1\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			capturePath := installFakeQMINetworkTools(t)
			manager, id := newStartedQMITestManager(t)

			result, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
				Enabled:   false,
				APN:       testCase.apn,
				IPVersion: "IP",
			})
			if err != nil {
				t.Fatalf("disable QMI network: %v", err)
			}
			profile, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatalf("read qmi-network profile capture: %v", err)
			}
			if testCase.want == "" {
				if strings.Contains(string(profile), "APN=") {
					t.Fatalf("empty APN profile = %q, want no APN entry", profile)
				}
			} else if !strings.Contains(string(profile), testCase.want) {
				t.Fatalf("valid APN profile = %q, want %q", profile, testCase.want)
			}
			if result.APN != strings.TrimSpace(testCase.apn) {
				t.Fatalf("result APN = %q, want %q", result.APN, strings.TrimSpace(testCase.apn))
			}
		})
	}
}

func TestSetNetworkQMIReportsIPV4V6DowngradeTruthfully(t *testing.T) {
	capturePath := installFakeQMINetworkTools(t)
	manager, id := newStartedQMITestManager(t)

	result, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled:   false,
		APN:       "internet",
		IPVersion: "IPV4V6",
	})
	if err != nil {
		t.Fatalf("disable QMI network: %v", err)
	}
	if result.IPVersion != "IP" {
		t.Fatalf("result IP version = %q, want truthful IPv4-only value IP", result.IPVersion)
	}
	if !strings.Contains(strings.ToLower(result.Detail), "ipv4v6") ||
		!strings.Contains(strings.ToLower(result.Detail), "ipv4") {
		t.Fatalf("result detail = %q, want explicit IPV4V6-to-IPv4 downgrade", result.Detail)
	}
	profile, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read qmi-network profile capture: %v", err)
	}
	if !strings.Contains(string(profile), "IP_TYPE=4\n") {
		t.Fatalf("qmi-network profile = %q, want IP_TYPE=4", profile)
	}
}

func TestSetNetworkQMIRejectsIPv6BeforeStartingBackend(t *testing.T) {
	capturePath := installFakeQMINetworkTools(t)
	manager, id := newStartedQMITestManager(t)

	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled:   false,
		APN:       "internet",
		IPVersion: "IPV6",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ipv4 only") {
		t.Fatalf("error = %v, want explicit QMI IPv4-only rejection", err)
	}
	if profile, readErr := os.ReadFile(capturePath); readErr == nil {
		t.Fatalf("unsupported IPv6 request reached qmi-network: %q", profile)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("inspect qmi-network profile capture: %v", readErr)
	}
}

func installFakeQMINetworkTools(t *testing.T) string {
	t.Helper()
	toolDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "qmi-profile.conf")
	qmiNetworkPath := filepath.Join(toolDir, "qmi-network")
	qmiNetwork := `#!/bin/sh
profile=${1#--profile=}
/bin/cp "$profile" "$VOCAT_QMI_PROFILE_CAPTURE"
printf 'fake qmi-network %s\n' "$3"
exit 0
`
	if err := os.WriteFile(qmiNetworkPath, []byte(qmiNetwork), 0o700); err != nil {
		t.Fatalf("write fake qmi-network: %v", err)
	}
	ipPath := filepath.Join(toolDir, "ip")
	if err := os.WriteFile(ipPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake ip: %v", err)
	}
	t.Setenv("PATH", toolDir)
	t.Setenv("VOCAT_QMI_PROFILE_CAPTURE", capturePath)
	return capturePath
}

func newStartedQMITestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	const id = "qualcomm-test-qmi"
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:               id,
			VendorID:         "05c6",
			ProductID:        "f000",
			Manufacturer:     "Qualcomm",
			Product:          "MSM8916",
			QMIControl:       "/dev/cdc-wdm0",
			NetworkInterface: "wwan0",
		}}},
		Opener:         &staticOpener{},
		CommandTimeout: time.Second,
		LongTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager, id
}
