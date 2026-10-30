// Package remoteproc observes the remote processor owning a native WWAN port.
// It never starts, stops, or changes recovery settings on a remote processor.
package remoteproc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("modem remote processor is unavailable")

// Observation describes a positively identified WWAN remote processor. An
// empty Path means sysfs did not expose a matching owner (for example in a
// container or on a PCIe modem); callers must retain their normal behavior.
type Observation struct {
	Path       string
	State      string
	Recovery   string
	Generation uint64
}

type history struct {
	generation   uint64
	interrupted  bool
	runningSince time.Time
}

type monitor struct {
	sysRoot    string
	resolve    func(string) (string, error)
	now        func() time.Time
	settle     time.Duration
	mu         sync.Mutex
	seen       map[string]*history
	portOwners map[string]string
}

var system = newMonitor("/sys")

func newMonitor(sysRoot string) *monitor {
	return &monitor{sysRoot: sysRoot, resolve: filepath.EvalSymlinks,
		now: time.Now, settle: 5 * time.Second, seen: make(map[string]*history), portOwners: make(map[string]string)}
}

// CheckPort reads Linux's documented remoteproc state/recovery ABI:
// https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-class-remoteproc
// Only an ancestor of this WWAN port is considered: remoteproc0 may be Wi-Fi.
// After an observed interruption, require a short stable running interval
// before letting ordinary clients reopen. This is a debounce, not a guarantee
// that SIM initialization has completed; existing readiness checks still apply.
func CheckPort(port string) (Observation, error) { return system.checkPort(port) }

func (m *monitor) checkPort(port string) (Observation, error) {
	name := filepath.Base(port)
	if !strings.HasPrefix(name, "wwan") {
		return Observation{}, nil
	}
	// AT and QMI ports of the same wwanN device share one owner, even when
	// one class symlink disappears during recovery before its first AT access.
	key := name
	end := len("wwan")
	for end < len(name) && name[end] >= '0' && name[end] <= '9' {
		end++
	}
	if end > len("wwan") {
		key = name[:end]
	}
	devicePath, err := m.resolve(filepath.Join(m.sysRoot, "class", "wwan", name))
	if err != nil {
		return m.missingTopology(key)
	}
	entries, err := os.ReadDir(filepath.Join(m.sysRoot, "class", "remoteproc"))
	if err != nil {
		return m.missingTopology(key)
	}
	owner := ""
	ownerDepth := 0
	for _, entry := range entries {
		classPath := filepath.Join(m.sysRoot, "class", "remoteproc", entry.Name())
		realPath, resolveErr := m.resolve(classPath)
		if resolveErr != nil || filepath.Base(filepath.Dir(realPath)) != "remoteproc" {
			continue
		}
		parent := filepath.Dir(filepath.Dir(realPath))
		if within(parent, devicePath) && len(parent) > ownerDepth {
			owner, ownerDepth = classPath, len(parent)
		}
	}
	if owner == "" {
		return m.missingTopology(key)
	}
	// Serialize the read and transition so two callers cannot apply observations
	// out of order and mistake a recovering processor for a second cold boot.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.portOwners[key] = owner
	item := m.seen[owner]
	state, err := os.ReadFile(filepath.Join(owner, "state"))
	if err != nil {
		if item != nil && item.interrupted {
			return Observation{Path: owner, Generation: item.generation},
				fmt.Errorf("%w: cannot read state during recovery", ErrUnavailable)
		}
		return Observation{}, nil
	}
	obs := Observation{Path: owner, State: strings.TrimSpace(string(state))}
	if recovery, readErr := os.ReadFile(filepath.Join(owner, "recovery")); readErr == nil {
		obs.Recovery = strings.TrimSpace(string(recovery))
	}
	if item == nil {
		item = &history{generation: 1}
		m.seen[owner] = item
	}
	obs.Generation = item.generation
	switch obs.State {
	case "crashed", "offline", "suspended", "invalid", "deleted", "detached":
		if !item.interrupted {
			item.generation++
			obs.Generation = item.generation
		}
		item.interrupted = true
		item.runningSince = time.Time{}
		return obs, fmt.Errorf("%w: state=%s recovery=%s; restore the modem before retrying", ErrUnavailable, obs.State, obs.Recovery)
	case "running", "attached":
		if item.interrupted {
			now := m.now()
			if item.runningSince.IsZero() {
				item.runningSince = now
			}
			if now.Sub(item.runningSince) < m.settle {
				return obs, fmt.Errorf("%w: waiting for stable running state after recovery", ErrUnavailable)
			}
			item.interrupted = false
		}
	default:
		if item.interrupted {
			item.runningSince = time.Time{}
			return obs, fmt.Errorf("%w: unknown state during recovery", ErrUnavailable)
		}
	}
	return obs, nil
}

func (m *monitor) missingTopology(port string) (Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner := m.portOwners[port]
	if item := m.seen[owner]; item != nil && item.interrupted {
		return Observation{Path: owner, Generation: item.generation},
			fmt.Errorf("%w: device sysfs is unavailable during recovery", ErrUnavailable)
	}
	return Observation{}, nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
