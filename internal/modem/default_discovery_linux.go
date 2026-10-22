//go:build linux

package modem

import "time"

func NewSystemDiscoverer() Discoverer {
	return ATProbingDiscoverer{
		Base:    NewSysFSDiscoverer("/sys", "/dev"),
		Probe:   probeATPort,
		Timeout: 1500 * time.Millisecond,
	}
}
