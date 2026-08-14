package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vocat/internal/auth"
	"vocat/internal/store"
)

func TestBootstrapAdminOnlyInitializesAnEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocat.db")
	withBootstrapStdin(t, "first-secure-password\n", func() {
		if err := runBootstrapAdmin([]string{"--database", path}); err != nil {
			t.Fatal(err)
		}
	})
	withBootstrapStdin(t, "second-secure-password\n", func() {
		if err := runBootstrapAdmin([]string{"--database", path}); err != nil {
			t.Fatal(err)
		}
	})
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service, err := auth.New(database, auth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(context.Background(), "admin", "first-secure-password"); err != nil {
		t.Fatalf("initial password was overwritten: %v", err)
	}
}

func withBootstrapStdin(t *testing.T, input string, action func()) {
	t.Helper()
	original := os.Stdin
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	os.Stdin = file
	t.Cleanup(func() { os.Stdin = original; _ = file.Close() })
	action()
	os.Stdin = original
}
