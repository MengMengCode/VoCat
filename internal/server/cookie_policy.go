package server

import (
	"net/http"
	"net/url"
	"strings"

	"vocat/internal/sso"
)

// forwardedHTTPS only uses deployment-authorized proxy metadata. Host is never
// taken from X-Forwarded-Host: proxies must preserve the browser's Host header.
func (s *Server) forwardedHTTPS(r *http.Request) bool {
	return s.currentAccessConfig().trustProxy && len(r.Header.Values("X-Forwarded-Proto")) == 1 && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// ssoRequestOrigin keeps the configured public entry and accepts explicit
// loopback/private IP alternatives. Arbitrary Host headers cannot create a new
// public callback destination. Each entry must also be registered with the IdP.
func (s *Server) ssoRequestOrigin(r *http.Request, configured string) (string, error) {
	scheme := "http"
	if r.TLS != nil || s.forwardedHTTPS(r) {
		scheme = "https"
	}
	candidate, err := sso.NormalizePublicOrigin(scheme + "://" + r.Host)
	saved, savedErr := url.Parse(configured)
	if savedErr == nil && saved.Host != "" {
		// Nginx may terminate TLS without the application trusting all proxy headers.
		canonical, canonicalErr := sso.NormalizePublicOrigin(saved.Scheme + "://" + r.Host)
		if canonicalErr == nil && canonical == configured {
			if (r.TLS != nil || s.forwardedHTTPS(r)) && saved.Scheme == "http" {
				return candidate, err
			}
			return configured, nil
		}
	}
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(candidate)
	if !sso.IsLocalHost(parsed.Hostname()) {
		return "", sso.ErrInvalidConfiguration
	}
	return candidate, nil
}

// secureCookiesForRequest uses only the process override and request transport.
// Password sessions must not depend on saved SSO configuration or its availability.
func (s *Server) secureCookiesForRequest(r *http.Request) bool {
	return s.secureCookies || r.TLS != nil || s.forwardedHTTPS(r)
}

// ssoSessionSecureCookies preserves the SSO entry policy on CSRF refreshes.
// Only an authenticated SSO session may consult its provider configuration.
func (s *Server) ssoSessionSecureCookies(r *http.Request) (bool, error) {
	if s.secureCookiesForRequest(r) {
		return true, nil
	}
	snapshot, err := s.sso.Snapshot(r.Context())
	if err != nil {
		return false, err
	}
	origin, err := s.ssoRequestOrigin(r, snapshot.Config.PublicOrigin)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(origin, "https://"), nil
}
