//go:build linux

package modem

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// linuxSerialTransport deliberately keeps the tty descriptor non-blocking.
// A blocking descriptor can race after poll(2) reports readability: another
// reader consumes the byte first, read(2) sleeps indefinitely, and a library
// Close waiting for the reader deadlocks. Poll plus O_NONBLOCK makes every
// read bounded and lets Session observe its context deadline without closing
// a transport that is in use.
type linuxSerialTransport struct {
	fd          atomic.Int64
	readTimeout atomic.Int64
}

func openSerialTransport(path string, baudRate int) (Transport, error) {
	speed, ok := linuxBaudRate(baudRate)
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate %d", baudRate)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	transport := &linuxSerialTransport{}
	transport.fd.Store(int64(fd))
	transport.readTimeout.Store(int64(100 * time.Millisecond))
	if err := configureLinuxSerial(fd, speed); err != nil {
		_ = unix.Close(fd)
		transport.fd.Store(-1)
		return nil, err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil &&
		!errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EINVAL) {
		_ = unix.Close(fd)
		transport.fd.Store(-1)
		return nil, fmt.Errorf("claim serial port: %w", err)
	}
	return transport, nil
}

func configureLinuxSerial(fd int, speed uint32) error {
	settings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return fmt.Errorf("read serial settings: %w", err)
	}
	settings.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF
	settings.Oflag &^= unix.OPOST
	settings.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	settings.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	settings.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | speed
	settings.Ispeed = speed
	settings.Ospeed = speed
	settings.Cc[unix.VMIN] = 0
	settings.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, settings); err != nil {
		return fmt.Errorf("apply serial settings: %w", err)
	}
	return nil
}

func linuxBaudRate(rate int) (uint32, bool) {
	switch rate {
	case 9600:
		return unix.B9600, true
	case 19200:
		return unix.B19200, true
	case 38400:
		return unix.B38400, true
	case 57600:
		return unix.B57600, true
	case 115200:
		return unix.B115200, true
	case 230400:
		return unix.B230400, true
	case 460800:
		return unix.B460800, true
	case 921600:
		return unix.B921600, true
	default:
		return 0, false
	}
}

func (transport *linuxSerialTransport) currentFD() (int, error) {
	fd := int(transport.fd.Load())
	if fd < 0 {
		return -1, os.ErrClosed
	}
	return fd, nil
}

func (transport *linuxSerialTransport) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	fd, err := transport.currentFD()
	if err != nil {
		return 0, err
	}
	timeout := time.Duration(transport.readTimeout.Load())
	deadline := time.Time{}
	if timeout >= 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		wait := -1
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, nil
			}
			wait = int((remaining + time.Millisecond - 1) / time.Millisecond)
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLERR | unix.POLLHUP}}
		_, err = unix.Poll(fds, wait)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if fds[0].Revents == 0 {
			return 0, nil
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return 0, os.ErrClosed
		}
		count, readErr := unix.Read(fd, buffer)
		if errors.Is(readErr, unix.EINTR) || errors.Is(readErr, unix.EAGAIN) ||
			errors.Is(readErr, unix.EWOULDBLOCK) {
			continue
		}
		if count == 0 && readErr == nil && fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			return 0, io.EOF
		}
		return count, readErr
	}
}

func (transport *linuxSerialTransport) Write(buffer []byte) (int, error) {
	fd, err := transport.currentFD()
	if err != nil {
		return 0, err
	}
	for {
		count, writeErr := unix.Write(fd, buffer)
		if errors.Is(writeErr, unix.EINTR) {
			continue
		}
		if errors.Is(writeErr, unix.EAGAIN) || errors.Is(writeErr, unix.EWOULDBLOCK) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			if _, pollErr := unix.Poll(fds, 100); pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
				return 0, pollErr
			}
			continue
		}
		return count, writeErr
	}
}

func (transport *linuxSerialTransport) Drain() error {
	fd, err := transport.currentFD()
	if err != nil {
		return err
	}
	return unix.IoctlSetInt(fd, unix.TCSBRK, 1)
}

func (transport *linuxSerialTransport) ResetInputBuffer() error {
	fd, err := transport.currentFD()
	if err != nil {
		return err
	}
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}

func (transport *linuxSerialTransport) SetReadTimeout(timeout time.Duration) error {
	if timeout < 0 {
		return fmt.Errorf("invalid serial read timeout %s", timeout)
	}
	transport.readTimeout.Store(int64(timeout))
	return nil
}

func (transport *linuxSerialTransport) Close() error {
	fd := int(transport.fd.Swap(-1))
	if fd < 0 {
		return nil
	}
	if err := unix.Close(fd); err != nil && !errors.Is(err, syscall.EBADF) {
		return err
	}
	return nil
}
