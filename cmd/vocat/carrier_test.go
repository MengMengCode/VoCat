package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"howett.net/plist"
)

func TestRunCarrierImportIPCCPreviewsAndInstallsExplicitly(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.ipcc")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("Payload/Test.bundle/carrier.plist")
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := plist.NewEncoder(&encoded).Encode(map[string]any{
		"CarrierName":    "Test Carrier",
		"SupportedSIMs":  []any{"99901"},
		"SupportedPLMNs": []any{"99901"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(encoded.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	var preview bytes.Buffer
	if err := runCarrier([]string{"import-ipcc", "--document-only", archivePath}, &preview); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(preview.Bytes(), &document); err != nil || document.Version != 1 {
		t.Fatalf("preview = %q, version=%d, error=%v", preview.String(), document.Version, err)
	}

	installDir := t.TempDir()
	var output bytes.Buffer
	if err := runCarrier([]string{
		"import-ipcc", "--id", "cli-test", "--install", "--profile-dir", installDir, archivePath,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var installed struct {
		InstalledPath   string `json:"installed_path"`
		RestartRequired bool   `json:"restart_required"`
	}
	if err := json.Unmarshal(output.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	if !installed.RestartRequired || filepath.Base(installed.InstalledPath) != "cli-test.json" {
		t.Fatalf("install output = %s", output.String())
	}
	if _, err := os.Stat(filepath.Join(installDir, "cli-test.json")); err != nil {
		t.Fatal(err)
	}
}
