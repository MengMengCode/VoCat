package ike

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"vocat/internal/vowifi"
)

const (
	eapRequest  = 1
	eapResponse = 2
	eapSuccess  = 3
	eapFailure  = 4

	eapTypeIdentity     = 1
	eapTypeNotification = 2
	eapTypeNak          = 3
	eapTypeAKA          = 23
	eapTypeAKAPrime     = 50

	akaSubtypeChallenge    = 1
	akaSubtypeAuthReject   = 2
	akaSubtypeSyncFailure  = 4
	akaSubtypeIdentity     = 5
	akaSubtypeNotification = 12
	akaSubtypeReauth       = 13
	akaSubtypeClientError  = 14

	akaAttrRAND           = 1
	akaAttrAUTN           = 2
	akaAttrRES            = 3
	akaAttrAUTS           = 4
	akaAttrPermanentIDReq = 10
	akaAttrMAC            = 11
	akaAttrNotification   = 12
	akaAttrAnyIDReq       = 13
	akaAttrIdentity       = 14
	akaAttrFullAuthIDReq  = 17
	akaAttrClientError    = 22
	akaAttrKDFInput       = 23
	akaAttrKDF            = 24
	akaAttrResultInd      = 135
	akaAttrBidding        = 136

	akaPrimeKDF = 1
)

var errAKAProvider = errors.New("ike: SIM AKA provider failure")
var errAKAPrimeKDFUnsupported = errors.New("ike: EAP-AKA' server offered no supported KDF")
var errAKAPrimeAuthenticationReject = errors.New("ike: EAP-AKA' authentication parameters rejected")

type eapPacket struct {
	Code       uint8
	Identifier uint8
	Type       uint8
	Data       []byte
}

func parseEAPPacket(encoded []byte) (eapPacket, error) {
	if len(encoded) < 4 {
		return eapPacket{}, errors.New("ike: truncated EAP header")
	}
	length := int(binary.BigEndian.Uint16(encoded[2:4]))
	if length != len(encoded) {
		return eapPacket{}, fmt.Errorf("ike: EAP length %d does not match payload length %d", length, len(encoded))
	}
	packet := eapPacket{Code: encoded[0], Identifier: encoded[1]}
	switch packet.Code {
	case eapRequest, eapResponse:
		if len(encoded) < 5 {
			return eapPacket{}, errors.New("ike: typed EAP packet is truncated")
		}
		packet.Type = encoded[4]
		packet.Data = append([]byte(nil), encoded[5:]...)
	case eapSuccess, eapFailure:
		if len(encoded) != 4 {
			return eapPacket{}, errors.New("ike: EAP success/failure has trailing data")
		}
	default:
		return eapPacket{}, fmt.Errorf("ike: unsupported EAP code %d", packet.Code)
	}
	return packet, nil
}

func marshalEAPPacket(packet eapPacket) ([]byte, error) {
	length := 4
	if packet.Code == eapRequest || packet.Code == eapResponse {
		if packet.Type == 0 {
			return nil, errors.New("ike: typed EAP packet has no type")
		}
		length += 1 + len(packet.Data)
	} else if len(packet.Data) != 0 || packet.Type != 0 {
		return nil, errors.New("ike: EAP success/failure cannot carry type data")
	}
	if length > 65535 {
		return nil, errors.New("ike: EAP packet exceeds 65535 bytes")
	}
	encoded := make([]byte, length)
	encoded[0] = packet.Code
	encoded[1] = packet.Identifier
	binary.BigEndian.PutUint16(encoded[2:4], uint16(length))
	if length > 4 {
		encoded[4] = packet.Type
		copy(encoded[5:], packet.Data)
	}
	return encoded, nil
}

type akaAttribute struct {
	Type   uint8
	Raw    []byte
	Offset int
}

func parseAKAAttributes(encoded []byte) ([]akaAttribute, error) {
	var result []akaAttribute
	for offset := 0; offset < len(encoded); {
		if len(result) >= 64 || offset+2 > len(encoded) {
			return nil, errors.New("ike: malformed EAP-AKA attribute list")
		}
		length := int(encoded[offset+1]) * 4
		if length < 4 || offset+length > len(encoded) {
			return nil, fmt.Errorf("ike: EAP-AKA attribute %d has invalid length %d", encoded[offset], length)
		}
		result = append(result, akaAttribute{
			Type:   encoded[offset],
			Raw:    append([]byte(nil), encoded[offset:offset+length]...),
			Offset: offset,
		})
		offset += length
	}
	return result, nil
}

func oneAKAAttribute(attributes []akaAttribute, kind uint8) (akaAttribute, error) {
	var result akaAttribute
	count := 0
	for _, attribute := range attributes {
		if attribute.Type == kind {
			result = attribute
			count++
		}
	}
	if count != 1 {
		return akaAttribute{}, fmt.Errorf("ike: EAP-AKA expected one attribute %d, got %d", kind, count)
	}
	return result, nil
}

func marshalAKAAttribute(kind uint8, value []byte) ([]byte, error) {
	length := 2 + len(value)
	padded := (length + 3) &^ 3
	if padded/4 > 255 {
		return nil, errors.New("ike: EAP-AKA attribute is too long")
	}
	encoded := make([]byte, padded)
	encoded[0] = kind
	encoded[1] = uint8(padded / 4)
	copy(encoded[2:], value)
	return encoded, nil
}

