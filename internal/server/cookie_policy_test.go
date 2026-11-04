package server

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// TestBrowserEntryOrigin checks entry selection without trusting arbitrary proxy headers.
func TestBrowserEntryOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, host, saved, forwarded, want string
		tls, trust, reject                 bool
	}{
		{name: "HTTPS reverse proxy", host: "vo.example", saved: "https://vo.example", forwarded: "http", want: "https://vo.example"},
		{name: "legacy expanded public IPv6 behind proxy", host: "[2001:db8::1]:8443", saved: "https://[2001:0DB8:0:0:0:0:0:1]:8443", want: "https://[2001:db8::1]:8443"},
		{name: "legacy mapped IPv6 behind proxy", host: "[::ffff:c0a8:114]:7575", saved: "https://[::ffff:192.168.1.20]:7575", want: "https://[::ffff:c0a8:114]:7575"},
		{name: "different public IPv6 host", host: "[2001:db8::2]:8443", saved: "https://[2001:0DB8:0:0:0:0:0:1]:8443", reject: true},
		{name: "different public IPv6 port", host: "[2001:db8::1]:8444", saved: "https://[2001:0DB8:0:0:0:0:0:1]:8443", reject: true},
		{name: "private HTTP", host: "192.168.111.27:7575", saved: "https://vo.example", want: "http://192.168.111.27:7575"},
		{name: "loopback HTTP", host: "127.0.0.1:7575", saved: "https://vo.example", want: "http://127.0.0.1:7575"},
		{name: "direct TLS", host: "192.168.111.27:7575", saved: "https://vo.example", tls: true, want: "https://192.168.111.27:7575"},
		{name: "ignore forged proxy header", host: "192.168.111.27:7575", saved: "https://vo.example", forwarded: "https", want: "http://192.168.111.27:7575"},
		{name: "trusted proxy", host: "192.168.111.27:7575", saved: "https://vo.example", trust: true, forwarded: "https", want: "https://192.168.111.27:7575"},
		{name: "unknown public Host", host: "evil.example", saved: "https://vo.example", reject: true},
		{name: "metadata address", host: "169.254.169.254", saved: "https://vo.example", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{access: parsedAccessConfig{trustProxy: tc.trust}}
			r := httptest.NewRequest("GET", "http://"+tc.host+"/", nil)
			r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			got, err := s.ssoRequestOrigin(r, tc.saved)
			if (err != nil) != tc.reject || (!tc.reject && got != tc.want) {
				t.Fatalf("origin=%q err=%v want=%q reject=%v", got, err, tc.want, tc.reject)
			}
		})
	}
}

// Password Cookie policy must not infer a protocol from SSO configuration.
func TestPasswordCookieTransportPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, proto             string
		tls, trust, force, want bool
	}{
		{name: "plain HTTP"},
		{name: "direct TLS", tls: true, want: true},
		{name: "trusted HTTPS proxy", trust: true, proto: "https", want: true},
		{name: "untrusted proxy header", proto: "https"},
		{name: "ambiguous proxy header", trust: true, proto: "https,http"},
		{name: "explicit legacy override", force: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{secureCookies: tc.force, access: parsedAccessConfig{trustProxy: tc.trust}}
			request := httptest.NewRequest("POST", "http://vocat.example/api/auth/login", nil)
			request.Header.Set("X-Forwarded-Proto", tc.proto)
			if tc.tls {
				request.TLS = &tls.ConnectionState{}
			}
			if got := server.secureCookiesForRequest(request); got != tc.want {
				t.Fatalf("secure=%v want=%v", got, tc.want)
			}
		})
	}
}

// TestBrowserHTTPSCookieHints accepts only unambiguous same-authority HTTPS browser hints.
func TestBrowserHTTPSCookieHints(t *testing.T) {
	for _, tc := range []struct {
		name, host        string
		origins, referers []string
		want              bool
	}{
		{name: "HTTPS Origin", host: "vocat.example", origins: []string{"https://vocat.example"}, want: true},
		{name: "default HTTPS port", host: "vocat.example:443", origins: []string{"https://vocat.example"}, want: true},
		{name: "HTTPS Referer", host: "vocat.example", referers: []string{"https://vocat.example/settings?tab=security"}, want: true},
		{name: "HTTP Origin", host: "vocat.example", origins: []string{"http://vocat.example"}, referers: []string{"https://vocat.example/settings"}},
		{name: "cross host", host: "vocat.example", origins: []string{"https://evil.example"}},
		{name: "cross port", host: "vocat.example:7575", origins: []string{"https://vocat.example"}},
		{name: "opaque Origin", host: "vocat.example", origins: []string{"null"}, referers: []string{"https://vocat.example/"}},
		{name: "duplicate Origin", host: "vocat.example", origins: []string{"https://vocat.example", "https://vocat.example"}},
		{name: "Origin with path", host: "vocat.example", origins: []string{"https://vocat.example/settings"}},
		{name: "Origin with query", host: "vocat.example", origins: []string{"https://vocat.example?"}},
		{name: "credentials", host: "vocat.example", origins: []string{"https://user@vocat.example"}},
		{name: "fragment", host: "vocat.example", referers: []string{"https://vocat.example/#fragment"}},
		{name: "no browser headers", host: "vocat.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://"+tc.host+"/api/auth/session", nil)
			request.Header["Origin"] = tc.origins
			request.Header["Referer"] = tc.referers
			if got := browserHTTPSCookieHint(request); got != tc.want {
				t.Fatalf("HTTPS hint=%v want=%v", got, tc.want)
			}
		})
	}
}
