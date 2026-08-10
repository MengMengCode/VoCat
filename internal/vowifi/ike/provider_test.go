package ike

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

var errFirstAuthObserved = errors.New("test: first IKE_AUTH observed")

type constantReader struct{ value byte }

func (reader constantReader) Read(destination []byte) (int, error) {
	for index := range destination {
		destination[index] = reader.value
	}
	return len(destination), nil
}

type firstAuthCaptureTransport struct {
	t       *testing.T
	calls   int
	suite   negotiatedSuite
	keys    ikeKeys
	spii    [8]byte
	spir    [8]byte
	nonceI  []byte
	nonceR  []byte
	floated bool
}

func (transport *firstAuthCaptureTransport) LocalAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 500}
}

func (transport *firstAuthCaptureTransport) RemoteAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(198, 51, 100, 20), Port: 500}
}

func (transport *firstAuthCaptureTransport) Float(context.Context) error {
	transport.floated = true
	return nil
}

func (transport *firstAuthCaptureTransport) RoundTrip(_ context.Context, packet []byte) ([]byte, error) {
	transport.calls++
	switch transport.calls {
	case 1:
		return transport.answerIKEInit(packet)
	case 2:
		return nil, transport.observeFirstAuth(packet)
	default:
		return nil, errors.New("test: unexpected exchange")
	}
}

func (transport *firstAuthCaptureTransport) answerIKEInit(packet []byte) ([]byte, error) {
	header, body, err := parseIKEPacket(packet)
	if err != nil {
		return nil, err
	}
	payloads, err := parsePayloadChain(header.NextPayload, body)
	if err != nil {
		return nil, err
	}
	ke, err := onePayload(payloads, payloadKE)
	if err != nil {
		return nil, err
	}
	nonce, err := onePayload(payloads, payloadNonce)
	if err != nil {
		return nil, err
	}
	group := uint16(ke.Body[0])<<8 | uint16(ke.Body[1])
	if group != dhMODP1024 || len(ke.Body[4:]) != 128 {
		transport.t.Fatalf("Vodafone init KE = group %d length %d", group, len(ke.Body[4:]))
	}
	serverDH, err := newDHExchange(group, constantReader{value: 0x77})
	if err != nil {
		return nil, err
	}
	shared, err := serverDH.shared(ke.Body[4:])
	if err != nil {
		return nil, err
	}
	transport.suite = legacyTestSuite()
	transport.spii = header.InitiatorSPI
	transport.spir = [8]byte{0x80, 1, 2, 3, 4, 5, 6, 7}
	transport.nonceI = append([]byte(nil), nonce.Body...)
	transport.nonceR = bytes.Repeat([]byte{0x88}, 32)
	transport.keys, err = deriveIKEKeys(
		transport.suite,
		shared,
		transport.nonceI,
		transport.nonceR,
		transport.spii,
		transport.spir,
	)
	if err != nil {
		return nil, err
	}
	selectedSA, _ := marshalProposals([]proposal{{
		Number:   1,
		Protocol: protocolIKE,
		Transforms: []transform{
			{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 128},
			{Type: transformPRF, ID: prfHMACSHA1},
			{Type: transformIntegrity, ID: integrityHMACSHA1_96},
			{Type: transformDH, ID: dhMODP1024},
		},
	}})
	keBody := make([]byte, 4+len(serverDH.Public))
	keBody[1] = byte(group)
	copy(keBody[4:], serverDH.Public)
	first, responseBody, _ := marshalPayloadChain([]payload{
		{Type: payloadSA, Body: selectedSA},
		{Type: payloadKE, Body: keBody},
		{Type: payloadNonce, Body: transport.nonceR},
	})
	return ikeHeader{
		InitiatorSPI: transport.spii,
		ResponderSPI: transport.spir,
		NextPayload:  first,
		Exchange:     exchangeIKEInit,
		Flags:        flagResponse,
	}.marshal(responseBody), nil
}

