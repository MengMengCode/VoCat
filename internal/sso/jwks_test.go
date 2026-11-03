package sso

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
