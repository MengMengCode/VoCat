package device

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/modem"
)

// recoveryResultClient exposes the new card after a rejected reset, allowing
// ICCID verification to succeed even though recovery itself failed.
type recoveryResultClient struct {
	mu sync.Mutex
	recoveringSIMClient
	resetErr       error
	resetAttempted bool
}

func (c *recoveryResultClient) Execute(ctx context.Context, command string) (modem.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case command == `AT+CSIM=10,"0070000001"`:
		return okResponse(`+CSIM: 6,"019000"`), nil
	case strings.HasPrefix(command, "AT+CSIM="):
		if strings.Contains(command, "BF31") {
			return okResponse(`+CSIM: 16,"BF31038001009000"`), nil
		}
		return okResponse(`+CSIM: 4,"9000"`), nil
	case command == "AT+CFUN=0":
		c.resetAttempted = true
		return okResponse(), c.resetErr
	case command == "AT+CCID" && !c.resetAttempted:
		return okResponse("+CCID: 8900000000000000002"), nil
	default:
		return c.recoveringSIMClient.Execute(ctx, command)
	}
}

// TestESIMSwitchReturnsRecoveryErrorDespiteVerifiedIdentity exercises the public
// switch path rather than only checking the reset helper's return value.
func TestESIMSwitchReturnsRecoveryErrorDespiteVerifiedIdentity(t *testing.T) {
	cause := errors.New("SIM power-down rejected")
	client := &recoveryResultClient{
		recoveringSIMClient: recoveringSIMClient{pinReads: 1},
		resetErr:            cause,
	}
	manager, id := newStartedTestManager(t, client)
	if err := manager.ESIMSwitchProfile(context.Background(), id, "8900000000000000001", ""); !errors.Is(err, cause) {
		t.Fatalf("switch error = %v, want recovery failure", err)
	}
	entry, err := manager.Get(id)
	if err != nil || entry.Snapshot == nil || entry.Snapshot.ICCID != "8900000000000000001" {
		t.Fatalf("recovery did not refresh the new identity: entry=%+v err=%v", entry, err)
	}
}

// TestProfileSwitchRecoveryRetainsCompletedResult verifies late waiters still
// receive the failure and that cleanup permits a later recovery to start.
func TestProfileSwitchRecoveryRetainsCompletedResult(t *testing.T) {
	cause := errors.New("SIM power-down rejected")
	client := &recoveryResultClient{
		recoveringSIMClient: recoveringSIMClient{pinReads: 1},
		resetErr:            cause,
	}
	manager, id := newStartedTestManager(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := manager.startProfileSwitchRecovery(id)
	if joined := manager.startProfileSwitchRecovery(id); joined != first {
		t.Fatal("overlapping recovery did not reuse the active handle")
	}
	select {
	case <-first.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if manager.esimRecoveryActive(id) {
		t.Fatal("completed recovery still blocks subsequent operations")
	}
	for i := 0; i < 3; i++ {
		if err := manager.waitForESIMRecoveryResult(ctx, first); !errors.Is(err, cause) {
			t.Fatalf("late waiter error = %v, want recovery failure", err)
		}
	}
	second := manager.startProfileSwitchRecovery(id)
	if second == first {
		t.Fatal("subsequent operation reused an old recovery result")
	}
	if err := manager.waitForESIMRecoveryResult(ctx, second); !errors.Is(err, cause) {
		t.Fatalf("subsequent recovery error = %v", err)
	}
}
