package modem

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

const defaultATProbeTimeout = 1500 * time.Millisecond

type ATProbeFunc func(context.Context, Port, time.Duration) error

type ATProbingDiscoverer struct {
	Base    Discoverer
	Probe   ATProbeFunc
	Timeout time.Duration
}

func (discoverer ATProbingDiscoverer) Discover(ctx context.Context) ([]Candidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if discoverer.Base == nil {
		return nil, errors.New("modem: AT probing discoverer requires a base discoverer")
	}
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

	result := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !candidate.ATProbeRequired {
			result = append(result, candidate)
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

		responsive := false
		busy := false
		for _, port := range ports {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			probeErr := discoverer.Probe(ctx, port, timeout)
			if probeErr == nil {
				candidate.ATPort = port
				candidate.ATProbeRequired = false
				candidate.ATProbeBusy = false
				responsive = true
				break
			}
			if errors.Is(probeErr, ErrATPortBusy) {
				busy = true
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if responsive {
			result = append(result, candidate)
			continue
		}
		if busy {
			candidate.ATProbeBusy = true
			result = append(result, candidate)
			continue
		}
		if candidate.QMIControl != "" {
			candidate.ATProbeRequired = false
			result = append(result, candidate)
		}
	}
	return result, nil
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
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
