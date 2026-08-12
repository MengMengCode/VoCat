package device

import (
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
		esimTestProfile(t, "89441000400128014257", "Vodafone UK", "Vodafone UK eSIM", 0x00),
		esimTestProfile(t, "89441000430011604140", "Vodafone UK", "Vodafone UK eSIM", 0x01),
		esimTestProfile(t, "89852351225001058508", "Webbing", "WEBBING", 0x00),
	)
	payload := tlv([]byte{0xBF, 0x2D}, body)

	profiles := parseProfilesInfo(payload)
	if len(profiles) != 3 {
		t.Fatalf("expected 3 profiles, got %d: %#v", len(profiles), profiles)
	}
	if profiles[0].ICCID != "89441000400128014257" || profiles[0].State != 0 {
		t.Fatalf("profile[0] = %#v", profiles[0])
	}
	if profiles[1].ICCID != "89441000430011604140" || profiles[1].State != 1 || profiles[1].StateText != "已启用" {
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
	real := esimTestProfile(t, "89441000400316048687", "Vodafone UK", "Vodafone UK eSIM", 0x01)
	duplicate := esimTestProfile(t, "89441000400316048687", "Duplicate", "Duplicate", 0x00)
	metadata := tlv([]byte{0xE3}, tlv([]byte{0x80}, []byte{0x01}))
	empty := tlv([]byte{0xE3})
	payload := tlv([]byte{0xBF, 0x2D}, tlv([]byte{0xA0}, metadata, real, empty, duplicate))

	profiles := parseProfilesInfo(payload)
	if len(profiles) != 1 {
		t.Fatalf("profiles = %#v, want one addressable profile", profiles)
	}
	if profiles[0].ICCID != "89441000400316048687" || profiles[0].Name != "Vodafone UK eSIM" {
		t.Fatalf("profile = %#v", profiles[0])
	}
}

func TestICCIDRoundTrip(t *testing.T) {
	for _, digits := range []string{"89441000400128014257", "8985235122500105850", "1"} {
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
	if _, err := encodeICCID("894410004001280142571"); err == nil {
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
	request, err := buildDeleteProfileRequest("89441000400128014257")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(request)); got != "BF330C5A0A98440100041082102475" {
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
	request, err := buildSetNicknameRequest("89441000400128014257", "Test")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(request)); got != "BF29125A0A98440100041082102475900454657374" {
		t.Fatalf("SetNickname request = %s", got)
	}
	result, ok := setNicknameResult([]byte{0xBF, 0x29, 0x03, 0x80, 0x01, 0x00})
	if !ok || result != 0 {
		t.Fatalf("SetNickname result = (%d, %v)", result, ok)
	}
	if _, err := buildSetNicknameRequest("89441000400128014257", strings.Repeat("名", 65)); !errors.Is(err, ErrESIMNicknameTooLong) {
		t.Fatalf("long nickname error = %v", err)
	}
}

func TestDisableProfileRequestAndResult(t *testing.T) {
	request, err := buildDisableProfileRequest("89441000400128014257")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ToUpper(hex.EncodeToString(request)); got != "BF3211A00C5A0A984401000410821024758101FF" {
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
	manager := &Manager{
		esimCache: map[string]EsimInfo{
			"dev": {Profiles: []EsimProfile{
				{ICCID: "old", Name: "Old profile", State: 1, StateText: "old state"},
				{ICCID: "target", Name: "Target profile", State: 0, StateText: "target state"},
			}},
		},
		esimActiveName: map[string]string{"dev": "Old profile"},
	}

	manager.markCachedProfileEnabled("dev", "target")
	info, ok := manager.cachedESIMInfo("dev")
	if !ok || info.Profiles[0].State != 0 || info.Profiles[0].StateText != "已禁用" {
		t.Fatalf("old profile state = %#v", info.Profiles[0])
	}
	if info.Profiles[1].State != 1 || info.Profiles[1].StateText != "已启用" {
		t.Fatalf("target profile state = %#v", info.Profiles[1])
	}
	if got := manager.ActiveESIMProfileName("dev"); got != "Target profile" {
		t.Fatalf("active profile name = %q, want target profile", got)
	}
}

func TestMergeVerifiedProfileSnapshotPublishesLiveICCID(t *testing.T) {
	manager, id := newStartedTestManager(t, &transcriptClient{})
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	manager.setResult(id, state, &Snapshot{
		DeviceID: id,
		ICCID:    "old",
		IMSI:     "old-imsi",
		SIMReady: true,
	}, nil)
	manager.beginRecovery(id, state)
	manager.mergeVerifiedProfileSnapshot(id, "target")
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Snapshot == nil || entry.Snapshot.ICCID != "target" || entry.Snapshot.IMSI != "old-imsi" || !entry.Snapshot.SIMReady || !entry.Snapshot.Responsive || entry.Recovering {
		t.Fatalf("snapshot after verified merge = %#v", entry.Snapshot)
	}
}

func TestActiveESIMProfileNameUsesEnabledCachedProfile(t *testing.T) {
	manager := &Manager{esimCache: map[string]EsimInfo{
		"wwan0": {Profiles: []EsimProfile{
			{ICCID: "89441000400316034372", Name: "Vodafone UK eSIM", State: 0},
			{ICCID: "89636624020107210949", Name: "DITO +639910074034", ServiceProvider: "DITO", State: 1},
		}},
	}}

	if got := manager.ActiveESIMProfileName("wwan0"); got != "DITO +639910074034" {
		t.Fatalf("active eSIM profile name = %q", got)
	}
	if got := manager.ActiveESIMProfileName("missing"); got != "" {
		t.Fatalf("missing active eSIM profile name = %q", got)
	}

	manager.cacheActiveESIMProfileName("wwan0", []EsimInventoryEntry{{
		Info: EsimInfo{Profiles: []EsimProfile{{
			ICCID: "89636624020107210949", Name: "", ServiceProvider: "DITO", State: 1,
		}}},
	}})
	if got := manager.ActiveESIMProfileName("wwan0"); got != "DITO" {
		t.Fatalf("inventory active eSIM profile name = %q", got)
	}
}
