package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type contextQueryExecer interface {
	contextExecer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ErrSMSDeliveryReportAmbiguous means a reusable TP-MR matched more than one
// outbound submission and the report did not contain enough evidence to safely
// choose one. Callers may retry if they later obtain an IMSI, report id, or
// service-center timestamp.
var ErrSMSDeliveryReportAmbiguous = errors.New("ambiguous SMS delivery report")

// SaveSMSMessage inserts a new message or updates an existing record. A
// non-empty (device_id, message_id) pair is idempotent for modem retries.
func (s *Store) SaveSMSMessage(ctx context.Context, value SMSMessage) (SMSMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SMSMessage{}, fmt.Errorf("begin SMS update: %w", err)
	}
	defer tx.Rollback()
	saved, err := saveSMSMessage(ctx, tx, value)
	if err != nil {
		return SMSMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return SMSMessage{}, fmt.Errorf("commit SMS update: %w", err)
	}
	return saved, nil
}

func saveSMSMessage(
	ctx context.Context,
	executor contextQueryExecer,
	value SMSMessage,
) (SMSMessage, error) {
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.ModemIMEI = strings.TrimSpace(value.ModemIMEI)
	value.Peer = strings.TrimSpace(value.Peer)
	value.Direction = strings.ToLower(strings.TrimSpace(value.Direction))
	if value.DeviceID == "" {
		return SMSMessage{}, errors.New("SMS device id is required")
	}
	if value.Peer == "" {
		return SMSMessage{}, errors.New("SMS peer is required")
	}
	switch value.Direction {
	case "inbound", "outbound", "received", "sent":
	default:
		return SMSMessage{}, fmt.Errorf("unsupported SMS direction %q", value.Direction)
	}
	if value.PartsTotal == 0 {
		value.PartsTotal = 1
	}
	if value.PartsTotal < 1 {
		return SMSMessage{}, errors.New("SMS parts total must be positive")
	}
	extra, err := normalizeJSONObject(value.Extra)
	if err != nil {
		return SMSMessage{}, fmt.Errorf("normalize SMS extra data: %w", err)
	}
	now := time.Now().UTC()

	// Concatenated (long) SMS arrive as one segment per delivery. Ingest points
	// address the whole message with a stable "concat:" message id and carry the
	// segment text plus its UDH sequence in Extra. Fold each occurrence into one
	// stored row. UDH references are small and reusable, so completed or stale
	// occurrences remain separate instead of donating old parts to a later SMS.
	concatMessage := isConcatSMSMessageID(value.MessageID)
	if concatMessage {
		hardwareKey := smsHardwareKey(value.ModemIMEI, value.DeviceID)
		baseMessageID := value.MessageID
		occurrencePrefix := baseMessageID + ":occ:"
		segmentDocument, decodeErr := decodeJSONObject(extra)
		if decodeErr != nil {
			return SMSMessage{}, fmt.Errorf("decode concatenated SMS segment: %w", decodeErr)
		}
		descriptor, describeErr := describeConcatSegment(segmentDocument, value.Body)
		if describeErr != nil {
			return SMSMessage{}, fmt.Errorf("describe concatenated SMS segment: %w", describeErr)
		}
		incomingTime := value.Timestamp
		if incomingTime.IsZero() {
			incomingTime = now
		}

		rows, queryErr := executor.QueryContext(
			ctx,
			smsMessageSelect+` WHERE
				COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
				AND (message_id = ? OR substr(message_id, 1, ?) = ?)
			ORDER BY id DESC`,
			hardwareKey,
			baseMessageID,
			len(occurrencePrefix),
			occurrencePrefix,
		)
		if queryErr != nil {
			return SMSMessage{}, fmt.Errorf("list concatenated SMS occurrences: %w", queryErr)
		}
		candidates := make([]SMSMessage, 0)
		for rows.Next() {
			candidate, scanErr := scanSMSMessage(rows)
			if scanErr != nil {
				_ = rows.Close()
				return SMSMessage{}, fmt.Errorf("scan concatenated SMS occurrence: %w", scanErr)
			}
			candidates = append(candidates, candidate)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			_ = rows.Close()
			return SMSMessage{}, fmt.Errorf("iterate concatenated SMS occurrences: %w", rowsErr)
		}
		if closeErr := rows.Close(); closeErr != nil {
			return SMSMessage{}, fmt.Errorf("close concatenated SMS occurrences: %w", closeErr)
		}

		var existing SMSMessage
		for _, candidate := range candidates {
			complete, hasSequence, sameSegment, inspectErr := inspectConcatOccurrence(
				candidate.Extra,
				descriptor,
				value.Body,
			)
			if inspectErr != nil {
				return SMSMessage{}, fmt.Errorf("inspect concatenated SMS occurrence %d: %w", candidate.ID, inspectErr)
			}
			if sameSegment {
				// Periodic modem scans replay every physical segment. Its exact
				// occurrence id identifies the persisted row even after the UDH
				// reference has been reused; legacy rows fall back to fingerprints.
				return candidate, nil
			}
			if existing.ID == 0 && !complete && !hasSequence &&
				concatTimesNear(candidate.Timestamp, incomingTime) {
				existing = candidate
			}
		}

		if isExactMEConcatSegment(segmentDocument, descriptor) {
			retained, findErr := findRetainedMEConcatSegmentOwner(
				ctx,
				executor,
				hardwareKey,
				baseMessageID,
				value,
				descriptor,
			)
			if findErr != nil {
				return SMSMessage{}, fmt.Errorf("find retained ME concatenated SMS segment: %w", findErr)
			}
			if retained.ID != 0 {
				// ME belongs to the modem, so the first full scan after a SIM swap
				// replays segments retained from the previous subscriber. Preserve
				// the row that first recorded this exact storage occurrence instead
				// of copying it into the new subscriber epoch. Equal PDU bytes in a
				// different modem slot remain a distinct physical occurrence.
				return retained, nil
			}
		}

		for _, legacySource := range legacyCellularSourceCandidates(value.Source) {
			legacyMessageID := legacyConcatMessageID(
				legacySource,
				value.ModemIMEI,
				value.DeviceID,
				value.Peer,
				descriptor.reference,
				descriptor.total,
			)
			if legacyMessageID == baseMessageID {
				continue
			}
			legacyOwner, findErr := findLegacyConcatSegmentOwner(
				ctx,
				executor,
				hardwareKey,
				legacyMessageID,
				legacySource,
				value,
				segmentDocument,
				descriptor,
			)
			if findErr != nil {
				return SMSMessage{}, fmt.Errorf("find legacy concatenated SMS segment: %w", findErr)
			}
			if legacyOwner.ID != 0 {
				upgraded, upgradeErr := backfillLegacyConcatSegmentIdentity(
					ctx,
					executor,
					legacyOwner,
					segmentDocument,
					descriptor,
					now,
				)
				if upgradeErr != nil {
					return SMSMessage{}, fmt.Errorf("upgrade legacy concatenated SMS segment: %w", upgradeErr)
				}
				return upgraded, nil
			}
		}

		if existing.ID == 0 && len(candidates) > 0 {
			// The base id already names a completed/stale occurrence. Give this
			// new occurrence a durable suffix; later rescans find it by exact
			// occurrence id, with a fingerprint fallback for legacy ingest.
			segmentIdentity := descriptor.occurrenceID
			if segmentIdentity == "" {
				segmentIdentity = descriptor.fingerprint
			}
			digest := sha256.Sum256([]byte(fmt.Sprintf(
				"%s|%s|%d|%d",
				baseMessageID,
				segmentIdentity,
				descriptor.sequence,
				now.UnixNano(),
			)))
			value.MessageID = occurrencePrefix + hex.EncodeToString(digest[:8])
		}
		var existingExtra json.RawMessage
		if existing.ID != 0 {
			value.MessageID = existing.MessageID
			existingExtra = existing.Extra
		}
		mergedBody, mergedExtra, changed, mergeErr := mergeConcatSegment(existingExtra, value.Body, extra)
		if mergeErr != nil {
			return SMSMessage{}, fmt.Errorf("merge concatenated SMS segment: %w", mergeErr)
		}
		if existing.ID != 0 && !changed {
			return existing, nil
		}
		value.Body = mergedBody
		extra = mergedExtra
		if existing.ID != 0 {
			// A new segment advanced the message. Replace the stale partial row so
			// the merged row receives a fresh durable id; the Telegram id-cursor
			// then surfaces the now-more-complete message exactly once. Carry
			// forward identity and history fields.
			value.IMSI = existing.IMSI
			if _, delErr := executor.ExecContext(ctx, `DELETE FROM sms_messages WHERE id = ?`, existing.ID); delErr != nil {
				return SMSMessage{}, fmt.Errorf("replace concatenated SMS: %w", delErr)
			}
			value.ID = 0
			value.CreatedAt = existing.CreatedAt
			value.Read = value.Read || existing.Read
			if !existing.Timestamp.IsZero() &&
				(value.Timestamp.IsZero() || existing.Timestamp.Before(value.Timestamp)) {
				value.Timestamp = existing.Timestamp
			}
		}
	}
	if !concatMessage && value.ID == 0 {
		adopted, found, adoptErr := adoptLegacyStorageSMSMessage(
			ctx,
			executor,
			value,
			extra,
			now,
		)
		if adoptErr != nil {
			return SMSMessage{}, fmt.Errorf("adopt legacy modem SMS occurrence: %w", adoptErr)
		}
		if found {
			return adopted, nil
		}
	}
	if value.Timestamp.IsZero() {
		value.Timestamp = now
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = now
	}

	if value.ID > 0 {
		result, err := executor.ExecContext(ctx, `
			UPDATE sms_messages SET
				message_id = ?, device_id = ?, modem_imei = ?, imsi = ?, peer = ?,
				direction = ?, body = ?, message_time = ?, status = ?,
				source = ?, parts_total = ?, delivery_state = ?, is_read = ?,
				extra_json = ?, updated_at = ?
			WHERE id = ?
		`,
			value.MessageID, value.DeviceID, value.ModemIMEI, value.IMSI, value.Peer,
			value.Direction, value.Body, value.Timestamp.Unix(), value.Status,
			value.Source, value.PartsTotal, value.DeliveryState,
			boolInt(value.Read), string(extra), value.UpdatedAt.Unix(), value.ID,
		)
		if err != nil {
			return SMSMessage{}, fmt.Errorf("update SMS %d: %w", value.ID, err)
		}
		if err := requireAffected(result); err != nil {
			return SMSMessage{}, err
		}
		return scanSMSMessage(executor.QueryRowContext(ctx, smsMessageSelect+` WHERE id = ?`, value.ID))
	}

	result, err := executor.ExecContext(ctx, `
		INSERT INTO sms_messages (
			message_id, device_id, modem_imei, imsi, peer, direction, body, message_time,
			status, source, parts_total, delivery_state, is_read, extra_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO UPDATE SET
			device_id = excluded.device_id,
			modem_imei = CASE
				WHEN excluded.modem_imei <> '' THEN excluded.modem_imei
				ELSE sms_messages.modem_imei
			END,
				imsi = sms_messages.imsi,
			peer = excluded.peer,
			direction = excluded.direction,
			body = excluded.body,
			message_time = MIN(sms_messages.message_time, excluded.message_time),
			status = excluded.status,
			source = excluded.source,
			parts_total = excluded.parts_total,
			delivery_state = excluded.delivery_state,
			is_read = excluded.is_read,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at
	`,
		value.MessageID, value.DeviceID, value.ModemIMEI, value.IMSI, value.Peer,
		value.Direction, value.Body, value.Timestamp.Unix(), value.Status,
		value.Source, value.PartsTotal, value.DeliveryState,
		boolInt(value.Read), string(extra), value.CreatedAt.Unix(),
		value.UpdatedAt.Unix(),
	)
	if err != nil {
		return SMSMessage{}, fmt.Errorf("save SMS: %w", err)
	}
	if value.MessageID != "" {
		hardwareKey := smsHardwareKey(value.ModemIMEI, value.DeviceID)
		return scanSMSMessage(executor.QueryRowContext(
			ctx,
			smsMessageSelect+` WHERE
				COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
				AND message_id = ?`,
			hardwareKey,
			value.MessageID,
		))
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SMSMessage{}, fmt.Errorf("read inserted SMS id: %w", err)
	}
	return scanSMSMessage(executor.QueryRowContext(ctx, smsMessageSelect+` WHERE id = ?`, id))
}

