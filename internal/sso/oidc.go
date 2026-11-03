package sso

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

type oidcMetadata struct {
	Issuer            string          `json:"issuer"`
	AuthorizationURL  string          `json:"authorization_endpoint"`
	TokenURL          string          `json:"token_endpoint"`
	JWKSURL           string          `json:"jwks_uri"`
	SigningAlgorithms []string        `json:"id_token_signing_alg_values_supported"`
	TokenAuthMethods  json.RawMessage `json:"token_endpoint_auth_methods_supported"`
	PKCEMethods       []string        `json:"code_challenge_methods_supported"`
	ResponseTypes     []string        `json:"response_types_supported"`
	ResponseModes     []string        `json:"response_modes_supported"`
	GrantTypes        []string        `json:"grant_types_supported"`
}

type oidcProvider struct {
	config   Config
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	keySet   *callbackKeySet
}

// newOIDCProvider validates HTTPS discovery, endpoint origins, and advertised protocol capabilities.
// It selects token authentication before exchanging credentials and permits only asymmetric signing algorithms.
func newOIDCProvider(ctx context.Context, config Config, secret string, client *http.Client, policy *destinationPolicy) (Provider, error) {
	if err := requireEndpoint(ctx, policy, config.IssuerURL, true); err != nil {
		return nil, err
	}
	// Keep Discovery HTTPS throughout its redirect chain, in addition to the
	// exact configured Issuer origin enforced by the guarded transport.
	discoveryClient := *client
	discoveryClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := client.CheckRedirect(request, via); err != nil {
			return err
		}
		return policy.validate(request.Context(), request.URL.String(), true)
	}
	ctx = oidc.ClientContext(ctx, &discoveryClient)
	provider, err := oidc.NewProvider(ctx, config.IssuerURL)
	if err != nil {
		// The library's errors may include full response bodies; never propagate them.
		return nil, ErrProviderUnavailable
	}
	var metadata oidcMetadata
	if provider.Claims(&metadata) != nil || metadata.Issuer != config.IssuerURL {
		return nil, ErrProviderUnavailable
	}
	for i, endpoint := range []string{metadata.AuthorizationURL, metadata.TokenURL, metadata.JWKSURL} {
		if err := requireEndpoint(ctx, policy, endpoint, i == 0); err != nil {
			return nil, ErrProviderUnavailable
		}
	}
	// OIDC defaults to Basic only when this optional metadata is omitted.
	// A declared empty, null, or unsupported list must not grant capabilities.
	tokenAuthMethods := []string{"client_secret_basic"}
	if len(metadata.TokenAuthMethods) != 0 && json.Unmarshal(metadata.TokenAuthMethods, &tokenAuthMethods) != nil {
		return nil, ErrProviderUnavailable
	}
	tokenAuthMethod := config.TokenAuthMethod
	if tokenAuthMethod == "auto" {
		tokenAuthMethod = "client_secret_basic"
		if !contains(tokenAuthMethods, tokenAuthMethod) {
			tokenAuthMethod = "client_secret_post"
		}
	}
	if !contains(tokenAuthMethods, tokenAuthMethod) ||
		!contains(metadata.PKCEMethods, "S256") ||
		!contains(metadata.ResponseTypes, "code") ||
		(len(metadata.ResponseModes) > 0 && !contains(metadata.ResponseModes, "query")) ||
		(len(metadata.GrantTypes) > 0 && !contains(metadata.GrantTypes, "authorization_code")) {
		return nil, ErrProviderUnavailable
	}
	var algorithms []string
	for _, algorithm := range metadata.SigningAlgorithms {
		switch algorithm {
		case oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512, oidc.EdDSA:
			algorithms = append(algorithms, algorithm)
		}
	}
	if len(algorithms) == 0 {
		return nil, ErrProviderUnavailable
	}
	keySet := &callbackKeySet{url: metadata.JWKSURL, client: client}
	for _, algorithm := range algorithms {
		keySet.algorithms = append(keySet.algorithms, jose.SignatureAlgorithm(algorithm))
	}
	return &oidcProvider{
		config: config,
		oauth: oauth2.Config{
			ClientID: config.ClientID, ClientSecret: secret,
			// Resolve auto once from verified Discovery; never probe by resending credentials.
			Endpoint:    oauth2.Endpoint{AuthURL: metadata.AuthorizationURL, TokenURL: metadata.TokenURL, AuthStyle: authStyle(tokenAuthMethod)},
			RedirectURL: CallbackURL(config), Scopes: config.Scopes,
		},
		// No Skip* options: signature, expiry, issuer, and client checks stay on.
		verifier: oidc.NewVerifier(config.IssuerURL, keySet, &oidc.Config{ClientID: config.ClientID, SupportedSigningAlgs: algorithms}),
		client:   client,
		keySet:   keySet,
	}, nil
}

