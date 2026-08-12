package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"vocat/internal/exportproxy"
	"vocat/internal/store"
)

func (s *Server) routeExportProxyAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	if cleanPath != "export-proxies" && !strings.HasPrefix(cleanPath, "export-proxies/") {
		return false
	}
	if !s.developerActive(r.Context()) || s.exportProxy == nil {
		writeError(w, http.StatusForbidden, "developer_mode_required", "Export Proxy is available only in developer mode")
		return true
	}

	segments := splitAPIPath(cleanPath)
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			configs, err := s.exportProxy.Configs()
			if err != nil {
				s.writeExportProxyError(w, err)
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"configs": configs}})
		case http.MethodPost:
			var config exportproxy.Config
			if err := s.decodeJSON(w, r, &config); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return true
			}
			if s.rejectUnsupportedExportProxyDevice(w, r.Context(), config.DeviceID) {
				return true
			}
			created, err := s.exportProxy.Create(r.Context(), config)
			if err != nil {
				s.writeExportProxyError(w, err)
				return true
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": created})
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return true
	}

	if len(segments) == 2 && segments[1] == "status" {
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		statuses, err := s.exportProxy.Status()
		if err != nil {
			s.writeExportProxyError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"configs": statuses}})
		return true
	}

	if len(segments) != 2 || strings.TrimSpace(segments[1]) == "" {
		writeError(w, http.StatusNotFound, "not_found", "Export Proxy endpoint not found")
		return true
	}
	id := segments[1]
	switch r.Method {
	case http.MethodPut:
		var config exportproxy.Config
		if err := s.decodeJSON(w, r, &config); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		if s.rejectUnsupportedExportProxyDevice(w, r.Context(), config.DeviceID) {
			return true
		}
		updated, err := s.exportProxy.Update(r.Context(), id, config)
		if err != nil {
			s.writeExportProxyError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	case http.MethodDelete:
		if err := s.exportProxy.Delete(r.Context(), id); err != nil {
			s.writeExportProxyError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

func (s *Server) rejectUnsupportedExportProxyDevice(w http.ResponseWriter, ctx context.Context, deviceID string) bool {
	config, err := s.store.Device(ctx, strings.TrimSpace(deviceID))
	if err != nil {
		s.writeStoreError(w, err)
		return true
	}
	if config.DeviceType == store.DeviceTypeUSBSIMReader {
		writeError(w, http.StatusConflict, "wifi_calling_only_device", "USB SIM readers cannot export cellular data as a proxy")
		return true
	}
	return false
}

func (s *Server) writeExportProxyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, exportproxy.ErrDisabled):
		writeError(w, http.StatusForbidden, "developer_mode_required", "Export Proxy is disabled")
	case errors.Is(err, exportproxy.ErrNotFound):
		writeError(w, http.StatusNotFound, "export_proxy_not_found", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "export_proxy_invalid", err.Error())
	}
}
