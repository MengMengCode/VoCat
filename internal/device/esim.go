package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/i18n"
	"vocat/internal/modem"
	"vocat/internal/pcsc"
)

// eUICC / eSIM (LPA, SGP.22) access over the modem's AT+CSIM APDU passthrough.
//
// The Quectel EC20's AT+CCHO logical-channel command is non-functional on the
// deployed firmware, so — like lpac's `at_csim` backend — we drive MANAGE
// CHANNEL / SELECT / STORE DATA manually over AT+CSIM. The logical channel is
// separate from the modem's own basic channel, so reading and switching
// profiles does not disturb the modem's network registration.
//
// Verified against a live eUICC: channel open/select/STORE DATA/close all
// succeed and GetProfilesInfo returns every profile with no authentication.

// isdRAID is the standard ISD-R AID that hosts the LPA functions (ES10).
const isdRAID = "A0000005591010FFFFFFFF8900000100"

// xesimISDRAID is the alternate ISD-R application exposed by XeSIM cards.
// It implements the same ES10 interface, but is not selectable through the
// standard ...0100 AID. Selecting it is a read-only capability probe; profile
// state is never changed during discovery.
const xesimISDRAID = "A0000005591010FFFFFFFF8900000177"

// eSTK multi-SE products expose each eUICC storage through its own vendor
// ISD-R AID. The standard GSMA AID aliases one of them, so probing only that
// AID silently hides the second storage.
const (
	estkProductAID = "A06573746B6D65FFFFFFFFFFFF6D6774"
	estkSE0AID     = "A06573746B6D65FFFF4953442D522030"
	estkSE1AID     = "A06573746B6D65FFFF4953442D522031"
)

func targetEuiccAID(aidHex string) string {
	aidHex = strings.ToUpper(strings.TrimSpace(aidHex))
	if aidHex == "" {
		return isdRAID
	}
	return aidHex
}

var (
	errNoLogicalChannel  = errors.New("esim: modem could not open a logical channel")
	errNoEUICC           = errors.New("esim: no eUICC (ISD-R) found on the inserted card")
	errESIMSW            = errors.New("esim: eUICC returned an error status word")
	errESIMRecovering    = errors.New("esim: profile-switch recovery is in progress")
	errEUICCChannelStuck = errors.New("esim: eUICC APDU channel is unavailable until the modem restarts")
)

// ErrNoEUICC is returned when the inserted card exposes no eUICC ISD-R, so the
// HTTP layer can render the empty state instead of an error.
var ErrNoEUICC = errNoEUICC

// ErrEUICCChannelStuck means the modem kept rejecting MANAGE CHANNEL or
// SELECT ISD-R with the EC20's non-descriptive +CME ERROR: 0 after retries.
// This is observed after SIM hot-swap and requires a modem restart; repeating
// the same profile-list request cannot reset the baseband's UIM/APDU state.
var ErrEUICCChannelStuck = errEUICCChannelStuck

// EsimProfile is one eUICC profile decoded from GetProfilesInfo.
type EsimProfile struct {
	ICCID           string `json:"iccid"`
	AID             string `json:"aidHex"`
	ServiceProvider string `json:"serviceProviderName,omitempty"`
	Name            string `json:"name,omitempty"`
	Nickname        string `json:"nickname,omitempty"`
	State           int    `json:"state"` // 0 = disabled, 1 = enabled
	StateText       string `json:"stateText"`
	Class           string `json:"classText,omitempty"`
}

// EsimInfo is the decoded profile list plus chip metadata for one eUICC.
type EsimInfo struct {
	EID      string        `json:"eid,omitempty"`
	AID      string        `json:"aidHex,omitempty"`
	Profiles []EsimProfile `json:"profiles"`
}

// EsimInventoryEntry is one independently addressable eUICC storage together
// with its profile list and production metadata.
type EsimInventoryEntry struct {
	Info EsimInfo
	Chip EsimChipInfo
}

// EnabledProfile returns the currently enabled profile, or nil.
func (info *EsimInfo) EnabledProfile() *EsimProfile {
	for index := range info.Profiles {
		if info.Profiles[index].State == 1 {
			return &info.Profiles[index]
		}
	}
	return nil
}

// decodeICCID converts a GSM BCD (nibble-swapped) ICCID to its digit string.
func decodeICCID(raw []byte) string {
	var builder strings.Builder
	for _, b := range raw {
		lo, hi := b&0x0F, b>>4
		if lo <= 9 {
			builder.WriteByte(byte('0' + lo))
		}
		if hi <= 9 {
			builder.WriteByte(byte('0' + hi))
		}
	}
	return builder.String()
}

// encodeFixedDigitBCD converts decimal digits to GSM BCD (nibble-swapped) and
// pads every unused nibble with F up to the requested fixed field size.
func encodeFixedDigitBCD(digits string, octets int, label string) ([]byte, error) {
	digits = strings.TrimSpace(digits)
	if digits == "" {
		return nil, fmt.Errorf("esim: empty %s", label)
	}
	if octets <= 0 || len(digits) > octets*2 {
		return nil, fmt.Errorf("esim: %s exceeds its %d-byte field", label, octets)
	}
	out := make([]byte, octets)
	for index := range out {
		out[index] = 0xFF
	}
	for index := 0; index < len(digits); index += 2 {
		lo := digits[index]
		if lo < '0' || lo > '9' {
			return nil, fmt.Errorf("esim: invalid %s digit %q", label, lo)
		}
		// hiNibble is the high nibble value; a trailing odd digit pads with 0xF.
		hiNibble := byte(0xF)
		if index+1 < len(digits) {
			hi := digits[index+1]
			if hi < '0' || hi > '9' {
				return nil, fmt.Errorf("esim: invalid %s digit %q", label, hi)
			}
			hiNibble = hi - '0'
		}
		out[index/2] = hiNibble<<4 | (lo - '0')
	}
	return out, nil
}

// SGP.22 defines Iccid as the 10-octet EF-ICCID representation even when the
// printed identifier contains only 18 or 19 digits.
func encodeICCID(digits string) ([]byte, error) {
	return encodeFixedDigitBCD(digits, 10, "ICCID")
}

func buildEnableProfileRequest(iccid string) ([]byte, error) {
	return buildEnableProfileRequestWithRefresh(iccid, true)
}

