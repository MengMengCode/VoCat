package ike

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

type deadlineError struct{}

func (deadlineError) Error() string   { return "deadline" }
func (deadlineError) Timeout() bool   { return true }
func (deadlineError) Temporary() bool { return true }

func TestRoundTripDatagramWaitsBeyondFirst500Milliseconds(t *testing.T) {
	available := make(chan struct{})
	go func() {
		time.Sleep(700 * time.Millisecond)
		close(available)
	}()
	writes := 0
	started := time.Now()
	response, err := roundTripDatagram(
		context.Background(),
		2*time.Second,
		func([]byte) error {
			writes++
			return nil
		},
		func(buffer []byte, deadline time.Time) (int, error) {
			select {
			case <-available:
				copy(buffer, []byte("response"))
				return len("response"), nil
			case <-time.After(time.Until(deadline)):
				return 0, deadlineError{}
			}
		},
		[]byte("request"),
	)
	if err != nil {
		t.Fatalf("roundTripDatagram() error = %v", err)
	}
	if string(response) != "response" || writes < 2 {
		t.Fatalf("response=%q writes=%d", response, writes)
	}
	if elapsed := time.Since(started); elapsed < 650*time.Millisecond {
		t.Fatalf("round trip returned too early after %v", elapsed)
	}
}

func TestRoundTripDatagramHonorsTotalTimeout(t *testing.T) {
	started := time.Now()
	_, err := roundTripDatagram(
		context.Background(),
		120*time.Millisecond,
		func([]byte) error { return nil },
		func(_ []byte, deadline time.Time) (int, error) {
			time.Sleep(time.Until(deadline))
			return 0, deadlineError{}
		},
		[]byte("request"),
	)
	if err == nil {
		t.Fatal("roundTripDatagram() accepted a missing response")
	}
	elapsed := time.Since(started)
	if elapsed < 100*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Fatalf("total timeout elapsed = %v, want approximately 120ms", elapsed)
	}
}

func TestSOCKS5UDPDatagramRoundTrip(t *testing.T) {
	remote := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 4500}
	encoded, err := marshalSOCKS5Datagram(remote, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	payload, decoded, err := parseSOCKS5Datagram(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.IP.Equal(remote.IP) || decoded.Port != remote.Port || string(payload) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("decoded SOCKS datagram = %v %v %x", decoded, remote, payload)
	}
	fragmented := append([]byte(nil), encoded...)
	fragmented[2] = 1
	if _, _, err := parseSOCKS5Datagram(fragmented); err == nil {
		t.Fatal("fragmented SOCKS5 UDP datagram was accepted")
	}
}

func TestSOCKS5UDPAssociateDomainReplyIsResolved(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		reply := []byte{5, 0, 0, 3, byte(len("localhost"))}
		reply = append(reply, "localhost"...)
		var port [2]byte
		binary.BigEndian.PutUint16(port[:], 7897)
		reply = append(reply, port[:]...)
		_, _ = server.Write(reply)
	}()
	address, err := readSOCKS5Reply(context.Background(), client, net.DefaultResolver)
	if err != nil {
		t.Fatalf("readSOCKS5Reply() error = %v", err)
	}
	if address.IP == nil || address.Port != 7897 {
		t.Fatalf("resolved relay = %v", address)
	}
}

func TestSOCKS5InitialExchangeFallsBackAcrossResolvedEPDGAddresses(t *testing.T) {
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	connection, err := net.DialUDP("udp", nil, relay.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	first := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 500}
	second := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 20), Port: 500}
	transport := &socks5UDP{
		config:  transportConfig{Timeout: 80 * time.Millisecond},
		udp:     connection,
		remote:  cloneUDPAddr(first),
		remotes: cloneUDPAddrs([]*net.UDPAddr{first, second}),
	}
	defer transport.Close()
	requestHeader := ikeHeader{
		InitiatorSPI: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		Exchange:     exchangeIKEInit,
		Flags:        flagInitiator,
	}
	request := requestHeader.marshal([]byte("request"))
	response := ikeHeader{
		InitiatorSPI: requestHeader.InitiatorSPI,
		ResponderSPI: [8]byte{8, 7, 6, 5, 4, 3, 2, 1},
		Exchange:     exchangeIKEInit,
		Flags:        flagResponse,
	}.marshal([]byte("response"))
	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, peer, readErr := relay.ReadFromUDP(buffer)
			if readErr != nil {
				serverDone <- readErr
				return
			}
			_, destination, parseErr := parseSOCKS5Datagram(buffer[:n])
			if parseErr != nil {
				serverDone <- parseErr
				return
			}
			if !destination.IP.Equal(second.IP) {
				continue
			}
			wire, marshalErr := marshalSOCKS5Datagram(second, response)
			if marshalErr == nil {
				_, marshalErr = relay.WriteToUDP(wire, peer)
			}
			serverDone <- marshalErr
			return
		}
	}()

	got, err := transport.RoundTrip(context.Background(), request)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if string(got) != string(response) {
		t.Fatalf("RoundTrip() response = %x", got)
	}
	if !transport.RemoteAddr().IP.Equal(second.IP) {
		t.Fatalf("selected ePDG = %v, want %v", transport.RemoteAddr(), second)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("relay: %v", err)
	}
}

