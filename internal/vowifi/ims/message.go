package ims

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const maxSIPHeaderBytes = 64 << 10

type sipResponse struct {
	StatusCode int
	Reason     string
	Headers    map[string][]string
	Body       []byte
	rawHeaders []byte
}

type sipRequest struct {
	Method     string
	URI        string
	Headers    map[string][]string
	Body       []byte
	rawHeaders []byte
	flow       string // only identifies the inbound SIP flow
}

func (request *sipRequest) values(name string) []string {
	if request == nil {
		return nil
	}
	return append([]string(nil), request.Headers[strings.ToLower(strings.TrimSpace(name))]...)
}

func (request *sipRequest) value(name string) string {
	values := request.values(name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

type sipPacket struct {
	Response *sipResponse
	Request  *sipRequest
}

func (response *sipResponse) values(name string) []string {
	if response == nil {
		return nil
	}
	return append([]string(nil), response.Headers[strings.ToLower(strings.TrimSpace(name))]...)
}

func (response *sipResponse) value(name string) string {
	values := response.values(name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func parseSIPResponse(packet []byte) (*sipResponse, error) {
	parsed, err := parseSIPPacket(packet)
	if err != nil {
		return nil, err
	}
	if parsed.Response == nil {
		return nil, errors.New("ims: SIP packet is not a response")
	}
	return parsed.Response, nil
}

func parseSIPPacket(packet []byte) (sipPacket, error) {
	headerEnd, delimiterSize := findHeaderEnd(packet)
	if headerEnd < 0 {
		return sipPacket{}, errors.New("ims: incomplete SIP headers")
	}
	if headerEnd > maxSIPHeaderBytes {
		return sipPacket{}, errors.New("ims: SIP headers exceed limit")
	}
	parsed, contentLength, err := parseSIPHeaderBlockAny(packet[:headerEnd])
	if err != nil {
		return sipPacket{}, err
	}
	body := packet[headerEnd+delimiterSize:]
	if contentLength > len(body) {
		return sipPacket{}, errors.New("ims: incomplete SIP body")
	}
	if parsed.Response != nil {
		parsed.Response.Body = append([]byte(nil), body[:contentLength]...)
		parsed.Response.rawHeaders = append([]byte(nil), packet[:headerEnd+delimiterSize]...)
	} else {
		parsed.Request.Body = append([]byte(nil), body[:contentLength]...)
		parsed.Request.rawHeaders = append([]byte(nil), packet[:headerEnd+delimiterSize]...)
	}
	return parsed, nil
}

func readSIPResponse(reader *bufio.Reader) (*sipResponse, error) {
	packet, err := readSIPPacket(reader)
	if err != nil {
		return nil, err
	}
	if packet.Response == nil {
		return nil, errors.New("ims: SIP packet is not a response")
	}
	return packet.Response, nil
}

func readSIPPacket(reader *bufio.Reader) (sipPacket, error) {
	packet, _, err := readSIPPacketWithKeepalive(reader, nil)
	return packet, err
}

// readSIPPacketWithKeepalive reads one SIP message from a connection-oriented
// SIP stream. A double CRLF received between SIP messages is reported to
// onCRLF as an inbound ping. A single CRLF is ignored: in a TCP byte stream it
// is indistinguishable from the first half of a peer ping, so it must not be
// used as proof that a local keepalive received a pong.
func readSIPPacketWithKeepalive(
	reader *bufio.Reader,
	onCRLF func(ping bool) error,
) (sipPacket, bool, error) {
	blankCRLF := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return sipPacket{}, false, fmt.Errorf("ims: read SIP headers: %w", err)
		}
		if line == "\r\n" {
			if blankCRLF {
				if onCRLF != nil {
					if err := onCRLF(true); err != nil {
						return sipPacket{}, false, err
					}
				}
				blankCRLF = false
			} else {
				blankCRLF = true
			}
			continue
		}
		blankCRLF = false
		packet, err := readSIPPacketAfterStartLine(reader, line)
		return packet, true, err
	}
}

func readSIPPacketAfterStartLine(reader *bufio.Reader, startLine string) (sipPacket, error) {
	var header bytes.Buffer
	header.WriteString(startLine)
	for header.Len() <= maxSIPHeaderBytes {
		line, err := reader.ReadString('\n')
		if err != nil {
			return sipPacket{}, fmt.Errorf("ims: read SIP headers: %w", err)
		}
		header.WriteString(line)
		if line == "\r\n" || line == "\n" {
			headerBytes := header.Bytes()
			headerEnd, _ := findHeaderEnd(headerBytes)
			if headerEnd < 0 {
				return sipPacket{}, errors.New("ims: incomplete SIP headers")
			}
			packet, contentLength, parseErr := parseSIPHeaderBlockAny(headerBytes[:headerEnd])
			if parseErr != nil {
				return sipPacket{}, parseErr
			}
			body := []byte(nil)
			if contentLength > 0 {
				body = make([]byte, contentLength)
				if _, err := io.ReadFull(reader, body); err != nil {
					return sipPacket{}, fmt.Errorf("ims: read SIP body: %w", err)
				}
				if packet.Response != nil {
					packet.Response.Body = body
				} else {
					packet.Request.Body = body
				}
			}
			if packet.Response != nil {
				packet.Response.rawHeaders = append([]byte(nil), headerBytes...)
			} else {
				packet.Request.rawHeaders = append([]byte(nil), headerBytes...)
			}
			return packet, nil
		}
	}
	return sipPacket{}, errors.New("ims: SIP headers exceed limit")
}

func parseSIPHeaderBlock(block []byte) (*sipResponse, int, error) {
	packet, length, err := parseSIPHeaderBlockAny(block)
	if err != nil {
		return nil, 0, err
	}
	if packet.Response == nil {
		return nil, 0, errors.New("ims: SIP packet is not a response")
	}
	return packet.Response, length, nil
}

func parseSIPHeaderBlockAny(block []byte) (sipPacket, int, error) {
	text := strings.ReplaceAll(string(block), "\r\n", "\n")
	rawLines := strings.Split(text, "\n")
	if len(rawLines) == 0 {
		return sipPacket{}, 0, errors.New("ims: empty SIP message")
	}
	startFields := strings.Fields(strings.TrimSpace(rawLines[0]))
	if len(startFields) < 2 {
		return sipPacket{}, 0, errors.New("ims: invalid SIP start line")
	}

	lines := make([]string, 0, len(rawLines)-1)
	for _, raw := range rawLines[1:] {
		if raw == "" {
			continue
		}
		if (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += " " + strings.TrimSpace(raw)
			continue
		}
		lines = append(lines, raw)
	}

	headers := make(map[string][]string)
	for _, line := range lines {
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			return sipPacket{}, 0, errors.New("ims: malformed SIP header")
		}
		name := strings.ToLower(strings.TrimSpace(line[:colon]))
		if name == "" {
			return sipPacket{}, 0, errors.New("ims: empty SIP header name")
		}
		// RFC 3261 compact forms commonly appear on bandwidth-sensitive IMS
		// links. Canonicalize the fields used by transactions and MESSAGE.
		if canonical, ok := map[string]string{
			"v": "via", "f": "from", "t": "to", "i": "call-id",
			"l": "content-length", "c": "content-type",
		}[name]; ok {
			name = canonical
		}
		value := strings.TrimSpace(line[colon+1:])
		headers[name] = append(headers[name], value)
	}

	contentLength := 0
	var err error
	if values := headers["content-length"]; len(values) > 0 {
		contentLength, err = strconv.Atoi(strings.TrimSpace(values[len(values)-1]))
		if err != nil || contentLength < 0 || contentLength > 1<<20 {
			return sipPacket{}, 0, errors.New("ims: invalid SIP Content-Length")
		}
	}
	if strings.EqualFold(startFields[0], "SIP/2.0") {
		statusCode, parseErr := strconv.Atoi(startFields[1])
		if parseErr != nil || statusCode < 100 || statusCode > 699 {
			return sipPacket{}, 0, errors.New("ims: invalid SIP response status code")
		}
		reason := ""
		if len(startFields) > 2 {
			reason = strings.Join(startFields[2:], " ")
		}
		return sipPacket{Response: &sipResponse{StatusCode: statusCode, Reason: reason, Headers: headers}}, contentLength, nil
	}
	if len(startFields) != 3 || !strings.EqualFold(startFields[2], "SIP/2.0") ||
		startFields[0] == "" || startFields[1] == "" {
		return sipPacket{}, 0, errors.New("ims: invalid SIP request line")
	}
	return sipPacket{Request: &sipRequest{
		Method:  strings.ToUpper(startFields[0]),
		URI:     startFields[1],
		Headers: headers,
	}}, contentLength, nil
}

func findHeaderEnd(packet []byte) (index int, delimiterSize int) {
	if index := bytes.Index(packet, []byte("\r\n\r\n")); index >= 0 {
		return index, 4
	}
	if index := bytes.Index(packet, []byte("\n\n")); index >= 0 {
		return index, 2
	}
	return -1, 0
}

func splitHeaderValues(values []string) []string {
	var result []string
	for _, value := range values {
		start := 0
		quoted := false
		escaped := false
		angleDepth := 0
		for index, character := range value {
			switch {
			case escaped:
				escaped = false
			case quoted && character == '\\':
				escaped = true
			case character == '"':
				quoted = !quoted
			case !quoted && character == '<':
				angleDepth++
			case !quoted && character == '>' && angleDepth > 0:
				angleDepth--
			case !quoted && angleDepth == 0 && character == ',':
				if item := strings.TrimSpace(value[start:index]); item != "" {
					result = append(result, item)
				}
				start = index + 1
			}
		}
		if item := strings.TrimSpace(value[start:]); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func cseqNumber(value string) (uint32, string, error) {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0, "", errors.New("ims: malformed CSeq")
	}
	number, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil {
		return 0, "", errors.New("ims: malformed CSeq number")
	}
	return uint32(number), strings.ToUpper(fields[1]), nil
}
