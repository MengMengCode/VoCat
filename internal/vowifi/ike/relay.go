package ike

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type sessionRelay struct {
	transport  datagramTransport
	suite      negotiatedSuite
	keys       ikeKeys
	spii       [8]byte
	spir       [8]byte
	natt       bool
	keepalive  time.Duration
	dpdTimeout time.Duration
	nextID     uint32

	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	esp      chan []byte
	failures chan error

	mu              sync.Mutex
	lastErr         error
	lastDPD         time.Time
	pendingDPD      uint32
	pendingDeadline time.Time
	missedDPD       int
}

const dpdMissLimit = 3

func newSessionRelay(
	transport datagramTransport,
	suite negotiatedSuite,
	keys ikeKeys,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	deleteMessageID uint32,
	natt bool,
	keepalive time.Duration,
) *sessionRelay {
	if keepalive <= 0 {
		keepalive = 15 * time.Second
	}
	dpdTimeout := keepalive / 2
	if dpdTimeout < 100*time.Millisecond {
		dpdTimeout = 100 * time.Millisecond
	}
	if dpdTimeout > 5*time.Second {
		dpdTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	relay := &sessionRelay{
		transport:  transport,
		suite:      suite,
		keys:       keys,
		spii:       initiatorSPI,
		spir:       responderSPI,
		natt:       natt,
		keepalive:  keepalive,
		dpdTimeout: dpdTimeout,
		nextID:     deleteMessageID,
		lastDPD:    time.Now(),
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		esp:        make(chan []byte, 64),
		failures:   make(chan error, 1),
	}
	go relay.run()
	return relay
}

func (relay *sessionRelay) run() {
	defer close(relay.done)
	defer close(relay.esp)
	buffer := make([]byte, 65535)
	lastKeepalive := time.Now()
	for {
		if err := relay.ctx.Err(); err != nil {
			return
		}
		n, isIKE, err := relay.transport.ReceiveSessionPacket(relay.ctx, buffer)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				return
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				if err := relay.tick(time.Now()); err != nil {
					relay.fail(err)
					return
				}
				if relay.natt && time.Since(lastKeepalive) >= relay.keepalive {
					if sendErr := relay.transport.SendSessionPacket(relay.ctx, []byte{0xff}, false); sendErr != nil {
						relay.fail(sendErr)
						return
					}
					lastKeepalive = time.Now()
				}
				continue
			}
			relay.fail(err)
			return
		}
		packet := append([]byte(nil), buffer[:n]...)
		if isIKE {
			if err := relay.handleIKE(packet); err != nil {
				if errors.Is(err, errMismatchedSessionSPIs) {
					// A reconnect can reuse the same NAT mapping while the ePDG still
					// has packets queued for the previous IKE SA. Those packets are
					// unrelated to this authenticated session and must be discarded;
					// treating one as fatal tears down the newly established CHILD_SA.
					continue
				}
				relay.fail(err)
				return
			}
			if err := relay.tick(time.Now()); err != nil {
				relay.fail(err)
				return
			}
			continue
		}
		if len(packet) == 1 && packet[0] == 0xff {
			// Peer NAT keepalive.
			continue
		}
		if len(packet) < 8 {
			// Unauthenticated network input must not tear down the session.
			continue
		}
		select {
		case relay.esp <- packet:
		default:
			// Keep the sole socket reader available for IKE/DPD if the
			// data-plane consumer falls behind.
		case <-relay.ctx.Done():
			return
		}
		if err := relay.tick(time.Now()); err != nil {
			relay.fail(err)
			return
		}
	}
}

var errMismatchedSessionSPIs = errors.New("ike: session packet has mismatched SPIs")

func (relay *sessionRelay) handleIKE(packet []byte) error {
	header, _, err := parseIKEPacket(packet)
	if err != nil {
		return err
	}
	if header.InitiatorSPI != relay.spii || header.ResponderSPI != relay.spir {
		return errMismatchedSessionSPIs
	}
	if header.Flags&flagResponse != 0 {
		decryptedHeader, _, err := decryptPayloads(packet, relay.suite, relay.keys.SKer, relay.keys.SKar)
		if err != nil {
			return err
		}
		relay.mu.Lock()
		if relay.pendingDPD != 0 && decryptedHeader.MessageID == relay.pendingDPD {
			relay.pendingDPD = 0
			relay.pendingDeadline = time.Time{}
			relay.missedDPD = 0
		}
		relay.mu.Unlock()
		return nil
	}
	if header.Exchange != exchangeInformational {
		return fmt.Errorf("ike: unsupported responder-initiated exchange %d", header.Exchange)
	}
	decryptedHeader, payloads, err := decryptPayloads(packet, relay.suite, relay.keys.SKer, relay.keys.SKar)
	if err != nil {
		return err
	}
	if len(payloads) != 0 {
		return errors.New("ike: responder INFORMATIONAL request is not an empty DPD probe")
	}
	response, err := encryptPayloads(ikeHeader{
		InitiatorSPI: relay.spii,
		ResponderSPI: relay.spir,
		Exchange:     exchangeInformational,
		Flags:        flagInitiator | flagResponse,
		MessageID:    decryptedHeader.MessageID,
	}, nil, relay.suite, relay.keys.SKei, relay.keys.SKai, nil)
	if err != nil {
		return err
	}
	return relay.transport.SendSessionPacket(relay.ctx, response, true)
}

