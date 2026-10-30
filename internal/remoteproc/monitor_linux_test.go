//go:build linux

package remoteproc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxSysfsSymlinksIdentifyModemOwner(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "devices", "soc", "4080000.remoteproc")
	owner := filepath.Join(physical, "remoteproc", "remoteproc1")
	port := filepath.Join(physical, "wwan", "wwan0", "wwan0qmi0")
	for _, path := range []string{owner, port, filepath.Join(root, "class", "wwan"), filepath.Join(root, "class", "remoteproc")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(port, filepath.Join(root, "class", "wwan", "wwan0qmi0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(owner, filepath.Join(root, "class", "remoteproc", "remoteproc1")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "state"), []byte("crashed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	obs, err := newMonitor(root).checkPort("/dev/wwan0qmi0")
	if !errors.Is(err, ErrUnavailable) || obs.State != "crashed" || filepath.Base(obs.Path) != "remoteproc1" {
		t.Fatalf("sysfs association failed: %+v %v", obs, err)
	}
}
