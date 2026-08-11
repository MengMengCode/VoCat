//go:build linux

package ike

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	userspaceTunnelMTU         = 1380
	maxUserspaceSelectorRoutes = 2048
	tunReadPollInterval        = 100 * time.Millisecond
)

type linuxUserspaceInstaller struct {
	ipCommand string
}

type linuxUserspaceHandle struct {
	ipCommand string
	config    ChildSAConfig
	tunnel    *espTunnel
	tun       *os.File
	relay     NATTPacketRelay

	runContext context.Context
	cancel     context.CancelFunc
	wait       sync.WaitGroup
	cancelOnce sync.Once
	closeOnce  sync.Once
	closeMu    sync.Mutex

	mu          sync.Mutex
	closed      bool
	terminalErr error
	failures    chan error
	cleanup     []ipCleanupCommand

	cleanupExecutor func(context.Context, ...string) ([]byte, error)
}

type ipCleanupCommand struct {
	operation string
	arguments []string
	phase     int
}

const (
	cleanupSelectorRule  = 10
	cleanupLinkDown      = 20
	cleanupAddress       = 30
	cleanupSelectorRoute = 40
	cleanupFailRule      = 50
	cleanupFailRoute     = 60
)

func (*linuxUserspaceHandle) DataplaneMode() string { return "userspace" }

func (installer linuxUserspaceInstaller) Install(
	ctx context.Context,
	config ChildSAConfig,
) (ChildSAHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.Relay == nil {
		return nil, errors.New("ike: user-space ESP requires a NAT-T packet relay")
	}
	if !config.UDPEncapsulation {
		return nil, errors.New("ike: user-space ESP relay requires negotiated UDP encapsulation")
	}
	if len(config.PCSCF) == 0 {
		return nil, errors.New("ike: user-space ESP requires at least one negotiated P-CSCF address")
	}
	if err := validateUserspaceRoutes(config); err != nil {
		return nil, err
	}
	command := strings.TrimSpace(installer.ipCommand)
	if command == "" {
		command = "ip"
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, errors.New("Linux iproute2 is required to configure the user-space CHILD_SA")
	}
	tunnel, err := newESPTunnel(config, nil)
	if err != nil {
		return nil, err
	}
	tun, actualName, err := openLinuxTUN(config.Name)
	if err != nil {
		return nil, err
	}
	config.Name = actualName
	runContext, cancel := context.WithCancel(context.Background())
	handle := &linuxUserspaceHandle{
		ipCommand:  command,
		config:     cloneChildSAConfig(config),
		tunnel:     tunnel,
		tun:        tun,
		relay:      config.Relay,
		runContext: runContext,
		cancel:     cancel,
		failures:   make(chan error, 1),
	}
	if err := handle.configure(ctx); err != nil {
		cancel()
		handle.cleanupNetwork(context.Background())
		_ = tun.Close()
		return nil, err
	}
	handle.wait.Add(2)
	go handle.copyTUNToRelay()
	go handle.copyRelayToTUN()
	return handle, nil
}

func openLinuxTUN(name string) (*os.File, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", errors.New("ike: TUN interface name is required")
	}
	request, err := unix.NewIfreq(name)
	if err != nil {
		return nil, "", fmt.Errorf("ike: invalid TUN interface name: %w", err)
	}
	request.SetUint16(uint16(unix.IFF_TUN | unix.IFF_NO_PI))
	descriptor, err := unix.Open(
		"/dev/net/tun",
		unix.O_RDWR|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, "", fmt.Errorf("ike: open /dev/net/tun: %w", err)
	}
	if err := unix.IoctlIfreq(descriptor, unix.TUNSETIFF, request); err != nil {
		_ = unix.Close(descriptor)
		return nil, "", fmt.Errorf("ike: create TUN interface: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), "/dev/net/tun:"+request.Name())
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, "", errors.New("ike: create TUN file handle")
	}
	return file, request.Name(), nil
}

