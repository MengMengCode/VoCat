package device

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

var gsm7DefaultAlphabet = [128]rune{
	'@', '£', '$', '¥', 'è', 'é', 'ù', 'ì',
	'ò', 'Ç', '\n', 'Ø', 'ø', '\r', 'Å', 'å',
	'Δ', '_', 'Φ', 'Γ', 'Λ', 'Ω', 'Π', 'Ψ',
	'Σ', 'Θ', 'Ξ', '\x1b', 'Æ', 'æ', 'ß', 'É',
	' ', '!', '"', '#', '¤', '%', '&', '\'',
	'(', ')', '*', '+', ',', '-', '.', '/',
	'0', '1', '2', '3', '4', '5', '6', '7',
	'8', '9', ':', ';', '<', '=', '>', '?',
	'¡', 'A', 'B', 'C', 'D', 'E', 'F', 'G',
	'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O',
	'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W',
	'X', 'Y', 'Z', 'Ä', 'Ö', 'Ñ', 'Ü', '§',
	'¿', 'a', 'b', 'c', 'd', 'e', 'f', 'g',
	'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o',
	'p', 'q', 'r', 's', 't', 'u', 'v', 'w',
	'x', 'y', 'z', 'ä', 'ö', 'ñ', 'ü', 'à',
}

var gsm7ExtensionAlphabet = map[byte]rune{
	0x0a: '\f',
	0x14: '^',
	0x28: '{',
	0x29: '}',
	0x2f: '\\',
	0x3c: '[',
	0x3d: '~',
	0x3e: ']',
	0x40: '|',
	0x65: '€',
}

var gsm7Encoder = func() map[rune][]byte {
	result := make(map[rune][]byte, len(gsm7DefaultAlphabet)+len(gsm7ExtensionAlphabet))
	for code, character := range gsm7DefaultAlphabet {
		if code == 0x1b {
			continue
		}
		result[character] = []byte{byte(code)}
	}
	for code, character := range gsm7ExtensionAlphabet {
		result[character] = []byte{0x1b, code}
	}
	return result
}()

type preparedSMS struct {
	to              string
	encoding        SMSEncoding
	payload         []byte
	prompt          string
	setup           []string
	tpduLength      int
	part            int
	total           int
	concatReference *int
}

// PrepareSMSSubmitTPDUs applies the same recipient validation, alphabet
// selection and concatenation rules as SendSMS, but returns raw SMS-SUBMIT
// TPDUs suitable for a non-AT transport such as 3GPP SMS over IMS.
func PrepareSMSSubmitTPDUs(recipient, text string) ([]SMSSubmitTPDU, error) {
	parts, err := prepareSMSParts(recipient, text)
	if err != nil {
		return nil, err
	}
	result := make([]SMSSubmitTPDU, 0, len(parts))
	for _, part := range parts {
		pduHex := string(part.payload)
		encoding := part.encoding
		if part.encoding == SMSEncodingGSM7Text {
			septets, ok := encodeGSM7(text)
			if !ok {
				return nil, errors.New("SMS text could not be encoded as GSM-7")
			}
			pduHex, _, err = encodeSubmitPDU(part.to, septets, nil)
			if err != nil {
				return nil, err
			}
			encoding = SMSEncodingGSM7PDU
		}
		pdu, err := hex.DecodeString(pduHex)
		if err != nil || len(pdu) < 2 {
			return nil, errors.New("encoded SMS PDU is invalid")
		}
		smscBytes := int(pdu[0])
		if 1+smscBytes >= len(pdu) {
			return nil, errors.New("encoded SMS PDU has no TPDU")
		}
		result = append(result, SMSSubmitTPDU{
			To:              part.to,
			Encoding:        encoding,
			TPDU:            append([]byte(nil), pdu[1+smscBytes:]...),
			Part:            part.part,
			Total:           part.total,
			ConcatReference: part.concatReference,
		})
	}
	return result, nil
}

// DecodeSMSDeliverTPDU decodes a TPDU carried by RP-DATA. The existing modem
// decoder expects the AT PDU-mode SMSC prefix, so an empty SMSC is prepended.
func DecodeSMSDeliverTPDU(tpdu []byte) (SMSMessage, error) {
	if len(tpdu) == 0 {
		return SMSMessage{}, errors.New("SMS TPDU is empty")
	}
	pdu := make([]byte, 1, len(tpdu)+1)
	pdu = append(pdu, tpdu...)
	return decodeSMSPDU(hex.EncodeToString(pdu))
}

