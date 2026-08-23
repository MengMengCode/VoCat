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
