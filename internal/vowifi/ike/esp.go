package ike

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"
)

const (
	espHeaderLength            = 8
	espReplayWindow            = 64
	espFragmentCacheTTL        = 30 * time.Second
	espFragmentCacheMaxEntries = 256
)

var (
	errESPAuthentication = errors.New("ike: ESP authentication failed")
	errESPReplay         = errors.New("ike: ESP packet is outside the replay window")
	errESPPolicyDrop     = errors.New("ike: ESP packet is not eligible for this CHILD_SA")
)

// espTunnel protects complete IPv4 or IPv6 packets using an IKEv2 CHILD_SA.
// It deliberately implements only the negotiated suites offered by this
// package: AES-CBC with HMAC-SHA1-96 or HMAC-SHA2-256-128 and no ESN.
type espTunnel struct {
	outbound *espDirection
	inbound  *espDirection

	initiatorSelectors []trafficSelector
	responderSelectors []trafficSelector

	outboundFragments fragmentPortCache
	inboundFragments  fragmentPortCache
}

type espDirection struct {
	spi       uint32
	block     cipher.Block
	authKey   []byte
	integrity string
	icvLength int
	random    io.Reader

	mu       sync.Mutex
	sequence uint32
	replay   replayWindow
}

type replayWindow struct {
	highest uint32
	bitmap  uint64
}

type innerPacketMetadata struct {
	source          net.IP
	destination     net.IP
	protocol        uint8
	sourcePort      uint16
	destinationPort uint16
	nextHeader      uint8
	fragmented      bool
	firstFragment   bool
	fragmentID      uint32
}

func newESPTunnel(config ChildSAConfig, randomSource io.Reader) (*espTunnel, error) {
	if config.InboundSPI == 0 || config.OutboundSPI == 0 {
		return nil, errors.New("ike: ESP SPIs must be nonzero")
	}
	if randomSource == nil {
		randomSource = rand.Reader
	}
	outbound, err := newESPDirection(
		config.OutboundSPI,
		config.OutboundEncKey,
		config.OutboundAuthKey,
		config.Encryption,
		config.Integrity,
		randomSource,
	)
	if err != nil {
		return nil, fmt.Errorf("ike: outbound ESP: %w", err)
	}
	inbound, err := newESPDirection(
		config.InboundSPI,
		config.InboundEncKey,
		config.InboundAuthKey,
		config.Encryption,
		config.Integrity,
		randomSource,
	)
	if err != nil {
		return nil, fmt.Errorf("ike: inbound ESP: %w", err)
	}
	if len(config.InitiatorSelectors) == 0 || len(config.ResponderSelectors) == 0 {
		return nil, errors.New("ike: ESP traffic selectors are required")
	}
	return &espTunnel{
		outbound:           outbound,
		inbound:            inbound,
		initiatorSelectors: copyESPTrafficSelectors(config.InitiatorSelectors),
		responderSelectors: copyESPTrafficSelectors(config.ResponderSelectors),
	}, nil
}

func newESPDirection(
	spi uint32,
	encryptionKey []byte,
	authenticationKey []byte,
	encryption string,
	integrity string,
	randomSource io.Reader,
) (*espDirection, error) {
	expectedEncryptionLength := 0
	switch encryption {
	case "aes-cbc-128":
		expectedEncryptionLength = 16
	case "aes-cbc-256":
		expectedEncryptionLength = 32
	default:
		return nil, fmt.Errorf("unsupported encryption suite %q", encryption)
	}
	if len(encryptionKey) != expectedEncryptionLength {
		return nil, fmt.Errorf("AES key has length %d, want %d", len(encryptionKey), expectedEncryptionLength)
	}
	expectedAuthenticationLength := 0
	icvLength := 0
	switch integrity {
	case "hmac-sha1-96":
		expectedAuthenticationLength = sha1.Size
		icvLength = 12
	case "hmac-sha2-256-128":
		expectedAuthenticationLength = sha256.Size
		icvLength = 16
	default:
		return nil, fmt.Errorf("unsupported integrity suite %q", integrity)
	}
	if len(authenticationKey) != expectedAuthenticationLength {
		return nil, fmt.Errorf(
			"authentication key has length %d, want %d",
			len(authenticationKey),
			expectedAuthenticationLength,
		)
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	return &espDirection{
		spi:       spi,
		block:     block,
		authKey:   append([]byte(nil), authenticationKey...),
		integrity: integrity,
		icvLength: icvLength,
		random:    randomSource,
	}, nil
}

func (tunnel *espTunnel) seal(innerPacket []byte) ([]byte, error) {
	if tunnel == nil {
		return nil, errors.New("ike: nil ESP tunnel")
	}
	metadata, err := parseInnerPacket(innerPacket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errESPPolicyDrop, err)
	}
	if !tunnel.packetAllowedWithFragments(
		metadata,
		tunnel.initiatorSelectors,
		tunnel.responderSelectors,
		&tunnel.outboundFragments,
	) {
		return nil, fmt.Errorf("%w: outbound packet is outside negotiated traffic selectors", errESPPolicyDrop)
	}
	return tunnel.outbound.seal(innerPacket, metadata.nextHeader)
}

