//go:build linux && (amd64 || arm64)

package pcsc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ElMostafaIdrassi/goscard"
)

type nativeBackend struct {
	initializeOnce sync.Once
	initializeErr  error
}

func newNativeBackend() Backend { return &nativeBackend{} }

func (backend *nativeBackend) initialize() error {
	backend.initializeOnce.Do(func() {
		if err := goscard.Initialize(goscard.NewDefaultLogger(goscard.LogLevelNone)); err != nil {
			backend.initializeErr = fmt.Errorf("%w: pcsc-lite client library could not be loaded", ErrUnavailable)
		}
	})
	return backend.initializeErr
}

func (backend *nativeBackend) Readers(ctx context.Context) ([]Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := backend.initialize(); err != nil {
		return nil, err
	}
	cardContext, _, err := goscard.NewContext(goscard.SCardScopeSystem, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: pcscd is not reachable", ErrUnavailable)
	}
	defer cardContext.Release()
	names, _, err := cardContext.ListReaders(nil)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no readers") {
			return []Reader{}, nil
		}
		return nil, fmt.Errorf("pcsc: list readers: %w", err)
	}
	presentNames, atrs, _, _ := cardContext.ListReadersWithCardPresent(nil)
	present := make(map[string]string, len(presentNames))
	for index, name := range presentNames {
		atr := ""
		if index < len(atrs) {
			atr = atrs[index]
		}
		present[name] = atr
	}
	readers := make([]Reader, 0, len(names))
	for _, name := range names {
		reader := Reader{Name: name}
		reader.ATR, reader.CardPresent = present[name]
		if path, ok := backend.readerUSBPath(cardContext, name); ok {
			reader.USBPath = path
			reader.VendorID = readSysfsText(path, "idVendor")
			reader.ProductID = readSysfsText(path, "idProduct")
			reader.Manufacturer = readSysfsText(path, "manufacturer")
			reader.Product = readSysfsText(path, "product")
		} else {
			reader.USBPath = "pcsc:" + name
		}
		if reader.Product == "" {
			reader.Product = strings.TrimSpace(strings.TrimSuffix(name, " 00 00"))
		}
		readers = append(readers, reader)
	}
	return readers, nil
}

func (backend *nativeBackend) readerUSBPath(cardContext goscard.Context, name string) (string, bool) {
	card, _, err := cardContext.Connect(name, goscard.SCardShareDirect, goscard.SCardProtocolT0|goscard.SCardProtocolT1)
	if err != nil {
		return "", false
	}
	defer card.Disconnect(goscard.SCardLeaveCard)
	attribute, _, err := card.GetAttrib(goscard.SCardAttrChannelID)
	if err != nil || len(attribute) < 4 {
		return "", false
	}
	channel := binary.LittleEndian.Uint32(attribute[:4])
	if channel>>16 != 0x0020 {
		return "", false
	}
	bus, device := int((channel>>8)&0xFF), int(channel&0xFF)
	entries, err := os.ReadDir("/sys/bus/usb/devices")
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		path := filepath.Join("/sys/bus/usb/devices", entry.Name())
		entryBus, busErr := readSysfsInt(path, "busnum")
		entryDevice, deviceErr := readSysfsInt(path, "devnum")
		if busErr == nil && deviceErr == nil && entryBus == bus && entryDevice == device {
			return entry.Name(), true
		}
	}
	return "", false
}

func (backend *nativeBackend) Open(ctx context.Context, selector Selector) (Card, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	readers, err := backend.Readers(ctx)
	if err != nil {
		return nil, err
	}
	reader, ok := matchReader(readers, selector)
	if !ok {
		return nil, ErrReaderNotFound
	}
	if !reader.CardPresent {
		return nil, ErrNoCard
	}
	cardContext, _, err := goscard.NewContext(goscard.SCardScopeSystem, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create context", ErrUnavailable)
	}
	card, _, err := cardContext.Connect(reader.Name, goscard.SCardShareShared, goscard.SCardProtocolT0|goscard.SCardProtocolT1)
	if err != nil {
		cardContext.Release()
		return nil, fmt.Errorf("pcsc: connect reader: %w", err)
	}
	if _, err := card.BeginTransaction(); err != nil {
		card.Disconnect(goscard.SCardLeaveCard)
		cardContext.Release()
		return nil, fmt.Errorf("pcsc: begin card transaction: %w", err)
	}
	return &nativeCard{context: &cardContext, card: &card}, nil
}

