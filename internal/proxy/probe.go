package proxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"vocat/internal/i18n"
)

type ProbeResult struct {
	Reachable      bool   `json:"reachable"`
	HandshakeOK    bool   `json:"handshake_ok"`
	UDPAssociateOK bool   `json:"udp_associate_ok"`
	UDPExchangeOK  bool   `json:"udp_exchange_ok"`
	AuthMethod     string `json:"auth_method,omitempty"`
	RelayAddr      string `json:"relay_addr,omitempty"`
	DNSServer      string `json:"dns_server,omitempty"`
	DNSName        string `json:"dns_name,omitempty"`
	DNSRCode       int    `json:"dns_rcode,omitempty"`
	RoundTripMS    int64  `json:"round_trip_ms,omitempty"`
	Diagnosis      string `json:"diagnosis,omitempty"`
	Hint           string `json:"hint,omitempty"`
}

const (
	defaultProbeDNSServer = "1.1.1.1:53"
	defaultProbeDNSName   = "example.com"
)

func ProbeSOCKS5(
	ctx context.Context,
	address string,
	username string,
	password string,
	timeout time.Duration,
) (ProbeResult, error) {
	return probeSOCKS5(ctx, address, username, password, timeout, defaultProbeDNSServer, defaultProbeDNSName)
}

// probeSOCKS5 performs both the SOCKS5 control-plane negotiation and a real
// UDP DNS round trip through the returned relay. Keeping the target injectable
// makes the negative paths deterministic in tests without weakening the
// production probe.
func probeSOCKS5(
	ctx context.Context,
	address string,
	username string,
	password string,
	timeout time.Duration,
	dnsServer string,
	dnsName string,
) (ProbeResult, error) {
	address = strings.TrimSpace(address)
	if _, _, err := net.SplitHostPort(address); err != nil {
		return ProbeResult{}, fmt.Errorf("proxy: upstream address must be host:port: %w", err)
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := (&net.Dialer{Timeout: timeout}).DialContext(probeContext, "tcp", address)
	if err != nil {
		return ProbeResult{
			Diagnosis: "tcp_unreachable",
			Hint:      i18n.T("检查地址、端口、防火墙与上游代理监听状态。"),
		}, err
	}
	defer connection.Close()
	result := ProbeResult{Reachable: true}
	_ = connection.SetDeadline(time.Now().Add(timeout))

	methods := []byte{0}
	if username != "" {
		methods = append(methods, 2)
	}
	greeting := append([]byte{5, byte(len(methods))}, methods...)
	if _, err := connection.Write(greeting); err != nil {
		return result, err
	}
	methodResponse := make([]byte, 2)
	if _, err := io.ReadFull(connection, methodResponse); err != nil {
		return result, err
	}
	if methodResponse[0] != 5 || methodResponse[1] == 0xff {
		result.Diagnosis = "no_acceptable_auth"
		return result, errors.New("proxy: upstream rejected all SOCKS5 authentication methods")
	}
	switch methodResponse[1] {
	case 0:
		result.AuthMethod = "none"
	case 2:
		result.AuthMethod = "username_password"
		if username == "" || len(username) > 255 || len(password) > 255 {
			return result, errors.New("proxy: upstream requires username/password authentication")
		}
		authRequest := []byte{1, byte(len(username))}
		authRequest = append(authRequest, []byte(username)...)
		authRequest = append(authRequest, byte(len(password)))
		authRequest = append(authRequest, []byte(password)...)
		if _, err := connection.Write(authRequest); err != nil {
			return result, err
		}
		authResponse := make([]byte, 2)
		if _, err := io.ReadFull(connection, authResponse); err != nil {
			return result, err
		}
		if authResponse[0] != 1 || authResponse[1] != 0 {
			result.Diagnosis = "authentication_failed"
			return result, errors.New("proxy: upstream username/password authentication failed")
		}
	default:
		result.AuthMethod = fmt.Sprintf("method_%d", methodResponse[1])
		return result, errors.New("proxy: upstream selected an unsupported authentication method")
	}
	result.HandshakeOK = true

	if _, err := connection.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return result, err
	}
	reader := bufio.NewReader(connection)
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return result, err
	}
	if header[0] != 5 {
		return result, errors.New("proxy: invalid UDP ASSOCIATE response version")
	}
	if header[1] != 0 {
		result.Diagnosis = "udp_associate_rejected"
		result.Hint = i18n.T("该代理不能承载 ePDG 所需的 UDP；启用上游 SOCKS5 UDP 转发后重试。")
		return result, fmt.Errorf("proxy: upstream rejected UDP ASSOCIATE with code %d", header[1])
	}
	host, err := readSOCKSAddress(reader, header[3])
	if err != nil {
		return result, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return result, err
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])
	result.UDPAssociateOK = true
	result.RelayAddr = net.JoinHostPort(host, fmt.Sprintf("%d", port))
	result.DNSServer = dnsServer
	result.DNSName = dnsName

	if err := probeUDPExchange(probeContext, connection, &result, host, port, dnsServer, dnsName, timeout); err != nil {
		if result.Diagnosis == "" {
			result.Diagnosis = "udp_no_roundtrip"
		}
		if result.Hint == "" {
			result.Hint = i18n.T("UDP ASSOCIATE 已建立，但实际 UDP 数据没有返回；检查节点 UDP 转发、路由和防火墙。")
		}
		return result, err
	}
	result.Diagnosis = "ready"
	result.Hint = i18n.T("TCP 握手、认证、UDP ASSOCIATE 与真实 UDP DNS 往返均通过。")
	return result, nil
}

