package device

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

type notificationFallbackReply struct {
	body []byte
	sw   int
	err  error
}

// notificationFallbackSession is a nativeQMIEuiccSession that replays a fixed
// script of APDU exchanges, asserting the exact request bytes at each step.
type notificationFallbackSession struct {
	t          *testing.T
	requests   [][]byte
	replies    []notificationFallbackReply
	calls      int
	afterFirst func()
}

func (session *notificationFallbackSession) SendAPDU(_ context.Context, _ uint8, _ uint8, apdu []byte) ([]byte, error) {
	session.t.Helper()
	index := session.calls
	session.calls++
	if index >= len(session.requests) || index >= len(session.replies) {
		session.t.Fatalf("unexpected APDU %X", apdu)
	}
	if !bytes.Equal(apdu, session.requests[index]) {
		session.t.Fatalf("APDU[%d]=%X want=%X", index, apdu, session.requests[index])
	}
	if index == 0 && session.afterFirst != nil {
		session.afterFirst()
	}
	reply := session.replies[index]
	if reply.err != nil {
		return nil, reply.err
	}
	return append(append([]byte(nil), reply.body...), byte(reply.sw>>8), byte(reply.sw)), nil
}

func (session *notificationFallbackSession) OpenLogicalChannel(context.Context, uint8, []byte) (byte, error) {
	return 0, nil
}
func (session *notificationFallbackSession) CloseLogicalChannel(context.Context, uint8, uint8) error {
	return nil
}
func (session *notificationFallbackSession) GetOperatingMode(context.Context) (qmi.OperatingMode, error) {
	var mode qmi.OperatingMode
	return mode, nil
}
func (session *notificationFallbackSession) SetOperatingMode(context.Context, qmi.OperatingMode) error {
	return nil
}
func (session *notificationFallbackSession) Close() error { return nil }

func TestRetrieveNotificationsEncodingFallback(t *testing.T) {
	seq := uint64(598)
	// Fixed bytes from the lpac revision pinned by OpenEUICC (d214738).
	standard := []byte{0x80, 0xE2, 0x91, 0, 9, 0xBF, 0x2B, 6, 0xA0, 4, 0x80, 2, 2, 0x56, 0}
	legacy := []byte{0x80, 0xE2, 0x91, 0, 7, 0xBF, 0x2B, 4, 0x80, 2, 2, 0x56, 0}
	all := []byte{0x80, 0xE2, 0x91, 0, 3, 0xBF, 0x2B, 0, 0}
	metadata := derConstruct(0xBF2F, derEncode(0x80, []byte{2, 0x56}), derEncode(0x81, []byte{4, 0x10}), derEncode(0x0C, []byte("notify.example.com")))
	signed := derConstruct(0x30, metadata, derEncode(0x5F37, []byte{1, 2, 3}))
	success := derConstruct(0xBF2B, derConstruct(0xA0, signed))
	empty := []byte{0xBF, 0x2B, 2, 0xA0, 0}
	undefined := []byte{0xBF, 0x2B, 3, 0x81, 1, 0x7F}
	networkErr := errors.New("APDU transport failure")
	for _, tc := range []struct {
		name          string
		requests      [][]byte
		replies       []notificationFallbackReply
		noSeq, cancel bool
		count         int
		wantErr       bool
		cause         error
	}{
		{name: "standard_success", requests: [][]byte{standard}, replies: []notificationFallbackReply{{success, 0x9000, nil}}, count: 1},
		{name: "legacy_success", requests: [][]byte{standard, legacy}, replies: []notificationFallbackReply{{undefined, 0x9000, nil}, {success, 0x9000, nil}}, count: 1},
		{name: "empty_no_fallback", requests: [][]byte{standard}, replies: []notificationFallbackReply{{empty, 0x9000, nil}}},
		{name: "both_rejected", requests: [][]byte{standard, legacy}, replies: []notificationFallbackReply{{undefined, 0x9000, nil}, {undefined, 0x9000, nil}}, wantErr: true},
		{name: "transport_no_fallback", requests: [][]byte{standard}, replies: []notificationFallbackReply{{nil, 0, networkErr}}, wantErr: true, cause: networkErr},
		{name: "malformed_no_fallback", requests: [][]byte{standard}, replies: []notificationFallbackReply{{[]byte{0xBF}, 0x9000, nil}}, wantErr: true},
		{name: "malformed_error_no_fallback", requests: [][]byte{standard}, replies: []notificationFallbackReply{{[]byte{0xBF, 0x2B, 4, 0x81, 1, 0x7F, 0xFF}, 0x9000, nil}}, wantErr: true},
		{name: "other_error_no_fallback", requests: [][]byte{standard}, replies: []notificationFallbackReply{{[]byte{0xBF, 0x2B, 3, 0x81, 1, 1}, 0x9000, nil}}, wantErr: true},
		{name: "status_no_fallback", requests: [][]byte{standard}, replies: []notificationFallbackReply{{nil, 0x6A80, nil}}, wantErr: true},
		{name: "all_no_duplicate", noSeq: true, requests: [][]byte{all}, replies: []notificationFallbackReply{{undefined, 0x9000, nil}}, wantErr: true},
		{name: "cancel_no_fallback", cancel: true, requests: [][]byte{standard}, replies: []notificationFallbackReply{{undefined, 0x9000, nil}}, wantErr: true, cause: context.Canceled},
		{name: "legacy_transport_preserves_error", requests: [][]byte{standard, legacy}, replies: []notificationFallbackReply{{undefined, 0x9000, nil}, {nil, 0, networkErr}}, wantErr: true, cause: networkErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session := &notificationFallbackSession{t: t, requests: tc.requests, replies: tc.replies}
			if tc.cancel {
				session.afterFirst = cancel
			}
			channel := &euiccChannel{qmiSession: session}
			param := &seq
			if tc.noSeq {
				param = nil
			}
			got, err := channel.retrieveNotifications(ctx, param)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("cause lost: %v", err)
			}
			if len(got) != tc.count {
				t.Fatalf("count=%d want=%d", len(got), tc.count)
			}
			if len(got) > 0 && (got[0].SequenceNumber != seq || !bytes.Equal(got[0].raw, signed)) {
				t.Fatal("notification changed")
			}
			if session.calls != len(tc.requests) {
				t.Fatalf("calls=%d want=%d", session.calls, len(tc.requests))
			}
		})
	}
}