type akaKeys struct {
	KEncr []byte
	KAut  []byte
	KRe   []byte
	MSK   []byte
	EMSK  []byte
}

func deriveAKAKeys(identity, ik, ck []byte) (akaKeys, error) {
	if len(identity) == 0 {
		return akaKeys{}, errors.New("ike: EAP-AKA identity is empty")
	}
	if len(ik) != 16 || len(ck) != 16 {
		return akaKeys{}, fmt.Errorf("ike: EAP-AKA requires 16-byte IK and CK, got %d and %d", len(ik), len(ck))
	}
	material := make([]byte, 0, len(identity)+32)
	material = append(material, identity...)
	material = append(material, ik...)
	material = append(material, ck...)
	masterKey := sha1.Sum(material)
	stream := fips1862PRF(masterKey[:], 160)
	return akaKeys{
		KEncr: append([]byte(nil), stream[0:16]...),
		KAut:  append([]byte(nil), stream[16:32]...),
		MSK:   append([]byte(nil), stream[32:96]...),
		EMSK:  append([]byte(nil), stream[96:160]...),
	}, nil
}

// deriveAKAPrimeKeys implements the 4G EAP-AKA' KDF from 3GPP TS 33.402
// and RFC 9048 Sections 3.3 and 3.4. The first six AUTN octets carry
// SQN xor AK and the high bit of the first AMF octet is the separation bit.
func deriveAKAPrimeKeys(identity, ik, ck, autn, networkName []byte) (akaKeys, error) {
	if len(identity) == 0 {
		return akaKeys{}, errors.New("ike: EAP-AKA' identity is empty")
	}
	if len(ik) != 16 || len(ck) != 16 {
		return akaKeys{}, fmt.Errorf("ike: EAP-AKA' requires 16-byte IK and CK, got %d and %d", len(ik), len(ck))
	}
	if len(autn) != 16 {
		return akaKeys{}, fmt.Errorf("ike: EAP-AKA' requires a 16-byte AUTN, got %d", len(autn))
	}
	if autn[6]&0x80 == 0 {
		return akaKeys{}, errors.New("ike: EAP-AKA' AUTN has no AMF separation bit")
	}
	if len(networkName) == 0 || len(networkName) > 65535 || !utf8.Valid(networkName) {
		return akaKeys{}, errors.New("ike: EAP-AKA' network name is empty or invalid UTF-8")
	}

	baseKey := make([]byte, 0, 32)
	baseKey = append(baseKey, ck...)
	baseKey = append(baseKey, ik...)
	input := make([]byte, 0, 1+len(networkName)+2+6+2)
	input = append(input, 0x20)
	input = append(input, networkName...)
	input = binary.BigEndian.AppendUint16(input, uint16(len(networkName)))
	input = append(input, autn[:6]...)
	input = binary.BigEndian.AppendUint16(input, 6)
	prime := hmac.New(sha256.New, baseKey)
	_, _ = prime.Write(input)
	ckIKPrime := prime.Sum(nil)

	prfKey := make([]byte, 0, 32)
	prfKey = append(prfKey, ckIKPrime[16:32]...) // IK'
	prfKey = append(prfKey, ckIKPrime[0:16]...)  // CK'
	seed := append([]byte("EAP-AKA'"), identity...)
	stream := akaPrimePRF(prfKey, seed, 208)
	return akaKeys{
		KEncr: append([]byte(nil), stream[0:16]...),
		KAut:  append([]byte(nil), stream[16:48]...),
		KRe:   append([]byte(nil), stream[48:80]...),
		MSK:   append([]byte(nil), stream[80:144]...),
		EMSK:  append([]byte(nil), stream[144:208]...),
	}, nil
}

func akaPrimePRF(key, seed []byte, length int) []byte {
	result := make([]byte, 0, length)
	var previous []byte
	for counter := byte(1); len(result) < length; counter++ {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(previous)
		_, _ = mac.Write(seed)
		_, _ = mac.Write([]byte{counter})
		previous = mac.Sum(nil)
		result = append(result, previous...)
	}
	return result[:length]
}

func fips1862PRF(seed []byte, length int) []byte {
	xkey := new(big.Int).SetBytes(seed)
	modulus := new(big.Int).Lsh(big.NewInt(1), 160)
	result := make([]byte, 0, length)
	for len(result) < length {
		xval := xkey.FillBytes(make([]byte, 20))
		word := fipsSHA1G(xval)
		result = append(result, word[:]...)
		increment := new(big.Int).SetBytes(word[:])
		xkey.Add(xkey, increment)
		xkey.Add(xkey, big.NewInt(1))
		xkey.Mod(xkey, modulus)
	}
	return result[:length]
}

