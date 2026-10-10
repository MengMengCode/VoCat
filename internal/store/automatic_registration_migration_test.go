package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMigration25PreservesTasksAndRuns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v24.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 24; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("schema %d: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 24"); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: raw}
	mustSaveDevice(t, legacy, "modem", "Modem")
	taskNextRun := time.Now().Add(time.Hour).Unix()
	nowUnix := time.Now().Unix()
	res, err := raw.ExecContext(ctx, `INSERT INTO automatic_tasks (
		name, enabled, device_id, profile_iccid, profile_aid, task_type,
		environment, interval_days, start_date, run_time, timezone, payload_json,
		retry_count, notify, next_run_at, last_run_at, last_status,
		last_error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"Existing SMS", 1, "modem", "card", "", "sms", "cellular", 1,
		"2026-10-02", "12:00", "UTC", `{"phone":"10086","message":"test"}`, 0, 0,
		taskNextRun, 0, "", "", nowUnix, nowUnix)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	_, err = raw.ExecContext(ctx, `INSERT INTO automatic_task_runs (
		task_id, device_id, scheduled_at, started_at, finished_at, status,
		attempts, output, error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, "modem", nowUnix, nowUnix, nowUnix, "success", 1, "old result", "", nowUnix, nowUnix)
	if err != nil {
		t.Fatal(err)
	}
	// Deleted IDs must not be reused by the rebuilt AUTOINCREMENT tables.
	if _, err := raw.ExecContext(ctx, "UPDATE sqlite_sequence SET seq = 100 WHERE name IN ('automatic_tasks','automatic_task_runs')"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	database := openTestStore(t, path)
	afterTask, err := database.AutomaticTask(ctx, taskID)
	if err != nil || afterTask.Name != "Existing SMS" || !afterTask.RevertProfile {
		t.Fatalf("task changed unexpectedly or revert_profile not migrated: %+v error=%v", afterTask, err)
	}
	afterRuns, err := database.ListAutomaticTaskRuns(ctx, 10)
	if err != nil || len(afterRuns) != 1 || afterRuns[0].Output != "old result" {
		t.Fatalf("runs changed: %+v error=%v", afterRuns, err)
	}
	task := afterTask
	task.ID, task.TaskType, task.Payload = 0, "cellular_attach", []byte(`{}`)
	task.NextRunAt = time.Now().Add(-time.Minute)
	added, err := database.SaveAutomaticTask(ctx, task)
	if err != nil || added.ID <= 100 {
		t.Fatalf("new task=%+v error=%v", added, err)
	}
	claimed, err := database.ClaimDueAvailableAutomaticTasks(ctx, time.Now(), 10)
	if err != nil || len(claimed) != 1 || claimed[0].TaskID != added.ID || claimed[0].ID <= 100 {
		t.Fatalf("claims=%+v error=%v", claimed, err)
	}
	var table string
	if err := database.db.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&table); err != sql.ErrNoRows {
		t.Fatalf("foreign key check=%v", err)
	}
	if err := database.DeleteAutomaticTask(ctx, afterTask.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := database.ListAutomaticTaskRuns(ctx, 10)
	if err != nil || len(remaining) != 1 || remaining[0].TaskID != added.ID {
		t.Fatalf("cascade deletion: runs=%+v error=%v", remaining, err)
	}
}

func TestMigration26AddsRevertProfile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v25.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	defer raw.Close()
	for version := 1; version <= 25; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("schema %d: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 25"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO devices (id, name, created_at, updated_at) VALUES ('d1', 'D1', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO automatic_tasks (
		name, enabled, device_id, profile_iccid, profile_aid, task_type,
		environment, interval_days, start_date, run_time, timezone, payload_json,
		retry_count, notify, next_run_at, last_run_at, last_status,
		last_error, created_at, updated_at
	) VALUES ('Test Task', 1, 'd1', 'iccid1', '', 'sms', 'cellular', 1, '2026-10-10', '12:00', 'UTC', '{}', 0, 0, 1000, 0, '', '', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db := openTestStore(t, path)
	task, err := db.AutomaticTask(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !task.RevertProfile {
		t.Fatalf("expected RevertProfile to default to true after migration 26, got false")
	}
}
