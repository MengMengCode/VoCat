package device

import (
	"context"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"vocat/internal/modem"
)

func (manager *Manager) readSnapshot(
	ctx context.Context,
	id string,
	candidate modem.Candidate,
	backend string,
	previousICCID string,
	previousSnapshot *Snapshot,
	client modem.Client,
) (Snapshot, error) {
	snapshot := Snapshot{
		DeviceID:      id,
		Port:          candidate.ATPort.OpenPath(),
		OperatingMode: -1,
		UpdatedAt:     time.Now().UTC(),
	}
	ati, err := manager.command(ctx, client, "ATI")
	if err != nil {
		return snapshot, fmt.Errorf("probe modem: %w", err)
	}
	snapshot.Responsive = true
	snapshot.Manufacturer, snapshot.Model, snapshot.Firmware = parseATI(ati.Lines)
	if snapshot.Model == "" && !strings.EqualFold(candidate.Product, "Android") {
		snapshot.Model = candidate.Product
	}
	// Native MHI/QMI devices expose their immutable modem identity through DMS.
	// Read it before any SIM-dependent AT probes: a missing/bad card can make
	// those commands slow or fail, but must never prevent IMEI from appearing.
	if strings.EqualFold(strings.TrimSpace(backend), "qmi") && isNativeQMICandidate(candidate) {
		qmiContext, cancelQMI := manager.withTimeout(ctx, manager.commandTimeout*5)
		qmiIMEI, qmiErr := manager.readNativeQMIIMEI(qmiContext, candidate)
		cancelQMI()
		if qmiErr == nil {
			snapshot.IMEI = qmiIMEI
		} else {
			snapshot.Warnings = append(snapshot.Warnings, "read IMEI via QMI DMS: "+qmiErr.Error())
		}
	}

	optional := func(command string) (modem.Response, bool) {
		response, commandErr := manager.command(ctx, client, command)
		if commandErr != nil {
			snapshot.Warnings = append(snapshot.Warnings, commandErr.Error())
			return response, false
		}
		return response, true
	}

	if response, ok := optional("AT+CPIN?"); ok {
		snapshot.SIMStatus, snapshot.SIMReady = parseCPIN(response)
	}
	ccid, ccidErr := manager.command(ctx, client, "AT+CCID")
	if ccidErr != nil {
		ccid, ccidErr = manager.command(ctx, client, "AT+QCCID")
	}
	if ccidErr != nil && strings.EqualFold(strings.TrimSpace(backend), "qmi") && isNativeQMICandidate(candidate) &&
		strings.EqualFold(strings.TrimSpace(snapshot.SIMStatus), "READY") {
		// Without a READY SIM the QMI UIM ICCID read blocks until its (long)
		// timeout, and every refresh holds the device lock while it does so,
		// starving the AT terminal. Only fall back to QMI when the AT CPIN
		// probe already proved a card is present.
		qmiContext, cancelQMI := manager.withTimeout(ctx, manager.commandTimeout*5)
		qmiICCID, qmiErr := manager.readNativeQMIICCID(qmiContext, candidate)
		cancelQMI()
		if qmiErr == nil {
			snapshot.ICCID = qmiICCID
			ccidErr = nil
		} else {
			snapshot.Warnings = append(snapshot.Warnings, "read ICCID via QMI UIM: "+qmiErr.Error())
		}
	}
	if ccidErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "read ICCID: "+ccidErr.Error())
	} else {
		if snapshot.ICCID == "" {
			snapshot.ICCID = parseICCIDIdentifier(ccid, []string{"+CCID:", "+QCCID:"}, 18, 22)
		}
	}
	previousICCID = strings.TrimSpace(previousICCID)
	if previousICCID != "" && snapshot.ICCID != "" && !strings.EqualFold(previousICCID, snapshot.ICCID) {
		// A different physical SIM must never inherit the previous card's
		// permission to use cellular RF. Disable RF before reading serving-cell
		// or operator state; policy reconciliation will then start VoWiFi.
		if _, err := manager.command(ctx, client, "AT+CFUN=4"); err != nil {
			return snapshot, fmt.Errorf("protect changed SIM with RF off: %w", err)
		}
		snapshot.SIMChanged = true
	}
	if response, ok := optional("AT+CIMI"); ok {
		snapshot.IMSI = parseIdentifier(response, []string{"+CIMI:"}, 10, 18)
	}
	// EF_SPN is the SIM-issued brand (for example "Lebara"), which is distinct
	// from the IMSI sponsor/core PLMN. A Lebara UK subscription may therefore
	// legitimately carry a Vodafone NL IMSI while still presenting Lebara as
	// its customer-facing operator. Failure is intentionally silent because
	// EF_SPN is optional and some physical SIMs deny CRSM access to it.
	if response, spnErr := manager.command(ctx, client, "AT+CRSM=176,28486,0,0,17"); spnErr == nil {
		snapshot.SPN = parseSPN(response)
	}
	if previousSnapshot != nil && previousSnapshot.IdentityFilesRead &&
		strings.EqualFold(strings.TrimSpace(previousSnapshot.ICCID), strings.TrimSpace(snapshot.ICCID)) {
		snapshot.MNCLength = previousSnapshot.MNCLength
		snapshot.GID1 = previousSnapshot.GID1
		snapshot.GID2 = previousSnapshot.GID2
		snapshot.IdentityFilesRead = true
	} else {
		// Android's carrier resolver does not identify MVNOs from MCC/MNC alone.
		// Read these files once per inserted ICCID and cache even an empty result;
		// repeatedly probing unsupported EFs would add avoidable modem traffic.
		if efAD := manager.readTransparentSIMFile(ctx, client, 28589); len(efAD) >= 4 {
			if length := int(efAD[3] & 0x0f); length == 2 || length == 3 {
				snapshot.MNCLength = length
			}
		}
		snapshot.GID1 = encodeSIMGroupID(manager.readTransparentSIMFile(ctx, client, 28478))
		snapshot.GID2 = encodeSIMGroupID(manager.readTransparentSIMFile(ctx, client, 28479))
		snapshot.IdentityFilesRead = true
	}
	if response, ok := optional("AT+CSQ"); ok {
		snapshot.SignalRaw, snapshot.SignalPercent, snapshot.RSSIDBm = parseCSQ(response)
	}
	servingPLMN := ""
	if response, ok := optional(`AT+QENG="servingcell"`); ok {
		metrics := parseQENG(response)
		servingPLMN = metrics.PLMN
		snapshot.AccessTech = metrics.AccessTech
		snapshot.Band = metrics.Band
		snapshot.Channel = metrics.Channel
		snapshot.RSRP = metrics.RSRP
		snapshot.RSRQ = metrics.RSRQ
		snapshot.SINR = metrics.SINR
		if metrics.RSSI != nil {
			snapshot.RSSIDBm = metrics.RSSI
		}
	}
	if response, ok := optional("AT+COPS?"); ok {
		operator := parseCOPS(response)
		if operator.Code != "" {
			snapshot.OperatorCode = operator.Code
		} else {
			snapshot.OperatorCode = servingPLMN
		}
		snapshot.OperatorName = carrierNameForPLMN(snapshot.OperatorCode, operator.Name)
		if snapshot.AccessTech == "" {
			snapshot.AccessTech = operator.AccessTech
		}
	}
	for _, command := range []string{"AT+CEREG?", "AT+CGREG?", "AT+CREG?"} {
		response, registrationErr := manager.command(ctx, client, command)
		if registrationErr != nil {
			continue
		}
		if status, found := parseRegistrationStatus(response); found {
			snapshot.RegistrationStatus = status
			snapshot.RegistrationSource = strings.TrimSuffix(strings.TrimPrefix(command, "AT+"), "?")
			break
		}
	}
	if strings.EqualFold(backend, "qmi") {
		registration, found := readPlatformRegistration(ctx, candidate)
		if found {
			snapshot.RegistrationStatus = registration.Status
			snapshot.RegistrationSource = "QMI NAS"
			snapshot.PSAttached = registration.PSAttached
			if registration.PLMN != "" {
				snapshot.OperatorCode = registration.PLMN
				snapshot.OperatorName = carrierNameForPLMN(registration.PLMN, registration.Name)
			}
		}
	}
	if snapshot.RegistrationSource == "" && (snapshot.OperatorName != "" || snapshot.OperatorCode != "") {
		// Older firmware can omit registration queries while COPS still proves
		// that an operator is selected.
		snapshot.RegistrationStatus = 1
		snapshot.RegistrationSource = "COPS"
	}
	if snapshot.IMEI == "" {
		// AT+CGSN on some MHI modems (the UFI dongle behind the OpenStick 410)
		// returns the IMEI line but never a final OK, so it would block until the
		// caller's deadline (30s during a periodic refresh) and starve every other
		// device operation behind the lock. Give it an independent short timeout
		// and let the WWAN transport's drain discard the trailing stale bytes.
		cgsnCtx, cancelCGSN := context.WithTimeout(ctx, manager.commandTimeout)
		cgsnResponse, cgsnErr := manager.command(cgsnCtx, client, "AT+CGSN")
		cancelCGSN()
		if cgsnErr == nil {
			if imei := parseIdentifier(cgsnResponse, []string{"+CGSN:", "+GSN:"}, 14, 17); imei != "" {
				snapshot.IMEI = imei
			}
		}
	}
	if snapshot.IMEI == "" && strings.EqualFold(strings.TrimSpace(backend), "qmi") && isNativeQMICandidate(candidate) {
		qmiContext, cancelQMI := manager.withTimeout(ctx, manager.commandTimeout*5)
		qmiIMEI, qmiErr := manager.readNativeQMIIMEI(qmiContext, candidate)
		cancelQMI()
		if qmiErr == nil {
			snapshot.IMEI = qmiIMEI
		} else {
			snapshot.Warnings = append(snapshot.Warnings, "read IMEI via QMI DMS: "+qmiErr.Error())
		}
	}
	if snapshot.IMEI == "" && previousSnapshot != nil {
		// IMEI is hardware identity and does not change with the inserted card.
		// Preserve a prior successful read across a transient QMI/AT failure.
		snapshot.IMEI = previousSnapshot.IMEI
	}
	if response, ok := optional("AT+CFUN?"); ok {
		if mode, found := parseCFUN(response); found {
			snapshot.OperatingMode = mode
			snapshot.ModeKnown = true
			snapshot.FlightMode = isRadioOffMode(mode)
			snapshot.RadioOff = snapshot.FlightMode
		}
	}

	phone, warnings := manager.readPhoneNumber(ctx, client)
	snapshot.Phone = phone
	snapshot.Warnings = append(snapshot.Warnings, warnings...)
	snapshot.UpdatedAt = time.Now().UTC()
	return snapshot, nil
}