func prepareSMS(recipient, text string) (preparedSMS, error) {
	parts, err := prepareSMSPartsWithReference(recipient, text, 0)
	if err != nil {
		return preparedSMS{}, err
	}
	if len(parts) != 1 {
		return preparedSMS{}, fmt.Errorf(
			"%w: message requires %d parts",
			ErrSMSTooLong,
			len(parts),
		)
	}
	return parts[0], nil
}

func prepareSMSParts(recipient, text string) ([]preparedSMS, error) {
	parts, err := prepareSMSPartsWithReference(recipient, text, 0)
	if err != nil || len(parts) <= 1 {
		return parts, err
	}
	var reference [1]byte
	if _, err := rand.Read(reference[:]); err != nil {
		return nil, fmt.Errorf("generate SMS concatenation reference: %w", err)
	}
	return prepareSMSPartsWithReference(recipient, text, reference[0])
}

func prepareSMSPartsWithReference(
	recipient string,
	text string,
	reference byte,
) ([]preparedSMS, error) {
	recipient, err := normalizeSMSRecipient(recipient)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, ErrSMSEmpty
	}
	if strings.IndexByte(text, 0) >= 0 {
		return nil, errors.New("SMS text contains NUL")
	}

	septets, gsm7 := encodeGSM7(text)
	if gsm7 && len(septets) <= 160 && canSendDirectGSM7(text) {
		return []preparedSMS{{
			to:       recipient,
			encoding: SMSEncodingGSM7Text,
			payload:  []byte(text),
			prompt:   fmt.Sprintf(`AT+CMGS="%s"`, recipient),
			setup: []string{
				"AT+CMGF=1",
				`AT+CSCS="GSM"`,
				// TP-SRR requests a network delivery status report.
				"AT+CSMP=49,167,0,0",
			},
			part:  1,
			total: 1,
		}}, nil
	}
	if gsm7 && len(septets) <= 160 {
		pdu, tpduLength, err := encodeSubmitPDU(recipient, septets, nil)
		if err != nil {
			return nil, err
		}
		return []preparedSMS{{
			to:         recipient,
			encoding:   SMSEncodingGSM7PDU,
			payload:    []byte(pdu),
			prompt:     fmt.Sprintf("AT+CMGS=%d", tpduLength),
			setup:      []string{"AT+CMGF=0"},
			tpduLength: tpduLength,
			part:       1,
			total:      1,
		}}, nil
	}
	if gsm7 {
		segments := splitGSM7(septets, 153)
		if len(segments) > 255 {
			return nil, fmt.Errorf(
				"%w: GSM-7 requires %d concatenated parts; maximum is 255",
				ErrSMSTooLong,
				len(segments),
			)
		}
		referenceValue := int(reference)
		result := make([]preparedSMS, 0, len(segments))
		for index, segment := range segments {
			header := concatUDH(reference, len(segments), index+1)
			pdu, tpduLength, encodeErr := encodeSubmitPDUWithHeader(
				recipient,
				segment,
				nil,
				header,
			)
			if encodeErr != nil {
				return nil, encodeErr
			}
			result = append(result, preparedSMS{
				to:              recipient,
				encoding:        SMSEncodingGSM7PDU,
				payload:         []byte(pdu),
				prompt:          fmt.Sprintf("AT+CMGS=%d", tpduLength),
				setup:           []string{"AT+CMGF=0"},
				tpduLength:      tpduLength,
				part:            index + 1,
				total:           len(segments),
				concatReference: &referenceValue,
			})
		}
		return result, nil
	}

	units := utf16.Encode([]rune(text))
	if len(units) <= 70 {
		ucs2 := encodeUCS2Units(units)
		pdu, tpduLength, err := encodeSubmitPDU(recipient, nil, ucs2)
		if err != nil {
			return nil, err
		}
		return []preparedSMS{{
			to:         recipient,
			encoding:   SMSEncodingUCS2PDU,
			payload:    []byte(pdu),
			prompt:     fmt.Sprintf("AT+CMGS=%d", tpduLength),
			setup:      []string{"AT+CMGF=0"},
			tpduLength: tpduLength,
			part:       1,
			total:      1,
		}}, nil
	}
	segments := splitUCS2(units, 67)
	if len(segments) > 255 {
		return nil, fmt.Errorf(
			"%w: UCS2 requires %d concatenated parts; maximum is 255",
			ErrSMSTooLong,
			len(segments),
		)
	}
	referenceValue := int(reference)
	result := make([]preparedSMS, 0, len(segments))
	for index, segment := range segments {
		header := concatUDH(reference, len(segments), index+1)
		pdu, tpduLength, encodeErr := encodeSubmitPDUWithHeader(
			recipient,
			nil,
			encodeUCS2Units(segment),
			header,
		)
		if encodeErr != nil {
			return nil, encodeErr
		}
		result = append(result, preparedSMS{
			to:              recipient,
			encoding:        SMSEncodingUCS2PDU,
			payload:         []byte(pdu),
			prompt:          fmt.Sprintf("AT+CMGS=%d", tpduLength),
			setup:           []string{"AT+CMGF=0"},
			tpduLength:      tpduLength,
			part:            index + 1,
			total:           len(segments),
			concatReference: &referenceValue,
		})
	}
	return result, nil
}

