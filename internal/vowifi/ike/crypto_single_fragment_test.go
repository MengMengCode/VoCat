package ike

import (
	"bytes"
	"testing"
)

func TestRFC7383SmallPayloadStaysUnfragmented(t *testing.T) {
	suite := legacyTestSuite()
	encryptionKey := bytes.Repeat([]byte{0x11}, 16)
	integrityKey := bytes.Repeat([]byte{0x22}, 20)

	header := ikeHeader{
		InitiatorSPI: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		ResponderSPI: [8]byte{8, 7, 6, 5, 4, 3, 2, 1},
		Exchange:     exchangeIKEAuth,
		Flags:        flagInitiator,
		MessageID:    1,
	}

	inner := []payload{
		{Type: payloadIDi, Body: []byte{3, 0, 0, 0, 'u', 's', 'e', 'r'}},
		{Type: payloadEAP, Body: bytes.Repeat([]byte{0x33}, 64)},
	}

	packets, err := encryptPayloadsFragmented(
		header,
		inner,
		suite,
		encryptionKey,
		integrityKey,
		defaultIKEFragmentSize,
		bytes.NewReader(bytes.Repeat([]byte{0x77}, 256)),
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(packets) != 1 {
		t.Fatalf("got %d packets, want 1", len(packets))
	}

	hdr, _, err := parseIKEPacket(packets[0])
	if err != nil {
		t.Fatal(err)
	}

	if hdr.NextPayload != payloadEncrypted {
		t.Fatalf(
			"small protected exchange NextPayload = %d, want %d (payloadEncrypted)",
			hdr.NextPayload,
			payloadEncrypted,
		)
	}

	decodedHeader, decoded, err := decryptPayloadsAny(
		packets[0],
		nil,
		suite,
		encryptionKey,
		integrityKey,
	)
	if err != nil {
		t.Fatal(err)
	}

	if decodedHeader.MessageID != header.MessageID || len(decoded) != len(inner) {
		t.Fatal("decrypted small exchange mismatch")
	}
}
