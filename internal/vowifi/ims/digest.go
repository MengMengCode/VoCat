package ims

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"vocat/internal/vowifi"
)

type digestChallenge struct {
	Realm     string
	Nonce     string
	Opaque    string
	Algorithm string
	QOP       string
	Stale     bool
	Proxy     bool
}

type digestCredentials struct {
	Username    string
	AKAResponse []byte
	AUTS        string
	URI         string
	Method      string
	CNonce      string
	NC          uint32
}

func parseDigestChallenge(value string, proxy bool) (digestChallenge, error) {
	scheme, parameters, found := strings.Cut(strings.TrimSpace(value), " ")
	if !found || !strings.EqualFold(scheme, "Digest") {
		return digestChallenge{}, errors.New("ims: unsupported SIP authentication scheme")
	}
	directives, err := parseAuthDirectives(parameters)
	if err != nil {
		return digestChallenge{}, err
	}
	challenge := digestChallenge{
		Realm:     directives["realm"],
		Nonce:     directives["nonce"],
		Opaque:    directives["opaque"],
		Algorithm: directives["algorithm"],
		Proxy:     proxy,
		Stale:     strings.EqualFold(directives["stale"], "true"),
	}
	if challenge.Realm == "" || challenge.Nonce == "" {
		return digestChallenge{}, errors.New("ims: incomplete SIP digest challenge")
	}
	if challenge.Algorithm == "" {
		// RFC 3310 inherits the HTTP Digest default: an omitted algorithm is
		// plain MD5, not AKA. This provider has no subscriber password and
		// must not misinterpret an ordinary nonce as RAND || AUTN.
		return digestChallenge{}, errors.New("ims: digest challenge omitted the AKA algorithm")
	}
	if !strings.EqualFold(challenge.Algorithm, "AKAv1-MD5") {
		return digestChallenge{}, fmt.Errorf("ims: unsupported digest algorithm %q", challenge.Algorithm)
	}
	if qop := directives["qop"]; qop != "" {
		for _, candidate := range strings.Split(qop, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), "auth") {
				challenge.QOP = "auth"
				break
			}
		}
		if challenge.QOP == "" {
			return digestChallenge{}, errors.New("ims: digest challenge does not offer qop=auth")
		}
	}
	return challenge, nil
}

func parseAuthDirectives(value string) (map[string]string, error) {
	directives := make(map[string]string)
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ' ' || value[index] == '\t' || value[index] == ',') {
			index++
		}
		if index == len(value) {
			break
		}
		keyStart := index
		for index < len(value) && value[index] != '=' && value[index] != ',' {
			index++
		}
		if index == len(value) || value[index] != '=' {
			return nil, errors.New("ims: malformed digest directive")
		}
		key := strings.ToLower(strings.TrimSpace(value[keyStart:index]))
		index++
		for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
			index++
		}
		var directiveValue strings.Builder
		if index < len(value) && value[index] == '"' {
			index++
			closed := false
			for index < len(value) {
				switch value[index] {
				case '\\':
					index++
					if index == len(value) {
						return nil, errors.New("ims: malformed quoted digest directive")
					}
					directiveValue.WriteByte(value[index])
					index++
				case '"':
					index++
					closed = true
				default:
					directiveValue.WriteByte(value[index])
					index++
				}
				if closed {
					break
				}
			}
			if !closed {
				return nil, errors.New("ims: unterminated quoted digest directive")
			}
		} else {
			start := index
			for index < len(value) && value[index] != ',' {
				index++
			}
			directiveValue.WriteString(strings.TrimSpace(value[start:index]))
		}
		if key == "" {
			return nil, errors.New("ims: empty digest directive name")
		}
		directives[key] = directiveValue.String()
		for index < len(value) && value[index] != ',' {
			if value[index] != ' ' && value[index] != '\t' {
				return nil, errors.New("ims: malformed digest directive separator")
			}
			index++
		}
	}
	return directives, nil
}

type akaMaterial struct {
	response []byte
	auts     []byte
	ck       []byte
	ik       []byte
}

func clearAKAMaterial(material *akaMaterial) {
	if material == nil {
		return
	}
	zeroBytes(material.response)
	zeroBytes(material.auts)
	zeroBytes(material.ck)
	zeroBytes(material.ik)
	*material = akaMaterial{}
}

