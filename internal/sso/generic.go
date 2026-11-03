package sso

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

const (
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token"
	githubUserURL      = "https://api.github.com/user"
)

type oauthProvider struct {
	config    Config
	oauth     oauth2.Config
	client    *http.Client
	userURL   string
	field     string
	authority string
}

// newOAuthProvider validates configured endpoints and builds the GitHub or generic OAuth2 adapter.
func newOAuthProvider(ctx context.Context, config Config, secret string, client *http.Client, policy *destinationPolicy) (Provider, error) {
	authorize, token, user := config.AuthURL, config.TokenURL, config.UserInfoURL
	field, authority := config.UserIDField, config.ProviderInstanceID
	if config.Kind == "github" {
		authorize, token, user = githubAuthorizeURL, githubTokenURL, githubUserURL
		field, authority = "id", "https://github.com"
	}
	for i, endpoint := range []string{authorize, token, user} {
		if err := requireEndpoint(ctx, policy, endpoint, i == 0); err != nil {
			return nil, err
		}
	}
	return &oauthProvider{
		config: config,
		oauth: oauth2.Config{
			ClientID:     config.ClientID,
			ClientSecret: secret,
			Endpoint:     oauth2.Endpoint{AuthURL: authorize, TokenURL: token, AuthStyle: authStyle(config.TokenAuthMethod)},
			Scopes:       config.Scopes,
			RedirectURL:  CallbackURL(config),
		},
		client: client, userURL: user, field: field, authority: authority,
	}, nil
}

// AuthorizationURL validates the flow and adds an S256 challenge when PKCE is enabled.
func (p *oauthProvider) AuthorizationURL(flow AuthorizationFlow) (string, error) {
	if !validAuthorizationFlow(p.config, flow, false) {
		return "", ErrInvalidIdentity
	}
	options := []oauth2.AuthCodeOption{}
	if p.config.PKCEMode == "s256" {
		options = append(options, oauth2.S256ChallengeOption(flow.PKCEVerifier))
	}
	return p.oauth.AuthCodeURL(flow.State, options...), nil
}

// VerifyCallback exchanges the code and reads a stable identity from the configured user endpoint.
// Provider tokens are used only for this exchange and are never returned or persisted.
func (p *oauthProvider) VerifyCallback(ctx context.Context, flow AuthorizationFlow, code, responseIssuer string) (ExternalIdentity, error) {
	if code == "" || len(code) > 8192 || !validExchangeFlow(p.config, flow) {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.client)
	options := []oauth2.AuthCodeOption{}
	if p.config.PKCEMode == "s256" {
		options = append(options, oauth2.VerifierOption(flow.PKCEVerifier))
	}
	token, err := p.oauth.Exchange(ctx, code, options...)
	if err != nil || token.AccessToken == "" || len(token.AccessToken) > MaxIDTokenBytes || hasControl(token.AccessToken) {
		return ExternalIdentity{}, ErrProviderUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userURL, nil)
	if err != nil {
		return ExternalIdentity{}, ErrProviderUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Accept", "application/json")
	if p.config.Kind == "github" {
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		request.Header.Set("User-Agent", "VoCat-SSO")
	}
	response, err := p.client.Do(request)
	if err != nil {
		return ExternalIdentity{}, ErrProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ExternalIdentity{}, ErrProviderUnavailable
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ExternalIdentity{}, ErrProviderUnavailable
	}
	var claims map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&claims) != nil || claims == nil || decoder.Decode(new(any)) != io.EOF {
		return ExternalIdentity{}, ErrProviderUnavailable
	}
	subject, err := stableID(claims[p.field])
	if err != nil {
		return ExternalIdentity{}, ErrInvalidIdentity
	}
	if p.config.Kind == "github" {
		number, ok := claims[p.field].(json.Number)
		integer, valid := new(big.Int).SetString(string(number), 10)
		if !ok || !valid || integer.Sign() <= 0 {
			return ExternalIdentity{}, ErrInvalidIdentity
		}
	}
	name := displayClaim(claims["name"])
	if name == "" {
		name = displayClaim(claims["login"])
	}
	return ExternalIdentity{Protocol: "oauth2", Authority: p.authority, Subject: subject, DisplayName: name, Email: displayClaim(claims["email"])}, nil
}

// authStyle maps an explicit token authentication method to the OAuth2 client setting.
func authStyle(method string) oauth2.AuthStyle {
	if method == "client_secret_post" {
		return oauth2.AuthStyleInParams
	}
	return oauth2.AuthStyleInHeader
}

// validAuthorizationFlow requires state, an OIDC nonce when applicable, and a valid exchange flow.
func validAuthorizationFlow(config Config, flow AuthorizationFlow, oidc bool) bool {
	return flow.State != "" && (!oidc || flow.Nonce != "") && validExchangeFlow(config, flow)
}

// validExchangeFlow checks the exact callback URI and the configured PKCE requirements.
func validExchangeFlow(config Config, flow AuthorizationFlow) bool {
	if flow.RedirectURI == "" || flow.RedirectURI != CallbackURL(config) {
		return false
	}
	if config.PKCEMode != "s256" {
		return config.Kind == "generic" && config.PKCEMode == "disabled"
	}
	if len(flow.PKCEVerifier) < 43 || len(flow.PKCEVerifier) > 128 {
		return false
	}
	for _, c := range flow.PKCEVerifier {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}

// stableID preserves string identifiers and arbitrary-size integers while rejecting ambiguous JSON values.
func stableID(value any) (string, error) {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" || len(v) > 2048 || hasControl(v) {
			return "", ErrInvalidIdentity
		}
		return v, nil
	case json.Number:
		// Reject decimal/exponent encodings, preserving every integer digit.
		if strings.ContainsAny(string(v), ".eE") {
			return "", ErrInvalidIdentity
		}
		integer, ok := new(big.Int).SetString(string(v), 10)
		if !ok || len(v) > 2048 {
			return "", ErrInvalidIdentity
		}
		return integer.String(), nil
	default:
		return "", fmt.Errorf("%w: stable user ID is missing or not a string/integer", ErrInvalidIdentity)
	}
}

// displayClaim accepts only bounded strings without control characters for display metadata.
func displayClaim(value any) string {
	text, ok := value.(string)
	if !ok || len(text) > 512 || hasControl(text) {
		return ""
	}
	return text
}
