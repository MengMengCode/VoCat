package ike

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"

	"vocat/internal/vowifi"
)

const (
	configRequest = 1
	configReply   = 2

	configInternalIPv4Address = 1
	configInternalIPv4DNS     = 3
	configApplicationVersion  = 7
	configInternalIPv6Address = 8
	configInternalIPv6DNS     = 10
	configPCSCFIPv4Address    = 20
	configPCSCFIPv6Address    = 21

	trafficSelectorIPv4Range = 7
	trafficSelectorIPv6Range = 8
)

type espSuite struct {
	EncryptionID   uint16
	EncryptionBits int
	IntegrityID    uint16
	ESN            uint16
}

func (suite espSuite) encryptionKeyLength() (int, error) {
	if suite.EncryptionID != encryptionAESCBC || (suite.EncryptionBits != 128 && suite.EncryptionBits != 256) {
		return 0, fmt.Errorf("%w: ESP encryption id=%d bits=%d", errUnsupportedSuite, suite.EncryptionID, suite.EncryptionBits)
	}
	return suite.EncryptionBits / 8, nil
}

func (suite espSuite) integrityKeyLength() (int, error) {
	switch suite.IntegrityID {
	case integrityHMACSHA1_96:
		return 20, nil
	case integrityHMACSHA256_128:
		return 32, nil
	default:
		return 0, fmt.Errorf("%w: ESP integrity id=%d", errUnsupportedSuite, suite.IntegrityID)
	}
}

func parseESPSuite(item proposal) (espSuite, error) {
	if item.Protocol != protocolESP || len(item.SPI) != 4 {
		return espSuite{}, fmt.Errorf("%w: invalid ESP proposal protocol or SPI", errUnsupportedSuite)
	}
	var suite espSuite
	seen := make(map[uint8]bool)
	for _, candidate := range item.Transforms {
		if seen[candidate.Type] {
			return espSuite{}, fmt.Errorf("%w: duplicate ESP transform type %d", errUnsupportedSuite, candidate.Type)
		}
		seen[candidate.Type] = true
		switch candidate.Type {
		case transformEncryption:
			suite.EncryptionID = candidate.ID
			suite.EncryptionBits = candidate.KeyLength
		case transformIntegrity:
			suite.IntegrityID = candidate.ID
		case transformESN:
			suite.ESN = candidate.ID
		default:
			return espSuite{}, fmt.Errorf("%w: unsupported ESP transform type %d", errUnsupportedSuite, candidate.Type)
		}
	}
	if _, err := suite.encryptionKeyLength(); err != nil {
		return espSuite{}, err
	}
	if _, err := suite.integrityKeyLength(); err != nil {
		return espSuite{}, err
	}
	if suite.ESN != 0 {
		return espSuite{}, fmt.Errorf("%w: ESP extended sequence numbers are unsupported", errUnsupportedSuite)
	}
	return suite, nil
}

type trafficSelector struct {
	IPProtocol uint8
	StartPort  uint16
	EndPort    uint16
	StartIP    net.IP
	EndIP      net.IP
}

func anyTrafficSelector(ipv6 bool) payload {
	var selector []byte
	if ipv6 {
		selector = make([]byte, 40)
		selector[0] = trafficSelectorIPv6Range
		binary.BigEndian.PutUint16(selector[2:4], uint16(len(selector)))
		binary.BigEndian.PutUint16(selector[6:8], 65535)
		copy(selector[8:24], net.IPv6zero)
		for index := 24; index < 40; index++ {
			selector[index] = 0xff
		}
	} else {
		selector = make([]byte, 16)
		selector[0] = trafficSelectorIPv4Range
		binary.BigEndian.PutUint16(selector[2:4], uint16(len(selector)))
		binary.BigEndian.PutUint16(selector[6:8], 65535)
		copy(selector[8:12], net.IPv4zero.To4())
		copy(selector[12:16], net.IPv4bcast.To4())
	}
	body := append([]byte{1, 0, 0, 0}, selector...)
	return payload{Body: body}
}

