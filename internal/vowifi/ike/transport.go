package ike

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/vowifi"
)

type datagramTransport interface {
	LocalAddr() *net.UDPAddr
	RemoteAddr() *net.UDPAddr
	Float(context.Context) error
	RoundTrip(context.Context, []byte) ([]byte, error)
	RoundTripExchange(context.Context, [][]byte) ([][]byte, error)
	SendESP(context.Context, []byte) error
	ReceiveESP(context.Context, []byte) (int, error)
	SendSessionPacket(context.Context, []byte, bool) error
	ReceiveSessionPacket(context.Context, []byte) (int, bool, error)
	Close() error
}

type transportConfig struct {
	Resolver *net.Resolver
	Dialer   *net.Dialer
	Timeout  time.Duration
}

func newDatagramTransport(
	ctx context.Context,
	config transportConfig,
	route vowifi.ProxyRoute,
	host string,
) (datagramTransport, error) {
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{}
	}
	if config.Timeout <= 0 {
		config.Timeout = 12 * time.Second
	}
	addresses, err := resolveEPDG(ctx, config.Resolver, host)
	if err != nil {
		return nil, fmt.Errorf("ike: resolve ePDG: %w", err)
	}
	var remoteIPs []net.IP
	for _, address := range addresses {
		candidate := address.IP.To4()
		if candidate == nil {
			candidate = address.IP.To16()
		}
		if candidate == nil {
			continue
		}
		duplicate := false
		for _, existing := range remoteIPs {
			if existing.Equal(candidate) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			remoteIPs = append(remoteIPs, append(net.IP(nil), candidate...))
		}
	}
	if len(remoteIPs) == 0 {
		return nil, errors.New("ike: ePDG did not resolve to an IP address")
	}
	remotes := make([]*net.UDPAddr, 0, len(remoteIPs))
	for _, remoteIP := range remoteIPs {
		remotes = append(remotes, &net.UDPAddr{IP: remoteIP, Port: 500})
	}
	switch route.Mode {
	case "", vowifi.ProxyModeDirect:
		return newDirectUDP(ctx, config, remotes[0])
	case vowifi.ProxyModeSOCKS5:
		return newSOCKS5UDP(ctx, config, route, remotes)
	default:
		return nil, fmt.Errorf("ike: unsupported proxy mode %q", route.Mode)
	}
}

func roundTripDatagram(
	ctx context.Context,
	timeout time.Duration,
	write func([]byte) error,
	read func([]byte, time.Time) (int, error),
	packet []byte,
) ([]byte, error) {
	writeAll := func(values [][]byte) error {
		if len(values) > 0 {
			return write(values[0])
		}
		return nil
	}
	responses, err := roundTripFragments(ctx, timeout, writeAll, read, [][]byte{packet})
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, errors.New("ike: empty datagram response")
	}
	return responses[0], nil
}

func roundTripFragments(
	ctx context.Context,
	timeout time.Duration,
	writeAll func([][]byte) error,
	read func([]byte, time.Time) (int, error),
	packets [][]byte,
) ([][]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	retransmit := []time.Duration{4 * time.Second, 7200 * time.Millisecond}
	buffer := make([]byte, 65535)
	var lastErr error
	for _, interval := range retransmit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := writeAll(packets); err != nil {
			return nil, err
		}
		attemptDeadline := time.Now().Add(interval)
		if deadline.Before(attemptDeadline) {
			attemptDeadline = deadline
		}
		var (
			totalExpected uint16
			fragments     = make(map[uint16][]byte)
		)
		for time.Now().Before(attemptDeadline) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n, err := read(buffer, attemptDeadline)
			if err == nil {
				pkt := append([]byte(nil), buffer[:n]...)
				header, body, parseErr := parseIKEPacket(pkt)
				if parseErr == nil && header.NextPayload == payloadEncryptedFragment && len(body) >= 8 {
					fragNum := binary.BigEndian.Uint16(body[4:6])
					total := binary.BigEndian.Uint16(body[6:8])
					if total > 1 {
						if totalExpected == 0 {
							totalExpected = total
						}
						fragments[fragNum] = pkt
						if uint16(len(fragments)) == totalExpected {
							res := make([][]byte, 0, totalExpected)
							for i := uint16(1); i <= totalExpected; i++ {
								res = append(res, fragments[i])
							}
							return res, nil
						}
						continue
					}
				}
				return [][]byte{pkt}, nil
			}
			if timeoutError, ok := err.(net.Error); ok && timeoutError.Timeout() {
				lastErr = err
				break
			}
			return nil, err
		}
		if !time.Now().Before(deadline) {
			break
		}
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return nil, fmt.Errorf("ike: UDP exchange timed out: %w", lastErr)
}

