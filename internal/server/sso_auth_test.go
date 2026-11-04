package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/go-jose/go-jose/v4"

	"vocat/internal/auth"
	"vocat/internal/sso"
	"vocat/internal/store"
)

type ssoTestTransport func(*http.Request) (*http.Response, error)

// RoundTrip adapts a fixture function to the HTTP transport interface.
func (transport ssoTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

type ssoHTTPFixture struct {
	server         *httptest.Server
	client         *http.Client
	store          *store.Store
	auth           *auth.Service
	handler        *Server
	providerClient *http.Client
}

// newSSOHTTPFixture creates isolated storage, local authentication, and a deterministic generic OAuth2 provider.
func newSSOHTTPFixture(t *testing.T) *ssoHTTPFixture {
	t.Helper()
	return newSSOHTTPFixtureWithPath(t, ":memory:")
}

// newSSOHTTPFixtureWithPath supports persistent databases and failure injection through an independent SQLite connection.
func newSSOHTTPFixtureWithPath(t *testing.T, path string) *ssoHTTPFixture {
	t.Helper()
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authentication, err := auth.New(database, auth.Options{SessionTTL: time.Hour, BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatal(err)
	}
	if err := authentication.EnsureAdmin(context.Background(), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	providerClient := &http.Client{Transport: ssoTestTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			if r.Form.Get("client_secret") != "fixture-client-secret" || r.Form.Get("code_verifier") == "" || !strings.HasSuffix(r.Form.Get("redirect_uri"), "/api/auth/sso/callback") {
				return nil, fmt.Errorf("invalid token request")
			}
			body = `{"access_token":"fixture-external-token","token_type":"Bearer"}`
		case "/user":
			if r.Header.Get("Authorization") != "Bearer fixture-external-token" {
				return nil, fmt.Errorf("missing access token")
			}
			body = `{"ExternalID":900719925474099312345,"name":"Fixture account","email":"fixture@example.test"}`
		default:
			return nil, fmt.Errorf("unexpected IdP endpoint")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	handler, err := New(Options{Store: database, Auth: authentication,
		Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>fixture</html>")}},
		SSO:    sso.New(database, sso.Options{HTTPClient: providerClient}),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	jar, _ := cookiejar.New(nil)
	return &ssoHTTPFixture{server: httpServer, store: database, auth: authentication, handler: handler, providerClient: providerClient, client: &http.Client{Jar: jar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// request sends a fixture HTTP request with explicit body, CSRF, and cookies and decodes its JSON response.
func (fixture *ssoHTTPFixture) request(t *testing.T, client *http.Client, method, path string, payload any, csrf string, cookies ...*http.Cookie) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	r, err := http.NewRequest(method, fixture.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Accept", "application/json")
	if payload != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		r.Header.Set(csrfHeaderName, csrf)
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return response, body
}

// login establishes a local password session and returns its matching CSRF token.
func (fixture *ssoHTTPFixture) login(t *testing.T) string {
	t.Helper()
	response, body := fixture.request(t, fixture.client, "POST", "/api/auth/login", map[string]any{"username": "admin", "password": "correct-password"}, "")
	if response.StatusCode != 200 {
		t.Fatalf("login = %d: %#v", response.StatusCode, body)
	}
	return body["data"].(map[string]any)["csrf_token"].(string)
}

// config returns a disabled generic provider draft using the fixture origin and endpoints.
func (fixture *ssoHTTPFixture) config() map[string]any {
	return map[string]any{"kind": "generic", "display_name": "Fixture", "enabled": false,
		"public_origin": fixture.server.URL, "client_id": "fixture-client", "auth_url": "https://id.example.test/authorize",
		"token_url": "https://id.example.test/token", "user_info_url": "https://id.example.test/user", "user_id_field": "ExternalID",
		"scopes": []string{"profile"}, "token_auth_method": "client_secret_post", "pkce_mode": "s256"}
}

// ssoFlowCookie locates the callback cookie and checks its path, SameSite, and HttpOnly boundaries.
func ssoFlowCookie(t *testing.T, response *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if strings.HasPrefix(cookie.Name, ssoCookiePrefix) {
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/api/auth/sso" || cookie.Domain != "" {
				t.Fatalf("invalid flow cookie: %#v", cookie)
			}
			return cookie
		}
	}
	t.Fatal("flow cookie missing")
	return nil
}

// assertSSOResponseFields checks the public response contract contains exactly the allowed fields.
func assertSSOResponseFields(t *testing.T, data map[string]any, fields ...string) {
	t.Helper()
	if len(data) != len(fields) {
		t.Fatalf("unexpected response fields: %#v, want %v", data, fields)
	}
	for _, field := range fields {
		if _, ok := data[field]; !ok {
			t.Errorf("response field %q missing from %#v", field, data)
		}
	}
}

// TestSSOHTTPBindLoginAndRevocation covers cross-site binding, login, replay, selective revocation, and secure cookie policy.
func TestSSOHTTPBindLoginAndRevocation(t *testing.T) {
	f := newSSOHTTPFixture(t)
	ctx := context.Background()
	csrf := f.login(t)
	apiCredential, err := f.auth.CreateAPIToken(ctx, "persistent-token", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	config := f.config()
	response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": config, "client_secret": "fixture-client-secret"}, csrf)
	if response.StatusCode != 200 {
		t.Fatalf("save = %d: %#v", response.StatusCode, body)
	}
	assertSSOResponseFields(t, body["data"].(map[string]any), "config", "binding", "configured", "has_client_secret", "reauthentication_required")
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "fixture-client-secret") || strings.Contains(string(encoded), "fixture-external-token") {
		t.Fatal("response leaked secret")
	}
	response, body = f.request(t, f.client, "POST", "/api/settings/sso/check", map[string]any{"expected_revision": 1}, csrf)
	if response.StatusCode != 200 || body["data"].(map[string]any)["revision"] != float64(1) {
		t.Fatalf("check = %d: %#v", response.StatusCode, body)
	}
	assertSSOResponseFields(t, body["data"].(map[string]any), "revision")
	response, body = f.request(t, f.client, "POST", "/api/auth/sso/bind", map[string]any{"expected_revision": 1}, csrf)
	if response.StatusCode != 200 {
		t.Fatalf("bind = %d: %#v", response.StatusCode, body)
	}
	assertSSOResponseFields(t, body["data"].(map[string]any), "authorization_url")
	flowCookie := ssoFlowCookie(t, response)
	authorize, _ := url.Parse(body["data"].(map[string]any)["authorization_url"].(string))
	if authorize.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("PKCE missing")
	}
	callback := "/api/auth/sso/callback?code=fixture-code&state=" + url.QueryEscape(authorize.Query().Get("state"))
	anonymous := &http.Client{CheckRedirect: f.client.CheckRedirect}
	// This request intentionally carries no main Strict session cookie.
	response, _ = f.request(t, anonymous, "GET", callback, nil, "", flowCookie)
	if response.StatusCode != 303 || response.Header.Get("Location") != "/settings?sso_result=bound" {
		t.Fatalf("cross-site bind = %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName || cookie.Name == csrfCookieName {
			t.Fatal("binding must not create or clear main credentials")
		}
	}
	snapshot, err := f.store.SSOSnapshot(ctx)
	if err != nil || snapshot.Binding == nil || snapshot.Binding.Subject != "900719925474099312345" || snapshot.Config.Revision != 2 {
		t.Fatalf("bound snapshot: %#v, %v", snapshot, err)
	}
	config["enabled"] = true
	response, body = f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 2, "config": config}, csrf)
	if response.StatusCode != 200 {
		t.Fatalf("enable = %d: %#v", response.StatusCode, body)
	}
	response, body = f.request(t, anonymous, "GET", "/api/auth/providers", nil, "")
	if response.StatusCode != 200 || body["data"].(map[string]any)["sso"].(map[string]any)["enabled"] != true {
		t.Fatalf("providers = %#v", body)
	}
	assertSSOResponseFields(t, body["data"].(map[string]any), "sso")
	assertSSOResponseFields(t, body["data"].(map[string]any)["sso"].(map[string]any), "enabled", "display_name")
	response, _ = f.request(t, anonymous, "GET", "/api/auth/sso/start?redirect="+url.QueryEscape("/devices/modem-1?tab=sms#latest"), nil, "")
	if response.StatusCode != 302 {
		t.Fatalf("start = %d", response.StatusCode)
	}
	flowCookie = ssoFlowCookie(t, response)
	authorize, _ = url.Parse(response.Header.Get("Location"))
	callback = "/api/auth/sso/callback?code=fixture-code&state=" + url.QueryEscape(authorize.Query().Get("state"))
	// An unrelated browser cannot consume the valid flow.
	response, _ = f.request(t, anonymous, "GET", callback, nil, "")
	if !strings.Contains(response.Header.Get("Location"), "invalid_flow") {
		t.Fatal("missing browser association accepted")
	}
	response, _ = f.request(t, anonymous, "GET", callback, nil, "", flowCookie)
	if response.StatusCode != 303 || response.Header.Get("Location") != "/devices/modem-1?tab=sms#latest" {
		t.Fatalf("SSO login = %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
		if cookie.Name == csrfCookieName {
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("missing Strict local credentials")
	}
	if authenticated, err := f.auth.Authenticate(ctx, sessionCookie.Value); err != nil || authenticated.LoginMethod != "sso" {
		t.Fatalf("SSO session = %#v %v", authenticated, err)
	}
	// Replay fails without clearing the successful local session.
	response, _ = f.request(t, anonymous, "GET", callback, nil, "", flowCookie, sessionCookie, csrfCookie)
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			t.Fatal("replay cleared a valid session")
		}
	}
	config["enabled"] = false
	response, body = f.request(t, anonymous, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 3, "config": config}, csrfCookie.Value, sessionCookie, csrfCookie)
	if response.StatusCode != 200 || body["data"].(map[string]any)["reauthentication_required"] != true {
		t.Fatalf("SSO save reauth = %d: %#v", response.StatusCode, body)
	}
	if _, err := f.auth.Authenticate(ctx, sessionCookie.Value); err == nil {
		t.Fatal("SSO session survived saving config")
	}
	if _, err := f.auth.AuthenticateAPIToken(ctx, apiCredential.Token); err != nil {
		t.Fatalf("API token revoked: %v", err)
	}
	response, body = f.request(t, f.client, "GET", "/api/settings/sso", nil, "")
	if response.StatusCode != 200 {
		t.Fatal("password session revoked")
	}
	assertSSOResponseFields(t, body["data"].(map[string]any), "config", "binding", "configured", "has_client_secret")
	// One saved HTTPS configuration also serves the independent local HTTP entry.
	snapshot, err = f.store.SSOSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Config.Enabled, snapshot.Config.PublicOrigin = true, "https://vocat.example.test"
	if _, _, err := f.store.SaveSSOConfiguration(ctx, store.SSOSaveRequest{ExpectedRevision: snapshot.Config.Revision, Config: snapshot.Config}); err != nil {
		t.Fatal(err)
	}
	response, body = f.request(t, anonymous, "GET", "/api/auth/providers", nil, "")
	if body["data"].(map[string]any)["sso"].(map[string]any)["enabled"] != true {
		t.Fatal("HTTPS SSO requires a manual cookie override")
	}
	response, _ = f.request(t, anonymous, "GET", "/api/auth/sso/start", nil, "")
	if response.StatusCode != http.StatusFound {
		t.Fatal("local HTTP alternative rejected")
	}
	target, _ := url.Parse(response.Header.Get("Location"))
	if target.Query().Get("redirect_uri") != f.server.URL+sso.CallbackPath {
		t.Fatal("local entry did not select its own callback")
	}

	snapshot, err = f.store.SSOSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response, body = f.request(t, f.client, "DELETE", "/api/auth/sso/binding", map[string]any{"expected_revision": snapshot.Config.Revision}, csrf)
	if response.StatusCode != http.StatusOK || body["data"].(map[string]any)["reauthentication_required"] != false {
		t.Fatalf("password-session unbind = %d: %#v", response.StatusCode, body)
	}
	assertSSOResponseFields(t, body["data"].(map[string]any), "reauthentication_required")
}

// TestSSOMutationsRejectCrossSessionCSRF checks matching cookie/header tokens still belong to the authenticated session.
func TestSSOMutationsRejectCrossSessionCSRF(t *testing.T) {
	f := newSSOHTTPFixture(t)
	ctx := context.Background()
	csrf := f.login(t)
	response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": f.config(), "client_secret": "fixture-client-secret"}, csrf)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save = %d: %#v", response.StatusCode, body)
	}
	response, body = f.request(t, f.client, "POST", "/api/auth/sso/bind", map[string]any{"expected_revision": 1}, csrf)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bind = %d: %#v", response.StatusCode, body)
	}
	flowCookie := ssoFlowCookie(t, response)
	authorize, err := url.Parse(body["data"].(map[string]any)["authorization_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	anonymous := &http.Client{CheckRedirect: f.client.CheckRedirect}
	response, _ = f.request(t, anonymous, "GET", "/api/auth/sso/callback?code=fixture-code&state="+url.QueryEscape(authorize.Query().Get("state")), nil, "", flowCookie)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/settings?sso_result=bound" {
		t.Fatalf("binding callback failed: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	before, err := f.store.SSOSnapshot(ctx)
	if err != nil || before.Binding == nil {
		t.Fatalf("bound snapshot unavailable: %v", err)
	}
	other, err := f.auth.PrepareCredentials(auth.Principal{ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateSession(ctx, 1, auth.TokenHash(other.SessionToken), auth.TokenHash(other.CSRFToken), other.ExpiresAt, false); err != nil {
		t.Fatal(err)
	}
	serverURL, _ := url.Parse(f.server.URL)
	var sessionCookie *http.Cookie
	for _, cookie := range f.client.Jar.Cookies(serverURL) {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("authenticated session cookie missing")
	}
	config := f.config()
	config["display_name"] = "must not be saved"
	for _, endpoint := range []struct {
		method, path string
		payload      map[string]any
	}{
		{"PUT", "/api/settings/sso", map[string]any{"expected_revision": before.Config.Revision, "config": config}},
		{"POST", "/api/auth/sso/bind", map[string]any{"expected_revision": before.Config.Revision}},
		{"DELETE", "/api/auth/sso/binding", map[string]any{"expected_revision": before.Config.Revision}},
		{"POST", "/api/settings/sso/check", map[string]any{"expected_revision": before.Config.Revision}},
	} {
		t.Run(endpoint.method+" "+endpoint.path, func(t *testing.T) {
			response, body := f.request(t, anonymous, endpoint.method, endpoint.path, endpoint.payload, other.CSRFToken,
				sessionCookie, &http.Cookie{Name: csrfCookieName, Value: other.CSRFToken})
			if response.StatusCode != http.StatusForbidden || body["error"].(map[string]any)["code"] != "invalid_csrf" {
				t.Fatalf("cross-session CSRF = %d: %#v", response.StatusCode, body)
			}
			if len(response.Cookies()) != 0 {
				t.Fatal("rejected mutation changed browser cookies")
			}
			after, err := f.store.SSOSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected mutation changed SSO settings, secret, or binding: %v", err)
			}
		})
	}
}

// TestSSOCredentialManagementRejectsBearerAndKeepsSession rejects mixed bearer/browser management without revoking the browser session.
func TestSSOCredentialManagementRejectsBearerAndKeepsSession(t *testing.T) {
	f := newSSOHTTPFixture(t)
	csrf := f.login(t)
	credential, err := f.auth.CreateAPIToken(context.Background(), "management-probe", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/api/settings/sso"}, {"PUT", "/api/settings/sso"}, {"POST", "/api/settings/sso/check"}, {"POST", "/api/auth/sso/bind"}, {"DELETE", "/api/auth/sso/binding"},
	} {
		r, _ := http.NewRequest(endpoint.method, f.server.URL+endpoint.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+credential.Token)
		r.Header.Set(csrfHeaderName, csrf)
		response, err := f.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Errorf("mixed Bearer+Cookie %s = %d", endpoint.path, response.StatusCode)
		}
	}
	response, _ := f.request(t, f.client, "GET", "/api/auth/session", nil, "")
	if response.StatusCode != 200 {
		t.Fatal("rejected management request revoked local session")
	}
}

// TestSSORemovedManagementFieldsRejectedWithoutChangingState checks obsolete request fields cannot alter configuration or bindings.
func TestSSORemovedManagementFieldsRejectedWithoutChangingState(t *testing.T) {
	f := newSSOHTTPFixture(t)
	csrf := f.login(t)
	response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": f.config(), "client_secret": "fixture-client-secret"}, csrf)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save = %d: %#v", response.StatusCode, body)
	}
	before, err := f.store.SSOSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"replace_existing", true}, {"replace_existing", false}, {"current_password", "wrong"},
	} {
		payload := map[string]any{"expected_revision": before.Config.Revision, field.name: field.value}
		response, body = f.request(t, f.client, "POST", "/api/auth/sso/bind", payload, csrf)
		if response.StatusCode != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "invalid_request" {
			t.Fatalf("removed bind field %s was accepted: %d %#v", field.name, response.StatusCode, body)
		}
		for _, cookie := range response.Cookies() {
			if strings.HasPrefix(cookie.Name, ssoCookiePrefix) {
				t.Fatal("rejected bind request created a flow cookie")
			}
		}
	}
	for _, clear := range []bool{true, false} {
		response, body = f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": before.Config.Revision, "config": f.config(), "clear_client_secret": clear}, csrf)
		if response.StatusCode != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "invalid_request" {
			t.Fatalf("removed clear field was accepted: %d %#v", response.StatusCode, body)
		}
	}
	after, err := f.store.SSOSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected obsolete fields changed configuration, secret, or binding: %v", err)
	}
	response, _ = f.request(t, f.client, "GET", "/api/auth/session", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatal("obsolete management fields revoked the password session")
	}
}

