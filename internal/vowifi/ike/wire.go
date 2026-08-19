package ike

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	ikeHeaderLength = 28
	ikeMajorVersion = 2
	ikeMinorVersion = 0

	exchangeIKEInit       = 34
	exchangeIKEAuth       = 35
	exchangeInformational = 37

	flagInitiator = 0x08
	flagResponse  = 0x20

	payloadNone      = 0
	payloadSA        = 33
	payloadKE        = 34
	payloadIDi       = 35
	payloadIDr       = 36
	payloadCert      = 37
	payloadAuth      = 39
	payloadNonce     = 40
	payloadNotify    = 41
	payloadDelete    = 42
	payloadTSi       = 44
	payloadTSr       = 45
	payloadEncrypted = 46
	payloadCP        = 47
	payloadEAP       = 48

	protocolIKE = 1
	protocolESP = 3

	transformEncryption = 1
	transformPRF        = 2
	transformIntegrity  = 3
	transformDH         = 4
	transformESN        = 5

	encryptionAESCBC = 12

	prfHMACSHA1   = 2
	prfHMACSHA256 = 5

	integrityHMACSHA1_96     = 2
	integrityHMACSHA256_128  = 12
	dhMODP1024               = 2
	dhMODP2048               = 14
	transformAttributeKeyLen = 14

	notifyInitialContact  = 16384
	notifyMOBIKESupported = 16396
	notifyNATSource       = 16388
	notifyNATDestination  = 16389
	notifyCookie          = 16390
	notifyEAPOnlyAuth     = 16417
	notifyDeviceIdentity  = 41101
	notifyInvalidKE       = 17
	notifyNoProposal      = 14
)

var (
	errMalformedPacket   = errors.New("ike: malformed packet")
	errUnexpectedPacket  = errors.New("ike: unexpected packet")
	errUnsupportedSuite  = errors.New("ike: unsupported negotiated suite")
	errIntegrityMismatch = errors.New("ike: encrypted payload integrity mismatch")
)

type ikeHeader struct {
	InitiatorSPI [8]byte
	ResponderSPI [8]byte
	NextPayload  uint8
	Version      uint8
	Exchange     uint8
	Flags        uint8
	MessageID    uint32
	Length       uint32
}

func (header ikeHeader) marshal(body []byte) []byte {
	packet := make([]byte, ikeHeaderLength+len(body))
	copy(packet[0:8], header.InitiatorSPI[:])
	copy(packet[8:16], header.ResponderSPI[:])
	packet[16] = header.NextPayload
	if header.Version == 0 {
		header.Version = ikeMajorVersion<<4 | ikeMinorVersion
	}
	packet[17] = header.Version
	packet[18] = header.Exchange
	packet[19] = header.Flags
	binary.BigEndian.PutUint32(packet[20:24], header.MessageID)
	binary.BigEndian.PutUint32(packet[24:28], uint32(len(packet)))
	copy(packet[28:], body)
	return packet
}

func parseIKEPacket(packet []byte) (ikeHeader, []byte, error) {
	if len(packet) < ikeHeaderLength {
		return ikeHeader{}, nil, fmt.Errorf("%w: header is truncated", errMalformedPacket)
	}
	var header ikeHeader
	copy(header.InitiatorSPI[:], packet[0:8])
	copy(header.ResponderSPI[:], packet[8:16])
	header.NextPayload = packet[16]
	header.Version = packet[17]
	header.Exchange = packet[18]
	header.Flags = packet[19]
	header.MessageID = binary.BigEndian.Uint32(packet[20:24])
	header.Length = binary.BigEndian.Uint32(packet[24:28])
	if header.Version>>4 != ikeMajorVersion {
		return ikeHeader{}, nil, fmt.Errorf("%w: unsupported IKE major version %d", errMalformedPacket, header.Version>>4)
	}
	if header.Length < ikeHeaderLength || uint64(header.Length) != uint64(len(packet)) {
		return ikeHeader{}, nil, fmt.Errorf("%w: encoded length %d does not match datagram length %d", errMalformedPacket, header.Length, len(packet))
	}
	return header, packet[ikeHeaderLength:], nil
}

