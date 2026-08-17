//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestDJIUSBControlTransferLayout(t *testing.T) {
	var transfer usbControlTransfer
	if got := unsafe.Sizeof(transfer); got != 24 {
		t.Fatalf("usbControlTransfer size = %d, want 24", got)
	}
	if transfer.RequestType != 0 || transfer.Request != 0 {
		t.Fatal("zero-value transfer unexpectedly initialized")
	}
}

func TestReadUSBNumber(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "busnum")
	if err := os.WriteFile(path, []byte("12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readUSBNumber(path); err != nil || got != 12 {
		t.Fatalf("readUSBNumber() = %d, %v, want 12, nil", got, err)
	}
	if err := os.WriteFile(path, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readUSBNumber(path); err == nil {
		t.Fatal("readUSBNumber(0) unexpectedly succeeded")
	}
}

func TestWriteSysfsDoesNotCreateMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if err := writeSysfs(path, "value"); err == nil {
		t.Fatal("writeSysfs(missing) unexpectedly succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing sysfs path was created: %v", err)
	}
}

func TestRepairDJIQMIRequiresQMICLIBeforeUSBAccess(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := repairDJIQMI(context.Background())
	if err == nil {
		t.Fatal("repairDJIQMI() unexpectedly succeeded without qmicli")
	}
	if !strings.Contains(err.Error(), "qmicli is required") || !strings.Contains(err.Error(), "libqmi-utils") {
		t.Fatalf("repairDJIQMI() error = %q, want an actionable qmicli prerequisite error", err)
	}
	if strings.Contains(err.Error(), "DTR repair attempt") || strings.Contains(err.Error(), "USB topology") {
		t.Fatalf("repairDJIQMI() touched the repair path before checking qmicli: %v", err)
	}
}

func TestDJISerialInterfaceLayout(t *testing.T) {
	if djiFirstSerialIndex != 0 || djiLastSerialIndex != 3 || djiATIndex != 2 || djiQMIIndex != 4 {
		t.Fatalf(
			"DJI interface layout = serial %d-%d, AT %d, QMI %d; want serial 0-3, AT 2, QMI 4",
			djiFirstSerialIndex,
			djiLastSerialIndex,
			djiATIndex,
			djiQMIIndex,
		)
	}
}

func TestBindDJISerialInterfacesAlreadyCorrect(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	devRoot := filepath.Join(root, "dev")
	usbRoot := filepath.Join(sysRoot, "bus", "usb", "devices")
	driversRoot := filepath.Join(sysRoot, "bus", "usb", "drivers")
	optionRoot := filepath.Join(driversRoot, "option")
	if err := os.MkdirAll(optionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := djiFirstSerialIndex; index <= djiLastSerialIndex; index++ {
		interfacePath := filepath.Join(usbRoot, fmt.Sprintf("1-1:1.%d", index))
		if err := os.MkdirAll(filepath.Join(interfacePath, fmt.Sprintf("ttyUSB%d", index)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(optionRoot, filepath.Join(interfacePath, "driver")); err != nil {
			t.Fatal(err)
		}
	}

	interfaces, devices, atDevice, err := bindDJISerialInterfaces(
		context.Background(),
		sysRoot,
		devRoot,
		usbRoot,
		driversRoot,
		"1-1",
	)
	if err != nil {
		t.Fatalf("bindDJISerialInterfaces() error = %v", err)
	}
	if len(interfaces) != 4 || interfaces[2] != "1-1:1.2" {
		t.Fatalf("interfaces = %#v, want four interfaces with AT at 1-1:1.2", interfaces)
	}
	if len(devices) != 4 || devices[2] != filepath.Join(devRoot, "ttyUSB2") {
		t.Fatalf("devices = %#v, want four devices with AT at ttyUSB2", devices)
	}
	if atDevice != filepath.Join(devRoot, "ttyUSB2") {
		t.Fatalf("AT device = %q, want %q", atDevice, filepath.Join(devRoot, "ttyUSB2"))
	}
}

func TestRetryDJIQMISucceedsAfterTransientFailures(t *testing.T) {
	attempts := 0
	result, err := retryDJIQMI(context.Background(), 3, time.Millisecond, func(context.Context) (djiQMIRepairResult, error) {
		attempts++
		if attempts < 3 {
			return djiQMIRepairResult{}, errors.New("transient QMI timeout")
		}
		return djiQMIRepairResult{ControlDevice: "/dev/cdc-wdm0"}, nil
	})
	if err != nil {
		t.Fatalf("retryDJIQMI() error = %v", err)
	}
	if attempts != 3 || result.Attempts != 3 {
		t.Fatalf("attempts = %d, result.Attempts = %d, want 3", attempts, result.Attempts)
	}
}

func TestRetryDJIQMIStopsAfterBoundedAttempts(t *testing.T) {
	attempts := 0
	_, err := retryDJIQMI(context.Background(), 2, time.Millisecond, func(context.Context) (djiQMIRepairResult, error) {
		attempts++
		return djiQMIRepairResult{}, errors.New("persistent failure")
	})
	if err == nil {
		t.Fatal("retryDJIQMI() unexpectedly succeeded")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}
