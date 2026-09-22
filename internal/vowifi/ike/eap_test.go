package ike

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vocat/internal/vowifi"
)

type testAKAProvider struct {
	result    vowifi.AKAResult
	err       error
	challenge vowifi.AKAChallenge
	calls     int
}

func (provider *testAKAProvider) CheckReady(context.Context, vowifi.SIMIdentity) (vowifi.AKAEvidence, error) {
	return vowifi.AKAEvidence{Ready: true, Application: "USIM"}, nil
}

func (provider *testAKAProvider) Authenticate(
	_ context.Context,
	_ vowifi.SIMIdentity,
	challenge vowifi.AKAChallenge,
) (vowifi.AKAResult, error) {
	provider.calls++
	provider.challenge = challenge
	return provider.result, provider.err
}

func testSIMIdentity() vowifi.SIMIdentity {
	return vowifi.SIMIdentity{
		IMSI:    "234150123456789",
		HomeMCC: "234",
		HomeMNC: "15",
	}
}

func TestEAPAKAIdentityRequiresExactlyOneRequestAttribute(t *testing.T) {
	client, err := newAKAClient(testSIMIdentity(), &testAKAProvider{})
	if err != nil {
		t.Fatalf("newAKAClient() error = %v", err)
	}
	request, err := marshalEAPPacket(eapPacket{
		Code:       eapRequest,
		Identifier: 9,
		Type:       eapTypeAKA,
		Data:       []byte{akaSubtypeIdentity, 0, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	action, err := client.handle(context.Background(), request)
	if err != nil {
		t.Fatalf("handle malformed identity error = %v", err)
	}
	response, err := parseEAPPacket(action.Response)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != eapResponse || response.Identifier != 9 ||
		response.Type != eapTypeAKA || len(response.Data) < 1 ||
		response.Data[0] != akaSubtypeClientError {
		t.Fatalf("malformed identity response = %#v", response)
	}

	permanent, _ := marshalAKAAttribute(akaAttrPermanentIDReq, []byte{0, 0})
	validRequest, _ := marshalEAPPacket(eapPacket{
		Code:       eapRequest,
		Identifier: 10,
		Type:       eapTypeAKA,
		Data:       append([]byte{akaSubtypeIdentity, 0, 0}, permanent...),
	})
	action, err = client.handle(context.Background(), validRequest)
	if err != nil {
		t.Fatalf("handle valid identity error = %v", err)
	}
	response, _ = parseEAPPacket(action.Response)
	if response.Data[0] != akaSubtypeIdentity {
		t.Fatalf("valid identity response subtype = %d", response.Data[0])
	}
	attributes, err := parseAKAAttributes(response.Data[3:])
	if err != nil {
		t.Fatal(err)
	}
	identity, err := oneAKAAttribute(attributes, akaAttrIdentity)
	if err != nil {
		t.Fatal(err)
	}
	length := int(identity.Raw[2])<<8 | int(identity.Raw[3])
	if got := string(identity.Raw[4 : 4+length]); got != "0234150123456789@nai.epc.mnc015.mcc234.3gppnetwork.org" {
		t.Fatalf("permanent AKA identity = %q", got)
	}
}

func TestPermanentAKAIdentityRewritesDITORoamingPrefix(t *testing.T) {
	installDITONativeAliasProfile(t)
	for _, test := range []struct {
		iccid string
		imsi  string
		want  string
	}{
		{"89636626000000000001", "204047616000001", "0515661015000001@nai.epc.mnc066.mcc515.3gppnetwork.org"},
		{"89636626000000000002", "204047616000002", "0515661015000002@nai.epc.mnc066.mcc515.3gppnetwork.org"},
	} {
		identity, err := permanentAKAIdentity(vowifi.SIMIdentity{
			ICCID: test.iccid, IMSI: test.imsi, HomeMCC: "515", HomeMNC: "66",
		})
		if err != nil {
			t.Fatal(err)
		}
		if string(identity) != test.want {
			t.Fatalf("permanent AKA identity = %q, want %q", identity, test.want)
		}
	}
}

func installDITONativeAliasProfile(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	emptyDir := t.TempDir()
	t.Cleanup(func() {
		if err := vowifi.LoadCarrierProfileDirectory(emptyDir); err != nil {
			t.Errorf("clear carrier profiles: %v", err)
		}
	})
	profile := `{"version":1,"profiles":[{"id":"test-dito-native-alias","match":{"home_plmns":["51566"],"imsi_prefixes":["204047616"],"iccid_prefixes":["89636626"]},"identity":{"subscriber_imsi_rewrite":{"from_prefix":"204047616","to_prefix":"515661015"}},"route":{"mcc":"515","mnc":"66"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vowifi.LoadCarrierProfileDirectory(dir); err != nil {
		t.Fatal(err)
	}
}

func TestEAPFailureReportsAuthenticationStage(t *testing.T) {
	client, err := newAKAClient(testSIMIdentity(), &testAKAProvider{})
	if err != nil {
		t.Fatal(err)
	}
	failure, err := marshalEAPPacket(eapPacket{Code: eapFailure, Identifier: 3})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.handle(context.Background(), failure)
	if !errors.Is(err, vowifi.ErrEAPAuthenticationRejected) || !strings.Contains(err.Error(), "initial IKE_AUTH identity exchange") {
		t.Fatalf("pre-challenge failure = %v", err)
	}
	client.challengeComplete = true
	_, err = client.handle(context.Background(), failure)
	if !errors.Is(err, vowifi.ErrEAPAuthenticationRejected) || !strings.Contains(err.Error(), "after the SIM AKA challenge response") {
		t.Fatalf("post-challenge failure = %v", err)
	}
}

func TestEAPFailureReportsIdentityResponseStage(t *testing.T) {
	client, err := newAKAClient(testSIMIdentity(), &testAKAProvider{})
	if err != nil {
		t.Fatal(err)
	}
	identityRequest, _ := marshalEAPPacket(eapPacket{
		Code: eapRequest, Identifier: 4, Type: eapTypeIdentity,
	})
	if _, err := client.handle(context.Background(), identityRequest); err != nil {
		t.Fatal(err)
	}
	failure, _ := marshalEAPPacket(eapPacket{Code: eapFailure, Identifier: 5})
	_, err = client.handle(context.Background(), failure)
	if !errors.Is(err, vowifi.ErrEAPAuthenticationRejected) || !strings.Contains(err.Error(), "after EAP-Response/Identity") {
		t.Fatalf("identity-stage failure = %v", err)
	}
}

func TestEAPAKAChallengeTypedSIMAndMAC(t *testing.T) {
	result := vowifi.AKAResult{
		RES: bytes.Repeat([]byte{0x91}, 8),
		CK:  bytes.Repeat([]byte{0x92}, 16),
		IK:  bytes.Repeat([]byte{0x93}, 16),
	}
	provider := &testAKAProvider{result: result}
	client, err := newAKAClient(testSIMIdentity(), provider)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := deriveAKAKeys(client.identity, result.IK, result.CK)
	if err != nil {
		t.Fatal(err)
	}
	randValue := bytes.Repeat([]byte{0xa1}, 16)
	autnValue := bytes.Repeat([]byte{0xa2}, 16)
	randAttribute, _ := marshalAKAAttribute(akaAttrRAND, append([]byte{0, 0}, randValue...))
	autnAttribute, _ := marshalAKAAttribute(akaAttrAUTN, append([]byte{0, 0}, autnValue...))
	macAttribute, _ := marshalAKAAttribute(akaAttrMAC, make([]byte, 18))
	data := []byte{akaSubtypeChallenge, 0, 0}
	data = append(data, randAttribute...)
	data = append(data, autnAttribute...)
	data = append(data, macAttribute...)
	request, _ := marshalEAPPacket(eapPacket{
		Code:       eapRequest,
		Identifier: 21,
		Type:       eapTypeAKA,
		Data:       data,
	})
	attributes, _ := parseAKAAttributes(data[3:])
	mac, _ := oneAKAAttribute(attributes, akaAttrMAC)
	macOffset := 5 + 3 + mac.Offset
	copy(request[macOffset+4:macOffset+20], akaMAC(keys.KAut, request))

	action, err := client.handle(context.Background(), request)
	if err != nil {
		t.Fatalf("handle challenge error = %v", err)
	}
	if provider.calls != 1 || !bytes.Equal(provider.challenge.RAND[:], randValue) ||
		!bytes.Equal(provider.challenge.AUTN[:], autnValue) {
		t.Fatalf("typed SIM challenge = %#v calls=%d", provider.challenge, provider.calls)
	}
	response, err := parseEAPPacket(action.Response)
	if err != nil {
		t.Fatal(err)
	}
	if response.Data[0] != akaSubtypeChallenge {
		t.Fatalf("challenge response subtype = %d", response.Data[0])
	}
	responseAttributes, err := parseAKAAttributes(response.Data[3:])
	if err != nil {
		t.Fatal(err)
	}
	res, err := oneAKAAttribute(responseAttributes, akaAttrRES)
	if err != nil {
		t.Fatal(err)
	}
	if bits := int(res.Raw[2])<<8 | int(res.Raw[3]); bits != len(result.RES)*8 {
		t.Fatalf("AT_RES bits = %d", bits)
	}
	responseMAC, err := oneAKAAttribute(responseAttributes, akaAttrMAC)
	if err != nil {
		t.Fatal(err)
	}
	zeroed := append([]byte(nil), action.Response...)
	responseOffset := 5 + 3 + responseMAC.Offset
	actualMAC := append([]byte(nil), zeroed[responseOffset+4:responseOffset+20]...)
	for index := responseOffset + 4; index < responseOffset+20; index++ {
		zeroed[index] = 0
	}
	if !bytes.Equal(actualMAC, akaMAC(keys.KAut, zeroed)) {
		t.Fatal("response AT_MAC does not authenticate the complete EAP packet")
	}
	success, _ := marshalEAPPacket(eapPacket{Code: eapSuccess, Identifier: 22})
	finalAction, err := client.handle(context.Background(), success)
	if err != nil || !finalAction.Success {
		t.Fatalf("authenticated EAP success = %#v err=%v", finalAction, err)
	}
}

func TestEAPAKAUSIMNetworkAuthenticationFailureSendsReject(t *testing.T) {
	provider := &testAKAProvider{err: vowifi.ErrEC20AKAMACFailure}
	client, err := newAKAClient(testSIMIdentity(), provider)
	if err != nil {
		t.Fatal(err)
	}
	randAttribute, _ := marshalAKAAttribute(akaAttrRAND, append([]byte{0, 0}, bytes.Repeat([]byte{1}, 16)...))
	autnAttribute, _ := marshalAKAAttribute(akaAttrAUTN, append([]byte{0, 0}, bytes.Repeat([]byte{2}, 16)...))
	macAttribute, _ := marshalAKAAttribute(akaAttrMAC, make([]byte, 18))
	data := append([]byte{akaSubtypeChallenge, 0, 0}, randAttribute...)
	data = append(data, autnAttribute...)
	data = append(data, macAttribute...)
	request, _ := marshalEAPPacket(eapPacket{Code: eapRequest, Identifier: 5, Type: eapTypeAKA, Data: data})
	action, err := client.handle(context.Background(), request)
	if err != nil {
		t.Fatalf("handle USIM MAC failure error = %v", err)
	}
	response, _ := parseEAPPacket(action.Response)
	if len(response.Data) != 3 || response.Data[0] != akaSubtypeAuthReject {
		t.Fatalf("USIM MAC failure response = %#v", response)
	}
	if !errors.Is(provider.err, vowifi.ErrEC20AKAMACFailure) {
		t.Fatal("test provider lost the MAC failure sentinel")
	}
}
