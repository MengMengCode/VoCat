package ike

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSessionPacket struct {
	data []byte
	ike  bool
	err  error
}

type fakeSentPacket struct {
	data []byte
	ike  bool
}

type fakeSessionTransport struct {
	incoming      chan fakeSessionPacket
	sent          chan fakeSentPacket
	closed        chan struct{}
	ignoreContext bool
	once          sync.Once
	readers       atomic.Int32
	maxReads      atomic.Int32
}

func newFakeSessionTransport() *fakeSessionTransport {
	return &fakeSessionTransport{
		incoming: make(chan fakeSessionPacket, 16),
		sent:     make(chan fakeSentPacket, 16),
		closed:   make(chan struct{}),
	}
}

func (transport *fakeSessionTransport) LocalAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4500}
}
func (transport *fakeSessionTransport) RemoteAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 4500}
}
func (*fakeSessionTransport) Float(context.Context) error { return nil }
func (*fakeSessionTransport) RoundTrip(context.Context, []byte) ([]byte, error) {
	return nil, context.DeadlineExceeded
}
func (transport *fakeSessionTransport) SendESP(ctx context.Context, packet []byte) error {
	return transport.SendSessionPacket(ctx, packet, false)
}
func (transport *fakeSessionTransport) ReceiveESP(ctx context.Context, buffer []byte) (int, error) {
	n, _, err := transport.ReceiveSessionPacket(ctx, buffer)
	return n, err
}
func (transport *fakeSessionTransport) SendSessionPacket(
	ctx context.Context,
	packet []byte,
	ike bool,
) error {
	select {
	case transport.sent <- fakeSentPacket{data: append([]byte(nil), packet...), ike: ike}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-transport.closed:
		return net.ErrClosed
	}
}
func (transport *fakeSessionTransport) ReceiveSessionPacket(
	ctx context.Context,
	buffer []byte,
) (int, bool, error) {
	active := transport.readers.Add(1)
	for {
		current := transport.maxReads.Load()
		if active <= current || transport.maxReads.CompareAndSwap(current, active) {
			break
		}
	}
	defer transport.readers.Add(-1)
	if transport.ignoreContext {
		select {
		case packet := <-transport.incoming:
			if packet.err != nil {
				return 0, false, packet.err
			}
			copy(buffer, packet.data)
			return len(packet.data), packet.ike, nil
		case <-transport.closed:
			return 0, false, net.ErrClosed
		}
	}
	select {
	case packet := <-transport.incoming:
		if packet.err != nil {
			return 0, false, packet.err
		}
		copy(buffer, packet.data)
		return len(packet.data), packet.ike, nil
	case <-time.After(5 * time.Millisecond):
		return 0, false, deadlineError{}
	case <-ctx.Done():
		return 0, false, ctx.Err()
	case <-transport.closed:
		return 0, false, net.ErrClosed
	}
}

func TestSessionRelayCloseInterruptsStuckTransportRead(t *testing.T) {
	transport := newFakeSessionTransport()
	transport.ignoreContext = true
	relay := newSessionRelay(
		transport,
		legacyTestSuite(),
		ikeKeys{},
		[8]byte{1},
		[8]byte{2},
		9,
		true,
		time.Hour,
	)
	deadline := time.Now().Add(time.Second)
	for transport.readers.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if transport.readers.Load() == 0 {
		t.Fatal("relay did not enter the transport read")
	}
	done := make(chan error, 1)
	go func() { done <- relay.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("close relay: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay Close did not interrupt the transport read")
	}
}
func (transport *fakeSessionTransport) Close() error {
	transport.once.Do(func() { close(transport.closed) })
	return nil
}