type directUDP struct {
	mu      sync.Mutex
	readMu  sync.Mutex
	writeMu sync.Mutex
	config  transportConfig
	conn    *net.UDPConn
	remote  *net.UDPAddr
	floated bool
}

func newDirectUDP(ctx context.Context, config transportConfig, remote *net.UDPAddr) (*directUDP, error) {
	transport := &directUDP{config: config, remote: cloneUDPAddr(remote)}
	if err := transport.dial(ctx, false); err != nil {
		return nil, err
	}
	return transport, nil
}

func (transport *directUDP) dial(ctx context.Context, bind4500 bool) error {
	dialer := *transport.config.Dialer
	if bind4500 {
		localIP := net.IP(nil)
		if transport.conn != nil {
			if current, ok := transport.conn.LocalAddr().(*net.UDPAddr); ok {
				localIP = append(net.IP(nil), current.IP...)
			}
		}
		dialer.LocalAddr = &net.UDPAddr{IP: localIP, Port: 4500}
	}
	connection, err := dialer.DialContext(ctx, "udp", transport.remote.String())
	if err != nil && bind4500 {
		dialer.LocalAddr = nil
		connection, err = dialer.DialContext(ctx, "udp", transport.remote.String())
	}
	if err != nil {
		return fmt.Errorf("ike: dial ePDG UDP: %w", err)
	}
	udp, ok := connection.(*net.UDPConn)
	if !ok {
		_ = connection.Close()
		return errors.New("ike: UDP dialer returned a non-UDP connection")
	}
	old := transport.conn
	transport.conn = udp
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (transport *directUDP) LocalAddr() *net.UDPAddr {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.conn == nil {
		return nil
	}
	address, _ := transport.conn.LocalAddr().(*net.UDPAddr)
	return cloneUDPAddr(address)
}

func (transport *directUDP) RemoteAddr() *net.UDPAddr {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return cloneUDPAddr(transport.remote)
}

func (transport *directUDP) Float(ctx context.Context) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.floated {
		return nil
	}
	transport.remote.Port = 4500
	if err := transport.dial(ctx, true); err != nil {
		return err
	}
	transport.floated = true
	return nil
}

func (transport *directUDP) RoundTrip(ctx context.Context, packet []byte) ([]byte, error) {
	responses, err := transport.RoundTripExchange(ctx, [][]byte{packet})
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, errors.New("ike: empty exchange response")
	}
	return responses[0], nil
}

