package update

import (
	"io"
	"log/slog"
	"testing"
)

func TestDetectSystemdUnitUsesExplicitOverride(t *testing.T) {
	t.Setenv("VOCAT_SYSTEMD_UNIT", "vocat-test.service")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if got := detectSystemdUnit(logger); got != "vocat-test.service" {
		t.Fatalf("detectSystemdUnit() = %q, want vocat-test.service", got)
	}
}

func TestSystemdUnitFromCgroup(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "cgroup v2", data: "0::/system.slice/vocat-test.service\n", want: "vocat-test.service"},
		{name: "legacy", data: "1:name=systemd:/system.slice/vocat@dji.service\n", want: "vocat@dji.service"},
		{name: "no service", data: "0::/user.slice/user-1000.slice/session-1.scope\n", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := systemdUnitFromCgroup(test.data); got != test.want {
				t.Fatalf("systemdUnitFromCgroup() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidSystemdUnitRejectsArgumentsAndPaths(t *testing.T) {
	for _, value := range []string{"vocat", "../vocat.service", "vocat.service --now", "vocat.service/other"} {
		if validSystemdUnit.MatchString(value) {
			t.Fatalf("validSystemdUnit unexpectedly accepted %q", value)
		}
	}
}
