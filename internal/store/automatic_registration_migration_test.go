package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
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
	task, err := legacy.SaveAutomaticTask(ctx, AutomaticTask{Name: "Existing SMS", Enabled: true, DeviceID: "modem", ProfileICCID: "card", TaskType: "sms", Environment: "cellular", IntervalDays: 1, StartDate: "2026-10-02", RunTime: "12:00", Timezone: "UTC", Payload: []byte(`{"phone":"10086","message":"test"}`), NextRunAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := legacy.QueueAutomaticTaskNow(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Output = "success", "old result"
	if err := legacy.UpdateAutomaticTaskRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	beforeTask, err := legacy.AutomaticTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := legacy.ListAutomaticTaskRuns(ctx, 10)
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
	afterTask, err := database.AutomaticTask(ctx, task.ID)
	if err != nil || !reflect.DeepEqual(beforeTask, afterTask) {
		t.Fatalf("task changed: %+v error=%v", afterTask, err)
	}
	afterRuns, err := database.ListAutomaticTaskRuns(ctx, 10)
	if err != nil || !reflect.DeepEqual(beforeRuns, afterRuns) {
		t.Fatalf("runs changed: %+v error=%v", afterRuns, err)
	}
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
	if err := database.DeleteAutomaticTask(ctx, beforeTask.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := database.ListAutomaticTaskRuns(ctx, 10)
	if err != nil || len(remaining) != 1 || remaining[0].TaskID != added.ID {
		t.Fatalf("cascade deletion: runs=%+v error=%v", remaining, err)
	}
}
