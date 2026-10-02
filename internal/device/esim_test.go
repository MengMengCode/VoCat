package device

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"vocat/internal/modem"
)

// tlv builds one BER-TLV element from a (possibly multi-byte) tag and a body
// assembled from the given parts.
func tlv(tag []byte, parts ...[]byte) []byte {
	var body []byte
	for _, part := range parts {
		body = append(body, part...)
	}
	out := append([]byte(nil), tag...)
	switch {
	case len(body) < 0x80:
		out = append(out, byte(len(body)))
	case len(body) < 0x100:
		out = append(out, 0x81, byte(len(body)))
	default:
		out = append(out, 0x82, byte(len(body)>>8), byte(len(body)))
	}
	return append(out, body...)
}

func esimTestProfile(t *testing.T, iccidDigits, provider, name string, state byte) []byte {
	t.Helper()
	bcd, err := encodeICCID(iccidDigits)
	if err != nil {
		t.Fatalf("encodeICCID: %v", err)
	}
	aid, _ := hex.DecodeString("A0000005591010FFFFFFFF8900001000")
	// Icon deliberately contains 0x5A and 0xE3 bytes to prove the parser never
	// descends into primitive (non-constructed) leaves.
	icon := []byte{0x89, 0x50, 0x4E, 0x47, 0x5A, 0xE3, 0x05, 0x9F, 0x70, 0x01}
	return tlv([]byte{0xE3},
		tlv([]byte{0x5A}, bcd),
		tlv([]byte{0x4F}, aid),
		tlv([]byte{0x9F, 0x70}, []byte{state}),
		tlv([]byte{0x91}, []byte(provider)),
		tlv([]byte{0x92}, []byte(name)),
		tlv([]byte{0x94}, icon),
	)
}

func TestParseProfilesInfoRealShape(t *testing.T) {
	// BF2D root (this card echoes the request tag) -> A0 list -> E3 records.
	body := tlv([]byte{0xA0},
		esimTestProfile(t, "8944100000000000001", "Vodafone UK", "Vodafone UK eSIM", 0x00),
		esimTestProfile(t, "8944100000000000002", "Vodafone UK", "Vodafone UK eSIM", 0x01),
		esimTestProfile(t, "8985200000000000001", "Webbing", "WEBBING", 0x00),
	)
	payload := tlv([]byte{0xBF, 0x2D}, body)

	profiles := parseProfilesInfo(payload)
	if len(profiles) != 3 {
		t.Fatalf("expected 3 profiles, got %d: %#v", len(profiles), profiles)
	}
	if profiles[0].ICCID != "8944100000000000001" || profiles[0].State != 0 {
		t.Fatalf("profile[0] = %#v", profiles[0])
	}
	if profiles[1].ICCID != "8944100000000000002" || profiles[1].State != 1 || profiles[1].StateText != "已启用" {
		t.Fatalf("profile[1] = %#v", profiles[1])
	}
	if profiles[2].ServiceProvider != "Webbing" || profiles[2].Name != "WEBBING" || profiles[2].State != 0 {
		t.Fatalf("profile[2] = %#v", profiles[2])
	}
	info := &EsimInfo{Profiles: profiles}
	enabled := info.EnabledProfile()
	if enabled == nil || enabled.Name != "Vodafone UK eSIM" {
		t.Fatalf("enabled profile = %#v", enabled)
	}
}

func TestParseProfilesInfoSkipsNestedMetadataE3WithoutICCID(t *testing.T) {
	real := esimTestProfile(t, "8944100000000000003", "Vodafone UK", "Vodafone UK eSIM", 0x01)
	duplicate := esimTestProfile(t, "8944100000000000003", "Duplicate", "Duplicate", 0x00)
	metadata := tlv([]byte{0xE3}, tlv([]byte{0x80}, []byte{0x01}))
	empty := tlv([]byte{0xE3})
	payload := tlv([]byte{0xBF, 0x2D}, tlv([]byte{0xA0}, metadata, real, empty, duplicate))

	profiles := parseProfilesInfo(payload)
	if len(profiles) != 1 {
		t.Fatalf("profiles = %#v, want one addressable profile", profiles)
	}
	if profiles[0].ICCID != "8944100000000000003" || profiles[0].Name != "Vodafone UK eSIM" {
		t.Fatalf("profile = %#v", profiles[0])
	}
}

