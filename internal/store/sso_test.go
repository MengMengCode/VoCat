package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var ssoTestSequence atomic.Int64

// ssoTestHash produces deterministic credential digests for store fixtures.
func ssoTestHash(label string) []byte {
	digest := sha256.Sum256([]byte(label))
	return digest[:]
}

// ssoTestDatabase creates an isolated database with a password session and disabled SSO configuration.
func ssoTestDatabase(t *testing.T) (*Store, SSOSnapshot, []byte) {
	t.Helper()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "sso.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.SetAdmin(ctx, "admin", []byte("password hash")); err != nil {
		t.Fatal(err)
	}
	session := ssoTestHash("password session")
	if err := database.CreateSession(ctx, 1, session, ssoTestHash("csrf"), time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	secret := "initial-secret"
	snapshot, revoked, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{
		Config: SSOConfig{Kind: "pocketid", ClientID: "vocat", IssuerURL: "https://id.example.com",
			PublicOrigin: "https://vocat.example.com", Scopes: []string{"openid", "profile", "email"},
			TokenAuthMethod: "client_secret_basic", PKCEMode: "s256"},
		ClientSecret: &secret, CurrentSessionHash: session,
	})
	if err != nil || revoked {
		t.Fatalf("initial configuration: revoked=%v error=%v", revoked, err)
	}
	return database, snapshot, session
}

// ssoTestFlow persists a uniquely identified flow with optional browser and initiating-session context.
func ssoTestFlow(t *testing.T, database *Store, snapshot SSOSnapshot, purpose string, session []byte, browserIDs ...string) SSOFlow {
	t.Helper()
	id := fmt.Sprintf("flow-%d", ssoTestSequence.Add(1))
	flow := SSOFlow{
		ID: id, StateHash: ssoTestHash(id + "state"), BrowserTokenHash: ssoTestHash(id + "browser"),
		Purpose: purpose, ProviderInstanceID: snapshot.Config.ProviderInstanceID, ConfigRevision: snapshot.Config.Revision,
		NonceHash: ssoTestHash(id + "nonce"), PKCEVerifier: id + "verifier", RedirectURI: "https://vocat.example.com/api/auth/sso/callback",
		ReturnPath: "/devices/a?tab=sms#latest", CreatedAt: time.Now().UTC().Truncate(time.Second),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if purpose == "bind" {
		flow.AdminID = 1
		flow.InitiatingSessionHash = session
	}
	if err := database.CreateSSOFlow(context.Background(), flow, browserIDs); err != nil {
		t.Fatal(err)
	}
	return flow
}

// ssoTestClaim claims a fixture flow using its matching state and browser digests.
func ssoTestClaim(t *testing.T, database *Store, flow SSOFlow) SSOFlow {
	t.Helper()
	claimed, err := database.ClaimSSOFlow(context.Background(), flow.StateHash, flow.BrowserTokenHash)
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}

// ssoTestIdentity returns a stable identity matching the fixture provider.
func ssoTestIdentity() SSOIdentity {
	return SSOIdentity{Protocol: "oidc", Authority: "https://id.example.com", Subject: "stable-sub", DisplayName: "Example user", Email: "user@example.com"}
}

// ssoTestBound binds the fixture identity and enables SSO while retaining the password session.
func ssoTestBound(t *testing.T) (*Store, SSOSnapshot, []byte) {
	t.Helper()
	database, snapshot, session := ssoTestDatabase(t)
	flow := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "bind", session))
	snapshot, err := database.FinalizeSSOBinding(context.Background(), flow, ssoTestIdentity())
	if err != nil || snapshot.Binding == nil || snapshot.Config.Enabled {
		t.Fatalf("bind while disabled: binding=%v error=%v", snapshot.Binding, err)
	}
	snapshot.Config.Enabled = true
	snapshot, revoked, err := database.SaveSSOConfiguration(context.Background(), SSOSaveRequest{
		ExpectedRevision: snapshot.Config.Revision, Config: snapshot.Config, CurrentSessionHash: session,
	})
	if err != nil || revoked || !snapshot.Config.Enabled {
		t.Fatalf("enable bound provider: revoked=%v error=%v", revoked, err)
	}
	return database, snapshot, session
}

