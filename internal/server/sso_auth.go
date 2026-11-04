package server

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"vocat/internal/auth"
	"vocat/internal/sso"
	"vocat/internal/store"
)

const (
	ssoCookiePrefix    = "vocat_sso_"
	ssoStartPerMinute  = 10
	ssoManagePerMinute = 5
)

type ssoRateWindow struct {
	start time.Time
	count int
}
type ssoRateLimiter struct {
	mu      sync.Mutex
	windows map[string]ssoRateWindow
}

// newSSORateLimiter creates independent in-memory rate windows for SSO starts and management actions.
func newSSORateLimiter() *ssoRateLimiter {
	return &ssoRateLimiter{windows: make(map[string]ssoRateWindow)}
}

// allow prunes expired windows and admits requests only while the key remains below its per-minute limit.
func (limiter *ssoRateLimiter) allow(key string, limit int) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := time.Now()
	if len(limiter.windows) >= 2048 {
		for candidate, window := range limiter.windows {
			if now.Sub(window.start) >= time.Minute {
				delete(limiter.windows, candidate)
			}
		}
	}
	window := limiter.windows[key]
	if window.start.IsZero() || now.Sub(window.start) >= time.Minute {
		if window.start.IsZero() && len(limiter.windows) >= 10000 {
			return false
		}
		window = ssoRateWindow{start: now}
	}
	if window.count >= limit {
		return false
	}
	window.count++
	limiter.windows[key] = window
	return true
}

// requireBrowserSession deliberately rejects Bearer before reading cookies,
// including requests carrying both kinds of credential.
func (s *Server) requireBrowserSession(w http.ResponseWriter, r *http.Request) (auth.AuthenticatedSession, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if _, present := bearerToken(r); present {
		writeError(w, http.StatusForbidden, "session_required", "SSO credentials are managed from a browser session")
		return auth.AuthenticatedSession{}, false
	}
	token, ok := s.sessionToken(w, r)
	if !ok {
		return auth.AuthenticatedSession{}, false
	}
	var session auth.AuthenticatedSession
	var err error
	if mutatingMethod(r.Method) {
		csrf, valid := s.validateDoubleSubmitCSRF(w, r)
		if !valid {
			return auth.AuthenticatedSession{}, false
		}
		session, err = s.auth.ValidateCSRF(r.Context(), token, csrf)
	} else {
		session, err = s.auth.Authenticate(r.Context(), token)
	}
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			s.authenticationRequired(w, r)
		case errors.Is(err, auth.ErrInvalidCSRF):
			writeError(w, http.StatusForbidden, "invalid_csrf", "CSRF validation failed")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		}
		return auth.AuthenticatedSession{}, false
	}
	return session, true
}

// handleAuthProviders exposes only enabled SSO capability and its display label, without configuration or secrets.
func (s *Server) handleAuthProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	snapshot, err := s.sso.Snapshot(r.Context())
	if err != nil {
		s.writeSSOError(w, err)
		return
	}
	capability := map[string]any{"enabled": false}
	if snapshot.Config.Enabled && snapshot.Binding != nil && sso.ValidateComplete(snapshot.Config, snapshot.ClientSecret) == nil {
		capability = map[string]any{"enabled": true, "display_name": snapshot.Config.DisplayName}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"sso": capability}})
}

