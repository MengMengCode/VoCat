package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOperationalHealthEndpointsAreAnonymousAndNonIdentifying(t *testing.T) {
	app := newTestApplication(t)

	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/healthz", contentType: "application/json", contains: `"status":"ok"`},
		{path: "/readyz", contentType: "application/json", contains: `"status":"ready"`},
		{path: "/metrics", contentType: "text/plain", contains: "vocat_ready 1"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response, err := app.client.Get(app.server.URL + test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.StatusCode, body)
			}
			if !strings.Contains(response.Header.Get("Content-Type"), test.contentType) {
				t.Fatalf("Content-Type = %q", response.Header.Get("Content-Type"))
			}
			if !strings.Contains(string(body), test.contains) {
				t.Fatalf("body = %q, want %q", body, test.contains)
			}
			for _, forbidden := range []string{"imsi", "iccid", "msisdn", "proxy", "device_id"} {
				if strings.Contains(strings.ToLower(string(body)), forbidden) {
					t.Fatalf("body exposes forbidden label %q: %s", forbidden, body)
				}
			}
		})
	}
}

func TestOperationalHealthEndpointsRejectPOST(t *testing.T) {
	app := newTestApplication(t)
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		request, err := http.NewRequest(http.MethodPost, app.server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := app.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", path, response.StatusCode, http.StatusMethodNotAllowed)
		}
	}
}
