//go:build linux

package modem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSysFSDiscoverySelectsInterface04AndNeverInterface02(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idVendor"), "2c7c\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idProduct"), "0125\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "manufacturer"), "Android\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "product"), "Android\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "serial"), "Android\n")

	for _, item := range []struct {
		interfaceName   string
		interfaceNumber string
		tty             string
	}{
		{"1-6:1.2", "02", "ttyUSB0"},
		{"1-6:1.3", "03", "ttyUSB1"},
		{"1-6:1.4", "04", "ttyUSB2"},
		{"1-6:1.5", "05", "ttyUSB3"},
	} {
		mustWrite(
			t,
			filepath.Join(usbRoot, item.interfaceName, "bInterfaceNumber"),
			item.interfaceNumber+"\n",
		)
		mustMkdir(t, filepath.Join(
			usbRoot,
			item.interfaceName,
			item.tty,
			"tty",
			item.tty,
		))
	}
	mustMkdir(t, filepath.Join(usbRoot, "1-6:1.0", "net", "enx001122334455"))
	mustMkdir(t, filepath.Join(usbRoot, "1-6:1.4", "usbmisc", "cdc-wdm0"))

	discoverer := NewSysFSDiscoverer(sysRoot, devRoot)
	candidates, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.ID != "quectel-0125-1-6" {
		t.Fatalf("ID = %q", candidate.ID)
	}
	if candidate.ATPort.Name != "ttyUSB2" {
		t.Fatalf("AT port = %#v, want ttyUSB2", candidate.ATPort)
	}
	if candidate.ATPort.InterfaceNumber != 0x04 {
		t.Fatalf("AT interface = %d, want 4", candidate.ATPort.InterfaceNumber)
	}
	if candidate.Ports[0].Role != PortRoleDiagnostic {
		t.Fatalf("interface 02 role = %q", candidate.Ports[0].Role)
	}
	if candidate.QMIControl != filepath.Join(devRoot, "cdc-wdm0") {
		t.Fatalf("QMI control = %q", candidate.QMIControl)
	}
	if candidate.NetworkInterface != "enx001122334455" {
		t.Fatalf("network interface = %q", candidate.NetworkInterface)
	}
}

func TestSysFSDiscoverySelectsTTYUSB2InQMIInterface00Layout(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idVendor"), "2c7c\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idProduct"), "0125\n")

	for number, tty := range []string{"ttyUSB0", "ttyUSB1", "ttyUSB2", "ttyUSB3"} {
		interfaceName := "1-6:1." + strconv.Itoa(number)
		mustWrite(
			t,
			filepath.Join(usbRoot, interfaceName, "bInterfaceNumber"),
			fmt.Sprintf("%02x\n", number),
		)
		mustMkdir(t, filepath.Join(usbRoot, interfaceName, tty, "tty", tty))
	}
	mustWrite(
		t,
		filepath.Join(usbRoot, "1-6:1.4", "bInterfaceNumber"),
		"04\n",
	)
	mustMkdir(t, filepath.Join(usbRoot, "1-6:1.4", "usbmisc", "cdc-wdm0"))
	mustMkdir(t, filepath.Join(usbRoot, "1-6:1.4", "net", "wwp0s20f0u6i4"))

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.ATPort.Name != "ttyUSB2" ||
		candidate.ATPort.InterfaceNumber != 0x02 ||
		candidate.ATPort.Role != PortRoleAT {
		t.Fatalf("AT port = %#v, want ttyUSB2 on interface 02", candidate.ATPort)
	}
	if candidate.QMIControl != filepath.Join(devRoot, "cdc-wdm0") {
		t.Fatalf("QMI control = %q", candidate.QMIControl)
	}
	if candidate.NetworkInterface != "wwp0s20f0u6i4" {
		t.Fatalf("network interface = %q", candidate.NetworkInterface)
	}
}

func TestSysFSDiscoverySelectsATPortForSecondQMIUSBModem(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")

	for _, modem := range []struct {
		usbName string
		ttys    []string
		wdm     string
	}{
		{"1-6", []string{"ttyUSB0", "ttyUSB1", "ttyUSB2", "ttyUSB3"}, "cdc-wdm0"},
		{"1-5", []string{"ttyUSB4", "ttyUSB5", "ttyUSB6", "ttyUSB7"}, "cdc-wdm1"},
	} {
		mustWrite(t, filepath.Join(usbRoot, modem.usbName, "idVendor"), "2c7c\n")
		mustWrite(t, filepath.Join(usbRoot, modem.usbName, "idProduct"), "0125\n")
		for number, tty := range modem.ttys {
			interfaceName := modem.usbName + ":1." + strconv.Itoa(number)
			mustWrite(t, filepath.Join(usbRoot, interfaceName, "bInterfaceNumber"), fmt.Sprintf("%02x\n", number))
			mustMkdir(t, filepath.Join(usbRoot, interfaceName, tty, "tty", tty))
		}
		mustWrite(t, filepath.Join(usbRoot, modem.usbName+":1.4", "bInterfaceNumber"), "04\n")
		mustMkdir(t, filepath.Join(usbRoot, modem.usbName+":1.4", "usbmisc", modem.wdm))
	}

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(candidates))
	}
	for _, candidate := range candidates {
		switch filepath.Base(candidate.USBPath) {
		case "1-5":
			if candidate.ATPort.Name != "ttyUSB6" || candidate.ATPort.Role != PortRoleAT {
				t.Fatalf("second modem AT port = %#v, want ttyUSB6", candidate.ATPort)
			}
		case "1-6":
			if candidate.ATPort.Name != "ttyUSB2" || candidate.ATPort.Role != PortRoleAT {
				t.Fatalf("first modem AT port = %#v, want ttyUSB2", candidate.ATPort)
			}
		default:
			t.Fatalf("unexpected candidate USB path %q", candidate.USBPath)
		}
	}
}

