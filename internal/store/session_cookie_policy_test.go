package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionCookieMigration29PreservesExistingCredentials keeps legacy sessions and bearer tokens valid without guessing their transport.
func TestSessionCookieMigration29PreservesExistingCredentials(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 29; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				db.Close()
				t.Fatalf("baseline migration %d: %v", version, err)
			}
		}
	}
	for _, statement := range []string{
		`PRAGMA user_version = 29`,
		`INSERT INTO admins VALUES (1, 'admin', X'1234', 1, 1)`,
		`INSERT INTO sessions (token_hash, admin_id, csrf_hash, expires_at, created_at) VALUES (X'1234', 1, X'5678', 4102444800, 1)`,
		`INSERT INTO sso_bindings (id, admin_id, provider_instance_id, protocol, authority, subject, created_at, updated_at) VALUES ('binding', 1, 'provider', 'oidc', 'https://id.example', 'subject', 1, 1)`,
		`INSERT INTO sessions (token_hash, admin_id, csrf_hash, expires_at, created_at, login_method, sso_binding_id) VALUES (X'1122', 1, X'3344', 4102444800, 1, 'sso', 'binding')`,
		`INSERT INTO api_tokens (name, token_hash, created_at) VALUES ('existing', X'4321', 1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, tc := range []struct {
		token, csrf []byte
		binding     string
	}{
		{[]byte{0x12, 0x34}, []byte{0x56, 0x78}, ""},
		{[]byte{0x11, 0x22}, []byte{0x33, 0x44}, "binding"},
	} {
		session, err := database.SessionByTokenHash(ctx, tc.token)
		if err != nil || session.SecureCookies || session.SSOBindingID != tc.binding || !bytes.Equal(session.CSRFHash, tc.csrf) {
			t.Fatalf("legacy session changed: err=%v", err)
		}
	}
	if _, err := database.APITokenByName(ctx, "existing"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE sessions SET secure_cookies = 2`); err == nil {
		t.Fatal("invalid cookie policy accepted")
	}
}

// TestSessionSecureCookieUpgradePersists verifies that HTTPS recognition survives reopening the database and cannot affect other sessions.
func TestSessionSecureCookieUpgradePersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "policy.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetAdmin(ctx, "admin", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	for _, token := range []byte{1, 2} {
		if err := database.CreateSession(ctx, 1, []byte{token}, []byte{3}, time.Now().Add(time.Hour), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.RequireSessionSecureCookies(ctx, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := database.RequireSessionSecureCookies(ctx, []byte{9}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, token := range []byte{1, 2} {
		session, err := database.SessionByTokenHash(ctx, []byte{token})
		if err != nil || session.SecureCookies != (token == 1) || !bytes.Equal(session.CSRFHash, []byte{3}) {
			t.Fatalf("policy did not survive restart for session %d: %v", token, err)
		}
	}
}
