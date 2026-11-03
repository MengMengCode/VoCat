package sso

import (
	"encoding/json"
	"os"
	"testing"
)

// TestSharedSafeRedirectVectors checks redirect policy against fixtures shared with browser tests.
func TestSharedSafeRedirectVectors(t *testing.T) {
	raw, err := os.ReadFile("../../web/test/fixtures/ssoRedirect.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Input    *string `json:"input"`
		Expected string  `json:"expected"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		input := ""
		if vector.Input != nil {
			input = *vector.Input
		}
		if result := SafeRedirect(input, "https://vocat.example.com"); result != vector.Expected {
			t.Errorf("SafeRedirect(%q) = %q, want %q", input, result, vector.Expected)
		}
	}
}
