package qmiport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"vocat/internal/remoteproc"
)

func TestLeaseKeepsPortOpenAndSerializesUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wwan0qmi0")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	coordinator := newCoordinator(func(path string) (portHandle, error) {
		opens.Add(1)
		return os.OpenFile(path, os.O_RDWR, 0)
	})
	t.Cleanup(func() { _ = coordinator.close() })

	first, err := coordinator.acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := coordinator.acquire(ctx, path); err == nil {
		t.Fatal("second acquire succeeded before the first lease was released")
	}
	first.Release()

	second, err := coordinator.acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	second.Release()
	if got := opens.Load(); got != 1 {
		t.Fatalf("keepalive opens = %d, want 1", got)
	}
}

func TestLeaseBlocksCrashedModemAndReopensSameNodeAfterRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wwan0qmi0")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var opens int
	var previous portHandle
	coordinator := newCoordinator(func(path string) (portHandle, error) {
		if previous != nil {
			if _, err := previous.Stat(); err != nil {
				t.Fatal("old keepalive closed before replacement opened")
			}
		}
		opens++
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		previous = file
		return file, err
	})
	t.Cleanup(func() { _ = coordinator.close() })
	observation := remoteproc.Observation{Path: "modem", State: "running", Generation: 1}
	var healthErr error
	coordinator.observe = func(string) (remoteproc.Observation, error) { return observation, healthErr }
	lease, err := coordinator.acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	healthErr = remoteproc.ErrUnavailable
	observation.State = "crashed"
	observation.Generation = 2
	for range 2 {
		if _, err := coordinator.acquire(context.Background(), path); !errors.Is(err, remoteproc.ErrUnavailable) {
			t.Fatalf("crashed access: %v", err)
		}
	}
	if opens != 1 {
		t.Fatalf("opened crashed port %d times", opens)
	}
	healthErr = nil
	observation.State = "running"
	lease, err = coordinator.acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if opens != 2 {
		t.Fatalf("same-inode recovery opens=%d, want 2", opens)
	}
}

func TestLeaseReopensReplacedDeviceNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot unlink the open keepalive descriptor; native WWAN is Linux-only")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "wwan0qmi0")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	coordinator := newCoordinator(func(path string) (portHandle, error) {
		opens.Add(1)
		return os.OpenFile(path, os.O_RDWR, 0)
	})
	t.Cleanup(func() { _ = coordinator.close() })

	first, err := coordinator.acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
	if got := opens.Load(); got != 2 {
		t.Fatalf("keepalive opens = %d, want 2 after node replacement", got)
	}
}
