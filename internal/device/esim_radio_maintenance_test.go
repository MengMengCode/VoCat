package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
	"vocat/internal/modem"
)

func TestProfileSwitchBlocksOrdinaryATFlightPolicyButPermitsOwner(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
		{command: "AT+CFUN=4", response: okResponse()},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
		{command: "AT+CFUN=1", response: okResponse()},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
	}}
	manager, id := newStartedTestManager(t, client)
	lease, err := manager.beginProfileSwitchLease(context.Background(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			manager.releaseProfileSwitchLease(id, lease)
		}
	})
	for _, enabled := range []bool{true, false} {
		if _, err := manager.SetFlight(context.Background(), id, enabled); !errors.Is(err, ErrESIMRecovering) {
			t.Fatalf("ordinary SetFlight(%v) crossed switch maintenance: %v", enabled, err)
		}
	}
	if manager.opener.(*staticOpener).openCount != 0 {
		t.Fatal("blocked flight policy opened the AT modem")
	}
	owner := context.WithValue(context.Background(), esimSwitchLeaseKey{}, lease)
	if result, err := manager.SetFlight(owner, id, true); err != nil || !result.RadioOff {
		t.Fatalf("switch owner could not preserve RF-off: result=%+v err=%v", result, err)
	}
	manager.releaseProfileSwitchLease(id, lease)
	released = true
	if result, err := manager.SetFlight(context.Background(), id, false); err != nil || result.RadioOff {
		t.Fatalf("flight policy did not resume after switch: result=%+v err=%v", result, err)
	}
	client.assertDone(t)
}

func TestProfileSwitchBlocksOrdinaryQMIFlightPolicyButPermitsOwner(t *testing.T) {
	manager, atOpener, id := newStartedNativeQMITestManager(t)
	session := &fakeQMIRadioSession{mode: qmi.ModeOnline}
	opened := 0
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		opened++
		return session, nil
	}
	lease, err := manager.beginProfileSwitchLease(context.Background(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			manager.releaseProfileSwitchLease(id, lease)
		}
	})
	for _, enabled := range []bool{true, false} {
		if _, err := manager.SetFlight(context.Background(), id, enabled); !errors.Is(err, ErrESIMRecovering) {
			t.Fatalf("ordinary SetFlight(%v) crossed QMI switch maintenance: %v", enabled, err)
		}
	}
	if opened != 0 || atOpener.openCount != 0 {
		t.Fatalf("blocked flight policy opened QMI/AT: qmi=%d at=%d", opened, atOpener.openCount)
	}
	owner := context.WithValue(context.Background(), esimSwitchLeaseKey{}, lease)
	if result, err := manager.SetFlight(owner, id, true); err != nil || !result.RadioOff {
		t.Fatalf("QMI owner could not preserve RF-off: result=%+v err=%v", result, err)
	}
	manager.releaseProfileSwitchLease(id, lease)
	released = true
	state, _ := manager.lookup(id)
	// The outer read models a SIM identity read that invokes RF policy while
	// retaining its own maintenance read gate. Nested acquisition must finish.
	unlockRead, err := manager.lockProfileSwitchRead(context.Background(), id, state)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockRead()
	if _, err := manager.SetFlight(context.Background(), id, true); err != nil {
		t.Fatalf("nested flight policy did not resume after switch: %v", err)
	}
	if len(session.setModes) != 1 || session.setModes[0] != qmi.ModeLowPower {
		t.Fatalf("unexpected RF transitions: %v", session.setModes)
	}
}

func TestProfileSwitchBlocksBackgroundDataAndRegistrationBeforeIO(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "AT"
		if native {
			name = "QMI"
		}
		t.Run(name, func(t *testing.T) {
			var manager *Manager
			var id string
			var opener *staticOpener
			if native {
				manager, opener, id = newStartedNativeQMITestManager(t)
			} else {
				manager, id = newStartedTestManager(t, &transcriptClient{})
				opener = manager.opener.(*staticOpener)
			}
			manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
				t.Error("background registration opened QMI during profile switch")
				return nil, errors.New("unexpected QMI open")
			}
			manager.qmiDataOpener = func(context.Context, string) (qmiDataSession, error) {
				t.Error("background network policy opened QMI WDS during profile switch")
				return nil, errors.New("unexpected WDS open")
			}
			lease, err := manager.beginProfileSwitchLease(context.Background(), id, true)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.releaseProfileSwitchLease(id, lease)
			for _, enabled := range []bool{true, false} {
				if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{Enabled: enabled, APN: "internet"}); !errors.Is(err, ErrESIMRecovering) {
					t.Fatalf("SetNetwork(%v) crossed switch maintenance: %v", enabled, err)
				}
			}
			if err := manager.PrepareRegistration(context.Background(), id, "internet", "IP"); !errors.Is(err, ErrESIMRecovering) {
				t.Fatalf("PrepareRegistration crossed switch maintenance: %v", err)
			}
			if _, err := manager.SetOperatorSelection(context.Background(), id, true, "", nil); !errors.Is(err, ErrESIMRecovering) {
				t.Fatalf("automatic operator selection crossed switch maintenance: %v", err)
			}
			if _, err := manager.ReRegisterOperator(context.Background(), id); !errors.Is(err, ErrESIMRecovering) {
				t.Fatalf("background re-registration crossed switch maintenance: %v", err)
			}
			if opener.openCount != 0 {
				t.Fatal("blocked background policy opened the AT modem")
			}
		})
	}
}

