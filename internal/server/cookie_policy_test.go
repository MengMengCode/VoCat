package server

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestBrowserEntryOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, host, saved, forwarded, want string
		tls, trust, reject                 bool
	}{
		{name: "HTTPS reverse proxy", host: "vo.example", saved: "https://vo.example", forwarded: "http", want: "https://vo.example"},
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
