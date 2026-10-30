package modem

import (
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
)

// ATProfile groups the protocol-level AT capabilities used by device flows.
// Its implementations are immutable and safe to share between candidates.
type ATProfile struct {
	APDU     APDUCommandProfile
	Operator OperatorATProfile
	PDP      PDPATProfile
	Identity IdentityATProfile
}

// ATProfileOverride replaces only the non-nil capability groups for a
// chipset family. A nil group inherits the standard 3GPP implementation.
type ATProfileOverride struct {
	Chipset  Chipset
	APDU     APDUCommandProfile
	Operator OperatorATProfile
	PDP      PDPATProfile
	Identity IdentityATProfile
}

type APDUCommandProfile interface {
	Build(command APDUCommand, channel int, apdu []byte, form APDUForm) (string, error)
	Parse(command APDUCommand, response Response) ([]byte, error)
}

// APDUFormPreference optionally supplies a verified initial command form.
// A dialect's successfully learned form takes precedence over this hint.
type APDUFormPreference interface {
	PreferredForm(command APDUCommand) APDUForm
}

type OperatorATProfile interface {
	ScanCommand() string
	SelectionQueryCommand() string
	ManualSelectionCommand(plmn string, accessTechnology *int) (string, error)
	AutomaticSelectionCommand() string
	DeregisterCommand() string
	SetOperatorFormatCommand(format int) string
	ParseScan(Response) []ATOperatorRecord
	ParseSelection(Response) (ATOperatorSelection, error)
}

type PDPATProfile interface {
	ConfigureContextCommand(cid int, ipVersion, apn string) (string, error)
	ConfigureAuthenticationCommand(cid, authCode int, username, password string) (string, error)
	AttachCommand(enabled bool) string
	ActivateCommand(cid int, enabled bool) string
}

// IdentityATProfile selects the commands used to read the modem's identity
// (manufacturer/model/firmware), its ICCID, and whether the call-list query
// AT+CLCC is implemented at all. Device flows treat every entry as optional
// bookkeeping except the liveness rule: an entirely silent identity probe
// still fails the snapshot.
type IdentityATProfile interface {
	// DeviceInfoCommands returns the ordered identification probes. The
	// standard profile uses the single ATI probe; vendor profiles list the
	// platform's own commands and each probe is tolerated independently.
	DeviceInfoCommands() []string
	// ICCIDCommands returns the ordered ICCID query fallback chain.
	ICCIDCommands() []string
	// SupportsCLCC reports whether AT+CLCC can be polled for call state.
	SupportsCLCC() bool
}

type APDUCommand uint8

const (
	APDUCSIM APDUCommand = iota + 1
	APDUCGLA
)

type APDUForm uint8

const (
	APDUQuoted APDUForm = iota
	APDUUnquoted
)

type ATOperatorRecord struct {
	StatusCode           string
	LongName             string
	ShortName            string
	Numeric              string
	AccessTechnologyCode string
}

type ATOperatorSelection struct {
	Mode                 int
	Format               int
	Operator             string
	AccessTechnologyCode string
}

type standardAPDUProfile struct{}
type zxicAPDUProfile struct{ standardAPDUProfile }
type standardOperatorProfile struct{}
type standardPDPProfile struct{}
type standardIdentityATProfile struct{}
type zxicIdentityATProfile struct{}

// builtinProfileOverrides holds adaptations for the USB identities verified
// by ChipsetForUSB. Unverified products retain the standard command set.
var builtinProfileOverrides = []ATProfileOverride{
	{
		// The verified UZ901 firmware ships no ATI, no AT+CCID family, and no
		// AT+CLCC: each is answered with vendor +CME ERROR: 6003 (command
		// not recognized). Identity comes from the prefixed +CGMI/+CGMM/
		// +CGMR queries, the ICCID from the +ZICCID query, and call state
		// only from vendor URCs.
		Chipset:  ChipsetZXIC,
		APDU:     zxicAPDUProfile{},
		Identity: zxicIdentityATProfile{},
	},
}

func ResolveATProfile(candidate Candidate) ATProfile {
	return resolveATProfile(candidate, builtinProfileOverrides)
}

func resolveATProfile(candidate Candidate, overrides []ATProfileOverride) ATProfile {
	profile := ATProfile{
		APDU:     standardAPDUProfile{},
		Operator: standardOperatorProfile{},
		PDP:      standardPDPProfile{},
		Identity: standardIdentityATProfile{},
	}
	chipset := ChipsetForUSB(candidate.VendorID, candidate.ProductID)
	for _, override := range overrides {
		if chipset != override.Chipset {
			continue
		}
		if override.APDU != nil {
			profile.APDU = override.APDU
		}
		if override.Operator != nil {
			profile.Operator = override.Operator
		}
		if override.PDP != nil {
			profile.PDP = override.PDP
		}
		if override.Identity != nil {
			profile.Identity = override.Identity
		}
		break
	}
	return profile
}

