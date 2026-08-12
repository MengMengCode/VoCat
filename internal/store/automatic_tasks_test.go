package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutomaticTasksAreClaimedInDeviceQueueOrderAndAdvanceSchedule(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, filepath.Join(t.TempDir(), "automatic-tasks.db"))
	mustSaveDevice(t, database, "ec20", "EC20")
	now := time.Now().UTC().Truncate(time.Second)
	for index := 0; index < 2; index++ {
		payload, _ := json.Marshal(map[string]any{"phone": "10086", "message": "test"})
		if _, err := database.SaveAutomaticTask(ctx, AutomaticTask{
			Name: "task", Enabled: true, DeviceID: "ec20", ProfileICCID: "8944100000000000000",
			TaskType: "sms", Environment: "vowifi", IntervalDays: 2,
			StartDate: "2026-08-10", RunTime: "12:00", Timezone: "Asia/Shanghai", Payload: payload,
			NextRunAt: now.Add(time.Duration(index-2) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := database.ClaimDueAutomaticTasks(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].DeviceID != "ec20" || runs[1].DeviceID != "ec20" || runs[0].TaskID >= runs[1].TaskID {
		t.Fatalf("claimed runs = %+v", runs)
	}
	tasks, err := database.ListAutomaticTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if !task.NextRunAt.After(now) {
			t.Fatalf("task %d next run was not advanced: %v", task.ID, task.NextRunAt)
		}
	}
	second, err := database.ClaimDueAutomaticTasks(ctx, now, 10)
	if err != nil || len(second) != 0 {
		t.Fatalf("same schedule claimed twice: %+v, %v", second, err)
	}
}

func TestDeletingAutomaticTaskRemovesRunHistory(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, filepath.Join(t.TempDir(), "automatic-task-delete.db"))
	mustSaveDevice(t, database, "ec20", "EC20")
	task, err := database.SaveAutomaticTask(ctx, AutomaticTask{
		Name: "task", Enabled: true, DeviceID: "ec20", ProfileICCID: "one",
		TaskType: "call", Environment: "cellular", IntervalDays: 1,
		StartDate: "2026-08-10", RunTime: "12:00", Timezone: "Asia/Shanghai", Payload: []byte(`{"phone":"10086","duration_seconds":10}`),
		NextRunAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueAutomaticTaskNow(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteAutomaticTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	runs, err := database.ListAutomaticTaskRuns(ctx, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("orphan runs = %+v, %v", runs, err)
	}
}

func TestListAutomaticTaskRunsPaginated(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, filepath.Join(t.TempDir(), "automatic-task-runs-page.db"))
	mustSaveDevice(t, database, "ec20", "EC20")
	task, err := database.SaveAutomaticTask(ctx, AutomaticTask{
		Name: "task", Enabled: true, DeviceID: "ec20", ProfileICCID: "one",
		TaskType: "call", Environment: "cellular", IntervalDays: 1,
		StartDate: "2026-08-10", RunTime: "12:00", Timezone: "Asia/Shanghai", Payload: []byte(`{"phone":"10086","duration_seconds":10}`),
		NextRunAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 5; index++ {
		if _, err := database.QueueAutomaticTaskNow(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	first, total, err := database.ListAutomaticTaskRunsPaginated(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(first) != 2 {
		t.Fatalf("first page: total = %d, runs = %+v", total, first)
	}
	if first[0].ID <= first[1].ID {
		t.Fatalf("runs not newest-first: %+v", first)
	}

	last, total, err := database.ListAutomaticTaskRunsPaginated(ctx, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(last) != 1 {
		t.Fatalf("last page: total = %d, runs = %+v", total, last)
	}

	// Out-of-range paging inputs are clamped to defaults, not errors.
	all, total, err := database.ListAutomaticTaskRunsPaginated(ctx, 0, -5)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(all) != 5 {
		t.Fatalf("clamped page: total = %d, runs = %+v", total, all)
	}
}

func TestRecoverAutomaticTaskRunsFailsRunningAndReturnsQueued(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, filepath.Join(t.TempDir(), "automatic-task-recovery.db"))
	mustSaveDevice(t, database, "ec20", "EC20")
	task, err := database.SaveAutomaticTask(ctx, AutomaticTask{
		Name: "task", Enabled: true, DeviceID: "ec20", ProfileICCID: "one",
		TaskType: "call", Environment: "cellular", IntervalDays: 1,
		StartDate: "2026-08-10", RunTime: "12:00", Timezone: "Asia/Shanghai", Payload: []byte(`{"phone":"10086","duration_seconds":10}`),
		NextRunAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := database.QueueAutomaticTaskNow(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	running.Status = "running"
	running.StartedAt = time.Now().UTC().Add(-time.Minute)
	running.Attempts = 1
	if err := database.UpdateAutomaticTaskRun(ctx, running); err != nil {
		t.Fatal(err)
	}
	queued, err := database.QueueAutomaticTaskNow(ctx, task)
	if err != nil {
		t.Fatal(err)
	}

	recoveredAt := time.Now().UTC().Truncate(time.Second)
	recovered, err := database.RecoverAutomaticTaskRuns(ctx, recoveredAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].ID != queued.ID || recovered[0].Status != "queued" {
		t.Fatalf("recovered queued runs = %+v", recovered)
	}
	runs, err := database.ListAutomaticTaskRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundRunning := false
	for _, run := range runs {
		if run.ID == running.ID {
			foundRunning = true
			if run.Status != "failed" || run.FinishedAt.IsZero() || !strings.Contains(run.Error, "service restarted") {
				t.Fatalf("recovered running run = %+v", run)
			}
		}
	}
	if !foundRunning {
		t.Fatal("running run was not found after recovery")
	}
	recoveredTask, err := database.AutomaticTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredTask.LastStatus != "failed" || !strings.Contains(recoveredTask.LastError, "service restarted") {
		t.Fatalf("recovered task status = %+v", recoveredTask)
	}
}