func probeUDPExchange(
	ctx context.Context,
	control net.Conn,
	result *ProbeResult,
	relayHost string,
	relayPort int,
	dnsServer string,
	dnsName string,
	timeout time.Duration,
) error {
	if result == nil {
		return errors.New("proxy: probe result is nil")
	}
	dnsAddress, err := net.ResolveUDPAddr("udp", strings.TrimSpace(dnsServer))
	if err != nil {
		result.Diagnosis = "invalid_dns_target"
		return fmt.Errorf("proxy: resolve UDP probe target: %w", err)
	}
	relayHost = strings.TrimSpace(relayHost)
	if relayIP := net.ParseIP(relayHost); relayIP != nil && relayIP.IsUnspecified() {
		remoteHost, _, splitErr := net.SplitHostPort(control.RemoteAddr().String())
		if splitErr != nil {
			result.Diagnosis = "invalid_udp_relay"
			return fmt.Errorf("proxy: resolve wildcard UDP relay: %w", splitErr)
		}
		relayHost = remoteHost
	}
	relayAddress, err := net.ResolveUDPAddr("udp", net.JoinHostPort(relayHost, fmt.Sprintf("%d", relayPort)))
	if err != nil {
		result.Diagnosis = "invalid_udp_relay"
		return fmt.Errorf("proxy: resolve UDP relay: %w", err)
	}

	localNetwork := "udp4"
	if relayAddress.IP != nil && relayAddress.IP.To4() == nil {
		localNetwork = "udp6"
	}
	udpConnection, err := net.ListenUDP(localNetwork, nil)
	if err != nil {
		result.Diagnosis = "udp_socket_failed"
		return fmt.Errorf("proxy: open UDP probe socket: %w", err)
	}
	defer udpConnection.Close()

	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := udpConnection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("proxy: set UDP probe deadline: %w", err)
	}

	query, queryID, err := buildDNSQuery(dnsName)
	if err != nil {
		result.Diagnosis = "invalid_dns_name"
		return err
	}
	datagram, err := buildSOCKSUDPDatagram(dnsAddress, query)
	if err != nil {
		result.Diagnosis = "invalid_dns_target"
		return err
	}
	startedAt := time.Now()
	if _, err := udpConnection.WriteToUDP(datagram, relayAddress); err != nil {
		result.Diagnosis = "udp_send_failed"
		return fmt.Errorf("proxy: send UDP DNS probe: %w", err)
	}

	responseBuffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			result.Diagnosis = "udp_no_roundtrip"
			return fmt.Errorf("proxy: UDP DNS probe cancelled: %w", err)
		}
		count, sender, err := udpConnection.ReadFromUDP(responseBuffer)
		if err != nil {
			result.Diagnosis = "udp_no_roundtrip"
			return fmt.Errorf("proxy: UDP DNS probe did not return: %w", err)
		}
		if !sameUDPAddress(sender, relayAddress) {
			continue
		}
		payload, err := parseSOCKSUDPDatagram(responseBuffer[:count])
		if err != nil {
			result.Diagnosis = "udp_invalid_response"
			return fmt.Errorf("proxy: parse UDP relay response: %w", err)
		}
		rcode, err := validateDNSResponse(payload, queryID)
		if err != nil {
			result.Diagnosis = "dns_invalid_response"
			return err
		}
		result.UDPExchangeOK = true
		result.DNSRCode = rcode
		result.RoundTripMS = time.Since(startedAt).Milliseconds()
		if result.RoundTripMS < 1 {
			result.RoundTripMS = 1
		}
		return nil
	}
}

