package sso

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"vocat/internal/store"
)

func TestCheckRechecksRevisionAfterFetchingJWKS(t *testing.T) {
	f := newOIDCFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.SetAdmin(ctx, "admin", []byte("fixture-password-hash")); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	client := f.server.Client()
	baseTransport := client.Transport
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/jwks" {
			close(entered)
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return baseTransport.RoundTrip(request)
	})
	service := New(database, Options{HTTPClient: client})
	secret := "a-test-secret"
	snapshot, _, err := service.Save(ctx, store.SSOSaveRequest{Config: f.config, ClientSecret: &secret})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- service.Check(ctx, snapshot.Config.Revision) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("configuration check did not reach the JWKS endpoint")
	}
	config := snapshot.Config
	config.DisplayName = "Changed during configuration check"
	if _, _, err := service.Save(ctx, store.SSOSaveRequest{ExpectedRevision: snapshot.Config.Revision, Config: config}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, store.ErrSSOConfigChanged) {
			t.Fatalf("configuration check published an obsolete revision: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("configuration check did not complete after the JWKS response")
	}
	if f.jwksCalls.Load() != 1 || f.tokenCalls.Load() != 0 || f.userinfoCalls.Load() != 0 {
		t.Fatal("configuration check skipped JWKS verification or authenticated an account")
	}
}
