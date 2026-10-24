package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/auth"
	"vocat/internal/device"
	vocatmcp "vocat/internal/mcp"
	"vocat/internal/store"
)

// newMCPTestApplication builds an application with the MCP endpoint mounted,
// mirroring how cmd/vocat wires the read-write and read-only variants.
func newMCPTestApplication(t *testing.T) (testApplication, *auth.Service) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authService, err := auth.New(database, auth.Options{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authService.EnsureAdmin(context.Background(), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	readWrite, err := vocatmcp.New(vocatmcp.Options{
		Store:   database,
		Devices: &stubMCPDevices{},
		Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := vocatmcp.New(vocatmcp.Options{
		Store:    database,
		Devices:  &stubMCPDevices{},
		Version:  "test",
		ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{
		Store:  database,
		Auth:   authService,
		Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}},
		MCP: &MCPHandlers{
			ReadWrite: readWrite.Handler(),
			ReadOnly:  readOnly.Handler(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return testApplication{server: httpServer, client: httpServer.Client()}, authService
}

// stubMCPDevices satisfies the MCP device seam with no hardware behind it.
type stubMCPDevices struct{}

func (d *stubMCPDevices) List() []device.Device { return nil }

func (d *stubMCPDevices) Get(string) (device.Device, error) {
	return device.Device{}, store.ErrNotFound
}

func (d *stubMCPDevices) Refresh(context.Context, string) (device.Snapshot, error) {
	return device.Snapshot{}, store.ErrNotFound
}

func (d *stubMCPDevices) SendSMS(context.Context, string, string, string) (device.SMSSendResult, error) {
	return device.SMSSendResult{}, store.ErrNotFound
}

func (d *stubMCPDevices) ESIMListProfiles(context.Context, string) (device.EsimInfo, error) {
	return device.EsimInfo{}, store.ErrNotFound
}

func (d *stubMCPDevices) ESIMSwitchProfile(context.Context, string, string, string) error {
	return store.ErrNotFound
}

func TestMCPEndpointDisabledByDefault(t *testing.T) {
	app := newTestApplication(t)
	response, err := app.client.Post(app.server.URL+"/mcp", "application/json",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("/mcp without wiring status = %d, want 404", response.StatusCode)
	}
}

func TestMCPEndpointRequiresBearerToken(t *testing.T) {
	app, _ := newMCPTestApplication(t)

	response, err := app.client.Post(app.server.URL+"/mcp", "application/json",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("/mcp without token = %d, want 401 and no session", response.StatusCode)
	}

	forged, err := http.NewRequest(http.MethodPost, app.server.URL+"/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	forged.Header.Set("Content-Type", "application/json")
	forged.Header.Set("Authorization", "Bearer "+auth.APITokenPrefix+"forged")
	response, err = app.client.Do(forged)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/mcp with forged token = %d, want 401", response.StatusCode)
	}
}

func TestMCPEndpointServesToolListByTokenKind(t *testing.T) {
	app, authService := newMCPTestApplication(t)
	write, err := authService.CreateAPIToken(context.Background(), "agent-rw", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := authService.CreateAPIToken(context.Background(), "agent-ro", true, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		token    string
		wantSend bool
	}{
		{token: write.Token, wantSend: true},
		{token: readOnly.Token, wantSend: false},
	} {
		endpoint := app.server.URL + "/mcp"
		sessionID := mcpInitialize(t, endpoint, test.token)
		names := mcpListToolNames(t, endpoint, test.token, sessionID)
		if !names["list_devices"] || !names["vowifi_status"] {
			t.Fatalf("tool list missing read tools: %v", names)
		}
		if names["sms_send"] != test.wantSend {
			t.Fatalf("sms_send presence = %v for wantSend=%v: %v", names["sms_send"], test.wantSend, names)
		}
	}
}

// mcpInitialize performs the JSON-RPC initialize handshake and returns the
// session id the Streamable HTTP transport issued.
func mcpInitialize(t *testing.T, url string, token string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"test","version":"0"}}}`
	response := mcpPost(t, url, token, "", body)
	sessionID := response.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize response carried no Mcp-Session-Id header")
	}
	var result struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	decodeMCPBody(t, response, &result)
	if result.Result.ServerInfo.Name != "vocat" {
		t.Fatalf("serverInfo.name = %q, want vocat", result.Result.ServerInfo.Name)
	}
	// The initialized notification completes the handshake.
	mcpPost(t, url, token, sessionID,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return sessionID
}

func mcpListToolNames(t *testing.T, url string, token string, sessionID string) map[string]bool {
	t.Helper()
	response := mcpPost(t, url, token, sessionID,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	var result struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	decodeMCPBody(t, response, &result)
	names := map[string]bool{}
	for _, tool := range result.Result.Tools {
		names[tool.Name] = true
	}
	return names
}

func mcpPost(t *testing.T, url string, token string, sessionID string, payload string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token)
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	// Notification posts answer 202 Accepted per the Streamable HTTP spec;
	// request posts answer 200 with the JSON-RPC result.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s status = %d: %s", url, response.StatusCode, body)
	}
	return response
}

// decodeMCPBody handles both single JSON responses and SSE-framed ones.
func decodeMCPBody(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	contentType := response.Header.Get("Content-Type")
	payload := string(body)
	if strings.Contains(contentType, "text/event-stream") {
		for _, line := range strings.Split(payload, "\n") {
			if strings.HasPrefix(line, "data:") {
				payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				break
			}
		}
	}
	if err := json.Unmarshal([]byte(payload), destination); err != nil {
		t.Fatalf("decode mcp response %q (%s): %v", payload, contentType, err)
	}
}
