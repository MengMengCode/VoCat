package device

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestES9P routes an es9pClient at a throwaway TLS server.
func newTestES9P(t *testing.T, handler http.HandlerFunc) *es9pClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &es9pClient{
		smdp:     strings.TrimPrefix(server.URL, "https://"),
		endpoint: endpoint,
		http:     server.Client(),
	}
}

func successEnvelope(fields map[string]any) map[string]any {
	env := map[string]any{
		"header": map[string]any{"functionExecutionStatus": map[string]any{"status": "Executed-Success"}},
	}
	for key, value := range fields {
		env[key] = value
	}
	return env
}

func b64(value []byte) string { return base64.StdEncoding.EncodeToString(value) }

func TestNewES9PClientRejectsUnsafeAddress(t *testing.T) {
	for _, address := range []string{
		"https://rsp.example.com",
		"127.0.0.1",
		"169.254.169.254",
		"rsp.example.com/unexpected/path",
		"user:password@rsp.example.com",
		"rsp.example.com\r\nX-Injected: yes",
	} {
		if _, err := newES9PClient(context.Background(), address); err == nil {
			t.Errorf("newES9PClient(%q) accepted an unsafe address", address)
		}
	}
}

func TestInitiateAuthenticationSuccess(t *testing.T) {
	signed1 := []byte{0x30, 0x03, 0x80, 0x01, 0x09}
	client := newTestES9P(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gsma/rsp2/es9plus/initiateAuthentication" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Admin-Protocol") != "gsma/rsp/v2.2.2" {
			t.Errorf("X-Admin-Protocol = %q", r.Header.Get("X-Admin-Protocol"))
		}
		if r.Header.Get("User-Agent") != "gsma-rsp-lpad" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["smdpAddress"] == "" || req["euiccChallenge"] == "" || req["euiccInfo1"] == "" {
			t.Errorf("missing request fields: %v", req)
		}
		_ = json.NewEncoder(w).Encode(successEnvelope(map[string]any{
			"transactionId":       "dHJhbnNhY3Rpb24=",
			"serverSigned1":       b64(signed1),
			"serverSignature1":    b64([]byte{0x01, 0x02, 0x03}),
			"euiccCiPKIdToBeUsed": b64([]byte{0x04, 0x05}),
			"serverCertificate":   b64([]byte{0x30, 0x01, 0x00}),
		}))
	})

	result, err := client.initiateAuthentication(context.Background(), []byte{0x09, 0x09}, []byte{0x08, 0x08})
	if err != nil {
		t.Fatalf("initiateAuthentication: %v", err)
	}
	if result.TransactionID != "dHJhbnNhY3Rpb24=" {
		t.Errorf("transactionId = %q", result.TransactionID)
	}
	if !bytes.Equal(result.ServerSigned1, signed1) {
		t.Errorf("serverSigned1 = %X", result.ServerSigned1)
	}
	if !bytes.Equal(result.EuiccCiPKIDToBeUsed, []byte{0x04, 0x05}) {
		t.Errorf("euiccCiPKIdToBeUsed = %X", result.EuiccCiPKIDToBeUsed)
	}
}

// A Failed status with a server-supplied message surfaces that message verbatim.
func TestAuthenticateClientFailureMessage(t *testing.T) {
	client := newTestES9P(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"header": map[string]any{"functionExecutionStatus": map[string]any{
				"status": "Failed",
				"statusCodeData": map[string]string{
					"subjectCode": "8.2.6", "reasonCode": "3.8", "message": "The matchingID is not found",
				},
			}},
		})
	})
	_, err := client.authenticateClient(context.Background(), "dA==", []byte{0x01})
	if err == nil || err.Error() != "The matchingID is not found" {
		t.Fatalf("err = %v", err)
	}
	if code := ESIMDownloadErrorCode(err); code != "activation_code_refused" {
		t.Fatalf("code = %q, want activation_code_refused", code)
	}
}

// A Failed status with only codes (no message) falls back to the SGP.22 table,
// and the insufficient-memory pair maps to the SPA's special error code.
func TestGetBoundProfilePackageInsufficientMemory(t *testing.T) {
	client := newTestES9P(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"header": map[string]any{"functionExecutionStatus": map[string]any{
				"status":         "Failed",
				"statusCodeData": map[string]string{"subjectCode": "8.1", "reasonCode": "4.8"},
			}},
		})
	})
	_, err := client.getBoundProfilePackage(context.Background(), "dA==", []byte{0x01})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "sufficient space") {
		t.Fatalf("err = %v, want table-supplied space message", err)
	}
	if code := ESIMDownloadErrorCode(err); code != "euicc_insufficient_memory" {
		t.Fatalf("code = %q, want euicc_insufficient_memory", code)
	}
}

func TestGetBoundProfilePackageSuccess(t *testing.T) {
	pkg := []byte{0xBF, 0x36, 0x05, 0x01, 0x02, 0x03, 0x04, 0x05}
	client := newTestES9P(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gsma/rsp2/es9plus/getBoundProfilePackage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["transactionId"] == "" || req["prepareDownloadResponse"] == "" {
			t.Errorf("missing request fields: %v", req)
		}
		_ = json.NewEncoder(w).Encode(successEnvelope(map[string]any{
			"boundProfilePackage": b64(pkg),
		}))
	})
	got, err := client.getBoundProfilePackage(context.Background(), "dA==", []byte{0xAA})
	if err != nil {
		t.Fatalf("getBoundProfilePackage: %v", err)
	}
	if !bytes.Equal(got, pkg) {
		t.Fatalf("bpp = %X, want %X", got, pkg)
	}
}

func TestHandleNotificationRequiresHTTP204(t *testing.T) {
	pending := []byte{0xBF, 0x37, 0x00}
	client := newTestES9P(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gsma/rsp2/es9plus/handleNotification" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Admin-Protocol") != "gsma/rsp/v2.2.2" {
			t.Errorf("X-Admin-Protocol = %q", r.Header.Get("X-Admin-Protocol"))
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		decoded, err := base64.StdEncoding.DecodeString(request["pendingNotification"])
		if err != nil || !bytes.Equal(decoded, pending) {
			t.Errorf("pendingNotification = %q (%X), err=%v", request["pendingNotification"], decoded, err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := client.handleNotification(context.Background(), pending); err != nil {
		t.Fatalf("handleNotification: %v", err)
	}

	client = newTestES9P(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(successEnvelope(nil))
	})
	if err := client.handleNotification(context.Background(), pending); err == nil || !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("HTTP 200 error = %v", err)
	}
}
