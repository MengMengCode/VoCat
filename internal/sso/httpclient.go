package sso

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	RequestTimeout   = 10 * time.Second
	MaxResponseBytes = 1 << 20
	MaxIDTokenBytes  = 64 << 10
)

var errResponseTooLarge = errors.New("identity provider response exceeds limit")

type destinationPolicy struct {
	allowed       map[string]bool
	private       bool
	testTransport bool
	lookup        func(context.Context, string, string) ([]netip.Addr, error)
}

type privateHTTPContextKey struct{}

// newHTTPClient builds an origin-constrained client with DNS checks, timeouts, and response limits.
// An injected transport is reserved for trusted fixtures and bypasses production address checks.
func newHTTPClient(c Config, injected *http.Client) (*http.Client, *destinationPolicy, error) {
	p := &destinationPolicy{
		allowed:       make(map[string]bool),
		testTransport: injected != nil,
		lookup:        net.DefaultResolver.LookupNetIP,
	}
	var initial []string
	switch c.Kind {
	case "pocketid":
		initial = []string{c.IssuerURL}
		p.private = true
	case "github":
		initial = []string{githubAuthorizeURL, githubTokenURL, githubUserURL}
	case "generic":
		initial = []string{c.AuthURL, c.TokenURL, c.UserInfoURL}
		p.private = true
	}
	// Administrator-configured exact origins approve ordinary private IdP hosts.
	// Discovery cannot add other destinations. GitHub keeps its fixed,
	// public-only preset; HTTP backends must stay private at every DNS check.
	for _, raw := range initial {
		if raw == "" {
			continue
		}
		u, err := parseURL(raw)
		if err != nil {
			return nil, nil, err
		}
		p.allowed[origin(u)] = true
	}
	var client http.Client
	if injected != nil {
		client = *injected
	}
	base := client.Transport
	if base == nil {
		base = &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, errors.New("invalid identity provider address")
				}
				privateHTTP, _ := ctx.Value(privateHTTPContextKey{}).(bool)
				addresses, err := p.resolve(ctx, host, privateHTTP)
				if err != nil {
					return nil, err
				}
				dialer := net.Dialer{Timeout: RequestTimeout}
				var lastErr error
				for _, ip := range addresses {
					connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
					if err == nil {
						return connection, nil
					}
					lastErr = err
				}
				return nil, lastErr
			},
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   RequestTimeout,
			ResponseHeaderTimeout: RequestTimeout,
			ExpectContinueTimeout: time.Second,
			IdleConnTimeout:       30 * time.Second,
			ForceAttemptHTTP2:     true,
		}
	}
	client.Timeout = RequestTimeout
	client.Transport = &guardedTransport{base: base, policy: p}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too many identity provider redirects")
		}
		if len(via) > 0 && (via[0].Method != http.MethodGet || via[0].Header.Get("Authorization") != "") {
			return errors.New("credential-bearing identity provider requests cannot redirect")
		}
		return p.validate(request.Context(), request.URL.String(), false)
	}
	return &client, p, nil
}

// validate checks an endpoint against configured origins, scheme requirements, and resolved addresses.
func (p *destinationPolicy) validate(ctx context.Context, raw string, requireHTTPS bool) error {
	u, err := parseURL(raw)
	if err != nil {
		return err
	}
	if !p.allowed[origin(u)] {
		return invalidConfig("endpoint origin is not explicitly permitted")
	}
	if u.Scheme == "http" && (requireHTTPS || !p.private) {
		return invalidConfig("endpoint requires HTTPS")
	}
	if !p.testTransport {
		_, err = p.resolve(ctx, u.Hostname(), u.Scheme == "http")
	}
	return err
}

// resolve rejects a DNS result if any address violates policy; HTTP destinations must all be private.
func (p *destinationPolicy) resolve(ctx context.Context, host string, requirePrivate bool) ([]netip.Addr, error) {
	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addresses = []netip.Addr{literal}
	} else {
		var err error
		addresses, err = p.lookup(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("%w: identity provider DNS lookup failed", ErrProviderUnavailable)
		}
	}
	if len(addresses) == 0 {
		return nil, invalidConfig("endpoint has no IP addresses")
	}
	for i, address := range addresses {
		address = address.Unmap()
		if !allowedAddress(address, p.private) || (requirePrivate && !address.IsPrivate()) {
			return nil, invalidConfig("endpoint resolves to a prohibited address")
		}
		addresses[i] = address
	}
	return addresses, nil
}

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure platform/metadata virtual address.
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::/96"), // Deprecated IPv4-compatible IPv6 addresses.
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fd00:ec2::254/128"), // EC2 IPv6 instance metadata.
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// allowedAddress excludes special and privileged networks, allowing ordinary private IPs only by policy.
func allowedAddress(address netip.Addr, private bool) bool {
	address = address.Unmap()
	if !address.IsValid() || address.Zone() != "" || !address.IsGlobalUnicast() {
		return false
	}
	for _, blocked := range blockedNetworks {
		if blocked.Contains(address) {
			return false
		}
	}
	if address.IsPrivate() {
		return private
	}
	return true
}

type guardedTransport struct {
	base   http.RoundTripper
	policy *destinationPolicy
}

// RoundTrip validates each destination and bounds the response body before returning it to a provider.
func (t *guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.policy.validate(request.Context(), request.URL.String(), false); err != nil {
		return nil, err
	}
	if request.URL.Scheme == "http" {
		request = request.Clone(context.WithValue(request.Context(), privateHTTPContextKey{}, true))
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > MaxResponseBytes {
		response.Body.Close()
		return nil, errResponseTooLarge
	}
	response.Body = &limitedResponseBody{ReadCloser: response.Body, remaining: MaxResponseBytes}
	return response, nil
}

type limitedResponseBody struct {
	io.ReadCloser
	remaining int
}

// Read enforces the response byte budget, probing at the boundary to distinguish EOF from overflow.
func (r *limitedResponseBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, errResponseTooLarge
		}
		return 0, err
	}
	if len(buffer) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.ReadCloser.Read(buffer)
	r.remaining -= n
	return n, err
}

// requireEndpoint rejects missing metadata endpoints before applying destination policy.
func requireEndpoint(ctx context.Context, policy *destinationPolicy, raw string, public bool) error {
	if raw == "" {
		return invalidConfig("provider metadata is missing a required endpoint")
	}
	return policy.validate(ctx, raw, public)
}
