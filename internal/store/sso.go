package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SSOSameIdentitySource compares only fields that define the identity namespace.
// User-facing labels, transport policy, scopes and credentials can change without
// reassigning an external account to a different security identity.
func SSOSameIdentitySource(a, b SSOConfig) bool {
	if a.Kind != b.Kind || a.ClientID != b.ClientID {
		return false
	}
	switch a.Kind {
	case "pocketid":
		return a.IssuerURL == b.IssuerURL
	case "github":
		return true
	case "generic":
		return a.AuthURL == b.AuthURL && a.TokenURL == b.TokenURL &&
			a.UserInfoURL == b.UserInfoURL && a.UserIDField == b.UserIDField
	default:
		return false
	}
}

// SSOSnapshot reads config, secret and binding in one SQLite transaction. The
// database remains authoritative even when another process updates credentials.
func (s *Store) SSOSnapshot(ctx context.Context) (SSOSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SSOSnapshot{}, fmt.Errorf("begin sso snapshot: %w", err)
	}
	defer tx.Rollback()
	snapshot, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return SSOSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return SSOSnapshot{}, fmt.Errorf("commit sso snapshot: %w", err)
	}
	return snapshot, nil
}

// readSSOSnapshot loads configuration, secret, and a matching binding through one database executor.
func readSSOSnapshot(ctx context.Context, executor contextQueryExecer) (SSOSnapshot, error) {
	var result SSOSnapshot
	setting, err := appSetting(executor.QueryRowContext(ctx, appSettingSelect+` WHERE key = ?`, SSOConfigKey))
	if err == nil {
		if err := json.Unmarshal(setting.Value, &result.Config); err != nil {
			return SSOSnapshot{}, fmt.Errorf("decode sso configuration: %w", err)
		}
		result.HasConfiguration = true
	} else if !errors.Is(err, ErrNotFound) {
		return SSOSnapshot{}, fmt.Errorf("read sso configuration: %w", err)
	}
	setting, err = appSetting(executor.QueryRowContext(ctx, appSettingSelect+` WHERE key = ?`, SSOClientSecretKey))
	if err == nil {
		if err := json.Unmarshal(setting.Value, &result.ClientSecret); err != nil {
			return SSOSnapshot{}, fmt.Errorf("decode sso credential: %w", err)
		}
	} else if !errors.Is(err, ErrNotFound) {
		return SSOSnapshot{}, fmt.Errorf("read sso credential: %w", err)
	}
	binding, err := scanSSOBinding(executor.QueryRowContext(ctx, ssoBindingSelect+` WHERE admin_id = 1`))
	if err == nil && ssoBindingMatchesConfig(binding, result.Config) {
		result.Binding = &binding
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return SSOSnapshot{}, fmt.Errorf("read sso binding: %w", err)
	}
	return result, nil
}

// writeSSOConfig serializes non-secret SSO settings through the caller's transaction.
func writeSSOConfig(ctx context.Context, executor contextQueryExecer, config SSOConfig) error {
	value, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode sso configuration: %w", err)
	}
	return upsertAppSetting(ctx, executor, AppSetting{Key: SSOConfigKey, Value: value})
}

