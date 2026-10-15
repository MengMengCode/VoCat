package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type e911TestController struct {
	fakeVoWiFiController
	identity vowifi.SIMIdentity
	readErr  error
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
