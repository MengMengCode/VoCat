package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxLogEvents is the hard storage ceiling. Every new row beyond this limit
// replaces the oldest row regardless of the optional, stricter retention rule.
const MaxLogEvents = 10000

func (s *Store) AppendAuditEvent(ctx context.Context, value AuditEvent) (AuditEvent, error) {
	value.Action = strings.TrimSpace(value.Action)
	if value.Action == "" {
		return AuditEvent{}, errors.New("audit action is required")
	}
	details, err := normalizeJSONObject(value.Details)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("normalize audit details: %w", err)
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events (
			actor, action, entity_type, entity_id, outcome, remote_addr,
			details_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		value.Actor, value.Action, value.EntityType, value.EntityID,
		value.Outcome, value.RemoteAddr, string(details), value.CreatedAt.Unix(),
	)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("append audit event: %w", err)
	}
	value.ID, err = result.LastInsertId()
	if err != nil {
		return AuditEvent{}, fmt.Errorf("read audit event id: %w", err)
	}
	value.Details = details
	return value, nil
}

func (s *Store) ListAuditEvents(ctx context.Context, filter AuditFilter) ([]AuditEvent, error) {
	clauses := make([]string, 0, 7)
	args := make([]any, 0, 8)
	if filter.Actor != "" {
		clauses = append(clauses, `actor = ?`)
		args = append(args, filter.Actor)
	}
	if filter.Action != "" {
		clauses = append(clauses, `action = ?`)
		args = append(args, filter.Action)
	}
	if filter.EntityType != "" {
		clauses = append(clauses, `entity_type = ?`)
		args = append(args, filter.EntityType)
	}
	if filter.EntityID != "" {
		clauses = append(clauses, `entity_id = ?`)
		args = append(args, filter.EntityID)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, `created_at >= ?`)
		args = append(args, filter.Since.UTC().Unix())
	}
	if !filter.Until.IsZero() {
		clauses = append(clauses, `created_at < ?`)
		args = append(args, filter.Until.UTC().Unix())
	}
	if filter.BeforeID > 0 {
		clauses = append(clauses, `id < ?`)
		args = append(args, filter.BeforeID)
	}
	query := auditEventSelect
	if len(clauses) > 0 {
		query += ` WHERE ` + strings.Join(clauses, ` AND `)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, normalizedLimit(filter.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	values := make([]AuditEvent, 0)
	for rows.Next() {
		value, err := auditEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return values, nil
}

const auditEventSelect = `
	SELECT id, actor, action, entity_type, entity_id, outcome,
		remote_addr, details_json, created_at
	FROM audit_events`

func auditEvent(row rowScanner) (AuditEvent, error) {
	var value AuditEvent
	var details string
	var createdAt int64
	err := row.Scan(
		&value.ID, &value.Actor, &value.Action, &value.EntityType,
		&value.EntityID, &value.Outcome, &value.RemoteAddr, &details,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditEvent{}, ErrNotFound
	}
	if err != nil {
		return AuditEvent{}, err
	}
	value.Details = []byte(details)
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	return value, nil
}

func (s *Store) AppendLogEvent(ctx context.Context, value LogEvent) (LogEvent, error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	value.Level = strings.ToLower(strings.TrimSpace(value.Level))
	if value.Level == "" {
		return LogEvent{}, errors.New("log level is required")
	}
	if strings.TrimSpace(value.Message) == "" {
		return LogEvent{}, errors.New("log message is required")
	}
	fields, err := normalizeJSONValue(value.Fields)
	if err != nil {
		return LogEvent{}, fmt.Errorf("normalize log fields: %w", err)
	}
	if value.Time.IsZero() {
		value.Time = time.Now().UTC()
	}
	value.Fields = fields
	if !s.logClearedAt.IsZero() && !value.Time.After(s.logClearedAt) {
		// The entry was queued before a user cleared the log. Silently discard it
		// so an in-flight persistence worker cannot resurrect cleared history.
		return value, nil
	}
	if s.logMinLevel != "" && isLogLevelBelow(value.Level, s.logMinLevel) {
		return value, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LogEvent{}, fmt.Errorf("begin log append: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO log_events (event_time, level, message, caller, fields_json)
		VALUES (?, ?, ?, ?, ?)
	`, value.Time.Unix(), value.Level, value.Message, value.Caller, string(fields))
	if err != nil {
		return LogEvent{}, fmt.Errorf("append log event: %w", err)
	}
	value.ID, err = result.LastInsertId()
	if err != nil {
		return LogEvent{}, fmt.Errorf("read log event id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM log_events
		WHERE id <= COALESCE((
			SELECT id FROM log_events ORDER BY id DESC LIMIT 1 OFFSET ?
		), 0)
	`, MaxLogEvents); err != nil {
		return LogEvent{}, fmt.Errorf("enforce log event limit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return LogEvent{}, fmt.Errorf("commit log append: %w", err)
	}
	return value, nil
}

func (s *Store) ListLogEvents(ctx context.Context, filter LogFilter) ([]LogEvent, error) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if filter.Level != "" {
		clauses = append(clauses, `level = ?`)
		args = append(args, strings.ToLower(filter.Level))
	}
	if filter.ExcludeMessage != "" {
		clauses = append(clauses, `LOWER(TRIM(message)) <> ?`)
		args = append(args, strings.ToLower(strings.TrimSpace(filter.ExcludeMessage)))
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, `event_time >= ?`)
		args = append(args, filter.Since.UTC().Unix())
	}
	if !filter.Until.IsZero() {
		clauses = append(clauses, `event_time < ?`)
		args = append(args, filter.Until.UTC().Unix())
	}
	if filter.BeforeID > 0 {
		clauses = append(clauses, `id < ?`)
		args = append(args, filter.BeforeID)
	}
	query := logEventSelect
	if len(clauses) > 0 {
		query += ` WHERE ` + strings.Join(clauses, ` AND `)
	}
	query += ` ORDER BY event_time DESC, id DESC LIMIT ?`
	args = append(args, normalizedLimit(filter.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list log events: %w", err)
	}
	defer rows.Close()
	values := make([]LogEvent, 0)
	for rows.Next() {
		value, err := logEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan log event: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate log events: %w", err)
	}
	return values, nil
}

const logEventSelect = `
	SELECT id, event_time, level, message, caller, fields_json
	FROM log_events`

func logEvent(row rowScanner) (LogEvent, error) {
	var value LogEvent
	var eventTime int64
	var fields string
	err := row.Scan(
		&value.ID, &eventTime, &value.Level, &value.Message,
		&value.Caller, &fields,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return LogEvent{}, ErrNotFound
	}
	if err != nil {
		return LogEvent{}, err
	}
	value.Time = time.Unix(eventTime, 0).UTC()
	value.Fields = []byte(fields)
	return value, nil
}

func (s *Store) PruneAuditEvents(ctx context.Context, before time.Time) (int64, error) {
	return deleteEventsBefore(ctx, s.db, `audit_events`, `created_at`, before)
}

func (s *Store) PruneLogEvents(ctx context.Context, before time.Time) (int64, error) {
	return deleteEventsBefore(ctx, s.db, `log_events`, `event_time`, before)
}

// CountLogEvents returns how many application log rows are persisted.
func (s *Store) CountLogEvents(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_events`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count log events: %w", err)
	}
	return count, nil
}

// ClearLogEvents permanently removes all persisted logs. Entries timestamped
// at or before clearedAt are also rejected if they were already queued by the
// asynchronous persistence worker.
func (s *Store) ClearLogEvents(ctx context.Context, clearedAt time.Time) (int64, error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if clearedAt.IsZero() {
		clearedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM log_events`)
	if err != nil {
		return 0, fmt.Errorf("clear log events: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read cleared log count: %w", err)
	}
	if clearedAt.After(s.logClearedAt) {
		s.logClearedAt = clearedAt
	}
	return affected, nil
}

// PruneLogEventsToCount keeps only the newest `keep` log rows, deleting the
// rest. keep <= 0 deletes everything.
func (s *Store) PruneLogEventsToCount(ctx context.Context, keep int) (int64, error) {
	if keep < 0 {
		keep = 0
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM log_events WHERE id NOT IN (
			SELECT id FROM log_events ORDER BY id DESC LIMIT ?
		)
	`, keep)
	if err != nil {
		return 0, fmt.Errorf("prune log events to count: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned log count: %w", err)
	}
	return affected, nil
}

// SetLogMinLevel configures the minimum log severity to persist.
// Events below this level are ignored by AppendLogEvent.
func (s *Store) SetLogMinLevel(level string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logMinLevel = strings.ToLower(strings.TrimSpace(level))
}

// LogMinLevel returns the current minimum log level threshold.
func (s *Store) LogMinLevel() string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return s.logMinLevel
}

// PruneLogEventsBelowLevel deletes persisted log events with a severity lower
// than minLevel ("debug", "info", "warn", "error").
func (s *Store) PruneLogEventsBelowLevel(ctx context.Context, minLevel string) (int64, error) {
	minSev := logLevelSeverity(minLevel)
	if minSev <= 1 {
		return 0, nil
	}
	var excluded []string
	for _, lvl := range []string{"debug", "info", "warn", "warning"} {
		if logLevelSeverity(lvl) < minSev {
			excluded = append(excluded, lvl)
		}
	}
	if len(excluded) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(excluded))
	args := make([]any, len(excluded))
	for i, lvl := range excluded {
		placeholders[i] = "?"
		args[i] = lvl
	}
	query := fmt.Sprintf(`DELETE FROM log_events WHERE LOWER(level) IN (%s)`, strings.Join(placeholders, ","))
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("prune log events below level: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned log count: %w", err)
	}
	return affected, nil
}

func isLogLevelBelow(level, minLevel string) bool {
	return logLevelSeverity(level) < logLevelSeverity(minLevel)
}

func logLevelSeverity(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error":
		return 4
	case "warn", "warning":
		return 3
	case "info":
		return 2
	case "debug":
		return 1
	default:
		return 2
	}
}

func deleteEventsBefore(
	ctx context.Context,
	executor contextExecer,
	table string,
	column string,
	before time.Time,
) (int64, error) {
	// table and column are internal constants from the callers above.
	result, err := executor.ExecContext(
		ctx,
		`DELETE FROM `+table+` WHERE `+column+` < ?`,
		before.UTC().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("prune %s: %w", table, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned %s count: %w", table, err)
	}
	return affected, nil
}

// PruneEvents removes audit and application logs atomically.
func (s *Store) PruneEvents(
	ctx context.Context,
	auditBefore time.Time,
	logBefore time.Time,
) (auditCount int64, logCount int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin event pruning: %w", err)
	}
	defer tx.Rollback()
	auditCount, err = deleteEventsBefore(ctx, tx, `audit_events`, `created_at`, auditBefore)
	if err != nil {
		return 0, 0, err
	}
	logCount, err = deleteEventsBefore(ctx, tx, `log_events`, `event_time`, logBefore)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit event pruning: %w", err)
	}
	return auditCount, logCount, nil
}