func TestICCIDRoundTrip(t *testing.T) {
	for _, digits := range []string{"8944100000000000001", "8985200000000000001", "1"} {
		bcd, err := encodeICCID(digits)
		if err != nil {
			t.Fatalf("encodeICCID(%q): %v", digits, err)
		}
		if len(bcd) != 10 {
			t.Fatalf("encodeICCID(%q) length = %d, want fixed 10 octets", digits, len(bcd))
		}
		if got := decodeICCID(bcd); got != digits {
			t.Fatalf("round trip %q -> %q", digits, got)
		}
	}
	if _, err := encodeICCID("894410000000000000001"); err == nil {
		t.Fatal("21-digit ICCID was accepted")
	}
}

func TestEnableProfileRequestPads18DigitICCIDToTenOctets(t *testing.T) {
	request, err := buildEnableProfileRequest("894921007608519523")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(request)); got != "BF3111A00C5A0A989412006780155932FF8101FF" {
		t.Fatalf("EnableProfile request = %s", got)
	}
}

func TestDeleteProfileRequestAndResult(t *testing.T) {
	request, err := buildDeleteProfileRequest("89441000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(request)); got != "BF330C5A0A98440100000000000010" {
		t.Fatalf("DeleteProfile request = %s", got)
	}
	result, ok := deleteProfileResult([]byte{0xBF, 0x33, 0x03, 0x80, 0x01, 0x00})
	if !ok || result != 0 {
		t.Fatalf("DeleteProfile result = (%d, %v)", result, ok)
	}
	activeResponse := []byte{0xBF, 0x33, 0x03, 0x80, 0x01, 0x02}
	if err := deleteProfileResponseError(2, activeResponse); !errors.Is(err, ErrESIMDeleteProfileNotDisabled) {
		t.Fatalf("DeleteProfile result 2 error = %v", err)
	}
}

func TestSetNicknameRequestAndResult(t *testing.T) {
	request, err := buildSetNicknameRequest("89441000000000000001", "Test")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(request)); got != "BF29125A0A98440100000000000010900454657374" {
		t.Fatalf("SetNickname request = %s", got)
	}
	result, ok := setNicknameResult([]byte{0xBF, 0x29, 0x03, 0x80, 0x01, 0x00})
	if !ok || result != 0 {
		t.Fatalf("SetNickname result = (%d, %v)", result, ok)
	}
	if _, err := buildSetNicknameRequest("89441000000000000001", strings.Repeat("名", 65)); !errors.Is(err, ErrESIMNicknameTooLong) {
		t.Fatalf("long nickname error = %v", err)
	}
}

func TestDisableProfileRequestAndResult(t *testing.T) {
	request, err := buildDisableProfileRequest("89441000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(request)); got != "BF3211A00C5A0A984401000000000000108101FF" {
		t.Fatalf("DisableProfile request = %s", got)
	}
	result, ok := disableProfileResult([]byte{0xBF, 0x32, 0x03, 0x80, 0x01, 0x00})
	if !ok || result != 0 {
		t.Fatalf("DisableProfile result = (%d, %v)", result, ok)
	}
	busyResponse := []byte{0xBF, 0x32, 0x03, 0x80, 0x01, 0x05}
	if err := disableProfileResponseError(5, busyResponse); !errors.Is(err, ErrESIMDisableCATBusy) {
		t.Fatalf("DisableProfile result 5 error = %v", err)
	}
}

