package device

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMBNSelectionDoesNotMutateBeforeCleanupSucceeds(t *testing.T) {
	cleanupErr := errors.New("injected host cleanup failure")
	for _, override := range []string{"", "CU"} {
		t.Run("override="+override, func(t *testing.T) {
			executor := &fakeMBNExecutor{list: rowProfiles(true), autosel: "1"}
			calls := 0
			changed, previous, selected, err := reconcileMBNSelectionBeforeChange(context.Background(), executor, "45400", override, func() error {
				calls++
				return cleanupErr
			})
			if !errors.Is(err, cleanupErr) || changed || previous != MBNProfileCMCC || selected != previous || calls != 1 {
				t.Fatalf("selection after failed cleanup = %t, %q, %q, %v (calls=%d)", changed, previous, selected, err, calls)
			}
			if len(executor.cmds) != 1 || executor.cmds[0] != `AT+QMBNCFG="List"` {
				t.Fatalf("mutated MBN before cleanup: %v", executor.cmds)
			}
			// A retry still sees the old selection and can complete its required change.
			changed, _, _, err = reconcileMBNSelectionBeforeChange(context.Background(), executor, "45400", override, func() error { return nil })
			if err != nil || !changed {
				t.Fatalf("selection retry did not apply pending change: %t, %v", changed, err)
			}
		})
	}
}

func TestMatchingMBNDoesNotTearDownHostData(t *testing.T) {
	for _, override := range []string{"", "CMCC"} {
		t.Run("override="+override, func(t *testing.T) {
			executor := &fakeMBNExecutor{list: ceProfiles(true), autosel: "1"}
			changed, _, _, err := reconcileMBNSelectionBeforeChange(context.Background(), executor, "46000", override, func() error {
				t.Error("unchanged selection tore down host data")
				return errors.New("unexpected cleanup")
			})
			if err != nil || changed {
				t.Fatalf("matching MBN result = %t, %v", changed, err)
			}
			joined := strings.Join(executor.cmds, "\n")
			if strings.Contains(joined, `"Select",`) {
				t.Fatalf("reselected unchanged MBN: %v", executor.cmds)
			}
			if override != "" && !strings.Contains(joined, `"AutoSel",0`) {
				t.Fatal("same-profile manual override no longer disables automatic selection")
			}
		})
	}
}

func TestMBNSelectionCleansHostBeforeFirstMutation(t *testing.T) {
	executor := &fakeMBNExecutor{list: rowProfiles(true), autosel: "1"}
	calls := 0
	changed, _, _, err := reconcileMBNSelectionBeforeChange(context.Background(), executor, "45400", "", func() error {
		calls++
		if len(executor.cmds) != 1 || executor.cmds[0] != `AT+QMBNCFG="List"` {
			t.Fatalf("cleanup followed an MBN mutation: %v", executor.cmds)
		}
		return nil
	})
	if err != nil || !changed || calls != 1 {
		t.Fatalf("selection result=%t err=%v callbacks=%d", changed, err, calls)
	}
}
