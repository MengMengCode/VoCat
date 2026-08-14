package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateExecutableRejectsNonExecutableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-vocat")
	if err := os.WriteFile(path, []byte("not an executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateExecutable(context.Background(), path); err == nil {
		t.Fatal("validateExecutable accepted invalid file")
	}
}

func TestBackupAndReplaceRetainsPreviousBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux replacement behavior")
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "vocat")
	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := backupAndReplace(target, replacement); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(target + ".previous")
	if err != nil {
		t.Fatalf("read retained backup: %v", err)
	}
	if string(old) != "old" {
		t.Fatalf("backup = %q", old)
	}
}