func dualStackTrafficSelectors(kind uint8) payload {
	ipv4 := anyTrafficSelector(false)
	ipv6 := anyTrafficSelector(true)
	body := []byte{2, 0, 0, 0}
	body = append(body, ipv4.Body[4:]...)
	body = append(body, ipv6.Body[4:]...)
	return payload{Type: kind, Body: body}
}

func parseTrafficSelectors(item payload) ([]trafficSelector, error) {
	if len(item.Body) < 4 {
		return nil, errors.New("ike: traffic selector payload is truncated")
	}
	count := int(item.Body[0])
	offset := 4
	result := make([]trafficSelector, 0, count)
	for index := 0; index < count; index++ {
		if offset+8 > len(item.Body) {
			return nil, errors.New("ike: traffic selector is truncated")
		}
		length := int(binary.BigEndian.Uint16(item.Body[offset+2 : offset+4]))
		if length < 16 || offset+length > len(item.Body) {
			return nil, errors.New("ike: traffic selector has an invalid length")
		}
		selector := trafficSelector{
			IPProtocol: item.Body[offset+1],
			StartPort:  binary.BigEndian.Uint16(item.Body[offset+4 : offset+6]),
			EndPort:    binary.BigEndian.Uint16(item.Body[offset+6 : offset+8]),
		}
		switch item.Body[offset] {
		case trafficSelectorIPv4Range:
			if length != 16 {
				return nil, errors.New("ike: IPv4 traffic selector has an invalid length")
			}
			selector.StartIP = append(net.IP(nil), item.Body[offset+8:offset+12]...)
			selector.EndIP = append(net.IP(nil), item.Body[offset+12:offset+16]...)
		case trafficSelectorIPv6Range:
			if length != 40 {
				return nil, errors.New("ike: IPv6 traffic selector has an invalid length")
			}
			selector.StartIP = append(net.IP(nil), item.Body[offset+8:offset+24]...)
			selector.EndIP = append(net.IP(nil), item.Body[offset+24:offset+40]...)
		default:
			return nil, fmt.Errorf("ike: unsupported traffic selector type %d", item.Body[offset])
		}
		result = append(result, selector)
		offset += length
	}
	if offset != len(item.Body) {
		return nil, errors.New("ike: traffic selector payload has trailing bytes")
	}
	return result, nil
}

type networkConfiguration struct {
	LocalIPv4  net.IP
	LocalIPv6  net.IP
	IPv6Prefix uint8
	DNS        []net.IP
	PCSCF      []net.IP
}

func configurationRequest() payload {
	attributes := []uint16{
		configInternalIPv4Address,
		configInternalIPv6Address,
		configInternalIPv4DNS,
		configInternalIPv6DNS,
		configPCSCFIPv4Address,
		configPCSCFIPv6Address,
		// Android's IKE library always appends APPLICATION_VERSION to the
		// initial configuration request, even when the value is empty.
		configApplicationVersion,
	}
	body := []byte{configRequest, 0, 0, 0}
	for _, attribute := range attributes {
		var header [4]byte
		binary.BigEndian.PutUint16(header[0:2], attribute)
		body = append(body, header[:]...)
	}
	return payload{Type: payloadCP, Body: body}
}

