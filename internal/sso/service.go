package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"vocat/internal/store"
)

const (
	FlowTTL      = 10 * time.Minute
	RemoteBudget = 30 * time.Second
)

// Service always loads configuration and secret in one store snapshot. Provider
// instances are derived from that revision, never an independent mutable cache.
type Service struct {
	store           *store.Store
	providerOptions []ProviderOptions
}

type Options struct {
	// HTTPClient is explicit dependency injection for in-process protocol tests.
	// The application never obtains it from user configuration.
	HTTPClient *http.Client
}

// New creates an SSO service backed by authoritative store snapshots and optional fixture transport.
func New(database *store.Store, options Options) *Service {
	s := &Service{store: database}
	if options.HTTPClient != nil {
		s.providerOptions = []ProviderOptions{{HTTPClient: options.HTTPClient}}
	}
	return s
}

// Snapshot reads configuration, credentials, and binding from one consistent store transaction.
func (s *Service) Snapshot(ctx context.Context) (store.SSOSnapshot, error) {
	return s.store.SSOSnapshot(ctx)
}

// Save validates the expected revision and checks provider readiness before enabling SSO.
// The store then atomically rechecks authorization, saves the configuration, and revokes affected sessions.
func (s *Service) Save(ctx context.Context, request store.SSOSaveRequest) (store.SSOSnapshot, bool, error) {
	current, err := s.Snapshot(ctx)
	if err != nil {
		return store.SSOSnapshot{}, false, err
	}
	if request.ExpectedRevision != current.Config.Revision {
		return store.SSOSnapshot{}, false, store.ErrSSOConfigChanged
	}
	request.Config, err = NormalizeConfig(request.Config)
	if err != nil {
		return store.SSOSnapshot{}, false, err
	}
	secret := current.ClientSecret
	if current.HasConfiguration && !store.SSOSameIdentitySource(current.Config, request.Config) {
		secret = ""
	}
	if request.ClientSecret != nil {
		secret = *request.ClientSecret
	}
	if request.Config.Enabled {
		if current.Binding == nil {
			return store.SSOSnapshot{}, false, store.ErrSSOIdentityNotBound
		}
		if !store.SSOSameIdentitySource(current.Config, request.Config) {
			return store.SSOSnapshot{}, false, store.ErrSSORebindRequired
		}
		if err := ValidateComplete(request.Config, secret); err != nil {
			return store.SSOSnapshot{}, false, err
		}
		ctx, cancel := context.WithTimeout(ctx, RemoteBudget)
		defer cancel()
		if err := CheckConfiguration(ctx, request.Config, secret, s.providerOptions...); err != nil {
			if ctx.Err() != nil {
				return store.SSOSnapshot{}, false, ctx.Err()
			}
			return store.SSOSnapshot{}, false, err
		}
	}
	return s.store.SaveSSOConfiguration(ctx, request)
}

type StartRequest struct {
	// PublicOrigin is a validated browser entry selected by the HTTP server.
	PublicOrigin     string
	Purpose          string
	ExpectedRevision int64
	ReturnPath       string
	BrowserFlowIDs   []string
	AdminID          int64
	SessionHash      []byte
	SessionExpiresAt time.Time
}

type StartedFlow struct {
	Flow             store.SSOFlow
	BrowserToken     string
	AuthorizationURL string
}

