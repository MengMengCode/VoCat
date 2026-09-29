package smsdecode

import "bytes"

const (
	wspPDUPush          byte = 0x06
	wspPDUConfirmedPush byte = 0x07

	wspContentTypePlain byte = 0x03
	wspContentTypeSI    byte = 0x2d
	wspContentTypeSIC   byte = 0x2e
	wspContentTypeMMS   byte = 0x3e
)

var wspWellKnownContentTypes = map[byte]string{
	wspContentTypePlain: "text/plain",
	wspContentTypeSI:    "text/vnd.wap.si",
	wspContentTypeSIC:   "application/vnd.wap.sic",
	wspContentTypeMMS:   "application/vnd.wap.mms-message",
}

// parseWSPPush reads a connectionless WSP Push (WSP 1.0 §8.2.4):
//
//	Transaction-ID, PDU-Type, HeadersLen, Content-Type + headers, body.
func parseWSPPush(payload []byte) (contentType string, body []byte, ok bool) {
	if len(payload) < 4 {
		return "", nil, false
	}
	pduType := payload[1]
	if pduType != wspPDUPush && pduType != wspPDUConfirmedPush {
		return "", nil, false
	}
	headersLen, index, ok := readUintvar(payload, 2)
	if !ok || headersLen < 1 || index+headersLen > len(payload) {
		return "", nil, false
	}
	headerBytes := payload[index : index+headersLen]
	contentType, _, ok = readWSPContentType(headerBytes)
	if !ok || contentType == "" {
		return "", nil, false
	}
	return contentType, payload[index+headersLen:], true
}

func readWSPContentType(header []byte) (string, int, bool) {
	if len(header) == 0 {
		return "", 0, false
	}
	if header[0]&0x80 != 0 {
		name, ok := wspWellKnownContentTypes[header[0]&0x7f]
		if !ok {
			return "", 0, false
		}
		return name, 1, true
	}
	if header[0] < 32 {
		length, index, ok := readValueLength(header, 0)
		if !ok || index+length > len(header) {
			return "", 0, false
		}
		media := header[index : index+length]
		name, _, ok := readWSPMediaType(media)
		if !ok {
			return "", 0, false
		}
		return name, index + length, true
	}
	return readCString(header, 0)
}

func readWSPMediaType(media []byte) (string, int, bool) {
	if len(media) == 0 {
		return "", 0, false
	}
	if media[0]&0x80 != 0 {
		name, ok := wspWellKnownContentTypes[media[0]&0x7f]
		if !ok {
			return "", 0, false
		}
		return name, 1, true
	}
	return readCString(media, 0)
}

func readCString(data []byte, index int) (string, int, bool) {
	if index >= len(data) {
		return "", index, false
	}
	end := bytes.IndexByte(data[index:], 0)
	if end < 0 {
		return string(data[index:]), len(data), true
	}
	return string(data[index : index+end]), index + end + 1, true
}

func readUintvar(data []byte, index int) (int, int, bool) {
	value := 0
	for shift := 0; shift < 5; shift++ {
		if index >= len(data) {
			return 0, index, false
		}
		octet := data[index]
		index++
		value = (value << 7) | int(octet&0x7f)
		if octet&0x80 == 0 {
			return value, index, true
		}
	}
	return 0, index, false
}

func readValueLength(data []byte, index int) (int, int, bool) {
	if index >= len(data) {
		return 0, index, false
	}
	octet := data[index]
	if octet < 31 {
		return int(octet), index + 1, true
	}
	if octet == 31 {
		return readUintvar(data, index+1)
	}
	return 0, index, false
}

func skipValueLengthPayload(data []byte, index int) (int, bool) {
	length, next, ok := readValueLength(data, index)
	if !ok || next+length > len(data) {
		return index, false
	}
	return next + length, true
}

func skipLongInteger(data []byte, index int) (int, bool) {
	if index >= len(data) || data[index] < 1 || data[index] > 30 {
		return index, false
	}
	next := index + 1 + int(data[index])
	if next > len(data) {
		return index, false
	}
	return next, true
}

func skipTextString(data []byte, index int) (int, bool) {
	if index > len(data) {
		return index, false
	}
	end := bytes.IndexByte(data[index:], 0)
	if end < 0 {
		return len(data), true
	}
	return index + end + 1, true
}

func skipShortInteger(data []byte, index int) (int, bool) {
	if index >= len(data) || data[index]&0x80 == 0 {
		return index, false
	}
	return index + 1, true
}
