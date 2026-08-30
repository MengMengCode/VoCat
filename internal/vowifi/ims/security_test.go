package ims

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"vocat/internal/vowifi"
)

func TestParseSecurityAgreementSelectsSupportedIPSec(t *testing.T) {
	proposal := securityProposal{
		spiClient:  1001,
		spiServer:  1002,
		portClient: 40666,
		portServer: 55610,
	}
	selected := "ipsec-3gpp;q=0.100;alg=hmac-sha-1-96;prot=esp;mod=trans;" +
		"ealg=aes-cbc;spi-c=2001;spi-s=2002;port-c=50601;port-s=50600"
	unsupported := "digest;q=0.900"
	agreement, err := parseSecurityAgreement(
		[]string{unsupported + ", " + selected},
		proposal,
	)
	if err != nil {
		t.Fatalf("parseSecurityAgreement() error = %v", err)
	}
	if agreement.selected.spiClient != 2001 ||
		agreement.selected.spiServer != 2002 ||
		agreement.selected.portClient != 50601 ||
		agreement.selected.portServer != 50600 {
		t.Fatalf("selected mechanism = %#v", agreement.selected)
	}
	if agreement.verifyValue != unsupported+", "+selected {
		t.Fatalf("Security-Verify = %q", agreement.verifyValue)
	}
}

func TestSecurityProposalDefaultUsesAESCBC(t *testing.T) {
	identity := vowifi.SIMIdentity{HomeMCC: "999", HomeMNC: "99"}
	if got := securityEncryptionForIdentity(identity); got != "aes-cbc" {
		t.Fatalf("standard security encryption = %q, want aes-cbc", got)
	}
	proposal := securityProposal{
		spiClient: 1001, spiServer: 1002,
		portClient: 40666, portServer: 55610,
		encryption: securityEncryptionForIdentity(identity),
	}
	if got, want := proposal.headerValue(), "ipsec-3gpp;q=1.000;alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=aes-cbc;spi-c=0000001001;spi-s=0000001002;port-c=40666;port-s=55610"; got != want {
		t.Fatalf("Security-Client = %q, want %q", got, want)
	}
}

func TestSecurityAgreementAcceptsO2FallbackEncryption(t *testing.T) {
	proposal := securityProposal{
		spiClient:          1001,
		spiServer:          1002,
		portClient:         5062,
		portServer:         5063,
		encryption:         "null",
		fallbackEncryption: "aes-cbc",
	}
	value := "ipsec-3gpp;q=0.5;alg=hmac-sha-1-96;prot=esp;mod=trans;" +
		"ealg=aes-cbc;spi-c=2001;spi-s=2002;port-c=50601;port-s=50600"
	agreement, err := parseSecurityAgreement([]string{value}, proposal)
	if err != nil {
		t.Fatalf("parseSecurityAgreement(aes-cbc fallback) error = %v", err)
	}
	if got := agreement.selected.encryption; got != "aes-cbc" {
		t.Fatalf("selected encryption = %q, want aes-cbc", got)
	}
}

func TestParseSecurityAgreementSkipsIncompleteCarrierAlternatives(t *testing.T) {
	proposal := securityProposal{
		spiClient:  1001,
		spiServer:  1002,
		portClient: 40666,
		portServer: 55610,
	}
	incomplete := []string{
		"ipsec-3gpp;q=0.100;alg=hmac-md5-96;mod=trans",
		"ipsec-3gpp;q=0.200;alg=hmac-sha-1-96;ealg=des-ede3-cbc;mod=trans",
		"ipsec-3gpp;q=0.300;alg=hmac-sha-1-96;ealg=aes-cbc;mod=trans",
	}
	selected := "ipsec-3gpp;q=1.000;alg=hmac-sha-1-96;prot=esp;mod=trans;" +
		"ealg=aes-cbc;spi-c=2001;spi-s=2002;port-c=50601;port-s=50600"
	values := []string{strings.Join(append(incomplete, selected), ", ")}

	agreement, err := parseSecurityAgreement(values, proposal)
	if err != nil {
		t.Fatalf("parseSecurityAgreement() error = %v", err)
	}
	if agreement.selected.spiClient != 2001 || agreement.selected.spiServer != 2002 {
		t.Fatalf("selected mechanism = %#v", agreement.selected)
	}
}

