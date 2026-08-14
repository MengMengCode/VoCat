package vowifi

import (
	"fmt"
	"strings"
)

// CarrierProfile is the small, protocol-facing portion of a carrier policy
// that VoCat needs while starting a session.  It deliberately contains no
// subscriber secret or operator credential.  The profile is resolved from the
// live home PLMN, not from the serving network name.
//
// The preset name is kept compatible with VoHive so diagnostics from the two
// implementations can be compared directly (for example O2_de_26203).
type CarrierProfile struct {
	MCC               string `json:"mcc,omitempty"`
	MNC               string `json:"mnc,omitempty"`
	PLMN              string `json:"plmn,omitempty"`
	PresetID          string `json:"preset_id,omitempty"`
	Source            string `json:"source,omitempty"`
	EPDG              string `json:"epdg,omitempty"`
	IMSAPN            string `json:"ims_apn,omitempty"`
	EAPMethod         string `json:"eap_method,omitempty"`
	IMSTransport      string `json:"ims_transport,omitempty"`
	AllowSHA1         bool   `json:"allow_sha1,omitempty"`
	UseMODP1024       bool   `json:"use_modp1024,omitempty"`
	IMSIdentitySource string `json:"ims_identity_source,omitempty"`
}

const (
	CarrierSourceBuiltin  = "builtin"
	CarrierSourceFallback = "standard_fallback"
	IMSIdentityDerived    = "imsi_derived"
	IMSIdentityExplicit   = "explicit"
)

// ResolveCarrierProfile applies the same MCC/MNC matching rule as VoHive's
// carrier resolver.  The explicit home PLMN supplied by the SIM reader wins;
// no MNC length is guessed from a display/operator name.
func ResolveCarrierProfile(identity SIMIdentity) CarrierProfile {
	mcc := strings.TrimSpace(identity.HomeMCC)
	mnc := strings.TrimSpace(identity.HomeMNC)
	if !isNDigits(mcc, 3, 3) || !isNDigits(mnc, 2, 3) {
		return CarrierProfile{
			Source:            CarrierSourceFallback,
			IMSAPN:            "ims",
			EAPMethod:         "aka",
			IMSTransport:      "tcp",
			IMSIdentitySource: IMSIdentityDerived,
		}
	}
	canonicalMNC := mnc
	for len(canonicalMNC) < 3 {
		canonicalMNC = "0" + canonicalMNC
	}
	plmn := mcc + canonicalMNC
	profile := CarrierProfile{
		MCC:               mcc,
		MNC:               canonicalMNC,
		PLMN:              plmn,
		PresetID:          plmn,
		Source:            CarrierSourceFallback,
		IMSAPN:            "ims",
		EAPMethod:         "aka",
		IMSTransport:      "tcp",
		IMSIdentitySource: IMSIdentityDerived,
	}
	switch plmn {
	case "262003":
		// O2 Germany / Telefónica Germany.  The network currently requires the
		// SHA-1 compatibility offer in addition to the strong-first proposal.
		profile.PresetID = "O2_de_26203"
		profile.Source = CarrierSourceBuiltin
		profile.AllowSHA1 = true
	case "234015":
		profile.PresetID = "Vodafone_uk_23415"
		profile.Source = CarrierSourceBuiltin
	case "515002":
		// Globe Philippines publishes a dedicated static ePDG hostname rather
		// than the standard PLMN-derived name. Keep the hostname here so DNS
		// resolution follows the operator profile and remains dynamic.
		profile.PresetID = "Globe_PH_51502"
		profile.Source = CarrierSourceBuiltin
		profile.EPDG = "weconnect.globe.com.ph"
	}
	return profile
}

func (profile CarrierProfile) String() string {
	if profile.PresetID == "" {
		return "standard_fallback"
	}
	return fmt.Sprintf("%s (%s)", profile.PresetID, profile.Source)
}
