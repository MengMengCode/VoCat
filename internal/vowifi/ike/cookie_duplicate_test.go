package ike

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func cookiePacket(spi [8]byte, flags uint8, cookie []byte) []byte {
	first, body, _ := marshalPayloadChain([]payload{makeNotify(notifyCookie, cookie)})
	return ikeHeader{InitiatorSPI: spi, Exchange: exchangeIKEInit, Flags: flags, NextPayload: first}.marshal(body)
}

// Reproduces the live failure: a retransmitted init leaves two identical COOKIE
// replies, and the second reaches the client after it has echoed the first.
func TestDirectUDPWaitsPastDelayedDuplicateCookie(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	transport, err := newDirectUDP(context.Background(), transportConfig{Dialer: &net.Dialer{}, Timeout: time.Second}, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	spi := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	cookie := []byte{1, 3, 5, 7}
	initial := ikeHeader{InitiatorSPI: spi, Exchange: exchangeIKEInit, Flags: flagInitiator}.marshal(nil)
	challenge := cookiePacket(spi, flagResponse, cookie)
	final := ikeHeader{InitiatorSPI: spi, ResponderSPI: [8]byte{9}, Exchange: exchangeIKEInit, Flags: flagResponse}.marshal(nil)
	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		_, peer, err := server.ReadFromUDP(buffer)
		if err != nil {
			done <- err
			return
		}
		_, err = server.WriteToUDP(challenge, peer)
		if err != nil {
			done <- err
			return
		}
		_, peer, err = server.ReadFromUDP(buffer)
		if err != nil {
			done <- err
			return
		}
		_, err = server.WriteToUDP(challenge, peer)
		if err != nil {
			done <- err
			return
		}
		_, err = server.WriteToUDP(final, peer)
		done <- err
	}()
	got, err := transport.RoundTrip(context.Background(), initial)
	if err != nil || !bytes.Equal(got, challenge) {
		t.Fatalf("initial reply: %x, %v", got, err)
	}
	got, err = transport.RoundTrip(context.Background(), cookiePacket(spi, flagInitiator, cookie))
	if err != nil || !bytes.Equal(got, final) {
		t.Fatalf("duplicate ended the handshake: %x, %v", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDifferentCookieIsNotDiscarded(t *testing.T) {
	spi := [8]byte{1}
	request := cookiePacket(spi, flagInitiator, []byte{1})
	if repeatedCookieChallenge(request, cookiePacket(spi, flagResponse, []byte{2})) {
		t.Fatal("fresh cookie was discarded")
	}
}

func TestSOCKS5PinsGatewayThatIssuedCookie(t *testing.T) {
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	connection, err := net.DialUDP("udp", nil, relay.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	first := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 500}
	second := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 500}
	transport := &socks5UDP{config: transportConfig{Timeout: time.Second}, udp: connection, remote: cloneUDPAddr(first), remotes: cloneUDPAddrs([]*net.UDPAddr{first, second})}
	defer transport.Close()
	spi := [8]byte{1}
	cookie := []byte{1, 2, 3, 4}
	challenge := cookiePacket(spi, flagResponse, cookie)
	final := ikeHeader{InitiatorSPI: spi, ResponderSPI: [8]byte{9}, Exchange: exchangeIKEInit, Flags: flagResponse}.marshal(nil)
	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		for i := 0; i < 2; i++ {
			n, peer, err := relay.ReadFromUDP(buffer)
			if err != nil {
				done <- err
				return
			}
			_, dest, err := parseSOCKS5Datagram(buffer[:n])
			if err != nil {
				done <- err
				return
			}
			if !dest.IP.Equal(first.IP) {
				done <- &net.AddrError{Err: "cookie sent to a different gateway", Addr: dest.String()}
				return
			}
			packet := challenge
			if i == 1 {
				packet = final
			}
			wire, _ := marshalSOCKS5Datagram(first, packet)
			if _, err = relay.WriteToUDP(wire, peer); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	initial := ikeHeader{InitiatorSPI: spi, Exchange: exchangeIKEInit, Flags: flagInitiator}.marshal(nil)
	got, err := transport.RoundTrip(context.Background(), initial)
	if err != nil || !bytes.Equal(got, challenge) {
		t.Fatalf("cookie: %x %v", got, err)
	}
	got, err = transport.RoundTrip(context.Background(), cookiePacket(spi, flagInitiator, cookie))
	if err != nil || !bytes.Equal(got, final) {
		t.Fatalf("pinned retry: %x %v", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
