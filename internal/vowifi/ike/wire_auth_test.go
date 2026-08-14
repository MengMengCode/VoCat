package ike

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"vocat/internal/vowifi"
)

func TestPayloadAndProposalWireRoundTrip(t *testing.T) {
	offered := proposal{
		Number:   1,
		Protocol: protocolIKE,
		Transforms: []transform{
			{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 128},
			{Type: transformPRF, ID: prfHMACSHA1},
			{Type: transformIntegrity, ID: integrityHMACSHA1_96},
			{Type: transformDH, ID: dhMODP1024},
		},
	}
	sa, err := marshalProposals([]proposal{offered})
	if err != nil {
		t.Fatalf("marshalProposals() error = %v", err)
	}
	first, wire, err := marshalPayloadChain([]payload{
		{Type: payloadSA, Body: sa},
		{Type: payloadNonce, Body: bytes.Repeat([]byte{0xaa}, 32)},
	})
	if err != nil {
		t.Fatalf("marshalPayloadChain() error = %v", err)
	}
	if first != payloadSA || len(wire) < 8 || wire[0] != payloadNonce {
		t.Fatalf("unexpected payload chain header: first=%d wire=%x", first, wire[:8])
	}
	decoded, err := parsePayloadChain(first, wire)
	if err != nil {
		t.Fatalf("parsePayloadChain() error = %v", err)
	}
	if len(decoded) != 2 || decoded[0].Type != payloadSA || decoded[1].Type != payloadNonce {
		t.Fatalf("decoded payload chain = %#v", decoded)
	}
	proposals, err := parseProposals(decoded[0].Body)
	if err != nil {
		t.Fatalf("parseProposals() error = %v", err)
	}
	if len(proposals) != 1 || len(proposals[0].Transforms) != 4 ||
		proposals[0].Transforms[0].KeyLength != 128 {
		t.Fatalf("decoded proposal = %#v", proposals)
	}
}

func TestInitialEAPOnlyAuthCarriesAPNIDrAndNotify(t *testing.T) {
	idi := payload{Type: payloadIDi, Body: []byte{3, 0, 0, 0, 'u'}}
	idr := payload{Type: payloadIDr, Body: []byte{2, 0, 0, 0, 'i', 'm', 's'}}
	payloads := buildInitialEAPOnlyAuth(
		idi,
		idr,
		[]byte{1, 2, 3},
		dualStackTrafficSelectors(payloadTSi),
		dualStackTrafficSelectors(payloadTSr),
	)
	if len(payloads) != 9 || payloads[0].Type != payloadIDi || payloads[1].Type != payloadIDr {
		t.Fatalf("initial auth payload order = %#v", payloads)
	}
	if got := string(payloads[1].Body[4:]); got != "ims" || payloads[1].Body[0] != 2 {
		t.Fatalf("IDr = type %d value %q, want ID_FQDN ims", payloads[1].Body[0], got)
	}
	kind, data, err := parseNotify(payloads[2])
	if err != nil {
		t.Fatalf("parseNotify(EAP_ONLY_AUTHENTICATION) error = %v", err)
	}
	if kind != notifyEAPOnlyAuth || len(data) != 0 {
		t.Fatalf("notify = %d/%x, want EAP_ONLY_AUTHENTICATION", kind, data)
	}
	kind, data, err = parseNotify(payloads[3])
	if err != nil {
		t.Fatalf("parseNotify() error = %v", err)
	}
	if kind != notifyMOBIKESupported || len(data) != 0 {
		t.Fatalf("notify = %d/%x, want MOBIKE_SUPPORTED", kind, data)
	}
	kind, data, err = parseNotify(payloads[4])
	if err != nil {
		t.Fatalf("parseNotify() error = %v", err)
	}
	if kind != notifyInitialContact || len(data) != 0 {
		t.Fatalf("notify = %d/%x, want INITIAL_CONTACT", kind, data)
	}
	for _, kind := range []uint8{payloadTSi, payloadTSr} {
		item, err := onePayload(payloads, kind)
		if err != nil {
			t.Fatalf("onePayload(%d) error = %v", kind, err)
		}
		selectors, err := parseTrafficSelectors(item)
		if err != nil {
			t.Fatalf("parseTrafficSelectors(%d) error = %v", kind, err)
		}
		if len(selectors) != 2 || selectors[0].StartIP.To4() == nil || selectors[1].StartIP.To4() != nil {
			t.Fatalf("dual-stack selectors = %#v", selectors)
		}
	}
}

