package sso

import (
	"context"
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
			Algorithm string `json:"alg"`
			Use       string `json:"use"`
		}
		if json.Unmarshal(raw, &metadata) != nil || (metadata.Use != "" && metadata.Use != "sig") {
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
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, ErrProviderUnavailable
	}
	return keys, nil
}
