package device

import (
	"context"
	"testing"

	"vocat/internal/modem"
)

func TestParseCellularIMSStatus(t *testing.T) {
	for _, test := range []struct {
		line               string
		configured, active bool
	}{
		{`+QCFG: "ims",0,0`, false, false},
		{`+QCFG: "ims",1,0`, true, false},
		{`+QCFG: "ims", 1, 1`, true, true},
		{`+QCFG: "ims",1`, true, false},
	} {
		status, err := parseCellularIMSStatus([]string{test.line})
		if err != nil || !status.Supported || status.Configured != test.configured || status.Registered != test.active {
			t.Errorf("parseCellularIMSStatus(%q) = %+v, %v", test.line, status, err)
		}
	}
	if _, err := parseCellularIMSStatus([]string{"OK"}); err == nil {
		t.Fatal("missing QCFG status was accepted")
	}
}

func TestParseCellularCSRegistration(t *testing.T) {
	for _, test := range []struct {
		line       string
		registered bool
	}{
		{`+CREG: 0,1`, true},
		{`+CREG: 2,5,"1234","12345678",7`, true},
		{`+CREG: 0,3`, false},
	} {
		registered, known := parseCellularCSRegistration([]string{test.line})
		if !known || registered != test.registered {
			t.Errorf("parseCellularCSRegistration(%q) = %t, %t", test.line, registered, known)
		}
	}
	if _, known := parseCellularCSRegistration([]string{"OK"}); known {
		t.Fatal("missing CREG status was accepted")
	}
}

func TestSetCellularIMSEnablesAndRebootsOnlyOnce(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="ims"`, response: modem.Response{Lines: []string{`+QCFG: "ims",0,0`}, Final: "OK"}},
		{command: `AT+QCFG="ims",1`, response: modem.Response{Final: "OK"}},
		{command: `AT+QCFG="ims"`, response: modem.Response{Lines: []string{`+QCFG: "ims",1,0`}, Final: "OK"}},
		{command: "AT+CFUN=1,1", response: modem.Response{Final: "OK"}},
	}}
	manager, id := newStartedTestManager(t, client)
	status, err := manager.SetCellularIMS(context.Background(), id, true)
	if err != nil || !status.Configured || !status.Changed || !status.Rebooting {
		t.Fatalf("SetCellularIMS = %+v, %v", status, err)
	}
	if client.closeCount != 1 {
		t.Fatalf("close count = %d, want 1", client.closeCount)
	}
	client.assertDone(t)
}

func TestSetCellularIMSNoopDoesNotReboot(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+QCFG="ims"`, response: modem.Response{Lines: []string{`+QCFG: "ims",1,1`}, Final: "OK"}},
	}}
	manager, id := newStartedTestManager(t, client)
	status, err := manager.SetCellularIMS(context.Background(), id, true)
	if err != nil || status.Changed || status.Rebooting || !status.Registered {
		t.Fatalf("SetCellularIMS = %+v, %v", status, err)
	}
	if client.closeCount != 0 {
		t.Fatalf("close count = %d, want 0", client.closeCount)
	}
	client.assertDone(t)
}