func isExactMEConcatSegment(
	document map[string]any,
	descriptor concatSegmentDescriptor,
) bool {
	storage, _ := document["storage"].(string)
	return strings.EqualFold(strings.TrimSpace(storage), "ME") &&
		descriptor.occurrenceID != ""
}

func findRetainedMEConcatSegmentOwner(
	ctx context.Context,
	executor contextQueryExecer,
	hardwareKey string,
	baseMessageID string,
	value SMSMessage,
	descriptor concatSegmentDescriptor,
) (SMSMessage, error) {
	occurrencePrefix := baseMessageID + ":occ:"
	rows, err := executor.QueryContext(
		ctx,
		smsMessageSelect+` WHERE
			COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
			AND source = ?
			AND peer = ?
			AND direction = ?
			AND substr(message_id, 1, ?) = ?
			AND message_id <> ?
			AND substr(message_id, 1, ?) <> ?
		ORDER BY id DESC`,
		hardwareKey,
		value.Source,
		value.Peer,
		value.Direction,
		len(ConcatMessageIDPrefix),
		ConcatMessageIDPrefix,
		baseMessageID,
		len(occurrencePrefix),
		occurrencePrefix,
	)
	if err != nil {
		return SMSMessage{}, err
	}
	for rows.Next() {
		candidate, scanErr := scanSMSMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return SMSMessage{}, scanErr
		}
		matches, matchErr := persistedMEConcatOccurrenceMatches(candidate.Extra, descriptor)
		if matchErr != nil {
			_ = rows.Close()
			return SMSMessage{}, matchErr
		}
		if matches {
			if closeErr := rows.Close(); closeErr != nil {
				return SMSMessage{}, closeErr
			}
			return candidate, nil
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return SMSMessage{}, rowsErr
	}
	if closeErr := rows.Close(); closeErr != nil {
		return SMSMessage{}, closeErr
	}
	return SMSMessage{}, nil
}

