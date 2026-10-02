package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

func newSMSExportTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{store: database, logger: regionTestLogger()}
}

func saveSMSExportTestMessage(t *testing.T, s *Server, m store.SMSMessage) store.SMSMessage {
	t.Helper()
	if m.DeviceID == "" {
		m.DeviceID = "device"
	}
	if m.Peer == "" {
		m.Peer = "peer"
	}
	if m.Direction == "" {
		m.Direction = "inbound"
	}
	saved, err := s.store.SaveSMSMessage(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestSMSExportJSONSchemaFiltersAndRenamedDevice(t *testing.T) {
	s := newSMSExportTestServer(t)
	ctx := context.Background()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := s.store.UpsertDevice(ctx, store.Device{ID: "renamed", Name: "Renamed", ModemIMEI: "imei-a"}); err != nil {
		t.Fatal(err)
	}
	saved := saveSMSExportTestMessage(t, s, store.SMSMessage{
		MessageID: "exact", DeviceID: "old-name", ModemIMEI: "imei-a", ICCID: "card-a", IMSI: "imsi-a", LocalPhone: "+123",
		Body: " \r\n" + strings.Repeat("原文🙂 <script> & ", 100) + "\n ", Timestamp: stamp, Status: "received", Source: "cellular_at",
		PartsTotal: 3, DeliveryState: "delivered", Read: true, Extra: []byte(`{"key":"value"}`), CreatedAt: stamp.Add(-time.Hour), UpdatedAt: stamp.Add(time.Hour),
	})
	for i, m := range []store.SMSMessage{
		{DeviceID: "old-name", ModemIMEI: "imei-a", Timestamp: stamp.Add(-time.Second)},
		{DeviceID: "old-name", ModemIMEI: "imei-a", Timestamp: stamp.Add(time.Second)},
		{DeviceID: "other", ModemIMEI: "imei-b", Timestamp: stamp},
	} {
		m.MessageID = fmt.Sprint(i)
		saveSMSExportTestMessage(t, s, m)
	}
	query := url.Values{"device_id": {"renamed"}, "since": {stamp.Format(time.RFC3339)}, "until": {stamp.Add(time.Second).Format(time.RFC3339)}}
	response := httptest.NewRecorder()
	s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?"+query.Encode(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertSMSExportHeaders(t, response, "json", "1")
	var got struct {
		Version    int               `json:"format_version"`
		ExportedAt time.Time         `json:"exported_at"`
		Filters    map[string]string `json:"filters"`
		Messages   []map[string]any  `json:"messages"`
		Count      int               `json:"message_count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantFilters := map[string]string{"device_id": "renamed", "modem_imei": "imei-a", "since": stamp.Format(time.RFC3339), "until": stamp.Add(time.Second).Format(time.RFC3339)}
	if got.Version != 1 || got.ExportedAt.IsZero() || got.Count != 1 || len(got.Messages) != 1 || !reflect.DeepEqual(got.Filters, wantFilters) {
		t.Fatalf("archive metadata/messages = %#v", got)
	}
	expected := map[string]any{
		"id": saved.ID, "message_id": saved.MessageID, "device_id": saved.DeviceID, "modem_imei": saved.ModemIMEI,
		"iccid": saved.ICCID, "imsi": saved.IMSI, "local_phone": saved.LocalPhone, "peer": saved.Peer,
		"direction": saved.Direction, "body": saved.Body, "timestamp": saved.Timestamp, "status": saved.Status,
		"source": saved.Source, "parts_total": saved.PartsTotal, "delivery_state": saved.DeliveryState,
		"read": saved.Read, "extra": saved.Extra, "created_at": saved.CreatedAt, "updated_at": saved.UpdatedAt,
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(encoded, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Messages[0], want) {
		t.Fatalf("exported record=%#v, want %#v", got.Messages[0], want)
	}
	after, err := s.store.ListSMSMessages(ctx, store.SMSFilter{ModemIMEI: "imei-a", Since: stamp, Until: stamp.Add(time.Second)})
	if err != nil || len(after) != 1 || !reflect.DeepEqual(after[0], saved) {
		t.Fatalf("export changed stored message: %#v, %v", after, err)
	}
}

func assertSMSExportHeaders(t *testing.T, response *httptest.ResponseRecorder, format, count string) {
	t.Helper()
	contentType := "application/json; charset=utf-8"
	if format == "html" {
		contentType = "text/html; charset=utf-8"
	}
	h := response.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("Content-Type") != contentType || h.Get("X-SMS-Export-Count") != count || h.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="vocat-sms-`) || !strings.HasSuffix(h.Get("Content-Disposition"), "."+format+`"`) {
		t.Fatalf("download headers = %#v", h)
	}
}

func TestSMSExportEmptyJSONAndAllDevices(t *testing.T) {
	s := newSMSExportTestServer(t)
	for _, count := range []int{0, 2} {
		if count > 0 {
			for _, id := range []string{"a", "b"} {
				saveSMSExportTestMessage(t, s, store.SMSMessage{DeviceID: id})
			}
		}
		response := httptest.NewRecorder()
		s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?device_id=all", nil))
		assertSMSExportHeaders(t, response, "json", fmt.Sprint(count))
		var got struct {
			Messages []json.RawMessage `json:"messages"`
			Count    int               `json:"message_count"`
			Filters  map[string]string `json:"filters"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Messages == nil || len(got.Messages) != count || got.Count != count || len(got.Filters) != 0 {
			t.Fatalf("archive=%s", response.Body.String())
		}
	}
}

func TestSMSExportHTMLSafeOfflineConversationGroups(t *testing.T) {
	s := newSMSExportTestServer(t)
	peer := `<img src=x onerror="alert(1)"> & peer`
	body := "  </pre><script>alert('body')</script>\n& literal  "
	for i, m := range []store.SMSMessage{
		{DeviceID: "old", ModemIMEI: "imei-a", ICCID: "card-a"},
		{DeviceID: "renamed", ModemIMEI: "imei-a", ICCID: "card-a"},
		{DeviceID: "renamed", ModemIMEI: "imei-a", ICCID: "card-b"},
		{DeviceID: "other", ModemIMEI: "imei-b", ICCID: "card-a"},
		{DeviceID: "legacy", IMSI: "imsi-a"},
		{DeviceID: "legacy", IMSI: "imsi-b"},
		{DeviceID: "legacy"},
	} {
		m.MessageID = fmt.Sprint(i)
		m.Peer = peer
		m.Body = body
		saveSMSExportTestMessage(t, s, m)
	}
	response := httptest.NewRecorder()
	s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?format=html", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertSMSExportHeaders(t, response, "html", "7")
	output := response.Body.String()
	if strings.Count(output, `<section class="sms-conversation"`) != 6 || strings.Count(output, `<article class="sms-message `) != 7 {
		t.Fatal("unexpected conversation grouping")
	}
	if !strings.Contains(output, "<h2>"+html.EscapeString(peer)+"</h2>") || strings.Count(output, `<pre class="message-bubble"><span>`+html.EscapeString(body)+"</span></pre>") != 7 {
		t.Fatal("text was not preserved/escaped")
	}
	trustedScript := "<script>" + smsExportScript + "</script>"
	if strings.Count(output, trustedScript) != 1 {
		t.Fatal("archive must contain the exact compiled-in reader script once")
	}
	untrustedOutput := strings.Replace(output, trustedScript, "", 1)
	for _, forbidden := range []string{"<script", "<img", "<iframe", "<link", "<form", "@import", "url("} {
		if strings.Contains(strings.ToLower(untrustedOutput), forbidden) {
			t.Fatalf("active/external content %q in archive", forbidden)
		}
	}
	if !strings.Contains(html.UnescapeString(output), smsExportHTMLPolicy) || response.Header().Get("Content-Security-Policy") != "sandbox allow-scripts; "+smsExportHTMLPolicy {
		t.Fatal("missing or inconsistent offline CSP")
	}
}

func TestSMSExportRejectsInvalidRequests(t *testing.T) {
	s := newSMSExportTestServer(t)
	cases := []struct {
		method, query string
		status        int
	}{
		{"POST", "", 405}, {"HEAD", "", 405}, {"DELETE", "", 405},
		{"GET", "format=csv", 400}, {"GET", "format=JSON", 400}, {"GET", "limit=1", 400}, {"GET", "before_id=3", 400}, {"GET", "peer=a", 400}, {"GET", "imsi=a", 400},
		{"GET", "device_id=%ZZ", 400}, {"GET", "device_id=a;ignored=b", 400},
		{"GET", "format=json&format=html", 400}, {"GET", "device_id=a&device_id=b", 400}, {"GET", "since=&since=", 400}, {"GET", "until=&until=", 400},
		{"GET", "since=2026-01-01", 400}, {"GET", "until=bad", 400}, {"GET", "since=2026-01-01T00:00:00.1Z", 400}, {"GET", "since=0001-01-01T00:00:00Z", 400},
		{"GET", "since=2026-01-02T00:00:00Z&until=2026-01-01T00:00:00Z", 400}, {"GET", "since=2026-01-01T00:00:00Z&until=2026-01-01T00:00:00Z", 400},
	}
	for _, tc := range cases {
		t.Run(tc.method+"/"+tc.query, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.handleSMSExport(response, httptest.NewRequest(tc.method, "/api/sms/export?"+tc.query, nil))
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Disposition") != "" || response.Header().Get("X-SMS-Export-Count") != "" {
				t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
		})
	}
}

func TestSMSExportAuthenticatedRoute(t *testing.T) {
	app := newTestApplication(t)
	response, err := app.client.Get(app.server.URL + "/api/sms/export")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Content-Disposition") != "" {
		t.Fatalf("unauthenticated status=%d headers=%v", response.StatusCode, response.Header)
	}
	response, err = app.client.Post(app.server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"correct-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d", response.StatusCode)
	}
	response, err = app.client.Get(app.server.URL + "/api/sms/export")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !json.Valid(data) || response.Header.Get("X-SMS-Export-Count") != "0" {
		t.Fatalf("authenticated status=%d body=%s", response.StatusCode, data)
	}
}