// fipsSHA1G is the SHA-1 compression function G(t, XVAL) from FIPS 186-2.
// Unlike ordinary SHA-1, the 160-bit XVAL is zero-filled to one compression
// block and is not followed by SHA-1 message padding.
func fipsSHA1G(xval []byte) [20]byte {
	var words [80]uint32
	var block [64]byte
	copy(block[:20], xval)
	for index := 0; index < 16; index++ {
		words[index] = binary.BigEndian.Uint32(block[index*4 : index*4+4])
	}
	for index := 16; index < 80; index++ {
		value := words[index-3] ^ words[index-8] ^ words[index-14] ^ words[index-16]
		words[index] = value<<1 | value>>31
	}
	a := uint32(0x67452301)
	b := uint32(0xEFCDAB89)
	c := uint32(0x98BADCFE)
	d := uint32(0x10325476)
	e := uint32(0xC3D2E1F0)
	initialA, initialB, initialC, initialD, initialE := a, b, c, d, e
	for index := 0; index < 80; index++ {
		var function, constant uint32
		switch {
		case index < 20:
			function = (b & c) | (^b & d)
			constant = 0x5A827999
		case index < 40:
			function = b ^ c ^ d
			constant = 0x6ED9EBA1
		case index < 60:
			function = (b & c) | (b & d) | (c & d)
			constant = 0x8F1BBCDC
		default:
			function = b ^ c ^ d
			constant = 0xCA62C1D6
		}
		rotatedA := a<<5 | a>>27
		next := rotatedA + function + e + constant + words[index]
		e = d
		d = c
		c = b<<30 | b>>2
		b = a
		a = next
	}
	values := [5]uint32{initialA + a, initialB + b, initialC + c, initialD + d, initialE + e}
	var result [20]byte
	for index, value := range values {
		binary.BigEndian.PutUint32(result[index*4:index*4+4], value)
	}
	return result
}

func permanentAKAIdentity(identity vowifi.SIMIdentity) ([]byte, error) {
	return permanentAKAIdentityForType(identity, eapTypeAKA)
}

func permanentAKAIdentityForType(identity vowifi.SIMIdentity, method uint8) ([]byte, error) {
	imsi := strings.TrimSpace(identity.IMSI)
	if len(imsi) < 5 || len(imsi) > 16 {
		return nil, errors.New("ike: IMSI length is invalid for EAP-AKA")
	}
	for _, digit := range imsi {
		if digit < '0' || digit > '9' {
			return nil, errors.New("ike: IMSI contains a non-digit")
		}
	}
	mcc := strings.TrimSpace(identity.HomeMCC)
	mnc := strings.TrimSpace(identity.HomeMNC)
	if len(mcc) != 3 || (len(mnc) != 2 && len(mnc) != 3) {
		return nil, errors.New("ike: explicit home MCC/MNC is required for EAP-AKA")
	}
	for len(mnc) < 3 {
		mnc = "0" + mnc
	}
	prefix := "0"
	if method == eapTypeAKAPrime {
		prefix = "6"
	} else if method != eapTypeAKA {
		return nil, fmt.Errorf("ike: unsupported EAP-AKA method type %d", method)
	}
	return []byte(fmt.Sprintf("%s%s@nai.epc.mnc%s.mcc%s.3gppnetwork.org", prefix, imsi, mnc, mcc)), nil
}

type eapAction struct {
	Response []byte
	Success  bool
}

type akaClient struct {
	identity          []byte
	simIdentity       vowifi.SIMIdentity
	provider          vowifi.AKAProvider
	method            uint8
	methodStarted     bool
	terminalFailure   bool
	failureExpected   bool
	notificationSeen  bool
	lastResponseID    uint8
	hasLastResponseID bool
	keys              akaKeys
	challengeComplete bool
	resultIndication  bool
	protectedSuccess  bool
	kdfPendingOffer   []uint16
	kdfAcceptedOffer  []uint16
	kdfNetworkName    []byte
	lastRequest       []byte
	lastAction        eapAction
	identityRounds    int
	identitySeen      map[uint8]bool
}

func newAKAClient(identity vowifi.SIMIdentity, provider vowifi.AKAProvider) (*akaClient, error) {
	return newAKAClientWithMethod(identity, provider, "aka")
}

func newAKAClientWithMethod(identity vowifi.SIMIdentity, provider vowifi.AKAProvider, methodName string) (*akaClient, error) {
	if provider == nil {
		return nil, errors.New("ike: AKA provider is required")
	}
	method := uint8(eapTypeAKA)
	switch strings.ToLower(strings.TrimSpace(methodName)) {
	case "", "aka":
	case "aka-prime", "aka'":
		method = eapTypeAKAPrime
	default:
		return nil, fmt.Errorf("ike: unsupported EAP method %q", methodName)
	}
	nai, err := permanentAKAIdentityForType(identity, method)
	if err != nil {
		return nil, err
	}
	return &akaClient{identity: nai, simIdentity: identity, provider: provider, method: method}, nil
}

