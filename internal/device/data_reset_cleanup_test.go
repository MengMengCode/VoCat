package device

import (
	"context"
	"errors"
	"testing"
)

func TestModemResetWaitsForHostCleanup(t *testing.T) {
	for _, operation := range []string{"reboot", "flight", "sim-reset"} {
		t.Run(operation, func(t *testing.T) {
			client := &transcriptClient{}
			if operation != "sim-reset" {
				client.steps = []clientStep{{command: "AT+CFUN?", response: okResponse("+CFUN: 1")}}
			}
			manager, id := newStartedTestManager(t, client)
			state, _ := manager.lookup(id)
			cause := errors.New("host routes could not be removed")
			owner := &transitionHostSession{err: cause}
			state.atHostSession, state.dataBackend = owner, "at"
			var err error
			switch operation {
			case "reboot":
				err = manager.Reboot(context.Background(), id)
			case "flight":
				_, err = manager.SetFlight(context.Background(), id, true)
			case "sim-reset":
				err = manager.softResetForProfileSwitch(context.Background(), id)
			}
			if !errors.Is(err, cause) || state.atHostSession != owner {
				t.Fatalf("reset lost failed cleanup ownership: err=%v, owner=%v", err, state.atHostSession)
			}
			client.assertDone(t) // No RF/reset command can follow failed cleanup.
		})
	}
}

func TestIMSSettingIsNotWrittenBeforeHostCleanupSucceeds(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="ims"`, response: okResponse(`+QCFG: "ims",0,0`)},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
	}}
	manager, id := newStartedTestManager(t, client)
	state, _ := manager.lookup(id)
	cause := errors.New("host teardown failed")
	state.atHostSession = &transitionHostSession{err: cause}
	status, err := manager.SetCellularIMS(context.Background(), id, CellularIMSModeForceEnabled)
	if !errors.Is(err, cause) || status.Changed || status.Rebooting {
		t.Fatalf("IMS setting changed before cleanup succeeded: status=%+v err=%v", status, err)
	}
	client.assertDone(t)
}
