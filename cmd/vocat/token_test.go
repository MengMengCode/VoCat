package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vocat/internal/auth"
	"vocat/internal/store"
)

func TestTokenCreateListRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocat.db")
	var stdout bytes.Buffer

	if err := runToken([]string{"create", "--database", path, "ci-runner"}, &stdout); err != nil {
		t.Fatalf("create error = %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "token: "+auth.APITokenPrefix) {
		t.Fatalf("create output hides the token: %q", output)
	}
	if !strings.Contains(output, "read-write") || !strings.Contains(output, "expires: never") {
		t.Fatalf("create output missing mode/expiry: %q", output)
	}

	stdout.Reset()
	if err := runToken([]string{"create", "--database", path, "--read-only", "--ttl", "1h", "metrics"}, &stdout); err != nil {
		t.Fatalf("create read-only error = %v", err)
	}
	if !strings.Contains(stdout.String(), "read-only") {
		t.Fatalf("read-only mode missing from output: %q", stdout.String())
	}

	duplicate := bytes.Buffer{}
	if err := runToken([]string{"create", "--database", path, "ci-runner"}, &duplicate); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create error = %v, want already exists", err)
	}

	stdout.Reset()
	if err := runToken([]string{"list", "--database", path}, &stdout); err != nil {
		t.Fatalf("list error = %v", err)
	}
	listing := stdout.String()
	for _, want := range []string{"ci-runner", "metrics", "yes", "no"} {
		if !strings.Contains(listing, want) {
			t.Fatalf("list output missing %q: %q", want, listing)
		}
	}

	if err := runToken([]string{"revoke", "--database", path, "metrics"}, &stdout); err != nil {
		t.Fatalf("revoke error = %v", err)
	}
	stdout.Reset()
	if err := runToken([]string{"list", "--database", path}, &stdout); err != nil {
		t.Fatalf("list after revoke error = %v", err)
	}
	if strings.Contains(stdout.String(), "metrics") {
		t.Fatalf("revoked token still listed: %q", stdout.String())
	}

	missing := bytes.Buffer{}
	if err := runToken([]string{"revoke", "--database", path, "metrics"}, &missing); err == nil ||
		!strings.Contains(err.Error(), "unknown api token") {
		t.Fatalf("revoke unknown error = %v, want unknown api token", err)
	}

	// Every issued and revoked token left an audit trail with the CLI actor.
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	events, err := database.ListAuditEvents(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, event := range events {
		if event.Actor != "cli" || event.EntityType != "api_token" {
			t.Fatalf("unexpected audit event %+v", event)
		}
		actions[event.Action]++
	}
	if actions["auth.token.create"] != 2 || actions["auth.token.revoke"] != 1 {
		t.Fatalf("audit actions = %v", actions)
	}
}

func TestTokenValidationAndUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vacat-skip.db")
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"create"},
		{"create", "--database", path},
		{"create", "--database", path, "has space"},
		{"list", "extra"},
		{"revoke"},
	} {
		var stdout bytes.Buffer
		if err := runToken(args, &stdout); err == nil {
			t.Fatalf("runToken(%v) succeeded, want usage error", args)
		}
	}

	var stdout bytes.Buffer
	if err := runToken([]string{"create", "--database", filepath.Join(t.TempDir(), "ok.db"), "--ttl", "not-a-duration", "name"}, &stdout); err == nil {
		t.Fatal("invalid ttl accepted")
	}
	// Negative durations are rejected outright; only zero means never.
	if err := runToken([]string{"create", "--database", filepath.Join(t.TempDir(), "ok.db"), "--ttl", "-1h", "name"}, &stdout); err == nil {
		t.Fatal("negative ttl accepted")
	}
	if err := runToken([]string{"create", "--database", filepath.Join(t.TempDir(), "ok.db"), "--ttl", "0", "zero-is-never"}, &stdout); err != nil {
		t.Fatalf("zero ttl rejected: %v", err)
	}
}

func TestTokenCreateBeforeBootstrapWarningPath(t *testing.T) {
	// Tokens created on a fresh database are wiped by the first bootstrap
	// (SetAdmin revokes every credential). The CLI must still work on a
	// database with no administrator yet.
	path := filepath.Join(t.TempDir(), "vocat.db")
	var stdout bytes.Buffer
	if err := runToken([]string{"create", "--database", path, "early"}, &stdout); err != nil {
		t.Fatalf("create on fresh database error = %v", err)
	}
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tokens, err := database.APITokens(context.Background())
	if err != nil || len(tokens) != 1 || tokens[0].Name != "early" {
		t.Fatalf("tokens after create = %+v, %v", tokens, err)
	}
	if !tokens[0].CreatedAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("unexpected created_at %v", tokens[0].CreatedAt)
	}
}
