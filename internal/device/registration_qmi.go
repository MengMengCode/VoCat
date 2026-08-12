package device

import (
	"regexp"
	"strings"
)

type platformRegistration struct {
	Status         int
	PLMN           string
	Name           string
	PSAttached     bool
	LimitedService bool
}

var qmiQuotedFieldPattern = regexp.MustCompile(`(?i)^\s*([^:]+):\s*'([^']*)'\s*$`)

func parseQMIRegistration(output string) (platformRegistration, bool) {
	result := platformRegistration{}
	registrationState := ""
	roaming := false
	mcc := ""
	mnc := ""
	pcsDigit := false
	for _, rawLine := range strings.Split(output, "\n") {
		match := qmiQuotedFieldPattern.FindStringSubmatch(strings.TrimSpace(rawLine))
		if len(match) != 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		value := strings.TrimSpace(match[2])
		switch key {
		case "registration state":
			registrationState = strings.ToLower(value)
		case "roaming status":
			roaming = strings.EqualFold(value, "on")
		case "ps":
			result.PSAttached = strings.EqualFold(value, "attached")
		case "status":
			// QMI NAS can report a camped/limited roaming service even when
			// Registration state is still searching and PS is detached. Keep
			// this separate from the full registered states so SMS-only
			// roaming is visible without being mistaken for packet attach.
			result.LimitedService = strings.EqualFold(value, "limited")
		case "mcc":
			if mcc == "" {
				mcc = value
			}
		case "mnc":
			if mnc == "" {
				mnc = value
			}
		case "description":
			if result.Name == "" {
				result.Name = value
			}
		case "mnc with pcs digit":
			pcsDigit = strings.EqualFold(value, "yes")
		}
	}
	switch registrationState {
	case "registered":
		result.Status = 1
		if roaming {
			result.Status = 5
		}
	case "not-registered-searching", "searching":
		result.Status = 2
	case "registration-denied", "denied":
		result.Status = 3
	case "not-registered":
		result.Status = 0
	default:
		return platformRegistration{}, false
	}
	if decimalDigits(mcc, 3, 3) && decimalDigits(mnc, 1, 3) {
		width := 2
		if pcsDigit {
			width = 3
		}
		for len(mnc) < width {
			mnc = "0" + mnc
		}
		result.PLMN = mcc + mnc
	}
	return result, true
}
