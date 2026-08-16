//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