func TestParseSecurityAgreementFailsClosed(t *testing.T) {
	proposal := securityProposal{
		spiClient:  1001,
		spiServer:  1002,
		portClient: 40666,
		portServer: 55610,
	}
	valid := "ipsec-3gpp;q=0.100;alg=hmac-sha-1-96;prot=esp;mod=trans;" +
		"ealg=aes-cbc;spi-c=2001;spi-s=2002;port-c=50601;port-s=50600"
	for _, test := range []struct {
		name   string
		values []string
	}{
		{
			name: "unsupported integrity algorithm",
			values: []string{
				strings.Replace(valid, "hmac-sha-1-96", "hmac-md5-96", 1),
			},
		},
		{
			name: "server SPI collides with UE SPI",
			values: []string{
				strings.Replace(valid, "spi-c=2001", "spi-c=1001", 1),
			},
		},
		{
			name: "server SPIs collide",
			values: []string{
				strings.Replace(valid, "spi-s=2002", "spi-s=2001", 1),
			},
		},
		{
			name: "malformed ipsec offer poisons otherwise valid list",
			values: []string{
				valid + ", ipsec-3gpp;q=0.200;alg=hmac-sha-1-96;alg=hmac-sha-1-96",
			},
		},
		{
			name: "partially specified SA parameters poison otherwise valid list",
			values: []string{
				valid + ", ipsec-3gpp;q=0.200;alg=hmac-sha-1-96;spi-c=3001",
			},
		},
		{
			name:   "no offer",
			values: nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseSecurityAgreement(test.values, proposal)
			if err == nil {
				t.Fatal("parseSecurityAgreement() error = nil")
			}
		})
	}
}

func TestExpandIPSecKeys(t *testing.T) {
	ck := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	ik := []byte{16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	encryption, integrity, err := expandIPSecKeys(ck, ik, "aes-cbc", "hmac-sha-1-96")
	if err != nil {
		t.Fatalf("expandIPSecKeys() error = %v", err)
	}
	if !reflect.DeepEqual(encryption, ck) {
		t.Fatalf("encryption key = %v, want %v", encryption, ck)
	}
	wantIntegrity := append(append([]byte(nil), ik...), 0, 0, 0, 0)
	if !reflect.DeepEqual(integrity, wantIntegrity) {
		t.Fatalf("integrity key = %v, want %v", integrity, wantIntegrity)
	}
	encryption[0] ^= 0xff
	integrity[0] ^= 0xff
	if ck[0] != 0 || ik[0] != 16 {
		t.Fatal("expanded keys alias AKA key material")
	}
}

func TestExpandIPSecKeysForAndroidAlgorithmSet(t *testing.T) {
	ck := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}
	ik := []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}
	tripleDES, md5Key, err := expandIPSecKeys(ck, ik, "des-ede3-cbc", "hmac-md5-96")
	if err != nil {
		t.Fatal(err)
	}
	if len(tripleDES) != 24 || len(md5Key) != 16 {
		t.Fatalf("3DES/MD5 key lengths = %d/%d", len(tripleDES), len(md5Key))
	}
	for _, value := range tripleDES {
		ones := 0
		for bits := value; bits != 0; bits >>= 1 {
			ones += int(bits & 1)
		}
		if ones%2 != 1 {
			t.Fatalf("3DES byte %02x does not have odd parity", value)
		}
	}
	nullKey, sha1Key, err := expandIPSecKeys(ck, ik, "null", "hmac-sha-1-96")
	if err != nil || len(nullKey) != 0 || len(sha1Key) != 20 {
		t.Fatalf("null/SHA1 keys = %d/%d, %v", len(nullKey), len(sha1Key), err)
	}
}

func TestAndroidIMSProposalOffersAllDefaultAlgorithms(t *testing.T) {
	proposal := securityProposal{
		spiClient: 1001, spiServer: 1002, portClient: 5062, portServer: 5063,
		integrityAlgorithms:      []string{"hmac-md5-96", "hmac-sha-1-96"},
		encryptionAlgorithmsList: []string{"null", "des-ede3-cbc", "aes-cbc"},
	}
	header := proposal.headerValue()
	for _, combination := range []string{
		"alg=hmac-md5-96;prot=esp;mod=trans;ealg=null",
		"alg=hmac-md5-96;prot=esp;mod=trans;ealg=des-ede3-cbc",
		"alg=hmac-md5-96;prot=esp;mod=trans;ealg=aes-cbc",
		"alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=null",
		"alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=des-ede3-cbc",
		"alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=aes-cbc",
	} {
		if !strings.Contains(header, combination) {
			t.Fatalf("Android IMS Security-Client omitted %q: %s", combination, header)
		}
	}
	if got := len(splitHeaderValues([]string{header})); got != 6 {
		t.Fatalf("Security-Client mechanism count = %d, want 6", got)
	}
}

