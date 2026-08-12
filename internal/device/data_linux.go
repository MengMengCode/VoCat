//go:build linux

package device

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"vocat/internal/modem"
)

func setQMINetwork(
	ctx context.Context,
	candidate modem.Candidate,
	enabled bool,
	apn string,
	ipVersion string,
	username string,
	password string,
	authentication string,
) (NetworkResult, error) {
	effectiveIPVersion := ipVersion
	downgradeDetail := ""
	switch ipVersion {
	case "IP":
	case "IPV4V6":
		// qmi-network accepts one IP family per session and the current host
		// configuration path uses IPv4 DHCP and IPv4 policy routes. Keep the
		// default dual-stack API request working, but report the actual family.
		effectiveIPVersion = "IP"
		downgradeDetail = "requested IPV4V6; QMI data backend is IPv4 only, using IPv4"
	case "IPV6":
		return NetworkResult{}, errors.New("QMI data backend is IPv4 only; IPv6 is not supported")
	default:
		return NetworkResult{}, fmt.Errorf("unsupported QMI IP version %q", ipVersion)
	}

	qmiNetwork, err := exec.LookPath("qmi-network")
	if err != nil {
		return NetworkResult{}, fmt.Errorf("%w: install libqmi-utils to control %s", ErrDataBackendUnavailable, candidate.QMIControl)
	}
	profile, err := os.CreateTemp("", "vocat-qmi-*.conf")
	if err != nil {
		return NetworkResult{}, fmt.Errorf("create temporary QMI profile: %w", err)
	}
	profilePath := profile.Name()
	defer os.Remove(profilePath)
	profileText := "IP_TYPE=4\nPROXY=yes\n"
	if apn != "" {
		profileText = "APN=" + apn + "\n" + profileText
	}
	if username != "" {
		profileText += "APN_USER=" + shellProfileValue(username) + "\n"
	}
	if password != "" {
		profileText += "APN_PASS=" + shellProfileValue(password) + "\n"
	}
	if authentication != "" && authentication != "NONE" {
		profileText += "APN_AUTH=" + shellProfileValue(strings.ToLower(authentication)) + "\n"
	}
	if _, err := fmt.Fprint(profile, profileText); err != nil {
		_ = profile.Close()
		return NetworkResult{}, fmt.Errorf("write temporary QMI profile: %w", err)
	}
	if err := profile.Chmod(0o600); err != nil {
		_ = profile.Close()
		return NetworkResult{}, fmt.Errorf("protect temporary QMI profile: %w", err)
	}
	if err := profile.Close(); err != nil {
		return NetworkResult{}, fmt.Errorf("close temporary QMI profile: %w", err)
	}

	action := "stop"
	if enabled {
		action = "start"
	}
	command := exec.CommandContext(ctx, qmiNetwork, "--profile="+profilePath, candidate.QMIControl, action)
	output, err := command.CombinedOutput()
	detail := strings.TrimSpace(string(output))
	if err != nil {
		lowerDetail := strings.ToLower(detail)
		idempotentStop := !enabled && (strings.Contains(lowerDetail, "already stopped") ||
			strings.Contains(lowerDetail, "not started") || strings.Contains(lowerDetail, "no network"))
		idempotentStart := enabled && (strings.Contains(lowerDetail, "already started") ||
			strings.Contains(lowerDetail, "already connected"))
		if !idempotentStop && !idempotentStart {
			return NetworkResult{}, fmt.Errorf("qmi-network %s failed: %w: %s", action, err, detail)
		}
	}
	if downgradeDetail != "" {
		detail = strings.TrimSpace(downgradeDetail + "\n" + detail)
	}
	ipCommand, lookErr := exec.LookPath("ip")
	if lookErr != nil {
		return NetworkResult{}, fmt.Errorf("%w: install iproute2 to control %s", ErrDataBackendUnavailable, candidate.NetworkInterface)
	}
	linkAction := "down"
	if enabled {
		linkAction = "up"
	}
	linkOutput, linkErr := exec.CommandContext(ctx, ipCommand, "link", "set", "dev", candidate.NetworkInterface, linkAction).CombinedOutput()
	if linkErr != nil {
		return NetworkResult{}, fmt.Errorf("set %s %s: %w: %s", candidate.NetworkInterface, linkAction, linkErr, strings.TrimSpace(string(linkOutput)))
	}
	if enabled {
		busybox, busyboxErr := exec.LookPath("busybox")
		if busyboxErr != nil {
			return NetworkResult{}, fmt.Errorf("%w: busybox udhcpc is required for %s", ErrDataBackendUnavailable, candidate.NetworkInterface)
		}
		dhcpDetail, dhcpErr := configureExportProxyDHCP(ctx, busybox, ipCommand, candidate.NetworkInterface)
		if dhcpErr != nil {
			rollbackCtx, cancelRollback := context.WithTimeout(context.Background(), managerCommandCleanupTimeout)
			defer cancelRollback()
			clearExportProxyRoute(rollbackCtx, candidate.NetworkInterface)
			_, _ = exec.CommandContext(rollbackCtx, qmiNetwork, "--profile="+profilePath, candidate.QMIControl, "stop").CombinedOutput()
			_, _ = exec.CommandContext(rollbackCtx, ipCommand, "link", "set", "dev", candidate.NetworkInterface, "down").CombinedOutput()
			return NetworkResult{}, fmt.Errorf("QMI session started but protected DHCP failed: %w", dhcpErr)
		}
		detail = strings.TrimSpace(detail + "\n" + dhcpDetail)
	} else {
		clearExportProxyRoute(ctx, candidate.NetworkInterface)
		_, _ = exec.CommandContext(ctx, ipCommand, "-4", "addr", "flush", "dev", candidate.NetworkInterface, "scope", "global").CombinedOutput()
	}
	return NetworkResult{
		Enabled:       enabled,
		Backend:       "qmi",
		Interface:     candidate.NetworkInterface,
		ControlDevice: candidate.QMIControl,
		APN:           apn,
		IPVersion:     effectiveIPVersion,
		Detail:        detail,
	}, nil
}