func (manager *Manager) readTransparentSIMFile(ctx context.Context, client modem.Client, fileID int) []byte {
	response, err := manager.command(ctx, client, fmt.Sprintf("AT+CRSM=192,%d,0,0,0", fileID))
	if err != nil {
		return nil
	}
	size := transparentSIMFileSize(crsmPayload(response))
	if size <= 0 || size > 64 {
		return nil
	}
	response, err = manager.command(ctx, client, fmt.Sprintf("AT+CRSM=176,%d,0,0,%d", fileID, size))
	if err != nil {
		return nil
	}
	return crsmPayload(response)
}

func transparentSIMFileSize(payload []byte) int {
	// USIM FCP templates contain file size in tag 0x80. Skip the outer 0x62
	// template and walk its immediate TLVs.
	content := payload
	if len(content) >= 2 && content[0] == 0x62 {
		length, header, ok := berLength(content[1:])
		if !ok || 1+header+length > len(content) {
			return 0
		}
		content = content[1+header : 1+header+length]
	}
	for offset := 0; offset+2 <= len(content); {
		tag := content[offset]
		length, header, ok := berLength(content[offset+1:])
		start := offset + 1 + header
		end := start + length
		if !ok || end > len(content) {
			break
		}
		if tag == 0x80 && (length == 1 || length == 2) {
			size := 0
			for _, value := range content[start:end] {
				size = size<<8 | int(value)
			}
			return size
		}
		offset = end
	}
	// Legacy GSM GET RESPONSE data stores file size in bytes 2 and 3.
	if len(payload) >= 4 && payload[0] != 0x62 {
		return int(payload[2])<<8 | int(payload[3])
	}
	return 0
}

