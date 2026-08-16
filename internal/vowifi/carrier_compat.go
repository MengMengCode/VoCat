package vowifi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	CarrierProfileStandard = "standard-3gpp"
	IKEProposalModern      = "modern"
	IKEProposalLegacy      = "legacy-sha1-modp1024"
	IMSProfileStandard     = "standard"
	IMSProfileO2Germany    = "o2-germany"
	IMSProfileATT          = "att"
)

// CarrierProfile contains only interoperability choices that cannot be
// reliably discovered from the SIM or negotiated with the network. All
// protocol layers consume this common result so their carrier handling cannot
// drift into separate MCC/MNC switch statements.
type CarrierProfile struct {
	ID                 string
	MatchSource        string
	RouteMCC           string
	RouteMNC           string
	EPDG               string
	IKEProposal        string
	AdvertiseEAPOnly   bool
	IMSTransport       string
	IMSIdentityProfile string
	IMSRegisterProfile string
	IMSIPSecEncryption string
	SMSCenter          string
	PANICountry        string
	PANINode           string
	IMSDialURIScheme   string
	IMSUserEqPhone     bool
	IMSVoiceCodecs     []string
}

type carrierProfileDocument struct {
	Version  int                  `json:"version"`
	Profiles []carrierProfileRule `json:"profiles"`
}

type carrierProfileRule struct {
	ID    string              `json:"id"`
	Match carrierProfileMatch `json:"match"`
	Route carrierProfileRoute `json:"route"`
	EPDG  carrierProfileEPDG  `json:"epdg"`
	IKE   carrierProfileIKE   `json:"ike"`
	IMS   carrierProfileIMS   `json:"ims"`
}

type carrierProfileMatch struct {
	HomePLMNs     []string `json:"home_plmns"`
	IMSIPrefixes  []string `json:"imsi_prefixes"`
	ICCIDPrefixes []string `json:"iccid_prefixes"`
	SPNs          []string `json:"spns"`
	GID1Prefixes  []string `json:"gid1_prefixes"`
	GID2Prefixes  []string `json:"gid2_prefixes"`
}

type carrierProfileRoute struct {
	MCC string `json:"mcc"`
	MNC string `json:"mnc"`
}

type carrierProfileEPDG struct {
	Hostname        string   `json:"hostname"`
	DNSHosts        []string `json:"dns_hosts"`
	DNSClientSubnet string   `json:"dns_client_subnet"`
}

type carrierProfileIKE struct {
	Proposal         string `json:"proposal"`
	AdvertiseEAPOnly *bool  `json:"advertise_eap_only"`
}

type carrierProfileIMS struct {
	Transport       string   `json:"transport"`
	IdentityProfile string   `json:"identity_profile"`
	RegisterProfile string   `json:"register_profile"`
	IPSecEncryption string   `json:"ipsec_encryption"`
	SMSCenter       string   `json:"sms_center"`
	PANICountry     string   `json:"pani_country"`
	PANINode        string   `json:"pani_node"`
	DialURIScheme   string   `json:"dial_uri_scheme"`
	UserEqPhone     *bool    `json:"user_eq_phone"`
	VoiceCodecs     []string `json:"voice_codecs"`
}

//go:embed carrier_profiles.json
var carrierProfilesJSON []byte

var builtinCarrierProfiles = mustLoadCarrierProfiles(carrierProfilesJSON)

func mustLoadCarrierProfiles(encoded []byte) []carrierProfileRule {
	var document carrierProfileDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		panic("vowifi: invalid embedded carrier profiles: " + err.Error())
	}
	if document.Version != 1 {
		panic(fmt.Sprintf("vowifi: unsupported carrier profile version %d", document.Version))
	}
	seen := make(map[string]struct{}, len(document.Profiles))
	for index := range document.Profiles {
		rule := &document.Profiles[index]
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			panic("vowifi: carrier profile ID is empty")
		}
		if _, duplicate := seen[rule.ID]; duplicate {
			panic("vowifi: duplicate carrier profile " + rule.ID)
		}
		seen[rule.ID] = struct{}{}
		if !validCarrierProfileRule(*rule) {
			panic("vowifi: invalid carrier profile " + rule.ID)
		}
	}
	return document.Profiles
}

