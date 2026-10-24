package server

import (
	"context"
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
		s.logger.Warn("api token rejected", "error", err)
		// Junk probes stay in the log; only well-formed vcat_ tokens reach
		// the audit trail.
		if strings.HasPrefix(token, auth.APITokenPrefix) {
			s.recordAudit(r.Context(), "", "auth.token", "api_token", "", "failure", requestRemoteHost(r))
		}
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusUnauthorized, "invalid_token", "invalid or expired api token")
		return r, false
	}
	if principal.ReadOnly && mutatingMethod(r.Method) {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusForbidden, "token_read_only", "this api token is read-only")
		return r, false
	}
	return r.WithContext(context.WithValue(r.Context(), apiTokenContextKey{}, principal)), true
}

// bearerPrincipalFromRequest resolves the identity stashed by requireBearer;
// false means the request was session-authenticated.
func bearerPrincipalFromRequest(r *http.Request) (auth.APITokenPrincipal, bool) {
	principal, ok := r.Context().Value(apiTokenContextKey{}).(auth.APITokenPrincipal)
	return principal, ok
}
