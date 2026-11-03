package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

type oidcFixture struct {
	server                  *httptest.Server
	key                     *rsa.PrivateKey
	mu                      sync.Mutex
	signingKey              *rsa.PrivateKey
	signingID               string
	publishedKey            *rsa.PrivateKey
	publishedID             string
	changeClaims            func(map[string]any)
	changeMeta              func(map[string]any)
	changeToken             func(map[string]any)
	expectedTokenAuthMethod string
	userinfoCalls           atomic.Int64
	tokenCalls              atomic.Int64
	jwksCalls               atomic.Int64
	flow                    AuthorizationFlow
	config                  Config
}

// newOIDCFixture creates a TLS identity provider with mutable discovery, claims, and signing keys for protocol tests.
func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	nonce := "a-random-oidc-nonce"
	nonceHash := sha256.Sum256([]byte(nonce))
	f := &oidcFixture{key: key, signingKey: key, publishedKey: key, signingID: "key1", publishedID: "key1", expectedTokenAuthMethod: "client_secret_basic"}
	f.flow = AuthorizationFlow{State: "a-random-state", Nonce: nonce, NonceHash: nonceHash[:], PKCEVerifier: oauth2.GenerateVerifier(), RedirectURI: "https://vocat.example/api/auth/sso/callback"}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			metadata := map[string]any{
				"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"}, "response_types_supported": []string{"code"},
			}
			f.mu.Lock()
			if f.changeMeta != nil {
				f.changeMeta(metadata)
			}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(metadata)
		case "/jwks":
			f.jwksCalls.Add(1)
			f.mu.Lock()
			jwk := jose.JSONWebKey{Key: &f.publishedKey.PublicKey, KeyID: f.publishedID, Algorithm: string(jose.RS256), Use: "sig"}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
		case "/token":
			f.tokenCalls.Add(1)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			client, secret, ok := r.BasicAuth()
			f.mu.Lock()
			expectedMethod := f.expectedTokenAuthMethod
			f.mu.Unlock()
			if expectedMethod == "client_secret_post" {
				client, secret, ok = r.Form.Get("client_id"), r.Form.Get("client_secret"), r.Header.Get("Authorization") == ""
			} else if r.Form.Has("client_id") || r.Form.Has("client_secret") {
				ok = false
			}
			if !ok || r.Method != http.MethodPost || client != "vocat-client" || secret != "a-test-secret" || r.Form.Get("redirect_uri") != f.flow.RedirectURI || r.Form.Get("code_verifier") != f.flow.PKCEVerifier || r.Form.Get("grant_type") != "authorization_code" || r.URL.RawQuery != "" {
				t.Errorf("wrong token exchange parameters")
				http.Error(w, "bad exchange", http.StatusBadRequest)
				return
			}
			claims := map[string]any{"iss": f.server.URL, "sub": "stable-user", "aud": "vocat-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": f.flow.Nonce, "name": "Administrator"}
			f.mu.Lock()
			if f.changeClaims != nil {
				f.changeClaims(claims)
			}
			key, kid := f.signingKey, f.signingID
			f.mu.Unlock()
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid).WithHeader("jku", f.server.URL+"/untrusted-keys"))
			if err != nil {
				t.Error(err)
				return
			}
			encoded, _ := json.Marshal(claims)
			object, err := signer.Sign(encoded)
			if err != nil {
				t.Error(err)
				return
			}
			serialized, err := object.CompactSerialize()
			if err != nil {
				t.Error(err)
				return
			}
			tokenResponse := map[string]any{"access_token": "ephemeral-access", "refresh_token": "discarded-refresh", "token_type": "Bearer", "id_token": serialized}
			f.mu.Lock()
			if f.changeToken != nil {
				f.changeToken(tokenResponse)
			}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(tokenResponse)
		default:
			f.userinfoCalls.Add(1)
			http.Error(w, "should never call UserInfo or token-provided jku", http.StatusTeapot)
		}
	}))
	t.Cleanup(f.server.Close)
	f.config = Config{Kind: "pocketid", PublicOrigin: "https://vocat.example", ClientID: "vocat-client", IssuerURL: f.server.URL, ProviderInstanceID: "instance", Scopes: []string{"openid", "profile", "email"}, TokenAuthMethod: "client_secret_basic", PKCEMode: "s256"}
	return f
}