// SaveSSOConfiguration atomically saves both settings and revokes SSO sessions.
// A successful write returns whether the caller's browser session was revoked.
func (s *Store) SaveSSOConfiguration(ctx context.Context, request SSOSaveRequest) (SSOSnapshot, bool, error) {
	if request.ClientSecret != nil && (strings.TrimSpace(*request.ClientSecret) == "" || *request.ClientSecret == SecretMask) {
		return SSOSnapshot{}, false, fmt.Errorf("%w: invalid client secret update", ErrSSONotConfigured)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("begin sso configuration update: %w", err)
	}
	defer tx.Rollback()
	previous, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return SSOSnapshot{}, false, err
	}
	if previous.Config.Revision != request.ExpectedRevision {
		return SSOSnapshot{}, false, ErrSSOConfigChanged
	}
	if _, err := ssoAdmin(ctx, tx); err != nil {
		return SSOSnapshot{}, false, err
	}
	if len(request.CurrentSessionHash) != 0 {
		if err := ssoSessionValid(ctx, tx, request.CurrentSessionHash, 1, time.Now()); err != nil {
			return SSOSnapshot{}, false, err
		}
	}
	changedSource := previous.HasConfiguration && !SSOSameIdentitySource(previous.Config, request.Config)
	if changedSource && previous.Binding != nil && !request.ResetBinding {
		return SSOSnapshot{}, false, ErrSSORebindRequired
	}
	if changedSource && previous.ClientSecret != "" && request.ClientSecret == nil {
		return SSOSnapshot{}, false, fmt.Errorf("%w: re-enter the client secret for the new identity source", ErrSSONotConfigured)
	}
	config := request.Config
	config.Revision = previous.Config.Revision + 1
	config.ProviderInstanceID = previous.Config.ProviderInstanceID
	if changedSource || config.ProviderInstanceID == "" {
		config.ProviderInstanceID, err = newSSOID()
		if err != nil {
			return SSOSnapshot{}, false, err
		}
	}
	if changedSource {
		if request.Config.Enabled {
			return SSOSnapshot{}, false, ErrSSORebindRequired
		}
		config.Enabled = false
	}
	secret := previous.ClientSecret
	if changedSource {
		secret = ""
	}
	if request.ClientSecret != nil {
		secret = *request.ClientSecret
	}
	if config.Enabled && (previous.Binding == nil || !ssoBindingMatchesConfig(*previous.Binding, config) || secret == "") {
		return SSOSnapshot{}, false, ErrSSONotConfigured
	}
	reauthenticated, err := ssoSessionIsSSO(ctx, tx, request.CurrentSessionHash)
	if err != nil {
		return SSOSnapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE login_method = 'sso'`); err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("revoke sso sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_flows`); err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("cancel sso flows: %w", err)
	}
	if changedSource {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sso_bindings`); err != nil {
			return SSOSnapshot{}, false, fmt.Errorf("clear changed sso binding: %w", err)
		}
	}
	if err := writeSSOConfig(ctx, tx, config); err != nil {
		return SSOSnapshot{}, false, err
	}
	if secret == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_settings WHERE key = ?`, SSOClientSecretKey); err != nil {
			return SSOSnapshot{}, false, fmt.Errorf("clear sso credential: %w", err)
		}
	} else {
		value, _ := json.Marshal(secret)
		if err := upsertAppSetting(ctx, tx, AppSetting{Key: SSOClientSecretKey, Value: value, Sensitive: true}); err != nil {
			return SSOSnapshot{}, false, err
		}
	}
	result, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return SSOSnapshot{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("commit sso configuration: %w", err)
	}
	return result, reauthenticated, nil
}