func (client *akaClient) handle(ctx context.Context, encoded []byte) (eapAction, error) {
	if len(client.lastRequest) != 0 && bytes.Equal(client.lastRequest, encoded) {
		return eapAction{
			Response: append([]byte(nil), client.lastAction.Response...),
			Success:  client.lastAction.Success,
		}, nil
	}
	packet, err := parseEAPPacket(encoded)
	if err != nil {
		return eapAction{}, err
	}
	if packet.Code == eapRequest && len(client.lastRequest) >= 2 && packet.Identifier == client.lastRequest[1] {
		// A repeated Identifier is only a retransmission when the complete
		// Request is identical. Reusing it with different content is a method
		// failure and must never cause a second SIM operation.
		client.terminalFailure = true
		return eapAction{}, nil
	}
	switch packet.Code {
	case eapFailure:
		if !client.hasLastResponseID || packet.Identifier != client.lastResponseID {
			// RFC 3748 Section 4.2 requires matching the last Response and
			// requires a mismatched Failure to be silently discarded.
			return eapAction{}, nil
		}
		if !client.failureExpected {
			// RFC 4187 Section 6.3.3 only permits EAP-Failure after the peer
			// sent Client-Error, Authentication-Reject, or acknowledged a
			// failure Notification. All other Failures are silently discarded.
			return eapAction{}, nil
		}
		client.failureExpected = false
		client.terminalFailure = true
		stage := "before the SIM AKA challenge (identity or subscription rejected)"
		if client.challengeComplete {
			stage = "after the SIM AKA response (AKA result or subscription rejected)"
		}
		return eapAction{}, fmt.Errorf("%w %s", vowifi.ErrEAPAuthenticationRejected, stage)
	case eapSuccess:
		if client.terminalFailure {
			return eapAction{}, fmt.Errorf("%w: EAP success received after terminal authentication failure", vowifi.ErrEAPAuthenticationRejected)
		}
		if !client.hasLastResponseID || packet.Identifier != client.lastResponseID {
			return eapAction{}, nil
		}
		if !client.challengeComplete {
			return eapAction{}, errors.New("ike: EAP success arrived before an authenticated AKA challenge")
		}
		if client.resultIndication && !client.protectedSuccess {
			return eapAction{}, errors.New("ike: unprotected EAP success received after AT_RESULT_IND")
		}
		return eapAction{Success: true}, nil
	case eapRequest:
		if client.terminalFailure {
			return eapAction{}, fmt.Errorf("%w: EAP request received after terminal authentication failure", vowifi.ErrEAPAuthenticationRejected)
		}
	default:
		return eapAction{}, fmt.Errorf("ike: unexpected EAP code %d from responder", packet.Code)
	}
	var action eapAction
	switch packet.Type {
	case eapTypeIdentity:
		if client.methodStarted {
			// RFC 3748 Section 2.1 does not permit identity re-query after a
			// specific authentication method has started.
			return eapAction{}, nil
		}
		response, err := marshalEAPPacket(eapPacket{
			Code:       eapResponse,
			Identifier: packet.Identifier,
			Type:       eapTypeIdentity,
			Data:       client.identity,
		})
		action = eapAction{Response: response}
		if err != nil {
			return eapAction{}, err
		}
	case eapTypeNotification:
		response, err := marshalEAPPacket(eapPacket{
			Code:       eapResponse,
			Identifier: packet.Identifier,
			Type:       eapTypeNotification,
		})
		if err != nil {
			return eapAction{}, err
		}
		action = eapAction{Response: response}
	case client.method:
		action, err = client.handleAKARequest(ctx, packet)
		if err != nil {
			return action, err
		}
		if len(action.Response) != 0 {
			client.methodStarted = true
		}
	default:
		if client.methodStarted {
			// RFC 3748 Section 2.1 forbids changing methods after the peer has
			// sent a non-Nak response. Silently discard such a request.
			return eapAction{}, nil
		}
		if packet.Type < 4 {
			return eapAction{}, fmt.Errorf("ike: responder requested unsupported EAP type %d", packet.Type)
		}
		response, err := marshalEAPPacket(eapPacket{
			Code:       eapResponse,
			Identifier: packet.Identifier,
			Type:       eapTypeNak,
			Data:       []byte{client.method},
		})
		if err != nil {
			return eapAction{}, err
		}
		action = eapAction{Response: response}
	}
	if len(action.Response) != 0 {
		client.lastResponseID = packet.Identifier
		client.hasLastResponseID = true
		client.lastRequest = append(client.lastRequest[:0], encoded...)
		client.lastAction = eapAction{Response: append([]byte(nil), action.Response...), Success: action.Success}
	}
	return action, nil
}

func (client *akaClient) handleAKARequest(ctx context.Context, packet eapPacket) (eapAction, error) {
	if len(packet.Data) < 3 {
		return client.clientErrorResponse(packet.Identifier)
	}
	subtype := packet.Data[0]
	if packet.Data[1] != 0 || packet.Data[2] != 0 {
		return client.clientErrorResponse(packet.Identifier)
	}
	attributes, err := parseAKAAttributes(packet.Data[3:])
	if err != nil {
		return client.clientErrorResponse(packet.Identifier)
	}
	if client.challengeComplete && subtype != akaSubtypeNotification {
		return client.clientErrorResponse(packet.Identifier)
	}
	switch subtype {
	case akaSubtypeIdentity:
		action, err := client.respondAKAIdentity(packet.Identifier, attributes)
		if err != nil {
			return client.clientErrorResponse(packet.Identifier)
		}
		return action, nil
	case akaSubtypeChallenge:
		action, err := client.respondAKAChallenge(ctx, packet.Identifier, attributes, packet)
		if err != nil && !errors.Is(err, errAKAProvider) &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return client.clientErrorResponse(packet.Identifier)
		}
		return action, err
	case akaSubtypeNotification:
		action, err := client.respondAKANotification(packet.Identifier, attributes, packet)
		if err != nil {
			return client.clientErrorResponse(packet.Identifier)
		}
		return action, nil
	case akaSubtypeReauth:
		return client.clientErrorResponse(packet.Identifier)
	default:
		return client.clientErrorResponse(packet.Identifier)
	}
}