func TestInitialStandardEAPAuthOmitsEAPOnlyNotify(t *testing.T) {
	idi := payload{Type: payloadIDi, Body: []byte{3, 0, 0, 0, 'u'}}
	idr := payload{Type: payloadIDr, Body: []byte{2, 0, 0, 0, 'i', 'm', 's'}}
	payloads := buildInitialEAPAuth(
		idi,
		idr,
		[]byte{1, 2, 3},
		dualStackTrafficSelectors(payloadTSi),
		dualStackTrafficSelectors(payloadTSr),
		false,
	)
	if len(payloads) != 8 || payloads[0].Type != payloadIDi || payloads[1].Type != payloadIDr {
		t.Fatalf("initial standard EAP payload order = %#v", payloads)
	}
	initialContact := 0
	mobikeSupported := 0
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, _, err := parseNotify(item)
		if err != nil {
			t.Fatal(err)
		}
		if kind == notifyEAPOnlyAuth {
			t.Fatal("standard EAP initial request contains EAP_ONLY_AUTHENTICATION")
		}
		if kind == notifyInitialContact {
			initialContact++
		}
		if kind == notifyMOBIKESupported {
			mobikeSupported++
		}
	}
	if initialContact != 1 {
		t.Fatalf("standard EAP initial request INITIAL_CONTACT count = %d, want 1", initialContact)
	}
	if mobikeSupported != 1 {
		t.Fatalf("standard EAP initial request MOBIKE_SUPPORTED count = %d, want 1", mobikeSupported)
	}
	if payloads[2].Type != payloadNotify || payloads[3].Type != payloadNotify {
		t.Fatalf("standard EAP Android notify order = %#v", payloads[:4])
	}
}

func TestConfigurationRequestMatchesAndroidAttributes(t *testing.T) {
	cp := configurationRequest()
	if cp.Type != payloadCP || len(cp.Body) < 4 || cp.Body[0] != configRequest {
		t.Fatalf("configuration request = %#v", cp)
	}
	var attributes []uint16
	for offset := 4; offset < len(cp.Body); {
		if offset+4 > len(cp.Body) {
			t.Fatalf("truncated attribute at %d", offset)
		}
		kind := uint16(cp.Body[offset])<<8 | uint16(cp.Body[offset+1])
		length := int(cp.Body[offset+2])<<8 | int(cp.Body[offset+3])
		if offset+4+length > len(cp.Body) {
			t.Fatalf("attribute %d exceeds payload", kind)
		}
		attributes = append(attributes, kind)
		offset += 4 + length
	}
	want := []uint16{1, 8, 3, 10, 20, 21, configApplicationVersion}
	if !slices.Equal(attributes, want) {
		t.Fatalf("configuration attributes = %v, want %v", attributes, want)
	}
}

func TestO2GermanyUsesStandardEAPAuthentication(t *testing.T) {
	for _, mnc := range []string{"03", "003"} {
		if advertiseEAPOnlyAuthentication("262", mnc) {
			t.Fatalf("O2 Germany 262-%s unexpectedly uses EAP-only", mnc)
		}
	}
	if !advertiseEAPOnlyAuthentication("262", "02") || !advertiseEAPOnlyAuthentication("234", "15") {
		t.Fatal("non-O2 PLMN lost the existing EAP-only policy")
	}
}

