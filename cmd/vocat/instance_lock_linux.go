//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockServerInstance(databasePath string) (*os.File, error) {
	// The modem, PC/SC reader, XFRM policies and listener are host resources,
	// not database resources. Lock per OS user so a diagnostic instance using a
	// different VOCAT_DATABASE_PATH cannot silently steal the same AT port from
	// the managed service. Prefer /run because systemd's PrivateTmp would
	// otherwise hide the managed service's lock from a manually started process.
	// The UID-specific directory still permits intentionally isolated users to
	// operate independently; development hosts without writable /run fall back
	// to TempDir.
	uid := os.Geteuid()
	directory := filepath.Join("/run", fmt.Sprintf("vocat-%d", uid))
	if uid == 0 {
		directory = "/run/vocat"
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		directory = os.TempDir()
	}
	path := filepath.Join(directory, "vocat-server.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open server instance lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.New("another vocat server already controls this host's modem resources")
		}
		return nil, fmt.Errorf("lock server instance: %w", err)
	}
	return file, nil
}
