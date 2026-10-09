package device

import (
	"regexp"
	"strings"
	"unicode"

	"vocat/internal/modem"
)

const maxHardwareErrorDetail = 1024

var longHexPayload = regexp.MustCompile(`(?i)\b[0-9a-f]{48,}\b`)

// HardwareErrorDetail returns a diagnostic error suitable for persistent and
// browser-visible logs. AT payloads can contain APDU authentication material,
// SMS data, or APN credentials, so CommandError values retain only the command
// name and modem final result. Very long hexadecimal payloads from wrapped
// protocol errors are removed as a second line of defence.
func HardwareErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := redactCommandErrors(err.Error(), err)
	detail = longHexPayload.ReplaceAllString(detail, "[redacted hex payload]")
	detail = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) && character != '\t' && character != '\n' {
			return ' '
		}
		return character
	}, strings.TrimSpace(detail))
	runes := []rune(detail)
	if len(runes) > maxHardwareErrorDetail {
		detail = string(runes[:maxHardwareErrorDetail]) + "..."
	}
	return detail
}

func redactCommandErrors(detail string, err error) string {
	if commandErr, ok := err.(*modem.CommandError); ok {
		detail = strings.ReplaceAll(detail, commandErr.Error(), safeCommandError(commandErr))
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range wrapped.Unwrap() {
			detail = redactCommandErrors(detail, child)
		}
	case interface{ Unwrap() error }:
		if child := wrapped.Unwrap(); child != nil {
			detail = redactCommandErrors(detail, child)
		}
	}
	return detail
}

func safeCommandError(err *modem.CommandError) string {
	command := safeATCommandName(err.Command)
	final := strings.TrimSpace(err.Final)
	if final == "" {
		final = "unknown modem error"
	}
	return command + " failed: " + final
}

func safeATCommandName(command string) string {
	command = strings.ToUpper(strings.TrimSpace(command))
	if command == "" {
		return "AT command"
	}
	if strings.HasPrefix(command, "ATD") {
		return "ATD"
	}
	for index, character := range command {
		if character == '=' || character == '?' || character == ',' ||
			character == '"' || unicode.IsSpace(character) {
			command = command[:index]
			break
		}
	}
	if !strings.HasPrefix(command, "AT") || len(command) > 32 {
		return "AT command"
	}
	return command
}
