package device

import (
	"fmt"
	"strings"
	"unicode"

	"vocat/internal/i18n"
)

// BlockedMCCs lists the mobile country codes whose SIM cards must not be
// served by this product. The product does not provide service to mainland
// China cards; the set mirrors the CN entry of the MCC table used for upstream
// proxy routing (460/461). It is keyed by MCC with a display name for logs and
// user-facing messaging.
var BlockedMCCs = map[string]string{
	"460": "中国",
	"461": "中国",
}

// CardMCCMNC splits an IMSI into its mobile country code and mobile network
// code. The MCC is the leading three digits and the MNC the following two or
// three. Empty strings are returned for an unusable IMSI.
func CardMCCMNC(imsi string) (mcc string, mnc string) {
	return CardMCCMNCWithLength(imsi, 0)
}

// CardMCCMNCWithLength uses the MNC length advertised by EF_AD when available.
// Without it the historical three-digit behavior is retained for callers that
// have only an IMSI.
func CardMCCMNCWithLength(imsi string, mncLength int) (mcc string, mnc string) {
	digits := strings.TrimSpace(imsi)
	if len(digits) < 5 ||
		strings.IndexFunc(digits, func(r rune) bool { return !unicode.IsDigit(r) }) >= 0 {
		return "", ""
	}
	mcc = digits[:3]
	mnc = digits[3:]
	if mncLength != 2 && mncLength != 3 {
		mncLength = 3
	}
	if len(mnc) > mncLength {
		mnc = mnc[:mncLength]
	}
	return mcc, mnc
}

// RegionBlockReason returns a human-readable reason when the SIM identified by
// the IMSI belongs to a blocked region. It returns an empty string when the
// card is allowed or when the IMSI is unavailable: only a confirmed blocked
// MCC triggers a block (fail-open), so a transient IMSI read failure never
// denies service to a legitimate card.
func RegionBlockReason(imsi string) string {
	mcc, _ := CardMCCMNC(imsi)
	country, blocked := BlockedMCCs[mcc]
	if !blocked {
		return ""
	}
	return i18n.Tf("SIM 卡归属地为%s（MCC %s），本服务不向该地区卡片提供数据/短信/VoWiFi", i18n.T(country), mcc)
}

// regionBlockError reports whether the currently inserted SIM must not be
// served. It reads only the cached snapshot IMSI and never issues an extra AT
// command, so it adds no modem round-trip and leaves guarded command
// transcripts untouched. A missing snapshot or IMSI yields nil (fail-open);
// the periodic region enforcement forces airplane mode as the authoritative
// backstop.
func (manager *Manager) regionBlockError(state *managedDevice) error {
	manager.mu.RLock()
	var snapshot *Snapshot
	if state.snapshot != nil {
		value := *state.snapshot
		snapshot = &value
	}
	manager.mu.RUnlock()
	if snapshot == nil {
		return nil
	}
	if reason := RegionBlockReason(snapshot.IMSI); reason != "" {
		return fmt.Errorf("%w: %s", ErrRegionBlocked, reason)
	}
	return nil
}
