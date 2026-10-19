package server

import "net/http"

// This route runs behind the normal authenticated API middleware. Raw records
// deliberately do not appear in inbox responses, exports, or notifications.
func (s *Server) handleSMSDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	values, err := s.store.ListSMSRawRecords(r.Context(), r.URL.Query().Get("device_id"), queryLimit(r, 100))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": values})
}