// ssoTestMaterial creates unique session and CSRF digests for finalization tests.
func ssoTestMaterial() SSOSessionMaterial {
	id := fmt.Sprintf("session-%d", ssoTestSequence.Add(1))
	return SSOSessionMaterial{TokenHash: ssoTestHash(id), CSRFHash: ssoTestHash(id + "csrf"), ExpiresAt: time.Now().Add(time.Hour)}
}

// TestSSOMigration28PreservesExistingData checks upgrade compatibility for sessions, tokens, devices, and disabled SSO defaults.
func TestSSOMigration28PreservesExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 28; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("baseline migration %d: %v", version, err)
			}
		}
	}
	for _, statement := range []string{
		`PRAGMA user_version = 28`,
		`INSERT INTO admins VALUES (1, 'admin', X'1234', 1, 1)`,
		`INSERT INTO sessions VALUES (X'1234', 1, X'5678', 4102444800, 1)`,
		`INSERT INTO api_tokens (name, token_hash, created_at) VALUES ('existing', X'4321', 1)`,
		`INSERT INTO devices (id, name, created_at, updated_at) VALUES ('modem', 'Existing modem', 1, 1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	session, err := database.SessionByTokenHash(ctx, []byte{0x12, 0x34})
	if err != nil || session.LoginMethod != "password" || session.SSOBindingID != "" || !bytes.Equal(session.CSRFHash, []byte{0x56, 0x78}) {
		t.Fatalf("existing session changed: %+v error=%v", session, err)
	}
	if _, err := database.APITokenByName(ctx, "existing"); err != nil {
		t.Fatal(err)
	}
	if devices, err := database.ListDevices(ctx); err != nil || len(devices) != 1 || devices[0].Name != "Existing modem" {
		t.Fatalf("devices lost: %+v error=%v", devices, err)
	}
	snapshot, err := database.SSOSnapshot(ctx)
	if err != nil || snapshot.HasConfiguration || snapshot.Config.Enabled || snapshot.Binding != nil || snapshot.Config.Revision != 0 {
		t.Fatalf("upgrade activated sso: %+v error=%v", snapshot, err)
	}
}

// TestSSOBrowserPossessionAndSingleClaim checks browser proof, competing claims, and removal of the persisted PKCE verifier.
func TestSSOBrowserPossessionAndSingleClaim(t *testing.T) {
	database, snapshot, session := ssoTestDatabase(t)
	flow := ssoTestFlow(t, database, snapshot, "bind", session)
	if _, err := database.ClaimSSOFlow(context.Background(), flow.StateHash, ssoTestHash("wrong browser")); !errors.Is(err, ErrSSOInvalidFlow) {
		t.Fatalf("wrong browser error=%v", err)
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := database.ClaimSSOFlow(context.Background(), flow.StateHash, flow.BrowserTokenHash)
			if err == nil {
				if claimed.PKCEVerifier != flow.PKCEVerifier {
					t.Errorf("claimed verifier missing")
				}
				successes.Add(1)
			} else if !errors.Is(err, ErrSSOInvalidFlow) {
				t.Errorf("unexpected competing callback error=%v", err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful claims=%d", successes.Load())
	}
	var verifier string
	if err := database.db.QueryRow(`SELECT pkce_verifier FROM sso_flows WHERE id = ?`, flow.ID).Scan(&verifier); err != nil || verifier != "" {
		t.Fatalf("claimed verifier persisted: %q error=%v", verifier, err)
	}
}

// TestSSOConfigAtomicSecretAndNamespace checks atomic secret updates, revision conflicts, and identity namespace changes.
func TestSSOConfigAtomicSecretAndNamespace(t *testing.T) {
	database, snapshot, session := ssoTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	originalID := snapshot.Config.ProviderInstanceID
	config := snapshot.Config
	config.DisplayName = "New label"
	changed, revoked, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{
		ExpectedRevision: config.Revision, Config: config, CurrentSessionHash: session,
	})
	if err != nil || revoked || changed.ClientSecret != snapshot.ClientSecret || changed.Config.ProviderInstanceID != originalID {
		t.Fatalf("preserve secret: %+v revoked=%v error=%v", changed.Config, revoked, err)
	}
	if _, _, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{ExpectedRevision: config.Revision, Config: config}); !errors.Is(err, ErrSSOConfigChanged) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	config = changed.Config
	config.ClientID = "new-client"
	if _, _, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{ExpectedRevision: config.Revision, Config: config}); !errors.Is(err, ErrSSONotConfigured) {
		t.Fatalf("old secret inherited by changed source: %v", err)
	}
	for _, invalid := range []string{"", "   ", SecretMask} {
		if _, _, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{ExpectedRevision: config.Revision, Config: changed.Config, ClientSecret: &invalid}); err == nil {
			t.Fatalf("invalid secret %q accepted", invalid)
		}
	}
	newSecret := "replacement-secret"
	if _, err := database.db.Exec(`CREATE TRIGGER reject_sso_secret BEFORE UPDATE ON app_settings WHEN NEW.key = 'auth.sso.client_secret' BEGIN SELECT RAISE(ABORT, 'simulated failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{ExpectedRevision: config.Revision, Config: config, ClientSecret: &newSecret}); err == nil {
		t.Fatal("expected simulated atomic save failure")
	}
	unchanged, err := database.SSOSnapshot(ctx)
	if err != nil || unchanged.Config.Revision != changed.Config.Revision || unchanged.Config.ClientID != changed.Config.ClientID || unchanged.ClientSecret != changed.ClientSecret {
		t.Fatalf("partial save escaped rollback: %+v error=%v", unchanged.Config, err)
	}
	if _, err := database.db.Exec(`DROP TRIGGER reject_sso_secret`); err != nil {
		t.Fatal(err)
	}
	reset, _, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{ExpectedRevision: config.Revision, Config: config, ClientSecret: &newSecret})
	if err != nil || reset.ClientSecret != newSecret || reset.Config.ProviderInstanceID == originalID || reset.Config.Enabled {
		t.Fatalf("reset namespace/secret: %+v error=%v", reset.Config, err)
	}
}

