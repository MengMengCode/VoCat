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

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
	vowifiruntime "vocat/internal/vowifi/runtime"
)

type imsSMSController interface {
	SendSMS(context.Context, string, vowifi.SMSSubmitRequest) (vowifi.SMSSubmitResult, error)
}

type subscriberBoundSMSSender interface {
	SendSMSBoundSubscriber(
		context.Context,
		string,
		string,
		string,
	) (device.SMSSendResult, device.SMSSubscriberIdentity, error)
}

type subscriberBoundSMSReader interface {
	ListSMSBoundSubscriber(
		context.Context,
		string,
	) (device.SMSSubscriberScan, error)
}

var (
	_ subscriberBoundSMSSender = (*device.Manager)(nil)
	_ subscriberBoundSMSReader = (*device.Manager)(nil)
)

type smsMEProvenance struct {
	imsi     string
	baseline bool
}

const smsPersistenceTimeout = 10 * time.Second

func smsPersistenceContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), smsPersistenceTimeout)
}

func (s *Server) beginSMSMEBaseline(hardware, imsi string) bool {
	hardware = strings.TrimSpace(hardware)
	imsi = strings.TrimSpace(imsi)
	s.smsMEMu.Lock()
	defer s.smsMEMu.Unlock()
	if s.smsMEProvenance == nil {
		s.smsMEProvenance = make(map[string]smsMEProvenance)
	}
	state := s.smsMEProvenance[hardware]
	if state.imsi != imsi {
		state = smsMEProvenance{imsi: imsi}
	}
	s.smsMEProvenance[hardware] = state
	return state.baseline
}

func smsStorageMessageIMSI(storage, imsi string, meBaselineTrusted bool) string {
	if strings.EqualFold(strings.TrimSpace(storage), "ME") && !meBaselineTrusted {
		return ""
	}
	return strings.TrimSpace(imsi)
}

func (s *Server) completeSMSMEBaseline(hardware, imsi string) {
	hardware = strings.TrimSpace(hardware)
	imsi = strings.TrimSpace(imsi)
	s.smsMEMu.Lock()
	defer s.smsMEMu.Unlock()
	state := s.smsMEProvenance[hardware]
	if state.imsi != imsi {
		return
	}
	state.baseline = true
	s.smsMEProvenance[hardware] = state
}

func smsStorageWasListed(storages []string, wanted string) bool {
	for _, storage := range storages {
		if strings.EqualFold(strings.TrimSpace(storage), wanted) {
			return true
		}
	}
	return false
}

func smsRawPDUFingerprint(rawPDU string) [sha256.Size]byte {
	normalized := strings.ToUpper(strings.TrimSpace(rawPDU))
	return sha256.Sum256([]byte(normalized))
}

func smsMEConcatSubscriberEpoch(identity device.SMSSubscriberIdentity) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(identity.ICCID),
		strings.TrimSpace(identity.IMSI),
	}, "\x00")))
	return "me:" + hex.EncodeToString(digest[:])
}

