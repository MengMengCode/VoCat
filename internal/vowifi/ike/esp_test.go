package ike

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"net"
	"testing"
)

type espRepeatingReader byte

func (value espRepeatingReader) Read(destination []byte) (int, error) {
	for index := range destination {
		destination[index] = byte(value)
	}
	return len(destination), nil
}

func TestESPTunnelRoundTripNegotiatedSuites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		encryption string
		integrity  string
		encKey     []byte
		authKey    []byte
	}{
		{
			name:       "AES128-SHA1",
			encryption: "aes-cbc-128",
			integrity:  "hmac-sha1-96",
			encKey:     bytes.Repeat([]byte{0x11}, 16),
			authKey:    bytes.Repeat([]byte{0x22}, 20),
		},
		{
			name:       "AES256-SHA256",
			encryption: "aes-cbc-256",
			integrity:  "hmac-sha2-256-128",
			encKey:     bytes.Repeat([]byte{0x33}, 32),
			authKey:    bytes.Repeat([]byte{0x44}, 32),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tunnel := mustTestESPTunnel(t, test.encryption, test.integrity, test.encKey, test.authKey)
			outbound := testIPv4UDPPacket(
				net.IPv4(10, 0, 0, 2),
				net.IPv4(10, 0, 0, 9),
				40123,
				50600,
				[]byte("REGISTER"),
			)
			protected, err := tunnel.seal(outbound)
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			if got := binary.BigEndian.Uint32(protected[0:4]); got != 0x11223344 {
				t.Fatalf("SPI = %#x, want %#x", got, uint32(0x11223344))
			}
			if got := binary.BigEndian.Uint32(protected[4:8]); got != 1 {
				t.Fatalf("sequence = %d, want 1", got)
			}

			// A peer opens the outbound SA with the same SPI and key material.
			peer := mustTestESPTunnel(t, test.encryption, test.integrity, test.encKey, test.authKey)
			peer.initiatorSelectors, peer.responderSelectors =
				peer.responderSelectors, peer.initiatorSelectors
			cleartext, err := peer.open(protected)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if !bytes.Equal(cleartext, outbound) {
				t.Fatalf("round trip changed the inner packet")
			}
		})
	}
}