func (transport *firstAuthCaptureTransport) observeFirstAuth(packet []byte) error {
	header, payloads, err := decryptPayloads(
		packet,
		transport.suite,
		transport.keys.SKei,
		transport.keys.SKai,
	)
	if err != nil {
		return err
	}
	if header.Exchange != exchangeIKEAuth || header.MessageID != 1 || header.Flags != flagInitiator {
		transport.t.Fatalf("first IKE_AUTH header = %#v", header)
	}
	idr, err := onePayload(payloads, payloadIDr)
	if err != nil {
		transport.t.Fatal(err)
	}
	if idr.Body[0] != 2 || string(idr.Body[4:]) != "ims" {
		transport.t.Fatalf("requested IDr = type %d value %q", idr.Body[0], idr.Body[4:])
	}
	foundEAPOnly := false
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, data, err := parseNotify(item)
		if err != nil {
			transport.t.Fatal(err)
		}
		if kind == notifyEAPOnlyAuth && len(data) == 0 {
			foundEAPOnly = true
		}
	}
	if !foundEAPOnly {
		transport.t.Fatal("first IKE_AUTH omitted EAP_ONLY_AUTHENTICATION")
	}
	for _, kind := range []uint8{payloadIDi, payloadSA, payloadTSi, payloadTSr, payloadCP} {
		if _, err := onePayload(payloads, kind); err != nil {
			transport.t.Fatalf("first IKE_AUTH payload %d: %v", kind, err)
		}
	}
	return errFirstAuthObserved
}

func (*firstAuthCaptureTransport) SendESP(context.Context, []byte) error {
	return errors.New("test: unused")
}
func (*firstAuthCaptureTransport) ReceiveESP(context.Context, []byte) (int, error) {
	return 0, errors.New("test: unused")
}
func (*firstAuthCaptureTransport) SendSessionPacket(context.Context, []byte, bool) error {
	return errors.New("test: unused")
}
func (*firstAuthCaptureTransport) ReceiveSessionPacket(context.Context, []byte) (int, bool, error) {
	return 0, false, errors.New("test: unused")
}
func (*firstAuthCaptureTransport) Close() error { return nil }

type unusedInstaller struct{}

func (unusedInstaller) Install(context.Context, ChildSAConfig) (ChildSAHandle, error) {
	return nil, errors.New("test: installer must not run")
}

type relayOrderedChild struct {
	relayDone <-chan struct{}
}

func (child relayOrderedChild) Close(ctx context.Context) error {
	select {
	case <-child.relayDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSessionCloseStopsRelayBeforeChildDataplane(t *testing.T) {
	t.Parallel()
	transport := newFakeSessionTransport()
	relay := newSessionRelay(
		transport,
		legacyTestSuite(),
		ikeKeys{},
		[8]byte{1},
		[8]byte{2},
		true,
		time.Hour,
	)
	session := &Session{
		child:     relayOrderedChild{relayDone: relay.done},
		relay:     relay,
		transport: transport,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := session.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestProviderVodafoneFirstAuthIsEAPOnlyAndRequestsIMSAPN(t *testing.T) {
	capture := &firstAuthCaptureTransport{t: t}
	provider, err := NewProvider(Config{
		Random:    constantReader{value: 0x42},
		Timeout:   time.Second,
		Installer: unusedInstaller{},
		APN:       "ims",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.transportFactory = func(
		context.Context,
		transportConfig,
		vowifi.ProxyRoute,
		string,
	) (datagramTransport, error) {
		return capture, nil
	}
	aka := &testAKAProvider{}
	_, err = provider.Start(context.Background(), vowifi.TunnelRequest{
		DeviceID: "ec20-1",
		Identity: vowifi.SIMIdentity{
			ICCID:   "8944100000000000000",
			IMSI:    "234150123456789",
			HomeMCC: "234",
			HomeMNC: "15",
		},
		EPDG: "epdg.epc.mnc015.mcc234.pub.3gppnetwork.org",
		AKA:  aka,
	})
	if !errors.Is(err, errFirstAuthObserved) {
		t.Fatalf("Start() error = %v, want capture sentinel", err)
	}
	if capture.calls != 2 || capture.floated || aka.calls != 0 {
		t.Fatalf("capture calls=%d floated=%v AKA calls=%d", capture.calls, capture.floated, aka.calls)
	}
}

var _ io.Reader = constantReader{}
var _ datagramTransport = (*firstAuthCaptureTransport)(nil)
