package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerOpenWrtDriverBehavior(t *testing.T) {
	tests := []struct {
		name       string
		paths      []string
		skipVoWiFi bool
		load       bool
		install    bool
		wantError  bool
		warning    bool
	}{
		{name: "PCSC reader without USB modem drivers", paths: []string{"class/misc/tun"}, warning: true},
		{name: "MHI modem without USB modem drivers", paths: []string{"class/misc/tun", "bus/mhi/drivers/mhi_wwan_ctrl", "bus/mhi/drivers/qmi_wwan_mhi", "class/wwan/wwan0at0"}, warning: true},
		{name: "PCSC reader with VoWiFi skipped", skipVoWiFi: true, warning: true},
		{name: "MHI modem with VoWiFi skipped", paths: []string{"bus/mhi/drivers/mhi_wwan_ctrl", "bus/mhi/drivers/qmi_wwan_mhi", "class/wwan/wwan0at0"}, skipVoWiFi: true, warning: true},
		{name: "missing required TUN", paths: []string{"bus/usb-serial", "bus/usb/drivers/qmi_wwan"}, wantError: true},
		{name: "missing TUN with VoWiFi skipped", paths: []string{"bus/usb-serial", "bus/usb/drivers/qmi_wwan"}, skipVoWiFi: true},
		{name: "USB drivers load from installed modules", paths: []string{"class/misc/tun"}, load: true},
		{name: "USB drivers install from firmware feed", paths: []string{"class/misc/tun"}, install: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := map[string]string{"MOCK_MANAGER": "apk"}
			if test.skipVoWiFi {
				environment["SKIP_VOWIFI_CHECK"] = "1"
			}
			if test.load {
				environment["MOCK_LOAD"] = "1"
			}
			if test.install {
				environment["MOCK_PACKAGES"] = "1"
				environment["MOCK_INSTALL"] = "1"
			}
			output, calls, sysfs, err := runInstallerBehavior(t, test.paths, environment, "install_openwrt_kernel_drivers\n")
			if (err != nil) != test.wantError {
				t.Fatalf("installer error = %v, want error %v\n%s", err, test.wantError, output)
			}
			if test.warning && !strings.Contains(output, "Warning:") {
				t.Fatalf("missing optional USB drivers did not produce a warning:\n%s", output)
			}
			if test.wantError && (!strings.Contains(output, "kmod-tun") || !strings.Contains(output, "--skip-vowifi-check")) {
				t.Fatalf("required TUN failure lacks a remediation hint:\n%s", output)
			}
			if test.skipVoWiFi && (strings.Contains(calls, "modprobe tun\n") || strings.Contains(calls, "kmod-tun")) {
				t.Fatalf("VoWiFi skip still checked TUN:\n%s", calls)
			}
			if test.load || test.install {
				for _, path := range []string{"bus/usb-serial", "bus/usb/drivers/qmi_wwan"} {
					if _, err := os.Stat(filepath.Join(sysfs, filepath.FromSlash(path))); err != nil {
						t.Fatalf("USB driver was not registered at %s: %v\n%s", path, err, calls)
					}
				}
			}
			if test.install {
				if strings.Count(calls, "apk update\n") != 1 || !strings.Contains(calls, "apk add ") {
					t.Fatalf("firmware driver install did not use one feed refresh:\n%s", calls)
				}
			}
		})
	}
}