func smsStorageMessageID(
	transport string,
	hardware string,
	subscriberScope string,
	storage string,
	index int,
	rawDigest [sha256.Size]byte,
) string {
	transport = normalizeCellularSMSTransport(transport)
	storage = strings.ToUpper(strings.TrimSpace(storage))
	scope := "unattributed"
	if subscriberScope = strings.TrimSpace(subscriberScope); subscriberScope != "" {
		scope = "imsi:" + subscriberScope
	}
	provenanceDigest := sha256.Sum256([]byte(strings.Join([]string{
		transport,
		strings.TrimSpace(hardware),
		storage,
		scope,
	}, "\x00")))
	return fmt.Sprintf(
		"modem:%s:%s:%s:%d:%s",
		transport,
		hex.EncodeToString(provenanceDigest[:8]),
		storage,
		index,
		hex.EncodeToString(rawDigest[:8]),
	)
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
	if !r.URL.Query().Has("imsi") {
		// An explicit (possibly empty, for legacy rows) subscriber scope is
		// mandatory. Treating a missing IMSI as a wildcard would merge or delete
		// same-peer conversations belonging to different SIMs.
		writeError(w, http.StatusBadRequest, "subscriber_required", "SMS subscriber identity is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.syncModemSMS(r.Context(), deviceID)
		filter := s.smsStoreFilter(r.Context(), deviceID, modemIMEI)
		filter.IMSI = imsi
		filter.IMSIExact = true
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
		for _, message := range messages {
			if !message.Read && (message.Direction == "inbound" || message.Direction == "received") {
				if err := s.store.MarkSMSRead(r.Context(), message.ID); err == nil {
					message.Read = true
				}
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
		filter.IMSIExact = true
		filter.Peer = peer
		deleted, err := s.store.DeleteSMSThreadByFilter(r.Context(), filter)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"deleted": deleted},
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
	// SMS over IMS can wait for the network's RP submit report longer than the
	// server-wide response deadline. Leave cancellation bound to r.Context, but
	// do not let the HTTP WriteTimeout truncate an otherwise live transaction.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
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
	config, err := s.store.Device(r.Context(), request.DeviceID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	entry, physicalID, present := s.physicalForConfig(config)
	if !s.requirePhysicalDevice(w, present) {
		return
	}
	if config.VoWiFiEnabled {
		if s.vowifi == nil {
			s.writeIMSSMSNotReady(w, "VoWiFi SMS runtime is unavailable")
			return
		}
		state, stateErr := s.vowifi.State(request.DeviceID)
		if stateErr != nil {
			s.writeIMSSMSNotReady(w, "VoWiFi SMS state is unavailable: "+stateErr.Error())
			return
		}
		if !state.IMSReady || !state.SMSReady {
			detail := firstNonEmpty(state.LastError, state.LastReason, "IMS SMS is not registered")
			s.writeIMSSMSNotReady(w, detail)
			return
		}
		if !runtimeSMSIdentityMatchesSnapshot(state, entry.Snapshot) {
			s.writeIMSSMSNotReady(w, "the live SIM identity no longer matches the active IMS subscriber; reconnect VoWiFi")
			return
		}
		sender, canSendIMS := s.vowifi.(imsSMSController)
		if !canSendIMS {
			s.writeIMSSMSNotReady(w, "VoWiFi runtime does not expose IMS SMS submission")
			return
		}
		result, sendErr := sender.SendSMS(r.Context(), request.DeviceID, vowifi.SMSSubmitRequest{
			Recipient: request.Phone,
			Text:      request.Message,
		})
		if errors.Is(sendErr, vowifi.ErrSMSNotReady) && result.PartsAttempted == 0 {
			s.writeIMSSMSNotReady(w, "IMS SMS became unavailable before submission; reconnect VoWiFi")
			return
		}
		s.writeIMSSMSSendResult(w, r, request.DeviceID, request.Message, entry, state, result, sendErr)
		return
	}
	sender, canBindSubscriber := s.devices.(subscriberBoundSMSSender)
	if !canBindSubscriber {
		writeError(
			w,
			http.StatusServiceUnavailable,
			"sms_subscriber_identity_unavailable",
			"cellular SMS cannot safely bind the submission to the active SIM",
		)
		return
	}
	result, identity, sendErr := sender.SendSMSBoundSubscriber(
		r.Context(),
		physicalID,
		request.Phone,
		request.Message,
	)
	if sendErr != nil && result.PartsAttempted == 0 {
		s.writeDeviceError(w, sendErr)
		return
	}
	modemIMEI := firstNonEmpty(
		snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI }),
		config.ModemIMEI,
	)
	transport := normalizeCellularSMSTransport(result.Transport)
	extra, _ := json.Marshal(map[string]any{
		"transport":            transport,
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
		"%s-submit:%s:%d:%d",
		transport,
		firstNonEmpty(modemIMEI, request.DeviceID),
		result.MessageReference,
		result.SubmittedAt.UnixNano(),
	)
	persistContext, cancelPersist := smsPersistenceContext(r.Context())
	saved, err := s.store.SaveSMSMessage(persistContext, store.SMSMessage{
		MessageID:     messageID,
		DeviceID:      request.DeviceID,
		ModemIMEI:     modemIMEI,
		IMSI:          identity.IMSI,
		Peer:          result.To,
		Direction:     "outbound",
		Body:          request.Message,
		Timestamp:     result.SubmittedAt,
		Status:        result.SubmissionStatus,
		Source:        transport,
		PartsTotal:    result.PartsTotal,
		DeliveryState: result.DeliveryStatus,
		Read:          true,
		Extra:         extra,
	})
	cancelPersist()
	if err != nil {
		// Submission has already interacted with the modem. A persistence error
		// must not erase that evidence or invite an automatic retry that could
		// duplicate an accepted part. Keep the evidence inside the nested error
		// object because the web API exposes that object for non-2xx responses.
		s.logger.Error(
			"persist cellular SMS submission failed",
			"device_id", request.DeviceID,
			"transport", transport,
			"parts_attempted", result.PartsAttempted,
			"parts_accepted", result.PartsAccepted,
			"error", err,
		)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{
				"code":                "sms_persistence_failed",
				"message":             "The modem submission result could not be saved. Do not retry automatically; inspect the acceptance evidence.",
				"retry_safe":          false,
				"transport":           transport,
				"parts_total":         result.PartsTotal,
				"parts_attempted":     result.PartsAttempted,
				"parts_accepted":      result.PartsAccepted,
				"all_parts_accepted":  result.AllPartsAccepted,
				"accepted_by_modem":   result.AcceptedByModem,
				"message_reference":   result.MessageReference,
				"reference_known":     result.ReferenceKnown,
				"submission_status":   result.SubmissionStatus,
				"concat_reference":    result.ConcatReference,
				"part_results":        result.PartResults,
				"persistence_state":   "failed",
				"submission_accepted": result.AllPartsAccepted,
				"outcome":             smsSendOutcome(result.AllPartsAccepted, result.PartsAccepted, result.PartsTotal, result.DeliveryConfirmed),
			},
		})
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
		"transport":           transport,
	}
	if sendErr != nil {
		data["retry_safe"] = false
		if result.PartsAccepted > 0 {
			data["warning"] = "Only part of the multipart SMS was accepted by the modem. Do not retry the whole message."
			writeJSON(w, http.StatusAccepted, map[string]any{"data": data})
			return
		}
		s.logger.Warn(
			"SMS submission failed after modem interaction",
			"device_id", request.DeviceID,
			"parts_attempted", result.PartsAttempted,
			"parts_accepted", result.PartsAccepted,
			"error", sendErr,
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
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": apiError{
				Code:    "sms_submission_unconfirmed",
				Message: "The modem did not confirm acceptance of every SMS part.",
			},
			"data": data,
		})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": data})
}