// invalidateSSOAfterAdminUpdate advances the SSO revision and cancels pending flows after a credential reset.
func invalidateSSOAfterAdminUpdate(ctx context.Context, tx *sql.Tx) error {
	setting, err := appSetting(tx.QueryRowContext(ctx, appSettingSelect+` WHERE key = ?`, SSOConfigKey))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sso revision after admin update: %w", err)
	}
	var config SSOConfig
	if err := json.Unmarshal(setting.Value, &config); err != nil {
		return fmt.Errorf("decode sso revision after admin update: %w", err)
	}
	config.Revision++
	if err := writeSSOConfig(ctx, tx, config); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_flows`); err != nil {
		return fmt.Errorf("cancel sso flows after admin update: %w", err)
	}
	return nil
}

// CreateSSOFlow checks current authorization and expiry, enforces flow limits, and stores the flow atomically.
// Binding flows are limited to the lifetime of their initiating session.
func (s *Store) CreateSSOFlow(ctx context.Context, flow SSOFlow, browserFlowIDs []string) error {
	if flow.ID == "" || len(flow.StateHash) != 32 || len(flow.BrowserTokenHash) != 32 ||
		(flow.Purpose != "login" && flow.Purpose != "bind") {
		return ErrSSOInvalidFlow
	}
	now := time.Now().UTC()
	if flow.CreatedAt.IsZero() {
		flow.CreatedAt = now
	}
	if !flow.ExpiresAt.After(now) || !flow.ExpiresAt.After(flow.CreatedAt) || flow.ExpiresAt.After(now.Add(10*time.Minute+time.Second)) {
		return ErrSSOFlowExpired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sso flow: %w", err)
	}
	defer tx.Rollback()
	snapshot, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateSSOFlowAuthorization(ctx, tx, snapshot, flow, now); err != nil {
		return err
	}
	if flow.Purpose == "bind" {
		var sessionExpiry int64
		if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE token_hash = ?`, flow.InitiatingSessionHash).Scan(&sessionExpiry); err != nil {
			return ErrSSOInvalidFlow
		}
		if flow.ExpiresAt.Unix() > sessionExpiry {
			flow.ExpiresAt = time.Unix(sessionExpiry, 0).UTC()
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_flows WHERE expires_at <= ?`, now.Unix()); err != nil {
		return fmt.Errorf("prune expired sso flows: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sso_flows`).Scan(&count); err != nil {
		return fmt.Errorf("count pending sso flows: %w", err)
	}
	if count >= SSOMaxPendingFlows {
		return ErrSSORateLimited
	}
	seen := make(map[string]struct{}, len(browserFlowIDs))
	browserCount := 0
	for _, id := range browserFlowIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sso_flows WHERE id = ? AND expires_at > ?`, id, now.Unix()).Scan(&exists); err != nil {
			return fmt.Errorf("count browser sso flows: %w", err)
		}
		browserCount += exists
		if browserCount >= SSOMaxBrowserFlows {
			return ErrSSORateLimited
		}
	}
	var adminID any
	var initiatingHash any
	if flow.Purpose == "bind" {
		adminID = flow.AdminID
		initiatingHash = flow.InitiatingSessionHash
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sso_flows (
		id, state_hash, browser_token_hash, purpose, provider_instance_id,
		config_revision, initiating_session_hash, admin_id,
		nonce_hash, pkce_verifier, redirect_uri, return_path, created_at, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		flow.ID, flow.StateHash, flow.BrowserTokenHash, flow.Purpose, flow.ProviderInstanceID,
		flow.ConfigRevision, initiatingHash, adminID,
		nilIfEmpty(flow.NonceHash), flow.PKCEVerifier, flow.RedirectURI, flow.ReturnPath,
		flow.CreatedAt.Unix(), flow.ExpiresAt.Unix())
	if err != nil {
		return fmt.Errorf("insert sso flow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sso flow: %w", err)
	}
	return nil
}

// nilIfEmpty maps an empty digest to SQL NULL for optional flow fields.
func nilIfEmpty(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

const ssoFlowSelect = `SELECT id, state_hash, browser_token_hash, purpose,
	provider_instance_id, config_revision, initiating_session_hash, admin_id,
	nonce_hash, pkce_verifier, redirect_uri, return_path,
	created_at, expires_at FROM sso_flows`

// scanSSOFlow decodes nullable authorization fields and UTC timestamps, mapping absent rows to invalid flows.
func scanSSOFlow(row rowScanner) (SSOFlow, error) {
	var flow SSOFlow
	var adminID sql.NullInt64
	var created, expires int64
	err := row.Scan(&flow.ID, &flow.StateHash, &flow.BrowserTokenHash, &flow.Purpose,
		&flow.ProviderInstanceID, &flow.ConfigRevision, &flow.InitiatingSessionHash,
		&adminID, &flow.NonceHash, &flow.PKCEVerifier, &flow.RedirectURI,
		&flow.ReturnPath, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return SSOFlow{}, ErrSSOInvalidFlow
	}
	if err != nil {
		return SSOFlow{}, fmt.Errorf("read sso flow: %w", err)
	}
	flow.AdminID = adminID.Int64
	flow.CreatedAt = time.Unix(created, 0).UTC()
	flow.ExpiresAt = time.Unix(expires, 0).UTC()
	return flow, nil
}

// SSOFlowByStateHash returns an unclaimed flow without consuming it or authorizing a callback.
func (s *Store) SSOFlowByStateHash(ctx context.Context, stateHash []byte) (SSOFlow, error) {
	if len(stateHash) != 32 {
		return SSOFlow{}, ErrSSOInvalidFlow
	}
	return scanSSOFlow(s.db.QueryRowContext(ctx, ssoFlowSelect+` WHERE state_hash = ? AND claimed_at = 0`, stateHash))
}

// ClaimSSOFlow checks browser possession before changing any row. A forged
// callback cannot consume another browser's valid state. The verifier is removed
// from persistent storage as soon as the flow has been claimed once.
func (s *Store) ClaimSSOFlow(ctx context.Context, stateHash, browserHash []byte) (SSOFlow, error) {
	if len(stateHash) != 32 || len(browserHash) != 32 {
		return SSOFlow{}, ErrSSOInvalidFlow
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSOFlow{}, fmt.Errorf("begin sso flow claim: %w", err)
	}
	defer tx.Rollback()
	flow, err := scanSSOFlow(tx.QueryRowContext(ctx, ssoFlowSelect+` WHERE state_hash = ? AND claimed_at = 0`, stateHash))
	if err != nil {
		return SSOFlow{}, err
	}
	if subtle.ConstantTimeCompare(flow.BrowserTokenHash, browserHash) != 1 {
		return SSOFlow{}, ErrSSOInvalidFlow
	}
	now := time.Now().UTC()
	snapshot, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return SSOFlow{}, err
	}
	if err := validateSSOFlowAuthorization(ctx, tx, snapshot, flow, now); err != nil {
		return SSOFlow{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE sso_flows SET claimed_at = ?, pkce_verifier = '' WHERE id = ? AND claimed_at = 0`, now.Unix(), flow.ID)
	if err != nil {
		return SSOFlow{}, fmt.Errorf("claim sso flow: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return SSOFlow{}, ErrSSOInvalidFlow
	}
	if err := tx.Commit(); err != nil {
		return SSOFlow{}, fmt.Errorf("commit sso flow claim: %w", err)
	}
	return flow, nil
}

