//go:build linux

package modem

import (
	"errors"
	"io"
	"testing"

	"golang.org/x/sys/unix"
)

// TestNativeWWANATTransportDrainDiscardsPendingBytes verifies Drain discards
// every byte already buffered on the transport. A command that timed out (e.g.
// AT+CGSN on an MHI modem that never answers OK) can leave its late reply in
// the input buffer; the next command's Drain must clear it, however much data
// is pending, before the session writes the new command.
func TestNativeWWANATTransportDrainDiscardsPendingBytes(t *testing.T) {
	readFD, writeFD := socketpair(t)
	defer unix.Close(writeFD)

	// More than one 4096-byte Drain read: a slow CGSN reply (echo + IMEI +
	// trailing CRLF) can exceed a single buffer.
	payload := make([]byte, 12000)
	for index := range payload {
		payload[index] = byte('A' + index%26)
	}
	payload = append(payload, []byte("\r\n+CGSN: 357091089453326\r\n")...)
	if _, err := unix.Write(writeFD, payload); err != nil {
		t.Fatalf("seed stale bytes: %v", err)
	}

	transport := &nativeWWANATTransport{fd: readFD, readTimeout: -1}
	if err := transport.Drain(); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	assertNoPendingBytes(t, readFD, "after Drain")

	// Draining a clean transport is a fast no-op that must not block or error.
	if err := transport.Drain(); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
}

// TestNativeWWANATTransportDrainRejectsClosedTransport covers the guard that
// keeps a poisoned session from draining a wedged, already-closed fd.
func TestNativeWWANATTransportDrainRejectsClosedTransport(t *testing.T) {
	readFD, writeFD := socketpair(t)
	defer unix.Close(writeFD)
	transport := &nativeWWANATTransport{fd: readFD, readTimeout: -1}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := transport.Drain(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Drain after Close = %v, want ErrClosedPipe", err)
	}
}

func socketpair(t *testing.T) (int, int) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fds[0], fds[1]
}

func assertNoPendingBytes(t *testing.T, fd int, context string) {
	t.Helper()
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	ready, err := unix.Poll(fds, 0)
	if err != nil {
		t.Fatalf("poll %s: %v", context, err)
	}
	if ready != 0 {
		t.Fatalf("%s: fd still readable", context)
	}
}