// handleSSOStart rate-limits anonymous login starts and creates a browser-associated authorization redirect.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ip := s.currentAccessConfig().clientIP(r).String()
	if !s.ssoLimiter.allow("start:"+ip, ssoStartPerMinute) {
		w.Header().Set("Retry-After", "60")
		s.writeSSOError(w, store.ErrSSORateLimited)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["redirect"]) > 1 || len(r.URL.RawQuery) > 16384 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid redirect parameter")
		return
	}
	snapshot, err := s.sso.Snapshot(r.Context())
	if err != nil {
		s.writeSSOError(w, err)
		return
	}
	origin, err := s.ssoRequestOrigin(r, snapshot.Config.PublicOrigin)
	if err != nil {
		s.writeSSOError(w, err)
		return
	}

	started, err := s.sso.Start(r.Context(), sso.StartRequest{PublicOrigin: origin, Purpose: "login", ExpectedRevision: snapshot.Config.Revision, ReturnPath: query.Get("redirect"), BrowserFlowIDs: s.ssoBrowserFlows(r)})
	if err != nil {
		s.writeSSOError(w, err)
		return
	}
	s.setSSOFlowCookie(w, started.Flow, started.BrowserToken, false)
	s.auditSSO(r, "", "auth.sso.start", started.Flow, "success")
	http.Redirect(w, r, started.AuthorizationURL, http.StatusFound)
}

// allowSSOManagement independently limits provider checks and binding starts by client IP and browser session.
func (s *Server) allowSSOManagement(w http.ResponseWriter, r *http.Request, session auth.AuthenticatedSession, operation string) bool {
	ip := s.currentAccessConfig().clientIP(r).String()
	if !s.ssoLimiter.allow(operation+":ip:"+ip, ssoManagePerMinute) || !s.ssoLimiter.allow(operation+":session:"+hex.EncodeToString(session.TokenHash()), ssoManagePerMinute) {
		w.Header().Set("Retry-After", "60")
		s.writeSSOError(w, store.ErrSSORateLimited)
		return false
	}
	return true
}

// handleSSOBind requires session-bound CSRF and a saved revision before starting an administrator identity binding.
func (s *Server) handleSSOBind(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	session, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	if !s.allowSSOManagement(w, r, session, "bind") {
		return
	}
	var request struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "a saved configuration revision is required")
		return
	}
	snapshot, err := s.sso.Snapshot(r.Context())
	if err != nil {
		s.writeSSOError(w, err)
		return
	}
	origin, err := s.ssoRequestOrigin(r, snapshot.Config.PublicOrigin)
	if err != nil {
		s.writeSSOError(w, err)
		return
	}
	started, err := s.sso.Start(r.Context(), sso.StartRequest{
		PublicOrigin: origin, Purpose: "bind", ExpectedRevision: *request.ExpectedRevision, ReturnPath: "/settings",
		AdminID: session.Principal.ID, SessionHash: session.TokenHash(), SessionExpiresAt: session.ExpiresAt,
		BrowserFlowIDs: s.ssoBrowserFlows(r),
	})
	if err != nil {
		s.writeSSOError(w, err)
		return
	}
	s.setSSOFlowCookie(w, started.Flow, started.BrowserToken, false)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"authorization_url": started.AuthorizationURL}})
}

// handleSSOUnbind removes the binding at the expected revision and clears credentials if the current SSO session was revoked.
func (s *Server) handleSSOUnbind(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}
	session, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "a saved configuration revision is required")
		return
	}
	snapshot, revoked, err := s.store.UnbindSSO(r.Context(), *request.ExpectedRevision, session.TokenHash())
	if err != nil {
		s.writeSSOError(w, err)
		return
	}
	if revoked {
		s.clearAuthCookies(w, r)
	}
	s.recordAudit(r.Context(), session.Principal.Username, "auth.sso.unbind", "sso", fmt.Sprintf("%s:%d", snapshot.Config.Kind, snapshot.Config.Revision), "success", requestRemoteHost(r))
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"reauthentication_required": revoked}})
}

