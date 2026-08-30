package vowifi

import "testing"

func TestResolveIMSDeviceIdentityPreservesModemIMEI(t *testing.T) {
	imei, userAgent := ResolveIMSDeviceIdentity(SIMIdentity{IMEI: "353024112557010", IMSI: "234150123456789"})
	if imei != "353024112557010" || userAgent != "" {
		t.Fatalf("ResolveIMSDeviceIdentity() = %q, %q", imei, userAgent)
	}
}

func TestResolveIMSDeviceIdentityUsesPairedStableFallback(t *testing.T) {
	identity := SIMIdentity{IMSI: "234150123456789"}
	firstIMEI, firstUserAgent := ResolveIMSDeviceIdentity(identity)
	secondIMEI, secondUserAgent := ResolveIMSDeviceIdentity(identity)
	if firstIMEI != "358983364567896" || firstUserAgent != "iOS/18.6.2 iPhone" {
		t.Fatalf("fallback identity = %q, %q", firstIMEI, firstUserAgent)
	}
	if firstIMEI != secondIMEI || firstUserAgent != secondUserAgent {
		t.Fatalf("fallback identity is not stable: %q/%q != %q/%q", firstIMEI, firstUserAgent, secondIMEI, secondUserAgent)
	}
}

func TestResolveIMSDeviceIdentityLeavesMissingInputsUnchanged(t *testing.T) {
	imei, userAgent := ResolveIMSDeviceIdentity(SIMIdentity{ICCID: "8944100000000000000"})
	if imei != "" || userAgent != "" {
		t.Fatalf("missing IMSI produced fallback identity = %q, %q", imei, userAgent)
	}
}
