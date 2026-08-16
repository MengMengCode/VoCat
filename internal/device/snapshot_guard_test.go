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

// AT+CGSN on some MHI modems returns the IMEI line but never a final OK, so it
// would block until the caller's deadline and hold the device lock for the
// whole periodic refresh. The snapshot must bound CGSN with its own short
// timeout instead of inheriting the refresh deadline.
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
	// CGSN succeeds so the snapshot does not fall back to the QMI DMS IMEI
	// read either; the test focuses on the UIM ICCID fallback being skipped
	// without a READY card.
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
	// Exactly one QMI open is expected: the immutable DMS IMEI read runs
	// unconditionally for native QMI candidates (IMEI is hardware identity,
	// independent of the card). The UIM ICCID fallback, which would block
	// without a READY SIM, must be skipped.
	if qmiCalls != 1 {
		t.Fatalf("qmiRadioOpener called %d times, want 1 (DMS IMEI only, UIM ICCID must be skipped without a READY SIM)", qmiCalls)
	}
	for _, warning := range snapshot.Warnings {
		if strings.Contains(warning, "QMI UIM") {
			t.Fatalf("unexpected QMI ICCID warning: %q", warning)
		}
	}
}