type payload struct {
	Type     uint8
	Critical bool
	Body     []byte
}

func marshalPayloadChain(payloads []payload) (uint8, []byte, error) {
	if len(payloads) == 0 {
		return payloadNone, nil, nil
	}
	var output bytes.Buffer
	for index, item := range payloads {
		if item.Type == payloadNone || item.Type == payloadEncrypted {
			return 0, nil, fmt.Errorf("ike: invalid ordinary payload type %d", item.Type)
		}
		next := uint8(payloadNone)
		if index+1 < len(payloads) {
			next = payloads[index+1].Type
		}
		length := 4 + len(item.Body)
		if length > 65535 {
			return 0, nil, errors.New("ike: payload exceeds 65535 bytes")
		}
		output.WriteByte(next)
		if item.Critical {
			output.WriteByte(0x80)
		} else {
			output.WriteByte(0)
		}
		var encodedLength [2]byte
		binary.BigEndian.PutUint16(encodedLength[:], uint16(length))
		output.Write(encodedLength[:])
		output.Write(item.Body)
	}
	return payloads[0].Type, output.Bytes(), nil
}

func parsePayloadChain(first uint8, encoded []byte) ([]payload, error) {
	var result []payload
	next := first
	offset := 0
	for next != payloadNone {
		if len(result) >= 64 {
			return nil, fmt.Errorf("%w: too many chained payloads", errMalformedPacket)
		}
		if offset+4 > len(encoded) {
			return nil, fmt.Errorf("%w: payload header is truncated", errMalformedPacket)
		}
		following := encoded[offset]
		flags := encoded[offset+1]
		length := int(binary.BigEndian.Uint16(encoded[offset+2 : offset+4]))
		if length < 4 || offset+length > len(encoded) {
			return nil, fmt.Errorf("%w: payload type %d has invalid length %d", errMalformedPacket, next, length)
		}
		body := append([]byte(nil), encoded[offset+4:offset+length]...)
		result = append(result, payload{Type: next, Critical: flags&0x80 != 0, Body: body})
		offset += length
		next = following
	}
	if offset != len(encoded) {
		return nil, fmt.Errorf("%w: %d trailing payload bytes", errMalformedPacket, len(encoded)-offset)
	}
	return result, nil
}

func payloadsOfType(payloads []payload, kind uint8) []payload {
	var matches []payload
	for _, item := range payloads {
		if item.Type == kind {
			matches = append(matches, item)
		}
	}
	return matches
}

func onePayload(payloads []payload, kind uint8) (payload, error) {
	matches := payloadsOfType(payloads, kind)
	if len(matches) != 1 {
		return payload{}, fmt.Errorf("%w: expected one payload type %d, got %d", errUnexpectedPacket, kind, len(matches))
	}
	return matches[0], nil
}

type transform struct {
	Type      uint8
	ID        uint16
	KeyLength int
}

type proposal struct {
	Number     uint8
	Protocol   uint8
	SPI        []byte
	Transforms []transform
}