func persistedMEConcatOccurrenceMatches(
	extra json.RawMessage,
	descriptor concatSegmentDescriptor,
) (bool, error) {
	document, err := decodeJSONObject(extra)
	if err != nil {
		return false, nil
	}
	storage, _ := document["storage"].(string)
	if !strings.EqualFold(strings.TrimSpace(storage), "ME") {
		return false, nil
	}
	concat, _ := document["concat"].(map[string]any)
	if numberAsInt(concat["reference"]) != descriptor.reference ||
		numberAsInt(concat["total"]) != descriptor.total {
		return false, nil
	}
	occurrenceIDs, _ := document["concat_occurrence_ids"].(map[string]any)
	occurrenceID, _ := occurrenceIDs[strconv.Itoa(descriptor.sequence)].(string)
	if strings.TrimSpace(occurrenceID) == "" ||
		strings.TrimSpace(occurrenceID) != descriptor.occurrenceID {
		return false, nil
	}
	fingerprints, _ := document["concat_fingerprints"].(map[string]any)
	fingerprint, _ := fingerprints[strconv.Itoa(descriptor.sequence)].(string)
	if fingerprint = strings.TrimSpace(fingerprint); fingerprint != "" && fingerprint != descriptor.fingerprint {
		return false, fmt.Errorf("concat sequence %d occurrence fingerprint changed", descriptor.sequence)
	}
	return true, nil
}

func findLegacyConcatSegmentOwner(
	ctx context.Context,
	executor contextQueryExecer,
	hardwareKey string,
	legacyMessageID string,
	legacySource string,
	value SMSMessage,
	segmentDocument map[string]any,
	descriptor concatSegmentDescriptor,
) (SMSMessage, error) {
	candidate, err := scanSMSMessage(executor.QueryRowContext(
		ctx,
		smsMessageSelect+` WHERE
			COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
			AND message_id = ?
			AND source = ?
			AND peer = ?
			AND direction = ?`,
		hardwareKey,
		legacyMessageID,
		legacySource,
		value.Peer,
		value.Direction,
	))
	if errors.Is(err, ErrNotFound) {
		return SMSMessage{}, nil
	}
	if err != nil {
		return SMSMessage{}, err
	}

	storedDocument, err := decodeJSONObject(candidate.Extra)
	if err != nil {
		return SMSMessage{}, err
	}
	incomingStorage, _ := segmentDocument["storage"].(string)
	storedStorage, _ := storedDocument["storage"].(string)
	if strings.TrimSpace(incomingStorage) == "" ||
		!strings.EqualFold(strings.TrimSpace(storedStorage), strings.TrimSpace(incomingStorage)) {
		return SMSMessage{}, nil
	}
	if strings.EqualFold(strings.TrimSpace(incomingStorage), "SM") {
		incomingIMSI := strings.TrimSpace(value.IMSI)
		if incomingIMSI == "" || strings.TrimSpace(candidate.IMSI) != incomingIMSI {
			return SMSMessage{}, nil
		}
	}

	_, hasSequence, sameSegment, inspectErr := inspectConcatOccurrence(
		candidate.Extra,
		descriptor,
		value.Body,
	)
	if inspectErr != nil {
		return SMSMessage{}, inspectErr
	}
	if !hasSequence || !sameSegment {
		return SMSMessage{}, nil
	}
	if legacyConcatIdentityMatches(storedDocument, descriptor) {
		return candidate, nil
	}
	// A body-only legacy match is safe only when the modem replays the stable
	// original inbound SMS timestamp. Stored MO records generally receive the
	// current host time on every scan, so they cannot use this fallback.
	if value.Direction != "inbound" && value.Direction != "received" {
		return SMSMessage{}, nil
	}
	if value.Timestamp.IsZero() || !concatTimesNear(candidate.Timestamp, value.Timestamp) {
		return SMSMessage{}, nil
	}
	return candidate, nil
}

func legacyCellularSourceCandidates(currentSource string) []string {
	currentSource = strings.TrimSpace(currentSource)
	switch {
	case strings.EqualFold(currentSource, "cellular_qmi"):
		// Native 410 devices were scanned through AT before QMI WMS support.
		// Probe that released identity first, while retaining same-source QMI
		// compatibility for any unscoped rows written by intermediate builds.
		return []string{"cellular_at", "cellular_qmi"}
	case strings.EqualFold(currentSource, "cellular_at"):
		return []string{"cellular_at"}
	case currentSource == "":
		return nil
	default:
		// IMS and any future transports may inspect only their own legacy rows.
		return []string{currentSource}
	}
}

func legacyConcatIdentityMatches(
	document map[string]any,
	descriptor concatSegmentDescriptor,
) bool {
	sequenceKey := strconv.Itoa(descriptor.sequence)
	occurrenceIDs, _ := document["concat_occurrence_ids"].(map[string]any)
	storedOccurrenceID, _ := occurrenceIDs[sequenceKey].(string)
	if strings.TrimSpace(storedOccurrenceID) != "" && descriptor.occurrenceID != "" {
		return strings.TrimSpace(storedOccurrenceID) == descriptor.occurrenceID
	}
	fingerprints, _ := document["concat_fingerprints"].(map[string]any)
	storedFingerprint, _ := fingerprints[sequenceKey].(string)
	return strings.TrimSpace(storedFingerprint) != "" &&
		strings.TrimSpace(storedFingerprint) == descriptor.fingerprint
}

func backfillLegacyConcatSegmentIdentity(
	ctx context.Context,
	executor contextQueryExecer,
	candidate SMSMessage,
	segmentDocument map[string]any,
	descriptor concatSegmentDescriptor,
	now time.Time,
) (SMSMessage, error) {
	if descriptor.occurrenceID == "" {
		return candidate, nil
	}
	document, err := decodeJSONObject(candidate.Extra)
	if err != nil {
		return SMSMessage{}, err
	}
	sequenceKey := strconv.Itoa(descriptor.sequence)
	changed := false
	putIdentity := func(key, incoming string) error {
		incoming = strings.TrimSpace(incoming)
		if incoming == "" {
			return nil
		}
		values := make(map[string]any)
		if stored, found := document[key]; found {
			var valid bool
			values, valid = stored.(map[string]any)
			if !valid {
				return fmt.Errorf("legacy %s must be an object", key)
			}
		}
		if existing, _ := values[sequenceKey].(string); strings.TrimSpace(existing) != "" {
			if strings.TrimSpace(existing) != incoming {
				return fmt.Errorf("legacy concat sequence %d %s conflicts", descriptor.sequence, key)
			}
			return nil
		}
		values[sequenceKey] = incoming
		document[key] = values
		changed = true
		return nil
	}
	if err := putIdentity("concat_occurrence_ids", descriptor.occurrenceID); err != nil {
		return SMSMessage{}, err
	}
	fingerprint, _ := segmentDocument["segment_fingerprint"].(string)
	if err := putIdentity("concat_fingerprints", fingerprint); err != nil {
		return SMSMessage{}, err
	}
	if !changed {
		return candidate, nil
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return SMSMessage{}, err
	}
	result, err := executor.ExecContext(ctx, `
		UPDATE sms_messages
		SET extra_json = ?, updated_at = ?
		WHERE id = ?
	`, string(encoded), now.Unix(), candidate.ID)
	if err != nil {
		return SMSMessage{}, err
	}
	if err := requireAffected(result); err != nil {
		return SMSMessage{}, err
	}
	return scanSMSMessage(executor.QueryRowContext(ctx, smsMessageSelect+` WHERE id = ?`, candidate.ID))
}

