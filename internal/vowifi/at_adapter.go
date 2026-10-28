package vowifi

import (
	"context"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/modem"
)

// The shared AT-modem core behind the vendor adapters (EC20, ZXIC). One
// implementation owns the UICC and radio flows; a vendor profile only
// selects the command dialect those flows parameterize on.

var (
	ErrATSIMNotReady       = errors.New("vocat: modem SIM is not ready")
	ErrATMNCUnavailable    = errors.New("vocat: modem SIM does not expose an explicit MNC length")
	ErrATApplicationAbsent = errors.New("vocat: modem has no usable USIM or ISIM application")
	ErrATIdentityChanged   = errors.New("vocat: modem SIM identity changed during authentication")
	ErrATAKACommand        = errors.New("vocat: modem USIM AUTHENTICATE command failed")
	ErrATAKAResponse       = errors.New("vocat: modem returned an invalid USIM AUTHENTICATE response")
	ErrATAKAMACFailure     = errors.New("vocat: modem USIM rejected the network authentication token")
)

const (
	usimAIDPrefix         = "A0000000871002"
	isimAIDPrefix         = "A0000000871004"
	efDIRFileID           = 0x2f00
	efADDecimal           = 28589 // 0x6FAD
	efEHPLMNDecimal       = 28441 // 0x6F19 (3GPP TS 31.102 EF_EHPLMN)
	channelCleanupTimeout = 3 * time.Second
)

// ATModemExecutor exposes only semantic AT and APDU operations. Production
// code passes the device manager directly; tests can use an evidence
// transcript.
type ATModemExecutor interface {
	ExecuteAT(context.Context, string, string) (modem.Response, error)
	ExecuteAPDU(context.Context, string, modem.APDUCommand, int, []byte) ([]byte, error)
}

// ATUICCLocker serializes multi-command UICC transactions across in-process
// clients. device.Manager implements it so an APDU cannot interleave with
// another client's UICC transaction.
type ATUICCLocker interface {
	LockUICC()
	UnlockUICC()
}

// ATModemAdapterOptions configures the shared AT-modem core.
type ATModemAdapterOptions struct {
	// PureAirplanePolicy reports the independent user policy. The adapter only
	// changes the transactional CFUN projection used by VoWiFi and never
	// changes this policy.
	PureAirplanePolicy func(deviceID string) bool

	// HomePLMN supplies an explicit operator configuration when EF_AD omits
	// the MNC length. The returned MCC/MNC must exactly prefix the live IMSI.
	HomePLMN func(deviceID, iccid, imsi string) (mcc, mnc string, ok bool)

	// RestoreCellularData permits reactivating PDP contexts that were active
	// before the VoWiFi transaction. It is deliberately false by default:
	// VoWiFi must never start billable cellular data unless an operator has
	// explicitly opted in to that separate behavior.
	RestoreCellularData bool
}

// ATVendorProfile carries the per-chipset command dialect the shared core
// parameterizes its flows on. Vendor differences stay limited to the
// commands listed here; an unadapted command must never be issued to an
// adapted chipset.
type ATVendorProfile struct {
	// Name labels the vendor in diagnostics.
	Name string
	// ICCIDCommands is the ordered ICCID query chain.
	ICCIDCommands []string
	// ICCIDPrefixes lists every response prefix the chain may parse.
	ICCIDPrefixes []string
	// CUADDiscovery enables the AT+CUAD application probe. Chipsets without
	// the command answer a vendor "not recognized" error, so their profile
	// goes straight to the standards-based EF_DIR discovery.
	CUADDiscovery bool
	// ToleratePDPInventoryRefusal degrades a failed AT+CGACT? inventory to
	// "no active contexts" instead of failing the radio phase: some firmware
	// refuses the query while the radio is off.
	ToleratePDPInventoryRefusal bool
}

// atModemAdapter implements SIMIdentityReader, AKAProvider, and
// RadioController using standardized AT commands on the AT port selected by
// device.Manager. It never discovers or opens serial ports itself.
type atModemAdapter struct {
	executor ATModemExecutor
	vendor   ATVendorProfile
	options  ATModemAdapterOptions

	apduMu      sync.Mutex
	mu          sync.Mutex
	bindings    map[string]atSIMBinding
	checkpoints map[string]atRadioCheckpoint
}

type atSIMBinding struct {
	deviceID     string
	iccid        string
	imsi         string
	aid          string
	application  string
	basicChannel bool
}

type atRadioCheckpoint struct {
	activeCIDs []int
}

// newATModemAdapter validates the executor and the vendor profile before any
// flow can run on a half-configured adapter.
func newATModemAdapter(
	executor ATModemExecutor,
	options ATModemAdapterOptions,
	vendor ATVendorProfile,
) (*atModemAdapter, error) {
	if executor == nil {
		return nil, errors.New("vocat: AT executor is required")
	}
	if vendor.Name == "" || len(vendor.ICCIDCommands) == 0 || len(vendor.ICCIDPrefixes) == 0 {
		return nil, errors.New("vocat: AT adapter vendor profile is incomplete")
	}
	return &atModemAdapter{
		executor:    executor,
		vendor:      vendor,
		options:     options,
		bindings:    make(map[string]atSIMBinding),
		checkpoints: make(map[string]atRadioCheckpoint),
	}, nil
}