func (s *Server) writeIMSSMSNotReady(w http.ResponseWriter, message string) {
	writeError(w, http.StatusServiceUnavailable, "ims_sms_not_ready", message)
}

func runtimeSMSIdentityMatchesSnapshot(state vowifi.State, snapshot *device.Snapshot) bool {
	if snapshot == nil {
		return false
	}
	runtimeICCID := strings.TrimSpace(state.ICCID)
	runtimeIMSI := strings.TrimSpace(state.IMSI)
	liveICCID := strings.TrimSpace(snapshot.ICCID)
	liveIMSI := strings.TrimSpace(snapshot.IMSI)
	return runtimeICCID != "" && runtimeIMSI != "" && liveICCID != "" && liveIMSI != "" &&
		runtimeICCID == liveICCID && runtimeIMSI == liveIMSI
}

func (s *Server) writeIMSSMSSendResult(
	w http.ResponseWriter,
	r *http.Request,
	deviceID string,
	body string,
	entry device.Device,
	runtimeState vowifi.State,
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
	imsi := strings.TrimSpace(runtimeState.IMSI)
	modemIMEI := snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI })
	persistContext, cancelPersist := smsPersistenceContext(r.Context())
	defer cancelPersist()
	if config, configErr := s.store.Device(persistContext, deviceID); configErr == nil {
		modemIMEI = firstNonEmpty(modemIMEI, config.ModemIMEI)
	}
	saved, err := s.store.SaveSMSMessage(persistContext, store.SMSMessage{
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
		s.logger.Error(
			"persist IMS SMS submission failed",
			"device_id", deviceID,
			"parts_attempted", result.PartsAttempted,
			"parts_accepted", result.PartsAccepted,
			"error", err,
		)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{
				"code":                "sms_persistence_failed",
				"message":             "The IMS submission result could not be saved. Do not retry automatically; inspect the acceptance evidence.",
				"retry_safe":          false,
				"transport":           "ims",
				"parts_total":         result.PartsTotal,
				"parts_attempted":     result.PartsAttempted,
				"parts_accepted":      result.PartsAccepted,
				"all_parts_accepted":  result.AllPartsAccepted,
				"concat_reference":    result.ConcatReference,
				"part_results":        result.PartResults,
				"submission_status":   result.SubmissionStatus,
				"persistence_state":   "failed",
				"submission_accepted": result.AllPartsAccepted,
				"delivery_confirmed":  result.DeliveryConfirmed,
				"outcome":             smsSendOutcome(result.AllPartsAccepted, result.PartsAccepted, result.PartsTotal, result.DeliveryConfirmed),
			},
		})
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
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": apiError{
				Code:    "ims_sms_submission_unconfirmed",
				Message: "IMS did not confirm acceptance of every SMS part.",
			},
			"data": data,
		})
		return
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
	if err := s.store.DeleteSMSMessage(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": true}})
}

