package device

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"vocat/internal/loghub"
	"vocat/internal/modem"
)

// 未实现的 QMI 能力会直接使测试失败，避免探测意外触发射频或 SIM 重置。
type inventoryProbeQMISession struct {
	nativeQMIEuiccSession
	t          *testing.T
	available  bool
	profileErr error
	opened     bool
	closed     bool
}

func (session *inventoryProbeQMISession) OpenLogicalChannel(_ context.Context, slot uint8, aid []byte) (byte, error) {
	if slot != 1 {
		session.t.Fatalf("unexpected QMI slot %d", slot)
	}
	aidHex := strings.ToUpper(hex.EncodeToString(aid))
	if aidHex == estkProductAID || (session.available && (aidHex == estkSE0AID || aidHex == estkSE1AID)) {
		session.opened = true
		return 7, nil
	}
	return 0, errors.New("QMI eUICC application not found")
}

func (session *inventoryProbeQMISession) CloseLogicalChannel(_ context.Context, slot, channel uint8) error {
	if slot != 1 || channel != 7 || !session.opened || session.closed {
		session.t.Fatalf("closed an unowned QMI channel: slot=%d channel=%d", slot, channel)
	}
	session.closed = true
	return nil
}

func (session *inventoryProbeQMISession) SendAPDU(_ context.Context, slot, channel uint8, apdu []byte) ([]byte, error) {
	if slot != 1 || channel != 7 || !session.opened || session.closed {
		session.t.Fatalf("unexpected QMI APDU: slot=%d channel=%d", slot, channel)
	}
	var payload []byte
	switch {
	case bytes.Contains(apdu, []byte{0xBF, 0x2D}):
		if session.profileErr != nil {
			return nil, session.profileErr
		}
		iccid, err := encodeICCID("8900000000000000001")
		if err != nil {
			session.t.Fatal(err)
		}
		payload = derConstruct(0xBF2D, derConstruct(0xE3, derEncode(0x5A, iccid)))
	case bytes.Contains(apdu, []byte{0xBF, 0x3E}):
		payload = derConstruct(0xBF3E, derEncode(0x5A, bytes.Repeat([]byte{0x89}, 16)))
	case bytes.Contains(apdu, []byte{0xBF, 0x22}):
		payload = []byte{0xBF, 0x22, 0x00}
	case bytes.Contains(apdu, []byte{0xBF, 0x3C}):
		payload = []byte{0xBF, 0x3C, 0x00}
	default:
		session.t.Fatalf("unexpected APDU %X", apdu)
	}
	return append(payload, 0x90, 0x00), nil
}

func (session *inventoryProbeQMISession) Close() error {
	if session.opened && !session.closed {
		session.t.Fatal("QMI session released before its channel was closed")
	}
	return nil
}

type inventoryProbeATClient struct {
	*transcriptClient
	afterExecute func(context.Context, string)
}

func (client *inventoryProbeATClient) Execute(ctx context.Context, command string) (modem.Response, error) {
	response, err := client.transcriptClient.Execute(ctx, command)
	if client.afterExecute != nil {
		client.afterExecute(ctx, command)
	}
	return response, err
}

