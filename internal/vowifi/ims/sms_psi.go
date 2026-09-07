package ims

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

// smsCenterPSIReader is optional: older AKA adapters retain numeric SMSC routing.
// The PSI selects only the SIP target, never the RP-Destination address.
type smsCenterPSIReader interface {
	ReadSMSCenterPSI(context.Context, string) (string, error)
}

func (session *Session) smsTarget(ctx context.Context, smsc string) (string, error) {
	fallback := "tel:" + normalizeE164(smsc)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reader, ok := session.provider.aka.(smsCenterPSIReader)
	if !ok {
		return fallback, nil
	}
	psi, err := reader.ReadSMSCenterPSI(ctx, session.request.DeviceID)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	if err != nil || !validSMSPSI(psi) {
		return fallback, nil
	}
	return psi, nil
}

// validSMSPSI accepts a conservative bare SIP/SIPS/TEL routing URI. Unlike the
// header URI helpers it never extracts a URI from display names or trims input.
// URI headers, passwords and nested escapes are intentionally not supported:
// a SIM service-centre route must not supply MESSAGE headers or credentials.
// Validate decoded bytes too, but preserve the original URI on the wire.
func validSMSPSI(uri string) bool {
	decoded, err := url.PathUnescape(uri)
	if err != nil {
		return false
	}
	for _, value := range []string{uri, decoded} {
		for _, c := range value {
			if c <= 32 || c >= 127 || strings.ContainsRune("<>\"\\?#", c) {
				return false
			}
		}
	}
	if strings.Contains(decoded, "%") {
		return false
	}
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok || rest == "" {
		return false
	}
	switch strings.ToLower(scheme) {
	case "sip", "sips":
		if user, host, found := strings.Cut(rest, "@"); found {
			if !smsPSIComponent(user, "&=+$,;/") {
				return false
			}
			rest = host
		}
		address, params, hasParams := strings.Cut(rest, ";")
		if hasParams && !smsPSIParameters(params) {
			return false
		}
		host := address
		if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
			host = address[1 : len(address)-1]
		} else if strings.Contains(address, ":") {
			var port string
			host, port, err = net.SplitHostPort(address)
			if err != nil || !smsPSIChars(port, "0123456789") {
				return false
			}
			if _, err := decimalPort(port); err != nil {
				return false
			}
		}
		if strings.ContainsAny(address, "[]") {
			return strings.Contains(host, ":") && net.ParseIP(host) != nil
		}
		return smsPSIHost(host)
	case "tel":
		number, params, hasParams := strings.Cut(rest, ";")
		if hasParams && !smsPSIParameters(params) {
			return false
		}
		global := strings.HasPrefix(number, "+")
		number = strings.TrimPrefix(number, "+")
		if !smsPSIChars(number, "0123456789-.()") || !strings.ContainsAny(number, "0123456789") {
			return false
		}
		if !global {
			for _, param := range strings.Split(params, ";") {
				key, value, found := strings.Cut(param, "=")
				if found && strings.EqualFold(key, "phone-context") {
					return smsPSIHost(value) || (strings.HasPrefix(value, "+") && smsPSIChars(value[1:], "0123456789-.()") && strings.ContainsAny(value, "0123456789"))
				}
			}
			return false
		}
		return true
	default:
		return false
	}
}

func smsPSIParameters(params string) bool {
	for _, param := range strings.Split(params, ";") {
		key, value, hasValue := strings.Cut(param, "=")
		if !smsPSIComponent(key, "[]/:&+$") || (hasValue && !smsPSIComponent(value, "[]/:&+$")) {
			return false
		}
	}
	return true
}

func smsPSIComponent(value, extra string) bool {
	decoded, err := url.PathUnescape(value)
	return err == nil && smsPSIChars(decoded, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.!~*'()"+extra)
}

func smsPSIChars(value, allowed string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune(allowed, c) {
			return false
		}
	}
	return true
}

func smsPSIHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	// RFC 3261 hostname toplabel starts with ALPHA; malformed IPv4 must not
	// sneak through as a numeric DNS name after ParseIP rejects it.
	top := labels[len(labels)-1]
	if top == "" || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", rune(top[0])) {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") || !smsPSIChars(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") {
			return false
		}
	}
	return true
}