func splitGSM7(septets []byte, maximum int) [][]byte {
	var result [][]byte
	for len(septets) > 0 {
		count := maximum
		if count > len(septets) {
			count = len(septets)
		}
		if count < len(septets) && septets[count-1] == 0x1b {
			count--
		}
		result = append(result, append([]byte(nil), septets[:count]...))
		septets = septets[count:]
	}
	return result
}

func splitUCS2(units []uint16, maximum int) [][]uint16 {
	var result [][]uint16
	for len(units) > 0 {
		count := maximum
		if count > len(units) {
			count = len(units)
		}
		if count < len(units) &&
			utf16.IsSurrogate(rune(units[count-1])) &&
			units[count-1] >= 0xd800 && units[count-1] <= 0xdbff &&
			units[count] >= 0xdc00 && units[count] <= 0xdfff {
			count--
		}
		result = append(result, append([]uint16(nil), units[:count]...))
		units = units[count:]
	}
	return result
}

func encodeUCS2Units(units []uint16) []byte {
	result := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		result = append(result, byte(unit>>8), byte(unit))
	}
	return result
}

func concatUDH(reference byte, total, sequence int) []byte {
	return []byte{0x05, 0x00, 0x03, reference, byte(total), byte(sequence)}
}

func normalizeSMSRecipient(value string) (string, error) {
	var result strings.Builder
	for _, character := range strings.TrimSpace(value) {
		switch {
		case character >= '0' && character <= '9':
			result.WriteRune(character)
		case character == '+' && result.Len() == 0:
			result.WriteRune(character)
		case character == ' ' || character == '-' ||
			character == '(' || character == ')':
		default:
			return "", ErrSMSInvalidRecipient
		}
	}
	normalized := result.String()
	digits := strings.TrimPrefix(normalized, "+")
	if len(digits) < 1 || len(digits) > 20 {
		return "", ErrSMSInvalidRecipient
	}
	if strings.IndexFunc(digits, func(character rune) bool {
		return character < '0' || character > '9'
	}) >= 0 {
		return "", ErrSMSInvalidRecipient
	}
	return normalized, nil
}

func encodeGSM7(text string) ([]byte, bool) {
	result := make([]byte, 0, len(text))
	for _, character := range text {
		encoded, ok := gsm7Encoder[character]
		if !ok {
			return nil, false
		}
		result = append(result, encoded...)
	}
	return result, true
}

func canSendDirectGSM7(text string) bool {
	for _, character := range text {
		encoded, ok := gsm7Encoder[character]
		if !ok || len(encoded) != 1 || encoded[0] == 0x1b ||
			character > 0x7f || byte(character) != encoded[0] {
			return false
		}
	}
	return true
}

func encodeSubmitPDU(
	recipient string,
	gsm7Septets []byte,
	ucs2 []byte,
) (string, int, error) {
	return encodeSubmitPDUWithHeader(recipient, gsm7Septets, ucs2, nil)
}