func TestESPEncryptionMatchesRFC3602TunnelModeVector(t *testing.T) {
	t.Parallel()
	// RFC 3602 section 4, case #7. Authentication is intentionally outside
	// that RFC vector, so this assertion covers ESP SPI/sequence/IV layout,
	// tunnel-mode padding/trailer, and all 96 AES-CBC ciphertext octets.
	key := mustDecodeHex(t, "0123456789abcdef0123456789abcdef")
	iv := mustDecodeHex(t, "f4e765244f6407adf13dc1380f673f37")
	innerPacket := mustDecodeHex(t,
		"45000054090400004001f988c0a87b03c0a87bc8"+
			"08009f76a90a0100b49c083d02a20400"+
			"08090a0b0c0d0e0f1011121314151617"+
			"18191a1b1c1d1e1f2021222324252627"+
			"28292a2b2c2d2e2f3031323334353637",
	)
	expectedCiphertext := mustDecodeHex(t,
		"773b5241a4c449225e4f3ce5ed611b0c"+
			"237ca96cf74a93013c1b0ea1a0cf70f8"+
			"e4ecaec78ac53aad7a0f022b859243c6"+
			"47752e94a859352b8a4d4d2decd136e5"+
			"c177f132ad3fbfb2201ac9904c74ee0a"+
			"109e0ca1e4dfe9d5a100b842f1c22f0d",
	)
	direction, err := newESPDirection(
		0x8765,
		key,
		bytes.Repeat([]byte{0x5a}, 20),
		"aes-cbc-128",
		"hmac-sha1-96",
		bytes.NewReader(iv),
	)
	if err != nil {
		t.Fatal(err)
	}
	direction.sequence = 1
	packet, err := direction.seal(innerPacket, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := packet[:8]; !bytes.Equal(got, mustDecodeHex(t, "0000876500000002")) {
		t.Fatalf("ESP header = %x", got)
	}
	if got := packet[8:24]; !bytes.Equal(got, iv) {
		t.Fatalf("ESP IV = %x", got)
	}
	if got := packet[24 : 24+len(expectedCiphertext)]; !bytes.Equal(got, expectedCiphertext) {
		t.Fatalf("ESP ciphertext = %x, want %x", got, expectedCiphertext)
	}
}

func TestESPRejectsTamperWrongSPIAndReplay(t *testing.T) {
	t.Parallel()
	tunnel := mustTestESPTunnel(
		t,
		"aes-cbc-128",
		"hmac-sha1-96",
		bytes.Repeat([]byte{0x51}, 16),
		bytes.Repeat([]byte{0x61}, 20),
	)
	peer := mustTestESPTunnel(
		t,
		"aes-cbc-128",
		"hmac-sha1-96",
		bytes.Repeat([]byte{0x51}, 16),
		bytes.Repeat([]byte{0x61}, 20),
	)
	peer.initiatorSelectors, peer.responderSelectors =
		peer.responderSelectors, peer.initiatorSelectors
	inner := testIPv4UDPPacket(
		net.IPv4(10, 0, 0, 2),
		net.IPv4(10, 0, 0, 9),
		42000,
		50600,
		[]byte("payload"),
	)
	protected, err := tunnel.seal(inner)
	if err != nil {
		t.Fatal(err)
	}

	tampered := append([]byte(nil), protected...)
	tampered[len(tampered)-1] ^= 0x80
	if _, err := peer.open(tampered); !errors.Is(err, errESPAuthentication) {
		t.Fatalf("tampered packet error = %v, want authentication failure", err)
	}

	wrongSPI := append([]byte(nil), protected...)
	wrongSPI[0] ^= 0x01
	if _, err := peer.open(wrongSPI); err == nil {
		t.Fatal("packet with wrong SPI was accepted")
	}

	if _, err := peer.open(protected); err != nil {
		t.Fatalf("first authenticated packet: %v", err)
	}
	if _, err := peer.open(protected); !errors.Is(err, errESPReplay) {
		t.Fatalf("replayed packet error = %v, want replay rejection", err)
	}
}

func TestESPReplayWindowAcceptsAuthenticatedReordering(t *testing.T) {
	t.Parallel()
	sender := mustDefaultESPTunnel(t)
	receiver := mustDefaultESPTunnel(t)
	receiver.initiatorSelectors, receiver.responderSelectors =
		receiver.responderSelectors, receiver.initiatorSelectors
	var protected [][]byte
	for index := 0; index < 3; index++ {
		packet := testIPv4UDPPacket(
			net.IPv4(10, 0, 0, 2),
			net.IPv4(10, 0, 0, 9),
			uint16(40000+index),
			50600,
			[]byte{byte(index)},
		)
		value, err := sender.seal(packet)
		if err != nil {
			t.Fatal(err)
		}
		protected = append(protected, value)
	}
	for _, index := range []int{2, 0, 1} {
		if _, err := receiver.open(protected[index]); err != nil {
			t.Fatalf("open sequence %d: %v", index+1, err)
		}
	}
	if _, err := receiver.open(protected[0]); !errors.Is(err, errESPReplay) {
		t.Fatalf("duplicate reordered packet error = %v", err)
	}
}

func TestESPRejectsAuthenticatedInvalidPaddingWithoutConsumingSequence(t *testing.T) {
	t.Parallel()
	sender := mustDefaultESPTunnel(t)
	receiver := mustDefaultESPTunnel(t)
	receiver.initiatorSelectors, receiver.responderSelectors =
		receiver.responderSelectors, receiver.initiatorSelectors
	inner := testIPv4UDPPacket(
		net.IPv4(10, 0, 0, 2),
		net.IPv4(10, 0, 0, 9),
		40000,
		50600,
		[]byte("one"),
	)
	protected, err := sender.seal(inner)
	if err != nil {
		t.Fatal(err)
	}
	malformed := append([]byte(nil), protected...)
	rewriteESPPlaintext(t, receiver.inbound, malformed, func(plaintext []byte) {
		paddingLength := int(plaintext[len(plaintext)-2])
		if paddingLength == 0 {
			plaintext[len(plaintext)-2] = 1
			plaintext[len(plaintext)-3] = 0xff
			return
		}
		plaintext[len(plaintext)-2-paddingLength] ^= 0xff
	})
	if _, err := receiver.open(malformed); err == nil {
		t.Fatal("authenticated packet with invalid padding was accepted")
	}
	if _, err := receiver.open(protected); err != nil {
		t.Fatalf("invalid packet consumed the sequence number: %v", err)
	}
}

func TestESPTrafficSelectorsAreEnforcedInBothDirections(t *testing.T) {
	t.Parallel()
	tunnel := mustDefaultESPTunnel(t)
	disallowed := testIPv4UDPPacket(
		net.IPv4(10, 0, 0, 2),
		net.IPv4(203, 0, 113, 10),
		40000,
		50600,
		nil,
	)
	if _, err := tunnel.seal(disallowed); err == nil {
		t.Fatal("outbound packet outside responder selector was accepted")
	}

	sender := mustDefaultESPTunnel(t)
	receiver := mustDefaultESPTunnel(t)
	receiver.initiatorSelectors, receiver.responderSelectors =
		receiver.responderSelectors, receiver.initiatorSelectors
	allowed := testIPv4UDPPacket(
		net.IPv4(10, 0, 0, 2),
		net.IPv4(10, 0, 0, 9),
		40000,
		50600,
		nil,
	)
	protected, err := sender.seal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	rewriteESPPlaintext(t, receiver.inbound, protected, func(plaintext []byte) {
		copy(plaintext[16:20], net.IPv4(203, 0, 113, 10).To4())
	})
	if _, err := receiver.open(protected); err == nil {
		t.Fatal("authenticated inbound packet outside selectors was accepted")
	}
}

func TestESPSequenceExhaustionRequiresRekey(t *testing.T) {
	t.Parallel()
	tunnel := mustDefaultESPTunnel(t)
	tunnel.outbound.sequence = math.MaxUint32
	packet := testIPv4UDPPacket(
		net.IPv4(10, 0, 0, 2),
		net.IPv4(10, 0, 0, 9),
		40000,
		50600,
		nil,
	)
	if _, err := tunnel.seal(packet); err == nil {
		t.Fatal("ESP sequence wrapped instead of requiring rekey")
	}
}

func TestParseInnerIPv6ESP(t *testing.T) {
	t.Parallel()
	packet := make([]byte, 40+8)
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:6], 8)
	packet[6] = 50
	packet[7] = 64
	copy(packet[8:24], net.ParseIP("2001:db8::1").To16())
	copy(packet[24:40], net.ParseIP("2001:db8::2").To16())
	metadata, err := parseInnerPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.protocol != 50 || metadata.nextHeader != 41 {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func TestParseInnerIPv6ESPTrimsTrailingAlignmentBytes(t *testing.T) {
	t.Parallel()
	packet := make([]byte, 40+20+4)
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:6], 20)
	packet[6] = 6
	packet[7] = 64
	copy(packet[8:24], net.ParseIP("2001:db8::1").To16())
	copy(packet[24:40], net.ParseIP("2001:db8::2").To16())
	binary.BigEndian.PutUint16(packet[40:42], 49686)
	binary.BigEndian.PutUint16(packet[42:44], 5060)
	metadata, err := parseInnerPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.protocol != 6 || metadata.sourcePort != 49686 || metadata.destinationPort != 5060 {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func mustDefaultESPTunnel(t *testing.T) *espTunnel {
	t.Helper()
	return mustTestESPTunnel(
		t,
		"aes-cbc-128",
		"hmac-sha1-96",
		bytes.Repeat([]byte{0x31}, 16),
		bytes.Repeat([]byte{0x41}, 20),
	)
}

func mustTestESPTunnel(
	t *testing.T,
	encryption string,
	integrity string,
	encryptionKey []byte,
	authenticationKey []byte,
) *espTunnel {
	t.Helper()
	selector := func(ip net.IP) trafficSelector {
		return trafficSelector{
			StartPort: 0,
			EndPort:   65535,
			StartIP:   append(net.IP(nil), ip.To4()...),
			EndIP:     append(net.IP(nil), ip.To4()...),
		}
	}
	tunnel, err := newESPTunnel(ChildSAConfig{
		InboundSPI:         0x11223344,
		OutboundSPI:        0x11223344,
		Encryption:         encryption,
		Integrity:          integrity,
		InboundEncKey:      encryptionKey,
		InboundAuthKey:     authenticationKey,
		OutboundEncKey:     encryptionKey,
		OutboundAuthKey:    authenticationKey,
		InitiatorSelectors: []trafficSelector{selector(net.IPv4(10, 0, 0, 2))},
		ResponderSelectors: []trafficSelector{selector(net.IPv4(10, 0, 0, 9))},
	}, espRepeatingReader(0xa5))
	if err != nil {
		t.Fatal(err)
	}
	return tunnel
}

func testIPv4UDPPacket(
	source net.IP,
	destination net.IP,
	sourcePort uint16,
	destinationPort uint16,
	payload []byte,
) []byte {
	packet := make([]byte, 20+8+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], source.To4())
	copy(packet[16:20], destination.To4())
	binary.BigEndian.PutUint16(packet[20:22], sourcePort)
	binary.BigEndian.PutUint16(packet[22:24], destinationPort)
	binary.BigEndian.PutUint16(packet[24:26], uint16(8+len(payload)))
	copy(packet[28:], payload)
	return packet
}

func rewriteESPPlaintext(
	t *testing.T,
	direction *espDirection,
	packet []byte,
	rewrite func([]byte),
) {
	t.Helper()
	blockSize := direction.block.BlockSize()
	authenticatedLength := len(packet) - direction.icvLength
	iv := packet[espHeaderLength : espHeaderLength+blockSize]
	ciphertext := packet[espHeaderLength+blockSize : authenticatedLength]
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(direction.block, iv).CryptBlocks(plaintext, ciphertext)
	rewrite(plaintext)
	cipher.NewCBCEncrypter(direction.block, iv).CryptBlocks(ciphertext, plaintext)
	copy(packet[authenticatedLength:], direction.authenticationCode(packet[:authenticatedLength]))
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