type currentStorageSMSDescriptor struct {
	storage             string
	index               int
	rawPDU              string
	occurrenceID        string
	fingerprint         string
	encoding            string
	dataCodingScheme    int
	hasDataCodingScheme bool
}

func adoptLegacyStorageSMSMessage(
	ctx context.Context,
	executor contextQueryExecer,
	value SMSMessage,
	extra json.RawMessage,
	now time.Time,
) (SMSMessage, bool, error) {
	document, err := decodeJSONObject(extra)
	if err != nil {
		return SMSMessage{}, false, err
	}
	descriptor, ok := describeCurrentStorageSMS(value, document)
	if !ok {
		return SMSMessage{}, false, nil
	}
	hardwareKey := smsHardwareKey(value.ModemIMEI, value.DeviceID)
	if _, currentErr := scanSMSMessage(executor.QueryRowContext(
		ctx,
		smsMessageSelect+` WHERE
			COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
			AND message_id = ?`,
		hardwareKey,
		value.MessageID,
	)); currentErr == nil {
		// Once the current durable occurrence exists, preserve the normal upsert
		// path for status/read/metadata refreshes instead of consulting legacy ids.
		return SMSMessage{}, false, nil
	} else if !errors.Is(currentErr, ErrNotFound) {
		return SMSMessage{}, false, currentErr
	}
	seen := make(map[int64]struct{})
	for _, legacySource := range legacyCellularSourceCandidates(value.Source) {
		for _, legacyMessageID := range legacyStorageMessageIDCandidates(descriptor) {
			candidate, queryErr := scanSMSMessage(executor.QueryRowContext(
				ctx,
				smsMessageSelect+` WHERE
					COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
					AND message_id = ?
					AND source = ?
					AND peer = ?
					AND direction = ?`,
				hardwareKey,
				legacyMessageID,
				legacySource,
				value.Peer,
				value.Direction,
			))
			if errors.Is(queryErr, ErrNotFound) {
				continue
			}
			if queryErr != nil {
				return SMSMessage{}, false, queryErr
			}
			seen[candidate.ID] = struct{}{}
			matches, matchErr := legacyStorageSMSMatches(candidate, value, descriptor, true)
			if matchErr != nil {
				return SMSMessage{}, false, matchErr
			}
			if matches {
				upgraded, upgradeErr := backfillLegacyStorageSMSIdentity(
					ctx,
					executor,
					candidate,
					descriptor,
					now,
				)
				return upgraded, upgradeErr == nil, upgradeErr
			}
		}

		prefix := legacyStorageMessageIDPrefix(descriptor.storage, descriptor.index)
		rows, queryErr := executor.QueryContext(
			ctx,
			smsMessageSelect+` WHERE
				COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
				AND substr(message_id, 1, ?) = ?
				AND source = ?
				AND peer = ?
				AND direction = ?
			ORDER BY id DESC`,
			hardwareKey,
			len(prefix),
			prefix,
			legacySource,
			value.Peer,
			value.Direction,
		)
		if queryErr != nil {
			return SMSMessage{}, false, queryErr
		}
		for rows.Next() {
			candidate, scanErr := scanSMSMessage(rows)
			if scanErr != nil {
				_ = rows.Close()
				return SMSMessage{}, false, scanErr
			}
			if _, alreadyChecked := seen[candidate.ID]; alreadyChecked {
				continue
			}
			matches, matchErr := legacyStorageSMSMatches(candidate, value, descriptor, false)
			if matchErr != nil {
				_ = rows.Close()
				return SMSMessage{}, false, matchErr
			}
			if !matches {
				continue
			}
			if closeErr := rows.Close(); closeErr != nil {
				return SMSMessage{}, false, closeErr
			}
			upgraded, upgradeErr := backfillLegacyStorageSMSIdentity(
				ctx,
				executor,
				candidate,
				descriptor,
				now,
			)
			return upgraded, upgradeErr == nil, upgradeErr
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			_ = rows.Close()
			return SMSMessage{}, false, rowsErr
		}
		if closeErr := rows.Close(); closeErr != nil {
			return SMSMessage{}, false, closeErr
		}
	}
	return SMSMessage{}, false, nil
}

func describeCurrentStorageSMS(
	value SMSMessage,
	document map[string]any,
) (currentStorageSMSDescriptor, bool) {
	if !strings.EqualFold(strings.TrimSpace(value.Source), "cellular_at") &&
		!strings.EqualFold(strings.TrimSpace(value.Source), "cellular_qmi") {
		return currentStorageSMSDescriptor{}, false
	}
	storage, _ := document["storage"].(string)
	storage = strings.ToUpper(strings.TrimSpace(storage))
	if storage != "SM" && storage != "ME" {
		return currentStorageSMSDescriptor{}, false
	}
	descriptor := currentStorageSMSDescriptor{
		storage: storage,
		index:   numberAsInt(document["modem_index"]),
	}
	descriptor.rawPDU, _ = document["raw_pdu"].(string)
	descriptor.occurrenceID, _ = document["segment_occurrence_id"].(string)
	descriptor.fingerprint, _ = document["segment_fingerprint"].(string)
	descriptor.encoding, _ = storageSMSEncoding(document["encoding"])
	descriptor.dataCodingScheme, descriptor.hasDataCodingScheme = storageSMSDataCodingScheme(
		document["data_coding_scheme"],
	)
	descriptor.occurrenceID = strings.TrimSpace(descriptor.occurrenceID)
	descriptor.fingerprint = strings.TrimSpace(descriptor.fingerprint)
	if descriptor.index < 0 || strings.TrimSpace(descriptor.rawPDU) == "" ||
		descriptor.occurrenceID == "" ||
		strings.TrimSpace(value.MessageID) != descriptor.occurrenceID ||
		!isCurrentStorageOccurrenceID(descriptor.occurrenceID, value.Source, storage, descriptor.index) {
		return currentStorageSMSDescriptor{}, false
	}
	if descriptor.fingerprint == "" {
		digest := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(descriptor.rawPDU))))
		descriptor.fingerprint = "sha256:" + hex.EncodeToString(digest[:])
	}
	return descriptor, true
}

func isCurrentStorageOccurrenceID(occurrenceID, source, storage string, index int) bool {
	parts := strings.Split(strings.TrimSpace(occurrenceID), ":")
	if len(parts) != 6 || parts[0] != "modem" ||
		!strings.EqualFold(parts[1], strings.TrimSpace(source)) ||
		!strings.EqualFold(parts[3], strings.TrimSpace(storage)) ||
		parts[4] != strconv.Itoa(index) || len(parts[2]) != 16 || len(parts[5]) != 16 {
		return false
	}
	_, provenanceErr := hex.DecodeString(parts[2])
	_, digestErr := hex.DecodeString(parts[5])
	return provenanceErr == nil && digestErr == nil
}

