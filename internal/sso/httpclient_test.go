package sso

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

// TestNetworkAddressPolicy distinguishes public and ordinary private IPs from prohibited special addresses.
func TestNetworkAddressPolicy(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "::1", "169.254.169.254", "168.63.129.16", "fd00:ec2::254", "::10.0.0.1", "fec0::1", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "100.64.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "::ffff:169.254.169.254", "::ffff:127.0.0.1"} {
		address := netip.MustParseAddr(value)
		if allowedAddress(address, false) || allowedAddress(address, true) {
			t.Errorf("special address permitted: %s", value)
		}
	}
	for _, value := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.2", "fd12::1", "::ffff:192.168.1.2"} {
		address := netip.MustParseAddr(value)
		if allowedAddress(address, false) || !allowedAddress(address, true) {
			t.Errorf("private exception policy incorrect: %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !allowedAddress(netip.MustParseAddr(value), false) {
			t.Errorf("public address rejected: %s", value)
		}
	}
}

// TestNetworkExplicitProviderOriginsSupportHTTPSOnPublicAndPrivateAddresses checks exact configured scheme, host, and port boundaries.
func TestNetworkExplicitProviderOriginsSupportHTTPSOnPublicAndPrivateAddresses(t *testing.T) {
	for _, publicOrigin := range []string{"http://localhost:5173", "https://vocat.nas.example:8443", "https://vocat.example"} {
		for _, config := range []Config{
			{Kind: "pocketid", PublicOrigin: publicOrigin, IssuerURL: "https://id.nas.example:8443"},
			{Kind: "generic", PublicOrigin: publicOrigin, AuthURL: "https://id.nas.example:8443/authorize", TokenURL: "https://id.nas.example:8443/token", UserInfoURL: "https://id.nas.example:8443/userinfo"},
		} {
			_, policy, err := newHTTPClient(config, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, address := range []string{"8.8.8.8", "192.168.1.20", "fd12::20", "::ffff:192.168.1.20"} {
				policy.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr(address)}, nil
				}
				if err := requireEndpoint(context.Background(), policy, "https://id.nas.example:8443/token", true); err != nil {
					t.Errorf("explicit %s HTTPS provider rejected %s with VoCat origin %s: %v", config.Kind, address, publicOrigin, err)
				}
			}
			for _, endpoint := range []string{"https://id.nas.example/token", "https://other.nas.example:8443/token", "http://id.nas.example:8443/token"} {
				if policy.validate(context.Background(), endpoint, false) == nil {
					t.Errorf("configured HTTPS origin granted an unconfigured scheme, host, or port: %s", endpoint)
				}
			}
		}
	}
}

// TestNetworkGenericHTTPRequiresEveryAddressToRemainPrivate rejects mixed DNS results and unconfigured HTTP origins.
func TestNetworkGenericHTTPRequiresEveryAddressToRemainPrivate(t *testing.T) {
	_, policy, err := newHTTPClient(Config{Kind: "generic", AuthURL: "https://id.nas.example/authorize", TokenURL: "http://backend.nas.example:1411/token", UserInfoURL: "http://backend.nas.example:1411/userinfo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		addresses []string
		permitted bool
	}{
		{name: "RFC1918 and ULA", addresses: []string{"192.168.1.20", "fd12::20"}, permitted: true},
		{name: "mapped RFC1918", addresses: []string{"::ffff:10.1.2.3"}, permitted: true},
		{name: "public only", addresses: []string{"8.8.8.8"}},
		{name: "mixed private and public", addresses: []string{"192.168.1.20", "8.8.8.8"}},
		{name: "mixed private and link-local", addresses: []string{"192.168.1.20", "169.254.169.254"}},
		{name: "loopback", addresses: []string{"127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
				addresses := make([]netip.Addr, len(tc.addresses))
				for i, address := range tc.addresses {
					addresses[i] = netip.MustParseAddr(address)
				}
				return addresses, nil
			}
			if err := policy.validate(context.Background(), "http://backend.nas.example:1411/token", false); (err == nil) != tc.permitted {
				t.Fatalf("HTTP policy permitted=%v want %v: %v", err == nil, tc.permitted, err)
			}
		})
	}
	policy.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.168.1.20")}, nil
	}
	for _, endpoint := range []string{"http://unknown.nas.example:1411/token", "http://backend.nas.example/token", "http://id.nas.example/token"} {
		if policy.validate(context.Background(), endpoint, false) == nil {
			t.Errorf("explicit HTTP backend granted an unknown origin: %s", endpoint)
		}
	}
	if requireEndpoint(context.Background(), policy, "http://backend.nas.example:1411/authorize", true) == nil {
		t.Fatal("explicit HTTP backend weakened public endpoint HTTPS requirement")
	}
}

// TestNetworkExplicitPrivateOriginsCannotReachPrivilegedAddresses ensures explicit origins cannot authorize special networks.
func TestNetworkExplicitPrivateOriginsCannotReachPrivilegedAddresses(t *testing.T) {
	_, policy, err := newHTTPClient(Config{Kind: "pocketid", IssuerURL: "https://id.nas.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"127.0.0.1", "::1", "169.254.169.254", "168.63.129.16", "fd00:ec2::254", "fe80::1", "::ffff:169.254.169.254", "100.64.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "0.0.0.0", "224.0.0.1"} {
		policy.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(address)}, nil
		}
		if policy.validate(context.Background(), "https://id.nas.example/token", false) == nil {
			t.Errorf("explicit origin permitted privileged address %s", address)
		}
	}
}