func buildEnableProfileRequestWithRefresh(iccid string, refresh bool) ([]byte, error) {
	bcd, err := encodeICCID(iccid)
	if err != nil {
		return nil, err
	}
	profileID := derConstruct(0xA0, derEncode(0x5A, bcd))
	refreshFlag := byte(0x00)
	if refresh {
		refreshFlag = 0xFF
	}
	return derConstruct(0xBF31, profileID, derEncode(0x81, []byte{refreshFlag})), nil
}

// parseCSIM extracts the payload and status word from an AT+CSIM response.
func parseCSIM(response modem.Response) ([]byte, int, error) {
	value := valueAfterPrefix(response, "+CSIM:")
	if value == "" {
		return nil, 0, errors.New("esim: modem did not return a +CSIM result")
	}
	parts := csvValues(value)
	if len(parts) < 2 {
		return nil, 0, fmt.Errorf("esim: malformed +CSIM result %q", value)
	}
	hexData := strings.Trim(parts[1], `"`)
	if len(hexData) < 4 {
		return nil, 0, fmt.Errorf("esim: short +CSIM data %q", hexData)
	}
	raw, err := hex.DecodeString(hexData)
	if err != nil {
		return nil, 0, fmt.Errorf("esim: decode +CSIM data: %w", err)
	}
	sw := int(raw[len(raw)-2])<<8 | int(raw[len(raw)-1])
	return raw[:len(raw)-2], sw, nil
}

// euiccChannel is an open logical channel to the eUICC's ISD-R.
type euiccChannel struct {
	manager      *Manager
	id           string
	channel      int
	pcscSession  *pcsc.Session
	qmiSession   nativeQMIEuiccSession
	qmiSlot      uint8
	resetOnClose bool
}

func (channel *euiccChannel) registerProfileRefresh(ctx context.Context) (bool, error) {
	refreshSession, ok := channel.qmiSession.(nativeQMIRefreshSession)
	if !ok {
		return false, nil
	}
	if err := refreshSession.RegisterUIMRefresh(ctx); err != nil {
		var unsupported *qmi.NotSupportedError
		if errors.As(err, &unsupported) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (channel *euiccChannel) completeProfileRefresh(ctx context.Context) error {
	refreshSession, ok := channel.qmiSession.(nativeQMIRefreshSession)
	if !ok {
		return nil
	}
	return refreshSession.CompleteUIMRefresh(ctx)
}

func (channel *euiccChannel) acknowledgeProfileRefresh(ctx context.Context) error {
	refreshSession, ok := channel.qmiSession.(nativeQMIRefreshSession)
	if !ok {
		return nil
	}
	return refreshSession.AcknowledgeUIMRefresh(ctx)
}

func (channel *euiccChannel) recoverCATBusy(ctx context.Context) error {
	if channel.qmiSession == nil {
		return nil
	}
	// A power cycle must happen while the CAT2 client remains registered, or
	// the card can issue its first proactive command before VoCat is listening
	// and immediately become busy again.
	if channel.channel > 0 {
		_ = channel.qmiSession.CloseLogicalChannel(ctx, channel.qmiSlot, byte(channel.channel))
		channel.channel = 0
	}
	power, ok := channel.qmiSession.(interface {
		PowerOffSIM(context.Context, uint8) error
		PowerOnSIM(context.Context, uint8) error
	})
	if !ok {
		return nil
	}
	if err := power.PowerOffSIM(ctx, channel.qmiSlot); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
	}
	if err := power.PowerOnSIM(ctx, channel.qmiSlot); err != nil {
		return err
	}
	return channel.completeProfileRefresh(ctx)
}

// csimAPDUTimeout bounds a single AT+CSIM exchange. Loading a BoundProfilePackage
// makes the eUICC decrypt/write sizeable SCP03t segments on-card, which can exceed
// the modem's default 3s command timeout, so eSIM APDUs get a longer budget.
const csimAPDUTimeout = 30 * time.Second

// csim sends one raw APDU over AT+CSIM and returns payload + status word.
func (manager *Manager) csim(ctx context.Context, id string, apdu []byte) ([]byte, int, error) {
	command := fmt.Sprintf("AT+CSIM=%d,\"%s\"", len(apdu)*2, strings.ToUpper(hex.EncodeToString(apdu)))
	state, err := manager.lookup(id)
	if err != nil {
		return nil, 0, err
	}
	if err := lockMutexContext(ctx, &state.opMu); err != nil {
		return nil, 0, err
	}
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return nil, 0, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		return nil, 0, err
	}
	// Give each eUICC APDU its own generous deadline (withTimeout preserves an
	// existing one, so a shorter caller deadline still wins).
	apduCtx, cancel := context.WithTimeout(ctx, csimAPDUTimeout)
	defer cancel()
	response, err := manager.command(apduCtx, client, command)
	if err != nil {
		return nil, 0, err
	}
	return parseCSIM(response)
}

// openEuicc opens a logical channel and selects the ISD-R AID on it.
func (manager *Manager) openEuicc(ctx context.Context, id string) (*euiccChannel, error) {
	return manager.openEuiccAID(ctx, id, isdRAID)
}