type nativeCard struct {
	context *goscard.Context
	card    *goscard.Card
	closed  bool
}

func (card *nativeCard) Transmit(ctx context.Context, command []byte) ([]byte, uint16, error) {
	if card == nil || card.card == nil || card.closed {
		return nil, 0, errors.New("pcsc: card session is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return card.transmit(ctx, append([]byte(nil), command...), 0)
}

// TransmitRaw performs exactly one APDU exchange. Stateful eUICC callers need
// to observe 61xx themselves because GET RESPONSE must target their logical
// channel rather than the basic channel.
func (card *nativeCard) TransmitRaw(ctx context.Context, command []byte) ([]byte, uint16, error) {
	if card == nil || card.card == nil || card.closed {
		return nil, 0, errors.New("pcsc: card session is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	pci := goscard.SCardIoRequestT0
	if card.card.ActiveProtocol() == goscard.SCardProtocolT1 {
		pci = goscard.SCardIoRequestT1
	}
	response, _, err := card.card.Transmit(&pci, append([]byte(nil), command...), nil)
	if err != nil {
		return nil, 0, err
	}
	if len(response) < 2 {
		return nil, 0, errors.New("pcsc: APDU response omitted its status word")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	last := len(response) - 2
	return append([]byte(nil), response[:last]...), uint16(response[last])<<8 | uint16(response[last+1]), nil
}

func (card *nativeCard) transmit(ctx context.Context, command []byte, depth int) ([]byte, uint16, error) {
	if depth > 8 {
		return nil, 0, errors.New("pcsc: too many APDU continuations")
	}
	pci := goscard.SCardIoRequestT0
	if card.card.ActiveProtocol() == goscard.SCardProtocolT1 {
		pci = goscard.SCardIoRequestT1
	}
	response, _, err := card.card.Transmit(&pci, command, nil)
	if err != nil {
		return nil, 0, err
	}
	if len(response) < 2 {
		return nil, 0, errors.New("pcsc: APDU response omitted its status word")
	}
	data := append([]byte(nil), response[:len(response)-2]...)
	sw1, sw2 := response[len(response)-2], response[len(response)-1]
	if sw1 == 0x6C && len(command) >= 5 {
		retry := append([]byte(nil), command...)
		retry[len(retry)-1] = sw2
		return card.transmit(ctx, retry, depth+1)
	}
	if sw1 == 0x61 || sw1 == 0x9F {
		more, sw, err := card.transmit(ctx, []byte{0x00, 0xC0, 0x00, 0x00, sw2}, depth+1)
		if err != nil {
			return nil, 0, err
		}
		return append(data, more...), sw, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return data, uint16(sw1)<<8 | uint16(sw2), nil
}

func (card *nativeCard) Close() error {
	return card.close(goscard.SCardLeaveCard)
}

func (card *nativeCard) CloseWithReset() error {
	return card.close(goscard.SCardResetCard)
}

func (card *nativeCard) close(disposition goscard.SCardDisposition) error {
	if card == nil || card.closed {
		return nil
	}
	card.closed = true
	var result []error
	if card.card != nil {
		if _, err := card.card.EndTransaction(disposition); err != nil {
			result = append(result, err)
		}
		if _, err := card.card.Disconnect(disposition); err != nil {
			result = append(result, err)
		}
	}
	if card.context != nil {
		if _, err := card.context.Release(); err != nil {
			result = append(result, err)
		}
	}
	return errors.Join(result...)
}

func readSysfsText(usbPath, name string) string {
	value, err := os.ReadFile(filepath.Join("/sys/bus/usb/devices", usbPath, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func readSysfsInt(path, name string) (int, error) {
	value, err := os.ReadFile(filepath.Join(path, name))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(value)))
}