// TestSSOBoundSourceChangeRequiresConfirmation checks a bound identity cannot silently move to a different provider source.
func TestSSOBoundSourceChangeRequiresConfirmation(t *testing.T) {
	database, snapshot, session := ssoTestBound(t)
	config := snapshot.Config
	config.Enabled = false
	config.ClientID = "different-client"
	secret := "new-client-secret"
	request := SSOSaveRequest{ExpectedRevision: config.Revision, Config: config, ClientSecret: &secret, CurrentSessionHash: session}
	if _, _, err := database.SaveSSOConfiguration(context.Background(), request); !errors.Is(err, ErrSSORebindRequired) {
		t.Fatalf("changed bound source without confirmation: %v", err)
	}
	request.ResetBinding = true
	reset, _, err := database.SaveSSOConfiguration(context.Background(), request)
	if err != nil || reset.Binding != nil || reset.Config.Enabled || reset.Config.ProviderInstanceID == snapshot.Config.ProviderInstanceID {
		t.Fatalf("confirmed source change: %+v error=%v", reset.Config, err)
	}
}

// TestSSOClaimedFinalizationRevocation verifies finalization rechecks configuration and session authorization after a flow is claimed.
func TestSSOClaimedFinalizationRevocation(t *testing.T) {
	operations := []string{"disable", "save", "unbind", "password"}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			database, snapshot, session := ssoTestBound(t)
			ctx := context.Background()
			login := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "login", nil))
			bind := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "bind", session))
			switch operation {
			case "disable", "save":
				config := snapshot.Config
				if operation == "disable" {
					config.Enabled = false
				} else {
					config.DisplayName = "Changed label"
				}
				if _, _, err := database.SaveSSOConfiguration(ctx, SSOSaveRequest{ExpectedRevision: config.Revision, Config: config, CurrentSessionHash: session}); err != nil {
					t.Fatal(err)
				}
			case "unbind":
				if _, _, err := database.UnbindSSO(ctx, snapshot.Config.Revision, session); err != nil {
					t.Fatal(err)
				}
			case "password":
				if err := database.SetAdmin(ctx, "admin", []byte("changed hash")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := database.FinalizeSSOLogin(ctx, login, ssoTestIdentity(), ssoTestMaterial()); !errors.Is(err, ErrSSOConfigChanged) {
				t.Fatalf("claimed login survived %s: %v", operation, err)
			}
			if _, err := database.FinalizeSSOBinding(ctx, bind, ssoTestIdentity()); !errors.Is(err, ErrSSOConfigChanged) {
				t.Fatalf("claimed bind survived %s: %v", operation, err)
			}
		})
	}
}