func (s *Server) syncModemSMS(ctx context.Context, onlyDevice string) {
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
		entry, physicalID, present := s.physicalForConfig(config)
		if !present {
			continue
		}
		// Do not queue modem-storage traffic while VoWiFi is reading the SIM or
		// running AKA. EC20/EC25 can resume an AT SM/ME catch-up scan once the
		// session is stable. Native Qualcomm 410 keeps its cellular radio offline
		// while VoWiFi owns the subscriber, so its QMI WMS catch-up waits until the
		// radio has been restored instead of competing with live IMS delivery.
		if s.vowifi != nil {
			state, stateErr := s.vowifi.State(config.ID)
			if shouldDeferModemSMSSync(state, stateErr) ||
				shouldDeferNative410SMSCatchUp(
					config,
					firstNonEmpty(entry.Candidate.ID, entry.ID),
					state,
					stateErr,
				) {
				continue
			}
		}
		reader, canBindSubscriber := s.devices.(subscriberBoundSMSReader)
		if !canBindSubscriber {
			s.logger.Debug(
				"modem SMS synchronization skipped",
				"device_id", config.ID,
				"error", "device manager cannot bind SMS storage to the active subscriber",
			)
			continue
		}
		listContext, cancelList := context.WithTimeout(ctx, 30*time.Second)
		scan, err := reader.ListSMSBoundSubscriber(listContext, physicalID)
		cancelList()
		if err != nil {
			s.logger.Debug("modem SMS synchronization skipped", "device_id", config.ID, "error", err)
			continue
		}
		transport := normalizeCellularSMSTransport(scan.Transport)
		messages := scan.Messages
		imsi := scan.Identity.IMSI
		modemIMEI := firstNonEmpty(
			snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI }),
			config.ModemIMEI,
		)
		meScanned := smsStorageWasListed(scan.Storages, "ME")
		hardwareKey := firstNonEmpty(modemIMEI, "device:"+config.ID)
		meProvenanceKey := normalizeCellularSMSTransport(transport) + "\x00" + hardwareKey
		meBaselineTrusted := false
		if meScanned {
			// Capture one immutable trust decision for the whole scan. Another
			// concurrent sync may complete the baseline while this loop runs, but
			// that must not reattribute the latter half of the same storage image.
			meBaselineTrusted = s.beginSMSMEBaseline(meProvenanceKey, imsi)
		}
		mePersistenceComplete := true
		for _, message := range messages {
			isPersistentME := strings.EqualFold(strings.TrimSpace(message.Storage), "ME")
			if isPersistentME &&
				(strings.TrimSpace(message.DecodeError) != "" || strings.TrimSpace(message.RawPDU) == "") {
				// A listed slot is not a complete ME baseline until its raw record
				// was read and decoded. QMI represents raw-read failures as an ME
				// message carrying DecodeError and no PDU; AT can expose malformed
				// records with the same incomplete shape.
				mePersistenceComplete = false
			}
			if message.Direction == device.SMSDirectionStatusReport &&
				message.MessageReference != nil && message.StatusCode != nil {
				reportDigest := smsRawPDUFingerprint(message.RawPDU)
				_, applyErr := s.store.ApplySMSDeliveryReport(ctx, store.SMSDeliveryReport{
					ReportID:  transport + ":" + hex.EncodeToString(reportDigest[:]),
					DeviceID:  config.ID,
					ModemIMEI: modemIMEI,
					// Modem storage can retain a status report across a SIM swap. Do
					// not manufacture its subscriber identity from today's snapshot;
					// the store will use the stable report fingerprint and SMSC time,
					// or leave an ambiguous reusable TP-MR unmatched.
					IMSI:              "",
					Peer:              message.To,
					Source:            transport,
					MessageReference:  *message.MessageReference,
					StatusCode:        *message.StatusCode,
					DeliveryState:     message.DeliveryStatus,
					ServiceCenterTime: message.ServiceCenterTimestamp,
					DischargeTime:     message.DischargeTimestamp,
					ReceivedAt:        time.Now().UTC(),
				})
				if applyErr != nil &&
					!errors.Is(applyErr, store.ErrNotFound) &&
					!errors.Is(applyErr, store.ErrSMSDeliveryReportAmbiguous) {
					s.logger.Warn("apply modem SMS delivery report failed", "device_id", config.ID, "error", applyErr)
				}
				continue
			}
			peer := firstNonEmpty(message.From, message.To)
			if peer == "" {
				if isPersistentME {
					// SaveSMSMessage requires a peer. Do not certify a baseline that
					// contained a record the server could not persist.
					mePersistenceComplete = false
				}
				continue
			}
			messageIMSI := smsStorageMessageIMSI(message.Storage, imsi, meBaselineTrusted)
			digest := smsRawPDUFingerprint(message.RawPDU)
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
			messageIDScope := messageIMSI
			if isPersistentME {
				// ME is modem-owned. Its durable occurrence ID must survive a
				// process restart, whose first scan is intentionally untrusted, so
				// subscriber attribution is not part of the ID. Store upsert keeps
				// the IMSI assigned when this occurrence was first inserted.
				messageIDScope = ""
			}
			segmentOccurrenceID := smsStorageMessageID(
				transport,
				hardwareKey,
				messageIDScope,
				message.Storage,
				message.Index,
				digest,
			)
			messageID := segmentOccurrenceID
			if message.Concat != nil && message.Concat.Total > 1 {
				// A segment of a carrier-split long SMS. Address the whole message
				// with a stable id so SaveSMSMessage folds every segment into one
				// progressively merged row instead of one row per segment.
				concatMessageIDScope := messageIDScope
				if isPersistentME {
					// Unlike one-slot ME records, an incomplete concatenated message
					// can collide with another SIM reusing the same peer/reference.
					// Bind reassembly to a privacy-preserving live subscriber epoch;
					// this remains stable across restart even when the first scan is
					// untrusted for displayed IMSI attribution.
					concatMessageIDScope = smsMEConcatSubscriberEpoch(scan.Identity)
				}
				messageID = store.StableConcatMessageID(
					transport, modemIMEI, config.ID, concatMessageIDScope, peer,
					message.Concat.Reference, message.Concat.Total,
				)
			}
			extra, _ := json.Marshal(map[string]any{
				"transport": transport,
				// A storage copy is not proof that this API process submitted or
				// accepted the message. In particular, a QMI MO copy can carry a
				// reusable TP-MR and must not compete with an API submission when a
				// later status report is matched.
				"reference_known":       false,
				"accepted_by_modem":     false,
				"modem_index":           message.Index,
				"storage":               message.Storage,
				"storage_status":        message.StorageStatus,
				"encoding":              message.Encoding,
				"concat":                message.Concat,
				"decode_error":          message.DecodeError,
				"status_code":           message.StatusCode,
				"message_reference":     message.MessageReference,
				"delivery_status":       message.DeliveryStatus,
				"data_coding_scheme":    message.DataCodingScheme,
				"raw_pdu":               message.RawPDU,
				"segment_occurrence_id": segmentOccurrenceID,
				"segment_fingerprint":   "sha256:" + hex.EncodeToString(digest[:]),
			})
			_, saveErr := s.store.SaveSMSMessage(ctx, store.SMSMessage{
				MessageID:     messageID,
				DeviceID:      config.ID,
				ModemIMEI:     modemIMEI,
				IMSI:          messageIMSI,
				Peer:          peer,
				Direction:     direction,
				Body:          message.Text,
				Timestamp:     timestamp,
				Status:        string(message.StorageStatus),
				Source:        transport,
				PartsTotal:    concatTotal(message.Concat),
				DeliveryState: message.DeliveryStatus,
				Read:          message.StorageStatus == device.SMSStatusReceivedRead,
				Extra:         extra,
			})
			if saveErr != nil {
				if isPersistentME {
					mePersistenceComplete = false
				}
				s.logger.Warn("persist modem SMS failed", "device_id", config.ID, "error", saveErr)
			}
		}
		if meScanned && mePersistenceComplete {
			s.completeSMSMEBaseline(meProvenanceKey, imsi)
		}
	}
}

func normalizeCellularSMSTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cellular_qmi":
		return "cellular_qmi"
	default:
		return "cellular_at"
	}
}

func shouldDeferModemSMSSync(state vowifi.State, stateErr error) bool {
	if errors.Is(stateErr, vowifiruntime.ErrSubscriberChangeInProgress) {
		return true
	}
	if stateErr != nil {
		return false
	}
	// Stopping publishes Enabled=false before cleanup finishes. Do not race a
	// storage scan against IMS/tunnel teardown or radio restoration.
	if state.Phase == vowifi.PhaseStopping {
		return true
	}
	if (state.Phase == vowifi.PhaseIdle || state.Phase == vowifi.PhaseFailed) &&
		hasRadioRestoreCleanupError(state.CleanupErrors) {
		// Idle/Failed is normally safe only after radio restoration. A recorded
		// restore failure means the modem may still be RF-off or transitioning.
		return true
	}
	if !state.Enabled {
		return false
	}
	// PhaseIMSReady is normally a transient point immediately before EnableSMS;
	// only the explicit AllowIMSWithoutSMS terminal reason makes it quiescent.
	// Failed is also safe because radio restoration completed before publication.
	settledIMSWithoutSMS := state.Phase == vowifi.PhaseIMSReady &&
		state.LastReason == "ims_registered_sms_unavailable"
	return !settledIMSWithoutSMS && state.Phase != vowifi.PhaseSMSReady &&
		state.Phase != vowifi.PhaseFailed
}