func (adapter *atModemAdapter) ReadIdentity(
	ctx context.Context,
	deviceID string,
) (SIMIdentity, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return SIMIdentity{}, errors.New("vocat: device ID is required")
	}

	pin, err := adapter.execute(ctx, deviceID, "AT+CPIN?")
	if err != nil {
		return SIMIdentity{}, fmt.Errorf("read SIM state: %w", err)
	}
	if !responseContainsValue(pin, "+CPIN:", "READY") {
		return SIMIdentity{}, ErrATSIMNotReady
	}

	imsiResponse, err := adapter.execute(ctx, deviceID, "AT+CIMI")
	if err != nil {
		return SIMIdentity{}, fmt.Errorf("read modem IMSI: %w", err)
	}
	imsi := digitIdentifier(imsiResponse, []string{"+CIMI:"}, 10, 18)
	if imsi == "" {
		return SIMIdentity{}, errors.New("vocat: modem returned no valid IMSI")
	}
	iccid, err := adapter.readICCID(ctx, deviceID)
	if err != nil {
		return SIMIdentity{}, err
	}

	imeiResponse, imeiErr := adapter.execute(ctx, deviceID, "AT+CGSN")
	imei := ""
	if imeiErr == nil {
		imei = modem.ParseIMEI(imeiResponse)
	}
	if imei == "" {
		imeiResponse, imeiErr = adapter.execute(ctx, deviceID, "AT+CGSN=1")
		if imeiErr != nil {
			return SIMIdentity{}, fmt.Errorf("read modem IMEI: %w", imeiErr)
		}
		imei = modem.ParseIMEI(imeiResponse)
	}
	if imei == "" {
		return SIMIdentity{}, errors.New("vocat: modem returned no valid IMEI")
	}

	homeMCC, homeMNC, err := adapter.readHomePLMN(
		ctx,
		deviceID,
		iccid,
		imsi,
	)
	if err != nil {
		return SIMIdentity{}, err
	}
	identity := SIMIdentity{
		ICCID:   iccid,
		IMSI:    imsi,
		IMEI:    imei,
		HomeMCC: homeMCC,
		HomeMNC: homeMNC,
	}
	if reader, ok := adapter.executor.(SIMMetadataReader); ok {
		if metadata, metadataErr := reader.ReadSIMMetadata(ctx, deviceID); metadataErr == nil {
			identity.SPN = strings.TrimSpace(metadata.SPN)
			identity.GID1 = strings.TrimSpace(metadata.GID1)
			identity.GID2 = strings.TrimSpace(metadata.GID2)
		}
	}
	identity = applyAssignedCarrierRoute(identity)
	adapter.mu.Lock()
	adapter.bindings[iccid] = atSIMBinding{
		deviceID: deviceID,
		iccid:    iccid,
		imsi:     imsi,
	}
	adapter.mu.Unlock()
	return identity, nil
}

// ReadSMSCenter returns the service-centre address configured by the SIM.
// AT+CSCA? is read-only and remains available while cellular RF is disabled
// for a Wi-Fi Calling session.
func (adapter *atModemAdapter) ReadSMSCenter(ctx context.Context, deviceID string) (string, error) {
	response, err := adapter.execute(ctx, strings.TrimSpace(deviceID), "AT+CSCA?")
	if err != nil {
		return "", fmt.Errorf("read SMS service centre: %w", err)
	}
	fields := parseCSV(valueAfterATPrefix(response, "+CSCA:"))
	if len(fields) == 0 {
		return "", errors.New("vocat: modem returned no SMS service-centre address")
	}
	value := strings.Trim(strings.TrimSpace(fields[0]), `"`)
	digits := strings.TrimPrefix(value, "+")
	if !validDigits(digits, 3, 20) {
		return "", errors.New("vocat: modem returned an invalid SMS service-centre address")
	}
	return value, nil
}

func (adapter *atModemAdapter) readHomePLMN(
	ctx context.Context,
	deviceID string,
	iccid string,
	imsi string,
) (string, string, error) {
	// AT&T 310/280 is a three-digit MNC. Prefer the assigned subscription
	// prefix when EF_AD is stale or ambiguous after a profile switch.
	if strings.HasPrefix(strings.TrimSpace(imsi), "310280") {
		return "310", "280", nil
	}
	mncLength, efErr := adapter.readExplicitMNCLength(ctx, deviceID)
	if efErr == nil {
		if len(imsi) < 3+mncLength {
			return "", "", errors.New(
				"vocat: IMSI is shorter than the EF_AD home PLMN",
			)
		}
		return imsi[:3], imsi[3 : 3+mncLength], nil
	}
	if adapter.options.HomePLMN != nil {
		mcc, mnc, ok := adapter.options.HomePLMN(deviceID, iccid, imsi)
		mcc = strings.TrimSpace(mcc)
		mnc = strings.TrimSpace(mnc)
		if ok && validConfiguredHomePLMN(imsi, mcc, mnc) {
			return mcc, mnc, nil
		}
	}
	// Exact assigned HPLMN prefixes are data, not an MNC-length heuristic.
	if mcc, mnc, ok := assignedHomePLMN(imsi); ok {
		return mcc, mnc, nil
	}
	// 3GPP TS 31.102 Section 4.2.84: Query EF_EHPLMN (Equivalent Home PLMN).
	if ehplmns, err := adapter.readEHPLMN(ctx, deviceID); err == nil && len(ehplmns) > 0 {
		first := ehplmns[0]
		if len(first) >= 5 {
			return first[:3], first[3:], nil
		}
	}
	return "", "", efErr
}

