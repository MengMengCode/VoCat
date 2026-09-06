package ims

import (
	"net"
	"strings"
	"testing"

	"vocat/internal/vowifi"
)

// Exercise the real session constructor with fresh providers and connections,
// rather than testing a helper or relying on provider-local cached state.
func newStableInstanceTestSession(t *testing.T, request vowifi.IMSRequest) *Session {
	t.Helper()
	provider, err := NewProvider(&recordingAKA{}, Config{SecurityMode: SecurityDisabled})
	if err != nil {
		t.Fatal(err)
	}
	connection, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = connection.Close() })
	identity, err := deriveIdentities(request.Identity, provider.config)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newSession(provider, request, identity, pcscfEndpoint{host: "192.0.2.1", port: 5060}, "tcp", connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.abort)
	return session
}

func TestNewSessionStableInstanceFallback(t *testing.T) {
	base := vowifi.IMSRequest{DeviceID: " modem0 ", Identity: vowifi.SIMIdentity{
		IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01",
	}}
	first := newStableInstanceTestSession(t, base)
	for _, imei := range []string{"", "bad-imei", "35302411255701", "3530241125570100", "35302411255701x", "３５３０２４１１２５５７０１０"} {
		request := base
		request.DeviceID = "modem0"
		request.Identity.IMEI = imei
		request.Identity.IMSI = "001010987654321"
		if next := newStableInstanceTestSession(t, request); next.instanceID != first.instanceID {
			t.Errorf("unavailable IMEI %q: DeviceID fallback changed: %q != %q", imei, next.instanceID, first.instanceID)
		}
	}
	base.DeviceID = "modem1"
	if next := newStableInstanceTestSession(t, base); next.instanceID == first.instanceID {
		t.Error("different DeviceIDs share an instance")
	}
	base.DeviceID = " "
	legacy := newStableInstanceTestSession(t, base)
	if next := newStableInstanceTestSession(t, base); next.instanceID != legacy.instanceID {
		t.Error("legacy IMSI-only caller is not stable")
	}
	base.Identity.IMSI = "001010987654321"
	if next := newStableInstanceTestSession(t, base); next.instanceID == legacy.instanceID {
		t.Error("different IMSI-only callers share an instance")
	}
}

func TestNewSessionStableInstanceGSMA(t *testing.T) {
	request := vowifi.IMSRequest{DeviceID: "modem0", Identity: vowifi.SIMIdentity{
		IMEI: "353024112557010", IMSI: "234100000000001", HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF",
	}}
	for range 2 {
		session := newStableInstanceTestSession(t, request)
		if session.instanceID != "urn:gsma:imei:353024112557010-0" {
			t.Fatalf("GSMA representation changed: %q", session.instanceID)
		}
	}
	request.Identity.IMEI = "invalid"
	first := newStableInstanceTestSession(t, request)
	if next := newStableInstanceTestSession(t, request); first.instanceID != next.instanceID || !strings.HasPrefix(next.instanceID, "urn:uuid:") {
		t.Fatal("GSMA invalid-IMEI fallback must use stable UUID")
	}
}

func TestStableInstanceMissingIdentity(t *testing.T) {
	connection, peer := net.Pipe()
	defer connection.Close()
	defer peer.Close()
	session, err := newSession(&Provider{config: Config{SecurityMode: SecurityDisabled}}, vowifi.IMSRequest{}, identitySet{}, pcscfEndpoint{}, "tcp", connection)
	if session != nil {
		session.abort()
	}
	if err == nil || !strings.Contains(err.Error(), "stable SIP instance") {
		t.Fatalf("missing all identities: session=%v, error=%v", session, err)
	}
}

func TestNewSessionStableInstance(t *testing.T) {
	base := vowifi.IMSRequest{DeviceID: "usb-1", Identity: vowifi.SIMIdentity{
		IMEI: "353024112557010", IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01",
	}}
	first := newStableInstanceTestSession(t, base)
	// Independent Python uuid.uuid5(NAMESPACE_URL, name) vector locks down
	// the namespace/name and RFC version/variant across process restarts.
	if first.instanceID != "urn:uuid:eddf02e1-07af-5305-bad0-a7ce44dd536d" {
		t.Fatalf("UUIDv5 compatibility vector: got %q", first.instanceID)
	}
	for _, test := range []struct {
		name     string
		change   func(*vowifi.IMSRequest)
		wantSame bool
	}{
		{"new_provider_and_connection", func(r *vowifi.IMSRequest) {}, true},
		{"different_hardware", func(r *vowifi.IMSRequest) { r.Identity.IMEI = "490154203237518" }, false},
		{"changed_USB_device_ID", func(r *vowifi.IMSRequest) { r.DeviceID = "usb-2" }, true},
		{"changed_IMSI", func(r *vowifi.IMSRequest) { r.Identity.IMSI = "001010987654321" }, true},
		{"changed_SIM_profile", func(r *vowifi.IMSRequest) {
			r.Identity.ICCID = "8901000000000000002"
			r.Identity.HomeMCC = "999"
			r.Identity.HomeMNC = "99"
		}, true},
		{"IMEI_without_device_ID", func(r *vowifi.IMSRequest) { r.DeviceID = "" }, true},
		{"trimmed_IMEI", func(r *vowifi.IMSRequest) { r.Identity.IMEI = " 353024112557010\n" }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.change(&request)
			next := newStableInstanceTestSession(t, request)
			if same := first.instanceID == next.instanceID; same != test.wantSame {
				t.Errorf("instance IDs %q and %q: same=%v, want %v", first.instanceID, next.instanceID, same, test.wantSame)
			}
			if first.callID == next.callID || first.fromTag == next.fromTag {
				t.Error("Call-ID and From tag must remain fresh per session")
			}
			if !strings.Contains(next.buildContact("192.0.2.10:5060", next.imsRegisterOptions()), `+sip.instance="<`+next.instanceID+`>"`) {
				t.Error("Contact does not carry the session instance ID")
			}
		})
	}
}
