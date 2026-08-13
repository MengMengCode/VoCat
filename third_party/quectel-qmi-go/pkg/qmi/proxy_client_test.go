package qmi

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestClientProxyOpenRunsBeforeInitialSync(t *testing.T) {
	const devicePath = "/dev/cdc-wdm-test0"

	errCh := withProxyTransportForTest(t, func(conn net.Conn) error {
		defer conn.Close()

		openReq, err := readQMIFrameFromConn(conn)
		if err != nil {
			return err
		}
		if err := assertCTLRequest(openReq, CTLInternalProxyOpen); err != nil {
			return err
		}
		tlv := FindTLV(openReq.TLVs, TLVProxyDevicePath)
		if tlv == nil {
			return fmt.Errorf("proxy open request missing device path TLV")
		}
		if got := string(tlv.Value); got != devicePath {
			return fmt.Errorf("proxy open device path = %q, want %q", got, devicePath)
		}
		if err := writeCTLSuccess(conn, openReq); err != nil {
			return err
		}

		syncReq, err := readQMIFrameFromConn(conn)
		if err != nil {
			return err
		}
		if err := assertCTLRequest(syncReq, 0x0027); err != nil {
			return err
		}
		return writeCTLSuccess(conn, syncReq)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := NewClientWithOptions(ctx, devicePath, ClientOptions{UseProxy: true})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestClientProxyAllocateClientIDAfterOpen(t *testing.T) {
	const devicePath = "/dev/cdc-wdm-test1"
	const clientID = 0x23

	errCh := withProxyTransportForTest(t, func(conn net.Conn) error {
		defer conn.Close()

		openReq, err := readQMIFrameFromConn(conn)
		if err != nil {
			return err
		}
		if err := assertCTLRequest(openReq, CTLInternalProxyOpen); err != nil {
			return err
		}
		if err := writeCTLSuccess(conn, openReq); err != nil {
			return err
		}

		allocReq, err := readQMIFrameFromConn(conn)
		if err != nil {
			return err
		}
		if err := assertCTLRequest(allocReq, CTLGetClientID); err != nil {
			return err
		}
		tlv := FindTLV(allocReq.TLVs, 0x01)
		if tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != ServiceDMS {
			return fmt.Errorf("allocate client ID TLV = %#v, want service DMS", tlv)
		}
		return writeCTLResponse(conn, allocReq, []TLV{
			successTLV(),
			{Type: 0x01, Value: []byte{ServiceDMS, clientID}},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := NewClientWithOptions(ctx, devicePath, ClientOptions{
		UseProxy:     true,
		SyncOnOpen:   false,
		ReadDeadline: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	defer client.Close()

	got, err := client.AllocateClientIDWithContext(ctx, ServiceDMS)
	if err != nil {
		t.Fatalf("AllocateClientIDWithContext() error = %v", err)
	}
	if got != clientID {
		t.Fatalf("client ID = 0x%02x, want 0x%02x", got, clientID)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestClientFallsBackToRawWhenProxyTransportOpenFails(t *testing.T) {
	const devicePath = "/dev/cdc-wdm-fallback0"

	proxyAttempts := 0
	rawAttempts := 0
	restoreProxy := replaceProxyTransportForTest(t, func(context.Context, ClientOptions) (qmiTransport, error) {
		proxyAttempts++
		return nil, errors.New("proxy unavailable")
	})
	defer restoreProxy()
	restoreRaw := replaceRawTransportForTest(t, func(path string) (qmiTransport, error) {
		rawAttempts++
		if path != devicePath {
			t.Fatalf("raw path=%q, want %q", path, devicePath)
		}
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() {
			serverConn.Close()
		})
		return clientConn, nil
	})
	defer restoreRaw()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := NewClientWithOptions(ctx, devicePath, ClientOptions{
		UseProxy:           true,
		ProxyFallbackToRaw: true,
		SyncOnOpen:         false,
		ReadDeadline:       5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	defer client.Close()

	if proxyAttempts != 1 {
		t.Fatalf("proxy attempts=%d, want 1", proxyAttempts)
	}
	if rawAttempts != 1 {
		t.Fatalf("raw attempts=%d, want 1", rawAttempts)
	}
	if client.opts.UseProxy {
		t.Fatal("client.opts.UseProxy=true after fallback, want false")
	}
}

func TestClientFallsBackToRawWhenProxyDeviceOpenFails(t *testing.T) {
	const devicePath = "/dev/cdc-wdm-fallback1"

	proxyAttempts := 0
	rawAttempts := 0
	restoreProxy := replaceProxyTransportForTest(t, func(context.Context, ClientOptions) (qmiTransport, error) {
		proxyAttempts++
		clientConn, serverConn := net.Pipe()
		go func() {
			_ = serverConn.Close()
		}()
		return clientConn, nil
	})
	defer restoreProxy()
	restoreRaw := replaceRawTransportForTest(t, func(path string) (qmiTransport, error) {
		rawAttempts++
		if path != devicePath {
			t.Fatalf("raw path=%q, want %q", path, devicePath)
		}
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() {
			serverConn.Close()
		})
		return clientConn, nil
	})
	defer restoreRaw()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := NewClientWithOptions(ctx, devicePath, ClientOptions{
		UseProxy:           true,
		ProxyFallbackToRaw: true,
		SyncOnOpen:         false,
		ReadDeadline:       5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	defer client.Close()

	if proxyAttempts != 1 {
		t.Fatalf("proxy attempts=%d, want 1", proxyAttempts)
	}
	if rawAttempts != 1 {
		t.Fatalf("raw attempts=%d, want 1", rawAttempts)
	}
	if client.opts.UseProxy {
		t.Fatal("client.opts.UseProxy=true after fallback, want false")
	}
}

func withProxyTransportForTest(t *testing.T, server func(net.Conn) error) <-chan error {
	t.Helper()

	errCh := make(chan error, 1)
	restore := replaceProxyTransportForTest(t, func(ctx context.Context, opts ClientOptions) (qmiTransport, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			errCh <- server(serverConn)
		}()
		return clientConn, nil
	})
	t.Cleanup(func() {
		restore()
	})
	return errCh
}

func replaceProxyTransportForTest(t *testing.T, hook func(context.Context, ClientOptions) (qmiTransport, error)) func() {
	t.Helper()

	old := openProxyTransportHook
	openProxyTransportHook = hook
	restored := false
	return func() {
		if restored {
			return
		}
		openProxyTransportHook = old
		restored = true
	}
}

func replaceRawTransportForTest(t *testing.T, hook func(string) (qmiTransport, error)) func() {
	t.Helper()

	old := openRawTransportHook
	openRawTransportHook = hook
	restored := false
	return func() {
		if restored {
			return
		}
		openRawTransportHook = old
		restored = true
	}
}

func readQMIFrameFromConn(conn net.Conn) (*Packet, error) {
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return nil, err
	}

	header := make([]byte, 3)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	frameLen := 1 + int(binary.LittleEndian.Uint16(header[1:3]))
	if frameLen < len(header) {
		return nil, fmt.Errorf("invalid QMUX frame length %d", frameLen)
	}

	frame := make([]byte, frameLen)
	copy(frame, header)
	if _, err := io.ReadFull(conn, frame[len(header):]); err != nil {
		return nil, err
	}
	return UnmarshalPacket(frame)
}

func assertCTLRequest(p *Packet, msgID uint16) error {
	if p.ServiceType != ServiceControl {
		return fmt.Errorf("service = 0x%02x, want CTL", p.ServiceType)
	}
	if p.MessageID != msgID {
		return fmt.Errorf("message ID = 0x%04x, want 0x%04x", p.MessageID, msgID)
	}
	return nil
}

func writeCTLSuccess(conn net.Conn, req *Packet) error {
	return writeCTLResponse(conn, req, []TLV{successTLV()})
}

func writeCTLResponse(conn net.Conn, req *Packet, tlvs []TLV) error {
	var tlvBytes []byte
	for _, tlv := range tlvs {
		tlvBytes = append(tlvBytes, tlv.Marshal()...)
	}

	ctlHeader := CTLHeader{
		ControlFlags:  0x01,
		TransactionID: uint8(req.TransactionID),
		MessageID:     req.MessageID,
		Length:        uint16(len(tlvBytes)),
	}
	body := ctlHeader.Marshal()
	body = append(body, tlvBytes...)

	qmuxHeader := QmuxHeader{
		IFType:       0x01,
		Length:       uint16(len(body) + 5),
		ControlFlags: 0x00,
		ServiceType:  req.ServiceType,
		ClientID:     req.ClientID,
	}
	frame := qmuxHeader.Marshal()
	frame = append(frame, body...)

	_, err := conn.Write(frame)
	return err
}

func successTLV() TLV {
	return TLV{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}}
}
