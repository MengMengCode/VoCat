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
	transport datagramTransport
	suite     negotiatedSuite
	keys      ikeKeys
	spii      [8]byte
	spir      [8]byte
	natt      bool
	keepalive time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	esp    chan []byte

	mu      sync.Mutex
	lastErr error
}

func newSessionRelay(
	transport datagramTransport,
	suite negotiatedSuite,
	keys ikeKeys,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	natt bool,
	keepalive time.Duration,
) *sessionRelay {
	if keepalive <= 0 {
		keepalive = 20 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	relay := &sessionRelay{
		transport: transport,
		suite:     suite,
		keys:      keys,
		spii:      initiatorSPI,
		spir:      responderSPI,
		natt:      natt,
		keepalive: keepalive,
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		esp:       make(chan []byte, 64),
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

func (relay *sessionRelay) fail(err error) {
	relay.mu.Lock()
	if relay.lastErr == nil {
		relay.lastErr = err
	}
	relay.mu.Unlock()
	relay.cancel()
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

func (relay *sessionRelay) terminalErrorIfFailure() error {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return relay.lastErr
}

var _ NATTPacketRelay = (*sessionRelay)(nil)