func hasRadioRestoreCleanupError(cleanupErrors []string) bool {
	for _, cleanupErr := range cleanupErrors {
		if strings.HasPrefix(strings.TrimSpace(cleanupErr), "restore radio:") {
			return true
		}
	}
	return false
}

func shouldDeferNative410SMSCatchUp(
	config store.Device,
	liveDeviceID string,
	state vowifi.State,
	stateErr error,
) bool {
	if stateErr != nil || !state.Enabled ||
		!isNativeQualcomm410SMSDevice(config, liveDeviceID) {
		return false
	}
	settledIMSWithoutSMS := state.Phase == vowifi.PhaseIMSReady &&
		state.LastReason == "ims_registered_sms_unavailable"
	return state.Phase == vowifi.PhaseSMSReady || settledIMSWithoutSMS
}

func isNativeQualcomm410SMSDevice(config store.Device, liveDeviceID string) bool {
	if liveDeviceID = strings.TrimSpace(liveDeviceID); liveDeviceID != "" {
		// Native MSM8916 discovery is rooted in /sys/class/wwan and uses wwan*
		// candidate IDs. Prefer that live backend evidence over a stale database
		// device type so RF-off VoWiFi sessions never race a QMI WMS scan.
		return strings.HasPrefix(liveDeviceID, "wwan")
	}
	return store.NormalizeDeviceType(config.DeviceType) == store.DeviceTypeWiFi410
}

// StartSMSSyncLoop periodically persists inbound cellular SMS even when no
// client has the SMS page open. The first tick is delayed so startup SIM/AKA
// work gets exclusive use of the modem. Stable EC20/EC25 VoWiFi sessions still
// scan SM/ME as a catch-up path; native Qualcomm 410 waits for RF restoration.
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
	s.logger.Error("database operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "database_error", "the database operation failed")
}
