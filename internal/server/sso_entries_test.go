package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"vocat/internal/sso"
	"vocat/internal/store"
)

// Exercise the same saved identity over multiple browser entries, including
// callback origin binding, primary cookie refresh and the first CSRF write.
func TestSSOAutomaticBrowserEntries(t *testing.T) {
	f := newSSOHTTPFixture(t)
	csrf := f.login(t)
	base, _ := url.Parse(f.server.URL)
	passwordCookies := f.client.Jar.Cookies(base)
	do := func(origin, method, path string, body any, cookies []*http.Cookie, csrf string, directTLS bool) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&b).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, origin+path, &b)
		r.RemoteAddr = "192.168.111.3:12345"
		// httptest derives TLS from URL; reset it for the reverse-proxy cases.
		r.TLS = nil
		if directTLS {
			r.TLS = &tls.ConnectionState{}
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(csrfHeaderName, csrf)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w
	}
	config := f.config()
	config["public_origin"] = "https://vo.example"
	w := do("https://vo.example", "PUT", "/api/settings/sso", map[string]any{"expected_revision": 0, "config": config, "client_secret": "fixture-client-secret"}, passwordCookies, csrf, false)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w = do("https://vo.example", "POST", "/api/auth/sso/bind", map[string]any{"expected_revision": 1}, passwordCookies, csrf, false)
	if w.Code != 200 {
		t.Fatalf("bind: %d %s", w.Code, w.Body)
	}
	var payload struct {
		Data struct {
			URL string `json:"authorization_url"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	authURL, _ := url.Parse(payload.Data.URL)
	callback := sso.CallbackPath + "?code=fixture-code&state=" + url.QueryEscape(authURL.Query().Get("state"))
	w = do("https://vo.example", "GET", callback, nil, w.Result().Cookies(), "", false)
	if w.Code != 303 || w.Header().Get("Location") != "/settings?sso_result=bound" {
		t.Fatalf("bind callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	config["enabled"] = true
	w = do("https://vo.example", "PUT", "/api/settings/sso", map[string]any{"expected_revision": 2, "config": config}, passwordCookies, csrf, false)
	if w.Code != 200 {
		t.Fatalf("enable without Cookie environment override: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		origin            string
		secure, directTLS bool
	}{
		{"https://vo.example", true, false},
		{"http://192.168.111.27:7575", false, false},
		{"http://127.0.0.1:7575", false, false},
		{"http://[::1]:7575", false, false},
		{"http://[::ffff:c0a8:114]:7575", false, false},
		{"https://192.168.111.27:7575", true, true},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			w := do(tc.origin, "GET", "/api/auth/sso/start", nil, nil, "", tc.directTLS)
			if w.Code != 302 {
				t.Fatalf("start: %d %s", w.Code, w.Body)
			}
			flowCookies := w.Result().Cookies()
			if len(flowCookies) != 1 || flowCookies[0].Secure != tc.secure || flowCookies[0].SameSite != http.SameSiteLaxMode {
				t.Fatal("wrong flow Cookie policy")
			}
			u, _ := url.Parse(w.Header().Get("Location"))
			if u.Query().Get("redirect_uri") != tc.origin+sso.CallbackPath {
				t.Fatalf("callback=%s", u.Query().Get("redirect_uri"))
			}
			callback := sso.CallbackPath + "?code=fixture-code&state=" + url.QueryEscape(u.Query().Get("state"))
			wrong := do("http://10.0.0.99:7575", "GET", callback, nil, flowCookies, "", false)
			if wrong.Header().Get("Location") != "/login?sso_error=invalid_flow" {
				t.Fatal("cross-entry callback accepted")
			}
			w = do(tc.origin, "GET", callback, nil, flowCookies, "", tc.directTLS)
			if w.Code != 303 || w.Header().Get("Location") != "/" {
				t.Fatalf("login: %d %s", w.Code, w.Header().Get("Location"))
			}
			var credentials []*http.Cookie
			var csrf string
			for _, c := range w.Result().Cookies() {
				if c.Name == sessionCookieName || c.Name == csrfCookieName {
					if c.Secure != tc.secure || c.SameSite != http.SameSiteStrictMode {
						t.Fatal("wrong primary Cookie policy")
					}
					credentials = append(credentials, c)
					if c.Name == csrfCookieName {
						csrf = c.Value
					}
				}
			}
			if len(credentials) != 2 {
				t.Fatal("missing login credentials")
			}
			w = do(tc.origin, "GET", "/api/auth/session", nil, credentials, "", tc.directTLS)
			if w.Code != 200 {
				t.Fatalf("session: %d %s", w.Code, w.Body)
			}
			for _, c := range w.Result().Cookies() {
				if c.Name == csrfCookieName && c.Secure != tc.secure {
					t.Fatal("session refresh changed transport policy")
				}
			}
			w = do(tc.origin, "PUT", "/api/settings/security", map[string]any{"mode": "internal"}, credentials, csrf, tc.directTLS)
			if w.Code != 200 {
				t.Fatalf("first CSRF write: %d %s", w.Code, w.Body)
			}
		})
	}
	// Simulate a configuration saved by an older version without re-saving it
	// through the new normalization path; the existing binding remains usable.
	snapshot, err := f.store.SSOSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Config.PublicOrigin = "https://[2001:0DB8:0:0:0:0:0:1]:8443"
	legacy, _, err := f.store.SaveSSOConfiguration(context.Background(), store.SSOSaveRequest{ExpectedRevision: snapshot.Config.Revision, Config: snapshot.Config})
	if err != nil || legacy.Binding == nil {
		t.Fatalf("prepare legacy configuration: %v", err)
	}
	entry := "https://[2001:db8::1]:8443"
	w = do(entry, "GET", "/api/auth/sso/start", nil, nil, "", false)
	if w.Code != http.StatusFound {
		t.Fatalf("legacy IPv6 start: %d %s", w.Code, w.Body)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Query().Get("redirect_uri") != entry+sso.CallbackPath {
		t.Fatalf("legacy IPv6 callback: %v %v", u, err)
	}
	callback = sso.CallbackPath + "?code=fixture-code&state=" + url.QueryEscape(u.Query().Get("state"))
	w = do(entry, "GET", callback, nil, w.Result().Cookies(), "", false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("legacy IPv6 login: %d %s", w.Code, w.Header().Get("Location"))
	}
}
