package device

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type transitionHostLink struct {
	entered chan struct{}
	release chan struct{}
	session *fakeATHostSession
}

type canceledStartupHostLink struct{ cancel context.CancelFunc }

func (link canceledStartupHostLink) Start(context.Context, string) (atHostDataSession, error) {
	link.cancel()
	return nil, context.Canceled
}

type uncleanStartupHostLink struct {
	session atHostDataSession
	err     error
}

func (link uncleanStartupHostLink) Start(context.Context, string) (atHostDataSession, error) {
	return link.session, link.err
}

func (link *transitionHostLink) Start(ctx context.Context, _ string) (atHostDataSession, error) {
	close(link.entered)
	select {
	case <-link.release:
		return link.session, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type transitionHostSession struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	err     error
	calls   int
}

func (*transitionHostSession) Status(context.Context) (bool, string, error) {
	return true, "lease ready", nil
}

func (session *transitionHostSession) Close(ctx context.Context) error {
	session.mu.Lock()
	session.calls++
	first := session.calls == 1
	err := session.err
	session.mu.Unlock()
	if first && session.entered != nil {
		close(session.entered)
		select {
		case <-session.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func transitionATSteps() []clientStep {
	return []clientStep{
		{command: "AT+CGACT?", response: okResponse("+CGACT: 1,0")},
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}
}

func transitionATRequest() NetworkRequest {
	return NetworkRequest{Enabled: true, Backend: "at", APN: "internet", IPVersion: "IPV4V6"}
}

func TestNetworkTransitionLockWaitHonorsCancellation(t *testing.T) {
	for _, kind := range []string{"at/op", "at/data", "qmi/data"} {
		t.Run(kind, func(t *testing.T) {
			manager, id := newStartedATDataTestManager(t, &transcriptClient{}, &fakeATHostLink{})
			state, _ := manager.lookup(id)
			mutex := &state.dataMu
			backend := "at"
			if kind == "at/op" {
				mutex = &state.opMu
			} else if kind == "qmi/data" {
				backend = "qmi"
			}
			mutex.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := manager.SetNetwork(ctx, id, NetworkRequest{Enabled: true, Backend: backend})
				done <- err
			}()
			select {
			case err := <-done:
				mutex.Unlock()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lock wait = %v, want deadline", err)
				}
			case <-time.After(time.Second):
				mutex.Unlock()
				<-done
				t.Fatal("canceled operation remained blocked on the transition lock")
			}
		})
	}
}

func TestQMITransitionWaitsForATStartup(t *testing.T) {
	link := &transitionHostLink{
		entered: make(chan struct{}), release: make(chan struct{}), session: &fakeATHostSession{statusOK: true},
	}
	manager, id := newStartedATDataTestManager(t, &transcriptClient{steps: transitionATSteps()}, link)
	done := make(chan error, 1)
	go func() {
		_, err := manager.SetNetwork(context.Background(), id, transitionATRequest())
		done <- err
	}()
	<-link.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := manager.SetNetwork(ctx, id, NetworkRequest{Enabled: false, Backend: "qmi"})
	close(link.release)
	if setupErr := <-done; setupErr != nil {
		t.Fatal(setupErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("QMI transition crossed in-flight AT startup: %v", err)
	}
	if link.session.isClosed() {
		t.Fatal("canceled QMI transition closed the AT startup's lease")
	}
}

func TestQMITransitionWaitsForPreviousATCleanup(t *testing.T) {
	old := &transitionHostSession{entered: make(chan struct{}), release: make(chan struct{})}
	manager, id := newStartedATDataTestManager(t, &transcriptClient{steps: transitionATSteps()}, &fakeATHostLink{})
	state, _ := manager.lookup(id)
	state.atHostSession, state.dataBackend = old, "at"
	done := make(chan error, 1)
	go func() {
		_, err := manager.SetNetwork(context.Background(), id, transitionATRequest())
		done <- err
	}()
	<-old.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := manager.SetNetwork(ctx, id, NetworkRequest{Enabled: false, Backend: "qmi"})
	close(old.release)
	if setupErr := <-done; setupErr != nil {
		t.Fatal(setupErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("QMI transition crossed old AT cleanup: %v", err)
	}
}

func TestFailedATCleanupRetainsOwnerAndBlocksReplacement(t *testing.T) {
	cleanupErr := errors.New("injected host cleanup failure")
	old := &transitionHostSession{err: cleanupErr}
	link := &fakeATHostLink{}
	manager, id := newStartedATDataTestManager(t, &transcriptClient{steps: transitionATSteps()}, link)
	state, _ := manager.lookup(id)
	state.atHostSession, state.dataBackend = old, "at"
	if _, err := manager.SetNetwork(context.Background(), id, transitionATRequest()); !errors.Is(err, cleanupErr) {
		t.Fatalf("setup did not report cleanup failure: %v", err)
	}
	if state.atHostSession != old || len(link.startedInterfaces()) != 0 {
		t.Fatal("failed cleanup lost the previous owner or started its replacement")
	}
	old.mu.Lock()
	old.err = nil
	old.mu.Unlock()
	if _, err := manager.SetNetwork(context.Background(), id, transitionATRequest()); err != nil {
		t.Fatal(err)
	}
	if state.atHostSession == old || len(link.startedInterfaces()) != 1 {
		t.Fatal("successful cleanup retry did not hand ownership to the replacement")
	}
}

func TestATRollbackUsesIndependentCleanupContext(t *testing.T) {
	client := &transcriptClient{steps: append(transitionATSteps(), clientStep{
		command: "AT+CGACT=0,1", response: okResponse(),
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, id := newStartedATDataTestManager(t, client, canceledStartupHostLink{cancel: cancel})
	if _, err := manager.SetNetwork(ctx, id, transitionATRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("setup error = %v, want cancellation", err)
	}
	client.assertDone(t)
}

func TestFailedATStartupRetainsSessionWhoseCleanupFailed(t *testing.T) {
	startupErr, cleanupErr := errors.New("injected DHCP startup failure"), errors.New("injected host cleanup failure")
	session := &transitionHostSession{err: cleanupErr}
	client := &transcriptClient{steps: transitionATSteps()}
	manager, id := newStartedATDataTestManager(t, client, uncleanStartupHostLink{session: session, err: startupErr})
	_, err := manager.SetNetwork(context.Background(), id, transitionATRequest())
	if !errors.Is(err, startupErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("setup did not preserve both startup and cleanup failures: %v", err)
	}
	state, _ := manager.lookup(id)
	if state.atHostSession != session {
		t.Fatal("failed startup lost the remaining host cleanup owner")
	}
	client.assertDone(t) // No PDP rollback may run before host cleanup succeeds.
}

func TestQMISwitchCannotDiscardFailedATCleanup(t *testing.T) {
	cleanupErr := errors.New("injected host cleanup failure")
	old := &transitionHostSession{err: cleanupErr}
	manager, id := newStartedATDataTestManager(t, &transcriptClient{}, &fakeATHostLink{})
	state, _ := manager.lookup(id)
	state.atHostSession, state.dataBackend = old, "at"
	manager.mu.Lock()
	state.candidate.QMIControl = "/dev/cdc-wdm-fixture"
	manager.mu.Unlock()
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{Enabled: true, Backend: "qmi"}); !errors.Is(err, cleanupErr) {
		t.Fatalf("QMI switch did not report AT cleanup failure: %v", err)
	}
	if state.atHostSession != old {
		t.Fatal("QMI switch discarded a failed AT cleanup owner")
	}
}

func TestManagerStopReportsATCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("injected host cleanup failure")
	old := &transitionHostSession{err: cleanupErr}
	manager, id := newStartedATDataTestManager(t, &transcriptClient{}, &fakeATHostLink{})
	state, _ := manager.lookup(id)
	state.atHostSession, state.dataBackend = old, "at"
	if err := manager.Stop(context.Background()); !errors.Is(err, cleanupErr) {
		t.Fatalf("Stop did not report AT cleanup failure: %v", err)
	}
	if state.atHostSession != old {
		t.Fatal("Stop discarded a failed AT cleanup owner")
	}
}
