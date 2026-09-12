//go:build linux

package ims

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// Leave room below the IPv6 minimum MTU for TCP and transport-mode ESP,
// including CBC padding, IV and integrity data. Some older XFRM kernels loop
// on local Packet Too Big errors instead of shrinking protected TCP segments.
const protectedTCPMaxSegment = 1100

func configureProtectedTCPDialer(dialer *net.Dialer) {
	// Apply before connect so both outbound segmentation and the MSS advertised
	// in the SYN account for ESP overhead. A post-connect change is too late.
	dialer.Control = func(_, _ string, raw syscall.RawConn) error {
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
}