func validateUserspaceRoutes(config ChildSAConfig) error {
	if config.InnerLocalIPv4 == nil && config.InnerLocalIPv6 == nil {
		return errors.New("ike: user-space ESP requires an assigned inner address")
	}
	validLocal := func(ip net.IP) bool {
		return ip != nil &&
			!ip.IsUnspecified() &&
			!ip.IsMulticast() &&
			ipAllowedBySelectors(ip, config.InitiatorSelectors)
	}
	if config.InnerLocalIPv4 != nil && !validLocal(config.InnerLocalIPv4) {
		return errors.New("ike: assigned inner IPv4 address is outside initiator traffic selectors")
	}
	if config.InnerLocalIPv6 != nil && !validLocal(config.InnerLocalIPv6) {
		return errors.New("ike: assigned inner IPv6 address is outside initiator traffic selectors")
	}
	matchingFamily := false
	for _, pcscf := range config.PCSCF {
		if pcscf == nil || pcscf.IsUnspecified() || pcscf.IsMulticast() {
			return errors.New("ike: P-CSCF address is invalid")
		}
		if !ipAllowedBySelectors(pcscf, config.ResponderSelectors) {
			return fmt.Errorf("ike: P-CSCF %s is outside responder traffic selectors", pcscf)
		}
		if (pcscf.To4() != nil && config.InnerLocalIPv4 != nil) ||
			(pcscf.To4() == nil && pcscf.To16() != nil && config.InnerLocalIPv6 != nil) {
			matchingFamily = true
		}
	}
	if !matchingFamily {
		return errors.New("ike: no P-CSCF address matches an assigned inner address family")
	}
	return nil
}

func ipAllowedBySelectors(ip net.IP, selectors []trafficSelector) bool {
	for _, selector := range selectors {
		if ipWithinRange(ip, selector.StartIP, selector.EndIP) {
			return true
		}
	}
	return false
}

func (handle *linuxUserspaceHandle) configure(ctx context.Context) error {
	name := handle.config.Name
	// The link comes up before any route names it as an output device. The
	// 410's 5.15 kernel rejects "route add <prefix> dev <tun>" with "Device for
	// nexthop is not up" while the TUN is down, so staging selector routes
	// first is not portable. Bringing the link up carries no leak risk on its
	// own: the interface has no address and nothing routes to it until the
	// policy tables and both fail-closed rules below are in place.
	if err := handle.run(
		ctx,
		"enable TUN interface",
		"link", "set", "dev", name,
		"mtu", strconv.Itoa(userspaceTunnelMTU),
		"up",
	); err != nil {
		return err
	}
	handle.recordCleanupAt(cleanupLinkDown, "disable TUN interface", "link", "set", "dev", name, "down")

	table, priority := userspaceRoutingIdentifiers(handle.config.InboundSPI)
	if handle.config.InnerLocalIPv4 != nil {
		prefixes, err := selectorRoutePrefixes(handle.config.ResponderSelectors, false)
		if err != nil {
			return err
		}
		if err := handle.configureFamily(ctx, "-4", handle.config.InnerLocalIPv4, prefixes, 32, table, priority); err != nil {
			return err
		}
	}
	if handle.config.InnerLocalIPv6 != nil {
		prefixes, err := selectorRoutePrefixes(handle.config.ResponderSelectors, true)
		if err != nil {
			return err
		}
		if err := handle.configureFamily(ctx, "-6", handle.config.InnerLocalIPv6, prefixes, 128, table, priority); err != nil {
			return err
		}
	}

	// Policy tables and both fail-closed rules are complete before an inner
	// address becomes usable, preventing a setup-time fall-through to main.
	if handle.config.InnerLocalIPv4 != nil {
		address := handle.config.InnerLocalIPv4.String() + "/32"
		if err := handle.run(
			ctx,
			"assign TUN IPv4 address",
			"-4", "address", "add",
			address,
			"dev", name,
			"noprefixroute",
		); err != nil {
			return err
		}
		handle.recordCleanupAt(cleanupAddress, "remove TUN IPv4 address", "-4", "address", "delete", address, "dev", name)
	}
	if handle.config.InnerLocalIPv6 != nil {
		prefix := handle.config.InnerIPv6Prefix
		if prefix == 0 || prefix > 128 {
			prefix = 128
		}
		address := fmt.Sprintf("%s/%d", handle.config.InnerLocalIPv6.String(), prefix)
		if err := handle.run(
			ctx,
			"assign TUN IPv6 address",
			"-6", "address", "add",
			address,
			"dev", name,
			"noprefixroute",
		); err != nil {
			return err
		}
		handle.recordCleanupAt(cleanupAddress, "remove TUN IPv6 address", "-6", "address", "delete", address, "dev", name)
	}
	return nil
}

