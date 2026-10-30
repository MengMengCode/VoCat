// Package qmiport coordinates access to native Linux WWAN QMI control ports.
package qmiport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"vocat/internal/remoteproc"
)

type portHandle interface {
	Close() error
	Stat() (os.FileInfo, error)
}

type portOpener func(string) (portHandle, error)

type entry struct {
	gate chan struct{}

	keeperMu         sync.Mutex
	keeper           portHandle
	keeperOwner      string
	keeperGeneration uint64
}

type coordinator struct {
	mu      sync.Mutex
	entries map[string]*entry
	opener  portOpener
	observe func(string) (remoteproc.Observation, error)
}

// Lease serializes one QMI transaction sequence for a control port. Release
// does not close the keepalive descriptor: the old OpenStick 410 WWAN driver
// removes DATA5_CNTL when the final descriptor closes, and does not reliably
// recreate it until the modem is reset.
type Lease struct {
	entry *entry
	once  sync.Once
}

var processCoordinator = newCoordinator(openPort)

func newCoordinator(opener portOpener) *coordinator {
	return &coordinator{
		entries: make(map[string]*entry),
		opener:  opener,
		observe: remoteproc.CheckPort,
	}
}

func openPort(path string) (portHandle, error) {
	return os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}

// Acquire keeps path open for the process lifetime and grants exclusive QMI
// access until the returned lease is released. A modem reset replaces the
// device node; ensureKeeper detects that inode change and rearms the keepalive.
func Acquire(ctx context.Context, path string) (*Lease, error) {
	return processCoordinator.acquire(ctx, path)
}

func (coordinator *coordinator) acquire(ctx context.Context, path string) (*Lease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return nil, errors.New("QMI control path is required")
	}
	coordinator.mu.Lock()
	item := coordinator.entries[path]
	if item == nil {
		item = &entry{gate: make(chan struct{}, 1)}
		item.gate <- struct{}{}
		coordinator.entries[path] = item
	}
	coordinator.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-item.gate:
	}
	if err := ctx.Err(); err != nil {
		item.gate <- struct{}{}
		return nil, err
	}
	observation, err := coordinator.observe(path)
	if err != nil {
		item.gate <- struct{}{}
		return nil, err
	}
	if err := coordinator.ensureKeeper(path, item, observation); err != nil {
		item.gate <- struct{}{}
		return nil, fmt.Errorf("keep QMI control port %s open: %w", path, err)
	}
	return &Lease{entry: item}, nil
}

func (coordinator *coordinator) ensureKeeper(path string, item *entry, observation remoteproc.Observation) error {
	item.keeperMu.Lock()
	defer item.keeperMu.Unlock()

	currentInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	if item.keeper != nil {
		keeperInfo, statErr := item.keeper.Stat()
		sameGeneration := observation.Path == "" ||
			item.keeperOwner == "" ||
			(item.keeperOwner == observation.Path && item.keeperGeneration == observation.Generation)
		if statErr == nil && os.SameFile(currentInfo, keeperInfo) && sameGeneration {
			if observation.Path != "" {
				item.keeperOwner = observation.Path
				item.keeperGeneration = observation.Generation
			}
			return nil
		}
	}
	keeper, err := coordinator.opener(path)
	if err != nil {
		return err
	}
	// Open the replacement before closing the old descriptor. On downstream
	// 410 drivers even a brief final-close can remove the live QMI port.
	previous := item.keeper
	item.keeper = keeper
	item.keeperOwner = observation.Path
	item.keeperGeneration = observation.Generation
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

// Release allows the next QMI-UIM operation to use this control port.
func (lease *Lease) Release() {
	if lease == nil || lease.entry == nil {
		return
	}
	lease.once.Do(func() {
		lease.entry.gate <- struct{}{}
	})
}

func (coordinator *coordinator) close() error {
	coordinator.mu.Lock()
	entries := make([]*entry, 0, len(coordinator.entries))
	for _, item := range coordinator.entries {
		entries = append(entries, item)
	}
	coordinator.entries = make(map[string]*entry)
	coordinator.mu.Unlock()

	var errs []error
	for _, item := range entries {
		item.keeperMu.Lock()
		if item.keeper != nil {
			errs = append(errs, item.keeper.Close())
			item.keeper = nil
		}
		item.keeperMu.Unlock()
	}
	return errors.Join(errs...)
}
