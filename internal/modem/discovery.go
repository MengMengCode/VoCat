package modem

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	djiVendorID    = "2ca3"
	dji4GProductID = "4006"
	// quectelVendorID covers Quectel USB modems exposed purely as serial or
	// RNDIS/ECM devices (for example the EC200A at 2c7c:6005). Their control
	// interface is not bound to qmi_wwan, so the QMI-binding gate would skip
	// them even though they expose a usable AT serial port.
	quectelVendorID = "2c7c"
)

type SysFSDiscoverer struct {
	SysRoot string
	DevRoot string
}

func NewSysFSDiscoverer(sysRoot, devRoot string) *SysFSDiscoverer {
	return &SysFSDiscoverer{
		SysRoot: filepath.Clean(sysRoot),
		DevRoot: filepath.Clean(devRoot),
	}
}

type discoveredUSBDevice struct {
	candidate Candidate
	ports     map[string]Port
}

func (d *SysFSDiscoverer) Discover(ctx context.Context) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	usbRoot := filepath.Join(d.SysRoot, "bus", "usb", "devices")
	entries, err := os.ReadDir(usbRoot)
	if err != nil {
		if os.IsNotExist(err) {
			entries = nil
		} else {
			return nil, fmt.Errorf("discover USB QMI modems: %w", err)
		}
	}

	// Candidate modems are identified by kernel driver binding instead of a
	// vendor-ID whitelist. qmi_wwan only binds Qualcomm QMI control interfaces,
	// so any USB device with a bound interface exposes a live QMI channel. This
	// keeps discovery vendor-neutral (SIMCom, Sierra, Telit and other
	// Qualcomm-based modems are found automatically) while MBIM-only devices
	// stay out, because cdc_mbim binds their control interface instead and the
	// project has no MBIM backend.
	qmiBound := d.qmiWWANBoundDevices()

	aliases := readSerialAliases(filepath.Join(d.DevRoot, "serial", "by-id"))
	devices := make(map[string]*discoveredUSBDevice)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		interfaceNumber, ok := parseUSBInterfaceName(entry.Name())
		if !ok {
			continue
		}
		interfacePath := filepath.Join(usbRoot, entry.Name())
		resolvedInterface, err := filepath.EvalSymlinks(interfacePath)
		if err != nil {
			resolvedInterface = interfacePath
		}
		if value, err := readHexByte(filepath.Join(resolvedInterface, "bInterfaceNumber")); err == nil {
			interfaceNumber = value
		}

		deviceName := strings.SplitN(entry.Name(), ":", 2)[0]
		devicePath := filepath.Join(usbRoot, deviceName)
		resolvedDevice, err := filepath.EvalSymlinks(devicePath)
		if err != nil {
			resolvedDevice = devicePath
		}
		vendorID := strings.ToLower(readTrimmed(filepath.Join(resolvedDevice, "idVendor")))
		productID := strings.ToLower(readTrimmed(filepath.Join(resolvedDevice, "idProduct")))
		if _, bound := qmiBound[deviceName]; !bound && !IsDJI4GUSB(vendorID, productID) {
			// A bound qmi_wwan interface is the strongest vendor-neutral "this is
			// a live QMI modem" signal, but it excludes Quectel modules running
			// in a serial or RNDIS/ECM USB composition (no qmi_wwan binding).
			// Re-admit them by vendor so their AT serial ports stay discoverable;
			// the candidate is only kept if a ttyUSB/ttyACM node is actually
			// found below, which is exactly the AT-bearing composition we want.
			if !isQuectelUSBModem(vendorID) {
				continue
			}
		}

		state := devices[deviceName]
		if state == nil {
			serialNumber := readTrimmed(filepath.Join(resolvedDevice, "serial"))
			manufacturer, product := normalizeUSBIdentity(
				vendorID,
				productID,
				readTrimmed(filepath.Join(resolvedDevice, "manufacturer")),
				readTrimmed(filepath.Join(resolvedDevice, "product")),
			)
			state = &discoveredUSBDevice{
				candidate: Candidate{
					ID:           candidateID(vendorID, productID, serialNumber, deviceName),
					VendorID:     vendorID,
					ProductID:    productID,
					Manufacturer: manufacturer,
					Product:      product,
					SerialNumber: serialNumber,
					USBPath:      devicePath,
					USBGeneration: strings.TrimSpace(
						readTrimmed(filepath.Join(resolvedDevice, "busnum")) + ":" +
							readTrimmed(filepath.Join(resolvedDevice, "devnum")),
					),
				},
				ports: make(map[string]Port),
			}
			devices[deviceName] = state
		}

		ttyNames, qmiControls, networkInterfaces := scanUSBInterface(resolvedInterface)
		for _, name := range ttyNames {
			if !strings.HasPrefix(name, "ttyUSB") && !strings.HasPrefix(name, "ttyACM") {
				continue
			}
			path := filepath.Join(d.DevRoot, name)
			state.ports[name] = Port{
				Path:            path,
				StablePath:      reliableSerialAlias(state.candidate.SerialNumber, aliases[name]),
				Name:            name,
				InterfaceNumber: interfaceNumber,
				Role:            quecPortRole(interfaceNumber, name),
			}
		}
		if state.candidate.QMIControl == "" && len(qmiControls) > 0 {
			state.candidate.QMIControl = filepath.Join(d.DevRoot, qmiControls[0])
		}
		if state.candidate.NetworkInterface == "" && len(networkInterfaces) > 0 {
			state.candidate.NetworkInterface = networkInterfaces[0]
		}
	}

	result := make([]Candidate, 0, len(devices))
	for _, state := range devices {
		state.candidate.Ports = make([]Port, 0, len(state.ports))
		for _, port := range state.ports {
			state.candidate.Ports = append(state.candidate.Ports, port)
		}
		sort.Slice(state.candidate.Ports, func(i, j int) bool {
			left, right := state.candidate.Ports[i], state.candidate.Ports[j]
			if left.InterfaceNumber != right.InterfaceNumber {
				return left.InterfaceNumber < right.InterfaceNumber
			}
			return left.Name < right.Name
		})
		assignQuectelPortRoles(state.candidate.Ports)
		state.candidate.ATPort = selectATPort(state.candidate.Ports)
		if !state.candidate.HasATPort() {
			// A modem without a usable AT port cannot be driven by vocat, but it
			// is far more useful to surface it with a discovery issue than to
			// silently drop it: the operator sees the device is present and gets
			// told why it is unusable. Two shapes land here:
			//   * qmi_wwan is bound but no ttyUSB/ttyACM exists — the option/qcserial
			//     driver did not claim the serial interfaces (often a missing PID
			//     in its device-ID table, common on Ubuntu for EG25-G carrier
			//     builds). The modem is alive; it just lacks an AT node.
			//   * no qmi_wwan binding (Quectel re-admitted by vendor) and no AT
			//     port — typically an MBIM/RNDIS/ECM composition. The module is on
			//     the bus but exposes no AT serial interface vocat can open.
			// Both resolve the same operator action: add the PID to the option
			// driver or switch the module to a QMI+AT composition.
			state.candidate.DiscoveryIssue = "at_port_missing"
		}
		result = append(result, state.candidate)
	}
	wwanCandidates, err := d.discoverWWAN(ctx)
	if err != nil {
		return nil, err
	}
	result = append(result, wwanCandidates...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// IsDJI4GUSB reports whether a USB identity belongs to the first-generation
// DJI/Baiwang 4G module. It keeps the factory 2ca3:4006 identity usable without
// requiring a persistent AT+QCFG USB identity rewrite to Quectel 2c7c:0125.
func IsDJI4GUSB(vendorID, productID string) bool {
	return strings.EqualFold(strings.TrimSpace(vendorID), djiVendorID) &&
		strings.EqualFold(strings.TrimSpace(productID), dji4GProductID)
}

// isQuectelUSBModem reports whether a USB identity belongs to a Quectel
// module. Quectel's serial/RNDIS/ECM compositions (e.g. EC200A at 2c7c:6005)
// do not bind qmi_wwan, so discovery must fall back to the vendor ID to keep
// them visible. The candidate is only retained if it exposes an AT serial
// port, which filters out unrelated Quectel-branded peripherals.
func isQuectelUSBModem(vendorID string) bool {
	return strings.EqualFold(strings.TrimSpace(vendorID), quectelVendorID)
}

// normalizeUSBIdentity replaces the placeholder strings shipped by the
// classic Quectel EC20/EC25 USB composition. Linux faithfully exposes those
// modules as "Android / Android", but that text is a firmware placeholder,
// not the modem model or manufacturer.
func normalizeUSBIdentity(vendorID, productID, manufacturer, product string) (string, string) {
	if !isQuectelUSBModem(vendorID) ||
		!strings.EqualFold(strings.TrimSpace(productID), "0125") {
		return manufacturer, product
	}
	if strings.EqualFold(strings.TrimSpace(manufacturer), "Android") || strings.TrimSpace(manufacturer) == "" {
		manufacturer = "Quectel"
	}
	if strings.EqualFold(strings.TrimSpace(product), "Android") || strings.TrimSpace(product) == "" {
		product = "Quectel EC20 / EC25"
	}
	return manufacturer, product
}

// reliableSerialAlias rejects the generic serial number used by older
// EC20/EC25 firmware. A /dev/serial/by-id link derived from "Android" is not a
// hardware identity: two modules can publish the same link, and udev may point
// it at a different tty after reboot. The live tty plus USB topology/IMEI is
// safer and is re-resolved on every discovery pass.
func reliableSerialAlias(serialNumber, alias string) string {
	if strings.EqualFold(strings.TrimSpace(serialNumber), "Android") {
		return ""
	}
	return alias
}

type discoveredWWANDevice struct {
	index    string
	ports    []Port
	qmiNames []string
	sysPath  string
}

// discoverWWAN covers PCIe/MHI modems exposed through Linux's wwan subsystem,
// for example /dev/wwan0at0 and /dev/wwan0qmi0. These devices do not appear on
// the USB bus and therefore need a separate discovery path.
func (d *SysFSDiscoverer) discoverWWAN(ctx context.Context) ([]Candidate, error) {
	classRoot := filepath.Join(d.SysRoot, "class", "wwan")
	classEntries, err := os.ReadDir(classRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("discover PCIe/MHI WWAN devices: %w", err)
		}
		classEntries = nil
	}

	// Normal kernels expose these ports in /sys/class/wwan. Also inspect /dev
	// because some downstream MHI packages create the character devices but do
	// not populate the class directory in the host namespace/container.
	portNames := make(map[string]struct{})
	for _, entry := range classEntries {
		portNames[entry.Name()] = struct{}{}
	}
	if devEntries, devErr := os.ReadDir(d.DevRoot); devErr == nil {
		for _, entry := range devEntries {
			if _, _, _, ok := parseWWANPortName(entry.Name()); ok {
				portNames[entry.Name()] = struct{}{}
			}
		}
	} else if !os.IsNotExist(devErr) {
		return nil, fmt.Errorf("inspect WWAN device nodes: %w", devErr)
	}
	names := make([]string, 0, len(portNames))
	for name := range portNames {
		names = append(names, name)
	}
	sort.Strings(names)

	groups := make(map[string]*discoveredWWANDevice)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		index, kind, portIndex, ok := parseWWANPortName(name)
		if !ok {
			continue
		}
		group := groups[index]
		if group == nil {
			group = &discoveredWWANDevice{index: index}
			groups[index] = group
		}
		classPath := filepath.Join(classRoot, name)
		if resolved, resolveErr := filepath.EvalSymlinks(classPath); resolveErr == nil {
			group.sysPath = filepath.Dir(resolved)
		}
		switch kind {
		case "at":
			group.ports = append(group.ports, Port{
				Path: filepath.Join(d.DevRoot, name), Name: name,
				InterfaceNumber: portIndex, Role: PortRoleAT,
			})
		case "qmi":
			group.qmiNames = append(group.qmiNames, name)
		}
	}
	result := make([]Candidate, 0, len(groups))
	for _, group := range groups {
		sort.Slice(group.ports, func(i, j int) bool {
			return group.ports[i].InterfaceNumber < group.ports[j].InterfaceNumber
		})
		sort.Strings(group.qmiNames)
		if len(group.ports) == 0 && len(group.qmiNames) == 0 {
			continue
		}
		if group.sysPath == "" {
			group.sysPath = filepath.Join(classRoot, "wwan"+group.index)
		}
		vendorID, productID := readPCIIdentity(group.sysPath, d.SysRoot)
		manufacturer := ""
		if vendorID == "17cb" {
			manufacturer = "Qualcomm"
		}
		candidate := Candidate{
			HardwareKind: "wwan", ID: "mhi-wwan" + group.index,
			VendorID: vendorID, ProductID: productID, Manufacturer: manufacturer,
			Product: "PCIe/MHI WWAN modem", USBPath: group.sysPath,
			Ports: group.ports, NetworkInterface: selectWWANNetworkInterface(d.SysRoot, group.index),
		}
		if len(group.ports) > 0 {
			candidate.ATPort = selectWWANATPort(group.ports)
		}
		if len(group.qmiNames) > 0 {
			candidate.QMIControl = filepath.Join(d.DevRoot, group.qmiNames[0])
		}
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// selectWWANATPort prefers the secondary AT port (…at1) over the primary
// (…at0) when both exist, falling back to the first AT port otherwise. Some
// Qualcomm MHI modems (notably the UFI dongle behind the OpenStick 410) answer
// on at1 immediately while at0 delays every response by 10-20 seconds, so the
// secondary port is the usable AT channel.
func selectWWANATPort(ports []Port) Port {
	for _, port := range ports {
		if port.InterfaceNumber == 1 {
			return port
		}
	}
	return ports[0]
}

func parseWWANPortName(name string) (index, kind string, portIndex int, ok bool) {
	if !strings.HasPrefix(name, "wwan") {
		return "", "", 0, false
	}
	rest := strings.TrimPrefix(name, "wwan")
	cut := 0
	for cut < len(rest) && rest[cut] >= '0' && rest[cut] <= '9' {
		cut++
	}
	if cut == 0 {
		return "", "", 0, false
	}
	index, rest = rest[:cut], rest[cut:]
	for _, candidateKind := range []string{"at", "qmi"} {
		if !strings.HasPrefix(rest, candidateKind) {
			continue
		}
		numberText := strings.TrimPrefix(rest, candidateKind)
		number, err := strconv.Atoi(numberText)
		if err != nil || number < 0 {
			return "", "", 0, false
		}
		return index, candidateKind, number, true
	}
	return "", "", 0, false
}

func selectWWANNetworkInterface(sysRoot, index string) string {
	exact := "wwan" + index
	if _, err := os.Stat(filepath.Join(sysRoot, "class", "net", exact)); err == nil {
		return exact
	}
	entries, _ := os.ReadDir(filepath.Join(sysRoot, "class", "net"))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), exact) {
			return entry.Name()
		}
	}
	return ""
}

