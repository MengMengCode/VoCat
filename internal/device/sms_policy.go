package device

import "strings"

// BlockedSMSDestination reports whether the recipient is in a barred
// country. Normalization mirrors the PDU/IMS paths so the block cannot be
// sidestepped by dropping the leading "+" or using a 00 international
// prefix. Shared by the HTTP API and the MCP tool surface so both
// transports enforce one policy.
func BlockedSMSDestination(phone string) (bool, string) {
	var digits strings.Builder
	for _, c := range strings.TrimSpace(phone) {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		}
	}
	d := digits.String()
	if strings.HasPrefix(d, "00") {
		d = d[2:]
	}
	if strings.HasPrefix(d, "86") {
		return true, "SMS to +86 (China) destinations is not allowed"
	}
	return false, ""
}
