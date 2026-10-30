package remoteproc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fixture struct {
	m     *monitor
	owner string
	port  string
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{m: newMonitor(root), port: "/dev/wwan0qmi0", clock: time.Unix(100, 0)}
	f.owner = filepath.Join(root, "class", "remoteproc", "remoteproc1")
	wifi := filepath.Join(root, "class", "remoteproc", "remoteproc0")
	for _, path := range []string{f.owner, wifi} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	physical := filepath.Join(root, "devices", "soc", "4080000.remoteproc")
	paths := map[string]string{
		filepath.Join(root, "class", "wwan", "wwan0qmi0"): filepath.Join(physical, "wwan", "wwan0", "wwan0qmi0"),
		f.owner: filepath.Join(physical, "remoteproc", "remoteproc1"),
		wifi:    filepath.Join(root, "devices", "soc", "a204000.remoteproc", "remoteproc", "remoteproc0"),
	}
	f.m.resolve = func(path string) (string, error) {
		if target, ok := paths[path]; ok {
			return target, nil
		}
		return "", os.ErrNotExist
	}
	f.m.now = func() time.Time { return f.clock }
	f.write(t, "state", "running")
	f.write(t, "recovery", "disabled")
	if err := os.WriteFile(filepath.Join(wifi, "state"), []byte("crashed"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) write(t *testing.T, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.owner, name), []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestChecksOnlyRemoteProcessorOwningWWANPort(t *testing.T) {
	f := newFixture(t)
	obs, err := f.m.checkPort(f.port)
	if err != nil || obs.Path != f.owner || obs.State != "running" || obs.Generation != 1 {
		t.Fatalf("unrelated crashed Wi-Fi affected modem: obs=%+v err=%v", obs, err)
	}
	// A lexical prefix is not an ancestor (foo.remoteproc-extra is unrelated).
	f.m.resolve = func(path string) (string, error) {
		if filepath.Base(path) == "wwan0qmi0" {
			return filepath.Join("devices", "foo-extra", "wwan"), nil
		}
		return filepath.Join("devices", "foo", "remoteproc", filepath.Base(path)), nil
	}
	obs, err = f.m.checkPort(f.port)
	if err != nil || obs.Path != "" {
		t.Fatalf("sibling matched: %+v %v", obs, err)
	}
}

func TestCrashBlocksAccessAndRecoveryDebouncesWithoutChangingSettings(t *testing.T) {
	f := newFixture(t)
	initial, err := f.m.checkPort(f.port)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "state", "crashed")
	for range 3 {
		obs, err := f.m.checkPort(f.port)
		if !errors.Is(err, ErrUnavailable) || obs.Generation != initial.Generation+1 {
			t.Fatalf("crash: %+v %v", obs, err)
		}
	}
	for _, state := range []string{"detached", "unrecognized-state"} {
		f.write(t, "state", state)
		if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s admitted interrupted modem: %v", state, err)
		}
	}
	f.write(t, "state", "running")
	if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("immediate reopen: %v", err)
	}
	f.clock = f.clock.Add(4 * time.Second)
	if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("premature reopen: %v", err)
	}
	f.clock = f.clock.Add(time.Second)
	obs, err := f.m.checkPort(f.port)
	if err != nil || obs.Generation != initial.Generation+1 {
		t.Fatalf("stable: %+v %v", obs, err)
	}
	recovery, err := os.ReadFile(filepath.Join(f.owner, "recovery"))
	if err != nil || string(recovery) != "disabled\n" {
		t.Fatalf("recovery setting changed: %q %v", recovery, err)
	}
	f.write(t, "state", "crashed")
	obs, err = f.m.checkPort(f.port)
	if !errors.Is(err, ErrUnavailable) || obs.Generation != initial.Generation+2 {
		t.Fatalf("second crash: %+v %v", obs, err)
	}
}

func TestKnownInterruptionStaysBlockedWhenStateCannotBeRead(t *testing.T) {
	f := newFixture(t)
	f.write(t, "state", "offline")
	if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.owner, "state")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lost recovery state permitted access: %v", err)
	}
}

func TestInterruptedModemStaysBlockedWhileSysfsTopologyDisappears(t *testing.T) {
	f := newFixture(t)
	f.write(t, "state", "crashed")
	if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	resolve := f.m.resolve
	f.m.resolve = func(string) (string, error) { return "", os.ErrNotExist }
	if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lost port topology admitted crashed modem: %v", err)
	}
	if _, err := f.m.checkPort("/dev/wwan0at0"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AT bypassed interrupted QMI owner: %v", err)
	}
	if obs, err := f.m.checkPort("/dev/wwan1at0"); err != nil || obs.Path != "" {
		t.Fatalf("unrelated WWAN blocked: %+v %v", obs, err)
	}
	f.m.resolve = resolve
	class := filepath.Dir(f.owner)
	if err := os.Rename(class, class+"-temporarily-absent"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.checkPort(f.port); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lost remoteproc class admitted crashed modem: %v", err)
	}
}

func TestMissingSysfsAndNonWWANPortsKeepExistingBehavior(t *testing.T) {
	m := newMonitor(t.TempDir())
	for _, port := range []string{"/dev/ttyUSB2", "/dev/cdc-wdm0", "/dev/wwan1qmi0", ""} {
		if obs, err := m.checkPort(port); err != nil || obs.Path != "" {
			t.Fatalf("%s: %+v %v", port, obs, err)
		}
	}
}