func readPCIIdentity(path, sysRoot string) (vendorID, productID string) {
	root := filepath.Clean(sysRoot)
	for current := filepath.Clean(path); current != "." && current != string(filepath.Separator); current = filepath.Dir(current) {
		vendor := strings.TrimPrefix(strings.ToLower(readTrimmed(filepath.Join(current, "vendor"))), "0x")
		device := strings.TrimPrefix(strings.ToLower(readTrimmed(filepath.Join(current, "device"))), "0x")
		if vendor != "" && device != "" {
			return vendor, device
		}
		if current == root {
			break
		}
	}
	return "", ""
}

func parseUSBInterfaceName(name string) (int, bool) {
	_, suffix, ok := strings.Cut(name, ":")
	if !ok {
		return 0, false
	}
	_, numberText, ok := strings.Cut(suffix, ".")
	if !ok || numberText == "" {
		return 0, false
	}
	number, err := strconv.ParseInt(numberText, 10, 32)
	return int(number), err == nil
}

func readHexByte(path string) (int, error) {
	value := readTrimmed(path)
	number, err := strconv.ParseUint(value, 16, 8)
	return int(number), err
}

func readTrimmed(path string) string {
	value, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func scanUSBInterface(root string) (ttyNames, qmiControls, networkInterfaces []string) {
	ttySeen := make(map[string]struct{})
	qmiSeen := make(map[string]struct{})
	netSeen := make(map[string]struct{})
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := entry.Name()
		switch {
		case entry.IsDir() && (strings.HasPrefix(name, "ttyUSB") || strings.HasPrefix(name, "ttyACM")):
			ttySeen[name] = struct{}{}
		case strings.HasPrefix(name, "cdc-wdm"):
			qmiSeen[name] = struct{}{}
		case entry.IsDir() && filepath.Base(filepath.Dir(path)) == "net":
			netSeen[name] = struct{}{}
		}
		return nil
	})
	for name := range ttySeen {
		ttyNames = append(ttyNames, name)
	}
	for name := range qmiSeen {
		qmiControls = append(qmiControls, name)
	}
	for name := range netSeen {
		networkInterfaces = append(networkInterfaces, name)
	}
	sort.Strings(ttyNames)
	sort.Strings(qmiControls)
	sort.Strings(networkInterfaces)
	return
}

