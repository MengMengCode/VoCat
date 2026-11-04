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
	normalizedSaved, normalizedErr := sso.NormalizePublicOrigin(configured)
	if savedErr == nil && saved.Host != "" && normalizedErr == nil {
		// Nginx may terminate TLS without the application trusting all proxy headers.
		canonical, canonicalErr := sso.NormalizePublicOrigin(saved.Scheme + "://" + r.Host)
		if canonicalErr == nil && canonical == normalizedSaved {
			if (r.TLS != nil || s.forwardedHTTPS(r)) && saved.Scheme == "http" {
				return candidate, err
			}
			return normalizedSaved, nil
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

// secureCookiesForRequest never consults saved SSO settings for password
// sessions. Same-authority browser hints can only upgrade Cookie protection;
// they are not trusted for access control, authentication, or callback routing.
func (s *Server) secureCookiesForRequest(r *http.Request) bool {
	return s.secureCookies || r.TLS != nil || s.forwardedHTTPS(r) || browserHTTPSCookieHint(r)
}

// browserHTTPSCookieHint handles TLS termination without trusting forwarded
// headers globally. Origin takes precedence; Referer supports same-origin GET
// refreshes that omit Origin. Neither hint can turn a secure decision off.
func browserHTTPSCookieHint(r *http.Request) bool {
	values := r.Header.Values("Origin")
	origin := len(values) != 0
	if !origin {
		values = r.Header.Values("Referer")
	}
	if len(values) != 1 {
		return false
	}
	raw := values[0]
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\#") {
		return false
	}
	hint, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(hint.Scheme, "https") || hint.Opaque != "" || hint.User != nil || hint.Host == "" {
		return false
	}
	if origin && (hint.Path != "" || hint.RawQuery != "" || hint.ForceQuery) {
		return false
	}
	expected, err := sso.NormalizePublicOrigin("https://" + r.Host)
	if err != nil {
		return false
	}
	actual, err := sso.NormalizePublicOrigin("https://" + hint.Host)
	return err == nil && actual == expected
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
