package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vocat/internal/auth"
	"vocat/internal/sso"
	"vocat/internal/store"
)

// Write DTO intentionally omits the store-owned revision and namespace. The
// JSON decoder rejects attempts to set them, or to disable password login.
type ssoConfigInput struct {
	Enabled         bool     `json:"enabled"`
	Kind            string   `json:"kind"`
	DisplayName     string   `json:"display_name"`
	PublicOrigin    string   `json:"public_origin"`
	ClientID        string   `json:"client_id"`
	IssuerURL       string   `json:"issuer_url,omitempty"`
	AuthURL         string   `json:"auth_url,omitempty"`
	TokenURL        string   `json:"token_url,omitempty"`
	UserInfoURL     string   `json:"user_info_url,omitempty"`
	UserIDField     string   `json:"user_id_field,omitempty"`
	Scopes          []string `json:"scopes"`
	TokenAuthMethod string   `json:"token_auth_method"`
	PKCEMode        string   `json:"pkce_mode"`
}

// config copies writable provider fields into the store model while leaving revision and namespace store-owned.
func (input ssoConfigInput) config() store.SSOConfig {
	return store.SSOConfig{
		Enabled: input.Enabled, Kind: input.Kind, DisplayName: input.DisplayName,
		PublicOrigin: input.PublicOrigin, ClientID: input.ClientID, IssuerURL: input.IssuerURL,
		AuthURL: input.AuthURL, TokenURL: input.TokenURL, UserInfoURL: input.UserInfoURL, UserIDField: input.UserIDField,
		Scopes: input.Scopes, TokenAuthMethod: input.TokenAuthMethod, PKCEMode: input.PKCEMode,
	}
}

// The read contract exposes the revision clients need for writes, while keeping
// the store's identity namespace private and present in persisted configuration.
type ssoConfigView struct {
	Revision int64 `json:"revision"`
	ssoConfigInput
}

// ssoPublicConfig exposes provider settings and revision without the private identity namespace.
func ssoPublicConfig(config store.SSOConfig) ssoConfigView {
	return ssoConfigView{
		Revision: config.Revision,
		ssoConfigInput: ssoConfigInput{
			Enabled: config.Enabled, Kind: config.Kind, DisplayName: config.DisplayName,
			PublicOrigin: config.PublicOrigin, ClientID: config.ClientID, IssuerURL: config.IssuerURL,
			AuthURL: config.AuthURL, TokenURL: config.TokenURL, UserInfoURL: config.UserInfoURL,
			UserIDField: config.UserIDField, Scopes: config.Scopes,
			TokenAuthMethod: config.TokenAuthMethod, PKCEMode: config.PKCEMode,
		},
	}
}

