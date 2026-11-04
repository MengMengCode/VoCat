package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/auth"
	"vocat/internal/loghub"
	"vocat/internal/store"
)

func TestUserOperationLoggerExcludesReadTraffic(t *testing.T) {
	hub := loghub.New(slog.NewTextHandler(io.Discard, nil), 100)
	server := &Server{logger: slog.New(hub)}
	handler := server.logUserOperation(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if history := hub.History(10, slog.LevelDebug, ""); len(history) != 0 {
		t.Fatalf("GET traffic produced diagnostic logs: %#v", history)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPatch, "/api/devices/dev1/network", nil))
	history := hub.History(10, slog.LevelDebug, "")
	if len(history) != 1 {
		t.Fatalf("mutation log count = %d, want 1", len(history))
	}
	if history[0].Message != "user operation completed" || history[0].Fields["category"] != "network" {
		t.Fatalf("mutation log = %#v", history[0])
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/logs/history", nil))
	if history = hub.History(10, slog.LevelDebug, ""); len(history) != 1 {
		t.Fatalf("log clear endpoint produced an operation log: %#v", history)
	}
}

type testApplication struct {
	server *httptest.Server
	client *http.Client
}

func newTestApplication(t *testing.T) testApplication {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
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
		Assets:              assets,
		MaxRequestBodyBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return testApplication{
		server: httpServer,
		client: &http.Client{Jar: jar},
	}
}

func TestHealthAndSPAFallback(t *testing.T) {
	app := newTestApplication(t)

	response, err := app.client.Get(app.server.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers not present")
	}
	if response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS must not be enabled")
	}

	response, err = app.client.Get(app.server.URL + "/settings/deep/link")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if !bytes.Contains(body, []byte("SPA shell")) {
		t.Fatalf("SPA fallback body = %q", body)
	}

	response, err = app.client.Get(app.server.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
}

func TestLoginSessionCSRFAndLogout(t *testing.T) {
	app := newTestApplication(t)

	loginBody := bytes.NewBufferString(`{"username":"admin","password":"correct-password"}`)
	response, err := app.client.Post(app.server.URL+"/api/auth/login", "application/json", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	var loginResponse struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&loginResponse); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || loginResponse.Data.CSRFToken == "" {
		t.Fatalf("login status = %d, body = %+v", response.StatusCode, loginResponse)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("invalid session cookie: %+v", sessionCookie)
	}

	response, err = app.client.Get(app.server.URL + "/api/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	var sessionResponse struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&sessionResponse); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || sessionResponse.Data.CSRFToken == "" {
		t.Fatalf("session status = %d, body = %+v", response.StatusCode, sessionResponse)
	}

	request, err := http.NewRequest(http.MethodPost, app.server.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without CSRF status = %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost, app.server.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(csrfHeaderName, sessionResponse.Data.CSRFToken)
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", response.StatusCode)
	}

	response, err = app.client.Get(app.server.URL + "/api/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout status = %d", response.StatusCode)
	}
}

func TestUnifiedAPIErrors(t *testing.T) {
	app := newTestApplication(t)

	response, err := app.client.Get(app.server.URL + "/api/not-present")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
	response.Body.Close()

	loginBody := bytes.NewBufferString(`{"username":"admin","password":"correct-password"}`)
	response, err = app.client.Post(app.server.URL+"/api/auth/login", "application/json", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}

	response, err = app.client.Get(app.server.URL + "/api/not-present")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("authenticated not-found status = %d", response.StatusCode)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "not_found" {
		t.Fatalf("error = %+v", envelope.Error)
	}

	badLogin := bytes.NewBufferString(`{"username":"admin","password":"wrong","extra":true}`)
	response, err = app.client.Post(app.server.URL+"/api/auth/login", "application/json", badLogin)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d", response.StatusCode)
	}
}

func TestUnauthenticatedBrowserNavigationRedirectsToLogin(t *testing.T) {
	app := newTestApplication(t)
	client := *app.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	request, err := http.NewRequest(http.MethodGet, app.server.URL+"/api/devices", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("navigation status = %d", response.StatusCode)
	}
	if location := response.Header.Get("Location"); location != "/login" {
		t.Fatalf("navigation location = %q", location)
	}
}

func TestNewRequiresIndex(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authService, err := auth.New(database, auth.Options{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{
		Store:  database,
		Auth:   authService,
		Assets: fs.FS(fstest.MapFS{}),
	}); err == nil {
		t.Fatal("New() unexpectedly accepted assets without index.html")
	}
}

// TestSecureCookieAttributes checks Secure, HttpOnly, SameSite, and deletion attributes for authentication cookies.
func TestSecureCookieAttributes(t *testing.T) {
	recorder := httptest.NewRecorder()
	server := &Server{secureCookies: true}
	server.setAuthCookies(
		recorder,
		"session-token",
		"csrf-token",
		time.Now().Add(time.Hour),
		true,
	)

	var sessionCookie *http.Cookie
	var csrfCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		switch cookie.Name {
		case sessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure ||
		sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("invalid session cookie: %+v", sessionCookie)
	}
	if csrfCookie == nil || csrfCookie.HttpOnly || !csrfCookie.Secure ||
		csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("invalid CSRF cookie: %+v", csrfCookie)
	}
}

func TestUIPreferencesDefaultPublicReadAndPersistedWrite(t *testing.T) {
	app := newTestApplication(t)

	readLanguage := func() (int, string) {
		response, err := app.client.Get(app.server.URL + "/api/settings/preferences")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body struct {
			Data struct {
				Language string `json:"language"`
			} `json:"data"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body.Data.Language
	}

	status, language := readLanguage()
	if status != http.StatusOK || language != "en" {
		t.Fatalf("default preferences = %d %q", status, language)
	}

	putLanguage := func(value string, csrf string) int {
		request, err := http.NewRequest(
			http.MethodPut,
			app.server.URL+"/api/settings/preferences",
			strings.NewReader(`{"language":`+strconv.Quote(value)+`}`),
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			request.Header.Set(csrfHeaderName, csrf)
		}
		response, err := app.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}

	if status := putLanguage("zh", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated write status = %d", status)
	}

	loginBody := bytes.NewBufferString(`{"username":"admin","password":"correct-password"}`)
	response, err := app.client.Post(app.server.URL+"/api/auth/login", "application/json", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	var loginResponse struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&loginResponse); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || loginResponse.Data.CSRFToken == "" {
		t.Fatalf("login status = %d", response.StatusCode)
	}

	if status := putLanguage("fr", loginResponse.Data.CSRFToken); status != http.StatusBadRequest {
		t.Fatalf("invalid language status = %d", status)
	}
	if status := putLanguage("zh", loginResponse.Data.CSRFToken); status != http.StatusOK {
		t.Fatalf("write status = %d", status)
	}
	if status, language := readLanguage(); status != http.StatusOK || language != "zh" {
		t.Fatalf("persisted preferences = %d %q", status, language)
	}
}