func (adapter *atModemAdapter) readEHPLMN(
	ctx context.Context,
	deviceID string,
) ([]string, error) {
	commands := []string{
		fmt.Sprintf("AT+CRSM=176,%d,0,0,0", efEHPLMNDecimal),
		fmt.Sprintf("AT+CRSM=176,%d,0,0,12", efEHPLMNDecimal),
	}
	var lastErr error
	for _, command := range commands {
		response, err := adapter.execute(ctx, deviceID, command)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := parseCRSMData(response)
		if err != nil || len(data) < 3 {
			lastErr = err
			continue
		}
		plmns := parsePLMNListFromBytes(data)
		if len(plmns) > 0 {
			return plmns, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("vocat: EF_EHPLMN is empty or unavailable")
	}
	return nil, lastErr
}

func parsePLMNListFromBytes(data []byte) []string {
	var plmns []string
	for i := 0; i+3 <= len(data); i += 3 {
		b1, b2, b3 := data[i], data[i+1], data[i+2]
		mcc1 := b1 & 0x0f
		mcc2 := (b1 >> 4) & 0x0f
		mcc3 := b2 & 0x0f
		mnc3 := (b2 >> 4) & 0x0f
		mnc1 := b3 & 0x0f
		mnc2 := (b3 >> 4) & 0x0f

		if mcc1 > 9 || mcc2 > 9 || mcc3 > 9 || mnc1 > 9 || mnc2 > 9 {
			continue
		}
		mcc := fmt.Sprintf("%d%d%d", mcc1, mcc2, mcc3)
		var mnc string
		if mnc3 <= 9 {
			mnc = fmt.Sprintf("%d%d%d", mnc1, mnc2, mnc3)
		} else {
			mnc = fmt.Sprintf("%d%d", mnc1, mnc2)
		}
		if len(mcc) == 3 && (len(mnc) == 2 || len(mnc) == 3) {
			plmns = append(plmns, mcc+mnc)
		}
	}
	return plmns
}

func assignedHomePLMN(imsi string) (mcc, mnc string, ok bool) {
	assignments := []struct {
		prefix    string
		mncLength int
	}{
		{prefix: "20404", mncLength: 2},  // Vodafone NL core; some Lebara subscriptions.
		{prefix: "23415", mncLength: 2},  // Vodafone UK.
		{prefix: "23487", mncLength: 2},  // Lebara Mobile UK.
		{prefix: "310280", mncLength: 3}, // AT&T / RedPocket GSMA.
	}
	for _, assignment := range assignments {
		if strings.HasPrefix(imsi, assignment.prefix) {
			return imsi[:3], imsi[3 : 3+assignment.mncLength], true
		}
	}
	return "", "", false
}

func validConfiguredHomePLMN(imsi, mcc, mnc string) bool {
	if !validDigits(mcc, 3, 3) || !validDigits(mnc, 2, 3) {
		return false
	}
	return strings.HasPrefix(imsi, mcc+mnc)
}

func (adapter *atModemAdapter) readExplicitMNCLength(
	ctx context.Context,
	deviceID string,
) (int, error) {
	commands := []string{
		fmt.Sprintf("AT+CRSM=176,%d,0,0,4", efADDecimal),
		fmt.Sprintf("AT+CRSM=176,%d,0,0,0", efADDecimal),
	}
	var lastErr error
	for _, command := range commands {
		response, err := adapter.execute(ctx, deviceID, command)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := parseCRSMData(response)
		if err != nil {
			lastErr = err
			continue
		}
		if len(data) < 4 {
			lastErr = ErrATMNCUnavailable
			continue
		}
		length := int(data[3] & 0x0f)
		if length == 2 || length == 3 {
			return length, nil
		}
		lastErr = ErrATMNCUnavailable
	}
	if lastErr == nil {
		lastErr = ErrATMNCUnavailable
	}
	return 0, fmt.Errorf("%w: %v", ErrATMNCUnavailable, lastErr)
}

func (adapter *atModemAdapter) readICCID(
	ctx context.Context,
	deviceID string,
) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		for _, command := range adapter.vendor.ICCIDCommands {
			response, err := adapter.execute(ctx, deviceID, command)
			if err != nil {
				lastErr = err
				continue
			}
			value := iccidIdentifier(
				response,
				adapter.vendor.ICCIDPrefixes,
				18,
				22,
			)
			if value != "" {
				return value, nil
			}
			lastErr = errors.New("response contained no valid ICCID")
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return "", fmt.Errorf("read ICCID: %w", lastErr)
}

func (adapter *atModemAdapter) CheckReady(
	ctx context.Context,
	identity SIMIdentity,
) (AKAEvidence, error) {
	binding, err := adapter.bindingFor(identity)
	if err != nil {
		return AKAEvidence{}, err
	}
	if err := adapter.verifyLiveICCID(ctx, binding); err != nil {
		return AKAEvidence{}, err
	}

	// CCHO/CGLA/GET RESPONSE/CCHC is one UICC transaction. Serialize it
	// across the adapter so periodic device refreshes or another AKA exchange
	// cannot insert an APDU between a 61xx response and GET RESPONSE.
	adapter.apduMu.Lock()
	defer adapter.apduMu.Unlock()
	if locker, ok := adapter.executor.(ATUICCLocker); ok {
		locker.LockUICC()
		defer locker.UnlockUICC()
	}

	aid, application, err := adapter.discoverAKAApplication(ctx, binding.deviceID)
	if err != nil {
		return AKAEvidence{}, err
	}
	channel, err := adapter.openLogicalChannel(ctx, binding.deviceID, aid)
	basicChannel := false
	if err == nil {
		if err := adapter.closeLogicalChannelWithCleanup(
			binding.deviceID,
			channel,
		); err != nil {
			return AKAEvidence{}, err
		}
	} else {
		// Several EC20 firmware branches reject CCHO/CGLA even though their
		// basic-channel CSIM implementation is standards compliant.
		if err := adapter.selectBasicApplication(
			ctx,
			binding.deviceID,
			aid,
		); err != nil {
			return AKAEvidence{}, errors.Join(
				ErrATApplicationAbsent,
				err,
			)
		}
		basicChannel = true
	}

	binding.aid = aid
	binding.application = application
	binding.basicChannel = basicChannel
	adapter.mu.Lock()
	adapter.bindings[binding.iccid] = binding
	adapter.mu.Unlock()
	return AKAEvidence{Ready: true, Application: application}, nil
}

func (adapter *atModemAdapter) Authenticate(
	ctx context.Context,
	identity SIMIdentity,
	challenge AKAChallenge,
) (AKAResult, error) {
	return adapter.authenticateWithApplication(ctx, identity, challenge, "")
}

func (adapter *atModemAdapter) AuthenticateWithPreference(
	ctx context.Context,
	identity SIMIdentity,
	challenge AKAChallenge,
	preference string,
) (AKAResult, error) {
	return adapter.authenticateWithApplication(ctx, identity, challenge, preference)
}

func (adapter *atModemAdapter) authenticateWithApplication(
	ctx context.Context,
	identity SIMIdentity,
	challenge AKAChallenge,
	preference string,
) (AKAResult, error) {
	binding, err := adapter.bindingFor(identity)
	if err != nil {
		return AKAResult{}, err
	}
	if strings.EqualFold(strings.TrimSpace(preference), "isim_strict") && binding.application != "ISIM" {
		aid, application, err := adapter.discoverPreferredAKAApplication(
			ctx,
			binding.deviceID,
			isimAIDPrefix,
			"ISIM",
		)
		if err != nil {
			return AKAResult{}, err
		}
		binding.aid = aid
		binding.application = application
		// 保留已验证的 CSIM 访问方式，验证新应用成功后再缓存绑定。
	}
	if binding.aid == "" {
		if _, err := adapter.CheckReady(ctx, identity); err != nil {
			return AKAResult{}, err
		}
		binding, err = adapter.bindingFor(identity)
		if err != nil {
			return AKAResult{}, err
		}
	}
	if strings.EqualFold(strings.TrimSpace(preference), "isim_strict") && binding.application != "ISIM" {
		return AKAResult{}, fmt.Errorf(
			"%w: ISIM strict requested, selected %s (%s)",
			ErrATApplicationAbsent,
			binding.application,
			binding.aid,
		)
	}
	if err := adapter.verifyLiveICCID(ctx, binding); err != nil {
		return AKAResult{}, err
	}

	adapter.apduMu.Lock()
	defer adapter.apduMu.Unlock()
	if locker, ok := adapter.executor.(ATUICCLocker); ok {
		locker.LockUICC()
		defer locker.UnlockUICC()
	}

	apdu := buildUSIMAuthenticateAPDU(challenge)
	var raw []byte
	if binding.basicChannel {
		raw, err = adapter.authenticateBasicApplication(ctx, binding, apdu)
		if err != nil {
			return AKAResult{}, err
		}
	} else {
		channel, err := adapter.openLogicalChannel(
			ctx,
			binding.deviceID,
			binding.aid,
		)
		if err != nil {
			var commandErr *modem.CommandError
			if binding.application != "ISIM" || ctx.Err() != nil ||
				!errors.As(err, &commandErr) || commandErr.Final != "ERROR" {
				return AKAResult{}, err
			}
			// 仅在 CCHO 明确拒绝时尝试 CSIM，严格 ISIM 不降级为 USIM。
			var basicErr error
			raw, basicErr = adapter.authenticateBasicApplication(ctx, binding, apdu)
			if basicErr != nil {
				if errors.Is(basicErr, ErrATAKACommand) {
					return AKAResult{}, errors.Join(fmt.Errorf("open application: %w", commandErr), basicErr)
				}
				return AKAResult{}, errors.Join(err, basicErr)
			}
			binding.basicChannel = true
		} else {
			var commandErr error
			raw, commandErr = adapter.transmitLogicalAPDU(
				ctx,
				binding.deviceID,
				channel,
				apdu,
			)
			closeErr := adapter.closeLogicalChannelWithCleanup(
				binding.deviceID,
				channel,
			)
			if commandErr != nil {
				if closeErr != nil {
					return AKAResult{}, errors.Join(commandErr, closeErr)
				}
				return AKAResult{}, commandErr
			}
			if closeErr != nil {
				return AKAResult{}, closeErr
			}
		}
	}
	result, err := parseUSIMAuthenticateResponse(raw)
	if err != nil {
		return AKAResult{}, err
	}
	adapter.mu.Lock()
	adapter.bindings[binding.iccid] = binding
	adapter.mu.Unlock()
	return result, nil
}

func (adapter *atModemAdapter) authenticateBasicApplication(ctx context.Context, binding atSIMBinding, apdu []byte) ([]byte, error) {
	if err := adapter.selectBasicApplication(ctx, binding.deviceID, binding.aid); err != nil {
		return nil, err
	}
	raw, err := adapter.transmitBasicAPDU(ctx, binding.deviceID, apdu)
	if err != nil {
		return nil, ErrATAKACommand
	}
	return raw, nil
}

func buildUSIMAuthenticateAPDU(challenge AKAChallenge) []byte {
	// TS 31.102 AUTHENTICATE, 3G security context (P2=0x81):
	// Lc=34, then LV(RAND) and LV(AUTN), followed by Le.
	apdu := make([]byte, 0, 40)
	apdu = append(apdu, 0x00, 0x88, 0x00, 0x81, 0x22, 0x10)
	apdu = append(apdu, challenge.RAND[:]...)
	apdu = append(apdu, 0x10)
	apdu = append(apdu, challenge.AUTN[:]...)
	apdu = append(apdu, 0x00)
	return apdu
}

func parseUSIMAuthenticateResponse(raw []byte) (AKAResult, error) {
	if len(raw) < 2 {
		return AKAResult{}, fmt.Errorf(
			"%w: response length %d has no status word",
			ErrATAKAResponse,
			len(raw),
		)
	}
	status := uint16(raw[len(raw)-2])<<8 | uint16(raw[len(raw)-1])
	body := raw[:len(raw)-2]
	if status != 0x9000 {
		switch status {
		case 0x9862:
			return AKAResult{}, ErrATAKAMACFailure
		default:
			return AKAResult{}, fmt.Errorf(
				"%w: status word %04X",
				ErrATAKAResponse,
				status,
			)
		}
	}
	if len(body) < 2 {
		return AKAResult{}, fmt.Errorf(
			"%w: response body length %d is too short",
			ErrATAKAResponse,
			len(body),
		)
	}
	tag := body[0]
	value := body[1:]

	switch tag {
	case 0xdb:
		res, rest, ok := takeLV(value)
		if !ok {
			return AKAResult{}, fmt.Errorf(
				"%w: malformed RES field in %d-byte success value",
				ErrATAKAResponse,
				len(value),
			)
		}
		if len(res) < 4 || len(res) > 16 {
			return AKAResult{}, fmt.Errorf(
				"%w: invalid RES length %d",
				ErrATAKAResponse,
				len(res),
			)
		}
		ck, rest, ok := takeLV(rest)
		if !ok || len(ck) != 16 {
			return AKAResult{}, fmt.Errorf(
				"%w: invalid CK length %d",
				ErrATAKAResponse,
				len(ck),
			)
		}
		ik, rest, ok := takeLV(rest)
		if !ok || len(ik) != 16 {
			return AKAResult{}, fmt.Errorf(
				"%w: invalid IK length %d",
				ErrATAKAResponse,
				len(ik),
			)
		}
		// Kc is present on many USIMs. It is not needed by EAP-AKA, but when
		// present its LV still has to be structurally valid.
		if len(rest) > 0 {
			kc, tail, ok := takeLV(rest)
			if !ok || len(kc) != 8 || len(tail) != 0 {
				return AKAResult{}, fmt.Errorf(
					"%w: invalid optional Kc length %d with %d trailing bytes",
					ErrATAKAResponse,
					len(kc),
					len(tail),
				)
			}
		}
		return AKAResult{
			RES: append([]byte(nil), res...),
			CK:  append([]byte(nil), ck...),
			IK:  append([]byte(nil), ik...),
		}, nil
	case 0xdc:
		auts, tail, ok := takeLV(value)
		if !ok || len(auts) != 14 || len(tail) != 0 {
			return AKAResult{}, fmt.Errorf(
				"%w: invalid AUTS length %d with %d trailing bytes",
				ErrATAKAResponse,
				len(auts),
				len(tail),
			)
		}
		return AKAResult{
			AUTS:                   append([]byte(nil), auts...),
			SynchronizationFailure: true,
		}, nil
	default:
		return AKAResult{}, fmt.Errorf(
			"%w: unsupported response tag %02X with %d value bytes",
			ErrATAKAResponse,
			tag,
			len(value),
		)
	}
}

func takeLV(value []byte) (field, rest []byte, ok bool) {
	if len(value) == 0 {
		return nil, value, false
	}
	length := int(value[0])
	if length > len(value)-1 {
		return nil, value, false
	}
	return value[1 : 1+length], value[1+length:], true
}

func (adapter *atModemAdapter) Snapshot(
	ctx context.Context,
	deviceID string,
) (RadioSnapshot, error) {
	mode, err := adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return RadioSnapshot{}, err
	}
	active, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		if !adapter.vendor.ToleratePDPInventoryRefusal {
			return RadioSnapshot{}, err
		}
		// Some firmware refuses the PDP inventory while the radio is off
		// (the ZXIC platform answers vendor +CME ERROR: 148), which wedged
		// the whole VoWiFi enable at access_ready. The inventory only feeds
		// a checkpoint that never drives restoration (RestoreCellularData
		// is false by default) and RF-off implies no active PDP, so degrade
		// to "no active contexts" instead of failing the phase. The
		// operating-mode read above still proves the control channel is
		// alive.
		active = nil
	}
	adapter.mu.Lock()
	adapter.checkpoints[deviceID] = atRadioCheckpoint{
		activeCIDs: append([]int(nil), active...),
	}
	adapter.mu.Unlock()

	purePolicy := false
	if adapter.options.PureAirplanePolicy != nil {
		purePolicy = adapter.options.PureAirplanePolicy(deviceID)
	}
	return RadioSnapshot{
		CellularDataEnabled: len(active) > 0,
		OperatingMode:       mode,
		PureAirplanePolicy:  purePolicy,
	}, nil
}