func TestEnableProfileResultErrors(t *testing.T) {
	undefinedResponse := []byte{0xBF, 0x31, 0x03, 0x80, 0x01, 0x7F}
	result, ok := enableProfileResult(undefinedResponse)
	if !ok || result != 0x7F {
		t.Fatalf("EnableProfile result = (%d, %v)", result, ok)
	}
	if err := enableProfileResponseError(byte(result), undefinedResponse); !errors.Is(err, ErrESIMEnableUndefined) {
		t.Fatalf("EnableProfile undefinedError = %v", err)
	}
	policyResponse := []byte{0xBF, 0x31, 0x03, 0x80, 0x01, 0x03}
	if err := enableProfileResponseError(3, policyResponse); !errors.Is(err, ErrESIMEnableDisallowedPolicy) {
		t.Fatalf("EnableProfile policy error = %v", err)
	}
}

func TestEnableProfileRequestWithAID(t *testing.T) {
	aid, err := hex.DecodeString("A0000005591010FFFFFFFF8900000101")
	if err != nil {
		t.Fatal(err)
	}
	req, err := buildEnableProfileRequestWithAID(aid, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(req)); got != "BF3117A0124F10A0000005591010FFFFFFFF89000001018101FF" {
		t.Fatalf("EnableProfile with AID request = %s, want BF3117A0124F10A0000005591010FFFFFFFF89000001018101FF", got)
	}
	reqNoRefresh, err := buildEnableProfileRequestWithAID(aid, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(reqNoRefresh)); got != "BF3117A0124F10A0000005591010FFFFFFFF8900000101810100" {
		t.Fatalf("EnableProfile with AID (refresh=false) = %s", got)
	}
	if _, err := buildEnableProfileRequestWithAID(nil, true); err == nil {
		t.Fatal("empty AID should be rejected")
	}
	if _, err := buildEnableProfileRequestWithAID(make([]byte, 17), true); err == nil {
		t.Fatal("17-byte AID should be rejected (>16 octets)")
	}
}

func TestDisableProfileRequestWithAID(t *testing.T) {
	aid, err := hex.DecodeString("A0000005591010FFFFFFFF8900000101")
	if err != nil {
		t.Fatal(err)
	}
	req, err := buildDisableProfileRequestWithAID(aid)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(req)); got != "BF3217A0124F10A0000005591010FFFFFFFF89000001018101FF" {
		t.Fatalf("DisableProfile with AID request = %s", got)
	}
}

func TestIsHexAID(t *testing.T) {
	if isHexAID("8986040111111111111") {
		t.Fatal("decimal ICCID should not be identified as hex AID")
	}
	if isHexAID("894921007608519523") {
		t.Fatal("decimal ICCID should not be identified as hex AID")
	}
	if !isHexAID("A0000005591010FFFFFFFF8900000100") {
		t.Fatal("32-char hex AID should be identified as hex AID")
	}
	if !isHexAID("a0000005591010ffffffff8900000100") {
		t.Fatal("lowercase 32-char hex AID should be identified as hex AID")
	}
	if !isHexAID("A0000005591010FF") {
		t.Fatal("16-char hex AID with hex letters should be identified as hex AID")
	}
}

func TestEnableProfileCommandError0x07AndSGP22Results(t *testing.T) {
	cmdErrResp := []byte{0xBF, 0x31, 0x03, 0x80, 0x01, 0x07}
	res, ok := enableProfileResult(cmdErrResp)
	if !ok || res != 7 {
		t.Fatalf("enableProfileResult = (%d, %v), want (7, true)", res, ok)
	}
	if err := enableProfileResponseError(byte(res), cmdErrResp); !errors.Is(err, ErrESIMCommandError) {
		t.Fatalf("result 7 should map to ErrESIMCommandError, got: %v", err)
	}

	enterpriseResp := []byte{0xBF, 0x31, 0x03, 0x80, 0x01, 0x06}
	if err := enableProfileResponseError(6, enterpriseResp); !errors.Is(err, ErrESIMDisallowedByEnterpriseRule) {
		t.Fatalf("result 6 should map to ErrESIMDisallowedByEnterpriseRule, got: %v", err)
	}

	rpmResp := []byte{0xBF, 0x31, 0x03, 0x80, 0x01, 0x09}
	if err := enableProfileResponseError(9, rpmResp); !errors.Is(err, ErrESIMDisallowedForRPM) {
		t.Fatalf("result 9 should map to ErrESIMDisallowedForRPM, got: %v", err)
	}

	noPortResp := []byte{0xBF, 0x31, 0x03, 0x80, 0x01, 0x0A}
	if err := enableProfileResponseError(10, noPortResp); !errors.Is(err, ErrESIMNoEsimPortAvailable) {
		t.Fatalf("result 10 should map to ErrESIMNoEsimPortAvailable, got: %v", err)
	}
}

