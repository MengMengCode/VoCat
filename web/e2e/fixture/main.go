// This executable exists only for browser tests. It runs the real VoCat server
// with an isolated SQLite database and a signed, local OIDC identity provider.
// Test-only endpoints and the loopback TLS transport are never in cmd/vocat.
package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"vocat/internal/auth"
	"vocat/internal/server"
	"vocat/internal/sso"
	"vocat/internal/store"
	"vocat/web"
)

const (
	appAddress    = "127.0.0.1:17475"
	idpAddress    = "127.0.0.1:17476"
	appOrigin     = "https://vocat.test:17475"
	idpOrigin     = "https://pocket.test:17476"
	clientID      = "vocat-browser-test"
	clientSecret  = "browser-test-client-secret"
	localPassword = "browser-test-local-password"
)

type authorization struct{ State, Nonce, Challenge, Redirect string }
type callbackTrace struct {
	Cookies        []string `json:"cookies"`
	ClearedCookies []string `json:"cleared_cookies"`
	Status         int      `json:"status"`
}
type statistics struct {
	Callbacks        []callbackTrace `json:"callbacks"`
	TokenRequests    int             `json:"token_requests"`
	PKCEVerified     bool            `json:"pkce_verified"`
	BasicVerified    bool            `json:"basic_verified"`
	RedirectVerified bool            `json:"redirect_verified"`
}

type fixture struct {
	handler   atomic.Pointer[server.Server]
	client    *http.Client
	directory string
	key       *rsa.PrivateKey
	mu        sync.Mutex
	requests  map[string]authorization
	codes     map[string]authorization
	stats     statistics
	databases []*store.Store
	sequence  int
}

// main reports fixture startup or serving failures to the browser-test runner.
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run starts isolated TLS application and identity-provider servers with a fixture-only trusted transport.
// Temporary databases and listeners are released when the test process terminates.
func run() error {
	certificate, roots, err := testCertificate()
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "vocat-browser-sso-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || (host != "pocket.test" && host != "vocat.test") {
				return nil, errors.New("test transport refused a non-fixture host")
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
	}
	defer transport.CloseIdleConnections()
	f := &fixture{directory: directory, key: key, client: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
	if err := f.reset(); err != nil {
		return err
	}
	defer func() {
		for _, database := range f.databases {
			database.Close()
		}
	}()

	servers := []*http.Server{
		{Addr: appAddress, Handler: http.HandlerFunc(f.serveTLSProxy), ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)},
		{Addr: "127.0.0.1:17477", Handler: http.HandlerFunc(f.serveApp), ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)},
		{Addr: idpAddress, Handler: http.HandlerFunc(f.serveIDP), ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)},
	}
	failures := make(chan error, len(servers))
	for _, srv := range servers {
		listener, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			return err
		}
		if srv.Addr != "127.0.0.1:17477" {
			listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		}
		go func(srv *http.Server, listener net.Listener) { failures <- srv.Serve(listener) }(srv, listener)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Println("VoCat SSO browser fixture ready")
	select {
	case <-ctx.Done():
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	for _, srv := range servers {
		_ = srv.Close()
	}
	return nil
}

// reset creates a fresh application database and clears provider authorization state and captured statistics.
func (f *fixture) reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence++
	database, err := store.Open(context.Background(), filepath.Join(f.directory, fmt.Sprintf("test-%d.db", f.sequence)))
	if err != nil {
		return err
	}
	authentication, err := auth.New(database, auth.Options{SessionTTL: time.Hour, BcryptCost: 4})
	if err != nil {
		database.Close()
		return err
	}
	if err := authentication.EnsureAdmin(context.Background(), "admin", localPassword); err != nil {
		database.Close()
		return err
	}
	handler, err := server.New(server.Options{Store: database, Auth: authentication, Assets: web.Dist,
		SSO: sso.New(database, sso.Options{HTTPClient: f.client})})
	if err != nil {
		database.Close()
		return err
	}
	f.databases = append(f.databases, database)
	f.requests = make(map[string]authorization)
	f.codes = make(map[string]authorization)
	f.stats = statistics{Callbacks: []callbackTrace{}}
	f.handler.Store(handler)
	return nil
}