func berLength(value []byte) (length, header int, ok bool) {
	if len(value) == 0 {
		return 0, 0, false
	}
	if value[0] < 0x80 {
		return int(value[0]), 1, true
	}
	count := int(value[0] & 0x7f)
	if count == 0 || count > 2 || len(value) < count+1 {
		return 0, 0, false
	}
	for _, item := range value[1 : count+1] {
		length = length<<8 | int(item)
	}
	return length, count + 1, true
}

func encodeSIMGroupID(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	allPadding := true
	for _, item := range value {
		if item != 0xff {
			allPadding = false
			break
		}
	}
	if allPadding {
		return ""
	}
	return strings.ToUpper(hex.EncodeToString(value))
}

func parseSPN(response modem.Response) string {
	value := valueAfterPrefix(response, "+CRSM:")
	fields := csvValues(value)
	if len(fields) < 3 {
		return ""
	}
	sw1, sw1Err := strconv.Atoi(strings.TrimSpace(fields[0]))
	sw2, sw2Err := strconv.Atoi(strings.TrimSpace(fields[1]))
	if sw1Err != nil || sw2Err != nil || (sw1 != 0x90 && sw1 != 0x91 && sw1 != 0x9f) || sw2 < 0 || sw2 > 255 {
		return ""
	}
	raw, err := hex.DecodeString(strings.Trim(strings.TrimSpace(fields[2]), `"`))
	if err != nil || len(raw) < 2 {
		return ""
	}
	alpha := raw[1:] // byte 0 is the display-condition bit field.
	for len(alpha) > 0 && (alpha[len(alpha)-1] == 0xff || alpha[len(alpha)-1] == 0x00) {
		alpha = alpha[:len(alpha)-1]
	}
	if len(alpha) == 0 {
		return ""
	}
	if alpha[0] == 0x80 {
		ucs2 := alpha[1:]
		if len(ucs2)%2 != 0 {
			ucs2 = ucs2[:len(ucs2)-1]
		}
		units := make([]uint16, 0, len(ucs2)/2)
		for index := 0; index+1 < len(ucs2); index += 2 {
			unit := uint16(ucs2[index])<<8 | uint16(ucs2[index+1])
			if unit != 0xffff && unit != 0 {
				units = append(units, unit)
			}
		}
		return strings.TrimSpace(string(utf16.Decode(units)))
	}
	// EF_SPN uses the unpacked GSM default alphabet. Its printable Latin subset
	// is byte-compatible with UTF-8/ASCII and covers operator brands in practice.
	printable := make([]byte, 0, len(alpha))
	for _, value := range alpha {
		if value >= 0x20 && value <= 0x7e {
			printable = append(printable, value)
		}
	}
	return strings.TrimSpace(string(printable))
}