func legacyStorageMessageIDCandidates(descriptor currentStorageSMSDescriptor) []string {
	representations := []string{
		descriptor.rawPDU,
		strings.TrimSpace(descriptor.rawPDU),
	}
	compact := strings.Join(strings.Fields(descriptor.rawPDU), "")
	if _, err := hex.DecodeString(compact); err == nil {
		representations = append(
			representations,
			compact,
			strings.ToUpper(compact),
			strings.ToLower(compact),
		)
	}
	seen := make(map[string]struct{}, len(representations))
	ids := make([]string, 0, len(representations))
	for _, representation := range representations {
		if _, duplicate := seen[representation]; duplicate {
			continue
		}
		seen[representation] = struct{}{}
		digest := sha256.Sum256([]byte(representation))
		id := legacyStorageMessageIDPrefix(descriptor.storage, descriptor.index) +
			hex.EncodeToString(digest[:8])
		ids = append(ids, id)
	}
	return ids
}

func legacyStorageMessageIDPrefix(storage string, index int) string {
	return fmt.Sprintf("modem:%s:%d:", strings.ToUpper(strings.TrimSpace(storage)), index)
}

func legacyStorageSMSMatches(
	candidate SMSMessage,
	incoming SMSMessage,
	descriptor currentStorageSMSDescriptor,
	exactLegacyID bool,
) (bool, error) {
	document, err := decodeJSONObject(candidate.Extra)
	if err != nil {
		return false, err
	}
	storage, _ := document["storage"].(string)
	if !strings.EqualFold(strings.TrimSpace(storage), descriptor.storage) ||
		numberAsInt(document["modem_index"]) != descriptor.index {
		return false, nil
	}
	if descriptor.storage == "SM" {
		incomingIMSI := strings.TrimSpace(incoming.IMSI)
		if incomingIMSI == "" || strings.TrimSpace(candidate.IMSI) != incomingIMSI {
			return false, nil
		}
	}
	storedOccurrenceID, _ := document["segment_occurrence_id"].(string)
	storedFingerprint, _ := document["segment_fingerprint"].(string)
	storedOccurrenceID = strings.TrimSpace(storedOccurrenceID)
	storedFingerprint = strings.TrimSpace(storedFingerprint)
	if storedOccurrenceID != "" || storedFingerprint != "" {
		if storedOccurrenceID != "" && storedOccurrenceID != descriptor.occurrenceID {
			return false, nil
		}
		if storedFingerprint != "" && storedFingerprint != descriptor.fingerprint {
			return false, nil
		}
		return true, nil
	}
	if exactLegacyID {
		// The released id itself contains the exact SHA-256 digest of the raw
		// representation, so no timestamp/body heuristic is needed.
		return true, nil
	}
	if incoming.Direction != "inbound" && incoming.Direction != "received" {
		return false, nil
	}
	// AT and QMI can expose different raw representations for one received PDU.
	// Without an exact released digest, require the same stored SMSC second and
	// every decoded identity field available from both representations.
	if incoming.Body != candidate.Body || incoming.Timestamp.IsZero() ||
		candidate.Timestamp.Unix() != incoming.Timestamp.Unix() ||
		!legacyStorageDecodedMetadataMatches(document, descriptor) {
		return false, nil
	}
	return true, nil
}

func legacyStorageDecodedMetadataMatches(
	document map[string]any,
	descriptor currentStorageSMSDescriptor,
) bool {
	if storedEncoding, available := storageSMSEncoding(document["encoding"]); available &&
		descriptor.encoding != "" && storedEncoding != descriptor.encoding {
		return false
	}
	if storedDCS, available := storageSMSDataCodingScheme(document["data_coding_scheme"]); available &&
		descriptor.hasDataCodingScheme && storedDCS != descriptor.dataCodingScheme {
		return false
	}
	return true
}

func storageSMSEncoding(value any) (string, bool) {
	encoding, ok := value.(string)
	encoding = strings.ToLower(strings.TrimSpace(encoding))
	if !ok || encoding == "" || encoding == "unknown" {
		return "", false
	}
	return encoding, true
}

func storageSMSDataCodingScheme(value any) (int, bool) {
	dcs := numberAsInt(value)
	return dcs, dcs >= 0
}

func backfillLegacyStorageSMSIdentity(
	ctx context.Context,
	executor contextQueryExecer,
	candidate SMSMessage,
	descriptor currentStorageSMSDescriptor,
	now time.Time,
) (SMSMessage, error) {
	document, err := decodeJSONObject(candidate.Extra)
	if err != nil {
		return SMSMessage{}, err
	}
	changed := false
	putIfEmpty := func(key, value string) {
		stored, _ := document[key].(string)
		if strings.TrimSpace(stored) == "" && strings.TrimSpace(value) != "" {
			document[key] = value
			changed = true
		}
	}
	putIfEmpty("segment_occurrence_id", descriptor.occurrenceID)
	putIfEmpty("segment_fingerprint", descriptor.fingerprint)
	putIfEmpty("raw_pdu", descriptor.rawPDU)
	if !changed {
		return candidate, nil
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return SMSMessage{}, err
	}
	result, err := executor.ExecContext(ctx, `
		UPDATE sms_messages
		SET extra_json = ?, updated_at = ?
		WHERE id = ?
	`, string(encoded), now.Unix(), candidate.ID)
	if err != nil {
		return SMSMessage{}, err
	}
	if err := requireAffected(result); err != nil {
		return SMSMessage{}, err
	}
	return scanSMSMessage(executor.QueryRowContext(ctx, smsMessageSelect+` WHERE id = ?`, candidate.ID))
}

