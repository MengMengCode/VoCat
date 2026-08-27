package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type imsSMSController interface {
	SendSMS(context.Context, string, vowifi.SMSSubmitRequest) (vowifi.SMSSubmitResult, error)
}

func (s *Server) routeSMSAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	switch cleanPath {
	case "sms/contacts":
		s.handleSMSContacts(w, r)
	case "sms/thread":
		s.handleSMSThread(w, r)
	case "sms/send":
		s.handleSMSSend(w, r)
	default:
		segments := splitAPIPath(cleanPath)
		if len(segments) == 3 && segments[0] == "sms" && segments[1] == "messages" {
			s.handleSMSMessage(w, r, segments[2])
			return true
		}
		return false
	}
	return true
}

func (s *Server) handleSMSContacts(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	deviceID := normalizeSMSDeviceFilter(r.URL.Query().Get("device_id"))
	s.syncModemSMS(r.Context(), deviceID)
	filter := s.smsStoreFilter(r.Context(), deviceID, "")
	filter.Limit = queryLimit(r, 100)
	contacts, err := s.store.ListSMSContacts(r.Context(), filter)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	result := make([]map[string]any, 0, len(contacts))
	for _, contact := range contacts {
		result = append(result, map[string]any{
			"device_id":      contact.DeviceID,
			"device_name":    contact.DeviceName,
			"modem_imei":     contact.ModemIMEI,
			"imsi":           contact.IMSI,
			"local_phone":    contact.LocalPhone,
			"peer":           contact.Peer,
			"display_name":   contact.DisplayName,
			"last_message":   contact.LastMessage,
			"last_content":   contact.LastMessage,
			"last_timestamp": contact.LastTimestamp,
			"direction":      contact.Direction,
			"last_type":      "sms",
			"last_sms_id":    contact.LastSMSID,
			"unread_count":   contact.UnreadCount,
			"message_count":  contact.MessageCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) handleSMSThread(w http.ResponseWriter, r *http.Request) {
	deviceID := normalizeSMSDeviceFilter(r.URL.Query().Get("device_id"))
	modemIMEI := strings.TrimSpace(r.URL.Query().Get("modem_imei"))
	imsi := strings.TrimSpace(r.URL.Query().Get("imsi"))
	peer := strings.TrimSpace(r.URL.Query().Get("peer"))
	if peer == "" {
		writeError(w, http.StatusBadRequest, "invalid_peer", "SMS peer is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.syncModemSMS(r.Context(), deviceID)
		filter := s.smsStoreFilter(r.Context(), deviceID, modemIMEI)
		filter.IMSI = imsi
		filter.Peer = peer
		filter.Limit = queryLimit(r, 100)
		if beforeID, parseErr := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("before_id")), 10, 64); parseErr == nil && beforeID > 0 {
			filter.BeforeID = beforeID
		}
		messages, err := s.store.ListSMSMessages(r.Context(), filter)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		unreadIDs := make([]int64, 0, len(messages))
		for i := range messages {
			if !messages[i].Read && (messages[i].Direction == "inbound" || messages[i].Direction == "received") {
				messages[i].Read = true
				unreadIDs = append(unreadIDs, messages[i].ID)
			}
		}
		if len(unreadIDs) > 0 {
			if markErr := s.store.MarkSMSMessagesRead(r.Context(), unreadIDs); markErr != nil {
				s.logger.Warn("mark SMS messages read failed", "error", markErr)
			}
		}
		reverseSMS(messages)
		result := make([]map[string]any, 0, len(messages))
		for _, message := range messages {
			result = append(result, storedSMSResponse(message))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodDelete:
		filter := s.smsStoreFilter(r.Context(), deviceID, modemIMEI)
		filter.IMSI = imsi
		filter.Peer = peer
		filter.Limit = 1000
		messages, err := s.store.ListSMSMessages(r.Context(), filter)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if len(messages) == 0 {
			writeError(w, http.StatusNotFound, "not_found", "SMS thread was not found")
			return
		}
		if err := s.deleteSMSMessages(r.Context(), messages); err != nil {
			s.writeDeviceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"deleted": len(messages)},
		})
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func normalizeSMSDeviceFilter(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "all") {
		return ""
	}
	return value
}

// smsStoreFilter resolves a mutable configured device ID to the modem's stable
// IMEI. The ID is still used to address the live modem, but persisted history
// remains attached to the same hardware after the user renames that ID.
func (s *Server) smsStoreFilter(ctx context.Context, deviceID, requestedIMEI string) store.SMSFilter {
	filter := store.SMSFilter{ModemIMEI: strings.TrimSpace(requestedIMEI)}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return filter
	}
	filter.ModemIMEI = ""
	filter.DeviceID = deviceID
	config, err := s.store.Device(ctx, deviceID)
	if err != nil {
		return filter
	}
	imei := strings.TrimSpace(config.ModemIMEI)
	if entry, _, present := s.physicalForConfig(config); present {
		imei = firstNonEmpty(
			snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI }),
			imei,
		)
	}
	if imei != "" {
		filter.DeviceID = ""
		filter.ModemIMEI = imei
	}
	return filter
}

