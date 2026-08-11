package server

import (
	"encoding/json"
	"testing"

	"vocat/internal/store"
)

func TestDeviceCarrierProfilePayloadAndResponse(t *testing.T) {
	var payload deviceConfigPayload
	if err := json.Unmarshal([]byte(`{
		"id":"modem-1",
		"name":"410",
		"device_type":"wifi_410",
		"apn":"internet",
		"ims_apn":"ims",
		"ims_private_identity":"impi@example.net",
		"ims_public_identity":"sip:+12025550123@example.net",
		"ims_sms_center":"+12025550100",
		"ims_transport":"udp",
		"ims_allow_imsi_derived_identity":false,
		"vowifi_eap_method":"aka-prime",
		"vowifi_allow_sha1":true,
		"vowifi_use_modp1024":true
	}`), &payload); err != nil {
		t.Fatal(err)
	}
	got := payload.toStoreDevice()
	if got.APN != "internet" || got.IMSAPN != "ims" ||
		got.IMSPrivateIdentity != "impi@example.net" ||
		got.IMSPublicIdentity != "sip:+12025550123@example.net" ||
		got.IMSSMSCenter != "+12025550100" || got.IMSTransport != "udp" ||
		got.IMSAllowIMSIDerivedIdentity || got.VoWiFiEAPMethod != "aka-prime" ||
		!got.VoWiFiAllowSHA1 || !got.VoWiFiUseMODP1024 {
		t.Fatalf("carrier profile payload = %+v", got)
	}
	response := storedDeviceConfig(got)
	for key, want := range map[string]any{
		"apn": "internet", "ims_apn": "ims",
		"ims_private_identity":            "impi@example.net",
		"ims_public_identity":             "sip:+12025550123@example.net",
		"ims_sms_center":                  "+12025550100",
		"ims_transport":                   "udp",
		"ims_allow_imsi_derived_identity": false,
		"vowifi_eap_method":               "aka-prime",
		"vowifi_allow_sha1":               true,
		"vowifi_use_modp1024":             true,
	} {
		if response[key] != want {
			t.Errorf("response[%q] = %#v, want %#v", key, response[key], want)
		}
	}
}

func TestDeviceCarrierProfileDefaultsDoNotReuseCellularAPN(t *testing.T) {
	got := (deviceConfigPayload{ID: "modem-1", Name: "410", DeviceType: store.DeviceTypeWiFi410, APN: "internet"}).toStoreDevice()
	if got.IMSAPN != "ims" {
		t.Fatalf("IMS APN = %q, want independent default ims", got.IMSAPN)
	}
	if got.IMSTransport != "tcp" || got.VoWiFiEAPMethod != "aka" {
		t.Fatalf("carrier defaults = %+v", got)
	}
	if !got.IMSAllowIMSIDerivedIdentity {
		t.Fatal("new device must preserve standards-based IMSI identity derivation by default")
	}
	if got.VoWiFiAllowSHA1 || got.VoWiFiUseMODP1024 {
		t.Fatal("new device must not enable weak legacy IKE crypto by default")
	}
}