func encodeSubmitPDUWithHeader(
	recipient string,
	gsm7Septets []byte,
	ucs2 []byte,
	userDataHeader []byte,
) (string, int, error) {
	digits := strings.TrimPrefix(recipient, "+")
	address, err := encodeSemiOctets(digits)
	if err != nil {
		return "", 0, err
	}
	toa := byte(0x81)
	if strings.HasPrefix(recipient, "+") {
		toa = 0x91
	}

	// SMS-SUBMIT with TP-SRR set so both AT PDU mode and SMS over IMS can
	// receive an SMS-STATUS-REPORT for this submission.
	firstOctet := byte(0x21)
	if len(userDataHeader) > 0 {
		if int(userDataHeader[0])+1 != len(userDataHeader) {
			return "", 0, errors.New("invalid SMS concatenation header")
		}
		firstOctet |= 0x40
	}
	pdu := []byte{
		0x00, // Use the SMSC configured on the SIM.
		firstOctet,
		0x00, // TP-MR allocated by the modem/network.
		byte(len(digits)),
		toa,
	}
	pdu = append(pdu, address...)
	pdu = append(pdu, 0x00) // TP-PID
	switch {
	case gsm7Septets != nil:
		headerSeptets := 0
		startBit := 0
		if len(userDataHeader) > 0 {
			headerSeptets = (len(userDataHeader)*8 + 6) / 7
			startBit = headerSeptets * 7
		}
		userDataLength := headerSeptets + len(gsm7Septets)
		if userDataLength > 160 {
			return "", 0, ErrSMSTooLong
		}
		packed := packSeptets(gsm7Septets, startBit)
		copy(packed, userDataHeader)
		pdu = append(pdu, 0x00, byte(userDataLength))
		pdu = append(pdu, packed...)
	case ucs2 != nil:
		userDataLength := len(userDataHeader) + len(ucs2)
		if userDataLength > 140 {
			return "", 0, ErrSMSTooLong
		}
		pdu = append(pdu, 0x08, byte(userDataLength))
		pdu = append(pdu, userDataHeader...)
		pdu = append(pdu, ucs2...)
	default:
		return "", 0, errors.New("SMS PDU has no user data")
	}
	return strings.ToUpper(hex.EncodeToString(pdu)), len(pdu) - 1, nil
}

func encodeSemiOctets(digits string) ([]byte, error) {
	if digits == "" {
		return nil, ErrSMSInvalidRecipient
	}
	result := make([]byte, (len(digits)+1)/2)
	for index := 0; index < len(digits); index += 2 {
		low := digits[index]
		if low < '0' || low > '9' {
			return nil, ErrSMSInvalidRecipient
		}
		high := byte('F')
		if index+1 < len(digits) {
			high = digits[index+1]
			if high < '0' || high > '9' {
				return nil, ErrSMSInvalidRecipient
			}
		}
		highNibble := byte(0x0f)
		if high != 'F' {
			highNibble = high - '0'
		}
		result[index/2] = (highNibble << 4) | (low - '0')
	}
	return result, nil
}

func packSeptets(septets []byte, startBit int) []byte {
	if len(septets) == 0 {
		return nil
	}
	bitLength := startBit + len(septets)*7
	result := make([]byte, (bitLength+7)/8)
	for index, septet := range septets {
		bit := startBit + index*7
		byteIndex := bit / 8
		shift := uint(bit % 8)
		result[byteIndex] |= (septet & 0x7f) << shift
		if shift > 1 && byteIndex+1 < len(result) {
			result[byteIndex+1] |= (septet & 0x7f) >> (8 - shift)
		}
	}
	return result
}

func unpackSeptets(data []byte, count, startBit int) ([]byte, error) {
	if count < 0 || startBit < 0 || startBit+count*7 > len(data)*8 {
		return nil, errors.New("GSM-7 user data is truncated")
	}
	result := make([]byte, count)
	for index := 0; index < count; index++ {
		bit := startBit + index*7
		byteIndex := bit / 8
		shift := uint(bit % 8)
		value := data[byteIndex] >> shift
		if shift > 1 && byteIndex+1 < len(data) {
			value |= data[byteIndex+1] << (8 - shift)
		}
		result[index] = value & 0x7f
	}
	return result, nil
}

