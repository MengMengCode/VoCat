package extensions

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestInstallURLRejectsNonHTTPSAndPrivateDestinations(t *testing.T) {
	manager, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	for _, raw := range []string{
		"http://example.com/plugin.zip",
		"https://user@example.com/plugin.zip",
		"https://example.com/plugin.zip\r\nX-Injected: yes",
		"https://127.0.0.1/plugin.zip",
		"https://169.254.169.254/latest/meta-data/",
	} {
		if _, err := manager.InstallURL(context.Background(), raw, ""); err == nil {
			t.Errorf("InstallURL(%q) accepted an unsafe destination", raw)
		}
	}
}

func TestInstallListDisableAndUninstall(t *testing.T) {
	manager, err := NewManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	archive := testPackage(t, map[string]string{
		ManifestFilename: `{
			"schema_version":1,"id":"test-plugin","name":"Test","version":"1.0.0",
			"permissions":["devices.read"],
			"contributions":[{"id":"test-page","label":"Test","location":"sidebar","entry":"web/index.html"}]
		}`,
		"web/index.html": "<h1>test</h1>",
	})
	plugin, err := manager.Install(bytes.NewReader(archive), "")
	if err != nil {
		t.Fatal(err)
	}
	if plugin.ID != "test-plugin" || !plugin.Enabled || len(manager.List()) != 1 {
		t.Fatalf("unexpected installed plugin: %#v", plugin)
	}
	if _, err := manager.SetEnabled(plugin.ID, false); err != nil {
		t.Fatal(err)
	}
	if manager.List()[0].Enabled {
		t.Fatal("plugin remained enabled")
	}
	if err := manager.Uninstall(plugin.ID); err != nil {
		t.Fatal(err)
	}
	if len(manager.List()) != 0 {
		t.Fatal("plugin remained installed")
	}
}

func TestInstallRejectsPathTraversal(t *testing.T) {
	manager, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	archive := testPackage(t, map[string]string{
		ManifestFilename: `{
			"schema_version":1,"id":"bad-plugin","name":"Bad","version":"1",
			"contributions":[{"id":"bad-page","label":"Bad","location":"sidebar","entry":"web/index.html"}]
		}`,
		"../escaped": "bad",
	})
	if _, err := manager.Install(bytes.NewReader(archive), ""); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("Install traversal error = %v", err)
	}
}

func TestInstallVerifiesSHA256(t *testing.T) {
	manager, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	archive := testPackage(t, map[string]string{
		ManifestFilename: `{
			"schema_version":1,"id":"hash-plugin","name":"Hash","version":"1",
			"contributions":[{"id":"hash-page","label":"Hash","location":"sidebar","entry":"web/index.html"}]
		}`,
		"web/index.html": "ok",
	})
	if _, err := manager.Install(bytes.NewReader(archive), strings.Repeat("0", 64)); err == nil {
		t.Fatal("Install accepted incorrect SHA-256")
	}
}

func testPackage(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
