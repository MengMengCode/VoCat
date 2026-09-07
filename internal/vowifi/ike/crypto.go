package ike

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net"
)

type ikeKeys struct {
	SKd  []byte
	SKai []byte
	SKar []byte
	SKei []byte
	SKer []byte
	SKpi []byte
	SKpr []byte
}

func (suite negotiatedSuite) prf() (func() hash.Hash, int, error) {
	switch suite.PRFID {
	case prfHMACSHA1:
		return sha1.New, sha1.Size, nil
	case prfHMACSHA256:
		return sha256.New, sha256.Size, nil
	default:
		return nil, 0, fmt.Errorf("%w: PRF id %d", errUnsupportedSuite, suite.PRFID)
	}
}

func (suite negotiatedSuite) encryptionKeyLength() (int, error) {
	switch suite.EncryptionBits {
	case 128, 256:
		return suite.EncryptionBits / 8, nil
	default:
		return 0, fmt.Errorf("%w: AES key length %d", errUnsupportedSuite, suite.EncryptionBits)
	}
}

func (suite negotiatedSuite) integrityLengths() (keyLength int, checksumLength int, err error) {
	switch suite.IntegrityID {
	case integrityHMACSHA1_96:
		return sha1.Size, 12, nil
	case integrityHMACSHA256_128:
		return sha256.Size, 16, nil
	default:
		return 0, 0, fmt.Errorf("%w: integrity id %d", errUnsupportedSuite, suite.IntegrityID)
	}
}

func prf(suite negotiatedSuite, key, data []byte) ([]byte, error) {
	hashFactory, _, err := suite.prf()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(hashFactory, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil), nil
}

func prfPlus(suite negotiatedSuite, key, seed []byte, length int) ([]byte, error) {
	if length < 0 {
		return nil, errors.New("ike: negative key stream length")
	}
	result := make([]byte, 0, length)
	var previous []byte
	for counter := byte(1); len(result) < length; counter++ {
		if counter == 0 {
			return nil, errors.New("ike: PRF+ output is too long")
		}
		input := make([]byte, 0, len(previous)+len(seed)+1)
		input = append(input, previous...)
		input = append(input, seed...)
		input = append(input, counter)
		block, err := prf(suite, key, input)
		if err != nil {
			return nil, err
		}
		result = append(result, block...)
		previous = block
	}
	return result[:length], nil
}

func deriveIKEKeys(
	suite negotiatedSuite,
	sharedSecret []byte,
	initiatorNonce []byte,
	responderNonce []byte,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
) (ikeKeys, error) {
	_, preferredLength, err := suite.prf()
	if err != nil {
		return ikeKeys{}, err
	}
	encryptionLength, err := suite.encryptionKeyLength()
	if err != nil {
		return ikeKeys{}, err
	}
	integrityLength, _, err := suite.integrityLengths()
	if err != nil {
		return ikeKeys{}, err
	}
	nonceKey := append(append([]byte(nil), initiatorNonce...), responderNonce...)
	skeyseed, err := prf(suite, nonceKey, sharedSecret)
	if err != nil {
		return ikeKeys{}, err
	}
	seed := make([]byte, 0, len(initiatorNonce)+len(responderNonce)+16)
	seed = append(seed, initiatorNonce...)
	seed = append(seed, responderNonce...)
	seed = append(seed, initiatorSPI[:]...)
	seed = append(seed, responderSPI[:]...)
	total := preferredLength + integrityLength*2 + encryptionLength*2 + preferredLength*2
	stream, err := prfPlus(suite, skeyseed, seed, total)
	if err != nil {
		return ikeKeys{}, err
	}
	take := func(length int) []byte {
		value := append([]byte(nil), stream[:length]...)
		stream = stream[length:]
		return value
	}
	return ikeKeys{
		SKd:  take(preferredLength),
		SKai: take(integrityLength),
		SKar: take(integrityLength),
		SKei: take(encryptionLength),
		SKer: take(encryptionLength),
		SKpi: take(preferredLength),
		SKpr: take(preferredLength),
	}, nil
}

