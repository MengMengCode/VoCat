package ims

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func ussiCancellationTestSession() (*Session, *identityTestConn) {
	conn := &identityTestConn{}
	return &Session{
		provider: &Provider{config: Config{TransactionTimeout: 10 * time.Millisecond}},
		conn:     conn, transport: "tcp", cseq: 1,
		transactions: make(map[sipTransactionKey]chan *sipResponse),
		ussiDialogs: map[string]*ussiDialog{"dialog-1": {
			callID: "dialog-1", target: "sip:as@example.test",
			from: "<sip:ue@example.test>;tag=local", to: "<sip:as@example.test>;tag=remote",
			state: "awaiting_input", turns: make(chan ussiTurnEvent, 1),
		}},
	}, conn
}

func TestSessionUSSIRejectsUnknownExplicitDialog(t *testing.T) {
	for _, operation := range []string{"continue", "cancel"} {
		for _, count := range []int{0, 1, 2} {
			t.Run(operation+"/"+strconv.Itoa(count), func(t *testing.T) {
				session, conn := ussiCancellationTestSession()
				if count == 0 {
					delete(session.ussiDialogs, "dialog-1")
				}
				if count == 2 {
					session.ussiDialogs["dialog-2"] = &ussiDialog{callID: "dialog-2"}
				}
				writes := 0
				conn.onWrite = func(p []byte) (int, error) { writes++; return len(p), nil }
				var err error
				if operation == "continue" {
					_, err = session.sendUSSISessionContinue(context.Background(), vowifi.USSISubmitRequest{SessionID: "stale-dialog", Input: "1"})
				} else {
					err = session.CancelUSSI(context.Background(), "stale-dialog")
				}
				if !errors.Is(err, vowifi.ErrUSSISessionNotFound) || writes != 0 || len(session.ussiDialogs) != count {
					t.Fatalf("unknown ID affected another dialog: err=%v writes=%d remaining=%d", err, writes, len(session.ussiDialogs))
				}
			})
		}
	}
}

func TestSessionUSSICancelFailureRetainsRecordAndCanRetry(t *testing.T) {
	for _, failure := range []string{"write", "rejected", "timeout"} {
		for _, finalCode := range []int{200, 481} {
			t.Run(failure+"/"+strconv.Itoa(finalCode), func(t *testing.T) {
				session, conn := ussiCancellationTestSession()
				writes := 0
				conn.onWrite = func(packet []byte) (int, error) {
					writes++
					parsed, err := parseSIPPacket(packet)
					if err != nil || parsed.Request == nil {
						t.Fatalf("invalid SIP: %v", err)
					}
					r := parsed.Request
					if r.Method != "BYE" || r.value("Call-ID") != "dialog-1" || !strings.Contains(r.value("To"), "tag=remote") {
						t.Fatalf("wrong dialog: %+v", r)
					}
					status := finalCode
					if writes == 1 {
						switch failure {
						case "write":
							return 0, errors.New("injected connection failure")
						case "timeout":
							return len(packet), nil
						case "rejected":
							status = 503
						}
					}
					session.dispatchPacket(sipPacket{Response: &sipResponse{StatusCode: status, Headers: map[string][]string{
						"call-id": {r.value("Call-ID")}, "cseq": {r.value("CSeq")},
					}}}, nil)
					return len(packet), nil
				}
				if err := session.CancelUSSI(context.Background(), "dialog-1"); err == nil {
					t.Fatal("cancel failure was hidden")
				}
				if dialog := session.ussiDialogs["dialog-1"]; dialog == nil || !dialog.cancelPending {
					t.Fatal("failed BYE lost its cancellation record")
				}
				if _, err := session.sendUSSISessionContinue(context.Background(), vowifi.USSISubmitRequest{SessionID: "dialog-1", Input: "1"}); err == nil || writes != 1 {
					t.Fatalf("resumed a session after BYE: err=%v writes=%d", err, writes)
				}
				if err := session.CancelUSSI(context.Background(), "dialog-1"); err != nil {
					t.Fatalf("retry failed: %v", err)
				}
				if len(session.ussiDialogs) != 0 || writes != 2 {
					t.Fatalf("successful cancellation not released: dialogs=%d writes=%d", len(session.ussiDialogs), writes)
				}
			})
		}
	}
}