func (tunnel *espTunnel) open(packet []byte) ([]byte, error) {
	if tunnel == nil {
		return nil, errors.New("ike: nil ESP tunnel")
	}
	return tunnel.inbound.open(packet, func(innerPacket []byte, nextHeader uint8) error {
		metadata, err := parseInnerPacket(innerPacket)
		if err != nil {
			return err
		}
		if metadata.nextHeader != nextHeader {
			return errors.New("ike: ESP trailer does not match the inner IP version")
		}
		if !tunnel.packetAllowedWithFragments(
			metadata,
			tunnel.responderSelectors,
			tunnel.initiatorSelectors,
			&tunnel.inboundFragments,
		) {
			return errors.New("ike: inbound packet is outside negotiated traffic selectors")
		}
		return nil
	})
}

func (direction *espDirection) seal(innerPacket []byte, nextHeader uint8) ([]byte, error) {
	direction.mu.Lock()
	defer direction.mu.Unlock()

	if direction.sequence == math.MaxUint32 {
		return nil, errors.New("ike: ESP sequence number exhausted; rekey is required")
	}
	direction.sequence++
	sequence := direction.sequence

	blockSize := direction.block.BlockSize()
	paddingLength := (blockSize - ((len(innerPacket) + 2) % blockSize)) % blockSize
	plaintext := make([]byte, len(innerPacket)+paddingLength+2)
	copy(plaintext, innerPacket)
	for index := 0; index < paddingLength; index++ {
		plaintext[len(innerPacket)+index] = byte(index + 1)
	}
	plaintext[len(plaintext)-2] = byte(paddingLength)
	plaintext[len(plaintext)-1] = nextHeader

	authenticatedLength := espHeaderLength + blockSize + len(plaintext)
	packet := make([]byte, authenticatedLength+direction.icvLength)
	binary.BigEndian.PutUint32(packet[0:4], direction.spi)
	binary.BigEndian.PutUint32(packet[4:8], sequence)
	iv := packet[espHeaderLength : espHeaderLength+blockSize]
	if _, err := io.ReadFull(direction.random, iv); err != nil {
		return nil, fmt.Errorf("ike: generate ESP IV: %w", err)
	}
	cipher.NewCBCEncrypter(direction.block, iv).CryptBlocks(
		packet[espHeaderLength+blockSize:authenticatedLength],
		plaintext,
	)
	icv := direction.authenticationCode(packet[:authenticatedLength])
	copy(packet[authenticatedLength:], icv)
	return packet, nil
}

