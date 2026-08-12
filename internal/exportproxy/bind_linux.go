//go:build linux

package exportproxy

import (
	"bufio"
	"context"
	"hash/fnv"
	"net"
	"os"
	"strings"
	"syscall"
	"unicode"
)

func platformSupported() error { return nil }

func boundDialer(networkInterface string) net.Dialer {
	return net.Dialer{Control: func(_, _ string, raw syscall.RawConn) error {
		var bindError error
		err := raw.Control(func(fd uintptr) {
			if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, int(exportRouteMark(networkInterface))); err != nil {
				bindError = err
				return
			}
			bindError = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, networkInterface)
		})
		if err != nil {
			return err
		}
		return bindError
	}}
}

func exportRouteMark(networkInterface string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(networkInterface))
	return 0x56000000 | (hash.Sum32() & 0x00ffffff)
}

func boundResolver(networkInterface string) *net.Resolver {
	dialer := boundDialer(networkInterface)
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var lastError error
		for _, server := range exportRouteDNSServers(networkInterface) {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(server, "53"))
			if err == nil {
				return connection, nil
			}
			lastError = err
		}
		return nil, lastError
	}}
}

func exportRouteDNSServers(networkInterface string) []string {
	if !validInterfaceName(networkInterface) {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	root, err := os.OpenRoot("/run/vocat")
	if err != nil {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	defer root.Close()
	file, err := root.Open("cellular-" + networkInterface + ".dns")
	if err != nil {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	defer file.Close()
	servers := make([]string, 0, 2)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if value := strings.TrimSpace(scanner.Text()); net.ParseIP(value) != nil {
			servers = append(servers, value)
		}
	}
	if len(servers) == 0 {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	return servers
}

// Linux IFNAMSIZ is 16 including the terminator. Restricting names here both
// matches kernel interface names and prevents a stored device value from ever
// becoming a filesystem path component.
func validInterfaceName(value string) bool {
	if value == "" || len(value) > 15 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character > unicode.MaxASCII || !(character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}
