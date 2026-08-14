package pcsc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// pcsc-lite exposes a small, versioned protocol over its local Unix socket.
// Speaking that protocol directly keeps VoCat's Linux binaries fully static;
// loading libpcsclite through dlopen would pull a glibc interpreter into an
// otherwise CGO-free build and make it unusable on musl-based routers.
const (
	pcscProtocolMajor        = 4
	pcscProtocolCurrentMinor = 6
	pcscProtocolOldestMinor  = 4

	pcscCmdEstablishContext = 0x01
	pcscCmdReleaseContext   = 0x02
	pcscCmdConnect          = 0x04
	pcscCmdDisconnect       = 0x06
	pcscCmdBeginTransaction = 0x07
	pcscCmdEndTransaction   = 0x08
	pcscCmdTransmit         = 0x09
	pcscCmdGetAttrib        = 0x0f
	pcscCmdVersion          = 0x11
	pcscCmdGetReadersState  = 0x12

	pcscScopeSystem      = 0x0002
	pcscProtocolT0       = 0x0001
	pcscProtocolT1       = 0x0002
	pcscProtocolAny      = pcscProtocolT0 | pcscProtocolT1
	pcscShareShared      = 0x0002
	pcscShareDirect      = 0x0003
	pcscLeaveCard        = 0x0000
	pcscResetCard        = 0x0001
	pcscCardPresent      = 0x0004
	pcscAttrChannelID    = 0x00020110
	pcscMaxReaderName    = 128
	pcscMaxATR           = 33
	pcscMaxReaders       = 16
	pcscReaderStateSize  = 184
	pcscGetSetBodySize   = 280
	pcscMaxAttribute     = 264
	pcscMaxAPDUResponse  = 65548
	pcscDefaultIOTimeout = 30 * time.Second
	pcscSuccess          = uint32(0)
	pcscNoSmartcard      = uint32(0x8010000c)
	pcscNoService        = uint32(0x8010001d)
	pcscServiceStopped   = uint32(0x8010001e)
	pcscNoReaders        = uint32(0x8010002e)
)

type pcscdClient struct {
	conn        net.Conn
	contextID   uint32
	serverMinor int32
}

type pcscdReaderState struct {
	name     string
	state    uint32
	atr      []byte
	protocol uint32
}

func establishPCSCD(ctx context.Context, conn net.Conn) (*pcscdClient, error) {
	client := &pcscdClient{conn: conn}
	version := make([]byte, 12)
	binary.LittleEndian.PutUint32(version[0:4], pcscProtocolMajor)
	binary.LittleEndian.PutUint32(version[4:8], pcscProtocolCurrentMinor)
	for {
		if err := client.exchange(ctx, pcscCmdVersion, version); err != nil {
			return nil, err
		}
		major := int32(binary.LittleEndian.Uint32(version[0:4]))
		client.serverMinor = int32(binary.LittleEndian.Uint32(version[4:8]))
		rv := binary.LittleEndian.Uint32(version[8:12])
		if rv == pcscSuccess {
			break
		}
		if rv != pcscServiceStopped || major != pcscProtocolMajor || client.serverMinor < pcscProtocolOldestMinor || client.serverMinor >= pcscProtocolCurrentMinor {
			return nil, pcscError("negotiate protocol", rv)
		}
		// pcsc-lite answers a newer client's first probe with its own
		// compatible minor version. Retry on the same connection with that
		// value, matching libpcsclite's official fallback behavior.
		binary.LittleEndian.PutUint32(version[0:4], pcscProtocolMajor)
		binary.LittleEndian.PutUint32(version[4:8], uint32(client.serverMinor))
		binary.LittleEndian.PutUint32(version[8:12], pcscSuccess)
	}
	if client.serverMinor < pcscProtocolOldestMinor {
		return nil, fmt.Errorf("pcsc: unsupported pcscd protocol %d.%d", pcscProtocolMajor, client.serverMinor)
	}
	body := make([]byte, 12)
	binary.LittleEndian.PutUint32(body[0:4], pcscScopeSystem)
	if err := client.exchange(ctx, pcscCmdEstablishContext, body); err != nil {
		return nil, err
	}
	if rv := binary.LittleEndian.Uint32(body[8:12]); rv != pcscSuccess {
		return nil, pcscError("establish context", rv)
	}
	client.contextID = binary.LittleEndian.Uint32(body[4:8])
	return client, nil
}