func TestDisableProfileCommandErrorAndSGP22Results(t *testing.T) {
	cmdErrResp := []byte{0xBF, 0x32, 0x03, 0x80, 0x01, 0x07}
	res, ok := disableProfileResult(cmdErrResp)
	if !ok || res != 7 {
		t.Fatalf("disableProfileResult = (%d, %v), want (7, true)", res, ok)
	}
	if err := disableProfileResponseError(res, cmdErrResp); !errors.Is(err, ErrESIMDisableCommandError) {
		t.Fatalf("result 7 should map to ErrESIMDisableCommandError, got: %v", err)
	}
	if err := disableProfileResponseError(6, []byte{0xBF, 0x32, 0x03, 0x80, 0x01, 0x06}); !errors.Is(err, ErrESIMDisableDisallowedByEnterprise) {
		t.Fatalf("result 6 should map to ErrESIMDisableDisallowedByEnterprise, got: %v", err)
	}
	if err := disableProfileResponseError(0x7F, []byte{0xBF, 0x32, 0x03, 0x80, 0x01, 0x7F}); !errors.Is(err, ErrESIMDisableUndefined) {
		t.Fatalf("result 0x7F should map to ErrESIMDisableUndefined, got: %v", err)
	}
}

func TestESIMSwitchProfileFallsBackToAIDOnCommandError(t *testing.T) {
	const targetICCID = "894921007608519523"
	targetAIDHex := "A0000005591010FFFFFFFF8900000101"

	manageChannelOpen := clientStep{
		command:  `AT+CSIM=10,"0070000001"`,
		response: okResponse(`+CSIM: 6,"019000"`),
	}
	selectISDR := clientStep{
		command:  fmt.Sprintf(`AT+CSIM=42,"01A4040010%s"`, isdRAID),
		response: okResponse(`+CSIM: 4,"9000"`),
	}
	enableICCIDStoreData := clientStep{
		command:  `AT+CSIM=52,"81E2910014BF3111A00C5A0A989412006780155932FF8101FF00"`,
		response: okResponse(`+CSIM: 16,"BF31038001079000"`),
	}
	enableAIDStoreData := clientStep{
		command:  `AT+CSIM=64,"81E291001ABF3117A0124F10A0000005591010FFFFFFFF89000001018101FF00"`,
		response: okResponse(`+CSIM: 16,"BF31038001009000"`),
	}
	manageChannelClose := clientStep{
		command:  `AT+CSIM=10,"0070800100"`,
		response: okResponse(`+CSIM: 4,"9000"`),
	}
	verifyICCID := clientStep{
		command:  "AT+CCID",
		response: okResponse("+CCID: " + targetICCID + "F"),
	}
	refreshATI := clientStep{
		command:  "ATI",
		response: modem.Response{Final: "ERROR"},
		err:      errors.New("refresh stub in test"),
	}

	client := &transcriptClient{steps: []clientStep{
		manageChannelOpen,
		selectISDR,
		enableICCIDStoreData,
		enableAIDStoreData,
		manageChannelClose,
		verifyICCID,
		refreshATI,
	}}
	manager, id := newStartedTestManager(t, client)
	state, _ := manager.lookup(id)
	state.candidate.Product = "TestModem"

	manager.cacheESIMInfo(id, EsimInfo{
		AID: isdRAID,
		Profiles: []EsimProfile{
			{
				ICCID: targetICCID,
				AID:   targetAIDHex,
				State: 0,
			},
		},
	})

	err := manager.ESIMSwitchProfile(context.Background(), id, targetICCID, "")
	if err != nil {
		t.Fatalf("ESIMSwitchProfile with AID fallback failed: %v", err)
	}
	client.assertDone(t)

	cached, ok := manager.cachedESIMInfo(id)
	if !ok || cached.Profiles[0].State != 1 {
		t.Fatalf("profile was not marked enabled in cache: %+v", cached)
	}
}

