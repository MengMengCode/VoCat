package ims

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"vocat/internal/vowifi"
)

const zeroIMEIInstanceID = "urn:gsma:imei:00000000-000000-0"

func (session *Session) contactOptions(current vowifi.IMSRegisterOptions) vowifi.IMSRegisterOptions {
	if session.registerContactOptions != nil {
		return *session.registerContactOptions
	}
	return current
}

func (session *Session) stableContactUUID() string {
	if session.instanceUUID != "" {
		return session.instanceUUID
	}
	// Also support direct Session construction by internal callers and tests.
	if strings.HasPrefix(session.instanceID, "urn:uuid:") {
		return strings.TrimPrefix(session.instanceID, "urn:uuid:")
	}
	uuid, _ := stableInstanceUUID(session.request)
	return uuid
}

func (session *Session) registerContactURI(address string, options vowifi.IMSRegisterOptions) string {
	user := session.identity.user
	switch options.ContactUserSource {
	case vowifi.IMSContactUserStableInstance:
		user = session.stableContactUUID()
	case vowifi.IMSContactUserPublicIdentity:
		// Explicitly override the address-only legacy GSMA layout.
	default:
		if options.ContactFormat == vowifi.IMSContactFormatGSMA {
			return "sip:" + address
		}
	}
	return fmt.Sprintf("sip:%s@%s;transport=%s", user, address, session.transport)
}

func (session *Session) registerInstanceID(options vowifi.IMSRegisterOptions) string {
	switch options.InstanceIDFormat {
	case vowifi.IMSInstanceIDFormatUUID:
		return "urn:uuid:" + session.stableContactUUID()
	case vowifi.IMSInstanceIDFormatGSMAIMEI:
		imei := strings.TrimSpace(session.request.Identity.IMEI)
		if digitsBetween(imei, 15, 15) {
			// RFC 7254 sections 3 and 4.2.3: TAC-SNR-spare. The stored
			// check digit is replaced with zero in the transmitted URN.
			return "urn:gsma:imei:" + imei[:8] + "-" + imei[8:14] + "-0"
		}
		if options.InstanceIDFallback == vowifi.IMSInstanceIDFallbackZeroIMEI {
			return zeroIMEIInstanceID
		}
		return "urn:uuid:" + session.stableContactUUID()
	default:
		// Preserve the existing standard/ATT/GSMA representations unless a
		// profile explicitly opts into the new REGISTER policy. In particular,
		// do not change the instance used by normal call dialogs (RFC 7255).
		return session.instanceID
	}
}

// selectRegisteredContact prefers the URI actually advertised by this session.
// Registrars return all bindings for an identity, including other devices.
func (session *Session) selectRegisteredContact(contacts []string) string {
	options := session.contactOptions(session.imsRegisterOptions())
	wantedURI := session.registerContactURI(session.contactAddress(), options)
	wantedInstance := session.registerInstanceID(options)
	uriMatch, instanceMatch := "", ""
	ambiguousInstance := false
	for _, contact := range contacts {
		uri, parameters := registrationContactParts(contact)
		instance, _ := registrationContactParameter(parameters, "+sip.instance")
		instanceMatches := wantedInstance != "" && strings.EqualFold(instance, "<"+wantedInstance+">")
		uriMatches := registrationURIEqual(uri, wantedURI)
		if !uriMatches && options.ContactFormat == vowifi.IMSContactFormatGSMA && options.ContactUserSource == "" {
			// Legacy GSMA Contact omits transport. Retain compatibility with
			// registrars that add the selected transport to its address-only URI.
			uriMatches = registrationURIEqual(uri, wantedURI+";transport="+session.transport)
		}
		if uriMatches {
			uriMatch = contact
			if instanceMatches {
				return contact
			}
		}
		// A zero IMEI is a carrier compatibility presentation, not a unique
		// device identity. It must never identify a binding on its own.
		if instanceMatches && wantedInstance != zeroIMEIInstanceID {
			if instanceMatch != "" {
				ambiguousInstance = true
			}
			instanceMatch = contact
		}
	}
	if uriMatch != "" {
		return uriMatch
	}
	// Retain compatibility with registrars that rewrite the Contact address,
	// provided exactly one binding has our non-shared instance identifier.
	if !ambiguousInstance {
		return instanceMatch
	}
	return ""
}

