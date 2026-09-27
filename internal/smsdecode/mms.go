package smsdecode

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const (
	mmsMessageType           byte = 0x8c
	mmsTransactionID         byte = 0x98
	mmsVersion               byte = 0x8d
	mmsFrom                  byte = 0x89
	mmsSubject               byte = 0x96
	mmsTo                    byte = 0x97
	mmsMessageClass          byte = 0x8a
	mmsMessageSize           byte = 0x8e
	mmsExpiry                byte = 0x88
	mmsDate                  byte = 0x85
	mmsContentLocation       byte = 0x83
	mmsPriority              byte = 0x8f
	mmsDeliveryReport        byte = 0x86
	mmsDeliveryTime          byte = 0x87
	mmsMessageID             byte = 0x8b
	mmsReadReply             byte = 0x90
	mmsReportAllowed         byte = 0x91
	mmsResponseStatus        byte = 0x92
	mmsResponseText          byte = 0x93
	mmsSenderVisibility      byte = 0x94
	mmsStatus                byte = 0x95
	mmsContentType           byte = 0x84
	mmsRetrieveStatus        byte = 0x99
	mmsRetrieveText          byte = 0x9a
	mmsReadStatus            byte = 0x9b
	mmsReplyCharging         byte = 0x9c
	mmsReplyChargingDeadline byte = 0x9d
	mmsReplyChargingID       byte = 0x9e
	mmsReplyChargingSize     byte = 0x9f

	mmsNotificationInd byte = 0x82

	charsetUSASCII = 3
	charsetLatin1  = 4
	charsetUTF8    = 106
	charsetGBK     = 113
	charsetGB18030 = 114
	charsetUCS2    = 1000
	charsetUTF16BE = 1013
	charsetUTF16LE = 1014
	charsetUTF16   = 1015
	charsetGB2312  = 2025
)

func decodeMMSNotificationText(body []byte) (string, bool) {
	subject := ""
	notification := false
	index := 0
	for index < len(body) {
		tag := body[index]
		index++
		var ok bool
		switch tag {
		case mmsMessageType:
			if index >= len(body) {
				return mmsResult(subject, notification)
			}
			notification = notification || body[index] == mmsNotificationInd
			index++
		case mmsSubject:
			var text string
			var consumed int
			text, consumed, ok = decodeEncodedString(body[index:])
			if !ok {
				return mmsResult(subject, notification)
			}
			subject = text
			index += consumed
		case mmsFrom:
			index, ok = skipValueLengthPayload(body, index)
			if !ok {
				return mmsResult(subject, notification)
			}
		case mmsExpiry, mmsDeliveryTime, mmsReplyChargingDeadline:
			index, ok = skipValueLengthPayload(body, index)
			if !ok {
				return mmsResult(subject, notification)
			}
		case mmsDate, mmsMessageSize, mmsReplyChargingSize:
			index, ok = skipLongInteger(body, index)
			if !ok {
				return mmsResult(subject, notification)
			}
		case mmsTransactionID, mmsContentLocation, mmsMessageID, mmsReplyChargingID:
			index, ok = skipTextString(body, index)
			if !ok {
				return mmsResult(subject, notification)
			}
		case mmsVersion, mmsMessageClass, mmsPriority, mmsDeliveryReport,
			mmsReadReply, mmsReportAllowed, mmsResponseStatus, mmsSenderVisibility,
			mmsStatus, mmsRetrieveStatus, mmsReadStatus, mmsReplyCharging:
			index, ok = skipShortInteger(body, index)
			if !ok {
				return mmsResult(subject, notification)
			}
		case mmsTo, mmsResponseText, mmsRetrieveText:
			_, consumed, encodedOK := decodeEncodedString(body[index:])
			if !encodedOK {
				return mmsResult(subject, notification)
			}
			index += consumed
		case mmsContentType:
			_, consumed, typeOK := readWSPContentType(body[index:])
			if !typeOK {
				return mmsResult(subject, notification)
			}
			index += consumed
		default:
			return mmsResult(subject, notification)
		}
	}
	return mmsResult(subject, notification)
}

