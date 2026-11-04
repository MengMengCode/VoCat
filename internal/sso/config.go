package sso

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"vocat/internal/store"
)

const CallbackPath = "/api/auth/sso/callback"

// NormalizeConfig validates a draft without DNS, discovery, or any other network
// access. An incomplete disabled draft may be saved while an IdP is unavailable.
func NormalizeConfig(c Config) (Config, error) {
	c.Kind = strings.ToLower(strings.TrimSpace(c.Kind))
	if c.Kind == "" {
		c.Kind = "pocketid"
	}
	if c.Kind != "pocketid" && c.Kind != "github" && c.Kind != "generic" {
		return c, invalidConfig("unsupported provider kind")
	}
	c.DisplayName = strings.TrimSpace(c.DisplayName)
	if c.DisplayName == "" {
		switch c.Kind {
		case "pocketid":
			c.DisplayName = "Pocket ID"
		case "github":
			c.DisplayName = "GitHub"
		default:
			c.DisplayName = "SSO"
		}
	}
	if len(c.DisplayName) > 128 || hasControl(c.DisplayName) {
		return c, invalidConfig("display name must contain at most 128 bytes without control characters")
	}
	c.ClientID = strings.TrimSpace(c.ClientID)
	if len(c.ClientID) > 2048 || hasControl(c.ClientID) {
		return c, invalidConfig("invalid client ID")
	}
	if c.PublicOrigin != "" {
		var err error
		c.PublicOrigin, err = NormalizePublicOrigin(c.PublicOrigin)
		if err != nil {
			return c, err
		}
	}
	if c.TokenAuthMethod == "" {
		switch c.Kind {
		case "pocketid":
			c.TokenAuthMethod = "auto"
		case "generic":
			c.TokenAuthMethod = "client_secret_post"
		default:
			c.TokenAuthMethod = "client_secret_basic"
		}
	}
	if c.TokenAuthMethod != "client_secret_basic" && c.TokenAuthMethod != "client_secret_post" && !(c.Kind == "pocketid" && c.TokenAuthMethod == "auto") {
		return c, invalidConfig("token authentication must be client_secret_basic or client_secret_post; Pocket ID also supports auto")
	}
	if c.PKCEMode == "" {
		c.PKCEMode = "s256"
	}
	if c.PKCEMode != "s256" && !(c.Kind == "generic" && c.PKCEMode == "disabled") {
		return c, invalidConfig("this provider requires PKCE S256")
	}
	if len(c.Scopes) == 0 {
		switch c.Kind {
		case "pocketid":
			c.Scopes = []string{"openid", "profile", "email"}
		case "github":
			c.Scopes = []string{"read:user"}
		default:
			c.Scopes = []string{}
		}
	}
	scopes := make([]string, 0, len(c.Scopes))
	seenScopes := map[string]bool{}
	for _, scope := range c.Scopes {
		if scope == "" || len(scope) > 256 || strings.ContainsFunc(scope, unicode.IsSpace) || hasControl(scope) {
			return c, invalidConfig("scopes must be nonempty strings without whitespace")
		}
		if c.Kind == "pocketid" && scope != "openid" && scope != "profile" && scope != "email" {
			return c, invalidConfig("Pocket ID scopes are limited to openid, profile, and email")
		}
		if !seenScopes[scope] {
			seenScopes[scope] = true
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) > 32 {
		return c, invalidConfig("too many scopes")
	}
	if c.Kind == "pocketid" && !seenScopes["openid"] {
		return c, invalidConfig("Pocket ID requires the openid scope")
	}
	c.Scopes = scopes
	switch c.Kind {
	case "pocketid":
		c.AuthURL, c.TokenURL, c.UserInfoURL, c.UserIDField = "", "", "", ""
		if c.IssuerURL != "" {
			u, err := parseURL(c.IssuerURL)
			if err != nil || u.Scheme != "https" || u.RawQuery != "" || u.ForceQuery {
				return c, invalidConfig("issuer must be an exact HTTPS URL without query or fragment")
			}
			// Preserve issuer spelling, path and trailing slash for exact iss checks.
		}
	case "github":
		if c.AuthURL != "" || c.TokenURL != "" || c.UserInfoURL != "" {
			return c, invalidConfig("GitHub endpoints cannot be overridden")
		}
		c.IssuerURL, c.UserIDField = "", ""
	case "generic":
		c.IssuerURL = ""
		if c.UserIDField == "" {
			c.UserIDField = "id"
		}
		if len(c.UserIDField) > 256 || hasControl(c.UserIDField) {
			return c, invalidConfig("user ID field must be a top-level JSON field name")
		}
		for index, raw := range []string{c.AuthURL, c.TokenURL, c.UserInfoURL} {
			if raw == "" {
				continue
			}
			u, err := parseURL(raw)
			if err != nil || (index == 0 && u.Scheme != "https") {
				return c, invalidConfig("authorization requires HTTPS; HTTP is limited to explicitly configured private backends")
			}
			// Credential-bearing endpoints may not smuggle secrets in a query.
			if index > 0 && (u.RawQuery != "" || u.ForceQuery) {
				return c, invalidConfig("token and user information endpoints cannot contain a query")
			}
		}
	}
	return c, nil
}

// ValidateComplete rejects drafts missing the credentials or endpoints needed for a login flow.
func ValidateComplete(c Config, secret string) error {
	if c.PublicOrigin == "" || c.ClientID == "" || secret == "" {
		return store.ErrSSONotConfigured
	}
	switch c.Kind {
	case "pocketid":
		if c.IssuerURL == "" {
			return store.ErrSSONotConfigured
		}
	case "generic":
		if c.AuthURL == "" || c.TokenURL == "" || c.UserInfoURL == "" || c.UserIDField == "" {
			return store.ErrSSONotConfigured
		}
	case "github":
	default:
		return invalidConfig("unsupported provider kind")
	}
	return nil
}

// CallbackURL derives the fixed callback endpoint from the configured public origin.
func CallbackURL(c Config) string {
	if c.PublicOrigin == "" {
		return ""
	}
	return strings.TrimSuffix(c.PublicOrigin, "/") + CallbackPath
}

// invalidConfig adds a safe diagnostic to the configuration error sentinel.
func invalidConfig(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfiguration, message)
}

