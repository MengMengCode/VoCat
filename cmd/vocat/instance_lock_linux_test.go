//go:build linux

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServerInstanceLockRejectsSecondProcess(t *testing.T) {
	firstDatabase := filepath.Join(t.TempDir(), "vocat.db")
	first, err := lockServerInstance(firstDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	secondDatabase := filepath.Join(t.TempDir(), "other.db")
	second, err := lockServerInstance(secondDatabase)
	if second != nil {
		second.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "already controls this host") {
		t.Fatalf("second lock error = %v", err)
	}
}
