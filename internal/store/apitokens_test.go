package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func newTokenStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestAPITokenUniqueConstraints(t *testing.T) {
	ctx := context.Background()
	database := newTokenStore(t)

	digest := sha256.Sum256([]byte("vcat_first"))
	if _, err := database.CreateAPIToken(ctx, "same-name", digest[:], false, time.Time{}); err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	otherDigest := sha256.Sum256([]byte("vcat_second"))
	if _, err := database.CreateAPIToken(ctx, "same-name", otherDigest[:], false, time.Time{}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := database.CreateAPIToken(ctx, "other-name", digest[:], false, time.Time{}); err == nil {
		t.Fatal("duplicate token hash accepted")
	}
}

func TestAPITokenTouchThreshold(t *testing.T) {
	ctx := context.Background()
	database := newTokenStore(t)

	digest := sha256.Sum256([]byte("vcat_touch"))
	if _, err := database.CreateAPIToken(ctx, "touched", digest[:], true, time.Time{}); err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if err := database.TouchAPIToken(ctx, digest[:], 5*time.Minute); err != nil {
		t.Fatalf("TouchAPIToken() error = %v", err)
	}
	first, err := database.APITokenByName(ctx, "touched")
	if err != nil {
		t.Fatalf("APITokenByName() error = %v", err)
	}
	if first.LastUsedAt.IsZero() {
		t.Fatal("touch did not record last_used_at")
	}
	if first.ReadOnly != true {
		t.Fatal("read_only not persisted")
	}

	// A second touch inside the threshold must not move last_used_at.
	time.Sleep(1100 * time.Millisecond)
	if err := database.TouchAPIToken(ctx, digest[:], 5*time.Minute); err != nil {
		t.Fatalf("TouchAPIToken() second error = %v", err)
	}
	second, err := database.APITokenByName(ctx, "touched")
	if err != nil {
		t.Fatalf("APITokenByName() second error = %v", err)
	}
	if !second.LastUsedAt.Equal(first.LastUsedAt) {
		t.Fatal("last_used_at moved inside the threshold")
	}
}

func TestDeleteAPITokenAndExpired(t *testing.T) {
	ctx := context.Background()
	database := newTokenStore(t)

	digest := sha256.Sum256([]byte("vcat_keep"))
	if _, err := database.CreateAPIToken(ctx, "keeper", digest[:], false, time.Time{}); err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	expiredDigest := sha256.Sum256([]byte("vcat_expired"))
	if _, err := database.CreateAPIToken(
		ctx,
		"expired",
		expiredDigest[:],
		false,
		time.Now().UTC().Add(-time.Hour),
	); err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}

	if err := database.DeleteAPIToken(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteAPIToken(unknown) error = %v, want ErrNotFound", err)
	}
	if err := database.DeleteExpiredAPITokens(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("DeleteExpiredAPITokens() error = %v", err)
	}
	tokens, err := database.APITokens(ctx)
	if err != nil {
		t.Fatalf("APITokens() error = %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "keeper" {
		t.Fatalf("APITokens() = %+v, want only keeper", tokens)
	}
	if err := database.DeleteAPIToken(ctx, "keeper"); err != nil {
		t.Fatalf("DeleteAPIToken() error = %v", err)
	}
	if _, err := database.APITokenByHash(ctx, digest[:]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("APITokenByHash(deleted) error = %v, want ErrNotFound", err)
	}
}
