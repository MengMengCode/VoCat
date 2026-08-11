package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

const maxCallDuration = 10 * time.Minute

func (s *Server) handleCalls(w http.ResponseWriter, r *http.Request, config store.Device, physicalID string) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	transport := s.callTransport(config.ID)
	if transport == "vowifi" {
		controller, ok := s.vowifi.(VoWiFiCallController)
		if !ok {
			writeError(w, http.StatusNotImplemented, "vowifi_voice_unavailable", "the active VoWiFi IMS session does not expose voice-call signalling")
			return true
		}
		calls, err := controller.Calls(config.ID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "vowifi_call_failed", err.Error())
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"device_id": config.ID, "transport": transport, "calls": calls,
		}})
		return true
	}
	response, err := s.devices.ExecuteAT(r.Context(), physicalID, "AT+CLCC")
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"device_id": config.ID,
			"transport": transport,
			"calls":     parseCLCC(response),
			"raw":       response.Text(),
		},
	})
	return true
}

func (s *Server) handleCallAction(w http.ResponseWriter, r *http.Request, config store.Device, physicalID, action string) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	command := ""
	duration := time.Duration(0)
	number := ""
	callID := ""
	var dtmfDigit byte
	var dtmfDuration time.Duration
	switch action {
	case "dial":
		var request struct {
			Number          string `json:"number"`
			DurationSeconds int    `json:"duration_seconds"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		number = strings.TrimSpace(request.Number)
		if !validDialNumber(number) {
			writeError(w, http.StatusBadRequest, "invalid_number", "phone number is invalid")
			return true
		}
		if isEmergencyDialNumber(number) {
			writeError(
				w,
				http.StatusNotImplemented,
				"emergency_call_unsupported",
				"emergency calls are not supported through VoCat; use a carrier-provisioned phone or contact local emergency services directly",
			)
			return true
		}
		duration = time.Duration(request.DurationSeconds) * time.Second
		if duration < 0 || duration > maxCallDuration {
			writeError(w, http.StatusBadRequest, "invalid_duration", "duration_seconds must be 0 (no automatic hang-up) or between 1 and 600")
			return true
		}
		command = "ATD" + number + ";"
	case "answer":
		var request struct {
			CallID string `json:"call_id"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		callID = strings.TrimSpace(request.CallID)
		command = "ATA"
	case "hangup":
		var request struct {
			CallID string `json:"call_id"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		callID = strings.TrimSpace(request.CallID)
		command = "ATH"
	case "dtmf":
		var request struct {
			CallID     string `json:"call_id"`
			Digit      string `json:"digit"`
			DurationMS int    `json:"duration_ms"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		callID = strings.TrimSpace(request.CallID)
		var valid bool
		dtmfDigit, valid = normalizeDTMFDigit(request.Digit)
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid_dtmf_digit", "digit must be one of 0-9, *, #, or A-D")
			return true
		}
		if request.DurationMS == 0 {
			request.DurationMS = 120
		}
		if request.DurationMS < 40 || request.DurationMS > 5000 {
			writeError(w, http.StatusBadRequest, "invalid_dtmf_duration", "duration_ms must be between 40 and 5000")
			return true
		}
		dtmfDuration = time.Duration(request.DurationMS) * time.Millisecond
	default:
		writeError(w, http.StatusNotFound, "not_found", "call action not found")
		return true
	}

	transport := s.callTransport(config.ID)
	if transport == "vowifi" {
		controller, ok := s.vowifi.(VoWiFiCallController)
		if !ok {
			writeError(w, http.StatusNotImplemented, "vowifi_voice_unavailable", "the active VoWiFi IMS session does not expose voice-call signalling")
			return true
		}
		var result any
		var err error
		switch action {
		case "dial":
			result, err = controller.DialCall(r.Context(), config.ID, number)
		case "answer":
			callID, err = resolveVoWiFiCallID(controller, config.ID, callID, "ringing")
			if err == nil {
				result, err = controller.AnswerCall(r.Context(), config.ID, callID)
			}
		case "hangup":
			callID, err = resolveVoWiFiCallID(controller, config.ID, callID, "")
			if err == nil {
				err = controller.HangupCall(r.Context(), config.ID, callID)
			}
		case "dtmf":
			callID, err = resolveVoWiFiCallID(controller, config.ID, callID, "active")
			if err == nil {
				dtmfController, ok := s.vowifi.(VoWiFiCallDTMFController)
				if !ok {
					writeError(w, http.StatusNotImplemented, "vowifi_dtmf_unavailable", "the active IMS media session does not expose RFC 4733 DTMF")
					return true
				}
				err = dtmfController.SendDTMF(r.Context(), config.ID, callID, dtmfDigit, dtmfDuration)
				result = map[string]any{"digit": string(dtmfDigit), "duration_ms": int(dtmfDuration / time.Millisecond)}
			}
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, "vowifi_call_failed", err.Error())
			return true
		}
		if action == "dial" {
			if call, ok := result.(vowifi.Call); ok {
				callID = call.ID
			}
			if duration > 0 {
				go s.hangupVoWiFiAfter(config.ID, callID, duration)
			}
		}
		s.recordAudit(r.Context(), "admin", "call."+action, "device", config.ID, "success", transport)
		responseData := map[string]any{
			"accepted": true, "action": action, "number": number, "call_id": callID,
			"duration_seconds": int(duration / time.Second), "transport": transport, "call": result,
		}
		if action == "dtmf" {
			responseData["digit"] = string(dtmfDigit)
			responseData["duration_ms"] = int(dtmfDuration / time.Millisecond)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"data": responseData})
		return true
	}
	if action == "dtmf" {
		writeError(w, http.StatusNotImplemented, "cellular_dtmf_unavailable", "cellular in-call DTMF is not implemented by this modem control path")
		return true
	}
	operationContext, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	response, err := s.devices.ExecuteAT(operationContext, physicalID, command)
	cancel()
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(response.Final), "OK") {
		writeError(w, http.StatusBadGateway, "call_rejected", "modem did not accept the call action")
		return true
	}
	if action == "dial" {
		if duration > 0 {
			go s.hangupAfter(config.ID, physicalID, duration)
		}
	}
	s.recordAudit(r.Context(), "admin", "call."+action, "device", config.ID, "success", transport)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]any{
			"accepted": true, "action": action, "number": number,
			"duration_seconds": int(duration / time.Second), "transport": transport,
		},
	})
	return true
}

