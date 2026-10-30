package ike

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSOCKS5RelayLookupErrorDoesNotExposeProxyResponse(t *testing.T) {
	const secret = "syntheticproxysecret"
	const domain = "relay." + secret + ".invalid."
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if err := client.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		reply := append([]byte{5, 0, 0, 3, byte(len(domain))}, domain...)
		reply = append(reply, 0x1e, 0xd9)
		_, err := server.Write(reply)
		written <- err
	}()
	var lookups atomic.Int32
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			lookups.Add(1)
			return nil, fmt.Errorf("untrusted resolver detail: %s", secret)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := readSOCKS5Reply(ctx, client, resolver)
	if writeErr := <-written; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err == nil {
		t.Fatal("accepted an unresolved relay domain")
	}
	if lookups.Load() == 0 {
		t.Fatal("fixture did not exercise the private resolver error")
	}
	var output bytes.Buffer
	slog.New(slog.NewJSONHandler(&output, nil)).Warn("VoWiFi operation failed", "error", err)
	for _, value := range []string{err.Error(), fmt.Sprintf("%+v", err), output.String()} {
		if strings.Contains(value, secret) || strings.Contains(value, domain) ||
			strings.Contains(value, "untrusted resolver detail") {
			t.Fatal("relay lookup failure exposed untrusted proxy or resolver data")
		}
	}
}

func TestSanitizeSOCKS5RelayLookupErrorPreservesOnlySafeClassification(t *testing.T) {
	const secret = "syntheticproxysecret"
	privateCause := errors.New(secret)
	tests := []struct {
		name          string
		cause         error
		wantContext   error
		wantDNS       bool
		wantTimeout   bool
		wantTemporary bool
		wantNotFound  bool
	}{
		{name: "canceled", cause: fmt.Errorf("nested: %w", context.Canceled), wantContext: context.Canceled},
		{name: "deadline", cause: fmt.Errorf("nested: %w", context.DeadlineExceeded), wantContext: context.DeadlineExceeded},
		{name: "unknown", cause: privateCause},
		{name: "timeout", cause: &net.DNSError{Err: secret, Name: secret, Server: secret, UnwrapErr: privateCause,
			IsTimeout: true, IsTemporary: true}, wantDNS: true, wantTimeout: true, wantTemporary: true},
		{name: "not found", cause: &net.DNSError{Err: secret, Name: secret, Server: secret, UnwrapErr: privateCause,
			IsNotFound: true}, wantDNS: true, wantNotFound: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := fmt.Errorf("%s: %w", secret, test.cause)
			got := sanitizeSOCKS5RelayLookupError(original)
			if got == nil || strings.Contains(fmt.Sprintf("%+v", got), secret) {
				t.Fatal("sanitized lookup error retained sensitive text")
			}
			if test.wantContext != nil && !errors.Is(got, test.wantContext) {
				t.Fatalf("lost context classification: %v", got)
			}
			if errors.Is(got, original) || errors.Is(got, privateCause) {
				t.Fatal("sanitized lookup error retained an original error chain")
			}
			for current := got; current != nil; current = errors.Unwrap(current) {
				if current == original || current == test.cause {
					t.Fatal("original error remains accessible through Unwrap")
				}
			}
			var dns *net.DNSError
			if errors.As(got, &dns) {
				if dns.Name != "" || dns.Server != "" || dns.UnwrapErr != nil || strings.Contains(dns.Err, secret) {
					t.Fatal("sanitized DNS error retained sensitive fields")
				}
				if test.wantDNS && (dns.IsTimeout != test.wantTimeout || dns.IsTemporary != test.wantTemporary ||
					dns.IsNotFound != test.wantNotFound) {
					t.Fatal("sanitized DNS error changed classification")
				}
			} else if test.wantDNS {
				t.Fatal("lost DNS error classification")
			}
			if test.wantTimeout {
				var network net.Error
				if !errors.As(got, &network) || !network.Timeout() || network.Temporary() != test.wantTemporary {
					t.Fatal("lost network timeout classification")
				}
			}
		})
	}
}
