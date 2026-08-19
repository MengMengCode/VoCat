package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

// overviewStreamInterval is the cadence at which the overview SSE stream pushes
// a fresh snapshot. It is a package var so tests can shorten it.
var overviewStreamInterval = 2 * time.Second

// beginSSE prepares a response for Server-Sent Events and returns its response
// controller for explicit flushes.
func beginSSE(w http.ResponseWriter) *http.ResponseController {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return controller
}

func writeSSEEvent(w http.ResponseWriter, controller *http.ResponseController, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	return controller.Flush()
}

// handleOverviewStream pushes the device's overview as SSE events so the UI can
// watch link/SIM/VoWiFi state change live instead of polling.
func (s *Server) handleOverviewStream(
	w http.ResponseWriter,
	r *http.Request,
	config store.Device,
	entry device.Device,
	physicalPresent bool,
) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	controller := beginSSE(w)
	if err := writeSSEEvent(w, controller, "connected", map[string]any{}); err != nil {
		return true
	}
	ticker := time.NewTicker(overviewStreamInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return true
		case <-ticker.C:
			// The config passed in was read when the stream opened. Re-read it on
			// every tick so edits made while watching (roaming data, APN, VoWiFi,
			// name…) take effect; otherwise the stream keeps replaying the stale
			// snapshot and the UI flaps between SSE-old and REST-new values.
			fresh, err := s.store.Device(r.Context(), config.ID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					// The device was deleted while streaming; end the stream.
					return true
				}
				// Transient store hiccup: keep the last known config for this tick.
			} else {
				config = fresh
			}
			currentEntry, _, present := s.physicalForConfig(config)
			overview := s.configuredDeviceOverview(config, currentEntry, present)
			if err := writeSSEEvent(w, controller, "overview", overview); err != nil {
				return true
			}
		}
	}
}

// operatorCandidateWire maps a scanned network to the candidate shape the SPA
// reads. The SSE stream is not run through the api client's camelizer, so keys
// are emitted already-camelCase (the blocking endpoint's camelize leaves them
// unchanged). includesPcsDigit is a North-American PCS PLMN concept the modem
// layer does not derive, so it is always false here.
func operatorCandidateWire(op device.ScannedOperator) map[string]any {
	rats := []string{}
	if op.Act != "" {
		rats = []string{op.Act}
	}
	return map[string]any{
		"status":           op.Status,
		"operatorName":     op.Name,
		"shortName":        op.Short,
		"plmn":             op.Numeric,
		"countryCode":      op.Country,
		"rats":             rats,
		"includesPcsDigit": false,
	}
}

func operatorCandidatesWire(operators []device.ScannedOperator) []map[string]any {
	candidates := make([]map[string]any, 0, len(operators))
	for _, op := range operators {
		candidates = append(candidates, operatorCandidateWire(op))
	}
	return candidates
}

// handleOperatorScan runs a blocking, abortable operator scan and returns the
// discovered networks in one response.
func (s *Server) handleOperatorScan(w http.ResponseWriter, r *http.Request, physicalID string) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	result, err := s.devices.ScanOperators(r.Context(), physicalID)
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"scanId":     fmt.Sprintf("scan-%d", time.Now().UnixNano()),
			"status":     result.Status,
			"candidates": operatorCandidatesWire(result.Operators),
		},
	})
	return true
}

// handleOperatorScanStream reports scan progress over SSE: an initial "running"
// event followed by a terminal "complete" (with candidates) or "failed" event.
func (s *Server) handleOperatorScanStream(w http.ResponseWriter, r *http.Request, physicalID string) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	controller := beginSSE(w)
	scanID := fmt.Sprintf("scan-%d", time.Now().UnixNano())
	if err := writeSSEEvent(w, controller, "operator_scan", map[string]any{
		"scanId": scanID,
		"status": "running",
	}); err != nil {
		return true
	}
	result, err := s.devices.ScanOperators(r.Context(), physicalID)
	if err != nil {
		_ = writeSSEEvent(w, controller, "operator_scan", map[string]any{
			"scanId":    scanID,
			"status":    "failed",
			"message":   err.Error(),
			"retryable": true,
		})
		return true
	}
	_ = writeSSEEvent(w, controller, "operator_scan", map[string]any{
		"scanId":     scanID,
		"status":     result.Status,
		"candidates": operatorCandidatesWire(result.Operators),
	})
	return true
}

