package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebAddressWithPort(t *testing.T) {
	tests := []struct {
		name    string
		address string
		port    string
		want    string
		wantErr bool
	}{
		{name: "IPv4", address: "0.0.0.0:7575", port: "8080", want: "0.0.0.0:8080"},
		{name: "IPv6", address: "[::]:7575", port: "8443", want: "[::]:8443"},
		{name: "minimum", address: "127.0.0.1:7575", port: "1", want: "127.0.0.1:1"},
		{name: "maximum", address: "127.0.0.1:7575", port: "65535", want: "127.0.0.1:65535"},
		{name: "zero", address: "0.0.0.0:7575", port: "0", wantErr: true},
		{name: "too large", address: "0.0.0.0:7575", port: "65536", wantErr: true},
		{name: "not numeric", address: "0.0.0.0:7575", port: "http", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := webAddressWithPort(test.address, test.port)
			if test.wantErr {
				if !errors.Is(err, errInvalidWebPort) {
					t.Fatalf("error = %v, want errInvalidWebPort", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("webAddressWithPort() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestRewriteEnvValuePreservesOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte("VOCAT_ADMIN_PASSWORD=secret\nVOCAT_ADDR=0.0.0.0:7575\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewriteEnvValue(path, "VOCAT_ADDR", "0.0.0.0:8080"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	if strings.Contains(got, "VOCAT_ADMIN_PASSWORD") || !strings.Contains(got, "VOCAT_ADDR=0.0.0.0:8080\n") || strings.Contains(got, ":7575") {
		t.Fatalf("rewritten env = %q", got)
	}
	if err := rewriteEnvValue(path, "VOCAT_ADDR", "0.0.0.0:9000\nVOCAT_ADMIN_PASSWORD=changed"); err == nil {
		t.Fatal("environment line injection was accepted")
	}
	if err := rewriteEnvValue(path, "VOCAT_ADMIN_PASSWORD", "changed-password"); err == nil {
		t.Fatal("administrator credential was accepted for the environment file")
	}
}

func TestMenuIncludesWebPortOptionInBothLanguages(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		options := strings.Join(newMenu(lang).options(), "\n")
		if !strings.Contains(options, "3)") || !strings.Contains(strings.ToLower(options), "web") {
			t.Fatalf("%s menu options do not contain Web port entry: %q", lang, options)
		}
	}
}

func TestMenuCredentialResetPromptsDoNotRequestCurrentPassword(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		menu := newMenu(lang)
		prompts := strings.Join([]string{
			menu.newUsername("admin"),
			menu.newPassword(),
			menu.confirmPassword(),
		}, "\n")
		if strings.Contains(strings.ToLower(prompts), "current password") || strings.Contains(prompts, "当前密码") {
			t.Fatalf("%s credential reset still requests the current password: %q", lang, prompts)
		}
		if !strings.Contains(prompts, "admin") {
			t.Fatalf("%s username prompt does not show the current username: %q", lang, prompts)
		}
	}
}
