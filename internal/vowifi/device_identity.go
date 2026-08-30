package vowifi

import "strings"

const (
	// The fallback device profile is deliberately fixed so that its IMEI and
	// User-Agent always describe the same device class.
	spoofedDeviceIMEITAC   = "35898336"
	spoofedDeviceUserAgent = "iOS/18.6.2 iPhone"
)

// ResolveIMSDeviceIdentity returns the device identity used by IMS and the
// matching fallback User-Agent. A valid modem IMEI is preserved and does not
// require a fallback User-Agent. When the modem has no usable IMEI, the
// deterministic fallback is derived from the IMSI and completed with an IMEI
// check digit.
func ResolveIMSDeviceIdentity(identity SIMIdentity) (imei, userAgent string) {
	imei = strings.TrimSpace(identity.IMEI)
	if validIMEIOrIMEISV(imei) {
		return imei, ""
	}

	imsi := strings.TrimSpace(identity.IMSI)
	if !isNDigits(imsi, 5, 16) {
		return "", ""
	}
	serial := imsi
	if len(serial) >= 6 {
		serial = serial[len(serial)-6:]
	} else {
		serial = "123456"
	}
	base := spoofedDeviceIMEITAC + serial
	return base + string([]byte{'0' + imeiCheckDigit(base)}), spoofedDeviceUserAgent
}

func validIMEIOrIMEISV(value string) bool {
	return (len(value) == 15 || len(value) == 16) && isNDigits(value, len(value), len(value))
}

func imeiCheckDigit(value string) byte {
	sum := 0
	for index, digit := range []byte(value) {
		value := int(digit - '0')
		if index%2 == 1 {
			value *= 2
			if value > 9 {
				value -= 9
			}
		}
		sum += value
	}
	return byte((10 - sum%10) % 10)
}