func (adapter *atModemAdapter) StopCellularData(
	ctx context.Context,
	deviceID string,
) error {
	active, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		if !adapter.vendor.ToleratePDPInventoryRefusal {
			return err
		}
		// With the radio already off the firmware refuses the PDP query,
		// and without an inventory there is nothing to deactivate — the
		// modem-side PDN is torn down by the RF-off transition itself.
		// Vendor profiles that answer the query keep the strict
		// deactivate-and-verify path below.
		return nil
	}
	for _, cid := range active {
		if _, err := adapter.execute(
			ctx,
			deviceID,
			fmt.Sprintf("AT+CGACT=0,%d", cid),
		); err != nil {
			return fmt.Errorf("deactivate PDP context %d: %w", cid, err)
		}
	}
	remaining, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return errors.New("vocat: cellular data remained active")
	}
	return nil
}

func (adapter *atModemAdapter) EnterVoWiFiRFOff(
	ctx context.Context,
	deviceID string,
) error {
	mode, err := adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if mode != 4 {
		if _, err := adapter.execute(ctx, deviceID, "AT+CFUN=4"); err != nil {
			return fmt.Errorf("enter RF-off mode: %w", err)
		}
	}
	mode, err = adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if mode != 4 {
		return fmt.Errorf("vocat: modem reported CFUN=%d after RF-off request", mode)
	}
	return nil
}