func shellProfileValue(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// exportProxyRouteIdentity must stay in sync with the Export Proxy plugin's
// Linux socket mark. Unmarked host traffic never sees the cellular default
// route; only plugin sockets carrying this mark are policy-routed to it.
func exportProxyRouteIdentity(networkInterface string) (mark uint32, table, priority int) {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(networkInterface))
	value := hash.Sum32()
	mark = 0x56000000 | (value & 0x00ffffff)
	table = 20000 + int(value%10000)
	priority = 20000 + int(value%10000)
	return
}

func configureExportProxyDHCP(ctx context.Context, busybox, ipCommand, networkInterface string) (string, error) {
	lease, err := os.CreateTemp("", "vocat-dhcp-lease-*.env")
	if err != nil {
		return "", err
	}
	leasePath := lease.Name()
	_ = lease.Close()
	_ = os.Remove(leasePath)
	defer os.Remove(leasePath)
	script, err := os.CreateTemp("", "vocat-udhcpc-*.sh")
	if err != nil {
		return "", err
	}
	scriptPath := script.Name()
	defer os.Remove(scriptPath)
	scriptText := fmt.Sprintf(`#!/bin/sh
case "$1" in
  bound|renew)
    (umask 077; printf 'ip=%%s\nsubnet=%%s\nrouter=%%s\ndns=%%s\n' "$ip" "$subnet" "$router" "$dns" > %q)
    ;;
esac
exit 0
`, leasePath)
	if _, err := script.WriteString(scriptText); err != nil {
		_ = script.Close()
		return "", err
	}
	if err := script.Chmod(0o700); err != nil {
		_ = script.Close()
		return "", err
	}
	if err := script.Close(); err != nil {
		return "", err
	}
	output, err := exec.CommandContext(ctx, busybox, "udhcpc", "-q", "-n", "-t", "5", "-T", "3", "-i", networkInterface, "-s", scriptPath).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(output)), "address family not supported") {
			return "", fmt.Errorf("udhcpc cannot open its link-layer socket: allow AF_PACKET in the vocat systemd service RestrictAddressFamilies setting: %w", err)
		}
		return "", fmt.Errorf("udhcpc: %w: %s", err, strings.TrimSpace(string(output)))
	}
	raw, err := os.ReadFile(leasePath)
	if err != nil {
		return "", fmt.Errorf("read DHCP lease: %w", err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	address := net.ParseIP(values["ip"]).To4()
	maskIP := net.ParseIP(values["subnet"]).To4()
	if address == nil || maskIP == nil {
		return "", errors.New("DHCP returned no valid IPv4 address/subnet")
	}
	mask := net.IPMask(maskIP)
	ones, bits := mask.Size()
	if bits != 32 || ones < 0 {
		return "", errors.New("DHCP returned an invalid IPv4 subnet")
	}
	network := address.Mask(mask)
	routers := strings.Fields(values["router"])
	if len(routers) > 0 && net.ParseIP(routers[0]).To4() == nil {
		return "", errors.New("DHCP returned an invalid IPv4 gateway")
	}
	if result, addrErr := exec.CommandContext(ctx, ipCommand, "-4", "addr", "replace", fmt.Sprintf("%s/%d", address.String(), ones), "dev", networkInterface).CombinedOutput(); addrErr != nil {
		return "", fmt.Errorf("configure cellular address: %w: %s", addrErr, strings.TrimSpace(string(result)))
	}
	mark, table, priority := exportProxyRouteIdentity(networkInterface)
	clearExportProxyRoute(ctx, networkInterface)
	connectedCIDR := fmt.Sprintf("%s/%d", network.String(), ones)
	if result, routeErr := exec.CommandContext(ctx, ipCommand, "-4", "route", "replace", "table", strconv.Itoa(table), connectedCIDR, "dev", networkInterface, "scope", "link", "src", address.String()).CombinedOutput(); routeErr != nil {
		clearExportProxyRoute(ctx, networkInterface)
		return "", fmt.Errorf("install protected connected route: %w: %s", routeErr, strings.TrimSpace(string(result)))
	}
	defaultArgs := []string{"-4", "route", "replace", "table", strconv.Itoa(table), "default"}
	if len(routers) > 0 {
		defaultArgs = append(defaultArgs, "via", routers[0])
	}
	defaultArgs = append(defaultArgs, "dev", networkInterface, "onlink")
	if result, routeErr := exec.CommandContext(ctx, ipCommand, defaultArgs...).CombinedOutput(); routeErr != nil {
		clearExportProxyRoute(ctx, networkInterface)
		return "", fmt.Errorf("install protected default route: %w: %s", routeErr, strings.TrimSpace(string(result)))
	}
	markText := fmt.Sprintf("0x%x", mark)
	result, err := exec.CommandContext(ctx, ipCommand, "rule", "add", "priority", strconv.Itoa(priority), "fwmark", markText, "lookup", strconv.Itoa(table)).CombinedOutput()
	if err != nil {
		clearExportProxyRoute(ctx, networkInterface)
		return "", fmt.Errorf("install protected routing rule: %w: %s", err, strings.TrimSpace(string(result)))
	}
	if err := writeExportProxyDNS(networkInterface, strings.Fields(values["dns"])); err != nil {
		clearExportProxyRoute(ctx, networkInterface)
		return "", fmt.Errorf("publish protected DNS configuration: %w", err)
	}
	return fmt.Sprintf("protected DHCP lease %s/%d", address.String(), ones), nil
}

func exportProxyDNSPath(networkInterface string) string {
	safeName := strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			return character
		}
		return '_'
	}, networkInterface)
	return "/run/vocat/cellular-" + safeName + ".dns"
}