// validateSSOFlowAuthorization rechecks revision, provider namespace, expiry, and purpose-specific authorization.
func validateSSOFlowAuthorization(ctx context.Context, tx *sql.Tx, snapshot SSOSnapshot, flow SSOFlow, now time.Time) error {
	if snapshot.Config.Revision != flow.ConfigRevision || snapshot.Config.ProviderInstanceID != flow.ProviderInstanceID || !snapshot.HasConfiguration {
		return ErrSSOConfigChanged
	}
	if !flow.ExpiresAt.After(now) {
		return ErrSSOFlowExpired
	}
	if snapshot.ClientSecret == "" {
		return ErrSSONotConfigured
	}
	if flow.Purpose == "login" {
		if len(flow.InitiatingSessionHash) != 0 || flow.AdminID != 0 {
			return ErrSSOInvalidFlow
		}
		if !snapshot.Config.Enabled {
			return ErrSSODisabled
		}
		if snapshot.Binding == nil {
			return ErrSSOIdentityNotBound
		}
		return nil
	}
	if flow.Purpose != "bind" || flow.AdminID != 1 {
		return ErrSSOInvalidFlow
	}
	return ssoSessionValid(ctx, tx, flow.InitiatingSessionHash, flow.AdminID, now)
}

// ssoSessionValid requires a live initiating session belonging to the expected administrator.
func ssoSessionValid(ctx context.Context, tx *sql.Tx, hash []byte, adminID int64, now time.Time) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s JOIN admins a ON a.id = s.admin_id
		WHERE s.token_hash = ? AND s.admin_id = ? AND s.expires_at > ?`, hash, adminID, now.Unix()).Scan(&count); err != nil {
		return fmt.Errorf("check sso initiating session: %w", err)
	}
	if count != 1 {
		return ErrSSOInvalidFlow
	}
	return nil
}

// claimedSSOFlow requires a live, claimed row matching the immutable authorization fields supplied by the caller.
func claimedSSOFlow(ctx context.Context, tx *sql.Tx, flow SSOFlow) error {
	stored, err := scanSSOFlow(tx.QueryRowContext(ctx, ssoFlowSelect+` WHERE id = ? AND state_hash = ? AND claimed_at > 0`, flow.ID, flow.StateHash))
	if err != nil {
		return err
	}
	if !stored.ExpiresAt.After(time.Now()) {
		return ErrSSOFlowExpired
	}
	if stored.ConfigRevision != flow.ConfigRevision || stored.ProviderInstanceID != flow.ProviderInstanceID ||
		stored.Purpose != flow.Purpose || stored.AdminID != flow.AdminID ||
		!bytes.Equal(stored.InitiatingSessionHash, flow.InitiatingSessionHash) || !stored.ExpiresAt.Equal(flow.ExpiresAt) {
		return ErrSSOInvalidFlow
	}
	return nil
}

// validSSOIdentity checks the verified identity protocol and authority against the configured provider.
func validSSOIdentity(identity SSOIdentity, config SSOConfig) bool {
	if identity.Subject == "" || identity.Authority == "" {
		return false
	}
	switch config.Kind {
	case "pocketid":
		return identity.Protocol == "oidc" && identity.Authority == config.IssuerURL
	case "github":
		return identity.Protocol == "oauth2" && identity.Authority == "https://github.com"
	case "generic":
		return identity.Protocol == "oauth2" && identity.Authority == config.ProviderInstanceID
	default:
		return false
	}
}

// ssoBindingMatchesConfig requires both the provider namespace and identity authority to match current settings.
func ssoBindingMatchesConfig(binding SSOBinding, config SSOConfig) bool {
	return binding.ProviderInstanceID == config.ProviderInstanceID && validSSOIdentity(SSOIdentity{
		Protocol: binding.Protocol, Authority: binding.Authority, Subject: binding.Subject,
	}, config)
}

// sameSSOIdentity compares the protocol, authority, and stable subject that identify an external account.
func sameSSOIdentity(binding SSOBinding, identity SSOIdentity) bool {
	return binding.Protocol == identity.Protocol && binding.Authority == identity.Authority && binding.Subject == identity.Subject
}

// FinalizeSSOBinding runs after remote verification. It rechecks the original
// browser session, configuration and expiration while holding the write tx.
func (s *Store) FinalizeSSOBinding(ctx context.Context, flow SSOFlow, identity SSOIdentity) (SSOSnapshot, error) {
	if flow.Purpose != "bind" {
		return SSOSnapshot{}, ErrSSOInvalidFlow
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSOSnapshot{}, fmt.Errorf("begin sso binding: %w", err)
	}
	defer tx.Rollback()
	snapshot, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return SSOSnapshot{}, err
	}
	if err := validateSSOFlowAuthorization(ctx, tx, snapshot, flow, time.Now()); err != nil {
		return SSOSnapshot{}, err
	}
	if err := claimedSSOFlow(ctx, tx, flow); err != nil {
		return SSOSnapshot{}, err
	}
	if !validSSOIdentity(identity, snapshot.Config) {
		return SSOSnapshot{}, ErrSSOInvalidIdentity
	}
	if snapshot.Binding != nil && !sameSSOIdentity(*snapshot.Binding, identity) {
		return SSOSnapshot{}, ErrSSOBindingExists
	}
	now := time.Now().UTC().Unix()
	if snapshot.Binding != nil {
		_, err = tx.ExecContext(ctx, `UPDATE sso_bindings SET display_name = ?, email = ?, updated_at = ? WHERE id = ?`,
			identity.DisplayName, identity.Email, now, snapshot.Binding.ID)
	} else {
		bindingID, idErr := newSSOID()
		if idErr != nil {
			return SSOSnapshot{}, idErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sso_bindings
			(id, admin_id, provider_instance_id, protocol, authority, subject, display_name, email, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, bindingID, flow.AdminID,
			flow.ProviderInstanceID, identity.Protocol, identity.Authority, identity.Subject,
			identity.DisplayName, identity.Email, now, now)
	}
	if err != nil {
		return SSOSnapshot{}, fmt.Errorf("save sso binding: %w", err)
	}
	snapshot.Config.Revision++
	if err := writeSSOConfig(ctx, tx, snapshot.Config); err != nil {
		return SSOSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_flows`); err != nil {
		return SSOSnapshot{}, fmt.Errorf("cancel flows after sso binding: %w", err)
	}
	result, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return SSOSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return SSOSnapshot{}, fmt.Errorf("commit sso binding: %w", err)
	}
	return result, nil
}

