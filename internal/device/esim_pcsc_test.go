package device

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"vocat/internal/pcsc"
)

// This card rejects EnableProfile with refresh=true, even with AID addressing.
// A successful refresh=false commit becomes visible only after a host reset.
type profileSwitchPCSCBackend struct {
	mu             sync.Mutex
	iccid          string
	pendingICCID   string
	targetICCID    string
	targetAID      string
	rejectICCID    bool
	resets         int
	resetErr       error
	enableRequests [][]byte
}

// Readers exposes one present eUICC so the manager discovers a PC/SC device.
func (*profileSwitchPCSCBackend) Readers(context.Context) ([]pcsc.Reader, error) {
	return []pcsc.Reader{{Name: "test eUICC reader", USBPath: "test-reader", CardPresent: true}}, nil
}

// Open creates a session that shares the card identity and staged activation.
func (backend *profileSwitchPCSCBackend) Open(context.Context, pcsc.Selector) (pcsc.Card, error) {
	return &profileSwitchPCSCCard{backend: backend}, nil
}

type profileSwitchPCSCCard struct {
	backend      *profileSwitchPCSCBackend
	selectedFile uint16
}

// Transmit emulates profile commits and the identity reads used for verification.
// Requests that require proactive REFRESH are rejected with commandError.
func (card *profileSwitchPCSCCard) Transmit(_ context.Context, command []byte) ([]byte, uint16, error) {
	backend := card.backend
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(command) < 5 {
		return nil, 0, fmt.Errorf("short test APDU: %X", command)
	}
	switch command[1] {
	case 0x70:
		if command[2] == 0 {
			return []byte{1}, 0x9000, nil
		}
		return nil, 0x9000, nil
	case 0xA4:
		if command[2] == 0 && len(command) >= 7 {
			card.selectedFile = binary.BigEndian.Uint16(command[5:7])
		}
		return nil, 0x9000, nil
	case 0xB2:
		if card.selectedFile == 0x2F00 && command[2] == 1 {
			return []byte{0x61, 9, 0x4F, 7, 0xA0, 0, 0, 0, 0x87, 0x10, 2}, 0x9000, nil
		}
		return nil, 0x6A83, nil
	case 0xB0:
		switch card.selectedFile {
		case 0x2FE2:
			data, err := encodeICCID(backend.iccid)
			return data, 0x9000, err
		case 0x6F07:
			return []byte{8, 0x19, 0x32, 0x54, 0x76, 0x98, 0x10, 0x32, 0x54}, 0x9000, nil
		default:
			return nil, 0x6A82, nil
		}
	case 0xE2:
		length := int(command[4])
		if len(command) < 5+length {
			return nil, 0, fmt.Errorf("truncated test STORE DATA")
		}
		request := append([]byte(nil), command[5:5+length]...)
		backend.enableRequests = append(backend.enableRequests, request)
		result := byte(7)
		if refresh := derFindValue(request, 0x81); len(refresh) == 1 && refresh[0] == 0 {
			iccid := derFindValue(request, 0x5A)
			aid := derFindValue(request, 0x4F)
			if (!backend.rejectICCID && decodeICCID(iccid) == backend.targetICCID) ||
				hex.EncodeToString(aid) == backend.targetAID {
				backend.pendingICCID = backend.targetICCID
				result = 0
			}
		}
		return []byte{0xBF, 0x31, 3, 0x80, 1, result}, 0x9000, nil
	default:
		return nil, 0, fmt.Errorf("unexpected test APDU: %X", command)
	}
}

// Close releases a test session without applying a staged profile switch.
func (*profileSwitchPCSCCard) Close() error { return nil }

// CloseWithReset records a reset attempt and applies activation only on success.
// A simulated reset error leaves the old ICCID visible to subsequent sessions.
func (card *profileSwitchPCSCCard) CloseWithReset() error {
	backend := card.backend
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.resets++
	if backend.resetErr != nil {
		return backend.resetErr
	}
	if backend.pendingICCID != "" {
		backend.iccid = backend.pendingICCID
		backend.pendingICCID = ""
	}
	return nil
}