func parseConfiguration(item payload) (networkConfiguration, error) {
	if item.Type != payloadCP || len(item.Body) < 4 || item.Body[0] != configReply {
		return networkConfiguration{}, errors.New("ike: missing or invalid configuration reply")
	}
	var configuration networkConfiguration
	for offset := 4; offset < len(item.Body); {
		if offset+4 > len(item.Body) {
			return networkConfiguration{}, errors.New("ike: truncated configuration attribute")
		}
		kind := binary.BigEndian.Uint16(item.Body[offset : offset+2])
		length := int(binary.BigEndian.Uint16(item.Body[offset+2 : offset+4]))
		offset += 4
		if offset+length > len(item.Body) {
			return networkConfiguration{}, errors.New("ike: invalid configuration attribute length")
		}
		value := item.Body[offset : offset+length]
		switch kind & 0x7fff {
		case configInternalIPv4Address:
			if length == 4 {
				configuration.LocalIPv4 = append(net.IP(nil), value...)
			}
		case configInternalIPv6Address:
			if length != 17 {
				return networkConfiguration{}, errors.New("ike: INTERNAL_IP6_ADDRESS must contain 16 address bytes and one prefix byte")
			}
			if value[16] > 128 {
				return networkConfiguration{}, errors.New("ike: INTERNAL_IP6_ADDRESS prefix exceeds 128")
			}
			configuration.LocalIPv6 = append(net.IP(nil), value[:16]...)
			configuration.IPv6Prefix = value[16]
		case configInternalIPv4DNS:
			if length == 4 {
				configuration.DNS = append(configuration.DNS, append(net.IP(nil), value...))
			}
		case configInternalIPv6DNS:
			if length == 16 {
				configuration.DNS = append(configuration.DNS, append(net.IP(nil), value...))
			}
		case configPCSCFIPv4Address:
			if length == 4 {
				configuration.PCSCF = append(configuration.PCSCF, append(net.IP(nil), value...))
			}
		case configPCSCFIPv6Address:
			if length == 16 {
				configuration.PCSCF = append(configuration.PCSCF, append(net.IP(nil), value...))
			}
		}
		offset += length
	}
	return configuration, nil
}

type ChildSAConfig struct {
	Name               string
	OuterLocal         net.IP
	OuterRemote        net.IP
	InnerLocalIPv4     net.IP
	InnerLocalIPv6     net.IP
	InnerIPv6Prefix    uint8
	PCSCF              []net.IP
	DNS                []net.IP
	InboundSPI         uint32
	OutboundSPI        uint32
	Encryption         string
	Integrity          string
	InboundEncKey      []byte
	InboundAuthKey     []byte
	OutboundEncKey     []byte
	OutboundAuthKey    []byte
	InitiatorSelectors []trafficSelector
	ResponderSelectors []trafficSelector
	UDPEncapsulation   bool
	ProxyMode          vowifi.ProxyMode
	Relay              NATTPacketRelay
}

// NATTPacketRelay carries raw ESP packets inside UDP/4500.  A user-space
// CHILD_SA installer must use this relay when ProxyMode is SOCKS5; kernel
// XFRM output cannot transparently enter a SOCKS5 UDP association.
type NATTPacketRelay interface {
	SendESP(context.Context, []byte) error
	ReceiveESP(context.Context, []byte) (int, error)
}

type ChildSAHandle interface {
	Close(context.Context) error
}

type DataplaneEvidence interface {
	DataplaneMode() string
}

type DataplaneFailureNotifier interface {
	Failures() <-chan error
}

type ChildSAInstaller interface {
	Install(context.Context, ChildSAConfig) (ChildSAHandle, error)
}

func deriveChildSAKeys(
	ikeSuite negotiatedSuite,
	childSuite espSuite,
	skd []byte,
	initiatorNonce []byte,
	responderNonce []byte,
) (outboundEncryption, outboundIntegrity, inboundEncryption, inboundIntegrity []byte, err error) {
	encryptionLength, err := childSuite.encryptionKeyLength()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	integrityLength, err := childSuite.integrityKeyLength()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	seed := append(append([]byte(nil), initiatorNonce...), responderNonce...)
	stream, err := prfPlus(ikeSuite, skd, seed, 2*(encryptionLength+integrityLength))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	take := func(length int) []byte {
		value := append([]byte(nil), stream[:length]...)
		stream = stream[length:]
		return value
	}
	return take(encryptionLength), take(integrityLength), take(encryptionLength), take(integrityLength), nil
}

func espSuiteNames(suite espSuite) (encryption string, integrity string) {
	encryption = fmt.Sprintf("aes-cbc-%d", suite.EncryptionBits)
	switch suite.IntegrityID {
	case integrityHMACSHA1_96:
		integrity = "hmac-sha1-96"
	case integrityHMACSHA256_128:
		integrity = "hmac-sha2-256-128"
	}
	return encryption, integrity
}
