//go:build linux

package modem

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeWWANATReaderInvalidDescriptorDoesNotReserveDevice(t *testing.T) {
	nativeWWANATReaders.Lock()
	before := len(nativeWWANATReaders.active)
	nativeWWANATReaders.Unlock()
	for _, fd := range []int{-1, 1 << 30} {
		reader, err := startNativeWWANATReader(fd)
		if err == nil || reader != nil {
			t.Fatalf("invalid descriptor %d: reader=%v err=%v", fd, reader, err)
		}
	}
	nativeWWANATReaders.Lock()
	after := len(nativeWWANATReaders.active)
	nativeWWANATReaders.Unlock()
	if after != before {
		t.Fatalf("invalid descriptors changed reservations: before=%d after=%d", before, after)
	}
}

func TestNativeWWANATReaderReservationLastsUntilProcessExit(t *testing.T) {
	readFD, peerFD := socketpair(t)
	defer unix.Close(peerFD)
	if err := unix.SetNonblock(readFD, false); err != nil {
		_ = unix.Close(readFD)
		t.Fatal(err)
	}
	retained, err := unix.FcntlInt(uintptr(readFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(readFD)
		t.Fatal(err)
	}
	defer unix.Close(retained)
	reader, err := startNativeWWANATReader(readFD)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	duplicate, err := unix.FcntlInt(uintptr(retained), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	other, err := startNativeWWANATReader(duplicate)
	if other != nil || !errors.Is(err, ErrATPortBusy) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("second reader: reader=%v err=%v", other, err)
	}
	if _, err := unix.FcntlInt(uintptr(duplicate), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("rejected descriptor was not consumed: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reader.done:
	default:
		t.Fatal("reader Close returned before its process was reaped")
	}
	reopened, err := unix.FcntlInt(uintptr(retained), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	next, err := startNativeWWANATReader(reopened)
	if err != nil {
		t.Fatalf("device remained reserved after process exit: %v", err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}
