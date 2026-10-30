//go:build linux

package modem

func NewSystemDiscoverer() Discoverer {
	return NewSystemDiscovererWithOptions(DiscoveryOptions{})
}

func NewSystemDiscovererWithOptions(options DiscoveryOptions) Discoverer {
	return &ATProbingDiscoverer{
		Base:            NewSysFSDiscoverer("/sys", "/dev"),
		Probe:           probeATPort,
		ProbeUnknownUSB: options.ProbeUnknownUSB,
	}
}