// TestSSOLogoutOnlyCancelsInitiatingBind checks logout invalidates its binding flow without consuming unrelated login flows.
func TestSSOLogoutOnlyCancelsInitiatingBind(t *testing.T) {
	database, snapshot, session := ssoTestBound(t)
	ctx := context.Background()
	login := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "login", nil))
	bind := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "bind", session))
	if err := database.DeleteSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := database.FinalizeSSOBinding(ctx, bind, ssoTestIdentity()); !errors.Is(err, ErrSSOInvalidFlow) {
		t.Fatalf("logged-out bind survived: %v", err)
	}
	material := ssoTestMaterial()
	if _, err := database.FinalizeSSOLogin(ctx, login, ssoTestIdentity(), material); err != nil {
		t.Fatalf("anonymous login canceled by unrelated logout: %v", err)
	}
	if _, err := database.FinalizeSSOLogin(ctx, login, ssoTestIdentity(), ssoTestMaterial()); !errors.Is(err, ErrSSOInvalidFlow) {
		t.Fatalf("finalized login replay succeeded: %v", err)
	}
}

// TestSSOIdentityChecksAndSelectiveSessionRevocation checks exact identity matching and SSO-only session invalidation.
func TestSSOIdentityChecksAndSelectiveSessionRevocation(t *testing.T) {
	database, snapshot, passwordSession := ssoTestBound(t)
	ctx := context.Background()
	if _, err := database.CreateAPIToken(ctx, "keep-token", ssoTestHash("api token"), false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []SSOIdentity{
		{Protocol: "oidc", Authority: "https://id.example.com", Subject: "other-sub", Email: "user@example.com"},
		{Protocol: "oidc", Authority: "https://other.example.com", Subject: "stable-sub"},
		{Protocol: "oauth2", Authority: "https://id.example.com", Subject: "stable-sub"},
	} {
		flow := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "login", nil))
		if _, err := database.FinalizeSSOLogin(ctx, flow, identity, ssoTestMaterial()); !errors.Is(err, ErrSSOIdentityNotBound) {
			t.Fatalf("unbound identity accepted: %v", err)
		}
	}
	flow := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "login", nil))
	material := ssoTestMaterial()
	material.SecureCookies = true
	if _, err := database.FinalizeSSOLogin(ctx, flow, ssoTestIdentity(), material); err != nil {
		t.Fatal(err)
	}
	created, err := database.SessionByTokenHash(ctx, material.TokenHash)
	if err != nil || !created.SecureCookies || created.LoginMethod != "sso" || created.SSOBindingID != snapshot.Binding.ID {
		t.Fatalf("sso session source missing: %+v error=%v", created, err)
	}
	result, reauth, err := database.UnbindSSO(ctx, snapshot.Config.Revision, material.TokenHash)
	if err != nil || !reauth || result.Config.Enabled || result.Binding != nil {
		t.Fatalf("unbinding active sso session: reauth=%v error=%v", reauth, err)
	}
	if _, err := database.SessionByTokenHash(ctx, material.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sso session survived unbinding: %v", err)
	}
	if _, err := database.SessionByTokenHash(ctx, passwordSession); err != nil {
		t.Fatalf("password session revoked: %v", err)
	}
	if _, err := database.APITokenByName(ctx, "keep-token"); err != nil {
		t.Fatalf("api token revoked: %v", err)
	}
}

