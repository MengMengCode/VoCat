package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
	"vocat/internal/vowifi/e911"
)

type e911SIMController interface {
	WithSIM(context.Context, string, func(vowifi.SIMIdentity, vowifi.AKAProvider) error) error
}

type e911Session struct {
	mu                  sync.Mutex
	id, deviceID, iccid string
	expiresAt           time.Time
	carrier             e911.Carrier
	client              *e911.ATTClient
}

type e911SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*e911Session
}

func newE911SessionManager() *e911SessionManager {
	return &e911SessionManager{sessions: make(map[string]*e911Session)}
}

func (m *e911SessionManager) add(session *e911Session) error {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	session.id = hex.EncodeToString(token[:])
	session.expiresAt = time.Now().Add(15 * time.Minute)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, previous := range m.sessions {
		if time.Now().After(previous.expiresAt) || previous.deviceID == session.deviceID {
			delete(m.sessions, id)
		}
	}
	if len(m.sessions) >= 32 {
		return errors.New("too many active carrier sessions; retry after a session expires")
	}
	m.sessions[session.id] = session
	return nil
}

func (m *e911SessionManager) get(id, deviceID string) *e911Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil || session.deviceID != deviceID || time.Now().After(session.expiresAt) {
		return nil
	}
	return session
}

func (m *e911SessionManager) remove(id, deviceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[id]; session != nil && session.deviceID == deviceID {
		delete(m.sessions, id)
	}
}

func e911CarrierForSnapshot(snapshot *device.Snapshot) e911.Carrier {
	if snapshot == nil {
		return e911.Carrier{}
	}
	mcc, mnc := device.CardMCCMNCWithLength(snapshot.IMSI, snapshot.MNCLength)
	_, name, _, _ := device.CarrierForSIM(device.CarrierIdentity{IMSI: snapshot.IMSI, ICCID: snapshot.ICCID, SPN: snapshot.SPN, GID1: snapshot.GID1, GID2: snapshot.GID2, MNCLength: snapshot.MNCLength})
	return e911.Detect(vowifi.SIMIdentity{IMSI: snapshot.IMSI, ICCID: snapshot.ICCID, HomeMCC: mcc, HomeMNC: mnc, SPN: snapshot.SPN, GID1: snapshot.GID1, GID2: snapshot.GID2}, name)
}

func carrierForE911Identity(identity vowifi.SIMIdentity) e911.Carrier {
	_, name, _, _ := device.CarrierForSIM(device.CarrierIdentity{IMSI: identity.IMSI, ICCID: identity.ICCID, SPN: identity.SPN, GID1: identity.GID1, GID2: identity.GID2, MNCLength: len(identity.HomeMNC)})
	return e911.Detect(identity, name)
}

func (s *Server) handleE911(w http.ResponseWriter, r *http.Request, config store.Device, tail []string) bool {
	controller, ok := s.vowifi.(e911SIMController)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "e911_unavailable", "SIM authentication is not available")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	if len(tail) == 1 {
		if !requireMethod(w, r, http.MethodDelete) {
			return true
		}
		s.e911Sessions.remove(tail[0], config.ID)
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"closed": true}})
		return true
	}
	if len(tail) == 0 {
		if !requireMethod(w, r, http.MethodPost) {
			return true
		}
		var session *e911Session
		var carrier e911.Carrier
		readSIM := false
		err := controller.WithSIM(ctx, config.ID, func(identity vowifi.SIMIdentity, aka vowifi.AKAProvider) error {
			readSIM = true
			carrier = carrierForE911Identity(identity)
			if carrier.Method != "att_nsds" {
				return nil
			}
			client, err := e911.NewATTClient(identity, aka, nil)
			if err != nil {
				return err
			}
			if err := client.Start(ctx); err != nil {
				return err
			}
			session = &e911Session{deviceID: config.ID, iccid: identity.ICCID, carrier: carrier, client: client}
			return nil
		})
		if err != nil {
			if !readSIM {
				err = errors.New("could not read the active SIM; check that the module is available")
			}
			s.e911Error(w, err)
			return true
		}
		if carrier.ID == "" {
			writeError(w, http.StatusConflict, "e911_not_us", "E911 setup is available for US SIMs only")
			return true
		}
		if session == nil {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"carrier": carrier}})
			return true
		}
		if err := s.e911Sessions.add(session); err != nil {
			s.e911Error(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": session.view()})
		return true
	}
	if len(tail) != 2 || (tail[1] != "verify" && tail[1] != "submit" && tail[1] != "refresh") {
		writeError(w, http.StatusNotFound, "not_found", "E911 operation was not found")
		return true
	}
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	session := s.e911Sessions.get(tail[0], config.ID)
	if session == nil {
		writeError(w, http.StatusGone, "e911_expired", "carrier session expired; reopen E911 setup")
		return true
	}
	var request struct {
		Address        e911.Address `json:"address"`
		CandidateIndex *int         `json:"candidate_index"`
		TermsAccepted  bool         `json:"terms_accepted"`
	}
	if tail[1] != "refresh" {
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if time.Now().After(session.expiresAt) {
		writeError(w, http.StatusGone, "e911_expired", "carrier session expired; reopen E911 setup")
		return true
	}
	var candidates []e911.Candidate
	readSIM := false
	err := controller.WithSIM(ctx, config.ID, func(identity vowifi.SIMIdentity, _ vowifi.AKAProvider) error {
		readSIM = true
		if identity.ICCID != session.iccid {
			return errors.New("SIM changed; reopen E911 setup for the active SIM")
		}
		var err error
		switch tail[1] {
		case "verify":
			candidates, err = session.client.Verify(ctx, request.Address)
		case "submit":
			if request.CandidateIndex == nil {
				return errors.New("validate and select an address first")
			}
			_, err = session.client.Submit(ctx, *request.CandidateIndex, request.TermsAccepted)
		case "refresh":
			err = session.client.Refresh(ctx)
		}
		return err
	})
	if err != nil {
		if !readSIM {
			err = errors.New("could not read the active SIM; check that the module is available")
		}
		s.e911Error(w, err)
		return true
	}
	if tail[1] == "verify" {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"candidates": candidates}})
		return true
	}
	if session.client.Status.Confirmed {
		// Cache only carrier-confirmed metadata, scoped to the SIM rather than
		// the modem. Opening the dialog always refreshes it from the carrier.
		payload, _ := json.Marshal(session.client.Status)
		if err := s.store.UpsertAppSetting(ctx, store.AppSetting{Key: "e911_confirmed:" + session.iccid, Value: payload}); err != nil {
			s.logger.Warn("could not cache confirmed E911 status", "device_id", config.ID)
		}
	}
	result := session.view()
	if tail[1] == "submit" && session.client.Status.Confirmed && config.VoWiFiEnabled && s.vowifi != nil {
		_, reconnectErr := s.vowifi.RequestReconnect(config.ID)
		result["reconnect_scheduled"] = reconnectErr == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func (session *e911Session) view() map[string]any {
	return map[string]any{"id": session.id, "carrier": session.carrier, "expires_at": session.expiresAt, "terms": session.client.Terms, "status": session.client.Status}
}

func (s *Server) e911Error(w http.ResponseWriter, err error) {
	// The HTTP client and AKA boundary return sanitized errors, never URLs
	// containing tokens, raw carrier responses, APDUs or key material.
	message := strings.TrimSpace(err.Error())
	if len(message) > 400 {
		message = "carrier provisioning failed; retry E911 setup"
	}
	writeError(w, http.StatusBadGateway, "e911_carrier_error", message)
}
