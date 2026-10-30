package modem

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type probeTestDiscoverer struct {
	candidates []Candidate
	err        error
}

func (discoverer probeTestDiscoverer) Discover(context.Context) ([]Candidate, error) {
	return append([]Candidate(nil), discoverer.candidates...), discoverer.err
}

func TestATProbingDiscovererSelectsResponsiveUnknownPort(t *testing.T) {
	candidate := Candidate{
		ID:              "unknown-usb",
		ATProbeRequired: true,
		Ports: []Port{
			{Path: "/dev/ttyUSB2", Name: "ttyUSB2", InterfaceNumber: 2},
			{Path: "/dev/ttyUSB1", Name: "ttyUSB1", InterfaceNumber: 1},
		},
	}
	var probed []string
	discoverer := ATProbingDiscoverer{
		Base:            probeTestDiscoverer{candidates: []Candidate{candidate}},
		ProbeUnknownUSB: true,
		Probe: func(_ context.Context, port Port, timeout time.Duration) error {
			probed = append(probed, port.Name)
			if timeout != 1500*time.Millisecond {
				t.Errorf("probe timeout = %s, want 1.5s", timeout)
			}
			if port.Name == "ttyUSB1" {
				return errors.New("not an AT port")
			}
			return nil
		},
	}

	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !reflect.DeepEqual(probed, []string{"ttyUSB1", "ttyUSB2"}) {
		t.Fatalf("probed ports = %v, want deterministic order [ttyUSB1 ttyUSB2]", probed)
	}
	if len(got) != 1 || got[0].ATPort.Name != "ttyUSB2" {
		t.Fatalf("candidates = %#v, want responsive ttyUSB2 selected", got)
	}
}

func TestATProbingDiscovererContinuesAfterOnePortFails(t *testing.T) {
	candidate := Candidate{
		ID:              "unknown-usb",
		ATProbeRequired: true,
		Ports: []Port{
			{Path: "/dev/ttyUSB0", Name: "ttyUSB0"},
			{Path: "/dev/ttyUSB1", Name: "ttyUSB1"},
		},
	}
	attempts := 0
	discoverer := ATProbingDiscoverer{
		Base:            probeTestDiscoverer{candidates: []Candidate{candidate}},
		ProbeUnknownUSB: true,
		Probe: func(context.Context, Port, time.Duration) error {
			attempts++
			if attempts == 1 {
				return errors.New("port rejected AT")
			}
			return nil
		},
	}

	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if attempts != 2 || len(got) != 1 || got[0].ATPort.Name != "ttyUSB1" {
		t.Fatalf("attempts=%d candidates=%#v, want both ports probed and ttyUSB1 selected", attempts, got)
	}
}

func TestATProbingDiscovererDropsUnresponsiveUnknownCandidate(t *testing.T) {
	candidate := Candidate{
		ID: "unknown-usb", ATProbeRequired: true,
		Ports: []Port{{Path: "/dev/ttyUSB0", Name: "ttyUSB0"}},
	}
	discoverer := ATProbingDiscoverer{
		Base:            probeTestDiscoverer{candidates: []Candidate{candidate}},
		ProbeUnknownUSB: true,
		Probe:           func(context.Context, Port, time.Duration) error { return errors.New("not an AT port") },
	}

	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("candidates = %#v, want unresponsive non-QMI device dropped", got)
	}
}