func (direction *espDirection) open(
	packet []byte,
	validate func([]byte, uint8) error,
) ([]byte, error) {
	direction.mu.Lock()
	defer direction.mu.Unlock()

	blockSize := direction.block.BlockSize()
	minimumLength := espHeaderLength + blockSize + blockSize + direction.icvLength
	if len(packet) < minimumLength {
		return nil, errors.New("ike: ESP packet is truncated")
	}
	if binary.BigEndian.Uint32(packet[0:4]) != direction.spi {
		return nil, errors.New("ike: ESP packet has an unexpected SPI")
	}
	sequence := binary.BigEndian.Uint32(packet[4:8])
	if sequence == 0 || !direction.replay.wouldAccept(sequence) {
		return nil, errESPReplay
	}
	authenticatedLength := len(packet) - direction.icvLength
	ciphertext := packet[espHeaderLength+blockSize : authenticatedLength]
	if len(ciphertext) == 0 || len(ciphertext)%blockSize != 0 {
		return nil, errors.New("ike: ESP ciphertext is not block aligned")
	}
	expectedICV := direction.authenticationCode(packet[:authenticatedLength])
	if subtle.ConstantTimeCompare(expectedICV, packet[authenticatedLength:]) != 1 {
		return nil, errESPAuthentication
	}

	plaintext := make([]byte, len(ciphertext))
	iv := packet[espHeaderLength : espHeaderLength+blockSize]
	cipher.NewCBCDecrypter(direction.block, iv).CryptBlocks(plaintext, ciphertext)
	if len(plaintext) < 2 {
		return nil, errors.New("ike: ESP plaintext is truncated")
	}
	paddingLength := int(plaintext[len(plaintext)-2])
	if paddingLength > len(plaintext)-2 {
		return nil, errors.New("ike: ESP padding length is invalid")
	}
	paddingStart := len(plaintext) - 2 - paddingLength
	for index := 0; index < paddingLength; index++ {
		if plaintext[paddingStart+index] != byte(index+1) {
			return nil, errors.New("ike: ESP padding bytes are invalid")
		}
	}
	nextHeader := plaintext[len(plaintext)-1]
	if nextHeader != 4 && nextHeader != 41 {
		return nil, fmt.Errorf("ike: unsupported ESP next-header value %d", nextHeader)
	}
	innerPacket := append([]byte(nil), plaintext[:paddingStart]...)
	if validate != nil {
		if err := validate(innerPacket, nextHeader); err != nil {
			return nil, err
		}
	}
	direction.replay.commit(sequence)
	return innerPacket, nil
}