func (adapter *atModemAdapter) Restore(
	ctx context.Context,
	deviceID string,
	snapshot RadioSnapshot,
) error {
	if snapshot.OperatingMode < 0 {
		return errors.New("vocat: invalid radio snapshot")
	}
	targetMode := snapshot.OperatingMode
	if snapshot.PureAirplanePolicy {
		// VoWiFi teardown is intentionally fail-closed. Even if the modem was in
		// CFUN=1 before setup, disabling VoWiFi must leave RF off until the user
		// explicitly turns airplane mode off through the separate control.
		targetMode = 4
	}
	currentMode, err := adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if currentMode != targetMode {
		if _, err := adapter.execute(
			ctx,
			deviceID,
			fmt.Sprintf("AT+CFUN=%d", targetMode),
		); err != nil {
			return fmt.Errorf("restore operating mode: %w", err)
		}
	}
	currentMode, err = adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if currentMode != targetMode {
		return fmt.Errorf(
			"vocat: restore reported CFUN=%d, expected %d",
			currentMode,
			targetMode,
		)
	}

	adapter.mu.Lock()
	checkpoint, found := adapter.checkpoints[deviceID]
	adapter.mu.Unlock()
	if !found {
		if snapshot.CellularDataEnabled {
			return errors.New("vocat: PDP restore evidence is unavailable")
		}
		checkpoint.activeCIDs = nil
	}
	if (snapshot.OperatingMode == 0 || snapshot.OperatingMode == 4) &&
		len(checkpoint.activeCIDs) > 0 {
		return errors.New("vocat: snapshot has active data in RF-off mode")
	}
	desiredCIDs := checkpoint.activeCIDs
	if !adapter.options.RestoreCellularData || targetMode == 0 || targetMode == 4 {
		desiredCIDs = nil
	}
	if err := adapter.reconcileActiveCIDs(
		ctx,
		deviceID,
		desiredCIDs,
	); err != nil {
		return err
	}
	adapter.mu.Lock()
	delete(adapter.checkpoints, deviceID)
	adapter.mu.Unlock()
	return nil
}

func (adapter *atModemAdapter) reconcileActiveCIDs(
	ctx context.Context,
	deviceID string,
	desired []int,
) error {
	current, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		return err
	}
	desiredSet := integerSet(desired)
	currentSet := integerSet(current)
	for _, cid := range current {
		if _, wanted := desiredSet[cid]; wanted {
			continue
		}
		if _, err := adapter.execute(
			ctx,
			deviceID,
			fmt.Sprintf("AT+CGACT=0,%d", cid),
		); err != nil {
			return fmt.Errorf("restore PDP context %d: %w", cid, err)
		}
	}
	for _, cid := range desired {
		if _, active := currentSet[cid]; active {
			continue
		}
		if _, err := adapter.execute(
			ctx,
			deviceID,
			fmt.Sprintf("AT+CGACT=1,%d", cid),
		); err != nil {
			return fmt.Errorf("restore PDP context %d: %w", cid, err)
		}
	}
	verified, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		return err
	}
	if !sameIntegers(verified, desired) {
		return fmt.Errorf(
			"vocat: PDP restore mismatch: active contexts %v",
			verified,
		)
	}
	return nil
}