// blockedSMSDestination reports whether the recipient is in a barred country.
// Normalization mirrors the PDU/IMS paths so the block cannot be sidestepped by
// dropping the leading "+" or using a 00 international prefix.
func blockedSMSDestination(phone string) (bool, string) {
	var digits strings.Builder
	for _, c := range strings.TrimSpace(phone) {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		}
	}
	d := digits.String()
	if strings.HasPrefix(d, "00") {
		d = d[2:]
	}
	if strings.HasPrefix(d, "86") {
		return true, "SMS to +86 (China) destinations is not allowed"
	}
	return false, ""
}

func (s *Server) handleSMSSend(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return
	}
	var request struct {
		Phone    string `json:"phone"`
		Message  string `json:"message"`
		DeviceID string `json:"device_id"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.DeviceID = strings.TrimSpace(request.DeviceID)
	if request.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "device_required", "a sending device is required")
		return
	}
	if blocked, reason := blockedSMSDestination(request.Phone); blocked {
		writeError(w, http.StatusBadRequest, "blocked_destination", reason)
		return
	}
	// Validate the logical message before consuming a global send slot. Both
	// cellular AT and VoWiFi IMS use this same encoder/validator.
	if _, err := device.PrepareSMSSubmitTPDUs(request.Phone, request.Message); err != nil {
		s.writeDeviceError(w, err)
		return
	}
	config, err := s.store.Device(r.Context(), request.DeviceID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	entry, physicalID, present := s.physicalForConfig(config)
	if !s.requirePhysicalDevice(w, present) {
		return
	}
	limit := developer.SMSHourlyLimit(r.Context(), s.store)
	reservation, err := s.store.ReserveSMSSend(r.Context(), request.DeviceID, limit, time.Now().UTC())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !reservation.Allowed {
		retryAfter := time.Until(reservation.ResetAt)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		w.Header().Set("Retry-After", strconv.FormatInt(int64((retryAfter+time.Second-1)/time.Second), 10))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": apiError{
				Code:    "sms_rate_limited",
				Message: fmt.Sprintf("Global SMS limit reached: at most %d messages may be submitted in a rolling one-hour window.", reservation.Limit),
			},
			"data": map[string]any{
				"limit":       reservation.Limit,
				"used":        reservation.Used,
				"remaining":   reservation.Remaining,
				"reset_at":    reservation.ResetAt,
				"retry_after": int64((retryAfter + time.Second - 1) / time.Second),
			},
		})
		return
	}
	if config.VoWiFiEnabled && s.vowifi != nil {
		state, stateErr := s.vowifi.State(request.DeviceID)
		sender, canSendIMS := s.vowifi.(imsSMSController)
		if stateErr == nil && state.IMSReady && state.SMSReady && canSendIMS {
			result, sendErr := sender.SendSMS(r.Context(), request.DeviceID, vowifi.SMSSubmitRequest{
				Recipient: request.Phone,
				Text:      request.Message,
			})
			if sendErr == nil || result.PartsAttempted > 0 || !errors.Is(sendErr, vowifi.ErrSMSNotReady) {
				s.writeIMSSMSSendResult(w, r, request.DeviceID, request.Message, entry, result, sendErr)
				return
			}
		}
	}
	// Prefer VoWiFi IMS when it is ready. If IMS is unavailable, including on a
	// native OpenStick 410 registered on a roaming cellular network, use the
	// modem's discovered auxiliary AT port and the existing AT+CMGS path.
	result, sendErr := s.devices.SendSMS(
		r.Context(),
		physicalID,
		request.Phone,
		request.Message,
	)
	if sendErr != nil && result.PartsAttempted == 0 {
		s.writeDeviceError(w, sendErr)
		return
	}
	imsi := snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMSI })
	modemIMEI := firstNonEmpty(
		snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI }),
		config.ModemIMEI,
	)
	extra, _ := json.Marshal(map[string]any{
		"encoding":             result.Encoding,
		"message_reference":    result.MessageReference,
		"reference_known":      result.ReferenceKnown,
		"accepted_by_modem":    result.AcceptedByModem,
		"delivery_confirmed":   result.DeliveryConfirmed,
		"submission_status":    result.SubmissionStatus,
		"modem_final":          result.ModemFinal,
		"modem_evidence_count": len(result.ModemEvidence),
		"parts_total":          result.PartsTotal,
		"parts_attempted":      result.PartsAttempted,
		"parts_accepted":       result.PartsAccepted,
		"all_parts_accepted":   result.AllPartsAccepted,
		"concat_reference":     result.ConcatReference,
		"part_results":         result.PartResults,
	})
	messageID := fmt.Sprintf(
		"at-submit:%s:%d:%d",
		firstNonEmpty(modemIMEI, request.DeviceID),
		result.MessageReference,
		result.SubmittedAt.UnixNano(),
	)
	saved, err := s.store.SaveSMSMessage(r.Context(), store.SMSMessage{
		MessageID:     messageID,
		DeviceID:      request.DeviceID,
		ModemIMEI:     modemIMEI,
		IMSI:          imsi,
		Peer:          result.To,
		Direction:     "outbound",
		Body:          request.Message,
		Timestamp:     result.SubmittedAt,
		Status:        result.SubmissionStatus,
		Source:        "cellular_at",
		PartsTotal:    result.PartsTotal,
		DeliveryState: result.DeliveryStatus,
		Read:          true,
		Extra:         extra,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	data := map[string]any{
		"message_id":          saved.MessageID,
		"id":                  saved.ID,
		"parts_total":         saved.PartsTotal,
		"parts_attempted":     result.PartsAttempted,
		"parts_accepted":      result.PartsAccepted,
		"all_parts_accepted":  result.AllPartsAccepted,
		"concat_reference":    result.ConcatReference,
		"part_results":        result.PartResults,
		"delivery_state":      saved.DeliveryState,
		"submission_state":    saved.Status,
		"message_reference":   result.MessageReference,
		"reference_known":     result.ReferenceKnown,
		"submission_accepted": result.AllPartsAccepted,
		"delivery_confirmed":  result.DeliveryConfirmed,
		"outcome":             smsSendOutcome(result.AllPartsAccepted, result.PartsAccepted, result.PartsTotal, result.DeliveryConfirmed),
		"transport":           "cellular_at",
	}
	if sendErr != nil {
		data["retry_safe"] = false
		if result.PartsAccepted > 0 {
			s.logger.Warn("multipart SMS was only partially accepted",
				"category", "sms", "event", "sms.submission",
				"device_id", request.DeviceID, "peer", request.Phone,
				"transport", "cellular_at", "parts_attempted", result.PartsAttempted,
				"parts_accepted", result.PartsAccepted, "raw_error", sendErr,
			)
			data["warning"] = "Only part of the multipart SMS was accepted by the modem. Do not retry the whole message."
			writeJSON(w, http.StatusAccepted, map[string]any{"data": data})
			return
		}
		s.logger.Warn(
			"SMS submission failed after modem interaction",
			"category", "sms",
			"event", "sms.submission",
			"device_id", request.DeviceID,
			"peer", request.Phone,
			"transport", "cellular_at",
			"parts_attempted", result.PartsAttempted,
			"parts_accepted", result.PartsAccepted,
			"raw_error", sendErr,
		)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": apiError{
				Code:    "sms_submission_failed",
				Message: "The modem did not provide complete proof that the SMS was accepted. Inspect part_results before retrying.",
			},
			"data": data,
		})
		return
	}
	if !result.AllPartsAccepted {
		s.logger.Warn("SMS submission was not confirmed",
			"category", "sms", "event", "sms.submission",
			"device_id", request.DeviceID, "peer", request.Phone,
			"transport", "cellular_at", "modem_final", result.ModemFinal,
			"parts_attempted", result.PartsAttempted, "parts_accepted", result.PartsAccepted,
		)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": apiError{
				Code:    "sms_submission_unconfirmed",
				Message: "The modem did not confirm acceptance of every SMS part.",
			},
			"data": data,
		})
		return
	}
	s.logger.Info("SMS submission accepted",
		"category", "sms", "event", "sms.submission",
		"device_id", request.DeviceID, "peer", request.Phone,
		"transport", "cellular_at", "parts", result.PartsAccepted,
	)
	writeJSON(w, http.StatusAccepted, map[string]any{"data": data})
}

func (s *Server) writeIMSSMSSendResult(
	w http.ResponseWriter,
	r *http.Request,
	deviceID string,
	body string,
	entry device.Device,
	result vowifi.SMSSubmitResult,
	sendErr error,
) {
	if sendErr != nil && result.PartsAttempted == 0 {
		if errors.Is(sendErr, device.ErrSMSInvalidRecipient) ||
			errors.Is(sendErr, device.ErrSMSEmpty) ||
			errors.Is(sendErr, device.ErrSMSTooLong) {
			s.writeDeviceError(w, sendErr)
			return
		}
		writeError(w, http.StatusBadGateway, "ims_sms_submission_failed", sendErr.Error())
		return
	}
	extra, _ := json.Marshal(map[string]any{
		"transport":          "ims",
		"encoding":           result.Encoding,
		"parts_total":        result.PartsTotal,
		"parts_attempted":    result.PartsAttempted,
		"parts_accepted":     result.PartsAccepted,
		"all_parts_accepted": result.AllPartsAccepted,
		"concat_reference":   result.ConcatReference,
		"part_results":       result.PartResults,
		"delivery_confirmed": result.DeliveryConfirmed,
		"submission_status":  result.SubmissionStatus,
	})
	imsi := snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMSI })
	modemIMEI := snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI })
	if config, configErr := s.store.Device(r.Context(), deviceID); configErr == nil {
		modemIMEI = firstNonEmpty(modemIMEI, config.ModemIMEI)
	}
	saved, err := s.store.SaveSMSMessage(r.Context(), store.SMSMessage{
		MessageID:     fmt.Sprintf("ims-submit:%s:%d", firstNonEmpty(modemIMEI, deviceID), result.SubmittedAt.UnixNano()),
		DeviceID:      deviceID,
		ModemIMEI:     modemIMEI,
		IMSI:          imsi,
		Peer:          result.To,
		Direction:     "outbound",
		Body:          body,
		Timestamp:     result.SubmittedAt,
		Status:        result.SubmissionStatus,
		Source:        "ims",
		PartsTotal:    result.PartsTotal,
		DeliveryState: imsSMSDeliveryState(result),
		Read:          true,
		Extra:         extra,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	data := map[string]any{
		"message_id":          saved.MessageID,
		"id":                  saved.ID,
		"parts_total":         result.PartsTotal,
		"parts_attempted":     result.PartsAttempted,
		"parts_accepted":      result.PartsAccepted,
		"all_parts_accepted":  result.AllPartsAccepted,
		"concat_reference":    result.ConcatReference,
		"part_results":        result.PartResults,
		"delivery_state":      saved.DeliveryState,
		"submission_state":    saved.Status,
		"transport":           "ims",
		"submission_accepted": result.AllPartsAccepted,
		"delivery_confirmed":  result.DeliveryConfirmed,
		"outcome":             smsSendOutcome(result.AllPartsAccepted, result.PartsAccepted, result.PartsTotal, result.DeliveryConfirmed),
	}
	if sendErr != nil {
		s.logger.Warn("IMS SMS submission failed",
			"category", "sms", "event", "sms.submission",
			"device_id", deviceID, "peer", result.To, "transport", "ims",
			"parts_attempted", result.PartsAttempted, "parts_accepted", result.PartsAccepted,
			"raw_error", sendErr,
		)
		data["retry_safe"] = false
		data["warning"] = sendErr.Error()
		if result.PartsAccepted == 0 {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": apiError{
					Code:    "ims_sms_submission_failed",
					Message: "IMS did not accept the SMS submission.",
				},
				"data": data,
			})
			return
		}
	}
	if !result.AllPartsAccepted && result.PartsAccepted == 0 {
		s.logger.Warn("IMS SMS submission was not confirmed",
			"category", "sms", "event", "sms.submission",
			"device_id", deviceID, "peer", result.To, "transport", "ims",
			"parts_attempted", result.PartsAttempted, "parts_accepted", result.PartsAccepted,
		)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": apiError{
				Code:    "ims_sms_submission_unconfirmed",
				Message: "IMS did not confirm acceptance of every SMS part.",
			},
			"data": data,
		})
		return
	}
	if result.AllPartsAccepted {
		s.logger.Info("IMS SMS submission accepted",
			"category", "sms", "event", "sms.submission",
			"device_id", deviceID, "peer", result.To, "transport", "ims",
			"parts", result.PartsAccepted,
		)
	} else {
		s.logger.Warn("multipart IMS SMS was only partially accepted",
			"category", "sms", "event", "sms.submission",
			"device_id", deviceID, "peer", result.To, "transport", "ims",
			"parts_attempted", result.PartsAttempted, "parts_accepted", result.PartsAccepted,
		)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": data})
}

func smsSendOutcome(allAccepted bool, partsAccepted, partsTotal int, deliveryConfirmed bool) string {
	switch {
	case deliveryConfirmed:
		return "delivered"
	case allAccepted && partsTotal > 0 && partsAccepted == partsTotal:
		return "accepted_unconfirmed"
	case partsAccepted > 0:
		return "partial"
	default:
		return "failed"
	}
}

func imsSMSDeliveryState(result vowifi.SMSSubmitResult) string {
	switch smsSendOutcome(result.AllPartsAccepted, result.PartsAccepted, result.PartsTotal, result.DeliveryConfirmed) {
	case "delivered":
		return "delivered"
	case "accepted_unconfirmed":
		return "accepted_by_ims"
	case "partial":
		return "partial"
	default:
		return "failed"
	}
}

func (s *Server) handleSMSMessage(w http.ResponseWriter, r *http.Request, idText string) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_sms_id", "SMS message ID must be a positive integer")
		return
	}
	message, err := s.store.SMSMessage(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := s.deleteSMSMessages(r.Context(), []store.SMSMessage{message}); err != nil {
		s.writeDeviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": true}})
}

func (s *Server) syncModemSMS(ctx context.Context, onlyDevice string) {
	s.smsSyncMu.Lock()
	defer s.smsSyncMu.Unlock()
	if s.devices == nil {
		return
	}
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		s.logger.Warn("list devices for SMS synchronization failed", "error", err)
		return
	}
	for _, config := range configs {
		if onlyDevice != "" && config.ID != onlyDevice {
			continue
		}
		// A PC/SC USB reader has no modem storage or AT command channel. Its
		// messages are delivered by the active VoWiFi IMS session, so attempting
		// an AT+CMGL catch-up scan would only poison the reader's health state
		// with ErrNoATPort.
		if !supportsModemSMSStorage(config) {
			continue
		}
		// Do not queue CMGL traffic on the same serial actor while VoWiFi is
		// reading the SIM or running AKA. Once the session is stable, resume the
		// SM/ME scan as a catch-up path: an SMS submitted while the card was
		// offline may be delivered to modem storage when it comes back, even
		// though subsequent live SMS is delivered by SIP MESSAGE.
		if s.vowifi != nil {
			state, stateErr := s.vowifi.State(config.ID)
			if shouldDeferModemSMSSync(state, stateErr) {
				continue
			}
		}
		entry, physicalID, present := s.physicalForConfig(config)
		if !present {
			continue
		}
		// OpenStick 410 controls cellular registration through QMI but receives
		// stored SMS through its AT port. Its firmware can reset CNMI after a
		// profile switch, leaving newly delivered SMS invisible to VoCat.
		if store.NormalizeDeviceType(config.DeviceType) == store.DeviceTypeWiFi410 {
			setupContext, cancelSetup := context.WithTimeout(ctx, 5*time.Second)
			_, setupErr := s.devices.ExecuteAT(setupContext, physicalID, `AT+CNMI=2,1,0,0,0`)
			cancelSetup()
			if setupErr != nil {
				s.logger.Debug("OpenStick 410 cellular SMS notification setup skipped", "device_id", config.ID, "error", setupErr)
			}
		}
		listContext, cancelList := context.WithTimeout(ctx, 30*time.Second)
		messages, err := s.devices.ListSMS(listContext, physicalID)
		cancelList()
		if err != nil {
			s.logger.Debug("modem SMS synchronization skipped", "device_id", config.ID, "error", err)
			continue
		}
		imsi := snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMSI })
		modemIMEI := firstNonEmpty(
			snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI }),
			config.ModemIMEI,
		)
		for _, message := range messages {
			if message.Direction == device.SMSDirectionStatusReport &&
				message.MessageReference != nil && message.StatusCode != nil {
				_, applyErr := s.store.ApplySMSDeliveryReport(ctx, store.SMSDeliveryReport{
					DeviceID:          config.ID,
					ModemIMEI:         modemIMEI,
					IMSI:              imsi,
					Peer:              message.To,
					Source:            "cellular_at",
					MessageReference:  *message.MessageReference,
					StatusCode:        *message.StatusCode,
					DeliveryState:     message.DeliveryStatus,
					ServiceCenterTime: message.ServiceCenterTimestamp,
					DischargeTime:     message.DischargeTimestamp,
					ReceivedAt:        time.Now().UTC(),
				})
				if applyErr != nil && !errors.Is(applyErr, store.ErrNotFound) {
					s.logger.Warn("apply modem SMS delivery report failed", "device_id", config.ID, "error", applyErr)
				}
				continue
			}
			peer := firstNonEmpty(message.From, message.To)
			if peer == "" {
				continue
			}
			timestamp := time.Now().UTC()
			if message.ServiceCenterTimestamp != nil {
				timestamp = message.ServiceCenterTimestamp.UTC()
			} else if message.DischargeTimestamp != nil {
				timestamp = message.DischargeTimestamp.UTC()
			}
			direction := "inbound"
			if message.Direction == device.SMSDirectionSubmitted {
				direction = "outbound"
			}
			messageID := modemSMSMessageID(message, modemIMEI, config.ID, peer)
			extra, _ := json.Marshal(map[string]any{
				"modem_index":        message.Index,
				"storage":            message.Storage,
				"storage_status":     message.StorageStatus,
				"encoding":           message.Encoding,
				"concat":             message.Concat,
				"decode_error":       message.DecodeError,
				"status_code":        message.StatusCode,
				"message_reference":  message.MessageReference,
				"delivery_status":    message.DeliveryStatus,
				"data_coding_scheme": message.DataCodingScheme,
				// Every AT modem periodically rescans persistent SMS storage. Keep a
				// completed multipart row's cursor stable when a decoder presents a
				// segment differently on a later pass, otherwise notification
				// providers can treat the old message as newly inserted.
				"keep_durable_id_on_rescan": true,
			})
			saveResult, saveErr := s.store.SaveSMSMessageWithResult(ctx, store.SMSMessage{
				MessageID:     messageID,
				DeviceID:      config.ID,
				ModemIMEI:     modemIMEI,
				IMSI:          imsi,
				Peer:          peer,
				Direction:     direction,
				Body:          message.Text,
				Timestamp:     timestamp,
				Status:        string(message.StorageStatus),
				Source:        "cellular_at",
				PartsTotal:    concatTotal(message.Concat),
				DeliveryState: message.DeliveryStatus,
				Read:          message.StorageStatus == device.SMSStatusReceivedRead,
				Extra:         extra,
			})
			if saveErr != nil {
				s.logger.Warn("persist modem SMS failed", "category", "sms", "device_id", config.ID, "raw_error", saveErr)
			} else if saved := saveResult.Message; saveResult.Inserted && saved.Direction == "inbound" &&
				store.ConcatSMSReadyToNotify(saved.MessageID, saved.Extra) {
				s.logger.Info("cellular SMS received",
					"category", "sms", "event", "sms.received",
					"device_id", config.ID, "peer", saved.Peer,
					"transport", "cellular_at", "encoding", message.Encoding,
					"parts", saved.PartsTotal,
				)
			}
		}
	}
}

func modemSMSMessageID(message device.SMSMessage, modemIMEI, deviceID, peer string) string {
	digest := sha256.Sum256([]byte(message.RawPDU))
	messageID := fmt.Sprintf(
		"modem:%s:%d:%s",
		message.Storage,
		message.Index,
		hex.EncodeToString(digest[:8]),
	)
	if message.Concat != nil && message.Concat.Total > 1 {
		// A segment of a carrier-split long SMS. Address the whole message
		// with a storage-generation id so SaveSMSMessage folds every segment
		// into one row without colliding with an older message that reused the
		// same UDH reference. SM and ME can expose duplicate copies of the same
		// slots, so the generation uses the first segment index, not storage.
		source := "cellular_at"
		if message.Concat.Sequence > 0 {
			baseIndex := message.Index - (message.Concat.Sequence - 1)
			if baseIndex >= 0 {
				source = fmt.Sprintf("cellular_at:%d", baseIndex)
			}
		}
		messageID = store.StableConcatMessageID(
			source, modemIMEI, deviceID, peer,
			message.Concat.Reference, message.Concat.Total,
		)
	}
	return messageID
}

func (s *Server) deleteSMSMessages(ctx context.Context, messages []store.SMSMessage) error {
	s.smsSyncMu.Lock()
	defer s.smsSyncMu.Unlock()
	for _, message := range messages {
		if err := s.deleteModemSMS(ctx, message); err != nil {
			return err
		}
	}
	for _, message := range messages {
		if err := s.store.DeleteSMSMessage(ctx, message.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deleteModemSMS(ctx context.Context, stored store.SMSMessage) error {
	if stored.Source != "cellular_at" ||
		(!strings.HasPrefix(stored.MessageID, "modem:") && !strings.HasPrefix(stored.MessageID, store.ConcatMessageIDPrefix)) {
		return nil
	}
	if s.devices == nil {
		return errors.New("device manager is unavailable for modem SMS deletion")
	}
	configs, err := s.store.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("list devices for modem SMS deletion: %w", err)
	}
	var config store.Device
	found := false
	for _, candidate := range configs {
		if stored.ModemIMEI != "" && candidate.ModemIMEI == stored.ModemIMEI {
			config, found = candidate, true
			break
		}
	}
	if !found {
		for _, candidate := range configs {
			if candidate.ID == stored.DeviceID {
				config, found = candidate, true
				break
			}
		}
	}
	if !found {
		return device.ErrNotFound
	}
	_, physicalID, present := s.physicalForConfig(config)
	if !present {
		return device.ErrNotFound
	}
	listContext, cancelList := context.WithTimeout(ctx, 30*time.Second)
	modemMessages, err := s.devices.ListSMS(listContext, physicalID)
	cancelList()
	if err != nil {
		return fmt.Errorf("list modem SMS before deletion: %w", err)
	}
	locations := make(map[string]device.SMSMessage)
	for _, message := range modemMessages {
		peer := firstNonEmpty(message.From, message.To)
		if peer == "" || modemSMSMessageID(message, stored.ModemIMEI, config.ID, peer) != stored.MessageID {
			continue
		}
		locations[message.Storage+":"+strconv.Itoa(message.Index)] = message
	}
	for _, message := range locations {
		deleteContext, cancelDelete := context.WithTimeout(ctx, 10*time.Second)
		err := s.devices.DeleteSMSFromStorage(deleteContext, physicalID, message.Storage, message.Index)
		cancelDelete()
		if err != nil {
			return fmt.Errorf("delete modem SMS from %s index %d: %w", message.Storage, message.Index, err)
		}
	}
	return nil
}

func supportsModemSMSStorage(config store.Device) bool {
	deviceType := store.NormalizeDeviceType(config.DeviceType)
	if deviceType == store.DeviceTypeUSBSIMReader {
		return false
	}
	if deviceType == store.DeviceTypeWiFi410 {
		return strings.TrimSpace(config.ATPort) != ""
	}
	return true
}

func shouldDeferModemSMSSync(state vowifi.State, stateErr error) bool {
	if stateErr != nil || !state.Enabled {
		return false
	}
	// SMSReady is a quiescent runtime state: SIM/AKA setup has finished and
	// reading stored messages cannot race the eSIM/VoWiFi startup sequence.
	// Failed is also safe because the orchestrator has restored cellular radio
	// operation before publishing the terminal failure state.
	return state.Phase != vowifi.PhaseSMSReady && state.Phase != vowifi.PhaseFailed
}

// StartSMSSyncLoop periodically persists inbound cellular SMS even when no
// client has the SMS page open. The first tick is delayed so startup SIM/AKA
// work gets exclusive use of the modem. Stable VoWiFi sessions still scan SM
// and ME as a catch-up path for messages delivered while the card was offline.
func (s *Server) StartSMSSyncLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncModemSMS(ctx, "")
		}
	}
}

func storedSMSResponse(message store.SMSMessage) map[string]any {
	return map[string]any{
		"id":             message.ID,
		"message_id":     message.MessageID,
		"device_id":      message.DeviceID,
		"modem_imei":     message.ModemIMEI,
		"imsi":           message.IMSI,
		"peer":           message.Peer,
		"direction":      message.Direction,
		"body":           message.Body,
		"content":        message.Body,
		"sender":         ternaryString(message.Direction == "outbound", "", message.Peer),
		"recipient":      ternaryString(message.Direction == "outbound", message.Peer, ""),
		"type":           "sms",
		"timestamp":      message.Timestamp,
		"status":         message.Status,
		"source":         message.Source,
		"parts_total":    message.PartsTotal,
		"delivery_state": message.DeliveryState,
	}
}

func reverseSMS(messages []store.SMSMessage) {
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
}

func concatTotal(value *device.SMSConcatInfo) int {
	if value == nil || value.Total < 1 {
		return 1
	}
	return value.Total
}

func ternaryString(condition bool, yes string, no string) string {
	if condition {
		return yes
	}
	return no
}

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value < 1 {
		return fallback
	}
	if value > 1000 {
		return 1000
	}
	return value
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "the requested record was not found")
		return
	}
	s.logger.Error("database operation failed", "category", "system", "event", "store.operation_failed", "raw_error", err)
	writeError(w, http.StatusInternalServerError, "database_error", "the database operation failed")
}