func writeExportProxyDNS(networkInterface string, servers []string) error {
	valid := make([]string, 0, len(servers))
	for _, server := range servers {
		if address := net.ParseIP(server); address != nil {
			valid = append(valid, address.String())
		}
	}
	if len(valid) == 0 {
		// This is used only by marked Export Proxy sockets. It never changes the
		// host resolver and is merely a fallback for carriers omitting DHCP DNS.
		valid = []string{"1.1.1.1", "8.8.8.8"}
	}
	if err := os.MkdirAll("/run/vocat", 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp("/run/vocat", ".cellular-dns-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.WriteString(strings.Join(valid, "\n") + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, exportProxyDNSPath(networkInterface))
}

func clearExportProxyRoute(ctx context.Context, networkInterface string) {
	_ = os.Remove(exportProxyDNSPath(networkInterface))
	ipCommand, err := exec.LookPath("ip")
	if err != nil {
		return
	}
	mark, table, priority := exportProxyRouteIdentity(networkInterface)
	_, _ = exec.CommandContext(ctx, ipCommand, "rule", "del", "priority", strconv.Itoa(priority), "fwmark", fmt.Sprintf("0x%x", mark), "lookup", strconv.Itoa(table)).CombinedOutput()
	_, _ = exec.CommandContext(ctx, ipCommand, "-4", "route", "flush", "table", strconv.Itoa(table)).CombinedOutput()
}

const managerCommandCleanupTimeout = 15 * time.Second