func (s *Store) SaveSMSMessages(ctx context.Context, values []SMSMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SMS batch: %w", err)
	}
	defer tx.Rollback()
	for index, value := range values {
		if _, err := saveSMSMessage(ctx, tx, value); err != nil {
			return fmt.Errorf("save SMS batch item %d: %w", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SMS batch: %w", err)
	}
	return nil
}

func (s *Store) SMSMessage(ctx context.Context, id int64) (SMSMessage, error) {
	return scanSMSMessage(s.db.QueryRowContext(ctx, smsMessageSelect+` WHERE id = ?`, id))
}

// LatestSMSMessageID returns the current durable cursor used by notification
// consumers. Starting at this value avoids replaying the entire SMS archive
// whenever the service or a notification provider is restarted.
func (s *Store) LatestSMSMessageID(ctx context.Context) (int64, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM sms_messages`).Scan(&id); err != nil {
		return 0, fmt.Errorf("read latest SMS id: %w", err)
	}
	return id, nil
}

// ListInboundSMSAfterID returns newly inserted inbound messages in durable ID
// order. Telegram advances this cursor only after considering each item, so
// timestamp corrections and duplicate modem synchronisations cannot reorder or
// duplicate notifications.
func (s *Store) ListInboundSMSAfterID(ctx context.Context, afterID int64, limit int) ([]SMSMessage, error) {
	if afterID < 0 {
		afterID = 0
	}
	rows, err := s.db.QueryContext(ctx, smsMessageSelect+`
		WHERE id > ? AND direction IN ('inbound', 'received')
		ORDER BY id ASC
		LIMIT ?`, afterID, normalizedLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list new inbound SMS messages: %w", err)
	}
	defer rows.Close()
	values := make([]SMSMessage, 0)
	for rows.Next() {
		value, scanErr := scanSMSMessage(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan new inbound SMS message: %w", scanErr)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate new inbound SMS messages: %w", err)
	}
	return values, nil
}

// ApplySMSDeliveryReport attaches a TP-STATUS report to a provably matching
// outbound submission and advances its aggregate delivery state. Multipart
// messages become delivered only after every submitted part is reported.
func (s *Store) ApplySMSDeliveryReport(ctx context.Context, report SMSDeliveryReport) (SMSMessage, error) {
	report.ReportID = strings.TrimSpace(report.ReportID)
	report.DeviceID = strings.TrimSpace(report.DeviceID)
	report.ModemIMEI = strings.TrimSpace(report.ModemIMEI)
	if (report.DeviceID == "" && report.ModemIMEI == "") || report.MessageReference < 0 || report.MessageReference > 255 {
		return SMSMessage{}, errors.New("invalid SMS delivery report identity")
	}
	if report.ReceivedAt.IsZero() {
		report.ReceivedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SMSMessage{}, fmt.Errorf("begin SMS delivery report: %w", err)
	}
	defer tx.Rollback()
	query := smsMessageSelect + `
		WHERE ((? <> '' AND modem_imei = ?) OR (? = '' AND device_id = ?))
			AND direction IN ('outbound', 'sent')
			AND (? = '' OR imsi = ?)
			AND (? = '' OR peer = ?)
			AND (? = '' OR source = ?)
		ORDER BY created_at DESC, id DESC`
	rows, err := tx.QueryContext(
		ctx,
		query,
		report.ModemIMEI, report.ModemIMEI, report.ModemIMEI, report.DeviceID,
		report.IMSI, report.IMSI,
		report.Peer, report.Peer,
		report.Source, report.Source,
	)
	if err != nil {
		return SMSMessage{}, fmt.Errorf("find SMS delivery target: %w", err)
	}
	type deliveryCandidate struct {
		message SMSMessage
		extra   map[string]any
	}
	candidates := make([]deliveryCandidate, 0)
	for rows.Next() {
		candidate, scanErr := scanSMSMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return SMSMessage{}, scanErr
		}
		extra := make(map[string]any)
		if json.Unmarshal(candidate.Extra, &extra) != nil || !smsExtraHasReference(extra, report.MessageReference) {
			continue
		}
		candidates = append(candidates, deliveryCandidate{message: candidate, extra: extra})
	}
	if err := rows.Close(); err != nil {
		return SMSMessage{}, err
	}
	if len(candidates) == 0 {
		return SMSMessage{}, ErrNotFound
	}

	// A stable report id is stronger evidence than the reusable 8-bit TP-MR.
	// Rescanning an old status report must keep updating its original row even
	// after a newer submission has reused that reference.
	var target deliveryCandidate
	if report.ReportID != "" {
		for _, candidate := range candidates {
			if smsExtraHasReportID(candidate.extra, report.ReportID) {
				if target.message.ID != 0 && target.message.ID != candidate.message.ID {
					return SMSMessage{}, ErrSMSDeliveryReportAmbiguous
				}
				target = candidate
			}
		}
	}
	if target.message.ID == 0 {
		// TP-MR is only eight bits and local history may have been deleted. Even a
		// sole remaining candidate is not proof for a previously unseen report;
		// require the mandatory TP-SCTS to show that submission preceded it.
		if report.ServiceCenterTime == nil {
			return SMSMessage{}, ErrSMSDeliveryReportAmbiguous
		}
		for _, candidate := range candidates {
			if smsDeliveryCandidatePlausible(candidate.message, candidate.extra, report.MessageReference, *report.ServiceCenterTime) {
				if target.message.ID != 0 {
					return SMSMessage{}, ErrSMSDeliveryReportAmbiguous
				}
				target = candidate
			}
		}
		if target.message.ID == 0 {
			return SMSMessage{}, ErrNotFound
		}
	}
	targetMessage, targetExtra := target.message, target.extra
	reports, _ := targetExtra["delivery_reports"].(map[string]any)
	if reports == nil {
		reports = make(map[string]any)
	}
	reportValue := map[string]any{
		"report_id":      report.ReportID,
		"status_code":    report.StatusCode,
		"delivery_state": report.DeliveryState,
		"received_at":    report.ReceivedAt.UTC(),
	}
	if report.ServiceCenterTime != nil {
		reportValue["service_center_timestamp"] = report.ServiceCenterTime.UTC()
	}
	if report.DischargeTime != nil {
		reportValue["discharge_timestamp"] = report.DischargeTime.UTC()
	}
	reportKey := strconv.Itoa(report.MessageReference)
	if existing, _ := reports[reportKey].(map[string]any); existing != nil {
		reportValue = mergeSMSDeliveryReport(existing, reportValue)
	}
	reports[reportKey] = reportValue
	targetExtra["delivery_reports"] = reports
	targetMessage.DeliveryState = aggregateSMSDeliveryState(targetExtra, reports)
	targetMessage.Extra, err = json.Marshal(targetExtra)
	if err != nil {
		return SMSMessage{}, fmt.Errorf("encode SMS delivery reports: %w", err)
	}
	targetMessage.UpdatedAt = time.Now().UTC()
	saved, err := saveSMSMessage(ctx, tx, targetMessage)
	if err != nil {
		return SMSMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return SMSMessage{}, fmt.Errorf("commit SMS delivery report: %w", err)
	}
	return saved, nil
}

func smsExtraHasReportID(extra map[string]any, reportID string) bool {
	reports, _ := extra["delivery_reports"].(map[string]any)
	for _, value := range reports {
		report, _ := value.(map[string]any)
		if stored, _ := report["report_id"].(string); stored == reportID {
			return true
		}
		if values, _ := report["report_ids"].([]any); values != nil {
			for _, value := range values {
				if stored, _ := value.(string); stored == reportID {
					return true
				}
			}
		}
	}
	return false
}

func mergeSMSDeliveryReport(existing, incoming map[string]any) map[string]any {
	ids := make(map[string]struct{})
	collect := func(report map[string]any) {
		if id, _ := report["report_id"].(string); strings.TrimSpace(id) != "" {
			ids[id] = struct{}{}
		}
		if values, _ := report["report_ids"].([]any); values != nil {
			for _, value := range values {
				if id, _ := value.(string); strings.TrimSpace(id) != "" {
					ids[id] = struct{}{}
				}
			}
		}
	}
	collect(existing)
	collect(incoming)

	existingState, _ := existing["delivery_state"].(string)
	incomingState, _ := incoming["delivery_state"].(string)
	existingRank := smsDeliveryStateRank(existingState)
	incomingRank := smsDeliveryStateRank(incomingState)
	chosen := existing
	switch {
	case incomingRank > existingRank:
		chosen = incoming
	case incomingRank == existingRank && incomingRank < 2:
		existingAt, existingOK := smsJSONTime(existing["received_at"])
		incomingAt, incomingOK := smsJSONTime(incoming["received_at"])
		if incomingOK && (!existingOK || incomingAt.After(existingAt)) {
			chosen = incoming
		}
	}
	merged := make(map[string]any, len(chosen)+1)
	for key, value := range chosen {
		merged[key] = value
	}
	if len(ids) > 0 {
		values := make([]string, 0, len(ids))
		for id := range ids {
			values = append(values, id)
		}
		sort.Strings(values)
		merged["report_ids"] = values
	}
	return merged
}

func smsDeliveryStateRank(state string) int {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "delivered":
		return 3
	case "permanent_error", "temporary_error_no_retry", "failed", "rejected":
		return 2
	default:
		return 1
	}
}

func smsDeliveryCandidatePlausible(message SMSMessage, extra map[string]any, reference int, serviceCenterTime time.Time) bool {
	submittedAt := message.Timestamp
	parts, _ := extra["part_results"].([]any)
	for _, value := range parts {
		part, _ := value.(map[string]any)
		if !smsPartHasReference(part, reference) {
			continue
		}
		if parsed, ok := smsJSONTime(part["submittedAt"]); ok {
			submittedAt = parsed
		} else if parsed, ok := smsJSONTime(part["submitted_at"]); ok {
			submittedAt = parsed
		}
		break
	}
	// SMSC time cannot precede submission. Permit a small clock skew between
	// the modem/network and host, but do not guess between multiple candidates
	// that were both already submitted.
	return !submittedAt.After(serviceCenterTime.Add(5 * time.Minute))
}

func smsPartHasReference(part map[string]any, reference int) bool {
	if !smsPartCanReceiveReport(part) {
		return false
	}
	return numberAsInt(part["reference"]) == reference ||
		numberAsInt(part["messageReference"]) == reference ||
		numberAsInt(part["message_reference"]) == reference
}

func smsPartCanReceiveReport(part map[string]any) bool {
	for _, key := range []string{"referenceKnown", "reference_known"} {
		if value, found := part[key]; found {
			known, ok := value.(bool)
			if !ok || !known {
				return false
			}
		}
	}
	for _, key := range []string{"accepted", "acceptedByModem", "accepted_by_modem"} {
		if value, found := part[key]; found {
			accepted, ok := value.(bool)
			if !ok || !accepted {
				return false
			}
		}
	}
	return true
}

func smsJSONTime(value any) (time.Time, bool) {
	switch value := value.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		return parsed, err == nil
	case float64:
		return time.Unix(int64(value), 0).UTC(), value > 0
	case json.Number:
		unix, err := value.Int64()
		return time.Unix(unix, 0).UTC(), err == nil && unix > 0
	default:
		return time.Time{}, false
	}
}

func smsExtraHasReference(extra map[string]any, reference int) bool {
	topLevelReportable := true
	for _, key := range []string{"reference_known", "accepted_by_modem"} {
		if value, found := extra[key]; found {
			flag, ok := value.(bool)
			if !ok || !flag {
				topLevelReportable = false
			}
		}
	}
	if topLevelReportable && numberAsInt(extra["message_reference"]) == reference {
		return true
	}
	parts, _ := extra["part_results"].([]any)
	for _, value := range parts {
		part, _ := value.(map[string]any)
		if smsPartHasReference(part, reference) {
			return true
		}
	}
	return false
}

func aggregateSMSDeliveryState(extra map[string]any, reports map[string]any) string {
	parts, _ := extra["part_results"].([]any)
	references := make([]int, 0, len(parts))
	for _, value := range parts {
		part, _ := value.(map[string]any)
		if !smsPartCanReceiveReport(part) {
			continue
		}
		reference := numberAsInt(part["reference"])
		if reference < 0 {
			reference = numberAsInt(part["messageReference"])
		}
		if reference < 0 {
			reference = numberAsInt(part["message_reference"])
		}
		if reference >= 0 {
			references = append(references, reference)
		}
	}
	if len(references) == 0 {
		if reference := numberAsInt(extra["message_reference"]); reference >= 0 {
			references = append(references, reference)
		}
	}
	if len(references) == 0 {
		return "unknown"
	}
	delivered := 0
	for _, reference := range references {
		value, found := reports[strconv.Itoa(reference)]
		if !found {
			continue
		}
		report, _ := value.(map[string]any)
		state, _ := report["delivery_state"].(string)
		switch state {
		case "delivered":
			delivered++
		case "permanent_error", "temporary_error_no_retry", "failed", "rejected":
			return "failed"
		}
	}
	if delivered == len(references) {
		if accepted, ok := extra["all_parts_accepted"].(bool); ok && !accepted {
			return "partial"
		}
		return "delivered"
	}
	if accepted, ok := extra["all_parts_accepted"].(bool); ok && !accepted {
		return "partial"
	}
	return "pending_delivery_report"
}

func numberAsInt(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case json.Number:
		parsed, err := strconv.Atoi(string(number))
		if err == nil {
			return parsed
		}
	}
	return -1
}

func (s *Store) ListSMSMessages(ctx context.Context, filter SMSFilter) ([]SMSMessage, error) {
	where, args := smsWhere(filter, "")
	query := smsMessageSelect + where + ` ORDER BY message_time DESC, id DESC LIMIT ?`
	args = append(args, normalizedLimit(filter.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list SMS messages: %w", err)
	}
	defer rows.Close()

	values := make([]SMSMessage, 0)
	for rows.Next() {
		value, err := scanSMSMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan SMS message: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SMS messages: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteSMSMessage(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sms_messages WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete SMS %d: %w", id, err)
	}
	return requireAffected(result)
}

// MarkSMSRead marks one stored message as read without routing the already
// assembled row back through SaveSMSMessage. In particular, a concatenated SMS
// body is the result of merging all segments and must not be interpreted as one
// new segment merely to update its read state.
func (s *Store) MarkSMSRead(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sms_messages
		SET is_read = 1, updated_at = ?
		WHERE id = ?
	`, time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("mark SMS %d read: %w", id, err)
	}
	if err := requireAffected(result); err != nil {
		return fmt.Errorf("mark SMS %d read: %w", id, err)
	}
	return nil
}

