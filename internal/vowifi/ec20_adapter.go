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

var (
	ErrEC20SIMNotReady       = errors.New("vocat: EC20 SIM is not ready")
	ErrEC20MNCUnavailable    = errors.New("vocat: EC20 SIM does not expose an explicit MNC length")
	ErrEC20ApplicationAbsent = errors.New("vocat: EC20 has no usable USIM or ISIM application")
	ErrEC20IdentityChanged   = errors.New("vocat: EC20 SIM identity changed during authentication")
	ErrEC20AKACommand        = errors.New("vocat: EC20 USIM AUTHENTICATE command failed")
	ErrEC20AKAResponse       = errors.New("vocat: EC20 returned an invalid USIM AUTHENTICATE response")
	ErrEC20AKAMACFailure     = errors.New("vocat: EC20 USIM rejected the network authentication token")
)

const (
	usimAIDPrefix         = "A0000000871002"
	isimAIDPrefix         = "A0000000871004"
	efADDecimal           = 28589 // 0x6FAD
	channelCleanupTimeout = 3 * time.Second
)

// EC20ATExecutor is deliberately the same narrow shape as
// device.Manager.ExecuteAT. Production code passes the device manager directly;
// tests can use an evidence transcript without opening any serial device.
type EC20ATExecutor interface {
	ExecuteAT(context.Context, string, string) (modem.Response, error)
}

// EC20SensitiveATExecutor is implemented by device.Manager so an APDU carrying
// RAND and AUTN is not retained as a device error. The fallback exists only for
// small deterministic test executors.
type EC20SensitiveATExecutor interface {
	ExecuteSensitiveAT(context.Context, string, string) (modem.Response, error)
}

