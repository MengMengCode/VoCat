//go:build linux

package modem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	mustBindQMIWWAN(t, sysRoot, "1-6:1.4")

	discoverer := NewSysFSDiscoverer(sysRoot, devRoot)
	candidates, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.ID != "usb-2c7c-0125-1-6" {
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
	mustBindQMIWWAN(t, sysRoot, "1-6:1.4")

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
		mustBindQMIWWAN(t, sysRoot, modem.usbName+":1.4")
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
		mustBindQMIWWAN(t, sysRoot, item.usbName+":1.4")
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

func TestSysFSDiscoveryIgnoresUSBWithoutQMIWWANBinding(t *testing.T) {
	root := t.TempDir()
	usbRoot := filepath.Join(root, "sys", "bus", "usb", "devices")
	// A plain USB serial adapter (FTDI) exposes ttyUSB but no QMI interface and
	// is never bound to qmi_wwan, so it must not be treated as a modem.
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

func TestSysFSDiscoveryFindsNonQuectelVendorBoundToQMIWWAN(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	// A Sierra EM7430 flashed to its QMI (rmnet0) composition: non-Quectel
	// vendor, but its control interface is bound to qmi_wwan.
	mustWrite(t, filepath.Join(usbRoot, "1-3", "idVendor"), "1199\n")
	mustWrite(t, filepath.Join(usbRoot, "1-3", "idProduct"), "9077\n")
	mustWrite(t, filepath.Join(usbRoot, "1-3", "manufacturer"), "Sierra Wireless, Incorporated\n")
	mustWrite(t, filepath.Join(usbRoot, "1-3", "product"), "EM7430\n")
	for number, tty := range []string{"ttyUSB0", "ttyUSB1", "ttyUSB2", "ttyUSB3"} {
		interfaceName := "1-3:1." + strconv.Itoa(number)
		mustWrite(t, filepath.Join(usbRoot, interfaceName, "bInterfaceNumber"), fmt.Sprintf("%02x\n", number))
		mustMkdir(t, filepath.Join(usbRoot, interfaceName, tty, "tty", tty))
	}
	mustMkdir(t, filepath.Join(usbRoot, "1-3:1.4", "usbmisc", "cdc-wdm0"))
	mustBindQMIWWAN(t, sysRoot, "1-3:1.4")

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.VendorID != "1199" || candidate.Product != "EM7430" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if candidate.ID != "usb-1199-9077-1-3" {
		t.Fatalf("ID = %q", candidate.ID)
	}
	if candidate.ATPort.Role != PortRoleAT {
		t.Fatalf("AT port = %#v", candidate.ATPort)
	}
	if candidate.QMIControl != filepath.Join(devRoot, "cdc-wdm0") {
		t.Fatalf("QMI control = %q", candidate.QMIControl)
	}
	if candidate.DiscoveryIssue != "" {
		t.Fatalf("discovery issue = %q, want none", candidate.DiscoveryIssue)
	}
}

func TestSysFSDiscoveryMarksQMIModemWithoutATPort(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	// QMI control interface is bound, but no ttyUSB/ttyACM node exists (for
	// example the option/qcserial driver does not claim the serial interfaces).
	mustWrite(t, filepath.Join(usbRoot, "1-7", "idVendor"), "2c7c\n")
	mustWrite(t, filepath.Join(usbRoot, "1-7", "idProduct"), "0125\n")
	mustMkdir(t, filepath.Join(usbRoot, "1-7:1.4", "usbmisc", "cdc-wdm0"))
	mustBindQMIWWAN(t, sysRoot, "1-7:1.4")

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.DiscoveryIssue != "at_port_missing" {
		t.Fatalf("discovery issue = %q, want at_port_missing", candidate.DiscoveryIssue)
	}
	if candidate.HasATPort() {
		t.Fatalf("candidate unexpectedly has an AT port: %#v", candidate.ATPort)
	}
	if candidate.QMIControl != filepath.Join(devRoot, "cdc-wdm0") {
		t.Fatalf("QMI control = %q", candidate.QMIControl)
	}
}

func TestSysFSDiscoveryFindsHubAttachedQMIWWANDevice(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	// Device behind a hub: the USB path "1-4.3.2" contains extra segments, and
	// the qmi_wwan binding uses the same composite path before the colon.
	mustWrite(t, filepath.Join(usbRoot, "1-4.3.2", "idVendor"), "2c7c\n")
	mustWrite(t, filepath.Join(usbRoot, "1-4.3.2", "idProduct"), "0125\n")
	for number, tty := range []string{"ttyUSB0", "ttyUSB1", "ttyUSB2", "ttyUSB3"} {
		interfaceName := "1-4.3.2:1." + strconv.Itoa(number)
		mustWrite(t, filepath.Join(usbRoot, interfaceName, "bInterfaceNumber"), fmt.Sprintf("%02x\n", number))
		mustMkdir(t, filepath.Join(usbRoot, interfaceName, tty, "tty", tty))
	}
	mustMkdir(t, filepath.Join(usbRoot, "1-4.3.2:1.4", "usbmisc", "cdc-wdm0"))
	mustBindQMIWWAN(t, sysRoot, "1-4.3.2:1.4")

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.ATPort.Name != "ttyUSB2" {
		t.Fatalf("AT port = %#v, want ttyUSB2", candidate.ATPort)
	}
	if candidate.QMIControl != filepath.Join(devRoot, "cdc-wdm0") {
		t.Fatalf("QMI control = %q", candidate.QMIControl)
	}
	if !strings.Contains(candidate.ID, "1-4-3-2") {
		t.Fatalf("ID = %q, want hub topology in discovery key", candidate.ID)
	}
}

func TestSysFSDiscoveryFindsPCIeMHIWWANWithoutUSBBus(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	wwanRoot := filepath.Join(sysRoot, "class", "wwan")
	for _, name := range []string{"wwan0at1", "wwan0qmi0", "wwan0at0"} {
		mustMkdir(t, filepath.Join(wwanRoot, name))
	}
	mustMkdir(t, filepath.Join(sysRoot, "class", "net", "wwan0"))

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one MHI device", candidates)
	}
	candidate := candidates[0]
	if candidate.ID != "mhi-wwan0" || candidate.HardwareKind != "wwan" {
		t.Fatalf("identity = %#v", candidate)
	}
	if candidate.ATPort.Path != filepath.Join(devRoot, "wwan0at1") || candidate.ATPort.Role != PortRoleAT {
		t.Fatalf("AT port = %#v", candidate.ATPort)
	}
	if candidate.QMIControl != filepath.Join(devRoot, "wwan0qmi0") {
		t.Fatalf("QMI control = %q", candidate.QMIControl)
	}
	if candidate.NetworkInterface != "wwan0" {
		t.Fatalf("network interface = %q", candidate.NetworkInterface)
	}
}

func TestSysFSDiscoveryFindsWWANFromDevNodesWithoutClassDirectory(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	for _, name := range []string{"wwan2at0", "wwan2qmi0"} {
		mustWrite(t, filepath.Join(devRoot, name), "")
	}
	mustMkdir(t, filepath.Join(sysRoot, "class", "net", "wwan2"))

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one WWAN device", candidates)
	}
	if candidates[0].ATPort.Path != filepath.Join(devRoot, "wwan2at0") ||
		candidates[0].QMIControl != filepath.Join(devRoot, "wwan2qmi0") ||
		candidates[0].NetworkInterface != "wwan2" {
		t.Fatalf("candidate = %#v", candidates[0])
	}
}

