package device

import (
	"bytes"
	"errors"
	"testing"
)

// buildTestBPP assembles a synthetic BoundProfilePackage (BF36) with the
// initialiseSecureChannel + A0/A1/A3 sequences lpac's segmenter cares about.
func buildTestBPP(t *testing.T) (bpp []byte, parts map[string][]byte) {
	t.Helper()
	parts = map[string][]byte{}
	parts["bf23"] = tlv([]byte{0xBF, 0x23}, bytes.Repeat([]byte{0x01}, 20))
	parts["a0"] = tlv([]byte{0xA0}, bytes.Repeat([]byte{0x02}, 8))
	parts["a1c1"] = tlv([]byte{0x88}, bytes.Repeat([]byte{0x03}, 5))
	parts["a1c2"] = tlv([]byte{0x88}, bytes.Repeat([]byte{0x04}, 6))
	parts["a1"] = tlv([]byte{0xA1}, parts["a1c1"], parts["a1c2"])
	parts["a3c1"] = tlv([]byte{0x86}, bytes.Repeat([]byte{0x05}, 7))
	parts["a3"] = tlv([]byte{0xA3}, parts["a3c1"])
	bpp = tlv([]byte{0xBF, 0x36}, parts["bf23"], parts["a0"], parts["a1"], parts["a3"])
	return bpp, parts
}

func TestSegmentBoundProfilePackage(t *testing.T) {
	bpp, parts := buildTestBPP(t)
	segments, err := segmentBoundProfilePackage(bpp)
	if err != nil {
		t.Fatalf("segmentBoundProfilePackage: %v", err)
	}

	// The slices must reassemble into the exact original package.
	if got := bytes.Join(segments, nil); !bytes.Equal(got, bpp) {
		t.Fatalf("segments do not reassemble to the original BPP")
	}

	// Segment 0 = BF36 header + complete BF23 (secure channel first).
	if !bytes.HasPrefix(segments[0], []byte{0xBF, 0x36}) {
		t.Fatalf("segment 0 must start with BF36, got %X", segments[0][:3])
	}
	if !bytes.Contains(segments[0], parts["bf23"]) {
		t.Fatalf("segment 0 must contain the full BF23")
	}
	// A0 sent whole; A1/A3 split header then children.
	if !bytes.Equal(segments[1], parts["a0"]) {
		t.Fatalf("segment 1 should be the whole A0")
	}
	if !bytes.Equal(segments[2], parts["a1"][:2]) {
		t.Fatalf("segment 2 should be the A1 header only, got %X", segments[2])
	}
	if !bytes.Equal(segments[3], parts["a1c1"]) || !bytes.Equal(segments[4], parts["a1c2"]) {
		t.Fatalf("A1 children not segmented correctly")
	}
}

func TestSegmentBoundProfilePackageNoBF36(t *testing.T) {
	if _, err := segmentBoundProfilePackage(tlv([]byte{0xBF, 0x35}, []byte{0x00})); err == nil {
		t.Fatalf("expected error when BF36 is absent")
	}
}

func buildInstallResult(t *testing.T, finalResult []byte) []byte {
	t.Helper()
	iccidBCD, err := encodeICCID("8944476500017228672")
	if err != nil {
		t.Fatalf("encodeICCID: %v", err)
	}
	notifMeta := tlv([]byte{0xBF, 0x2F}, tlv([]byte{0x80}, []byte{0x01}), tlv([]byte{0x5A}, iccidBCD))
	bf27 := tlv([]byte{0xBF, 0x27}, notifMeta, finalResult)
	return tlv([]byte{0xBF, 0x37}, bf27)
}

func TestInstallationResultSuccess(t *testing.T) {
	payload := buildInstallResult(t, tlv([]byte{0xA2}, tlv([]byte{0xA0})))
	iccid, err := installationResult(payload)
	if err != nil {
		t.Fatalf("installationResult: %v", err)
	}
	if iccid != "8944476500017228672" {
		t.Fatalf("iccid = %q", iccid)
	}
}

