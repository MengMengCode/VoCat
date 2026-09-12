package server

import (
	"net/http"
	"vocat/internal/vowifisettings"
)

func (s *Server) handleVoWiFiSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		var request struct {
			MTUCompatibility *bool `json:"mtu_compatibility"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if request.MTUCompatibility == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "mtu_compatibility is required")
			return
		}
		if err := vowifisettings.SetMTUCompatibility(r.Context(), s.store, *request.MTUCompatibility); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.recordAudit(r.Context(), "admin", "settings.vowifi.mtu_compatibility", "settings", "vowifi", "success", "VoWiFi MTU compatibility updated")
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"mtu_compatibility": vowifisettings.MTUCompatibility(r.Context(), s.store),
	}})
}