func (adapter *atModemAdapter) readOperatingMode(
	ctx context.Context,
	deviceID string,
) (int, error) {
	response, err := adapter.execute(ctx, deviceID, "AT+CFUN?")
	if err != nil {
		return 0, fmt.Errorf("read operating mode: %w", err)
	}
	value := valueAfterATPrefix(response, "+CFUN:")
	fields := parseCSV(value)
	if len(fields) == 0 {
		return 0, errors.New("vocat: modem returned no CFUN mode")
	}
	mode, err := strconv.Atoi(fields[0])
	if err != nil || mode < 0 {
		return 0, errors.New("vocat: modem returned an invalid CFUN mode")
	}
	return mode, nil
}

func (adapter *atModemAdapter) readActiveCIDs(
	ctx context.Context,
	deviceID string,
) ([]int, error) {
	response, err := adapter.execute(ctx, deviceID, "AT+CGACT?")
	if err != nil {
		return nil, fmt.Errorf("read PDP contexts: %w", err)
	}
	var active []int
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "+CGACT:") {
			continue
		}
		fields := parseCSV(strings.TrimSpace(line[len("+CGACT:"):]))
		if len(fields) < 2 {
			return nil, errors.New("vocat: modem returned an invalid CGACT record")
		}
		cid, cidErr := strconv.Atoi(fields[0])
		state, stateErr := strconv.Atoi(fields[1])
		if cidErr != nil || stateErr != nil || cid <= 0 || (state != 0 && state != 1) {
			return nil, errors.New("vocat: modem returned an invalid CGACT record")
		}
		if state == 1 {
			active = append(active, cid)
		}
	}
	sort.Ints(active)
	return uniqueIntegers(active), nil
}

func (adapter *atModemAdapter) bindingFor(
	identity SIMIdentity,
) (atSIMBinding, error) {
	iccid := strings.TrimSpace(identity.ICCID)
	adapter.mu.Lock()
	binding, ok := adapter.bindings[iccid]
	adapter.mu.Unlock()
	if !ok || iccid == "" {
		return atSIMBinding{}, errors.New("vocat: SIM identity is not bound to a device")
	}
	if strings.TrimSpace(identity.IMSI) != binding.imsi {
		return atSIMBinding{}, ErrATIdentityChanged
	}
	return binding, nil
}

func (adapter *atModemAdapter) verifyLiveICCID(
	ctx context.Context,
	binding atSIMBinding,
) error {
	iccid, err := adapter.readICCID(ctx, binding.deviceID)
	if err != nil {
		return err
	}
	if iccid != binding.iccid {
		return ErrATIdentityChanged
	}
	return nil
}

func (adapter *atModemAdapter) discoverAKAApplication(
	ctx context.Context,
	deviceID string,
) (aid string, application string, err error) {
	if adapter.vendor.CUADDiscovery {
		response, cuadErr := adapter.execute(ctx, deviceID, "AT+CUAD")
		if cuadErr == nil {
			data, parseErr := parseCUADData(response)
			if parseErr == nil {
				aids := collectApplicationAIDs(data)
				for _, candidate := range aids {
					if strings.HasPrefix(candidate, usimAIDPrefix) {
						return candidate, "USIM", nil
					}
				}
				for _, candidate := range aids {
					if strings.HasPrefix(candidate, isimAIDPrefix) {
						return candidate, "ISIM", nil
					}
				}
				if len(aids) > 0 {
					return "", "", ErrATApplicationAbsent
				}
			}
		}
	}
	// CUAD is optional: some firmware branches reject it, and the ZXIC
	// platform does not implement it at all.
	// In that case do not immediately fall back to the seven-byte registered
	// application-provider prefix: cards may expose multiple USIM instances and
	// require the complete PIX from EF_DIR to select the provisioned one.  Read
	// EF_DIR over the standards-based basic channel, which remains available on
	// the same firmware that rejects CCHO/CGLA.
	if discovered, discoverErr := adapter.discoverBasicApplicationAID(
		ctx,
		deviceID,
		usimAIDPrefix,
	); discoverErr == nil {
		return discovered, "USIM", nil
	}

	// AT+CUAD is optional on older EC20 firmware. CCHO still provides a
	// standards-based, evidence-bearing probe of the assigned USIM AID.
	return usimAIDPrefix, "USIM", nil
}

func (adapter *atModemAdapter) discoverPreferredAKAApplication(
	ctx context.Context,
	deviceID string,
	aidPrefix string,
	application string,
) (string, string, error) {
	// EF_DIR 发现使用多条基本通道命令，必须持有同一事务锁。
	adapter.apduMu.Lock()
	defer adapter.apduMu.Unlock()
	if locker, ok := adapter.executor.(ATUICCLocker); ok {
		locker.LockUICC()
		defer locker.UnlockUICC()
	}
	if adapter.vendor.CUADDiscovery {
		response, cuadErr := adapter.execute(ctx, deviceID, "AT+CUAD")
		if cuadErr == nil {
			data, parseErr := parseCUADData(response)
			if parseErr == nil {
				for _, candidate := range collectApplicationAIDs(data) {
					if strings.HasPrefix(candidate, aidPrefix) {
						return candidate, application, nil
					}
				}
			}
		}
	}
	if discovered, discoverErr := adapter.discoverBasicApplicationAID(ctx, deviceID, aidPrefix); discoverErr == nil {
		return discovered, application, nil
	}
	// AT+CUAD is optional. Returning the standard AID prefix still lets CCHO
	// perform the authoritative application probe on older EC20 firmware.
	return aidPrefix, application, nil
}

func (adapter *atModemAdapter) discoverBasicApplicationAID(
	ctx context.Context,
	deviceID string,
	aidPrefix string,
) (string, error) {
	selectFile := func(fileID uint16) error {
		apdu := []byte{
			0x00, 0xa4, 0x00, 0x04, 0x02,
			byte(fileID >> 8), byte(fileID), 0x00,
		}
		raw, err := adapter.transmitBasicAPDU(ctx, deviceID, apdu)
		if err != nil {
			return err
		}
		_, status, err := splitAPDUStatus(raw)
		if err != nil {
			return err
		}
		if status != 0x9000 {
			return fmt.Errorf("vocat: basic-channel SELECT returned %04X", status)
		}
		return nil
	}
	if err := selectFile(0x3f00); err != nil {
		return "", fmt.Errorf("select MF for application discovery: %w", err)
	}
	if err := selectFile(efDIRFileID); err != nil {
		return "", fmt.Errorf("select EF_DIR for application discovery: %w", err)
	}
	for record := 1; record <= 32; record++ {
		raw, err := adapter.transmitBasicAPDU(
			ctx,
			deviceID,
			[]byte{0x00, 0xb2, byte(record), 0x04, 0x00},
		)
		if err != nil {
			return "", fmt.Errorf("read EF_DIR record %d: %w", record, err)
		}
		body, status, err := splitAPDUStatus(raw)
		if err != nil {
			return "", err
		}
		if status == 0x6a83 || status == 0x9402 {
			break
		}
		if status != 0x9000 {
			continue
		}
		for _, candidate := range collectApplicationAIDs(body) {
			if strings.HasPrefix(candidate, aidPrefix) {
				return candidate, nil
			}
		}
	}
	return "", ErrATApplicationAbsent
}

