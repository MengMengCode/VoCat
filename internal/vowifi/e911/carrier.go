// Package e911 implements carrier-backed emergency address provisioning.
package e911

import (
	"strings"

	"vocat/internal/vowifi"
)

type Carrier struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Method    string `json:"method"` // att_nsds, account, phone, unsupported
	HelpURL   string `json:"help_url,omitempty"`
	PortalURL string `json:"portal_url,omitempty"`
}

// Detect uses the home SIM identity, never the visited/roaming network. Brand
// rules precede host-network rules so an MVNO is not mistaken for its host.
func Detect(identity vowifi.SIMIdentity, carrierName string) Carrier {
	mcc := identity.HomeMCC
	if mcc == "" && len(identity.IMSI) >= 3 {
		mcc = identity.IMSI[:3]
	}
	if mcc < "310" || mcc > "316" || len(mcc) != 3 {
		return Carrier{}
	}
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