func (s *Store) DeleteSMSThread(
	ctx context.Context,
	deviceID string,
	imsi string,
	peer string,
) (int64, error) {
	return s.DeleteSMSThreadByFilter(ctx, SMSFilter{DeviceID: deviceID, IMSI: imsi, IMSIExact: true, Peer: peer})
}

// DeleteSMSThreadByFilter atomically deletes an entire hardware/SIM/peer
// thread. Unlike list APIs it has no pagination cap.
func (s *Store) DeleteSMSThreadByFilter(ctx context.Context, filter SMSFilter) (int64, error) {
	filter.DeviceID = strings.TrimSpace(filter.DeviceID)
	filter.ModemIMEI = strings.TrimSpace(filter.ModemIMEI)
	filter.IMSI = strings.TrimSpace(filter.IMSI)
	filter.Peer = strings.TrimSpace(filter.Peer)
	if (filter.DeviceID == "") == (filter.ModemIMEI == "") || !filter.IMSIExact || filter.Peer == "" {
		return 0, errors.New("SMS thread filter requires exactly one hardware identity, an exact IMSI, and peer")
	}
	where, args := smsWhere(SMSFilter{
		DeviceID: filter.DeviceID, ModemIMEI: filter.ModemIMEI,
		IMSI: filter.IMSI, IMSIExact: true, Peer: filter.Peer,
	}, "")
	result, err := s.db.ExecContext(ctx, `DELETE FROM sms_messages`+where, args...)
	if err != nil {
		return 0, fmt.Errorf("delete SMS thread: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read deleted SMS count: %w", err)
	}
	if affected == 0 {
		return 0, ErrNotFound
	}
	return affected, nil
}