func waitForProfileSwitchReservation(t *testing.T, manager *Manager, id string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		manager.esimRecoveryMu.Lock()
		_, pending := manager.esimSwitchPending[id]
		manager.esimRecoveryMu.Unlock()
		if pending {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("switch did not reserve the gate while draining earlier work")
		}
	}
}

type racingProfileRFClient struct {
	uncertainSwitchMaintenanceClient
	mode        int
	commitMode  int
	onlineSeen  chan struct{}
	allowOnline chan struct{}
}

func (client *racingProfileRFClient) Execute(ctx context.Context, command string) (modem.Response, error) {
	switch command {
	case "AT+CFUN?":
		return okResponse(fmt.Sprintf("+CFUN: %d", client.mode)), nil
	case "AT+CFUN=1":
		close(client.onlineSeen)
		select {
		case <-client.allowOnline:
			client.mode = 1
			return okResponse(), nil
		case <-ctx.Done():
			return modem.Response{}, ctx.Err()
		}
	case "AT+CFUN=4":
		client.mode = 4
		return okResponse(), nil
	}
	if strings.HasPrefix(command, "AT+CSIM=") && strings.Contains(command, "BF31") {
		client.commitMode = client.mode
	}
	return client.uncertainSwitchMaintenanceClient.Execute(ctx, command)
}

func TestProfileSwitchReassertsRFOffAfterEarlierPolicyFinishes(t *testing.T) {
	client := &racingProfileRFClient{
		uncertainSwitchMaintenanceClient: uncertainSwitchMaintenanceClient{
			commitSeen: make(chan struct{}), allowCommit: make(chan struct{}),
			enableResponse: `+CSIM: 16,"BF31038001039000"`,
		},
		mode: 4, onlineSeen: make(chan struct{}), allowOnline: make(chan struct{}),
	}
	close(client.allowCommit)
	manager, id := newStartedTestManager(t, client)
	t.Cleanup(func() { releaseMaintenanceTestChannel(client.allowOnline) })
	policyDone := make(chan error, 1)
	go func() { _, err := manager.SetFlight(context.Background(), id, false); policyDone <- err }()
	select {
	case <-client.onlineSeen:
	case <-time.After(time.Second):
		t.Fatal("earlier policy did not begin enabling RF")
	}
	switchDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { switchDone <- manager.ESIMSwitchProfile(ctx, id, "8900000000000000001", "") }()
	waitForProfileSwitchReservation(t, manager, id)
	if _, err := manager.Refresh(context.Background(), id); !errors.Is(err, ErrESIMRecovering) {
		t.Fatalf("new snapshot bypassed the pending writer: %v", err)
	}
	for _, enabled := range []bool{true, false} {
		if _, err := manager.SetFlight(context.Background(), id, enabled); !errors.Is(err, ErrESIMRecovering) {
			t.Fatalf("new flight policy bypassed the pending writer: %v", err)
		}
	}
	close(client.allowOnline)
	if err := <-policyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-switchDone; !errors.Is(err, ErrESIMEnableDisallowedPolicy) {
		t.Fatalf("switch did not reach its card-level result: %v", err)
	}
	if client.commitMode != 4 || client.mode != 4 {
		t.Fatalf("EnableProfile ran before atomic RF-off: commit_mode=%d final_mode=%d", client.commitMode, client.mode)
	}
}

func TestPendingProfileSwitchCancellationReleasesReservation(t *testing.T) {
	manager, id := newStartedTestManager(t, &recoveringSIMClient{pinReads: 1})
	unlockRead, err := manager.AcquireSIMRead(nil, id)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockRead()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.ESIMSwitchProfile(ctx, id, "8900000000000000001", "") }()
	waitForProfileSwitchReservation(t, manager, id)
	if unlock, err := manager.AcquireSIMRead(context.Background(), id); !errors.Is(err, ErrESIMRecovering) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("new public policy read bypassed the pending switch: %v", err)
	}
	if _, err := manager.SetFlight(context.Background(), id, false); !errors.Is(err, ErrESIMRecovering) {
		t.Fatalf("new reader bypassed the pending switch: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending switch lost cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending switch did not cancel")
	}
	// The original reader remains open; later nested work must be admitted
	// once the canceled writer has removed its reservation.
	if _, err := manager.SetFlight(context.Background(), id, true); err != nil {
		t.Fatalf("canceled switch left later readers blocked: %v", err)
	}
	if unlock, err := manager.AcquireSIMRead(ctx, id); !errors.Is(err, context.Canceled) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("public policy read ignored cancellation: %v", err)
	}
}

func TestProfileSwitchRequiresVerifiedRFOffBeforeEnableProfile(t *testing.T) {
	cause := errors.New("RF-off rejected")
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprintf("command_rejected_%v", reject), func(t *testing.T) {
			steps := []clientStep{
				{command: "AT+CFUN?", response: okResponse("+CFUN: 1")},
				{command: "AT+CFUN=4", response: okResponse()},
			}
			if reject {
				steps[1].err = cause
			} else {
				steps = append(steps, clientStep{command: "AT+CFUN?", response: okResponse("+CFUN: 1")})
			}
			client := &transcriptClient{steps: steps}
			manager, id := newStartedTestManager(t, client)
			err := manager.ESIMSwitchProfile(context.Background(), id, "8900000000000000001", "")
			if err == nil || (reject && !errors.Is(err, cause)) || (!reject && !strings.Contains(err.Error(), "verified RF-off")) {
				t.Fatalf("profile switch continued without RF-off proof: %v", err)
			}
			client.assertDone(t)
		})
	}
}