func (direction *espDirection) authenticationCode(packet []byte) []byte {
	var mac hashWriter
	switch direction.integrity {
	case "hmac-sha1-96":
		mac = hmac.New(sha1.New, direction.authKey)
	case "hmac-sha2-256-128":
		mac = hmac.New(sha256.New, direction.authKey)
	default:
		panic("unreachable ESP integrity suite")
	}
	_, _ = mac.Write(packet)
	return mac.Sum(nil)[:direction.icvLength]
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func (window replayWindow) wouldAccept(sequence uint32) bool {
	if sequence == 0 {
		return false
	}
	if window.highest == 0 || sequence > window.highest {
		return true
	}
	difference := window.highest - sequence
	if difference >= espReplayWindow {
		return false
	}
	return window.bitmap&(uint64(1)<<difference) == 0
}

func (window *replayWindow) commit(sequence uint32) {
	if window.highest == 0 {
		window.highest = sequence
		window.bitmap = 1
		return
	}
	if sequence > window.highest {
		difference := sequence - window.highest
		if difference >= espReplayWindow {
			window.bitmap = 1
		} else {
			window.bitmap = window.bitmap<<difference | 1
		}
		window.highest = sequence
		return
	}
	window.bitmap |= uint64(1) << (window.highest - sequence)
}

func parseInnerPacket(packet []byte) (innerPacketMetadata, error) {
	if len(packet) == 0 {
		return innerPacketMetadata{}, errors.New("ike: inner IP packet is empty")
	}
	switch packet[0] >> 4 {
	case 4:
		return parseInnerIPv4(packet)
	case 6:
		return parseInnerIPv6(packet)
	default:
		return innerPacketMetadata{}, errors.New("ike: inner packet is not IPv4 or IPv6")
	}
}

func parseInnerIPv4(packet []byte) (innerPacketMetadata, error) {
	if len(packet) < 20 {
		return innerPacketMetadata{}, errors.New("ike: inner IPv4 packet is truncated")
	}
	headerLength := int(packet[0]&0x0f) * 4
	if headerLength < 20 || headerLength > len(packet) {
		return innerPacketMetadata{}, errors.New("ike: inner IPv4 header length is invalid")
	}
	totalLength := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLength != len(packet) || totalLength < headerLength {
		return innerPacketMetadata{}, errors.New("ike: inner IPv4 total length is invalid")
	}
	metadata := innerPacketMetadata{
		source:      append(net.IP(nil), packet[12:16]...),
		destination: append(net.IP(nil), packet[16:20]...),
		protocol:    packet[9],
		nextHeader:  4,
	}
	flagsAndOffset := binary.BigEndian.Uint16(packet[6:8])
	fragmentOffset := flagsAndOffset & 0x1fff
	moreFragments := flagsAndOffset&0x2000 != 0
	if fragmentOffset != 0 || moreFragments {
		metadata.fragmented = true
		metadata.firstFragment = fragmentOffset == 0
		metadata.fragmentID = uint32(binary.BigEndian.Uint16(packet[4:6]))
	}
	if fragmentOffset == 0 {
		parseTransportPorts(packet[headerLength:], &metadata)
	}
	return metadata, nil
}

func parseInnerIPv6(packet []byte) (innerPacketMetadata, error) {
	if len(packet) < 40 {
		return innerPacketMetadata{}, errors.New("ike: inner IPv6 packet is truncated")
	}
	payloadLength := int(binary.BigEndian.Uint16(packet[4:6]))
	declaredLength := payloadLength + 40
	if declaredLength > len(packet) {
		return innerPacketMetadata{}, errors.New("ike: inner IPv6 payload length is invalid")
	}
	packet = packet[:declaredLength]
	metadata := innerPacketMetadata{
		source:      append(net.IP(nil), packet[8:24]...),
		destination: append(net.IP(nil), packet[24:40]...),
		nextHeader:  41,
	}
	protocol := packet[6]
	offset := 40
	firstFragment := true
	seenFragmentHeader := false
	for {
		switch protocol {
		case 0, 43, 60:
			if offset+2 > len(packet) {
				return innerPacketMetadata{}, errors.New("ike: inner IPv6 extension header is truncated")
			}
			length := (int(packet[offset+1]) + 1) * 8
			if length < 8 || offset+length > len(packet) {
				return innerPacketMetadata{}, errors.New("ike: inner IPv6 extension header length is invalid")
			}
			protocol = packet[offset]
			offset += length
		case 44:
			if seenFragmentHeader || offset+8 > len(packet) {
				return innerPacketMetadata{}, errors.New("ike: inner IPv6 fragment header is invalid")
			}
			seenFragmentHeader = true
			flagsAndOffset := binary.BigEndian.Uint16(packet[offset+2 : offset+4])
			fragOffset := flagsAndOffset & 0xfff8
			moreFragments := flagsAndOffset&0x0001 != 0
			if fragOffset != 0 || moreFragments {
				metadata.fragmented = true
				metadata.firstFragment = fragOffset == 0
				metadata.fragmentID = binary.BigEndian.Uint32(packet[offset+4 : offset+8])
			}
			if fragOffset != 0 {
				metadata.protocol = packet[offset]
				return metadata, nil
			}
			firstFragment = true
			protocol = packet[offset]
			offset += 8
		case 51:
			if offset+2 > len(packet) {
				return innerPacketMetadata{}, errors.New("ike: inner IPv6 AH header is truncated")
			}
			length := (int(packet[offset+1]) + 2) * 4
			if length < 8 || offset+length > len(packet) {
				return innerPacketMetadata{}, errors.New("ike: inner IPv6 AH header length is invalid")
			}
			protocol = packet[offset]
			offset += length
		default:
			metadata.protocol = protocol
			if firstFragment {
				parseTransportPorts(packet[offset:], &metadata)
			}
			return metadata, nil
		}
	}
}

func parseTransportPorts(payload []byte, metadata *innerPacketMetadata) {
	if metadata == nil || (metadata.protocol != 6 && metadata.protocol != 17) || len(payload) < 4 {
		return
	}
	metadata.sourcePort = binary.BigEndian.Uint16(payload[0:2])
	metadata.destinationPort = binary.BigEndian.Uint16(payload[2:4])
}

type fragmentKey struct {
	nextHeader  uint8
	protocol    uint8
	source      [16]byte
	destination [16]byte
	id          uint32
}

type fragmentPortEntry struct {
	sourcePort      uint16
	destinationPort uint16
	expiresAt       time.Time
}

type fragmentPortCache struct {
	mu      sync.Mutex
	entries map[fragmentKey]fragmentPortEntry
}

func makeFragmentKey(metadata innerPacketMetadata) fragmentKey {
	key := fragmentKey{
		nextHeader: metadata.nextHeader,
		protocol:   metadata.protocol,
		id:         metadata.fragmentID,
	}
	if source := metadata.source.To16(); source != nil {
		copy(key.source[:], source)
	}
	if destination := metadata.destination.To16(); destination != nil {
		copy(key.destination[:], destination)
	}
	return key
}

func (cache *fragmentPortCache) record(key fragmentKey, sourcePort uint16, destinationPort uint16, now time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[fragmentKey]fragmentPortEntry)
	}
	for existingKey, entry := range cache.entries {
		if !now.Before(entry.expiresAt) {
			delete(cache.entries, existingKey)
		}
	}
	if _, exists := cache.entries[key]; !exists && len(cache.entries) >= espFragmentCacheMaxEntries {
		var oldestKey fragmentKey
		var oldestExpiry time.Time
		first := true
		for existingKey, entry := range cache.entries {
			if first || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = existingKey
				oldestExpiry = entry.expiresAt
				first = false
			}
		}
		if !first {
			delete(cache.entries, oldestKey)
		}
	}
	cache.entries[key] = fragmentPortEntry{
		sourcePort:      sourcePort,
		destinationPort: destinationPort,
		expiresAt:       now.Add(espFragmentCacheTTL),
	}
}

