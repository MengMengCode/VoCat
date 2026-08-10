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

const quectelVendorID = "2c7c"

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
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("discover Quectel USB devices: %w", err)
		}
		entries = nil
	}

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
		if vendorID != quectelVendorID {
			continue
		}

		state := devices[deviceName]
		if state == nil {
			productID := strings.ToLower(readTrimmed(filepath.Join(resolvedDevice, "idProduct")))
			serialNumber := readTrimmed(filepath.Join(resolvedDevice, "serial"))
			state = &discoveredUSBDevice{
				candidate: Candidate{
					ID:           candidateID(productID, serialNumber, deviceName),
					VendorID:     vendorID,
					ProductID:    productID,
					Manufacturer: readTrimmed(filepath.Join(resolvedDevice, "manufacturer")),
					Product:      readTrimmed(filepath.Join(resolvedDevice, "product")),
					SerialNumber: serialNumber,
					USBPath:      devicePath,
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
				StablePath:      aliases[name],
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
		result = append(result, state.candidate)
	}
	for _, native := range d.discoverNativeWWAN() {
		merged := false
		for index := range result {
			if result[index].NetworkInterface == "" ||
				result[index].NetworkInterface != native.NetworkInterface {
				continue
			}
			if result[index].QMIControl == "" {
				result[index].QMIControl = native.QMIControl
			}
			if !result[index].HasATPort() && native.HasATPort() {
				result[index].ATPort = native.ATPort
				result[index].Ports = append(result[index].Ports, native.Ports...)
			}
			merged = true
			break
		}
		if !merged {
			result = append(result, native)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// discoverNativeWWAN finds embedded Qualcomm/OpenStick-style modems exposed by
// Linux's WWAN class. Unlike USB modems, these ports live directly under
// /sys/class/wwan and use /dev/wwan0at0 plus /dev/wwan0qmi0 instead of ttyUSB
// and cdc-wdm nodes.
func (d *SysFSDiscoverer) discoverNativeWWAN() []Candidate {
	classRoot := filepath.Join(d.SysRoot, "class", "wwan")
	entries, err := os.ReadDir(classRoot)
	if err != nil {
		return nil
	}
	type nativePorts struct {
		at  []string
		qmi []string
	}
	groups := make(map[string]*nativePorts)
	for _, entry := range entries {
		parent, kind := splitNativeWWANPortName(entry.Name())
		if parent == "" || kind == "" {
			continue
		}
		path := filepath.Join(d.DevRoot, entry.Name())
		if _, err := os.Stat(path); err != nil {
			continue
		}
		group := groups[parent]
		if group == nil {
			group = &nativePorts{}
			groups[parent] = group
		}
		switch kind {
		case "at":
			group.at = append(group.at, entry.Name())
		case "qmi":
			group.qmi = append(group.qmi, entry.Name())
		}
	}

	result := make([]Candidate, 0, len(groups))
	for parent, group := range groups {
		// Keep the native modem addressable through its AT port while the old
		// OpenStick kernel is recreating DATA5_CNTL. Requiring qmi here makes the
		// whole 410 disappear and prevents the CFUN reset that restores QMI.
		if len(group.qmi) == 0 && len(group.at) == 0 {
			continue
		}
		sort.Strings(group.at)
		sort.Strings(group.qmi)
		candidate := Candidate{
			ID:           parent,
			Manufacturer: "Qualcomm",
			Product:      "410 WiFi stick",
			USBPath:      filepath.Join(classRoot, parent),
		}
		if len(group.qmi) > 0 {
			candidate.QMIControl = filepath.Join(d.DevRoot, group.qmi[0])
		}
		if _, err := os.Stat(filepath.Join(d.SysRoot, "class", "net", parent)); err == nil {
			candidate.NetworkInterface = parent
		}
		for _, name := range group.at {
			candidate.Ports = append(candidate.Ports, Port{
				Path: filepath.Join(d.DevRoot, name),
				Name: name,
				Role: PortRoleAT,
			})
		}
		if len(candidate.Ports) > 0 {
			candidate.ATPort = candidate.Ports[0]
		}
		result = append(result, candidate)
	}
	return result
}

func splitNativeWWANPortName(name string) (parent, kind string) {
	if !strings.HasPrefix(name, "wwan") {
		return "", ""
	}
	index := len("wwan")
	for index < len(name) && name[index] >= '0' && name[index] <= '9' {
		index++
	}
	if index == len("wwan") || index == len(name) {
		return "", ""
	}
	parent = name[:index]
	for _, marker := range []string{"at", "qmi"} {
		if !strings.HasPrefix(name[index:], marker) {
			continue
		}
		suffix := name[index+len(marker):]
		if suffix == "" || strings.IndexFunc(suffix, func(character rune) bool {
			return character < '0' || character > '9'
		}) >= 0 {
			return "", ""
		}
		return parent, marker
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

func candidateID(productID, serialNumber, usbName string) string {
	serialNumber = strings.TrimSpace(serialNumber)
	if serialNumber != "" && !strings.EqualFold(serialNumber, "android") {
		return "quectel-" + sanitizeID(serialNumber)
	}
	return "quectel-" + sanitizeID(productID+"-"+usbName)
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