func (relay *sessionRelay) tick(now time.Time) error {
	relay.mu.Lock()
	if relay.pendingDPD != 0 {
		if now.Before(relay.pendingDeadline) {
			relay.mu.Unlock()
			return nil
		}
		relay.missedDPD++
		relay.pendingDPD = 0
		relay.pendingDeadline = time.Time{}
		if relay.missedDPD >= dpdMissLimit {
			missed := relay.missedDPD
			relay.mu.Unlock()
			return fmt.Errorf("ike: IKEv2 liveness check failed after %d unanswered DPD probes", missed)
		}
	}
	if now.Sub(relay.lastDPD) < relay.keepalive {
		relay.mu.Unlock()
		return nil
	}
	messageID := relay.nextID
	relay.nextID++
	if relay.nextID == 0 {
		relay.nextID = 1
	}
	relay.lastDPD = now
	relay.pendingDPD = messageID
	relay.pendingDeadline = now.Add(relay.dpdTimeout)
	relay.mu.Unlock()

	packet, err := encryptPayloads(ikeHeader{
		InitiatorSPI: relay.spii,
		ResponderSPI: relay.spir,
		Exchange:     exchangeInformational,
		Flags:        flagInitiator,
		MessageID:    messageID,
	}, nil, relay.suite, relay.keys.SKei, relay.keys.SKai, nil)
	if err != nil {
		return fmt.Errorf("ike: build IKEv2 liveness probe: %w", err)
	}
	if err := relay.transport.SendSessionPacket(relay.ctx, packet, true); err != nil {
		return fmt.Errorf("ike: send IKEv2 liveness probe: %w", err)
	}
	return nil
}

func (relay *sessionRelay) fail(err error) {
	relay.mu.Lock()
	notify := false
	if relay.lastErr == nil {
		relay.lastErr = err
		notify = true
	}
	relay.mu.Unlock()
	if notify {
		select {
		case relay.failures <- err:
		default:
		}
	}
	relay.cancel()
}

func (relay *sessionRelay) Failures() <-chan error {
	return relay.failures
}

func (relay *sessionRelay) SendESP(ctx context.Context, packet []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-relay.done:
		return relay.terminalError()
	default:
	}
	return relay.transport.SendSessionPacket(ctx, packet, false)
}

func (relay *sessionRelay) ReceiveESP(ctx context.Context, buffer []byte) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case packet, ok := <-relay.esp:
		if !ok {
			return 0, relay.terminalError()
		}
		if len(packet) > len(buffer) {
			return 0, errors.New("ike: ESP receive buffer is too small")
		}
		copy(buffer, packet)
		return len(packet), nil
	}
}

func (relay *sessionRelay) terminalError() error {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.lastErr != nil {
		return relay.lastErr
	}
	return net.ErrClosed
}

func (relay *sessionRelay) Close() error {
	relay.cancel()
	// ReceiveSessionPacket implementations normally observe the canceled
	// context through a short read deadline. Close the transport as an explicit
	// wake-up as well: a socket implementation that is stuck in Read must not
	// hold teardown (and the associated TUN interface) indefinitely.
	transportErr := relay.transport.Close()
	<-relay.done
	return errors.Join(relay.terminalErrorIfFailure(), transportErr)
}

func (relay *sessionRelay) CloseWithDelete(ctx context.Context) error {
	deleteErr := relay.sendIKEDelete(ctx)
	return errors.Join(deleteErr, relay.Close())
}

func (relay *sessionRelay) sendIKEDelete(ctx context.Context) error {
	relay.mu.Lock()
	messageID := relay.nextID
	relay.nextID++
	if relay.nextID == 0 {
		relay.nextID = 1
	}
	relay.mu.Unlock()
	return sendIKESADelete(ctx, relay.transport, relay.suite, relay.keys, relay.spii, relay.spir, messageID)
}

func sendIKESADelete(
	ctx context.Context,
	transport datagramTransport,
	suite negotiatedSuite,
	keys ikeKeys,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	messageID uint32,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		// Teardown is often called with the operation context already canceled.
		// Give the protocol-level release a short independent chance to leave.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		defer cancel()
	}
	request, err := encryptPayloads(ikeHeader{
		InitiatorSPI: initiatorSPI,
		ResponderSPI: responderSPI,
		Exchange:     exchangeInformational,
		Flags:        flagInitiator,
		MessageID:    messageID,
	}, []payload{{
		Type: payloadDelete,
		Body: []byte{protocolIKE, 0, 0, 0},
	}}, suite, keys.SKei, keys.SKai, nil)
	if err != nil {
		return fmt.Errorf("ike: build IKE SA delete: %w", err)
	}
	if err := transport.SendSessionPacket(ctx, request, true); err != nil {
		return fmt.Errorf("ike: send IKE SA delete: %w", err)
	}
	return nil
}

func (relay *sessionRelay) terminalErrorIfFailure() error {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return relay.lastErr
}

var _ NATTPacketRelay = (*sessionRelay)(nil)