func (handle *linuxUserspaceHandle) configureFamily(
	ctx context.Context,
	family string,
	local net.IP,
	selectorPrefixes []string,
	bits int,
	table uint32,
	priority uint32,
) error {
	tableValue := strconv.FormatUint(uint64(table), 10)
	priorityValue := strconv.FormatUint(uint64(priority), 10)
	failClosedTable, failClosedPriority := userspaceFailClosedRoutingIdentifiers(table, priority)
	failClosedTableValue := strconv.FormatUint(uint64(failClosedTable), 10)
	failClosedPriorityValue := strconv.FormatUint(uint64(failClosedPriority), 10)
	localPrefix := fmt.Sprintf("%s/%d", local.String(), bits)
	if err := handle.requireUnusedRoutingSlot(
		ctx,
		family,
		tableValue,
		priorityValue,
	); err != nil {
		return err
	}
	if err := handle.requireUnusedRoutingSlot(
		ctx,
		family,
		failClosedTableValue,
		failClosedPriorityValue,
	); err != nil {
		return err
	}
	unreachableArguments := []string{
		family, "route", "add",
		"table", failClosedTableValue,
		"unreachable", "default",
	}
	if err := handle.run(ctx, "install fail-closed route", unreachableArguments...); err != nil {
		return err
	}
	handle.recordCleanupAt(
		cleanupFailRoute,
		"remove fail-closed route",
		family, "route", "delete",
		"table", failClosedTableValue,
		"unreachable", "default",
	)

	// Selector routes are staged before either rule becomes reachable. They do
	// not carry a preferred source so they can be installed while the TUN is
	// still down and before the inner address exists.
	for _, prefix := range selectorPrefixes {
		routeArguments := []string{
			family, "route", "add",
			"table", tableValue,
			prefix,
			"dev", handle.config.Name,
		}
		if err := handle.run(ctx, "install negotiated selector route", routeArguments...); err != nil {
			return err
		}
		handle.recordCleanupAt(
			cleanupSelectorRoute,
			"remove negotiated selector route",
			family, "route", "delete",
			"table", tableValue,
			prefix,
			"dev", handle.config.Name,
		)
	}

	failClosedRuleArguments := []string{
		family, "rule", "add",
		"priority", failClosedPriorityValue,
		"from", localPrefix,
		"lookup", failClosedTableValue,
	}
	if err := handle.run(ctx, "install fail-closed fallback rule", failClosedRuleArguments...); err != nil {
		return err
	}
	handle.recordCleanupAt(
		cleanupFailRule,
		"remove fail-closed fallback rule",
		family, "rule", "delete",
		"priority", failClosedPriorityValue,
		"from", localPrefix,
		"lookup", failClosedTableValue,
	)

	// The lower-priority-number selector rule is committed last. From this
	// instant a selector miss continues directly into the already-live
	// fail-closed rule, never into the host main table.
	ruleArguments := []string{
		family, "rule", "add",
		"priority", priorityValue,
		"from", localPrefix,
		"lookup", tableValue,
	}
	if err := handle.run(ctx, "install selector source rule", ruleArguments...); err != nil {
		return err
	}
	handle.recordCleanupAt(
		cleanupSelectorRule,
		"remove selector source rule",
		family, "rule", "delete",
		"priority", priorityValue,
		"from", localPrefix,
		"lookup", tableValue,
	)
	return nil
}

