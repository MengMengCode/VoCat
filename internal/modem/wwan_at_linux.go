//go:build linux

package modem

import (
	"errors"
	"fmt"
	"io"
	"sync"
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
	closed      bool
}

func openNativeWWANATTransport(path string) (Transport, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &nativeWWANATTransport{fd: fd, readTimeout: -1}, nil
}

func (transport *nativeWWANATTransport) Read(buffer []byte) (int, error) {
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	if transport.closed {
		return 0, io.ErrClosedPipe
	}

	deadline := time.Time{}
	if transport.readTimeout >= 0 {
		deadline = time.Now().Add(transport.readTimeout)
	}
	for {
		timeout := -1
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, nil
			}
			timeout = int((remaining + time.Millisecond - 1) / time.Millisecond)
		}
		fds := []unix.PollFd{{Fd: int32(transport.fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if ready == 0 {
			return 0, nil
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
		return count, err
	}
}

func (transport *nativeWWANATTransport) Write(buffer []byte) (int, error) {
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	if transport.closed {
		return 0, io.ErrClosedPipe
	}
	for {
		count, err := unix.Write(transport.fd, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			fds := []unix.PollFd{{Fd: int32(transport.fd), Events: unix.POLLOUT}}
			if _, pollErr := unix.Poll(fds, 1000); pollErr != nil {
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
	if transport.closed {
		return io.ErrClosedPipe
	}
	// WWAN character-device writes are handed to the modem synchronously and
	// have no termios output queue to drain. A previous command that timed out
	// can leave late bytes in the input buffer (e.g. a slow CGSN reply that
	// arrives after the command deadline); discard them here so the next
	// command starts from a clean stream instead of mis-parsing stale output.
	buffer := make([]byte, 4096)
	for {
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

func (transport *nativeWWANATTransport) ResetInputBuffer() error {
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	if transport.closed {
		return io.ErrClosedPipe
	}
	buffer := make([]byte, 4096)
	for {
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
	if transport.closed {
		return io.ErrClosedPipe
	}
	transport.readTimeout = timeout
	return nil
}

func (transport *nativeWWANATTransport) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return nil
	}
	transport.closed = true
	return unix.Close(transport.fd)
}