func (manager *Manager) openEuiccAID(ctx context.Context, id, aidHex string) (*euiccChannel, error) {
	if manager.esimRecoveryActive(id) {
		return nil, errESIMRecovering
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		channel, err := manager.openEuiccOnceAID(ctx, id, aidHex)
		if err == nil {
			return channel, nil
		}
		lastErr = err
		if attempt == 0 && errors.Is(err, errNoLogicalChannel) &&
			manager.releaseStaleEuiccChannel(ctx, id) {
			// EC20 firmware exposes only one MANAGE CHANNEL slot. A canceled or
			// interrupted APDU transaction can leave channel 1 allocated, after
			// which every eSIM page load returns 6A81 until reboot. Closing the
			// orphan while holding the shared UICC transaction lock makes the
			// operation self-healing without disturbing an active AKA exchange.
			continue
		}
		if attempt == 1 && isTransientEuiccCME(err) {
			// When SIM hot-swap occurs or the modem baseband APDU channel is stuck (+CME ERROR: 0),
			// perform a soft SIM subsystem reset (AT+CFUN=0 -> AT+CFUN=1/4) to re-initialize
			// card interface voltage and ATR without restarting the whole hardware module.
			_ = manager.softResetForProfileSwitch(ctx, id)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(600 * time.Millisecond):
			}
			continue
		}
		if !isTransientEuiccCME(err) {
			return nil, err
		}
		if attempt == 2 {
			return nil, fmt.Errorf("%w: %v", ErrEUICCChannelStuck, err)
		}
		delay := time.Duration(attempt+1) * 250 * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

func (manager *Manager) releaseStaleEuiccChannel(ctx context.Context, id string) bool {
	_, sw, err := manager.csim(ctx, id, []byte{0x00, 0x70, 0x80, 0x01, 0x00})
	return err == nil && sw == 0x9000
}

func (manager *Manager) openEuiccOnce(ctx context.Context, id string) (*euiccChannel, error) {
	return manager.openEuiccOnceAID(ctx, id, isdRAID)
}

func (manager *Manager) openEuiccOnceAID(ctx context.Context, id, aidHex string) (*euiccChannel, error) {
	state, lookupErr := manager.lookup(id)
	if lookupErr != nil {
		return nil, lookupErr
	}
	candidate := manager.candidateFor(state)
	if candidate.HardwareKind == pcsc.HardwareKind {
		return manager.openPCSCEuiccOnceAID(ctx, id, candidate, aidHex)
	}
	if strings.EqualFold(manager.esimTransportFor(state), "qmi") && isNativeQMICandidate(candidate) {
		return manager.openQMIEuiccOnceAID(ctx, id, candidate, aidHex)
	}
	// MANAGE CHANNEL (open): 00 70 00 00 01 -> "<channel> 90 00". This EC20
	// firmware requires the explicit one-byte expected length: Le=00 opens a
	// channel but then rejects SELECT ISD-R at the AT+CSIM layer.
	payload, sw, err := manager.csim(ctx, id, []byte{0x00, 0x70, 0x00, 0x00, 0x01})
	if err != nil {
		return nil, err
	}
	if sw != 0x9000 || len(payload) != 1 {
		return nil, errNoLogicalChannel
	}
	channel := &euiccChannel{manager: manager, id: id, channel: int(payload[0])}

	// SELECT ISD-R by AID on the logical channel: CLA=channel, INS=A4, P1=04.
	aidHex = strings.ToUpper(strings.TrimSpace(aidHex))
	aid, err := hex.DecodeString(aidHex)
	if err != nil || len(aid) == 0 || len(aid) > 255 {
		channel.close(context.Background())
		return nil, fmt.Errorf("esim: invalid ISD-R AID %q", aidHex)
	}
	selectAID := append([]byte{byte(channel.channel), 0xA4, 0x04, 0x00, byte(len(aid))}, aid...)
	_, sw, err = manager.csim(ctx, id, selectAID)
	if err != nil {
		channel.close(context.Background())
		return nil, err
	}
	if sw>>8 == 0x61 {
		// Drain the select FCP the card is holding with a proper GET RESPONSE
		// (CLA=0x80|channel, INS=0xC0). transmit() injects the channel into the
		// CLA low nibble, so the first byte here stays 0x80.
		_, sw, _ = channel.transmit(ctx, []byte{0x80, 0xC0, 0x00, 0x00, byte(sw & 0xFF)}, 0x80)
	}
	if sw != 0x9000 {
		channel.close(context.Background())
		return nil, errNoEUICC
	}
	return channel, nil
}

func (manager *Manager) openQMIEuiccOnceAID(ctx context.Context, id string, candidate modem.Candidate, aidHex string) (*euiccChannel, error) {
	aidHex = strings.ToUpper(strings.TrimSpace(aidHex))
	aid, err := hex.DecodeString(aidHex)
	if err != nil || len(aid) == 0 || len(aid) > 255 {
		return nil, fmt.Errorf("esim: invalid ISD-R AID %q", aidHex)
	}
	if manager.qmiRadioOpener == nil {
		return nil, errors.New("esim: QMI UIM transport is unavailable")
	}
	openContext, cancel := context.WithTimeout(ctx, csimAPDUTimeout)
	defer cancel()
	radioSession, err := manager.qmiRadioOpener(openContext, candidate.QMIControl)
	if err != nil {
		return nil, fmt.Errorf("esim: open QMI UIM transport: %w", err)
	}
	session, ok := radioSession.(nativeQMIEuiccSession)
	if !ok {
		_ = radioSession.Close()
		return nil, errors.New("esim: QMI UIM transport does not support logical channels")
	}
	const slot uint8 = 1
	logicalChannel, err := session.OpenLogicalChannel(openContext, slot, aid)
	if recoverySession, recoveryOK := session.(nativeQMIChannelRecoverySession); recoveryOK && isQMIInsufficientResources(err) {
		logicalChannel, err = openNativeQMIChannelWithRecovery(openContext, recoverySession, slot, aid)
	}
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("%w: %v", errNoEUICC, err)
	}
	return &euiccChannel{
		manager: manager, id: id, channel: int(logicalChannel),
		qmiSession: session, qmiSlot: slot,
	}, nil
}

func (manager *Manager) openPCSCEuiccOnceAID(ctx context.Context, id string, candidate modem.Candidate, aidHex string) (*euiccChannel, error) {
	session, err := manager.cardReaders.OpenSession(ctx, pcsc.Selector{
		USBPath: candidate.USBPath, ReaderName: candidate.ReaderName,
	})
	if err != nil {
		return nil, err
	}
	payload, sw, err := session.Transmit(ctx, []byte{0x00, 0x70, 0x00, 0x00, 0x01})
	if err != nil || sw != 0x9000 || len(payload) != 1 {
		session.Close()
		if err != nil {
			return nil, fmt.Errorf("esim: PC/SC MANAGE CHANNEL: %w", err)
		}
		return nil, errNoLogicalChannel
	}
	channel := &euiccChannel{manager: manager, id: id, channel: int(payload[0]), pcscSession: session}
	aidHex = strings.ToUpper(strings.TrimSpace(aidHex))
	aid, err := hex.DecodeString(aidHex)
	if err != nil || len(aid) == 0 || len(aid) > 255 {
		channel.close(context.Background())
		return nil, fmt.Errorf("esim: invalid ISD-R AID %q", aidHex)
	}
	selectAID := append([]byte{byte(channel.channel), 0xA4, 0x04, 0x00, byte(len(aid))}, aid...)
	_, selectSW, err := channel.transmit(ctx, selectAID, 0x00)
	if err != nil || selectSW != 0x9000 {
		channel.close(context.Background())
		return nil, errNoEUICC
	}
	return channel, nil
}