// selectorRoutePrefixes turns each negotiated responder IP range into the
// smallest set of CIDR routes Linux can install. Routing is deliberately
// based only on the IP portion of the selectors: protocol and port limits are
// enforced again by espTunnel.seal before a packet can leave through NAT-T.
// Addresses outside these prefixes miss the selector table and are stopped by
// the next policy rule's dedicated unreachable table, so the inner source can
// never fall through to the host's main routing table. If TSr is /0, its TUN
// default lives in the selector table while the unreachable default remains in
// the separate fallback table, avoiding a same-prefix route conflict.
func selectorRoutePrefixes(selectors []trafficSelector, ipv6 bool) ([]string, error) {
	bits := 32
	if ipv6 {
		bits = 128
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		start, ok := selectorRouteAddress(selector.StartIP, ipv6)
		if !ok {
			continue
		}
		end, ok := selectorRouteAddress(selector.EndIP, ipv6)
		if !ok {
			return nil, errors.New("ike: responder traffic selector mixes address families")
		}
		startValue := new(big.Int).SetBytes(start)
		endValue := new(big.Int).SetBytes(end)
		if startValue.Cmp(endValue) > 0 {
			return nil, errors.New("ike: responder traffic selector IP range is invalid")
		}
		for startValue.Cmp(endValue) <= 0 {
			alignedHostBits := bits
			if startValue.Sign() != 0 {
				alignedHostBits = int(startValue.TrailingZeroBits())
				if alignedHostBits > bits {
					alignedHostBits = bits
				}
			}
			remaining := new(big.Int).Sub(endValue, startValue)
			remaining.Add(remaining, big.NewInt(1))
			remainingHostBits := remaining.BitLen() - 1
			hostBits := alignedHostBits
			if remainingHostBits < hostBits {
				hostBits = remainingHostBits
			}
			prefixLength := bits - hostBits
			addressBytes := startValue.FillBytes(make([]byte, bits/8))
			prefix := net.IP(addressBytes).String() + "/" + strconv.Itoa(prefixLength)
			if _, duplicate := seen[prefix]; !duplicate {
				if len(result) >= maxUserspaceSelectorRoutes {
					return nil, errors.New("ike: responder traffic selectors require too many Linux routes")
				}
				seen[prefix] = struct{}{}
				result = append(result, prefix)
			}
			startValue.Add(startValue, new(big.Int).Lsh(big.NewInt(1), uint(hostBits)))
		}
	}
	return result, nil
}

func selectorRouteAddress(address net.IP, ipv6 bool) ([]byte, bool) {
	if ipv6 {
		if address == nil || address.To4() != nil {
			return nil, false
		}
		value := address.To16()
		return append([]byte(nil), value...), value != nil
	}
	value := address.To4()
	return append([]byte(nil), value...), value != nil
}

func userspaceRoutingIdentifiers(spi uint32) (table uint32, priority uint32) {
	table = spi
	if table <= 255 {
		table |= 0x80000000
	}
	// Linux evaluates policy rules from the lowest numeric priority upward.
	// The built-in main/default rules are 32766/32767, so a full-width SPI
	// used directly as the priority would usually run too late and leak the
	// inner source through the host's default route. Keep a SPI-derived slot
	// strictly ahead of main; requireUnusedRoutingSlot rejects collisions.
	// Reserve adjacent even/odd priorities for selector lookup followed by a
	// fail-closed fallback lookup. The latter prevents RPDB from continuing to
	// the main table if the TUN (and its routes) disappears unexpectedly.
	priority = 10000 + (spi%10000)*2
	return table, priority
}

func userspaceFailClosedRoutingIdentifiers(table, priority uint32) (uint32, uint32) {
	failClosedTable := table ^ 0x40000000
	if failClosedTable <= 255 {
		failClosedTable |= 0xc0000000
	}
	return failClosedTable, priority + 1
}

func (handle *linuxUserspaceHandle) requireUnusedRoutingSlot(
	ctx context.Context,
	family string,
	table string,
	priority string,
) error {
	routeCommand := exec.CommandContext(
		ctx,
		handle.ipCommand,
		family, "-j", "route", "show", "table", "all",
	)
	routeOutput, routeErr := routeCommand.CombinedOutput()
	if routeErr != nil {
		message := strings.TrimSpace(string(routeOutput))
		if message == "" {
			message = routeErr.Error()
		}
		return fmt.Errorf("ike: inspect routing table %s: %s", table, message)
	}
	var routes []map[string]any
	if err := json.Unmarshal(routeOutput, &routes); err != nil {
		return fmt.Errorf("ike: parse Linux routing table inventory: %w", err)
	}
	for _, route := range routes {
		value, exists := route["table"]
		if !exists {
			continue
		}
		if routingTableValue(value) == table {
			return fmt.Errorf("ike: routing table %s is already in use", table)
		}
	}

	ruleCommand := exec.CommandContext(ctx, handle.ipCommand, family, "rule", "show")
	ruleOutput, err := ruleCommand.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(ruleOutput))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ike: inspect policy rules: %s", message)
	}
	prefix := priority + ":"
	for _, line := range strings.Split(string(ruleOutput), "\n") {
		fields := strings.Fields(line)
		if strings.HasPrefix(strings.TrimSpace(line), prefix) ||
			containsAdjacentFields(fields, "lookup", table) {
			return fmt.Errorf("ike: policy rule priority %s is already in use", priority)
		}
	}
	return nil
}

