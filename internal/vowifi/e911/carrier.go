// Package e911 implements carrier-backed emergency address provisioning.
package e911

import (
	"strings"

	"vocat/internal/vowifi"
)

type Carrier struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Method    string `json:"method"` // att_nsds, msp_websheet, ts43_websheet
	Network   string `json:"network,omitempty"`
	Verified  bool   `json:"verified"` // real-SIM end-to-end validation, not protocol tests
	HelpURL   string `json:"help_url,omitempty"`
	PortalURL string `json:"portal_url,omitempty"`
}

// Detect uses the home SIM identity, never the visited/roaming network. Brand
// rules precede host-network rules so an MVNO is not mistaken for its host.
func Detect(identity vowifi.SIMIdentity, carrierName string) (carrier Carrier) {
	mcc := identity.HomeMCC
	if mcc == "" && len(identity.IMSI) >= 3 {
		mcc = identity.IMSI[:3]
	}
	if mcc < "310" || mcc > "316" || len(mcc) != 3 {
		return Carrier{}
	}
	// Preserve the brand's own account/help URLs, while choosing the SIM
	// authentication protocol from its home network. Multi-network MVNOs must
	// never be pinned to one host network merely because their brand matches.
	defer func() {
		carrier.Network = homeNetwork(identity)
		switch carrier.Network {
		case "att":
			carrier.Method = "att_nsds"
		case "tmobile", "verizon":
			carrier.Method = "msp_websheet"
		default:
			carrier.Method = "ts43_websheet"
		}
		carrier.Verified = carrier.ID == "redpocket" && carrier.Network == "att"
	}()
	name := strings.ToLower(carrierName + " " + identity.SPN)
	profile := vowifi.ResolveCarrierProfile(identity)
	att := profile.EPDG == "epdg.epc.att.net" || identity.EPDG == "epdg.epc.att.net"
	if identity.HomeMCC == "310" {
		switch identity.HomeMNC {
		case "170", "280", "410", "150", "950":
			att = true
		}
	}
	redpocket := strings.Contains(name, "redpocket") || strings.Contains(name, "red pocket") || strings.Contains(profile.ID, "redpocket")
	if redpocket {
		c := Carrier{ID: "redpocket", Name: "RedPocket", Method: "account", HelpURL: "https://help.redpocket.com/what-is-wifi-calling-and-can-i-use-it-with-my-red-pocket-service", PortalURL: "https://www.redpocket.com/my-lines"}
		if att {
			c.Method = "att_nsds"
			c.Name = "RedPocket GSMA / AT&T"
		}
		return c
	}
	for _, rule := range []struct {
		match string
		value Carrier
	}{
		{"tello", Carrier{ID: "tello", Name: "Tello", Method: "account", PortalURL: "https://tello.com/account/my_settings", HelpURL: "https://tello.com/help_center/how-can-i-use-wi-fi-calling"}},
		{"mint", Carrier{ID: "mint", Name: "Mint Mobile", Method: "account", PortalURL: "https://my.mintmobile.com/", HelpURL: "https://www.mintmobile.com/help/what-is-wifi-calling-how-do-i-enable-it/"}},
		{"ultra", Carrier{ID: "ultra", Name: "Ultra Mobile", Method: "account", PortalURL: "https://my.ultramobile.com/", HelpURL: "https://www.ultramobile.com/wifi-calling/"}},
		{"us mobile", Carrier{ID: "usmobile", Name: "US Mobile", Method: "account", PortalURL: "https://app.usmobile.com/", HelpURL: "https://www.usmobile.com/help/docs/troubleshooting-and-setup/setting-up-device/wifi-calling"}},
		{"google fi", Carrier{ID: "googlefi", Name: "Google Fi", Method: "account", PortalURL: "https://fi.google.com/account", HelpURL: "https://support.google.com/fi/answer/6174034"}},
		{"visible", Carrier{ID: "visible", Name: "Visible", Method: "phone", PortalURL: "https://www.visible.com/account", HelpURL: "https://www.visible.com/help/wi-fi-calling"}},
		{"cricket", Carrier{ID: "cricket", Name: "Cricket Wireless", PortalURL: "https://www.cricketwireless.com/myaccount/", HelpURL: "https://www.cricketwireless.com/support/mobile-wifi/wifi-calling"}},
		{"metro", Carrier{ID: "metro", Name: "Metro by T-Mobile", PortalURL: "https://www.metrobyt-mobile.com/my-account", HelpURL: "https://www.metrobyt-mobile.com/support/product-support/wi-fi-calling"}},
		{"boost", Carrier{ID: "boost", Name: "Boost Mobile", PortalURL: "https://www.boostmobile.com/my-account"}},
		{"xfinity", Carrier{ID: "xfinity", Name: "Xfinity Mobile", PortalURL: "https://www.xfinity.com/mobile/my-account"}},
		{"spectrum", Carrier{ID: "spectrum", Name: "Spectrum Mobile", PortalURL: "https://www.spectrum.net/"}},
		{"consumer cellular", Carrier{ID: "consumer-cellular", Name: "Consumer Cellular", PortalURL: "https://www.consumercellular.com/login"}},
		{"straight talk", Carrier{ID: "straight-talk", Name: "Straight Talk", PortalURL: "https://www.straighttalk.com/my-account"}},
		{"tracfone", Carrier{ID: "tracfone", Name: "Tracfone", PortalURL: "https://www.tracfone.com/my-account"}},
		{"total", Carrier{ID: "total", Name: "Total Wireless", PortalURL: "https://www.totalwireless.com/my-account"}},
		{"us cellular", Carrier{ID: "uscellular", Name: "UScellular", PortalURL: "https://www.uscellular.com/"}},
		{"uscellular", Carrier{ID: "uscellular", Name: "UScellular", PortalURL: "https://www.uscellular.com/"}},
		{"c spire", Carrier{ID: "cspire", Name: "C Spire", PortalURL: "https://www.cspire.com/"}},
		{"cspire", Carrier{ID: "cspire", Name: "C Spire", PortalURL: "https://www.cspire.com/"}},
		{"cellcom", Carrier{ID: "cellcom", Name: "Cellcom", PortalURL: "https://www.cellcom.com/"}},
	} {
		if strings.Contains(name, rule.match) {
			return rule.value
		}
	}
	if att {
		label := strings.TrimSpace(carrierName)
		if label == "" {
			label = "AT&T"
		}
		return Carrier{ID: "att", Name: label, Method: "att_nsds", HelpURL: "https://www.att.com/support/article/wireless/KM1063258/"}
	}
	if strings.Contains(name, "t-mobile") || strings.Contains(name, "tmobile") || (mcc == "310" && (identity.HomeMNC == "260" || identity.HomeMNC == "240")) {
		return Carrier{ID: "tmobile", Name: "T-Mobile", Method: "account", PortalURL: "https://www.t-mobile.com/account/profile", HelpURL: "https://www.t-mobile.com/support/account/change-your-billing-and-e911-address"}
	}
	if strings.Contains(name, "verizon") || (mcc == "311" && identity.HomeMNC == "480") {
		return Carrier{ID: "verizon", Name: "Verizon", Method: "phone", HelpURL: "https://www.verizon.com/support/knowledge-base-201651/"}
	}
	return Carrier{ID: "us-other", Name: strings.TrimSpace(carrierName), Method: "unsupported"}
}

func homeNetwork(identity vowifi.SIMIdentity) string {
	profile := vowifi.ResolveCarrierProfile(identity)
	if profile.EPDG == "epdg.epc.att.net" || identity.EPDG == "epdg.epc.att.net" {
		return "att"
	}
	switch identity.HomeMCC + identity.HomeMNC {
	case "310170", "310280", "310410", "310150", "310950":
		return "att"
	case "310260", "310240", "310160", "310200", "310210", "310220", "310230", "310250", "310310", "310660", "310800", "311660", "311882":
		return "tmobile"
	case "311480", "311481", "311482", "311483", "311484", "311485", "311486", "311487", "311488", "311489", "310004", "310005", "310006", "310012":
		return "verizon"
	}
	return ""
}