func TestNewSecurityProposalUsesAndroidDefaultsForEveryCarrier(t *testing.T) {
	proposal, err := newSecurityProposal(net.ParseIP("127.0.0.1"), 45062, 45063)
	if err != nil {
		t.Fatalf("newSecurityProposal() error = %v", err)
	}
	if got, want := proposal.integrities(), []string{"hmac-md5-96", "hmac-sha-1-96"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("integrity algorithms = %v, want %v", got, want)
	}
	if got, want := proposal.encryptionAlgorithms(), []string{"null", "des-ede3-cbc", "aes-cbc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("encryption algorithms = %v, want %v", got, want)
	}
	if got := len(splitHeaderValues([]string{proposal.headerValue()})); got != 6 {
		t.Fatalf("Security-Client mechanism count = %d, want 6", got)
	}
}

func TestRotatedSecurityProposalChangesClientPortAndPreservesServerPort(t *testing.T) {
	previous := securityProposal{
		spiClient:                1001,
		spiServer:                1002,
		portClient:               45062,
		portServer:               45063,
		encryption:               "null",
		fallbackEncryption:       "aes-cbc",
		integrityAlgorithms:      []string{"hmac-sha-1-96"},
		encryptionAlgorithmsList: []string{"null", "aes-cbc"},
	}
	rotated, err := rotatedSecurityProposal(net.ParseIP("127.0.0.1"), previous)
	if err != nil {
		t.Fatalf("rotatedSecurityProposal() error = %v", err)
	}
	if rotated.portClient == previous.portClient {
		t.Fatalf("rotated port-c = %d, want a new port", rotated.portClient)
	}
	if rotated.portServer != previous.portServer {
		t.Fatalf("rotated port-s = %d, want %d", rotated.portServer, previous.portServer)
	}
	if rotated.spiClient == previous.spiClient || rotated.spiServer == previous.spiServer {
		t.Fatalf("rotated SPIs = %d/%d, want fresh SPIs", rotated.spiClient, rotated.spiServer)
	}
	if rotated.encryption != previous.encryption ||
		rotated.fallbackEncryption != previous.fallbackEncryption ||
		!reflect.DeepEqual(rotated.integrityAlgorithms, previous.integrityAlgorithms) ||
		!reflect.DeepEqual(rotated.encryptionAlgorithmsList, previous.encryptionAlgorithmsList) {
		t.Fatalf("rotated proposal changed negotiated preferences: %#v", rotated)
	}
	rotated.integrityAlgorithms[0] = "changed"
	rotated.encryptionAlgorithmsList[0] = "changed"
	if previous.integrityAlgorithms[0] == "changed" || previous.encryptionAlgorithmsList[0] == "changed" {
		t.Fatal("rotated proposal aliases the previous algorithm lists")
	}
}

func TestXFRMPlanContainsFourStatesAndProtocolSpecificPolicies(t *testing.T) {
	config := testIPSecSAConfig()
	install, err := buildXFRMInstallPlan(config)
	if err != nil {
		t.Fatalf("buildXFRMInstallPlan() error = %v", err)
	}
	if len(install) != 12 {
		t.Fatalf("install operation count = %d, want 12", len(install))
	}
	for index, operation := range install[:4] {
		if !containsArguments(operation.arguments, "xfrm", "state", "add") {
			t.Fatalf("operation %d is not a state add: %v", index, operation.arguments)
		}
	}
	for index, operation := range install[4:] {
		if !containsArguments(operation.arguments, "xfrm", "policy", "add") {
			t.Fatalf("operation %d is not a policy add: %v", index+4, operation.arguments)
		}
	}

	clientReqID := argumentAfter(t, install[0].arguments, "reqid")
	if got := argumentAfter(t, install[1].arguments, "reqid"); got != clientReqID {
		t.Fatalf("client SA pair reqids = %q and %q", clientReqID, got)
	}
	serverReqID := argumentAfter(t, install[2].arguments, "reqid")
	if got := argumentAfter(t, install[3].arguments, "reqid"); got != serverReqID {
		t.Fatalf("server SA pair reqids = %q and %q", serverReqID, got)
	}
	if clientReqID == serverReqID {
		t.Fatalf("SA pair reqids both equal %q", clientReqID)
	}

	wantPolicies := map[string]bool{
		"tcp 40666 50600 out": false,
		"udp 40666 50600 out": false,
		"tcp 50600 40666 in":  false,
		"udp 50600 40666 in":  false,
		"tcp 50601 55610 in":  false,
		"udp 50601 55610 in":  false,
		"tcp 55610 50601 out": false,
		"udp 55610 50601 out": false,
	}
	for _, operation := range install[4:] {
		sourcePort := "*"
		if value, ok := optionalArgumentAfter(operation.arguments, "sport"); ok {
			sourcePort = value
		}
		key := strings.Join([]string{
			argumentAfter(t, operation.arguments, "proto"),
			sourcePort,
			argumentAfter(t, operation.arguments, "dport"),
			argumentAfter(t, operation.arguments, "dir"),
		}, " ")
		if _, expected := wantPolicies[key]; !expected {
			t.Fatalf("unexpected policy %q: %v", key, operation.arguments)
		}
		wantPolicies[key] = true
	}
	for policy, found := range wantPolicies {
		if !found {
			t.Errorf("missing policy %q", policy)
		}
	}

	cleanup := buildXFRMCleanupPlan(config)
	if len(cleanup) != 12 {
		t.Fatalf("cleanup operation count = %d, want 12", len(cleanup))
	}
	keyHex := "0x" + strings.Repeat("11", 16)
	for _, operation := range cleanup {
		if strings.Contains(strings.Join(operation.arguments, " "), keyHex) {
			t.Fatalf("cleanup operation retained encryption key: %v", operation.arguments)
		}
	}
}

