//go:build linux

package modem

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeWWANATTransportReadEOF(t *testing.T) {
	fd, peer := socketpair(t)
	transport := &nativeWWANATTransport{fd: fd, readTimeout: 20 * time.Millisecond}
	defer transport.Close()
	defer unix.Close(peer)
	buffer := make([]byte, 64)
	if n, err := transport.Read(buffer); n != 0 || err != nil {
		t.Fatalf("Read timeout = %d, %v; want 0, nil", n, err)
	}
	payload := []byte("\r\nRING\r\n")
	if _, err := unix.Write(peer, payload); err != nil {
		t.Fatal(err)
	}
	if err := unix.Shutdown(peer, unix.SHUT_RDWR); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		buffer []byte
		want   []byte
		err    error
	}{{buffer, payload, nil}, {nil, nil, nil}, {buffer, nil, io.EOF}} {
		n, err := transport.Read(step.buffer)
		if !errors.Is(err, step.err) || !bytes.Equal(step.buffer[:n], step.want) {
			t.Fatalf("Read = %q, %v; want %q, %v", step.buffer[:n], err, step.want, step.err)
		}
	}
}

func TestNativeWWANATTransportDrainPreservesPendingBytes(t *testing.T) {
	readFD, writeFD := socketpair(t)
	defer unix.Close(writeFD)
	transport := &nativeWWANATTransport{fd: readFD, readTimeout: -1}
	defer transport.Close()

	payload := []byte("\r\nRING\r\n+CFUN: 1\r\nOK\r\n")
	if _, err := unix.Write(writeFD, payload); err != nil {
		t.Fatal(err)
	}
	if err := transport.Drain(); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	buffer := make([]byte, len(payload))
	count, err := unix.Read(readFD, buffer)
	if err != nil || count != len(payload) || !bytes.Equal(buffer, payload) {
		t.Fatalf("input after Drain = %q, %d, %v; want %q", buffer, count, err, payload)
	}
	if err := transport.Drain(); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
}

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
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fds[0], fds[1]
}
