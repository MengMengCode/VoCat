package ims

import "strings"

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
