package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"vocat/internal/store"
	"vocat/internal/vowifi"
	"vocat/internal/vowifi/e911"
)

type e911TestController struct {
	fakeVoWiFiController
	identity vowifi.SIMIdentity
	readErr  error
}

func TestE911PortalHandoffEscapesTokensAndAllowsOnlyItsCarrier(t *testing.T) {
	portal := &e911.Websheet{URL: "https://spg.vzw.com/SSFGateway/e911Location/changeAddress", Fields: url.Values{"token": {`secret"><script>leak()</script>`}, "line": {"synthetic"}}}
	r := httptest.NewRecorder()
	writeE911Portal(r, httptest.NewRequest(http.MethodGet, "/e911/session/portal", nil), portal)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "secret&#34;&gt;&lt;script&gt;") || strings.Contains(r.Body.String(), "<script>leak()") {
		t.Fatalf("unsafe portal token rendering: status %d", r.Code)
	}
	if r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(r.Header().Get("Content-Security-Policy"), "form-action https://spg.vzw.com;") || !strings.Contains(r.Header().Get("Content-Security-Policy"), "script-src 'nonce-") {
		t.Fatal("portal credentials are not isolated from caching, referrers, or other origins")
	}
	crossSite := httptest.NewRequest(http.MethodGet, "/e911/session/portal", nil)
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	r = httptest.NewRecorder()
	writeE911Portal(r, crossSite, portal)
	if r.Code != http.StatusForbidden || strings.Contains(r.Body.String(), "secret") {
		t.Fatal("cross-site portal handoff was allowed")
	}
}

func TestE911PortalChecksCurrentSIMBeforeRendering(t *testing.T) {
	manager := newE911SessionManager()
	session := &e911Session{deviceID: "dev1", iccid: "old-card"}
	if err := manager.add(session); err != nil {
		t.Fatal(err)
	}
	s := &Server{e911Sessions: manager, vowifi: &e911TestController{identity: vowifi.SIMIdentity{ICCID: "new-card"}}}
	r := httptest.NewRecorder()
	s.handleE911(r, httptest.NewRequest(http.MethodGet, "/e911", nil), store.Device{ID: "dev1"}, []string{session.id, "portal"})
	if r.Code != http.StatusBadGateway || !strings.Contains(r.Body.String(), "SIM changed") {
		t.Fatalf("portal did not check active SIM: %d", r.Code)
	}
}

func (c *e911TestController) WithSIM(_ context.Context, _ string, use func(vowifi.SIMIdentity, vowifi.AKAProvider) error) error {
	if c.readErr != nil {
		return c.readErr
	}
	return use(c.identity, nil)
}

func TestE911RefusesSessionAfterSIMSwitch(t *testing.T) {
	manager := newE911SessionManager()
	session := &e911Session{deviceID: "dev1", iccid: "old-card-secret"}
	if err := manager.add(session); err != nil {
		t.Fatal(err)
	}
	s := &Server{e911Sessions: manager, vowifi: &e911TestController{identity: vowifi.SIMIdentity{ICCID: "new-card-secret"}}}
	r := httptest.NewRecorder()
	s.handleE911(r, httptest.NewRequest(http.MethodPost, "/e911", nil), store.Device{ID: "dev1"}, []string{session.id, "refresh"})
	if r.Code != http.StatusBadGateway || !strings.Contains(r.Body.String(), "SIM changed") || strings.Contains(r.Body.String(), "card-secret") {
		t.Fatalf("unsafe SIM switch result: %d %s", r.Code, r.Body.String())
	}
}

func TestE911DoesNotExposeSIMCommandErrors(t *testing.T) {
	s := &Server{e911Sessions: newE911SessionManager(), vowifi: &e911TestController{readErr: errors.New("AT+CSIM=secret-authentication-data")}}
	r := httptest.NewRecorder()
	s.handleE911(r, httptest.NewRequest(http.MethodPost, "/e911", nil), store.Device{ID: "dev1"}, nil)
	if r.Code != http.StatusBadGateway || strings.Contains(r.Body.String(), "secret-authentication-data") {
		t.Fatalf("SIM error exposed: %d %s", r.Code, r.Body.String())
	}
}
