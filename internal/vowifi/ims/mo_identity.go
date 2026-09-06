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