func validCarrierProfileRule(rule carrierProfileRule) bool {
	match := rule.Match
	if len(match.HomePLMNs)+len(match.IMSIPrefixes)+len(match.ICCIDPrefixes)+
		len(match.SPNs)+len(match.GID1Prefixes)+len(match.GID2Prefixes) == 0 {
		return false
	}
	for _, plmn := range match.HomePLMNs {
		if canonicalPLMNValue(plmn) == "" {
			return false
		}
	}
	if (rule.Route.MCC == "") != (rule.Route.MNC == "") ||
		(rule.Route.MCC != "" && canonicalPLMN(rule.Route.MCC, rule.Route.MNC) == "") {
		return false
	}
	if proposal := strings.TrimSpace(rule.IKE.Proposal); proposal != "" &&
		proposal != IKEProposalModern && proposal != IKEProposalLegacy {
		return false
	}
	if transport := strings.ToLower(strings.TrimSpace(rule.IMS.Transport)); transport != "" &&
		transport != "tcp" && transport != "udp" {
		return false
	}
	if encryption := strings.ToLower(strings.TrimSpace(rule.IMS.IPSecEncryption)); encryption != "" &&
		encryption != "aes-cbc" && encryption != "null" {
		return false
	}
	if country := strings.ToUpper(strings.TrimSpace(rule.IMS.PANICountry)); country != "" &&
		(len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z') {
		return false
	}
	if scheme := strings.ToLower(strings.TrimSpace(rule.IMS.DialURIScheme)); scheme != "" && scheme != "tel" && scheme != "sip" {
		return false
	}
	for _, codec := range rule.IMS.VoiceCodecs {
		switch strings.ToUpper(strings.TrimSpace(codec)) {
		case "PCMA", "PCMU", "AMR", "AMR-WB":
		default:
			return false
		}
	}
	return true
}

// ResolveCarrierProfile returns the most specific built-in match. Exact SIM
// attributes add specificity, so a constrained MVNO rule wins over its host
// PLMN without weakening the default match for unrelated subscriptions.
func ResolveCarrierProfile(identity SIMIdentity) CarrierProfile {
	resolved := CarrierProfile{
		ID:                 CarrierProfileStandard,
		MatchSource:        "standard",
		IKEProposal:        IKEProposalModern,
		AdvertiseEAPOnly:   true,
		IMSIdentityProfile: IMSProfileStandard,
		IMSRegisterProfile: IMSProfileStandard,
		IMSIPSecEncryption: "aes-cbc",
		IMSDialURIScheme:   "tel",
		IMSVoiceCodecs:     []string{"PCMA", "PCMU"},
	}
	bestScore := -1
	for _, rule := range builtinCarrierProfiles {
		score, source, matched := matchCarrierProfile(rule.Match, identity)
		if !matched || score <= bestScore {
			continue
		}
		bestScore = score
		resolved = applyCarrierProfileRule(resolved, rule, source)
	}
	return resolved
}

func matchCarrierProfile(match carrierProfileMatch, identity SIMIdentity) (int, string, bool) {
	score := 0
	sources := make([]string, 0, 6)
	if len(match.HomePLMNs) > 0 {
		wanted := canonicalPLMN(identity.HomeMCC, identity.HomeMNC)
		if wanted == "" || !matchesAny(match.HomePLMNs, func(value string) bool {
			return canonicalPLMNValue(value) == wanted
		}) {
			return 0, "", false
		}
		score += 100
		sources = append(sources, "hplmn")
	}
	for _, selector := range []struct {
		name     string
		weight   int
		values   []string
		actual   string
		foldCase bool
	}{
		{name: "imsi", weight: 80, values: match.IMSIPrefixes, actual: identity.IMSI},
		{name: "iccid", weight: 70, values: match.ICCIDPrefixes, actual: identity.ICCID},
		{name: "gid1", weight: 50, values: match.GID1Prefixes, actual: identity.GID1, foldCase: true},
		{name: "gid2", weight: 40, values: match.GID2Prefixes, actual: identity.GID2, foldCase: true},
	} {
		if len(selector.values) == 0 {
			continue
		}
		actual := strings.TrimSpace(selector.actual)
		if actual == "" || !matchesAny(selector.values, func(prefix string) bool {
			prefix = strings.TrimSpace(prefix)
			if selector.foldCase {
				return strings.HasPrefix(strings.ToLower(actual), strings.ToLower(prefix))
			}
			return strings.HasPrefix(actual, prefix)
		}) {
			return 0, "", false
		}
		score += selector.weight
		sources = append(sources, selector.name)
	}
	if len(match.SPNs) > 0 {
		spn := strings.TrimSpace(identity.SPN)
		if spn == "" || !matchesAny(match.SPNs, func(value string) bool {
			return strings.EqualFold(strings.TrimSpace(value), spn)
		}) {
			return 0, "", false
		}
		score += 20
		sources = append(sources, "spn")
	}
	return score, strings.Join(sources, "+"), score > 0
}

func matchesAny(values []string, match func(string) bool) bool {
	for _, value := range values {
		if match(value) {
			return true
		}
	}
	return false
}