func (s *Store) MarkSMSThreadRead(
	ctx context.Context,
	deviceID string,
	imsi string,
	peer string,
) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sms_messages
		SET is_read = 1, updated_at = ?
		WHERE device_id = ? AND imsi = ? AND peer = ?
			AND direction IN ('inbound', 'received') AND is_read = 0
	`, time.Now().UTC().Unix(), deviceID, imsi, peer)
	if err != nil {
		return 0, fmt.Errorf("mark SMS thread read: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read marked SMS count: %w", err)
	}
	return affected, nil
}

// ListSMSContacts derives contacts and thread counters from messages. No
// duplicated contact/thread table can drift out of sync with message history.
func (s *Store) ListSMSContacts(ctx context.Context, filter SMSFilter) ([]SMSContact, error) {
	where, args := smsWhere(filter, "m.")
	query := `
		WITH resolved AS (
			SELECT
				m.*,
				COALESCE(NULLIF(m.modem_imei, ''), 'device:' || m.device_id) AS hardware_key,
				COALESCE((
					SELECT current_device.id
					FROM devices current_device
					WHERE m.modem_imei <> ''
						AND current_device.modem_imei = m.modem_imei
					ORDER BY current_device.updated_at DESC, current_device.id
					LIMIT 1
				), m.device_id) AS resolved_device_id
			FROM sms_messages m` + where + `
		), ranked AS (
			SELECT
				m.id, m.resolved_device_id, m.modem_imei, m.imsi, m.peer,
				m.body, m.message_time, m.direction,
				ROW_NUMBER() OVER (
					PARTITION BY m.hardware_key, m.imsi, m.peer
					ORDER BY m.message_time DESC, m.id DESC
				) AS row_number,
				SUM(CASE
					WHEN m.direction IN ('inbound', 'received') AND m.is_read = 0
					THEN 1 ELSE 0
				END) OVER (
					PARTITION BY m.hardware_key, m.imsi, m.peer
				) AS unread_count,
				COUNT(*) OVER (
					PARTITION BY m.hardware_key, m.imsi, m.peer
				) AS message_count
			FROM resolved m
		)
		SELECT
			r.resolved_device_id,
			COALESCE(d.name, ''),
			r.modem_imei,
			r.imsi,
			COALESCE(NULLIF(dr.phone_number, ''), NULLIF(vr.local_phone, ''), ''),
			r.peer,
			r.peer,
			r.body,
			r.message_time,
			r.direction,
			r.id,
			r.unread_count,
			r.message_count
		FROM ranked r
		LEFT JOIN devices d ON d.id = r.resolved_device_id
		LEFT JOIN device_runtime dr ON dr.device_id = r.resolved_device_id
			AND r.imsi <> '' AND dr.imsi = r.imsi
		LEFT JOIN vowifi_runtime vr ON vr.device_id = r.resolved_device_id
			AND r.imsi <> '' AND vr.imsi = r.imsi
		WHERE r.row_number = 1
		ORDER BY r.message_time DESC, r.id DESC
		LIMIT ?`
	args = append(args, normalizedLimit(filter.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list SMS contacts: %w", err)
	}
	defer rows.Close()

	values := make([]SMSContact, 0)
	for rows.Next() {
		var value SMSContact
		var timestamp int64
		if err := rows.Scan(
			&value.DeviceID, &value.DeviceName, &value.ModemIMEI, &value.IMSI,
			&value.LocalPhone, &value.Peer, &value.DisplayName,
			&value.LastMessage, &timestamp, &value.Direction,
			&value.LastSMSID, &value.UnreadCount, &value.MessageCount,
		); err != nil {
			return nil, fmt.Errorf("scan SMS contact: %w", err)
		}
		value.LastTimestamp = time.Unix(timestamp, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SMS contacts: %w", err)
	}
	return values, nil
}

const smsMessageSelect = `
	SELECT id, message_id, device_id, modem_imei, imsi, peer, direction, body,
		message_time, status, source, parts_total, delivery_state, is_read,
		extra_json, created_at, updated_at
	FROM sms_messages`

func scanSMSMessage(row rowScanner) (SMSMessage, error) {
	var value SMSMessage
	var messageTime, createdAt, updatedAt int64
	var read int
	var extra string
	err := row.Scan(
		&value.ID, &value.MessageID, &value.DeviceID, &value.ModemIMEI, &value.IMSI,
		&value.Peer, &value.Direction, &value.Body, &messageTime,
		&value.Status, &value.Source, &value.PartsTotal,
		&value.DeliveryState, &read, &extra, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SMSMessage{}, ErrNotFound
	}
	if err != nil {
		return SMSMessage{}, err
	}
	value.Read = read != 0
	value.Extra = []byte(extra)
	value.Timestamp = time.Unix(messageTime, 0).UTC()
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func smsWhere(filter SMSFilter, prefix string) (string, []any) {
	clauses := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if filter.DeviceID != "" {
		clauses = append(clauses, prefix+`device_id = ?`)
		args = append(args, filter.DeviceID)
	}
	if filter.ModemIMEI != "" {
		clauses = append(clauses, prefix+`modem_imei = ?`)
		args = append(args, filter.ModemIMEI)
	}
	if filter.IMSIExact || filter.IMSI != "" {
		clauses = append(clauses, prefix+`imsi = ?`)
		args = append(args, filter.IMSI)
	}
	if filter.Peer != "" {
		clauses = append(clauses, prefix+`peer = ?`)
		args = append(args, filter.Peer)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, prefix+`message_time >= ?`)
		args = append(args, filter.Since.UTC().Unix())
	}
	if !filter.Until.IsZero() {
		clauses = append(clauses, prefix+`message_time < ?`)
		args = append(args, filter.Until.UTC().Unix())
	}
	if filter.BeforeID > 0 {
		clauses = append(clauses, prefix+`id < ?`)
		args = append(args, filter.BeforeID)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func smsHardwareKey(modemIMEI, deviceID string) string {
	if modemIMEI = strings.TrimSpace(modemIMEI); modemIMEI != "" {
		return modemIMEI
	}
	return "device:" + strings.TrimSpace(deviceID)
}

func normalizedLimit(value int) int {
	if value <= 0 {
		return 100
	}
	if value > 1000 {
		return 1000
	}
	return value
}
