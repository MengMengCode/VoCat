package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"vocat/internal/auth"
	"vocat/internal/buildinfo"
	"vocat/internal/developer"
	"vocat/internal/i18n"
	"vocat/internal/loghub"
	"vocat/internal/store"
	"vocat/internal/update"
)

func (s *Server) routeGeneralAPI(w http.ResponseWriter, r *http.Request) bool {
	cleanPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/")
	if s.routeAutomaticTasksAPI(w, r, cleanPath) {
		return true
	}
	if s.routeExtensionAPI(w, r, cleanPath) {
		return true
	}
	if s.routeExportProxyAPI(w, r, cleanPath) {
		return true
	}
	if s.routeSMSAPI(w, r, cleanPath) {
		return true
	}
	if s.routeProxyAPI(w, r, cleanPath) {
		return true
	}
	if s.routeSettingsAPI(w, r, cleanPath) {
		return true
	}
	switch cleanPath {
	case "logs/history":
		s.handleLogHistory(w, r)
	case "logs/stream":
		s.handleLogStream(w, r)
	case "system/info":
		s.handleSystemInfo(w, r)
	case "system/update/check":
		s.handleUpdateCheck(w, r)
	case "system/update/apply":
		s.handleUpdateApply(w, r)
	case "settings/password":
		s.handlePasswordChange(w, r)
	case "settings/preferences":
		s.handleUIPreferences(w, r)
	case "settings/https":
		s.handleHTTPSSettings(w, r)
	case "settings/https/certificate":
		s.handleHTTPSCertificate(w, r)
	case "settings/developer":
		s.handleDeveloperSettings(w, r)
	default:
		return false
	}
	return true
}

const uiPreferencesSettingKey = "ui.preferences"

// loadUILanguage primes the process-level UI language (internal/i18n) from the
// persisted preference so backend-generated strings are translated correctly
// even before the first preferences request arrives after a restart.
func (s *Server) loadUILanguage(ctx context.Context) {
	setting, err := s.store.AppSetting(ctx, uiPreferencesSettingKey)
	if err != nil {
		return
	}
	var document struct {
		Language string `json:"language"`
	}
	if json.Unmarshal(setting.Value, &document) == nil {
		i18n.Set(document.Language)
	}
}