// discoverEuiccAIDs detects eSTK multi-SE and alternate-ISD-R cards without
// changing any profile state. The vendor product applet and candidate ISD-R
// applications are selected only as read-only capability probes. Per
// OpenEUICC's eSTK integration, generic AIDs are not appended after an eSTK SE
// opens, because the standard AID aliases one of the same storages.
func (manager *Manager) discoverEuiccAIDs(ctx context.Context, id string) []string {
	product, err := manager.openEuiccAID(ctx, id, estkProductAID)
	if err == nil {
		product.close(context.Background())

		var found []string
		for _, aid := range []string{estkSE0AID, estkSE1AID} {
			channel, err := manager.openEuiccAID(ctx, id, aid)
			if err != nil {
				continue
			}
			channel.close(context.Background())
			found = append(found, aid)
		}
		if len(found) > 0 {
			return found
		}
	}

	var found []string
	for _, aid := range []string{isdRAID, xesimISDRAID} {
		channel, err := manager.openEuiccAID(ctx, id, aid)
		if err != nil {
			continue
		}
		channel.close(context.Background())
		found = append(found, aid)
	}
	if len(found) > 0 {
		return found
	}
	// Preserve the old error path for a physical SIM with no eUICC. The caller
	// retries the standard AID once and returns ErrNoEUICC to the HTTP layer.
	return []string{isdRAID}
}

func isTransientEuiccCME(err error) bool {
	var commandErr *modem.CommandError
	return errors.As(err, &commandErr) &&
		strings.EqualFold(strings.TrimSpace(commandErr.Final), "+CME ERROR: 0")
}

// close releases the logical channel (MANAGE CHANNEL close).
func (channel *euiccChannel) close(ctx context.Context) {
	// Closing a logical channel is best-effort cleanup. Never let a modem that
	// stopped answering turn this cleanup into an unbounded wait (the eSIM page
	// would otherwise remain in its loading state forever).
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	if channel.qmiSession != nil {
		if channel.channel > 0 {
			_ = channel.qmiSession.CloseLogicalChannel(ctx, channel.qmiSlot, byte(channel.channel))
		}
		_ = channel.qmiSession.Close()
		channel.qmiSession = nil
		return
	}
	closeAPDU := []byte{0x00, 0x70, 0x80, byte(channel.channel), 0x00}
	_, _, _ = channel.exchange(ctx, closeAPDU)
	if channel.pcscSession != nil {
		if channel.resetOnClose {
			_ = channel.pcscSession.CloseWithReset()
		} else {
			_ = channel.pcscSession.Close()
		}
		channel.pcscSession = nil
	}
}

func (channel *euiccChannel) exchange(ctx context.Context, apdu []byte) ([]byte, int, error) {
	if channel.qmiSession != nil {
		raw, err := channel.qmiSession.SendAPDU(ctx, channel.qmiSlot, byte(channel.channel), apdu)
		if err != nil {
			return nil, 0, err
		}
		if len(raw) < 2 {
			return nil, 0, fmt.Errorf("esim: short QMI UIM APDU response")
		}
		sw := int(raw[len(raw)-2])<<8 | int(raw[len(raw)-1])
		return raw[:len(raw)-2], sw, nil
	}
	if channel.pcscSession != nil {
		payload, sw, err := channel.pcscSession.Transmit(ctx, apdu)
		return payload, int(sw), err
	}
	return channel.manager.csim(ctx, channel.id, apdu)
}

// transmit sends one APDU on the logical channel (CLA high nibble from insClass,
// channel number in the low nibble), following 61xx "more data" continuations,
// and returns the assembled payload.
func (channel *euiccChannel) transmit(ctx context.Context, apdu []byte, insClass byte) ([]byte, int, error) {
	apdu[0] = (apdu[0] & 0xF0) | byte(channel.channel)
	payload, sw, err := channel.exchange(ctx, apdu)
	if err != nil {
		return nil, 0, err
	}
	assembled := append([]byte(nil), payload...)
	guard := 0
	for sw>>8 == 0x61 && guard < 24 {
		guard++
		getResponse := []byte{0x80 | byte(channel.channel), 0xC0, 0x00, 0x00, byte(sw & 0xFF)}
		frag, nextSW, err := channel.exchange(ctx, getResponse)
		if err != nil {
			return nil, 0, err
		}
		assembled = append(assembled, frag...)
		sw = nextSW
	}
	return assembled, sw, nil
}

// es10 runs one ES10 command: it wraps the DER request body in one or more
// chained STORE DATA APDUs (see storeDataChained) and returns the assembled
// response body. Small requests produce a single P1=0x91/P2=0x00 block, exactly
// as before; larger ones (AuthenticateServer, LoadBoundProfilePackage, …) are
// split across continuation blocks.
func (channel *euiccChannel) es10(ctx context.Context, derRequest []byte) ([]byte, error) {
	return channel.storeDataChained(ctx, derRequest)
}

// derNode is one decoded BER-TLV element (long-form tags and lengths handled).
type derNode struct {
	tag      int
	value    []byte
	children []*derNode
}

// derParse decodes a sequence of BER-TLV elements. Constructed elements have
// their value recursively decoded into children.
func derParse(data []byte) []*derNode {
	var nodes []*derNode
	index := 0
	for index < len(data) {
		node, next, ok := derDecodeOne(data, index)
		if !ok {
			break
		}
		nodes = append(nodes, node)
		index = next
	}
	return nodes
}