func TestResponderIDrValidatorsSeparateEPDGAndAPN(t *testing.T) {
	epdg := payload{
		Type: payloadIDr,
		Body: append([]byte{2, 0, 0, 0}, []byte("epdg.epc.mnc015.mcc234.pub.3gppnetwork.org")...),
	}
	if err := validateFQDNIDr(epdg, "epdg.epc.mnc015.mcc234.pub.3gppnetwork.org", "initial ePDG"); err != nil {
		t.Fatalf("valid initial IDr rejected: %v", err)
	}
	apn := payload{Type: payloadIDr, Body: []byte{2, 0, 0, 0, 'i', 'm', 's'}}
	if err := validateFQDNIDr(apn, "ims", "final APN"); err != nil {
		t.Fatalf("valid final IDr rejected: %v", err)
	}
	wrongType := apn
	wrongType.Body = append([]byte(nil), apn.Body...)
	wrongType.Body[0] = 11
	if err := validateFQDNIDr(wrongType, "ims", "final APN"); err == nil {
		t.Fatal("non-ID_FQDN final IDr was accepted")
	}
	if err := validateFQDNIDr(epdg, "ims", "final APN"); err == nil {
		t.Fatal("ePDG identity was accepted as the APN identity")
	}
}

func TestFinalEAPResponderAUTHValidAndInvalidNeverAllowed(t *testing.T) {
	suite := legacyTestSuite()
	msk := bytes.Repeat([]byte{0x10}, 64)
	initialResponse := bytes.Repeat([]byte{0x20}, 96)
	initiatorNonce := bytes.Repeat([]byte{0x30}, 32)
	skpr := bytes.Repeat([]byte{0x40}, 20)
	idr := payload{Type: payloadIDr, Body: append([]byte{2, 0, 0, 0}, []byte("epdg.example")...)}
	signed, err := responderSignedOctets(initialResponse, initiatorNonce, suite, skpr, idr)
	if err != nil {
		t.Fatalf("responderSignedOctets() error = %v", err)
	}
	padded, _ := prf(suite, msk, []byte("Key Pad for IKEv2"))
	value, _ := prf(suite, padded, signed)
	auth := payload{Type: payloadAuth, Body: append([]byte{authMethodSharedKeyMIC, 0, 0, 0}, value...)}
	if err := verifyEAPResponderAUTH(auth, msk, initialResponse, initiatorNonce, suite, skpr, idr); err != nil {
		t.Fatalf("valid final responder AUTH rejected: %v", err)
	}
	auth.Body[len(auth.Body)-1] ^= 1
	if err := verifyEAPResponderAUTH(auth, msk, initialResponse, initiatorNonce, suite, skpr, idr); err == nil {
		t.Fatal("invalid final responder AUTH was accepted")
	}

	status, _, err := validateInitialResponderAUTH(
		[]payload{idr},
		initialResponse,
		initiatorNonce,
		suite,
		skpr,
		"epdg.example",
		"epdg.example",
		nil,
		nil,
		false,
	)
	if status != vowifi.ResponderAUTHMissing || !errors.Is(err, vowifi.ErrResponderAUTHRequired) {
		t.Fatalf("strict missing initial AUTH = status %q err %v", status, err)
	}
	status, _, err = validateInitialResponderAUTH(
		[]payload{idr},
		initialResponse,
		initiatorNonce,
		suite,
		skpr,
		"epdg.example",
		"epdg.example",
		nil,
		nil,
		true,
	)
	if status != vowifi.ResponderAUTHMissing || err != nil {
		t.Fatalf("EAP-only deferred initial AUTH = status %q err %v", status, err)
	}
}

func TestConfigurationIPv6PrefixIsMandatoryAndPreserved(t *testing.T) {
	ipv6 := bytes.Repeat([]byte{0x20}, 16)
	validBody := []byte{configReply, 0, 0, 0, 0, configInternalIPv6Address, 0, 17}
	validBody = append(validBody, ipv6...)
	validBody = append(validBody, 64)
	configuration, err := parseConfiguration(payload{Type: payloadCP, Body: validBody})
	if err != nil {
		t.Fatalf("parseConfiguration(valid IPv6) error = %v", err)
	}
	if configuration.IPv6Prefix != 64 || !bytes.Equal(configuration.LocalIPv6, ipv6) {
		t.Fatalf("IPv6 configuration = %#v", configuration)
	}
	invalidBody := []byte{configReply, 0, 0, 0, 0, configInternalIPv6Address, 0, 16}
	invalidBody = append(invalidBody, ipv6...)
	if _, err := parseConfiguration(payload{Type: payloadCP, Body: invalidBody}); err == nil {
		t.Fatal("16-byte INTERNAL_IP6_ADDRESS without prefix was accepted")
	}
}