func (transport *directUDP) RoundTripExchange(ctx context.Context, packets [][]byte) ([][]byte, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.conn == nil {
		return nil, errors.New("ike: UDP transport is closed")
	}
	if len(packets) == 0 {
		return nil, errors.New("ike: outbound packet list is empty")
	}
	requestHeader, _, err := parseIKEPacket(packets[0])
	if err != nil {
		return nil, fmt.Errorf("ike: invalid outbound packet: %w", err)
	}
	var wirePackets [][]byte
	for _, pkt := range packets {
		wire := pkt
		if transport.floated {
			wire = append([]byte{0, 0, 0, 0}, pkt...)
		}
		wirePackets = append(wirePackets, wire)
	}
	writeAll := func(values [][]byte) error {
		for _, value := range values {
			if err := transport.conn.SetWriteDeadline(deadlineFor(ctx, transport.config.Timeout)); err != nil {
				return err
			}
			if _, err := transport.conn.Write(value); err != nil {
				return err
			}
		}
		return nil
	}
	read := func(buffer []byte, attemptDeadline time.Time) (int, error) {
		for {
			if err := transport.conn.SetReadDeadline(attemptDeadline); err != nil {
				return 0, err
			}
			n, err := transport.conn.Read(buffer)
			if err != nil {
				return 0, err
			}
			if transport.floated {
				if !hasNonESPMarker(buffer[:n]) {
					continue
				}
				copy(buffer, buffer[4:n])
				n -= 4
			}
			if !ikeResponseMatchesRequest(buffer[:n], requestHeader) {
				continue
			}
			return n, nil
		}
	}
	return roundTripFragments(ctx, transport.config.Timeout, writeAll, read, wirePackets)
}

func (transport *directUDP) SendESP(ctx context.Context, packet []byte) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.conn == nil || !transport.floated {
		return errors.New("ike: ESP relay requires an active UDP/4500 transport")
	}
	if len(packet) < 8 {
		return errors.New("ike: ESP packet is too short")
	}
	if err := transport.conn.SetWriteDeadline(deadlineFor(ctx, transport.config.Timeout)); err != nil {
		return err
	}
	_, err := transport.conn.Write(packet)
	return err
}

func (transport *directUDP) ReceiveESP(ctx context.Context, buffer []byte) (int, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.conn == nil || !transport.floated {
		return 0, errors.New("ike: ESP relay requires an active UDP/4500 transport")
	}
	if err := transport.conn.SetReadDeadline(deadlineFor(ctx, transport.config.Timeout)); err != nil {
		return 0, err
	}
	n, err := transport.conn.Read(buffer)
	if err != nil {
		return 0, err
	}
	if n >= 4 && buffer[0] == 0 && buffer[1] == 0 && buffer[2] == 0 && buffer[3] == 0 {
		return 0, errors.New("ike: received an IKE packet on the ESP relay")
	}
	return n, nil
}

func (transport *directUDP) SendSessionPacket(ctx context.Context, packet []byte, ike bool) error {
	transport.mu.Lock()
	connection := transport.conn
	floated := transport.floated
	transport.mu.Unlock()
	if connection == nil {
		return errors.New("ike: UDP transport is closed")
	}
	wire := packet
	if floated {
		if ike {
			wire = append([]byte{0, 0, 0, 0}, packet...)
		}
	} else if !ike {
		return errors.New("ike: ESP is not UDP encapsulated on an un-floated transport")
	}
	transport.writeMu.Lock()
	defer transport.writeMu.Unlock()
	if err := connection.SetWriteDeadline(deadlineFor(ctx, transport.config.Timeout)); err != nil {
		return err
	}
	_, err := connection.Write(wire)
	return err
}

func (transport *directUDP) ReceiveSessionPacket(ctx context.Context, buffer []byte) (int, bool, error) {
	transport.mu.Lock()
	connection := transport.conn
	floated := transport.floated
	transport.mu.Unlock()
	if connection == nil {
		return 0, false, errors.New("ike: UDP transport is closed")
	}
	transport.readMu.Lock()
	defer transport.readMu.Unlock()
	if err := connection.SetReadDeadline(deadlineFor(ctx, time.Second)); err != nil {
		return 0, false, err
	}
	n, err := connection.Read(buffer)
	if err != nil {
		return 0, false, err
	}
	if !floated {
		return n, true, nil
	}
	if n >= 4 && buffer[0] == 0 && buffer[1] == 0 && buffer[2] == 0 && buffer[3] == 0 {
		copy(buffer, buffer[4:n])
		return n - 4, true, nil
	}
	return n, false, nil
}

func (transport *directUDP) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.conn == nil {
		return nil
	}
	err := transport.conn.Close()
	transport.conn = nil
	return err
}

