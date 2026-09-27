// Package smsdecode extracts a human-readable preview from binary SMS
// payloads. The important case is a WAP Push carrying an MMS
// m-notification-ind: VoCat cannot retrieve the multimedia body from the
// operator MMSC, but the notification subject is already in the SMS and is
// what phones show in the inbox.
package smsdecode

import (
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

const minHexPayloadBytes = 8

// DecodeDisplayText returns a readable preview of a binary SMS user-data
// payload. ok is false when the bytes are not a recognized WAP Push, so the
// caller should keep its existing representation (typically hexadecimal).
func DecodeDisplayText(payload []byte) (string, bool) {
	contentType, body, ok := parseWSPPush(payload)
	if !ok {
		return "", false
	}
	switch normalizeContentType(contentType) {
	case "application/vnd.wap.mms-message":
		return decodeMMSNotificationText(body)
	case "application/vnd.wap.sic", "text/vnd.wap.si":
		return decodeServiceIndicationText(contentType, body)
	case "text/plain":
		text := strings.TrimRight(string(body), "\x00")
		if readableText(text) && strings.TrimSpace(text) != "" {
			return text, true
		}
		return "", false
	default:
		return "", false
	}
}

// Preview returns a user-visible SMS body. Hexadecimal WAP Push payloads
// already stored from an older decoder become the notification subject;
// every other body is unchanged.
func Preview(value string) string {
	if text, ok := DecodeHexDisplayText(value); ok {
		return text
	}
	return value
}

// DecodeHexDisplayText is DecodeDisplayText for a payload stored as hex
// digits, the form 8-bit SMS concat reassembly joins.
func DecodeHexDisplayText(value string) (string, bool) {
	raw, ok := decodeHexPayload(value)
	if !ok {
		return "", false
	}
	return DecodeDisplayText(raw)
}

func decodeHexPayload(value string) ([]byte, bool) {
	value = strings.TrimSpace(value)
	if len(value) < minHexPayloadBytes*2 || len(value)%2 != 0 {
		return nil, false
	}
	for _, character := range value {
		if !isHexDigit(character) {
			return nil, false
		}
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) < minHexPayloadBytes {
		return nil, false
	}
	return raw, true
}

func isHexDigit(character rune) bool {
	return character >= '0' && character <= '9' ||
		character >= 'A' && character <= 'F' ||
		character >= 'a' && character <= 'f'
}

func readableText(text string) bool {
	if text == "" {
		return false
	}
	printable, total := 0, 0
	for _, character := range text {
		total++
		if unicode.IsPrint(character) || character == '\n' || character == '\r' || character == '\t' {
			printable++
		}
	}
	return total > 0 && printable*100 >= total*90 && utf8.ValidString(text)
}

func normalizeContentType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value
}
