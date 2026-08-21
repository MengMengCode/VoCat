package ike

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vocat/internal/vowifi"
)

const googleDNSOverHTTPS = "https://dns.google/resolve"

type dnsOverHTTPSResponse struct {
	Status int `json:"Status"`
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func resolveEPDG(ctx context.Context, resolver *net.Resolver, host string) ([]net.IPAddr, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	hostsToTry := []string{normalized}
	if alt := alternate3GPPHostname(normalized); alt != "" && alt != normalized {
		hostsToTry = append(hostsToTry, alt)
	}

	var systemErr error
	for _, targetHost := range hostsToTry {
		ips, err := resolver.LookupIP(ctx, "ip4", targetHost)
		if err == nil {
			addresses := make([]net.IPAddr, 0, len(ips))
			for _, ip := range ips {
				addresses = append(addresses, net.IPAddr{IP: ip})
			}
			valid := filterValidPublicEPDGAddresses(addresses)
			if len(valid) > 0 {
				return valid, nil
			}
		} else {
			systemErr = err
		}
	}

	subnet := vowifi.EPDGDNSClientSubnet(normalized)
	client := &http.Client{Timeout: 8 * time.Second}
	var fallbackErr error

	for _, targetHost := range hostsToTry {
		var fallback []net.IPAddr
		fallback, fallbackErr = resolveEPDGWithECS(ctx, client, googleDNSOverHTTPS, targetHost, subnet)
		if fallbackErr == nil && len(fallback) > 0 {
			return fallback, nil
		}
	}
	if systemErr == nil {
		systemErr = errors.New("system DNS returned no usable public IP addresses")
	}
	return nil, fmt.Errorf("system DNS failed (%v); geographic DNS fallback failed: %w", systemErr, fallbackErr)
}

func filterValidPublicEPDGAddresses(addresses []net.IPAddr) []net.IPAddr {
	result := make([]net.IPAddr, 0, len(addresses))
	for _, addr := range addresses {
		if addr.IP == nil || addr.IP.IsLoopback() || addr.IP.IsUnspecified() {
			continue
		}
		result = append(result, addr)
	}
	return result
}

func alternate3GPPHostname(host string) string {
	const prefix = "epdg.epc.mnc"
	if !strings.HasPrefix(host, prefix) {
		return ""
	}
	rest := host[len(prefix):]
	dot := strings.Index(rest, ".")
	if dot <= 0 {
		return ""
	}
	mnc := rest[:dot]
	suffix := rest[dot:]
	if len(mnc) == 3 && strings.HasPrefix(mnc, "0") {
		return prefix + mnc[1:] + suffix
	}
	if len(mnc) == 2 {
		return prefix + "0" + mnc + suffix
	}
	return ""
}

func resolveEPDGWithECS(
	ctx context.Context,
	client *http.Client,
	endpoint, host, subnet string,
) ([]net.IPAddr, error) {
	if client == nil {
		return nil, errors.New("nil DNS-over-HTTPS client")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse DNS-over-HTTPS endpoint: %w", err)
	}
	query := parsed.Query()
	query.Set("name", strings.TrimSpace(host))
	query.Set("type", "A")
	if strings.TrimSpace(subnet) != "" {
		query.Set("edns_client_subnet", strings.TrimSpace(subnet))
	}
	parsed.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build DNS-over-HTTPS request: %w", err)
	}
	request.Header.Set("Accept", "application/dns-json")
	request.Header.Set("Cache-Control", "no-cache")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query DNS-over-HTTPS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DNS-over-HTTPS returned HTTP %d", response.StatusCode)
	}
	var payload dnsOverHTTPSResponse
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode DNS-over-HTTPS response: %w", err)
	}
	if payload.Status != 0 {
		return nil, fmt.Errorf("DNS-over-HTTPS returned DNS status %d", payload.Status)
	}
	result := make([]net.IPAddr, 0, len(payload.Answer))
	for _, answer := range payload.Answer {
		if answer.Type != 1 && answer.Type != 28 {
			continue
		}
		ip := net.ParseIP(strings.TrimSuffix(strings.TrimSpace(answer.Data), "."))
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		duplicate := false
		for _, existing := range result {
			if existing.IP.Equal(ip) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, net.IPAddr{IP: append(net.IP(nil), ip...)})
		}
	}
	if len(result) == 0 {
		return nil, errors.New("DNS-over-HTTPS response contained no ePDG IP addresses")
	}
	return result, nil
}