type socks5UDP struct {
	mu      sync.Mutex
	readMu  sync.Mutex
	writeMu sync.Mutex
	config  transportConfig
	control net.Conn
	udp     *net.UDPConn
	relay   *net.UDPAddr
	remote  *net.UDPAddr
	remotes []*net.UDPAddr
	floated bool
}

func newSOCKS5UDP(
	ctx context.Context,
	config transportConfig,
	route vowifi.ProxyRoute,
	remotes []*net.UDPAddr,
) (*socks5UDP, error) {
	if len(remotes) == 0 || remotes[0] == nil {
		return nil, errors.New("ike: SOCKS5 transport requires an ePDG destination")
	}
	proxyAddress := strings.TrimSpace(route.Address)
	if _, _, err := net.SplitHostPort(proxyAddress); err != nil {
		return nil, fmt.Errorf("ike: invalid SOCKS5 proxy address: %w", err)
	}
	control, err := config.Dialer.DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, fmt.Errorf("ike: connect SOCKS5 proxy %s: %w", proxyAddress, err)
	}
	fail := func(cause error) (*socks5UDP, error) {
		_ = control.Close()
		return nil, cause
	}
	if err := control.SetDeadline(deadlineFor(ctx, config.Timeout)); err != nil {
		return fail(err)
	}
	methods := []byte{0}
	if route.Username != "" {
		methods = append(methods, 2)
	}
	greeting := append([]byte{5, byte(len(methods))}, methods...)
	if _, err := control.Write(greeting); err != nil {
		return fail(fmt.Errorf("ike: SOCKS5 greeting: %w", err))
	}
	var selection [2]byte
	if _, err := io.ReadFull(control, selection[:]); err != nil {
		return fail(fmt.Errorf("ike: SOCKS5 method selection: %w", err))
	}
	if selection[0] != 5 {
		return fail(errors.New("ike: SOCKS5 proxy returned an invalid version"))
	}
	switch selection[1] {
	case 0:
	case 2:
		if err := socksUserPassword(control, route.Username, route.Password); err != nil {
			return fail(err)
		}
	default:
		return fail(fmt.Errorf("ike: SOCKS5 proxy selected unsupported authentication method %d", selection[1]))
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return fail(fmt.Errorf("ike: SOCKS5 UDP ASSOCIATE request: %w", err))
	}
	relay, err := readSOCKS5Reply(ctx, control, config.Resolver)
	if err != nil {
		return fail(err)
	}
	if relay.IP == nil || relay.IP.IsUnspecified() {
		if peer, ok := control.RemoteAddr().(*net.TCPAddr); ok {
			relay.IP = append(net.IP(nil), peer.IP...)
		}
	}
	udpConnection, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		return fail(fmt.Errorf("ike: dial SOCKS5 UDP relay: %w", err))
	}
	_ = control.SetDeadline(time.Time{})
	return &socks5UDP{
		config:  config,
		control: control,
		udp:     udpConnection,
		relay:   relay,
		remote:  cloneUDPAddr(remotes[0]),
		remotes: cloneUDPAddrs(remotes),
	}, nil
}

func socksUserPassword(connection net.Conn, username, password string) error {
	if len(username) > 255 || len(password) > 255 {
		return errors.New("ike: SOCKS5 username or password exceeds 255 bytes")
	}
	request := []byte{1, byte(len(username))}
	request = append(request, username...)
	request = append(request, byte(len(password)))
	request = append(request, password...)
	if _, err := connection.Write(request); err != nil {
		return fmt.Errorf("ike: SOCKS5 credential exchange: %w", err)
	}
	var response [2]byte
	if _, err := io.ReadFull(connection, response[:]); err != nil {
		return fmt.Errorf("ike: SOCKS5 credential response: %w", err)
	}
	if response[0] != 1 || response[1] != 0 {
		return errors.New("ike: SOCKS5 authentication failed")
	}
	return nil
}

