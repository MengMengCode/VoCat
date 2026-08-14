package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const automaticTaskSelect = `
	SELECT id, name, enabled, device_id, profile_iccid, profile_aid,
		task_type, environment, interval_days, start_date, run_time,
		timezone, payload_json, retry_count, notify, next_run_at, last_run_at,
		last_status, last_error, created_at, updated_at
	FROM automatic_tasks`

func (s *Store) SaveAutomaticTask(ctx context.Context, value AutomaticTask) (AutomaticTask, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(value.Timezone) == "" {
		value.Timezone = time.Local.String()
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	if len(value.Payload) == 0 {
		value.Payload = []byte(`{}`)
	}
	if value.ID == 0 {
		result, err := s.db.ExecContext(ctx, `INSERT INTO automatic_tasks (
			name, enabled, device_id, profile_iccid, profile_aid, task_type,
			environment, interval_days, start_date, run_time, timezone, payload_json,
			retry_count, notify, next_run_at, last_run_at, last_status,
			last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			strings.TrimSpace(value.Name), value.Enabled, strings.TrimSpace(value.DeviceID),
			strings.TrimSpace(value.ProfileICCID), strings.TrimSpace(value.ProfileAID),
			value.TaskType, value.Environment, value.IntervalDays, value.StartDate,
			value.RunTime, value.Timezone, string(value.Payload), value.RetryCount, value.Notify,
			value.NextRunAt.Unix(), unixOrZero(value.LastRunAt), value.LastStatus,
			value.LastError, value.CreatedAt.Unix(), value.UpdatedAt.Unix())
		if err != nil {
			return AutomaticTask{}, fmt.Errorf("create automatic task: %w", err)
		}
		value.ID, _ = result.LastInsertId()
	} else {
		result, err := s.db.ExecContext(ctx, `UPDATE automatic_tasks SET
			name = ?, enabled = ?, device_id = ?, profile_iccid = ?, profile_aid = ?,
			task_type = ?, environment = ?, interval_days = ?, start_date = ?,
			run_time = ?, timezone = ?, payload_json = ?, retry_count = ?, notify = ?,
			next_run_at = ?, updated_at = ? WHERE id = ?`,
			strings.TrimSpace(value.Name), value.Enabled, strings.TrimSpace(value.DeviceID),
			strings.TrimSpace(value.ProfileICCID), strings.TrimSpace(value.ProfileAID),
			value.TaskType, value.Environment, value.IntervalDays, value.StartDate,
			value.RunTime, value.Timezone, string(value.Payload), value.RetryCount, value.Notify,
			value.NextRunAt.Unix(), value.UpdatedAt.Unix(), value.ID)
		if err != nil {
			return AutomaticTask{}, fmt.Errorf("update automatic task %d: %w", value.ID, err)
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return AutomaticTask{}, ErrNotFound
		}
	}
	return s.AutomaticTask(ctx, value.ID)
}

func (s *Store) AutomaticTask(ctx context.Context, id int64) (AutomaticTask, error) {
	return scanAutomaticTask(s.db.QueryRowContext(ctx, automaticTaskSelect+` WHERE id = ?`, id))
}

func (s *Store) ListAutomaticTasks(ctx context.Context) ([]AutomaticTask, error) {
	rows, err := s.db.QueryContext(ctx, automaticTaskSelect+` ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list automatic tasks: %w", err)
	}
	defer rows.Close()
	var result []AutomaticTask
	for rows.Next() {
		value, scanErr := scanAutomaticTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) DeleteAutomaticTask(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM automatic_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete automatic task %d: %w", id, err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ClaimDueAutomaticTasks(ctx context.Context, now time.Time, limit int) ([]AutomaticTaskRun, error) {
	return s.claimDueAutomaticTasks(ctx, now, limit, false)
}

// ClaimDueAvailableAutomaticTasks excludes task types and environments that
// are not exposed in the standard product surface.
func (s *Store) ClaimDueAvailableAutomaticTasks(ctx context.Context, now time.Time, limit int) ([]AutomaticTaskRun, error) {
	return s.claimDueAutomaticTasks(ctx, now, limit, true)
}

func (s *Store) claimDueAutomaticTasks(ctx context.Context, now time.Time, limit int, availableOnly bool) ([]AutomaticTaskRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	availability := ""
	if availableOnly {
		availability = " AND task_type <> 'public_ip' AND environment <> 'cellular'"
	}
	rows, err := tx.QueryContext(ctx, automaticTaskSelect+`
		WHERE enabled = 1 AND next_run_at <= ?`+availability+` ORDER BY next_run_at, id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	var tasks []AutomaticTask
	for rows.Next() {
		task, scanErr := scanAutomaticTask(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		tasks = append(tasks, task)
	}
	rows.Close()
	result := make([]AutomaticTaskRun, 0, len(tasks))
	for _, task := range tasks {
		next := task.NextRunAt
		location := time.Local
		if loaded, loadErr := time.LoadLocation(task.Timezone); loadErr == nil {
			location = loaded
		}
		for !next.After(now) {
			next = next.In(location).AddDate(0, 0, task.IntervalDays).UTC()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE automatic_tasks SET next_run_at = ?, updated_at = ? WHERE id = ?`, next.Unix(), now.Unix(), task.ID); err != nil {
			return nil, err
		}
		created, createErr := tx.ExecContext(ctx, `INSERT INTO automatic_task_runs (
			task_id, device_id, scheduled_at, status, created_at, updated_at
		) VALUES (?, ?, ?, 'queued', ?, ?)`, task.ID, task.DeviceID, task.NextRunAt.Unix(), now.Unix(), now.Unix())
		if createErr != nil {
			return nil, createErr
		}
		runID, _ := created.LastInsertId()
		result = append(result, AutomaticTaskRun{ID: runID, TaskID: task.ID, DeviceID: task.DeviceID, ScheduledAt: task.NextRunAt, Status: "queued", CreatedAt: now, UpdatedAt: now})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) QueueAutomaticTaskNow(ctx context.Context, task AutomaticTask) (AutomaticTaskRun, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO automatic_task_runs (
		task_id, device_id, scheduled_at, status, created_at, updated_at
	) VALUES (?, ?, ?, 'queued', ?, ?)`, task.ID, task.DeviceID, now.Unix(), now.Unix(), now.Unix())
	if err != nil {
		return AutomaticTaskRun{}, fmt.Errorf("queue automatic task: %w", err)
	}
	id, _ := result.LastInsertId()
	return AutomaticTaskRun{ID: id, TaskID: task.ID, DeviceID: task.DeviceID, ScheduledAt: now, Status: "queued", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) UpdateAutomaticTaskRun(ctx context.Context, run AutomaticTaskRun) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE automatic_task_runs SET
		started_at = ?, finished_at = ?, status = ?, attempts = ?, output = ?, error = ?, updated_at = ?
		WHERE id = ?`, unixOrZero(run.StartedAt), unixOrZero(run.FinishedAt), run.Status,
		run.Attempts, run.Output, run.Error, now.Unix(), run.ID)
	if err != nil {
		return fmt.Errorf("update automatic task run %d: %w", run.ID, err)
	}
	if run.Status == "success" || run.Status == "failed" {
		_, err = s.db.ExecContext(ctx, `UPDATE automatic_tasks SET
			last_run_at = ?, last_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
			run.FinishedAt.Unix(), run.Status, run.Error, now.Unix(), run.TaskID)
	}
	return err
}

// RecoverAutomaticTaskRuns reconciles durable run records with the in-memory
// scheduler after a process restart. Running work cannot still be executing,
// while queued work is safe to put back onto the per-device queues.
func (s *Store) RecoverAutomaticTaskRuns(ctx context.Context, now time.Time) ([]AutomaticTaskRun, error) {
	const restartError = "service restarted before the automatic task completed"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE automatic_task_runs SET
		status = 'failed', finished_at = ?, error = ?, updated_at = ?
		WHERE status = 'running'`, now.Unix(), restartError, now.Unix()); err != nil {
		return nil, fmt.Errorf("recover running automatic tasks: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE automatic_tasks SET
		last_run_at = ?, last_status = 'failed', last_error = ?, updated_at = ?
		WHERE id IN (
			SELECT task_id FROM automatic_task_runs
			WHERE status = 'failed' AND error = ? AND finished_at = ?
		)`, now.Unix(), restartError, now.Unix(), restartError, now.Unix()); err != nil {
		return nil, fmt.Errorf("recover automatic task status: %w", err)
	}
	rows, err := tx.QueryContext(ctx, automaticTaskRunSelect+` WHERE status = 'queued' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("recover queued automatic tasks: %w", err)
	}
	queued, err := scanAutomaticTaskRuns(rows)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return queued, nil
}

const automaticTaskRunSelect = `
	SELECT id, task_id, device_id, scheduled_at, started_at, finished_at,
		status, attempts, output, error, created_at, updated_at
	FROM automatic_task_runs`

func (s *Store) ListAutomaticTaskRuns(ctx context.Context, limit int) ([]AutomaticTaskRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, automaticTaskRunSelect+` ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanAutomaticTaskRuns(rows)
}

// ListAutomaticTaskRunsPaginated returns one page of runs (newest first) plus
// the total run count, so the UI can page through the full history instead of
// a fixed recent window.
func (s *Store) ListAutomaticTaskRunsPaginated(ctx context.Context, limit, offset int) ([]AutomaticTaskRun, int, error) {
	return s.listAutomaticTaskRunsPaginated(ctx, limit, offset, "")
}

// ListAvailableAutomaticTaskRunsPaginated omits history belonging to task
// types and environments that are not exposed in the standard product surface.
func (s *Store) ListAvailableAutomaticTaskRunsPaginated(ctx context.Context, limit, offset int) ([]AutomaticTaskRun, int, error) {
	const where = ` WHERE task_id IN (
		SELECT id FROM automatic_tasks WHERE task_type <> 'public_ip' AND environment <> 'cellular'
	)`
	return s.listAutomaticTaskRunsPaginated(ctx, limit, offset, where)
}

func (s *Store) listAutomaticTaskRunsPaginated(ctx context.Context, limit, offset int, where string) ([]AutomaticTaskRun, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	total := 0
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automatic_task_runs`+where).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count automatic task runs: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, automaticTaskRunSelect+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	runs, err := scanAutomaticTaskRuns(rows)
	if err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

func scanAutomaticTaskRuns(rows *sql.Rows) ([]AutomaticTaskRun, error) {
	defer rows.Close()
	var result []AutomaticTaskRun
	for rows.Next() {
		var value AutomaticTaskRun
		var scheduled, started, finished, created, updated int64
		if err := rows.Scan(&value.ID, &value.TaskID, &value.DeviceID, &scheduled, &started,
			&finished, &value.Status, &value.Attempts, &value.Output, &value.Error, &created, &updated); err != nil {
			return nil, err
		}
		value.ScheduledAt, value.StartedAt, value.FinishedAt = time.Unix(scheduled, 0).UTC(), timeFromUnix(started), timeFromUnix(finished)
		value.CreatedAt, value.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanAutomaticTask(row rowScanner) (AutomaticTask, error) {
	var value AutomaticTask
	var enabled, notify bool
	var payload string
	var nextRun, lastRun, created, updated int64
	if err := row.Scan(&value.ID, &value.Name, &enabled, &value.DeviceID, &value.ProfileICCID,
		&value.ProfileAID, &value.TaskType, &value.Environment, &value.IntervalDays,
		&value.StartDate, &value.RunTime, &value.Timezone, &payload, &value.RetryCount, &notify,
		&nextRun, &lastRun, &value.LastStatus, &value.LastError, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AutomaticTask{}, ErrNotFound
		}
		return AutomaticTask{}, err
	}
	value.Enabled, value.Notify = enabled, notify
	value.Payload = []byte(payload)
	value.NextRunAt, value.LastRunAt = time.Unix(nextRun, 0).UTC(), timeFromUnix(lastRun)
	value.CreatedAt, value.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return value, nil
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func timeFromUnix(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}
