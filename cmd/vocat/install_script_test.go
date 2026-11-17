package main

import (
	"os"
	"strings"
	"testing"
)

func TestInstallerValidatesDatabaseBeforeReplacingBinary(t *testing.T) {
	scriptBytes, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	mainStart := strings.LastIndex(script, "# --- Main ")
	if mainStart < 0 {
		t.Fatal("installer main section not found")
	}
	main := script[mainStart:]
	validateAt := strings.Index(main, `bootstrap_admin "${VOCAT_TMP}/vocat"`)
	installAt := strings.Index(main, "install_binary")
	if validateAt < 0 {
		t.Fatal("installer does not validate the database with the downloaded binary")
	}
	if installAt < 0 {
		t.Fatal("installer does not install the downloaded binary")
	}
	if validateAt > installAt {
		t.Fatal("installer replaces the current binary before validating database compatibility")
	}
}

func TestInstallerProvidesRequiredQMIUtilities(t *testing.T) {
	scriptBytes, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, required := range []string{
		"install_qmi_support()",
		"command -v qmicli",
		"command -v qmi-proxy",
		"/usr/libexec/qmi-proxy",
		"/usr/lib/qmi-proxy",
		"apt-get install -y libqmi-utils",
		"dnf install -y libqmi-utils",
		"pacman -Sy --noconfirm libqmi",
		"apk add --no-cache qmi-utils",
		"Could not install or find qmicli/qmi-proxy",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("installer is missing required QMI handling %q", required)
		}
	}
	mainStart := strings.LastIndex(script, "# --- Main ")
	if mainStart < 0 || !strings.Contains(script[mainStart:], "install_qmi_support") {
		t.Error("installer does not install QMI utilities from its main path")
	}
}

func TestInstallerSupportsOpenWrtOpkgAndApk(t *testing.T) {
	scriptBytes, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, required := range []string{
		"has_openwrt_pkg_manager()",
		"openwrt_pkg_update()",
		"openwrt_pkg_available()",
		"openwrt_pkg_install()",
		"apk update",
		`apk search -e "$1"`,
		`apk add "$@"`,
		"opkg update",
		`opkg list "$1"`,
		`opkg install "$@"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("installer is missing OpenWrt package manager handling %q", required)
		}
	}
	// Every OpenWrt install path must go through the opkg/apk abstraction,
	// otherwise OpenWrt 25+ (apk-only) images fall through to Alpine package
	// names that do not exist in OpenWrt feeds.
	for _, legacy := range []string{
		"opkg_has_package",
		"is_openwrt && command -v opkg",
	} {
		if strings.Contains(script, legacy) {
			t.Errorf("installer still contains opkg-only OpenWrt handling %q", legacy)
		}
	}
	// Kernel modules must never be force-installed across kernel ABIs.
	for _, forbidden := range []string{"--force-depends", "--force-broken-world", "--force-overwrite"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("installer must not bypass package manager kernel ABI checks with %q", forbidden)
		}
	}
}

func TestInstallerEnsuresOpenWrtKernelDrivers(t *testing.T) {
	scriptBytes, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, required := range []string{
		"install_openwrt_kernel_drivers()",
		"ensure_openwrt_kmod kmod-usb-serial usbserial /sys/bus/usb-serial",
		"ensure_openwrt_kmod kmod-tun tun /sys/class/misc/tun",
		"ensure_openwrt_kmod kmod-usb-net-qmi-wwan qmi_wwan /sys/bus/usb/drivers/qmi_wwan",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("installer is missing OpenWrt kernel driver handling %q", required)
		}
	}
	if strings.Contains(script, "--vowifi-only") || strings.Contains(script, "VOWIFI_ONLY") {
		t.Error("installer must not introduce a separate VoWiFi-only option; use --skip-vowifi-check")
	}

	start := strings.Index(script, "install_openwrt_kernel_drivers() {")
	if start < 0 {
		t.Fatal("install_openwrt_kernel_drivers body not found")
	}
	body := script[start:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	// Only the TUN driver (VoWiFi IMS tunnel) may be skipped with
	// --skip-vowifi-check; USB serial and QMI WWAN stay mandatory.
	skipAt := strings.Index(body, `if [ "$SKIP_VOWIFI_CHECK" = "1" ]`)
	tunAt := strings.Index(body, "ensure_openwrt_kmod kmod-tun")
	serialAt := strings.Index(body, "ensure_openwrt_kmod kmod-usb-serial")
	qmiAt := strings.Index(body, "ensure_openwrt_kmod kmod-usb-net-qmi-wwan")
	if skipAt < 0 || tunAt < skipAt {
		t.Error("kmod-tun must be skipped when --skip-vowifi-check is given")
	}
	if serialAt < 0 || serialAt > skipAt || qmiAt < 0 || qmiAt > skipAt {
		t.Error("kmod-usb-serial and kmod-usb-net-qmi-wwan must be required regardless of --skip-vowifi-check")
	}
	if !strings.Contains(body, "die ") {
		t.Error("installer must fail when required OpenWrt kernel drivers are missing")
	}

	mainStart := strings.LastIndex(script, "# --- Main ")
	if mainStart < 0 || !strings.Contains(script[mainStart:], "install_openwrt_kernel_drivers") {
		t.Error("installer does not check OpenWrt kernel drivers from its main path")
	}
}
