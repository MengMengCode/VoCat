package vowifi

import (
	"fmt"
	"strings"
)

const att310280EPDG = "epdg.epc.att.net"

// AssignedRoutePLMN returns a narrowly matched ePDG route PLMN without
// changing the subscription PLMN used for AKA identities. Some multi-profile
// and MVNO SIMs authenticate against their own HPLMN but use a host network's
// VoWiFi access gateway.
func AssignedRoutePLMN(iccid, imsi string) (string, string, bool) {
	iccid = strings.TrimSpace(iccid)
	imsi = strings.TrimSpace(imsi)
	switch {
	case strings.HasPrefix(iccid, "894416") && strings.HasPrefix(imsi, "204047"):
		// XeSIM/Lebara: keep 204/04 for AKA and use Vodafone UK's ePDG.
		return "234", "15", true
	case strings.HasPrefix(iccid, "894430") && strings.HasPrefix(imsi, "23433"):
		// CTExcel UK: keep 234/33 for AKA and use the EE UK ePDG used by
		// the initial VoWiFi provisioning path.
		return "234", "30", true
	default:
		return "", "", false
	}
}

// IsATT310280 reports whether the live subscription is on AT&T's three-digit
// 310/280 PLMN. It is shared by SWu and IMS so the carrier exception cannot
// drift between protocol layers.
func IsATT310280(identity SIMIdentity) bool {
	mcc := strings.TrimSpace(identity.HomeMCC)
	mnc := strings.TrimLeft(strings.TrimSpace(identity.HomeMNC), "0")
	imsi := strings.TrimSpace(identity.IMSI)
	return mcc == "310" && mnc == "280" && strings.HasPrefix(imsi, "310280")
}

func applyAssignedCarrierRoute(identity SIMIdentity) SIMIdentity {
	if strings.TrimSpace(identity.EPDG) != "" {
		return identity
	}
	if routeMCC, routeMNC, ok := AssignedRoutePLMN(identity.ICCID, identity.IMSI); ok {
		identity.EPDG = standardEPDGHostname(routeMCC, routeMNC)
	}
	return identity
}

func standardEPDGHostname(mcc, mnc string) string {
	mnc = strings.TrimSpace(mnc)
	for len(mnc) < 3 {
		mnc = "0" + mnc
	}
	return fmt.Sprintf(
		"epdg.epc.mnc%s.mcc%s.pub.3gppnetwork.org",
		mnc,
		strings.TrimSpace(mcc),
	)
}
