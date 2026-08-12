package server

import (
	"net/http"

	"vocat/internal/developer"
)

func (s *Server) handleDeveloperSettings(w http.ResponseWriter, r *http.Request) {
	if !s.developerActive(r.Context()) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeDeveloperSettings(w, r)
	case http.MethodPut:
		var request struct {
			DeviceLimit    *int `json:"device_limit"`
			SMSHourlyLimit *int `json:"sms_hourly_limit"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if request.DeviceLimit == nil && request.SMSHourlyLimit == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "at least one developer setting is required")
			return
		}
		if request.DeviceLimit != nil && (*request.DeviceLimit < 1 || *request.DeviceLimit > developer.MaxDeviceLimit) {
			writeError(w, http.StatusBadRequest, "invalid_device_limit", "device limit is outside the supported range")
			return
		}
		if request.SMSHourlyLimit != nil && (*request.SMSHourlyLimit < 1 || *request.SMSHourlyLimit > developer.MaxSMSHourlyLimit) {
			writeError(w, http.StatusBadRequest, "invalid_sms_hourly_limit", "SMS hourly limit is outside the supported range")
			return
		}
		if request.DeviceLimit != nil {
			if err := developer.SetDeviceLimit(r.Context(), s.store, *request.DeviceLimit); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_device_limit", err.Error())
				return
			}
			s.recordAudit(r.Context(), "admin", "settings.developer.device_limit", "settings", "developer", "success", "device limit updated")
		}
		if request.SMSHourlyLimit != nil {
			if err := developer.SetSMSHourlyLimit(r.Context(), s.store, *request.SMSHourlyLimit); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_sms_hourly_limit", err.Error())
				return
			}
			s.recordAudit(r.Context(), "admin", "settings.developer.sms_hourly_limit", "settings", "developer", "success", "global SMS hourly limit updated")
		}
		s.writeDeveloperSettings(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) writeDeveloperSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"device_limit":             developer.DeviceLimit(r.Context(), s.store, true),
		"default_device_limit":     developer.DefaultDeviceLimit,
		"max_device_limit":         developer.MaxDeviceLimit,
		"sms_hourly_limit":         developer.SMSHourlyLimit(r.Context(), s.store),
		"default_sms_hourly_limit": developer.DefaultSMSHourlyLimit,
		"max_sms_hourly_limit":     developer.MaxSMSHourlyLimit,
	}})
}