// TestSSOFlowExpiryAndBrowserLimits checks browser quotas, completed-flow release, and original persisted expiry.
func TestSSOFlowExpiryAndBrowserLimits(t *testing.T) {
	database, snapshot, _ := ssoTestBound(t)
	ctx := context.Background()
	ids := make([]string, 0, SSOMaxBrowserFlows)
	flows := make([]SSOFlow, 0, SSOMaxBrowserFlows)
	for i := 0; i < SSOMaxBrowserFlows; i++ {
		flow := ssoTestFlow(t, database, snapshot, "login", nil, ids...)
		ids = append(ids, flow.ID)
		flows = append(flows, flow)
	}
	sixth := flows[0]
	sixth.ID = "sixth"
	sixth.StateHash = ssoTestHash("sixth state")
	if err := database.CreateSSOFlow(ctx, sixth, ids); !errors.Is(err, ErrSSORateLimited) {
		t.Fatalf("sixth browser flow accepted: %v", err)
	}
	claimed := ssoTestClaim(t, database, flows[0])
	if _, err := database.FinalizeSSOLogin(ctx, claimed, ssoTestIdentity(), ssoTestMaterial()); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSSOFlow(ctx, sixth, ids); err != nil {
		t.Fatalf("completed flow still counted: %v", err)
	}
	claimed = ssoTestClaim(t, database, flows[1])
	if _, err := database.db.Exec(`UPDATE sso_flows SET created_at = ?, expires_at = ? WHERE id = ?`, time.Now().Add(-2*time.Second).Unix(), time.Now().Add(-time.Second).Unix(), claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.FinalizeSSOLogin(ctx, claimed, ssoTestIdentity(), ssoTestMaterial()); !errors.Is(err, ErrSSOFlowExpired) {
		t.Fatalf("claimed flow ignored original database expiration: %v", err)
	}
	if err := database.DeleteExpiredSSOFlows(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sso_flows WHERE id = ?`, claimed.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expired verifier/flow retained: count=%d error=%v", remaining, err)
	}
}

// TestSSOIndependentStorePasswordRevision checks credential resets through another store invalidate old authorization immediately.
func TestSSOIndependentStorePasswordRevision(t *testing.T) {
	database, snapshot, session := ssoTestBound(t)
	ctx := context.Background()
	var path string
	if err := database.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	claimed := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "login", nil))
	if err := other.SetAdmin(ctx, "admin", []byte("new password hash")); err != nil {
		t.Fatal(err)
	}
	current, err := database.SSOSnapshot(ctx)
	if err != nil || current.Config.Revision != snapshot.Config.Revision+1 || current.Binding == nil || current.Config.ProviderInstanceID != snapshot.Config.ProviderInstanceID {
		t.Fatalf("external credential reset was stale or lost binding: %+v error=%v", current.Config, err)
	}
	if _, err := database.SessionByTokenHash(ctx, session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("password reset left old session: %v", err)
	}
	if _, err := database.FinalizeSSOLogin(ctx, claimed, ssoTestIdentity(), ssoTestMaterial()); !errors.Is(err, ErrSSOConfigChanged) {
		t.Fatalf("external credential reset let old login finalize: %v", err)
	}
	fresh := ssoTestClaim(t, database, ssoTestFlow(t, database, current, "login", nil))
	if _, err := database.FinalizeSSOLogin(ctx, fresh, ssoTestIdentity(), ssoTestMaterial()); err != nil {
		t.Fatalf("fresh login needed process restart: %v", err)
	}
}

// TestSSOSessionSourceDatabaseConstraints checks insert and update guards enforce login-method and binding ownership consistency.
func TestSSOSessionSourceDatabaseConstraints(t *testing.T) {
	database, snapshot, session := ssoTestDatabase(t)
	// Seed a second administrator only to isolate the binding-ownership guard.
	// Restore CHECK enforcement before exercising the triggers; foreign keys stay on.
	for _, statement := range []string{
		`PRAGMA ignore_check_constraints = ON`,
		`INSERT INTO admins (id, username, password_hash, created_at, updated_at) VALUES (2, 'other-admin', X'1234', 1, 1)`,
		`PRAGMA ignore_check_constraints = OFF`,
		`INSERT INTO sso_bindings (id, admin_id, provider_instance_id, protocol, authority, subject, created_at, updated_at)
			VALUES ('other-binding', 2, 'other-instance', 'oidc', 'https://id.example.com', 'other-sub', 1, 1)`,
	} {
		if _, err := database.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name      string
		statement string
		args      []any
	}{
		{name: "insert sso without binding", statement: `INSERT INTO sessions (token_hash, admin_id, csrf_hash, expires_at, created_at, login_method) VALUES (X'11', 1, X'22', 4102444800, 1, 'sso')`},
		{name: "insert password with binding", statement: `INSERT INTO sessions (token_hash, admin_id, csrf_hash, expires_at, created_at, sso_binding_id) VALUES (X'11', 1, X'22', 4102444800, 1, 'missing')`},
		{name: "insert sso with missing binding", statement: `INSERT INTO sessions (token_hash, admin_id, csrf_hash, expires_at, created_at, login_method, sso_binding_id) VALUES (X'11', 1, X'22', 4102444800, 1, 'sso', 'missing')`},
		{name: "insert sso with another admin binding", statement: `INSERT INTO sessions (token_hash, admin_id, csrf_hash, expires_at, created_at, login_method, sso_binding_id) VALUES (X'11', 1, X'22', 4102444800, 1, 'sso', 'other-binding')`},
		{name: "update sso without binding", statement: `UPDATE sessions SET login_method = 'sso' WHERE token_hash = ?`, args: []any{session}},
		{name: "update sso with missing binding", statement: `UPDATE sessions SET login_method = 'sso', sso_binding_id = 'missing' WHERE token_hash = ?`, args: []any{session}},
		{name: "update sso with another admin binding", statement: `UPDATE sessions SET login_method = 'sso', sso_binding_id = 'other-binding' WHERE token_hash = ?`, args: []any{session}},
		{name: "update password with binding", statement: `UPDATE sessions SET sso_binding_id = 'other-binding' WHERE token_hash = ?`, args: []any{session}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := database.db.Exec(tc.statement, tc.args...); err == nil || !strings.Contains(err.Error(), "invalid session login source") {
				t.Fatalf("expected session source guard rejection, got %v", err)
			}
		})
	}
	flow := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "bind", session))
	bound, err := database.FinalizeSSOBinding(context.Background(), flow, ssoTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE sessions SET login_method = 'sso', sso_binding_id = ? WHERE token_hash = ?`, bound.Binding.ID, session); err != nil {
		t.Fatalf("valid session source update rejected: %v", err)
	}
	if _, err := database.db.Exec(`UPDATE sessions SET admin_id = 2 WHERE token_hash = ?`, session); err == nil || !strings.Contains(err.Error(), "invalid session login source") {
		t.Fatalf("expected guard rejection when changing session administrator, got %v", err)
	}
}

// TestSSOBindingRejectsOtherIdentityAndPreservesSessionsOnIdempotentUpdate checks identity replacement rejection and safe metadata refresh.
func TestSSOBindingRejectsOtherIdentityAndPreservesSessionsOnIdempotentUpdate(t *testing.T) {
	database, snapshot, passwordSession := ssoTestBound(t)
	ctx := context.Background()
	if _, err := database.CreateAPIToken(ctx, "keep-token", ssoTestHash("idempotent binding api token"), false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	login := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "login", nil))
	material := ssoTestMaterial()
	if _, err := database.FinalizeSSOLogin(ctx, login, ssoTestIdentity(), material); err != nil {
		t.Fatal(err)
	}
	other := ssoTestIdentity()
	other.Subject = "other-sub"
	bind := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "bind", material.TokenHash))
	if _, err := database.FinalizeSSOBinding(ctx, bind, other); !errors.Is(err, ErrSSOBindingExists) {
		t.Fatalf("silently replaced different external identity: %v", err)
	}
	unchanged, err := database.SSOSnapshot(ctx)
	if err != nil || unchanged.Binding == nil || unchanged.Binding.ID != snapshot.Binding.ID ||
		unchanged.Binding.Subject != snapshot.Binding.Subject || unchanged.Config.Revision != snapshot.Config.Revision {
		t.Fatalf("rejected identity changed binding: binding=%+v revision=%d error=%v", unchanged.Binding, unchanged.Config.Revision, err)
	}
	if _, err := database.SessionByTokenHash(ctx, material.TokenHash); err != nil {
		t.Fatalf("rejected identity revoked session: %v", err)
	}
	pendingLogin := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "login", nil))
	refresh := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "bind", material.TokenHash))
	identity := ssoTestIdentity()
	identity.DisplayName, identity.Email = "Updated account name", "updated@example.com"
	current, err := database.FinalizeSSOBinding(ctx, refresh, identity)
	if err != nil || current.Binding == nil || current.Binding.ID != snapshot.Binding.ID ||
		current.Binding.Subject != snapshot.Binding.Subject || current.Binding.DisplayName != identity.DisplayName ||
		current.Binding.Email != identity.Email || current.Config.Revision != snapshot.Config.Revision+1 {
		t.Fatalf("idempotent binding update failed: binding=%+v error=%v", current.Binding, err)
	}
	if session, err := database.SessionByTokenHash(ctx, material.TokenHash); err != nil || session.SSOBindingID != snapshot.Binding.ID {
		t.Fatalf("idempotent update revoked or changed SSO session: binding=%s error=%v", session.SSOBindingID, err)
	}
	if _, err := database.SessionByTokenHash(ctx, passwordSession); err != nil {
		t.Fatalf("idempotent update revoked password session: %v", err)
	}
	if _, err := database.APITokenByName(ctx, "keep-token"); err != nil {
		t.Fatalf("idempotent update revoked API token: %v", err)
	}
	if _, err := database.FinalizeSSOBinding(ctx, bind, ssoTestIdentity()); !errors.Is(err, ErrSSOConfigChanged) {
		t.Fatalf("old binding callback survived revision change: %v", err)
	}
	if _, err := database.FinalizeSSOLogin(ctx, pendingLogin, ssoTestIdentity(), ssoTestMaterial()); !errors.Is(err, ErrSSOConfigChanged) {
		t.Fatalf("old login callback survived revision change: %v", err)
	}
	var remaining int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sso_flows`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("idempotent update retained old flows: count=%d error=%v", remaining, err)
	}
}

// TestSSOBindingInsertFailureRollsBack checks failed persistence retains the binding flow and leaves configuration and sessions unchanged.
func TestSSOBindingInsertFailureRollsBack(t *testing.T) {
	database, snapshot, session := ssoTestDatabase(t)
	ctx := context.Background()
	flow := ssoTestClaim(t, database, ssoTestFlow(t, database, snapshot, "bind", session))
	if _, err := database.db.Exec(`CREATE TRIGGER reject_sso_binding BEFORE INSERT ON sso_bindings BEGIN SELECT RAISE(ABORT, 'simulated failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.FinalizeSSOBinding(ctx, flow, ssoTestIdentity()); err == nil {
		t.Fatal("failed binding insert reported success")
	}
	unchanged, err := database.SSOSnapshot(ctx)
	if err != nil || unchanged.Binding != nil || unchanged.Config.Revision != snapshot.Config.Revision {
		t.Fatalf("failed binding insert committed partial state: binding=%+v revision=%d error=%v", unchanged.Binding, unchanged.Config.Revision, err)
	}
	if _, err := database.SessionByTokenHash(ctx, session); err != nil {
		t.Fatalf("failed binding insert revoked initiating session: %v", err)
	}
	if _, err := database.db.Exec(`DROP TRIGGER reject_sso_binding`); err != nil {
		t.Fatal(err)
	}
	bound, err := database.FinalizeSSOBinding(ctx, flow, ssoTestIdentity())
	if err != nil || bound.Binding == nil || bound.Config.Revision != snapshot.Config.Revision+1 {
		t.Fatalf("failed insert consumed authorization state: binding=%+v revision=%d error=%v", bound.Binding, bound.Config.Revision, err)
	}
}

