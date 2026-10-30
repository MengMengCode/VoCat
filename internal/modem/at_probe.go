package modem

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	defaultATProbeTimeout = 1500 * time.Millisecond
	defaultATProbeBudget  = 4500 * time.Millisecond
)

// DiscoveryOptions controls probing that can change a serial port's settings.
type DiscoveryOptions struct {
	ProbeUnknownUSB bool
}

type ATProbeFunc func(context.Context, Port, time.Duration) error

type ATProbingDiscoverer struct {
	Base            Discoverer
	Probe           ATProbeFunc
	Timeout         time.Duration
	ProbeBudget     time.Duration
	ProbeUnknownUSB bool
	mu              sync.Mutex
	nextCandidate   int
	nextPorts       map[atProbeCursorKey]int
}

type atProbeCursorKey struct{ id, vendorID, productID, generation string }

func (discoverer *ATProbingDiscoverer) Discover(ctx context.Context) ([]Candidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if discoverer.Base == nil {
		return nil, errors.New("modem: AT probing discoverer requires a base discoverer")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for !discoverer.mu.TryLock() {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	defer discoverer.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates, err := discoverer.Base.Discover(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := discoverer.Timeout
	if timeout <= 0 {
		timeout = defaultATProbeTimeout
	}
	if discoverer.Probe == nil {
		return nil, errors.New("modem: AT probing discoverer requires a probe function")
	}
	budget := discoverer.ProbeBudget
	if budget <= 0 {
		budget = defaultATProbeBudget
	}
	scanCtx, cancelScan := context.WithTimeout(ctx, budget)
	defer cancelScan()
	scanDeadline, _ := scanCtx.Deadline()
	attempted := false

	previousPorts := discoverer.nextPorts
	discoverer.nextPorts = make(map[atProbeCursorKey]int, len(candidates))
	result := make([]Candidate, len(candidates))
	retained := make([]bool, len(candidates))
	start := discoverer.nextCandidate % max(1, len(candidates))
	discoverer.nextCandidate = (start + 1) % max(1, len(candidates))
	for offset := range candidates {
		index := (start + offset) % len(candidates)
		candidate := candidates[index]
		retain := func() { result[index], retained[index] = candidate, true }
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !candidate.ATProbeRequired {
			retain()
			continue
		}
		if !discoverer.ProbeUnknownUSB && !supportedUSBATProbe(candidate) {
			continue
		}

		ports := append([]Port(nil), candidate.Ports...)
		sort.Slice(ports, func(i, j int) bool {
			if ports[i].InterfaceNumber != ports[j].InterfaceNumber {
				return ports[i].InterfaceNumber < ports[j].InterfaceNumber
			}
			return ports[i].Name < ports[j].Name
		})
		if len(ports) == 0 && candidate.HasATPort() {
			ports = append(ports, candidate.ATPort)
		}
		key := atProbeCursorKey{candidate.ID, candidate.VendorID, candidate.ProductID, candidate.USBGeneration}
		startPort := previousPorts[key] % max(1, len(ports))
		discoverer.nextPorts[key] = startPort

		responsive := false
		busy := false
		// An existing session outranks rotation, including after the scan budget
		// expires. Checking the in-process lease never opens the serial device.
		for portIndex, port := range ports {
			if port.OpenPath() == "" {
				continue
			}
			release, available := tryAcquireATPort(port.OpenPath())
			if available {
				release()
				continue
			}
			candidate.ATPort, busy = port, true
			discoverer.nextPorts[key] = portIndex
			break
		}
		for portOffset := 0; !busy && portOffset < len(ports); portOffset++ {
			portIndex := (startPort + portOffset) % len(ports)
			port := ports[portIndex]
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if scanCtx.Err() != nil {
				break
			}
			fullWindow := !attempted || time.Until(scanDeadline) >= min(timeout, budget)
			attempted = true
			discoverer.nextPorts[key] = (portIndex + 1) % len(ports)
			probeCtx, cancelProbe := context.WithTimeout(scanCtx, timeout)
			probeErr := discoverer.Probe(probeCtx, port, timeout)
			cancelProbe()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if probeErr != nil && scanCtx.Err() != nil && !fullWindow {
				// Retry a port that only received the tail of this scan's budget.
				discoverer.nextPorts[key] = portIndex
			}
			if probeErr == nil {
				discoverer.nextPorts[key] = portIndex
				candidate.ATPort = port
				candidate.ATProbeRequired = false
				candidate.ATProbeBusy = false
				candidate.ATProbeDeferred = false
				if candidate.DiscoveryIssue == "at_port_missing" || candidate.DiscoveryIssue == "at_probe_pending" {
					candidate.DiscoveryIssue = ""
				}
				responsive = true
				break
			}
			if errors.Is(probeErr, ErrATPortBusy) {
				discoverer.nextPorts[key] = portIndex
				candidate.ATPort = port
				busy = true
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if responsive {
			retain()
			continue
		}
		if busy {
			candidate.ATProbeBusy = true
			if candidate.DiscoveryIssue == "" {
				candidate.DiscoveryIssue = "at_probe_pending"
			}
			retain()
			continue
		}
		if scanCtx.Err() != nil {
			candidate.ATProbeRequired = true
			candidate.ATProbeDeferred = true
			candidate.DiscoveryIssue = "at_probe_pending"
			retain()
			continue
		}
		if candidate.QMIControl != "" {
			candidate.ATProbeRequired = false
			retain()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	present := result[:0]
	for index, candidate := range result {
		if retained[index] {
			present = append(present, candidate)
		}
	}
	return present, nil
}

func supportedUSBATProbe(candidate Candidate) bool {
	return IsZXICFamily(candidate.VendorID, candidate.ProductID) ||
		IsDJI4GUSB(candidate.VendorID, candidate.ProductID) ||
		IsQuectelUSBModem(candidate.VendorID) || IsML307(candidate)
}

func probeATPort(ctx context.Context, port Port, timeout time.Duration) error {
	return probeATPortWithOpener(ctx, port, timeout, SerialOpener{})
}

func probeATPortWithOpener(ctx context.Context, port Port, timeout time.Duration, opener Opener) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opener == nil {
		return errors.New("modem: AT probe requires an opener")
	}
	if timeout <= 0 {
		timeout = defaultATProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := opener.Open(probeCtx, port)
	if err != nil {
		return err
	}
	defer client.Close()

	response, err := client.Execute(probeCtx, "AT")
	if err != nil {
		return err
	}
	if !response.OK() {
		return fmt.Errorf("modem: AT probe rejected: %s", response.Final)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _ = client.Execute(probeCtx, "AT+CGSN")
	// The identity query is optional; its deadline does not invalidate a
	// successful AT response. Discover checks its caller's cancellation.
	return nil
}