func (client *akaClient) respondAKAIdentity(identifier uint8, attributes []akaAttribute) (eapAction, error) {
	requests := 0
	requestKind := uint8(0)
	for _, attribute := range attributes {
		switch attribute.Type {
		case akaAttrPermanentIDReq, akaAttrAnyIDReq, akaAttrFullAuthIDReq:
			if len(attribute.Raw) != 4 {
				return eapAction{}, errors.New("ike: malformed EAP-AKA identity request attribute")
			}
			requests++
			requestKind = attribute.Type
		default:
			if attribute.Type < 128 {
				return eapAction{}, fmt.Errorf("ike: unsupported mandatory EAP-AKA identity attribute %d", attribute.Type)
			}
		}
	}
	if requests != 1 {
		return eapAction{}, errors.New("ike: EAP-AKA identity request must contain exactly one request attribute")
	}
	if client.identityRounds >= 3 {
		return eapAction{}, errors.New("ike: EAP-AKA identity exchange exceeded three rounds")
	}
	if client.identitySeen == nil {
		client.identitySeen = make(map[uint8]bool, 3)
	}
	if client.identitySeen[requestKind] {
		return eapAction{}, errors.New("ike: EAP-AKA repeated the same identity request type")
	}
	if requestKind == akaAttrAnyIDReq && client.identityRounds != 0 {
		return eapAction{}, errors.New("ike: AT_ANY_ID_REQ is only valid in the first identity round")
	}
	if requestKind == akaAttrFullAuthIDReq && client.identitySeen[akaAttrPermanentIDReq] {
		return eapAction{}, errors.New("ike: AT_FULLAUTH_ID_REQ cannot follow AT_PERMANENT_ID_REQ")
	}
	client.identityRounds++
	client.identitySeen[requestKind] = true
	identityAttribute, err := marshalAKAAttribute(akaAttrIdentity, append([]byte{byte(len(client.identity) >> 8), byte(len(client.identity))}, client.identity...))
	if err != nil {
		return eapAction{}, err
	}
	data := append([]byte{akaSubtypeIdentity, 0, 0}, identityAttribute...)
	response, err := marshalEAPPacket(eapPacket{
		Code:       eapResponse,
		Identifier: identifier,
		Type:       client.method,
		Data:       data,
	})
	return eapAction{Response: response}, err
}

