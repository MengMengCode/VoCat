package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxPluginUploadBytes int64 = 64 << 20

func (s *Server) routeExtensionAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	if cleanPath == "extensions" {
		if s.extensions == nil {
			writeError(w, http.StatusServiceUnavailable, "extensions_unavailable", "plugin manager is unavailable")
			return true
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": s.extensions.List()})
		default:
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return true
	}
	if cleanPath == "extensions/install-url" {
		if s.extensions == nil {
			writeError(w, http.StatusServiceUnavailable, "extensions_unavailable", "plugin manager is unavailable")
			return true
		}
		if !requireMethod(w, r, http.MethodPost) {
			return true
		}
		var request struct {
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		ctx, cancel := contextWithTimeout(r, 60*time.Second)
		defer cancel()
		plugin, err := s.extensions.InstallURL(ctx, request.URL, request.SHA256)
		if err != nil {
			writeError(w, http.StatusBadRequest, "plugin_install_failed", err.Error())
			return true
		}
		s.recordAudit(r.Context(), "admin", "plugin.install_url", "plugin", plugin.ID, "success", request.URL)
		writeJSON(w, http.StatusCreated, map[string]any{"data": plugin})
		return true
	}
	if cleanPath == "extensions/upload" {
		if s.extensions == nil {
			writeError(w, http.StatusServiceUnavailable, "extensions_unavailable", "plugin manager is unavailable")
			return true
		}
		if !requireMethod(w, r, http.MethodPost) {
			return true
		}
		r.Body = http.MaxBytesReader(nil, r.Body, maxPluginUploadBytes+(1<<20))
		if err := r.ParseMultipartForm(maxPluginUploadBytes); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_plugin_upload", "plugin upload must be multipart/form-data and no larger than 64 MiB")
			return true
		}
		file, _, err := r.FormFile("package")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_plugin_upload", "multipart field package is required")
			return true
		}
		defer file.Close()
		plugin, err := s.extensions.Install(io.LimitReader(file, maxPluginUploadBytes+1), r.FormValue("sha256"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "plugin_install_failed", err.Error())
			return true
		}
		s.recordAudit(r.Context(), "admin", "plugin.upload", "plugin", plugin.ID, "success", "upload")
		writeJSON(w, http.StatusCreated, map[string]any{"data": plugin})
		return true
	}

	segments := splitAPIPath(cleanPath)
	if len(segments) < 2 || segments[0] != "extensions" {
		return false
	}
	if s.extensions == nil {
		writeError(w, http.StatusServiceUnavailable, "extensions_unavailable", "plugin manager is unavailable")
		return true
	}
	id := segments[1]
	if len(segments) >= 3 && segments[2] == "backend" {
		s.extensions.ProxyBackend(w, r, id)
		return true
	}
	if len(segments) != 2 {
		writeError(w, http.StatusNotFound, "not_found", "plugin endpoint not found")
		return true
	}
	switch r.Method {
	case http.MethodPut:
		var request struct {
			Enabled bool `json:"enabled"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		plugin, err := s.extensions.SetEnabled(id, request.Enabled)
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "plugin_not_found", "plugin not found")
			return true
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "plugin_state_failed", err.Error())
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": plugin})
	case http.MethodDelete:
		if err := s.extensions.Uninstall(id); errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "plugin_not_found", "plugin not found")
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "plugin_uninstall_failed", err.Error())
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"uninstalled": true}})
		}
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

func (s *Server) handlePluginAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !s.requireAuthenticated(w, r) {
		return
	}
	if s.extensions == nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/plugin-assets/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	s.extensions.ServeAsset(w, r, parts[0], parts[1])
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