func TestVerifySwitchedICCIDReadsLiveModem(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{{
		command:  "AT+CCID",
		response: okResponse("+CCID: 89492026266006792824F"),
	}}}
	manager, id := newStartedTestManager(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.verifySwitchedICCID(ctx, id, "89492026266006792824"); err != nil {
		t.Fatalf("verifySwitchedICCID: %v", err)
	}
	client.assertDone(t)
}

func TestVerifySwitchedICCIDReadsML307MCCID(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CCID", response: modem.Response{Final: "ERROR"}, err: errors.New("CCID unsupported")},
		{command: "AT+QCCID", response: modem.Response{Final: "ERROR"}, err: errors.New("QCCID unsupported")},
		{command: "AT+MCCID", response: okResponse("+MCCID: 89492026266006792824F")},
	}}
	manager, id := newStartedTestManager(t, client)
	if err := manager.verifySwitchedICCIDAttempts(context.Background(), id, "89492026266006792824", 1, 0); err != nil {
		t.Fatalf("verifySwitchedICCIDAttempts: %v", err)
	}
	client.assertDone(t)
}

func TestVerifySwitchedICCIDAttemptsAllowsProactiveRefreshToSettle(t *testing.T) {
	const target = "89492026266006792824"
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CCID", response: okResponse("+CCID: 8944100000000000001F")},
		{command: "AT+CCID", response: okResponse("+CCID: " + target + "F")},
	}}
	manager, id := newStartedTestManager(t, client)
	if !manager.canVerifyProfileSwitchWithoutRestart(id) {
		t.Fatal("AT modem should be eligible for refresh verification before restart")
	}
	if err := manager.verifySwitchedICCIDAttempts(context.Background(), id, target, 2, 0); err != nil {
		t.Fatalf("verifySwitchedICCIDAttempts: %v", err)
	}
	client.assertDone(t)
}

func TestProfileSwitchRefreshProbeTimeoutIsBounded(t *testing.T) {
	for _, test := range []struct {
		command time.Duration
		want    time.Duration
	}{
		{command: 100 * time.Millisecond, want: 3 * time.Second},
		{command: 3 * time.Second, want: 7 * time.Second},
		{command: 30 * time.Second, want: 10 * time.Second},
	} {
		manager := &Manager{commandTimeout: test.command}
		if got := profileSwitchRefreshProbeTimeout(manager); got != test.want {
			t.Fatalf("command timeout %s: probe timeout = %s, want %s", test.command, got, test.want)
		}
	}
}

func TestEUMManufacturerForWatchData(t *testing.T) {
	if got := eumManufacturerForEID("35840574202500000125000001855764"); got != "WatchData Technologies Ltd." {
		t.Fatalf("manufacturer = %q", got)
	}
}

func TestEUMManufacturerForEastcompeace(t *testing.T) {
	if got := eumManufacturerForEID("89086030202200000025000015085962"); got != "Eastcompeace Technology Co., Ltd." {
		t.Fatalf("manufacturer = %q", got)
	}
}

func TestEUMManufacturerForModernThalesPrefix(t *testing.T) {
	if got := eumManufacturerForEID("89033023427100000000056707807049"); got != "Thales DIS France SAS" {
		t.Fatalf("manufacturer = %q", got)
	}
}

func TestEuiccSASTrimsCardPadding(t *testing.T) {
	payload := derConstruct(0xBF22, derEncode(0x0C, []byte("   SAS-UP-TEST   ")))
	if got := euiccSAS(payload); got != "SAS-UP-TEST" {
		t.Fatalf("SAS = %q", got)
	}
}