func TestSysFSDiscoveryDoesNotCollapseModemsWithSharedFactorySerial(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")

	for index, item := range []struct {
		usbName string
		ttyBase int
	}{
		{usbName: "1-5.1", ttyBase: 0},
		{usbName: "1-5.2", ttyBase: 4},
	} {
		mustWrite(t, filepath.Join(usbRoot, item.usbName, "idVendor"), "2c7c\n")
		mustWrite(t, filepath.Join(usbRoot, item.usbName, "idProduct"), "0125\n")
		mustWrite(t, filepath.Join(usbRoot, item.usbName, "serial"), "0123456789ABCDEF\n")
		for number := 0; number < 4; number++ {
			interfaceName := item.usbName + ":1." + strconv.Itoa(number)
			tty := fmt.Sprintf("ttyUSB%d", item.ttyBase+number)
			mustWrite(t, filepath.Join(usbRoot, interfaceName, "bInterfaceNumber"), fmt.Sprintf("%02x\n", number))
			mustMkdir(t, filepath.Join(usbRoot, interfaceName, tty, "tty", tty))
		}
		mustMkdir(t, filepath.Join(usbRoot, item.usbName+":1.4", "usbmisc", fmt.Sprintf("cdc-wdm%d", index)))
	}

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(candidates))
	}
	if candidates[0].ID == candidates[1].ID {
		t.Fatalf("shared factory serial collapsed discovery IDs to %q", candidates[0].ID)
	}
	for _, candidate := range candidates {
		if candidate.SerialNumber != "0123456789ABCDEF" {
			t.Fatalf("serial = %q", candidate.SerialNumber)
		}
		if candidate.ATPort.Role != PortRoleAT {
			t.Fatalf("AT port = %#v", candidate.ATPort)
		}
	}
}

func TestSysFSDiscoveryIgnoresNonQuectelUSB(t *testing.T) {
	root := t.TempDir()
	usbRoot := filepath.Join(root, "sys", "bus", "usb", "devices")
	mustWrite(t, filepath.Join(usbRoot, "2-1", "idVendor"), "0403\n")
	mustWrite(t, filepath.Join(usbRoot, "2-1:1.0", "bInterfaceNumber"), "00\n")
	mustMkdir(t, filepath.Join(usbRoot, "2-1:1.0", "ttyUSB9"))

	candidates, err := NewSysFSDiscoverer(
		filepath.Join(root, "sys"),
		filepath.Join(root, "dev"),
	).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("got %#v, want no candidates", candidates)
	}
}

func TestSysFSDiscoveryFindsNativeWWANQMIAndATPorts(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")

	for _, name := range []string{"wwan0", "wwan0at0", "wwan0qmi0"} {
		mustMkdir(t, filepath.Join(sysRoot, "class", "wwan", name))
	}
	mustWrite(t, filepath.Join(devRoot, "wwan0at0"), "")
	mustWrite(t, filepath.Join(devRoot, "wwan0qmi0"), "")
	mustMkdir(t, filepath.Join(sysRoot, "class", "net", "wwan0"))

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1: %#v", len(candidates), candidates)
	}
	candidate := candidates[0]
	if candidate.ID != "wwan0" {
		t.Fatalf("ID = %q, want wwan0", candidate.ID)
	}
	if candidate.USBPath != filepath.Join(sysRoot, "class", "wwan", "wwan0") {
		t.Fatalf("USB path = %q", candidate.USBPath)
	}
	if candidate.ATPort.OpenPath() != filepath.Join(devRoot, "wwan0at0") ||
		candidate.ATPort.Role != PortRoleAT {
		t.Fatalf("AT port = %#v", candidate.ATPort)
	}
	if candidate.QMIControl != filepath.Join(devRoot, "wwan0qmi0") {
		t.Fatalf("QMI control = %q", candidate.QMIControl)
	}
	if candidate.NetworkInterface != "wwan0" {
		t.Fatalf("network interface = %q", candidate.NetworkInterface)
	}
}

func TestSysFSDiscoveryRetainsNativeWWANWhileQMIPortRecovers(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")

	for _, name := range []string{"wwan0", "wwan0at0"} {
		mustMkdir(t, filepath.Join(sysRoot, "class", "wwan", name))
	}
	mustWrite(t, filepath.Join(devRoot, "wwan0at0"), "")
	mustMkdir(t, filepath.Join(sysRoot, "class", "net", "wwan0"))

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1: %#v", len(candidates), candidates)
	}
	candidate := candidates[0]
	if candidate.ID != "wwan0" || candidate.ATPort.OpenPath() != filepath.Join(devRoot, "wwan0at0") {
		t.Fatalf("native recovery candidate = %#v", candidate)
	}
	if candidate.QMIControl != "" {
		t.Fatalf("QMI control = %q, want empty while DATA5_CNTL recovers", candidate.QMIControl)
	}
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
