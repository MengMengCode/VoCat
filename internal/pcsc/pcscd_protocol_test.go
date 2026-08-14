package pcsc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

func TestPCSCDClientLifecycleAndTransmit(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		serverDone <- servePCSCDTestSession(serverConn)
	}()

	client, err := establishPCSCD(context.Background(), clientConn)
	if err != nil {
		t.Fatalf("establishPCSCD: %v", err)
	}
	states, err := client.readers(context.Background())
	if err != nil {
		t.Fatalf("readers: %v", err)
	}
	if len(states) != 1 || states[0].name != "VoCat Test Reader 00 00" || states[0].state&pcscCardPresent == 0 {
		t.Fatalf("states = %#v", states)
	}
	handle, protocol, err := client.connect(context.Background(), states[0].name, pcscShareShared, pcscProtocolAny)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if handle != 42 || protocol != pcscProtocolT1 {
		t.Fatalf("handle/protocol = %d/%d", handle, protocol)
	}
	if err := client.simpleCardCommand(context.Background(), pcscCmdBeginTransaction, handle, nil); err != nil {
		t.Fatalf("begin: %v", err)
	}
	response, err := client.transmit(context.Background(), handle, protocol, []byte{0x00, 0xa4, 0x00, 0x00})
	if err != nil {
		t.Fatalf("transmit: %v", err)
	}
	if !bytes.Equal(response, []byte{0x62, 0x02, 0x90, 0x00}) {
		t.Fatalf("response = %x", response)
	}
	disposition := uint32(pcscLeaveCard)
	if err := client.simpleCardCommand(context.Background(), pcscCmdEndTransaction, handle, &disposition); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := client.simpleCardCommand(context.Background(), pcscCmdDisconnect, handle, &disposition); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if err := client.closeContext(context.Background()); err != nil {
		t.Fatalf("close context: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake pcscd: %v", err)
	}
}

func servePCSCDTestSession(conn net.Conn) error {
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(conn, header); err != nil {
			return err
		}
		size := binary.LittleEndian.Uint32(header[0:4])
		command := binary.LittleEndian.Uint32(header[4:8])
		body := make([]byte, size)
		if _, err := io.ReadFull(conn, body); err != nil {
			return err
		}
		switch command {
		case pcscCmdVersion:
			binary.LittleEndian.PutUint32(body[0:4], pcscProtocolMajor)
			binary.LittleEndian.PutUint32(body[4:8], pcscProtocolCurrentMinor)
			if err := writeAll(conn, body); err != nil {
				return err
			}
		case pcscCmdEstablishContext:
			binary.LittleEndian.PutUint32(body[4:8], 7)
			if err := writeAll(conn, body); err != nil {
				return err
			}
		case pcscCmdGetReadersState:
			states := make([]byte, pcscMaxReaders*pcscReaderStateSize)
			copy(states, "VoCat Test Reader 00 00")
			binary.LittleEndian.PutUint32(states[132:136], pcscCardPresent)
			copy(states[140:143], []byte{0x3b, 0x00, 0x00})
			binary.LittleEndian.PutUint32(states[176:180], 3)
			binary.LittleEndian.PutUint32(states[180:184], pcscProtocolT1)
			if err := writeAll(conn, states); err != nil {
				return err
			}
		case pcscCmdConnect:
			binary.LittleEndian.PutUint32(body[140:144], 42)
			binary.LittleEndian.PutUint32(body[144:148], pcscProtocolT1)
			if err := writeAll(conn, body); err != nil {
				return err
			}
		case pcscCmdBeginTransaction, pcscCmdEndTransaction, pcscCmdDisconnect:
			if err := writeAll(conn, body); err != nil {
				return err
			}
		case pcscCmdTransmit:
			commandBody := make([]byte, binary.LittleEndian.Uint32(body[12:16]))
			if _, err := io.ReadFull(conn, commandBody); err != nil {
				return err
			}
			response := []byte{0x62, 0x02, 0x90, 0x00}
			binary.LittleEndian.PutUint32(body[24:28], uint32(len(response)))
			if err := writeAll(conn, body); err != nil {
				return err
			}
			if err := writeAll(conn, response); err != nil {
				return err
			}
		case pcscCmdReleaseContext:
			return writeAll(conn, body)
		default:
			return errors.New("unexpected fake pcscd command")
		}
	}
}