// TestSSOSecretAndRevisionRequestValidation rejects invalid secret updates, read-only fields, stale revisions, and insecure HTTPS activation.
func TestSSOSecretAndRevisionRequestValidation(t *testing.T) {
	f := newSSOHTTPFixture(t)
	csrf := f.login(t)
	for _, secret := range []any{"", nil, "********", "••••••"} {
		response, _ := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": f.config(), "client_secret": secret}, csrf)
		if response.StatusCode != 400 {
			t.Errorf("invalid secret %v = %d", secret, response.StatusCode)
		}
	}
	config := f.config()
	config["revision"] = 99
	response, _ := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": config}, csrf)
	if response.StatusCode != 400 {
		t.Fatal("accepted read-only config revision")
	}
	response, _ = f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"config": f.config()}, csrf)
	if response.StatusCode != 400 {
		t.Fatal("accepted missing expected_revision")
	}
	response, _ = f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 10, "config": f.config()}, csrf)
	if response.StatusCode != 409 {
		t.Fatal("accepted stale revision")
	}
	// HTTPS activation proceeds to the binding check without a manual Cookie flag.
	config = f.config()
	config["enabled"], config["public_origin"] = true, "HTTPS://vocat.example.test"
	response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": config}, csrf)
	if response.StatusCode != 409 || body["error"].(map[string]any)["code"] != "sso_identity_not_bound" {
		t.Fatalf("HTTPS did not reach the binding requirement: %d %#v", response.StatusCode, body)
	}
}

