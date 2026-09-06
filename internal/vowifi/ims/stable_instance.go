package ims

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"strings"

	"vocat/internal/vowifi"
)

// stableInstanceUUID derives a UUIDv5 from the RFC 9562 URL namespace and a
// VoCat-specific identity name. IMEI validation matches the existing GSMA form:
// exactly 15 ASCII digits after trimming, without additional checksum rules.
func stableInstanceUUID(request vowifi.IMSRequest) (string, error) {
	imei := strings.TrimSpace(request.Identity.IMEI)
	deviceID := strings.TrimSpace(request.DeviceID)
	imsi := strings.TrimSpace(request.Identity.IMSI)
	var name string
	switch {
	case digitsBetween(imei, 15, 15):
		name = "urn:vocat:sip-instance:imei:" + imei
	case deviceID != "":
		// Production orchestration supplies the configured device ID. This
		// fallback remains stable only while that configuration is unchanged.
		name = "urn:vocat:sip-instance:device-id:" + deviceID
	case digitsBetween(imsi, 5, 16):
		// Legacy direct Provider callers may supply only a SIM identity;
		// deriveIdentities already requires a valid IMSI. Preserve this contract
		// without randomness, but this last resort is SIM-, not hardware-bound.
		name = "urn:vocat:sip-instance:imsi:" + imsi
	default:
		return "", errors.New("ims: stable SIP instance requires a valid IMEI, DeviceID, or IMSI")
	}
	// RFC URL namespace: 6ba7b811-9dad-11d1-80b4-00c04fd430c8.
	namespace := [16]byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	hash := sha1.New() // UUIDv5 requires SHA-1; this is not an authentication secret.
	_, _ = hash.Write(namespace[:])
	_, _ = hash.Write([]byte(name))
	uuid := hash.Sum(nil)[:16]
	uuid[6] = (uuid[6] & 0x0f) | 0x50
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), nil
}
