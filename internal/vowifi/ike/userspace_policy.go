package ike

import "encoding/binary"

// userspaceOutboundPacketAllowed constrains the host-side TUN to addresses
// assigned by IKE, even when the peer negotiated wildcard traffic selectors.
func userspaceOutboundPacketAllowed(packet []byte, config ChildSAConfig) bool {
	metadata, err := parseInnerPacket(packet)
	if err != nil {
		return false
	}
	local := config.InnerLocalIPv6
	if metadata.nextHeader == 4 {
		local = config.InnerLocalIPv4
	}
	return local != nil && metadata.source.Equal(local)
}

// userspaceInboundPacketAllowed runs after ESP authentication and selector
// validation. S2b address configuration comes from IKE, so the peer must not
// change host addresses or routes through IPv6 Router Advertisements/Redirects.
func userspaceInboundPacketAllowed(packet []byte) bool {
	if len(packet) == 0 {
		return false
	}
	if packet[0]>>4 == 4 {
		return true
	}
	if packet[0]>>4 != 6 || len(packet) < 40 {
		return false
	}
	length := 40 + int(binary.BigEndian.Uint16(packet[4:6]))
	if length > len(packet) {
		return false
	}
	packet = packet[:length]
	protocol := packet[6]
	offset := 40
	fragmented := false
	for {
		switch protocol {
		case 0, 43, 60, 51:
			if offset+2 > len(packet) {
				return false
			}
			length := (int(packet[offset+1]) + 1) * 8
			if protocol == 51 {
				length = (int(packet[offset+1]) + 2) * 4
			}
			if length < 8 || offset+length > len(packet) {
				return false
			}
			protocol = packet[offset]
			offset += length
		case 44:
			if fragmented || offset+8 > len(packet) {
				return false
			}
			fragmented = true
			// Later fragments do not contain an upper-layer header. Inspect
			// the first fragment instead, and drop it if its header chain is
			// incomplete (RFC 7113), preventing forbidden reassembly.
			if binary.BigEndian.Uint16(packet[offset+2:offset+4])&0xfff8 != 0 {
				return true
			}
			protocol = packet[offset]
			offset += 8
		case 58:
			if offset >= len(packet) {
				return false
			}
			return packet[offset] != 134 && packet[offset] != 137
		default:
			return true
		}
	}
}