func (client *akaClient) respondAKAChallenge(
	ctx context.Context,
	identifier uint8,
	attributes []akaAttribute,
	request eapPacket,
) (eapAction, error) {
	networkName, kdfAttributes, negotiation, err := client.prepareAKAPrimeKDF(identifier, attributes)
	if errors.Is(err, errAKAPrimeKDFUnsupported) || errors.Is(err, errAKAPrimeAuthenticationReject) {
		return client.authenticationRejectResponse(identifier)
	}
	if err != nil {
		return eapAction{}, err
	}
	if negotiation != nil {
		return *negotiation, nil
	}
	for _, attribute := range attributes {
		switch attribute.Type {
		case akaAttrRAND, akaAttrAUTN, akaAttrMAC, akaAttrResultInd:
		case akaAttrKDF, akaAttrKDFInput:
			if client.method != eapTypeAKAPrime {
				return eapAction{}, fmt.Errorf("ike: EAP-AKA challenge contains AKA' attribute %d", attribute.Type)
			}
		default:
			if attribute.Type < 128 {
				return eapAction{}, fmt.Errorf("ike: unknown mandatory EAP-AKA challenge attribute %d", attribute.Type)
			}
		}
	}
	randAttribute, err := oneAKAAttribute(attributes, akaAttrRAND)
	if err != nil {
		return eapAction{}, err
	}
	autnAttribute, err := oneAKAAttribute(attributes, akaAttrAUTN)
	if err != nil {
		return eapAction{}, err
	}
	macAttribute, err := oneAKAAttribute(attributes, akaAttrMAC)
	if err != nil {
		return eapAction{}, err
	}
	if len(randAttribute.Raw) != 20 || len(autnAttribute.Raw) != 20 || len(macAttribute.Raw) != 20 {
		return eapAction{}, errors.New("ike: EAP-AKA RAND, AUTN, or MAC has an invalid length")
	}
	var challenge vowifi.AKAChallenge
	copy(challenge.RAND[:], randAttribute.Raw[4:20])
	copy(challenge.AUTN[:], autnAttribute.Raw[4:20])
	if client.method == eapTypeAKAPrime && challenge.AUTN[6]&0x80 == 0 {
		return client.authenticationRejectResponse(identifier)
	}
	result, err := client.provider.Authenticate(ctx, client.simIdentity, challenge)
	if err != nil {
		if errors.Is(err, vowifi.ErrEC20AKAMACFailure) {
			return client.authenticationRejectResponse(identifier)
		}
		return eapAction{}, errors.Join(errAKAProvider, fmt.Errorf("ike: SIM AKA authentication: %w", err))
	}
	if result.SynchronizationFailure {
		if len(result.AUTS) != 14 {
			return eapAction{}, errors.New("ike: SIM reported synchronization failure without a 14-byte AUTS")
		}
		autsAttribute, err := marshalAKAAttribute(akaAttrAUTS, result.AUTS)
		if err != nil {
			return eapAction{}, err
		}
		data := append([]byte{akaSubtypeSyncFailure, 0, 0}, autsAttribute...)
		if client.method == eapTypeAKAPrime {
			for _, attribute := range kdfAttributes {
				data = append(data, attribute.Raw...)
			}
		}
		response, err := marshalEAPPacket(eapPacket{Code: eapResponse, Identifier: identifier, Type: client.method, Data: data})
		return eapAction{Response: response}, err
	}
	if len(result.RES) < 4 || len(result.RES) > 16 {
		return eapAction{}, fmt.Errorf("ike: SIM returned invalid RES length %d", len(result.RES))
	}
	var keys akaKeys
	if client.method == eapTypeAKAPrime {
		keys, err = deriveAKAPrimeKeys(client.identity, result.IK, result.CK, challenge.AUTN[:], networkName)
	} else {
		keys, err = deriveAKAKeys(client.identity, result.IK, result.CK)
	}
	if err != nil {
		return eapAction{}, err
	}
	requestBytes, err := marshalEAPPacket(request)
	if err != nil {
		return eapAction{}, err
	}
	zeroed := append([]byte(nil), requestBytes...)
	macOffset := 5 + 3 + macAttribute.Offset
	if macOffset+20 > len(zeroed) {
		return eapAction{}, errors.New("ike: EAP-AKA MAC offset is invalid")
	}
	for index := macOffset + 4; index < macOffset+20; index++ {
		zeroed[index] = 0
	}
	expectedMAC := client.packetMAC(keys.KAut, zeroed)
	if subtle.ConstantTimeCompare(expectedMAC, macAttribute.Raw[4:20]) != 1 {
		return eapAction{}, errors.New("ike: EAP-AKA server MAC is invalid")
	}

	resValue := make([]byte, 2+len(result.RES))
	binary.BigEndian.PutUint16(resValue[0:2], uint16(len(result.RES)*8))
	copy(resValue[2:], result.RES)
	resAttribute, err := marshalAKAAttribute(akaAttrRES, resValue)
	if err != nil {
		return eapAction{}, err
	}
	macResponse, _ := marshalAKAAttribute(akaAttrMAC, make([]byte, 18))
	responseData := append([]byte{akaSubtypeChallenge, 0, 0}, resAttribute...)
	resultIndication := false
	for _, attribute := range attributes {
		if attribute.Type == akaAttrResultInd {
			if len(attribute.Raw) != 4 {
				return eapAction{}, errors.New("ike: malformed AT_RESULT_IND")
			}
			responseData = append(responseData, attribute.Raw...)
			resultIndication = true
		}
	}
	responseData = append(responseData, macResponse...)
	responseBytes, err := marshalEAPPacket(eapPacket{
		Code:       eapResponse,
		Identifier: identifier,
		Type:       client.method,
		Data:       responseData,
	})
	if err != nil {
		return eapAction{}, err
	}
	responseAttributes, _ := parseAKAAttributes(responseData[3:])
	responseMAC, err := oneAKAAttribute(responseAttributes, akaAttrMAC)
	if err != nil {
		return eapAction{}, err
	}
	responseMACOffset := 5 + 3 + responseMAC.Offset
	computed := client.packetMAC(keys.KAut, responseBytes)
	copy(responseBytes[responseMACOffset+4:responseMACOffset+20], computed)
	client.keys = keys
	client.challengeComplete = true
	client.resultIndication = resultIndication
	return eapAction{Response: responseBytes}, nil
}