// handleSSOCallback validates browser possession, claims one flow, and verifies the external identity.
// Binding or login authorization is rechecked atomically before any local session credentials are issued.
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 16384 || len(query["state"]) != 1 || len(query.Get("state")) > 256 {
		s.redirectSSOError(w, r, nil, "invalid_flow")
		return
	}
	for _, name := range []string{"code", "error", "iss", "error_description", "error_uri"} {
		if len(query[name]) > 1 || len(query.Get(name)) > 4096 {
			s.redirectSSOError(w, r, nil, "invalid_flow")
			return
		}
	}
	flow, err := s.sso.FindFlow(r.Context(), query.Get("state"))
	if err != nil {
		s.redirectSSOError(w, r, nil, "invalid_flow")
		return
	}
	flowOrigin, ok := strings.CutSuffix(flow.RedirectURI, sso.CallbackPath)
	if !ok {
		s.redirectSSOError(w, r, nil, "invalid_flow")
		return
	}
	requestOrigin, originErr := s.ssoRequestOrigin(r, flowOrigin)
	if originErr != nil || requestOrigin != flowOrigin {
		s.redirectSSOError(w, r, nil, "invalid_flow")
		return
	}
	cookie, err := r.Cookie(ssoCookiePrefix + flow.ID)
	if err != nil || subtle.ConstantTimeCompare(auth.TokenHash(cookie.Value), flow.BrowserTokenHash) != 1 {
		s.redirectSSOError(w, r, nil, "invalid_flow")
		return
	}
	claimed, err := s.sso.Claim(r.Context(), query.Get("state"), cookie.Value)
	s.setSSOFlowCookie(w, flow, "", true)
	if err != nil {
		s.redirectSSOError(w, r, &flow, ssoCallbackError(err))
		return
	}
	flow = claimed
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
		defer cancel()
		_ = s.store.DeleteSSOFlow(cleanupContext, flow.ID)
	}()
	if _, present := query["iss"]; present && query.Get("iss") == "" {
		s.redirectSSOError(w, r, &flow, "invalid_identity")
		return
	}
	code, providerError := query.Get("code"), query.Get("error")
	if code == "" && providerError != "" {
		category := "provider_unavailable"
		if providerError == "access_denied" {
			category = "access_denied"
		}
		s.redirectSSOError(w, r, &flow, category)
		return
	}
	if code == "" || providerError != "" {
		s.redirectSSOError(w, r, &flow, "invalid_flow")
		return
	}

	identity, err := s.sso.Verify(r.Context(), flow, code, query.Get("iss"))
	if err != nil {
		s.redirectSSOError(w, r, &flow, ssoCallbackError(err))
		return
	}
	if flow.Purpose == "bind" {
		_, err := s.store.FinalizeSSOBinding(r.Context(), flow, identity)
		if err != nil {
			s.redirectSSOError(w, r, &flow, ssoCallbackError(err))
			return
		}
		s.auditSSO(r, "", "auth.sso.bind", flow, "success")
		http.Redirect(w, r, "/settings?sso_result=bound", http.StatusSeeOther)
		return
	}
	admin, err := s.store.CurrentAdmin(r.Context())
	if err != nil {
		s.redirectSSOError(w, r, &flow, "invalid_flow")
		return
	}
	credentials, err := s.auth.PrepareCredentials(auth.Principal{ID: admin.ID, Username: admin.Username})
	if err != nil {
		s.redirectSSOError(w, r, &flow, "provider_unavailable")
		return
	}
	secure := s.secureCookiesForRequest(r) || strings.HasPrefix(flow.RedirectURI, "https://")
	_, err = s.store.FinalizeSSOLogin(r.Context(), flow, identity, store.SSOSessionMaterial{
		SecureCookies: secure,
		TokenHash:     auth.TokenHash(credentials.SessionToken), CSRFHash: auth.TokenHash(credentials.CSRFToken), ExpiresAt: credentials.ExpiresAt,
	})
	if err != nil {
		s.redirectSSOError(w, r, &flow, ssoCallbackError(err))
		return
	}
	s.setAuthCookies(w, credentials.SessionToken, credentials.CSRFToken, credentials.ExpiresAt, secure)
	s.auditSSO(r, admin.Username, "auth.sso.login", flow, "success")
	http.Redirect(w, r, sso.SafeRedirect(flow.ReturnPath, ""), http.StatusSeeOther)
}