func TestInstallationResultInsufficientMemory(t *testing.T) {
	// A2 { A1 { 80 bppCommandId=5, 81 errorReason=10 (insufficient memory) } }
	errResult := tlv([]byte{0xA1}, tlv([]byte{0x80}, []byte{0x05}), tlv([]byte{0x81}, []byte{0x0A}))
	payload := buildInstallResult(t, tlv([]byte{0xA2}, errResult))

	_, err := installationResult(payload)
	if err == nil {
		t.Fatalf("expected an installation error")
	}
	var installErr *esimInstallError
	if !errors.As(err, &installErr) {
		t.Fatalf("expected *esimInstallError, got %T (%v)", err, err)
	}
	if installErr.ErrorReason != 10 || installErr.CommandID != 5 {
		t.Fatalf("got reason=%d command=%d", installErr.ErrorReason, installErr.CommandID)
	}
	if code := ESIMDownloadErrorCode(err); code != "euicc_insufficient_memory" {
		t.Fatalf("error code = %q, want euicc_insufficient_memory", code)
	}
}

func TestESIMDownloadErrorCodePublicProfileFailures(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{
			err:  errors.New("The SM-DP+ has no CERT.DPauth.SIG which chains to one of the eSIM CA Root CA Certificate with a Public Key supported by the eUICC"),
			want: "euicc_ci_incompatible",
		},
		{err: errors.New("Refused"), want: "activation_code_refused"},
		{err: errors.New("campaign resource pool is empty"), want: "profile_pool_empty"},
	}
	for _, test := range tests {
		if got := ESIMDownloadErrorCode(test.err); got != test.want {
			t.Errorf("ESIMDownloadErrorCode(%q) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestAuthenticateServerResultError(t *testing.T) {
	success := derConstruct(0xBF38, derConstruct(0xA0, derEncode(0x30, nil)))
	if err := authenticateServerResultError(success); err != nil {
		t.Fatalf("success response returned %v", err)
	}
	errorResponse := derConstruct(0xBF38, derConstruct(0xA1,
		derConstruct(0x30, derEncode(0x80, []byte{0x01, 0x02}), derEncode(0x02, []byte{0x02})),
	))
	err := authenticateServerResultError(errorResponse)
	var authenticateErr *esimAuthenticateError
	if !errors.As(err, &authenticateErr) || authenticateErr.Code != 2 {
		t.Fatalf("error = %T %v, want authenticate code 2", err, err)
	}
	if code := ESIMDownloadErrorCode(err); code != "euicc_authentication_failed" {
		t.Fatalf("download error code = %q", code)
	}
}

func TestEuiccFreeNVRAM(t *testing.T) {
	// BF22 { 84 { 81 installedApp, 82 freeNonVolatile=0x05E849, 83 freeVolatile } }
	extRes := tlv([]byte{0x84},
		tlv([]byte{0x81}, []byte{0x00}),
		tlv([]byte{0x82}, []byte{0x05, 0xE8, 0x49}),
		tlv([]byte{0x83}, []byte{0x00}),
	)
	info2 := tlv([]byte{0xBF, 0x22}, extRes)
	n, ok := euiccFreeNVRAM(info2)
	if !ok || n != 387145 {
		t.Fatalf("freeNVRAM = %d, ok=%v (want 387145)", n, ok)
	}
	if _, ok := euiccFreeNVRAM(tlv([]byte{0xBF, 0x22})); ok {
		t.Fatalf("expected ok=false when extCardResource absent")
	}
}

func TestES10StatusAcceptsProactiveRefresh(t *testing.T) {
	for _, status := range []int{0x9000, 0x9100, 0x910B, 0x91FF} {
		if !es10StatusOK(status) {
			t.Fatalf("status %04X should be successful", status)
		}
	}
	for _, status := range []int{0x6A82, 0x6985, 0x9200} {
		if es10StatusOK(status) {
			t.Fatalf("status %04X should fail", status)
		}
	}
}