// FinalizeSSOLogin is the only path that creates a session marked as SSO.
// No credentials may be delivered to the browser until this transaction commits.
func (s *Store) FinalizeSSOLogin(ctx context.Context, flow SSOFlow, identity SSOIdentity, material SSOSessionMaterial) (Admin, error) {
	if flow.Purpose != "login" || len(material.TokenHash) != 32 || len(material.CSRFHash) != 32 || !material.ExpiresAt.After(time.Now()) {
		return Admin{}, ErrSSOInvalidFlow
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Admin{}, fmt.Errorf("begin sso login: %w", err)
	}
	defer tx.Rollback()
	snapshot, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return Admin{}, err
	}
	if err := validateSSOFlowAuthorization(ctx, tx, snapshot, flow, time.Now()); err != nil {
		return Admin{}, err
	}
	if err := claimedSSOFlow(ctx, tx, flow); err != nil {
		return Admin{}, err
	}
	if snapshot.Binding == nil || !validSSOIdentity(identity, snapshot.Config) || !sameSSOIdentity(*snapshot.Binding, identity) {
		return Admin{}, ErrSSOIdentityNotBound
	}
	admin, err := ssoAdmin(ctx, tx)
	if err != nil {
		return Admin{}, err
	}
	now := time.Now().UTC().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, admin_id, csrf_hash, expires_at, created_at, login_method, sso_binding_id, secure_cookies)
		VALUES (?, ?, ?, ?, ?, 'sso', ?, ?)`, material.TokenHash, admin.ID, material.CSRFHash,
		material.ExpiresAt.Unix(), now, snapshot.Binding.ID, material.SecureCookies)
	if err != nil {
		return Admin{}, fmt.Errorf("create sso session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sso_bindings SET last_login_at = ?, updated_at = ? WHERE id = ?`, now, now, snapshot.Binding.ID); err != nil {
		return Admin{}, fmt.Errorf("touch sso binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_flows WHERE id = ?`, flow.ID); err != nil {
		return Admin{}, fmt.Errorf("consume finalized sso flow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Admin{}, fmt.Errorf("commit sso login: %w", err)
	}
	return admin, nil
}

// UnbindSSO checks the revision and removes the binding, its sessions, and pending flows atomically.
// It disables SSO and reports whether the caller must authenticate again.
func (s *Store) UnbindSSO(ctx context.Context, expectedRevision int64, currentSessionHash []byte) (SSOSnapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("begin sso unbinding: %w", err)
	}
	defer tx.Rollback()
	snapshot, err := readSSOSnapshot(ctx, tx)
	if err != nil {
		return SSOSnapshot{}, false, err
	}
	if snapshot.Config.Revision != expectedRevision {
		return SSOSnapshot{}, false, ErrSSOConfigChanged
	}
	if !snapshot.HasConfiguration {
		return SSOSnapshot{}, false, ErrSSONotConfigured
	}
	if len(currentSessionHash) != 0 {
		if err := ssoSessionValid(ctx, tx, currentSessionHash, 1, time.Now()); err != nil {
			return SSOSnapshot{}, false, err
		}
	}
	reauthentication, err := ssoSessionIsSSO(ctx, tx, currentSessionHash)
	if err != nil {
		return SSOSnapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_bindings`); err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("delete sso binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_flows`); err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("cancel flows after sso unbinding: %w", err)
	}
	snapshot.Config.Enabled = false
	snapshot.Config.Revision++
	if err := writeSSOConfig(ctx, tx, snapshot.Config); err != nil {
		return SSOSnapshot{}, false, err
	}
	snapshot.Binding = nil
	if err := tx.Commit(); err != nil {
		return SSOSnapshot{}, false, fmt.Errorf("commit sso unbinding: %w", err)
	}
	return snapshot, reauthentication, nil
}

