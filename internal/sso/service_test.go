package sso

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

// TestSaveValidatesJWKSBeforeEnablingSSO checks enable validation and persistence for unusable, cancelled, and valid JWKS responses.
func TestSaveValidatesJWKSBeforeEnablingSSO(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		cancel    bool
		algorithm string
		wantErr   error
	}{
		{name: "unavailable keys", status: http.StatusServiceUnavailable, wantErr: ErrProviderUnavailable},
		{name: "malformed keys", status: http.StatusOK, body: `{`, wantErr: ErrProviderUnavailable},
		{name: "empty keys", status: http.StatusOK, body: `{"keys":[]}`, wantErr: ErrProviderUnavailable},
		{name: "encryption-only keys", status: http.StatusOK, body: `{"keys":[{"kty":"RSA","use":"enc","kid":"enc-only","n":"AQAB","e":"AQAB"}]}`, wantErr: ErrProviderUnavailable},
		{name: "incompatible key family", status: http.StatusOK, body: `{"keys":[{"kty":"RSA","n":"AQAB","e":"AQAB"}]}`, algorithm: "ES256", wantErr: ErrProviderUnavailable},
		{name: "cancelled request", cancel: true, wantErr: context.Canceled},
		{name: "usable keys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOIDCFixture(t)
			if tc.algorithm != "" {
				f.changeMeta = func(metadata map[string]any) {
					metadata["id_token_signing_alg_values_supported"] = []string{tc.algorithm}
				}
			}
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
			session, expiresAt := digest("password-session"), time.Now().Add(time.Hour)
			if err := database.CreateSession(ctx, 1, session, digest("csrf"), expiresAt, false); err != nil {
				t.Fatal(err)
			}
			client := f.server.Client()
			baseTransport := client.Transport
			var jwksCalls int
			client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/jwks" {
					jwksCalls++
					if tc.cancel {
						cancel()
						return nil, request.Context().Err()
					}
					if tc.status != 0 {
						return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: request}, nil
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
			started, err := service.Start(ctx, StartRequest{Purpose: "bind", ExpectedRevision: snapshot.Config.Revision, AdminID: 1, SessionHash: session, SessionExpiresAt: expiresAt})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := database.ClaimSSOFlow(ctx, started.Flow.StateHash, started.Flow.BrowserTokenHash)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err = database.FinalizeSSOBinding(ctx, claimed, store.SSOIdentity{Protocol: "oidc", Authority: f.server.URL, Subject: "stable-user"})
			if err != nil {
				t.Fatal(err)
			}
			if jwksCalls != 0 {
				t.Fatal("disabled configuration fetched JWKS")
			}
			config := snapshot.Config
			config.Enabled = true
			saved, revoked, err := service.Save(ctx, store.SSOSaveRequest{ExpectedRevision: config.Revision, Config: config, CurrentSessionHash: session})
			if !errors.Is(err, tc.wantErr) || revoked {
				t.Fatalf("enable SSO: revoked=%v error=%v, want %v", revoked, err, tc.wantErr)
			}
			if jwksCalls != 1 || f.tokenCalls.Load() != 0 || f.userinfoCalls.Load() != 0 {
				t.Fatal("enable must check JWKS once without authenticating an account")
			}
			persisted, err := service.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != nil {
				if !reflect.DeepEqual(persisted, snapshot) {
					t.Fatal("failed validation changed the saved configuration or binding")
				}
			} else if !saved.Config.Enabled || saved.Config.Revision != snapshot.Config.Revision+1 || !reflect.DeepEqual(saved, persisted) || !reflect.DeepEqual(saved.Binding, snapshot.Binding) {
				t.Fatal("successful validation did not save the enabled configuration and retain its binding")
			}
			if _, err := database.SessionByTokenHash(context.Background(), session); err != nil {
				t.Fatalf("enable validation revoked password session: %v", err)
			}
		})
	}
}

// TestCheckRechecksRevisionAfterFetchingJWKS ensures a remote readiness check cannot publish an obsolete configuration revision.
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
