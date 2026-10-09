package device

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"vocat/internal/modem"
)

func simResetSelectSteps(aid, sw string) []clientStep {
	return []clientStep{
		{command: `AT+CSIM=10,"0070000001"`, response: okResponse(`+CSIM: 6,"019000"`)},
		{command: fmt.Sprintf(`AT+CSIM=42,"01A4040010%s"`, aid), response: okResponse(fmt.Sprintf(`+CSIM: 4,"%s"`, sw))},
		{command: `AT+CSIM=10,"0070800100"`, response: okResponse(`+CSIM: 4,"9000"`)},
	}
}

func TestESIMManagementFailureRequiresLiveESTKAndTwoMissingApplications(t *testing.T) {
	for _, test := range []struct {
		name, productSW, se0SW, se1SW, standardSW string
		wantRecovery                              bool
	}{
		{"ordinary_SIM", "6A82", "", "", "6A82", false},
		{"estk_management_missing", "9000", "6A82", "6A82", "6A82", true},
		{"different_card_error", "9000", "6985", "6A82", "6A82", false},
		{"one_SE_available", "9000", "9000", "6A82", "", false},
		{"generic_management_available", "9000", "6A82", "6A82", "9000", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := simResetSelectSteps(estkProductAID, test.productSW)
			if test.productSW == "9000" {
				steps = append(steps, simResetSelectSteps(estkSE0AID, test.se0SW)...)
				steps = append(steps, simResetSelectSteps(estkSE1AID, test.se1SW)...)
			}
			if test.standardSW != "" {
				steps = append(steps, simResetSelectSteps(isdRAID, test.standardSW)...)
				steps = append(steps, simResetSelectSteps(xesimISDRAID, "6A82")...)
			}
			client := &transcriptClient{steps: steps}
			manager, id := newStartedTestManager(t, client)
			_, err := manager.discoverEuiccAIDsForInventory(context.Background(), id)
			if got := errors.Is(err, ErrESIMManagementUnavailable); got != test.wantRecovery {
				t.Fatalf("recovery required = %v, want %v; error=%v", got, test.wantRecovery, err)
			}
			if test.wantRecovery && errors.Is(err, ErrNoEUICC) {
				t.Fatal("known eSTK failure was classified as an ordinary SIM")
			}
			client.assertDone(t)
		})
	}
}

func TestOrdinarySIMInventoryDoesNotAddChannelProbeOrReset(t *testing.T) {
	var steps []clientStep
	for _, aid := range []string{estkProductAID, isdRAID, xesimISDRAID, isdRAID} {
		steps = append(steps, simResetSelectSteps(aid, "6A82")...)
	}
	client := &transcriptClient{steps: steps}
	manager, id := newStartedTestManager(t, client)
	if _, err := manager.ESIMInventory(context.Background(), id); !errors.Is(err, ErrNoEUICC) {
		t.Fatalf("inventory error = %v", err)
	}
	client.assertDone(t)
}

func TestResetSIMWaitsUntilReadyAndKeepsRFOff(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
		{command: "AT+CFUN=0", response: okResponse()},
		{command: "AT+CFUN=4", response: okResponse()},
		{command: "AT+CPIN?", err: &modem.CommandError{Final: "+CME ERROR: 14"}},
		{command: "AT+CPIN?", response: okResponse("+CPIN: READY")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
	}}
	manager, id := newStartedTestManager(t, client)
	if err := manager.ResetSIM(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	client.assertDone(t)
}

func TestResetSIMDoesNotInterruptAnOnlineModem(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
	}}
	manager, id := newStartedTestManager(t, client)
	if err := manager.ResetSIM(context.Background(), id); err == nil {
		t.Fatal("reset an online SIM")
	}
	client.assertDone(t)
}

type cancelAfterSIMPowerDown struct {
	*transcriptClient
	cancel context.CancelFunc
}

func (client *cancelAfterSIMPowerDown) Execute(ctx context.Context, command string) (modem.Response, error) {
	response, err := client.transcriptClient.Execute(ctx, command)
	if command == "AT+CFUN=0" && err == nil {
		client.cancel()
	}
	return response, err
}

func TestSIMPowerUpSurvivesCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transcript := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN=0", response: okResponse()},
		{command: "AT+CFUN=4", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, &cancelAfterSIMPowerDown{transcript, cancel})
	if err := manager.softResetForProfileSwitch(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("reset error = %v, want cancellation", err)
	}
	transcript.assertDone(t)
}

func TestSIMPowerUpAttemptedAfterUnconfirmedPowerDown(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN=0", err: modem.ErrCommandTimeout},
		{command: "AT+CFUN=4", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)
	if err := manager.softResetForProfileSwitch(context.Background(), id); !errors.Is(err, modem.ErrCommandTimeout) {
		t.Fatalf("reset error = %v, want timeout", err)
	}
	client.assertDone(t)
}
