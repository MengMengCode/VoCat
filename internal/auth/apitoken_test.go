package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/store"
)

func TestCreateAndAuthenticateAPIToken(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	credentials, err := service.CreateAPIToken(ctx, "ci-runner", true, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if !strings.HasPrefix(credentials.Token, APITokenPrefix) {
		t.Fatalf("token = %q, want %q prefix", credentials.Token, APITokenPrefix)
	}
	if credentials.Token == APITokenPrefix {
		t.Fatal("token has no random part")
	}

	principal, err := service.AuthenticateAPIToken(ctx, credentials.Token)
	if err != nil {
		t.Fatalf("AuthenticateAPIToken() error = %v", err)
	}
	if principal.Name != "ci-runner" || !principal.ReadOnly {
		t.Fatalf("principal = %+v, want name ci-runner read-only", principal)
	}

	// A second authentication within the touch interval must not move
	// last_used_at.
	first, err := service.ListAPITokens(ctx)
	if err != nil || len(first) != 1 {
		t.Fatalf("ListAPITokens() = %v, %v", first, err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, credentials.Token); err != nil {
		t.Fatalf("AuthenticateAPIToken() second error = %v", err)
	}
	second, err := service.ListAPITokens(ctx)
	if err != nil || len(second) != 1 {
		t.Fatalf("ListAPITokens() = %v, %v", second, err)
	}
	if !second[0].LastUsedAt.Equal(first[0].LastUsedAt) {
		t.Fatal("last_used_at moved within the touch interval")
	}

	if _, err := service.AuthenticateAPIToken(ctx, APITokenPrefix+"not-issued"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(unknown) error = %v, want ErrUnauthorized", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, "no-prefix"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(no prefix) error = %v, want ErrUnauthorized", err)
	}
}

func TestCreateAPITokenValidation(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	for _, name := range []string{"", strings.Repeat("a", 65), "has space", "has/slash", "-leading-hyphen"} {
		if _, err := service.CreateAPIToken(ctx, name, false, 0); !errors.Is(err, ErrInvalidTokenName) {
			t.Fatalf("CreateAPIToken(%q) error = %v, want ErrInvalidTokenName", name, err)
		}
	}
	if _, err := service.CreateAPIToken(ctx, "usable.name-1_x", false, 0); err != nil {
		t.Fatalf("CreateAPIToken(valid) error = %v", err)
	}
	if _, err := service.CreateAPIToken(ctx, "usable.name-1_x", false, 0); !errors.Is(err, ErrDuplicateTokenName) {
		t.Fatalf("CreateAPIToken(duplicate) error = %v, want ErrDuplicateTokenName", err)
	}
}

func TestAPITokenExpiry(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := New(database, Options{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// A token whose expiry already passed, as an expired installation would
	// have persisted it.
	credential := APITokenPrefix + "already-expired"
	digest := sha256.Sum256([]byte(credential))
	if _, err := database.CreateAPIToken(
		ctx,
		"expired",
		digest[:],
		false,
		time.Now().UTC().Add(-time.Minute),
	); err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	// A token with future expiry authenticates while fresh.
	fresh, err := service.CreateAPIToken(ctx, "still-valid", false, time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken(fresh) error = %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, fresh.Token); err != nil {
		t.Fatalf("AuthenticateAPIToken(fresh) error = %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, credential); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(expired) error = %v, want ErrUnauthorized", err)
	}

	// The expired token must be pruned; the fresh one kept.
	tokens, err := service.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("ListAPITokens() error = %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "still-valid" {
		t.Fatalf("ListAPITokens() = %+v, want only still-valid", tokens)
	}
	if err := service.RevokeAPIToken(ctx, "missing"); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("RevokeAPIToken(unknown) error = %v, want ErrUnknownToken", err)
	}
}

func TestRevokeAPIToken(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	credentials, err := service.CreateAPIToken(ctx, "revoked-later", false, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if err := service.RevokeAPIToken(ctx, "revoked-later"); err != nil {
		t.Fatalf("RevokeAPIToken() error = %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, credentials.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(revoked) error = %v, want ErrUnauthorized", err)
	}
	if err := service.RevokeAPIToken(ctx, "missing"); err == nil {
		t.Fatal("RevokeAPIToken(unknown) succeeded, want error")
	}
}

func TestSetAdminRevokesAPITokens(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := New(database, Options{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.EnsureAdmin(ctx, "admin", "first-password"); err != nil {
		t.Fatalf("EnsureAdmin() error = %v", err)
	}

	credentials, err := service.CreateAPIToken(ctx, "before-rotation", false, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if err := service.EnsureAdmin(ctx, "admin", "rotated-password"); err != nil {
		t.Fatalf("EnsureAdmin(rotation) error = %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, credentials.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(after rotation) error = %v, want ErrUnauthorized", err)
	}
}
