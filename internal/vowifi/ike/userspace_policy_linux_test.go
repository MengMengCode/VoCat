//go:build linux

package ike

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type userspacePolicyRelay struct {
	outbound chan []byte
	inbound  chan []byte
}

func (relay *userspacePolicyRelay) SendESP(ctx context.Context, packet []byte) error {
	select {
	case relay.outbound <- bytes.Clone(packet):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (relay *userspacePolicyRelay) ReceiveESP(ctx context.Context, buffer []byte) (int, error) {
	select {
	case packet := <-relay.inbound:
		if len(packet) > len(buffer) {
			return 0, fmt.Errorf("test ESP packet exceeds receive buffer")
		}
		return copy(buffer, packet), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func TestLinuxUserspacePolicyOutbound(t *testing.T) {
	handle, relay, peer, socket := userspacePolicyIntegrationSetup(t)
	packets := [][]byte{
		testIPv4UDPPacket(net.ParseIP("192.0.2.20"), handle.config.PCSCF[0], 42000, 5060, []byte("foreign-v4")),
		userspacePolicyIPv6UDP(net.ParseIP("2001:db8:feed::20"), handle.config.PCSCF[1], []byte("slaac")),
		userspacePolicyIPv6UDP(net.ParseIP("fe80::20"), handle.config.PCSCF[1], []byte("link-local")),
		userspacePolicyIPv6UDP(net.IPv6unspecified, handle.config.PCSCF[1], []byte("unspecified")),
		testIPv4UDPPacket(handle.config.InnerLocalIPv4, handle.config.PCSCF[0], 42000, 5060, []byte("assigned-v4")),
		userspacePolicyIPv6UDP(handle.config.InnerLocalIPv6, handle.config.PCSCF[1], []byte("assigned-v6")),
	}
	for _, packet := range packets {
		protocol := unix.ETH_P_IPV6
		if packet[0]>>4 == 4 {
			protocol = unix.ETH_P_IP
		}
		if err := unix.Sendto(socket, packet, 0, &unix.SockaddrLinklayer{
			Ifindex:  userspacePolicyInterfaceIndex(t, handle.config.Name),
			Protocol: userspacePolicyNetworkOrder(uint16(protocol)),
		}); err != nil {
			t.Fatalf("send packet through kernel TUN egress: %v", err)
		}
	}

	seen := make(map[string]bool)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(seen) < 2 {
		select {
		case protected := <-relay.outbound:
			packet, err := peer.open(protected)
			if err != nil {
				t.Fatalf("peer decrypt outbound ESP: %v", err)
			}
			metadata, err := parseInnerPacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			if !metadata.source.Equal(handle.config.InnerLocalIPv4) && !metadata.source.Equal(handle.config.InnerLocalIPv6) {
				t.Fatalf("unassigned source %s escaped into encrypted IMS traffic", metadata.source)
			}
			for _, marker := range []string{"assigned-v4", "assigned-v6"} {
				if bytes.HasSuffix(packet, []byte(marker)) {
					seen[marker] = true
				}
			}
		case <-deadline.C:
			t.Fatalf("assigned-address packets did not pass the live TUN/ESP loop: %v", seen)
		}
	}
	userspacePolicyAssertRunning(t, handle)
}

func TestLinuxUserspacePolicyInbound(t *testing.T) {
	handle, relay, peer, socket := userspacePolicyIntegrationSetup(t)
	beforeAddresses := userspacePolicyIPCommand(t, "-6", "address", "show", "dev", handle.config.Name)
	beforeRoutes := userspacePolicyIPCommand(t, "-6", "route", "show", "table", "main", "dev", handle.config.Name)
	ra := userspacePolicyRouterAdvertisement()
	redirect := userspacePolicyRedirect(handle.config.InnerLocalIPv6)
	marker := userspacePolicyIPv6UDP(handle.config.PCSCF[1], handle.config.InnerLocalIPv6, []byte("inbound-marker"))
	for _, packet := range [][]byte{ra, redirect, marker} {
		protected, err := peer.seal(packet)
		if err != nil {
			t.Fatalf("peer encrypt inbound packet: %v", err)
		}
		relay.inbound <- protected
	}

	deadline := time.Now().Add(5 * time.Second)
	buffer := make([]byte, 65535)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("valid inbound UDP did not reach the kernel through the live ESP/TUN loop")
		}
		poll := []unix.PollFd{{Fd: int32(socket), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, int(remaining.Milliseconds())+1); err != nil {
			if err == unix.EINTR {
				continue
			}
			t.Fatal(err)
		}
		count, address, err := unix.Recvfrom(socket, buffer, unix.MSG_DONTWAIT)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		linkAddress, ok := address.(*unix.SockaddrLinklayer)
		if !ok || linkAddress.Pkttype == unix.PACKET_OUTGOING {
			continue
		}
		packet := buffer[:count]
		if bytes.Equal(packet, ra) || bytes.Equal(packet, redirect) {
			t.Fatalf("ICMPv6 type %d reached the host kernel through the live ESP/TUN loop", packet[40])
		}
		if bytes.Equal(packet, marker) {
			break
		}
	}
	if addresses := userspacePolicyIPCommand(t, "-6", "address", "show", "dev", handle.config.Name); addresses != beforeAddresses {
		t.Fatalf("inbound control traffic changed interface addresses:\nbefore %s\nafter %s", beforeAddresses, addresses)
	}
	if routes := userspacePolicyIPCommand(t, "-6", "route", "show", "table", "main", "dev", handle.config.Name); routes != beforeRoutes {
		t.Fatalf("inbound control traffic changed main-table routes:\nbefore %s\nafter %s", beforeRoutes, routes)
	}
	userspacePolicyAssertRunning(t, handle)
}

func TestLinuxUserspaceRouterAdvertisementBaseline(t *testing.T) {
	if os.Getenv("VOCAT_NETNS_TEST") != "1" {
		t.Skip("set VOCAT_NETNS_TEST=1 inside an isolated Linux network namespace")
	}
	tun, name, err := openLinuxTUN("vocat-ra-base")
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	// TUN devices have no hardware address from which the kernel can derive
	// an interface identifier. Supply a deterministic link-local identifier
	// so this reproduction also works with EUI-64 address generation.
	userspacePolicyIPCommand(t, "-6", "address", "add", "fe80::220:2/64", "dev", name, "nodad")
	userspacePolicyIPCommand(t, "link", "set", "dev", name, "up")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := writeTUNPacket(ctx, int(tun.Fd()), userspacePolicyRouterAdvertisement()); err != nil {
		t.Fatalf("inject baseline router advertisement: %v", err)
	}
	for {
		addresses := userspacePolicyIPCommand(t, "-6", "address", "show", "dev", name)
		routes := userspacePolicyIPCommand(t, "-6", "route", "show", "table", "main", "dev", name)
		if strings.Contains(addresses, "2001:db8:face:") && strings.Contains(routes, "default via fe80::220") {
			t.Log("checksum-valid RA added an autonomous IPv6 address and main-table default route before policy enforcement")
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("kernel did not accept baseline RA; run in a fresh network namespace with IPv6 autoconfiguration enabled:\naddresses %s\nroutes %s", addresses, routes)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func userspacePolicyIntegrationSetup(t *testing.T) (*linuxUserspaceHandle, *userspacePolicyRelay, *espTunnel, int) {
	t.Helper()
	if os.Getenv("VOCAT_NETNS_TEST") != "1" {
		t.Skip("set VOCAT_NETNS_TEST=1 inside an isolated Linux network namespace")
	}
	relay := &userspacePolicyRelay{outbound: make(chan []byte, 64), inbound: make(chan []byte, 64)}
	selectors := []trafficSelector{
		{StartPort: 0, EndPort: 65535, StartIP: net.IPv4zero, EndIP: net.IPv4bcast},
		{StartPort: 0, EndPort: 65535, StartIP: net.IPv6zero, EndIP: net.ParseIP("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")},
	}
	config := ChildSAConfig{
		Name: "vocat-policy", InnerLocalIPv4: net.ParseIP("192.0.2.2"), InnerLocalIPv6: net.ParseIP("2001:db8:220::2"),
		PCSCF:      []net.IP{net.ParseIP("192.0.2.9"), net.ParseIP("2001:db8:220::9")},
		InboundSPI: 0x220101, OutboundSPI: 0x220102, Encryption: "aes-cbc-128", Integrity: "hmac-sha1-96",
		InboundEncKey: bytes.Repeat([]byte{1}, 16), OutboundEncKey: bytes.Repeat([]byte{2}, 16),
		InboundAuthKey: bytes.Repeat([]byte{3}, 20), OutboundAuthKey: bytes.Repeat([]byte{4}, 20),
		InitiatorSelectors: selectors, ResponderSelectors: selectors, UDPEncapsulation: true, Relay: relay,
	}
	installed, err := (linuxUserspaceInstaller{ipCommand: "ip"}).Install(context.Background(), config)
	if err != nil {
		t.Fatalf("install real user-space TUN: %v", err)
	}
	handle := installed.(*linuxUserspaceHandle)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := handle.Close(ctx); err != nil {
			t.Errorf("close test TUN: %v", err)
		}
	})
	peerConfig := cloneChildSAConfig(config)
	peerConfig.InboundSPI, peerConfig.OutboundSPI = config.OutboundSPI, config.InboundSPI
	peerConfig.InboundEncKey, peerConfig.OutboundEncKey = config.OutboundEncKey, config.InboundEncKey
	peerConfig.InboundAuthKey, peerConfig.OutboundAuthKey = config.OutboundAuthKey, config.InboundAuthKey
	peer, err := newESPTunnel(peerConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	socket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, int(userspacePolicyNetworkOrder(unix.ETH_P_ALL)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(socket) })
	if err := unix.Bind(socket, &unix.SockaddrLinklayer{
		Ifindex: userspacePolicyInterfaceIndex(t, config.Name), Protocol: userspacePolicyNetworkOrder(unix.ETH_P_ALL),
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for strings.Contains(userspacePolicyIPCommand(t, "-6", "address", "show", "dev", config.Name), "tentative") {
		if time.Now().After(deadline) {
			t.Fatal("test interface IPv6 duplicate address detection did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return handle, relay, peer, socket
}

func userspacePolicyAssertRunning(t *testing.T, handle *linuxUserspaceHandle) {
	t.Helper()
	select {
	case err := <-handle.Failures():
		t.Fatalf("policy drop terminated the session: %v", err)
	default:
	}
}

func userspacePolicyInterfaceIndex(t *testing.T, name string) int {
	t.Helper()
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return iface.Index
}

func userspacePolicyIPCommand(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("ip", arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func userspacePolicyNetworkOrder(value uint16) uint16 {
	var networkBytes [2]byte
	binary.BigEndian.PutUint16(networkBytes[:], value)
	return binary.NativeEndian.Uint16(networkBytes[:])
}

func userspacePolicyIPv6UDP(source, destination net.IP, payload []byte) []byte {
	packet := make([]byte, 48+len(payload))
	packet[0], packet[6], packet[7] = 0x60, 17, 64
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(packet)-40))
	copy(packet[8:24], source.To16())
	copy(packet[24:40], destination.To16())
	binary.BigEndian.PutUint16(packet[40:42], 42000)
	binary.BigEndian.PutUint16(packet[42:44], 5060)
	binary.BigEndian.PutUint16(packet[44:46], uint16(8+len(payload)))
	copy(packet[48:], payload)
	binary.BigEndian.PutUint16(packet[46:48], userspacePolicyIPv6Checksum(packet))
	return packet
}

func userspacePolicyRouterAdvertisement() []byte {
	packet := make([]byte, 88)
	packet[0], packet[6], packet[7] = 0x60, 58, 255
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(packet)-40))
	copy(packet[8:24], net.ParseIP("fe80::220").To16())
	copy(packet[24:40], net.ParseIP("ff02::1").To16())
	packet[40], packet[44] = 134, 64
	binary.BigEndian.PutUint16(packet[46:48], 1800)
	packet[56], packet[57], packet[58], packet[59] = 3, 4, 64, 0xc0
	binary.BigEndian.PutUint32(packet[60:64], 1800)
	binary.BigEndian.PutUint32(packet[64:68], 900)
	copy(packet[72:88], net.ParseIP("2001:db8:face::").To16())
	binary.BigEndian.PutUint16(packet[42:44], userspacePolicyIPv6Checksum(packet))
	return packet
}

func userspacePolicyRedirect(destination net.IP) []byte {
	packet := make([]byte, 80)
	packet[0], packet[6], packet[7] = 0x60, 58, 255
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(packet)-40))
	copy(packet[8:24], net.ParseIP("fe80::220").To16())
	copy(packet[24:40], destination.To16())
	packet[40] = 137
	copy(packet[48:64], net.ParseIP("fe80::221").To16())
	copy(packet[64:80], net.ParseIP("2001:db8:220::9").To16())
	binary.BigEndian.PutUint16(packet[42:44], userspacePolicyIPv6Checksum(packet))
	return packet
}

func userspacePolicyIPv6Checksum(packet []byte) uint16 {
	sum := uint32(len(packet)-40) + uint32(packet[6])
	for index := 8; index < 40; index += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[index : index+2]))
	}
	for index := 40; index+1 < len(packet); index += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[index : index+2]))
	}
	if len(packet)%2 != 0 {
		sum += uint32(packet[len(packet)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + sum>>16
	}
	return ^uint16(sum)
}