// serveApp dispatches real application requests and header-protected fixture controls, capturing callback cookie behavior.
func (f *fixture) serveApp(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__e2e/") {
		if r.URL.Path == "/__e2e/ready" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Header.Get("X-VoCat-E2E") != "fixture" {
			http.Error(w, "test header required", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/__e2e/reset":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if err := f.reset(); err != nil {
				http.Error(w, "reset failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/__e2e/stats":
			f.mu.Lock()
			defer f.mu.Unlock()
			jsonResponse(w, f.stats)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.URL.Path == "/api/auth/sso/callback" {
		trace := callbackTrace{Cookies: []string{}, ClearedCookies: []string{}}
		for _, cookie := range r.Cookies() {
			trace.Cookies = append(trace.Cookies, cookie.Name)
		}
		capture := &callbackWriter{ResponseWriter: w, trace: &trace}
		f.handler.Load().ServeHTTP(capture, r)
		f.mu.Lock()
		f.stats.Callbacks = append(f.stats.Callbacks, trace)
		f.mu.Unlock()
		return
	}
	f.handler.Load().ServeHTTP(w, r)
}

type callbackWriter struct {
	http.ResponseWriter
	trace *callbackTrace
}

// WriteHeader records callback status and deleted cookie names before forwarding the response.
func (w *callbackWriter) WriteHeader(status int) {
	w.trace.Status = status
	response := &http.Response{Header: w.Header()}
	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 {
			w.trace.ClearedCookies = append(w.trace.ClearedCookies, cookie.Name)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

var authorizationPage = template.Must(template.New("authorize").Parse(`<!doctype html><html lang="en"><meta name="viewport" content="width=device-width"><title>Test Pocket ID</title><body style="font-family:system-ui;max-width:32rem;margin:3rem auto;padding:1rem"><h1>Test Pocket ID</h1><p>Continue as the test administrator.</p><form method="post" action="/decision"><input type="hidden" name="request" value="{{.}}"><button name="decision" value="allow">Authorize test account</button> <button name="decision" value="cancel">Cancel authorization</button></form></body></html>`))

// serveIDP simulates discovery, signing keys, user authorization, cancellation, and one-use PKCE token exchange.
func (f *fixture) serveIDP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		jsonResponse(w, map[string]any{"issuer": idpOrigin, "authorization_endpoint": idpOrigin + "/authorize", "token_endpoint": idpOrigin + "/token", "jwks_uri": idpOrigin + "/jwks",
			"response_types_supported": []string{"code"}, "response_modes_supported": []string{"query"}, "grant_types_supported": []string{"authorization_code"},
			"code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/jwks":
		jsonResponse(w, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "browser-test-key", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
	case "/authorize":
		q := r.URL.Query()
		if q.Get("client_id") != clientID || !allowedCallback(q.Get("redirect_uri")) || q.Get("response_type") != "code" || q.Get("response_mode") != "query" || q.Get("code_challenge_method") != "S256" || len(q.Get("nonce")) < 32 || len(q.Get("state")) < 32 || len(q.Get("code_challenge")) < 32 || !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
			http.Error(w, "invalid authorization request", http.StatusBadRequest)
			return
		}
		id := randomID()
		f.mu.Lock()
		f.requests[id] = authorization{State: q.Get("state"), Nonce: q.Get("nonce"), Challenge: q.Get("code_challenge"), Redirect: q.Get("redirect_uri")}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = authorizationPage.Execute(w, id)
	case "/decision":
		if r.Method != http.MethodPost || r.ParseForm() != nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		authorization, ok := f.requests[r.Form.Get("request")]
		delete(f.requests, r.Form.Get("request"))
		if !ok {
			f.mu.Unlock()
			http.Error(w, "request expired", http.StatusBadRequest)
			return
		}
		q := url.Values{"state": {authorization.State}, "iss": {idpOrigin}}
		if r.Form.Get("decision") == "allow" {
			code := randomID()
			f.codes[code] = authorization
			q.Set("code", code)
		} else {
			q.Set("error", "access_denied")
		}
		f.mu.Unlock()
		http.Redirect(w, r, authorization.Redirect+"?"+q.Encode(), http.StatusSeeOther)
	case "/token":
		if r.Method != http.MethodPost || r.ParseForm() != nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		user, secret, basic := r.BasicAuth()
		user, _ = url.QueryUnescape(user)
		secret, _ = url.QueryUnescape(secret)
		f.mu.Lock()
		authorization, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		pkce := authorization.Challenge == base64.RawURLEncoding.EncodeToString(digest[:]) && r.Form.Get("code_verifier") != ""
		redirect := authorization.Redirect == r.Form.Get("redirect_uri") && allowedCallback(authorization.Redirect)
		validBasic := basic && user == clientID && secret == clientSecret
		if !ok || !validBasic || !pkce || !redirect || r.Form.Get("grant_type") != "authorization_code" || r.URL.RawQuery != "" {
			f.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			jsonResponse(w, map[string]string{"error": "invalid_grant"})
			return
		}
		f.stats.TokenRequests++
		f.stats.PKCEVerified = pkce
		f.stats.BasicVerified = validBasic
		f.stats.RedirectVerified = redirect
		f.mu.Unlock()
		now := time.Now().Unix()
		token, err := f.signedToken(map[string]any{"iss": idpOrigin, "aud": []string{clientID}, "sub": "browser-test-account", "iat": now, "exp": now + 300, "nonce": authorization.Nonce, "name": "Browser test administrator"})
		if err != nil {
			http.Error(w, "signing failed", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"access_token": "browser-test-transient-access-token", "refresh_token": "browser-test-must-never-persist", "token_type": "Bearer", "expires_in": 300, "id_token": token})
	default:
		http.NotFound(w, r)
	}
}

// signedToken creates an RS256 identity token using the fixture signing key and supplied claims.
func (f *fixture) signedToken(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "RS256", "kid": "browser-test-key"})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// jsonResponse serializes fixture responses as JSON.
func jsonResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// randomID creates an unpredictable URL-safe identifier for fixture authorization requests and codes.
func randomID() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:])
}

// testCertificate generates a short-lived local CA and server certificate covering only fixture hosts and loopback.
func testCertificate() (tls.Certificate, *x509.CertPool, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	certificate := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "VoCat browser test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"vocat.test", "pocket.test", "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	tlsCertificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return tlsCertificate, roots, nil
}

// allowedCallback confines fixture redirects to registered local callback entries.
func allowedCallback(uri string) bool {
	return uri == appOrigin+sso.CallbackPath || uri == "http://127.0.0.1:17477"+sso.CallbackPath || uri == "http://localhost:17477"+sso.CallbackPath
}

// serveTLSProxy simulates TLS termination with proxy-header trust disabled.
// The browser sees HTTPS while the application receives a plain backend request.
func (f *fixture) serveTLSProxy(w http.ResponseWriter, r *http.Request) {
	backend := r.Clone(r.Context())
	backend.TLS = nil
	backend.Header.Set("X-Forwarded-Proto", "https")
	f.serveApp(w, backend)
}
