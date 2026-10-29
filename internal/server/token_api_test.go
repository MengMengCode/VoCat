package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"vocat/internal/auth"
	"vocat/internal/store"
)

func TestAPITokenInventoryEndpoints(t *testing.T) {
	app, authService, database := newBearerTestApplication(t)
	write, err := authService.CreateAPIToken(context.Background(), "web-visible", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := authService.CreateAPIToken(context.Background(), "viewer", true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// A bearer read-only token can read the inventory.
	request, err := http.NewRequest(http.MethodGet, app.server.URL+"/api/settings/tokens", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+readOnly.Token)
	response, err := app.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET tokens status = %d (%s)", response.StatusCode, apiErrorCode(t, response))
	}
	var inventory struct {
		Data struct {
			Tokens []map[string]any `json:"tokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Data.Tokens) != 2 {
		t.Fatalf("tokens = %+v", inventory.Data.Tokens)
	}
	// The response must never carry credential material.
	encoded, _ := json.Marshal(inventory)
	if strings.Contains(string(encoded), write.Token) || strings.Contains(string(encoded), "token_hash") {
		t.Fatalf("inventory leaks credentials: %s", encoded)
	}

	// A read-only bearer cannot revoke.
	request, err = http.NewRequest(http.MethodDelete, app.server.URL+"/api/settings/tokens/web-visible", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+readOnly.Token)
	response, err = app.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	code := apiErrorCode(t, response)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || code != "token_read_only" {
		t.Fatalf("DELETE with read-only token = %d (%s), want 403", response.StatusCode, code)
	}

	// A write bearer revokes; the audit trail names the token actor.
	request, err = http.NewRequest(http.MethodDelete, app.server.URL+"/api/settings/tokens/viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+write.Token)
	response, err = app.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d (%s)", response.StatusCode, apiErrorCode(t, response))
	}
	if _, err := authService.AuthenticateAPIToken(context.Background(), readOnly.Token); err == nil {
		t.Fatal("revoked token still authenticates")
	}

	// Unknown names 404; a name-less delete 400s.
	request, err = http.NewRequest(http.MethodDelete, app.server.URL+"/api/settings/tokens/ghost", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+write.Token)
	response, err = app.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE unknown = %d, want 404", response.StatusCode)
	}

	events, err := database.ListAuditEvents(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "settings.tokens.revoke" && event.EntityID == "viewer" && event.Actor == "token:web-visible" {
			found = true
		}
	}
	if !found {
		t.Fatalf("revoke audit row missing: %+v", events)
	}
}

func TestAPITokenInventoryViaWebSession(t *testing.T) {
	app, authService, _ := newBearerTestApplication(t)
	if _, err := authService.CreateAPIToken(context.Background(), "listed", false, 0); err != nil {
		t.Fatal(err)
	}

	// The web session flow (cookie + CSRF) manages the same inventory.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	app.client = &http.Client{Jar: jar}
	loginBody := bytes.NewBufferString(`{"username":"admin","password":"correct-password"}`)
	response, err := app.client.Post(app.server.URL+"/api/auth/login", "application/json", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	request, err := http.NewRequest(http.MethodDelete, app.server.URL+"/api/settings/tokens/listed", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-CSRF-Token", login.Data.CSRFToken)
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session DELETE status = %d (%s)", response.StatusCode, apiErrorCode(t, response))
	}
}

func TestAPITokenCreationFromWebSessionOnly(t *testing.T) {
	app, authService, database := newBearerTestApplication(t)
	write, err := authService.CreateAPIToken(context.Background(), "writer", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := authService.CreateAPIToken(context.Background(), "reader", true, 0)
	if err != nil {
		t.Fatal(err)
	}

	// A read-only bearer is stopped by the method gate first.
	request, err := http.NewRequest(http.MethodPost, app.server.URL+"/api/settings/tokens",
		bytes.NewBufferString(`{"name":"nope"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+readOnly.Token)
	response, err := app.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	roCode := apiErrorCode(t, response)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || roCode != "token_read_only" {
		t.Fatalf("POST with read-only bearer = %d (%s), want 403 token_read_only", response.StatusCode, roCode)
	}

	// A bearer credential — even a write one — must not mint tokens.
	request, err = http.NewRequest(http.MethodPost, app.server.URL+"/api/settings/tokens",
		bytes.NewBufferString(`{"name":"self-perpetuating"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+write.Token)
	response, err = app.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	code := apiErrorCode(t, response)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || code != "session_required" {
		t.Fatalf("POST with bearer = %d (%s), want 403 session_required", response.StatusCode, code)
	}

	// The web session creates a token and sees the raw value exactly once.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	app.client = &http.Client{Jar: jar}
	loginBody := bytes.NewBufferString(`{"username":"admin","password":"correct-password"}`)
	response, err = app.client.Post(app.server.URL+"/api/auth/login", "application/json", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	request, err = http.NewRequest(http.MethodPost, app.server.URL+"/api/settings/tokens",
		bytes.NewBufferString(`{"name":"web-made","read_only":true,"ttl_hours":24}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", login.Data.CSRFToken)
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d (%s), want 201", response.StatusCode, apiErrorCode(t, response))
	}
	var created struct {
		Data struct {
			Token     string `json:"token"`
			Name      string `json:"name"`
			ReadOnly  bool   `json:"read_only"`
			ExpiresAt string `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Data.Token, auth.APITokenPrefix) ||
		created.Data.Name != "web-made" || !created.Data.ReadOnly || created.Data.ExpiresAt == "" {
		t.Fatalf("created payload = %+v", created.Data)
	}
	if _, err := authService.AuthenticateAPIToken(context.Background(), created.Data.Token); err != nil {
		t.Fatalf("web-created token does not authenticate: %v", err)
	}

	// Validation paths.
	for _, test := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"name":"web-made"}`, http.StatusConflict, "duplicate_token_name"},
		{`{"name":"bad name"}`, http.StatusBadRequest, "invalid_token_name"},
		{`{"name":"negative","ttl_hours":-1}`, http.StatusBadRequest, "invalid_ttl"},
	} {
		request, err = http.NewRequest(http.MethodPost, app.server.URL+"/api/settings/tokens",
			bytes.NewBufferString(test.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", login.Data.CSRFToken)
		response, err = app.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		code := apiErrorCode(t, response)
		_ = response.Body.Close()
		if response.StatusCode != test.status || code != test.code {
			t.Fatalf("POST %s = %d (%s), want %d (%s)", test.body, response.StatusCode, code, test.status, test.code)
		}
	}

	// The creation is audited as the administrator.
	events, err := database.ListAuditEvents(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "settings.tokens.create" && event.EntityID == "web-made" && event.Actor == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("create audit row missing: %+v", events)
	}
}