func TestSelectWWANATPortPrefersSecondaryATPort(t *testing.T) {
	ports := []Port{
		{Name: "wwan0at0", InterfaceNumber: 0, Role: PortRoleAT},
		{Name: "wwan0at1", InterfaceNumber: 1, Role: PortRoleAT},
	}
	if got := selectWWANATPort(ports); got.Name != "wwan0at1" {
		t.Fatalf("selectWWANATPort = %#v, want wwan0at1", got)
	}
}

func TestSelectWWANATPortFallsBackToPrimaryWhenOnlyAT0(t *testing.T) {
	ports := []Port{{Name: "wwan0at0", InterfaceNumber: 0, Role: PortRoleAT}}
	if got := selectWWANATPort(ports); got.Name != "wwan0at0" {
		t.Fatalf("selectWWANATPort = %#v, want wwan0at0", got)
	}
}

func TestParseWWANPortName(t *testing.T) {
	for _, test := range []struct {
		name, index, kind string
		port              int
		ok                bool
	}{
		{"wwan0at0", "0", "at", 0, true},
		{"wwan12qmi3", "12", "qmi", 3, true},
		{"wwan0", "", "", 0, false},
		{"wwanXat0", "", "", 0, false},
		{"cdc-wdm0", "", "", 0, false},
	} {
		index, kind, port, ok := parseWWANPortName(test.name)
		if index != test.index || kind != test.kind || port != test.port || ok != test.ok {
			t.Fatalf("parseWWANPortName(%q) = %q, %q, %d, %v", test.name, index, kind, port, ok)
		}
	}
}