func parseRegistrationStatus(response modem.Response) (int, bool) {
	for _, prefix := range []string{"+CEREG:", "+CGREG:", "+CREG:"} {
		values := csvValues(valueAfterPrefix(response, prefix))
		if len(values) == 0 {
			continue
		}
		index := 0
		// Query responses are <n>,<stat>; unsolicited responses are <stat>.
		if len(values) >= 2 {
			index = 1
		}
		status, err := strconv.Atoi(strings.TrimSpace(values[index]))
		if err == nil && status >= 0 && status <= 10 {
			return status, true
		}
	}
	return 0, false
}

func parseATI(lines []string) (manufacturer, model, firmware string) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "REVISION:"):
			firmware = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		case strings.Contains(upper, "QUECTEL"):
			manufacturer = line
		case strings.HasPrefix(upper, "EC20") || strings.HasPrefix(upper, "EC25"):
			model = line
		}
	}
	return
}

func parseCPIN(response modem.Response) (string, bool) {
	value := strings.ToUpper(valueAfterPrefix(response, "+CPIN:"))
	switch {
	case strings.Contains(value, "READY"):
		return "ready", true
	case strings.Contains(value, "SIM PIN"):
		return "pin_required", false
	case strings.Contains(value, "SIM PUK"):
		return "puk_required", false
	case strings.Contains(value, "NOT INSERTED"):
		return "not_inserted", false
	case value == "":
		return "unknown", false
	default:
		return strings.ToLower(strings.ReplaceAll(value, " ", "_")), false
	}
}