func mmsResult(subject string, notification bool) (string, bool) {
	subject = strings.TrimSpace(subject)
	if subject != "" && readableText(subject) {
		return subject, true
	}
	if notification {
		return "MMS", true
	}
	return "", false
}

func decodeEncodedString(data []byte) (string, int, bool) {
	if len(data) == 0 {
		return "", 0, false
	}
	if data[0] >= 32 {
		text, consumed, ok := readCString(data, 0)
		if !ok {
			return "", 0, false
		}
		return text, consumed, true
	}
	length, index, ok := readValueLength(data, 0)
	if !ok || index+length > len(data) {
		return "", 0, false
	}
	text, _ := decodeCharsetText(data[index : index+length])
	return text, index + length, true
}

func decodeCharsetText(payload []byte) (string, bool) {
	if len(payload) == 0 {
		return "", true
	}
	charset, index, ok := readWellKnownCharset(payload)
	if !ok {
		return decodeTextWithCharset(payload, 0)
	}
	return decodeTextWithCharset(payload[index:], charset)
}

func readWellKnownCharset(data []byte) (int, int, bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	if data[0]&0x80 != 0 {
		return int(data[0] & 0x7f), 1, true
	}
	if data[0] >= 1 && data[0] <= 30 {
		length := int(data[0])
		if 1+length > len(data) {
			return 0, 0, false
		}
		value := 0
		for _, octet := range data[1 : 1+length] {
			value = (value << 8) | int(octet)
		}
		return value, 1 + length, true
	}
	if data[0] == 31 {
		value, next, ok := readUintvar(data, 1)
		if !ok {
			return 0, 0, false
		}
		return value, next, true
	}
	return 0, 0, false
}

func decodeTextWithCharset(payload []byte, charset int) (string, bool) {
	payload = trimTrailingNUL(payload)
	if len(payload) == 0 {
		return "", true
	}
	switch charset {
	case 0, charsetUSASCII, charsetUTF8:
		if utf8.Valid(payload) {
			return string(payload), true
		}
		if text, ok := decodeGB18030(payload); ok {
			return text, true
		}
	case charsetLatin1:
		return decodeLatin1(payload), true
	case charsetUCS2, charsetUTF16, charsetUTF16BE:
		if text, ok := decodeUTF16(payload, false); ok {
			return text, true
		}
	case charsetUTF16LE:
		if text, ok := decodeUTF16(payload, true); ok {
			return text, true
		}
	case charsetGBK, charsetGB18030, charsetGB2312:
		if text, ok := decodeGB18030(payload); ok {
			return text, true
		}
	}
	if utf8.Valid(payload) && readableText(string(payload)) {
		return string(payload), true
	}
	if text, ok := decodeGB18030(payload); ok {
		return text, true
	}
	return "", false
}

func decodeUTF16(payload []byte, littleEndian bool) (string, bool) {
	if len(payload) == 0 {
		return "", true
	}
	if len(payload)%2 != 0 {
		return "", false
	}
	if len(payload) >= 2 {
		switch {
		case payload[0] == 0xfe && payload[1] == 0xff:
			littleEndian = false
			payload = payload[2:]
		case payload[0] == 0xff && payload[1] == 0xfe:
			littleEndian = true
			payload = payload[2:]
		}
	}
	units := make([]uint16, 0, len(payload)/2)
	for index := 0; index+1 < len(payload); index += 2 {
		if littleEndian {
			units = append(units, uint16(payload[index])|uint16(payload[index+1])<<8)
		} else {
			units = append(units, uint16(payload[index])<<8|uint16(payload[index+1]))
		}
	}
	return string(utf16.Decode(units)), true
}

func decodeGB18030(payload []byte) (string, bool) {
	decoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), payload)
	if err != nil {
		return "", false
	}
	text := string(decoded)
	if !readableText(text) {
		return "", false
	}
	return text, true
}

func decodeLatin1(payload []byte) string {
	runes := make([]rune, len(payload))
	for index, value := range payload {
		runes[index] = rune(value)
	}
	return string(runes)
}

func trimTrailingNUL(payload []byte) []byte {
	for len(payload) > 0 && payload[len(payload)-1] == 0 {
		payload = payload[:len(payload)-1]
	}
	return payload
}
