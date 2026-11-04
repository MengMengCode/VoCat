package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

// TestJWKSRejectsOversizedRSAKeys exercises the dependency's resource limit
// through our JWKS path without generating an oversized key or validating large primes.
func TestJWKSRejectsOversizedRSAKeys(t *testing.T) {
	valid, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public := jose.JSONWebKey{Key: &valid.PublicKey, KeyID: "valid", Use: "sig", Algorithm: string(jose.RS256)}
	// A 4097-bit modulus is just above go-jose's default 4096-bit ceiling.
	// The old dependency accepted this small JWK as a usable public key.
	modulus := new(big.Int).Lsh(big.NewInt(1), 4096)
	modulus.SetBit(modulus, 0, 1)
	oversized := map[string]string{
		"kty": "RSA", "kid": "oversized", "use": "sig", "alg": string(jose.RS256),
		"n": base64.RawURLEncoding.EncodeToString(modulus.Bytes()), "e": "AQAB",
	}
	for _, tc := range []struct {
		name         string
		includeValid bool
	}{
		{name: "oversized key only"},
		{name: "mixed set retains usable key", includeValid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := []any{oversized}
			if tc.includeValid {
				keys = append(keys, public)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewEncoder(w).Encode(map[string]any{"keys": keys}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			set := &callbackKeySet{url: server.URL, client: server.Client(), algorithms: []jose.SignatureAlgorithm{jose.RS256}}
			fetched, err := set.fetch(context.Background())
			if !tc.includeValid {
				if !errors.Is(err, ErrProviderUnavailable) || len(fetched) != 0 {
					t.Fatalf("oversized RSA key accepted: keys=%d error=%v", len(fetched), err)
				}
			} else if err != nil || len(fetched) != 1 || fetched[0].KeyID != public.KeyID {
				t.Fatalf("mixed JWKS did not retain only its usable key: keys=%d error=%v", len(fetched), err)
			}
		})
	}
}
