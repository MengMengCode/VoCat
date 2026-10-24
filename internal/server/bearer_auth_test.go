package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/auth"
	"vocat/internal/store"
)

// newBearerTestApplication mirrors newTestApplication but hands the auth
// service and store back so tests can issue API tokens and inspect the audit
// trail, and returns a cookie-less client to prove bearer requests need no
// session state.
func newBearerTestApplication(t *testing.T) (testApplication, *auth.Service, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authService, err := auth.New(database, auth.Options{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authService.EnsureAdmin(context.Background(), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html>SPA shell</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('ok')")},
	}
	handler, err := New(Options{
		Store:               database,
		Auth:                authService,
		Assets:              fs.FS(assets),
		MaxRequestBodyBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return testApplication{server: httpServer, client: httpServer.Client()}, authService, database
}

func apiErrorCode(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", body, err)
	}
	return envelope.Error.Code
}

func bearerRequest(t *testing.T, method string, url string, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestBearerTokenGrantsCookielessAPIAccess(t *testing.T) {
	app, authService, _ := newBearerTestApplication(t)
	write, err := authService.CreateAPIToken(context.Background(), "writer", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := authService.CreateAPIToken(context.Background(), "reader", true, 0)
	if err != nil {
		t.Fatal(err)
	}

	// No cookie jar anywhere: the header alone must be sufficient.
	response := bearerRequest(t, http.MethodGet, app.server.URL+"/api/devices", write.Token)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET with write token status = %d (%s)", response.StatusCode, apiErrorCode(t, response))
	}

	// A write token passes authentication and CSRF is skipped; the request
	// fails only at the device manager, which the test app does not provide.
	response = bearerRequest(t, http.MethodPost, app.server.URL+"/api/devices", write.Token)
	if response.StatusCode != http.StatusServiceUnavailable ||
		apiErrorCode(t, response) != "device_manager_unavailable" {
		t.Fatalf("POST with write token = %d (%s), want 503 device_manager_unavailable",
			response.StatusCode, apiErrorCode(t, response))
	}

	// read_only rejects mutations before routing, before body validation.
	response = bearerRequest(t, http.MethodPost, app.server.URL+"/api/devices", readOnly.Token)
	if response.StatusCode != http.StatusForbidden || apiErrorCode(t, response) != "token_read_only" {
		t.Fatalf("POST with read-only token = %d (%s), want 403 token_read_only",
			response.StatusCode, apiErrorCode(t, response))
	}
	response = bearerRequest(t, http.MethodGet, app.server.URL+"/api/devices", readOnly.Token)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET with read-only token status = %d (%s)", response.StatusCode, apiErrorCode(t, response))
	}
}

func TestInvalidBearerTokenRejectedWithoutCookieFallthrough(t *testing.T) {
	app, _, _ := newBearerTestApplication(t)

	response := bearerRequest(t, http.MethodGet, app.server.URL+"/api/devices", auth.APITokenPrefix+"forged")
	if response.StatusCode != http.StatusUnauthorized || apiErrorCode(t, response) != "invalid_token" {
		t.Fatalf("GET with forged token = %d (%s), want 401 invalid_token",
			response.StatusCode, apiErrorCode(t, response))
	}

	// A non-Bearer scheme is ignored, so the cookie flow answers instead.
	request, err := http.NewRequest(http.MethodGet, app.server.URL+"/api/devices", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	client := app.server.Client()
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || apiErrorCode(t, response) != "unauthorized" {
		t.Fatalf("GET with Basic scheme = %d (%s), want 401 unauthorized",
			response.StatusCode, apiErrorCode(t, response))
	}

	// A bare scheme with no credential is present-but-invalid and fails
	// closed instead of falling through to cookies.
	if empty := bearerRequest(t, http.MethodGet, app.server.URL+"/api/devices", ""); empty.StatusCode != http.StatusUnauthorized ||
		apiErrorCode(t, empty) != "invalid_token" {
		t.Fatalf("GET with empty bearer = %d (%s), want 401 invalid_token",
			empty.StatusCode, apiErrorCode(t, empty))
	}
}

func TestAuthEndpointsStayCookieOnly(t *testing.T) {
	app, authService, database := newBearerTestApplication(t)
	write, err := authService.CreateAPIToken(context.Background(), "writer", false, 0)
	if err != nil {
		t.Fatal(err)
	}

	// A valid bearer credential must not authenticate the session endpoints;
	// they manage the browser session and answer cookie-only.
	if response := bearerRequest(t, http.MethodGet, app.server.URL+"/api/auth/session", write.Token); response.StatusCode != http.StatusUnauthorized ||
		apiErrorCode(t, response) != "unauthorized" {
		t.Fatalf("GET /api/auth/session with bearer = %d (%s), want 401 unauthorized",
			response.StatusCode, apiErrorCode(t, response))
	}

	// Login is unaffected by a bearer header: the header is ignored and the
	// cookie session is issued normally.
	request, err := http.NewRequest(http.MethodPost, app.server.URL+"/api/auth/login",
		bytes.NewBufferString(`{"username":"admin","password":"correct-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+write.Token)
	response, err := app.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login with bearer header = %d (%s), want 200 (header ignored)",
			response.StatusCode, apiErrorCode(t, response))
	}

	// Junk probes (no vcat_ prefix) stay out of the audit trail.
	_ = bearerRequest(t, http.MethodGet, app.server.URL+"/api/devices", "not-a-vocat-token")
	events, err := database.ListAuditEvents(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Action == "auth.token" {
			t.Fatalf("junk probe wrote an audit row: %+v", event)
		}
	}
}

func TestInvalidBearerKeepsSessionAndWritesAudit(t *testing.T) {
	app, _, database := newBearerTestApplication(t)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	app.client = &http.Client{Jar: jar}
	loginBody := bytes.NewBufferString(`{"username":"admin","password":"correct-password"}`)
	response, err := app.client.Post(app.server.URL+"/api/auth/login", "application/json", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	// A failed bearer attempt must neither clear the session cookies nor
	// take the HTML redirect path.
	if failed := bearerRequest(t, http.MethodGet, app.server.URL+"/api/devices", auth.APITokenPrefix+"forged"); failed.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET with forged token status = %d", failed.StatusCode)
	}
	response, err = app.client.Get(app.server.URL + "/api/devices")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session broken after rejected bearer: status = %d", response.StatusCode)
	}

	// The well-formed but unknown token reaches the audit trail.
	events, err := database.ListAuditEvents(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "auth.token" && event.Outcome == "failure" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no auth.token failure audit event in %+v", events)
	}
}
