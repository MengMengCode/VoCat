//go:build linux

package ims

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// Leave room below a 1280-byte outer path MTU for inner IPv6/TCP+timestamps,
// transport-mode ipsec-3gpp ESP, and outer SWu tunnel-mode ESP + NAT-T/SOCKS5.
// This also prevents older XFRM kernels from looping on local Packet Too Big
// errors instead of shrinking protected TCP segments.
const protectedTCPMaxSegment = 1020

func setProtectedTCPMaxSegment(raw syscall.RawConn) error {
	var optionErr error
	if err := raw.Control(func(fd uintptr) {
		optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_MAXSEG, protectedTCPMaxSegment)
	}); err != nil {
		return fmt.Errorf("ims: access protected TCP socket: %w", err)
	}
	if optionErr != nil {
		return fmt.Errorf("ims: set protected TCP maximum segment size: %w", optionErr)
	}
	return nil
}

func configureProtectedTCPDialer(dialer *net.Dialer) {
	// Apply before connect so both outbound segmentation and the MSS advertised
	// in the SYN account for ESP overhead. A post-connect change is too late.
	dialer.Control = func(_, _ string, raw syscall.RawConn) error {
		return setProtectedTCPMaxSegment(raw)
	}
}

func configureProtectedTCPListener(listenConfig *net.ListenConfig) {
	// Apply before listen so accepted inbound TCP sockets from the P-CSCF
	// inherit user_mss and advertise the conservative MSS in SYN-ACK.
	listenConfig.Control = func(_, _ string, raw syscall.RawConn) error {
		return setProtectedTCPMaxSegment(raw)
	}
}