// TestNetworkGitHubKeepsFixedPublicOrigins checks the GitHub preset rejects private addresses and alternate hosts.
func TestNetworkGitHubKeepsFixedPublicOrigins(t *testing.T) {
	_, policy, err := newHTTPClient(Config{Kind: "github"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"192.168.1.20", "fd12::20", "8.8.8.8"} {
		policy.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(address)}, nil
		}
		for _, endpoint := range []string{githubAuthorizeURL, githubTokenURL, githubUserURL} {
			if err := policy.validate(context.Background(), endpoint, false); (err == nil) != (address == "8.8.8.8") {
				t.Errorf("GitHub preset accepted nonpublic destination %s or rejected public address: %v", address, err)
			}
		}
	}
	if policy.validate(context.Background(), "https://github.enterprise.example/token", false) == nil {
		t.Fatal("GitHub preset permitted an unfixed origin")
	}
}

// TestNetworkValidatesEveryDNSAddressAndPrivateHTTP checks all DNS answers and the stricter private HTTP policy.
func TestNetworkValidatesEveryDNSAddressAndPrivateHTTP(t *testing.T) {
	p := &destinationPolicy{
		allowed: map[string]bool{"https://id.example": true, "http://backend.example": true},
		private: true,
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("::1")}, nil
		},
	}
	if p.validate(context.Background(), "https://id.example/token", false) == nil {
		t.Fatal("safe A record masked prohibited AAAA record")
	}
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	if p.validate(context.Background(), "http://backend.example/token", false) == nil {
		t.Fatal("public insecure HTTP backend accepted")
	}
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}
	if err := p.validate(context.Background(), "http://backend.example/token", false); err != nil {
		t.Fatal(err)
	}
	if p.validate(context.Background(), "http://backend.example/authorize", true) == nil || p.validate(context.Background(), "https://untrusted.example", false) == nil {
		t.Fatal("insecure authorize or unpermitted origin accepted")
	}
}

// TestNetworkRechecksDNSAtDial verifies an HTTPS DNS rebinding target is rejected before connection.
func TestNetworkRechecksDNSAtDial(t *testing.T) {
	client, policy, err := newHTTPClient(Config{Kind: "pocketid", IssuerURL: "https://id.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int64
	policy.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	response, err := client.Get("https://id.example/token")
	if err == nil {
		response.Body.Close()
		t.Fatal("DNS rebinding target accepted")
	}
	if lookups.Load() < 2 {
		t.Fatal("dial did not revalidate DNS")
	}
}

// TestNetworkHTTPRechecksPrivateOnlyDNSAtDial checks HTTP dialing rejects a newly introduced public DNS answer.
func TestNetworkHTTPRechecksPrivateOnlyDNSAtDial(t *testing.T) {
	client, policy, err := newHTTPClient(Config{Kind: "generic", AuthURL: "https://id.nas.example/authorize", TokenURL: "http://backend.nas.example:1411/token", UserInfoURL: "http://backend.nas.example:1411/userinfo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int64
	policy.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("192.168.1.20")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("8.8.8.8")}, nil
	}
	response, err := client.Post("http://backend.nas.example:1411/token", "application/x-www-form-urlencoded", strings.NewReader("client_secret=test-exchange-credential"))
	if err == nil {
		response.Body.Close()
		t.Fatal("private HTTP backend accepted a public address during dialing")
	}
	if lookups.Load() < 2 {
		t.Fatal("HTTP dial did not revalidate DNS")
	}
}

// TestHTTPClientRefusesCredentialRedirectsAndOversizedBodies checks redirect, response-size, and production loopback restrictions.
func TestHTTPClientRefusesCredentialRedirectsAndOversizedBodies(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, server.URL+"/final", http.StatusFound)
		case "/cross-origin":
			http.Redirect(w, r, "https://untrusted.example/keys", http.StatusFound)
		case "/large":
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, strings.Repeat("x", MaxResponseBytes+1))
		default:
			io.WriteString(w, "ok")
		}
	}))
	defer server.Close()
	c := Config{Kind: "pocketid", IssuerURL: server.URL}
	client, _, err := newHTTPClient(c, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/redirect", "/cross-origin"} {
		request, _ := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader("client_secret=secret"))
		request.SetBasicAuth("client", "secret")
		if response, err := client.Do(request); err == nil {
			response.Body.Close()
			t.Fatalf("credential redirect followed: %s", path)
		}
	}
	if response, err := client.Get(server.URL + "/cross-origin"); err == nil {
		response.Body.Close()
		t.Fatal("untrusted GET redirect followed")
	}
	response, err := client.Get(server.URL + "/large")
	if err == nil {
		_, err = io.ReadAll(response.Body)
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("oversized response accepted")
	}
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("unexpected size error: %v", err)
	}
	// A production client cannot use the test loopback endpoint.
	production, _, _ := newHTTPClient(c, nil)
	if response, err := production.Get(server.URL + "/final"); err == nil {
		response.Body.Close()
		t.Fatal("production client allowed loopback")
	}
}
