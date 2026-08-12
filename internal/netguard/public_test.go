package netguard

import (
	"context"
	"testing"
)

func TestValidatePublicURLRejectsUnsafeDestinations(t *testing.T) {
	tests := []string{
		"http://127.0.0.1/plugin.zip",
		"https://[::1]/plugin.zip",
		"https://169.254.169.254/latest/meta-data/",
		"https://[64:ff9b::7f00:1]/",
		"https://[2002:7f00:1::]/",
		"file:///etc/passwd",
		"https://user:secret@example.com/plugin.zip",
	}
	for _, raw := range tests {
		if _, err := ValidatePublicURL(context.Background(), raw, false); err == nil {
			t.Errorf("ValidatePublicURL(%q) accepted an unsafe destination", raw)
		}
	}
}

func TestValidatePublicURLCanRequireHTTPS(t *testing.T) {
	if _, err := ValidatePublicURL(context.Background(), "http://8.8.8.8/plugin.zip", true); err == nil {
		t.Fatal("HTTP destination was accepted while HTTPS was required")
	}
}