func integrityMAC(suite negotiatedSuite, key, packetWithoutChecksum []byte) ([]byte, error) {
	var hashFactory func() hash.Hash
	switch suite.IntegrityID {
	case integrityHMACSHA1_96:
		hashFactory = sha1.New
	case integrityHMACSHA256_128:
		hashFactory = sha256.New
	default:
		return nil, fmt.Errorf("%w: integrity id %d", errUnsupportedSuite, suite.IntegrityID)
	}
	_, checksumLength, err := suite.integrityLengths()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(hashFactory, key)
	_, _ = mac.Write(packetWithoutChecksum)
	return mac.Sum(nil)[:checksumLength], nil
}

func encryptPayloads(
	header ikeHeader,
	inner []payload,
	suite negotiatedSuite,
	encryptionKey []byte,
	integrityKey []byte,
	random io.Reader,
) ([]byte, error) {
	if random == nil {
		random = rand.Reader
	}
	first, plaintext, err := marshalPayloadChain(inner)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("ike: initialize AES: %w", err)
	}
	paddingLength := block.BlockSize() - (len(plaintext)+1)%block.BlockSize()
	if paddingLength == block.BlockSize() {
		paddingLength = 0
	}
	padding := make([]byte, paddingLength)
	if _, err := io.ReadFull(random, padding); err != nil {
		return nil, fmt.Errorf("ike: generate encrypted payload padding: %w", err)
	}
	plaintext = append(plaintext, padding...)
	plaintext = append(plaintext, byte(paddingLength))
	iv := make([]byte, block.BlockSize())
	if _, err := io.ReadFull(random, iv); err != nil {
		return nil, fmt.Errorf("ike: generate encrypted payload IV: %w", err)
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plaintext)

	_, checksumLength, err := suite.integrityLengths()
	if err != nil {
		return nil, err
	}
	skLength := 4 + len(iv) + len(ciphertext) + checksumLength
	if skLength > 65535 {
		return nil, errors.New("ike: encrypted payload exceeds 65535 bytes")
	}
	body := make([]byte, skLength)
	body[0] = first
	body[1] = 0
	binary.BigEndian.PutUint16(body[2:4], uint16(skLength))
	copy(body[4:], iv)
	copy(body[4+len(iv):], ciphertext)
	header.NextPayload = payloadEncrypted
	packet := header.marshal(body)
	checksum, err := integrityMAC(suite, integrityKey, packet[:len(packet)-checksumLength])
	if err != nil {
		return nil, err
	}
	copy(packet[len(packet)-checksumLength:], checksum)
	return packet, nil
}

func decryptPayloads(
	packet []byte,
	suite negotiatedSuite,
	encryptionKey []byte,
	integrityKey []byte,
) (ikeHeader, []payload, error) {
	header, body, err := parseIKEPacket(packet)
	if err != nil {
		return ikeHeader{}, nil, err
	}
	if header.NextPayload != payloadEncrypted || len(body) < 4 {
		return ikeHeader{}, nil, fmt.Errorf("%w: message is not an encrypted IKE payload", errUnexpectedPacket)
	}
	skLength := int(binary.BigEndian.Uint16(body[2:4]))
	if skLength != len(body) {
		return ikeHeader{}, nil, fmt.Errorf("%w: encrypted payload length mismatch", errMalformedPacket)
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return ikeHeader{}, nil, fmt.Errorf("ike: initialize AES: %w", err)
	}
	_, checksumLength, err := suite.integrityLengths()
	if err != nil {
		return ikeHeader{}, nil, err
	}
	if len(body) < 4+block.BlockSize()+block.BlockSize()+checksumLength {
		return ikeHeader{}, nil, fmt.Errorf("%w: encrypted payload is too short", errMalformedPacket)
	}
	expected, err := integrityMAC(suite, integrityKey, packet[:len(packet)-checksumLength])
	if err != nil {
		return ikeHeader{}, nil, err
	}
	actual := packet[len(packet)-checksumLength:]
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return ikeHeader{}, nil, errIntegrityMismatch
	}
	ivStart := 4
	ciphertextStart := ivStart + block.BlockSize()
	ciphertextEnd := len(body) - checksumLength
	ciphertext := body[ciphertextStart:ciphertextEnd]
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return ikeHeader{}, nil, fmt.Errorf("%w: ciphertext is not block aligned", errMalformedPacket)
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, body[ivStart:ciphertextStart]).CryptBlocks(plaintext, ciphertext)
	paddingLength := int(plaintext[len(plaintext)-1])
	if paddingLength+1 > len(plaintext) {
		return ikeHeader{}, nil, fmt.Errorf("%w: invalid encrypted payload padding", errMalformedPacket)
	}
	plaintext = plaintext[:len(plaintext)-paddingLength-1]
	payloads, err := parsePayloadChain(body[0], plaintext)
	if err != nil {
		return ikeHeader{}, nil, err
	}
	return header, payloads, nil
}