func readSOCKS5Reply(ctx context.Context, connection net.Conn, resolver *net.Resolver) (*net.UDPAddr, error) {
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return nil, fmt.Errorf("ike: SOCKS5 UDP ASSOCIATE response: %w", err)
	}
	if header[0] != 5 || header[1] != 0 || header[2] != 0 {
		return nil, fmt.Errorf("ike: SOCKS5 UDP ASSOCIATE rejected with code %d", header[1])
	}
	ip, name, err := readSOCKSAddress(connection, header[3])
	if err != nil {
		return nil, err
	}
	var encodedPort [2]byte
	if _, err := io.ReadFull(connection, encodedPort[:]); err != nil {
		return nil, fmt.Errorf("ike: SOCKS5 relay port: %w", err)
	}
	if ip == nil && name != "" {
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		addresses, err := resolver.LookupIPAddr(ctx, name)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("ike: resolve SOCKS5 UDP relay domain %q: %w", name, err)
		}
		ip = addresses[0].IP
	}
	return &net.UDPAddr{IP: ip, Port: int(binary.BigEndian.Uint16(encodedPort[:]))}, nil
}

func readSOCKSAddress(reader io.Reader, kind byte) (net.IP, string, error) {
	switch kind {
	case 1:
		ip := make(net.IP, net.IPv4len)
		if _, err := io.ReadFull(reader, ip); err != nil {
			return nil, "", err
		}
		return ip, "", nil
	case 4:
		ip := make(net.IP, net.IPv6len)
		if _, err := io.ReadFull(reader, ip); err != nil {
			return nil, "", err
		}
		return ip, "", nil
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil {
			return nil, "", err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, name); err != nil {
			return nil, "", err
		}
		return nil, string(name), nil
	default:
		return nil, "", fmt.Errorf("ike: unsupported SOCKS5 address type %d", kind)
	}
}

func (transport *socks5UDP) LocalAddr() *net.UDPAddr {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.udp == nil {
		return nil
	}
	address, _ := transport.udp.LocalAddr().(*net.UDPAddr)
	return cloneUDPAddr(address)
}

func (transport *socks5UDP) RemoteAddr() *net.UDPAddr {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return cloneUDPAddr(transport.remote)
}

func (transport *socks5UDP) Float(_ context.Context) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.remote.Port = 4500
	transport.floated = true
	return nil
}

func (transport *socks5UDP) RoundTrip(ctx context.Context, packet []byte) ([]byte, error) {
	responses, err := transport.RoundTripExchange(ctx, [][]byte{packet})
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, errors.New("ike: empty exchange response")
	}
	return responses[0], nil
}

func (transport *socks5UDP) RoundTripExchange(ctx context.Context, packets [][]byte) ([][]byte, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.udp == nil {
		return nil, errors.New("ike: SOCKS5 UDP transport is closed")
	}
	if len(packets) == 0 {
		return nil, errors.New("ike: outbound packet list is empty")
	}
	requestHeader, _, err := parseIKEPacket(packets[0])
	if err != nil {
		return nil, fmt.Errorf("ike: invalid outbound packet: %w", err)
	}
	// Carrier ePDG hostnames commonly return several gateways. A SOCKS5
	// egress can reach a different subset than the local host, so an initial
	// timeout on one address must not make the entire hostname unavailable.
	// Once a gateway answers, keep it pinned for the lifetime of the IKE SA.
	if !transport.floated && requestHeader.Exchange == exchangeIKEInit && requestHeader.MessageID == 0 && len(transport.remotes) > 1 {
		var lastErr error
		var cookieResponse [][]byte
		for _, candidate := range transport.remotes {
			transport.remote = cloneUDPAddr(candidate)
			responses, attemptErr := transport.roundTripFragmentsLocked(ctx, packets, requestHeader)
			if attemptErr == nil {
				if len(responses) > 0 && ikeInitResponseHasCookie(responses[0]) {
					if cookieResponse == nil {
						cookieResponse = responses
					}
					continue
				}
				return responses, nil
			}
			lastErr = attemptErr
			if ctx.Err() != nil || !isNetworkTimeout(attemptErr) {
				return nil, attemptErr
			}
		}
		if cookieResponse != nil {
			return cookieResponse, nil
		}
		return nil, fmt.Errorf("ike: all %d resolved ePDG addresses timed out: %w", len(transport.remotes), lastErr)
	}
	return transport.roundTripFragmentsLocked(ctx, packets, requestHeader)
}