// provider constructs an OIDC adapter using the fixture's trusted TLS transport.
func (f *oidcFixture) provider(t *testing.T) Provider {
	t.Helper()
	p, err := NewProvider(context.Background(), f.config, "a-test-secret", ProviderOptions{HTTPClient: f.server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOIDCPKCEBasicAndIDTokenIdentity checks authorization parameters and identity claims from a verified token only.
func TestOIDCPKCEBasicAndIDTokenIdentity(t *testing.T) {
	f := newOIDCFixture(t)
	p := f.provider(t)
	authorization, err := p.AuthorizationURL(f.flow)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authorization)
	values := u.Query()
	for key, want := range map[string]string{"state": f.flow.State, "nonce": f.flow.Nonce, "redirect_uri": f.flow.RedirectURI, "code_challenge_method": "S256", "code_challenge": oauth2.S256ChallengeFromVerifier(f.flow.PKCEVerifier), "response_type": "code", "response_mode": "query"} {
		if got := values.Get(key); got != want {
			t.Errorf("authorization %s=%q want %q", key, got, want)
		}
	}
	identity, err := p.VerifyCallback(context.Background(), f.flow, "authorization-code", f.server.URL)
	if err != nil || identity.Protocol != "oidc" || identity.Authority != f.server.URL || identity.Subject != "stable-user" || identity.DisplayName != "Administrator" || identity.Email != "" {
		t.Fatalf("unexpected identity: %+v %v", identity, err)
	}
	if f.userinfoCalls.Load() != 0 {
		t.Fatal("UserInfo or token-provided jku was called")
	}
	if err := CheckConfiguration(context.Background(), f.config, "a-test-secret", ProviderOptions{HTTPClient: f.server.Client()}); err != nil {
		t.Fatal(err)
	}
}

// TestOIDCAutoTokenClientAuthentication verifies discovery selects one supported method before the token exchange.
func TestOIDCAutoTokenClientAuthentication(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		advertised []string
		omit       bool
		expected   string
	}{
		{name: "prefer basic regardless of advertisement order", configured: "auto", advertised: []string{"client_secret_post", "client_secret_basic"}, expected: "client_secret_basic"},
		{name: "post-only real token exchange", configured: "auto", advertised: []string{"client_secret_post"}, expected: "client_secret_post"},
		{name: "missing metadata defaults to basic", configured: "auto", omit: true, expected: "client_secret_basic"},
		{name: "empty config automatically selects post", advertised: []string{"client_secret_post"}, expected: "client_secret_post"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOIDCFixture(t)
			f.config.TokenAuthMethod = tc.configured
			f.expectedTokenAuthMethod = tc.expected
			f.changeMeta = func(metadata map[string]any) {
				if tc.omit {
					delete(metadata, "token_endpoint_auth_methods_supported")
				} else {
					metadata["token_endpoint_auth_methods_supported"] = tc.advertised
				}
			}
			p := f.provider(t)
			identity, err := p.VerifyCallback(context.Background(), f.flow, "authorization-code", f.server.URL)
			if err != nil || identity.Subject != "stable-user" {
				t.Fatalf("automatic token authentication failed: %+v, %v", identity, err)
			}
			if calls := f.tokenCalls.Load(); calls != 1 {
				t.Fatalf("token endpoint called %d times; authentication must be selected before the exchange", calls)
			}
		})
	}
}