const defaultIKEFragmentSize = 1100

func encryptPayloadsFragmented(
	header ikeHeader,
	inner []payload,
	suite negotiatedSuite,
	encryptionKey []byte,
	integrityKey []byte,
	maxFragmentSize int,
	random io.Reader,
) ([][]byte, error) {
	if random == nil {
		random = rand.Reader
	}
	if maxFragmentSize <= 0 {
		maxFragmentSize = defaultIKEFragmentSize
	}
	first, plaintext, err := marshalPayloadChain(inner)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("ike: initialize AES: %w", err)
	}
	_, checksumLength, err := suite.integrityLengths()
	if err != nil {
		return nil, err
	}

	maxChunk := maxFragmentSize - ikeHeaderLength - 8 - block.BlockSize() - block.BlockSize() - checksumLength
	if maxChunk < 64 {
		maxChunk = 64
	}

	var chunks [][]byte
	for len(plaintext) > 0 {
		take := len(plaintext)
		if take > maxChunk {
			take = maxChunk
		}
		chunks = append(chunks, plaintext[:take])
		plaintext = plaintext[take:]
	}
	if len(chunks) <= 1 {
		packet, err := encryptPayloads(
			header,
			inner,
			suite,
			encryptionKey,
			integrityKey,
			random,
		)
		if err != nil {
			return nil, err
		}
		return [][]byte{packet}, nil
	}

	totalFragments := uint16(len(chunks))

	var packets [][]byte
	for index, chunk := range chunks {
		fragNum := uint16(index + 1)
		fragNext := uint8(payloadNone)
		if fragNum == 1 {
			fragNext = first
		}

		paddingLength := block.BlockSize() - (len(chunk)+1)%block.BlockSize()
		if paddingLength == block.BlockSize() {
			paddingLength = 0
		}
		padding := make([]byte, paddingLength)
		if _, err := io.ReadFull(random, padding); err != nil {
			return nil, fmt.Errorf("ike: generate encrypted payload padding: %w", err)
		}
		paddedChunk := append(append([]byte(nil), chunk...), padding...)
		paddedChunk = append(paddedChunk, byte(paddingLength))

		iv := make([]byte, block.BlockSize())
		if _, err := io.ReadFull(random, iv); err != nil {
			return nil, fmt.Errorf("ike: generate encrypted payload IV: %w", err)
		}
		ciphertext := make([]byte, len(paddedChunk))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, paddedChunk)

		skfLength := 4 + 4 + len(iv) + len(ciphertext) + checksumLength
		if skfLength > 65535 {
			return nil, errors.New("ike: encrypted fragment exceeds 65535 bytes")
		}

		body := make([]byte, skfLength)
		body[0] = fragNext
		body[1] = 0
		binary.BigEndian.PutUint16(body[2:4], uint16(skfLength))
		binary.BigEndian.PutUint16(body[4:6], fragNum)
		binary.BigEndian.PutUint16(body[6:8], totalFragments)
		copy(body[8:], iv)
		copy(body[8+len(iv):], ciphertext)

		fragHeader := header
		fragHeader.NextPayload = payloadEncryptedFragment
		packet := fragHeader.marshal(body)
		checksum, err := integrityMAC(suite, integrityKey, packet[:len(packet)-checksumLength])
		if err != nil {
			return nil, err
		}
		copy(packet[len(packet)-checksumLength:], checksum)
		packets = append(packets, packet)
	}
	return packets, nil
}

