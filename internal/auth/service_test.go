package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	service, err := New(database, Options{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.EnsureAdmin(context.Background(), "admin", "correct-password"); err != nil {
		t.Fatalf("EnsureAdmin() error = %v", err)
	}
	return service
}

func TestLoginAuthenticateCSRFAndLogout(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	if _, err := service.Login(ctx, "admin", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
	credentials, err := service.Login(ctx, "admin", "correct-password")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	session, err := service.Authenticate(ctx, credentials.SessionToken)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if session.Principal.Username != "admin" {
		t.Fatalf("Principal = %+v", session.Principal)
	}
	if _, err := service.ValidateCSRF(ctx, credentials.SessionToken, "wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("ValidateCSRF() error = %v, want ErrInvalidCSRF", err)
	}
	if _, err := service.ValidateCSRF(ctx, credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatalf("ValidateCSRF() error = %v", err)
	}
	_, csrfToken, err := service.CSRFToken(
		ctx,
		credentials.SessionToken,
		credentials.CSRFToken,
	)
	if err != nil {
		t.Fatalf("CSRFToken() error = %v", err)
	}
	if csrfToken != credentials.CSRFToken {
		t.Fatal("CSRFToken() rotated an already valid token")
	}

	if err := service.Logout(ctx, credentials.SessionToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate() after logout error = %v, want ErrUnauthorized", err)
	}
}

func TestEnsureAdminRevokesSessionOnPasswordChange(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	credentials, err := service.Login(ctx, "admin", "correct-password")
	if err != nil {
		t.Fatal(err)
	}

	if err := service.EnsureAdmin(ctx, "admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session error = %v, want ErrUnauthorized", err)
	}
	if _, err := service.Login(ctx, "admin", "new-password"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

func TestEnsureAdminIfMissingDoesNotOverwriteChangedPassword(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if err := service.ChangePassword(ctx, "admin", "correct-password", "changed-password"); err != nil {
		t.Fatal(err)
	}
	created, err := service.EnsureAdminIfMissing(ctx, "admin", "stale-config-password")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing administrator was reported as newly created")
	}
	if _, err := service.Login(ctx, "admin", "changed-password"); err != nil {
		t.Fatalf("database password was overwritten: %v", err)
	}
	if _, err := service.Login(ctx, "admin", "stale-config-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("stale configured password became active: %v", err)
	}
}
