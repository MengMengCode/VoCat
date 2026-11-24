package ims

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestMTUCompatibilityRegisterTransport(t *testing.T) {
	for _, test := range []struct {
		name               string
		compatibility      bool
		agent              string
		wantTransport      string
		challengeBeforeTCP bool
	}{
		{"short UDP is retained", true, "vocat/test", "udp", false},
		{"oversized UDP is rebuilt for TCP", true, strings.Repeat("a", 1000), "tcp", false},
		{"401 makes UDP too large", true, "vocat/test", "tcp", true},
		{"disabled keeps ordinary UDP", false, strings.Repeat("a", 1000), "udp", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer tcp.Close()
			address := tcp.Addr().(*net.TCPAddr)
			udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: address.IP, Port: address.Port})
			if err != nil {
				t.Fatal(err)
			}
			defer udp.Close()

			deadline := time.Now().Add(5 * time.Second)
			if err := tcp.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if err := udp.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if test.challengeBeforeTCP {
					if err := replyToMTUTestUDPRegister(udp, true); err != nil {
						done <- err
						return
					}
				}
				if test.wantTransport == "tcp" {
					conn, err := tcp.Accept()
					if err != nil {
						done <- err
						return
					}
					defer conn.Close()
					if err := conn.SetDeadline(deadline); err != nil {
						done <- err
						return
					}
					packet, err := readSIPPacket(bufio.NewReader(conn))
					if err != nil {
						done <- err
						return
					}
					response, err := mtuTestRegisterResponse(packet, "tcp")
					if err == nil {
						_, err = conn.Write(response)
					}
					done <- err
					return
				}
				done <- replyToMTUTestUDPRegister(udp, false)
			}()

			provider, err := NewProvider(&recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4, 5, 6, 7, 8}}}, Config{
				Transport:             "udp",
				AutoTransportFallback: true,
				SecurityMode:          SecurityDisabled,
				TransactionTimeout:    time.Second,
				PublicIdentity:        "sip:u@example.test",
				UserAgent:             test.agent,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			session, err := provider.Start(ctx, vowifi.IMSRequest{
				Identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01"},
				Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
					Established:      true,
					DataplaneMode:    "userspace",
					MTUCompatibility: test.compatibility,
					LocalIPv4:        address.IP.String(),
					PCSCF:            []string{address.String()},
				}},
			})
			// The mock registrar deliberately rejects registration after verifying
			// the request. A SIP rejection must remain authoritative.
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if session != nil || !strings.Contains(fmt.Sprint(err), "403") {
				t.Fatalf("Start() = %v, %v; want the registrar's 403", session, err)
			}
			if test.wantTransport == "tcp" {
				if err := udp.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				buffer := make([]byte, 4096)
				_, _, err := udp.ReadFromUDP(buffer)
				if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
					t.Fatalf("oversized REGISTER reached the UDP listener: %v", err)
				}
			}
		})
	}
}

func mtuTestRegisterResponse(packet sipPacket, transport string) ([]byte, error) {
	if packet.Request == nil || packet.Request.Method != "REGISTER" {
		return nil, fmt.Errorf("expected REGISTER, got %#v", packet.Request)
	}
	request := packet.Request
	if !strings.HasPrefix(request.value("Via"), "SIP/2.0/"+strings.ToUpper(transport)+" ") ||
		!strings.Contains(request.value("Contact"), ";transport="+transport) {
		return nil, fmt.Errorf("REGISTER headers did not use %s", transport)
	}
	return testResponse(403, "Forbidden", request.value("Call-ID"), request.value("CSeq"), nil), nil
}

func replyToMTUTestUDPRegister(conn *net.UDPConn, challenge bool) error {
	buffer := make([]byte, 4096)
	count, client, err := conn.ReadFromUDP(buffer)
	if err != nil {
		return err
	}
	packet, err := parseSIPPacket(buffer[:count])
	if err != nil {
		return err
	}
	response, err := mtuTestRegisterResponse(packet, "udp")
	if err != nil {
		return err
	}
	if challenge {
		request := packet.Request
		nonce := base64.StdEncoding.EncodeToString([]byte("0123456789ABCDEF0123456789abcdef"))
		header := `WWW-Authenticate: Digest realm="ims.example.test",nonce="` + nonce + `",algorithm=AKAv1-MD5,qop="auth"`
		response = testResponse(401, "Unauthorized", request.value("Call-ID"), request.value("CSeq"), []string{header})
	}
	_, err = conn.WriteToUDP(response, client)
	return err
}