// Start creates a browser-bound, expiring login or binding flow from the current configuration.
// Binding requires an authenticated administrator session and cannot outlive that session.
func (s *Service) Start(ctx context.Context, request StartRequest) (StartedFlow, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return StartedFlow{}, err
	}
	if request.Purpose != "login" && request.Purpose != "bind" {
		return StartedFlow{}, store.ErrSSOInvalidFlow
	}
	if request.Purpose == "login" {
		if request.ExpectedRevision != 0 && request.ExpectedRevision != snapshot.Config.Revision {
			return StartedFlow{}, store.ErrSSOConfigChanged
		}
		if !snapshot.Config.Enabled || snapshot.Binding == nil {
			return StartedFlow{}, store.ErrSSODisabled
		}
	} else {
		if request.ExpectedRevision != snapshot.Config.Revision {
			return StartedFlow{}, store.ErrSSOConfigChanged
		}
		if request.AdminID == 0 || len(request.SessionHash) != sha256.Size {
			return StartedFlow{}, store.ErrSSOInvalidFlow
		}
	}
	if err := ValidateComplete(snapshot.Config, snapshot.ClientSecret); err != nil {
		return StartedFlow{}, store.ErrSSONotConfigured
	}
	config := snapshot.Config
	if request.PublicOrigin != "" {
		config.PublicOrigin, err = NormalizePublicOrigin(request.PublicOrigin)
		if err != nil {
			return StartedFlow{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteBudget)
	defer cancel()
	provider, err := NewProvider(ctx, config, snapshot.ClientSecret, s.providerOptions...)
	if err != nil {
		if ctx.Err() != nil {
			return StartedFlow{}, ctx.Err()
		}
		return StartedFlow{}, err
	}
	id, err := randomValue(16)
	if err != nil {
		return StartedFlow{}, err
	}
	state, err := randomValue(32)
	if err != nil {
		return StartedFlow{}, err
	}
	browserToken, err := randomValue(32)
	if err != nil {
		return StartedFlow{}, err
	}
	nonce, err := randomValue(32)
	if err != nil {
		return StartedFlow{}, err
	}
	now := time.Now().UTC()
	flow := store.SSOFlow{
		ID: id, StateHash: digest(state), BrowserTokenHash: digest(browserToken),
		Purpose: request.Purpose, ProviderInstanceID: snapshot.Config.ProviderInstanceID,
		ConfigRevision: snapshot.Config.Revision, AdminID: request.AdminID,
		InitiatingSessionHash: request.SessionHash,
		NonceHash:             digest(nonce), RedirectURI: CallbackURL(config),
		ReturnPath: SafeRedirect(request.ReturnPath, config.PublicOrigin),
		CreatedAt:  now, ExpiresAt: now.Add(FlowTTL),
	}
	if request.Purpose == "bind" {
		flow.ReturnPath = "/settings"
		if request.SessionExpiresAt.Before(flow.ExpiresAt) {
			flow.ExpiresAt = request.SessionExpiresAt
		}
	}
	if snapshot.Config.PKCEMode == "s256" {
		flow.PKCEVerifier = oauth2.GenerateVerifier()
	}
	authorizationURL, err := provider.AuthorizationURL(AuthorizationFlow{
		State: state, Nonce: nonce, NonceHash: flow.NonceHash,
		PKCEVerifier: flow.PKCEVerifier, RedirectURI: flow.RedirectURI,
	})
	if err != nil {
		return StartedFlow{}, err
	}
	if err := s.store.CreateSSOFlow(ctx, flow, request.BrowserFlowIDs); err != nil {
		return StartedFlow{}, err
	}
	return StartedFlow{Flow: flow, BrowserToken: browserToken, AuthorizationURL: authorizationURL}, nil
}

// FindFlow looks up an unclaimed flow by state without consuming it or proving browser possession.
func (s *Service) FindFlow(ctx context.Context, state string) (store.SSOFlow, error) {
	if len(state) < 32 || len(state) > 256 {
		return store.SSOFlow{}, store.ErrSSOInvalidFlow
	}
	return s.store.SSOFlowByStateHash(ctx, digest(state))
}

// Claim consumes a flow once after the store validates state, browser possession, and current authorization.
func (s *Service) Claim(ctx context.Context, state, browserToken string) (store.SSOFlow, error) {
	if len(state) < 32 || len(state) > 256 || len(browserToken) < 32 || len(browserToken) > 256 {
		return store.SSOFlow{}, store.ErrSSOInvalidFlow
	}
	return s.store.ClaimSSOFlow(ctx, digest(state), digest(browserToken))
}

// Verify checks the flow configuration revision and verifies the remote identity within a time budget.
// The caller must still finalize authorization atomically in the store before granting a session or binding.
func (s *Service) Verify(ctx context.Context, flow store.SSOFlow, code, responseIssuer string) (store.SSOIdentity, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return store.SSOIdentity{}, err
	}
	if snapshot.Config.Revision != flow.ConfigRevision || snapshot.Config.ProviderInstanceID != flow.ProviderInstanceID {
		return store.SSOIdentity{}, store.ErrSSOConfigChanged
	}
	callbackOrigin, ok := strings.CutSuffix(flow.RedirectURI, CallbackPath)
	if !ok {
		return store.SSOIdentity{}, store.ErrSSOInvalidFlow
	}
	snapshot.Config.PublicOrigin, err = NormalizePublicOrigin(callbackOrigin)
	if err != nil {
		return store.SSOIdentity{}, store.ErrSSOInvalidFlow
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteBudget)
	defer cancel()
	provider, err := NewProvider(ctx, snapshot.Config, snapshot.ClientSecret, s.providerOptions...)
	if err != nil {
		if ctx.Err() != nil {
			return store.SSOIdentity{}, ctx.Err()
		}
		return store.SSOIdentity{}, err
	}
	identity, err := provider.VerifyCallback(ctx, AuthorizationFlow{
		NonceHash: flow.NonceHash, PKCEVerifier: flow.PKCEVerifier, RedirectURI: flow.RedirectURI,
	}, code, responseIssuer)
	if err != nil && ctx.Err() != nil {
		return store.SSOIdentity{}, ctx.Err()
	}
	return identity, err
}

// Check validates provider readiness and rejects results made stale by a concurrent configuration change.
func (s *Service) Check(ctx context.Context, expectedRevision int64) error {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Config.Revision != expectedRevision {
		return store.ErrSSOConfigChanged
	}
	if err := ValidateComplete(snapshot.Config, snapshot.ClientSecret); err != nil {
		return store.ErrSSONotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteBudget)
	defer cancel()
	if err := CheckConfiguration(ctx, snapshot.Config, snapshot.ClientSecret, s.providerOptions...); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	latest, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if latest.Config.Revision != expectedRevision {
		return store.ErrSSOConfigChanged
	}
	return nil
}

// Cleanup runs with the application's lifetime; no IdP request is needed.
func (s *Service) Cleanup(ctx context.Context) error {
	return s.store.DeleteExpiredSSOFlows(ctx, time.Now().UTC())
}

// RunCleanup prunes expired flows immediately and every minute until the application context ends.
func (s *Service) RunCleanup(ctx context.Context) {
	_ = s.Cleanup(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Cleanup(ctx)
		}
	}
}

// randomValue generates a URL-safe random value without exposing random-source error details.
func randomValue(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", errors.New("sso: random source failed")
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// digest hashes transient flow credentials for storage and lookup.
func digest(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}
