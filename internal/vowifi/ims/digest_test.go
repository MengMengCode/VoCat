package ims

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"vocat/internal/vowifi"
)

type recordingAKA struct {
	result     vowifi.AKAResult
	err        error
	challenges []vowifi.AKAChallenge
}

type recordingPreferredAKA struct {
	recordingAKA
	preference string
}

func (aka *recordingPreferredAKA) AuthenticateWithPreference(
	ctx context.Context,
	identity vowifi.SIMIdentity,
	challenge vowifi.AKAChallenge,
	preference string,
) (vowifi.AKAResult, error) {
	aka.preference = preference
	return aka.Authenticate(ctx, identity, challenge)
}

func (aka *recordingAKA) CheckReady(context.Context, vowifi.SIMIdentity) (vowifi.AKAEvidence, error) {
	return vowifi.AKAEvidence{Ready: true, Application: "usim"}, nil
}

func (aka *recordingAKA) Authenticate(
	_ context.Context,
	_ vowifi.SIMIdentity,
	challenge vowifi.AKAChallenge,
) (vowifi.AKAResult, error) {
	aka.challenges = append(aka.challenges, challenge)
	return aka.result, aka.err
}

func TestDigestResponseRFC2617Vector(t *testing.T) {
	got := digestResponse(
		"Mufasa",
		"testrealm@host.com",
		[]byte("Circle Of Life"),
		"GET",
		"/dir/index.html",
		"dcd98b7102dd2f0e8b11d0f600bfb0c093",
		"00000001",
		"0a4f113b",
		"auth",
	)
	const want = "6629fae49393a05397450978507c4ef1"
	if got != want {
		t.Fatalf("digestResponse() = %q, want %q", got, want)
	}
}

func TestAuthenticateAKAMapsNonceToTypedChallenge(t *testing.T) {
	nonceBytes := make([]byte, 40)
	for index := range nonceBytes {
		nonceBytes[index] = byte(index)
	}
	aka := &recordingAKA{
		result: vowifi.AKAResult{RES: []byte{0xde, 0xad, 0xbe, 0xef}},
	}
	material, err := authenticateAKA(
		context.Background(),
		aka,
		vowifi.SIMIdentity{IMSI: "001010123456789"},
		digestChallenge{Nonce: base64.StdEncoding.EncodeToString(nonceBytes)},
		"",
	)
	if err != nil {
		t.Fatalf("authenticateAKA() error = %v", err)
	}
	if !reflect.DeepEqual(material.response, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("response = %x, want deadbeef", material.response)
	}
	if len(aka.challenges) != 1 {
		t.Fatalf("challenge count = %d, want 1", len(aka.challenges))
	}
	var wantRAND, wantAUTN [16]byte
	copy(wantRAND[:], nonceBytes[:16])
	copy(wantAUTN[:], nonceBytes[16:32])
	if !reflect.DeepEqual(aka.challenges[0].RAND, wantRAND) ||
		!reflect.DeepEqual(aka.challenges[0].AUTN, wantAUTN) {
		t.Fatalf("typed challenge = %#v", aka.challenges[0])
	}
}

func TestAuthenticateAKAReturnsSynchronizationEvidence(t *testing.T) {
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	auts := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
	aka := &recordingAKA{
		result: vowifi.AKAResult{
			AUTS:                   auts,
			SynchronizationFailure: true,
		},
	}
	material, err := authenticateAKA(
		context.Background(),
		aka,
		vowifi.SIMIdentity{},
		digestChallenge{Nonce: nonce},
		"",
	)
	if err != nil {
		t.Fatalf("authenticateAKA() error = %v", err)
	}
	if !reflect.DeepEqual(material.auts, auts) || len(material.response) != 0 {
		t.Fatalf("material = %#v", material)
	}
}

func TestAuthenticateAKAUsesPreferredApplicationWhenSupported(t *testing.T) {
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	aka := &recordingPreferredAKA{recordingAKA: recordingAKA{
		result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}},
	}}
	_, err := authenticateAKA(
		context.Background(),
		aka,
		vowifi.SIMIdentity{IMSI: "310280000000001"},
		digestChallenge{Nonce: nonce},
		"isim_strict",
	)
	if err != nil {
		t.Fatalf("authenticateAKA() error = %v", err)
	}
	if aka.preference != "isim_strict" {
		t.Fatalf("preference = %q, want isim_strict", aka.preference)
	}
}

func TestBuildDigestAuthorizationCarriesAUTSWithEmptyResponse(t *testing.T) {
	authorization := buildDigestAuthorization(
		digestChallenge{
			Realm:     "ims.example",
			Nonce:     "nonce",
			Algorithm: "AKAv1-MD5",
			QOP:       "auth",
		},
		digestCredentials{
			Username:    "private@ims.example",
			AKAResponse: nil,
			AUTS:        "AAECAwQFBgcICQoLDA0=",
			URI:         "sip:ims.example",
			Method:      "REGISTER",
			CNonce:      "cnonce",
			NC:          1,
		},
	)
	directives, err := parseAuthDirectives(strings.TrimPrefix(authorization, "Digest "))
	if err != nil {
		t.Fatalf("parseAuthDirectives() error = %v", err)
	}
	if directives["auts"] != "AAECAwQFBgcICQoLDA0=" {
		t.Fatalf("AUTS = %q", directives["auts"])
	}
	expected := digestResponse(
		"private@ims.example",
		"ims.example",
		nil,
		"REGISTER",
		"sip:ims.example",
		"nonce",
		"00000001",
		"cnonce",
		"auth",
	)
	if directives["response"] != expected {
		t.Fatalf("response = %q, want %q", directives["response"], expected)
	}
}

func TestParseDigestChallengeSelectsAuth(t *testing.T) {
	challenge, err := parseDigestChallenge(
		`Digest realm="ims.example", nonce="abc", algorithm=AKAv1-MD5, qop="auth-int, auth", opaque="x\"y"`,
		false,
	)
	if err != nil {
		t.Fatalf("parseDigestChallenge() error = %v", err)
	}
	if challenge.QOP != "auth" || challenge.Opaque != `x"y` {
		t.Fatalf("challenge = %#v", challenge)
	}
}