func decryptSingleFragment(
	packet []byte,
	suite negotiatedSuite,
	encryptionKey []byte,
	integrityKey []byte,
) (ikeHeader, uint8, uint16, uint16, []byte, error) {
	header, body, err := parseIKEPacket(packet)
	if err != nil {
		return ikeHeader{}, 0, 0, 0, nil, err
	}
	if header.NextPayload != payloadEncryptedFragment || len(body) < 8 {
		return ikeHeader{}, 0, 0, 0, nil, fmt.Errorf("%w: message is not an encrypted IKE fragment", errUnexpectedPacket)
	}
	skfLength := int(binary.BigEndian.Uint16(body[2:4]))
	if skfLength != len(body) {
		return ikeHeader{}, 0, 0, 0, nil, fmt.Errorf("%w: encrypted fragment length mismatch", errMalformedPacket)
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return ikeHeader{}, 0, 0, 0, nil, fmt.Errorf("ike: initialize AES: %w", err)
	}
	_, checksumLength, err := suite.integrityLengths()
	if err != nil {
		return ikeHeader{}, 0, 0, 0, nil, err
	}
	if len(body) < 8+block.BlockSize()+block.BlockSize()+checksumLength {
		return ikeHeader{}, 0, 0, 0, nil, fmt.Errorf("%w: encrypted fragment is too short", errMalformedPacket)
	}
	expected, err := integrityMAC(suite, integrityKey, packet[:len(packet)-checksumLength])
	if err != nil {
		return ikeHeader{}, 0, 0, 0, nil, err
	}
	actual := packet[len(packet)-checksumLength:]
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return ikeHeader{}, 0, 0, 0, nil, errIntegrityMismatch
	}

	fragNext := body[0]
	fragNum := binary.BigEndian.Uint16(body[4:6])
	totalFrags := binary.BigEndian.Uint16(body[6:8])
	if fragNum == 0 || totalFrags == 0 || fragNum > totalFrags {
		return ikeHeader{}, 0, 0, 0, nil, fmt.Errorf("%w: invalid fragment numbers %d/%d", errMalformedPacket, fragNum, totalFrags)
	}

	ivStart := 8
	ciphertextStart := ivStart + block.BlockSize()
	ciphertextEnd := len(body) - checksumLength
	ciphertext := body[ciphertextStart:ciphertextEnd]
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return ikeHeader{}, 0, 0, 0, nil, fmt.Errorf("%w: fragment ciphertext is not block aligned", errMalformedPacket)
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, body[ivStart:ciphertextStart]).CryptBlocks(plaintext, ciphertext)
	paddingLength := int(plaintext[len(plaintext)-1])
	if paddingLength+1 > len(plaintext) {
		return ikeHeader{}, 0, 0, 0, nil, fmt.Errorf("%w: invalid encrypted fragment padding", errMalformedPacket)
	}
	plaintext = plaintext[:len(plaintext)-paddingLength-1]
	return header, fragNext, fragNum, totalFrags, plaintext, nil
}

func decryptPayloadsAny(
	packet []byte,
	fragments [][]byte,
	suite negotiatedSuite,
	encryptionKey []byte,
	integrityKey []byte,
) (ikeHeader, []payload, error) {
	if len(fragments) > 0 {
		var (
			firstHeader   ikeHeader
			firstNext     uint8
			totalExpected uint16
			plaintexts    = make(map[uint16][]byte)
		)
		for _, fragPacket := range fragments {
			hdr, next, num, total, plain, err := decryptSingleFragment(fragPacket, suite, encryptionKey, integrityKey)
			if err != nil {
				return ikeHeader{}, nil, err
			}
			if totalExpected == 0 {
				firstHeader = hdr
				totalExpected = total
			} else if total != totalExpected || hdr.MessageID != firstHeader.MessageID || hdr.Exchange != firstHeader.Exchange {
				return ikeHeader{}, nil, fmt.Errorf("%w: inconsistent fragment headers", errMalformedPacket)
			}
			if num == 1 {
				firstNext = next
			}
			plaintexts[num] = plain
		}
		if uint16(len(plaintexts)) != totalExpected {
			return ikeHeader{}, nil, fmt.Errorf("%w: missing fragments: received %d of %d", errMalformedPacket, len(plaintexts), totalExpected)
		}
		var fullPlaintext []byte
		for i := uint16(1); i <= totalExpected; i++ {
			chunk, ok := plaintexts[i]
			if !ok {
				return ikeHeader{}, nil, fmt.Errorf("%w: missing fragment %d", errMalformedPacket, i)
			}
			fullPlaintext = append(fullPlaintext, chunk...)
		}
		payloads, err := parsePayloadChain(firstNext, fullPlaintext)
		if err != nil {
			return ikeHeader{}, nil, err
		}
		return firstHeader, payloads, nil
	}

	header, _, err := parseIKEPacket(packet)
	if err != nil {
		return ikeHeader{}, nil, err
	}
	if header.NextPayload == payloadEncryptedFragment {
		hdr, next, num, total, plain, err := decryptSingleFragment(packet, suite, encryptionKey, integrityKey)
		if err != nil {
			return ikeHeader{}, nil, err
		}
		if num != 1 || total != 1 {
			return ikeHeader{}, nil, fmt.Errorf("%w: standalone fragment with total=%d", errMalformedPacket, total)
		}
		payloads, err := parsePayloadChain(next, plain)
		if err != nil {
			return ikeHeader{}, nil, err
		}
		return hdr, payloads, nil
	}
	return decryptPayloads(packet, suite, encryptionKey, integrityKey)
}