// handleUSSDContinue continues an open USSD dialog. The session id (returned by
// the initial ussd request) selects the device.
func (s *Server) handleUSSDContinue(w http.ResponseWriter, r *http.Request) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	var request struct {
		SessionID string `json:"session_id"`
		Session   string `json:"sessionId"`
		Input     string `json:"input"`
		Command   string `json:"command"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	sessionID := firstNonEmpty(request.SessionID, request.Session)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "session_id is required")
		return true
	}
	input := firstNonEmpty(request.Input, request.Command)
	ctx, cancel := actionRequestContext(r.Context(), request.TimeoutMs)
	defer cancel()
	// A session opened by the USSI path maps back to a device id that may still
	// be VoWiFi-active. Prefer USSI continue when IMS is ready; otherwise report
	// the session as unavailable rather than falling through to the cellular
	// CUSD path, because the IMS session owns the actual dialog.
	if deviceID, sessionErr := s.ussdSessionDevice(sessionID); sessionErr == nil {
		if config, configErr := s.store.Device(r.Context(), deviceID); configErr == nil &&
			config.VoWiFiEnabled && s.vowifi != nil {
			if sender, ok := s.vowifi.(imsUSSIController); ok {
				if state, stateErr := s.vowifi.State(deviceID); stateErr == nil && state.IMSReady {
					result, sendErr := sender.SendUSSI(ctx, deviceID, vowifi.USSISubmitRequest{Input: input})
					if sendErr == nil {
						writeUSSDResult(w, ussdResultFromUSSI(result, deviceID, s))
						return true
					}
					if !errors.Is(sendErr, vowifi.ErrUSSINotReady) {
						s.writeDeviceError(w, sendErr)
						return true
					}
				}
			}
		}
		writeError(w, http.StatusServiceUnavailable, "ussi_session_unavailable",
			"USSI session is no longer available because the IMS registration has dropped")
		return true
	}
	result, err := s.devices.ContinueUSSD(ctx, sessionID, input)
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeUSSDResult(w, result)
	return true
}

// handleUSSDCancel aborts an open USSD dialog.
func (s *Server) handleUSSDCancel(w http.ResponseWriter, r *http.Request) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	var request struct {
		SessionID string `json:"session_id"`
		Session   string `json:"sessionId"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	sessionID := firstNonEmpty(request.SessionID, request.Session)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "session_id is required")
		return true
	}
	// Drop a USSI-originated session token locally. USSI has no network-side
	// release signalling in the minimal implementation, so dropping the handle
	// matches the cellular AT+CUSD=2 "best-effort abort" behavior.
	if _, sessionErr := s.ussdSessionDevice(sessionID); sessionErr == nil {
		s.dropUSSDSession(sessionID)
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"cancelled": true, "session_id": sessionID},
		})
		return true
	}
	if err := s.devices.CancelUSSD(r.Context(), sessionID); err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"cancelled": true, "session_id": sessionID},
	})
	return true
}

func writeUSSDResult(w http.ResponseWriter, result device.USSDResult) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"result": map[string]any{
				"status":       result.Status,
				"text":         result.Text,
				"raw":          result.Raw,
				"dcs":          result.DCS,
				"continueable": result.Continueable,
			},
			"session_id": result.SessionID,
		},
	})
}

// handleFixUSBNet sets the USB network mode on a discovered-but-unmanaged modem,
// addressed by its AT port. Used to rescue a modem stuck in the wrong USB mode
// before it is taken over.
func (s *Server) handleFixUSBNet(w http.ResponseWriter, r *http.Request) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	var request struct {
		ATPort string `json:"at_port"`
		Mode   *int   `json:"mode"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	mode := 0
	if request.Mode != nil {
		mode = *request.Mode
	}
	result, err := s.devices.SetUSBNetModeByPort(r.Context(), request.ATPort, mode)
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}