func (client *akaClient) prepareAKAPrimeKDF(
	identifier uint8,
	attributes []akaAttribute,
) ([]byte, []akaAttribute, *eapAction, error) {
	if client.method != eapTypeAKAPrime {
		return nil, nil, nil, nil
	}
	var kdfAttributes []akaAttribute
	var offered []uint16
	for _, attribute := range attributes {
		if attribute.Type != akaAttrKDF {
			continue
		}
		if len(attribute.Raw) != 4 {
			return nil, nil, nil, errors.New("ike: malformed EAP-AKA' AT_KDF")
		}
		kdfAttributes = append(kdfAttributes, attribute)
		offered = append(offered, binary.BigEndian.Uint16(attribute.Raw[2:4]))
	}
	if len(offered) == 0 {
		return nil, nil, nil, errAKAPrimeAuthenticationReject
	}
	var seen map[uint16]struct{}
	if client.kdfAcceptedOffer == nil && client.kdfPendingOffer == nil {
		seen = make(map[uint16]struct{}, len(offered))
		for _, value := range offered {
			if _, duplicate := seen[value]; duplicate {
				return nil, nil, nil, errors.New("ike: EAP-AKA' server sent duplicate AT_KDF values")
			}
			seen[value] = struct{}{}
		}
		if _, supported := seen[akaPrimeKDF]; !supported {
			return nil, nil, nil, errAKAPrimeKDFUnsupported
		}
		if offered[0] != akaPrimeKDF {
			client.kdfPendingOffer = append([]uint16(nil), offered...)
			selected, err := marshalAKAAttribute(akaAttrKDF, []byte{0, akaPrimeKDF})
			if err != nil {
				return nil, nil, nil, err
			}
			response, err := marshalEAPPacket(eapPacket{
				Code:       eapResponse,
				Identifier: identifier,
				Type:       eapTypeAKAPrime,
				Data:       append([]byte{akaSubtypeChallenge, 0, 0}, selected...),
			})
			if err != nil {
				return nil, nil, nil, err
			}
			action := &eapAction{Response: response}
			return nil, nil, action, nil
		}
	}
	input, err := oneAKAAttribute(attributes, akaAttrKDFInput)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(input.Raw) < 4 {
		return nil, nil, nil, errors.New("ike: malformed EAP-AKA' AT_KDF_INPUT")
	}
	networkLength := int(binary.BigEndian.Uint16(input.Raw[2:4]))
	if networkLength == 0 {
		return nil, nil, nil, errAKAPrimeAuthenticationReject
	}
	if 4+networkLength > len(input.Raw) {
		return nil, nil, nil, errors.New("ike: EAP-AKA' AT_KDF_INPUT has an invalid network name length")
	}
	if paddingLength := len(input.Raw) - 4 - networkLength; paddingLength > 3 {
		return nil, nil, nil, errors.New("ike: EAP-AKA' AT_KDF_INPUT has excessive padding")
	}
	for _, padding := range input.Raw[4+networkLength:] {
		if padding != 0 {
			return nil, nil, nil, errors.New("ike: EAP-AKA' AT_KDF_INPUT has non-zero padding")
		}
	}
	networkName := append([]byte(nil), input.Raw[4:4+networkLength]...)
	if !utf8.Valid(networkName) {
		return nil, nil, nil, errors.New("ike: EAP-AKA' network name is invalid UTF-8")
	}

	if client.kdfAcceptedOffer != nil {
		if !equalKDFOffer(offered, client.kdfAcceptedOffer) || !bytes.Equal(networkName, client.kdfNetworkName) {
			return nil, nil, nil, errors.New("ike: EAP-AKA' server changed the negotiated KDF or network name")
		}
		return networkName, kdfAttributes, nil, nil
	}
	if client.kdfPendingOffer != nil {
		expected := append([]uint16{akaPrimeKDF}, client.kdfPendingOffer...)
		if !equalKDFOffer(offered, expected) {
			return nil, nil, nil, errors.New("ike: EAP-AKA' server changed more than the negotiated KDF")
		}
		client.kdfPendingOffer = nil
		client.kdfAcceptedOffer = append([]uint16(nil), expected...)
		client.kdfNetworkName = append([]byte(nil), networkName...)
		return networkName, kdfAttributes, nil, nil
	}
	if offered[0] == akaPrimeKDF {
		client.kdfAcceptedOffer = append([]uint16(nil), offered...)
		client.kdfNetworkName = append([]byte(nil), networkName...)
		return networkName, kdfAttributes, nil, nil
	}
	return nil, nil, nil, errors.New("ike: EAP-AKA' reached an invalid KDF negotiation state")
}