// AuthorizationURL requires a valid OIDC flow and requests query-mode authorization with nonce and PKCE.
func (p *oidcProvider) AuthorizationURL(flow AuthorizationFlow) (string, error) {
	if !validAuthorizationFlow(p.config, flow, true) {
		return "", ErrInvalidIdentity
	}
	return p.oauth.AuthCodeURL(flow.State,
		oidc.Nonce(flow.Nonce), oauth2.S256ChallengeOption(flow.PKCEVerifier),
		oauth2.SetAuthURLParam("response_mode", "query")), nil
}

// VerifyCallback validates the response issuer, exchanges the code, and verifies the signed ID token.
// The identity must match issuer, audience, time, and nonce constraints; display claims come from that token.
func (p *oidcProvider) VerifyCallback(ctx context.Context, flow AuthorizationFlow, code, responseIssuer string) (ExternalIdentity, error) {
	if code == "" || len(code) > 8192 || !validExchangeFlow(p.config, flow) || len(flow.NonceHash) != sha256.Size || (responseIssuer != "" && responseIssuer != p.config.IssuerURL) {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.client)
	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow.PKCEVerifier))
	if err != nil {
		return ExternalIdentity{}, ErrProviderUnavailable
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" || len(rawIDToken) > MaxIDTokenBytes {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	keySetStatus := &keySetResult{}
	idToken, err := p.verifier.Verify(context.WithValue(ctx, keySetResultKey{}, keySetStatus), rawIDToken)
	if err != nil {
		if keySetStatus.unavailable {
			return ExternalIdentity{}, ErrProviderUnavailable
		}
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	now := time.Now()
	if idToken.Issuer != p.config.IssuerURL || len(idToken.Audience) != 1 || idToken.Audience[0] != p.config.ClientID || !idToken.Expiry.After(now) || strings.TrimSpace(idToken.Subject) == "" || len(idToken.Subject) > 2048 || hasControl(idToken.Subject) {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	var claims map[string]json.RawMessage
	if idToken.Claims(&claims) != nil {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	if !validNumericDate(claims["iat"], now.Add(60*time.Second)) || !validNumericDate(claims["exp"], time.Time{}) {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	if raw, present := claims["nbf"]; present && !validNumericDate(raw, now.Add(60*time.Second)) {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	if raw, present := claims["azp"]; present {
		var azp string
		if json.Unmarshal(raw, &azp) != nil || azp != p.config.ClientID {
			return ExternalIdentity{}, ErrInvalidIdentity
		}
	}
	var nonce string
	if json.Unmarshal(claims["nonce"], &nonce) != nil || nonce == "" || len(nonce) > 1024 {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	digest := sha256.Sum256([]byte(nonce))
	if subtle.ConstantTimeCompare(digest[:], flow.NonceHash) != 1 {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	claimText := func(name string) string {
		var value string
		if json.Unmarshal(claims[name], &value) != nil {
			return ""
		}
		return displayClaim(value)
	}
	// Display claims come only from the verified ID token. UserInfo is not used.
	return ExternalIdentity{Protocol: "oidc", Authority: idToken.Issuer, Subject: idToken.Subject, DisplayName: claimText("name"), Email: claimText("email")}, nil
}

// checkMetadata fetches the trusted JWKS and requires at least one usable public signing key.
func (p *oidcProvider) checkMetadata(ctx context.Context) error {
	_, err := p.keySet.fetch(ctx)
	return err
}

// validNumericDate accepts finite positive JSON timestamps and applies an optional upper bound.
func validNumericDate(raw json.RawMessage, upper time.Time) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == '"' || string(raw) == "null" {
		return false
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return false
	}
	seconds, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsInf(seconds, 0) || math.IsNaN(seconds) || seconds <= 0 {
		return false
	}
	return upper.IsZero() || seconds <= float64(upper.UnixNano())/float64(time.Second)
}

// contains reports whether a protocol capability list includes the exact requested value.
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
