package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
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

func New(database *store.Store, options Options) *Service {
	s := &Service{store: database}
	if options.HTTPClient != nil {
		s.providerOptions = []ProviderOptions{{HTTPClient: options.HTTPClient}}
	}
	return s
}

func (s *Service) Snapshot(ctx context.Context) (store.SSOSnapshot, error) {
	return s.store.SSOSnapshot(ctx)
}

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
		if _, err := NewProvider(ctx, request.Config, secret, s.providerOptions...); err != nil {
			if ctx.Err() != nil {
				return store.SSOSnapshot{}, false, ctx.Err()
			}
			return store.SSOSnapshot{}, false, err
		}
	}
	return s.store.SaveSSOConfiguration(ctx, request)
}

type StartRequest struct {
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
	ctx, cancel := context.WithTimeout(ctx, RemoteBudget)
	defer cancel()
	provider, err := NewProvider(ctx, snapshot.Config, snapshot.ClientSecret, s.providerOptions...)
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
		NonceHash:             digest(nonce), RedirectURI: CallbackURL(snapshot.Config),
		ReturnPath: SafeRedirect(request.ReturnPath, snapshot.Config.PublicOrigin),
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

func (s *Service) FindFlow(ctx context.Context, state string) (store.SSOFlow, error) {
	if len(state) < 32 || len(state) > 256 {
		return store.SSOFlow{}, store.ErrSSOInvalidFlow
	}
	return s.store.SSOFlowByStateHash(ctx, digest(state))
}

func (s *Service) Claim(ctx context.Context, state, browserToken string) (store.SSOFlow, error) {
	if len(state) < 32 || len(state) > 256 || len(browserToken) < 32 || len(browserToken) > 256 {
		return store.SSOFlow{}, store.ErrSSOInvalidFlow
	}
	return s.store.ClaimSSOFlow(ctx, digest(state), digest(browserToken))
}

func (s *Service) Verify(ctx context.Context, flow store.SSOFlow, code, responseIssuer string) (store.SSOIdentity, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return store.SSOIdentity{}, err
	}
	if snapshot.Config.Revision != flow.ConfigRevision || snapshot.Config.ProviderInstanceID != flow.ProviderInstanceID {
		return store.SSOIdentity{}, store.ErrSSOConfigChanged
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

func randomValue(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", errors.New("sso: random source failed")
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func digest(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}