// TestSSOSettingsSaveExplicitPrivateBackendDraftWithoutConnectionOptions verifies offline draft saves and the supported field whitelist.
func TestSSOSettingsSaveExplicitPrivateBackendDraftWithoutConnectionOptions(t *testing.T) {
	f := newSSOHTTPFixture(t)
	csrf := f.login(t)
	config := f.config()
	config["auth_url"] = "https://id.nas.invalid/authorize"
	config["token_url"] = "http://192.168.1.20:1411/token"
	config["user_info_url"] = "http://id.nas.invalid:1411/userinfo"
	response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": config}, csrf)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("explicit private backend draft was not saved offline: %d %#v", response.StatusCode, body)
	}
	saved := body["data"].(map[string]any)["config"].(map[string]any)
	for _, field := range []string{"auth_url", "token_url", "user_info_url"} {
		if saved[field] != config[field] {
			t.Errorf("saved %s changed: %v want %v", field, saved[field], config[field])
		}
	}
	supportedFields := map[string]bool{
		"revision": true, "enabled": true, "kind": true, "display_name": true,
		"public_origin": true, "client_id": true, "issuer_url": true, "auth_url": true, "token_url": true,
		"user_info_url": true, "user_id_field": true, "scopes": true, "token_auth_method": true, "pkce_mode": true,
	}
	for field := range saved {
		if !supportedFields[field] {
			t.Errorf("settings API returned an unsupported configuration field: %s", field)
		}
	}
	config["arbitrary_network_permission"] = true
	response, _ = f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": saved["revision"], "config": config}, csrf)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("settings API accepted an unsupported network permission: %d", response.StatusCode)
	}
}