func resolveVoWiFiCallID(controller VoWiFiCallController, deviceID, id, requiredState string) (string, error) {
	if id != "" {
		return id, nil
	}
	calls, err := controller.Calls(deviceID)
	if err != nil {
		return "", err
	}
	for _, call := range calls {
		if call.State != "ended" && call.State != "failed" && (requiredState == "" || call.State == requiredState) {
			return call.ID, nil
		}
	}
	return "", errors.New("no matching active call")
}

func (s *Server) hangupVoWiFiAfter(deviceID, callID string, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	<-timer.C
	controller, ok := s.vowifi.(VoWiFiCallController)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := controller.HangupCall(ctx, deviceID, callID); err != nil {
		s.logger.Warn("automatic VoWiFi call hangup failed", "device_id", deviceID, "call_id", callID, "error", err)
	}
}

func (s *Server) callTransport(deviceID string) string {
	if s.vowifi != nil {
		// Enabled is only the desired card policy. Calls can use IMS only after
		// registration has actually completed; otherwise keep using the modem's
		// circuit-switched call path instead of routing into an unavailable IMS
		// session.
		if state, err := s.vowifi.State(deviceID); err == nil && state.IMSReady {
			return "vowifi"
		}
	}
	return "cellular"
}

func (s *Server) hangupAfter(deviceID, physicalID string, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	<-timer.C
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.devices.ExecuteAT(ctx, physicalID, "ATH"); err != nil {
		s.logger.Warn("automatic call hangup failed", "device_id", deviceID, "error", err)
	}
}

func validDialNumber(value string) bool {
	if len(value) < 2 || len(value) > 32 {
		return false
	}
	for index, character := range value {
		if character >= '0' && character <= '9' || (index == 0 && character == '+') || character == '*' || character == '#' {
			continue
		}
		return false
	}
	return true
}

func normalizeDTMFDigit(value string) (byte, bool) {
	value = strings.TrimSpace(value)
	if len(value) != 1 {
		return 0, false
	}
	digit := value[0]
	if digit >= 'a' && digit <= 'd' {
		digit -= 'a' - 'A'
	}
	if digit >= '0' && digit <= '9' || digit == '*' || digit == '#' || digit >= 'A' && digit <= 'D' {
		return digit, true
	}
	return 0, false
}

// isEmergencyDialNumber recognizes universal and common national emergency
// short codes before either the modem ATD path or the VoWiFi IMS path is
// selected. VoCat does not implement carrier emergency registration, emergency
// APN/bearer setup, location provisioning, or emergency-call routing, so these
// numbers must never be presented as ordinary calls.
func isEmergencyDialNumber(value string) bool {
	number := strings.TrimSpace(value)
	number = strings.TrimPrefix(number, "+")
	for _, prefix := range []string{"*31#", "#31#"} {
		number = strings.TrimPrefix(number, prefix)
	}
	switch number {
	case "000", // Australia
		"08",             // legacy/mobile emergency short code in several regions
		"15", "17", "18", // France medical, police, fire
		"100", "101", "102", "108", // India legacy and integrated emergency services
		"110", "119", "120", "122", // China police, fire, ambulance, traffic police
		"111",                      // New Zealand
		"112",                      // GSM/3GPP universal emergency number
		"113", "115", "117", "118", // common European national emergency services
		"144", "166", // ambulance services in parts of Europe
		"190", "191", "192", "193", // Brazil emergency services
		"911",               // North American and other integrated emergency services
		"995",               // Singapore fire and ambulance
		"997", "998", "999": // European/UK and other national emergency services
		return true
	default:
		return false
	}
}

func parseCLCC(response modem.Response) []map[string]any {
	result := make([]map[string]any, 0)
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "+CLCC:") {
			continue
		}
		fields := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "+CLCC:")), ",")
		if len(fields) < 5 {
			continue
		}
		integer := func(index int) int {
			value, _ := strconv.Atoi(strings.TrimSpace(fields[index]))
			return value
		}
		call := map[string]any{
			"index": integer(0), "direction": integer(1), "state": integer(2),
			"mode": integer(3), "multiparty": integer(4), "raw": line,
		}
		if len(fields) > 5 {
			call["number"] = strings.Trim(strings.TrimSpace(fields[5]), `"`)
		}
		result = append(result, call)
	}
	return result
}