func TestXFRMPlanSupportsIntegrityOnlyESP(t *testing.T) {
	config := testIPSecSAConfig()
	config.EncryptionAlgorithm = "null"
	config.EncryptionKey = nil
	install, err := buildXFRMInstallPlan(config)
	if err != nil {
		t.Fatalf("buildXFRMInstallPlan(null) error = %v", err)
	}
	for index, operation := range install[:4] {
		joined := strings.Join(operation.arguments, " ")
		if !strings.Contains(joined, "auth-trunc hmac(sha1)") {
			t.Fatalf("null state %d omitted integrity: %v", index, operation.arguments)
		}
		if !containsArguments(operation.arguments, "enc", "cipher_null", "") {
			t.Fatalf("null state %d omitted Linux NULL cipher: %v", index, operation.arguments)
		}
	}
}

func TestXFRMPlanSupportsAndroidMD5AndTripleDES(t *testing.T) {
	config := testIPSecSAConfig()
	config.IntegrityAlgorithm = "hmac-md5-96"
	config.EncryptionAlgorithm = "des-ede3-cbc"
	config.IntegrityKey = []byte(strings.Repeat("\x22", 16))
	config.EncryptionKey = []byte(strings.Repeat("\x11", 24))

	install, err := buildXFRMInstallPlan(config)
	if err != nil {
		t.Fatalf("buildXFRMInstallPlan() error = %v", err)
	}
	for index, operation := range install[:4] {
		if !containsArguments(operation.arguments, "auth-trunc", "hmac(md5)", "0x"+strings.Repeat("22", 16), "96") {
			t.Fatalf("state %d omitted HMAC-MD5-96 transform: %v", index, operation.arguments)
		}
		if !containsArguments(operation.arguments, "enc", "cbc(des3_ede)", "0x"+strings.Repeat("11", 24)) {
			t.Fatalf("state %d omitted 3DES-CBC transform: %v", index, operation.arguments)
		}
	}
}

func TestValidateIPSecSAConfigRejectsDuplicateSPI(t *testing.T) {
	config := testIPSecSAConfig()
	config.PCSCFServerSPI = config.UEClientSPI
	if err := validateIPSecSAConfig(config); err == nil {
		t.Fatal("validateIPSecSAConfig() error = nil")
	}
}

func testIPSecSAConfig() IPSecSAConfig {
	return IPSecSAConfig{
		LocalIP:         net.ParseIP("10.0.0.2"),
		RemoteIP:        net.ParseIP("10.0.0.3"),
		UEClientSPI:     0x10000001,
		UEServerSPI:     0x10000002,
		PCSCFClientSPI:  0x20000001,
		PCSCFServerSPI:  0x20000002,
		UEClientPort:    40666,
		UEServerPort:    55610,
		PCSCFClientPort: 50601,
		PCSCFServerPort: 50600,
		EncryptionKey:   []byte(strings.Repeat("\x11", 16)),
		IntegrityKey:    []byte(strings.Repeat("\x22", 20)),
	}
}

func argumentAfter(t *testing.T, arguments []string, name string) string {
	t.Helper()
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			return arguments[index+1]
		}
	}
	t.Fatalf("arguments %v omit %q", arguments, name)
	return ""
}

func optionalArgumentAfter(arguments []string, name string) (string, bool) {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			return arguments[index+1], true
		}
	}
	return "", false
}

func containsArguments(arguments []string, sequence ...string) bool {
	if len(sequence) == 0 || len(sequence) > len(arguments) {
		return false
	}
	for start := 0; start+len(sequence) <= len(arguments); start++ {
		if reflect.DeepEqual(arguments[start:start+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

func TestErrorsExposeAgreementSentinel(t *testing.T) {
	_, err := parseSecurityAgreement(nil, securityProposal{})
	if !errors.Is(err, ErrIPSecAgreementRequired) {
		t.Fatalf("error = %v, want ErrIPSecAgreementRequired", err)
	}
}
