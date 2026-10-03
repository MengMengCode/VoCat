package modem

import "strings"

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