func (adapter *atModemAdapter) openLogicalChannel(
	ctx context.Context,
	deviceID string,
	aid string,
) (int, error) {
	response, err := adapter.execute(
		ctx,
		deviceID,
		fmt.Sprintf(`AT+CCHO="%s"`, aid),
	)
	if err != nil {
		return 0, fmt.Errorf("%w: open application: %w", ErrATApplicationAbsent, err)
	}
	channel, err := modem.ParseCCHOChannel(response)
	if err != nil {
		return 0, errors.New("vocat: modem returned an invalid logical channel")
	}
	return channel, nil
}

func (adapter *atModemAdapter) closeLogicalChannel(
	ctx context.Context,
	deviceID string,
	channel int,
) error {
	if _, err := adapter.execute(
		ctx,
		deviceID,
		fmt.Sprintf("AT+CCHC=%d", channel),
	); err != nil {
		return fmt.Errorf("close logical channel: %w", err)
	}
	return nil
}

func (adapter *atModemAdapter) closeLogicalChannelWithCleanup(
	deviceID string,
	channel int,
) error {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		channelCleanupTimeout,
	)
	defer cancel()
	return adapter.closeLogicalChannel(ctx, deviceID, channel)
}

func (adapter *atModemAdapter) selectBasicApplication(
	ctx context.Context,
	deviceID string,
	aid string,
) error {
	aidBytes, err := hex.DecodeString(aid)
	if err != nil || len(aidBytes) == 0 || len(aidBytes) > 255 {
		return errors.New("vocat: invalid USIM application identifier")
	}
	// SELECT by DF name, request the first/only matching application and FCP.
	apdu := []byte{0x00, 0xa4, 0x04, 0x04, byte(len(aidBytes))}
	apdu = append(apdu, aidBytes...)
	raw, err := adapter.transmitBasicAPDU(ctx, deviceID, apdu)
	if err != nil {
		return fmt.Errorf("select basic-channel application: %w", err)
	}
	_, status, err := splitAPDUStatus(raw)
	if err != nil {
		return err
	}
	if status != 0x9000 {
		return fmt.Errorf(
			"vocat: basic-channel SELECT returned %04X",
			status,
		)
	}
	return nil
}

func (adapter *atModemAdapter) transmitBasicAPDU(
	ctx context.Context,
	deviceID string,
	apdu []byte,
) ([]byte, error) {
	if len(apdu) == 0 || len(apdu) > 261 {
		return nil, errors.New("vocat: invalid APDU length")
	}
	var collected []byte
	current := append([]byte(nil), apdu...)
	for exchange := 0; exchange < 4; exchange++ {
		raw, err := adapter.executor.ExecuteAPDU(ctx, deviceID, modem.APDUCSIM, 0, current)
		if err != nil {
			return nil, errors.New("vocat: CSIM exchange failed")
		}
		body, status, err := splitAPDUStatus(raw)
		if err != nil {
			return nil, err
		}
		sw1 := byte(status >> 8)
		if sw1 == 0x6c {
			// The UICC knows the exact response length. Retry the original APDU
			// with the advised Le without retaining the procedure response.
			if len(current) < 5 {
				return nil, errors.New("vocat: APDU cannot apply corrected response length")
			}
			current[len(current)-1] = byte(status)
			continue
		}
		collected = append(collected, body...)
		if sw1 != 0x61 && sw1 != 0x9f {
			collected = append(collected, byte(status>>8), byte(status))
			return collected, nil
		}
		// GET RESPONSE on the same basic channel. Le=0 means 256 bytes.
		current = []byte{0x00, 0xc0, 0x00, 0x00, byte(status)}
	}
	return nil, errors.New("vocat: APDU response chaining exceeded limit")
}

func (adapter *atModemAdapter) transmitLogicalAPDU(
	ctx context.Context,
	deviceID string,
	channel int,
	apdu []byte,
) ([]byte, error) {
	if channel < 1 || channel > 19 || len(apdu) == 0 || len(apdu) > 261 {
		return nil, ErrATAKACommand
	}
	var collected []byte
	current := append([]byte(nil), apdu...)
	for exchange := 0; exchange < 4; exchange++ {
		raw, err := adapter.executor.ExecuteAPDU(ctx, deviceID, modem.APDUCGLA, channel, current)
		if err != nil {
			return nil, errors.Join(ErrATAKACommand, err)
		}
		body, status, err := splitAPDUStatus(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrATAKAResponse, err)
		}
		collected = append(collected, body...)
		sw1 := byte(status >> 8)
		if sw1 != 0x61 && sw1 != 0x9f {
			collected = append(collected, byte(status>>8), byte(status))
			return collected, nil
		}
		// CGLA carries the logical-channel identifier separately, so GET
		// RESPONSE retains the same interindustry CLA used by AUTHENTICATE.
		// Le=0 means 256 bytes when SW2 is zero.
		current = []byte{0x00, 0xc0, 0x00, 0x00, byte(status)}
	}
	return nil, fmt.Errorf(
		"%w: logical-channel response chaining exceeded limit",
		ErrATAKAResponse,
	)
}

func splitAPDUStatus(raw []byte) ([]byte, uint16, error) {
	if len(raw) < 2 {
		return nil, 0, errors.New("vocat: APDU has no status word")
	}
	status := uint16(raw[len(raw)-2])<<8 | uint16(raw[len(raw)-1])
	return raw[:len(raw)-2], status, nil
}

func (adapter *atModemAdapter) execute(
	ctx context.Context,
	deviceID string,
	command string,
) (modem.Response, error) {
	return adapter.executor.ExecuteAT(ctx, deviceID, command)
}