func TestSessionRelayDemuxesESPAndAnswersEncryptedDPD(t *testing.T) {
	transport := newFakeSessionTransport()
	suite := legacyTestSuite()
	keys := ikeKeys{
		SKai: bytes.Repeat([]byte{0x11}, 20),
		SKar: bytes.Repeat([]byte{0x12}, 20),
		SKei: bytes.Repeat([]byte{0x13}, 16),
		SKer: bytes.Repeat([]byte{0x14}, 16),
	}
	spii := [8]byte{1}
	spir := [8]byte{2}
	relay := newSessionRelay(transport, suite, keys, spii, spir, 9, true, time.Hour)
	defer relay.Close()

	esp := []byte{0, 0, 0, 9, 0, 0, 0, 1, 0xaa}
	transport.incoming <- fakeSessionPacket{data: esp, ike: false}
	buffer := make([]byte, 64)
	n, err := relay.ReceiveESP(context.Background(), buffer)
	if err != nil {
		t.Fatalf("ReceiveESP() error = %v", err)
	}
	if !bytes.Equal(buffer[:n], esp) {
		t.Fatalf("demuxed ESP = %x, want %x", buffer[:n], esp)
	}

	dpd, err := encryptPayloads(ikeHeader{
		InitiatorSPI: spii,
		ResponderSPI: spir,
		Exchange:     exchangeInformational,
		MessageID:    8,
	}, nil, suite, keys.SKer, keys.SKar, bytes.NewReader(bytes.Repeat([]byte{0x44}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	transport.incoming <- fakeSessionPacket{data: dpd, ike: true}
	select {
	case response := <-transport.sent:
		if !response.ike {
			t.Fatal("DPD response was sent as ESP")
		}
		header, payloads, err := decryptPayloads(response.data, suite, keys.SKei, keys.SKai)
		if err != nil {
			t.Fatalf("decrypt DPD response: %v", err)
		}
		if header.Exchange != exchangeInformational || header.MessageID != 8 ||
			header.Flags != flagInitiator|flagResponse || len(payloads) != 0 {
			t.Fatalf("DPD response header/payloads = %#v %#v", header, payloads)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not answer DPD")
	}
	if maximum := transport.maxReads.Load(); maximum != 1 {
		t.Fatalf("concurrent socket readers = %d, want exactly one", maximum)
	}
}

func TestSessionRelaySendsEncryptedIKESADelete(t *testing.T) {
	transport := newFakeSessionTransport()
	suite := legacyTestSuite()
	keys := ikeKeys{
		SKai: bytes.Repeat([]byte{0x11}, 20),
		SKar: bytes.Repeat([]byte{0x12}, 20),
		SKei: bytes.Repeat([]byte{0x13}, 16),
		SKer: bytes.Repeat([]byte{0x14}, 16),
	}
	spii := [8]byte{1}
	spir := [8]byte{2}
	relay := newSessionRelay(transport, suite, keys, spii, spir, 9, true, time.Hour)
	defer relay.Close()

	if err := relay.sendIKEDelete(context.Background()); err != nil {
		t.Fatalf("sendIKEDelete() error = %v", err)
	}
	select {
	case sent := <-transport.sent:
		if !sent.ike {
			t.Fatal("IKE SA delete was sent as ESP")
		}
		header, payloads, err := decryptPayloads(sent.data, suite, keys.SKei, keys.SKai)
		if err != nil {
			t.Fatalf("decrypt IKE SA delete: %v", err)
		}
		if header.Exchange != exchangeInformational || header.MessageID != 9 || header.Flags != flagInitiator {
			t.Fatalf("IKE SA delete header = %#v", header)
		}
		item, err := onePayload(payloads, payloadDelete)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(item.Body, []byte{protocolIKE, 0, 0, 0}) {
			t.Fatalf("IKE SA delete body = %x", item.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not send IKE SA delete")
	}
}

func TestSessionRelaySendsNATKeepalive(t *testing.T) {
	transport := newFakeSessionTransport()
	relay := newSessionRelay(
		transport,
		legacyTestSuite(),
		ikeKeys{},
		[8]byte{1},
		[8]byte{2},
		9,
		true,
		10*time.Millisecond,
	)
	defer relay.Close()
	select {
	case packet := <-transport.sent:
		if packet.ike || !bytes.Equal(packet.data, []byte{0xff}) {
			t.Fatalf("keepalive = ike:%v data:%x", packet.ike, packet.data)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("relay did not send a NAT-T keepalive")
	}
}

func TestSessionRelayDropsDelayedIKEPacketFromPreviousSA(t *testing.T) {
	transport := newFakeSessionTransport()
	spii := [8]byte{1}
	spir := [8]byte{2}
	relay := newSessionRelay(
		transport,
		legacyTestSuite(),
		ikeKeys{},
		spii,
		spir,
		9,
		true,
		time.Hour,
	)
	defer relay.Close()

	transport.incoming <- fakeSessionPacket{
		ike: true,
		data: ikeHeader{
			InitiatorSPI: [8]byte{9},
			ResponderSPI: [8]byte{8},
			Exchange:     exchangeInformational,
		}.marshal(nil),
	}
	wantedESP := []byte{0, 0, 0, 9, 0, 0, 0, 1, 0xaa}
	transport.incoming <- fakeSessionPacket{data: wantedESP}

	buffer := make([]byte, 64)
	count, err := relay.ReceiveESP(context.Background(), buffer)
	if err != nil {
		t.Fatalf("ReceiveESP() after stale IKE packet = %v", err)
	}
	if !bytes.Equal(buffer[:count], wantedESP) {
		t.Fatalf("ESP after stale IKE packet = %x, want %x", buffer[:count], wantedESP)
	}
}
