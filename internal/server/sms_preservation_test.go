package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vocat/internal/store"
)

func TestSyncModemSMSArchivesFailuresAndEmptyBodies(t *testing.T) {
	for _, test := range []struct{ name, body, decodeError string }{
		{"decode failure", "", "SMS user data is truncated"},
		{"partial decode", "partial text", "SMS user data header is truncated"},
		{"silent empty body", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := multipartSegment(7, 1, test.body, nil)
			message.Concat = nil
			message.RawPDU, message.RawUserData, message.DecodeError = "AABB", "BB", test.decodeError
			fixture := newMultipartSyncFixture(t, message)
			if deleted := fixture.scan(); len(deleted) != 0 {
				t.Fatalf("unreadable message deleted: %v", deleted)
			}
			raw, err := fixture.database.ListSMSRawRecords(fixture.ctx, multipartDeviceID, 10)
			if err != nil || len(raw) != 1 || raw[0].RawPDU != "AABB" || raw[0].RawUserData != "BB" || raw[0].DecodeError != test.decodeError {
				t.Fatalf("raw archive = %#v, %v", raw, err)
			}
			stored := fixture.stored()
			if len(stored) != 1 || smsMessageReadyToNotify(stored[0]) {
				t.Fatalf("bad SMS should be visible but not notified: %#v", stored)
			}
			response := storedSMSResponse(stored[0])
			wantStatus := "empty"
			if test.decodeError != "" {
				wantStatus = "failed"
			}
			if response["decode_status"] != wantStatus || response["decode_error"] != test.decodeError {
				t.Fatalf("parser status = %#v", response)
			}
			for _, payload := range []any{response, smsExportRecord(stored[0])} {
				encoded, err := json.Marshal(payload)
				if err != nil || strings.Contains(string(encoded), "AABB") || strings.Contains(string(encoded), "raw_pdu") {
					t.Fatalf("raw PDU leaked into inbox/export: %s, %v", encoded, err)
				}
			}
			fixture.scan()
			raw, err = fixture.database.ListSMSRawRecords(fixture.ctx, multipartDeviceID, 10)
			if err != nil || len(raw) != 1 {
				t.Fatalf("rescan duplicated archive: %#v, %v", raw, err)
			}
		})
	}
}

func TestSyncModemSMSArchiveAndInboxFailuresKeepModemCopy(t *testing.T) {
	for _, table := range []string{"sms_raw_records", "sms_messages"} {
		t.Run(table, func(t *testing.T) {
			message := multipartSegment(7, 1, "OTP test", nil)
			message.Concat = nil
			fixture := newMultipartSyncFixture(t, message)
			path := filepath.Join(t.TempDir(), "fail.db")
			db, err := store.Open(fixture.ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.UpsertDevice(fixture.ctx, store.Device{ID: multipartDeviceID, Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25}); err != nil {
				t.Fatal(err)
			}
			connection, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = connection.Exec(`CREATE TRIGGER fail_save BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(FAIL, 'forced save failure'); END`)
			_ = connection.Close()
			if err != nil {
				t.Fatal(err)
			}
			fixture.database, fixture.server.store = db, db
			if deleted := fixture.scan(); len(deleted) != 0 {
				t.Fatalf("save failed but modem copy deleted: %v", deleted)
			}
			raw, err := db.ListSMSRawRecords(fixture.ctx, multipartDeviceID, 10)
			if err != nil {
				t.Fatal(err)
			}
			if table == "sms_messages" && len(raw) != 1 {
				t.Fatalf("inbox failure lost raw record: %#v", raw)
			}
			if table == "sms_raw_records" && len(fixture.stored()) != 1 {
				t.Fatal("archive failure should still expose the decoded inbox record")
			}
		})
	}
}

func TestSyncModemSMSArchivesEveryMultipartSegment(t *testing.T) {
	first := multipartSegment(3, 1, "first ", nil)
	second := multipartSegment(4, 2, "second", nil)
	fixture := newMultipartSyncFixture(t, first, second)
	if deleted := fixture.scan(); len(deleted) != 2 {
		t.Fatalf("complete archived multipart not cleared: %v", deleted)
	}
	raw, err := fixture.database.ListSMSRawRecords(fixture.ctx, multipartDeviceID, 10)
	if err != nil || len(raw) != 2 || len(fixture.stored()) != 1 {
		t.Fatalf("multipart archive = %#v, %v", raw, err)
	}
}

func TestSyncModemSMSKeepsMultipartWithDecodeFailure(t *testing.T) {
	first := multipartSegment(3, 1, "", nil)
	first.DecodeError = "truncated"
	second := multipartSegment(4, 2, "second", nil)
	fixture := newMultipartSyncFixture(t, first, second)
	if deleted := fixture.scan(); len(deleted) != 0 {
		t.Fatalf("damaged multipart deleted: %v", deleted)
	}
	stored := fixture.stored()
	if len(stored) != 1 || store.SMSDecodeError(stored[0].Extra) != "part 1: truncated" || smsMessageReadyToNotify(stored[0]) {
		t.Fatalf("damaged multipart status lost: %#v", stored)
	}
}

func TestSMSDiagnosticsRequiresAuthentication(t *testing.T) {
	app := newTestApplication(t)
	server := app.server.Config.Handler.(*Server)
	if err := server.store.SaveSMSRawRecord(context.Background(), store.SMSRawRecord{
		DeviceID: "ec20", Storage: "ME", Slot: 1, RawPDU: "AABB", DecodeError: "truncated",
	}); err != nil {
		t.Fatal(err)
	}
	response, err := app.client.Get(app.server.URL + "/api/sms/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || strings.Contains(string(unauthenticated), "AABB") {
		t.Fatalf("unauthenticated diagnostic response: %d %s", response.StatusCode, unauthenticated)
	}
	response, err = app.client.Post(app.server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"correct-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = app.client.Get(app.server.URL + "/api/sms/diagnostics?device_id=ec20&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Data []store.SMSRawRecord `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || len(result.Data) != 1 || result.Data[0].RawPDU != "AABB" {
		t.Fatalf("authenticated diagnostics: status=%d data=%#v", response.StatusCode, result.Data)
	}
	filtered := httptest.NewRecorder()
	server.handleSMSDiagnostics(filtered, httptest.NewRequest(http.MethodGet, "/api/sms/diagnostics?device_id=other", nil))
	if strings.Contains(filtered.Body.String(), "AABB") {
		t.Fatal("device filter leaked another device's raw record")
	}
}