func (client *pcscdClient) exchange(ctx context.Context, command uint32, body []byte) error {
	if client == nil || client.conn == nil {
		return errors.New("pcsc: pcscd connection is closed")
	}
	if err := client.setDeadline(ctx); err != nil {
		return err
	}
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(body)))
	binary.LittleEndian.PutUint32(header[4:8], command)
	if err := writeAll(client.conn, header); err != nil {
		return fmt.Errorf("pcsc: send command %02x: %w", command, err)
	}
	if len(body) > 0 {
		if err := writeAll(client.conn, body); err != nil {
			return fmt.Errorf("pcsc: send command body %02x: %w", command, err)
		}
		if _, err := io.ReadFull(client.conn, body); err != nil {
			return fmt.Errorf("pcsc: receive command %02x: %w", command, err)
		}
	}
	return nil
}

func (client *pcscdClient) send(ctx context.Context, command uint32, body, extra []byte) error {
	if client == nil || client.conn == nil {
		return errors.New("pcsc: pcscd connection is closed")
	}
	if err := client.setDeadline(ctx); err != nil {
		return err
	}
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(body)))
	binary.LittleEndian.PutUint32(header[4:8], command)
	if err := writeAll(client.conn, header); err != nil {
		return err
	}
	if err := writeAll(client.conn, body); err != nil {
		return err
	}
	return writeAll(client.conn, extra)
}

func (client *pcscdClient) setDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(pcscDefaultIOTimeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	return client.conn.SetDeadline(deadline)
}

func (client *pcscdClient) readers(ctx context.Context) ([]pcscdReaderState, error) {
	if err := client.setDeadline(ctx); err != nil {
		return nil, err
	}
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[4:8], pcscCmdGetReadersState)
	if err := writeAll(client.conn, header); err != nil {
		return nil, fmt.Errorf("pcsc: request reader states: %w", err)
	}
	raw := make([]byte, pcscMaxReaders*pcscReaderStateSize)
	if _, err := io.ReadFull(client.conn, raw); err != nil {
		return nil, fmt.Errorf("pcsc: read reader states: %w", err)
	}
	result := make([]pcscdReaderState, 0, pcscMaxReaders)
	for offset := 0; offset < len(raw); offset += pcscReaderStateSize {
		state := raw[offset : offset+pcscReaderStateSize]
		name := cString(state[:pcscMaxReaderName])
		if name == "" {
			continue
		}
		atrLen := int(binary.LittleEndian.Uint32(state[176:180]))
		if atrLen < 0 || atrLen > pcscMaxATR {
			atrLen = 0
		}
		result = append(result, pcscdReaderState{
			name:     name,
			state:    binary.LittleEndian.Uint32(state[132:136]),
			atr:      append([]byte(nil), state[140:140+atrLen]...),
			protocol: binary.LittleEndian.Uint32(state[180:184]),
		})
	}
	return result, nil
}

func (client *pcscdClient) connect(ctx context.Context, reader string, share, protocols uint32) (int32, uint32, error) {
	if len(reader) >= pcscMaxReaderName {
		return 0, 0, errors.New("pcsc: reader name is too long")
	}
	body := make([]byte, 152)
	binary.LittleEndian.PutUint32(body[0:4], client.contextID)
	copy(body[4:132], reader)
	binary.LittleEndian.PutUint32(body[132:136], share)
	binary.LittleEndian.PutUint32(body[136:140], protocols)
	if err := client.exchange(ctx, pcscCmdConnect, body); err != nil {
		return 0, 0, err
	}
	if rv := binary.LittleEndian.Uint32(body[148:152]); rv != pcscSuccess {
		return 0, 0, pcscError("connect reader", rv)
	}
	return int32(binary.LittleEndian.Uint32(body[140:144])), binary.LittleEndian.Uint32(body[144:148]), nil
}