func routingTableValue(value any) string {
	switch typed := value.(type) {
	case float64:
		if typed >= 0 && typed <= float64(^uint32(0)) {
			return strconv.FormatUint(uint64(typed), 10)
		}
	case string:
		return typed
	}
	return ""
}

func containsAdjacentFields(fields []string, first string, second string) bool {
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] == first && fields[index+1] == second {
			return true
		}
	}
	return false
}

func (handle *linuxUserspaceHandle) ipv4PCSCF() []net.IP {
	var result []net.IP
	seen := make(map[string]struct{})
	for _, address := range handle.config.PCSCF {
		if address.To4() != nil {
			if _, duplicate := seen[address.String()]; duplicate {
				continue
			}
			result = append(result, append(net.IP(nil), address...))
			seen[address.String()] = struct{}{}
		}
	}
	return result
}

func (handle *linuxUserspaceHandle) ipv6PCSCF() []net.IP {
	var result []net.IP
	seen := make(map[string]struct{})
	for _, address := range handle.config.PCSCF {
		if address.To4() == nil && address.To16() != nil {
			if _, duplicate := seen[address.String()]; duplicate {
				continue
			}
			result = append(result, append(net.IP(nil), address...))
			seen[address.String()] = struct{}{}
		}
	}
	return result
}

func (handle *linuxUserspaceHandle) run(
	ctx context.Context,
	operation string,
	arguments ...string,
) error {
	command := exec.CommandContext(ctx, handle.ipCommand, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ike: %s: %s", operation, message)
	}
	return nil
}

func (handle *linuxUserspaceHandle) recordCleanupAt(phase int, operation string, arguments ...string) {
	handle.cleanup = append(handle.cleanup, ipCleanupCommand{
		operation: operation,
		arguments: append([]string(nil), arguments...),
		phase:     phase,
	})
}

func (handle *linuxUserspaceHandle) copyTUNToRelay() {
	defer handle.wait.Done()
	buffer := make([]byte, 65535)
	for {
		count, err := handle.readTUNPacket(buffer)
		if err != nil {
			if handle.runContext.Err() == nil && !errors.Is(err, os.ErrClosed) {
				handle.fail(fmt.Errorf("ike: read TUN packet: %w", err))
			}
			return
		}
		protected, err := handle.tunnel.seal(buffer[:count])
		if err != nil {
			// The kernel may emit IPv6 DAD/link-local traffic when the TUN is
			// brought up, and local processes may attempt unrelated routes.
			// Traffic-selector enforcement is a filter, not a session failure.
			if errors.Is(err, errESPPolicyDrop) {
				continue
			}
			handle.fail(err)
			return
		}
		if err := handle.relay.SendESP(handle.runContext, protected); err != nil {
			if handle.runContext.Err() == nil {
				handle.fail(fmt.Errorf("ike: relay outbound ESP: %w", err))
			}
			return
		}
	}
}

