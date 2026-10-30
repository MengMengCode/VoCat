package device

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

type uncertainSwitchMaintenanceClient struct {
	recoveringSIMClient
	mu                sync.Mutex
	commitSeen        chan struct{}
	allowCommit       chan struct{}
	resetSeen         chan struct{}
	allowReset        chan struct{}
	commitOnce        sync.Once
	resetOnce         sync.Once
	probeSeen         chan struct{}
	allowProbe        chan struct{}
	probeOnce         sync.Once
	probeCount        int
	resetFailure      error
	rejectFirstEnable bool
	enableResponse    string
	enableRequests    int
}

func (client *uncertainSwitchMaintenanceClient) Execute(ctx context.Context, command string) (modem.Response, error) {
	if strings.HasPrefix(command, "AT+CSIM=") {
		if strings.Contains(command, "BF31") {
			client.enableRequests++
			if client.rejectFirstEnable && client.enableRequests == 1 {
				return okResponse(`+CSIM: 16,"BF31038001079000"`), nil
			}
			client.commitOnce.Do(func() { close(client.commitSeen) })
			<-client.allowCommit
			if client.enableResponse != "" {
				return okResponse(client.enableResponse), nil
			}
			return modem.Response{}, modem.ErrCommandTimeout
		}
		if command == `AT+CSIM=10,"0070000001"` {
			return okResponse(`+CSIM: 6,"019000"`), nil
		}
		return okResponse(`+CSIM: 4,"9000"`), nil
	}
	if command == "AT+CFUN=0" {
		client.resetOnce.Do(func() { close(client.resetSeen) })
		<-client.allowReset
		return modem.Response{}, client.resetFailure
	}
	if command == "ATI" {
		client.mu.Lock()
		client.probeCount++
		client.mu.Unlock()
		if client.probeSeen != nil {
			client.probeOnce.Do(func() {
				close(client.probeSeen)
				<-client.allowProbe
			})
		}
	}
	return client.recoveringSIMClient.Execute(ctx, command)
}

func (client *uncertainSwitchMaintenanceClient) probes() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.probeCount
}

func releaseMaintenanceTestChannel(channel chan struct{}) {
	select {
	case <-channel:
	default:
		close(channel)
	}
}