func applyCarrierProfileRule(base CarrierProfile, rule carrierProfileRule, source string) CarrierProfile {
	base.ID = rule.ID
	base.MatchSource = source
	base.RouteMCC = strings.TrimSpace(rule.Route.MCC)
	base.RouteMNC = strings.TrimSpace(rule.Route.MNC)
	base.EPDG = strings.ToLower(strings.TrimSpace(rule.EPDG.Hostname))
	if value := strings.TrimSpace(rule.IKE.Proposal); value != "" {
		base.IKEProposal = value
	}
	if rule.IKE.AdvertiseEAPOnly != nil {
		base.AdvertiseEAPOnly = *rule.IKE.AdvertiseEAPOnly
	}
	if value := strings.ToLower(strings.TrimSpace(rule.IMS.Transport)); value != "" {
		base.IMSTransport = value
	}
	if value := strings.TrimSpace(rule.IMS.IdentityProfile); value != "" {
		base.IMSIdentityProfile = value
	}
	if value := strings.TrimSpace(rule.IMS.RegisterProfile); value != "" {
		base.IMSRegisterProfile = value
	}
	if value := strings.ToLower(strings.TrimSpace(rule.IMS.IPSecEncryption)); value != "" {
		base.IMSIPSecEncryption = value
	}
	base.SMSCenter = strings.TrimSpace(rule.IMS.SMSCenter)
	base.PANICountry = strings.ToUpper(strings.TrimSpace(rule.IMS.PANICountry))
	base.PANINode = strings.TrimSpace(rule.IMS.PANINode)
	if value := strings.ToLower(strings.TrimSpace(rule.IMS.DialURIScheme)); value != "" {
		base.IMSDialURIScheme = value
	}
	if rule.IMS.UserEqPhone != nil {
		base.IMSUserEqPhone = *rule.IMS.UserEqPhone
	}
	if len(rule.IMS.VoiceCodecs) > 0 {
		base.IMSVoiceCodecs = normalizeVoiceCodecs(rule.IMS.VoiceCodecs)
	}
	return base
}

func normalizeVoiceCodecs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func canonicalPLMN(mcc, mnc string) string {
	mcc = strings.TrimSpace(mcc)
	mnc = strings.TrimSpace(mnc)
	if !isNDigits(mcc, 3, 3) || !isNDigits(mnc, 2, 3) {
		return ""
	}
	for len(mnc) < 3 {
		mnc = "0" + mnc
	}
	return mcc + mnc
}

func canonicalPLMNValue(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "/", ""))
	if len(value) != 5 && len(value) != 6 {
		return ""
	}
	return canonicalPLMN(value[:3], value[3:])
}

// AssignedRoutePLMN remains available to callers that only have the legacy
// identifier pair. New code resolves the complete SIMIdentity so SPN/GID
// selectors can participate.
func AssignedRoutePLMN(iccid, imsi string) (string, string, bool) {
	identity := SIMIdentity{ICCID: strings.TrimSpace(iccid), IMSI: strings.TrimSpace(imsi)}
	if len(identity.IMSI) >= 5 {
		identity.HomeMCC = identity.IMSI[:3]
		for _, length := range []int{3, 2} {
			if len(identity.IMSI) < 3+length {
				continue
			}
			identity.HomeMNC = identity.IMSI[3 : 3+length]
			profile := ResolveCarrierProfile(identity)
			if profile.RouteMCC != "" {
				return profile.RouteMCC, profile.RouteMNC, true
			}
		}
	}
	return "", "", false
}

func IsATT310280(identity SIMIdentity) bool {
	return ResolveCarrierProfile(identity).IMSRegisterProfile == IMSProfileATT
}

func applyAssignedCarrierRoute(identity SIMIdentity) SIMIdentity {
	if strings.TrimSpace(identity.EPDG) != "" {
		return identity
	}
	profile := ResolveCarrierProfile(identity)
	switch {
	case profile.EPDG != "":
		identity.EPDG = profile.EPDG
	case profile.RouteMCC != "":
		identity.EPDG = standardEPDGHostname(profile.RouteMCC, profile.RouteMNC)
	}
	return identity
}

// EPDGDNSClientSubnet returns a deliberately scoped EDNS client subnet for an
// ePDG whose authoritative DNS only exposes addresses to home-country
// resolvers. An empty result means ordinary system DNS remains authoritative.
func EPDGDNSClientSubnet(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, rule := range builtinCarrierProfiles {
		for _, candidate := range rule.EPDG.DNSHosts {
			if host == strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), ".")) {
				return strings.TrimSpace(rule.EPDG.DNSClientSubnet)
			}
		}
	}
	return ""
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