// redirectSSOError returns fixed callback categories to the appropriate local page and bounds failure audit events.
func (s *Server) redirectSSOError(w http.ResponseWriter, r *http.Request, flow *store.SSOFlow, category string) {
	query := url.Values{"sso_error": {category}}
	path := "/login"
	if flow != nil {
		if flow.Purpose == "bind" {
			path = "/settings"
		} else {
			query.Set("redirect", sso.SafeRedirect(flow.ReturnPath, ""))
		}
		// Invalid callbacks are independently bounded to avoid log amplification.
		if s.ssoLimiter.allow("callback-log:"+requestRemoteHost(r), ssoStartPerMinute) {
			s.auditSSO(r, "", "auth.sso."+flow.Purpose, *flow, category)
		}
	}
	http.Redirect(w, r, path+"?"+query.Encode(), http.StatusSeeOther)
}

// ssoCallbackError maps internal failures to safe callback categories without exposing provider response bodies.
func ssoCallbackError(err error) string {
	switch {
	case errors.Is(err, store.ErrSSOConfigChanged), errors.Is(err, store.ErrSSODisabled):
		return "config_changed"
	case errors.Is(err, store.ErrSSOFlowExpired):
		return "flow_expired"
	case errors.Is(err, store.ErrSSOIdentityNotBound):
		return "identity_not_bound"
	case errors.Is(err, store.ErrSSOBindingExists):
		return "binding_exists"
	case errors.Is(err, sso.ErrInvalidIdentity), errors.Is(err, store.ErrSSOInvalidIdentity):
		return "invalid_identity"
	case errors.Is(err, store.ErrSSOInvalidFlow), errors.Is(err, store.ErrNotFound):
		return "invalid_flow"
	default:
		return "provider_unavailable"
	}
}

// setSSOFlowCookie sets or clears a flow-scoped HttpOnly Lax cookie so cross-site callbacks can prove browser possession.
func (s *Server) setSSOFlowCookie(w http.ResponseWriter, flow store.SSOFlow, token string, clear bool) {
	maxAge := int(time.Until(flow.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	cookie := &http.Cookie{Name: ssoCookiePrefix + flow.ID, Value: token, Path: "/api/auth/sso", HttpOnly: true, Secure: s.secureCookies || strings.HasPrefix(flow.RedirectURI, "https://"), SameSite: http.SameSiteLaxMode, Expires: flow.ExpiresAt, MaxAge: maxAge}
	if clear {
		cookie.Value = ""
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, cookie)
}

// ssoBrowserFlows extracts a bounded set of plausible flow IDs from browser cookies for per-browser quotas.
func (s *Server) ssoBrowserFlows(r *http.Request) []string {
	ids := make([]string, 0, store.SSOMaxBrowserFlows)
	for _, cookie := range r.Cookies() {
		if id, ok := strings.CutPrefix(cookie.Name, ssoCookiePrefix); ok && len(id) <= 64 && len(id) >= 16 {
			ids = append(ids, id)
			if len(ids) >= 100 {
				break
			}
		}
	}
	return ids
}

// auditSSO records provider kind, revision, actor, and outcome without authorization codes, flow secrets, or provider tokens.
func (s *Server) auditSSO(r *http.Request, actor, action string, flow store.SSOFlow, outcome string) {
	kind := "changed"
	if snapshot, err := s.sso.Snapshot(r.Context()); err == nil && snapshot.Config.ProviderInstanceID == flow.ProviderInstanceID {
		kind = snapshot.Config.Kind
	}
	if actor == "" && flow.AdminID != 0 {
		if admin, err := s.store.CurrentAdmin(r.Context()); err == nil && admin.ID == flow.AdminID {
			actor = admin.Username
		}
	}
	s.recordAudit(r.Context(), actor, action, "sso", fmt.Sprintf("%s:%d", kind, flow.ConfigRevision), outcome, requestRemoteHost(r))
}

// StartSSOCleanup runs expired-flow cleanup until the application lifetime context ends.
func (s *Server) StartSSOCleanup(ctx context.Context) { go s.sso.RunCleanup(ctx) }
