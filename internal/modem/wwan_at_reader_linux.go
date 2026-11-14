//go:build linux

package modem

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const nativeWWANATReaderArgument = "--vocat-internal-wwan-at-reader"

type nativeWWANATReaderKey struct {
	device uint64
	inode  uint64
	rdev   uint64
}

// A pending killed helper still owns its device until Wait reaps it. Keep the
// reservation during that interval so a stuck driver cannot accumulate helpers.
var nativeWWANATReaders = struct {
	sync.Mutex
	active map[nativeWWANATReaderKey]chan struct{}
}{active: make(map[nativeWWANATReaderKey]chan struct{})}

type nativeWWANATReader struct {
	output    *os.File
	process   *os.Process
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func init() {
	if len(os.Args) == 2 && os.Args[1] == nativeWWANATReaderArgument {
		os.Exit(runNativeWWANATReader())
	}
}

// runNativeWWANATReader owns fd 3. Only raw device bytes reach stdout; fd 4
// acknowledges initialization, without claiming a read has entered the driver.
func runNativeWWANATReader() int {
	flags, err := unix.FcntlInt(3, unix.F_GETFL, 0)
	if err != nil {
		return 1
	}
	if _, err := unix.FcntlInt(3, unix.F_SETFL, flags & ^unix.O_NONBLOCK); err != nil {
		return 1
	}
	ready := os.NewFile(4, "wwan-at-reader-ready")
	if _, err := ready.Write([]byte{1}); err != nil {
		return 1
	}
	_ = ready.Close()
	buffer := make([]byte, 4096)
	for {
		count, err := unix.Read(3, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if count > 0 {
			if err := writeAll(os.Stdout, buffer[:count]); err != nil {
				return 1
			}
		}
		if err != nil {
			return 1
		}
		if count == 0 {
			return 0
		}
	}
}

// startNativeWWANATReader consumes fd even on failure. A separate process lets
// Close terminate a blocking device read without retaining a Go runtime thread.
func startNativeWWANATReader(fd int) (*nativeWWANATReader, error) {
	input := os.NewFile(uintptr(fd), "wwan-at-reader-input")
	if input == nil {
		return nil, os.ErrInvalid
	}
	defer input.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	key := nativeWWANATReaderKey{device: uint64(stat.Dev), inode: uint64(stat.Ino), rdev: uint64(stat.Rdev)}
	if stat.Mode&unix.S_IFMT == unix.S_IFCHR {
		// Distinct device nodes and symlinks can name the same character port.
		key.device, key.inode = 0, 0
	}
	done := make(chan struct{})
	nativeWWANATReaders.Lock()
	if _, exists := nativeWWANATReaders.active[key]; exists {
		nativeWWANATReaders.Unlock()
		return nil, fmt.Errorf("%w: WWAN AT reader shutdown is still pending", ErrATPortBusy)
	}
	nativeWWANATReaders.active[key] = done
	nativeWWANATReaders.Unlock()
	release := func() {
		nativeWWANATReaders.Lock()
		delete(nativeWWANATReaders.active, key)
		nativeWWANATReaders.Unlock()
		close(done)
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()
	output, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer writer.Close()
	ready, notify, err := os.Pipe()
	if err != nil {
		_ = output.Close()
		return nil, err
	}
	defer ready.Close()
	defer notify.Close()
	// /proc/self/exe keeps the helper protocol tied to this executable even
	// when the installed binary is replaced by a self-update concurrently.
	command := exec.Command("/proc/self/exe", nativeWWANATReaderArgument)
	command.ExtraFiles = []*os.File{input, notify}
	command.Stdout = writer
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := command.Start(); err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("start WWAN AT reader: %w", err)
	}
	started = true
	reader := &nativeWWANATReader{output: output, process: command.Process, done: done}
	_ = writer.Close()
	_ = notify.Close()
	go func() {
		_ = command.Wait()
		release()
	}()
	if err := ready.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, errors.Join(err, reader.Close())
	}
	var status [1]byte
	if _, err := io.ReadFull(ready, status[:]); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize WWAN AT reader: %w", err), reader.Close())
	}
	if status[0] != 1 {
		return nil, errors.Join(errors.New("invalid WWAN AT reader initialization"), reader.Close())
	}
	return reader, nil
}

func (reader *nativeWWANATReader) Read(buffer []byte) (int, error) {
	return reader.output.Read(buffer)
}

func (reader *nativeWWANATReader) SetReadDeadline(deadline time.Time) error {
	return reader.output.SetReadDeadline(deadline)
}

// ResetInputBuffer drains the bytes currently queued in the nonblocking pipe.
// Control avoids an expired Go read deadline preventing the drain altogether.
func (reader *nativeWWANATReader) ResetInputBuffer() error {
	raw, err := reader.output.SyscallConn()
	if err != nil {
		return err
	}
	var drainErr error
	err = raw.Control(func(fd uintptr) {
		remaining, err := unix.IoctlGetInt(int(fd), unix.TIOCINQ)
		if err != nil {
			drainErr = err
			return
		}
		buffer := make([]byte, 4096)
		for remaining > 0 {
			count, err := unix.Read(int(fd), buffer[:min(remaining, len(buffer))])
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.EAGAIN) {
				return
			}
			if err != nil {
				drainErr = err
				return
			}
			if count == 0 {
				return
			}
			remaining -= count
		}
	})
	return errors.Join(err, drainErr)
}

func (reader *nativeWWANATReader) Close() error {
	reader.closeOnce.Do(func() {
		pipeErr := reader.output.Close()
		killErr := reader.process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		reader.closeErr = errors.Join(pipeErr, killErr)
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-reader.done:
		case <-timer.C:
			reader.closeErr = errors.Join(reader.closeErr, errors.New("WWAN AT reader shutdown is still pending"))
		}
	})
	return reader.closeErr
}