func (standardIdentityATProfile) DeviceInfoCommands() []string { return []string{"ATI"} }

func (standardIdentityATProfile) ICCIDCommands() []string {
	return []string{"AT+CCID", "AT+QCCID", "AT+MCCID"}
}

func (standardIdentityATProfile) SupportsCLCC() bool { return true }

func (zxicIdentityATProfile) DeviceInfoCommands() []string {
	return []string{"AT+CGMI", "AT+CGMM", "AT+CGMR"}
}

func (zxicIdentityATProfile) ICCIDCommands() []string { return []string{"AT+ZICCID?"} }

func (zxicIdentityATProfile) SupportsCLCC() bool { return false }

// The lab UZ901 firmware accepts unquoted CSIM/CGLA payloads. Prefer that
// verified form directly rather than replaying APDUs after vendor errors.
func (zxicAPDUProfile) PreferredForm(APDUCommand) APDUForm { return APDUUnquoted }

func (standardAPDUProfile) Build(command APDUCommand, channel int, apdu []byte, form APDUForm) (string, error) {
	if len(apdu) == 0 {
		return "", errors.New("modem: APDU is empty")
	}
	if form != APDUQuoted && form != APDUUnquoted {
		return "", errors.New("modem: invalid APDU form")
	}
	prefix := ""
	switch command {
	case APDUCSIM:
		prefix = "AT+CSIM=" + strconv.Itoa(len(apdu)*2) + ","
	case APDUCGLA:
		if channel < 1 || channel > 19 {
			return "", errors.New("modem: invalid APDU logical channel")
		}
		prefix = "AT+CGLA=" + strconv.Itoa(channel) + "," + strconv.Itoa(len(apdu)*2) + ","
	default:
		return "", errors.New("modem: invalid APDU command")
	}
	encoded := strings.ToUpper(hex.EncodeToString(apdu))
	if form == APDUQuoted {
		encoded = `"` + encoded + `"`
	}
	return prefix + encoded, nil
}

func (standardAPDUProfile) Parse(command APDUCommand, response Response) ([]byte, error) {
	prefix := ""
	switch command {
	case APDUCSIM:
		prefix = "+CSIM:"
	case APDUCGLA:
		prefix = "+CGLA:"
	default:
		return nil, errors.New("modem: invalid APDU command")
	}
	value := valueAfterATPrefix(response, prefix)
	if value == "" {
		return nil, errors.New("modem: APDU response is missing")
	}
	fields := parseATCSV(value)
	if len(fields) < 2 {
		return nil, errors.New("modem: malformed APDU response")
	}
	declared, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil || declared < 0 {
		return nil, errors.New("modem: invalid APDU response length")
	}
	encoded := strings.TrimSpace(strings.Trim(fields[1], `"`))
	if len(encoded) < 4 {
		return nil, errors.New("modem: APDU response is shorter than its status word")
	}
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("modem: APDU response is not hexadecimal")
	}
	if declared != len(encoded) && declared != len(data) {
		return nil, errors.New("modem: APDU response length mismatch")
	}
	return data, nil
}

func (standardOperatorProfile) ScanCommand() string { return "AT+COPS=?" }

func (standardOperatorProfile) SelectionQueryCommand() string { return "AT+COPS?" }

func (standardOperatorProfile) ManualSelectionCommand(plmn string, accessTechnology *int) (string, error) {
	plmn = strings.TrimSpace(plmn)
	if (len(plmn) != 5 && len(plmn) != 6) || !isDecimal(plmn) {
		return "", errors.New("modem: operator PLMN must contain 5 or 6 digits")
	}
	command := `AT+COPS=1,2,"` + plmn + `"`
	if accessTechnology != nil {
		if *accessTechnology < 0 || *accessTechnology > 9 {
			return "", errors.New("modem: invalid operator access technology")
		}
		command += "," + strconv.Itoa(*accessTechnology)
	}
	return command, nil
}

func (standardOperatorProfile) AutomaticSelectionCommand() string { return "AT+COPS=0" }

func (standardOperatorProfile) DeregisterCommand() string { return "AT+COPS=2" }

func (standardOperatorProfile) SetOperatorFormatCommand(format int) string {
	return "AT+COPS=3," + strconv.Itoa(format)
}

func (standardOperatorProfile) ParseScan(response Response) []ATOperatorRecord {
	result := make([]ATOperatorRecord, 0)
	for _, line := range response.Lines {
		payload, ok := valueAfterATPrefixOK(line, "+COPS:")
		if !ok {
			continue
		}
		for _, tuple := range extractOperatorTuples(payload) {
			fields := parseATCSV(tuple)
			if len(fields) < 4 {
				continue
			}
			record := ATOperatorRecord{
				StatusCode: strings.TrimSpace(fields[0]),
				LongName:   strings.TrimSpace(fields[1]),
				ShortName:  strings.TrimSpace(fields[2]),
				Numeric:    strings.TrimSpace(fields[3]),
			}
			if len(fields) >= 5 {
				record.AccessTechnologyCode = strings.TrimSpace(fields[4])
			}
			result = append(result, record)
		}
	}
	return result
}

