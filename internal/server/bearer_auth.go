package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"vocat/internal/auth"
)

type apiTokenContextKey struct{}

// bearerToken returns the credential from an Authorization: Bearer header.
// A header that names the Bearer scheme without a credential is reported as
// present with an empty token and fails closed in requireBearer. Other
// schemes and absent headers return false; the session-cookie flow stays
// authoritative for those requests.
func bearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < len(bearerScheme) ||
		!strings.EqualFold(header[:len(bearerScheme)], bearerScheme) {
		return "", false
	}
	if len(header) == len(bearerScheme) {
		return "", true
	}
	return strings.TrimSpace(header[len(bearerScheme):]), true
}

const bearerScheme = "Bearer"

// mutatingMethod matches the methods the CSRF gate already treats as
// state-changing; read-only tokens are held to the same line.
func mutatingMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// mutatingBearerRequest also accounts for the legacy GET endpoints that
// perform hardware operations or establish a bidirectional media channel.
// HTTP methods alone do not enforce a read-only credential boundary.
func mutatingBearerRequest(r *http.Request) bool {
	if mutatingMethod(r.Method) {
		return true
	}
	if r.Method != http.MethodGet {
		return false
	}
	// Mirror the actual API router's normalization and segment unescaping,
	// including encoded operation segments and trailing slashes.
	cleanPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/")
	segments := splitAPIPath(cleanPath)
	if len(segments) < 3 || segments[0] != "devices" {
		return false
	}
	switch strings.Join(segments[2:], "/") {
	case "esim/actions/download", "calls/media", "operator_selection/scan", "operator_selection/scan/stream":
		return true
	default:
		return false
	}
}

// requireBearer authenticates a request that carried a Bearer header and
// returns the request carrying the token principal in its context. The
// session-cookie flow, including CSRF, is deliberately skipped: a token in
// an Authorization header is not ambient browser credential. Invalid tokens
// write the response themselves; the dedicated error keeps cookie sessions
// alive and never redirects HTML clients the way authenticationRequired
// would.
func (s *Server) requireBearer(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	token, _ := bearerToken(r)
	principal, err := s.auth.AuthenticateAPIToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, auth.ErrUnauthorized) {
			s.logger.Warn("api token rejected", "error", err)
			// Junk probes stay in the log; only well-formed vcat_ tokens reach
			// the audit trail.
			if wellFormedTokenPrefix(token) {
				s.recordAudit(r.Context(), "", "auth.token", "api_token", "", "failure", requestRemoteHost(r))
			}
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusUnauthorized, "invalid_token", "invalid or expired api token")
			return r, false
		}
		// A store failure is not the client's fault: tell it the server
		// failed instead of training it to discard a valid credential.
		s.logger.Error("api token authentication failed", "error", err)
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return r, false
	}
	if principal.ReadOnly && mutatingBearerRequest(r) {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusForbidden, "token_read_only", "this api token is read-only")
		return r, false
	}
	return r.WithContext(context.WithValue(r.Context(), apiTokenContextKey{}, principal)), true
}

// wellFormedTokenPrefix reports whether a rejected credential at least
// looks like a Vocat token, gating which failures reach the audit trail.
func wellFormedTokenPrefix(token string) bool {
	return strings.HasPrefix(token, auth.APITokenPrefix)
}

// bearerPrincipalFromRequest resolves the identity stashed by requireBearer;
// false means the request was session-authenticated.
func bearerPrincipalFromRequest(r *http.Request) (auth.APITokenPrincipal, bool) {
	principal, ok := r.Context().Value(apiTokenContextKey{}).(auth.APITokenPrincipal)
	return principal, ok
}
