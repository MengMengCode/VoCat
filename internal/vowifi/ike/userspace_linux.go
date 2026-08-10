//go:build linux

package ike

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	userspaceTunnelMTU  = 1380
	tunReadPollInterval = 100 * time.Millisecond
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

	mu          sync.Mutex
	closed      bool
	terminalErr error
	failures    chan error
	cleanup     []ipCleanupCommand
}

type ipCleanupCommand struct {
	operation string
	arguments []string
}

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
	if handle.config.InnerLocalIPv4 != nil {
		if err := handle.run(
			ctx,
			"assign TUN IPv4 address",
			"-4", "address", "add",
			handle.config.InnerLocalIPv4.String()+"/32",
			"dev", name,
			"noprefixroute",
		); err != nil {
			return err
		}
	}
	if handle.config.InnerLocalIPv6 != nil {
		prefix := handle.config.InnerIPv6Prefix
		if prefix == 0 || prefix > 128 {
			prefix = 128
		}
		if err := handle.run(
			ctx,
			"assign TUN IPv6 address",
			"-6", "address", "add",
			fmt.Sprintf("%s/%d", handle.config.InnerLocalIPv6.String(), prefix),
			"dev", name,
			"noprefixroute",
		); err != nil {
			return err
		}
	}
	if err := handle.run(
		ctx,
		"enable TUN interface",
		"link", "set", "dev", name,
		"mtu", strconv.Itoa(userspaceTunnelMTU),
		"up",
	); err != nil {
		return err
	}

	table, priority := userspaceRoutingIdentifiers(handle.config.InboundSPI)
	if handle.config.InnerLocalIPv4 != nil {
		if err := handle.configureFamily(
			ctx,
			"-4",
			handle.config.InnerLocalIPv4,
			handle.ipv4PCSCF(),
			32,
			table,
			priority,
		); err != nil {
			return err
		}
	}
	if handle.config.InnerLocalIPv6 != nil {
		if err := handle.configureFamily(
			ctx,
			"-6",
			handle.config.InnerLocalIPv6,
			handle.ipv6PCSCF(),
			128,
			table,
			priority,
		); err != nil {
			return err
		}
	}
	return nil
}

func (handle *linuxUserspaceHandle) configureFamily(
	ctx context.Context,
	family string,
	local net.IP,
	pcscf []net.IP,
	bits int,
	table uint32,
	priority uint32,
) error {
	if len(pcscf) == 0 {
		return nil
	}
	tableValue := strconv.FormatUint(uint64(table), 10)
	priorityValue := strconv.FormatUint(uint64(priority), 10)
	localPrefix := fmt.Sprintf("%s/%d", local.String(), bits)
	if err := handle.requireUnusedRoutingSlot(
		ctx,
		family,
		tableValue,
		priorityValue,
	); err != nil {
		return err
	}
	ruleArguments := []string{
		family, "rule", "add",
		"priority", priorityValue,
		"from", localPrefix,
		"lookup", tableValue,
	}
	if err := handle.run(ctx, "install fail-closed source rule", ruleArguments...); err != nil {
		return err
	}
	handle.recordCleanup(
		"remove fail-closed source rule",
		family, "rule", "delete",
		"priority", priorityValue,
		"from", localPrefix,
		"lookup", tableValue,
	)

	unreachableArguments := []string{
		family, "route", "add",
		"table", tableValue,
		"unreachable", "default",
	}
	if err := handle.run(ctx, "install fail-closed route", unreachableArguments...); err != nil {
		return err
	}
	handle.recordCleanup(
		"remove fail-closed route",
		family, "route", "delete",
		"table", tableValue,
		"unreachable", "default",
	)

	for _, address := range pcscf {
		hostPrefix := fmt.Sprintf("%s/%d", address.String(), bits)
		routeArguments := []string{
			family, "route", "add",
			"table", tableValue,
			hostPrefix,
			"dev", handle.config.Name,
			"src", local.String(),
		}
		if err := handle.run(ctx, "install P-CSCF host route", routeArguments...); err != nil {
			return err
		}
		handle.recordCleanup(
			"remove P-CSCF host route",
			family, "route", "delete",
			"table", tableValue,
			hostPrefix,
			"dev", handle.config.Name,
			"src", local.String(),
		)
	}
	return nil
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
	priority = 10000 + spi%20000
	return table, priority
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

func (handle *linuxUserspaceHandle) recordCleanup(operation string, arguments ...string) {
	handle.cleanup = append(handle.cleanup, ipCleanupCommand{
		operation: operation,
		arguments: append([]string(nil), arguments...),
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
	handle.mu.Lock()
	if handle.closed {
		handle.mu.Unlock()
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
	handle.closeTUN()
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
	for index := len(handle.cleanup) - 1; index >= 0; index-- {
		item := handle.cleanup[index]
		command := exec.CommandContext(ctx, handle.ipCommand, item.arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = err.Error()
			}
			errs = append(errs, fmt.Errorf("ike: %s: %s", item.operation, message))
		}
	}
	handle.cleanup = nil
	return errors.Join(errs...)
}

var _ ChildSAInstaller = linuxUserspaceInstaller{}
var _ ChildSAHandle = (*linuxUserspaceHandle)(nil)
var _ DataplaneEvidence = (*linuxUserspaceHandle)(nil)
var _ DataplaneFailureNotifier = (*linuxUserspaceHandle)(nil)