func TestATProbingDiscovererRequiresOptInForUnknownUSB(t *testing.T) {
	unknown := Candidate{ID: "serial-adapter", VendorID: "0403", ProductID: "6001", ATProbeRequired: true,
		Ports: []Port{{Path: "/dev/ttyUSB0", Name: "ttyUSB0"}}}
	known := unknown
	known.ID, known.VendorID, known.ProductID = "supported-modem", "19d2", "0536"
	for _, enabled := range []bool{false, true} {
		var probed []string
		d := ATProbingDiscoverer{Base: probeTestDiscoverer{candidates: []Candidate{unknown, known}}, ProbeUnknownUSB: enabled,
			Probe: func(_ context.Context, port Port, _ time.Duration) error {
				probed = append(probed, port.Path)
				return nil
			}}
		got, err := d.Discover(context.Background())
		want := 1
		if enabled {
			want = 2
		}
		if err != nil || len(got) != want || len(probed) != want {
			t.Fatalf("opt-in=%v: candidates=%#v probes=%v err=%v", enabled, got, probed, err)
		}
		if !enabled && got[0].ID != known.ID {
			t.Fatalf("default discovery retained unknown USB: %#v", got)
		}
	}
}

func TestATProbingDiscovererClearsOnlyResolvedIssue(t *testing.T) {
	for _, issue := range []string{"at_port_missing", "at_probe_pending", "unrelated_issue"} {
		candidate := Candidate{ID: "modem", ATProbeRequired: true, DiscoveryIssue: issue,
			Ports: []Port{{Path: "/dev/ttyUSB0", Name: "ttyUSB0"}}}
		d := ATProbingDiscoverer{Base: probeTestDiscoverer{candidates: []Candidate{candidate}}, ProbeUnknownUSB: true,
			Probe: func(context.Context, Port, time.Duration) error { return nil }}
		got, err := d.Discover(context.Background())
		wantIssue := ""
		if issue == "unrelated_issue" {
			wantIssue = issue
		}
		if err != nil || len(got) != 1 || !got[0].HasATPort() || got[0].ATProbeRequired || got[0].DiscoveryIssue != wantIssue {
			t.Fatalf("initial issue=%q: candidates=%#v err=%v", issue, got, err)
		}
	}
}

func TestATProbingDiscovererKeepsBusyPortBeforeAnotherResponsivePort(t *testing.T) {
	first, second := Port{Path: "busy-port", Name: "ttyUSB0"}, Port{Path: "other-at-port", Name: "ttyUSB1"}
	release, ok := tryAcquireATPort(first.Path)
	if !ok {
		t.Fatal("lease active port")
	}
	defer release()
	attempts := 0
	d := ATProbingDiscoverer{Base: probeTestDiscoverer{candidates: []Candidate{{ID: "modem", ATProbeRequired: true, Ports: []Port{first, second}}}},
		ProbeUnknownUSB: true, Probe: func(_ context.Context, port Port, _ time.Duration) error {
			attempts++
			release, ok := tryAcquireATPort(port.Path)
			if !ok {
				return ErrATPortBusy
			}
			release()
			return nil
		}}
	got, err := d.Discover(context.Background())
	if err != nil || len(got) != 1 || !got[0].ATProbeBusy || got[0].ATPort != first || attempts != 0 {
		t.Fatalf("candidates=%#v attempts=%d err=%v", got, attempts, err)
	}
}

func TestATProbingDiscovererBudgetPreservesKnownAndDefersUnverifiedUSB(t *testing.T) {
	candidates := []Candidate{
		{ID: "slow", ATProbeRequired: true, Ports: []Port{{Path: "slow"}}},
		{ID: "not-yet-probed", ATProbeRequired: true, ATPort: Port{Path: "guessed-at"}, Ports: []Port{{Path: "guessed-at"}}},
		{ID: "known", VendorID: "2c7c", ATPort: Port{Path: "known-at"}},
	}
	attempts := 0
	d := ATProbingDiscoverer{Base: probeTestDiscoverer{candidates: candidates}, ProbeUnknownUSB: true,
		ProbeBudget: 30 * time.Millisecond, Probe: func(ctx context.Context, _ Port, _ time.Duration) error {
			attempts++
			<-ctx.Done()
			return ctx.Err()
		}}
	got, err := d.Discover(context.Background())
	if err != nil || len(got) != 3 || attempts != 1 || got[2].ID != "known" {
		t.Fatalf("candidates=%#v attempts=%d err=%v", got, attempts, err)
	}
	for _, candidate := range got[:2] {
		if !candidate.ATProbeDeferred || !candidate.ATProbeRequired || candidate.DiscoveryIssue != "at_probe_pending" {
			t.Fatalf("unverified candidate could be provisioned or lost: %#v", candidate)
		}
	}
}

func TestATProbingDiscovererPortTimeoutAndCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		d := ATProbingDiscoverer{Base: probeTestDiscoverer{candidates: []Candidate{{ID: "modem", ATProbeRequired: true,
			Ports: []Port{{Path: "slow"}, {Path: "responsive"}}}}}, ProbeUnknownUSB: true, Timeout: 10 * time.Millisecond,
			Probe: func(probeCtx context.Context, _ Port, _ time.Duration) error {
				attempts++
				if cancelCaller {
					cancel()
					return nil
				}
				if attempts == 1 {
					<-probeCtx.Done()
					return probeCtx.Err()
				}
				return nil
			}}
		got, err := d.Discover(ctx)
		cancel()
		if cancelCaller {
			if !errors.Is(err, context.Canceled) || got != nil {
				t.Fatalf("cancellation returned partial inventory: %#v, %v", got, err)
			}
		} else if err != nil || len(got) != 1 || got[0].ATPort.Path != "responsive" || attempts != 2 {
			t.Fatalf("port timeout blocked subsequent probe: %#v, %v attempts=%d", got, err, attempts)
		}
	}
}

func TestATProbingDiscovererBudgetProgressesAcrossPortsAndDevices(t *testing.T) {
	for _, portsPerDevice := range []int{4, 1, 2} {
		var candidates []Candidate
		for index := 0; index < 4; index++ {
			port := Port{Path: "fair-port-" + string(rune('0'+index)), InterfaceNumber: index}
			if index%portsPerDevice == 0 {
				candidates = append(candidates, Candidate{ID: port.Path, USBGeneration: "1:1", ATProbeRequired: true})
			}
			last := len(candidates) - 1
			candidates[last].Ports = append(candidates[last].Ports, port)
		}
		attempts := 0
		d := ATProbingDiscoverer{Base: probeTestDiscoverer{candidates: candidates}, ProbeUnknownUSB: true,
			Timeout: 10 * time.Millisecond, ProbeBudget: 12 * time.Millisecond,
			Probe: func(ctx context.Context, port Port, _ time.Duration) error {
				attempts++
				delay := 3 * time.Millisecond
				if port.Path == "fair-port-3" {
					delay = 5 * time.Millisecond
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
				if port.Path == "fair-port-3" {
					return nil
				}
				return errors.New("not an AT port")
			}}
		found := false
		for round := 0; round < 8 && !found; round++ {
			got, err := d.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range got {
				found = found || !candidate.ATProbeRequired && candidate.ATPort.Path == "fair-port-3"
			}
		}
		if !found {
			t.Fatalf("portsPerDevice=%d: responsive fourth port was starved", portsPerDevice)
		}
		if len(candidates) == 1 {
			candidates[0].USBGeneration = "1:2"
			d.Base = probeTestDiscoverer{candidates: candidates}
			d.Probe = func(_ context.Context, port Port, _ time.Duration) error {
				if port.Path != "fair-port-0" {
					t.Fatalf("new generation reused old port cursor: %s", port.Path)
				}
				return nil
			}
			if _, err := d.Discover(context.Background()); err != nil {
				t.Fatal(err)
			}
			// An active session outranks the newly selected responsive port 0.
			release, ok := tryAcquireATPort("fair-port-3")
			if !ok {
				t.Fatal("lease active port")
			}
			got, err := d.Discover(context.Background())
			release()
			if err != nil || len(got) != 1 || !got[0].ATProbeBusy || got[0].ATPort.Path != "fair-port-3" {
				t.Fatalf("rotation displaced active port: %#v, %v", got, err)
			}
		}
	}
}

func TestATProbingDiscovererCancellationWhileAnotherScanOwnsCursor(t *testing.T) {
	entered := make(chan struct{})
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	done := make(chan struct{})
	d := ATProbingDiscoverer{Base: probeTestDiscoverer{candidates: []Candidate{{ID: "modem", ATProbeRequired: true, Ports: []Port{{Path: "slow"}}}}},
		ProbeUnknownUSB: true, Probe: func(ctx context.Context, _ Port, _ time.Duration) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}}
	go func() { _, _ = d.Discover(firstCtx); close(done) }()
	defer func() { cancelFirst(); <-done }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if got, err := d.Discover(ctx); !errors.Is(err, context.DeadlineExceeded) || got != nil || time.Since(started) > time.Second {
		t.Fatalf("waiting scan ignored caller deadline: %#v, %v", got, err)
	}
}

