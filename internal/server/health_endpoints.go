package server

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

func (s *Server) handleLiveness(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

// handleMetrics exposes only process-level, non-identifying Prometheus data.
// Device IDs, SIM identities, phone numbers and proxy information never enter
// this unauthenticated endpoint.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ready := 0
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	if s.store.Ready(ctx) == nil {
		ready = 1
	}
	cancel()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "# HELP vocat_up Whether the process is running.\n# TYPE vocat_up gauge\nvocat_up 1\n")
	fmt.Fprintf(w, "# HELP vocat_ready Whether the database is ready.\n# TYPE vocat_ready gauge\nvocat_ready %d\n", ready)
	fmt.Fprintf(w, "# HELP vocat_uptime_seconds Process uptime.\n# TYPE vocat_uptime_seconds counter\nvocat_uptime_seconds %.0f\n", time.Since(s.startedAt).Seconds())
	fmt.Fprintf(w, "# HELP vocat_go_goroutines Current Go goroutines.\n# TYPE vocat_go_goroutines gauge\nvocat_go_goroutines %d\n", runtime.NumGoroutine())
}