func (client *pcscdClient) simpleCardCommand(ctx context.Context, command uint32, card int32, disposition *uint32) error {
	size := 8
	if disposition != nil {
		size = 12
	}
	body := make([]byte, size)
	binary.LittleEndian.PutUint32(body[0:4], uint32(card))
	if disposition != nil {
		binary.LittleEndian.PutUint32(body[4:8], *disposition)
	}
	if err := client.exchange(ctx, command, body); err != nil {
		return err
	}
	if rv := binary.LittleEndian.Uint32(body[size-4:]); rv != pcscSuccess {
		return pcscError("card command", rv)
	}
	return nil
}

func (client *pcscdClient) transmit(ctx context.Context, card int32, protocol uint32, command []byte) ([]byte, error) {
	body := make([]byte, 32)
	binary.LittleEndian.PutUint32(body[0:4], uint32(card))
	binary.LittleEndian.PutUint32(body[4:8], protocol)
	binary.LittleEndian.PutUint32(body[8:12], 8)
	binary.LittleEndian.PutUint32(body[12:16], uint32(len(command)))
	binary.LittleEndian.PutUint32(body[16:20], pcscProtocolAny)
	binary.LittleEndian.PutUint32(body[20:24], 8)
	binary.LittleEndian.PutUint32(body[24:28], pcscMaxAPDUResponse)
	if err := client.send(ctx, pcscCmdTransmit, body, command); err != nil {
		return nil, fmt.Errorf("pcsc: transmit APDU: %w", err)
	}
	if _, err := io.ReadFull(client.conn, body); err != nil {
		return nil, fmt.Errorf("pcsc: receive APDU result: %w", err)
	}
	if rv := binary.LittleEndian.Uint32(body[28:32]); rv != pcscSuccess {
		return nil, pcscError("transmit APDU", rv)
	}
	length := binary.LittleEndian.Uint32(body[24:28])
	if length > pcscMaxAPDUResponse {
		return nil, errors.New("pcsc: pcscd returned an oversized APDU")
	}
	response := make([]byte, length)
	if _, err := io.ReadFull(client.conn, response); err != nil {
		return nil, fmt.Errorf("pcsc: receive APDU: %w", err)
	}
	return response, nil
}

func (client *pcscdClient) getAttrib(ctx context.Context, card int32, attribute uint32) ([]byte, error) {
	body := make([]byte, pcscGetSetBodySize)
	binary.LittleEndian.PutUint32(body[0:4], uint32(card))
	binary.LittleEndian.PutUint32(body[4:8], attribute)
	binary.LittleEndian.PutUint32(body[272:276], pcscMaxAttribute)
	if err := client.exchange(ctx, pcscCmdGetAttrib, body); err != nil {
		return nil, err
	}
	if rv := binary.LittleEndian.Uint32(body[276:280]); rv != pcscSuccess {
		return nil, pcscError("get reader attribute", rv)
	}
	length := binary.LittleEndian.Uint32(body[272:276])
	if length > pcscMaxAttribute {
		return nil, errors.New("pcsc: pcscd returned an oversized attribute")
	}
	return append([]byte(nil), body[8:8+length]...), nil
}

func (client *pcscdClient) closeContext(ctx context.Context) error {
	if client == nil || client.conn == nil {
		return nil
	}
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body[0:4], client.contextID)
	err := client.exchange(ctx, pcscCmdReleaseContext, body)
	if err == nil {
		if rv := binary.LittleEndian.Uint32(body[4:8]); rv != pcscSuccess {
			err = pcscError("release context", rv)
		}
	}
	closeErr := client.conn.Close()
	client.conn = nil
	return errors.Join(err, closeErr)
}

func pcscError(operation string, code uint32) error {
	switch code {
	case pcscNoSmartcard:
		return ErrNoCard
	case pcscNoService, pcscServiceStopped:
		return fmt.Errorf("%w: %s failed with PC/SC status %08X", ErrUnavailable, operation, code)
	case pcscNoReaders:
		return ErrReaderNotFound
	default:
		return fmt.Errorf("pcsc: %s failed with status %08X", operation, code)
	}
}

func cString(value []byte) string {
	for index, current := range value {
		if current == 0 {
			return string(value[:index])
		}
	}
	return string(value)
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
		value = value[written:]
	}
	return nil
}