// TestOIDCTokenClientAuthenticationRejectsUnsupportedMetadata rejects malformed or incompatible capabilities without sending credentials.
func TestOIDCTokenClientAuthenticationRejectsUnsupportedMetadata(t *testing.T) {
	f := newOIDCFixture(t)
	for _, tc := range []struct {
		name       string
		configured string
		advertised any
		omit       bool
	}{
		{name: "auto rejects unknown methods", configured: "auto", advertised: []string{"private_key_jwt", "none"}},
		{name: "auto rejects empty declared list", configured: "auto", advertised: []string{}},
		{name: "auto rejects null declared list", configured: "auto", advertised: nil},
		{name: "auto rejects malformed methods", configured: "auto", advertised: "client_secret_basic"},
		{name: "explicit basic requires advertisement", configured: "client_secret_basic", advertised: []string{"client_secret_post"}},
		{name: "explicit post requires advertisement", configured: "client_secret_post", advertised: []string{"client_secret_basic"}},
		{name: "explicit post rejects missing metadata", configured: "client_secret_post", omit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.config.TokenAuthMethod = tc.configured
			f.mu.Lock()
			f.changeMeta = func(metadata map[string]any) {
				if tc.omit {
					delete(metadata, "token_endpoint_auth_methods_supported")
				} else {
					metadata["token_endpoint_auth_methods_supported"] = tc.advertised
				}
			}
			f.mu.Unlock()
			if _, err := NewProvider(context.Background(), f.config, "a-test-secret", ProviderOptions{HTTPClient: f.server.Client()}); !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("unsupported token authentication accepted or wrong error: %v", err)
			}
			if f.tokenCalls.Load() != 0 {
				t.Fatal("credentials were sent while validating authentication capabilities")
			}
		})
	}
}

// TestOIDCAutoTokenClientAuthenticationDoesNotRetryCredentials ensures authentication rejection cannot trigger credential probing.
func TestOIDCAutoTokenClientAuthenticationDoesNotRetryCredentials(t *testing.T) {
	f := newOIDCFixture(t)
	f.config.TokenAuthMethod = "auto"
	f.changeToken = func(response map[string]any) {
		response["error"] = "invalid_client"
	}
	p := f.provider(t)
	if _, err := p.VerifyCallback(context.Background(), f.flow, "authorization-code", ""); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("token authentication rejection returned unexpected error: %v", err)
	}
	if calls := f.tokenCalls.Load(); calls != 1 {
		t.Fatalf("token authentication rejection retried credentials: %d calls", calls)
	}
}

// TestOIDCRejectsInvalidClaimsAndResponseIssuer checks strict token claims and rejects issuer mismatch before exchange.
func TestOIDCRejectsInvalidClaimsAndResponseIssuer(t *testing.T) {
	f := newOIDCFixture(t)
	p := f.provider(t)
	cases := map[string]func(map[string]any){
		"wrong issuer":    func(c map[string]any) { c["iss"] = "https://different.example" },
		"wrong audience":  func(c map[string]any) { c["aud"] = "other-client" },
		"extra audience":  func(c map[string]any) { c["aud"] = []string{"vocat-client", "other-client"} },
		"wrong azp":       func(c map[string]any) { c["azp"] = "other-client" },
		"null azp":        func(c map[string]any) { c["azp"] = nil },
		"expired":         func(c map[string]any) { c["exp"] = time.Now().Add(-time.Second).Unix() },
		"missing iat":     func(c map[string]any) { delete(c, "iat") },
		"string iat":      func(c map[string]any) { c["iat"] = fmt.Sprint(time.Now().Unix()) },
		"null iat":        func(c map[string]any) { c["iat"] = nil },
		"future iat":      func(c map[string]any) { c["iat"] = time.Now().Add(61 * time.Second).Unix() },
		"future nbf":      func(c map[string]any) { c["nbf"] = time.Now().Add(61 * time.Second).Unix() },
		"string nbf":      func(c map[string]any) { c["nbf"] = fmt.Sprint(time.Now().Unix()) },
		"missing nonce":   func(c map[string]any) { delete(c, "nonce") },
		"wrong nonce":     func(c map[string]any) { c["nonce"] = "another-flow" },
		"empty subject":   func(c map[string]any) { c["sub"] = "" },
		"missing subject": func(c map[string]any) { delete(c, "sub") },
		"number subject":  func(c map[string]any) { c["sub"] = 12345 },
		"null subject":    func(c map[string]any) { c["sub"] = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			f.changeClaims = change
			f.mu.Unlock()
			if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("invalid claims accepted or wrong error: %v", err)
			}
		})
	}
	before := f.tokenCalls.Load()
	if _, err := p.VerifyCallback(context.Background(), f.flow, "code", "https://other.example"); !errors.Is(err, ErrInvalidIdentity) || f.tokenCalls.Load() != before {
		t.Fatal("response issuer mismatch was not rejected before exchange")
	}
}

