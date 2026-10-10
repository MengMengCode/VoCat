package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"vocat/internal/store"
)

const loggingSettingKey = "logs.retention"

// loggingConfig is the persisted log retention policy.
type loggingConfig struct {
	Mode  string `json:"mode"`  // "unlimited" (default) | "count" | "days"
	Count int    `json:"count"` // keep newest N entries when mode is "count"
	Days  int    `json:"days"`  // keep entries from the last N days when mode is "days"
	Level string `json:"level"` // "debug" (default) | "info" | "warn" | "error"
}

func defaultLoggingConfig() loggingConfig {
	return loggingConfig{Mode: "unlimited", Count: 10000, Days: 30, Level: "debug"}
}

func parseLoggingConfig(config loggingConfig) (loggingConfig, error) {
	mode := strings.ToLower(strings.TrimSpace(config.Mode))
	if mode == "" {
		mode = "unlimited"
	}
	if mode != "unlimited" && mode != "count" && mode != "days" {
		return loggingConfig{}, errors.New("mode must be \"unlimited\", \"count\", or \"days\"")
	}
	config.Mode = mode
	if config.Count < 1 {
		config.Count = 10000
	}
	if config.Count > store.MaxLogEvents {
		config.Count = store.MaxLogEvents
	}
	if config.Days < 1 {
		config.Days = 30
	}
	level := strings.ToLower(strings.TrimSpace(config.Level))
	if level == "" || level == "all" {
		level = "debug"
	}
	if level != "debug" && level != "info" && level != "warn" && level != "warning" && level != "error" {
		return loggingConfig{}, errors.New("level must be \"debug\", \"info\", \"warn\", or \"error\"")
	}
	if level == "warning" {
		level = "warn"
	}
	config.Level = level
	return config, nil
}

// loadLoggingConfig reads the persisted retention policy. "unlimited" means
// no user-selected limit below the global 10,000-row hard ceiling.
func (s *Server) loadLoggingConfig(ctx context.Context) loggingConfig {
	config := defaultLoggingConfig()
	setting, err := s.store.AppSetting(ctx, loggingSettingKey)
	if err == nil {
		var stored loggingConfig
		if json.Unmarshal(setting.Value, &stored) == nil {
			if parsed, parseErr := parseLoggingConfig(stored); parseErr == nil {
				config = parsed
			}
		}
	}
	if s.store != nil {
		s.store.SetLogMinLevel(config.Level)
	}
	return config
}

// applyLogRetention enforces the current retention policy against the persisted
// log events. The "unlimited" mode prunes nothing.
func (s *Server) applyLogRetention(ctx context.Context) error {
	config := s.loadLoggingConfig(ctx)
	if _, err := s.store.PruneLogEventsBelowLevel(ctx, config.Level); err != nil {
		return err
	}
	switch config.Mode {
	case "days":
		cutoff := time.Now().UTC().Add(-time.Duration(config.Days) * 24 * time.Hour)
		if _, err := s.store.PruneLogEvents(ctx, cutoff); err != nil {
			return err
		}
		_, err := s.store.PruneLogEventsToCount(ctx, store.MaxLogEvents)
		return err
	case "count":
		_, err := s.store.PruneLogEventsToCount(ctx, config.Count)
		return err
	default:
		_, err := s.store.PruneLogEventsToCount(ctx, store.MaxLogEvents)
		return err
	}
}

// StartLogRetentionLoop enforces the retention policy once at startup and then
// on the given interval until the context is cancelled.
func (s *Server) StartLogRetentionLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	if err := s.applyLogRetention(ctx); err != nil {
		s.logger.Warn("apply log retention failed", "error", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.applyLogRetention(ctx); err != nil {
				s.logger.Warn("apply log retention failed", "error", err)
			}
		}
	}
}

// handleLoggingSettings reads and writes the log retention policy.
//
//	GET /api/settings/logging
//	PUT /api/settings/logging
func (s *Server) handleLoggingSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		config := s.loadLoggingConfig(r.Context())
		stored, err := s.store.CountLogEvents(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"mode":        config.Mode,
				"count":       config.Count,
				"days":        config.Days,
				"level":       config.Level,
				"stored_logs": stored,
				"max_logs":    store.MaxLogEvents,
			},
		})
	case http.MethodPut:
		var request loggingConfig
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		config, err := parseLoggingConfig(request)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_logging_policy", err.Error())
			return
		}
		payload, err := json.Marshal(config)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			return
		}
		if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
			Key:   loggingSettingKey,
			Value: payload,
		}); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.audit(r, "settings.logging.update", "settings", "logging", "success")
		if err := s.applyLogRetention(r.Context()); err != nil {
			s.logger.Warn("apply log retention failed", "error", err)
		}
		stored, _ := s.store.CountLogEvents(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"mode":        config.Mode,
				"count":       config.Count,
				"days":        config.Days,
				"level":       config.Level,
				"stored_logs": stored,
				"max_logs":    store.MaxLogEvents,
			},
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