type smsExportFailWriter struct {
	remaining int
	err       error
}

func (w *smsExportFailWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, w.err
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestSMSExportWriteFailures(t *testing.T) {
	s := newSMSExportTestServer(t)
	saveSMSExportTestMessage(t, s, store.SMSMessage{Body: strings.Repeat("message", 200)})
	metadata := smsExportMetadata{FormatVersion: 1, ExportedAt: time.Unix(1700000000, 0).UTC()}
	sentinel := errors.New("disk full")
	for _, format := range []string{"json", "html"} {
		t.Run(format, func(t *testing.T) {
			var complete bytes.Buffer
			if _, err := s.writeSMSExport(context.Background(), &complete, format, store.SMSFilter{}, metadata); err != nil {
				t.Fatal(err)
			}
			for _, limit := range []int{0, complete.Len() / 2, complete.Len() - 1} {
				_, err := s.writeSMSExport(context.Background(), &smsExportFailWriter{remaining: limit, err: sentinel}, format, store.SMSFilter{}, metadata)
				if !errors.Is(err, sentinel) {
					t.Fatalf("write limit=%d error=%v", limit, err)
				}
			}
		})
	}
}

func TestSMSExportStagingFailuresDoNotReturnAttachment(t *testing.T) {
	for _, failure := range []string{"database", "temporary file"} {
		t.Run(failure, func(t *testing.T) {
			s := newSMSExportTestServer(t)
			if failure == "database" {
				if err := s.store.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				missing := filepath.Join(t.TempDir(), "missing")
				for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
					t.Setenv(key, missing)
				}
			}
			for _, format := range []string{"json", "html"} {
				response := httptest.NewRecorder()
				s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?format="+format, nil))
				if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Disposition") != "" || response.Header().Get("X-SMS-Export-Count") != "" || strings.Contains(response.Body.String(), `"message_count"`) || strings.Contains(response.Body.String(), "</html>") {
					t.Fatalf("failure returned success: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
				}
				if !strings.Contains(response.Body.String(), "sms_export_failed") {
					t.Fatalf("missing error: %s", response.Body.String())
				}
			}
		})
	}
}
