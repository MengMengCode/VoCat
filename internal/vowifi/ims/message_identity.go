package ims

import (
	"errors"
	"net/url"
	"strings"
)

// originatingSMSPublicIdentity is only for sessions whose public identity was
// generated for REGISTER. TS 24.229 5.1.1.1A / 5.1.2A.1.1 prohibit reusing that
// temporary identity in originating SMS. Explicit identities use the unchanged
// messagePublicIdentity policy instead.
func originatingSMSPublicIdentity(temporary string, associated []string) (string, string, error) {
	values := splitHeaderValues(associated)
	if len(values) > 0 {
		// The first P-Associated-URI is the network default. Do not skip an
		// unusable/default temporary identity and silently promote a later URI.
		uri := publicIdentityURI(values[0])
		key := publicIdentityKey(uri)
		// Reuse the conservative bare SIP/SIPS/TEL validator; authorization is
		// supplied by P-Associated-URI, never by the URI's numeric appearance.
		if key != "" && validSMSPSI(uri) && temporaryPublicIdentityKey(uri) != temporaryPublicIdentityKey(temporary) {
			return uri, "associated_default", nil
		}
	}
	return "", "", errors.New("ims: originating SMS requires a usable network default public identity distinct from the temporary registration identity")
}

// temporaryPublicIdentityKey is for exclusion only, never authorization.
// URI parameters do not make the known REGISTER-only identity suitable for MO;
// percent-encoded unreserved characters must not disguise that identity either.
// Keep messagePublicIdentity's conservative authorization comparator unchanged.
func temporaryPublicIdentityKey(value string) string {
	uri := publicIdentityURI(value)
	var normalized strings.Builder
	for i := 0; i < len(uri); i++ {
		if uri[i] == '%' && i+2 < len(uri) {
			decoded, err := url.PathUnescape(uri[i : i+3])
			if err == nil && len(decoded) == 1 && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.!~*'()", rune(decoded[0])) {
				normalized.WriteByte(decoded[0])
				i += 2
				continue
			}
		}
		normalized.WriteByte(uri[i])
	}
	uri, _, _ = strings.Cut(normalized.String(), ";")
	return publicIdentityKey(uri)
}

// messagePublicIdentity selects from the current registration's identities,
// without changing the identity used for REGISTER or its authentication.
func messagePublicIdentity(current, preferred string, associated []string) (string, string) {
	preferredKey := publicIdentityKey(publicIdentityURI(preferred))
	currentKey := publicIdentityKey(publicIdentityURI(current))
	var defaultURI, currentURI string
	for _, value := range splitHeaderValues(associated) {
		uri := publicIdentityURI(value)
		key := publicIdentityKey(uri)
		if key == "" {
			continue
		}
		if defaultURI == "" {
			defaultURI = uri
		}
		if preferredKey != "" && key == preferredKey {
			// Emit the registrar's URI, not a value copied from the request.
			return uri, "called_party"
		}
		if key == currentKey {
			currentURI = uri
		}
	}
	if currentURI != "" {
		return currentURI, "associated_current"
	}
	if defaultURI != "" {
		// TS 24.229 5.1.1.2.1: the first P-Associated-URI is the default;
		// an identity under registration absent from the list is barred.
		return defaultURI, "associated_default"
	}
	// Compatibility with registrars that do not supply usable associated URIs.
	return current, "configured_fallback"
}

// publicIdentityURI preserves URI parameters and quoted display-name commas.
// Unlike firstURI, a bare URI's semicolon is not treated as a header parameter.
func publicIdentityURI(value string) string {
	if strings.ContainsAny(value, "\r\n") {
		return ""
	}
	values := splitHeaderValues([]string{value})
	if len(values) != 1 {
		return ""
	}
	value = values[0]
	if start := strings.IndexByte(value, '<'); start >= 0 {
		end := strings.IndexByte(value[start+1:], '>')
		if end < 0 {
			return ""
		}
		value = value[start+1 : start+1+end]
	}
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "<>\" \t") {
		return ""
	}
	return value
}

// publicIdentityKey deliberately matches conservatively. RFC 3261 19.1.4
// permits case folding of the scheme and host, but NOT SIP userinfo. Preserve
// parameters, escaping, and TEL subscribers exactly; unfamiliar equivalent
// spellings fall back to another registered identity rather than authorizing
// a potentially different identity. This is not a general SIP URI comparator.
func publicIdentityKey(uri string) string {
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok || rest == "" {
		return ""
	}
	scheme = strings.ToLower(scheme)
	switch scheme {
	case "tel":
		return scheme + ":" + rest
	case "sip", "sips":
		at := strings.LastIndexByte(rest, '@')
		if at <= 0 || at == len(rest)-1 {
			return ""
		}
		userinfo, host := rest[:at+1], rest[at+1:]
		tail := ""
		if end := strings.IndexAny(host, ";?"); end >= 0 {
			host, tail = host[:end], host[end:]
		}
		if host == "" {
			return ""
		}
		return scheme + ":" + userinfo + strings.ToLower(host) + tail
	default:
		return ""
	}
}