func derDecodeOne(data []byte, start int) (*derNode, int, bool) {
	index := start
	if index >= len(data) {
		return nil, 0, false
	}
	first := data[index]
	index++
	constructed := first&0x20 != 0
	tag := int(first)
	if first&0x1F == 0x1F { // long-form tag: keep the full tag bytes (e.g. 9F70, BF2D)
		for index < len(data) {
			b := data[index]
			index++
			tag = tag<<8 | int(b)
			if b&0x80 == 0 {
				break
			}
		}
	}
	if index >= len(data) {
		return nil, 0, false
	}
	lengthByte := data[index]
	index++
	length := 0
	if lengthByte&0x80 == 0 {
		length = int(lengthByte)
	} else {
		count := int(lengthByte & 0x7F)
		if count == 0 || count > 4 || index+count > len(data) {
			return nil, 0, false
		}
		for i := 0; i < count; i++ {
			length = length<<8 | int(data[index])
			index++
		}
	}
	if index+length > len(data) {
		return nil, 0, false
	}
	value := data[index : index+length]
	node := &derNode{tag: tag, value: value}
	if constructed {
		node.children = derParse(value)
	}
	return node, index + length, true
}

// derValue returns the raw value of the first node with tag.
func derValue(nodes []*derNode, tag int) []byte {
	for _, node := range nodes {
		if node.tag == tag {
			return node.value
		}
	}
	return nil
}

// derFindAll recursively collects every node with the given tag. Icons live in
// primitive (non-constructed) leaves, so their bytes are never descended into.
func derFindAll(nodes []*derNode, tag int) []*derNode {
	var found []*derNode
	for _, node := range nodes {
		if node.tag == tag {
			found = append(found, node)
		}
		found = append(found, derFindAll(node.children, tag)...)
	}
	return found
}