func decodeGSM7(septets []byte) (string, error) {
	var result strings.Builder
	for index := 0; index < len(septets); index++ {
		code := septets[index]
		if code != 0x1b {
			if int(code) >= len(gsm7DefaultAlphabet) {
				return result.String(), errors.New("invalid GSM-7 code")
			}
			result.WriteRune(gsm7DefaultAlphabet[code])
			continue
		}
		index++
		if index >= len(septets) {
			return result.String(), errors.New("trailing GSM-7 escape")
		}
		character, ok := gsm7ExtensionAlphabet[septets[index]]
		if !ok {
			return result.String(), fmt.Errorf(
				"unknown GSM-7 extension 0x%02X",
				septets[index],
			)
		}
		result.WriteRune(character)
	}
	return result.String(), nil
}

// DecodeGSM7Septets decodes a GSM 7-bit default-alphabet string whose septets
// are stored one code per byte (the form USSI bodies use when DCS=0x0F). It
// returns the decoded text and ok=false if a code is out of range.
func DecodeGSM7Septets(data string) (string, bool) {
	decoded, err := decodeGSM7([]byte(data))
	return decoded, err == nil
}

type pduCursor struct {
	data  []byte
	index int
}

func (cursor *pduCursor) byte() (byte, error) {
	if cursor.index >= len(cursor.data) {
		return 0, errors.New("SMS PDU is truncated")
	}
	value := cursor.data[cursor.index]
	cursor.index++
	return value, nil
}

func (cursor *pduCursor) bytes(count int) ([]byte, error) {
	if count < 0 || cursor.index+count > len(cursor.data) {
		return nil, errors.New("SMS PDU is truncated")
	}
	value := cursor.data[cursor.index : cursor.index+count]
	cursor.index += count
	return value, nil
}

func decodeSMSPDU(raw string) (SMSMessage, error) {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	message := SMSMessage{
		Direction:     SMSDirectionUnknown,
		Encoding:      SMSEncodingUnknown,
		StorageStatus: SMSStatusUnknown,
		RawPDU:        raw,
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) < 2 {
		if err == nil {
			err = errors.New("SMS PDU is too short")
		}
		return message, err
	}
	cursor := &pduCursor{data: decoded}
	smscLength, _ := cursor.byte()
	if int(smscLength) > len(decoded)-1 {
		return message, errors.New("SMS PDU SMSC length exceeds payload")
	}
	if smscLength > 0 {
		smsc, _ := cursor.bytes(int(smscLength))
		if len(smsc) > 1 {
			message.ServiceCenter = decodeNumericAddress(
				smsc[1:],
				(len(smsc)-1)*2,
				smsc[0],
			)
		}
	}
	firstOctet, err := cursor.byte()
	if err != nil {
		return message, err
	}
	switch firstOctet & 0x03 {
	case 0:
		message.Direction = SMSDirectionReceived
		err = decodeDeliverPDU(cursor, firstOctet, &message)
	case 1:
		message.Direction = SMSDirectionSubmitted
		err = decodeSubmitPDU(cursor, firstOctet, &message)
	case 2:
		message.Direction = SMSDirectionStatusReport
		err = decodeStatusReportPDU(cursor, &message)
	default:
		err = errors.New("unsupported SMS PDU message type")
	}
	return message, err
}

func decodeDeliverPDU(
	cursor *pduCursor,
	firstOctet byte,
	message *SMSMessage,
) error {
	address, err := readTPAddress(cursor)
	if err != nil {
		return err
	}
	message.From = address
	pid, err := cursor.byte()
	if err != nil {
		return err
	}
	dcs, err := cursor.byte()
	if err != nil {
		return err
	}
	message.ProtocolID = int(pid)
	message.DataCodingScheme = int(dcs)
	timestamp, err := cursor.bytes(7)
	if err != nil {
		return err
	}
	if parsed, timestampErr := decodeSMSTimestamp(timestamp); timestampErr == nil {
		message.ServiceCenterTimestamp = parsed
	}
	udl, err := cursor.byte()
	if err != nil {
		return err
	}
	return decodeUserData(cursor.data[cursor.index:], firstOctet, dcs, int(udl), message)
}

