package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
	_ "time/tzdata" // HTML exports must work on hosts without system timezone data.

	"vocat/internal/store"
)

const smsExportVersion = 1

type smsExportScope struct {
	DeviceID  string `json:"device_id,omitempty"`
	ModemIMEI string `json:"modem_imei,omitempty"`
	Since     string `json:"since,omitempty"`
	Until     string `json:"until,omitempty"`
}

type smsExportMetadata struct {
	FormatVersion int            `json:"format_version"`
	ExportedAt    time.Time      `json:"exported_at"`
	Filters       smsExportScope `json:"filters"`
	HTMLLocation  *time.Location `json:"-"`
}

// Keep the archive schema independent of both Go field names and the UI's
// decoded/truncated message representation. Body is the exact stored text.
func smsExportRecord(m store.SMSMessage) map[string]any {
	return map[string]any{
		"id": m.ID, "message_id": m.MessageID, "device_id": m.DeviceID,
		"modem_imei": m.ModemIMEI, "iccid": m.ICCID, "imsi": m.IMSI,
		"local_phone": m.LocalPhone, "peer": m.Peer, "direction": m.Direction,
		"body": m.Body, "timestamp": m.Timestamp.UTC(), "status": m.Status,
		"source": m.Source, "parts_total": m.PartsTotal,
		"delivery_state": m.DeliveryState, "read": m.Read, "extra": m.Extra,
		"created_at": m.CreatedAt.UTC(), "updated_at": m.UpdatedAt.UTC(),
	}
}

func (s *Server) handleSMSExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_export_filter", "invalid export query encoding")
		return
	}
	format := query.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "html" {
		writeError(w, http.StatusBadRequest, "invalid_export_format", "export format must be json or html")
		return
	}
	// Reject unsupported or repeated filters rather than silently exporting a
	// broader set of private messages than the caller requested.
	for key, values := range query {
		if (key != "format" && key != "device_id" && key != "since" && key != "until" && !(key == "timezone" && format == "html")) || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_export_filter", "unsupported or repeated export filter")
			return
		}
	}
	location := time.UTC
	if values, ok := query["timezone"]; ok {
		zone := values[0]
		if zone == "" || zone == "Local" {
			writeError(w, http.StatusBadRequest, "invalid_export_timezone", "timezone must be an IANA timezone name")
			return
		}
		location, err = time.LoadLocation(zone)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_export_timezone", "timezone must be an IANA timezone name")
			return
		}
	}
	since, err := parseSMSExportTime(query.Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_export_range", "since must be an RFC3339 timestamp with whole seconds")
		return
	}
	until, err := parseSMSExportTime(query.Get("until"))
	if err != nil || (!since.IsZero() && !until.IsZero() && !since.Before(until)) {
		writeError(w, http.StatusBadRequest, "invalid_export_range", "until must be an RFC3339 timestamp with whole seconds, later than since")
		return
	}
	deviceID := normalizeSMSDeviceFilter(query.Get("device_id"))
	filter := s.smsStoreFilter(r.Context(), deviceID, "")
	filter.Since, filter.Until = since, until
	metadata := smsExportMetadata{
		FormatVersion: smsExportVersion, ExportedAt: time.Now().UTC(), HTMLLocation: location,
		Filters: smsExportScope{DeviceID: deviceID, ModemIMEI: filter.ModemIMEI},
	}
	if !since.IsZero() {
		metadata.Filters.Since = since.Format(time.RFC3339)
	}
	if !until.IsZero() {
		metadata.Filters.Until = until.Format(time.RFC3339)
	}

	// Stage a private temporary file before sending a success response. This
	// bounds server memory, releases SQLite before a slow download, and keeps a
	// query/disk/encoding failure from masquerading as a completed archive.
	file, err := os.CreateTemp("", "vocat-sms-export-*")
	if err != nil {
		s.smsExportError(w, err)
		return
	}
	defer func() {
		file.Close()
		os.Remove(file.Name())
	}()
	buffer := bufio.NewWriter(file)
	count, err := s.writeSMSExport(r.Context(), buffer, format, filter, metadata)
	if err == nil {
		err = buffer.Flush()
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		if r.Context().Err() == nil {
			s.smsExportError(w, err)
		}
		return
	}
	if r.Context().Err() != nil {
		return
	}
	name := "vocat-sms-" + metadata.ExportedAt.Format("20060102T150405Z") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-SMS-Export-Count", fmt.Sprint(count))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if format == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; "+smsExportHTMLPolicy)
	}
	http.ServeContent(w, r, name, time.Time{}, file)
}

func parseSMSExportTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Nanosecond() != 0 || parsed.IsZero() {
		return time.Time{}, fmt.Errorf("invalid SMS export timestamp")
	}
	return parsed.UTC(), nil
}

func (s *Server) smsExportError(w http.ResponseWriter, err error) {
	s.logger.Error("SMS export failed", "category", "system", "event", "sms.export_failed", "error", err)
	writeError(w, http.StatusInternalServerError, "sms_export_failed", "SMS export failed; no complete archive was generated")
}

func (s *Server) writeSMSExport(ctx context.Context, w io.Writer, format string, filter store.SMSFilter, metadata smsExportMetadata) (int, error) {
	count := 0
	encoder := json.NewEncoder(w)
	if format == "json" {
		header, err := json.Marshal(metadata)
		if err != nil {
			return 0, err
		}
		if _, err := fmt.Fprintf(w, "%s,\n\"messages\":[\n", header[:len(header)-1]); err != nil {
			return 0, err
		}
	} else {
		if metadata.HTMLLocation == nil {
			metadata.HTMLLocation = time.UTC
		}
		metadata.ExportedAt = metadata.ExportedAt.In(metadata.HTMLLocation)
		for _, value := range []*string{&metadata.Filters.Since, &metadata.Filters.Until} {
			if *value != "" {
				stamp, err := parseSMSExportTime(*value)
				if err != nil {
					return 0, err
				}
				*value = stamp.In(metadata.HTMLLocation).Format(time.RFC3339)
			}
		}
		if err := smsExportHTML.ExecuteTemplate(w, "header", metadata); err != nil {
			return 0, err
		}
	}
	var previous [3]string
	err := s.store.ExportSMSMessages(ctx, filter, func(m store.SMSMessage) error {
		if format == "json" {
			if count > 0 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if err := encoder.Encode(smsExportRecord(m)); err != nil {
				return err
			}
		} else {
			key := smsExportConversation(m)
			if count == 0 || key != previous {
				if count > 0 {
					if err := smsExportHTML.ExecuteTemplate(w, "conversationEnd", nil); err != nil {
						return err
					}
				}
				if err := smsExportHTML.ExecuteTemplate(w, "conversation", m); err != nil {
					return err
				}
				previous = key
			}
			m.Timestamp = m.Timestamp.In(metadata.HTMLLocation)
			if err := smsExportHTML.ExecuteTemplate(w, "message", m); err != nil {
				return err
			}
		}
		count++
		return nil
	})
	if err != nil {
		return count, err
	}
	if format == "json" {
		_, err = fmt.Fprintf(w, "],\n\"message_count\":%d}\n", count)
	} else {
		if count > 0 {
			if err = smsExportHTML.ExecuteTemplate(w, "conversationEnd", nil); err != nil {
				return count, err
			}
		}
		err = smsExportHTML.ExecuteTemplate(w, "footer", count)
	}
	return count, err
}

func smsExportConversation(m store.SMSMessage) [3]string {
	hardware := m.ModemIMEI
	if hardware == "" {
		hardware = "device:" + m.DeviceID
	}
	subscription := m.ICCID
	if subscription == "" {
		subscription = "unknown"
		if m.IMSI != "" {
			subscription = "imsi:" + m.IMSI
		}
	}
	return [3]string{hardware, subscription, m.Peer}
}
