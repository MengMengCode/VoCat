//go:build linux

package modem

import (
	"bytes"
	"errors"
	"fmt"
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

func TestNativeWWANATTransportBlockingReaderRestartsAfterError(t *testing.T) {
	readFD, writeFD := socketpair(t)
	defer unix.Close(writeFD)
	transport := &nativeWWANATTransport{
		fd:          readFD,
		readTimeout: -1,
		blocking:    true,
		readResults: make(chan readResult, 16),
	}
	transport.ensureReader()
	if !transport.readerRunning.Load() {
		t.Fatal("reader not running after ensureReader")
	}
	// Closing the fd makes the outstanding read fail with EBADF, which ends
	// persistentReader. A pending Read must observe that error, and a later
	// ensureReader must be able to start a replacement reader; a one-shot
	// start would leave every later Read timing out on a channel nobody
	// feeds.
	if err := unix.Close(readFD); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	if _, err := transport.Read(buffer); err == nil {
		t.Fatal("Read after reader death = nil error; want the read error")
	}
	deadline := time.Now().Add(2 * time.Second)
	for transport.readerRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatal("reader still marked running after fatal read error")
		}
		time.Sleep(5 * time.Millisecond)
	}
	transport.ensureReader()
	if !transport.readerRunning.Load() {
		t.Fatal("reader did not restart after ensureReader")
	}
	transport.closed.Store(true)
}

func TestNativeWWANATTransportBlockingReaderEOFStopsReader(t *testing.T) {
	readFD, writeFD := socketpair(t)
	transport := &nativeWWANATTransport{
		fd:          readFD,
		readTimeout: -1,
		blocking:    true,
		readResults: make(chan readResult, 16),
	}
	transport.ensureReader()
	// Closing the peer makes the outstanding read return (0, nil): the
	// character-device form of EOF. The reader must surface io.EOF and
	// stop instead of queueing empty results and spinning.
	if err := unix.Close(writeFD); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	if _, err := transport.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("Read = %v; want io.EOF", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for transport.readerRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatal("reader still running after EOF")
		}
		time.Sleep(5 * time.Millisecond)
	}
	transport.closed.Store(true)
}

func TestNativeWWANATTransportBlockingReaderDoesNotDropWhenQueueFull(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	readFD, writeFD := fds[0], fds[1]
	defer unix.Close(writeFD)
	transport := &nativeWWANATTransport{
		fd:          readFD,
		readTimeout: 5 * time.Second,
		blocking:    true,
		readResults: make(chan readResult, 16),
	}
	defer transport.Close()
	transport.ensureReader()

	const chunks = 24 // More than the result queue holds (16).
	var want []byte
	for i := 0; i < chunks; i++ {
		payload := []byte(fmt.Sprintf("chunk-%02d;", i))
		want = append(want, payload...)
		if _, err := unix.Write(writeFD, payload); err != nil {
			t.Fatal(err)
		}
	}
	// Give the reader time to fill the queue and block in deliver; with a
	// dropping reader the tail chunks would already be lost at this point.
	time.Sleep(100 * time.Millisecond)
	var got []byte
	buffer := make([]byte, 64)
	for len(got) < len(want) {
		n, err := transport.Read(buffer)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		got = append(got, buffer[:n]...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stream = %q; want %q", got, want)
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
