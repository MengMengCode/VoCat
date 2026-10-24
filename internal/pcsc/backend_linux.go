//go:build linux && (amd64 || arm64 || arm)

package pcsc

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type nativeBackend struct{ sysRoot string }

func newNativeBackend() Backend { return &nativeBackend{sysRoot: "/sys"} }

func (backend *nativeBackend) dial(ctx context.Context) (*pcscdClient, error) {
	paths := []string{strings.TrimSpace(os.Getenv("PCSCLITE_CSOCK_NAME")), "/run/pcscd/pcscd.comm", "/var/run/pcscd/pcscd.comm"}
	var failures []error
	seen := make(map[string]bool)
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		client, err := establishPCSCD(ctx, conn)
		if err == nil {
			return client, nil
		}
		_ = conn.Close()
		failures = append(failures, err)
	}
	return nil, fmt.Errorf("%w: pcscd socket is not reachable: %w", ErrUnavailable, errors.Join(failures...))
}

func ensurePCSCDService(ctx context.Context) {
	if os.Geteuid() != 0 {
		return
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		_ = exec.CommandContext(ctx, "systemctl", "start", "pcscd.socket").Run()
		_ = exec.CommandContext(ctx, "systemctl", "start", "pcscd").Run()
	} else if _, err := os.Stat("/etc/init.d/pcscd"); err == nil {
		_ = exec.CommandContext(ctx, "/etc/init.d/pcscd", "start").Run()
	} else if path, err := exec.LookPath("pcscd"); err == nil {
		_ = exec.CommandContext(ctx, path).Start()
	}
}

func reauthorizeUSBDevice(sysRoot, usbPath string) {
	if strings.Contains(usbPath, "..") || strings.Contains(usbPath, "/") || strings.Contains(usbPath, "\\") {
		return
	}
	authPath := filepath.Join(filepath.Clean(sysRoot), "bus", "usb", "devices", usbPath, "authorized")
	if _, err := os.Stat(authPath); err != nil {
		return
	}
	_ = os.WriteFile(authPath, []byte("0\n"), 0o644)
	time.Sleep(100 * time.Millisecond)
	_ = os.WriteFile(authPath, []byte("1\n"), 0o644)
}

func (backend *nativeBackend) waitForPCSCReaders(ctx context.Context, client *pcscdClient, physical []Reader, states []pcscdReaderState) []pcscdReaderState {
	// First pass: wait up to 2 seconds for active driver negotiation.
	pollDeadline := time.Now().Add(2 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(pollDeadline) {
		pollDeadline = dl
	}
	for len(states) < len(physical) && time.Now().Before(pollDeadline) {
		select {
		case <-ctx.Done():
			return states
		case <-time.After(250 * time.Millisecond):
		}
		if updated, err := client.readers(ctx); err == nil {
			states = updated
			if len(states) >= len(physical) {
				return states
			}
		}
	}
	if len(states) >= len(physical) {
		return states
	}

	// Second pass: if readers are still missing from pcscd, trigger a USB re-authorization
	// on the physical devices in sysfs to reset any stalled CCID endpoints, then poll briefly.
	reauthorized := false
	for _, phys := range physical {
		if phys.USBPath != "" {
			reauthorizeUSBDevice(backend.sysRoot, phys.USBPath)
			reauthorized = true
		}
	}
	if !reauthorized {
		return states
	}

	retryDeadline := time.Now().Add(2 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(retryDeadline) {
		retryDeadline = dl
	}
	for len(states) < len(physical) && time.Now().Before(retryDeadline) {
		select {
		case <-ctx.Done():
			return states
		case <-time.After(300 * time.Millisecond):
		}
		if updated, err := client.readers(ctx); err == nil {
			states = updated
			if len(states) >= len(physical) {
				return states
			}
		}
	}
	return states
}

func (backend *nativeBackend) Readers(ctx context.Context) ([]Reader, error) {
	physical := discoverUSBSmartCardReaders(backend.sysRoot, "pcsc_driver_missing")
	client, err := backend.dial(ctx)
	if err != nil && len(physical) > 0 {
		ensurePCSCDService(ctx)
		dialDeadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(dialDeadline) {
			select {
			case <-ctx.Done():
				break
			case <-time.After(200 * time.Millisecond):
			}
			if c, dialErr := backend.dial(ctx); dialErr == nil {
				client, err = c, nil
				break
			}
		}
	}
	if err != nil {
		if len(physical) > 0 {
			for index := range physical {
				physical[index].DiscoveryIssue = "pcsc_service_unavailable"
			}
			return physical, nil
		}
		return nil, err
	}
	defer client.closeContext(context.Background())
	states, err := client.readers(ctx)
	if err != nil {
		return nil, err
	}
	if len(physical) > 0 && len(states) < len(physical) {
		states = backend.waitForPCSCReaders(ctx, client, physical, states)
	}
	readers := make([]Reader, 0, len(states))
	for _, state := range states {
		reader := Reader{
			Name:        state.name,
			CardPresent: state.state&pcscCardPresent != 0,
			ATR:         strings.ToUpper(hex.EncodeToString(state.atr)),
		}
		if path, ok := backend.readerUSBPath(ctx, client, state.name); ok {
			reader.USBPath = path
			reader.VendorID = backend.readSysfsText(path, "idVendor")
			reader.ProductID = backend.readSysfsText(path, "idProduct")
			reader.Manufacturer = backend.readSysfsText(path, "manufacturer")
			reader.Product = backend.readSysfsText(path, "product")
		} else {
			reader.USBPath = "pcsc:" + state.name
		}
		if reader.Product == "" {
			reader.Product = strings.TrimSpace(strings.TrimSuffix(state.name, " 00 00"))
		}
		readers = append(readers, reader)
	}
	return filterVirtualPCDReaders(mergePCSCAndUSBReaders(readers, physical)), nil
}

func (backend *nativeBackend) readerUSBPath(ctx context.Context, client *pcscdClient, name string) (string, bool) {
	card, _, err := client.connect(ctx, name, pcscShareDirect, 0)
	if err != nil {
		return "", false
	}
	disposition := uint32(pcscLeaveCard)
	defer client.simpleCardCommand(context.Background(), pcscCmdDisconnect, card, &disposition)
	attribute, err := client.getAttrib(ctx, card, pcscAttrChannelID)
	if err != nil || len(attribute) < 4 {
		return "", false
	}
	channel := binary.LittleEndian.Uint32(attribute[:4])
	if channel>>16 != 0x0020 {
		return "", false
	}
	bus, device := int((channel>>8)&0xff), int(channel&0xff)
	usbRoot := filepath.Join(backend.sysRoot, "bus", "usb", "devices")
	entries, err := os.ReadDir(usbRoot)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		path := filepath.Join(usbRoot, entry.Name())
		entryBus, busErr := readSysfsInt(path, "busnum")
		entryDevice, deviceErr := readSysfsInt(path, "devnum")
		if busErr == nil && deviceErr == nil && entryBus == bus && entryDevice == device {
			return entry.Name(), true
		}
	}
	return "", false
}

func (backend *nativeBackend) Open(ctx context.Context, selector Selector) (Card, error) {
	readers, err := backend.Readers(ctx)
	if err != nil {
		return nil, err
	}
	reader, ok := matchReader(readers, selector)
	if !ok {
		return nil, ErrReaderNotFound
	}
	if reader.DiscoveryIssue != "" {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, reader.DiscoveryIssue)
	}
	if !reader.CardPresent {
		return nil, ErrNoCard
	}
	client, err := backend.dial(ctx)
	if err != nil {
		return nil, err
	}
	handle, protocol, err := client.connect(ctx, reader.Name, pcscShareShared, pcscProtocolAny)
	if err != nil {
		_ = client.closeContext(context.Background())
		return nil, err
	}
	if err := client.simpleCardCommand(ctx, pcscCmdBeginTransaction, handle, nil); err != nil {
		disposition := uint32(pcscLeaveCard)
		_ = client.simpleCardCommand(context.Background(), pcscCmdDisconnect, handle, &disposition)
		_ = client.closeContext(context.Background())
		return nil, fmt.Errorf("pcsc: begin card transaction: %w", err)
	}
	return &nativeCard{client: client, handle: handle, protocol: protocol}, nil
}