func authenticateAKA(
	ctx context.Context,
	provider vowifi.AKAProvider,
	identity vowifi.SIMIdentity,
	challenge digestChallenge,
) (akaMaterial, error) {
	nonce, err := decodeAKANonce(challenge.Nonce)
	if err != nil {
		return akaMaterial{}, err
	}
	// 3GPP HTTP Digest AKA encodes RAND || AUTN as the first 32 nonce octets.
	// Following server data remains in the digest nonce and never enters USIM.
	var akaChallenge vowifi.AKAChallenge
	copy(akaChallenge.RAND[:], nonce[:16])
	copy(akaChallenge.AUTN[:], nonce[16:32])
	result, err := provider.Authenticate(ctx, identity, akaChallenge)
	if err != nil {
		return akaMaterial{}, fmt.Errorf("ims: USIM AKA authentication failed: %w", err)
	}
	if result.SynchronizationFailure || len(result.AUTS) > 0 {
		if !result.SynchronizationFailure || len(result.AUTS) != 14 {
			return akaMaterial{}, errors.New("ims: USIM returned malformed AKA synchronization evidence")
		}
		return akaMaterial{auts: append([]byte(nil), result.AUTS...)}, nil
	}
	res, err := extractRES(result)
	if err != nil {
		return akaMaterial{}, err
	}
	return akaMaterial{
		response: res,
		ck:       append([]byte(nil), result.CK...),
		ik:       append([]byte(nil), result.IK...),
	}, nil
}

func decodeAKANonce(value string) ([]byte, error) {
	var decoded []byte
	var err error
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err = encoding.DecodeString(strings.TrimSpace(value))
		if err == nil {
			break
		}
	}
	if err != nil || len(decoded) < 32 {
		return nil, errors.New("ims: invalid AKA nonce")
	}
	return decoded, nil
}

func extractRES(result vowifi.AKAResult) ([]byte, error) {
	if len(result.RES) == 0 {
		return nil, errors.New("ims: USIM returned an empty AKA result")
	}
	if len(result.RES) < 4 || len(result.RES) > 16 {
		return nil, errors.New("ims: USIM returned an invalid RES length")
	}
	return append([]byte(nil), result.RES...), nil
}

func newDigestCredentials(
	username string,
	akaResponse []byte,
	uri string,
	method string,
	nc uint32,
) (digestCredentials, error) {
	cnonceBytes := make([]byte, 16)
	if _, err := rand.Read(cnonceBytes); err != nil {
		return digestCredentials{}, fmt.Errorf("ims: create digest cnonce: %w", err)
	}
	return digestCredentials{
		Username:    username,
		AKAResponse: akaResponse,
		URI:         uri,
		Method:      method,
		CNonce:      hex.EncodeToString(cnonceBytes),
		NC:          nc,
	}, nil
}

func buildDigestAuthorization(challenge digestChallenge, credentials digestCredentials) string {
	nc := fmt.Sprintf("%08x", credentials.NC)
	response := digestResponse(
		credentials.Username,
		challenge.Realm,
		credentials.AKAResponse,
		credentials.Method,
		credentials.URI,
		challenge.Nonce,
		nc,
		credentials.CNonce,
		challenge.QOP,
	)
	parts := []string{
		`username="` + quoteDigest(credentials.Username) + `"`,
		`realm="` + quoteDigest(challenge.Realm) + `"`,
		`nonce="` + quoteDigest(challenge.Nonce) + `"`,
		`uri="` + quoteDigest(credentials.URI) + `"`,
		`response="` + response + `"`,
		"algorithm=AKAv1-MD5",
	}
	if challenge.Opaque != "" {
		parts = append(parts, `opaque="`+quoteDigest(challenge.Opaque)+`"`)
	}
	if challenge.QOP != "" {
		parts = append(parts,
			"qop="+challenge.QOP,
			"nc="+nc,
			`cnonce="`+quoteDigest(credentials.CNonce)+`"`,
		)
	}
	if credentials.AUTS != "" {
		parts = append(parts, `auts="`+quoteDigest(credentials.AUTS)+`"`)
	}
	return "Digest " + strings.Join(parts, ", ")
}

func digestResponse(
	username string,
	realm string,
	akaResponse []byte,
	method string,
	uri string,
	nonce string,
	nc string,
	cnonce string,
	qop string,
) string {
	ha1Hash := md5.New()
	_, _ = ha1Hash.Write([]byte(username + ":" + realm + ":"))
	// AKAv1-MD5 is mandated by the IMS server challenge (3GPP TS 33.203).
	// akaResponse is the short-lived USIM RES value, not a stored password.
	_, _ = ha1Hash.Write(akaResponse)
	ha1 := hex.EncodeToString(ha1Hash.Sum(nil))
	ha2 := md5Hex(method + ":" + uri)
	if qop == "" {
		return md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func quoteDigest(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
