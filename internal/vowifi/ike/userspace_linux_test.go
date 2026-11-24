//go:build linux

package ike

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestValidateUserspaceRoutesAllowsOnlyNegotiatedEndpoints(t *testing.T) {
	t.Parallel()
	config := userspaceRouteTestConfig()
	if err := validateUserspaceRoutes(config); err != nil {
		t.Fatalf("valid negotiated route: %v", err)
	}

	config.PCSCF = []net.IP{net.IPv4(203, 0, 113, 10)}
	if err := validateUserspaceRoutes(config); err == nil {
		t.Fatal("P-CSCF outside responder selector was accepted")
	}
}

func TestValidateUserspaceRoutesRequiresMatchingAddressFamily(t *testing.T) {
	t.Parallel()
	config := userspaceRouteTestConfig()
	config.PCSCF = []net.IP{net.ParseIP("2001:db8::20")}
	config.ResponderSelectors = []trafficSelector{{
		StartPort: 0,
		EndPort:   65535,
		StartIP:   net.ParseIP("2001:db8::"),
		EndIP:     net.ParseIP("2001:db8::ffff"),
	}}
	if err := validateUserspaceRoutes(config); err == nil {
		t.Fatal("IPv6 P-CSCF without an assigned inner IPv6 address was accepted")
	}
}

func TestUserspaceRulePriorityAlwaysPrecedesMainRoute(t *testing.T) {
	t.Parallel()
	for _, spi := range []uint32{
		0,
		1,
		32767,
		0x01020304,
		0x80000000,
		0xffffffff,
	} {
		table, priority := userspaceRoutingIdentifiers(spi)
		if table <= 255 {
			t.Fatalf("SPI %08x produced reserved routing table %d", spi, table)
		}
		if priority < 10000 || priority >= 30000 || priority >= 32766 {
			t.Fatalf(
				"SPI %08x produced unsafe rule priority %d",
				spi,
				priority,
			)
		}
	}
}

func userspaceRouteTestConfig() ChildSAConfig {
	return ChildSAConfig{
		InnerLocalIPv4: net.IPv4(10, 132, 116, 34),
		PCSCF:          []net.IP{net.IPv4(10, 127, 192, 82)},
		InitiatorSelectors: []trafficSelector{{
			StartPort: 0,
			EndPort:   65535,
			StartIP:   net.IPv4(10, 132, 116, 34),
			EndIP:     net.IPv4(10, 132, 116, 34),
		}},
		ResponderSelectors: []trafficSelector{{
			StartPort: 0,
			EndPort:   65535,
			StartIP:   net.IPv4(10, 0, 0, 0),
			EndIP:     net.IPv4(10, 255, 255, 255),
		}},
	}
}

type blockingNATTRelay struct{}

func (blockingNATTRelay) SendESP(ctx context.Context, _ []byte) error {
	return ctx.Err()
}

func (blockingNATTRelay) ReceiveESP(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

type scriptedUserspaceSendRelay struct {
	blockingNATTRelay
	sendErrors []error
	sent       int
}

func (relay *scriptedUserspaceSendRelay) SendESP(context.Context, []byte) error {
	if relay.sent >= len(relay.sendErrors) {
		return errors.New("unexpected extra ESP send")
	}
	err := relay.sendErrors[relay.sent]
	relay.sent++
	return err
}

func TestUserspaceESPOversizeDoesNotStopTunnel(t *testing.T) {
	t.Parallel()
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(descriptors[0])
		_ = unix.Close(descriptors[1])
	})

	transportFailure := errors.New("relay closed unexpectedly")
	relay := &scriptedUserspaceSendRelay{sendErrors: []error{
		&net.OpError{Op: "write", Err: os.NewSyscallError("write", unix.EMSGSIZE)},
		nil,
		transportFailure,
	}}
	local := net.IPv4(10, 0, 0, 2)
	remote := net.IPv4(10, 0, 0, 9)
	runContext, cancel := context.WithCancel(context.Background())
	handle := &linuxUserspaceHandle{
		config:     ChildSAConfig{InnerLocalIPv4: local},
		tunnel:     mustTestESPTunnel(t, "aes-cbc-128", "hmac-sha1-96", make([]byte, 16), make([]byte, 20)),
		tunFD:      descriptors[0],
		relay:      relay,
		runContext: runContext,
		cancel:     cancel,
		failures:   make(chan error, 1),
	}
	t.Cleanup(func() {
		cancel()
		handle.wait.Wait()
	})
	handle.wait.Add(1)
	go handle.copyTUNToRelay()

	for _, payload := range [][]byte{make([]byte, 1252), []byte("small"), []byte("fatal")} {
		packet := testIPv4UDPPacket(local, remote, 42000, 5060, payload)
		if err := unix.Send(descriptors[1], packet, 0); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-handle.Failures():
		if !errors.Is(err, transportFailure) {
			t.Fatalf("tunnel reported the wrong failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the fatal relay error did not stop the tunnel")
	}
	handle.wait.Wait()
	if relay.sent != len(relay.sendErrors) {
		t.Fatalf("relay sent %d packets, want %d", relay.sent, len(relay.sendErrors))
	}
}

