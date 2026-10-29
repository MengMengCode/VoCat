package server

import (
	"errors"
	"net/http"

	"vocat/internal/auth"
)

// MCPHandlers carries the two MCP server variants mounted at /mcp. The
// read-write server exposes every tool; the read-only server omits the
// write tools entirely so a read-only agent cannot even list them.
type MCPHandlers struct {
	ReadWrite http.Handler
	ReadOnly  http.Handler
}

// handleMCP serves the MCP Streamable HTTP endpoint. Bearer authentication
// is required, and the token's read_only flag selects which tool surface
// the session receives. The endpoint is mounted unless MCP is explicitly
// disabled. The existing access-control middleware wraps this route like
// every other, so remote clients still need an admitting mode.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil || s.mcp.ReadWrite == nil || s.mcp.ReadOnly == nil {
		writeError(w, http.StatusNotFound, "not_found", "MCP is not enabled")
		return
	}
	token, present := bearerToken(r)
	if !present {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusUnauthorized, "unauthorized", "MCP requires a bearer api token")
		return
	}
	principal, err := s.auth.AuthenticateAPIToken(r.Context(), token)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		if errors.Is(err, auth.ErrUnauthorized) {
			s.logger.Warn("mcp token rejected", "error", err)
			if wellFormedTokenPrefix(token) {
				s.recordAudit(r.Context(), "", "auth.token", "api_token", "", "failure", requestRemoteHost(r))
			}
			writeError(w, http.StatusUnauthorized, "invalid_token", "invalid or expired api token")
			return
		}
		s.logger.Error("mcp token authentication failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}
	// The SDK handler reads the body itself; apply the same size cap the
	// /api JSON surface enforces so MCP posts cannot bypass it.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxRequestBodyBytes)
	handler := s.mcp.ReadWrite
	if principal.ReadOnly {
		handler = s.mcp.ReadOnly
	}
	handler.ServeHTTP(w, r)
}