func marshalProposals(proposals []proposal) ([]byte, error) {
	var output bytes.Buffer
	for proposalIndex, item := range proposals {
		if len(item.SPI) > 255 || len(item.Transforms) > 255 {
			return nil, errors.New("ike: proposal has too many bytes or transforms")
		}
		var transforms bytes.Buffer
		for transformIndex, candidate := range item.Transforms {
			var attributes []byte
			if candidate.KeyLength > 0 {
				attributes = make([]byte, 4)
				binary.BigEndian.PutUint16(attributes[0:2], 0x8000|transformAttributeKeyLen)
				binary.BigEndian.PutUint16(attributes[2:4], uint16(candidate.KeyLength))
			}
			length := 8 + len(attributes)
			if transformIndex+1 < len(item.Transforms) {
				transforms.WriteByte(3)
			} else {
				transforms.WriteByte(0)
			}
			transforms.WriteByte(0)
			var header [6]byte
			binary.BigEndian.PutUint16(header[0:2], uint16(length))
			header[2] = candidate.Type
			header[3] = 0
			binary.BigEndian.PutUint16(header[4:6], candidate.ID)
			transforms.Write(header[:])
			transforms.Write(attributes)
		}
		length := 8 + len(item.SPI) + transforms.Len()
		if proposalIndex+1 < len(proposals) {
			output.WriteByte(2)
		} else {
			output.WriteByte(0)
		}
		output.WriteByte(0)
		var header [6]byte
		binary.BigEndian.PutUint16(header[0:2], uint16(length))
		header[2] = item.Number
		header[3] = item.Protocol
		header[4] = uint8(len(item.SPI))
		header[5] = uint8(len(item.Transforms))
		output.Write(header[:])
		output.Write(item.SPI)
		output.Write(transforms.Bytes())
	}
	return output.Bytes(), nil
}