func equalKDFOffer(left, right []uint16) bool {
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

func (client *akaClient) respondAKANotification(
	identifier uint8,
	attributes []akaAttribute,
	request eapPacket,
) (eapAction, error) {
	if client.notificationSeen {
		return eapAction{}, errors.New("ike: EAP-AKA authentication received more than one notification round")
	}
	notification, err := oneAKAAttribute(attributes, akaAttrNotification)
	if err != nil {
		return eapAction{}, err
	}
	if len(notification.Raw) != 4 {
		return eapAction{}, errors.New("ike: malformed EAP-AKA notification")
	}
	code := binary.BigEndian.Uint16(notification.Raw[2:4])
	preAuthentication := code&0x4000 != 0
	success := code&0x8000 != 0
	if preAuthentication {
		if success || client.challengeComplete {
			return eapAction{}, errors.New("ike: EAP-AKA pre-authentication notification is invalid in the current phase")
		}
	} else if !client.challengeComplete {
		return eapAction{}, errors.New("ike: EAP-AKA post-authentication notification arrived before the challenge")
	}
	macCount := 0
	for _, attribute := range attributes {
		switch attribute.Type {
		case akaAttrNotification:
		case akaAttrMAC:
			macCount++
		default:
			if attribute.Type < 128 {
				return eapAction{}, fmt.Errorf("ike: unsupported mandatory EAP-AKA notification attribute %d", attribute.Type)
			}
		}
	}
	if preAuthentication && macCount != 0 {
		return eapAction{}, errors.New("ike: pre-authentication EAP-AKA notification must not contain AT_MAC")
	}
	if !preAuthentication && macCount != 1 {
		return eapAction{}, errors.New("ike: post-authentication EAP-AKA notification requires exactly one AT_MAC")
	}
	if code != 32768 {
		responseData := []byte{akaSubtypeNotification, 0, 0}
		if !preAuthentication {
			macAttribute, err := oneAKAAttribute(attributes, akaAttrMAC)
			if err != nil || len(macAttribute.Raw) != 20 {
				return client.clientErrorResponse(identifier)
			}
			requestBytes, err := marshalEAPPacket(request)
			if err != nil {
				return eapAction{}, err
			}
			zeroed := append([]byte(nil), requestBytes...)
			macOffset := 5 + 3 + macAttribute.Offset
			for index := macOffset + 4; index < macOffset+20; index++ {
				zeroed[index] = 0
			}
			if subtle.ConstantTimeCompare(client.packetMAC(client.keys.KAut, zeroed), macAttribute.Raw[4:20]) != 1 {
				return client.clientErrorResponse(identifier)
			}
			responseMAC, _ := marshalAKAAttribute(akaAttrMAC, make([]byte, 18))
			responseData = append(responseData, responseMAC...)
		}
		responseBytes, err := marshalEAPPacket(eapPacket{
			Code: eapResponse, Identifier: identifier, Type: client.method, Data: responseData,
		})
		if err != nil {
			return eapAction{}, err
		}
		if !preAuthentication {
			responseAttributes, _ := parseAKAAttributes(responseData[3:])
			responseMAC, _ := oneAKAAttribute(responseAttributes, akaAttrMAC)
			offset := 5 + 3 + responseMAC.Offset
			copy(responseBytes[offset+4:offset+20], client.packetMAC(client.keys.KAut, responseBytes))
		}
		client.notificationSeen = true
		client.terminalFailure = !success
		client.failureExpected = !success
		return eapAction{Response: responseBytes}, nil
	}
	if !client.challengeComplete || !client.resultIndication {
		return eapAction{}, errors.New("ike: unexpected protected EAP-AKA success notification")
	}
	macAttribute, err := oneAKAAttribute(attributes, akaAttrMAC)
	if err != nil {
		return eapAction{}, err
	}
	if len(macAttribute.Raw) != 20 {
		return eapAction{}, errors.New("ike: malformed notification AT_MAC")
	}
	requestBytes, err := marshalEAPPacket(request)
	if err != nil {
		return eapAction{}, err
	}
	zeroed := append([]byte(nil), requestBytes...)
	macOffset := 5 + 3 + macAttribute.Offset
	for index := macOffset + 4; index < macOffset+20; index++ {
		zeroed[index] = 0
	}
	if subtle.ConstantTimeCompare(client.packetMAC(client.keys.KAut, zeroed), macAttribute.Raw[4:20]) != 1 {
		return eapAction{}, errors.New("ike: EAP-AKA protected success MAC is invalid")
	}
	responseMAC, _ := marshalAKAAttribute(akaAttrMAC, make([]byte, 18))
	responseData := append([]byte{akaSubtypeNotification, 0, 0}, responseMAC...)
	responseBytes, err := marshalEAPPacket(eapPacket{
		Code:       eapResponse,
		Identifier: identifier,
		Type:       client.method,
		Data:       responseData,
	})
	if err != nil {
		return eapAction{}, err
	}
	responseAttributes, _ := parseAKAAttributes(responseData[3:])
	responseMACAttribute, _ := oneAKAAttribute(responseAttributes, akaAttrMAC)
	responseOffset := 5 + 3 + responseMACAttribute.Offset
	copy(responseBytes[responseOffset+4:responseOffset+20], client.packetMAC(client.keys.KAut, responseBytes))
	client.protectedSuccess = true
	client.notificationSeen = true
	return eapAction{Response: responseBytes}, nil
}

func akaMAC(key, packet []byte) []byte {
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(packet)
	return mac.Sum(nil)[:16]
}

func akaPrimeMAC(key, packet []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(packet)
	return mac.Sum(nil)[:16]
}

func (client *akaClient) packetMAC(key, packet []byte) []byte {
	if client.method == eapTypeAKAPrime {
		return akaPrimeMAC(key, packet)
	}
	return akaMAC(key, packet)
}

func (client *akaClient) clientErrorResponse(identifier uint8) (eapAction, error) {
	client.terminalFailure = true
	client.failureExpected = true
	return akaClientErrorResponseForType(identifier, client.method)
}

func (client *akaClient) authenticationRejectResponse(identifier uint8) (eapAction, error) {
	client.terminalFailure = true
	client.failureExpected = true
	return akaAuthenticationRejectResponseForType(identifier, client.method)
}

func akaClientErrorResponse(identifier uint8) (eapAction, error) {
	return akaClientErrorResponseForType(identifier, eapTypeAKA)
}

func akaClientErrorResponseForType(identifier, method uint8) (eapAction, error) {
	attribute, err := marshalAKAAttribute(akaAttrClientError, []byte{0, 0})
	if err != nil {
		return eapAction{}, err
	}
	response, err := marshalEAPPacket(eapPacket{
		Code:       eapResponse,
		Identifier: identifier,
		Type:       method,
		Data:       append([]byte{akaSubtypeClientError, 0, 0}, attribute...),
	})
	return eapAction{Response: response}, err
}

func akaAuthenticationRejectResponse(identifier uint8) (eapAction, error) {
	return akaAuthenticationRejectResponseForType(identifier, eapTypeAKA)
}

func akaAuthenticationRejectResponseForType(identifier, method uint8) (eapAction, error) {
	response, err := marshalEAPPacket(eapPacket{
		Code:       eapResponse,
		Identifier: identifier,
		Type:       method,
		Data:       []byte{akaSubtypeAuthReject, 0, 0},
	})
	return eapAction{Response: response}, err
}
