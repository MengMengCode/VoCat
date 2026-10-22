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
		Base: probeTestDiscoverer{candidates: []Candidate{candidate}},
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
		Base: probeTestDiscoverer{candidates: []Candidate{candidate}},
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
		Base:  probeTestDiscoverer{candidates: []Candidate{candidate}},
		Probe: func(context.Context, Port, time.Duration) error { return errors.New("not an AT port") },
	}

	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("candidates = %#v, want unresponsive non-QMI device dropped", got)
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
