package ike

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestUserspaceOutboundRequiresAssignedSource(t *testing.T) {
	t.Parallel()
	config := ChildSAConfig{
		InnerLocalIPv4: net.ParseIP("10.0.0.2"),
		InnerLocalIPv6: net.ParseIP("2001:db8::2"),
	}
	tests := []struct {
		name    string
		packet  []byte
		allowed bool
	}{
		{"assigned IPv4", testIPv4UDPPacket(config.InnerLocalIPv4, net.ParseIP("10.0.0.9"), 5060, 5060, nil), true},
		{"other IPv4", testIPv4UDPPacket(net.ParseIP("192.0.2.2"), net.ParseIP("10.0.0.9"), 5060, 5060, nil), false},
		{"unspecified IPv4", testIPv4UDPPacket(net.IPv4zero, net.ParseIP("10.0.0.9"), 5060, 5060, nil), false},
		{"assigned IPv6", userspacePolicyIPv6Packet(config.InnerLocalIPv6, 17, make([]byte, 8)), true},
		{"SLAAC IPv6 in same prefix", userspacePolicyIPv6Packet(net.ParseIP("2001:db8::abcd"), 17, make([]byte, 8)), false},
		{"link local IPv6", userspacePolicyIPv6Packet(net.ParseIP("fe80::2"), 58, []byte{133, 0, 0, 0}), false},
		{"DAD unspecified IPv6", userspacePolicyIPv6Packet(net.IPv6zero, 58, []byte{135, 0, 0, 0}), false},
		{"empty", nil, false},
		{"invalid version", []byte{0x70}, false},
		{"truncated IPv4", []byte{0x45}, false},
		{"truncated IPv6", []byte{0x60}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := userspaceOutboundPacketAllowed(test.packet, config); got != test.allowed {
				t.Fatalf("allowed = %v, want %v", got, test.allowed)
			}
		})
	}

	config.InnerLocalIPv6 = nil
	if userspaceOutboundPacketAllowed(tests[3].packet, config) {
		t.Fatal("IPv6 passed without an assigned inner IPv6 address")
	}
	mapped := userspacePolicyIPv6Packet(net.ParseIP("::ffff:10.0.0.2"), 17, make([]byte, 8))
	if userspaceOutboundPacketAllowed(mapped, config) {
		t.Fatal("IPv4-mapped IPv6 source matched the assigned IPv4 address")
	}
	config.InnerLocalIPv4 = nil
	config.InnerLocalIPv6 = net.ParseIP("2001:db8::2")
	if userspaceOutboundPacketAllowed(tests[0].packet, config) {
		t.Fatal("IPv4 passed without an assigned inner IPv4 address")
	}
}

func TestUserspaceInboundBlocksIPv6RouterConfiguration(t *testing.T) {
	t.Parallel()
	for _, extensions := range [][]uint8{
		nil,
		{0},
		{43},
		{60},
		{51},
		{0, 43, 60, 51, 60},
	} {
		for _, messageType := range []byte{1, 2, 3, 4, 128, 129, 134, 135, 136, 137} {
			packet := userspacePolicyIPv6WithExtensions(58, []byte{messageType, 0, 0, 0}, extensions...)
			want := messageType != 134 && messageType != 137
			if got := userspaceInboundPacketAllowed(packet); got != want {
				t.Fatalf("extensions %v, ICMPv6 type %d: allowed = %v, want %v", extensions, messageType, got, want)
			}
		}
	}
	for _, protocol := range []byte{6, 17, 50, 59} {
		packet := userspacePolicyIPv6WithExtensions(protocol, []byte{134, 137, 0, 0}, 0, 60)
		if !userspaceInboundPacketAllowed(packet) {
			t.Fatalf("non-ICMPv6 protocol %d was rejected", protocol)
		}
	}
	packet := testIPv4UDPPacket(net.ParseIP("10.0.0.9"), net.ParseIP("10.0.0.2"), 5060, 5060, []byte{134, 137})
	if !userspaceInboundPacketAllowed(packet) {
		t.Fatal("IPv4 user traffic was rejected")
	}
}