func readSerialAliases(root string) map[string]string {
	result := make(map[string]string)
	entries, err := os.ReadDir(root)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		target, err := os.Readlink(path)
		if err != nil {
			continue
		}
		name := filepath.Base(filepath.Clean(target))
		if strings.HasPrefix(name, "ttyUSB") || strings.HasPrefix(name, "ttyACM") {
			if existing := result[name]; existing == "" || path < existing {
				result[name] = path
			}
		}
	}
	return result
}

// qmiWWANBoundDevices returns the set of USB device paths (for example "1-6"
// or the hub-attached "1-4.3.2") that currently have at least one interface
// bound to the kernel's qmi_wwan driver. Interface entries in the driver
// directory are named "<device-path>:<interface>.<altsetting>", so the part
// before the first colon is the owning USB device. The qmi_wwan driver only
// binds Qualcomm QMI control interfaces, so membership doubles as a vendor-
// neutral "this is a live QMI modem" signal.
func (d *SysFSDiscoverer) qmiWWANBoundDevices() map[string]struct{} {
	driverRoot := filepath.Join(d.SysRoot, "bus", "usb", "drivers", "qmi_wwan")
	entries, err := os.ReadDir(driverRoot)
	if err != nil {
		return nil
	}
	devices := make(map[string]struct{})
	for _, entry := range entries {
		// The driver directory also holds control files (bind, unbind, uevent,
		// module, new_id, ...); only names containing a colon are interfaces.
		deviceName, _, ok := strings.Cut(entry.Name(), ":")
		if !ok || deviceName == "" {
			continue
		}
		devices[deviceName] = struct{}{}
	}
	return devices
}

