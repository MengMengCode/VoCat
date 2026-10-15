package e911

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"vocat/internal/vowifi"
)

// Client is a per-SIM provisioning session. Implementations either submit an
// address directly or hand off to the carrier's authenticated websheet.
type Client interface {
	Start(context.Context) error
	Refresh(context.Context) error
	Verify(context.Context, Address) ([]Candidate, error)
	Submit(context.Context, int, bool) (Status, error)
	Snapshot() Status
	TermsText() string
	Websheet() *Websheet
	BindAKA(vowifi.AKAProvider) error
}

// Websheet is kept server-side. Only the authenticated portal handoff renders
// these fields; they must not be serialized into device/session JSON or logs.
type Websheet struct {
	URL    string
	Fields url.Values
}

func NewClient(identity vowifi.SIMIdentity, aka vowifi.AKAProvider, carrier Carrier, httpClient *http.Client) (Client, error) {
	switch carrier.Method {
	case "att_nsds":
		return NewATTClient(identity, aka, httpClient)
	case "msp_websheet", "ts43_websheet":
		return NewEntitlementClient(identity, aka, carrier, httpClient)
	default:
		return nil, errors.New("carrier SIM provisioning is not available")
	}
}

func (c *ATTClient) Snapshot() Status    { return c.Status }
func (c *ATTClient) TermsText() string   { return c.Terms }
func (c *ATTClient) Websheet() *Websheet { return nil }
func (c *ATTClient) BindAKA(aka vowifi.AKAProvider) error {
	var err error
	c.eap, err = newEntitlementAKA(c.identity, aka)
	return err
}

func trustedWebsheet(raw string, roots []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || len(raw) > 16384 {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, root := range roots {
		if host == root || strings.HasSuffix(host, "."+root) {
			return true
		}
	}
	return false
}