// TestOIDCSigningKeyRotationAndWrongSignature checks trusted-key refresh supports rotation while rejecting unknown or incorrect signatures.
func TestOIDCSigningKeyRotationAndWrongSignature(t *testing.T) {
	f := newOIDCFixture(t)
	p := f.provider(t)
	if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); err != nil {
		t.Fatal(err)
	}
	secondKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.signingKey, f.signingID = secondKey, "key2"
	f.mu.Unlock()
	if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatal("unknown key was accepted")
	}
	f.mu.Lock()
	f.publishedKey, f.publishedID = secondKey, "key2"
	f.mu.Unlock()
	if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); err != nil {
		t.Fatalf("legitimate key rotation failed: %v", err)
	}
	f.mu.Lock()
	f.signingKey = f.key
	f.mu.Unlock()
	if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatal("wrong signature was accepted")
	}
	if f.jwksCalls.Load() < 2 || f.userinfoCalls.Load() != 0 {
		t.Fatal("key refresh did not use only the trusted JWKS endpoint")
	}
}

// TestOIDCMetadataStrictness checks discovery issuer, endpoints, and required protocol capabilities.
func TestOIDCMetadataStrictness(t *testing.T) {
	f := newOIDCFixture(t)
	for name, change := range map[string]func(map[string]any){
		"issuer slash mismatch":    func(m map[string]any) { m["issuer"] = f.server.URL + "/" },
		"unpermitted token origin": func(m map[string]any) { m["token_endpoint"] = "https://internal.example/token" },
		"no PKCE":                  func(m map[string]any) { delete(m, "code_challenge_methods_supported") },
		"plain PKCE":               func(m map[string]any) { m["code_challenge_methods_supported"] = []string{"plain"} },
		"no code response":         func(m map[string]any) { m["response_types_supported"] = []string{"id_token"} },
		"no JWKS":                  func(m map[string]any) { delete(m, "jwks_uri") },
		"none algorithm":           func(m map[string]any) { m["id_token_signing_alg_values_supported"] = []string{"none"} },
		"symmetric algorithm":      func(m map[string]any) { m["id_token_signing_alg_values_supported"] = []string{"HS256"} },
		"unsupported auth":         func(m map[string]any) { m["token_endpoint_auth_methods_supported"] = []string{"none"} },
	} {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			f.changeMeta = change
			f.mu.Unlock()
			if _, err := NewProvider(context.Background(), f.config, "a-test-secret", ProviderOptions{HTTPClient: f.server.Client()}); err == nil {
				t.Fatal("invalid discovery accepted")
			}
		})
	}
	f.mu.Lock()
	f.changeMeta = nil
	f.expectedTokenAuthMethod = "client_secret_post"
	f.mu.Unlock()
	f.config.TokenAuthMethod = "client_secret_post"
	p := f.provider(t)
	if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); err != nil {
		t.Fatalf("explicit advertised post client auth failed: %v", err)
	}
}