func (transport *socks5UDP) roundTripFragmentsLocked(ctx context.Context, packets [][]byte, requestHeader ikeHeader) ([][]byte, error) {
	var datagrams [][]byte
	for _, pkt := range packets {
		wireIKE := pkt
		if transport.floated {
			wireIKE = append([]byte{0, 0, 0, 0}, pkt...)
		}
		datagram, err := marshalSOCKS5Datagram(transport.remote, wireIKE)
		if err != nil {
			return nil, err
		}
		datagrams = append(datagrams, datagram)
	}
	writeAll := func(values [][]byte) error {
		for _, value := range values {
			if err := transport.udp.SetWriteDeadline(deadlineFor(ctx, transport.config.Timeout)); err != nil {
				return err
			}
			if _, err := transport.udp.Write(value); err != nil {
				return err
			}
		}
		return nil
	}
	read := func(buffer []byte, attemptDeadline time.Time) (int, error) {
		for {
			payload, err := readExpectedSOCKS5Datagram(
				transport.udp,
				transport.remote,
				buffer,
				attemptDeadline,
			)
			if err != nil {
				return 0, err
			}
			if transport.floated {
				if !hasNonESPMarker(payload) {
					continue
				}
				payload = payload[4:]
			}
			if !ikeResponseMatchesRequest(payload, requestHeader) {
				continue
			}
			copy(buffer, payload)
			return len(payload), nil
		}
	}
	return roundTripFragments(ctx, transport.config.Timeout, writeAll, read, datagrams)
}

func isNetworkTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func (transport *socks5UDP) SendESP(ctx context.Context, packet []byte) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.udp == nil || !transport.floated {
		return errors.New("ike: SOCKS5 ESP relay requires an active UDP/4500 association")
	}
	if len(packet) < 8 {
		return errors.New("ike: ESP packet is too short")
	}
	datagram, err := marshalSOCKS5Datagram(transport.remote, packet)
	if err != nil {
		return err
	}
	if err := transport.udp.SetWriteDeadline(deadlineFor(ctx, transport.config.Timeout)); err != nil {
		return err
	}
	_, err = transport.udp.Write(datagram)
	return err
}

func (transport *socks5UDP) ReceiveESP(ctx context.Context, buffer []byte) (int, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.udp == nil || !transport.floated {
		return 0, errors.New("ike: SOCKS5 ESP relay requires an active UDP/4500 association")
	}
	wire := make([]byte, len(buffer)+32)
	payload, err := readExpectedSOCKS5Datagram(
		transport.udp,
		transport.remote,
		wire,
		deadlineFor(ctx, transport.config.Timeout),
	)
	if err != nil {
		return 0, err
	}
	if len(payload) >= 4 && payload[0] == 0 && payload[1] == 0 && payload[2] == 0 && payload[3] == 0 {
		return 0, errors.New("ike: received an IKE packet on the SOCKS5 ESP relay")
	}
	if len(payload) > len(buffer) {
		return 0, io.ErrShortBuffer
	}
	copy(buffer, payload)
	return len(payload), nil
}