// TestSSOCheckFetchesDiscoveryAndJWKSWithoutBindingAnAccount checks readiness fetches metadata and keys without authenticating an identity.
func TestSSOCheckFetchesDiscoveryAndJWKSWithoutBindingAnAccount(t *testing.T) {
	f := newSSOHTTPFixture(t)
	csrf := f.login(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "check-key", Use: "sig", Algorithm: string(jose.ES256)}}})
	if err != nil {
		t.Fatal(err)
	}
	var discoveryCalls, jwksCalls, credentialCalls atomic.Int64
	var rejectDiscovery, rejectJWKS atomic.Bool
	f.providerClient.Transport = ssoTestTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			discoveryCalls.Add(1)
			metadata := map[string]any{
				"issuer": "https://id.example.test", "authorization_endpoint": "https://id.example.test/authorize",
				"token_endpoint": "https://id.example.test/token", "jwks_uri": "https://id.example.test/jwks",
				"id_token_signing_alg_values_supported": []string{"ES256"}, "code_challenge_methods_supported": []string{"S256"},
				"token_endpoint_auth_methods_supported": []string{"client_secret_basic"}, "response_types_supported": []string{"code"},
			}
			if rejectDiscovery.Load() {
				metadata["issuer"] = "https://different.example.test"
			}
			encoded, _ := json.Marshal(metadata)
			body = string(encoded)
		case "/jwks":
			jwksCalls.Add(1)
			body = string(jwks)
			if rejectJWKS.Load() {
				status, body = http.StatusServiceUnavailable, "unavailable"
			}
		default:
			credentialCalls.Add(1)
			return nil, fmt.Errorf("configuration check contacted a credential endpoint")
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("configuration metadata request carried credentials or used a non-GET method")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	config := map[string]any{"kind": "pocketid", "enabled": false, "public_origin": f.server.URL, "client_id": "check-client", "issuer_url": "https://id.example.test"}
	response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": config, "client_secret": "check-only-secret"}, csrf)
	if response.StatusCode != http.StatusOK || discoveryCalls.Load() != 0 || jwksCalls.Load() != 0 {
		t.Fatalf("disabled draft save performed remote validation or failed: %d %#v", response.StatusCode, body)
	}
	before, err := f.store.SSOSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response, body = f.request(t, f.client, "POST", "/api/settings/sso/check", map[string]any{"expected_revision": before.Config.Revision}, csrf)
	if response.StatusCode != http.StatusOK || discoveryCalls.Load() != 1 || jwksCalls.Load() != 1 || credentialCalls.Load() != 0 {
		t.Fatalf("check skipped real Discovery/JWKS or contacted credential endpoints: %d %#v", response.StatusCode, body)
	}
	assertSSOResponseFields(t, body["data"].(map[string]any), "revision")
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName || cookie.Name == csrfCookieName {
			t.Fatal("configuration check issued local credentials")
		}
	}
	rejectJWKS.Store(true)
	response, body = f.request(t, f.client, "POST", "/api/settings/sso/check", map[string]any{"expected_revision": before.Config.Revision}, csrf)
	if response.StatusCode != http.StatusBadGateway || body["error"].(map[string]any)["code"] != "sso_provider_unavailable" || jwksCalls.Load() != 2 {
		t.Fatalf("failed JWKS fetch was ignored: %d %#v", response.StatusCode, body)
	}
	rejectDiscovery.Store(true)
	response, body = f.request(t, f.client, "POST", "/api/settings/sso/check", map[string]any{"expected_revision": before.Config.Revision}, csrf)
	if response.StatusCode != http.StatusBadGateway || body["error"].(map[string]any)["code"] != "sso_provider_unavailable" || discoveryCalls.Load() != 3 || jwksCalls.Load() != 2 {
		t.Fatalf("invalid Discovery was ignored: %d %#v", response.StatusCode, body)
	}
	after, err := f.store.SSOSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) || after.Binding != nil || credentialCalls.Load() != 0 {
		t.Fatalf("configuration checks changed authorization state: %v", err)
	}
}

