package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"vocat/internal/auth"
)

// routeTokensAPI serves the WebUI's API-token lifecycle:
//
//	GET    /api/settings/tokens         list tokens (never the credentials)
//	POST   /api/settings/tokens         create a token; the raw value is
//	                                   returned exactly once, in this response
//	DELETE /api/settings/tokens/<name>  revoke one token
//
// Creation is limited to the administrator's browser session: a bearer token
// must not be able to mint sibling credentials, and the raw value lives only
// in the creation response. The CLI remains the path for automation.
func (s *Server) routeTokensAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	if cleanPath == "settings/tokens" {
		switch r.Method {
		case http.MethodGet:
			s.handleListAPITokens(w, r)
		case http.MethodPost:
			s.handleCreateAPIToken(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
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

func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	// Session-only: a bearer credential must not mint more credentials.
	if _, bearer := bearerPrincipalFromRequest(r); bearer {
		writeError(w, http.StatusForbidden, "session_required", "api tokens are created from the web session or the CLI")
		return
	}
	var request struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
		TTLHours int    `json:"ttl_hours"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.TTLHours < 0 || request.TTLHours > 24*365 {
		writeError(w, http.StatusBadRequest, "invalid_ttl", "ttl_hours must be between 0 (never expires) and 8760")
		return
	}
	credentials, err := s.auth.CreateAPIToken(
		r.Context(),
		request.Name,
		request.ReadOnly,
		time.Duration(request.TTLHours)*time.Hour,
	)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidTokenName):
			writeError(w, http.StatusBadRequest, "invalid_token_name", err.Error())
		case errors.Is(err, auth.ErrDuplicateTokenName):
			writeError(w, http.StatusConflict, "duplicate_token_name", err.Error())
		default:
			s.logger.Error("create api token failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		}
		return
	}
	s.audit(r, "settings.tokens.create", "api_token", credentials.Name, "success")
	expires := ""
	if !credentials.ExpiresAt.IsZero() {
		expires = credentials.ExpiresAt.Format(time.RFC3339)
	}
	// The one place a raw credential appears in a response body; treat it
	// like the login endpoints and keep it out of any cache.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"token":      credentials.Token,
			"name":       credentials.Name,
			"read_only":  credentials.ReadOnly,
			"expires_at": expires,
		},
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
