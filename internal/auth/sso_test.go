package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"vocat/internal/store"
)

// TestPreparedCredentialsRequireAtomicCommit verifies credential generation is independent and grants no persisted session.
func TestPreparedCredentialsRequireAtomicCommit(t *testing.T) {
	service := newTestService(t)
	principal := Principal{ID: 1, Username: "admin"}
	first, err := service.PrepareCredentials(principal)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.PrepareCredentials(principal)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionToken == first.CSRFToken || first.SessionToken == second.SessionToken || first.CSRFToken == second.CSRFToken {
		t.Fatal("local credentials must be independent random values")
	}
	if _, err := service.Authenticate(context.Background(), first.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("uncommitted credentials authenticated: %v", err)
	}
	if _, err := service.store.SessionByTokenHash(context.Background(), TokenHash(first.SessionToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("credential preparation persisted a session: %v", err)
	}
}

// TestAuthenticatedSessionDigestAccessorPreservesSessionAndCSRF checks digest copies cannot mutate session or CSRF state.
func TestAuthenticatedSessionDigestAccessorPreservesSessionAndCSRF(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	credentials, err := service.Login(ctx, "admin", "correct-password", false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Authenticate(ctx, credentials.SessionToken)
	if err != nil || session.LoginMethod != "password" {
		t.Fatalf("password session authentication failed: %v", err)
	}
	if !bytes.Equal(session.TokenHash(), TokenHash(credentials.SessionToken)) {
		t.Fatal("session digest accessor mismatch")
	}
	copy := session.TokenHash()
	copy[0] ^= 0xff
	if bytes.Equal(copy, session.TokenHash()) {
		t.Fatal("digest accessor exposed mutable session state")
	}
	if _, err := service.ValidateCSRF(ctx, credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatalf("digest accessor altered CSRF: %v", err)
	}
}