// parseProfilesInfo decodes a GetProfilesInfo response body into profiles. The
// ProfileInfo records (tag E3) are collected wherever they sit (some cards use
// a BF3D root, others echo BF2D, with an optional A0 list wrapper).
func parseProfilesInfo(payload []byte) []EsimProfile {
	records := derFindAll(derParse(payload), 0xE3)
	var profiles []EsimProfile
	seenICCID := make(map[string]struct{})
	for _, record := range records {
		fields := record.children
		profile := EsimProfile{
			ServiceProvider: string(derValue(fields, 0x91)),
			Name:            string(derValue(fields, 0x92)),
			Nickname:        string(derValue(fields, 0x90)),
		}
		if iccid := derValue(fields, 0x5A); iccid != nil {
			profile.ICCID = decodeICCID(iccid)
		}
		// E3 is reused by constructed metadata inside some eUICC 4.x profile
		// records. Recursive discovery is needed for cards that wrap the real
		// ProfileInfo list, but those nested E3 nodes are not profiles and carry
		// no ICCID. Never expose an entry that cannot be safely addressed by the
		// ES10c profile operations; also collapse duplicate ICCIDs defensively.
		if !validProfileICCID(profile.ICCID) {
			continue
		}
		if _, exists := seenICCID[profile.ICCID]; exists {
			continue
		}
		seenICCID[profile.ICCID] = struct{}{}
		if aid := derValue(fields, 0x4F); aid != nil {
			profile.AID = strings.ToUpper(hex.EncodeToString(aid))
		}
		if state := derValue(fields, 0x9F70); len(state) == 1 {
			profile.State = int(state[0])
		}
		profile.StateText = i18n.T("已禁用")
		if profile.State == 1 {
			profile.StateText = i18n.T("已启用")
		}
		if class := derValue(fields, 0x95); len(class) == 1 {
			profile.Class = map[int]string{0: "test", 1: "provisioning", 2: "operational"}[int(class[0])]
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func validProfileICCID(iccid string) bool {
	if len(iccid) < 18 || len(iccid) > 20 || !strings.HasPrefix(iccid, "89") {
		return false
	}
	for _, character := range iccid {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// ESIMListProfiles reads the eUICC profile list via ES10c GetProfilesInfo.
func (manager *Manager) ESIMListProfiles(ctx context.Context, id string) (EsimInfo, error) {
	ctx, cancel := boundESIMContext(ctx)
	defer cancel()
	if err := manager.lockESIMContext(ctx); err != nil {
		return EsimInfo{}, err
	}
	defer manager.unlockESIM()
	if manager.esimRecoveryActive(id) {
		if cached, ok := manager.cachedESIMInfo(id); ok {
			return cached, nil
		}
		return EsimInfo{}, errESIMRecovering
	}
	var lastErr error
	for _, aid := range manager.discoverEuiccAIDs(ctx, id) {
		channel, err := manager.openEuiccAID(ctx, id, aid)
		if err != nil {
			lastErr = err
			continue
		}
		payload, err := channel.es10(ctx, []byte{0xBF, 0x2D, 0x00}) // GetProfilesInfo
		channel.close(context.Background())
		if err != nil {
			lastErr = err
			continue
		}
		info := EsimInfo{AID: aid, Profiles: parseProfilesInfo(payload)}
		manager.cacheESIMInfo(id, info)
		return info, nil
	}
	if lastErr != nil {
		return EsimInfo{}, lastErr
	}
	return EsimInfo{}, ErrNoEUICC
}

func boundESIMContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 10*time.Second)
}

// ESIMSwitchProfile enables one profile by ICCID via ES10c EnableProfile.
func (manager *Manager) ESIMSwitchProfile(ctx context.Context, id string, iccid string, aidHex string) error {
	iccid = strings.TrimSpace(iccid)
	if iccid == "" {
		return errors.New("esim: an ICCID is required")
	}
	_, nativeQMI, nativeErr := manager.nativeQMIControl(id)
	if nativeErr != nil {
		return nativeErr
	}
	manager.lockESIM()
	if err := manager.waitForESIMRecovery(ctx, id); err != nil {
		manager.unlockESIM()
		return err
	}
	channel, err := manager.openEuiccAID(ctx, id, targetEuiccAID(aidHex))
	if err != nil {
		manager.unlockESIM()
		return err
	}
	refreshRequested := !nativeQMI
	if nativeQMI {
		refreshContext, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		refreshRequested, err = channel.registerProfileRefresh(refreshContext)
		cancelRefresh()
		if err != nil {
			channel.close(context.Background())
			manager.unlockESIM()
			return fmt.Errorf("esim: register QMI UIM refresh: %w", err)
		}
		// After a refresh=true attempt reports catBusy, retry without asking the
		// eUICC to start another REFRESH proactive command. SGP.22 permits the
		// card to terminate the pre-existing proactive session in this mode; the
		// native-QMI recovery below performs the required SIM reset and cache
		// reload on behalf of the device.
		if attempt, _ := ctx.Value(esimCATBusyRetryKey{}).(int); attempt > 0 {
			refreshRequested = false
		}
	}
	der, err := buildEnableProfileRequestWithRefresh(iccid, refreshRequested)
	if err != nil {
		channel.close(context.Background())
		manager.unlockESIM()
		return err
	}

	// EnableProfile request (SGP.22 ES10c, per lpac):
	//   BF31 { A0 { 5A <iccid bcd> } 81 01 FF }   (refresh = yes)
	// The profileIdentifier is an explicitly-tagged [0] CHOICE, so the ICCID
	// element (5A) must be wrapped in A0 — omitting that wrapper makes the eUICC
	// reject the command with result 0x7F (undefined error). refreshFlag (81)
	// stays a sibling of A0, directly under BF31.
	// EnableProfile is a non-idempotent commit. Once its APDU starts, a browser
	// disconnect or reverse-proxy timeout must not cancel it halfway through and
	// skip post-commit recovery; EC20 may otherwise remain in SIM failure
	// (+CME 13).
	commitContext, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), csimAPDUTimeout)
	payload, err := channel.es10(commitContext, der)
	cancelCommit()
	// A rejected EnableProfile (for example CAT busy) does not emit REFRESH.
	// Parse the card-level result before waiting for an indication, otherwise
	// every retry needlessly waits for the refresh timeout.
	resultBeforeClose, resultPresentBeforeClose := enableProfileResult(payload)
	if err == nil && resultPresentBeforeClose && byte(resultBeforeClose) == 5 && nativeQMI {
		// Registering CAT2 may immediately deliver a proactive command that was
		// already pending before EnableProfile. Drain it on catBusy so the raw
		// REFRESH command receives its terminal response before the retry.
		catContext, cancelCAT := context.WithTimeout(context.Background(), 3*time.Second)
		_ = channel.completeProfileRefresh(catContext)
		cancelCAT()
		if attempt, _ := ctx.Value(esimCATBusyRetryKey{}).(int); attempt == 0 {
			recoveryContext, cancelRecovery := context.WithTimeout(context.Background(), 12*time.Second)
			_ = channel.recoverCATBusy(recoveryContext)
			cancelRecovery()
		}
		ackContext, cancelAck := context.WithTimeout(context.Background(), 5*time.Second)
		_ = channel.acknowledgeProfileRefresh(ackContext)
		cancelAck()
	}
	if err == nil && resultPresentBeforeClose &&
		enableProfileResponseError(byte(resultBeforeClose), payload) == nil &&
		refreshRequested && nativeQMI {
		refreshContext, cancelRefresh := context.WithTimeout(context.Background(), 20*time.Second)
		_ = channel.completeProfileRefresh(refreshContext)
		cancelRefresh()
	}
	// Release the logical channel before any reset: openEuicc's csim holds
	// opMu only for the duration of each APDU, so by here the lock is free.
	closeContext, cancelClose := context.WithTimeout(context.Background(), csimAPDUTimeout)
	channel.resetOnClose = channel.pcscSession != nil
	channel.close(closeContext)
	cancelClose()
	if err != nil {
		// The card may have committed immediately before the transport error. A
		// detached reset is safe in either case and prevents an uncertain switch
		// from leaving the modem's SIM cache unusable.
		manager.startProfileSwitchRecovery(id)
		manager.unlockESIM()
		return err
	}
	// A transport SW 9000 only means the APDU reached the eUICC. The real outcome
	// is the EnableProfile result code (tag 80) inside the BF31 response — honour
	// it so a rejected switch is surfaced instead of reported as "switched".
	result, ok := enableProfileResult(payload)
	if !ok {
		manager.startProfileSwitchRecovery(id)
		manager.unlockESIM()
		return fmt.Errorf("esim: unexpected EnableProfile response %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	if err := enableProfileResponseError(byte(result), payload); err != nil {
		if errors.Is(err, ErrESIMEnableCATBusy) {
			attempt, _ := ctx.Value(esimCATBusyRetryKey{}).(int)
			if attempt < 11 {
				manager.unlockESIM()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
				}
				return manager.ESIMSwitchProfile(context.WithValue(ctx, esimCATBusyRetryKey{}, attempt+1), id, iccid, aidHex)
			}
		}
		manager.unlockESIM()
		return err
	}
	manager.markCachedProfileEnabled(id, iccid)
	// EnableProfile already requested an eUICC REFRESH. Some AT modems consume
	// that proactive command and expose the new subscription immediately, so a
	// full CFUN=1,1 reset would only add downtime. Give those devices a short
	// chance to prove that their SIM cache is current; modems that keep reporting
	// the old ICCID continue through the established reboot/recovery path below.
	if manager.canVerifyProfileSwitchWithoutRestart(id) {
		probeContext, cancelProbe := context.WithTimeout(
			context.WithoutCancel(ctx),
			profileSwitchRefreshProbeTimeout(manager),
		)
		probeErr := manager.verifySwitchedICCIDAttempts(probeContext, id, iccid, 3, time.Second)
		cancelProbe()
		if probeErr == nil {
			// Repopulate the cached snapshot while the AT transport is still live.
			// Verification above is authoritative, so snapshot refresh remains
			// best-effort just as it is after the legacy reboot path.
			refreshContext, cancelRefresh := context.WithTimeout(
				context.WithoutCancel(ctx),
				manager.longTimeout,
			)
			_, _ = manager.Refresh(refreshContext, id)
			cancelRefresh()
			manager.unlockESIM()
			mbnContext, cancelMBN := context.WithTimeout(context.WithoutCancel(ctx), profileSwitchVerificationTimeout(manager))
			defer cancelMBN()
			manager.reconcileEC20MBNAfterProfileSwitchBestEffort(mbnContext, id, iccid)
			return nil
		}
	}
	// The eUICC accepted the target profile. Reset and repopulate the modem in
	// a detached recovery so it survives an HTTP disconnect, but keep this API
	// call pending until the live modem ICCID proves that the switch took effect.
	manager.startProfileSwitchRecovery(id)
	manager.unlockESIM()

	verifyContext, cancelVerify := context.WithTimeout(context.WithoutCancel(ctx), profileSwitchVerificationTimeout(manager))
	defer cancelVerify()
	if err := manager.waitForESIMRecovery(verifyContext, id); err != nil {
		return err
	}
	if err := manager.verifySwitchedICCID(verifyContext, id, iccid); err != nil {
		return err
	}
	mbnContext, cancelMBN := context.WithTimeout(context.WithoutCancel(ctx), profileSwitchVerificationTimeout(manager))
	defer cancelMBN()
	manager.reconcileEC20MBNAfterProfileSwitchBestEffort(mbnContext, id, iccid)
	return nil
}

type esimCATBusyRetryKey struct{}

func (manager *Manager) startProfileSwitchRecovery(id string) {
	done := make(chan struct{})
	manager.esimRecoveryMu.Lock()
	if manager.esimRecoveries == nil {
		manager.esimRecoveries = make(map[string]chan struct{})
	}
	if manager.esimRecoveries[id] != nil {
		manager.esimRecoveryMu.Unlock()
		return
	}
	manager.esimRecoveries[id] = done
	manager.esimRecoveryMu.Unlock()
	go func() {
		manager.recoverAfterProfileSwitch(id)
		manager.esimRecoveryMu.Lock()
		if manager.esimRecoveries[id] == done {
			delete(manager.esimRecoveries, id)
			close(done)
		}
		manager.esimRecoveryMu.Unlock()
	}()
}

func (manager *Manager) waitForESIMRecovery(ctx context.Context, id string) error {
	manager.esimRecoveryMu.Lock()
	done := manager.esimRecoveries[id]
	manager.esimRecoveryMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("esim: wait for profile-switch recovery: %w", ctx.Err())
	}
}

