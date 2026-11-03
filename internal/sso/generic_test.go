package sso

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

// TestGenericStableIDsAndPKCE checks exact stable IDs, PKCE modes, client authentication, and sanitized provider failures.
func TestGenericStableIDsAndPKCE(t *testing.T) {
	var server *httptest.Server
	var userJSON = `{"customUserID":9007199254740993123456789,"name":"First Name"}`
	var tokenBody = `{"access_token":"temporary-access","token_type":"Bearer"}`
	var tokenStatus, userStatus = http.StatusOK, http.StatusOK
	var disabledPKCE, basicAuth bool
	flow := AuthorizationFlow{State: "state", PKCEVerifier: oauth2.GenerateVerifier(), RedirectURI: "https://vocat.example/api/auth/sso/callback"}
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("redirect_uri") != flow.RedirectURI || r.URL.RawQuery != "" {
				t.Error("invalid generic token request")
			}
			if basicAuth {
				client, secret, ok := r.BasicAuth()
				if !ok || client != "client" || secret != "secret" || r.Form.Get("client_secret") != "" {
					t.Error("explicit Basic authentication was not respected")
				}
			} else if r.Form.Get("client_id") != "client" || r.Form.Get("client_secret") != "secret" || r.Header.Get("Authorization") != "" {
				t.Error("default post authentication was not respected")
			}
			if (!disabledPKCE && r.Form.Get("code_verifier") != flow.PKCEVerifier) || (disabledPKCE && r.Form.Get("code_verifier") != "") {
				t.Error("wrong PKCE token parameters")
			}
			w.WriteHeader(tokenStatus)
			io.WriteString(w, tokenBody)
		case "/user":
			if r.Header.Get("Authorization") != "Bearer temporary-access" {
				t.Error("user information request missing bearer token")
			}
			w.WriteHeader(userStatus)
			io.WriteString(w, userJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := Config{Kind: "generic", ProviderInstanceID: "immutable-instance", ClientID: "client", PublicOrigin: "https://vocat.example", AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token", UserInfoURL: server.URL + "/user", UserIDField: "customUserID"}
	newProvider := func() Provider {
		t.Helper()
		p, err := NewProvider(context.Background(), c, "secret", ProviderOptions{HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := newProvider()
	identity, err := p.VerifyCallback(context.Background(), flow, "code", "")
	if err != nil || identity.Subject != "9007199254740993123456789" || identity.Authority != "immutable-instance" || identity.Protocol != "oauth2" {
		t.Fatalf("large integer not preserved: %+v %v", identity, err)
	}
	authorize, _ := p.AuthorizationURL(flow)
	u, _ := url.Parse(authorize)
	if u.Query().Get("code_challenge") != oauth2.S256ChallengeFromVerifier(flow.PKCEVerifier) || u.Query().Get("redirect_uri") != flow.RedirectURI {
		t.Fatal("generic authorization missing PKCE or redirect")
	}
	userJSON = `{"customUserID":"Exact Case Preserved","name":"New Name"}`
	identity, err = p.VerifyCallback(context.Background(), flow, "code", "")
	if err != nil || identity.Subject != "Exact Case Preserved" || identity.DisplayName != "New Name" {
		t.Fatalf("string ID not preserved: %+v %v", identity, err)
	}
	for _, value := range []string{`""`, `"   "`, "null", "true", "[]", "{}", "1.5", "1e3"} {
		userJSON = `{"customUserID":` + value + `}`
		if _, err := p.VerifyCallback(context.Background(), flow, "code", ""); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("invalid user ID accepted: %s (%v)", value, err)
		}
	}
	for _, raw := range []string{"<html>failure</html>", `{"customUserID":"ok"}{}`, `[]`, `null`} {
		userJSON = raw
		if _, err := p.VerifyCallback(context.Background(), flow, "code", ""); !errors.Is(err, ErrProviderUnavailable) {
			t.Errorf("invalid JSON accepted: %s (%v)", raw, err)
		}
	}
	userJSON = `{"customUserID":"ok"}`
	userStatus = http.StatusForbidden
	if _, err := p.VerifyCallback(context.Background(), flow, "code", ""); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("user information failure accepted")
	}
	userStatus = http.StatusOK
	for _, raw := range []string{`{}`, `{"access_token":""}`, `<html>client-secret failure</html>`} {
		tokenBody = raw
		if _, err := p.VerifyCallback(context.Background(), flow, "code", ""); !errors.Is(err, ErrProviderUnavailable) || strings.Contains(err.Error(), "client-secret") {
			t.Errorf("token response not rejected/sanitized: %v", err)
		}
	}
	tokenBody = `{"access_token":"temporary-access","token_type":"Bearer"}`
	tokenStatus = http.StatusUnauthorized
	if _, err := p.VerifyCallback(context.Background(), flow, "code", ""); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("bad client credentials accepted")
	}
	tokenStatus = http.StatusOK
	c.PKCEMode, c.TokenAuthMethod = "disabled", "client_secret_basic"
	disabledPKCE, basicAuth = true, true
	p = newProvider()
	if _, err := p.VerifyCallback(context.Background(), flow, "code", ""); err != nil {
		t.Fatalf("explicit legacy PKCE exception failed: %v", err)
	}
	authorize, _ = p.AuthorizationURL(flow)
	u, _ = url.Parse(authorize)
	if u.Query().Get("code_challenge") != "" {
		t.Fatal("disabled PKCE still sent challenge")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip adapts a fixture function to the HTTP transport interface.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestGitHubFixedEndpointsNumericIdentityAndUsernameChange checks fixed GitHub endpoints and identity stability across renames.
func TestGitHubFixedEndpointsNumericIdentityAndUsernameChange(t *testing.T) {
	flow := AuthorizationFlow{State: "state", PKCEVerifier: oauth2.GenerateVerifier(), RedirectURI: "https://vocat.example/api/auth/sso/callback"}
	login := "old-login"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			r.ParseForm()
			if r.Form.Get("code_verifier") != flow.PKCEVerifier || r.Form.Get("redirect_uri") != flow.RedirectURI {
				t.Error("GitHub exchange did not retain callback/PKCE")
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "github-access", "token_type": "Bearer"})
		case "/user":
			io.WriteString(w, `{"id":9007199254740993123456789,"login":"`+login+`"}`)
		default:
			t.Error("GitHub used non-fixed endpoint")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fixtureURL, _ := url.Parse(server.URL)
	base := server.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "github.com" && request.URL.Host != "api.github.com" {
			t.Error("unexpected production GitHub host")
		}
		clone := request.Clone(request.Context())
		copyURL := *request.URL
		clone.URL = &copyURL
		clone.URL.Scheme, clone.URL.Host = fixtureURL.Scheme, fixtureURL.Host
		return base.RoundTrip(clone)
	})}
	c := Config{Kind: "github", ClientID: "github-client", PublicOrigin: "https://vocat.example"}
	p, err := NewProvider(context.Background(), c, "github-secret", ProviderOptions{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	authorize, _ := p.AuthorizationURL(flow)
	u, _ := url.Parse(authorize)
	if u.Scheme != "https" || u.Host != "github.com" || u.Path != "/login/oauth/authorize" || u.Query().Get("scope") != "read:user" || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("incorrect GitHub authorize URL")
	}
	first, err := p.VerifyCallback(context.Background(), flow, "code", "")
	if err != nil {
		t.Fatal(err)
	}
	login = "renamed-login"
	second, err := p.VerifyCallback(context.Background(), flow, "code", "")
	if err != nil || first.Subject != "9007199254740993123456789" || first.Subject != second.Subject || second.DisplayName != login || second.Authority != "https://github.com" {
		t.Fatalf("GitHub identity changed with login name: %+v %+v %v", first, second, err)
	}
}
