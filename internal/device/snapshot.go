package device

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"vocat/internal/modem"
)

func (manager *Manager) readSnapshot(
	ctx context.Context,
	id string,
	candidate modem.Candidate,
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
	var atiIMEI string
	snapshot.Manufacturer, snapshot.Model, snapshot.Firmware, atiIMEI = parseATI(ati.Lines)
	snapshot.IMEI = atiIMEI
	if snapshot.Model == "" && !strings.EqualFold(candidate.Product, "Android") {
		snapshot.Model = candidate.Product
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
	// Read QMI DMS operating mode before consulting NAS/AT registration.  NAS
	// can retain the last serving-system indication after DMS enters low power,
	// so treating that indication as live would show a registered network while
	// flight mode is enabled.
	nativeQMI, nativeQMIWarnings := manager.readNativeQMISnapshot(ctx, candidate)
	if nativeQMI.modeKnown {
		snapshot.OperatingMode = nativeQMI.operatingMode
		snapshot.ModeKnown = true
		snapshot.FlightMode = nativeQMI.radioOff && nativeQMI.operatingMode != 1
		snapshot.RadioOff = nativeQMI.radioOff
	}
	if !nativeQMI.radioOff {
		if registration, found := readPlatformRegistration(ctx, candidate); found {
			snapshot.RegistrationStatus = registration.Status
			snapshot.RegistrationSource = "QMI NAS"
			snapshot.PSAttached = registration.PSAttached
			if registration.PLMN != "" {
				snapshot.OperatorCode = registration.PLMN
				snapshot.OperatorName = carrierNameForPLMN(registration.PLMN, registration.Name)
			}
		}
	}
	// Qualcomm/OpenStick firmware often leaves AT+QENG empty even while QMI
	// NAS has the serving LTE tuple.  Query the same native QMI session for
	// band/channel and DMS MSISDN, using it only as a native-path supplement.
	if nativeQMI.accessTech != "" && snapshot.AccessTech == "" {
		snapshot.AccessTech = nativeQMI.accessTech
	}
	if nativeQMI.band != "" {
		snapshot.Band = nativeQMI.band
	}
	if nativeQMI.channel != "" {
		snapshot.Channel = nativeQMI.channel
	}
	snapshot.Warnings = append(snapshot.Warnings, nativeQMIWarnings...)
	if nativeQMI.iccid != "" {
		snapshot.ICCID = nativeQMI.iccid
	}
	if nativeQMI.radioOff {
		maskSnapshotForRadioOff(&snapshot)
	}
	// Native Qualcomm WWAN firmware can reject every AT ICCID command while
	// QMI-UIM still exposes the active card. Read the authoritative identity
	// first so a periodic snapshot does not emit WARN entries for expected AT
	// fallback probes (and never attributes a native card from a stale AT port).
	if snapshot.ICCID == "" {
		if iccid, native, iccidErr := manager.readNativeQMIICCID(ctx, id); native {
			if iccidErr != nil {
				snapshot.Warnings = append(snapshot.Warnings, "read native QMI ICCID: "+iccidErr.Error())
			} else {
				snapshot.ICCID = iccid
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
		response, ok := optional("AT+CGSN")
		if ok {
			snapshot.IMEI = parseIdentifier(
				response,
				[]string{"+CGSN:", "+GSN:"},
				14,
				17,
			)
		}
	}

	if snapshot.ICCID == "" {
		var ccidErr error
		for _, command := range []string{"AT+CCID", "AT+QCCID", "AT+ICCID"} {
			var ccid modem.Response
			ccid, ccidErr = manager.command(ctx, client, command)
			if ccidErr != nil {
				continue
			}
			snapshot.ICCID = parseICCIDIdentifier(
				ccid,
				[]string{"+CCID:", "+QCCID:", "+ICCID:", "ICCID:"},
				18,
				22,
			)
			if snapshot.ICCID != "" {
				break
			}
		}
		if snapshot.ICCID == "" && ccidErr != nil {
			snapshot.Warnings = append(snapshot.Warnings, "read ICCID: "+ccidErr.Error())
		}
	}
	if response, ok := optional("AT+CIMI"); ok {
		snapshot.IMSI = parseIdentifier(response, []string{"+CIMI:"}, 10, 18)
	}
	// Native Qualcomm WWAN firmware can refuse AT+CIMI entirely while DMS still
	// exposes the subscriber IMSI. Treat the QMI DMS value as the authoritative
	// identity so a post-switch overview does not fall back to a stale or empty
	// IMSI when the AT serially begins reporting the new card.
	if nativeQMI.imsi != "" {
		snapshot.IMSI = nativeQMI.imsi
	}
	if !nativeQMI.modeKnown {
		if response, ok := optional("AT+CFUN?"); ok {
			if mode, found := parseCFUN(response); found {
				snapshot.OperatingMode = mode
				snapshot.ModeKnown = true
				snapshot.FlightMode = isRadioOffMode(mode)
				snapshot.RadioOff = snapshot.FlightMode
			}
		}
	}

	phone, warnings := manager.readPhoneNumber(ctx, client)
	if nativeQMI.phone.Number != "" {
		phone = nativeQMI.phone
	} else if isQMINotProvisioned(nativeQMI.phoneErr) {
		phone.Status = "CNUM、Own Numbers、EF_MSISDN 与 QMI DMS MSISDN 均未配置；号码不能由 IMSI/ICCID 推导，需要运营商或 IMS/VoWiFi 注册侧提供"
	}
	snapshot.Phone = phone
	snapshot.Warnings = append(snapshot.Warnings, warnings...)
	snapshot.UpdatedAt = time.Now().UTC()
	return snapshot, nil
}

// maskSnapshotForRadioOff removes stale NAS/AT serving data while preserving
// modem identity and SIM fields.  The DMS operating mode is the authoritative
// source when the radio is in flight/low-power mode.
func maskSnapshotForRadioOff(snapshot *Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.RegistrationStatus = 0
	snapshot.RegistrationSource = "QMI DMS"
	snapshot.PSAttached = false
	snapshot.OperatorName = ""
	snapshot.OperatorCode = ""
	snapshot.AccessTech = ""
	snapshot.Band = ""
	snapshot.Channel = ""
	snapshot.SignalRaw = nil
	snapshot.SignalPercent = nil
	snapshot.RSSIDBm = nil
	snapshot.RSRP = nil
	snapshot.RSRQ = nil
	snapshot.SINR = nil
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

func parseATI(lines []string) (manufacturer, model, firmware, imei string) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "MANUFACTURER:"):
			manufacturer = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		case strings.HasPrefix(upper, "MODEL:"):
			model = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		case strings.HasPrefix(upper, "REVISION:"):
			firmware = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		case strings.HasPrefix(upper, "IMEI:"):
			response := modem.Response{Lines: []string{line}}
			imei = parseIdentifier(response, []string{"IMEI:"}, 14, 17)
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
	// Native Qualcomm WWAN firmware exposes QMI DMS offline as CFUN=7.
	return mode == 0 || mode == 4 || mode == 7
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