// registrationContactParts ignores angle brackets in quoted display names and
// keeps URI parameters separate from Contact header parameters.
func registrationContactParts(contact string) (uri, parameters string) {
	quoted, escaped := false, false
	for index := 0; index < len(contact); index++ {
		switch character := contact[index]; {
		case escaped:
			escaped = false
		case quoted && character == '\\':
			escaped = true
		case character == '"':
			quoted = !quoted
		case !quoted && character == '<':
			if end := strings.IndexByte(contact[index+1:], '>'); end >= 0 {
				end += index + 1
				return strings.TrimSpace(contact[index+1 : end]), contact[end+1:]
			}
			return "", ""
		}
	}
	// RFC 3261 section 20.10 treats parameters of an unbracketed Contact as
	// header parameters; a URI with its own parameters uses angle brackets.
	uri, parameters, _ = strings.Cut(strings.TrimSpace(contact), ";")
	return uri, parameters
}

func registrationContactParameter(parameters, name string) (string, bool) {
	quoted, escaped, start := false, false, 0
	for index := 0; index <= len(parameters); index++ {
		if index == len(parameters) || (parameters[index] == ';' && !quoted) {
			part := strings.TrimSpace(parameters[start:index])
			key, value, _ := strings.Cut(part, "=")
			if strings.EqualFold(strings.TrimSpace(key), name) {
				return strings.Trim(strings.TrimSpace(value), `"`), true
			}
			start = index + 1
			continue
		}
		switch {
		case escaped:
			escaped = false
		case quoted && parameters[index] == '\\':
			escaped = true
		case parameters[index] == '"':
			quoted = !quoted
		}
	}
	return "", false
}

// registrationURIEqual compares the SIP URI components generated for REGISTER,
// preserving case-sensitive userinfo and ignoring URI parameter order (RFC
// 3261 section 19.1.4). This is deliberately not a general SIP URI parser.
func registrationURIEqual(left, right string) bool {
	leftScheme, leftRest, leftOK := strings.Cut(left, ":")
	rightScheme, rightRest, rightOK := strings.Cut(right, ":")
	if !leftOK || !rightOK || !strings.EqualFold(leftScheme, rightScheme) ||
		(!strings.EqualFold(leftScheme, "sip") && !strings.EqualFold(leftScheme, "sips")) {
		return false
	}
	leftRest, leftHeaders, _ := strings.Cut(leftRest, "?")
	rightRest, rightHeaders, _ := strings.Cut(rightRest, "?")
	if leftHeaders != rightHeaders {
		return false
	}
	// Split userinfo first: SIP userinfo itself may contain a semicolon.
	leftUser, rightUser := "", ""
	if at := strings.LastIndexByte(leftRest, '@'); at >= 0 {
		leftUser, leftRest = leftRest[:at+1], leftRest[at+1:]
	}
	if at := strings.LastIndexByte(rightRest, '@'); at >= 0 {
		rightUser, rightRest = rightRest[:at+1], rightRest[at+1:]
	}
	leftUser, leftErr := url.PathUnescape(leftUser)
	rightUser, rightErr := url.PathUnescape(rightUser)
	if leftErr != nil || rightErr != nil || leftUser != rightUser {
		return false
	}
	leftHost, leftParams, _ := strings.Cut(leftRest, ";")
	rightHost, rightParams, _ := strings.Cut(rightRest, ";")
	if leftHost == "" || !registrationHostEqual(leftHost, rightHost) {
		return false
	}
	leftValues, rightValues := registrationURIParameters(leftParams), registrationURIParameters(rightParams)
	// These parameters must appear in both URIs if present in either.
	for _, name := range []string{"transport", "user", "ttl", "method", "maddr"} {
		if leftValues[name] != rightValues[name] {
			return false
		}
	}
	for name, value := range leftValues {
		if other, present := rightValues[name]; present && value != other {
			return false
		}
	}
	return true
}

func registrationHostEqual(left, right string) bool {
	if strings.EqualFold(left, right) {
		return true
	}
	leftHost, leftPort, leftErr := net.SplitHostPort(left)
	rightHost, rightPort, rightErr := net.SplitHostPort(right)
	if leftErr != nil || rightErr != nil || leftPort != rightPort {
		return false
	}
	// RFC 5954 section 4.2 requires comparing numeric IP hosts by address,
	// so compressed and expanded IPv6 renderings identify the same binding.
	leftIP, rightIP := net.ParseIP(leftHost), net.ParseIP(rightHost)
	return leftIP != nil && rightIP != nil && leftIP.Equal(rightIP)
}

func registrationURIParameters(parameters string) map[string]string {
	values := make(map[string]string)
	for _, parameter := range strings.Split(parameters, ";") {
		name, value, _ := strings.Cut(parameter, "=")
		if name != "" {
			values[strings.ToLower(name)] = strings.ToLower(value)
		}
	}
	return values
}
