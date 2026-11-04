package server

import (
	"bytes"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestPasswordCookieAtHTTPSProxy checks CookieJar transport eligibility behind TLS termination.
func TestPasswordCookieAtHTTPSProxy(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		directTLS, trust, httpsOrigin bool
		wantSecure                    bool
	}{
		{name: "direct HTTPS", directTLS: true, httpsOrigin: true, wantSecure: true},
		{name: "HTTPS reverse proxy default settings", httpsOrigin: true, wantSecure: true},
		{name: "HTTPS trusted reverse proxy", httpsOrigin: true, trust: true, wantSecure: true},
		{name: "plain HTTP password client", wantSecure: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCookieCompatibilityFixture(t, "https://vocat.example")
			f.handler.access.trustProxy = tc.trust
			origin := "http://vocat.example"
			if tc.httpsOrigin {
				origin = "https://vocat.example"
			}
			request := httptest.NewRequest("POST", origin+"/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"review-local-password"}`))
			request.RemoteAddr = "127.0.0.1:12345"
			if !tc.directTLS {
				request.TLS = nil
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", origin)
			if tc.httpsOrigin {
				request.Header.Set("X-Forwarded-Proto", "https")
			}
			recorder := httptest.NewRecorder()
			f.handler.ServeHTTP(recorder, request)
			response := recorder.Result()
			if response.StatusCode != 200 {
				t.Fatalf("login=%d", response.StatusCode)
			}
			var session *http.Cookie
			for _, cookie := range response.Cookies() {
				if cookie.Name == sessionCookieName {
					session = cookie
				}
			}
			if session == nil {
				t.Fatal("missing Session cookie")
			}
			jar, _ := cookiejar.New(nil)
			source, _ := url.Parse(origin)
			jar.SetCookies(source, response.Cookies())
			httpEntry, _ := url.Parse("http://vocat.example/")
			eligible := false
			for _, cookie := range jar.Cookies(httpEntry) {
				if cookie.Name == sessionCookieName {
					eligible = true
				}
			}
			t.Logf("browser_origin=%s, direct_TLS=%v, trusted_proxy=%v, Secure=%v, session_eligible_for_HTTP=%v", origin, tc.directTLS, tc.trust, session.Secure, eligible)
			if session.Secure != tc.wantSecure {
				t.Errorf("HTTPS browser received an HTTP-eligible Session cookie")
			}
			if eligible == tc.wantSecure {
				t.Errorf("HTTP Cookie eligibility=%v, want %v", eligible, !tc.wantSecure)
			}

		})
	}
}