func TestInstallerOpenWrtPackageManagerBehavior(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "apk", want: "apk"},
		{name: "opkg", want: "opkg"},
		{name: "both", want: "apk"},
		{name: "none", want: "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, calls, _, err := runInstallerBehavior(t, nil, map[string]string{
				"MOCK_MANAGER":  test.name,
				"MOCK_PACKAGES": "1",
			}, `
if has_openwrt_pkg_manager; then
    printf 'manager=%s\n' "$OPENWRT_PKG_MGR"
    openwrt_pkg_update
    openwrt_pkg_update
    openwrt_pkg_available qmi-utils
    openwrt_pkg_install qmi-utils
else
    printf 'manager=none\n'
fi
`)
			if err != nil || !strings.Contains(output, "manager="+test.want+"\n") {
				t.Fatalf("package manager selection failed: %v\n%s", err, output)
			}
			if test.want == "none" {
				if calls != "" {
					t.Fatalf("missing package manager executed commands:\n%s", calls)
				}
				return
			}
			if strings.Count(calls, test.want+" update\n") != 1 {
				t.Fatalf("package manager did not refresh its feed exactly once:\n%s", calls)
			}
			install := test.want + " add qmi-utils\n"
			if test.want == "opkg" {
				install = "opkg install qmi-utils\n"
			}
			if !strings.Contains(calls, install) {
				t.Fatalf("package installation used the wrong manager:\n%s", calls)
			}
		})
	}
}

func runInstallerBehavior(t *testing.T, paths []string, environment map[string]string, action string) (string, string, string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required to execute installer behavior tests")
	}
	data, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	start := strings.Index(script, "# --- Host prerequisites ")
	end := strings.Index(script, "install_linux_ip_tool() {")
	if start < 0 || end <= start {
		t.Fatal("installer prerequisite helper definitions were not found")
	}
	// Source only helper definitions, mapping their sysfs reads into this test's
	// fixture. The installer main function and all host package tools stay unused.
	script = strings.ReplaceAll(script[start:end], "/sys/", `"$TEST_SYSFS"/`)
	root := t.TempDir()
	sysfs := filepath.Join(root, "sys")
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Join(sysfs, filepath.FromSlash(path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(root, "calls.log")
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bash, "--noprofile", "--norc", "-c", "set -euo pipefail\n"+script+installerBehaviorStubs+action)
	command.Env = append(os.Environ(),
		"TEST_SYSFS="+filepath.ToSlash(sysfs), "TEST_LOG="+filepath.ToSlash(log),
		"SKIP_VOWIFI_CHECK=0", "MOCK_MANAGER=none", "MOCK_PACKAGES=0", "MOCK_INSTALL=0", "MOCK_LOAD=0",
	)
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	output, runErr := command.CombinedOutput()
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(output), string(calls), sysfs, runErr
}

const installerBehaviorStubs = `
is_openwrt() { return 0; }
msg() { printf '%s\n' "$2"; }
die() { printf '%s\n' "$2" >&2; exit 1; }
command() {
    case "${2:-}" in
        apk) [ "$MOCK_MANAGER" = apk ] || [ "$MOCK_MANAGER" = both ] ;;
        opkg) [ "$MOCK_MANAGER" = opkg ] || [ "$MOCK_MANAGER" = both ] ;;
        modprobe) return 0 ;;
        *) builtin command "$@" ;;
    esac
}
register_mock_driver() {
    case "$1" in
        usbserial|kmod-usb-serial) mkdir -p "$TEST_SYSFS/bus/usb-serial" ;;
        qmi_wwan|kmod-usb-net-qmi-wwan) mkdir -p "$TEST_SYSFS/bus/usb/drivers/qmi_wwan" ;;
        tun|kmod-tun) mkdir -p "$TEST_SYSFS/class/misc/tun" ;;
    esac
}
modprobe() {
    printf 'modprobe %s\n' "$*" >> "$TEST_LOG"
    [ "$MOCK_LOAD" = 1 ] || return 1
    register_mock_driver "$1"
}
apk() {
    printf 'apk %s\n' "$*" >> "$TEST_LOG"
    case "$1" in
        update) return 0 ;;
        search) [ "$MOCK_PACKAGES" = 1 ] && printf '%s-1.0-r0\n' "$3" ;;
        add)
            [ "$MOCK_INSTALL" = 1 ] || return 0
            shift
            for package in "$@"; do register_mock_driver "$package"; done
            ;;
    esac
}
opkg() {
    printf 'opkg %s\n' "$*" >> "$TEST_LOG"
    case "$1" in
        update) return 0 ;;
        list) [ "$MOCK_PACKAGES" = 1 ] && printf '%s - 1.0-r0 - fixture package\n' "$2" ;;
        install) return 0 ;;
    esac
}
`