func decodeSubmitPDU(
	cursor *pduCursor,
	firstOctet byte,
	message *SMSMessage,
) error {
	reference, err := cursor.byte()
	if err != nil {
		return err
	}
	message.MessageReference = intPointer(int(reference))
	address, err := readTPAddress(cursor)
	if err != nil {
		return err
	}
	message.To = address
	pid, err := cursor.byte()
	if err != nil {
		return err
	}
	dcs, err := cursor.byte()
	if err != nil {
		return err
	}
	message.ProtocolID = int(pid)
	message.DataCodingScheme = int(dcs)
	switch (firstOctet >> 3) & 0x03 {
	case 2:
		if _, err := cursor.byte(); err != nil {
			return err
		}
	case 1, 3:
		if _, err := cursor.bytes(7); err != nil {
			return err
		}
	}
	udl, err := cursor.byte()
	if err != nil {
		return err
	}
	return decodeUserData(cursor.data[cursor.index:], firstOctet, dcs, int(udl), message)
}

func decodeStatusReportPDU(
	cursor *pduCursor,
	message *SMSMessage,
) error {
	reference, err := cursor.byte()
	if err != nil {
		return err
	}
	message.MessageReference = intPointer(int(reference))
	address, err := readTPAddress(cursor)
	if err != nil {
		return err
	}
	message.To = address
	scts, err := cursor.bytes(7)
	if err != nil {
		return err
	}
	discharge, err := cursor.bytes(7)
	if err != nil {
		return err
	}
	status, err := cursor.byte()
	if err != nil {
		return err
	}
	message.StatusCode = intPointer(int(status))
	message.DeliveryStatus = smsDeliveryStatus(status)
	if parsed, parseErr := decodeSMSTimestamp(scts); parseErr == nil {
		message.ServiceCenterTimestamp = parsed
	}
	if parsed, parseErr := decodeSMSTimestamp(discharge); parseErr == nil {
		message.DischargeTimestamp = parsed
	}
	return nil
}

func smsDeliveryStatus(status byte) string {
	switch {
	case status <= 0x1f:
		return "delivered"
	case status <= 0x3f:
		return "temporary_error"
	case status <= 0x5f:
		return "permanent_error"
	case status <= 0x7f:
		return "temporary_error_no_retry"
	default:
		return "reserved"
	}
}

func readTPAddress(cursor *pduCursor) (string, error) {
	length, err := cursor.byte()
	if err != nil {
		return "", err
	}
	toa, err := cursor.byte()
	if err != nil {
		return "", err
	}
	var byteCount int
	var septetCount int
	if toa&0x70 == 0x50 {
		// 3GPP TS 23.040 §9.1.2.5: For alphanumeric addresses, the length field
		// indicates the number of useful semi-octets (i.e. characters * 7 / 4, rounded up).
		// The number of characters is (length * 4) / 7 and byte count is (length + 1) / 2.
		// However, some non-standard sources specify length as the direct count of septets
		// (e.g. length=4 for 4 chars, which needs 4 bytes instead of (4+1)/2=2 bytes).
		if length >= 7 {
			byteCount = (int(length) + 1) / 2
			septetCount = int(length) * 4 / 7
		} else {
			byteCount = (int(length)*7 + 7) / 8
			septetCount = int(length)
		}
	} else {
		byteCount = (int(length) + 1) / 2
	}
	value, err := cursor.bytes(byteCount)
	if err != nil {
		return "", err
	}
	if toa&0x70 == 0x50 {
		septets, unpackErr := unpackSeptets(value, septetCount, 0)
		if unpackErr != nil {
			return "", unpackErr
		}
		return decodeGSM7(septets)
	}
	return decodeNumericAddress(value, int(length), toa), nil
}

func decodeNumericAddress(value []byte, digits int, toa byte) string {
	var result strings.Builder
	if toa&0x70 == 0x10 {
		result.WriteByte('+')
	}
	written := 0
	for _, octet := range value {
		for _, digit := range []byte{octet & 0x0f, octet >> 4} {
			if written >= digits {
				return result.String()
			}
			if digit <= 9 {
				result.WriteByte('0' + digit)
				written++
			}
		}
	}
	return result.String()
}

