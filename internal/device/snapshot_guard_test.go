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

// lenientATClient answers every command with a bare CommandError and records
// the commands it saw. It lets snapshot tests exercise the full readSnapshot
// sequence without enumerating every step of the transcript.
type lenientATClient struct {
	mu        sync.Mutex
	commands  []string
	cgsnDelay time.Duration
	cgsnIMEI  string
}

func (c *lenientATClient) Execute(ctx context.Context, command string) (modem.Response, error) {
	c.mu.Lock()
	c.commands = append(c.commands, command)
	c.mu.Unlock()
	if command == "ATI" {
		return okResponse("Qualcomm", "PCIe/MHI WWAN modem", "Revision: native-410"), nil
	}
	if command == "AT+CGSN" && c.cgsnDelay > 0 {
		select {
		case <-time.After(c.cgsnDelay):
		case <-ctx.Done():
		}
	}
	if command == "AT+CGSN" && c.cgsnIMEI != "" {
		return okResponse("+CGSN: " + c.cgsnIMEI), nil
	}
	return modem.Response{}, &modem.CommandError{Command: command, Final: "ERROR"}
}

func (c *lenientATClient) WaitURC(context.Context, func(string) bool) (string, error) {
	return "", errors.New("no URC")
}

func (c *lenientATClient) Close() error { return nil }

func (c *lenientATClient) saw(command string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, seen := range c.commands {
		if seen == command {
			return true
		}
	}
	return false
}

func TestSnapshotSkipsCGSNOnlyForNativeQMI(t *testing.T) {
	const commandTimeout = 20 * time.Millisecond
	for _, test := range []struct {
		name, id, backend string
		qmiErr            error
	}{
		{name: "native AT", id: "mhi-wwan0", backend: "at"},
		{name: "native QMI", id: "mhi-wwan0", backend: "qmi"},
		{name: "native AT failure", id: "mhi-wwan0", backend: "at", qmiErr: errors.New("DMS unavailable")},
		{name: "native QMI failure", id: "mhi-wwan0", backend: "qmi", qmiErr: errors.New("DMS unavailable")},
		{name: "native AT timeout", id: "mhi-wwan0", backend: "at", qmiErr: context.DeadlineExceeded},
		{name: "native QMI timeout", id: "mhi-wwan0", backend: "qmi", qmiErr: context.DeadlineExceeded},
		{name: "USB AT", id: "usb-ec20", backend: "at"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			candidate := modem.Candidate{ID: test.id, QMIControl: "/dev/wwan0qmi0"}
			client := &lenientATClient{}
			qmiCalls := 0
			manager := &Manager{commandTimeout: commandTimeout, qmiRadioOpener: func(ctx context.Context, _ string) (qmiRadioSession, error) {
				qmiCalls++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > commandTimeout {
					t.Fatal("DMS probe deadline exceeds CommandTimeout")
				}
				if test.qmiErr == context.DeadlineExceeded {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return &fakeQMIRadioSession{imei: "866241014372802", imeiErr: test.qmiErr}, nil
			}}
			previous := &Snapshot{IMEI: "866241014372801"}
			snapshot, err := manager.readSnapshot(ctx, test.id, candidate, test.backend, "", previous, client)
			if err != nil {
				t.Fatal(err)
			}
			if !client.saw("AT+CPIN?") || ctx.Err() != nil {
				t.Fatalf("CPIN did not continue with a live parent: commands = %v, error = %v", client.commands, ctx.Err())
			}
			if test.qmiErr != nil && !strings.Contains(strings.Join(snapshot.Warnings, "\n"), test.qmiErr.Error()) {
				t.Fatalf("warnings = %v, want DMS error %v", snapshot.Warnings, test.qmiErr)
			}
			wantCGSN := test.id == "usb-ec20"
			if client.saw("AT+CGSN") != wantCGSN || client.saw("AT+CGSN=1") != wantCGSN {
				t.Fatalf("commands = %v, want CGSN probes = %t", client.commands, wantCGSN)
			}
			wantCalls, wantIMEI := 0, previous.IMEI
			if !wantCGSN {
				wantCalls = 1
				if test.qmiErr == nil {
					wantIMEI = "866241014372802"
				}
			}
			if qmiCalls != wantCalls || snapshot.IMEI != wantIMEI {
				t.Fatalf("DMS calls = %d, IMEI = %q; want %d, %q", qmiCalls, snapshot.IMEI, wantCalls, wantIMEI)
			}
		})
	}
}

func TestManagerRefreshBoundsCGSNTimeout(t *testing.T) {
	client := &lenientATClient{cgsnDelay: 5 * time.Second}
	manager, id := newStartedTestManager(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	snapshot, err := manager.Refresh(ctx, id)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// CGSN times out after CommandTimeout (1s in the test manager); the rest
	// of the snapshot is immediate. An un-bounded CGSN would wait for the
	// 4s outer deadline (or worse, a real 30s refresh deadline).
	if elapsed > 3*time.Second {
		t.Fatalf("Refresh took %s; CGSN was not bounded by CommandTimeout", elapsed)
	}
	if !client.saw("AT+CGSN") {
		t.Fatalf("CGSN was never sent; commands = %v", client.commands)
	}
	if snapshot.IMEI != "" {
		t.Fatalf("IMEI = %q, want empty after CGSN timeout", snapshot.IMEI)
	}
}

// A missing SIM must not fall back to the QMI UIM ICCID read: without a READY
// card that call blocks until its long timeout and starves the AT terminal
// behind the device lock.
func TestManagerRefreshSkipsQMIICCIDWithoutReadySIM(t *testing.T) {
	client := &lenientATClient{cgsnIMEI: "866241014372802"}
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:               "mhi-wwan0",
			Product:          "PCIe/MHI WWAN modem",
			QMIControl:       "/dev/wwan0qmi0",
			NetworkInterface: "wwan0",
			ATPort:           modem.Port{Path: "/dev/wwan0at0", Name: "wwan0at0", Role: modem.PortRoleAT},
		}}},
		Opener: &staticOpener{client: client},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	qmiCalls := 0
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		qmiCalls++
		return nil, errors.New("QMI should not be opened without a SIM")
	}
	if err := manager.SetBackend("mhi-wwan0", "qmi"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := manager.Refresh(context.Background(), "mhi-wwan0")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Only the SIM-independent DMS read is attempted; the UIM ICCID fallback
	// must still be skipped without a READY SIM.
	if qmiCalls != 1 {
		t.Fatalf("qmiRadioOpener called %d times, want 1 DMS attempt and no UIM ICCID read", qmiCalls)
	}
	if client.saw("AT+CGSN") || client.saw("AT+CGSN=1") || snapshot.IMEI != "" {
		t.Fatalf("native IMEI fell back to AT: %q, commands = %v", snapshot.IMEI, client.commands)
	}
	for _, warning := range snapshot.Warnings {
		if strings.Contains(warning, "QMI UIM") {
			t.Fatalf("unexpected QMI ICCID warning: %q", warning)
		}
	}
}