// TestOIDCRequiresActualIDTokenAndAcceptsBoundedClockSkew rejects invalid ID tokens and permits only the configured time tolerance.
func TestOIDCRequiresActualIDTokenAndAcceptsBoundedClockSkew(t *testing.T) {
	f := newOIDCFixture(t)
	p := f.provider(t)
	for name, change := range map[string]func(map[string]any){
		"missing ID token":   func(response map[string]any) { delete(response, "id_token") },
		"empty ID token":     func(response map[string]any) { response["id_token"] = "" },
		"nonstring ID token": func(response map[string]any) { response["id_token"] = 1234 },
		"unsigned ID token": func(response map[string]any) {
			response["id_token"] = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user"}`)) + "."
		},
		"oversized ID token": func(response map[string]any) { response["id_token"] = strings.Repeat("x", MaxIDTokenBytes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			f.changeToken = change
			f.mu.Unlock()
			if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("invalid ID token accepted or wrong error: %v", err)
			}
		})
	}
	f.mu.Lock()
	f.changeToken = nil
	f.changeClaims = func(claims map[string]any) {
		claims["iat"] = time.Now().Add(59 * time.Second).Unix()
		claims["nbf"] = time.Now().Add(59 * time.Second).Unix()
		claims["azp"] = f.config.ClientID
	}
	f.mu.Unlock()
	if _, err := p.VerifyCallback(context.Background(), f.flow, "code", ""); err != nil {
		t.Fatalf("allowed 60-second clock tolerance failed: %v", err)
	}
}

// TestOIDCRejectsUntrustedTLS ensures discovery requires a trusted certificate.
func TestOIDCRejectsUntrustedTLS(t *testing.T) {
	f := newOIDCFixture(t)
	client := &http.Client{Transport: &http.Transport{}}
	if _, err := NewProvider(context.Background(), f.config, "a-test-secret", ProviderOptions{HTTPClient: client}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("untrusted TLS certificate accepted: %v", err)
	}
}

// TestDiscoveryCannotRedirectToHTTPBackend verifies discovery redirects retain HTTPS and sanitize provider errors.
func TestDiscoveryCannotRedirectToHTTPBackend(t *testing.T) {
	var requests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer backend.Close()
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, backend.URL+"/discovery", http.StatusFound)
	}))
	defer issuer.Close()
	c := Config{Kind: "pocketid", PublicOrigin: "https://vocat.example", ClientID: "client", IssuerURL: issuer.URL}
	_, err := NewProvider(context.Background(), c, "secret", ProviderOptions{HTTPClient: issuer.Client()})
	if err == nil || requests.Load() != 0 {
		t.Fatal("Discovery downgraded to HTTP backend")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("provider error exposed secret")
	}
	c.IssuerURL = strings.Replace(issuer.URL, "https:", "HTTPS:", 1)
	_, err = NewProvider(context.Background(), c, "secret", ProviderOptions{HTTPClient: issuer.Client()})
	if err == nil || requests.Load() != 0 {
		t.Fatal("uppercase issuer scheme bypassed discovery HTTPS redirect policy")
	}
}

// TestOIDCRejectsDiscoveryBackendOriginsBeyondConfiguredIssuer checks discovery cannot grant access to unconfigured endpoint origins.
func TestOIDCRejectsDiscoveryBackendOriginsBeyondConfiguredIssuer(t *testing.T) {
	f := newOIDCFixture(t)
	for _, backend := range []string{"https://backend.nas.example", "http://192.168.1.20:1411", strings.Replace(f.server.URL, "https:", "http:", 1)} {
		for _, endpoint := range []string{"token_endpoint", "jwks_uri", "authorization_endpoint"} {
			f.mu.Lock()
			f.changeMeta = func(metadata map[string]any) {
				metadata[endpoint] = backend + "/endpoint"
			}
			f.mu.Unlock()
			if _, err := NewProvider(context.Background(), f.config, "a-test-secret", ProviderOptions{HTTPClient: f.server.Client()}); !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("Discovery granted unconfigured %s origin %s: %v", endpoint, backend, err)
			}
		}
	}
	if f.tokenCalls.Load() != 0 || f.jwksCalls.Load() != 0 || f.userinfoCalls.Load() != 0 {
		t.Fatal("unconfigured Discovery endpoint was contacted")
	}
}