func (manager *Manager) esimRecoveryActive(id string) bool {
	manager.esimRecoveryMu.Lock()
	active := manager.esimRecoveries[id] != nil
	manager.esimRecoveryMu.Unlock()
	return active
}

func cloneESIMInfo(info EsimInfo) EsimInfo {
	info.Profiles = append([]EsimProfile(nil), info.Profiles...)
	return info
}

func (manager *Manager) cachedESIMInfo(id string) (EsimInfo, bool) {
	manager.esimCacheMu.RLock()
	info, ok := manager.esimCache[id]
	manager.esimCacheMu.RUnlock()
	return cloneESIMInfo(info), ok
}

func (manager *Manager) cacheESIMInfo(id string, info EsimInfo) {
	manager.esimCacheMu.Lock()
	manager.esimCache[id] = cloneESIMInfo(info)
	manager.esimCacheMu.Unlock()
}

func (manager *Manager) markCachedProfileEnabled(id, iccid string) {
	manager.esimCacheMu.Lock()
	info, ok := manager.esimCache[id]
	if ok {
		for index := range info.Profiles {
			if info.Profiles[index].ICCID == iccid {
				info.Profiles[index].State = 1
				info.Profiles[index].StateText = i18n.T("已启用")
			} else {
				info.Profiles[index].State = 0
				info.Profiles[index].StateText = i18n.T("已禁用")
			}
		}
		manager.esimCache[id] = info
	}
	manager.esimCacheMu.Unlock()
}

func (manager *Manager) markCachedProfileDisabled(id, iccid string) {
	manager.esimCacheMu.Lock()
	info, ok := manager.esimCache[id]
	if ok {
		for index := range info.Profiles {
			if info.Profiles[index].ICCID == iccid {
				info.Profiles[index].State = 0
				info.Profiles[index].StateText = i18n.T("已禁用")
				break
			}
		}
		manager.esimCache[id] = info
	}
	manager.esimCacheMu.Unlock()
}

func (manager *Manager) removeCachedProfile(id, iccid string) {
	manager.esimCacheMu.Lock()
	info, ok := manager.esimCache[id]
	if ok {
		profiles := info.Profiles[:0]
		for _, profile := range info.Profiles {
			if profile.ICCID != iccid {
				profiles = append(profiles, profile)
			}
		}
		info.Profiles = profiles
		manager.esimCache[id] = info
	}
	manager.esimCacheMu.Unlock()
}

func (manager *Manager) renameCachedProfile(id, iccid, nickname string) {
	manager.esimCacheMu.Lock()
	info, ok := manager.esimCache[id]
	if ok {
		for index := range info.Profiles {
			if info.Profiles[index].ICCID == iccid {
				info.Profiles[index].Nickname = nickname
				break
			}
		}
		manager.esimCache[id] = info
	}
	manager.esimCacheMu.Unlock()
}

// recoverAfterProfileSwitch owns the post-commit SIM reset independently of the
// initiating HTTP request.
func (manager *Manager) recoverAfterProfileSwitch(id string) {
	resetContext, cancelReset := context.WithTimeout(context.Background(), manager.longTimeout)
	if native, err := manager.powerCycleNativeQMISIM(resetContext, id); native {
		cancelReset()
		if err == nil {
			time.Sleep(1 * time.Second)
		}
		// Native WWAN identity and profile verification are both QMI-backed.
		// Do not enter the AT refresh path: OpenStick firmware can accept the
		// switch while timing out every EC20-specific AT identity command.
		return
	}
	cancelReset()
	if !manager.isPCSCDevice(id) {
		resetContext, cancelReset := context.WithTimeout(context.Background(), manager.commandTimeout*2)
		_ = manager.softResetForProfileSwitch(resetContext, id)
		cancelReset()
	}
	manager.refreshAfterProfileSwitch(id)
}

// refreshAfterProfileSwitch repopulates the device snapshot in the background
// after an eSIM profile switch.
func (manager *Manager) refreshAfterProfileSwitch(id string) {
	if manager.isPCSCDevice(id) {
		time.Sleep(500 * time.Millisecond)
		for attempt := 0; attempt < 5; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), manager.commandTimeout*2)
			_, _ = manager.Discover(ctx)
			_, err := manager.Refresh(ctx, id)
			cancel()
			if err == nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		return
	}
	const (
		settle   = 1 * time.Second
		interval = 1 * time.Second
		attempts = 5
	)
	time.Sleep(settle)
	for attempt := 0; attempt < attempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), manager.commandTimeout*2)
		_, err := manager.Refresh(ctx, id)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(interval)
	}
}

func (manager *Manager) isPCSCDevice(id string) bool {
	state, err := manager.lookup(id)
	if err != nil {
		return false
	}
	return manager.candidateFor(state).HardwareKind == pcsc.HardwareKind
}

