//go:build linux

package modem

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// nativeWWANATTransport adapts a Linux WWAN AT character device to Session's
// serial-like transport contract. WWAN ports are not TTYs, so termios ioctls
// used by ordinary serial libraries fail even though raw AT read/write works.
type nativeWWANATTransport struct {
	mu          sync.RWMutex
	fd          int
	readTimeout time.Duration
	closed      atomic.Bool
	blocking    bool

	// readerOnce guards lazy creation of readResults for transports that
	// were not built by openNativeWWANATTransport.
	readerOnce sync.Once
	// readerRunning reports whether persistentReader is alive. Downstream
	// bam-dmux drivers only hand a response to a read that is already blocked
	// in the driver, so exactly one such read must stay outstanding; it must
	// never be recreated per call. The flag is cleared when the reader exits,
	// so a later Read can start a replacement instead of timing out forever.
	readerRunning atomic.Bool
	// readerReady holds the channel the current reader closes immediately
	// before its first read, so whoever started it can wait until a read is
	// genuinely about to block in the driver before anything is written.
	readerReady atomic.Value // chan struct{}
	readResults chan readResult
}

// readResult carries one completed blocking read back to Read.
type readResult struct {
	err  error
	data []byte
}

// persistentReader keeps exactly one blocking read outstanding in the driver.
// It is started once when the port is opened, before the first AT command is
// written, because a response is only delivered to a read that is already
// waiting. ready is closed immediately before the first read, so the starter
// can wait for that point instead of merely having scheduled this goroutine.
// The descriptor is passed by value so this loop never races Close writing
// transport.fd while a read is outstanding.
func (transport *nativeWWANATTransport) persistentReader(ready chan struct{}, fd int) {
	defer transport.readerRunning.Store(false)
	buf := make([]byte, 512)
	first := true
	for {
		if transport.closed.Load() {
			if first {
				// Release anyone waiting on readiness; Read fails on closed.
				close(ready)
			}
			return
		}
		if first {
			first = false
			close(ready)
		}
		count, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if count < 0 {
			count = 0
		}
		if count == 0 && err == nil {
			// A zero-byte read with no error is how a character device
			// reports that the modem side went away. Queuing it as an
			// empty result would make Read report a timeout while this
			// loop spins; surface EOF like the poll-based path does.
			err = io.EOF
		}
		data := make([]byte, count)
		copy(data, buf[:count])
		if err != nil {
			// Clear the running flag before publishing the error, so a
			// consumer that retries Read at once can start the
			// replacement instead of waiting on a channel nobody feeds.
			transport.readerRunning.Store(false)
		}
		transport.deliver(readResult{err: err, data: data})
		if err != nil {
			return
		}
	}
}