func parseCRSMData(response modem.Response) ([]byte, error) {
	value := valueAfterATPrefix(response, "+CRSM:")
	fields := parseCSV(value)
	if len(fields) < 2 {
		return nil, errors.New("invalid CRSM response")
	}
	sw1, err1 := strconv.Atoi(fields[0])
	sw2, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return nil, errors.New("invalid CRSM status")
	}
	if sw1 != 0x90 && sw1 != 0x91 && sw1 != 0x9f {
		return nil, fmt.Errorf("CRSM status %02X%02X", sw1, sw2)
	}
	if len(fields) < 3 {
		return nil, errors.New("CRSM response has no data")
	}
	data, err := hex.DecodeString(strings.Trim(fields[2], `"`))
	if err != nil {
		return nil, errors.New("CRSM response data is not hexadecimal")
	}
	return data, nil
}

func parseCUADData(response modem.Response) ([]byte, error) {
	// Firmware may split the BER-TLV stream across adjacent quoted chunks
	// and continuation lines. Concatenating every hex fragment prevents an ISIM
	// AID after a USIM entry from being silently discarded.
	var encoded strings.Builder
	collect := false
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "+CUAD:") {
			collect = true
			line = strings.TrimSpace(line[len("+CUAD:"):])
		} else if !collect {
			continue
		}
		for _, fragment := range quotedHexFragments(line) {
			encoded.WriteString(fragment)
		}
	}
	if encoded.Len() == 0 {
		return nil, errors.New("CUAD response has no data")
	}
	data, err := hex.DecodeString(encoded.String())
	if err != nil || len(data) == 0 {
		return nil, errors.New("CUAD response data is invalid")
	}
	if len(data) >= 2 && data[len(data)-2] == 0x90 && data[len(data)-1] == 0x00 {
		data = data[:len(data)-2]
	}
	return data, nil
}

func quotedHexFragments(line string) []string {
	var fragments []string
	for {
		start := strings.IndexByte(line, '"')
		if start < 0 {
			break
		}
		line = line[start+1:]
		end := strings.IndexByte(line, '"')
		if end < 0 {
			break
		}
		fragment := strings.ToUpper(strings.TrimSpace(line[:end]))
		line = line[end+1:]
		if fragment == "" || len(fragment)%2 != 0 {
			continue
		}
		valid := true
		for _, character := range fragment {
			if (character < '0' || character > '9') && (character < 'A' || character > 'F') {
				valid = false
				break
			}
		}
		if valid {
			fragments = append(fragments, fragment)
		}
	}
	return fragments
}

func collectApplicationAIDs(data []byte) []string {
	var result []string
	var walk func([]byte)
	walk = func(value []byte) {
		for len(value) > 0 {
			for len(value) > 0 && value[0] == 0xff {
				value = value[1:]
			}
			if len(value) == 0 {
				return
			}
			tag, constructed, body, consumed, err := decodeBERTLV(value)
			if err != nil || consumed == 0 {
				return
			}
			if len(tag) == 1 && tag[0] == 0x4f && len(body) > 0 {
				result = append(result, strings.ToUpper(hex.EncodeToString(body)))
			}
			if constructed {
				walk(body)
			}
			value = value[consumed:]
		}
	}
	walk(data)
	return result
}

func decodeBERTLV(data []byte) (
	tag []byte,
	constructed bool,
	value []byte,
	consumed int,
	err error,
) {
	if len(data) < 2 {
		return nil, false, nil, 0, errors.New("short BER-TLV")
	}
	tagLength := 1
	if data[0]&0x1f == 0x1f {
		for {
			if tagLength >= len(data) {
				return nil, false, nil, 0, errors.New("short BER tag")
			}
			last := data[tagLength]&0x80 == 0
			tagLength++
			if last {
				break
			}
			if tagLength > 4 {
				return nil, false, nil, 0, errors.New("oversized BER tag")
			}
		}
	}
	body, bodyConsumed, err := decodeBERTLVValue(data[tagLength:])
	if err != nil {
		return nil, false, nil, 0, err
	}
	return data[:tagLength],
		data[0]&0x20 != 0,
		body,
		tagLength + bodyConsumed,
		nil
}

func decodeBERTLVValue(data []byte) ([]byte, int, error) {
	if len(data) == 0 {
		return nil, 0, errors.New("missing BER length")
	}
	length := int(data[0])
	lengthOctets := 1
	if data[0]&0x80 != 0 {
		count := int(data[0] & 0x7f)
		if count == 0 || count > 2 || len(data) < 1+count {
			return nil, 0, errors.New("invalid BER length")
		}
		length = 0
		for _, octet := range data[1 : 1+count] {
			length = length<<8 | int(octet)
		}
		lengthOctets += count
	}
	if length > len(data)-lengthOctets {
		return nil, 0, errors.New("short BER value")
	}
	return data[lengthOctets : lengthOctets+length],
		lengthOctets + length,
		nil
}

func responseContainsValue(
	response modem.Response,
	prefix string,
	expected string,
) bool {
	return strings.EqualFold(
		strings.Trim(strings.TrimSpace(valueAfterATPrefix(response, prefix)), `"`),
		expected,
	)
}

func valueAfterATPrefix(response modem.Response, prefix string) string {
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(prefix)) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func digitIdentifier(
	response modem.Response,
	prefixes []string,
	minimum int,
	maximum int,
) string {
	for _, prefix := range prefixes {
		value := strings.Trim(valueAfterATPrefix(response, prefix), `" `)
		if validDigits(value, minimum, maximum) {
			return value
		}
	}
	for _, line := range response.Lines {
		value := strings.TrimSpace(line)
		if validDigits(value, minimum, maximum) {
			return value
		}
	}
	return ""
}

func iccidIdentifier(
	response modem.Response,
	prefixes []string,
	minimum int,
	maximum int,
) string {
	normalize := func(value string) string {
		value = strings.Trim(value, `" `)
		value = strings.TrimRight(value, "Ff")
		if validDigits(value, minimum, maximum) {
			return value
		}
		return ""
	}
	for _, prefix := range prefixes {
		if value := normalize(valueAfterATPrefix(response, prefix)); value != "" {
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

func validDigits(value string, minimum int, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func parseCSV(value string) []string {
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

func integerSet(values []int) map[int]struct{} {
	result := make(map[int]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func uniqueIntegers(values []int) []int {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func sameIntegers(left, right []int) bool {
	left = append([]int(nil), left...)
	right = append([]int(nil), right...)
	sort.Ints(left)
	sort.Ints(right)
	left = uniqueIntegers(left)
	right = uniqueIntegers(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