func TestTransientEuiccCMEClassification(t *testing.T) {
	err := fmt.Errorf("select ISD-R: %w", &modem.CommandError{
		Command: `AT+CSIM=42,"01A40400"`,
		Final:   "+CME ERROR: 0",
	})
	if !isTransientEuiccCME(err) {
		t.Fatal("wrapped +CME ERROR: 0 must be retryable")
	}
	if isTransientEuiccCME(&modem.CommandError{Final: "+CME ERROR: 13"}) {
		t.Fatal("SIM failure must not be classified as a transient SELECT error")
	}
}

func TestDiscoverEuiccAIDsFindsXeSIMAlternateISDR(t *testing.T) {
	manageChannel := clientStep{
		command:  `AT+CSIM=10,"0070000001"`,
		response: okResponse(`+CSIM: 6,"019000"`),
	}
	closeChannel := clientStep{
		command:  `AT+CSIM=10,"0070800100"`,
		response: okResponse(`+CSIM: 4,"9000"`),
	}
	selectStep := func(aid, response string) clientStep {
		return clientStep{
			command:  fmt.Sprintf(`AT+CSIM=42,"01A4040010%s"`, aid),
			response: okResponse(fmt.Sprintf(`+CSIM: 4,"%s"`, response)),
		}
	}

	client := &transcriptClient{steps: []clientStep{
		// No eSTK product applet on this card.
		manageChannel,
		selectStep(estkProductAID, "6A82"),
		closeChannel,
		// XeSIM does not expose the standard GSMA ...0100 application.
		manageChannel,
		selectStep(isdRAID, "6A82"),
		closeChannel,
		// Its dedicated ...0177 ISD-R is selectable.
		manageChannel,
		selectStep(xesimISDRAID, "9000"),
		closeChannel,
	}}
	manager, id := newStartedTestManager(t, client)

	aids := manager.discoverEuiccAIDs(context.Background(), id)
	if len(aids) != 1 || aids[0] != xesimISDRAID {
		t.Fatalf("discovered AIDs = %#v, want XeSIM %s", aids, xesimISDRAID)
	}
	client.assertDone(t)
}

func TestNativeQMIUsesUIMLogicalChannelForEUICC(t *testing.T) {
	manager, _, id := newStartedNativeQMITestManager(t)
	if err := manager.SetBackend(id, "qmi"); err != nil {
		t.Fatal(err)
	}
	session := &fakeQMIRadioSession{
		openChannel:  3,
		apduResponse: []byte{0xDE, 0xAD, 0x90, 0x00},
	}
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		return session, nil
	}
	channel, err := manager.openEuiccAID(context.Background(), id, isdRAID)
	if err != nil {
		t.Fatalf("open QMI eUICC: %v", err)
	}
	payload, sw, err := channel.transmit(context.Background(), []byte{0x80, 0xCA, 0x00, 0x00, 0x00}, 0x80)
	if err != nil {
		t.Fatalf("transmit QMI APDU: %v", err)
	}
	if !bytes.Equal(payload, []byte{0xDE, 0xAD}) || sw != 0x9000 {
		t.Fatalf("QMI APDU response = %X/%04X", payload, sw)
	}
	channel.close(context.Background())
	if len(session.openedAIDs) != 1 || strings.ToUpper(hex.EncodeToString(session.openedAIDs[0])) != isdRAID {
		t.Fatalf("opened AIDs = %X", session.openedAIDs)
	}
	if len(session.apdus) != 1 || session.apdus[0][0] != 0x83 {
		t.Fatalf("QMI APDUs = %X", session.apdus)
	}
	if len(session.closedChannels) != 1 || session.closedChannels[0] != 3 || session.closeCount != 1 {
		t.Fatalf("closed channels/session = %v/%d", session.closedChannels, session.closeCount)
	}
}