func TestUncertainESIMSwitchKeepsSnapshotGateUntilDetachedRecoveryFinishes(t *testing.T) {
	cause := errors.New("reset rejected after uncertain EnableProfile")
	client := &uncertainSwitchMaintenanceClient{
		recoveringSIMClient: recoveringSIMClient{pinReads: 1},
		commitSeen:          make(chan struct{}), allowCommit: make(chan struct{}),
		resetSeen: make(chan struct{}), allowReset: make(chan struct{}), resetFailure: cause,
	}
	manager, id := newStartedTestManager(t, client)
	state, _ := manager.lookup(id)
	state.lastICCID = "8900000000000000002"
	state.snapshot = &Snapshot{ICCID: state.lastICCID, SIMReady: true, Responsive: true}
	t.Cleanup(func() {
		releaseMaintenanceTestChannel(client.allowCommit)
		releaseMaintenanceTestChannel(client.allowReset)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- manager.ESIMSwitchProfile(ctx, id, "8900000000000000001", "") }()
	select {
	case <-client.commitSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("EnableProfile was not started")
	}
	if entry, err := manager.Get(id); err != nil || entry.Snapshot != nil {
		t.Fatalf("old snapshot remained trusted during uncertain commit: entry=%+v err=%v", entry, err)
	}
	if _, err := manager.Refresh(context.Background(), id); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("snapshot entered during EnableProfile: %v", err)
	}
	if _, _, _, _, _, err := manager.ReadNativeQMIIdentity(context.Background(), id); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("native identity/auto hot-swap entered during EnableProfile: %v", err)
	}
	if err := manager.ESIMSwitchProfile(context.Background(), id, "8900000000000000002", ""); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("a second switch entered active maintenance: %v", err)
	}
	// The non-idempotent APDU is detached from the client cancellation. Its
	// uncertain result returns while post-commit reset is still blocked.
	cancel()
	close(client.allowCommit)
	select {
	case err := <-result:
		if !errors.Is(err, modem.ErrCommandTimeout) {
			t.Fatalf("uncertain result was lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("uncertain switch did not return")
	}
	select {
	case <-client.resetSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("detached SIM recovery did not begin")
	}
	if _, err := manager.Refresh(context.Background(), id); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("request return released the snapshot gate before recovery: %v", err)
	}
	if client.probes() != 0 {
		t.Fatalf("ordinary ATI probe ran during switch/recovery: %d", client.probes())
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := manager.WaitESIMRecovery(waitContext, id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery waiter ignored its deadline: %v", err)
	}
	cancelWait()
	close(client.allowReset)
	waitContext, cancelWait = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if err := manager.WaitESIMRecovery(waitContext, id); !errors.Is(err, cause) {
		t.Fatalf("recovery error was lost: %v", err)
	}
	if client.probes() == 0 {
		t.Fatal("maintenance owner could not perform its own bounded refresh")
	}
	if err := manager.WaitESIMRecovery(context.Background(), id); !errors.Is(err, cause) {
		t.Fatalf("late waiter lost completed recovery failure: %v", err)
	}
	if _, err := manager.Refresh(context.Background(), id); err != nil {
		t.Fatalf("completed recovery never released the snapshot gate: %v", err)
	}
}

func TestProfileSwitchWaitsForAlreadyRunningSnapshotBeforeCommit(t *testing.T) {
	client := &uncertainSwitchMaintenanceClient{
		recoveringSIMClient: recoveringSIMClient{pinReads: 1},
		commitSeen:          make(chan struct{}), allowCommit: make(chan struct{}),
		resetSeen: make(chan struct{}), allowReset: make(chan struct{}),
		probeSeen: make(chan struct{}), allowProbe: make(chan struct{}),
	}
	manager, id := newStartedTestManager(t, client)
	t.Cleanup(func() { releaseMaintenanceTestChannel(client.allowProbe) })
	refreshDone := make(chan error, 1)
	go func() { _, err := manager.Refresh(context.Background(), id); refreshDone <- err }()
	select {
	case <-client.probeSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot did not enter ATI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := manager.ESIMSwitchProfile(ctx, id, "8900000000000000001", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("switch did not wait for the in-flight snapshot: %v", err)
	}
	select {
	case <-client.commitSeen:
		t.Error("EnableProfile interleaved with the running snapshot")
	default:
	}
	close(client.allowProbe)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original snapshot failed to complete")
	}
}

func TestProfileSwitchRecoveryWithoutLeaseDoesNotWaitUnderESIMLock(t *testing.T) {
	cause := errors.New("reset rejected")
	client := &uncertainSwitchMaintenanceClient{
		recoveringSIMClient: recoveringSIMClient{pinReads: 1},
		resetSeen:           make(chan struct{}), allowReset: make(chan struct{}), resetFailure: cause,
		probeSeen: make(chan struct{}), allowProbe: make(chan struct{}),
	}
	manager, id := newStartedTestManager(t, client)
	t.Cleanup(func() {
		releaseMaintenanceTestChannel(client.allowProbe)
		releaseMaintenanceTestChannel(client.allowReset)
	})
	refreshDone := make(chan error, 1)
	go func() { _, err := manager.Refresh(context.Background(), id); refreshDone <- err }()
	select {
	case <-client.probeSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot did not enter ATI")
	}
	// DisableProfile starts recovery while holding the eSIM/UICC locks and
	// without a pre-existing profile-switch lease. It must return the handle
	// while the earlier snapshot is still draining.
	manager.lockESIM()
	started := make(chan *esimRecovery, 1)
	go func() {
		started <- manager.startProfileSwitchRecovery(id)
		manager.unlockESIM()
	}()
	var recovery *esimRecovery
	select {
	case recovery = <-started:
	case <-time.After(time.Second):
		t.Fatal("recovery waited synchronously for the snapshot under eSIM locks")
	}
	if _, err := manager.Refresh(context.Background(), id); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("new snapshot entered pending recovery: %v", err)
	}
	if err := manager.ESIMSwitchProfile(context.Background(), id, "8900000000000000001", ""); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("new switch entered pending recovery: %v", err)
	}
	close(client.allowProbe)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	close(client.allowReset)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.waitForESIMRecoveryResult(ctx, recovery); !errors.Is(err, cause) {
		t.Fatalf("direct recovery lost its reset result: %v", err)
	}
	if _, err := manager.Refresh(context.Background(), id); err != nil {
		t.Fatalf("direct recovery retained the snapshot gate: %v", err)
	}
}

