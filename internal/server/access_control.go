package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"vocat/internal/store"
)

const accessSettingKey = "security.access"

// accessConfig is the persisted network access policy.
type accessConfig struct {
	Mode              string   `json:"mode"`               // "internal" (default) or "public"
	AllowedCIDRs      []string `json:"allowed_cidrs"`      // extra CIDRs always allowed
	TrustProxyHeaders bool     `json:"trust_proxy_headers"` // honor X-Forwarded-For
}

// parsedAccessConfig is the validated runtime form of accessConfig.
type parsedAccessConfig struct {
	mode       string
	cidrs      []netip.Prefix
	trustProxy bool
}

// internalNetworks are always allowed when mode is "internal": loopback,
// RFC1918 private ranges, link-local, and IPv6 ULA.
var internalNetworks = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fc00::/7"),
}

func defaultAccessConfig() parsedAccessConfig {
	return parsedAccessConfig{mode: "internal"}
}

// parseAccessConfig validates and parses a persisted access policy.
func parseAccessConfig(config accessConfig) (parsedAccessConfig, error) {
	mode := strings.ToLower(strings.TrimSpace(config.Mode))
	if mode == "" {
		mode = "internal"
	}
	if mode != "internal" && mode != "public" {
		return parsedAccessConfig{}, errors.New("mode must be \"internal\" or \"public\"")
	}
	parsed := parsedAccessConfig{
		mode:       mode,
		trustProxy: config.TrustProxyHeaders,
	}
	for _, raw := range config.AllowedCIDRs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			parsed.cidrs = append(parsed.cidrs, prefix.Masked())
			continue
		}
		if address, err := netip.ParseAddr(raw); err == nil {
			bits := 32
			if address.Is6() {
				bits = 128
			}
			parsed.cidrs = append(parsed.cidrs, netip.PrefixFrom(address, bits))
			continue
		}
		return parsedAccessConfig{}, errors.New("invalid CIDR or IP: " + raw)
	}
	return parsed, nil
}

// allowed reports whether a client address may reach the service.
func (config parsedAccessConfig) allowed(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	// Normalize IPv4-mapped IPv6 addresses (e.g. ::ffff:192.168.1.5 seen on
	// dual-stack listeners) to their IPv4 form so they match the internal
	// ranges below; without this they would be denied even though they are
	// ordinary internal IPv4 clients.
	address = address.Unmap()
	if config.mode == "public" {
		return true
	}
	if address.IsLoopback() {
		return true
	}
	for _, prefix := range internalNetworks {
		if prefix.Contains(address) {
			return true
		}
	}
	for _, prefix := range config.cidrs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// clientIP determines the request's source address, honoring X-Forwarded-For
// only when the deployment is configured to trust proxy headers.
func (config parsedAccessConfig) clientIP(r *http.Request) netip.Addr {
	if config.trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if address, err := netip.ParseAddr(first); err == nil {
				return address.Unmap()
			}
		}
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			if address, err := netip.ParseAddr(real); err == nil {
				return address.Unmap()
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	// Report the canonical (unmapped) form so logs, the login rate-limit key,
	// and the access decision all agree on one representation of an IPv4 client.
	return address.Unmap()
}

// accessControl rejects requests whose source IP is outside the configured
// access policy. It wraps the whole mux so every route (API, SPA, plugins) is
// protected uniformly.
func (s *Server) accessControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.accessMu.RLock()
		config := s.access
		s.accessMu.RUnlock()
		address := config.clientIP(r)
		if config.allowed(address) {
			next.ServeHTTP(w, r)
			return
		}
		s.logger.Warn(
			"request denied by network access policy",
			"remote_addr", r.RemoteAddr,
			"client_ip", address.String(),
			"path", r.URL.Path,
		)
		writeError(
			w,
			http.StatusForbidden,
			"network_access_denied",
			"access is restricted to internal network addresses",
		)
	})
}

func (s *Server) currentAccessConfig() parsedAccessConfig {
	s.accessMu.RLock()
	defer s.accessMu.RUnlock()
	return s.access
}

// loadAccessConfig reads the persisted policy (defaulting to internal) into the
// runtime cache. Called at startup.
func (s *Server) loadAccessConfig(ctx context.Context) {
	config := defaultAccessConfig()
	setting, err := s.store.AppSetting(ctx, accessSettingKey)
	if err == nil {
		var stored accessConfig
		if json.Unmarshal(setting.Value, &stored) == nil {
			if parsed, parseErr := parseAccessConfig(stored); parseErr == nil {
				config = parsed
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		s.logger.Warn("load access policy failed", "error", err)
	}
	s.accessMu.Lock()
	s.access = config
	s.accessMu.Unlock()
}

// handleSecuritySettings reads and writes the network access policy.
//
//	GET /api/settings/security
//	PUT /api/settings/security
func (s *Server) handleSecuritySettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		config := s.currentAccessConfig()
		address := config.clientIP(r)
		cidrs := make([]string, 0, len(config.cidrs))
		for _, prefix := range config.cidrs {
			cidrs = append(cidrs, prefix.String())
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"mode":                config.mode,
				"allowed_cidrs":       cidrs,
				"trust_proxy_headers": config.trustProxy,
				"client_ip":           address.String(),
				"client_allowed":      config.allowed(address),
			},
		})
	case http.MethodPut:
		var request accessConfig
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		parsed, err := parseAccessConfig(request)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_access_policy", err.Error())
			return
		}
		payload, err := json.Marshal(request)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			return
		}
		if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
			Key:   accessSettingKey,
			Value: payload,
		}); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.accessMu.Lock()
		s.access = parsed
		s.accessMu.Unlock()
		s.audit(r, "settings.security.update", "settings", "security", "success")
		address := parsed.clientIP(r)
		cidrs := make([]string, 0, len(parsed.cidrs))
		for _, prefix := range parsed.cidrs {
			cidrs = append(cidrs, prefix.String())
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"mode":                parsed.mode,
				"allowed_cidrs":       cidrs,
				"trust_proxy_headers": parsed.trustProxy,
				"client_ip":           address.String(),
				"client_allowed":      parsed.allowed(address),
			},
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