func decodeUserData(
	data []byte,
	firstOctet byte,
	dcs byte,
	udl int,
	message *SMSMessage,
) error {
	alphabet := dcs & 0x0c
	expectedBytes := udl
	if alphabet == 0 {
		expectedBytes = (udl*7 + 7) / 8
	}
	if expectedBytes > len(data) {
		return errors.New("SMS user data is truncated")
	}
	data = data[:expectedBytes]
	message.RawUserData = strings.ToUpper(hex.EncodeToString(data))

	headerBytes := 0
	if firstOctet&0x40 != 0 {
		if len(data) == 0 {
			return errors.New("SMS has UDHI but no user data header")
		}
		headerBytes = int(data[0]) + 1
		if headerBytes > len(data) {
			return errors.New("SMS user data header is truncated")
		}
		message.Concat = parseConcatHeader(data[1:headerBytes])
	}

	switch alphabet {
	case 0:
		message.Encoding = SMSEncodingGSM7PDU
		headerSeptets := 0
		if headerBytes > 0 {
			headerSeptets = (headerBytes*8 + 6) / 7
		}
		textSeptets := udl - headerSeptets
		septets, err := unpackSeptets(data, textSeptets, headerSeptets*7)
		if err != nil {
			return err
		}
		text, err := decodeGSM7(septets)
		message.Text = text
		return err
	case 8:
		message.Encoding = SMSEncodingUCS2PDU
		payload := data[headerBytes:]
		if len(payload)%2 != 0 {
			return errors.New("UCS2 SMS has an odd byte count")
		}
		units := make([]uint16, 0, len(payload)/2)
		for index := 0; index < len(payload); index += 2 {
			units = append(units, uint16(payload[index])<<8|uint16(payload[index+1]))
		}
		message.Text = string(utf16.Decode(units))
		return nil
	default:
		// 8-bit (binary) user data has no portable text representation, so the
		// raw payload bytes are rendered as uppercase hexadecimal after the user
		// data header is stripped. This keeps the bubble non-empty and gives a
		// faithful rendering of the delivered content rather than a blank "".
		message.Encoding = SMSEncoding8BitPDU
		payload := data[headerBytes:]
		message.Text = strings.ToUpper(hex.EncodeToString(payload))
		return nil
	}
}

func parseConcatHeader(header []byte) *SMSConcatInfo {
	for index := 0; index+1 < len(header); {
		identifier := header[index]
		length := int(header[index+1])
		index += 2
		if index+length > len(header) {
			return nil
		}
		value := header[index : index+length]
		switch {
		case identifier == 0x00 && length == 3:
			return &SMSConcatInfo{
				Reference: int(value[0]),
				Total:     int(value[1]),
				Sequence:  int(value[2]),
			}
		case identifier == 0x08 && length == 4:
			return &SMSConcatInfo{
				Reference: int(value[0])<<8 | int(value[1]),
				Total:     int(value[2]),
				Sequence:  int(value[3]),
			}
		}
		index += length
	}
	return nil
}

func decodeSMSTimestamp(value []byte) (*time.Time, error) {
	if len(value) != 7 {
		return nil, errors.New("SMS timestamp must be seven octets")
	}
	component := func(octet byte) (int, error) {
		low, high := int(octet&0x0f), int(octet>>4)
		if low > 9 || high > 9 {
			return 0, errors.New("invalid timestamp semi-octet")
		}
		return low*10 + high, nil
	}
	parts := make([]int, 6)
	for index := range parts {
		parsed, err := component(value[index])
		if err != nil {
			return nil, err
		}
		parts[index] = parsed
	}
	zoneByte := value[6]
	negative := zoneByte&0x08 != 0
	zoneByte &^= 0x08
	quarters, err := component(zoneByte)
	if err != nil || quarters > 56 {
		return nil, errors.New("invalid SMS timestamp timezone")
	}
	offset := quarters * 15 * 60
	if negative {
		offset = -offset
	}
	year := 2000 + parts[0]
	if parts[0] >= 90 {
		year = 1900 + parts[0]
	}
	if parts[1] < 1 || parts[1] > 12 ||
		parts[2] < 1 || parts[2] > 31 ||
		parts[3] > 23 || parts[4] > 59 || parts[5] > 60 {
		return nil, errors.New("invalid SMS timestamp component")
	}
	zone := time.FixedZone("SMS", offset)
	result := time.Date(
		year,
		time.Month(parts[1]),
		parts[2],
		parts[3],
		parts[4],
		parts[5],
		0,
		zone,
	)
	return &result, nil
}

func parseDecimal(value string) (int, bool) {
	number, err := strconv.Atoi(strings.Trim(strings.TrimSpace(value), `"`))
	return number, err == nil
}
