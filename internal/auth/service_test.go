package auth

import (
	"context"
	"errors"
	"strings"
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

func TestResetAdminCredentialsChangesUsernameAndPasswordWithoutOldPassword(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	credentials, err := service.Login(ctx, "admin", "correct-password")
	if err != nil {
		t.Fatal(err)
	}

	if err := service.ResetAdminCredentials(ctx, "new-admin", "replacement-password"); err != nil {
		t.Fatalf("ResetAdminCredentials() error = %v", err)
	}
	if _, err := service.Login(ctx, "admin", "correct-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old credentials error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := service.Login(ctx, "new-admin", "replacement-password"); err != nil {
		t.Fatalf("new credentials login error = %v", err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session error = %v, want ErrUnauthorized", err)
	}
}

func TestResetAdminCredentialsValidatesInput(t *testing.T) {
	service := newTestService(t)
	for _, test := range []struct {
		name     string
		username string
		password string
	}{
		{name: "empty username", password: "replacement-password"},
		{name: "control whitespace", username: "bad\tname", password: "replacement-password"},
		{name: "empty password", username: "admin", password: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := service.ResetAdminCredentials(context.Background(), test.username, test.password); err == nil {
				t.Fatal("ResetAdminCredentials() accepted invalid input")
			}
		})
	}
}

func TestResetAdminCredentialsAcceptsPasswordsWithoutComplexityRules(t *testing.T) {
	ctx := context.Background()
	for _, password := range []string{"1", strings.Repeat("x", 256)} {
		service := newTestService(t)
		if err := service.ResetAdminCredentials(ctx, "admin", password); err != nil {
			t.Fatalf("ResetAdminCredentials(%d-byte password) error = %v", len(password), err)
		}
		if _, err := service.Login(ctx, "admin", password); err != nil {
			t.Fatalf("Login(%d-byte password) error = %v", len(password), err)
		}
	}
}

func TestChangePasswordAcceptsPasswordsWithoutComplexityRules(t *testing.T) {
	ctx := context.Background()
	for _, password := range []string{"1", strings.Repeat("long-password-", 32)} {
		service := newTestService(t)
		if err := service.ChangePassword(ctx, "admin", "correct-password", password); err != nil {
			t.Fatalf("ChangePassword(%d-byte password) error = %v", len(password), err)
		}
		if _, err := service.Login(ctx, "admin", password); err != nil {
			t.Fatalf("Login(%d-byte password) error = %v", len(password), err)
		}
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