type EC20AdapterOptions struct {
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

// EC20Adapter implements SIMIdentityReader, AKAProvider, and RadioController
// using standardized AT commands on the AT port selected by device.Manager.
// It never discovers or opens serial ports itself, so ttyUSB0 diagnostic cannot
// be selected accidentally.
type EC20Adapter struct {
	executor EC20ATExecutor
	options  EC20AdapterOptions

	apduMu      sync.Mutex
	mu          sync.Mutex
	bindings    map[string]ec20SIMBinding
	checkpoints map[string]ec20RadioCheckpoint
}

type ec20SIMBinding struct {
	deviceID     string
	iccid        string
	imsi         string
	aid          string
	application  string
	basicChannel bool
}

type ec20RadioCheckpoint struct {
	activeCIDs []int
}

var (
	_ SIMIdentityReader = (*EC20Adapter)(nil)
	_ AKAProvider       = (*EC20Adapter)(nil)
	_ RadioController   = (*EC20Adapter)(nil)
)

func NewEC20Adapter(
	executor EC20ATExecutor,
	options EC20AdapterOptions,
) (*EC20Adapter, error) {
	if executor == nil {
		return nil, errors.New("vocat: EC20 AT executor is required")
	}
	return &EC20Adapter{
		executor:    executor,
		options:     options,
		bindings:    make(map[string]ec20SIMBinding),
		checkpoints: make(map[string]ec20RadioCheckpoint),
	}, nil
}

func (adapter *EC20Adapter) ReadIdentity(
	ctx context.Context,
	deviceID string,
) (SIMIdentity, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return SIMIdentity{}, errors.New("vocat: EC20 device ID is required")
	}

	pin, err := adapter.execute(ctx, deviceID, "AT+CPIN?")
	if err != nil {
		return SIMIdentity{}, fmt.Errorf("read EC20 SIM state: %w", err)
	}
	if !responseContainsValue(pin, "+CPIN:", "READY") {
		return SIMIdentity{}, ErrEC20SIMNotReady
	}

	imsiResponse, err := adapter.execute(ctx, deviceID, "AT+CIMI")
	if err != nil {
		return SIMIdentity{}, fmt.Errorf("read EC20 IMSI: %w", err)
	}
	imsi := digitIdentifier(imsiResponse, []string{"+CIMI:"}, 10, 18)
	if imsi == "" {
		return SIMIdentity{}, errors.New("vocat: EC20 returned no valid IMSI")
	}
	iccid, err := adapter.readICCID(ctx, deviceID)
	if err != nil {
		return SIMIdentity{}, err
	}

	// Native OpenStick/410 WWAN firmware exposes the IMEI in ATI but may leave
	// AT+CGSN unanswered. Prefer the already-supported identification command
	// and retain CGSN as a fallback for ordinary EC20 firmware.
	imeiResponse, imeiErr := adapter.execute(ctx, deviceID, "ATI")
	imei := ""
	if imeiErr == nil {
		imei = digitIdentifier(imeiResponse, []string{"IMEI:", "+CGSN:", "+GSN:"}, 14, 17)
	}
	if imei == "" {
		imeiResponse, imeiErr = adapter.execute(ctx, deviceID, "AT+CGSN")
		if imeiErr == nil {
			imei = digitIdentifier(imeiResponse, []string{"+CGSN:", "+GSN:"}, 14, 17)
		}
	}
	if imeiErr != nil {
		return SIMIdentity{}, fmt.Errorf("read EC20 IMEI: %w", imeiErr)
	}
	if imei == "" {
		return SIMIdentity{}, errors.New("vocat: EC20 returned no valid IMEI")
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
	adapter.mu.Lock()
	adapter.bindings[iccid] = ec20SIMBinding{
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
func (adapter *EC20Adapter) ReadSMSCenter(ctx context.Context, deviceID string) (string, error) {
	response, err := adapter.execute(ctx, strings.TrimSpace(deviceID), "AT+CSCA?")
	if err != nil {
		return "", fmt.Errorf("read EC20 SMS service centre: %w", err)
	}
	fields := parseCSV(valueAfterATPrefix(response, "+CSCA:"))
	if len(fields) == 0 {
		return "", errors.New("vocat: EC20 returned no SMS service-centre address")
	}
	value := strings.Trim(strings.TrimSpace(fields[0]), `"`)
	digits := strings.TrimPrefix(value, "+")
	if !validDigits(digits, 3, 20) {
		return "", errors.New("vocat: EC20 returned an invalid SMS service-centre address")
	}
	return value, nil
}

func (adapter *EC20Adapter) readHomePLMN(
	ctx context.Context,
	deviceID string,
	iccid string,
	imsi string,
) (string, string, error) {
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
	// Exact assigned HPLMN prefixes are data, not an MNC-length heuristic. The
	// target Vodafone UK SIM is 234/15. Unknown assignments remain fail-closed.
	for prefix, mncLength := range map[string]int{"23415": 2} {
		if strings.HasPrefix(imsi, prefix) {
			return imsi[:3], imsi[3 : 3+mncLength], nil
		}
	}
	return "", "", efErr
}

func validConfiguredHomePLMN(imsi, mcc, mnc string) bool {
	if !validDigits(mcc, 3, 3) || !validDigits(mnc, 2, 3) {
		return false
	}
	return strings.HasPrefix(imsi, mcc+mnc)
}

func (adapter *EC20Adapter) readExplicitMNCLength(
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
			lastErr = ErrEC20MNCUnavailable
			continue
		}
		length := int(data[3] & 0x0f)
		if length == 2 || length == 3 {
			return length, nil
		}
		lastErr = ErrEC20MNCUnavailable
	}
	if lastErr == nil {
		lastErr = ErrEC20MNCUnavailable
	}
	return 0, fmt.Errorf("%w: %v", ErrEC20MNCUnavailable, lastErr)
}

func (adapter *EC20Adapter) readICCID(
	ctx context.Context,
	deviceID string,
) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		for _, command := range []string{"AT+CCID", "AT+QCCID", "AT+ICCID"} {
			response, err := adapter.execute(ctx, deviceID, command)
			if err != nil {
				lastErr = err
				continue
			}
			value := iccidIdentifier(
				response,
				[]string{"+CCID:", "+QCCID:", "+ICCID:", "ICCID:"},
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
	return "", fmt.Errorf("read EC20 ICCID: %w", lastErr)
}

func (adapter *EC20Adapter) CheckReady(
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
				ErrEC20ApplicationAbsent,
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

func (adapter *EC20Adapter) Authenticate(
	ctx context.Context,
	identity SIMIdentity,
	challenge AKAChallenge,
) (AKAResult, error) {
	binding, err := adapter.bindingFor(identity)
	if err != nil {
		return AKAResult{}, err
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
	if err := adapter.verifyLiveICCID(ctx, binding); err != nil {
		return AKAResult{}, err
	}

	adapter.apduMu.Lock()
	defer adapter.apduMu.Unlock()

	apdu := buildUSIMAuthenticateAPDU(challenge)
	var raw []byte
	if binding.basicChannel {
		if err := adapter.selectBasicApplication(
			ctx,
			binding.deviceID,
			binding.aid,
		); err != nil {
			return AKAResult{}, err
		}
		raw, err = adapter.transmitBasicAPDU(
			ctx,
			binding.deviceID,
			apdu,
			true,
		)
		if err != nil {
			return AKAResult{}, ErrEC20AKACommand
		}
	} else {
		channel, err := adapter.openLogicalChannel(
			ctx,
			binding.deviceID,
			binding.aid,
		)
		if err != nil {
			return AKAResult{}, err
		}
		var commandErr error
		raw, commandErr = adapter.transmitLogicalAPDU(
			ctx,
			binding.deviceID,
			channel,
			apdu,
			true,
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
	return parseUSIMAuthenticateResponse(raw)
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
			ErrEC20AKAResponse,
			len(raw),
		)
	}
	status := uint16(raw[len(raw)-2])<<8 | uint16(raw[len(raw)-1])
	body := raw[:len(raw)-2]
	if status != 0x9000 {
		switch status {
		case 0x9862:
			return AKAResult{}, ErrEC20AKAMACFailure
		default:
			return AKAResult{}, fmt.Errorf(
				"%w: status word %04X",
				ErrEC20AKAResponse,
				status,
			)
		}
	}
	if len(body) < 2 {
		return AKAResult{}, fmt.Errorf(
			"%w: response body length %d is too short",
			ErrEC20AKAResponse,
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
				ErrEC20AKAResponse,
				len(value),
			)
		}
		if len(res) < 4 || len(res) > 16 {
			return AKAResult{}, fmt.Errorf(
				"%w: invalid RES length %d",
				ErrEC20AKAResponse,
				len(res),
			)
		}
		ck, rest, ok := takeLV(rest)
		if !ok || len(ck) != 16 {
			return AKAResult{}, fmt.Errorf(
				"%w: invalid CK length %d",
				ErrEC20AKAResponse,
				len(ck),
			)
		}
		ik, rest, ok := takeLV(rest)
		if !ok || len(ik) != 16 {
			return AKAResult{}, fmt.Errorf(
				"%w: invalid IK length %d",
				ErrEC20AKAResponse,
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
					ErrEC20AKAResponse,
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
				ErrEC20AKAResponse,
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
			ErrEC20AKAResponse,
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

func (adapter *EC20Adapter) Snapshot(
	ctx context.Context,
	deviceID string,
) (RadioSnapshot, error) {
	mode, err := adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return RadioSnapshot{}, err
	}
	active, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		return RadioSnapshot{}, err
	}
	adapter.mu.Lock()
	adapter.checkpoints[deviceID] = ec20RadioCheckpoint{
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

func (adapter *EC20Adapter) StopCellularData(
	ctx context.Context,
	deviceID string,
) error {
	active, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		return err
	}
	for _, cid := range active {
		if _, err := adapter.execute(
			ctx,
			deviceID,
			fmt.Sprintf("AT+CGACT=0,%d", cid),
		); err != nil {
			return fmt.Errorf("deactivate EC20 PDP context %d: %w", cid, err)
		}
	}
	remaining, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return errors.New("vocat: EC20 cellular data remained active")
	}
	return nil
}

func (adapter *EC20Adapter) EnterVoWiFiRFOff(
	ctx context.Context,
	deviceID string,
) error {
	mode, err := adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if mode != 4 {
		if _, err := adapter.execute(ctx, deviceID, "AT+CFUN=4"); err != nil {
			return fmt.Errorf("enter EC20 RF-off mode: %w", err)
		}
	}
	mode, err = adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if mode != 4 {
		return fmt.Errorf("vocat: EC20 reported CFUN=%d after RF-off request", mode)
	}
	return nil
}

func (adapter *EC20Adapter) Restore(
	ctx context.Context,
	deviceID string,
	snapshot RadioSnapshot,
) error {
	if snapshot.OperatingMode < 0 {
		return errors.New("vocat: invalid EC20 radio snapshot")
	}
	currentMode, err := adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if currentMode != snapshot.OperatingMode {
		if _, err := adapter.execute(
			ctx,
			deviceID,
			fmt.Sprintf("AT+CFUN=%d", snapshot.OperatingMode),
		); err != nil {
			return fmt.Errorf("restore EC20 operating mode: %w", err)
		}
	}
	currentMode, err = adapter.readOperatingMode(ctx, deviceID)
	if err != nil {
		return err
	}
	if currentMode != snapshot.OperatingMode {
		return fmt.Errorf(
			"vocat: EC20 restore reported CFUN=%d, expected %d",
			currentMode,
			snapshot.OperatingMode,
		)
	}

	adapter.mu.Lock()
	checkpoint, found := adapter.checkpoints[deviceID]
	adapter.mu.Unlock()
	if !found {
		if snapshot.CellularDataEnabled {
			return errors.New("vocat: EC20 PDP restore evidence is unavailable")
		}
		checkpoint.activeCIDs = nil
	}
	if (snapshot.OperatingMode == 0 || snapshot.OperatingMode == 4) &&
		len(checkpoint.activeCIDs) > 0 {
		return errors.New("vocat: EC20 snapshot has active data in RF-off mode")
	}
	desiredCIDs := checkpoint.activeCIDs
	if !adapter.options.RestoreCellularData {
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

func (adapter *EC20Adapter) reconcileActiveCIDs(
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
			return fmt.Errorf("restore EC20 PDP context %d: %w", cid, err)
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
			return fmt.Errorf("restore EC20 PDP context %d: %w", cid, err)
		}
	}
	verified, err := adapter.readActiveCIDs(ctx, deviceID)
	if err != nil {
		return err
	}
	if !sameIntegers(verified, desired) {
		return fmt.Errorf(
			"vocat: EC20 PDP restore mismatch: active contexts %v",
			verified,
		)
	}
	return nil
}

func (adapter *EC20Adapter) readOperatingMode(
	ctx context.Context,
	deviceID string,
) (int, error) {
	response, err := adapter.execute(ctx, deviceID, "AT+CFUN?")
	if err != nil {
		return 0, fmt.Errorf("read EC20 operating mode: %w", err)
	}
	value := valueAfterATPrefix(response, "+CFUN:")
	fields := parseCSV(value)
	if len(fields) == 0 {
		return 0, errors.New("vocat: EC20 returned no CFUN mode")
	}
	mode, err := strconv.Atoi(fields[0])
	if err != nil || mode < 0 {
		return 0, errors.New("vocat: EC20 returned an invalid CFUN mode")
	}
	return mode, nil
}

func (adapter *EC20Adapter) readActiveCIDs(
	ctx context.Context,
	deviceID string,
) ([]int, error) {
	response, err := adapter.execute(ctx, deviceID, "AT+CGACT?")
	if err != nil {
		return nil, fmt.Errorf("read EC20 PDP contexts: %w", err)
	}
	var active []int
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "+CGACT:") {
			continue
		}
		fields := parseCSV(strings.TrimSpace(line[len("+CGACT:"):]))
		if len(fields) < 2 {
			return nil, errors.New("vocat: EC20 returned an invalid CGACT record")
		}
		cid, cidErr := strconv.Atoi(fields[0])
		state, stateErr := strconv.Atoi(fields[1])
		if cidErr != nil || stateErr != nil || cid <= 0 || (state != 0 && state != 1) {
			return nil, errors.New("vocat: EC20 returned an invalid CGACT record")
		}
		if state == 1 {
			active = append(active, cid)
		}
	}
	sort.Ints(active)
	return uniqueIntegers(active), nil
}

func (adapter *EC20Adapter) bindingFor(
	identity SIMIdentity,
) (ec20SIMBinding, error) {
	iccid := strings.TrimSpace(identity.ICCID)
	adapter.mu.Lock()
	binding, ok := adapter.bindings[iccid]
	adapter.mu.Unlock()
	if !ok || iccid == "" {
		return ec20SIMBinding{}, errors.New("vocat: EC20 SIM identity is not bound to a device")
	}
	if strings.TrimSpace(identity.IMSI) != binding.imsi {
		return ec20SIMBinding{}, ErrEC20IdentityChanged
	}
	return binding, nil
}

func (adapter *EC20Adapter) verifyLiveICCID(
	ctx context.Context,
	binding ec20SIMBinding,
) error {
	iccid, err := adapter.readICCID(ctx, binding.deviceID)
	if err != nil {
		return err
	}
	if iccid != binding.iccid {
		return ErrEC20IdentityChanged
	}
	return nil
}

func (adapter *EC20Adapter) discoverAKAApplication(
	ctx context.Context,
	deviceID string,
) (aid string, application string, err error) {
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
				return "", "", ErrEC20ApplicationAbsent
			}
		}
	}

	// AT+CUAD is optional on older EC20 firmware. CCHO still provides a
	// standards-based, evidence-bearing probe of the assigned USIM AID.
	return usimAIDPrefix, "USIM", nil
}

