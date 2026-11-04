package sso

import (
	"errors"
	"reflect"
	"testing"

	"vocat/internal/store"
)

// TestPublicOriginIPv6Normalization matches browser host serialization while preserving allowed ports.
func TestPublicOriginIPv6Normalization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://[2001:0DB8:0:0:0:0:0:1]:8443", "https://[2001:db8::1]:8443"},
		{"http://[0:0:0:0:0:0:0:1]:7575", "http://[::1]:7575"},
		{"http://[::ffff:192.168.1.20]:7575", "http://[::ffff:c0a8:114]:7575"},
		{"http://[::ffff:10.0.0.2]:80", "http://[::ffff:a00:2]"},
		{"https://[FD00:0:0:0:0:0:0:1]:443", "https://[fd00::1]"},
		{"https://Example.COM:8443", "https://example.com:8443"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizePublicOrigin(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			config, err := NormalizeConfig(Config{PublicOrigin: tc.input})
			if err != nil || config.PublicOrigin != tc.want {
				t.Fatalf("config origin %q, %v; want %q", config.PublicOrigin, err, tc.want)
			}
		})
	}
}

// TestConfigDraftAndDefaults checks offline draft validation, provider defaults, and URL and scope normalization.
func TestConfigDraftAndDefaults(t *testing.T) {
	c, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "pocketid" || c.Enabled || c.TokenAuthMethod != "auto" || c.PKCEMode != "s256" || !reflect.DeepEqual(c.Scopes, []string{"openid", "profile", "email"}) {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if !errors.Is(ValidateComplete(c, ""), store.ErrSSONotConfigured) {
		t.Fatal("draft was treated as complete")
	}
	c.PublicOrigin = "https://VoCat.Example:443/"
	c.IssuerURL = "https://id.example/path/"
	c.Scopes = []string{"openid", "openid", "email"}
	c, err = NormalizeConfig(c)
	if err != nil || c.IssuerURL != "https://id.example/path/" || c.PublicOrigin != "https://vocat.example" || CallbackURL(c) != "https://vocat.example/api/auth/sso/callback" || len(c.Scopes) != 2 {
		t.Fatalf("normalization failed: %+v %v", c, err)
	}
}

// TestConfigTokenAuthenticationDefaultsAndCompatibility checks provider defaults and limits auto selection to OIDC.
func TestConfigTokenAuthenticationDefaultsAndCompatibility(t *testing.T) {
	for kind, defaultMethod := range map[string]string{"pocketid": "auto", "github": "client_secret_basic", "generic": "client_secret_post"} {
		t.Run(kind, func(t *testing.T) {
			c, err := NormalizeConfig(Config{Kind: kind})
			if err != nil || c.TokenAuthMethod != defaultMethod {
				t.Fatalf("unexpected default token authentication: %q, %v", c.TokenAuthMethod, err)
			}
			for _, method := range []string{"client_secret_basic", "client_secret_post"} {
				c, err = NormalizeConfig(Config{Kind: kind, TokenAuthMethod: method})
				if err != nil || c.TokenAuthMethod != method {
					t.Fatalf("explicit token authentication %q lost: %q, %v", method, c.TokenAuthMethod, err)
				}
			}
		})
	}
	for _, kind := range []string{"github", "generic"} {
		if _, err := NormalizeConfig(Config{Kind: kind, TokenAuthMethod: "auto"}); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("%s accepted automatic authentication without Discovery: %v", kind, err)
		}
	}
}

// TestConfigExplicitProviderAddressesDoNotNeedConnectionOptions verifies explicit private endpoints need no network access to save drafts.
func TestConfigExplicitProviderAddressesDoNotNeedConnectionOptions(t *testing.T) {
	for _, publicOrigin := range []string{"http://localhost:5173", "https://vocat.nas.example:8443", "https://vocat.example"} {
		pocket, err := NormalizeConfig(Config{Kind: "pocketid", PublicOrigin: publicOrigin, IssuerURL: "https://id.nas.invalid/path/"})
		if err != nil || pocket.IssuerURL != "https://id.nas.invalid/path/" {
			t.Fatalf("Pocket ID draft address changed or required networking: %+v, %v", pocket, err)
		}
		generic, err := NormalizeConfig(Config{Kind: "generic", PublicOrigin: publicOrigin, AuthURL: "https://id.nas.invalid/authorize", TokenURL: "http://192.168.1.20:1411/token", UserInfoURL: "http://id.nas.invalid/userinfo"})
		if err != nil || generic.TokenURL != "http://192.168.1.20:1411/token" || generic.UserInfoURL != "http://id.nas.invalid/userinfo" {
			t.Fatalf("explicit Generic private HTTP draft rejected: %+v, %v", generic, err)
		}
	}
}

// TestConfigRejectsUnsafeOrUnsupportedFields covers invalid providers, URLs, scopes, and protocol options.
func TestConfigRejectsUnsafeOrUnsupportedFields(t *testing.T) {
	cases := []Config{
		{Kind: "saml"},
		{PublicOrigin: "https://user:pass@vocat.example"},
		{PublicOrigin: "http://vocat.example"},
		{PublicOrigin: "https://vocat.example/vocat/"},
		{PublicOrigin: "https://vocat.example?"},
		{PublicOrigin: "https://vocat.example#"},
		{PublicOrigin: "https://vocat.example:0"},
		{PublicOrigin: "https://vocat.example:"},
		{PublicOrigin: "https://[fe80::1%25en0]"},
		{IssuerURL: "http://127.0.0.1:1234"},
		{IssuerURL: "https://id.example#fragment"},
		{IssuerURL: "https://id.example/#"},
		{IssuerURL: "https://id.example#"},
		{IssuerURL: " https://id.example"},
		{PKCEMode: "disabled"},
		{Scopes: []string{"openid", "offline_access"}},
		{Scopes: []string{"profile"}},
		{Scopes: []string{"openid profile"}},
		{TokenAuthMethod: "none"},
		{Kind: "github", AuthURL: "https://evil.example/auth"},
		{Kind: "generic", AuthURL: "http://id.example/auth"},
		{Kind: "generic", TokenURL: "http://user@id.example/token"},
		{Kind: "generic", TokenURL: "http://id.example/token?secret=x"},
		{Kind: "generic", UserInfoURL: "http://id.example/userinfo#fragment"},
		{Kind: "generic", TokenURL: "https://id.example/token?secret=x"},
		{Kind: "generic", AuthURL: "https://id.example/authorize#"},
		{Kind: "generic", TokenURL: "https://id.example/token#"},
	}
	for _, c := range cases {
		t.Run(c.Kind+"/"+c.PublicOrigin+c.IssuerURL+c.TokenURL+c.AuthURL+c.PKCEMode, func(t *testing.T) {
			if _, err := NormalizeConfig(c); err == nil {
				t.Fatalf("unsafe config accepted: %+v", c)
			}
		})
	}
	for _, publicOrigin := range []string{"http://127.0.0.1:5173", "http://localhost:5173", "http://[::1]:5173"} {
		if _, err := NormalizeConfig(Config{PublicOrigin: publicOrigin}); err != nil {
			t.Errorf("loopback development origin rejected: %v", err)
		}
	}
}