// handleSSOSettings reads or saves SSO settings through browser-session authorization and revision checks.
// Writes require session-bound CSRF, reject secret masks, and report whether saving revoked the current session.
func (s *Server) handleSSOSettings(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		snapshot, err := s.sso.Snapshot(r.Context())
		if err != nil {
			s.writeSSOError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": ssoSettingsDTO(snapshot)})
	case http.MethodPut:
		var request struct {
			ExpectedRevision *int64          `json:"expected_revision"`
			Config           *ssoConfigInput `json:"config"`
			ClientSecret     json.RawMessage `json:"client_secret"`
			ResetBinding     bool            `json:"reset_binding"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || request.Config == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "config and expected_revision are required; read-only fields cannot be submitted")
			return
		}
		var secret *string
		if len(request.ClientSecret) > 0 {
			var value string
			if string(request.ClientSecret) == "null" || json.Unmarshal(request.ClientSecret, &value) != nil || strings.TrimSpace(value) == "" || strings.Trim(value, "*•● ") == "" || len(value) > 4096 {
				writeError(w, http.StatusBadRequest, "invalid_request", "client_secret must be a nonempty new value; omit it to keep the saved secret")
				return
			}
			secret = &value
		}
		config, err := sso.NormalizeConfig(request.Config.config())
		if err != nil {
			s.writeSSOError(w, err)
			return
		}

		snapshot, revoked, err := s.sso.Save(r.Context(), store.SSOSaveRequest{
			ExpectedRevision: *request.ExpectedRevision, Config: config,
			ClientSecret: secret,
			ResetBinding: request.ResetBinding, CurrentSessionHash: session.TokenHash(),
		})
		if err != nil {
			s.writeSSOError(w, err)
			return
		}
		if revoked {
			s.clearAuthCookies(w, r)
		}
		s.recordAudit(r.Context(), session.Principal.Username, "settings.sso.update", "sso", fmt.Sprintf("%s:%d", snapshot.Config.Kind, snapshot.Config.Revision), "success", requestRemoteHost(r))
		data := ssoSettingsDTO(snapshot)
		data["reauthentication_required"] = revoked
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// ssoSettingsDTO returns editable defaults or saved settings, binding metadata, and secret-presence flags.
func ssoSettingsDTO(snapshot store.SSOSnapshot) map[string]any {
	config := snapshot.Config
	if !snapshot.HasConfiguration {
		config, _ = sso.NormalizeConfig(store.SSOConfig{Kind: "pocketid"})
	}
	return map[string]any{
		"config": ssoPublicConfig(config), "binding": ssoBindingDTO(snapshot.Binding),
		"configured":        sso.ValidateComplete(config, snapshot.ClientSecret) == nil,
		"has_client_secret": snapshot.ClientSecret != "",
	}
}

// ssoBindingDTO exposes display metadata and timestamps without external subjects or internal binding identifiers.
func ssoBindingDTO(binding *store.SSOBinding) map[string]any {
	value := map[string]any{"bound": false, "display_name": "", "email": "", "created_at": nil, "last_login_at": nil}
	if binding != nil {
		value["bound"], value["display_name"], value["email"] = true, binding.DisplayName, binding.Email
		value["created_at"] = binding.CreatedAt.Format(time.RFC3339)
		if !binding.LastLoginAt.IsZero() {
			value["last_login_at"] = binding.LastLoginAt.Format(time.RFC3339)
		}
	}
	return value
}

// handleSSOCheck validates saved provider readiness within browser authorization, rate, and revision boundaries.
func (s *Server) handleSSOCheck(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	session, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	if !s.allowSSOManagement(w, r, session, "check") {
		return
	}
	var request struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "a saved configuration revision is required")
		return
	}
	if err := s.sso.Check(r.Context(), *request.ExpectedRevision); err != nil {
		s.writeSSOError(w, err)
		return
	}
	s.recordAudit(r.Context(), session.Principal.Username, "settings.sso.check", "sso", fmt.Sprint(*request.ExpectedRevision), "success", requestRemoteHost(r))
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"revision": *request.ExpectedRevision,
	}})
}

// Protocol errors can contain OAuth response bodies. Only fixed messages cross
// the HTTP or logging boundary; the original error is never printed here.
func (s *Server) writeSSOError(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	status, code, message := http.StatusInternalServerError, "internal_error", "an internal error occurred"
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "unauthorized", "authentication is required"
	case errors.Is(err, store.ErrSSOInvalidFlow):
		status, code, message = http.StatusUnauthorized, "unauthorized", "the initiating browser session is no longer valid"
	case errors.Is(err, store.ErrSSOConfigChanged):
		status, code, message = http.StatusConflict, "sso_config_changed", "SSO configuration changed; reload and try again"
	case errors.Is(err, store.ErrSSORebindRequired):
		status, code, message = http.StatusConflict, "sso_rebind_required", "changing this identity source requires explicit reset and a new binding"
	case errors.Is(err, store.ErrSSOBindingExists):
		status, code, message = http.StatusConflict, "sso_binding_exists", "an account is already bound; unbind it before binding another account"
	case errors.Is(err, store.ErrSSOIdentityNotBound):
		status, code, message = http.StatusConflict, "sso_identity_not_bound", "bind an account before enabling SSO"
	case errors.Is(err, store.ErrSSODisabled):
		status, code, message = http.StatusForbidden, "sso_disabled", "SSO login is disabled"
	case errors.Is(err, store.ErrSSONotConfigured):
		status, code, message = http.StatusBadRequest, "sso_not_configured", "save all required settings and the client secret first"
	case errors.Is(err, sso.ErrInvalidConfiguration):
		status, code, message = http.StatusBadRequest, "invalid_sso_config", "invalid SSO configuration; check the provider fields, URL and HTTPS settings"
	case errors.Is(err, store.ErrSSORateLimited):
		status, code, message = http.StatusTooManyRequests, "sso_rate_limited", "too many SSO attempts; complete an existing flow or try again later"
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "sso_provider_unavailable", "the identity provider timed out"
	case errors.Is(err, sso.ErrProviderUnavailable), errors.Is(err, sso.ErrInvalidIdentity):
		status, code, message = http.StatusBadGateway, "sso_provider_unavailable", "the identity provider could not complete the request"
	}
	writeError(w, status, code, message)
}