type nativeCard struct {
	client   *pcscdClient
	handle   int32
	protocol uint32
	closed   bool
}

func (card *nativeCard) Transmit(ctx context.Context, command []byte) ([]byte, uint16, error) {
	if card == nil || card.client == nil || card.closed {
		return nil, 0, errors.New("pcsc: card session is closed")
	}
	return card.transmit(ctx, append([]byte(nil), command...), 0)
}

func (card *nativeCard) TransmitRaw(ctx context.Context, command []byte) ([]byte, uint16, error) {
	if card == nil || card.client == nil || card.closed {
		return nil, 0, errors.New("pcsc: card session is closed")
	}
	response, err := card.client.transmit(ctx, card.handle, card.protocol, append([]byte(nil), command...))
	if err != nil {
		return nil, 0, err
	}
	return splitAPDUResponse(response)
}

func (card *nativeCard) transmit(ctx context.Context, command []byte, depth int) ([]byte, uint16, error) {
	if depth > 8 {
		return nil, 0, errors.New("pcsc: too many APDU continuations")
	}
	data, status, err := card.TransmitRaw(ctx, command)
	if err != nil {
		return nil, 0, err
	}
	sw1, sw2 := byte(status>>8), byte(status)
	if sw1 == 0x6c && len(command) >= 5 {
		retry := append([]byte(nil), command...)
		retry[len(retry)-1] = sw2
		return card.transmit(ctx, retry, depth+1)
	}
	if sw1 == 0x61 || sw1 == 0x9f {
		more, sw, err := card.transmit(ctx, []byte{0x00, 0xc0, 0x00, 0x00, sw2}, depth+1)
		if err != nil {
			return nil, 0, err
		}
		return append(data, more...), sw, nil
	}
	return data, status, ctx.Err()
}

func (card *nativeCard) Close() error { return card.close(pcscLeaveCard) }

func (card *nativeCard) CloseWithReset() error { return card.close(pcscResetCard) }

func (card *nativeCard) close(disposition uint32) error {
	if card == nil || card.closed {
		return nil
	}
	card.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result []error
	if card.client != nil {
		if err := card.client.simpleCardCommand(ctx, pcscCmdEndTransaction, card.handle, &disposition); err != nil {
			result = append(result, err)
		}
		if err := card.client.simpleCardCommand(ctx, pcscCmdDisconnect, card.handle, &disposition); err != nil {
			result = append(result, err)
		}
		if err := card.client.closeContext(ctx); err != nil {
			result = append(result, err)
		}
	}
	return errors.Join(result...)
}

func (backend *nativeBackend) readSysfsText(usbPath, name string) string {
	value, err := os.ReadFile(filepath.Join(backend.sysRoot, "bus", "usb", "devices", usbPath, name))
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