type probeTestClient struct {
	responses []probeResponse
	commands  []string
	closed    int
}

type probeResponse struct {
	response Response
	err      error
}

func (client *probeTestClient) Execute(_ context.Context, command string) (Response, error) {
	client.commands = append(client.commands, command)
	if len(client.responses) == 0 {
		return Response{}, errors.New("unexpected probe command")
	}
	result := client.responses[0]
	client.responses = client.responses[1:]
	return result.response, result.err
}

func (*probeTestClient) WaitURC(context.Context, func(string) bool) (string, error) {
	return "", errors.New("probe does not wait for URCs")
}

func (client *probeTestClient) Close() error {
	client.closed++
	return nil
}

type probeTestOpener struct {
	client *probeTestClient
	port   Port
}

func (opener *probeTestOpener) Open(_ context.Context, port Port) (Client, error) {
	opener.port = port
	return opener.client, nil
}

func TestProbeATPortUsesOnlyATAndCGSN(t *testing.T) {
	client := &probeTestClient{responses: []probeResponse{
		{response: Response{Final: "OK"}},
		{response: Response{Lines: []string{"867123456789012"}, Final: "OK"}},
	}}
	opener := &probeTestOpener{client: client}
	port := Port{Path: "/dev/ttyUSB0", Name: "ttyUSB0"}

	if err := probeATPortWithOpener(context.Background(), port, time.Second, opener); err != nil {
		t.Fatalf("probeATPortWithOpener: %v", err)
	}
	if !reflect.DeepEqual(client.commands, []string{"AT", "AT+CGSN"}) {
		t.Fatalf("probe commands = %q, want only AT and AT+CGSN", client.commands)
	}
	if opener.port != port || client.closed != 1 {
		t.Fatalf("opened port=%#v close calls=%d", opener.port, client.closed)
	}
}

func TestProbeATPortRequiresATSuccessButIgnoresCGSNFailure(t *testing.T) {
	t.Run("AT rejection", func(t *testing.T) {
		client := &probeTestClient{responses: []probeResponse{{response: Response{Final: "ERROR"}}}}
		opener := &probeTestOpener{client: client}
		if err := probeATPortWithOpener(context.Background(), Port{}, time.Second, opener); err == nil {
			t.Fatal("probe succeeded after AT rejection")
		}
		if !reflect.DeepEqual(client.commands, []string{"AT"}) {
			t.Fatalf("commands after AT rejection = %q", client.commands)
		}
	})

	t.Run("CGSN timeout", func(t *testing.T) {
		client := &probeTestClient{responses: []probeResponse{
			{response: Response{Final: "OK"}},
			{err: context.DeadlineExceeded},
		}}
		opener := &probeTestOpener{client: client}
		if err := probeATPortWithOpener(context.Background(), Port{}, time.Second, opener); err != nil {
			t.Fatalf("probe should accept responsive AT port despite CGSN timeout: %v", err)
		}
	})
}
