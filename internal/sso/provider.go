// Package sso verifies external identities. It never grants local permissions
// or persists identity-provider tokens; those decisions belong to the store.
package sso

import (
	"context"
	"errors"
	"net/http"

	"vocat/internal/store"
)

type Config = store.SSOConfig
type ExternalIdentity = store.SSOIdentity

var (
	ErrInvalidConfiguration = errors.New("invalid SSO configuration")
	ErrInvalidIdentity      = store.ErrSSOInvalidIdentity
	ErrProviderUnavailable  = errors.New("SSO provider unavailable")
)

type AuthorizationFlow struct {
	State        string
	Nonce        string
	NonceHash    []byte
	PKCEVerifier string
	RedirectURI  string
}

type Provider interface {
	AuthorizationURL(AuthorizationFlow) (string, error)
	VerifyCallback(context.Context, AuthorizationFlow, string, string) (ExternalIdentity, error)
}

// ProviderOptions supplies a trusted HTTP client to protocol fixtures. Production
// callers leave it empty and use the origin- and address-constrained transport.
// It is intentionally never populated from persisted/user-supplied configuration.
type ProviderOptions struct {
	HTTPClient *http.Client
}

// NewProvider validates a complete configuration and constructs its constrained protocol adapter.
// OIDC construction validates discovery; use CheckConfiguration to validate the JWKS as well.
func NewProvider(ctx context.Context, config Config, secret string, options ...ProviderOptions) (Provider, error) {
	config, err := NormalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := ValidateComplete(config, secret); err != nil {
		return nil, err
	}
	var option ProviderOptions
	if len(options) > 0 {
		option = options[0]
	}
	client, policy, err := newHTTPClient(config, option.HTTPClient)
	if err != nil {
		return nil, err
	}
	if config.Kind == "pocketid" {
		return newOIDCProvider(ctx, config, secret, client, policy)
	}
	return newOAuthProvider(ctx, config, secret, client, policy)
}

// CheckConfiguration validates provider setup, including usable OIDC signing keys, without a login.
func CheckConfiguration(ctx context.Context, config Config, secret string, options ...ProviderOptions) error {
	provider, err := NewProvider(ctx, config, secret, options...)
	if err != nil {
		return err
	}
	if checker, ok := provider.(interface{ checkMetadata(context.Context) error }); ok {
		return checker.checkMetadata(ctx)
	}
	return nil
}
