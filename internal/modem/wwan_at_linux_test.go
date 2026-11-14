//go:build linux

package modem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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

func TestNativeWWANATTransportBlockingSmallReadsAndEOF(t *testing.T) {
	pair := newNativeWWANATPair(t)
	if err := pair.transport.SetReadTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("AT response\r\n"), 79)
	if err := writeNativeWWANATPeer(context.Background(), pair.readPeer, want); err != nil {
		t.Fatal(err)
	}
	waitNativeWWANATPipeBytes(t, pair.transport.reader.output, 1)
	if n, err := pair.transport.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero-length Read = %d, %v", n, err)
	}
	if err := unix.Close(pair.readPeer); err != nil {
		t.Fatal(err)
	}
	pair.readPeer = -1
	got := readNativeWWANATBytes(t, pair.transport, len(want), 7)
	if !bytes.Equal(got, want) {
		t.Fatalf("small reads changed stream: received %d bytes, want %d", len(got), len(want))
	}
	// A race-instrumented helper delays os.Exit to finish race reporting.
	// Establish process exit before checking EOF instead of racing that delay
	// against the transport's one-second data-read deadline.
	select {
	case <-pair.transport.reader.done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not exit after peer EOF")
	}
	for range 2 {
		if n, err := pair.transport.Read(make([]byte, 16)); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("Read after peer EOF = %d, %v", n, err)
		}
	}
}

