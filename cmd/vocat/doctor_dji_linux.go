//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	djiVendorID  = "2ca3"
	djiProductID = "4006"
	djiQMIIndex  = 4
)

type usbControlTransfer struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
	Timeout     uint32
	Data        uintptr
}

func repairDJIQMI(ctx context.Context) (djiQMIRepairResult, error) {
	return retryDJIQMI(ctx, 3, 500*time.Millisecond, func(attemptContext context.Context) (djiQMIRepairResult, error) {
		return repairDJIQMIAt(attemptContext, "/sys", "/dev")
	})
}

func retryDJIQMI(
	ctx context.Context,
	maxAttempts int,
	delay time.Duration,
	attempt func(context.Context) (djiQMIRepairResult, error),
) (djiQMIRepairResult, error) {
	var result djiQMIRepairResult
	var err error
	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		result, err = attempt(ctx)
		result.Attempts = attemptNumber
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(time.Duration(attemptNumber) * delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
	return result, fmt.Errorf("failed after %d DTR repair attempt(s): %w", result.Attempts, err)
}

func repairDJIQMIAt(ctx context.Context, sysRoot, devRoot string) (result djiQMIRepairResult, returnErr error) {
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	entries, err := os.ReadDir(usbRoot)
	if err != nil {
		return result, fmt.Errorf("read USB topology: %w", err)
	}
	var usbNames []string
	for _, entry := range entries {
		devicePath := filepath.Join(usbRoot, entry.Name())
		vendor, vendorErr := readTrimmedFile(filepath.Join(devicePath, "idVendor"))
		product, productErr := readTrimmedFile(filepath.Join(devicePath, "idProduct"))
		if vendorErr == nil && productErr == nil &&
			strings.EqualFold(vendor, djiVendorID) && strings.EqualFold(product, djiProductID) {
			usbNames = append(usbNames, entry.Name())
		}
	}
	if len(usbNames) != 1 {
		return result, fmt.Errorf("expected exactly one DJI %s:%s USB device, found %d", djiVendorID, djiProductID, len(usbNames))
	}
	result.USBName = usbNames[0]
	result.Interface = fmt.Sprintf("%s:1.%d", result.USBName, djiQMIIndex)
	devicePath := filepath.Join(usbRoot, result.USBName)
	interfacePath := filepath.Join(usbRoot, result.Interface)
	if _, err := os.Stat(interfacePath); err != nil {
		return result, fmt.Errorf("DJI QMI interface %s unavailable: %w", result.Interface, err)
	}

	busNumber, err := readUSBNumber(filepath.Join(devicePath, "busnum"))
	if err != nil {
		return result, err
	}
	deviceNumber, err := readUSBNumber(filepath.Join(devicePath, "devnum"))
	if err != nil {
		return result, err
	}
	result.USBDevice = filepath.Join(devRoot, "bus", "usb", fmt.Sprintf("%03d", busNumber), fmt.Sprintf("%03d", deviceNumber))

	result.OriginalDriver = usbInterfaceDriver(interfacePath)
	if result.OriginalDriver != "" && result.OriginalDriver != "option" && result.OriginalDriver != "qmi_wwan" {
		return result, fmt.Errorf("refusing to replace unexpected interface driver %q", result.OriginalDriver)
	}
	driversRoot := filepath.Join(sysRoot, "bus", "usb", "drivers")
	if _, err := os.Stat(filepath.Join(driversRoot, "qmi_wwan")); err != nil {
		modprobe, lookErr := exec.LookPath("modprobe")
		if lookErr != nil {
			return result, errors.New("qmi_wwan is not loaded and modprobe is unavailable")
		}
		if output, loadErr := exec.CommandContext(ctx, modprobe, "qmi_wwan").CombinedOutput(); loadErr != nil {
			return result, fmt.Errorf("load qmi_wwan: %w: %s", loadErr, strings.TrimSpace(string(output)))
		}
	}

	interfaceDetached := false
	restoreOriginal := func() {
		if !interfaceDetached {
			return
		}
		if currentDriver := usbInterfaceDriver(interfacePath); currentDriver != "" {
			_ = writeSysfs(filepath.Join(driversRoot, currentDriver, "unbind"), result.Interface)
		}
		if result.OriginalDriver != "" {
			_ = writeSysfs(filepath.Join(driversRoot, result.OriginalDriver, "bind"), result.Interface)
		}
	}
	defer func() {
		if returnErr != nil {
			restoreOriginal()
		}
	}()
	if result.OriginalDriver != "" {
		if err := writeSysfs(filepath.Join(driversRoot, result.OriginalDriver, "unbind"), result.Interface); err != nil {
			return result, fmt.Errorf("unbind %s from %s: %w", result.OriginalDriver, result.Interface, err)
		}
		interfaceDetached = true
	}
	if err := assertUSBDTR(result.USBDevice, djiQMIIndex); err != nil {
		return result, err
	}

	bindPath := filepath.Join(driversRoot, "qmi_wwan", "bind")
	if err := writeSysfs(bindPath, result.Interface); err != nil {
		newIDErr := writeSysfs(filepath.Join(driversRoot, "qmi_wwan", "new_id"), djiVendorID+" "+djiProductID)
		if newIDErr != nil && !errors.Is(newIDErr, syscall.EEXIST) {
			return result, fmt.Errorf("register DJI qmi_wwan dynamic ID after bind failure %v: %w", err, newIDErr)
		}
		if usbInterfaceDriver(interfacePath) != "qmi_wwan" {
			if retryErr := writeSysfs(bindPath, result.Interface); retryErr != nil {
				return result, fmt.Errorf("bind qmi_wwan to %s: %w", result.Interface, retryErr)
			}
		}
	}
	if driver := usbInterfaceDriver(interfacePath); driver != "qmi_wwan" {
		return result, fmt.Errorf("interface %s driver is %q after qmi_wwan bind", result.Interface, driver)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		result.ControlDevice = firstDeviceNode(filepath.Join(interfacePath, "usbmisc"), devRoot, "cdc-wdm")
		result.NetworkInterface = firstEntryName(filepath.Join(interfacePath, "net"), "")
		if result.ControlDevice != "" {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if time.Now().After(deadline) {
			return result, fmt.Errorf("qmi_wwan bound but no cdc-wdm node appeared for %s", result.Interface)
		}
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	qmicli, err := exec.LookPath("qmicli")
	if err != nil {
		return result, errors.New("qmicli is required to verify DJI QMI readiness after DTR repair")
	}
	probeContext, cancelProbe := context.WithTimeout(ctx, 8*time.Second)
	output, probeErr := exec.CommandContext(probeContext, qmicli, "-d", result.ControlDevice, "--dms-get-operating-mode").CombinedOutput()
	cancelProbe()
	result.QMIProbe = strings.TrimSpace(string(output))
	if probeErr != nil {
		if probeContext.Err() != nil {
			probeErr = errors.Join(probeErr, probeContext.Err())
		}
		return result, fmt.Errorf("DMS readiness check after DTR repair: %w: %s", probeErr, result.QMIProbe)
	}
	interfaceDetached = false
	return result, nil
}

func assertUSBDTR(devicePath string, interfaceIndex int) error {
	fd, err := unix.Open(devicePath, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open USB device %s: %w", devicePath, err)
	}
	defer unix.Close(fd)
	if err := setUSBControlLineState(fd, interfaceIndex, false); err != nil {
		return fmt.Errorf("clear CDC DTR on %s interface %d: %w", devicePath, interfaceIndex, err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := setUSBControlLineState(fd, interfaceIndex, true); err != nil {
		return fmt.Errorf("assert CDC DTR on %s interface %d: %w", devicePath, interfaceIndex, err)
	}
	// QDC507 acknowledges the control transfer before its QMI firmware is ready.
	time.Sleep(time.Second)
	return nil
}

func setUSBControlLineState(fd, interfaceIndex int, dtr bool) error {
	var value uint16
	if dtr {
		value = 1 // USB_CDC_CTRL_DTR
	}
	transfer := usbControlTransfer{
		RequestType: 0x21, // host-to-device, class, interface
		Request:     0x22, // USB_CDC_REQ_SET_CONTROL_LINE_STATE
		Value:       value,
		Index:       uint16(interfaceIndex),
		Timeout:     5000,
	}
	const ioctlDirectionReadWrite = uintptr(3)
	request := ioctlDirectionReadWrite<<30 |
		uintptr(unsafe.Sizeof(transfer))<<16 |
		uintptr('U')<<8
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(unsafe.Pointer(&transfer)))
	if errno != 0 {
		return errno
	}
	return nil
}

func readTrimmedFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readUSBNumber(path string) (int, error) {
	value, err := readTrimmedFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > 999 {
		return 0, fmt.Errorf("invalid %s %q", filepath.Base(path), value)
	}
	return number, nil
}

func usbInterfaceDriver(interfacePath string) string {
	resolved, err := filepath.EvalSymlinks(filepath.Join(interfacePath, "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(resolved)
}

func writeSysfs(path, value string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(value)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func firstDeviceNode(directory, devRoot, prefix string) string {
	name := firstEntryName(directory, prefix)
	if name == "" {
		return ""
	}
	return filepath.Join(devRoot, name)
}

func firstEntryName(directory, prefix string) string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			return entry.Name()
		}
	}
	return ""
}