func TestSysFSDiscoveryFindsQuectelSerialModemWithoutQMIWWANBinding(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	// A Quectel EC200A in its USB-serial composition (2c7c:6005) exposes ttyUSB
	// control ports but no qmi_wwan-bound interface, so discovery must re-admit
	// it by vendor instead of skipping it.
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idVendor"), "2c7c\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idProduct"), "6005\n")
	for number, tty := range []string{"ttyUSB0", "ttyUSB1", "ttyUSB2", "ttyUSB3"} {
		interfaceName := "1-6:1." + strconv.Itoa(number)
		mustWrite(t, filepath.Join(usbRoot, interfaceName, "bInterfaceNumber"), fmt.Sprintf("%02x\n", number))
		mustMkdir(t, filepath.Join(usbRoot, interfaceName, tty, "tty", tty))
	}

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.VendorID != "2c7c" || candidate.ProductID != "6005" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if candidate.ID != "usb-2c7c-6005-1-6" {
		t.Fatalf("ID = %q", candidate.ID)
	}
	if candidate.ATPort.Name != "ttyUSB2" || candidate.ATPort.Role != PortRoleAT {
		t.Fatalf("AT port = %#v, want ttyUSB2 at role AT", candidate.ATPort)
	}
	if candidate.DiscoveryIssue != "" {
		t.Fatalf("discovery issue = %q, want none", candidate.DiscoveryIssue)
	}
}

func TestSysFSDiscoveryMarksQuectelPeripheralWithoutATPort(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	// A Quectel-branded peripheral exposing only a network interface (no
	// ttyUSB/ttyACM, no qmi_wwan binding) cannot be driven yet, but vocat
	// surfaces it with at_port_missing instead of silently dropping it so the
	// operator sees the device is present and learns what to fix.
	mustWrite(t, filepath.Join(usbRoot, "1-8", "idVendor"), "2c7c\n")
	mustWrite(t, filepath.Join(usbRoot, "1-8", "idProduct"), "6005\n")
	mustMkdir(t, filepath.Join(usbRoot, "1-8:1.0", "net", "enx001122334455"))

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.DiscoveryIssue != "at_port_missing" {
		t.Fatalf("discovery issue = %q, want at_port_missing", candidate.DiscoveryIssue)
	}
	if candidate.HasATPort() {
		t.Fatalf("candidate unexpectedly has an AT port: %#v", candidate.ATPort)
	}
	if candidate.NetworkInterface != "enx001122334455" {
		t.Fatalf("network interface = %q", candidate.NetworkInterface)
	}
}

func TestSysFSDiscoveryMarksQuectelMBIMCompositionWithoutATPort(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	// An EG25-G in MBIM composition (2c7c:0900) exposes cdc-wdm + net but no
	// ttyUSB and has no qmi_wwan binding (cdc_mbim binds the control interface
	// instead). vocat has no MBIM backend, so it must surface the device with
	// at_port_missing rather than hiding it.
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idVendor"), "2c7c\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "idProduct"), "0900\n")
	mustWrite(t, filepath.Join(usbRoot, "1-6", "product"), "EG25-G\n")
	mustMkdir(t, filepath.Join(usbRoot, "1-6:1.0", "usbmisc", "cdc-wdm0"))
	mustMkdir(t, filepath.Join(usbRoot, "1-6:1.0", "net", "wwp0s20f0u6"))

	candidates, err := NewSysFSDiscoverer(sysRoot, devRoot).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.DiscoveryIssue != "at_port_missing" {
		t.Fatalf("discovery issue = %q, want at_port_missing", candidate.DiscoveryIssue)
	}
	if candidate.HasATPort() {
		t.Fatalf("candidate unexpectedly has an AT port: %#v", candidate.ATPort)
	}
	if candidate.Product != "EG25-G" {
		t.Fatalf("product = %q", candidate.Product)
	}
	// cdc-wdm0 sits under usbmisc/, which scanUSBInterface reports as a QMI
	// control name; either way the device must appear present, not vanish.
	if candidate.QMIControl == "" && candidate.NetworkInterface == "" {
		t.Fatalf("candidate has neither QMI control nor net interface: %#v", candidate)
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

// mustBindQMIWWAN mimics the kernel's driver-binding directory entry: it adds
// interfaceName (e.g. "1-6:1.4") under /sys/bus/usb/drivers/qmi_wwan exactly
// like the real qmi_wwan driver directory does for a bound QMI interface.
func mustBindQMIWWAN(t *testing.T, sysRoot, interfaceName string) {
	t.Helper()
	driverDir := filepath.Join(sysRoot, "bus", "usb", "drivers", "qmi_wwan")
	if err := os.MkdirAll(driverDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sysRoot, "bus", "usb", "devices", interfaceName)
	if err := os.Symlink(target, filepath.Join(driverDir, interfaceName)); err != nil {
		t.Fatal(err)
	}
}