// TestSSOGlobalFlowLimitAndFailedFlowRelease checks the global flow cap and release after failed verification.
func TestSSOGlobalFlowLimitAndFailedFlowRelease(t *testing.T) {
	database, snapshot, _ := ssoTestBound(t)
	ctx := context.Background()
	flow := ssoTestFlow(t, database, snapshot, "login", nil)
	claimed := ssoTestClaim(t, database, flow)
	if err := database.DeleteSSOFlow(ctx, claimed.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sso_flows WHERE id = ?`, claimed.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed flow was not released: count=%d error=%v", count, err)
	}
	flow = ssoTestFlow(t, database, snapshot, "login", nil)
	// Replicate pending rows without 1000 individual configuration reads.
	_, err := database.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < ?)
		INSERT INTO sso_flows (id, state_hash, browser_token_hash, purpose, provider_instance_id, config_revision,
			nonce_hash, pkce_verifier, redirect_uri, return_path, created_at, expires_at)
		SELECT 'global-' || x, randomblob(32), browser_token_hash, purpose, provider_instance_id, config_revision,
			nonce_hash, pkce_verifier, redirect_uri, return_path, created_at, expires_at FROM n, sso_flows WHERE id = ?`, SSOMaxPendingFlows-1, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	flow.ID = "global-over-limit"
	flow.StateHash = ssoTestHash("global-over-limit")
	if err := database.CreateSSOFlow(ctx, flow, nil); !errors.Is(err, ErrSSORateLimited) {
		t.Fatalf("global pending flow cap ignored: %v", err)
	}
}

// TestSSOBindingConstraintsAndSessionExpiry checks unique administrator bindings and expired initiating-session rejection.
func TestSSOBindingConstraintsAndSessionExpiry(t *testing.T) {
	database, snapshot, session := ssoTestBound(t)
	ctx := context.Background()
	if _, err := database.db.Exec(`INSERT INTO sso_bindings (id, admin_id, provider_instance_id, protocol, authority, subject, created_at, updated_at)
		VALUES ('second-binding', 1, ?, 'oidc', 'https://id.example.com', 'second-sub', 1, 1)`, snapshot.Config.ProviderInstanceID); err == nil {
		t.Fatal("multiple bindings for the single administrator were allowed")
	}
	bind := ssoTestFlow(t, database, snapshot, "bind", session)
	if _, err := database.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`, time.Now().Add(-time.Second).Unix(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimSSOFlow(ctx, bind.StateHash, bind.BrowserTokenHash); !errors.Is(err, ErrSSOInvalidFlow) {
		t.Fatalf("expired initiating session allowed binding: %v", err)
	}
}
