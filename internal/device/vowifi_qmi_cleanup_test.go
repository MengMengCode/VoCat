package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

type authenticationQMISession struct {
	nativeQMIVoWiFiSession
	cancel   context.CancelFunc
	t        *testing.T
	cleanup  context.Context
	closeErr error
	closed   int
}

func (s *authenticationQMISession) OpenLogicalChannel(context.Context, uint8, []byte) (byte, error) {
	return 7, nil
}

func (s *authenticationQMISession) SendAPDU(ctx context.Context, slot, channel uint8, apdu []byte) ([]byte, error) {
	s.cancel()
	return nil, ctx.Err()
}

func (s *authenticationQMISession) CloseLogicalChannel(ctx context.Context, slot, channel uint8) error {
	if ctx.Err() != nil || slot != 1 || channel != 7 {
		s.t.Fatalf("invalid cleanup: context=%v slot=%d channel=%d", ctx.Err(), slot, channel)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
		s.t.Fatalf("cleanup needs a bounded independent deadline: %v", deadline)
	}
	s.cleanup = ctx
	return s.closeErr
}

func (s *authenticationQMISession) Close() error {
	s.closed++
	return nil
}

func TestAuthenticateNativeQMIChannelCleanup(t *testing.T) {
	for _, closeErr := range []error{nil, errors.New("close failed")} {
		manager, _, id := newStartedNativeQMITestManager(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		session := &authenticationQMISession{cancel: cancel, t: t, closeErr: closeErr}
		manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) { return session, nil }
		_, err := manager.AuthenticateNativeQMI(ctx, id, []byte{0xA0}, []byte{0x00, 0x88, 0x00, 0x81, 0x00})
		if !errors.Is(err, context.Canceled) || (closeErr != nil && !errors.Is(err, closeErr)) {
			t.Fatalf("lost authentication or cleanup error: %v", err)
		}
		if session.cleanup == nil || !errors.Is(session.cleanup.Err(), context.Canceled) || session.closed != 1 {
			t.Fatalf("cleanup context or session not released: %#v", session)
		}
	}
}