// parseURL accepts absolute HTTP URLs without credentials, fragments, or ambiguous ports.
func parseURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 4096 || hasControl(raw) || strings.ContainsAny(raw, "\\#") || strings.TrimSpace(raw) != raw {
		return nil, invalidConfig("invalid URL")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || (u.Scheme != "https" && u.Scheme != "http") || strings.Contains(u.Hostname(), "%") {
		return nil, invalidConfig("invalid HTTP URL")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, invalidConfig("invalid URL port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, invalidConfig("invalid URL port")
	}
	return u, nil
}

// origin canonicalizes a URL origin, removing default ports and preserving IPv6 brackets.
func origin(u *url.URL) string {
	port := u.Port()
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host += ":" + port
	}
	return u.Scheme + "://" + host
}

// isLoopbackHost recognizes localhost and literal loopback addresses for local development.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// hasControl reports Unicode control characters that are unsafe in configuration or claims.
func hasControl(value string) bool {
	return strings.ContainsFunc(value, unicode.IsControl)
}

// NormalizePublicOrigin permits HTTPS or explicit local/private HTTP browser entries.
func NormalizePublicOrigin(raw string) (string, error) {
	u, err := parseURL(raw)
	if err != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery {
		return "", invalidConfig("public origin must contain only scheme, host, and optional port")
	}
	if u.Scheme != "https" && !IsLocalHost(u.Hostname()) {
		return "", invalidConfig("HTTP public origins require localhost or a literal private IP")
	}
	// Browsers serialize IPv4-mapped IPv6 as hexadecimal, unlike netip.String.
	// Limit this normalization to the public entry, not identity-provider URLs.
	if ip, parseErr := netip.ParseAddr(u.Hostname()); parseErr == nil && ip.Is6() {
		port := u.Port()
		host := ip.String()
		if ip.Is4In6() {
			b := ip.As16()
			host = fmt.Sprintf("::ffff:%x:%x", uint16(b[12])<<8|uint16(b[13]), uint16(b[14])<<8|uint16(b[15]))
		}
		u.Host = "[" + host + "]"
		if port != "" {
			u.Host += ":" + port
		}
	}
	return origin(u), nil
}

// IsLocalHost never resolves DNS: only localhost, literal loopback and private IPs qualify.
func IsLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.Unmap().IsLoopback() || ip.Unmap().IsPrivate())
}