func TestDirectInitialExchangeFallsBackAcrossResolvedEPDGAddresses(t *testing.T) {
	first, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	transport, err := newDirectUDP(
		context.Background(),
		transportConfig{Dialer: &net.Dialer{}, Timeout: 80 * time.Millisecond},
		[]*net.UDPAddr{
			cloneUDPAddr(first.LocalAddr().(*net.UDPAddr)),
			cloneUDPAddr(second.LocalAddr().(*net.UDPAddr)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	requestHeader := ikeHeader{
		InitiatorSPI: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		Exchange:     exchangeIKEInit,
		Flags:        flagInitiator,
	}
	request := requestHeader.marshal([]byte("request"))
	response := ikeHeader{
		InitiatorSPI: requestHeader.InitiatorSPI,
		ResponderSPI: [8]byte{8, 7, 6, 5, 4, 3, 2, 1},
		Exchange:     exchangeIKEInit,
		Flags:        flagResponse,
	}.marshal([]byte("response"))
	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		_, peer, readErr := second.ReadFromUDP(buffer)
		if readErr == nil {
			_, readErr = second.WriteToUDP(response, peer)
		}
		serverDone <- readErr
	}()

	got, err := transport.RoundTrip(context.Background(), request)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if string(got) != string(response) {
		t.Fatalf("RoundTrip() response = %x", got)
	}
	if !udpAddrsEqual(transport.RemoteAddr(), second.LocalAddr().(*net.UDPAddr)) {
		t.Fatalf("selected ePDG = %v, want %v", transport.RemoteAddr(), second.LocalAddr())
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("second ePDG: %v", err)
	}
}

func TestSOCKS5RoundTripSkipsStaleAndESPDatagrams(t *testing.T) {
	relay, err := net.ListenUDP(
		"udp",
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	connection, err := net.DialUDP(
		"udp",
		nil,
		relay.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 4500}
	transport := &socks5UDP{
		config:  transportConfig{Timeout: time.Second},
		udp:     connection,
		remote:  cloneUDPAddr(remote),
		floated: true,
	}
	defer transport.Close()
	requestHeader := ikeHeader{
		InitiatorSPI: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		ResponderSPI: [8]byte{8, 7, 6, 5, 4, 3, 2, 1},
		Exchange:     exchangeIKEAuth,
		Flags:        flagInitiator,
		MessageID:    3,
	}
	request := requestHeader.marshal([]byte("request"))
	validResponse := ikeHeader{
		InitiatorSPI: requestHeader.InitiatorSPI,
		ResponderSPI: requestHeader.ResponderSPI,
		Exchange:     requestHeader.Exchange,
		Flags:        flagResponse,
		MessageID:    requestHeader.MessageID,
	}.marshal([]byte("response"))

	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		_, peer, err := relay.ReadFromUDP(buffer)
		if err != nil {
			serverDone <- err
			return
		}
		stale, err := marshalSOCKS5Datagram(
			&net.UDPAddr{IP: remote.IP, Port: 500},
			append([]byte{0, 0, 0, 0}, []byte("stale")...),
		)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := relay.WriteToUDP(stale, peer); err != nil {
			serverDone <- err
			return
		}
		esp, err := marshalSOCKS5Datagram(
			remote,
			[]byte{1, 2, 3, 4, 5, 6, 7, 8},
		)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := relay.WriteToUDP(esp, peer); err != nil {
			serverDone <- err
			return
		}
		staleIKE := ikeHeader{
			InitiatorSPI: requestHeader.InitiatorSPI,
			ResponderSPI: requestHeader.ResponderSPI,
			Exchange:     requestHeader.Exchange,
			Flags:        flagResponse,
			MessageID:    requestHeader.MessageID - 1,
		}.marshal([]byte("stale IKE"))
		staleIKE, err = marshalSOCKS5Datagram(
			remote,
			append([]byte{0, 0, 0, 0}, staleIKE...),
		)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := relay.WriteToUDP(staleIKE, peer); err != nil {
			serverDone <- err
			return
		}
		valid, err := marshalSOCKS5Datagram(
			remote,
			append([]byte{0, 0, 0, 0}, validResponse...),
		)
		if err == nil {
			_, err = relay.WriteToUDP(valid, peer)
		}
		serverDone <- err
	}()

	response, err := transport.RoundTrip(
		context.Background(),
		request,
	)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if string(response) != string(validResponse) {
		t.Fatalf("RoundTrip() response = %x", response)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("relay: %v", err)
	}
}

func TestSessionReadDoesNotBlockIndependentWrite(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	connection, err := net.DialUDP("udp", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	transport := &directUDP{
		config:  transportConfig{Timeout: time.Second},
		conn:    connection,
		remote:  cloneUDPAddr(server.LocalAddr().(*net.UDPAddr)),
		floated: true,
	}
	defer transport.Close()
	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64)
		_, _, err := transport.ReceiveSessionPacket(context.Background(), buffer)
		readDone <- err
	}()
	time.Sleep(30 * time.Millisecond)
	started := time.Now()
	if err := transport.SendSessionPacket(context.Background(), []byte{1, 2, 3, 4, 5, 6, 7, 8}, false); err != nil {
		t.Fatalf("SendSessionPacket() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("session write blocked behind reader for %v", elapsed)
	}
	buffer := make([]byte, 64)
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := server.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Fatalf("server received %d bytes, want 8", n)
	}
	_ = transport.Close()
	select {
	case err := <-readDone:
		if err == nil || (!errors.Is(err, net.ErrClosed) && !isNetworkClose(err)) {
			t.Fatalf("reader close error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session reader did not wake after Close")
	}
}

func isNetworkClose(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError)
}
