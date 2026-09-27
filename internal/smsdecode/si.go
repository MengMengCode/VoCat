package smsdecode

import (
	"bytes"
	"strings"
)

func decodeServiceIndicationText(contentType string, body []byte) (string, bool) {
	switch normalizeContentType(contentType) {
	case "text/vnd.wap.si":
		return extractXMLIndication(body)
	default:
		if text, ok := extractWBXMLStrings(body); ok {
			return text, true
		}
		return extractXMLIndication(body)
	}
}

func extractXMLIndication(body []byte) (string, bool) {
	lower := asciiLower(body)
	startTag := []byte("<indication")
	start := bytes.Index(lower, startTag)
	if start < 0 {
		return "", false
	}
	openEnd := bytes.IndexByte(body[start:], '>')
	if openEnd < 0 {
		return "", false
	}
	innerStart := start + openEnd + 1
	end := bytes.Index(lower[innerStart:], []byte("</indication"))
	if end < 0 {
		return "", false
	}
	text := strings.TrimSpace(string(body[innerStart : innerStart+end]))
	if readableText(text) {
		return text, true
	}
	return "", false
}

func extractWBXMLStrings(body []byte) (string, bool) {
	if len(body) < 4 {
		return "", false
	}
	index := 1                               // skip version
	_, index, ok := readUintvar(body, index) // public ID
	if !ok {
		return "", false
	}
	_, index, ok = readUintvar(body, index) // charset
	if !ok {
		return "", false
	}
	tableLen, index, ok := readUintvar(body, index)
	if !ok || index+tableLen > len(body) {
		return "", false
	}
	table := body[index : index+tableLen]
	index += tableLen

	var candidates []string
	for index < len(body) {
		switch body[index] {
		case 0x03: // STR_I
			text, next, stringOK := readCString(body, index+1)
			if !stringOK {
				index = len(body)
				break
			}
			text = strings.TrimSpace(text)
			if readableText(text) {
				candidates = append(candidates, text)
			}
			index = next
		case 0x83: // STR_T
			offset, next, refOK := readUintvar(body, index+1)
			if !refOK {
				index = len(body)
				break
			}
			if text, ok := stringTableText(table, offset); ok {
				candidates = append(candidates, text)
			}
			index = next
		default:
			index++
		}
	}
	return pickIndicationString(candidates)
}

func pickIndicationString(candidates []string) (string, bool) {
	best := ""
	for _, text := range candidates {
		if looksLikeURL(text) {
			continue
		}
		if len([]rune(text)) >= len([]rune(best)) {
			best = text
		}
	}
	if best != "" {
		return best, true
	}
	return "", false
}

func looksLikeURL(text string) bool {
	lower := strings.ToLower(text)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "www.")
}

func asciiLower(value []byte) []byte {
	lower := make([]byte, len(value))
	for i, b := range value {
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		lower[i] = b
	}
	return lower
}

func stringTableText(table []byte, offset int) (string, bool) {
	if offset >= len(table) {
		return "", false
	}
	end := bytes.IndexByte(table[offset:], 0)
	if end < 0 {
		end = len(table) - offset
	}
	text := strings.TrimSpace(string(table[offset : offset+end]))
	if readableText(text) {
		return text, true
	}
	return "", false
}
