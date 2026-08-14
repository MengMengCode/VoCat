package ike

import (
	"encoding/binary"
	"errors"
	"strings"
)

// deviceIdentityRequested reports the 3GPP DEVICE_IDENTITY request defined by
// TS 24.302. Android remembers this request and answers it in a later EAP
// IKE_AUTH request, but only after authenticating the ePDG.
func deviceIdentityRequested(payloads []payload) (bool, error) {
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, _, err := parseNotify(item)
		if err != nil {
			return false, err
		}
		if kind == notifyDeviceIdentity {
			return true, nil
		}
	}
	return false, nil
}

func deviceIdentityNotify(identity string) (payload, error) {
	identity = strings.TrimSpace(identity)
	if (len(identity) != 15 && len(identity) != 16) || !decimalDigits(identity) {
		return payload{}, errors.New("ike: device identity must contain 15 or 16 digits")
	}
	identityType := byte(1) // IMEI
	if len(identity) == 16 {
		identityType = 2 // IMEISV
	}
	data := make([]byte, 11)
	// TS 24.302 Figure 8.2.9.2: this inner length excludes its own two
	// octets, and is therefore 9 for an IMEI/IMEISV value.
	binary.BigEndian.PutUint16(data[:2], 9)
	data[2] = identityType
	for index := 0; index < 8; index++ {
		low := identity[index*2] - '0'
		high := byte(0x0f)
		if index*2+1 < len(identity) {
			high = identity[index*2+1] - '0'
		}
		data[index+3] = high<<4 | low
	}
	return makeNotify(notifyDeviceIdentity, data), nil
}

func decimalDigits(value string) bool {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return value != ""
}