// enableProfileResult extracts the EnableProfile result code (tag 80) from the
// ES10c response body. ok is false when no result code is present.
func enableProfileResult(payload []byte) (int, bool) {
	for _, node := range derFindAll(derParse(payload), 0x80) {
		if len(node.value) > 0 {
			return int(node.value[0]), true
		}
	}
	return 0, false
}

var (
	ErrESIMEnableProfileNotFound  = errors.New("esim: profile to enable was not found on the selected eUICC")
	ErrESIMProfileNotDisabled     = errors.New("esim: profile is not currently disabled")
	ErrESIMEnableDisallowedPolicy = errors.New("esim: profile switch is not allowed by the active profile policy")
	ErrESIMWrongProfileReenabling = errors.New("esim: profile cannot be re-enabled from the current profile state")
	ErrESIMEnableCATBusy          = errors.New("esim: card application toolkit is busy; retry enabling later")
	ErrESIMEnableUndefined        = errors.New("esim: eUICC returned undefinedError while enabling this profile; the card did not provide a more specific reason")
)

// enableProfileResponseError maps the complete SGP.22 EnableProfileResult
// enumeration. In particular, 0x7F is undefinedError: it is a definite card
// rejection, but it does not prove that the subscription itself is unusable.
func enableProfileResponseError(result byte, payload []byte) error {
	raw := strings.ToUpper(hex.EncodeToString(payload))
	wrap := func(cause error) error {
		return fmt.Errorf("%w (result=0x%02X, raw %s)", cause, result, raw)
	}
	switch result {
	case 0:
		return nil
	case 1:
		return wrap(ErrESIMEnableProfileNotFound)
	case 2:
		return wrap(ErrESIMProfileNotDisabled)
	case 3:
		return wrap(ErrESIMEnableDisallowedPolicy)
	case 4:
		return wrap(ErrESIMWrongProfileReenabling)
	case 5:
		return wrap(ErrESIMEnableCATBusy)
	case 0x7F:
		return wrap(ErrESIMEnableUndefined)
	default:
		return fmt.Errorf("esim: eUICC rejected EnableProfile, result=0x%02X (raw %s)", result, raw)
	}
}

func profileSwitchVerificationTimeout(manager *Manager) time.Duration {
	// A slow EC20 can spend one long command timeout resetting, then several
	// snapshot attempts reopening its USB serial port. Keep the HTTP operation
	// alive for that recovery, with a practical floor for unusually slow hosts.
	timeout := manager.longTimeout*2 + 90*time.Second
	if timeout < 2*time.Minute {
		return 2 * time.Minute
	}
	return timeout
}

func profileSwitchRefreshProbeTimeout(manager *Manager) time.Duration {
	// Allow both standard ICCID commands to consume one ordinary command
	// timeout, plus a small window for the eUICC REFRESH to settle. Keep the
	// optimisation bounded so an older modem reaches its required reboot soon.
	timeout := manager.commandTimeout*2 + time.Second
	if timeout < 3*time.Second {
		return 3 * time.Second
	}
	if timeout > 10*time.Second {
		return 10 * time.Second
	}
	return timeout
}

func (manager *Manager) canVerifyProfileSwitchWithoutRestart(id string) bool {
	_, native, err := manager.nativeQMIControl(id)
	return err == nil && !native && !manager.isPCSCDevice(id)
}

// verifySwitchedICCID performs a fresh baseband read after recovery. An ES10c
// result of zero only means the eUICC accepted the operation; the state change
// is finalized by REFRESH/reset. The UI must not report success until the modem
// is actually exposing the requested ICCID.
func (manager *Manager) verifySwitchedICCID(ctx context.Context, id, expected string) error {
	return manager.verifySwitchedICCIDAttempts(ctx, id, expected, 6, 1*time.Second)
}

func (manager *Manager) verifySwitchedICCIDAttempts(
	ctx context.Context,
	id string,
	expected string,
	attempts int,
	interval time.Duration,
) error {
	expected = strings.TrimSpace(expected)
	var lastICCID string
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if control, native, nativeErr := manager.nativeQMIControl(id); native {
			if nativeErr != nil {
				lastErr = nativeErr
			} else {
				state, lookupErr := manager.lookup(id)
				if lookupErr != nil {
					lastErr = lookupErr
				} else {
					candidate := manager.candidateFor(state)
					candidate.QMIControl = control
					live, readErr := manager.readNativeQMIICCID(ctx, candidate)
					if readErr == nil {
						lastICCID = strings.TrimSpace(live)
						if lastICCID == expected {
							return nil
						}
						lastErr = fmt.Errorf("native QMI still reports ICCID %s", lastICCID)
					} else {
						lastErr = readErr
					}
				}
			}
		} else if nativeErr != nil {
			lastErr = nativeErr
		} else if manager.isPCSCDevice(id) {
			snapshot, err := manager.Refresh(ctx, id)
			if err == nil {
				lastICCID = strings.TrimSpace(snapshot.ICCID)
				if lastICCID == expected {
					return nil
				}
				err = fmt.Errorf("reader still reports ICCID %s", lastICCID)
			}
			lastErr = err
		} else {
			for _, command := range []string{"AT+CCID", "AT+QCCID"} {
				commandContext, cancel := context.WithTimeout(ctx, manager.commandTimeout)
				response, err := manager.ExecuteAT(commandContext, id, command)
				cancel()
				if err != nil {
					lastErr = err
					continue
				}
				live := parseICCIDIdentifier(response, []string{"+CCID:", "+QCCID:"}, 18, 22)
				if live == "" {
					lastErr = errors.New("modem response contained no valid ICCID")
					continue
				}
				lastICCID = live
				if live == expected {
					return nil
				}
				lastErr = fmt.Errorf("modem still reports ICCID %s", live)
				break
			}
		}
		if attempt+1 < attempts {
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return fmt.Errorf("esim: verify enabled profile %s: %w", expected, ctx.Err())
			}
		}
	}
	if lastICCID != "" {
		return fmt.Errorf("esim: EnableProfile was accepted but target ICCID %s did not become active after modem recovery (current ICCID %s)", expected, lastICCID)
	}
	return fmt.Errorf("esim: EnableProfile was accepted but target ICCID %s could not be verified after modem recovery: %w", expected, lastErr)
}
