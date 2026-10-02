package server

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

func TestSMSExportHTMLTimezones(t *testing.T) {
	s := newSMSExportTestServer(t)
	stamps := []string{"2026-03-08T06:59:59Z", "2026-03-08T07:00:00Z", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"}
	for _, stamp := range stamps {
		parsed, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			t.Fatal(err)
		}
		saveSMSExportTestMessage(t, s, store.SMSMessage{MessageID: stamp, Timestamp: parsed})
	}
	for _, tc := range []struct {
		zone, label string
		want        []string
	}{
		{"Asia/Shanghai", "Asia/Shanghai", []string{"2026-03-08T14:59:59+08:00", "2026-03-08T15:00:00+08:00", "2026-11-01T13:30:00+08:00", "2026-11-01T14:30:00+08:00"}},
		{"America/New_York", "America/New_York", []string{"2026-03-08T01:59:59-05:00", "2026-03-08T03:00:00-04:00", "2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00"}},
		{"Asia/Kathmandu", "Asia/Kathmandu", []string{"2026-03-08T12:44:59+05:45", "2026-03-08T12:45:00+05:45"}},
		{"UTC", "UTC", stamps},
		{"", "UTC", stamps},
	} {
		t.Run(tc.label+tc.zone, func(t *testing.T) {
			query := url.Values{"format": {"html"}}
			if tc.zone != "" {
				query.Set("timezone", tc.zone)
			}
			response := httptest.NewRecorder()
			s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?"+query.Encode(), nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			body := html.UnescapeString(response.Body.String())
			if !strings.Contains(body, "Display timezone: "+tc.label) {
				t.Fatalf("missing timezone: %s", body)
			}
			for _, want := range tc.want {
				stamp, err := time.Parse(time.RFC3339, want)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(body, `<time datetime="`+want+`">`+stamp.Format("2006/01/02 15:04:05 Z07:00")+"</time>") {
					t.Errorf("missing message timestamp %s", want)
				}
			}
			assertSMSExportHeaders(t, response, "html", "4")
		})
	}
}

func TestSMSExportHTMLHeaderUsesDisplayTimezone(t *testing.T) {
	s := newSMSExportTestServer(t)
	query := url.Values{"format": {"html"}, "timezone": {"Asia/Shanghai"}, "since": {"2026-01-01T16:00:00Z"}, "until": {"2026-01-02T16:00:00Z"}}
	response := httptest.NewRecorder()
	s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?"+query.Encode(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := html.UnescapeString(response.Body.String())
	for _, want := range []string{"Since (inclusive): 2026-01-02T00:00:00+08:00", "Until (exclusive): 2026-01-03T00:00:00+08:00", "+08:00 · 格式版本", "Display timezone: Asia/Shanghai"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

func TestSMSExportRejectsInvalidTimezone(t *testing.T) {
	s := newSMSExportTestServer(t)
	for _, query := range []string{
		"format=html&timezone=", "format=html&timezone=Local", "format=html&timezone=Not/AZone",
		"format=html&timezone=../UTC", "format=html&timezone=UTC&timezone=Asia%2FShanghai",
		"format=json&timezone=Asia%2FShanghai", "timezone=UTC",
	} {
		response := httptest.NewRecorder()
		s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?"+query, nil))
		if response.Code != http.StatusBadRequest || response.Header().Get("Content-Disposition") != "" {
			t.Errorf("query=%q status=%d body=%s", query, response.Code, response.Body.String())
		}
	}
}

func TestSMSExportJSONTimestampsStayUTC(t *testing.T) {
	s := newSMSExportTestServer(t)
	stamp := time.Date(2026, 1, 2, 11, 4, 5, 0, time.FixedZone("browser", 8*60*60))
	m := store.SMSMessage{Timestamp: stamp, CreatedAt: stamp, UpdatedAt: stamp}
	for key, value := range smsExportRecord(m) {
		if value, ok := value.(time.Time); ok && (value.Location() != time.UTC || !value.Equal(stamp)) {
			t.Errorf("%s not normalized to UTC: %v", key, value)
		}
	}
	saveSMSExportTestMessage(t, s, m)
	query := url.Values{"since": {stamp.Add(-time.Second).Format(time.RFC3339)}, "until": {stamp.Add(time.Second).Format(time.RFC3339)}}
	response := httptest.NewRecorder()
	s.handleSMSExport(response, httptest.NewRequest(http.MethodGet, "/api/sms/export?"+query.Encode(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var archive struct {
		ExportedAt string            `json:"exported_at"`
		Filters    map[string]string `json:"filters"`
		Messages   []map[string]any  `json:"messages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &archive); err != nil {
		t.Fatal(err)
	}
	if len(archive.Messages) != 1 {
		t.Fatalf("messages=%v", archive.Messages)
	}
	for _, value := range []string{archive.ExportedAt, archive.Filters["since"], archive.Filters["until"]} {
		if !strings.HasSuffix(value, "Z") {
			t.Errorf("metadata not UTC: %s", value)
		}
	}
	for _, key := range []string{"timestamp", "created_at", "updated_at"} {
		value, ok := archive.Messages[0][key].(string)
		if !ok || !strings.HasSuffix(value, "Z") {
			t.Errorf("%s not UTC: %v", key, archive.Messages[0][key])
		}
	}
	if strings.Contains(response.Body.String(), "HTMLLocation") || strings.Contains(response.Body.String(), "timezone") {
		t.Fatal("HTML presentation metadata leaked into JSON")
	}
}
