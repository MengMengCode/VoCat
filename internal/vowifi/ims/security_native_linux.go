//go:build linux

package ims

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/iniwex5/netlink"
	"golang.org/x/sys/unix"
)

// The iproute2 shipped on some OpenStick images cannot represent an ESP
// cipher_null key through `ip xfrm state add`: it rejects the zero-length key
// before the request reaches the kernel.  Native XFRM netlink has no such
// parser limitation and is also the path used by VoHive 1.5.2.

type nativeIPSecHandle struct {
	mu       sync.Mutex
	states   []*netlink.XfrmState
	policies []*netlink.XfrmPolicy
	closed   bool
}

func installNativeIPSec(ctx context.Context, config IPSecSAConfig) (IPSecSAHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	states, policies, err := buildNativeXFRMPlan(config)
	if err != nil {
		return nil, err
	}
	handle := &nativeIPSecHandle{
		states:   states,
		policies: policies,
	}
	for _, state := range states {
		if err := ctx.Err(); err != nil {
			_ = handle.cleanup(context.Background())
			return nil, err
		}
		if err := netlink.XfrmStateAdd(state); err != nil {
			_ = handle.cleanup(context.Background())
			return nil, fmt.Errorf("native XFRM state add: %w", err)
		}
	}
	for _, policy := range policies {
		if err := ctx.Err(); err != nil {
			_ = handle.cleanup(context.Background())
			return nil, err
		}
		if err := netlink.XfrmPolicyAdd(policy); err != nil {
			_ = handle.cleanup(context.Background())
			return nil, fmt.Errorf("native XFRM policy add: %w", err)
		}
	}
	// The kernel has its own copy.  Keep only endpoint/SPI metadata for
	// cleanup, not AKA-derived key material.
	for _, state := range states {
		if state.Auth != nil {
			zeroBytes(state.Auth.Key)
			state.Auth.Key = nil
		}
		if state.Crypt != nil {
			zeroBytes(state.Crypt.Key)
			state.Crypt.Key = nil
		}
	}
	return handle, nil
}

func (handle *nativeIPSecHandle) Close(ctx context.Context) error {
	if handle == nil {
		return nil
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return nil
	}
	handle.closed = true
	return handle.cleanup(ctx)
}

func (handle *nativeIPSecHandle) cleanup(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var cleanupErrors []error
	for index := len(handle.policies) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			break
		}
		if err := netlink.XfrmPolicyDel(handle.policies[index]); err != nil && !errors.Is(err, unix.ENOENT) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("native XFRM policy delete: %w", err))
		}
	}
	for index := len(handle.states) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			break
		}
		if err := netlink.XfrmStateDel(handle.states[index]); err != nil && !errors.Is(err, unix.ENOENT) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("native XFRM state delete: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func buildNativeXFRMPlan(config IPSecSAConfig) ([]*netlink.XfrmState, []*netlink.XfrmPolicy, error) {
	if err := validateIPSecSAConfig(config); err != nil {
		return nil, nil, err
	}
	authName, _ := xfrmAuthAlgorithm(config.AuthAlgorithm)
	encName, _ := xfrmEncryptionAlgorithm(config.EncryptionAlgorithm, nil)
	authKey := append([]byte(nil), config.IntegrityKey...)
	encKey := append([]byte(nil), config.EncryptionKey...)
	state := func(source, destination net.IP, spi uint32, reqID uint32) *netlink.XfrmState {
		return &netlink.XfrmState{
			Src:          append(net.IP(nil), source...),
			Dst:          append(net.IP(nil), destination...),
			Proto:        netlink.XFRM_PROTO_ESP,
			Mode:         netlink.XFRM_MODE_TRANSPORT,
			Spi:          int(spi),
			Reqid:        int(reqID),
			ReplayWindow: 32,
			Auth: &netlink.XfrmStateAlgo{
				Name:        authName,
				Key:         append([]byte(nil), authKey...),
				TruncateLen: 96,
			},
			Crypt: &netlink.XfrmStateAlgo{
				Name: encName,
				Key:  append([]byte(nil), encKey...),
			},
		}
	}
	states := []*netlink.XfrmState{
		state(config.LocalIP, config.RemoteIP, config.PCSCFServerSPI, clientPairReqID(config)),
		state(config.RemoteIP, config.LocalIP, config.UEClientSPI, clientPairReqID(config)),
		state(config.RemoteIP, config.LocalIP, config.UEServerSPI, serverPairReqID(config)),
		state(config.LocalIP, config.RemoteIP, config.PCSCFClientSPI, serverPairReqID(config)),
	}
	policies := make([]*netlink.XfrmPolicy, 0, 6)
	for _, flow := range xfrmFlows(config) {
		for _, protocol := range flow.protocols {
			proto, err := nativeXFRMProtocol(protocol)
			if err != nil {
				return nil, nil, err
			}
			direction, err := nativeXFRMDirection(flow.direction)
			if err != nil {
				return nil, nil, err
			}
			policies = append(policies, &netlink.XfrmPolicy{
				Src:      nativeXFRMHostNetwork(flow.templateSource),
				Dst:      nativeXFRMHostNetwork(flow.templateDestination),
				Proto:    proto,
				SrcPort:  flow.sourcePort,
				DstPort:  flow.destinationPort,
				Dir:      direction,
				Priority: 100,
				Action:   netlink.XFRM_POLICY_ALLOW,
				Tmpls: []netlink.XfrmPolicyTmpl{{
					Src:   append(net.IP(nil), flow.templateSource...),
					Dst:   append(net.IP(nil), flow.templateDestination...),
					Proto: netlink.XFRM_PROTO_ESP,
					Mode:  netlink.XFRM_MODE_TRANSPORT,
					Spi:   int(flow.spi),
					Reqid: int(flow.reqid),
				}},
			})
		}
	}
	return states, policies, nil
}

func nativeXFRMProtocol(value string) (netlink.Proto, error) {
	switch value {
	case "tcp":
		return netlink.Proto(unix.IPPROTO_TCP), nil
	case "udp":
		return netlink.Proto(unix.IPPROTO_UDP), nil
	default:
		return 0, fmt.Errorf("ims: unsupported native XFRM policy protocol %q", value)
	}
}

func nativeXFRMDirection(value string) (netlink.Dir, error) {
	switch value {
	case "in":
		return netlink.XFRM_DIR_IN, nil
	case "out":
		return netlink.XFRM_DIR_OUT, nil
	default:
		return 0, fmt.Errorf("ims: unsupported native XFRM policy direction %q", value)
	}
}

func nativeXFRMHostNetwork(ip net.IP) *net.IPNet {
	bits := 128
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
		bits = 32
	}
	return &net.IPNet{IP: append(net.IP(nil), ip...), Mask: net.CIDRMask(bits, bits)}
}