func (handle *linuxUserspaceHandle) readTUNPacket(buffer []byte) (int, error) {
	descriptor := int(handle.tun.Fd())
	for {
		if err := handle.runContext.Err(); err != nil {
			return 0, err
		}
		poll := []unix.PollFd{{Fd: int32(descriptor), Events: unix.POLLIN}}
		ready, err := unix.Poll(poll, int(tunReadPollInterval/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if ready == 0 {
			continue
		}
		if err := handle.runContext.Err(); err != nil {
			return 0, err
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				return 0, os.ErrClosed
			}
			continue
		}
		count, err := unix.Read(descriptor, buffer)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		return count, err
	}
}

func (handle *linuxUserspaceHandle) copyRelayToTUN() {
	defer handle.wait.Done()
	buffer := make([]byte, 65535)
	for {
		count, err := handle.relay.ReceiveESP(handle.runContext, buffer)
		if err != nil {
			if handle.runContext.Err() == nil {
				handle.fail(fmt.Errorf("ike: relay inbound ESP: %w", err))
			}
			return
		}
		cleartext, err := handle.tunnel.open(buffer[:count])
		if err != nil {
			// Invalid ICVs, replays, malformed padding, and packets outside the
			// negotiated selectors are untrusted network input. Drop them
			// without allowing a forged datagram to tear down the CHILD_SA.
			continue
		}
		if err := writeFull(handle.tun, cleartext); err != nil {
			if handle.runContext.Err() == nil && !errors.Is(err, os.ErrClosed) {
				handle.fail(fmt.Errorf("ike: write TUN packet: %w", err))
			}
			return
		}
	}
}

func writeFull(destination io.Writer, packet []byte) error {
	count, err := destination.Write(packet)
	if err != nil {
		return err
	}
	if count != len(packet) {
		return io.ErrShortWrite
	}
	return nil
}

func (handle *linuxUserspaceHandle) fail(err error) {
	handle.mu.Lock()
	notify := false
	if handle.terminalErr == nil {
		handle.terminalErr = err
		notify = true
	}
	handle.mu.Unlock()
	if notify {
		select {
		case handle.failures <- err:
		default:
		}
	}
	handle.cancelRun()
}

func (handle *linuxUserspaceHandle) Failures() <-chan error {
	return handle.failures
}

func (handle *linuxUserspaceHandle) cancelRun() {
	handle.cancelOnce.Do(func() {
		handle.cancel()
	})
}

func (handle *linuxUserspaceHandle) closeTUN() {
	handle.closeOnce.Do(func() {
		_ = handle.tun.Close()
	})
}

func (handle *linuxUserspaceHandle) Close(ctx context.Context) error {
	// Serialize the complete shutdown sequence, not only cleanupNetwork. A
	// caller that observes closed must wait for the active Close to finish
	// waiting for the data-plane loops and updating the retained cleanup set
	// before it can decide whether a retry is required.
	handle.closeMu.Lock()
	defer handle.closeMu.Unlock()

	handle.mu.Lock()
	if handle.closed {
		hasCleanup := len(handle.cleanup) > 0
		handle.mu.Unlock()
		if hasCleanup {
			if err := handle.cleanupNetwork(ctx); err != nil {
				return err
			}
			handle.closeTUN()
		}
		return nil
	}
	handle.closed = true
	handle.mu.Unlock()

	handle.cancelRun()
	// TUN reads used to block forever after cancellation on the 410 kernel.
	// Wait for the context-aware poll loops before network cleanup, then close
	// the non-persistent descriptor so the deterministic interface name is
	// available to the next reconnect.
	handle.wait.Wait()
	cleanupErr := handle.cleanupNetwork(ctx)
	// If a safety-critical cleanup command failed, keep the descriptor and
	// fail-closed rules alive. A subsequent Close retries the retained command
	// set; only a successful teardown releases the non-persistent TUN.
	if cleanupErr == nil {
		handle.closeTUN()
	}
	// A terminal data-plane error is delivered exactly once through Failures.
	// Close reports only teardown errors so the orchestrator does not record
	// the same runtime cause again as a cleanup failure.
	return cleanupErr
}

func (handle *linuxUserspaceHandle) cleanupNetwork(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var errs []error
	guardRemovalAllowed := true
	remaining := make([]ipCleanupCommand, 0)
	// Teardown is safety ordered rather than merely reverse construction:
	// selector rules disappear first, then the link/address, while the
	// fail-closed rule and unreachable table remain until no inner-source
	// packet can be emitted. Stable ordering preserves IPv4/IPv6 determinism.
	sort.SliceStable(handle.cleanup, func(left, right int) bool {
		return handle.cleanup[left].phase < handle.cleanup[right].phase
	})
	for _, item := range handle.cleanup {
		if item.phase >= cleanupFailRule && !guardRemovalAllowed {
			remaining = append(remaining, item)
			continue
		}
		output, err := handle.executeCleanup(ctx, item.arguments...)
		if err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = err.Error()
			}
			errs = append(errs, fmt.Errorf("ike: %s: %s", item.operation, message))
			remaining = append(remaining, item)
			if item.phase == cleanupLinkDown || item.phase == cleanupAddress || item.phase == cleanupFailRule {
				guardRemovalAllowed = false
			}
		}
	}
	handle.cleanup = remaining
	return errors.Join(errs...)
}

func (handle *linuxUserspaceHandle) executeCleanup(ctx context.Context, arguments ...string) ([]byte, error) {
	if handle.cleanupExecutor != nil {
		return handle.cleanupExecutor(ctx, arguments...)
	}
	return exec.CommandContext(ctx, handle.ipCommand, arguments...).CombinedOutput()
}

var _ ChildSAInstaller = linuxUserspaceInstaller{}
var _ ChildSAHandle = (*linuxUserspaceHandle)(nil)
var _ DataplaneEvidence = (*linuxUserspaceHandle)(nil)
var _ DataplaneFailureNotifier = (*linuxUserspaceHandle)(nil)
