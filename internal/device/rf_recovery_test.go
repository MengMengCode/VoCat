package device

import (
	"context"
	"errors"
	"strings"
	"testing"

	"vocat/internal/modem"
)

func TestProfileRecoveryKeepsRFOffWithMissingOrPartialSnapshot(t *testing.T) {
	for _, partial := range []bool{false, true} {
		client := &transcriptClient{steps: []clientStep{
			{command: "AT+CFUN=0", response: okResponse()},
			{command: "AT+CFUN=4", response: okResponse()},
		}}
		manager, id := newStartedTestManager(t, client)
		state, _ := manager.lookup(id)
		if partial {
			state.snapshot = &Snapshot{SIMReady: false}
		}
		if err := manager.softResetForProfileSwitch(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		client.assertDone(t)
	}
}

type recoveringSIMClient struct {
	commands []string
	pinReads int
}

func (c *recoveringSIMClient) Execute(_ context.Context, command string) (modem.Response, error) {
	c.commands = append(c.commands, command)
	switch command {
	case "ATI":
		return okResponse("Quectel", "EC20", "Revision: test"), nil
	case "AT+CPIN?":
		c.pinReads++
		if c.pinReads == 1 {
			return modem.Response{}, &modem.CommandError{Final: "+CME ERROR: 13"}
		}
		return okResponse("+CPIN: READY"), nil
	case "AT+CFUN=0", "AT+CFUN=4", "AT+CFUN=1":
		return okResponse(), nil
	case "AT+CCID":
		return okResponse("+CCID: 8900000000000000001"), nil
	case "AT+CIMI":
		return okResponse("001010000000001"), nil
	case "AT+CFUN?":
		return okResponse("+CFUN: 4"), nil
	default:
		return modem.Response{}, &modem.CommandError{Final: "ERROR"}
	}
}

func (*recoveringSIMClient) Close() error { return nil }

func (*recoveringSIMClient) WaitURC(context.Context, func(string) bool) (string, error) {
	return "", errors.New("no URC")
}

func TestSnapshotSIMRecoveryDoesNotEnableRFBeforeReadingCFUN(t *testing.T) {
	c := &recoveringSIMClient{}
	m, id := newStartedTestManager(t, c)
	state, _ := m.lookup(id)
	state.lastICCID = "8900000000000000001"
	state.snapshot = &Snapshot{ModeKnown: true, FlightMode: true, OperatingMode: 4}
	if _, err := m.Refresh(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for _, command := range c.commands {
		if strings.HasPrefix(command, "AT+CFUN=1") {
			t.Fatalf("SIM recovery enabled RF: %v", c.commands)
		}
	}
}

func TestProfileRecoveryStopsOnFailedSIMPowerDown(t *testing.T) {
	cause := errors.New("SIM power-down rejected")
	client := &transcriptClient{steps: []clientStep{{command: "AT+CFUN=0", err: cause}}}
	m, id := newStartedTestManager(t, client)
	if err := m.softResetForProfileSwitch(context.Background(), id); !errors.Is(err, cause) {
		t.Fatalf("reset error = %v, want %v", err, cause)
	}
	client.assertDone(t)
}

func TestModemRebootBlockedWhileFlightModeIsEnabled(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{{command: "AT+CFUN?", response: okResponse("+CFUN: 4")}}}
	m, id := newStartedTestManager(t, client)
	if err := m.Reboot(context.Background(), id); err == nil {
		t.Fatal("accepted online reboot while flight mode is enabled")
	}
	client.assertDone(t)
}

func TestCellularIMSChangeCannotRebootProtectedCard(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="ims"`, response: okResponse(`+QCFG: "ims",0,0`)},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
	}}
	m, id := newStartedTestManager(t, client)
	if _, err := m.SetCellularIMS(context.Background(), id, CellularIMSModeForceEnabled); err == nil {
		t.Fatal("accepted restart-requiring IMS change while RF is off")
	}
	client.assertDone(t)
}

func TestOnlineModemRebootStillWorks(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
		{command: "AT+CFUN=1,1", response: okResponse()},
	}}
	m, id := newStartedTestManager(t, client)
	if err := m.Reboot(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	client.assertDone(t)
}

func TestOnlineModemRebootDoesNotProceedWithUnknownMode(t *testing.T) {
	cause := errors.New("mode unavailable")
	client := &transcriptClient{steps: []clientStep{{command: "AT+CFUN?", err: cause}}}
	m, id := newStartedTestManager(t, client)
	if err := m.Reboot(context.Background(), id); !errors.Is(err, cause) {
		t.Fatalf("reboot error = %v, want mode read failure", err)
	}
	client.assertDone(t)
}

func TestProfileMBNReconciliationDefersOnlineRestartWithRFOff(t *testing.T) {
	c := &recoveringSIMClient{pinReads: 1}
	m, id := newStartedTestManager(t, c)
	if err := m.ReconcileEC20MBNAfterProfileSwitch(context.Background(), id, "8900000000000000001"); err == nil || !strings.Contains(err.Error(), "online restart") {
		t.Fatalf("protected MBN reconciliation error = %v", err)
	}
	for _, command := range c.commands {
		if strings.Contains(command, `"Select"`) || strings.HasPrefix(command, "AT+CFUN=1") {
			t.Fatalf("MBN reconciliation mutated RF-off modem: %v", c.commands)
		}
	}
}