func TestProfileSwitchOwnerContextSurvivesWrappersButExpiresWithLease(t *testing.T) {
	manager, id := newStartedTestManager(t, &recoveringSIMClient{pinReads: 1})
	lease, err := manager.beginProfileSwitchLease(context.Background(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	owner, cancel := context.WithCancel(context.WithValue(context.Background(), esimSwitchLeaseKey{}, lease))
	cancel()
	detached, cancelDetached := context.WithTimeout(context.WithoutCancel(owner), time.Second)
	defer cancelDetached()
	unlockRead, err := manager.AcquireSIMRead(detached, id)
	if err != nil {
		manager.releaseProfileSwitchLease(id, lease)
		t.Fatalf("derived owner context could not acquire its policy read: %v", err)
	}
	unlockRead()
	if _, err := manager.Refresh(detached, id); err != nil {
		manager.releaseProfileSwitchLease(id, lease)
		t.Fatalf("derived owner context could not refresh its transaction: %v", err)
	}
	if _, _, _, _, _, err := manager.ReadNativeQMIIdentity(detached, id); errors.Is(err, errESIMRecovering) {
		manager.releaseProfileSwitchLease(id, lease)
		t.Fatalf("derived owner context was rejected by the identity gate: %v", err)
	}
	manager.releaseProfileSwitchLease(id, lease)
	next, err := manager.beginProfileSwitchLease(context.Background(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.releaseProfileSwitchLease(id, next)
	if _, err := manager.Refresh(detached, id); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("expired owner bypassed a later switch: %v", err)
	}
	if _, _, _, _, _, err := manager.ReadNativeQMIIdentity(detached, id); !errors.Is(err, errESIMRecovering) {
		t.Fatalf("expired owner bypassed the later identity gate: %v", err)
	}
}

func TestESIMSwitchAIDFallbackUncertaintyStartsRecovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
	}{
		{name: "transport timeout"},
		{name: "missing result", response: `+CSIM: 4,"9000"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("detached reset rejected")
			client := &uncertainSwitchMaintenanceClient{
				recoveringSIMClient: recoveringSIMClient{pinReads: 1},
				commitSeen:          make(chan struct{}), allowCommit: make(chan struct{}),
				resetSeen: make(chan struct{}), allowReset: make(chan struct{}), resetFailure: cause,
				rejectFirstEnable: true, enableResponse: tc.response,
			}
			manager, id := newStartedTestManager(t, client)
			t.Cleanup(func() { releaseMaintenanceTestChannel(client.allowReset) })
			const target = "8900000000000000001"
			manager.cacheESIMInfo(id, EsimInfo{Profiles: []EsimProfile{{
				ICCID: target, AID: "A0000005591010FFFFFFFF8900000101",
			}}})
			close(client.allowCommit)
			err := manager.ESIMSwitchProfile(context.Background(), id, target, "")
			if tc.response == "" {
				if !errors.Is(err, modem.ErrCommandTimeout) {
					t.Fatalf("fallback timeout was replaced by first rejection: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "unexpected EnableProfile response") {
				t.Fatalf("fallback uncertainty was replaced by first rejection: %v", err)
			}
			select {
			case <-client.resetSeen:
			case <-time.After(2 * time.Second):
				t.Fatal("uncertain fallback did not start detached recovery")
			}
			if _, err := manager.Refresh(context.Background(), id); !errors.Is(err, errESIMRecovering) {
				t.Fatalf("fallback released maintenance before recovery finished: %v", err)
			}
			close(client.allowReset)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := manager.WaitESIMRecovery(ctx, id); !errors.Is(err, cause) {
				t.Fatalf("fallback recovery result = %v", err)
			}
		})
	}
}

func TestFreshESIMSwitchDoesNotReusePreviousRecoveryFailure(t *testing.T) {
	client := &uncertainSwitchMaintenanceClient{
		recoveringSIMClient: recoveringSIMClient{pinReads: 1},
		commitSeen:          make(chan struct{}), allowCommit: make(chan struct{}),
		enableResponse: `+CSIM: 16,"BF31038001039000"`,
	}
	manager, id := newStartedTestManager(t, client)
	state, _ := manager.lookup(id)
	const oldICCID = "8900000000000000002"
	state.lastICCID = oldICCID
	state.snapshot = &Snapshot{ICCID: oldICCID, SIMReady: true, Responsive: true}
	previous := &esimRecovery{done: make(chan struct{}), err: errors.New("previous recovery failed")}
	close(previous.done)
	manager.esimLastRecoveries = map[string]*esimRecovery{id: previous}
	close(client.allowCommit)
	if err := manager.ESIMSwitchProfile(context.Background(), id, "8900000000000000001", ""); !errors.Is(err, ErrESIMEnableDisallowedPolicy) {
		t.Fatalf("new switch result = %v", err)
	}
	if err := manager.WaitESIMRecovery(context.Background(), id); err != nil {
		t.Fatalf("new switch reused previous recovery failure: %v", err)
	}
	if entry, err := manager.Get(id); err != nil || entry.Snapshot != nil {
		t.Fatalf("rejected switch left a trusted old snapshot: entry=%+v err=%v", entry, err)
	}
	if state.lastICCID != oldICCID {
		t.Fatalf("commit invalidation lost changed-card detection: lastICCID=%q", state.lastICCID)
	}
	if snapshot, err := manager.Refresh(context.Background(), id); err != nil || snapshot.ICCID != "8900000000000000001" || !snapshot.SIMChanged {
		t.Fatalf("live refresh did not repopulate identity after rejection: snapshot=%+v err=%v", snapshot, err)
	}
}

func TestWaitESIMRecoveryHonorsAlreadyCancelledContext(t *testing.T) {
	manager := &Manager{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.WaitESIMRecovery(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait without recovery ignored cancellation: %v", err)
	}
	completed := &esimRecovery{done: make(chan struct{}), err: errors.New("retained recovery error")}
	close(completed.done)
	manager.esimLastRecoveries = map[string]*esimRecovery{"dev": completed}
	if err := manager.WaitESIMRecovery(ctx, "dev"); !errors.Is(err, context.Canceled) {
		t.Fatalf("completed recovery ignored cancellation: %v", err)
	}
	if err := manager.WaitESIMRecovery(context.Background(), "dev"); !errors.Is(err, completed.err) {
		t.Fatalf("uncancelled waiter lost retained result: %v", err)
	}
}

type maintenanceIdentitySession struct {
	nativeQMIVoWiFiSession
	powerSeen  chan struct{}
	allowPower chan struct{}
	powered    bool
}

func (session *maintenanceIdentitySession) Close() error { return nil }

func (session *maintenanceIdentitySession) GetOperatingMode(context.Context) (qmi.OperatingMode, error) {
	return qmi.ModeLowPower, nil
}

func (session *maintenanceIdentitySession) GetICCID(context.Context) (string, error) {
	if !session.powered {
		return "", errors.New("SIM slot powered down")
	}
	return "8900000000000000001", nil
}

func (session *maintenanceIdentitySession) GetCardStatusDetails(context.Context) (*qmi.CardStatusDetails, qmi.SIMStatus, error) {
	if !session.powered {
		return &qmi.CardStatusDetails{CardState: 2, ErrorCode: 1}, qmi.SIMNotReady, nil
	}
	return &qmi.CardStatusDetails{CardState: 1, NumApp: 1, AppState: qmi.UIMAppStateReady}, qmi.SIMReady, nil
}

func (session *maintenanceIdentitySession) PowerOnSIM(ctx context.Context, _ uint8) error {
	close(session.powerSeen)
	select {
	case <-session.allowPower:
		session.powered = true
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *maintenanceIdentitySession) GetIMSI(context.Context) (string, error) {
	return "001010000000001", nil
}

func (session *maintenanceIdentitySession) GetIMEI(context.Context) (string, error) {
	return "000000000000001", nil
}

func (session *maintenanceIdentitySession) GetNativeMCCMNC(context.Context) (string, string, error) {
	return "001", "01", nil
}

func TestProfileSwitchWaitsForIdentityNestedHotSwapRecovery(t *testing.T) {
	manager, _, id := newStartedNativeQMITestManager(t)
	session := &maintenanceIdentitySession{powerSeen: make(chan struct{}), allowPower: make(chan struct{})}
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) { return session, nil }
	t.Cleanup(func() { releaseMaintenanceTestChannel(session.allowPower) })
	identityDone := make(chan error, 1)
	go func() {
		_, _, _, _, _, err := manager.ReadNativeQMIIdentity(context.Background(), id)
		identityDone <- err
	}()
	select {
	case <-session.powerSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("identity reader did not enter nested hot-swap recovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := manager.ESIMSwitchProfile(ctx, id, "8900000000000000001", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("switch overlapped the identity reader's SIM recovery: %v", err)
	}
	close(session.allowPower)
	select {
	case err := <-identityDone:
		if err != nil {
			t.Fatalf("identity reader could not finish its nested recovery: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nested identity recovery deadlocked on the profile gate")
	}
}
