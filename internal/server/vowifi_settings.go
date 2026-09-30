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
			MTUCompatibility *bool   `json:"mtu_compatibility"`
			IMSAPN           *string `json:"ims_apn"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if request.MTUCompatibility == nil && request.IMSAPN == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "at least one VoWiFi setting is required")
			return
		}
		if request.MTUCompatibility != nil {
			if err := vowifisettings.SetMTUCompatibility(r.Context(), s.store, *request.MTUCompatibility); err != nil {
				s.writeStoreError(w, err)
				return
			}
			s.recordAudit(r.Context(), "admin", "settings.vowifi.mtu_compatibility", "settings", "vowifi", "success", "VoWiFi MTU compatibility updated")
		}
		if request.IMSAPN != nil {
			if err := vowifisettings.SetIMSAPN(r.Context(), s.store, *request.IMSAPN); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_ims_apn", err.Error())
				return
			}
			s.recordAudit(r.Context(), "admin", "settings.vowifi.ims_apn", "settings", "vowifi", "success", "VoWiFi IMS APN updated")
		}
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"mtu_compatibility": vowifisettings.MTUCompatibility(r.Context(), s.store),
		"ims_apn":           vowifisettings.IMSAPN(r.Context(), s.store),
	}})
}
