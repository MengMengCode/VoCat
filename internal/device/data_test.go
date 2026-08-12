package device

import (
	"context"
	"errors"
	"strings"
	"testing"

	"vocat/internal/modem"
)

func TestSetNetworkATBackendActivatesAndDeactivatesPDP(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
		{command: "AT+CGACT=0,1", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)
	result, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	})
	if err != nil {
		t.Fatalf("enable network: %v", err)
	}
	if !result.Enabled || result.Backend != "at" || result.APN != "internet" {
		t.Fatalf("enable result = %#v", result)
	}
	result, err = manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: false, APN: "internet", IPVersion: "IP",
	})
	if err != nil {
		t.Fatalf("disable network: %v", err)
	}
	if result.Enabled {
		t.Fatalf("disable result = %#v", result)
	}
	client.assertDone(t)
}

func TestSetNetworkATBackendAppliesPAPCredentials(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+CGDCONT=1,"IPV4V6","giffgaff.com"`, response: okResponse()},
		{command: `AT+CGAUTH=1,1,"gg","p"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "giffgaff.com", IPVersion: "IPV4V6",
		Username: "gg", Password: "p", Authentication: "PAP",
	}); err != nil {
		t.Fatalf("enable authenticated network: %v", err)
	}
	client.assertDone(t)
}

func TestSetNetworkDoesNotExposeAPNCredentialsInErrorsOrState(t *testing.T) {
	const username = "private-user"
	const password = "private-password"
	command := `AT+CGAUTH=1,1,"` + username + `","` + password + `"`
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+CGDCONT=1,"IPV4V6","giffgaff.com"`, response: okResponse()},
		{command: command, err: &modem.CommandError{Command: command, Final: "ERROR"}},
	}}
	manager, id := newStartedTestManager(t, client)
	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "giffgaff.com", IPVersion: "IPV4V6",
		Username: username, Password: password, Authentication: "PAP",
	})
	if err == nil {
		t.Fatal("SetNetwork() error = nil")
	}
	if strings.Contains(err.Error(), username) || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "AT+CGAUTH") {
		t.Fatalf("SetNetwork() exposed credentials: %q", err)
	}
	entry, getErr := manager.Get(id)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if strings.Contains(entry.LastError, username) || strings.Contains(entry.LastError, password) || strings.Contains(entry.LastError, "AT+CGAUTH") {
		t.Fatalf("device state exposed credentials: %q", entry.LastError)
	}
	client.assertDone(t)
}

func TestSetNetworkRejectsUnsafeAPNBeforeOpeningModem(t *testing.T) {
	client := &transcriptClient{}
	manager, id := newStartedTestManager(t, client)
	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: `internet";AT+CFUN=0`, IPVersion: "IP",
	})
	if !errors.Is(err, ErrInvalidNetworkAPN) {
		t.Fatalf("error = %v, want ErrInvalidNetworkAPN", err)
	}
	client.assertDone(t)
}

func TestUSBNetModeReadAndGuardedWrite(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="usbnet"`, response: okResponse(`+QCFG: "usbnet",0`)},
		{command: `AT+QCFG="usbnet",1`, response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)
	mode, err := manager.USBNetMode(context.Background(), id)
	if err != nil {
		t.Fatalf("read USB mode: %v", err)
	}
	if mode.Mode != 0 || mode.Name != "QMI" {
		t.Fatalf("USB mode = %#v", mode)
	}
	mode, err = manager.SetUSBNetMode(context.Background(), id, 1)
	if err != nil {
		t.Fatalf("write USB mode: %v", err)
	}
	if mode.Mode != 1 || mode.Name != "ECM" {
		t.Fatalf("new USB mode = %#v", mode)
	}
	client.assertDone(t)
}

func TestOperatorSelectionManualAndAutomatic(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+COPS=1,2,"46000",7`, response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 1,2,"46000",7`)},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	act := 7
	selection, err := manager.SetOperatorSelection(context.Background(), id, false, "46000", &act)
	if err != nil {
		t.Fatalf("manual selection: %v", err)
	}
	if selection.Mode != 1 || selection.Format != 2 || selection.Operator != "46000" || selection.AccessTechnology != "LTE" {
		t.Fatalf("manual selection = %#v", selection)
	}
	selection, err = manager.SetOperatorSelection(context.Background(), id, true, "", nil)
	if err != nil {
		t.Fatalf("automatic selection: %v", err)
	}
	if selection.Mode != 0 || selection.Operator != "46001" {
		t.Fatalf("automatic selection = %#v", selection)
	}
	client.assertDone(t)
}

func TestOperatorSelectionRejectsAutomaticFallbackAsSuccess(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+COPS=1,2,"46000",7`, response: okResponse()},
		{command: "AT+COPS?", response: okResponse("+COPS: 0")},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	act := 7
	if _, err := manager.SetOperatorSelection(context.Background(), id, false, "46000", &act); err == nil {
		t.Fatal("manual selection must fail when readback returned automatic mode")
	}
	client.assertDone(t)
}

func TestOperatorSelectionCommandFailureRestoresAutomaticMode(t *testing.T) {
	selectionErr := errors.New("+CME ERROR: 30")
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+COPS=1,2,"46000",7`, err: selectionErr},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	act := 7
	_, err := manager.SetOperatorSelection(context.Background(), id, false, "46000", &act)
	if !errors.Is(err, selectionErr) {
		t.Fatalf("error = %v, want wrapped selection error", err)
	}
	client.assertDone(t)
}

func TestReRegisterOperatorReappliesAutomaticMode(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	selection, err := manager.ReRegisterOperator(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != 0 || selection.Operator != "46001" {
		t.Fatalf("selection = %#v", selection)
	}
	client.assertDone(t)
}

func TestReRegisterOperatorPreservesManualLock(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+COPS?", response: okResponse(`+COPS: 1,2,"46003",7`)},
		{command: "AT+COPS=2", response: okResponse()},
		{command: `AT+COPS=1,2,"46003",7`, response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 1,2,"46003",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	selection, err := manager.ReRegisterOperator(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != 1 || selection.Operator != "46003" || selection.AccessTechnology != "LTE" {
		t.Fatalf("selection = %#v", selection)
	}
	client.assertDone(t)
}

func TestReRegisterOperatorRecoversDeregisteredModeWithAutomaticSelection(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+COPS?", response: okResponse(`+COPS: 2`)},
		{command: `AT+QCFG="nwscanmode",0,1`, response: okResponse()},
		{command: "AT+COPS=2", response: okResponse()},
		{command: "AT+COPS=0", response: okResponse()},
		{command: "AT+COPS?", response: okResponse(`+COPS: 0,2,"46001",7`)},
	}}
	manager, id := newStartedTestManager(t, client)
	selection, err := manager.ReRegisterOperator(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != 0 || selection.Operator != "46001" {
		t.Fatalf("selection = %#v", selection)
	}
	client.assertDone(t)
}