func candidateID(vendorID, productID, serialNumber, usbName string) string {
	prefix := "usb-" + sanitizeID(vendorID)
	serialNumber = strings.TrimSpace(serialNumber)
	if serialNumber != "" && !strings.EqualFold(serialNumber, "android") {
		// A surprising number of EC20/EC25 carrier boards expose the same
		// factory/default USB serial number.  The device manager is keyed by this
		// value, so using the serial alone silently collapsed two modems connected
		// to the same hub into one entry.  Include the physical USB topology in the
		// discovery key; configured devices remain stable through ATMapper's
		// USB-path/IMEI matching even when Linux renumbers ttyUSB nodes.
		return prefix + "-" + sanitizeID(serialNumber+"-"+usbName)
	}
	return prefix + "-" + sanitizeID(productID+"-"+usbName)
}

func sanitizeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_' {
			result.WriteRune(character)
		} else {
			result.WriteByte('-')
		}
	}
	return strings.Trim(result.String(), "-")
}

func assignQuectelPortRoles(ports []Port) {
	// ttyUSB numbers are allocated globally by Linux. A second modem therefore
	// commonly exposes ttyUSB4..ttyUSB7, so absolute tty names cannot identify
	// the logical AT port. Infer the Quectel composition once per physical USB
	// device and assign roles from that device's interface numbers.
	base := 0x02
	for _, port := range ports {
		if port.InterfaceNumber <= 0x01 {
			base = 0x00
			break
		}
	}
	for index := range ports {
		switch ports[index].InterfaceNumber - base {
		case 0:
			ports[index].Role = PortRoleDiagnostic
		case 1:
			ports[index].Role = PortRoleNMEA
		case 2:
			ports[index].Role = PortRoleAT
		case 3:
			ports[index].Role = PortRoleModem
		default:
			ports[index].Role = PortRoleUnknown
		}
	}
}