func (transport *socks5UDP) SendSessionPacket(ctx context.Context, packet []byte, ike bool) error {
	transport.mu.Lock()
	connection := transport.udp
	floated := transport.floated
	remote := cloneUDPAddr(transport.remote)
	transport.mu.Unlock()
	if connection == nil || !floated {
		return errors.New("ike: SOCKS5 session transport is not on UDP/4500")
	}
	wire := packet
	if ike {
		wire = append([]byte{0, 0, 0, 0}, packet...)
	}
	datagram, err := marshalSOCKS5Datagram(remote, wire)
	if err != nil {
		return err
	}
	transport.writeMu.Lock()
	defer transport.writeMu.Unlock()
	if err := connection.SetWriteDeadline(deadlineFor(ctx, transport.config.Timeout)); err != nil {
		return err
	}
	_, err = connection.Write(datagram)
	return err
}

func (transport *socks5UDP) ReceiveSessionPacket(ctx context.Context, buffer []byte) (int, bool, error) {
	transport.mu.Lock()
	connection := transport.udp
	floated := transport.floated
	remote := cloneUDPAddr(transport.remote)
	transport.mu.Unlock()
	if connection == nil || !floated {
		return 0, false, errors.New("ike: SOCKS5 session transport is not on UDP/4500")
	}
	wire := make([]byte, len(buffer)+32)
	transport.readMu.Lock()
	defer transport.readMu.Unlock()
	payload, err := readExpectedSOCKS5Datagram(
		connection,
		remote,
		wire,
		deadlineFor(ctx, time.Second),
	)
	if err != nil {
		return 0, false, err
	}
	isIKE := len(payload) >= 4 && payload[0] == 0 && payload[1] == 0 && payload[2] == 0 && payload[3] == 0
	if isIKE {
		payload = payload[4:]
	}
	if len(payload) > len(buffer) {
		return 0, false, io.ErrShortBuffer
	}
	copy(buffer, payload)
	return len(payload), isIKE, nil
}

func (transport *socks5UDP) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	var errs []error
	if transport.udp != nil {
		if err := transport.udp.Close(); err != nil {
			errs = append(errs, err)
		}
		transport.udp = nil
	}
	if transport.control != nil {
		if err := transport.control.Close(); err != nil {
			errs = append(errs, err)
		}
		transport.control = nil
	}
	return errors.Join(errs...)
}

// readExpectedSOCKS5Datagram discards valid datagrams attributed to a
// different destination until the caller's deadline. A retransmitted
// IKE_SA_INIT response from UDP/500 can legitimately remain queued after the
// transport floats to UDP/4500; it must not be accepted as the current
// exchange, but it is not a reason to abort the authenticated session either.
func readExpectedSOCKS5Datagram(
	connection *net.UDPConn,
	remote *net.UDPAddr,
	wire []byte,
	deadline time.Time,
) ([]byte, error) {
	if connection == nil || remote == nil {
		return nil, errors.New("ike: SOCKS5 UDP transport is closed")
	}
	for {
		if err := connection.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		n, err := connection.Read(wire)
		if err != nil {
			return nil, err
		}
		payload, source, err := parseSOCKS5Datagram(wire[:n])
		if err != nil {
			return nil, err
		}
		if source.IP.Equal(remote.IP) && source.Port == remote.Port {
			return payload, nil
		}
	}
}

func hasNonESPMarker(packet []byte) bool {
	return len(packet) >= 4 &&
		packet[0] == 0 &&
		packet[1] == 0 &&
		packet[2] == 0 &&
		packet[3] == 0
}

func ikeResponseMatchesRequest(
	packet []byte,
	request ikeHeader,
) bool {
	response, _, err := parseIKEPacket(packet)
	if err != nil {
		return false
	}
	if response.InitiatorSPI != request.InitiatorSPI ||
		response.Exchange != request.Exchange ||
		response.MessageID != request.MessageID ||
		response.Flags&flagResponse == 0 ||
		response.Flags&flagInitiator != 0 {
		return false
	}
	var zeroSPI [8]byte
	if request.ResponderSPI == zeroSPI {
		if response.ResponderSPI != zeroSPI {
			return true
		}
		return response.Exchange == exchangeIKEInit &&
			response.MessageID == 0 &&
			ikeInitResponseHasCookie(packet)
	}
	return response.ResponderSPI == request.ResponderSPI
}

