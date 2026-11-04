package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestStorePersistsAdminAndSessions verifies that administrator, credential digests, expiry, and cookie policy survive reopening storage.
func TestStorePersistsAdminAndSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vocat.db")

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	passwordHash := []byte("password-hash")
	if err := database.SetAdmin(ctx, "admin", passwordHash); err != nil {
		t.Fatalf("SetAdmin() error = %v", err)
	}
	admin, err := database.AdminByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("AdminByUsername() error = %v", err)
	}

	tokenHash := bytes.Repeat([]byte{1}, 32)
	csrfHash := bytes.Repeat([]byte{2}, 32)
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := database.CreateSession(ctx, admin.ID, tokenHash, csrfHash, expiresAt, true); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	database, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer database.Close()

	session, err := database.SessionByTokenHash(ctx, tokenHash)
	if err != nil {
		t.Fatalf("SessionByTokenHash() error = %v", err)
	}
	if !session.SecureCookies || session.Admin.Username != "admin" || !bytes.Equal(session.CSRFHash, csrfHash) {
		t.Fatalf("unexpected session: %+v", session)
	}
	if !session.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", session.ExpiresAt, expiresAt)
	}
}

// TestDeleteExpiredSessions checks that cleanup removes expired session credentials.
func TestDeleteExpiredSessions(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	if err := database.SetAdmin(ctx, "admin", []byte("hash")); err != nil {
		t.Fatal(err)
	}
	admin, err := database.CurrentAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := bytes.Repeat([]byte{3}, 32)
	if err := database.CreateSession(
		ctx,
		admin.ID,
		tokenHash,
		bytes.Repeat([]byte{4}, 32),
		time.Now().Add(-time.Minute),
		false,
	); err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteExpiredSessions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SessionByTokenHash(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SessionByTokenHash() error = %v, want ErrNotFound", err)
	}
}