// TestSSOBindCallbackPreservesSameIdentityAndRejectsDifferentIdentity checks safe metadata refresh and account replacement rejection.
func TestSSOBindCallbackPreservesSameIdentityAndRejectsDifferentIdentity(t *testing.T) {
	f := newSSOHTTPFixture(t)
	csrf := f.login(t)
	response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": f.config(), "client_secret": "fixture-client-secret"}, csrf)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save = %d: %#v", response.StatusCode, body)
	}
	callback := func(revision int64) *http.Response {
		t.Helper()
		response, body := f.request(t, f.client, "POST", "/api/auth/sso/bind", map[string]any{"expected_revision": revision}, csrf)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("bind = %d: %#v", response.StatusCode, body)
		}
		authorize, _ := url.Parse(body["data"].(map[string]any)["authorization_url"].(string))
		anonymous := &http.Client{CheckRedirect: f.client.CheckRedirect}
		response, _ = f.request(t, anonymous, "GET", "/api/auth/sso/callback?code=fixture-code&state="+url.QueryEscape(authorize.Query().Get("state")), nil, "", ssoFlowCookie(t, response))
		for _, cookie := range response.Cookies() {
			if cookie.Name == sessionCookieName || cookie.Name == csrfCookieName {
				t.Fatal("binding callback altered main credentials")
			}
		}
		return response
	}
	response = callback(1)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/settings?sso_result=bound" {
		t.Fatalf("initial bind = %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	first, err := f.store.SSOSnapshot(context.Background())
	if err != nil || first.Binding == nil {
		t.Fatal("initial binding was not stored")
	}
	response = callback(first.Config.Revision)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/settings?sso_result=bound" {
		t.Fatalf("same identity bind = %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	same, err := f.store.SSOSnapshot(context.Background())
	if err != nil || same.Binding == nil || same.Binding.ID != first.Binding.ID || same.Config.Revision != first.Config.Revision+1 {
		t.Fatalf("same identity bind changed the binding identity: %v", err)
	}
	baseTransport := f.providerClient.Transport
	f.providerClient.Transport = ssoTestTransport(func(r *http.Request) (*http.Response, error) {
		response, err := baseTransport.RoundTrip(r)
		if err == nil && r.URL.Path == "/user" {
			response.Body.Close()
			response.Body = io.NopCloser(strings.NewReader(`{"ExternalID":"different-account","name":"Other account"}`))
		}
		return response, err
	})
	response = callback(same.Config.Revision)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/settings?sso_error=binding_exists" {
		t.Fatalf("different identity bind = %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	after, err := f.store.SSOSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(same, after) {
		t.Fatalf("binding conflict changed existing state: %v", err)
	}
	response, _ = f.request(t, f.client, "GET", "/api/auth/session", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatal("binding conflict revoked the initiating password session")
	}
}

// TestSSOCallbackRechecksConfigAfterBlockedTokenExchange checks a concurrent settings update invalidates an in-flight callback.
func TestSSOCallbackRechecksConfigAfterBlockedTokenExchange(t *testing.T) {
	for _, purpose := range []string{"bind", "login"} {
		t.Run(purpose, func(t *testing.T) {
			f := newSSOHTTPFixture(t)
			csrf := f.login(t)
			response, body := f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": f.config(), "client_secret": "fixture-client-secret"}, csrf)
			if response.StatusCode != 200 {
				t.Fatalf("save: %d %#v", response.StatusCode, body)
			}
			anonymous := &http.Client{CheckRedirect: f.client.CheckRedirect}
			startBinding := func(revision int64) (*http.Cookie, string) {
				t.Helper()
				response, body := f.request(t, f.client, "POST", "/api/auth/sso/bind", map[string]any{"expected_revision": revision}, csrf)
				if response.StatusCode != 200 {
					t.Fatalf("bind: %d %#v", response.StatusCode, body)
				}
				u, _ := url.Parse(body["data"].(map[string]any)["authorization_url"].(string))
				return ssoFlowCookie(t, response), "/api/auth/sso/callback?code=fixture-code&state=" + url.QueryEscape(u.Query().Get("state"))
			}
			flowCookie, callback := startBinding(1)
			if purpose == "login" {
				response, _ = f.request(t, anonymous, "GET", callback, nil, "", flowCookie)
				if response.StatusCode != 303 || !strings.Contains(response.Header.Get("Location"), "sso_result=bound") {
					t.Fatal("initial binding failed")
				}
				config := f.config()
				config["enabled"] = true
				response, body = f.request(t, f.client, "PUT", "/api/settings/sso", map[string]any{"expected_revision": 2, "config": config}, csrf)
				if response.StatusCode != 200 {
					t.Fatalf("enable: %d %#v", response.StatusCode, body)
				}
				response, _ = f.request(t, anonymous, "GET", "/api/auth/sso/start", nil, "")
				flowCookie = ssoFlowCookie(t, response)
				u, _ := url.Parse(response.Header.Get("Location"))
				callback = "/api/auth/sso/callback?code=fixture-code&state=" + url.QueryEscape(u.Query().Get("state"))
			}
			entered, release := make(chan struct{}), make(chan struct{})
			baseTransport := f.providerClient.Transport
			f.providerClient.Transport = ssoTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/token" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return baseTransport.RoundTrip(r)
			})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("GET", f.server.URL+callback, nil)
				r.RemoteAddr = "127.0.0.1:12345"
				r.AddCookie(flowCookie)
				w := httptest.NewRecorder()
				f.handler.ServeHTTP(w, r)
				result <- w
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("token exchange was not reached")
			}
			snapshot, err := f.store.SSOSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Config.Enabled = false
			if _, _, err := f.store.SaveSSOConfiguration(context.Background(), store.SSOSaveRequest{ExpectedRevision: snapshot.Config.Revision, Config: snapshot.Config}); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case w := <-result:
				if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "sso_error=config_changed") {
					t.Fatalf("late callback = %d %s", w.Code, w.Header().Get("Location"))
				}
				for _, cookie := range w.Result().Cookies() {
					if cookie.Name == sessionCookieName {
						t.Fatal("late callback issued or cleared local session")
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("callback did not finish")
			}
		})
	}
}

// TestSSORoutesKeepNetworkPolicyAndIndependentRateLimits checks SSO routes retain network access policy and independent request quotas.
func TestSSORoutesKeepNetworkPolicyAndIndependentRateLimits(t *testing.T) {
	f := newSSOHTTPFixture(t)
	for _, path := range []string{"/api/auth/providers", "/api/auth/sso/start", "/api/auth/sso/callback", "/api/settings/sso"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "203.0.113.1:443"
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "network_access_denied") {
			t.Errorf("SSO route bypassed network policy: %s %d", path, w.Code)
		}
	}
	for attempt := 0; attempt <= ssoStartPerMinute; attempt++ {
		response, _ := f.request(t, f.client, "GET", "/api/auth/sso/start", nil, "")
		if attempt == ssoStartPerMinute && response.StatusCode != 429 {
			t.Fatal("start limiter did not reject")
		}
	}
	// IdP failures and start throttling do not enter password lockout state.
	f.login(t)
}
