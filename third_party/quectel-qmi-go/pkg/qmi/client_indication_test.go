package qmi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestClientLogfUsesInjectedLogger(t *testing.T) {
	var gotLevel ClientLogLevel
	var gotMessage string
	c := &Client{
		opts: ClientOptions{
			Logf: func(level ClientLogLevel, format string, args ...any) {
				gotLevel = level
				gotMessage = fmt.Sprintf(format, args...)
			},
		},
	}

	c.logf(ClientLogLevelDebug, "hello %s", "qmi")

	if gotLevel != ClientLogLevelDebug {
		t.Fatalf("level=%v want %v", gotLevel, ClientLogLevelDebug)
	}
	if gotMessage != "hello qmi" {
		t.Fatalf("message=%q want %q", gotMessage, "hello qmi")
	}
}

func TestDispatchIndicationClassifiesNASEventReportSeparately(t *testing.T) {
	c := &Client{
		eventCh:            make(chan Event, 1),
		indicationInCh:     nil,
		closeCh:            make(chan struct{}),
		transactions:       make(map[uint32]*transactionEntry),
		recentTransactions: make(map[uint32]recentTransaction),
		clientIDs:          make(map[uint8]uint8),
	}

	c.dispatchIndication(&Packet{
		ServiceType:  ServiceNAS,
		MessageID:    NASEventReportInd,
		IsIndication: true,
	})

	got := <-c.eventCh
	if got.Type != EventNASEventReport {
		t.Fatalf("event type=%v want EventNASEventReport", got.Type)
	}
	if got.ServiceID != ServiceNAS || got.MessageID != NASEventReportInd {
		t.Fatalf("raw ids service=0x%02x msg=0x%04x", got.ServiceID, got.MessageID)
	}
}

func TestModemResetIndicationNotDroppedWhenQueueFull(t *testing.T) {
	c := &Client{
		opts:              ClientOptions{ReadDeadline: 5 * time.Millisecond},
		eventCh:           make(chan Event, 4),
		indicationInCh:    make(chan Event, 1),
		coalescedSignalCh: make(chan struct{}, 1),
		closeCh:           make(chan struct{}),
		coalesced: coalescedEventStore{
			events: make(map[string]Event),
		},
	}

	// Fill indication queue first so enqueueIndication must use coalesced fallback.
	c.indicationInCh <- Event{Type: EventUnknown}
	c.enqueueIndication(Event{
		Type:      EventModemReset,
		ServiceID: ServiceControl,
		MessageID: CTLRevokeClientIDInd,
	})

	done := make(chan struct{})
	c.wg.Add(1)
	go func() {
		c.indicationLoop()
		close(done)
	}()
	defer func() {
		close(c.closeCh)
		<-done
	}()

	deadline := time.After(1 * time.Second)
	for {
		select {
		case evt := <-c.eventCh:
			if evt.Type == EventModemReset {
				return
			}
		case <-deadline:
			t.Fatal("expected EventModemReset to be delivered")
		}
	}
}

func TestSendRequestWithCanceledContextDoesNotQueueWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Client{
		opts:         DefaultClientOptions(),
		writeCh:      make(chan writeRequest, 1),
		closeCh:      make(chan struct{}),
		transactions: make(map[uint32]*transactionEntry),
	}

	_, err := c.SendRequest(ctx, ServiceUIM, 1, UIMReadRecord, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := len(c.writeCh); got != 0 {
		t.Fatalf("SendRequest queued %d write(s) after context cancellation", got)
	}
	if got := len(c.transactions); got != 0 {
		t.Fatalf("SendRequest left %d transaction(s) after context cancellation", got)
	}
}

func TestCompletedTimedOutTransactionIsRememberedForLateResponse(t *testing.T) {
	c := &Client{
		opts:               DefaultClientOptions(),
		writeCh:            make(chan writeRequest, 1),
		closeCh:            make(chan struct{}),
		transactions:       make(map[uint32]*transactionEntry),
		recentTransactions: make(map[uint32]recentTransaction),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := c.SendRequest(ctx, ServiceUIM, 1, UIMReadRecord, nil)
		errCh <- err
	}()

	wr := <-c.writeCh
	wr.result <- nil

	err := <-errCh
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}

	key := uint32(ServiceUIM)<<16 | 1
	if !c.isRecentTransaction(key, ServiceUIM, UIMReadRecord) {
		t.Fatalf("timed out UIMReadRecord transaction was not retained for late response matching")
	}
	if got := len(c.transactions); got != 0 {
		t.Fatalf("timed out request left %d active transaction(s)", got)
	}
}
