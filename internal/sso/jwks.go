package sso

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/go-jose/go-jose/v4"
)

// callbackKeySet retains go-oidc's trusted-key verification boundary while
// fetching keys with the callback context. go-oidc's built-in RemoteKeySet uses
// context.WithoutCancel for its shared background refresh, which would leave an
// outbound request running after this callback was cancelled.
type callbackKeySet struct {
	url        string
	client     *http.Client
	algorithms []jose.SignatureAlgorithm
	mu         sync.RWMutex
	keys       []jose.JSONWebKey
}

type keySetResultKey struct{}
type keySetResult struct{ unavailable bool }

// VerifySignature checks cached keys and refreshes once from the trusted JWKS URI on failure.
// The refresh inherits callback cancellation and records provider unavailability separately from invalid signatures.
func (set *callbackKeySet) VerifySignature(ctx context.Context, token string) ([]byte, error) {
	object, err := jose.ParseSigned(token, set.algorithms)
	if err != nil || len(object.Signatures) != 1 {
		return nil, ErrInvalidIdentity
	}
	set.mu.RLock()
	keys := set.keys
	set.mu.RUnlock()
	if payload, err := verifyWithKeys(object, keys); err == nil {
		return payload, nil
	}
	// An unknown key or failed signature triggers one refresh at the trusted
	// discovery jwks_uri. No URL supplied in a token header is ever consulted.
	keys, err = set.fetch(ctx)
	if err != nil {
		if result, ok := ctx.Value(keySetResultKey{}).(*keySetResult); ok {
			result.unavailable = true
		}
		return nil, err
	}
	set.mu.Lock()
	set.keys = keys
	set.mu.Unlock()
	return verifyWithKeys(object, keys)
}

// verifyWithKeys tries keys matching the token key ID and algorithm, returning only a verified payload.
func verifyWithKeys(object *jose.JSONWebSignature, keys []jose.JSONWebKey) ([]byte, error) {
	header := object.Signatures[0].Header
	for _, key := range keys {
		if (header.KeyID != "" && key.KeyID != header.KeyID) || (key.Algorithm != "" && key.Algorithm != header.Algorithm) {
			continue
		}
		if payload, err := object.Verify(key); err == nil {
			return payload, nil
		}
	}
	return nil, ErrInvalidIdentity
}

// fetch reads a bounded JWKS document and retains valid public signing keys with permitted algorithms.
func (set *callbackKeySet) fetch(ctx context.Context) ([]jose.JSONWebKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, set.url, nil)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	response, err := set.client.Do(request)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrProviderUnavailable
	}
	body, err := io.ReadAll(response.Body)
	var document struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err != nil || json.Unmarshal(body, &document) != nil || len(document.Keys) == 0 || len(document.Keys) > 128 {
		return nil, ErrProviderUnavailable
	}
	keys := make([]jose.JSONWebKey, 0, len(document.Keys))
	for _, raw := range document.Keys {
		var metadata struct {
			Algorithm     string          `json:"alg"`
			Use           string          `json:"use"`
			KeyOperations json.RawMessage `json:"key_ops"`
		}
		if json.Unmarshal(raw, &metadata) != nil || (metadata.Use != "" && metadata.Use != "sig") ||
			!keyOperationsPermitVerify(metadata.KeyOperations, metadata.Use) {
			continue
		}
		if metadata.Algorithm != "" {
			allowed := false
			for _, algorithm := range set.algorithms {
				if string(algorithm) == metadata.Algorithm {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		var key jose.JSONWebKey
		if json.Unmarshal(raw, &key) == nil && key.Valid() && key.IsPublic() {
			for _, algorithm := range set.algorithms {
				if keySupportsAlgorithm(key, algorithm) {
					keys = append(keys, key)
					break
				}
			}
		}
	}
	if len(keys) == 0 {
		return nil, ErrProviderUnavailable
	}
	return keys, nil
}

// keyOperationsPermitVerify honors an explicit RFC 7517 key_ops restriction.
// An omitted member is unrestricted; null, malformed, and non-verifying lists
// do not authorize signature verification. go-jose does not retain this member.
func keyOperationsPermitVerify(raw json.RawMessage, use string) bool {
	if len(raw) == 0 {
		return true
	}
	var operations []string
	if json.Unmarshal(raw, &operations) != nil {
		return false
	}
	verify := false
	seen := make(map[string]bool, len(operations))
	for _, operation := range operations {
		if operation == "" || seen[operation] {
			return false
		}
		seen[operation] = true
		if operation == "verify" {
			verify = true
		}
		if use == "sig" {
			switch operation {
			case "encrypt", "decrypt", "wrapKey", "unwrapKey", "deriveKey", "deriveBits":
				return false
			}
		}
	}
	return verify
}

// keySupportsAlgorithm requires a compatible public key family, EC curve, and any explicit algorithm tag.
func keySupportsAlgorithm(key jose.JSONWebKey, algorithm jose.SignatureAlgorithm) bool {
	if key.Algorithm != "" && key.Algorithm != string(algorithm) {
		return false
	}
	switch public := key.Key.(type) {
	case *rsa.PublicKey:
		return algorithm == jose.RS256 || algorithm == jose.RS384 || algorithm == jose.RS512 ||
			algorithm == jose.PS256 || algorithm == jose.PS384 || algorithm == jose.PS512
	case *ecdsa.PublicKey:
		return algorithm == jose.ES256 && public.Curve == elliptic.P256() ||
			algorithm == jose.ES384 && public.Curve == elliptic.P384() ||
			algorithm == jose.ES512 && public.Curve == elliptic.P521()
	case ed25519.PublicKey:
		return algorithm == jose.EdDSA
	default:
		return false
	}
}