func (standardOperatorProfile) ParseSelection(response Response) (ATOperatorSelection, error) {
	value := valueAfterATPrefix(response, "+COPS:")
	if value == "" {
		return ATOperatorSelection{}, errors.New("modem: operator selection is missing")
	}
	fields := parseATCSV(value)
	if len(fields) == 0 {
		return ATOperatorSelection{}, errors.New("modem: malformed operator selection")
	}
	mode, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return ATOperatorSelection{}, errors.New("modem: invalid operator selection mode")
	}
	selection := ATOperatorSelection{Mode: mode}
	if len(fields) > 1 {
		selection.Format, err = strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			return ATOperatorSelection{}, errors.New("modem: invalid operator selection format")
		}
	}
	if len(fields) > 2 {
		selection.Operator = strings.TrimSpace(fields[2])
	}
	if len(fields) > 3 {
		selection.AccessTechnologyCode = strings.TrimSpace(fields[3])
	}
	return selection, nil
}

func (standardPDPProfile) ConfigureContextCommand(cid int, ipVersion, apn string) (string, error) {
	if !validPDPContextID(cid) {
		return "", errors.New("modem: invalid PDP context identifier")
	}
	ipVersion = strings.ToUpper(strings.TrimSpace(ipVersion))
	switch ipVersion {
	case "IP", "IPV6", "IPV4V6":
	default:
		return "", errors.New("modem: invalid PDP IP type")
	}
	quotedAPN, err := quoteATString(apn)
	if err != nil {
		return "", err
	}
	return "AT+CGDCONT=" + strconv.Itoa(cid) + `,"` + ipVersion + `",` + quotedAPN, nil
}

func (standardPDPProfile) ConfigureAuthenticationCommand(cid, authCode int, username, password string) (string, error) {
	if !validPDPContextID(cid) {
		return "", errors.New("modem: invalid PDP context identifier")
	}
	if authCode < 0 || authCode > 3 {
		return "", errors.New("modem: invalid PDP authentication type")
	}
	quotedUsername, err := quoteATString(username)
	if err != nil {
		return "", errors.New("modem: invalid PDP authentication value")
	}
	quotedPassword, err := quoteATString(password)
	if err != nil {
		return "", errors.New("modem: invalid PDP authentication value")
	}
	return "AT+CGAUTH=" + strconv.Itoa(cid) + "," + strconv.Itoa(authCode) + "," + quotedUsername + "," + quotedPassword, nil
}

func (standardPDPProfile) AttachCommand(enabled bool) string {
	if enabled {
		return "AT+CGATT=1"
	}
	return "AT+CGATT=0"
}

func (standardPDPProfile) ActivateCommand(cid int, enabled bool) string {
	active := 0
	if enabled {
		active = 1
	}
	return "AT+CGACT=" + strconv.Itoa(active) + "," + strconv.Itoa(cid)
}

func ParseCCHOChannel(response Response) (int, error) {
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if value, ok := valueAfterATPrefixOK(line, "+CCHO:"); ok {
			return parseCCHOChannelNumber(value)
		}
		if isDecimal(line) {
			return parseCCHOChannelNumber(line)
		}
	}
	return 0, errors.New("modem: CCHO response has no logical channel")
}

func parseCCHOChannelNumber(value string) (int, error) {
	value = strings.TrimSpace(strings.Trim(value, `"`))
	channel, err := strconv.Atoi(value)
	if err != nil || channel < 1 || channel > 19 {
		return 0, errors.New("modem: CCHO response has an invalid logical channel")
	}
	return channel, nil
}

func parseATCSV(value string) []string {
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

func valueAfterATPrefix(response Response, prefix string) string {
	for _, line := range response.Lines {
		if value, ok := valueAfterATPrefixOK(line, prefix); ok {
			return value
		}
	}
	return ""
}

func valueAfterATPrefixOK(line, prefix string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(prefix)) {
		return "", false
	}
	return strings.TrimSpace(line[len(prefix):]), true
}

func extractOperatorTuples(payload string) []string {
	tupleStart := -1
	depth := 0
	inQuote := false
	var tuples []string
	for index, character := range payload {
		switch {
		case character == '"':
			inQuote = !inQuote
		case !inQuote && character == '(':
			if depth == 0 {
				tupleStart = index + 1
			}
			depth++
		case !inQuote && character == ')':
			depth--
			if depth == 0 && tupleStart >= 0 {
				tuples = append(tuples, payload[tupleStart:index])
				tupleStart = -1
			}
		}
	}
	return tuples
}

func validPDPContextID(cid int) bool { return cid >= 1 && cid <= 255 }

func quoteATString(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("modem: invalid quoted AT value")
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`, nil
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