func TestNativeWWANATTransportBlockingReadTimeout(t *testing.T) {
	pair := newNativeWWANATPair(t)
	if err := pair.transport.SetReadTimeout(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if n, err := pair.transport.Read(make([]byte, 16)); n != 0 || err != nil {
		t.Fatalf("Read timeout = %d, %v; want 0, nil", n, err)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("read timeout took %s", elapsed)
	}
	if err := writeNativeWWANATPeer(context.Background(), pair.readPeer, []byte("OK\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := pair.transport.SetReadTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if got := readNativeWWANATBytes(t, pair.transport, 4, 16); string(got) != "OK\r\n" {
		t.Fatalf("data after timeout = %q", got)
	}
}

func TestNativeWWANATTransportCloseWakesInfiniteRead(t *testing.T) {
	pair := newNativeWWANATPair(t)
	readDone := make(chan error, 1)
	go func() {
		_, err := pair.transport.Read(make([]byte, 16))
		readDone <- err
	}()
	select {
	case err := <-readDone:
		t.Fatalf("Read returned before data or Close: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	closeNativeWWANATWithin(t, pair.transport)
	select {
	case err := <-readDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("closed Read = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read remained blocked after Close")
	}
}

func TestNativeWWANATTransportCloseReleasesBackpressuredWrite(t *testing.T) {
	pair := newNativeWWANATPair(t)
	flags, err := unix.FcntlInt(uintptr(pair.transport.fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK == 0 {
		t.Fatalf("write descriptor lost nonblocking mode: flags=%#x err=%v", flags, err)
	}
	fill := make([]byte, 8192)
	for {
		_, err := unix.Write(pair.transport.fd, fill)
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := pair.transport.Write([]byte("AT\r"))
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("full output unexpectedly accepted a write: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	closeNativeWWANATWithin(t, pair.transport)
	select {
	case err := <-writeDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("closed Write = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Write remained blocked after Close")
	}
}

func TestNativeWWANATTransportCloseReapsReaderAcrossReopens(t *testing.T) {
	for range 8 {
		pair := newNativeWWANATPair(t)
		closeNativeWWANATWithin(t, pair.transport)
		select {
		case <-pair.transport.reader.done:
		default:
			t.Fatalf("reader process %d was not reaped", pair.transport.reader.process.Pid)
		}
		if err := pair.transport.Close(); err != nil {
			t.Fatalf("repeated Close: %v", err)
		}
		pair.closePeers()
	}
}

func TestNativeWWANATTransportResetDiscardsUnreadBytesAfterTimeout(t *testing.T) {
	pair := newNativeWWANATPair(t)
	if err := pair.transport.SetReadTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := writeNativeWWANATPeer(context.Background(), pair.readPeer, []byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	waitNativeWWANATPipeBytes(t, pair.transport.reader.output, 6)
	if got := readNativeWWANATBytes(t, pair.transport, 2, 2); string(got) != "ab" {
		t.Fatalf("prefix = %q", got)
	}
	if err := pair.transport.ResetInputBuffer(); err != nil {
		t.Fatal(err)
	}
	if err := pair.transport.SetReadTimeout(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if n, err := pair.transport.Read(make([]byte, 16)); n != 0 || err != nil {
		t.Fatalf("Reset left unread bytes: %d, %v", n, err)
	}
	// Reset must also work after the prior Read left its pipe deadline expired.
	if err := writeNativeWWANATPeer(context.Background(), pair.readPeer, []byte("stale")); err != nil {
		t.Fatal(err)
	}
	waitNativeWWANATPipeBytes(t, pair.transport.reader.output, 5)
	if err := pair.transport.ResetInputBuffer(); err != nil {
		t.Fatalf("Reset after expired deadline: %v", err)
	}
	if err := writeNativeWWANATPeer(context.Background(), pair.readPeer, []byte("OK")); err != nil {
		t.Fatal(err)
	}
	if err := pair.transport.SetReadTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if got := readNativeWWANATBytes(t, pair.transport, 2, 16); string(got) != "OK" {
		t.Fatalf("input after Reset = %q", got)
	}
}

func TestNativeWWANATTransportBackpressurePreservesStream(t *testing.T) {
	pair := newNativeWWANATPair(t)
	if err := pair.transport.SetReadTimeout(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetsockoptInt(pair.readPeer, unix.SOL_SOCKET, unix.SO_SNDBUF, 4096); err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("0123456789abcdef"), 1<<16)
	sent := 0
	for sent < len(want) {
		n, err := unix.Write(pair.readPeer, want[sent:])
		if n > 0 {
			sent += n
		}
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if sent == len(want) {
		t.Fatal("test did not establish input backpressure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	writeDone, writerExited := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(writerExited)
		writeDone <- writeNativeWWANATPeer(ctx, pair.readPeer, want[sent:])
	}()
	defer func() {
		cancel()
		<-writerExited
	}()
	got := readNativeWWANATBytes(t, pair.transport, len(want), 307)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("backpressured stream changed: received %d bytes, want %d", len(got), len(want))
	}
}

type nativeWWANATPair struct {
	transport *nativeWWANATTransport
	readPeer  int
	writePeer int
}

func newNativeWWANATPair(t *testing.T) *nativeWWANATPair {
	t.Helper()
	readFD, readPeer := socketpair(t)
	writeFD, writePeer := socketpair(t)
	if err := unix.SetsockoptInt(writeFD, unix.SOL_SOCKET, unix.SO_SNDBUF, 4096); err != nil {
		t.Fatal(err)
	}
	// The constructor clears NONBLOCK only on readFD. Separate socket pairs
	// provide independent open-file descriptions, unlike dup of one descriptor.
	transport, err := newNativeWWANATTransport(readFD, writeFD)
	if err != nil {
		unix.Close(readPeer)
		unix.Close(writePeer)
		t.Fatal(err)
	}
	pair := &nativeWWANATPair{transport: transport, readPeer: readPeer, writePeer: writePeer}
	t.Cleanup(func() {
		pair.closePeers()
		closeNativeWWANATWithin(t, transport)
	})
	return pair
}

func (pair *nativeWWANATPair) closePeers() {
	for _, fd := range []*int{&pair.readPeer, &pair.writePeer} {
		if *fd >= 0 {
			unix.Close(*fd)
			*fd = -1
		}
	}
}

func closeNativeWWANATWithin(t *testing.T, transport *nativeWWANATTransport) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- transport.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not complete within 3 seconds")
	}
}

func readNativeWWANATBytes(t *testing.T, transport *nativeWWANATTransport, count, size int) []byte {
	t.Helper()
	buffer := make([]byte, size)
	got := make([]byte, 0, count)
	for len(got) < count {
		n, err := transport.Read(buffer[:min(len(buffer), count-len(got))])
		got = append(got, buffer[:n]...)
		if err != nil || n == 0 {
			t.Fatalf("Read stalled after %d of %d bytes: %v", len(got), count, err)
		}
	}
	return got
}

func writeNativeWWANATPeer(ctx context.Context, fd int, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Write(fd, data)
		if n > 0 {
			data = data[n:]
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			if _, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, 50); err != nil && !errors.Is(err, unix.EINTR) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func waitNativeWWANATPipeBytes(t *testing.T, output *os.File, want int) {
	t.Helper()
	connection, err := output.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var available int
		var queryErr error
		if err := connection.Control(func(fd uintptr) { available, queryErr = unix.IoctlGetInt(int(fd), unix.TIOCINQ) }); err != nil {
			t.Fatal(err)
		}
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if available >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pipe received %d bytes, want at least %d", available, want)
		}
		time.Sleep(time.Millisecond)
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
