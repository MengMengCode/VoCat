package proxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestProbeSOCKS5RequiresRealUDPExchange(t *testing.T) {
	address, stop := startProbeSOCKS5Server(t, false)
	defer stop()

	result, err := probeSOCKS5(
		context.Background(),
		address,
		"",
		"",
		250*time.Millisecond,
		"192.0.2.53:53",
		"example.test",
	)
	if err == nil {
		t.Fatal("Probe unexpectedly succeeded when the relay dropped UDP data")
	}
	if !result.UDPAssociateOK {
		t.Fatal("UDP ASSOCIATE should have succeeded")
	}
	if result.UDPExchangeOK {
		t.Fatal("UDP exchange should not be reported as successful")
	}
	if result.Diagnosis != "udp_no_roundtrip" {
		t.Fatalf("Diagnosis = %q, want udp_no_roundtrip", result.Diagnosis)
	}
}

func TestProbeSOCKS5ReportsRealUDPDNSRoundTrip(t *testing.T) {
	address, stop := startProbeSOCKS5Server(t, true)
	defer stop()

	result, err := probeSOCKS5(
		context.Background(),
		address,
		"",
		"",
		time.Second,
		"192.0.2.53:53",
		"example.test",
	)
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if !result.HandshakeOK || !result.UDPAssociateOK || !result.UDPExchangeOK {
		t.Fatalf("Probe evidence incomplete: %+v", result)
	}
	if result.Diagnosis != "ready" {
		t.Fatalf("Diagnosis = %q, want ready", result.Diagnosis)
	}
	if result.DNSName != "example.test" || result.DNSServer != "192.0.2.53:53" {
		t.Fatalf("Unexpected DNS evidence: %+v", result)
	}
}

func startProbeSOCKS5Server(t *testing.T, echoDNS bool) (string, func()) {
	t.Helper()
	udpConnection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		udpConnection.Close()
		t.Fatalf("Listen: %v", err)
	}

	if echoDNS {
		go func() {
			buffer := make([]byte, 2048)
			count, sender, readErr := udpConnection.ReadFromUDP(buffer)
			if readErr != nil || count < 22 {
				return
			}
			// The test target is IPv4, so the SOCKS5 UDP header is ten bytes.
			buffer[12] = 0x81
			buffer[13] = 0x80
			_, _ = udpConnection.WriteToUDP(buffer[:count], sender)
		}()
	}

	go func() {
		connection, acceptErr := tcpListener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		greeting := make([]byte, 3)
		if _, readErr := io.ReadFull(connection, greeting); readErr != nil {
			return
		}
		if _, writeErr := connection.Write([]byte{5, 0}); writeErr != nil {
			return
		}
		associate := make([]byte, 10)
		if _, readErr := io.ReadFull(connection, associate); readErr != nil {
			return
		}
		udpPort := udpConnection.LocalAddr().(*net.UDPAddr).Port
		response := []byte{5, 0, 0, 1, 127, 0, 0, 1, byte(udpPort >> 8), byte(udpPort)}
		if _, writeErr := connection.Write(response); writeErr != nil {
			return
		}
		_, _ = io.Copy(io.Discard, connection)
	}()

	return tcpListener.Addr().String(), func() {
		_ = tcpListener.Close()
		_ = udpConnection.Close()
	}
}
