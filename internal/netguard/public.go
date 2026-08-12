package netguard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ValidatePublicURL accepts an absolute HTTP(S) URL only when every currently
// resolved address is publicly routable. The transport returned by
// NewPublicHTTPClient repeats the same check when it dials, which also prevents
// DNS rebinding between validation and connection establishment.
func ValidatePublicURL(ctx context.Context, raw string, requireHTTPS bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
		return nil, errors.New("destination must be an absolute HTTP URL")
	}
	if parsed.User != nil {
		return nil, errors.New("destination URL cannot contain user information")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("destination URL must use HTTP or HTTPS")
	}
	if requireHTTPS && parsed.Scheme != "https" {
		return nil, errors.New("destination URL must use HTTPS")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, errors.New("destination URL has an invalid port")
		}
	}
	if _, err := resolvePublic(ctx, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

// NewPublicHTTPClient creates a client that never uses environment proxies,
// rejects private/special-use destinations at dial time, and validates every
// redirect before following it.
func NewPublicHTTPClient(timeout time.Duration, requireHTTPS bool) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           PublicDialer(timeout),
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return errors.New("too many redirects")
			}
			_, err := ValidatePublicURL(request.Context(), request.URL.String(), requireHTTPS)
			return err
		},
	}
}

// PublicDialer resolves the original hostname and connects directly to one of
// its validated public addresses. It does not pass the hostname back through a
// second resolver, so a DNS rebinding response cannot redirect the connection.
func PublicDialer(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse outbound address: %w", err)
		}
		addresses, err := resolvePublic(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := net.Dialer{Timeout: timeout}
		var lastErr error
		for _, address := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("connect to public destination: %w", lastErr)
	}
}

func resolvePublic(ctx context.Context, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		literal = literal.Unmap()
		if !publicAddress(literal) {
			return nil, errors.New("destination resolves to a private or special-use address")
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve destination: %w", err)
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !publicAddress(address) {
			return nil, errors.New("destination resolves to a private or special-use address")
		}
		result = append(result, address)
	}
	if len(result) == 0 {
		return nil, errors.New("destination has no IP address")
	}
	return result, nil
}

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	// Block both the well-known and local-use NAT64 prefixes. Otherwise a
	// public-looking IPv6 literal could translate to a private IPv4 target.
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2002::/16"),
}

func publicAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() {
		return false
	}
	for _, blocked := range blockedNetworks {
		if blocked.Contains(address) {
			return false
		}
	}
	return true
}