// DeleteExpiredSSOFlows removes pending and claimed flows whose original expiration has elapsed.
func (s *Store) DeleteExpiredSSOFlows(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sso_flows WHERE expires_at <= ?`, now.UTC().Unix()); err != nil {
		return fmt.Errorf("delete expired sso flows: %w", err)
	}
	return nil
}

// DeleteSSOFlow releases an already claimed flow after cancellation or remote
// verification failure. Only the trusted callback service should call it after
// browser possession and ClaimSSOFlow have succeeded.
func (s *Store) DeleteSSOFlow(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sso_flows WHERE id = ? AND claimed_at > 0`, id); err != nil {
		return fmt.Errorf("delete claimed sso flow: %w", err)
	}
	return nil
}

// ssoSessionIsSSO reports whether a supplied session digest belongs to an SSO session.
func ssoSessionIsSSO(ctx context.Context, tx *sql.Tx, hash []byte) (bool, error) {
	if len(hash) == 0 {
		return false, nil
	}
	var method string
	err := tx.QueryRowContext(ctx, `SELECT login_method FROM sessions WHERE token_hash = ?`, hash).Scan(&method)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read sso session origin: %w", err)
	}
	return method == "sso", nil
}

// ssoAdmin reads the single administrator within the caller's finalization transaction.
func ssoAdmin(ctx context.Context, tx *sql.Tx) (Admin, error) {
	return scanAdmin(tx.QueryRowContext(ctx, `SELECT id, username, password_hash, created_at, updated_at FROM admins WHERE id = 1`))
}

const ssoBindingSelect = `SELECT id, admin_id, provider_instance_id, protocol,
	authority, subject, display_name, email, created_at, updated_at, last_login_at FROM sso_bindings`

// scanSSOBinding decodes a binding and its UTC timestamps, preserving an unset last-login time.
func scanSSOBinding(row rowScanner) (SSOBinding, error) {
	var binding SSOBinding
	var created, updated, lastLogin int64
	err := row.Scan(&binding.ID, &binding.AdminID, &binding.ProviderInstanceID,
		&binding.Protocol, &binding.Authority, &binding.Subject, &binding.DisplayName,
		&binding.Email, &created, &updated, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return SSOBinding{}, ErrNotFound
	}
	if err != nil {
		return SSOBinding{}, err
	}
	binding.CreatedAt = time.Unix(created, 0).UTC()
	binding.UpdatedAt = time.Unix(updated, 0).UTC()
	if lastLogin != 0 {
		binding.LastLoginAt = time.Unix(lastLogin, 0).UTC()
	}
	return binding, nil
}

// newSSOID generates an opaque random identifier for provider namespaces and bindings.
func newSSOID() (string, error) {
	var buffer [24]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("generate sso id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer[:]), nil
}