func ikeInitResponseHasCookie(packet []byte) bool {
	header, body, err := parseIKEPacket(packet)
	if err != nil || header.Exchange != exchangeIKEInit || header.MessageID != 0 {
		return false
	}
	payloads, err := parsePayloadChain(header.NextPayload, body)
	if err != nil {
		return false
	}
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, data, err := parseNotify(item)
		if err == nil && kind == notifyCookie && len(data) > 0 {
			return true
		}
	}
	return false
}

func marshalSOCKS5Datagram(remote *net.UDPAddr, payload []byte) ([]byte, error) {
	if remote == nil || remote.IP == nil || remote.Port < 1 || remote.Port > 65535 {
		return nil, errors.New("ike: invalid SOCKS5 UDP destination")
	}
	result := []byte{0, 0, 0}
	if ip4 := remote.IP.To4(); ip4 != nil {
		result = append(result, 1)
		result = append(result, ip4...)
	} else if ip16 := remote.IP.To16(); ip16 != nil {
		result = append(result, 4)
		result = append(result, ip16...)
	} else {
		return nil, errors.New("ike: SOCKS5 UDP destination is not an IP address")
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(remote.Port))
	result = append(result, port[:]...)
	result = append(result, payload...)
	return result, nil
}

func parseSOCKS5Datagram(encoded []byte) ([]byte, *net.UDPAddr, error) {
	if len(encoded) < 4 || encoded[0] != 0 || encoded[1] != 0 {
		return nil, nil, errors.New("ike: malformed SOCKS5 UDP datagram")
	}
	if encoded[2] != 0 {
		return nil, nil, errors.New("ike: fragmented SOCKS5 UDP datagrams are unsupported")
	}
	offset := 4
	var ip net.IP
	switch encoded[3] {
	case 1:
		if offset+4 > len(encoded) {
			return nil, nil, errors.New("ike: truncated SOCKS5 IPv4 address")
		}
		ip = append(net.IP(nil), encoded[offset:offset+4]...)
		offset += 4
	case 4:
		if offset+16 > len(encoded) {
			return nil, nil, errors.New("ike: truncated SOCKS5 IPv6 address")
		}
		ip = append(net.IP(nil), encoded[offset:offset+16]...)
		offset += 16
	case 3:
		if offset >= len(encoded) {
			return nil, nil, errors.New("ike: truncated SOCKS5 domain length")
		}
		length := int(encoded[offset])
		offset++
		if offset+length > len(encoded) {
			return nil, nil, errors.New("ike: truncated SOCKS5 domain")
		}
		addresses, err := net.LookupIP(string(encoded[offset : offset+length]))
		if err != nil || len(addresses) == 0 {
			return nil, nil, errors.New("ike: cannot resolve SOCKS5 UDP response domain")
		}
		ip = addresses[0]
		offset += length
	default:
		return nil, nil, errors.New("ike: unsupported SOCKS5 UDP address type")
	}
	if offset+2 > len(encoded) {
		return nil, nil, errors.New("ike: truncated SOCKS5 UDP port")
	}
	port := int(binary.BigEndian.Uint16(encoded[offset : offset+2]))
	offset += 2
	return append([]byte(nil), encoded[offset:]...), &net.UDPAddr{IP: ip, Port: port}, nil
}

func deadlineFor(ctx context.Context, maximum time.Duration) time.Time {
	deadline := time.Now().Add(maximum)
	if ctx != nil {
		if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
			return caller
		}
	}
	return deadline
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), address.IP...), Port: address.Port, Zone: address.Zone}
}

func cloneUDPAddrs(addresses []*net.UDPAddr) []*net.UDPAddr {
	result := make([]*net.UDPAddr, 0, len(addresses))
	for _, address := range addresses {
		if address != nil {
			result = append(result, cloneUDPAddr(address))
		}
	}
	return result
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("ike: invalid UDP port")
	}
	return port, nil
}