func (adapter *EC20Adapter) openLogicalChannel(
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
		return 0, fmt.Errorf("%w: open application", ErrEC20ApplicationAbsent)
	}
	value := valueAfterATPrefix(response, "+CCHO:")
	if value == "" {
		for _, line := range response.Lines {
			line = strings.TrimSpace(line)
			if _, parseErr := strconv.Atoi(line); parseErr == nil {
				value = line
				break
			}
		}
	}
	channel, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || channel < 1 || channel > 19 {
		return 0, errors.New("vocat: EC20 returned an invalid logical channel")
	}
	return channel, nil
}

func (adapter *EC20Adapter) closeLogicalChannel(
	ctx context.Context,
	deviceID string,
	channel int,
) error {
	if _, err := adapter.execute(
		ctx,
		deviceID,
		fmt.Sprintf("AT+CCHC=%d", channel),
	); err != nil {
		return fmt.Errorf("close EC20 logical channel: %w", err)
	}
	return nil
}

func (adapter *EC20Adapter) closeLogicalChannelWithCleanup(
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

func (adapter *EC20Adapter) selectBasicApplication(
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
	raw, err := adapter.transmitBasicAPDU(ctx, deviceID, apdu, false)
	if err != nil {
		return fmt.Errorf("select EC20 basic-channel application: %w", err)
	}
	_, status, err := splitAPDUStatus(raw)
	if err != nil {
		return err
	}
	if status != 0x9000 {
		return fmt.Errorf(
			"vocat: EC20 basic-channel SELECT returned %04X",
			status,
		)
	}
	return nil
}

func (adapter *EC20Adapter) transmitBasicAPDU(
	ctx context.Context,
	deviceID string,
	apdu []byte,
	sensitive bool,
) ([]byte, error) {
	if len(apdu) == 0 || len(apdu) > 261 {
		return nil, errors.New("vocat: invalid EC20 APDU length")
	}
	var collected []byte
	current := append([]byte(nil), apdu...)
	for exchange := 0; exchange < 4; exchange++ {
		command := fmt.Sprintf(
			`AT+CSIM=%d,"%s"`,
			len(current)*2,
			strings.ToUpper(hex.EncodeToString(current)),
		)
		var response modem.Response
		var err error
		if sensitive {
			response, err = adapter.executeSensitive(ctx, deviceID, command)
		} else {
			response, err = adapter.execute(ctx, deviceID, command)
		}
		if err != nil {
			return nil, errors.New("vocat: EC20 CSIM exchange failed")
		}
		raw, err := parseCSIMData(response)
		if err != nil {
			return nil, err
		}
		body, status, err := splitAPDUStatus(raw)
		if err != nil {
			return nil, err
		}
		collected = append(collected, body...)
		sw1 := byte(status >> 8)
		if sw1 != 0x61 && sw1 != 0x9f {
			collected = append(collected, byte(status>>8), byte(status))
			return collected, nil
		}
		// GET RESPONSE on the same basic channel. Le=0 means 256 bytes.
		current = []byte{0x00, 0xc0, 0x00, 0x00, byte(status)}
	}
	return nil, errors.New("vocat: EC20 APDU response chaining exceeded limit")
}

func (adapter *EC20Adapter) transmitLogicalAPDU(
	ctx context.Context,
	deviceID string,
	channel int,
	apdu []byte,
	sensitive bool,
) ([]byte, error) {
	if channel < 1 || channel > 19 || len(apdu) == 0 || len(apdu) > 261 {
		return nil, ErrEC20AKACommand
	}
	var collected []byte
	current := append([]byte(nil), apdu...)
	for exchange := 0; exchange < 4; exchange++ {
		command := fmt.Sprintf(
			`AT+CGLA=%d,%d,"%s"`,
			channel,
			len(current)*2,
			strings.ToUpper(hex.EncodeToString(current)),
		)
		var response modem.Response
		var err error
		if sensitive {
			response, err = adapter.executeSensitive(ctx, deviceID, command)
		} else {
			response, err = adapter.execute(ctx, deviceID, command)
		}
		if err != nil {
			return nil, errors.Join(ErrEC20AKACommand, err)
		}
		raw, err := parseCGLAData(response)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEC20AKAResponse, err)
		}
		body, status, err := splitAPDUStatus(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEC20AKAResponse, err)
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
		ErrEC20AKAResponse,
	)
}

