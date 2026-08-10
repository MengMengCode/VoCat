//go:build !linux

package modem

import "fmt"

func openNativeWWANATTransport(path string) (Transport, error) {
	return nil, fmt.Errorf("native WWAN AT ports are unsupported on this platform: %s", path)
}
