//go:build linux

package qmi

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProxySocketAddressNormalizesCommonAbstractSocketNames(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default", in: "", want: "\x00qmi-proxy"},
		{name: "plain", in: "qmi-proxy", want: "\x00qmi-proxy"},
		{name: "at prefix", in: "@qmi-proxy", want: "\x00qmi-proxy"},
		{name: "nul prefix", in: "\x00qmi-proxy", want: "\x00qmi-proxy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxySocketAddress(tt.in); got != tt.want {
				t.Fatalf("proxySocketAddress(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestOpenProxyTransportRetriesUntilContextDeadline(t *testing.T) {
	proxyExecutable := filepath.Join(t.TempDir(), "qmi-proxy")
	if err := os.WriteFile(proxyExecutable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldDial := dialProxyHook
	oldStart := startProxyProcessHook
	oldRetryDelay := proxyRetryDelay
	t.Cleanup(func() {
		dialProxyHook = oldDial
		startProxyProcessHook = oldStart
		proxyRetryDelay = oldRetryDelay
	})

	attempts := 0
	starts := 0
	dialProxyHook = func(context.Context, string) (qmiTransport, error) {
		attempts++
		return nil, errors.New("proxy socket not ready")
	}
	startProxyProcessHook = func(string) error {
		starts++
		return nil
	}
	proxyRetryDelay = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := openProxyTransport(ctx, ClientOptions{
		ProxyPath:       "@qmi-proxy",
		ProxyExecutable: proxyExecutable,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("openProxyTransport() error=%v, want context deadline exceeded", err)
	}
	if starts != 1 {
		t.Fatalf("start attempts=%d, want 1", starts)
	}
	if attempts < 3 {
		t.Fatalf("dial attempts=%d, want at least 3 retries before deadline", attempts)
	}
}

func TestOpenProxyTransportRetriesUntilProxyIsReady(t *testing.T) {
	proxyExecutable := filepath.Join(t.TempDir(), "qmi-proxy")
	if err := os.WriteFile(proxyExecutable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldDial := dialProxyHook
	oldStart := startProxyProcessHook
	oldRetryDelay := proxyRetryDelay
	t.Cleanup(func() {
		dialProxyHook = oldDial
		startProxyProcessHook = oldStart
		proxyRetryDelay = oldRetryDelay
	})

	attempts := 0
	starts := 0
	var serverConn net.Conn
	dialProxyHook = func(context.Context, string) (qmiTransport, error) {
		attempts++
		if attempts < 4 {
			return nil, errors.New("proxy socket not ready")
		}
		clientConn, conn := net.Pipe()
		serverConn = conn
		return clientConn, nil
	}
	startProxyProcessHook = func(string) error {
		starts++
		return nil
	}
	proxyRetryDelay = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn, err := openProxyTransport(ctx, ClientOptions{
		ProxyPath:       "\x00qmi-proxy",
		ProxyExecutable: proxyExecutable,
	})
	if err != nil {
		t.Fatalf("openProxyTransport() error=%v", err)
	}
	defer conn.Close()
	if serverConn != nil {
		defer serverConn.Close()
	}
	if starts != 1 {
		t.Fatalf("start attempts=%d, want 1", starts)
	}
	if attempts != 4 {
		t.Fatalf("dial attempts=%d, want 4", attempts)
	}
}