func (cache *fragmentPortCache) lookup(key fragmentKey, now time.Time) (uint16, uint16, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok {
		return 0, 0, false
	}
	if !now.Before(entry.expiresAt) {
		delete(cache.entries, key)
		return 0, 0, false
	}
	return entry.sourcePort, entry.destinationPort, true
}

func (cache *fragmentPortCache) delete(key fragmentKey) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.entries, key)
}

func (tunnel *espTunnel) packetAllowedWithFragments(
	metadata innerPacketMetadata,
	sourceSelectors []trafficSelector,
	destinationSelectors []trafficSelector,
	fragments *fragmentPortCache,
) bool {
	if !metadata.fragmented {
		return packetAllowed(metadata, sourceSelectors, destinationSelectors)
	}
	key := makeFragmentKey(metadata)
	now := time.Now()
	if metadata.firstFragment {
		if packetAllowed(metadata, sourceSelectors, destinationSelectors) {
			fragments.record(key, metadata.sourcePort, metadata.destinationPort, now)
			return true
		}
		fragments.delete(key)
		return false
	}
	if sourcePort, destinationPort, ok := fragments.lookup(key, now); ok {
		metadata.sourcePort = sourcePort
		metadata.destinationPort = destinationPort
	}
	return packetAllowed(metadata, sourceSelectors, destinationSelectors)
}

func packetAllowed(
	metadata innerPacketMetadata,
	sourceSelectors []trafficSelector,
	destinationSelectors []trafficSelector,
) bool {
	return endpointAllowed(
		metadata.source,
		metadata.protocol,
		metadata.sourcePort,
		sourceSelectors,
	) && endpointAllowed(
		metadata.destination,
		metadata.protocol,
		metadata.destinationPort,
		destinationSelectors,
	)
}

func endpointAllowed(ip net.IP, protocol uint8, port uint16, selectors []trafficSelector) bool {
	for _, selector := range selectors {
		if selector.IPProtocol != 0 && selector.IPProtocol != protocol {
			continue
		}
		if port < selector.StartPort || port > selector.EndPort {
			continue
		}
		if ipWithinRange(ip, selector.StartIP, selector.EndIP) {
			return true
		}
	}
	return false
}

func ipWithinRange(ip net.IP, start net.IP, end net.IP) bool {
	normalizedIP, normalizedStart, normalizedEnd, ok := normalizeIPRange(ip, start, end)
	if !ok {
		return false
	}
	return bytes.Compare(normalizedIP, normalizedStart) >= 0 &&
		bytes.Compare(normalizedIP, normalizedEnd) <= 0
}

func normalizeIPRange(ip net.IP, start net.IP, end net.IP) ([]byte, []byte, []byte, bool) {
	if start4 := start.To4(); start4 != nil {
		ip4 := ip.To4()
		end4 := end.To4()
		if ip4 == nil || end4 == nil {
			return nil, nil, nil, false
		}
		return ip4, start4, end4, true
	}
	ip16 := ip.To16()
	start16 := start.To16()
	end16 := end.To16()
	if ip16 == nil || start16 == nil || end16 == nil || start.To4() != nil || end.To4() != nil {
		return nil, nil, nil, false
	}
	return ip16, start16, end16, true
}

func copyESPTrafficSelectors(selectors []trafficSelector) []trafficSelector {
	cloned := make([]trafficSelector, len(selectors))
	for index, selector := range selectors {
		cloned[index] = selector
		cloned[index].StartIP = append(net.IP(nil), selector.StartIP...)
		cloned[index].EndIP = append(net.IP(nil), selector.EndIP...)
	}
	return cloned
}