// TestESIMSwitchProfilePCSCUsesHostResetInsteadOfCardRefresh checks that both
// ICCID addressing and AID fallback activate a profile through one host reset
// on a card that rejects proactive REFRESH.
func TestESIMSwitchProfilePCSCUsesHostResetInsteadOfCardRefresh(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rejectICCID  bool
		wantRequests int
	}{
		{name: "ICCID", wantRequests: 1},
		{name: "AID fallback", rejectICCID: true, wantRequests: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const targetICCID = "894921007608519523"
			const targetAID = "a0000005591010ffffffff8900000101"
			backend := &profileSwitchPCSCBackend{
				iccid: "894921007608519524", targetICCID: targetICCID,
				targetAID: targetAID, rejectICCID: tc.rejectICCID,
			}
			manager, err := NewManager(Options{
				Discoverer: staticDiscoverer{}, Opener: &staticOpener{},
				CardReaders: pcsc.NewWithBackend(backend),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Stop(context.Background()) })
			devices := manager.List()
			if len(devices) != 1 {
				t.Fatalf("devices = %d, want one reader", len(devices))
			}
			id := devices[0].ID
			manager.cacheESIMInfo(id, EsimInfo{AID: isdRAID, Profiles: []EsimProfile{{
				ICCID: targetICCID, AID: targetAID,
			}}})

			if err := manager.ESIMSwitchProfile(context.Background(), id, targetICCID, ""); err != nil {
				t.Fatalf("switch on a card that rejects REFRESH: %v", err)
			}
			snapshot, err := manager.Refresh(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.ICCID != targetICCID {
				t.Fatalf("live ICCID = %q, want %q after host reset", snapshot.ICCID, targetICCID)
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.resets != 1 {
				t.Fatalf("card resets = %d, want one", backend.resets)
			}
			if len(backend.enableRequests) != tc.wantRequests {
				t.Fatalf("EnableProfile requests = %d, want %d", len(backend.enableRequests), tc.wantRequests)
			}
			if tc.rejectICCID && len(derFindValue(backend.enableRequests[1], 0x4F)) == 0 {
				t.Fatal("second EnableProfile request did not address the profile by AID")
			}
		})
	}
}

// TestESIMSwitchProfilePCSCRejectsFailedHostReset ensures that an accepted
// EnableProfile cannot be reported as successful when the reset fails and the
// reader continues to expose the original ICCID.
func TestESIMSwitchProfilePCSCRejectsFailedHostReset(t *testing.T) {
	const originalICCID = "894921007608519524"
	const targetICCID = "894921007608519523"
	backend := &profileSwitchPCSCBackend{
		iccid: originalICCID, targetICCID: targetICCID,
		targetAID: "a0000005591010ffffffff8900000101",
		resetErr:  errors.New("simulated PC/SC reset failure"),
	}
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{}, Opener: &staticOpener{},
		CardReaders: pcsc.NewWithBackend(backend),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	devices := manager.List()
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want one reader", len(devices))
	}

	err = manager.ESIMSwitchProfile(context.Background(), devices[0].ID, targetICCID, "")
	if err == nil || !strings.Contains(err.Error(), "did not become active") {
		t.Fatalf("switch after failed host reset = %v, want live ICCID verification failure", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.iccid != originalICCID || backend.pendingICCID != targetICCID {
		t.Fatalf("card state = active %q, pending %q; want original active, target staged",
			backend.iccid, backend.pendingICCID)
	}
	if backend.resets != 1 || len(backend.enableRequests) != 1 {
		t.Fatalf("reset attempts = %d, EnableProfile requests = %d; want one of each",
			backend.resets, len(backend.enableRequests))
	}
}
