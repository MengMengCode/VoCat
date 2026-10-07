package device

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"vocat/internal/loghub"
)

// 只覆盖鉴权需要的调用，意外使用其他 QMI 能力会使测试失败。
type authenticationQMISession struct {
	nativeQMIVoWiFiSession
	openChannel  func(context.Context, uint8, []byte) (byte, error)
	sendAPDU     func(context.Context, uint8, uint8, []byte) ([]byte, error)
	closeChannel func(context.Context, uint8, uint8) error
	closeCount   int
}

func (session *authenticationQMISession) OpenLogicalChannel(ctx context.Context, slot uint8, aid []byte) (byte, error) {
	return session.openChannel(ctx, slot, aid)
}

func (session *authenticationQMISession) SendAPDU(ctx context.Context, slot, channel uint8, apdu []byte) ([]byte, error) {
	return session.sendAPDU(ctx, slot, channel, apdu)
}

func (session *authenticationQMISession) CloseLogicalChannel(ctx context.Context, slot, channel uint8) error {
	return session.closeChannel(ctx, slot, channel)
}

func (session *authenticationQMISession) Close() error {
	session.closeCount++
	return nil
}

func TestAuthenticateNativeQMIChannelCleanup(t *testing.T) {
	apduFailure := errors.New("authentication APDU failed")
	closeFailure := errors.New("logical channel close failed")
	openFailure := errors.New("logical channel open failed")
	for _, test := range []struct {
		name           string
		cancelRequest  bool
		requestTimeout time.Duration
		apduErr        error
		closeErr       error
		openErr        error
	}{
		{name: "success"},
		{name: "request_canceled", cancelRequest: true, apduErr: context.Canceled},
		{name: "request_deadline_exceeded", requestTimeout: 100 * time.Millisecond, apduErr: context.DeadlineExceeded},
		{name: "authentication_failed", apduErr: apduFailure},
		{name: "close_failed", closeErr: closeFailure},
		{name: "authentication_and_close_failed", apduErr: apduFailure, closeErr: closeFailure},
		{name: "open_failed", openErr: openFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _, id := newStartedNativeQMITestManager(t)
			hub := loghub.New(nil, 100)
			manager.logger = slog.New(hub)
			ctx, cancel := context.WithCancel(context.Background())
			if test.requestTimeout > 0 {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), test.requestTimeout)
			}
			defer cancel()

			const ownedChannel uint8 = 7
			aid := []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02}
			apdu := []byte{0x00, 0x88, 0x00, 0x81, 0x00}
			wantResponse := []byte{0x90, 0x00}
			var cleanupContext context.Context
			sendCount, closeCount := 0, 0
			session := &authenticationQMISession{
				openChannel: func(_ context.Context, slot uint8, gotAID []byte) (byte, error) {
					if slot != 1 || !bytes.Equal(gotAID, aid) {
						t.Fatalf("open channel: slot=%d AID=%X", slot, gotAID)
					}
					return ownedChannel, test.openErr
				},
				sendAPDU: func(requestContext context.Context, slot, channel uint8, gotAPDU []byte) ([]byte, error) {
					sendCount++
					if requestContext != ctx || slot != 1 || channel != ownedChannel || !bytes.Equal(gotAPDU, apdu) {
						t.Fatalf("unexpected authentication request: slot=%d channel=%d APDU=%X", slot, channel, gotAPDU)
					}
					if test.cancelRequest {
						cancel()
					}
					if test.requestTimeout > 0 {
						<-requestContext.Done()
					}
					if test.apduErr != nil {
						return nil, test.apduErr
					}
					return wantResponse, nil
				},
				closeChannel: func(closeContext context.Context, slot, channel uint8) error {
					closeCount++
					if slot != 1 || channel != ownedChannel {
						t.Fatalf("closed an unowned channel: slot=%d channel=%d", slot, channel)
					}
					// 模拟底层库：已取消的上下文不能发送关闭命令。
					if err := closeContext.Err(); err != nil {
						t.Fatalf("cleanup was canceled by the authentication request: %v", err)
					}
					deadline, ok := closeContext.Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
						t.Fatalf("cleanup needs its own deadline within two seconds: %v, %v", deadline, ok)
					}
					cleanupContext = closeContext
					return test.closeErr
				},
			}
			manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
				return session, nil
			}

			response, err := manager.AuthenticateNativeQMI(ctx, id, aid, apdu)
			for _, wantErr := range []error{test.openErr, test.apduErr, test.closeErr} {
				if wantErr != nil && !errors.Is(err, wantErr) {
					t.Fatalf("authentication error = %v, missing %v", err, wantErr)
				}
			}
			if test.openErr == nil && test.apduErr == nil && test.closeErr == nil && err != nil {
				t.Fatalf("successful authentication returned %v", err)
			}
			if test.openErr == nil && test.apduErr == nil && !bytes.Equal(response, wantResponse) {
				t.Fatalf("authentication response changed: %X", response)
			}
			wantCalls := 1
			if test.openErr != nil {
				wantCalls = 0
			}
			if sendCount != wantCalls || closeCount != wantCalls || session.closeCount != 1 {
				t.Fatalf("send=%d channel close=%d session close=%d; want %d, %d, 1", sendCount, closeCount, session.closeCount, wantCalls, wantCalls)
			}
			if cleanupContext != nil && !errors.Is(cleanupContext.Err(), context.Canceled) {
				t.Fatalf("cleanup context not released after return: %v", cleanupContext.Err())
			}
			entries := hub.History(10, slog.LevelDebug, "")
			if test.closeErr == nil {
				if len(entries) != 0 {
					t.Fatalf("successful cleanup produced logs: %#v", entries)
				}
			} else if len(entries) != 1 || entries[0].Fields["device_id"] != id || entries[0].Fields["channel"] != uint64(ownedChannel) || entries[0].Fields["error"] != closeFailure.Error() {
				t.Fatalf("missing cleanup failure diagnostics: %#v", entries)
			}
		})
	}
}
