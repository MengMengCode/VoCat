package modem

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestATPortLeaseIsExclusiveUntilRelease(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "ttyUSB0")
	alias := filepath.Join(directory, "serial-by-id")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a symlink requires Windows developer mode or elevation: %v", err)
		}
		t.Fatal(err)
	}

	release, ok := tryAcquireATPort(target)
	if !ok {
		t.Fatal("first lease acquisition failed")
	}
	if secondRelease, secondOK := tryAcquireATPort(alias); secondOK {
		secondRelease()
		t.Fatal("symlink alias acquired a concurrent lease")
	}

	release()
	thirdRelease, thirdOK := tryAcquireATPort(alias)
	if !thirdOK {
		t.Fatal("lease was not available after release")
	}
	thirdRelease()
}

func TestSerialOpenerReleasesATPortLeaseOnOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-tty")
	if _, err := (SerialOpener{}).Open(context.Background(), Port{Path: path}); err == nil {
		t.Fatal("Open succeeded for a missing serial port")
	}

	release, ok := tryAcquireATPort(path)
	if !ok {
		t.Fatal("SerialOpener retained the AT port lease after open failure")
	}
	release()
}

func TestIsNativeWWANATPath(t *testing.T) {
	for _, path := range []string{
		"/dev/wwan0at0",
		"/dev/wwan12at3",
	} {
		if !isNativeWWANATPath(path) {
			t.Errorf("isNativeWWANATPath(%q) = false", path)
		}
	}
	for _, path := range []string{
		"/dev/wwan0qmi0",
		"/dev/ttyUSB2",
		"/tmp/wwan-at",
		"/dev/wwanat0",
	} {
		if isNativeWWANATPath(path) {
			t.Errorf("isNativeWWANATPath(%q) = true", path)
		}
	}
}
