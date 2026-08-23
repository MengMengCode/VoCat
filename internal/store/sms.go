package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type contextQueryExecer interface {
	contextExecer
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

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
	// segment text plus its UDH sequence in Extra. Fold each segment into a single
	// stored row so history, the web thread, and Telegram show one progressive
	// message that fills in as the remaining segments arrive.
	if isConcatSMSMessageID(value.MessageID) {
		hardwareKey := smsHardwareKey(value.ModemIMEI, value.DeviceID)
		existing, existingErr := scanSMSMessage(executor.QueryRowContext(
			ctx,
			smsMessageSelect+` WHERE
				COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id) = ?
				AND message_id = ?`,
			hardwareKey,
			value.MessageID,
		))
		if existingErr != nil && !errors.Is(existingErr, ErrNotFound) {
			return SMSMessage{}, fmt.Errorf("read existing concatenated SMS: %w", existingErr)
		}
		var existingExtra json.RawMessage
		if existingErr == nil {
			existingExtra = existing.Extra
		}
		mergedBody, mergedExtra, changed, mergeErr := mergeConcatSegment(existingExtra, value.Body, extra)
		if mergeErr != nil {
			return SMSMessage{}, fmt.Errorf("merge concatenated SMS segment: %w", mergeErr)
		}
		if existingErr == nil {
			if !changed {
				if value.Read != existing.Read {
					if _, err := executor.ExecContext(ctx, `UPDATE sms_messages SET is_read = ?, updated_at = ? WHERE id = ?`, boolInt(value.Read), now.Unix(), existing.ID); err != nil {
						return SMSMessage{}, fmt.Errorf("update concatenated SMS read state: %w", err)
					}
					existing.Read = value.Read
				}
				return existing, nil
			}
			// Once a concatenated message is complete, keep its durable row id
			// stable even if a later modem rescan presents one segment in a
			// slightly different decoded form. Notification consumers use this id
			// as their cursor; deleting and reinserting a completed row would make
			// an old stored SMS look new and repeatedly notify it on every scan.
			// Partial rows still receive a fresh id when they become complete so a
			// consumer that already skipped the partial row can surface it once.
			if ConcatSMSReadyToNotify(existing.MessageID, existing.Extra) && concatSMSKeepsDurableID(extra) {
				value.ID = existing.ID
				value.CreatedAt = existing.CreatedAt
				value.Read = value.Read || existing.Read
				if !existing.Timestamp.IsZero() &&
					(value.Timestamp.IsZero() || existing.Timestamp.Before(value.Timestamp)) {
					value.Timestamp = existing.Timestamp
				}
				value.Body = mergedBody
				extra = mergedExtra
			} else {
				// A new segment advanced the message. Replace the stale partial row so
				// the merged row receives a fresh durable id; the Telegram id-cursor
				// then surfaces the now-more-complete message exactly once. Carry
				// forward identity and history fields.
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
		value.Body = mergedBody
		extra = mergedExtra
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
			imsi = excluded.imsi,
			peer = excluded.peer,
			direction = excluded.direction,
			body = excluded.body,
			message_time = MIN(sms_messages.message_time, excluded.message_time),
			status = excluded.status,
			source = excluded.source,
			parts_total = excluded.parts_total,
			delivery_state = excluded.delivery_state,
			is_read = CASE
				WHEN sms_messages.is_read = 1 THEN 1
				ELSE excluded.is_read
			END,
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

// ApplySMSDeliveryReport attaches a TP-STATUS report to the newest matching
// outbound submission and advances its aggregate delivery state. Multipart
// messages become delivered only after every submitted part is reported.
func (s *Store) ApplySMSDeliveryReport(ctx context.Context, report SMSDeliveryReport) (SMSMessage, error) {
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
		ORDER BY created_at DESC, id DESC
		LIMIT 256`
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
	var target SMSMessage
	var targetExtra map[string]any
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
		target, targetExtra = candidate, extra
		break
	}
	if err := rows.Close(); err != nil {
		return SMSMessage{}, err
	}
	if target.ID == 0 {
		return SMSMessage{}, ErrNotFound
	}
	reports, _ := targetExtra["delivery_reports"].(map[string]any)
	if reports == nil {
		reports = make(map[string]any)
	}
	reportValue := map[string]any{
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
	reports[strconv.Itoa(report.MessageReference)] = reportValue
	targetExtra["delivery_reports"] = reports
	target.DeliveryState = aggregateSMSDeliveryState(targetExtra, reports)
	target.Extra, err = json.Marshal(targetExtra)
	if err != nil {
		return SMSMessage{}, fmt.Errorf("encode SMS delivery reports: %w", err)
	}
	target.UpdatedAt = time.Now().UTC()
	saved, err := saveSMSMessage(ctx, tx, target)
	if err != nil {
		return SMSMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return SMSMessage{}, fmt.Errorf("commit SMS delivery report: %w", err)
	}
	return saved, nil
}

func smsExtraHasReference(extra map[string]any, reference int) bool {
	if numberAsInt(extra["message_reference"]) == reference {
		return true
	}
	parts, _ := extra["part_results"].([]any)
	for _, value := range parts {
		part, _ := value.(map[string]any)
		if numberAsInt(part["reference"]) == reference ||
			numberAsInt(part["messageReference"]) == reference ||
			numberAsInt(part["message_reference"]) == reference {
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
		case "permanent_error", "failed", "rejected":
			return "failed"
		}
	}
	if delivered == len(references) {
		return "delivered"
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

func (s *Store) DeleteSMSThread(
	ctx context.Context,
	deviceID string,
	imsi string,
	peer string,
) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM sms_messages
		WHERE device_id = ? AND imsi = ? AND peer = ?
	`, deviceID, imsi, peer)
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

func (s *Store) MarkSMSMessagesRead(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, time.Now().UTC().Unix())
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf("UPDATE sms_messages SET is_read = 1, updated_at = ? WHERE id IN (%s) AND is_read = 0", strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mark SMS messages read: %w", err)
	}
	return nil
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
		LEFT JOIN vowifi_runtime vr ON vr.device_id = r.resolved_device_id
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
	if filter.IMSI != "" {
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
