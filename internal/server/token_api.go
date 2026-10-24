package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"vocat/internal/auth"
)

// routeTokensAPI serves the WebUI's API-token inventory:
//
//	GET    /api/settings/tokens         list tokens (never the credentials)
//	DELETE /api/settings/tokens/<name>  revoke one token
//
// Tokens are issued by the CLI only. The endpoint deliberately exposes no
// creation path: a raw token must never transit the browser session, where a
// copy-paste or screenshot would outlive the revocation it cannot perform.
func (s *Server) routeTokensAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	if cleanPath == "settings/tokens" {
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		s.handleListAPITokens(w, r)
		return true
	}
	name, found := strings.CutPrefix(cleanPath, "settings/tokens/")
	if !found {
		return false
	}
	if !requireMethod(w, r, http.MethodDelete) {
		return true
	}
	s.handleRevokeAPIToken(w, r, name)
	return true
}

func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.auth.ListAPITokens(r.Context())
	if err != nil {
		s.logger.Error("list api tokens failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}
	items := make([]map[string]any, 0, len(tokens))
	for _, token := range tokens {
		lastUsed := ""
		if !token.LastUsedAt.IsZero() {
			lastUsed = token.LastUsedAt.Format(time.RFC3339)
		}
		expires := ""
		if !token.ExpiresAt.IsZero() {
			expires = token.ExpiresAt.Format(time.RFC3339)
		}
		items = append(items, map[string]any{
			"name":         token.Name,
			"read_only":    token.ReadOnly,
			"created_at":   token.CreatedAt.Format(time.RFC3339),
			"last_used_at": lastUsed,
			"expires_at":   expires,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"tokens": items},
	})
}

func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request, rawName string) {
	name := strings.TrimSpace(rawName)
	if name == "" || strings.Contains(name, "/") {
		writeError(w, http.StatusBadRequest, "invalid_request", "token name is required")
		return
	}
	if err := s.auth.RevokeAPIToken(r.Context(), name); err != nil {
		if errors.Is(err, auth.ErrUnknownToken) {
			writeError(w, http.StatusNotFound, "not_found", "unknown api token")
			return
		}
		s.logger.Error("revoke api token failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}
	s.audit(r, "settings.tokens.revoke", "api_token", name, "success")
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]bool{"revoked": true},
	})
}