func quecPortRole(interfaceNumber int, name string) PortRole {
	// Initial best effort. assignQuectelPortRoles replaces this once every
	// interface belonging to the same physical modem has been collected.
	switch interfaceNumber {
	case 0x00:
		return PortRoleDiagnostic
	case 0x01:
		return PortRoleNMEA
	case 0x02:
		return PortRoleAT
	case 0x03:
		return PortRoleModem
	default:
		if name == "ttyUSB2" {
			return PortRoleAT
		}
		return PortRoleUnknown
	}
}

func selectATPort(ports []Port) Port {
	var best Port
	bestScore := 0
	for _, port := range ports {
		score := 0
		switch {
		case port.Role == PortRoleAT:
			score = 120
		case port.Name == "ttyUSB2":
			score = 100
		case port.InterfaceNumber == 0x04:
			score = 90
		case port.InterfaceNumber == 0x05:
			score = 40
		case port.Role == PortRoleModem:
			score = 30
		}
		if score > bestScore {
			best, bestScore = port, score
		}
	}
	if bestScore <= 0 {
		return Port{}
	}
	return best
}

type unsupportedDiscoverer struct{}

func (unsupportedDiscoverer) Discover(context.Context) ([]Candidate, error) {
	return nil, ErrUnsupportedPlatform
}