func buildDNSQuery(name string) ([]byte, uint16, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || len(name) > 253 {
		return nil, 0, errors.New("proxy: UDP probe DNS name is invalid")
	}
	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, 0, fmt.Errorf("proxy: generate DNS probe ID: %w", err)
	}
	queryID := binary.BigEndian.Uint16(idBytes[:])
	query := make([]byte, 12, 12+len(name)+6)
	binary.BigEndian.PutUint16(query[0:2], queryID)
	binary.BigEndian.PutUint16(query[2:4], 0x0100)
	binary.BigEndian.PutUint16(query[4:6], 1)
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, 0, errors.New("proxy: UDP probe DNS label is invalid")
		}
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 1, 0, 1)
	return query, queryID, nil
}

func buildSOCKSUDPDatagram(target *net.UDPAddr, payload []byte) ([]byte, error) {
	if target == nil || target.IP == nil || target.Port < 1 || target.Port > 65535 {
		return nil, errors.New("proxy: UDP target is invalid")
	}
	packet := []byte{0, 0, 0}
	if ipv4 := target.IP.To4(); ipv4 != nil {
		packet = append(packet, 1)
		packet = append(packet, ipv4...)
	} else if ipv6 := target.IP.To16(); ipv6 != nil {
		packet = append(packet, 4)
		packet = append(packet, ipv6...)
	} else {
		return nil, errors.New("proxy: UDP target address family is invalid")
	}
	packet = append(packet, byte(target.Port>>8), byte(target.Port))
	packet = append(packet, payload...)
	return packet, nil
}

func parseSOCKSUDPDatagram(packet []byte) ([]byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 {
		return nil, errors.New("invalid SOCKS5 UDP header")
	}
	if packet[2] != 0 {
		return nil, errors.New("fragmented SOCKS5 UDP response is unsupported")
	}
	offset := 4
	switch packet[3] {
	case 1:
		offset += net.IPv4len
	case 3:
		if len(packet) <= offset {
			return nil, errors.New("truncated SOCKS5 UDP domain")
		}
		offset += 1 + int(packet[offset])
	case 4:
		offset += net.IPv6len
	default:
		return nil, errors.New("unsupported SOCKS5 UDP address type")
	}
	if offset+2 > len(packet) {
		return nil, errors.New("truncated SOCKS5 UDP endpoint")
	}
	offset += 2
	if offset >= len(packet) {
		return nil, errors.New("empty SOCKS5 UDP payload")
	}
	return packet[offset:], nil
}

func validateDNSResponse(payload []byte, queryID uint16) (int, error) {
	if len(payload) < 12 {
		return 0, errors.New("proxy: DNS response is truncated")
	}
	if binary.BigEndian.Uint16(payload[0:2]) != queryID {
		return 0, errors.New("proxy: DNS response ID does not match")
	}
	flags := binary.BigEndian.Uint16(payload[2:4])
	if flags&0x8000 == 0 {
		return 0, errors.New("proxy: DNS response is not a response")
	}
	rcode := int(flags & 0x000f)
	if rcode != 0 {
		return rcode, fmt.Errorf("proxy: DNS probe returned response code %d", rcode)
	}
	return rcode, nil
}

func sameUDPAddress(left, right *net.UDPAddr) bool {
	if left == nil || right == nil || left.Port != right.Port {
		return false
	}
	if left.IP == nil || right.IP == nil {
		return true
	}
	return left.IP.Equal(right.IP)
}

func readSOCKSAddress(reader io.Reader, addressType byte) (string, error) {
	switch addressType {
	case 1:
		value := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		return net.IP(value).String(), nil
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil {
			return "", err
		}
		if length[0] == 0 {
			return "", errors.New("empty SOCKS5 domain")
		}
		value := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		return string(value), nil
	case 4:
		value := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		return net.IP(value).String(), nil
	default:
		return "", errors.New("unsupported SOCKS5 address type")
	}
}