func TestEUICCChannelStuckWrapsTransientCME(t *testing.T) {
	cause := &modem.CommandError{
		Command: `AT+CSIM=10,"0070000001"`,
		Final:   "+CME ERROR: 0",
	}
	err := fmt.Errorf("%w: %v", ErrEUICCChannelStuck, cause)
	if !errors.Is(err, ErrEUICCChannelStuck) {
		t.Fatal("wrapped hot-swap channel failure must retain its sentinel")
	}
}

func TestOpenEuiccRecoversOrphanedSingleLogicalChannel(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{
			command:  `AT+CSIM=10,"0070000001"`,
			response: okResponse(`+CSIM: 6,"006A81"`),
		},
		{
			command:  `AT+CSIM=10,"0070800100"`,
			response: okResponse(`+CSIM: 4,"9000"`),
		},
		{
			command:  `AT+CSIM=10,"0070000001"`,
			response: okResponse(`+CSIM: 6,"019000"`),
		},
		{
			command: fmt.Sprintf(
				`AT+CSIM=42,"01A4040010%s"`,
				isdRAID,
			),
			response: okResponse(`+CSIM: 4,"9000"`),
		},
		{
			command:  `AT+CSIM=10,"0070800100"`,
			response: okResponse(`+CSIM: 4,"9000"`),
		},
	}}
	manager, id := newStartedTestManager(t, client)

	manager.lockESIM()
	channel, err := manager.openEuiccAID(context.Background(), id, isdRAID)
	if err == nil {
		channel.close(context.Background())
	}
	manager.unlockESIM()
	if err != nil {
		t.Fatalf("open eUICC after orphaned channel: %v", err)
	}
	client.assertDone(t)
}

func TestWaitForESIMRecovery(t *testing.T) {
	done := make(chan struct{})
	manager := &Manager{esimRecoveries: map[string]chan struct{}{"dev": done}}
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.waitForESIMRecovery(ctx, "dev"); err != nil {
		t.Fatalf("waitForESIMRecovery: %v", err)
	}

	blocked := make(chan struct{})
	manager.esimRecoveries["blocked"] = blocked
	timeoutContext, cancelTimeout := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelTimeout()
	if err := manager.waitForESIMRecovery(timeoutContext, "blocked"); err == nil {
		t.Fatal("waitForESIMRecovery must honor caller cancellation")
	}
}

func TestESIMListProfilesReturnsCacheDuringRecovery(t *testing.T) {
	done := make(chan struct{})
	manager := &Manager{
		esimRecoveries: map[string]chan struct{}{"dev": done},
		esimCache: map[string]EsimInfo{
			"dev": {Profiles: []EsimProfile{{ICCID: "old", State: 1}}},
		},
	}

	info, err := manager.ESIMListProfiles(context.Background(), "dev")
	if err != nil {
		t.Fatalf("ESIMListProfiles during recovery: %v", err)
	}
	if len(info.Profiles) != 1 || info.Profiles[0].ICCID != "old" {
		t.Fatalf("cached profiles = %#v", info.Profiles)
	}

	// The returned value must not alias the manager cache.
	info.Profiles[0].ICCID = "changed"
	cached, _ := manager.cachedESIMInfo("dev")
	if cached.Profiles[0].ICCID != "old" {
		t.Fatalf("caller mutated cache: %#v", cached.Profiles)
	}
}

func TestMarkCachedProfileEnabled(t *testing.T) {
	manager := &Manager{esimCache: map[string]EsimInfo{
		"dev": {Profiles: []EsimProfile{
			{ICCID: "old", State: 1, StateText: "old state"},
			{ICCID: "target", State: 0, StateText: "target state"},
		}},
	}}

	manager.markCachedProfileEnabled("dev", "target")
	info, ok := manager.cachedESIMInfo("dev")
	if !ok || info.Profiles[0].State != 0 || info.Profiles[0].StateText != "已禁用" {
		t.Fatalf("old profile state = %#v", info.Profiles[0])
	}
	if info.Profiles[1].State != 1 || info.Profiles[1].StateText != "已启用" {
		t.Fatalf("target profile state = %#v", info.Profiles[1])
	}
}
