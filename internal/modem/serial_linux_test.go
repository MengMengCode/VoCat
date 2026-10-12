//go:build linux

package modem

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func linuxTestSerialPath(t *testing.T) (string, int) {
	t.Helper()
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(master) })
	if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("/dev/pts/%d", number), master
}

// A long-lived exec child models qmi-proxy without accessing real modems.
func TestLinuxSerialExecHelper(t *testing.T) {
	if os.Getenv("VOCAT_TEST_SERIAL_EXEC_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestLinuxNativeTransportCloseInterruptsIO(t *testing.T) {
	for _, kind := range []string{"serial", "wwan"} {
		for _, operation := range []string{"read", "write", "drain"} {
			if kind == "wwan" && operation == "drain" {
				continue // WWAN Drain never waits for output.
			}
			t.Run(kind+"/"+operation, func(t *testing.T) {
				fd, peer := socketpair(t)
				serial := &linuxSerialTransport{}
				serial.fd.Store(int64(fd))
				serial.readTimeout.Store(int64(time.Hour))
				var transport Transport = serial
				mu, wantErr := &serial.mu, os.ErrClosed
				if kind == "wwan" {
					wwan := &nativeWWANATTransport{fd: fd, readTimeout: -1}
					transport, mu, wantErr = wwan, &wwan.mu, io.ErrClosedPipe
				}
				defer transport.Close()
				defer unix.Close(peer)
				if operation != "read" {
					for {
						_, err := unix.Write(fd, make([]byte, 4096))
						if errors.Is(err, unix.EAGAIN) {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				ioDone := make(chan error, 1)
				go func() {
					var err error
					switch operation {
					case "read":
						_, err = transport.Read(make([]byte, 1))
					case "write":
						_, err = transport.Write([]byte("AT\r"))
					case "drain":
						err = transport.Drain()
					}
					ioDone <- err
				}()
				waitForTransportReadLock(t, mu)
				closeDone := make(chan error, 1)
				go func() { closeDone <- transport.Close() }()
				for _, result := range []struct {
					done chan error
					want error
				}{{closeDone, nil}, {ioDone, wantErr}} {
					select {
					case err := <-result.done:
						if !errors.Is(err, result.want) {
							t.Fatalf("Close/IO error = %v, want %v", err, result.want)
						}
					case <-time.After(time.Second):
						t.Fatal("Close did not interrupt pending I/O")
					}
				}
			})
		}
	}
}

func waitForTransportReadLock(t *testing.T, mu *sync.RWMutex) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for mu.TryLock() {
		mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("I/O did not acquire transport lock")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLinuxSerialCloseReleasesTTYWhileExecChildRemainsAlive(t *testing.T) {
	for cycle := 0; cycle < 5; cycle++ {
		t.Run(fmt.Sprintf("reset-%d", cycle), func(t *testing.T) {
			path, master := linuxTestSerialPath(t)
			transport, err := openSerialTransport(path, 115200)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			fd, err := transport.(*linuxSerialTransport).currentFD()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLinuxSerialExecHelper$")
			child.Env = append(os.Environ(), "VOCAT_TEST_SERIAL_EXEC_HELPER=1")
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = stdin.Close()
				if err := child.Wait(); err != nil {
					t.Errorf("exec child: %v", err)
				}
			}()
			if ready, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || ready != "ready\n" {
				t.Fatalf("exec child readiness = %q, %v", ready, err)
			}
			inheritedPath, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", child.Process.Pid, fd))
			if inheritedPath == path {
				t.Errorf("exec child inherited serial descriptor %d for %s", fd, path)
			}
			if err := transport.Close(); err != nil {
				t.Fatal(err)
			}
			// EIO means the last slave descriptor has closed; an inherited
			// descriptor keeps it alive. The PTY master retains TIOCEXCL itself,
			// so reopening this synthetic slave cannot test USB tty recovery.
			if _, err := unix.Read(master, make([]byte, 1)); !errors.Is(err, unix.EIO) {
				t.Fatalf("serial tty remains open after transport close: master read = %v, want EIO", err)
			}
		})
	}
}