func TestLinuxUserspaceInstallerLifecycle(t *testing.T) {
	if os.Getenv("VOCAT_NETNS_TEST") != "1" {
		t.Skip("set VOCAT_NETNS_TEST=1 inside an isolated Linux network namespace")
	}
	config := userspaceRouteTestConfig()
	config.Name = "vocat-swu-test"
	config.InboundSPI = 0x01020304
	config.OutboundSPI = 0x05060708
	config.Encryption = "aes-cbc-128"
	config.Integrity = "hmac-sha1-96"
	config.InboundEncKey = make([]byte, 16)
	config.InboundAuthKey = make([]byte, 20)
	config.OutboundEncKey = make([]byte, 16)
	config.OutboundAuthKey = make([]byte, 20)
	config.UDPEncapsulation = true
	config.Relay = blockingNATTRelay{}

	tableID, _ := userspaceRoutingIdentifiers(config.InboundSPI)
	table := strconv.FormatUint(uint64(tableID), 10)
	if output, err := exec.Command(
		"ip", "-4", "route", "add",
		"table", table, "unreachable", "default",
	).CombinedOutput(); err != nil {
		t.Fatalf("preoccupy route table: %v: %s", err, output)
	}
	if conflicting, err := (linuxUserspaceInstaller{ipCommand: "ip"}).Install(
		context.Background(),
		config,
	); err == nil {
		_ = conflicting.Close(context.Background())
		t.Fatal("installer accepted a preoccupied routing table")
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "delete",
		"table", table, "unreachable", "default",
	).CombinedOutput(); err != nil {
		t.Fatalf("release preoccupied route table: %v: %s", err, output)
	}

	handle, err := (linuxUserspaceInstaller{ipCommand: "ip"}).Install(
		context.Background(),
		config,
	)
	if err != nil {
		t.Fatalf("install user-space CHILD_SA: %v", err)
	}
	if mode := handle.(DataplaneEvidence).DataplaneMode(); mode != "userspace" {
		t.Fatalf("dataplane mode = %q", mode)
	}
	if output, err := exec.Command("ip", "link", "show", "dev", config.Name).CombinedOutput(); err != nil {
		t.Fatalf("TUN interface was not created: %v: %s", err, output)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "show", "table", table,
	).CombinedOutput(); err != nil ||
		!strings.Contains(string(output), "10.127.192.82") ||
		!strings.Contains(string(output), "unreachable default") {
		t.Fatalf("isolated P-CSCF routes are missing: %v: %s", err, output)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "get", "10.127.192.82",
		"from", "10.132.116.34",
	).CombinedOutput(); err != nil ||
		!strings.Contains(string(output), "dev "+config.Name) ||
		!strings.Contains(string(output), "table "+table) {
		t.Fatalf(
			"P-CSCF route did not win before the main table: %v: %s",
			err,
			output,
		)
	}
	if output, err := exec.Command(
		"ip", "-4", "route", "get", "198.51.100.1",
		"from", "10.132.116.34",
	).CombinedOutput(); err == nil {
		t.Fatalf(
			"non-P-CSCF inner traffic escaped the unreachable default: %s",
			output,
		)
	}

	expectedFailure := errors.New("test relay stopped")
	concrete := handle.(*linuxUserspaceHandle)
	concrete.fail(expectedFailure)
	select {
	case failure := <-concrete.Failures():
		if !errors.Is(failure, expectedFailure) {
			t.Fatalf("runtime failure = %v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal failure was not published")
	}

	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handle.Close(closeContext); err != nil {
		t.Fatalf("close user-space CHILD_SA: %v", err)
	}
	if err := exec.Command("ip", "link", "show", "dev", config.Name).Run(); err == nil {
		t.Fatal("TUN interface survived Close")
	}
	output, err := exec.Command("ip", "-4", "rule", "show").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "from 10.132.116.34") {
		t.Fatalf("source rule survived Close: %s", output)
	}
}

var _ NATTPacketRelay = blockingNATTRelay{}
