package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"vocat/internal/auth"
	"vocat/internal/store"
)

// TestSSOFlowCookieSecureOverride covers issuance and deletion under the global override.
func TestSSOFlowCookieSecureOverride(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, origin := range []string{"http://192.168.1.20:7575", "https://vocat.example"} {
			for _, clear := range []bool{false, true} {
				s := &Server{secureCookies: force}
				flow := store.SSOFlow{ID: "test-flow", RedirectURI: origin + "/api/auth/sso/callback", ExpiresAt: time.Now().Add(time.Minute)}
				w := httptest.NewRecorder()
				s.setSSOFlowCookie(w, flow, "browser-token", clear)
				cookies := w.Result().Cookies()
				want := force || origin == "https://vocat.example"
				if len(cookies) != 1 {
					t.Fatal("missing flow cookie")
				}
				c := cookies[0]
				if c.Secure != want || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/auth/sso" {
					t.Fatalf("force=%v origin=%s clear=%v: invalid cookie flags", force, origin, clear)
				}
				if clear && (c.Value != "" || c.MaxAge != -1) {
					t.Fatal("flow cookie was not cleared")
				}
			}
		}
	}
}

// TestSessionCSRFReplacementRemembersSecureLogin reproduces hint-free proxy refreshes with missing or stale CSRF cookies.
func TestSessionCSRFReplacementRemembersSecureLogin(t *testing.T) {
	for _, tc := range []struct {
		name, entry, origin             string
		directTLS, trust, force, secure bool
	}{
		{name: "HTTPS browser behind default proxy", entry: "https://vocat.example", origin: "https://vocat.example", secure: true},
		{name: "direct TLS", entry: "https://vocat.example", directTLS: true, secure: true},
		{name: "trusted proxy", entry: "http://vocat.example", trust: true, secure: true},
		{name: "force Secure", entry: "http://vocat.example", force: true, secure: true},
		{name: "HTTP API client", entry: "http://vocat.example"},
		{name: "private HTTP browser", entry: "http://192.168.111.27:7575", origin: "http://192.168.111.27:7575"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSSOHTTPFixture(t)
			if err := f.auth.ResetAdminCredentials(context.Background(), "admin", "cookie-policy-fixture"); err != nil {
				t.Fatal(err)
			}
			f.handler.secureCookies = tc.force
			f.handler.access.trustProxy = tc.trust
			r := httptest.NewRequest("POST", tc.entry+"/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"cookie-policy-fixture"}`))
			r.Header.Set("Content-Type", "application/json")
			if !tc.directTLS {
				r.TLS = nil
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.trust {
				r.Header.Set("X-Forwarded-Proto", "https")
			}
			w := httptest.NewRecorder()
			r.RemoteAddr = "127.0.0.1:12345"
			f.handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("login=%d", w.Code)
			}
			var session *http.Cookie
			var validCSRF string
			for _, c := range w.Result().Cookies() {
				if c.Name == sessionCookieName {
					session = c
				}
				if c.Name == csrfCookieName {
					validCSRF = c.Value
				}
			}
			if session == nil || validCSRF == "" {
				t.Fatal("missing login credentials")
			}
			stored, err := f.store.SessionByTokenHash(context.Background(), auth.TokenHash(session.Value))
			if err != nil || stored.SecureCookies != tc.secure {
				t.Fatalf("stored Secure policy=%v err=%v", stored.SecureCookies, err)
			}
			for _, csrf := range []string{validCSRF, "", "stale-csrf"} {
				// Backend sees HTTP without Origin, Referer or forwarded headers.
				r = httptest.NewRequest("GET", "http://"+r.Host+"/api/auth/session", nil)
				r.AddCookie(session)
				if csrf != "" {
					r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
				}
				w = httptest.NewRecorder()
				r.RemoteAddr = "127.0.0.1:12345"
				f.handler.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("refresh=%d", w.Code)
				}
				found := false
				for _, c := range w.Result().Cookies() {
					if c.Name == csrfCookieName {
						found = true
						if c.Secure != tc.secure {
							t.Fatalf("CSRF replacement Secure=%v, want=%v", c.Secure, tc.secure)
						}
					}
				}
				if (tc.secure || csrf != validCSRF) && !found {
					t.Fatal("missing CSRF replacement")
				}
				var body struct {
					Data struct {
						CSRF string `json:"csrf_token"`
					} `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				mutation := httptest.NewRequest("PUT", "http://"+r.Host+"/api/settings/security", bytes.NewBufferString(`{"mode":"internal"}`))
				mutation.AddCookie(session)
				mutation.AddCookie(&http.Cookie{Name: csrfCookieName, Value: body.Data.CSRF})
				mutation.Header.Set("Content-Type", "application/json")
				mutation.Header.Set(csrfHeaderName, body.Data.CSRF)
				w = httptest.NewRecorder()
				mutation.RemoteAddr = "127.0.0.1:12345"
				f.handler.ServeHTTP(w, mutation)
				if w.Code != 200 {
					t.Fatalf("first CSRF write=%d", w.Code)
				}
			}
		})
	}
}

// TestSessionCookiePolicyOnlyUpgrades keeps a recognized HTTPS refresh sticky for an existing HTTP session.
func TestSessionCookiePolicyOnlyUpgrades(t *testing.T) {
	f := newSSOHTTPFixture(t)
	f.login(t)
	base := f.server.URL
	entry, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	cookies := f.client.Jar.Cookies(entry)
	for _, hint := range []string{"https://vocat.example/settings", ""} {
		r := httptest.NewRequest("GET", "http://vocat.example/api/auth/session", nil)
		if hint != "" {
			r.Header.Set("Referer", hint)
		}
		for _, c := range cookies {
			if c.Name == sessionCookieName {
				r.AddCookie(c)
			}
		}
		w := httptest.NewRecorder()
		r.RemoteAddr = "127.0.0.1:12345"
		f.handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("session=%d", w.Code)
		}
		found := false
		for _, c := range w.Result().Cookies() {
			if c.Name == csrfCookieName {
				found = true
				if !c.Secure {
					t.Fatal("HTTPS policy was downgraded")
				}
			}
		}
		if !found {
			t.Fatal("missing Secure CSRF cookie")
		}
	}
}
