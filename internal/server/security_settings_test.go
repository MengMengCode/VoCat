package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

func TestParseAccessConfigValidation(t *testing.T) {
	if _, err := parseAccessConfig(accessConfig{Mode: "bogus"}); err == nil {
		t.Fatal("accepted an invalid mode")
	}
	if _, err := parseAccessConfig(accessConfig{Mode: "internal", AllowedCIDRs: []string{"not-a-cidr"}}); err == nil {
		t.Fatal("accepted an invalid CIDR")
	}
	parsed, err := parseAccessConfig(accessConfig{Mode: "internal", AllowedCIDRs: []string{"203.0.113.0/24", "198.51.100.7"}})
	if err != nil {
		t.Fatalf("parseAccessConfig: %v", err)
	}
	if len(parsed.cidrs) != 2 {
		t.Fatalf("cidrs = %v", parsed.cidrs)
	}
}

func TestAccessControlMiddleware(t *testing.T) {
	server := &Server{logger: regionTestLogger()}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := server.accessControl(ok)

	check := func(config parsedAccessConfig, remoteAddr string, forwardedFor string) int {
		server.accessMu.Lock()
		server.access = config
		server.accessMu.Unlock()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remoteAddr
		if forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", forwardedFor)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Code
	}

	internal := parsedAccessConfig{mode: "internal"}
	if got := check(internal, "192.168.2.10:5000", ""); got != http.StatusOK {
		t.Fatalf("private IP denied: %d", got)
	}
	if got := check(internal, "127.0.0.1:5000", ""); got != http.StatusOK {
		t.Fatalf("loopback denied: %d", got)
	}
	if got := check(internal, "8.8.8.8:5000", ""); got != http.StatusForbidden {
		t.Fatalf("public IP allowed in internal mode: %d", got)
	}
	// Custom CIDR admits an otherwise-public range.
	withCIDR := parsedAccessConfig{mode: "internal"}
	parsed, _ := parseAccessConfig(accessConfig{Mode: "internal", AllowedCIDRs: []string{"8.8.8.0/24"}})
	withCIDR = parsed
	if got := check(withCIDR, "8.8.8.8:5000", ""); got != http.StatusOK {
		t.Fatalf("custom CIDR not honored: %d", got)
	}
	// Public mode allows anything.
	public := parsedAccessConfig{mode: "public"}
	if got := check(public, "8.8.8.8:5000", ""); got != http.StatusOK {
		t.Fatalf("public mode denied a public IP: %d", got)
	}
	// Proxy headers are ignored unless explicitly trusted.
	trust := parsedAccessConfig{mode: "internal", trustProxy: true}
	if got := check(trust, "8.8.8.8:5000", "192.168.1.20"); got != http.StatusOK {
		t.Fatalf("trusted X-Forwarded-For not honored: %d", got)
	}
	if got := check(internal, "8.8.8.8:5000", "192.168.1.20"); got != http.StatusForbidden {
		t.Fatalf("untrusted X-Forwarded-For was honored: %d", got)
	}
}

func TestLoginRateLimiterLocksAndResets(t *testing.T) {
	limiter := newLoginRateLimiter()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	key := "192.168.1.1|admin"

	for i := 0; i < limiter.maxFailures-1; i++ {
		if _, locked := limiter.recordFailure(key); locked {
			t.Fatalf("locked after %d failures, below threshold", i+1)
		}
	}
	if _, locked := limiter.recordFailure(key); !locked {
		t.Fatal("not locked at the failure threshold")
	}
	if _, locked := limiter.checkLocked(key); !locked {
		t.Fatal("checkLocked did not report the lock")
	}
	// Success clears the track record.
	limiter.recordSuccess(key)
	if _, locked := limiter.checkLocked(key); locked {
		t.Fatal("still locked after a success")
	}
	// Lockout expires after the lockout duration.
	for i := 0; i < limiter.maxFailures; i++ {
		limiter.recordFailure(key)
	}
	now = now.Add(limiter.lockout + time.Second)
	if _, locked := limiter.checkLocked(key); locked {
		t.Fatal("lock did not expire after the lockout window")
	}
}

func newSettingsTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{
		store:               database,
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		access:              defaultAccessConfig(),
	}
}

