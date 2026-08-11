package ike

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	t           *testing.T
	allowSHA1   bool
	useMODP1024 bool
	calls       int
	suite       negotiatedSuite
	keys        ikeKeys
	spii        [8]byte
	spir        [8]byte
	nonceI      []byte
	nonceR      []byte
	floated     bool
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
	sa, err := onePayload(payloads, payloadSA)
	if err != nil {
		return nil, err
	}
	offers, err := parseProposals(sa.Body)
	if err != nil || len(offers) != 1 {
		return nil, fmt.Errorf("test: parse offered IKE proposal: %w", err)
	}
	hasSHA1PRF := containsTransform(offers[0], transformPRF, prfHMACSHA1)
	hasSHA1Integrity := containsTransform(offers[0], transformIntegrity, integrityHMACSHA1_96)
	if hasSHA1PRF != transport.allowSHA1 || hasSHA1Integrity != transport.allowSHA1 {
		transport.t.Fatalf(
			"SHA-1 offer PRF=%v integrity=%v, want both %v",
			hasSHA1PRF, hasSHA1Integrity, transport.allowSHA1,
		)
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
	wantGroup := uint16(dhMODP2048)
	wantLength := 256
	if transport.useMODP1024 {
		wantGroup = dhMODP1024
		wantLength = 128
	}
	if group != wantGroup || len(ke.Body[4:]) != wantLength {
		transport.t.Fatalf("IKE init KE = group %d length %d, want group %d length %d", group, len(ke.Body[4:]), wantGroup, wantLength)
	}
	serverDH, err := newDHExchange(group, constantReader{value: 0x77})
	if err != nil {
		return nil, err
	}
	shared, err := serverDH.shared(ke.Body[4:])
	if err != nil {
		return nil, err
	}
	transport.suite = negotiatedSuite{
		EncryptionID:   encryptionAESCBC,
		EncryptionBits: 128,
		PRFID:          prfHMACSHA256,
		IntegrityID:    integrityHMACSHA256_128,
		DHID:           wantGroup,
	}
	if transport.allowSHA1 {
		transport.suite.PRFID = prfHMACSHA1
		transport.suite.IntegrityID = integrityHMACSHA1_96
	}
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
			{Type: transformPRF, ID: transport.suite.PRFID},
			{Type: transformIntegrity, ID: transport.suite.IntegrityID},
			{Type: transformDH, ID: transport.suite.DHID},
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
	capture := &firstAuthCaptureTransport{t: t, allowSHA1: true, useMODP1024: true}
	provider, err := NewProvider(Config{
		Random:      constantReader{value: 0x42},
		Timeout:     time.Second,
		Installer:   unusedInstaller{},
		APN:         "ims",
		AllowSHA1:   true,
		UseMODP1024: true,
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

func TestProviderVodafoneDefaultsToStrongCryptoDespitePLMN(t *testing.T) {
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
	_, err = provider.Start(context.Background(), vowifi.TunnelRequest{
		DeviceID: "ec20-1",
		Identity: vowifi.SIMIdentity{
			ICCID:   "8944100000000000000",
			IMSI:    "234150123456789",
			HomeMCC: "234",
			HomeMNC: "15",
		},
		EPDG: "epdg.epc.mnc015.mcc234.pub.3gppnetwork.org",
		AKA:  &testAKAProvider{},
	})
	if !errors.Is(err, errFirstAuthObserved) {
		t.Fatalf("Start() error = %v, want capture sentinel", err)
	}
}

func TestProviderTelefonicaGermanyAutomaticallyOffersSHA1WithMODP2048(t *testing.T) {
	capture := &firstAuthCaptureTransport{t: t, allowSHA1: true}
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
	_, err = provider.Start(context.Background(), vowifi.TunnelRequest{
		DeviceID: "openstick-1",
		Identity: vowifi.SIMIdentity{
			ICCID:   "8949000000000000000",
			IMSI:    "262070123456789",
			HomeMCC: "262",
			HomeMNC: "07",
		},
		EPDG: "epdg.epc.mnc007.mcc262.pub.3gppnetwork.org",
		AKA:  &testAKAProvider{},
	})
	if !errors.Is(err, errFirstAuthObserved) {
		t.Fatalf("Start() error = %v, want capture sentinel", err)
	}
	if capture.useMODP1024 {
		t.Fatal("Telefonica compatibility unexpectedly enabled MODP1024")
	}
}

func TestTelefonicaGermanySHA1CompatibilityIsCarrierScoped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		identity vowifi.SIMIdentity
		want     bool
	}{
		{name: "O2 two-digit MNC", identity: vowifi.SIMIdentity{HomeMCC: "262", HomeMNC: "07"}, want: true},
		{name: "O2 three-digit MNC", identity: vowifi.SIMIdentity{HomeMCC: "262", HomeMNC: "003"}, want: true},
		{name: "German non-Telefonica", identity: vowifi.SIMIdentity{HomeMCC: "262", HomeMNC: "02"}, want: false},
		{name: "same MNC abroad", identity: vowifi.SIMIdentity{HomeMCC: "234", HomeMNC: "07"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := telefonicaGermanySHA1Compatibility(test.identity); got != test.want {
				t.Fatalf("compatibility = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProviderWeakCryptoRequiresExplicitOptIn(t *testing.T) {
	strongIKE := ikeOffer(dhMODP2048, false)
	strongESP := espOffer([]byte{1, 2, 3, 4}, false)
	for _, candidate := range append(append([]transform(nil), strongIKE.Transforms...), strongESP.Transforms...) {
		if candidate.Type == transformPRF && candidate.ID == prfHMACSHA1 ||
			candidate.Type == transformIntegrity && candidate.ID == integrityHMACSHA1_96 ||
			candidate.Type == transformDH && candidate.ID == dhMODP1024 {
			t.Fatalf("strong default advertised legacy transform: %#v", candidate)
		}
	}
	if got := transformIDs(strongIKE, transformPRF); !equalUint16s(got, []uint16{prfHMACSHA256}) {
		t.Fatalf("strong IKE PRFs = %v", got)
	}
	if got := transformIDs(strongIKE, transformIntegrity); !equalUint16s(got, []uint16{integrityHMACSHA256_128}) {
		t.Fatalf("strong IKE integrity = %v", got)
	}
	legacyIKE := ikeOffer(dhMODP1024, true)
	legacyESP := espOffer([]byte{1, 2, 3, 4}, true)
	if got := transformIDs(legacyIKE, transformPRF); !equalUint16s(got, []uint16{prfHMACSHA256, prfHMACSHA1}) {
		t.Fatalf("legacy IKE PRFs = %v", got)
	}
	if got := transformIDs(legacyIKE, transformIntegrity); !equalUint16s(got, []uint16{integrityHMACSHA256_128, integrityHMACSHA1_96}) {
		t.Fatalf("legacy IKE integrity = %v", got)
	}
	if got := transformIDs(legacyESP, transformIntegrity); !equalUint16s(got, []uint16{integrityHMACSHA256_128, integrityHMACSHA1_96}) {
		t.Fatalf("legacy ESP integrity = %v", got)
	}
	sha1WithGroup14 := ikeOffer(dhMODP2048, true)
	if got := transformIDs(sha1WithGroup14, transformDH); !equalUint16s(got, []uint16{dhMODP2048}) {
		t.Fatalf("SHA-1-compatible group14 offer DH = %v", got)
	}
	group2WithSHA256 := ikeOffer(dhMODP1024, false)
	if got := transformIDs(group2WithSHA256, transformPRF); !equalUint16s(got, []uint16{prfHMACSHA256}) {
		t.Fatalf("group2 strong PRFs = %v", got)
	}
	if got := transformIDs(group2WithSHA256, transformIntegrity); !equalUint16s(got, []uint16{integrityHMACSHA256_128}) {
		t.Fatalf("group2 strong integrity = %v", got)
	}
}

func transformIDs(item proposal, kind uint8) []uint16 {
	var result []uint16
	for _, candidate := range item.Transforms {
		if candidate.Type == kind {
			result = append(result, candidate.ID)
		}
	}
	return result
}

func containsTransform(item proposal, kind uint8, id uint16) bool {
	for _, candidate := range item.Transforms {
		if candidate.Type == kind && candidate.ID == id {
			return true
		}
	}
	return false
}

func equalUint16s(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestProviderEAPMethodConfiguration(t *testing.T) {
	defaultProvider, err := NewProvider(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if defaultProvider.config.EAPMethod != "aka" {
		t.Fatalf("default EAP method = %q", defaultProvider.config.EAPMethod)
	}
	primeProvider, err := NewProvider(Config{EAPMethod: "AKA-PRIME"})
	if err != nil {
		t.Fatalf("NewProvider(AKA-PRIME) error = %v", err)
	}
	if primeProvider.config.EAPMethod != "aka-prime" {
		t.Fatalf("normalized EAP method = %q", primeProvider.config.EAPMethod)
	}
	if _, err := NewProvider(Config{EAPMethod: "auto"}); err == nil {
		t.Fatal("NewProvider() accepted ambiguous automatic AKA downgrade")
	}
}

var _ io.Reader = constantReader{}
var _ datagramTransport = (*firstAuthCaptureTransport)(nil)