func TestUserspaceInboundInspectsFirstIPv6Fragment(t *testing.T) {
	t.Parallel()
	for _, messageType := range []byte{2, 128, 134, 137} {
		for _, more := range []bool{false, true} {
			fragment := userspacePolicyFragment(58, 0, more, []byte{messageType, 0, 0, 0})
			packet := userspacePolicyIPv6WithExtensions(44, fragment, 0, 60)
			want := messageType != 134 && messageType != 137
			if got := userspaceInboundPacketAllowed(packet); got != want {
				t.Fatalf("ICMPv6 type %d, more %v: allowed = %v, want %v", messageType, more, got, want)
			}
		}
	}
	// Fragmentable Destination Options still lead to the ICMPv6 type in the
	// offset-zero fragment, including for atomic fragments.
	for _, messageType := range []byte{2, 128, 134, 137} {
		options := append([]byte{58, 0, 0, 0, 0, 0, 0, 0}, messageType, 0, 0, 0)
		packet := userspacePolicyIPv6WithExtensions(44, userspacePolicyFragment(60, 0, true, options))
		want := messageType != 134 && messageType != 137
		if got := userspaceInboundPacketAllowed(packet); got != want {
			t.Fatalf("fragmentable options, ICMPv6 type %d: allowed = %v, want %v", messageType, got, want)
		}
	}
	// A later fragment's data is opaque. Its leading bytes can look like an
	// ICMPv6 type or malformed extension without representing those headers.
	for _, protocol := range []byte{6, 17, 58, 60} {
		packet := userspacePolicyIPv6WithExtensions(44, userspacePolicyFragment(protocol, 1, false, []byte{134, 255, 0, 0}))
		if !userspaceInboundPacketAllowed(packet) {
			t.Fatalf("noninitial fragment for protocol %d was rejected", protocol)
		}
	}
}

func TestUserspaceInboundRejectsIncompleteIPv6HeaderChains(t *testing.T) {
	t.Parallel()
	tests := map[string][]byte{
		"empty":                       nil,
		"unknown IP version":          {0x70},
		"truncated IPv6":              {0x60},
		"missing ICMPv6 type":         userspacePolicyIPv6WithExtensions(58, nil),
		"truncated extension":         userspacePolicyIPv6WithExtensions(60, []byte{58}),
		"oversized extension":         userspacePolicyIPv6WithExtensions(60, []byte{58, 255, 0, 0, 0, 0, 0, 0}),
		"truncated AH":                userspacePolicyIPv6WithExtensions(51, []byte{58}),
		"oversized AH":                userspacePolicyIPv6WithExtensions(51, []byte{58, 255, 0, 0, 0, 0, 0, 0}),
		"truncated fragment":          userspacePolicyIPv6WithExtensions(44, []byte{58}),
		"first fragment without type": userspacePolicyIPv6WithExtensions(44, userspacePolicyFragment(58, 0, true, nil)),
		"split extension chain":       userspacePolicyIPv6WithExtensions(44, userspacePolicyFragment(60, 0, true, []byte{58, 1, 0, 0, 0, 0, 0, 0})),
		"duplicate fragment headers":  userspacePolicyIPv6WithExtensions(44, userspacePolicyFragment(44, 0, true, userspacePolicyFragment(58, 0, true, []byte{128, 0, 0, 0}))),
	}
	for name, packet := range tests {
		t.Run(name, func(t *testing.T) {
			if userspaceInboundPacketAllowed(packet) {
				t.Fatal("incomplete or malformed IPv6 header chain was accepted")
			}
		})
	}
	packet := userspacePolicyIPv6WithExtensions(58, []byte{128, 0, 0, 0})
	binary.BigEndian.PutUint16(packet[4:6], 100)
	if userspaceInboundPacketAllowed(packet) {
		t.Fatal("truncated IPv6 payload was accepted")
	}
}

func userspacePolicyIPv6WithExtensions(protocol uint8, payload []byte, extensions ...uint8) []byte {
	for index := len(extensions) - 1; index >= 0; index-- {
		header := make([]byte, 8)
		if extensions[index] == 51 {
			header = make([]byte, 12)
			header[1] = 1
		}
		header[0] = protocol
		payload = append(header, payload...)
		protocol = extensions[index]
	}
	return userspacePolicyIPv6Packet(net.ParseIP("2001:db8::9"), protocol, payload)
}

func userspacePolicyIPv6Packet(source net.IP, protocol uint8, payload []byte) []byte {
	packet := make([]byte, 40+len(payload))
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(payload)))
	packet[6] = protocol
	packet[7] = 255
	copy(packet[8:24], source.To16())
	copy(packet[24:40], net.ParseIP("2001:db8::2").To16())
	copy(packet[40:], payload)
	return packet
}

func userspacePolicyFragment(protocol uint8, offset uint16, more bool, payload []byte) []byte {
	header := make([]byte, 8)
	header[0] = protocol
	bits := offset << 3
	if more {
		bits |= 1
	}
	binary.BigEndian.PutUint16(header[2:4], bits)
	binary.BigEndian.PutUint32(header[4:8], 0x12345678)
	return append(header, payload...)
}