var modpPrimes = map[uint16]string{
	dhMODP1024: "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
		"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
		"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
		"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381" +
		"FFFFFFFFFFFFFFFF",
	dhMODP2048: "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
		"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
		"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
		"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3D" +
		"C2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F" +
		"83655D23DCA3AD961C62F356208552BB9ED529077096966D" +
		"670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
		"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9" +
		"DE2BCBF6955817183995497CEA956AE515D2261898FA0510" +
		"15728E5A8AACAA68FFFFFFFFFFFFFFFF",
}

type dhExchange struct {
	Group   uint16
	prime   *big.Int
	private *big.Int
	Public  []byte
}

func newDHExchange(group uint16, random io.Reader) (*dhExchange, error) {
	primeHex, ok := modpPrimes[group]
	if !ok {
		return nil, fmt.Errorf("%w: DH group %d", errUnsupportedSuite, group)
	}
	primeBytes, err := hex.DecodeString(primeHex)
	if err != nil {
		return nil, fmt.Errorf("ike: internal MODP constant: %w", err)
	}
	prime := new(big.Int).SetBytes(primeBytes)
	if random == nil {
		random = rand.Reader
	}
	sample := make([]byte, len(primeBytes))
	if _, err := io.ReadFull(random, sample); err != nil {
		return nil, fmt.Errorf("ike: generate DH private value: %w", err)
	}
	private := new(big.Int).SetBytes(sample)
	private.Mod(private, new(big.Int).Sub(prime, big.NewInt(3)))
	private.Add(private, big.NewInt(2))
	publicInteger := new(big.Int).Exp(big.NewInt(2), private, prime)
	public := publicInteger.FillBytes(make([]byte, len(primeBytes)))
	return &dhExchange{Group: group, prime: prime, private: private, Public: public}, nil
}

func (exchange *dhExchange) shared(peerPublic []byte) ([]byte, error) {
	if exchange == nil || exchange.prime == nil || exchange.private == nil {
		return nil, errors.New("ike: DH exchange is not initialized")
	}
	if len(peerPublic) != len(exchange.Public) {
		return nil, fmt.Errorf("ike: peer DH value length %d does not match group length %d", len(peerPublic), len(exchange.Public))
	}
	peer := new(big.Int).SetBytes(peerPublic)
	upper := new(big.Int).Sub(exchange.prime, big.NewInt(2))
	if peer.Cmp(big.NewInt(2)) < 0 || peer.Cmp(upper) > 0 {
		return nil, errors.New("ike: peer DH public value is outside the safe range")
	}
	shared := new(big.Int).Exp(peer, exchange.private, exchange.prime)
	if shared.Sign() == 0 || shared.Cmp(big.NewInt(1)) == 0 {
		return nil, errors.New("ike: invalid trivial DH shared secret")
	}
	return shared.FillBytes(make([]byte, len(exchange.Public))), nil
}

func natDetectionHash(
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	ip net.IP,
	port uint16,
) ([]byte, error) {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	} else if ip16 := ip.To16(); ip16 != nil {
		ip = ip16
	} else {
		return nil, errors.New("ike: NAT detection address is not an IP address")
	}
	input := make([]byte, 0, 16+len(ip)+2)
	input = append(input, initiatorSPI[:]...)
	input = append(input, responderSPI[:]...)
	input = append(input, ip...)
	var encodedPort [2]byte
	binary.BigEndian.PutUint16(encodedPort[:], port)
	input = append(input, encodedPort[:]...)
	sum := sha1.Sum(input)
	return sum[:], nil
}