func TestHandleSecuritySettingsRoundTrip(t *testing.T) {
	server := newSettingsTestServer(t)
	body := `{"mode":"internal","allowed_cidrs":["203.0.113.0/24"],"trust_proxy_headers":true}`
	request := httptest.NewRequest(http.MethodPut, "/api/settings/security", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "192.168.2.20:5000"
	recorder := httptest.NewRecorder()
	server.handleSecuritySettings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if server.currentAccessConfig().trustProxy != true {
		t.Fatal("runtime access config was not updated")
	}
	// Persisted?
	setting, err := server.store.AppSetting(context.Background(), accessSettingKey)
	if err != nil || !strings.Contains(string(setting.Value), "203.0.113.0/24") {
		t.Fatalf("access policy not persisted: %v %v", setting, err)
	}
	// GET reflects it.
	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/security", nil)
	getReq.RemoteAddr = "192.168.2.20:5000"
	server.handleSecuritySettings(getRec, getReq)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(getRec.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data["trust_proxy_headers"] != true || envelope.Data["client_allowed"] != true {
		t.Fatalf("GET data = %v", envelope.Data)
	}
}

func TestHandleSecuritySettingsRejectsBadPolicy(t *testing.T) {
	server := newSettingsTestServer(t)
	request := httptest.NewRequest(http.MethodPut, "/api/settings/security", strings.NewReader(`{"mode":"nowhere"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleSecuritySettings(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestHandleLoggingSettingsRoundTripAndEnforceCount(t *testing.T) {
	server := newSettingsTestServer(t)
	// Seed 10 log rows.
	for i := 0; i < 10; i++ {
		if _, err := server.store.AppendLogEvent(context.Background(), store.LogEvent{
			Level: "info", Message: "entry",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Keep only the newest 4.
	request := httptest.NewRequest(http.MethodPut, "/api/settings/logging", strings.NewReader(`{"mode":"count","count":4}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleLoggingSettings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	count, err := server.store.CountLogEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("stored log count = %d, want 4 after retention", count)
	}

	// Verify GET returns level as well
	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/logging", nil)
	getRec := httptest.NewRecorder()
	server.handleLoggingSettings(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", getRec.Code)
	}
	if !strings.Contains(getRec.Body.String(), `"level":"debug"`) {
		t.Fatalf("GET body = %s, want default level debug", getRec.Body.String())
	}
}

func TestHandleLoggingSettingsLevelRetention(t *testing.T) {
	server := newSettingsTestServer(t)
	ctx := context.Background()

	// Seed logs with various levels
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		if _, err := server.store.AppendLogEvent(ctx, store.LogEvent{Level: lvl, Message: lvl}); err != nil {
			t.Fatal(err)
		}
	}

	// Set level to warn
	request := httptest.NewRequest(http.MethodPut, "/api/settings/logging", strings.NewReader(`{"mode":"unlimited","level":"warn"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleLoggingSettings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"level":"warn"`) {
		t.Fatalf("body = %s, want level warn", recorder.Body.String())
	}

	// Existing debug and info logs should have been pruned -> count should be 2 (warn, error)
	count, err := server.store.CountLogEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count after prune = %d, want 2", count)
	}

	// New debug log should be ignored
	if _, err := server.store.AppendLogEvent(ctx, store.LogEvent{Level: "debug", Message: "new debug"}); err != nil {
		t.Fatal(err)
	}
	// New error log should be persisted
	if _, err := server.store.AppendLogEvent(ctx, store.LogEvent{Level: "error", Message: "new error"}); err != nil {
		t.Fatal(err)
	}
	count, _ = server.store.CountLogEvents(ctx)
	if count != 3 {
		t.Fatalf("count after new logs = %d, want 3", count)
	}
}

func TestLoggingCountIsClampedToHardLimit(t *testing.T) {
	config, err := parseLoggingConfig(loggingConfig{Mode: "count", Count: store.MaxLogEvents + 500})
	if err != nil {
		t.Fatal(err)
	}
	if config.Count != store.MaxLogEvents {
		t.Fatalf("count = %d, want %d", config.Count, store.MaxLogEvents)
	}
}

func TestLoginLockoutViaHTTP(t *testing.T) {
	app := newTestApplication(t)
	for i := 0; i < 4; i++ {
		response, err := app.client.Post(app.server.URL+"/api/auth/login", "application/json",
			strings.NewReader(`{"username":"admin","password":"wrong"}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i+1, response.StatusCode)
		}
	}
	// Fifth consecutive failure crosses the threshold and locks.
	response, err := app.client.Post(app.server.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("5th failure status = %d, want 429", response.StatusCode)
	}
	// Even the correct password is refused while locked.
	response, err = app.client.Post(app.server.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"correct-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked login status = %d, want 429", response.StatusCode)
	}
}