func parseProposals(encoded []byte) ([]proposal, error) {
	var result []proposal
	offset := 0
	for {
		if offset == len(encoded) {
			break
		}
		if len(result) >= 16 || offset+8 > len(encoded) {
			return nil, fmt.Errorf("%w: invalid SA proposal header", errMalformedPacket)
		}
		last := encoded[offset]
		length := int(binary.BigEndian.Uint16(encoded[offset+2 : offset+4]))
		spiSize := int(encoded[offset+6])
		transformCount := int(encoded[offset+7])
		if length < 8+spiSize || offset+length > len(encoded) {
			return nil, fmt.Errorf("%w: invalid SA proposal length", errMalformedPacket)
		}
		item := proposal{
			Number:   encoded[offset+4],
			Protocol: encoded[offset+5],
			SPI:      append([]byte(nil), encoded[offset+8:offset+8+spiSize]...),
		}
		transformOffset := offset + 8 + spiSize
		proposalEnd := offset + length
		for transformOffset < proposalEnd {
			if len(item.Transforms) >= 32 || transformOffset+8 > proposalEnd {
				return nil, fmt.Errorf("%w: invalid transform header", errMalformedPacket)
			}
			transformLength := int(binary.BigEndian.Uint16(encoded[transformOffset+2 : transformOffset+4]))
			if transformLength < 8 || transformOffset+transformLength > proposalEnd {
				return nil, fmt.Errorf("%w: invalid transform length", errMalformedPacket)
			}
			transformEnd := transformOffset + transformLength
			if transformEnd < proposalEnd && encoded[transformOffset] != 3 {
				return nil, fmt.Errorf("%w: non-final transform has invalid chaining marker", errMalformedPacket)
			}
			if transformEnd == proposalEnd && encoded[transformOffset] != 0 {
				return nil, fmt.Errorf("%w: final transform has invalid chaining marker", errMalformedPacket)
			}
			candidate := transform{
				Type: encoded[transformOffset+4],
				ID:   binary.BigEndian.Uint16(encoded[transformOffset+6 : transformOffset+8]),
			}
			attributes := encoded[transformOffset+8 : transformOffset+transformLength]
			for len(attributes) > 0 {
				if len(attributes) < 4 {
					return nil, fmt.Errorf("%w: truncated transform attribute", errMalformedPacket)
				}
				attributeType := binary.BigEndian.Uint16(attributes[0:2])
				if attributeType&0x8000 != 0 {
					if attributeType&0x7fff == transformAttributeKeyLen {
						candidate.KeyLength = int(binary.BigEndian.Uint16(attributes[2:4]))
					}
					attributes = attributes[4:]
					continue
				}
				attributeLength := int(binary.BigEndian.Uint16(attributes[2:4]))
				if attributeLength < 0 || 4+attributeLength > len(attributes) {
					return nil, fmt.Errorf("%w: invalid transform TLV attribute", errMalformedPacket)
				}
				if attributeType == transformAttributeKeyLen && attributeLength == 2 {
					candidate.KeyLength = int(binary.BigEndian.Uint16(attributes[4:6]))
				}
				attributes = attributes[4+attributeLength:]
			}
			item.Transforms = append(item.Transforms, candidate)
			transformOffset += transformLength
		}
		if transformOffset != proposalEnd || len(item.Transforms) != transformCount {
			return nil, fmt.Errorf("%w: transform count mismatch", errMalformedPacket)
		}
		result = append(result, item)
		offset = proposalEnd
		if last == 0 {
			if offset != len(encoded) {
				return nil, fmt.Errorf("%w: bytes follow last proposal", errMalformedPacket)
			}
			break
		}
		if last != 2 {
			return nil, fmt.Errorf("%w: invalid proposal chaining marker %d", errMalformedPacket, last)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: empty SA payload", errMalformedPacket)
	}
	return result, nil
}

type negotiatedSuite struct {
	EncryptionID   uint16
	EncryptionBits int
	PRFID          uint16
	IntegrityID    uint16
	DHID           uint16
}

func parseIKESuite(item proposal) (negotiatedSuite, error) {
	if item.Protocol != protocolIKE || len(item.SPI) != 0 {
		return negotiatedSuite{}, fmt.Errorf("%w: responder selected a non-IKE proposal", errUnsupportedSuite)
	}
	var suite negotiatedSuite
	seen := make(map[uint8]bool)
	for _, candidate := range item.Transforms {
		if seen[candidate.Type] {
			return negotiatedSuite{}, fmt.Errorf("%w: duplicate transform type %d", errUnsupportedSuite, candidate.Type)
		}
		seen[candidate.Type] = true
		switch candidate.Type {
		case transformEncryption:
			suite.EncryptionID = candidate.ID
			suite.EncryptionBits = candidate.KeyLength
		case transformPRF:
			suite.PRFID = candidate.ID
		case transformIntegrity:
			suite.IntegrityID = candidate.ID
		case transformDH:
			suite.DHID = candidate.ID
		default:
			return negotiatedSuite{}, fmt.Errorf("%w: IKE transform type %d", errUnsupportedSuite, candidate.Type)
		}
	}
	if suite.EncryptionID != encryptionAESCBC || (suite.EncryptionBits != 128 && suite.EncryptionBits != 256) {
		return negotiatedSuite{}, fmt.Errorf("%w: encryption id=%d bits=%d", errUnsupportedSuite, suite.EncryptionID, suite.EncryptionBits)
	}
	if suite.PRFID != prfHMACSHA1 && suite.PRFID != prfHMACSHA256 {
		return negotiatedSuite{}, fmt.Errorf("%w: PRF id=%d", errUnsupportedSuite, suite.PRFID)
	}
	if suite.IntegrityID != integrityHMACSHA1_96 && suite.IntegrityID != integrityHMACSHA256_128 {
		return negotiatedSuite{}, fmt.Errorf("%w: integrity id=%d", errUnsupportedSuite, suite.IntegrityID)
	}
	if suite.DHID != dhMODP1024 && suite.DHID != dhMODP2048 {
		return negotiatedSuite{}, fmt.Errorf("%w: DH id=%d", errUnsupportedSuite, suite.DHID)
	}
	return suite, nil
}

func makeNotify(notifyType uint16, data []byte) payload {
	body := make([]byte, 4+len(data))
	body[0] = 0
	body[1] = 0
	binary.BigEndian.PutUint16(body[2:4], notifyType)
	copy(body[4:], data)
	return payload{Type: payloadNotify, Body: body}
}

func parseNotify(item payload) (uint16, []byte, error) {
	if item.Type != payloadNotify || len(item.Body) < 4 {
		return 0, nil, fmt.Errorf("%w: invalid notify payload", errMalformedPacket)
	}
	spiSize := int(item.Body[1])
	if 4+spiSize > len(item.Body) {
		return 0, nil, fmt.Errorf("%w: truncated notify SPI", errMalformedPacket)
	}
	return binary.BigEndian.Uint16(item.Body[2:4]), append([]byte(nil), item.Body[4+spiSize:]...), nil
}
