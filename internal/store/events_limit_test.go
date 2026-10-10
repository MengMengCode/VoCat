package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestAppendLogEventEnforcesHardLimit(t *testing.T) {
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.db.ExecContext(context.Background(), `
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value <= ?
		)
		INSERT INTO log_events(event_time, level, message, caller, fields_json)
		SELECT value, 'info', 'seed-' || value, '', '{}' FROM sequence
	`, MaxLogEvents); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendLogEvent(context.Background(), LogEvent{
		Level: "info", Message: "newest", Time: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	count, err := database.CountLogEvents(context.Background())
	if err != nil || count != MaxLogEvents {
		t.Fatalf("CountLogEvents = %d, %v; want %d", count, err, MaxLogEvents)
	}
	logs, err := database.ListLogEvents(context.Background(), LogFilter{Limit: 1})
	if err != nil || len(logs) != 1 || logs[0].Message != "newest" {
		t.Fatalf("newest log = %#v, %v", logs, err)
	}
}

func TestClearLogEventsRejectsAlreadyQueuedEntries(t *testing.T) {
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cutoff := time.Now().UTC()
	if _, err := database.AppendLogEvent(context.Background(), LogEvent{
		Level: "info", Message: "existing", Time: cutoff.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err := database.ClearLogEvents(context.Background(), cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("ClearLogEvents = %d, %v", deleted, err)
	}
	late, err := database.AppendLogEvent(context.Background(), LogEvent{
		Level: "info", Message: "queued-before-clear", Time: cutoff.Add(-time.Millisecond),
	})
	if err != nil || late.ID != 0 {
		t.Fatalf("old queued append = %+v, %v", late, err)
	}
	if _, err := database.AppendLogEvent(context.Background(), LogEvent{
		Level: "info", Message: fmt.Sprintf("new-%d", MaxLogEvents), Time: cutoff.Add(time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	count, err := database.CountLogEvents(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("CountLogEvents = %d, %v; want 1", count, err)
	}
}

func TestAppendLogEventMinLevelFilter(t *testing.T) {
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	database.SetLogMinLevel("warn")
	ctx := context.Background()

	// Debug should be filtered out
	debugEvt, err := database.AppendLogEvent(ctx, LogEvent{Level: "debug", Message: "debug msg"})
	if err != nil || debugEvt.ID != 0 {
		t.Fatalf("debug append should be filtered out, got ID=%d err=%v", debugEvt.ID, err)
	}

	// Info should be filtered out
	infoEvt, err := database.AppendLogEvent(ctx, LogEvent{Level: "info", Message: "info msg"})
	if err != nil || infoEvt.ID != 0 {
		t.Fatalf("info append should be filtered out, got ID=%d err=%v", infoEvt.ID, err)
	}

	// Warn should be appended
	warnEvt, err := database.AppendLogEvent(ctx, LogEvent{Level: "warn", Message: "warn msg"})
	if err != nil || warnEvt.ID == 0 {
		t.Fatalf("warn append should succeed, got ID=%d err=%v", warnEvt.ID, err)
	}

	// Error should be appended
	errEvt, err := database.AppendLogEvent(ctx, LogEvent{Level: "error", Message: "error msg"})
	if err != nil || errEvt.ID == 0 {
		t.Fatalf("error append should succeed, got ID=%d err=%v", errEvt.ID, err)
	}

	count, err := database.CountLogEvents(ctx)
	if err != nil || count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestPruneLogEventsBelowLevel(t *testing.T) {
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	// Insert rows directly without filter
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		if _, err := database.AppendLogEvent(ctx, LogEvent{Level: lvl, Message: lvl + " message"}); err != nil {
			t.Fatal(err)
		}
	}

	// Prune below warn -> should delete debug and info (2 rows)
	pruned, err := database.PruneLogEventsBelowLevel(ctx, "warn")
	if err != nil {
		t.Fatalf("prune error: %v", err)
	}
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2", pruned)
	}

	remaining, err := database.ListLogEvents(ctx, LogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining logs count = %d, want 2", len(remaining))
	}
	for _, r := range remaining {
		if r.Level != "warn" && r.Level != "error" {
			t.Fatalf("unexpected remaining level: %s", r.Level)
		}
	}
}
