package modem

import "strings"

// IsML307 identifies the ML307 USB modem family by its USB identity or product string.
func IsML307(candidate Candidate) bool {
	if strings.EqualFold(strings.TrimSpace(candidate.VendorID), "2ecc") &&
		strings.EqualFold(strings.TrimSpace(candidate.ProductID), "3012") {
		return true
	}
	return strings.Contains(strings.ToUpper(candidate.Product), "ML307")
}

func ParseIMEI(response Response) string {
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		for _, prefix := range []string{"+CGSN:", "+GSN:"} {
			if strings.HasPrefix(upper, prefix) {
				if value := normalizeIMEI(line[len(prefix):]); value != "" {
					return value
				}
			}
		}
		if value := normalizeIMEI(line); value != "" {
			return value
		}
	}
	return ""
}

func normalizeIMEI(value string) string {
	value = strings.Trim(value, `" `)
	value = strings.TrimPrefix(value, "+")
	if len(value) < 14 || len(value) > 17 {
		return ""
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return ""
		}
	}
	return value
}