// deliver queues one reader result for Read. It waits while the queue is
// full instead of dropping bytes: Session consumes the channel as a
// continuous AT byte stream, and a silent gap could truncate a response or
// an unsolicited event without anyone noticing. Delivery only gives up
// once the transport is closed, when nobody can consume the result anyway.
func (transport *nativeWWANATTransport) deliver(result readResult) {
	for {
		select {
		case transport.readResults <- result:
			return
		default:
		}
		if transport.closed.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ensureReader starts the persistent blocking reader if it is not running.
// It is called when the port is opened and again from Read, so a reader that
// exited on a fatal error is replaced rather than leaving every later Read
// to time out against a channel nobody feeds.
func (transport *nativeWWANATTransport) ensureReader() {
	transport.readerOnce.Do(func() {
		if transport.readResults == nil {
			transport.readResults = make(chan readResult, 16)
		}
	})
	if transport.readerRunning.CompareAndSwap(false, true) {
		ready := make(chan struct{})
		transport.readerReady.Store(ready)
		go transport.persistentReader(ready, transport.fd)
	}
}

// waitReaderReady blocks until the current reader is about to issue its
// first read, so a command written immediately afterwards cannot overtake
// it: on bam-dmux a response is only handed to a read that is already
// waiting in the driver.
func (transport *nativeWWANATTransport) waitReaderReady() {
	if ready, ok := transport.readerReady.Load().(chan struct{}); ok && ready != nil {
		<-ready
	}
}

func openNativeWWANATTransport(path string) (Transport, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	// The UFI003/OpenStick 410 bam-dmux AT port only answers a read that is
	// already blocked in the driver: poll() never reports POLLIN and a
	// non-blocking read returns EAGAIN. Clear O_NONBLOCK and keep one blocking
	// reader outstanding so responses are actually delivered.
	if _, fcntlErr := unix.FcntlInt(uintptr(fd), unix.F_SETFL, 0); fcntlErr != nil {
		return &nativeWWANATTransport{fd: fd, readTimeout: -1}, nil
	}
	transport := &nativeWWANATTransport{
		fd:          fd,
		readTimeout: -1,
		blocking:    true,
		readResults: make(chan readResult, 16),
	}
	transport.ensureReader()
	transport.waitReaderReady()
	return transport, nil
}

func (transport *nativeWWANATTransport) Read(buffer []byte) (int, error) {
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	if transport.closed.Load() {
		return 0, io.ErrClosedPipe
	}

	deadline := time.Time{}
	if transport.readTimeout >= 0 {
		deadline = time.Now().Add(transport.readTimeout)
	}

	if transport.blocking {
		// Blind spot for the poll-based loop below: this driver never raises
		// POLLIN, so only the persistent blocking reader can observe data.
		transport.ensureReader()
		transport.waitReaderReady()
		if transport.readResults == nil {
			return 0, io.ErrClosedPipe
		}
		var wait <-chan time.Time
		if !deadline.IsZero() {
			timer := time.NewTimer(time.Until(deadline))
			defer timer.Stop()
			wait = timer.C
		}
		select {
		case result := <-transport.readResults:
			if result.err != nil {
				return 0, result.err
			}
			return copy(buffer, result.data), nil
		case <-wait:
			return 0, nil
		}
	}

	for {
		if transport.closed.Load() {
			return 0, io.ErrClosedPipe
		}
		timeout := 100
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, nil
			}
			timeout = int((min(remaining, 100*time.Millisecond) + time.Millisecond - 1) / time.Millisecond)
		}
		fds := []unix.PollFd{{Fd: int32(transport.fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if transport.closed.Load() {
			return 0, io.ErrClosedPipe
		}
		if ready == 0 {
			continue
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 &&
			fds[0].Revents&unix.POLLIN == 0 {
			return 0, io.EOF
		}
		count, err := unix.Read(transport.fd, buffer)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if count < 0 {
			count = 0
		}
		if len(buffer) > 0 && count == 0 && err == nil && fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			return 0, io.EOF
		}
		return count, err
	}
}

func (transport *nativeWWANATTransport) Write(buffer []byte) (int, error) {
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	for {
		if transport.closed.Load() {
			return 0, io.ErrClosedPipe
		}
		count, err := unix.Write(transport.fd, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			fds := []unix.PollFd{{Fd: int32(transport.fd), Events: unix.POLLOUT}}
			if _, pollErr := unix.Poll(fds, 100); pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
				return 0, pollErr
			}
			continue
		}
		if count < 0 {
			count = 0
		}
		return count, err
	}
}

func (transport *nativeWWANATTransport) Drain() error {
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	if transport.closed.Load() {
		return io.ErrClosedPipe
	}
	// WWAN writes have no termios output queue. Input belongs to Session;
	// discarding it here could lose a pending final or an unsolicited event.
	return nil
}

func (transport *nativeWWANATTransport) ResetInputBuffer() error {
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	if transport.closed.Load() {
		return io.ErrClosedPipe
	}
	if transport.blocking {
		// Discard anything the persistent reader queued for a previous command.
		// The blocked read in the driver keeps running uninterrupted.
		if transport.readResults != nil {
			for {
				select {
				case <-transport.readResults:
					continue
				default:
				}
				break
			}
		}
		return nil
	}
	buffer := make([]byte, 4096)
	for {
		if transport.closed.Load() {
			return io.ErrClosedPipe
		}
		fds := []unix.PollFd{{Fd: int32(transport.fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, 0)
		if err != nil {
			return err
		}
		if ready == 0 || fds[0].Revents&unix.POLLIN == 0 {
			return nil
		}
		if _, err := unix.Read(transport.fd, buffer); err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return err
		}
	}
}

func (transport *nativeWWANATTransport) SetReadTimeout(timeout time.Duration) error {
	if timeout < -1 {
		return fmt.Errorf("invalid read timeout %s", timeout)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed.Load() {
		return io.ErrClosedPipe
	}
	transport.readTimeout = timeout
	return nil
}

func (transport *nativeWWANATTransport) Close() error {
	// Blocking path: closing the descriptor does not necessarily wake the
	// persistent reader out of unix.Read on this class of driver. The
	// reader notices closed at the top of its loop and exits when the
	// device next delivers data or the read errors, so a reader can
	// outlive its transport until then. VoCat opens this transport for
	// the lifetime of a modem session, which bounds the linger to at
	// most one goroutine per session, and it cannot outlive the process.
	transport.closed.Store(true)
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.fd < 0 {
		return nil
	}
	err := unix.Close(transport.fd)
	transport.fd = -1
	return err
}
