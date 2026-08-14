package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var configEnvironment = []string{
	"VOCAT_CONFIG",
	"VOCAT_ADDR",
	"VOCAT_DATABASE_PATH",
	"VOCAT_ADMIN_USERNAME",
	"VOCAT_ADMIN_PASSWORD",
	"VOCAT_ADMIN_PASSWORD_B64",
	"VOCAT_SESSION_TTL",
	"VOCAT_SECURE_COOKIES",
	"VOCAT_SHUTDOWN_TIMEOUT",
	"VOCAT_MAX_REQUEST_BODY_BYTES",
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range configEnvironment {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Address != "0.0.0.0:7575" {
		t.Fatalf("Address = %q", cfg.Address)
	}
}

func TestLoadFileThenEnvironmentOverride(t *testing.T) {
	clearConfigEnvironment(t)
	path := filepath.Join(t.TempDir(), "vocat.json")
	content := []byte(`{
		"address": "127.0.0.1:8000",
		"database_path": "/tmp/from-file.db",
		"session_ttl": "2h",
		"secure_cookies": false,
		"shutdown_timeout": "12s",
		"max_request_body_bytes": 4096
	}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("VOCAT_CONFIG", path)
	t.Setenv("VOCAT_ADDR", "0.0.0.0:9000")
	t.Setenv("VOCAT_SECURE_COOKIES", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Address != "0.0.0.0:9000" || !cfg.SecureCookies {
		t.Fatalf("environment override not applied: %+v", cfg)
	}
	if cfg.SessionTTL != 2*time.Hour {
		t.Fatalf("file values not applied: %+v", cfg)
	}
}

func TestLoadRejectsUnknownJSONField(t *testing.T) {
	clearConfigEnvironment(t)
	path := filepath.Join(t.TempDir(), "vocat.json")
	if err := os.WriteFile(path, []byte(`{"unknown": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOCAT_CONFIG", path)

	if _, err := Load(); err == nil {
		t.Fatal("Load() unexpectedly accepted unknown field")
	}
}

func TestLoadIgnoresLegacyAdministratorConfiguration(t *testing.T) {
	clearConfigEnvironment(t)
	path := filepath.Join(t.TempDir(), "vocat.json")
	if err := os.WriteFile(path, []byte(`{
		"admin_username": "legacy-admin",
		"admin_password": "legacy-password"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOCAT_CONFIG", path)
	t.Setenv("VOCAT_ADMIN_USERNAME", "environment-admin")
	t.Setenv("VOCAT_ADMIN_PASSWORD", "environment-password")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected ignored legacy credentials: %v", err)
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("VOCAT_SESSION_TTL", "tomorrow")

	if _, err := Load(); err == nil {
		t.Fatal("Load() unexpectedly accepted invalid duration")
	}
}