func splitAPDUStatus(raw []byte) ([]byte, uint16, error) {
	if len(raw) < 2 {
		return nil, 0, errors.New("vocat: EC20 APDU has no status word")
	}
	status := uint16(raw[len(raw)-2])<<8 | uint16(raw[len(raw)-1])
	return raw[:len(raw)-2], status, nil
}

func (adapter *EC20Adapter) execute(
	ctx context.Context,
	deviceID string,
	command string,
) (modem.Response, error) {
	return adapter.executor.ExecuteAT(ctx, deviceID, command)
}

func (adapter *EC20Adapter) executeSensitive(
	ctx context.Context,
	deviceID string,
	command string,
) (modem.Response, error) {
	if executor, ok := adapter.executor.(EC20SensitiveATExecutor); ok {
		return executor.ExecuteSensitiveAT(ctx, deviceID, command)
	}
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
	fields := parseCSV(valueAfterATPrefix(response, "+CUAD:"))
	if len(fields) == 0 {
		return nil, errors.New("CUAD response has no data")
	}
	value := fields[len(fields)-1]
	data, err := hex.DecodeString(strings.Trim(value, `"`))
	if err != nil || len(data) == 0 {
		return nil, errors.New("CUAD response data is invalid")
	}
	if len(data) >= 2 && data[len(data)-2] == 0x90 && data[len(data)-1] == 0x00 {
		data = data[:len(data)-2]
	}
	return data, nil
}

func collectApplicationAIDs(data []byte) []string {
	var result []string
	var walk func([]byte)
	walk = func(value []byte) {
		for len(value) > 0 {
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

func parseCGLAData(response modem.Response) ([]byte, error) {
	return parseATAPDUData(response, "+CGLA:")
}

func parseCSIMData(response modem.Response) ([]byte, error) {
	return parseATAPDUData(response, "+CSIM:")
}

func parseATAPDUData(response modem.Response, prefix string) ([]byte, error) {
	fields := parseCSV(valueAfterATPrefix(response, prefix))
	if len(fields) < 2 {
		return nil, errors.New("EC20 response has no APDU")
	}
	declared, err := strconv.Atoi(fields[0])
	if err != nil || declared < 0 {
		return nil, errors.New("EC20 response has invalid APDU length")
	}
	encoded := strings.Trim(fields[1], `" `)
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("EC20 APDU response is not hexadecimal")
	}
	if declared != len(encoded) && declared != len(data) {
		return nil, errors.New("EC20 APDU response length mismatch")
	}
	return data, nil
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