// handleUIPreferences reads and writes UI preferences such as the interface
// language. Preferences live in the database so they stay consistent across
// the browsers and devices of the single administrator.
func (s *Server) handleUIPreferences(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeUIPreferences(w, r)
	case http.MethodPut:
		var request struct {
			Language string `json:"language"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		language := strings.ToLower(strings.TrimSpace(request.Language))
		if language != "en" && language != "zh" {
			writeError(w, http.StatusBadRequest, "invalid_language", "language must be \"en\" or \"zh\"")
			return
		}
		raw, err := json.Marshal(map[string]string{"language": language})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			return
		}
		if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
			Key:   uiPreferencesSettingKey,
			Value: raw,
		}); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.writeUIPreferences(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) writeUIPreferences(w http.ResponseWriter, r *http.Request) {
	language := "en"
	setting, err := s.store.AppSetting(r.Context(), uiPreferencesSettingKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		s.writeStoreError(w, err)
		return
	default:
		var document struct {
			Language string `json:"language"`
		}
		if json.Unmarshal(setting.Value, &document) == nil &&
			(document.Language == "en" || document.Language == "zh") {
			language = document.Language
		}
	}
	// Keep the process-level UI language in sync so backend-generated strings
	// (status text, errors, hints) translate to match the SPA.
	i18n.Set(language)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"language": language},
	})
}

func (s *Server) handleLogHistory(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("lines"))
	if err != nil || limit < 1 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	minimum := logLevel(r.URL.Query().Get("level"))
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))

	// History is served from the persisted log_events table so it reflects the
	// configured retention policy and survives restarts (the in-memory hub only
	// backs the live stream).
	entries := []loghub.Entry{}
	if s.store != nil {
		events, err := s.store.ListLogEvents(r.Context(), store.LogFilter{Limit: limit})
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		for _, event := range events {
			if storedLogLevel(event.Level) < minimum {
				continue
			}
			entry := storedLogToEntry(event)
			if search != "" && !storedLogContains(entry, search) {
				continue
			}
			entries = append(entries, entry)
		}
		// ListLogEvents is newest-first; present chronologically.
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"logs": entries},
	})
}

func storedLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "error":
		return slog.LevelError
	case "warn", "warning":
		return slog.LevelWarn
	case "debug":
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

func storedLogToEntry(event store.LogEvent) loghub.Entry {
	var fields map[string]any
	if len(event.Fields) > 0 {
		if err := json.Unmarshal(event.Fields, &fields); err != nil {
			fields = nil
		}
	}
	return loghub.Entry{
		Time:    event.Time,
		Level:   event.Level,
		Message: event.Message,
		Caller:  event.Caller,
		Fields:  fields,
	}
}

func storedLogContains(entry loghub.Entry, search string) bool {
	if strings.Contains(strings.ToLower(entry.Message), search) ||
		strings.Contains(strings.ToLower(entry.Caller), search) {
		return true
	}
	for key, value := range entry.Fields {
		if strings.Contains(strings.ToLower(key), search) ||
			strings.Contains(strings.ToLower(fmt.Sprint(value)), search) {
			return true
		}
	}
	return false
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if s.logs == nil {
		writeError(w, http.StatusServiceUnavailable, "log_stream_unavailable", "live log stream is unavailable")
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		s.logger.Debug("stream write deadline is controlled by the HTTP server", "error", err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("event: connected\ndata: {}\n\n")); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		return
	}

	minimum := logLevel(r.URL.Query().Get("level"))
	entries, cancel := s.logs.Subscribe(128)
	defer cancel()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		case entry, ok := <-entries:
			if !ok {
				return
			}
			if logLevel(entry.Level) < minimum {
				continue
			}
			if _, err := w.Write([]byte("event: log\ndata: ")); err != nil {
				return
			}
			if err := encoder.Encode(entry); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func logLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "error":
		return slog.LevelError
	case "warn", "warning":
		return slog.LevelWarn
	case "debug":
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"version":      buildinfo.Version,
			"build_time":   buildinfo.BuildTime,
			"config":       "VOCAT_CONFIG and environment",
			"os":           runtime.GOOS,
			"architecture": runtime.GOARCH,
			"uptime":       formatDuration(time.Since(s.startedAt)),
			"developer":    s.developerActive(r.Context()),
		},
	})
}

func (s *Server) developerActive(ctx context.Context) bool {
	return s.developerEnabled && developer.Enabled(ctx, s.store)
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if strings.TrimSpace(s.updateRepository) == "" || s.updateCheck == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"available": false,
				"version":   buildinfo.Version,
				"message":   i18n.T("未配置受信任的软件更新源；不会从未知地址下载或执行文件。"),
			},
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := s.updateCheck(
		ctx,
		s.updateRepository,
		s.updateToken,
		buildinfo.Version,
	)
	if err != nil {
		s.logger.Warn("check for updates failed", "repository", s.updateRepository, "error", err)
		writeError(w, http.StatusBadGateway, "update_check_failed", err.Error())
		return
	}
	message := ""
	if result.Available {
		message = result.ReleaseNotes
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"available":       result.Available,
			"current_version": result.Current,
			"version":         result.Latest,
			"message":         message,
			"repository":      s.updateRepository,
			"is_docker":       runningInDocker(),
		},
	})
}

func runningInDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("VOCAT_CONTAINER")), "docker")
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if strings.TrimSpace(s.updateRepository) == "" || s.updateApply == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"applied": false,
				"message": i18n.T("未配置受信任的软件更新源；未执行任何更新。"),
			},
		})
		return
	}
	if runningInDocker() {
		writeError(w, http.StatusConflict, "container_update_required", "pull the latest container image and recreate the container")
		return
	}
	s.updateMu.Lock()
	if s.updateApplying {
		s.updateMu.Unlock()
		writeError(w, http.StatusConflict, "update_busy", "another update is already in progress")
		return
	}
	s.updateApplying = true
	s.updateMu.Unlock()
	defer func() {
		s.updateMu.Lock()
		s.updateApplying = false
		s.updateMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := s.updateApply(ctx, s.logger, update.Options{
		Repo:  s.updateRepository,
		Token: s.updateToken,
	}, false)
	if err != nil {
		s.logger.Error("apply update failed", "repository", s.updateRepository, "error", err)
		writeError(w, http.StatusBadGateway, "update_apply_failed", err.Error())
		return
	}
	if !result.Applied {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"applied": false,
				"version": result.Latest,
				"message": "The installed version is already current.",
			},
		})
		return
	}
	// A binary update changes the trusted server code underneath every active
	// browser/API session. Revoke every durable token before scheduling the
	// restart and expire this client's cookies so all users must authenticate
	// against the newly installed version.
	if err := s.store.DeleteAllSessions(r.Context()); err != nil {
		s.logger.Error("revoke sessions after update failed", "error", err)
		writeError(w, http.StatusInternalServerError, "update_session_revocation_failed", "The update was installed, but active sessions could not be revoked; restart the service and sign in again.")
		return
	}
	s.clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"applied":                   true,
			"version":                   result.Latest,
			"reauthentication_required": true,
			"message":                   "Update verified and installed; all sessions were revoked and the service is restarting.",
		},
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if s.updateRestart != nil {
		restart := s.updateRestart
		logger := s.logger
		go func() {
			time.Sleep(time.Second)
			if err := restart(logger); err != nil {
				logger.Error("restart after update failed", "error", err)
			}
		}()
	}
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var request struct {
		OldPassword     string `json:"old_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if request.NewPassword != request.ConfirmPassword {
		writeError(w, http.StatusBadRequest, "password_mismatch", "new password and confirmation do not match")
		return
	}
	sessionToken, ok := s.sessionToken(w, r)
	if !ok {
		return
	}
	session, err := s.auth.Authenticate(r.Context(), sessionToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication is required")
		return
	}
	if err := s.auth.ChangePassword(
		r.Context(),
		session.Principal.Username,
		request.OldPassword,
		request.NewPassword,
	); err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		case errors.Is(err, auth.ErrEmptyPassword):
			writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		case strings.Contains(err.Error(), "must differ"):
			writeError(w, http.StatusBadRequest, "password_reused", err.Error())
		default:
			s.logger.Error("password change failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		}
		return
	}
	s.clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"changed": true, "reauthentication_required": true},
	})
}

func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	days := int(duration / (24 * time.Hour))
	duration %= 24 * time.Hour
	hours := int(duration / time.Hour)
	duration %= time.Hour
	minutes := int(duration / time.Minute)
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
