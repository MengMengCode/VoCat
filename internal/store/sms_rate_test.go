package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveSMSSendIsGlobalAndRolling(t *testing.T) {
	database := openTestStore(t, ":memory:")
	now := time.Unix(1_800_000_000, 0).UTC()

	first, err := database.ReserveSMSSend(context.Background(), "ec20_1", 2, now)
	if err != nil || !first.Allowed || first.Used != 1 || first.Remaining != 1 {
		t.Fatalf("first reservation = %+v, %v", first, err)
	}
	second, err := database.ReserveSMSSend(context.Background(), "ec20_2", 2, now.Add(time.Second))
	if err != nil || !second.Allowed || second.Used != 2 || second.Remaining != 0 {
		t.Fatalf("second reservation = %+v, %v", second, err)
	}
	blocked, err := database.ReserveSMSSend(context.Background(), "another-device", 2, now.Add(2*time.Second))
	if err != nil || blocked.Allowed || blocked.Used != 2 || !blocked.ResetAt.Equal(now.Add(SMSRateWindow)) {
		t.Fatalf("blocked reservation = %+v, %v", blocked, err)
	}
	afterWindow, err := database.ReserveSMSSend(context.Background(), "ec20_1", 2, now.Add(SMSRateWindow+time.Second))
	if err != nil || !afterWindow.Allowed || afterWindow.Used != 1 {
		t.Fatalf("reservation after rolling window = %+v, %v", afterWindow, err)
	}
}

func TestReserveSMSSendCannotExceedLimitConcurrently(t *testing.T) {
	database := openTestStore(t, ":memory:")
	now := time.Unix(1_800_000_000, 0).UTC()
	const limit = 10
	const callers = 40
	var allowed atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			result, err := database.ReserveSMSSend(context.Background(), "device", limit, now)
			if err != nil {
				t.Errorf("reservation %d: %v", index, err)
				return
			}
			if result.Allowed {
				allowed.Add(1)
			}
		}(index)
	}
	wait.Wait()
	if got := allowed.Load(); got != limit {
		t.Fatalf("allowed reservations = %d, want %d", got, limit)
	}
}