func parseCSQ(response modem.Response) (raw, percent, dbm *int) {
	values := csvValues(valueAfterPrefix(response, "+CSQ:"))
	if len(values) == 0 {
		return nil, nil, nil
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value < 0 || value > 31 {
		return nil, nil, nil
	}
	raw = intPointer(value)
	scaled := (value*100 + 15) / 31
	percent = intPointer(scaled)
	signalDBM := -113 + value*2
	dbm = intPointer(signalDBM)
	return
}

type qengMetrics struct {
	PLMN       string
	AccessTech string
	Band       string
	Channel    string
	RSSI       *int
	RSRP       *int
	RSRQ       *int
	SINR       *int
}

func parseQENG(response modem.Response) qengMetrics {
	for _, line := range response.Lines {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+QENG:") {
			continue
		}
		values := csvValues(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
		if len(values) < 3 || !strings.EqualFold(values[0], "servingcell") {
			continue
		}
		result := qengMetrics{AccessTech: strings.ToUpper(values[2])}
		if strings.EqualFold(values[2], "LTE") && len(values) >= 17 {
			if decimalDigits(values[4], 3, 3) && decimalDigits(values[5], 2, 3) {
				result.PLMN = values[4] + values[5]
			}
			result.Channel = values[8]
			if values[9] != "" {
				result.Band = "B" + values[9]
			}
			result.RSRP = parseOptionalInt(values[13])
			result.RSRQ = parseOptionalInt(values[14])
			result.RSSI = parseOptionalInt(values[15])
			result.SINR = parseOptionalInt(values[16])
		}
		return result
	}
	return qengMetrics{}
}

func decimalDigits(value string, minimum, maximum int) bool {
	value = strings.TrimSpace(value)
	return len(value) >= minimum && len(value) <= maximum && strings.IndexFunc(value, func(character rune) bool {
		return character < '0' || character > '9'
	}) < 0
}

type operatorInfo struct {
	Name       string
	Code       string
	AccessTech string
}

func parseCOPS(response modem.Response) operatorInfo {
	values := csvValues(valueAfterPrefix(response, "+COPS:"))
	if len(values) < 3 {
		return operatorInfo{}
	}
	result := operatorInfo{Name: values[2]}
	format, _ := strconv.Atoi(values[1])
	if format == 2 {
		result.Code = values[2]
		result.Name = ""
	}
	if len(values) >= 4 {
		result.AccessTech = accessTechnology(values[3])
	}
	return result
}

func accessTechnology(value string) string {
	switch strings.TrimSpace(value) {
	case "0":
		return "GSM"
	case "2":
		return "UTRAN"
	case "3":
		return "EDGE"
	case "4":
		return "HSDPA"
	case "5":
		return "HSUPA"
	case "6":
		return "HSPA"
	case "7":
		return "LTE"
	case "9":
		return "NR5G"
	default:
		return ""
	}
}

func parseCFUN(response modem.Response) (int, bool) {
	values := csvValues(valueAfterPrefix(response, "+CFUN:"))
	if len(values) == 0 {
		return 0, false
	}
	mode, err := strconv.Atoi(values[0])
	return mode, err == nil
}

func isRadioOffMode(mode int) bool {
	return mode == 0 || mode == 4
}

func valueAfterPrefix(response modem.Response, prefix string) string {
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(prefix)) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func csvValues(value string) []string {
	reader := csv.NewReader(strings.NewReader(value))
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true
	record, err := reader.Read()
	if err != nil && err != io.EOF {
		return nil
	}
	for index := range record {
		record[index] = strings.TrimSpace(record[index])
	}
	return record
}

func firstDigitLine(response modem.Response, minimum, maximum int) string {
	for _, line := range response.Lines {
		value := strings.TrimSpace(line)
		if len(value) < minimum || len(value) > maximum {
			continue
		}
		if strings.IndexFunc(value, func(character rune) bool {
			return !unicode.IsDigit(character)
		}) < 0 {
			return value
		}
	}
	return ""
}

func parseIdentifier(
	response modem.Response,
	prefixes []string,
	minimum, maximum int,
) string {
	for _, prefix := range prefixes {
		value := strings.Trim(valueAfterPrefix(response, prefix), `" `)
		if len(value) >= minimum && len(value) <= maximum &&
			strings.IndexFunc(value, func(character rune) bool {
				return !unicode.IsDigit(character)
			}) < 0 {
			return value
		}
	}
	return firstDigitLine(response, minimum, maximum)
}

// parseICCIDIdentifier accepts all trailing hexadecimal F nibbles exposed from
// the fixed 10-octet EF-ICCID representation. A 19-digit ICCID has one filler
// nibble while an 18-digit ICCID has two; neither is part of the identifier.
func parseICCIDIdentifier(
	response modem.Response,
	prefixes []string,
	minimum, maximum int,
) string {
	normalize := func(value string) string {
		value = strings.Trim(value, `" `)
		value = strings.TrimRight(value, "Ff")
		if len(value) >= minimum && len(value) <= maximum &&
			strings.IndexFunc(value, func(character rune) bool { return !unicode.IsDigit(character) }) < 0 {
			return value
		}
		return ""
	}
	for _, prefix := range prefixes {
		if value := normalize(valueAfterPrefix(response, prefix)); value != "" {
			return value
		}
	}
	for _, line := range response.Lines {
		if value := normalize(strings.TrimSpace(line)); value != "" {
			return value
		}
	}
	return ""
}

func parseOptionalInt(value string) *int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return intPointer(number)
}

func intPointer(value int) *int {
	return &value
}
