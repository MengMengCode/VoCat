package store

import (
	"errors"
	"time"
)

const (
	SSOConfigKey       = "auth.sso.config"
	SSOClientSecretKey = "auth.sso.client_secret"
	SSOMaxBrowserFlows = 5
	SSOMaxPendingFlows = 1000
)

var (
	ErrSSOConfigChanged    = errors.New("sso_config_changed")
	ErrSSODisabled         = errors.New("sso_disabled")
	ErrSSONotConfigured    = errors.New("sso_not_configured")
	ErrSSOInvalidFlow      = errors.New("sso_invalid_flow")
	ErrSSOFlowExpired      = errors.New("sso_flow_expired")
	ErrSSOBindingExists    = errors.New("sso_binding_exists")
	ErrSSORebindRequired   = errors.New("sso_rebind_required")
	ErrSSORateLimited      = errors.New("sso_rate_limited")
	ErrSSOIdentityNotBound = errors.New("sso_identity_not_bound")
	ErrSSOInvalidIdentity  = errors.New("sso_invalid_identity")
)

// SSOConfig excludes the client secret. Revision and namespace are store-owned.
type SSOConfig struct {
	Revision           int64    `json:"revision"`
	ProviderInstanceID string   `json:"provider_instance_id"`
	Enabled            bool     `json:"enabled"`
	Kind               string   `json:"kind"`
	DisplayName        string   `json:"display_name"`
	PublicOrigin       string   `json:"public_origin"`
	ClientID           string   `json:"client_id"`
	IssuerURL          string   `json:"issuer_url,omitempty"`
	AuthURL            string   `json:"auth_url,omitempty"`
	TokenURL           string   `json:"token_url,omitempty"`
	UserInfoURL        string   `json:"user_info_url,omitempty"`
	UserIDField        string   `json:"user_id_field,omitempty"`
	Scopes             []string `json:"scopes"`
	TokenAuthMethod    string   `json:"token_auth_method"`
	PKCEMode           string   `json:"pkce_mode"`
}

type SSOBinding struct {
	ID                 string
	AdminID            int64
	ProviderInstanceID string
	Protocol           string
	Authority          string
	Subject            string
	DisplayName        string
	Email              string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastLoginAt        time.Time
}

// SSOSnapshot is an internal, atomic view. Never marshal it to HTTP responses.
type SSOSnapshot struct {
	Config           SSOConfig
	ClientSecret     string `json:"-"`
	Binding          *SSOBinding
	HasConfiguration bool
}

type SSOSaveRequest struct {
	ExpectedRevision   int64
	Config             SSOConfig
	ClientSecret       *string
	ResetBinding       bool
	CurrentSessionHash []byte
}

type SSOFlow struct {
	ID                    string
	StateHash             []byte
	BrowserTokenHash      []byte
	Purpose               string
	ProviderInstanceID    string
	ConfigRevision        int64
	InitiatingSessionHash []byte
	AdminID               int64
	NonceHash             []byte
	PKCEVerifier          string
	RedirectURI           string
	ReturnPath            string
	CreatedAt             time.Time
	ExpiresAt             time.Time
}

type SSOIdentity struct {
	Protocol    string
	Authority   string
	Subject     string
	DisplayName string
	Email       string
}

type SSOSessionMaterial struct {
	SecureCookies bool
	TokenHash     []byte
	CSRFHash      []byte
	ExpiresAt     time.Time
}
