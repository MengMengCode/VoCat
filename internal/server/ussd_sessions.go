package server

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"vocat/internal/device"
)

// ussdSessionStore is the HTTP-layer counterpart of device.Manager's USSD
// session map. A USSI awaiting-input reply opens a token here so the existing
// continue/cancel endpoints keep working; the token only records which device
// the dialog belongs to — the IMS session owns the actual network dialog.
type ussdSessionStore struct {
	mu       sync.Mutex
	sessions map[string]ussdServerSession
}

type ussdServerSession struct {
	deviceID  string
	createdAt time.Time
}

func newUSSDSessionStore() ussdSessionStore {
	return ussdSessionStore{sessions: make(map[string]ussdServerSession)}
}

func (store *ussdSessionStore) open(deviceID string) string {
	var token [8]byte
	_, _ = rand.Read(token[:])
	id := hex.EncodeToString(token[:])
	store.mu.Lock()
	if store.sessions == nil {
		store.sessions = make(map[string]ussdServerSession)
	}
	store.sessions[id] = ussdServerSession{deviceID: deviceID, createdAt: time.Now().UTC()}
	store.mu.Unlock()
	return id
}

func (store *ussdSessionStore) device(sessionID string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.sessions[strings.TrimSpace(sessionID)]
	if !ok {
		return "", device.ErrUSSDSessionNotFound
	}
	return session.deviceID, nil
}

func (store *ussdSessionStore) drop(sessionID string) {
	store.mu.Lock()
	delete(store.sessions, strings.TrimSpace(sessionID))
	store.mu.Unlock()
}
