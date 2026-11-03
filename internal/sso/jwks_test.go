package sso

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// TestJWKSRequiresCompatibleSigningKey checks key families, EC curves, algorithm tags, and mixed sets at fetch time.
func TestJWKSRequiresCompatibleSigningKey(t *testing.T) {
	f := newOIDCFixture(t)
	rsaKey := jose.JSONWebKey{Key: &f.key.PublicKey}
	ecKeys := make(map[jose.SignatureAlgorithm]jose.JSONWebKey)
	for algorithm, curve := range map[jose.SignatureAlgorithm]elliptic.Curve{
		jose.ES256: elliptic.P256(), jose.ES384: elliptic.P384(), jose.ES512: elliptic.P521(),
	} {
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		ecKeys[algorithm] = jose.JSONWebKey{Key: &key.PublicKey}
	}
	edPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edKey := jose.JSONWebKey{Key: edPublic}
	taggedRSA := rsaKey
	taggedRSA.Algorithm = string(jose.RS384)
	wrongFamily := rsaKey
	wrongFamily.Algorithm = string(jose.ES256)
	encryptionKey := rsaKey
	encryptionKey.Use = "enc"
	for _, tc := range []struct {
		name       string
		keys       []jose.JSONWebKey
		algorithms []jose.SignatureAlgorithm
		wantKeys   int
	}{
		{name: "RSA RS256", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.RS256}, wantKeys: 1},
		{name: "RSA RS384", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.RS384}, wantKeys: 1},
		{name: "RSA RS512", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.RS512}, wantKeys: 1},
		{name: "RSA PS256", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.PS256}, wantKeys: 1},
		{name: "RSA PS384", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.PS384}, wantKeys: 1},
		{name: "RSA PS512", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.PS512}, wantKeys: 1},
		{name: "RSA cannot verify EC", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.ES256}},
		{name: "P256 ES256", keys: []jose.JSONWebKey{ecKeys[jose.ES256]}, algorithms: []jose.SignatureAlgorithm{jose.ES256}, wantKeys: 1},
		{name: "P384 ES384", keys: []jose.JSONWebKey{ecKeys[jose.ES384]}, algorithms: []jose.SignatureAlgorithm{jose.ES384}, wantKeys: 1},
		{name: "P521 ES512", keys: []jose.JSONWebKey{ecKeys[jose.ES512]}, algorithms: []jose.SignatureAlgorithm{jose.ES512}, wantKeys: 1},
		{name: "P384 cannot verify ES256", keys: []jose.JSONWebKey{ecKeys[jose.ES384]}, algorithms: []jose.SignatureAlgorithm{jose.ES256}},
		{name: "P521 cannot verify ES384", keys: []jose.JSONWebKey{ecKeys[jose.ES512]}, algorithms: []jose.SignatureAlgorithm{jose.ES384}},
		{name: "P256 cannot verify ES512", keys: []jose.JSONWebKey{ecKeys[jose.ES256]}, algorithms: []jose.SignatureAlgorithm{jose.ES512}},
		{name: "EC cannot verify RSA", keys: []jose.JSONWebKey{ecKeys[jose.ES256]}, algorithms: []jose.SignatureAlgorithm{jose.RS256}},
		{name: "Ed25519 EdDSA", keys: []jose.JSONWebKey{edKey}, algorithms: []jose.SignatureAlgorithm{jose.EdDSA}, wantKeys: 1},
		{name: "Ed25519 cannot verify EC", keys: []jose.JSONWebKey{edKey}, algorithms: []jose.SignatureAlgorithm{jose.ES256}},
		{name: "RSA cannot verify EdDSA", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.EdDSA}},
		{name: "matching explicit algorithm", keys: []jose.JSONWebKey{taggedRSA}, algorithms: []jose.SignatureAlgorithm{jose.RS384}, wantKeys: 1},
		{name: "unadvertised explicit algorithm", keys: []jose.JSONWebKey{taggedRSA}, algorithms: []jose.SignatureAlgorithm{jose.RS256}},
		{name: "tag cannot override key family", keys: []jose.JSONWebKey{wrongFamily}, algorithms: []jose.SignatureAlgorithm{jose.ES256}},
		{name: "encryption only", keys: []jose.JSONWebKey{encryptionKey}, algorithms: []jose.SignatureAlgorithm{jose.RS256}},
		{name: "mixed keys", keys: []jose.JSONWebKey{rsaKey, ecKeys[jose.ES256]}, algorithms: []jose.SignatureAlgorithm{jose.ES256}, wantKeys: 1},
		{name: "one advertised algorithm suffices", keys: []jose.JSONWebKey{rsaKey}, algorithms: []jose.SignatureAlgorithm{jose.ES256, jose.RS256}, wantKeys: 1},
		{name: "matching tag among advertised algorithms", keys: []jose.JSONWebKey{taggedRSA}, algorithms: []jose.SignatureAlgorithm{jose.RS256, jose.RS384}, wantKeys: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: tc.keys}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			set := &callbackKeySet{url: server.URL, client: server.Client(), algorithms: tc.algorithms}
			keys, err := set.fetch(context.Background())
			if tc.wantKeys == 0 {
				if !errors.Is(err, ErrProviderUnavailable) {
					t.Fatalf("incompatible key set accepted: keys=%d error=%v", len(keys), err)
				}
			} else if err != nil || len(keys) != tc.wantKeys {
				t.Fatalf("usable key set rejected: keys=%d want=%d error=%v", len(keys), tc.wantKeys, err)
			}
		})
	}
}

// TestJWKSRequestInheritsCallbackCancellation verifies cancellation stops an in-flight trusted-key refresh.
func TestJWKSRequestInheritsCallbackCancellation(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	keySet := &callbackKeySet{url: server.URL, client: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := keySet.fetch(ctx)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("JWKS request did not begin")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled JWKS request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("JWKS fetch did not respect callback cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("JWKS request continued after cancellation")
	}
}