func TestESIMInventoryQMIATChannelProbe(t *testing.T) {
	profileFailure := errors.New("profile read failed")
	openFailure := errors.New("AT channel open failed")
	closeFailure := errors.New("AT channel close failed")
	for _, test := range []struct {
		name              string
		initiallyReady    bool
		recovers          bool
		noATPort          bool
		cancelBeforeProbe bool
		cancelAfterOpen   bool
		profileErr        error
		openErr           error
		closeErr          error
		closeResponse     string
		wantErr           error
		wantProbe         bool
		wantRetry         bool
	}{
		{name: "healthy", initiallyReady: true},
		{name: "recovers_two_storages", recovers: true, wantProbe: true, wantRetry: true},
		{name: "still_missing_retries_only_once", wantProbe: true, wantRetry: true, wantErr: ErrNoEUICC},
		{name: "no_AT_port", noATPort: true, wantErr: ErrNoEUICC},
		{name: "profile_error_is_not_retried", initiallyReady: true, profileErr: profileFailure, wantErr: profileFailure},
		{name: "canceled_before_probe", cancelBeforeProbe: true, wantErr: context.Canceled},
		{name: "canceled_after_open_still_closes", cancelAfterOpen: true, wantProbe: true, wantErr: context.Canceled},
		{name: "open_failed", openErr: openFailure, wantProbe: true, wantErr: openFailure},
		{name: "close_failed", closeErr: closeFailure, wantProbe: true, wantErr: closeFailure},
		{name: "close_rejected", closeResponse: `+CSIM: 4,"6A81"`, wantProbe: true, wantErr: ErrNoEUICC},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, opener, id := newStartedNativeQMITestManager(t)
			if err := manager.SetBackend(id, "qmi"); err != nil {
				t.Fatal(err)
			}
			hub := loghub.New(nil, 100)
			manager.logger = slog.New(hub)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := test.initiallyReady
			qmiOpens, opensBeforeProbe := 0, 0
			manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
				qmiOpens++
				if test.cancelBeforeProbe {
					cancel()
				}
				return &inventoryProbeQMISession{t: t, available: ready, profileErr: test.profileErr}, nil
			}
			client := &inventoryProbeATClient{transcriptClient: &transcriptClient{}}
			opener.client = client
			if test.noATPort {
				state, _ := manager.lookup(id)
				state.candidate.ATPort = modem.Port{}
			}
			if test.wantProbe {
				client.steps = append(client.steps, clientStep{
					command: `AT+CSIM=10,"0070000001"`, response: okResponse(`+CSIM: 6,"029000"`), err: test.openErr,
				})
				if test.openErr == nil {
					closeResponse := test.closeResponse
					if closeResponse == "" {
						closeResponse = `+CSIM: 4,"9000"`
					}
					client.steps = append(client.steps, clientStep{
						command: `AT+CSIM=10,"0070800200"`, response: okResponse(closeResponse), err: test.closeErr,
					})
				}
			}
			client.afterExecute = func(commandContext context.Context, command string) {
				if manager.uiccMu.TryLock() {
					manager.uiccMu.Unlock()
					t.Fatal("AT probe did not hold the shared UICC lock")
				}
				if commandContext.Err() != nil {
					t.Fatalf("AT probe/cleanup context already canceled: %v", commandContext.Err())
				}
				deadline, ok := commandContext.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
					t.Fatalf("AT probe/cleanup must be bounded to two seconds: %v", deadline)
				}
				if command == `AT+CSIM=10,"0070000001"` {
					opensBeforeProbe = qmiOpens
					if test.cancelAfterOpen {
						cancel()
					}
				} else if test.closeErr == nil {
					ready = test.recovers
				}
			}

			entries, err := manager.ESIMInventory(ctx, id)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("inventory error = %v, want %v", err, test.wantErr)
			}
			if err == nil && (len(entries) != 2 || len(entries[0].Info.Profiles) != 1 || len(entries[1].Info.Profiles) != 1) {
				t.Fatalf("expected a fresh QMI inventory with two storages: %#v", entries)
			}
			if test.wantProbe {
				if opensBeforeProbe == 0 || (qmiOpens > opensBeforeProbe) != test.wantRetry {
					t.Fatalf("QMI opens before/after probe = %d/%d, retry=%v", opensBeforeProbe, qmiOpens, test.wantRetry)
				}
				if len(hub.History(10, slog.LevelInfo, "")) != 1 && !test.cancelAfterOpen {
					t.Fatal("probe outcome was not logged exactly once")
				}
			} else if len(hub.History(10, slog.LevelInfo, "")) != 0 {
				t.Fatal("inventory logged a probe that was not attempted")
			}
			client.assertDone(t)
		})
	}
}

func TestESIMInventoryATTransportDoesNotAddChannelProbe(t *testing.T) {
	var steps []clientStep
	for _, aid := range []string{estkProductAID, isdRAID, xesimISDRAID, isdRAID} {
		steps = append(steps,
			clientStep{command: `AT+CSIM=10,"0070000001"`, response: okResponse(`+CSIM: 6,"029000"`)},
			clientStep{command: `AT+CSIM=42,"02A4040010` + aid + `"`, response: okResponse(`+CSIM: 4,"6A82"`)},
			clientStep{command: `AT+CSIM=10,"0070800200"`, response: okResponse(`+CSIM: 4,"9000"`)},
		)
	}
	client := &transcriptClient{steps: steps}
	manager, id := newStartedTestManager(t, client)
	if _, err := manager.ESIMInventory(context.Background(), id); !errors.Is(err, ErrNoEUICC) {
		t.Fatalf("inventory error = %v, want no eUICC", err)
	}
	client.assertDone(t)
}

func TestProbeATLogicalChannelRejectsInvalidResponses(t *testing.T) {
	for _, response := range []string{`+CSIM: 4,"6A81"`, `+CSIM: 4,"9000"`, `+CSIM: 6,"009000"`, `+CSIM: 6,"149000"`} {
		t.Run(response, func(t *testing.T) {
			client := &transcriptClient{steps: []clientStep{{
				command: `AT+CSIM=10,"0070000001"`, response: okResponse(response),
			}}}
			manager, id := newStartedTestManager(t, client)
			manager.LockUICC()
			err := manager.probeATLogicalChannel(context.Background(), id)
			manager.UnlockUICC()
			if err == nil {
				t.Fatal("invalid channel allocation was accepted")
			}
			client.assertDone(t)
		})
	}
}
