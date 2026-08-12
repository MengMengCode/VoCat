package pcsc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const usimAIDPrefix = "A0000000871002"

type Service struct {
	mu      sync.Mutex
	backend Backend
}

// Session is an exclusive connection to one smart card. It is used by eUICC
// operations which must keep the same PC/SC transaction and logical channel
// alive across a sequence of APDUs.
type Session struct {
	service *Service
	card    Card
	closed  bool
}

func New() *Service {
	return &Service{backend: newNativeBackend()}
}

func NewWithBackend(backend Backend) *Service {
	return &Service{backend: backend}
}

func DeviceID(reader Reader) string {
	identity := strings.TrimSpace(reader.USBPath)
	if identity == "" {
		identity = strings.TrimSpace(reader.Name)
	}
	sum := sha256.Sum256([]byte(identity))
	return "reader-" + hex.EncodeToString(sum[:8])
}

func (service *Service) Readers(ctx context.Context) ([]Reader, error) {
	if service == nil || service.backend == nil {
		return nil, ErrUnavailable
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.backend.Readers(ctx)
}

// OpenSession opens one card and holds the service lock until Close. Callers
// must close the returned session; this prevents AKA/identity reads from
// interleaving with a stateful ES10 transaction.
func (service *Service) OpenSession(ctx context.Context, selector Selector) (*Session, error) {
	if service == nil || service.backend == nil {
		return nil, ErrUnavailable
	}
	if err := selector.validate(); err != nil {
		return nil, err
	}
	service.mu.Lock()
	card, err := service.backend.Open(ctx, selector)
	if err != nil {
		service.mu.Unlock()
		return nil, err
	}
	return &Session{service: service, card: card}, nil
}

// Transmit sends one raw APDU. Unlike the ordinary SIM helpers, it leaves
// 61xx continuation handling to the eUICC logical-channel implementation so
// GET RESPONSE uses the correct channel CLA.
func (session *Session) Transmit(ctx context.Context, command []byte) ([]byte, uint16, error) {
	if session == nil || session.card == nil || session.closed {
		return nil, 0, errors.New("pcsc: card session is closed")
	}
	if raw, ok := session.card.(interface {
		TransmitRaw(context.Context, []byte) ([]byte, uint16, error)
	}); ok {
		return raw.TransmitRaw(ctx, command)
	}
	return session.card.Transmit(ctx, command)
}

func (session *Session) Close() error {
	return session.close(false)
}

// CloseWithReset resets the card while releasing the PC/SC connection. eUICC
// EnableProfile requires this refresh boundary before the newly enabled USIM
// application and ICCID become visible to subsequent callers.
func (session *Session) CloseWithReset() error {
	return session.close(true)
}

func (session *Session) close(reset bool) error {
	if session == nil || session.closed {
		return nil
	}
	session.closed = true
	var err error
	if resetter, ok := session.card.(interface{ CloseWithReset() error }); reset && ok {
		err = resetter.CloseWithReset()
	} else {
		err = session.card.Close()
	}
	session.service.mu.Unlock()
	return err
}

func (service *Service) Snapshot(ctx context.Context, selector Selector, pin string) (Snapshot, error) {
	readers, err := service.Readers(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	reader, ok := matchReader(readers, selector)
	if !ok {
		return Snapshot{}, ErrReaderNotFound
	}
	result := Snapshot{Reader: reader}
	if !reader.CardPresent {
		return result, ErrNoCard
	}
	identity, err := service.ReadIdentity(ctx, selector, pin)
	result.Identity = identity
	return result, err
}

func (service *Service) ReadIdentity(ctx context.Context, selector Selector, pin string) (Identity, error) {
	if service == nil || service.backend == nil {
		return Identity{}, ErrUnavailable
	}
	if err := selector.validate(); err != nil {
		return Identity{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	card, err := service.backend.Open(ctx, selector)
	if err != nil {
		return Identity{}, err
	}
	defer card.Close()
	return readIdentity(ctx, card, pin)
}

func (service *Service) CheckReady(
	ctx context.Context,
	selector Selector,
	expectedICCID string,
	pin string,
) (string, error) {
	if service == nil || service.backend == nil {
		return "", ErrUnavailable
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	card, err := service.backend.Open(ctx, selector)
	if err != nil {
		return "", err
	}
	defer card.Close()
	iccid, err := readICCID(ctx, card)
	if err != nil {
		return "", err
	}
	if expected := strings.TrimSpace(expectedICCID); expected != "" && !strings.EqualFold(expected, iccid) {
		return "", ErrCardChanged
	}
	aid, err := selectUSIM(ctx, card)
	if err != nil {
		return "", err
	}
	if err := verifyPIN(ctx, card, pin); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(aid)), nil
}

func (service *Service) Authenticate(
	ctx context.Context,
	selector Selector,
	expectedICCID string,
	pin string,
	challenge AKAChallenge,
) (AKAResult, error) {
	if service == nil || service.backend == nil {
		return AKAResult{}, ErrUnavailable
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	card, err := service.backend.Open(ctx, selector)
	if err != nil {
		return AKAResult{}, err
	}
	defer card.Close()
	iccid, err := readICCID(ctx, card)
	if err != nil {
		return AKAResult{}, err
	}
	if expected := strings.TrimSpace(expectedICCID); expected != "" && !strings.EqualFold(expected, iccid) {
		return AKAResult{}, ErrCardChanged
	}
	if _, err := selectUSIM(ctx, card); err != nil {
		return AKAResult{}, err
	}
	if err := verifyPIN(ctx, card, pin); err != nil {
		return AKAResult{}, err
	}
	apdu := make([]byte, 0, 40)
	apdu = append(apdu, 0x00, 0x88, 0x00, 0x81, 0x22, 0x10)
	apdu = append(apdu, challenge.RAND[:]...)
	apdu = append(apdu, 0x10)
	apdu = append(apdu, challenge.AUTN[:]...)
	apdu = append(apdu, 0x00)
	data, sw, err := card.Transmit(ctx, apdu)
	if err != nil {
		return AKAResult{}, errors.New("pcsc: USIM authentication transport failed")
	}
	if sw == 0x9862 {
		return AKAResult{}, ErrAKARejected
	}
	if sw != 0x9000 {
		return AKAResult{}, fmt.Errorf("pcsc: USIM authentication failed with status %04X", sw)
	}
	return parseAKAResponse(data)
}

func matchReader(readers []Reader, selector Selector) (Reader, bool) {
	path := strings.TrimSpace(selector.USBPath)
	name := strings.TrimSpace(selector.ReaderName)
	for _, reader := range readers {
		if path != "" && reader.USBPath == path {
			return reader, true
		}
	}
	for _, reader := range readers {
		if name != "" && reader.Name == name {
			return reader, true
		}
	}
	return Reader{}, false
}

func readIdentity(ctx context.Context, card Card, pin string) (Identity, error) {
	identity := Identity{PINTries: -1}
	iccid, err := readICCID(ctx, card)
	if err != nil {
		return identity, err
	}
	identity.ICCID = iccid
	aid, err := selectUSIM(ctx, card)
	if err != nil {
		return identity, err
	}
	identity.USIMAID = append([]byte(nil), aid...)
	if err := verifyPIN(ctx, card, pin); err != nil {
		identity.PINRequired = errors.Is(err, ErrPINRequired) || errors.Is(err, ErrPINTriesLow)
		var pinErr *PINError
		if errors.As(err, &pinErr) {
			identity.PINTries = pinErr.Tries
		}
		return identity, err
	}
	if err := selectFile(ctx, card, []byte{0x6F, 0x07}); err != nil {
		return identity, fmt.Errorf("pcsc: select EF_IMSI: %w", err)
	}
	imsiData, err := readBinary(ctx, card, 9)
	if err != nil {
		return identity, fmt.Errorf("pcsc: read EF_IMSI: %w", err)
	}
	identity.IMSI, err = decodeIMSI(imsiData)
	if err != nil {
		return identity, err
	}
	if _, selectErr := selectApplication(ctx, card, aid); selectErr == nil {
		if selectErr = selectFile(ctx, card, []byte{0x6F, 0xAD}); selectErr == nil {
			if data, readErr := readBinary(ctx, card, 4); readErr == nil && len(data) >= 4 {
				length := int(data[3] & 0x0f)
				if length == 2 || length == 3 {
					identity.MNCLength = length
				}
			}
		}
	}
	if _, selectErr := selectApplication(ctx, card, aid); selectErr == nil {
		identity.SPN = readSPN(ctx, card)
	}
	if _, selectErr := selectApplication(ctx, card, aid); selectErr == nil {
		identity.SMSC = readSMSC(ctx, card)
	}
	return identity, nil
}

func readICCID(ctx context.Context, card Card) (string, error) {
	if err := selectMF(ctx, card); err != nil {
		return "", err
	}
	if err := selectFile(ctx, card, []byte{0x2F, 0xE2}); err != nil {
		return "", fmt.Errorf("pcsc: select EF_ICCID: %w", err)
	}
	data, err := readBinary(ctx, card, 10)
	if err != nil {
		return "", fmt.Errorf("pcsc: read EF_ICCID: %w", err)
	}
	value := decodeSwappedBCD(data, false)
	if len(value) < 18 || len(value) > 22 {
		return "", errors.New("pcsc: card returned an invalid ICCID")
	}
	return value, nil
}

func selectUSIM(ctx context.Context, card Card) ([]byte, error) {
	if err := selectMF(ctx, card); err != nil {
		return nil, err
	}
	if err := selectFile(ctx, card, []byte{0x2F, 0x00}); err != nil {
		return nil, fmt.Errorf("pcsc: select EF_DIR: %w", err)
	}
	var usimAID []byte
	for record := 1; record <= 32; record++ {
		data, sw, err := card.Transmit(ctx, []byte{0x00, 0xB2, byte(record), 0x04, 0x00})
		if err != nil {
			return nil, err
		}
		if sw == 0x6A83 || sw == 0x9402 {
			break
		}
		if sw != 0x9000 {
			continue
		}
		aid := findTLV(data, 0x4F)
		if len(aid) == 0 {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(hex.EncodeToString(aid)), usimAIDPrefix) {
			usimAID = append([]byte(nil), aid...)
			break
		}
	}
	if len(usimAID) == 0 {
		return nil, ErrUSIMUnavailable
	}
	if _, err := selectApplication(ctx, card, usimAID); err != nil {
		return nil, err
	}
	return usimAID, nil
}

func selectMF(ctx context.Context, card Card) error {
	_, sw, err := card.Transmit(ctx, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00, 0x00})
	return requireStatus("select MF", sw, err)
}

func selectFile(ctx context.Context, card Card, fileID []byte) error {
	if len(fileID) != 2 {
		return errors.New("pcsc: invalid file identifier")
	}
	apdu := []byte{0x00, 0xA4, 0x00, 0x04, 0x02, fileID[0], fileID[1], 0x00}
	_, sw, err := card.Transmit(ctx, apdu)
	return requireStatus("select file", sw, err)
}

func selectApplication(ctx context.Context, card Card, aid []byte) ([]byte, error) {
	if len(aid) == 0 || len(aid) > 32 {
		return nil, errors.New("pcsc: invalid USIM AID")
	}
	apdu := []byte{0x00, 0xA4, 0x04, 0x04, byte(len(aid))}
	apdu = append(apdu, aid...)
	apdu = append(apdu, 0x00)
	data, sw, err := card.Transmit(ctx, apdu)
	if err := requireStatus("select USIM application", sw, err); err != nil {
		return nil, err
	}
	return data, nil
}

func readBinary(ctx context.Context, card Card, length int) ([]byte, error) {
	if length <= 0 || length > 256 {
		return nil, errors.New("pcsc: invalid binary read length")
	}
	le := byte(length)
	if length == 256 {
		le = 0
	}
	data, sw, err := card.Transmit(ctx, []byte{0x00, 0xB0, 0x00, 0x00, le})
	if err := requireStatus("read binary", sw, err); err != nil {
		return nil, err
	}
	return data, nil
}

func verifyPIN(ctx context.Context, card Card, pin string) error {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return nil
	}
	if len(pin) < 4 || len(pin) > 8 || !decimalDigits(pin) {
		return errors.New("pcsc: SIM PIN must contain 4 to 8 digits")
	}
	_, sw, err := card.Transmit(ctx, []byte{0x00, 0x20, 0x00, 0x01, 0x00})
	if err != nil {
		return errors.New("pcsc: SIM PIN status check failed")
	}
	if sw == 0x9000 {
		return nil
	}
	tries := -1
	if sw&0xFFF0 == 0x63C0 {
		tries = int(sw & 0x000F)
		if tries <= 2 {
			return &PINError{Kind: ErrPINTriesLow, Tries: tries}
		}
	}
	body := bytes.Repeat([]byte{0xFF}, 8)
	copy(body, []byte(pin))
	apdu := append([]byte{0x00, 0x20, 0x00, 0x01, 0x08}, body...)
	_, sw, err = card.Transmit(ctx, apdu)
	if err != nil {
		return errors.New("pcsc: SIM PIN verification transport failed")
	}
	if sw == 0x9000 {
		return nil
	}
	if sw&0xFFF0 == 0x63C0 {
		return &PINError{Kind: ErrPINRejected, Tries: int(sw & 0x000F)}
	}
	return ErrPINRejected
}

func requireStatus(operation string, sw uint16, err error) error {
	if err != nil {
		return fmt.Errorf("pcsc: %s transport failed", operation)
	}
	if sw == 0x9000 {
		return nil
	}
	if sw == 0x6982 || sw == 0x9804 {
		return &PINError{Kind: ErrPINRequired, Tries: -1}
	}
	return fmt.Errorf("pcsc: %s failed with status %04X", operation, sw)
}

func decodeSwappedBCD(value []byte, dropFirstNibble bool) string {
	var result strings.Builder
	for _, octet := range value {
		for _, nibble := range []byte{octet & 0x0F, octet >> 4} {
			if dropFirstNibble {
				dropFirstNibble = false
				continue
			}
			if nibble == 0x0F {
				return result.String()
			}
			if nibble > 9 {
				return ""
			}
			result.WriteByte('0' + nibble)
		}
	}
	return result.String()
}

func decodeIMSI(data []byte) (string, error) {
	if len(data) < 2 {
		return "", errors.New("pcsc: EF_IMSI is too short")
	}
	length := int(data[0])
	if length <= 0 || length > len(data)-1 {
		return "", errors.New("pcsc: EF_IMSI has an invalid length")
	}
	value := decodeSwappedBCD(data[1:1+length], true)
	if len(value) < 10 || len(value) > 18 || !decimalDigits(value) {
		return "", errors.New("pcsc: card returned an invalid IMSI")
	}
	return value, nil
}

func decimalDigits(value string) bool {
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

func findTLV(data []byte, wanted byte) []byte {
	for len(data) >= 2 {
		tag := data[0]
		data = data[1:]
		length, consumed, ok := decodeTLVLength(data)
		if !ok || consumed+length > len(data) {
			return nil
		}
		value := data[consumed : consumed+length]
		if tag == wanted {
			return append([]byte(nil), value...)
		}
		if tag&0x20 != 0 {
			if nested := findTLV(value, wanted); len(nested) > 0 {
				return nested
			}
		}
		data = data[consumed+length:]
	}
	return nil
}

func decodeTLVLength(data []byte) (length, consumed int, ok bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	if data[0]&0x80 == 0 {
		return int(data[0]), 1, true
	}
	count := int(data[0] & 0x7F)
	if count < 1 || count > 2 || len(data) < 1+count {
		return 0, 0, false
	}
	length = 0
	for _, octet := range data[1 : 1+count] {
		length = length<<8 | int(octet)
	}
	return length, 1 + count, true
}

func parseAKAResponse(data []byte) (AKAResult, error) {
	if len(data) < 2 {
		return AKAResult{}, errors.New("pcsc: USIM returned a short AKA response")
	}
	switch data[0] {
	case 0xDB:
		res, rest, ok := takeLV(data[1:])
		if !ok || len(res) < 4 || len(res) > 16 {
			return AKAResult{}, errors.New("pcsc: USIM returned an invalid AKA RES")
		}
		ck, rest, ok := takeLV(rest)
		if !ok || len(ck) != 16 {
			return AKAResult{}, errors.New("pcsc: USIM returned an invalid AKA CK")
		}
		ik, rest, ok := takeLV(rest)
		if !ok || len(ik) != 16 {
			return AKAResult{}, errors.New("pcsc: USIM returned an invalid AKA IK")
		}
		if len(rest) > 0 {
			kc, tail, valid := takeLV(rest)
			if !valid || len(kc) != 8 || len(tail) != 0 {
				return AKAResult{}, errors.New("pcsc: USIM returned invalid trailing AKA material")
			}
		}
		return AKAResult{RES: append([]byte(nil), res...), CK: append([]byte(nil), ck...), IK: append([]byte(nil), ik...)}, nil
	case 0xDC:
		auts, tail, ok := takeLV(data[1:])
		if !ok || len(auts) != 14 || len(tail) != 0 {
			return AKAResult{}, errors.New("pcsc: USIM returned invalid AKA synchronization evidence")
		}
		return AKAResult{AUTS: append([]byte(nil), auts...), SynchronizationFailure: true}, nil
	default:
		return AKAResult{}, errors.New("pcsc: USIM returned an unsupported AKA response")
	}
}

func takeLV(data []byte) (value, rest []byte, ok bool) {
	if len(data) == 0 || int(data[0]) > len(data)-1 {
		return nil, data, false
	}
	length := int(data[0])
	return data[1 : 1+length], data[1+length:], true
}

func readSPN(ctx context.Context, card Card) string {
	if err := selectFile(ctx, card, []byte{0x6F, 0x46}); err != nil {
		return ""
	}
	data, err := readBinary(ctx, card, 17)
	if err != nil || len(data) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(data[1:]), "\x00\xFF"))
}

func readSMSC(ctx context.Context, card Card) string {
	if err := selectFile(ctx, card, []byte{0x6F, 0x42}); err != nil {
		return ""
	}
	data, sw, err := card.Transmit(ctx, []byte{0x00, 0xB2, 0x01, 0x04, 0x00})
	if err != nil || sw != 0x9000 || len(data) < 15 {
		return ""
	}
	sca := data[len(data)-15 : len(data)-3]
	if len(sca) < 2 || sca[0] < 2 || int(sca[0]) > len(sca)-1 {
		return ""
	}
	digits := decodeSwappedBCD(sca[2:1+int(sca[0])], false)
	if !decimalDigits(digits) {
		return ""
	}
	if sca[1]&0x70 == 0x10 {
		return "+" + digits
	}
	return digits
}
