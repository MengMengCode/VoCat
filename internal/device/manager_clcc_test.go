package device

import (
	"context"
	"testing"
	"time"

	"vocat/internal/modem"
)

// newCLCCFamilyTestManager starts a manager over one candidate so the
// capability probe sees the resolved chipset profile.
func newCLCCFamilyTestManager(t *testing.T, vendorID, productID string) (*Manager, string) {
	t.Helper()
	id := "clcc-" + vendorID + "-" + productID
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:        id,
			VendorID:  vendorID,
			ProductID: productID,
			ATPort: modem.Port{
				Path: "/dev/ttyUSB0",
				Name: "ttyUSB0",
				Role: modem.PortRoleAT,
			},
		}}},
		Opener:         &staticOpener{client: &zxicScriptedClient{responses: map[string][]string{}}},
		CommandTimeout: time.Second,
		LongTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager, id
}

// The call-list capability is a property of the firmware dialect, resolved
// from the device's AT profile. Pollers ask the manager instead of pattern
// matching USB identities at each call site.
func TestManagerSupportsCLCCFollowsChipsetProfile(t *testing.T) {
	zxicManager, zxicID := newCLCCFamilyTestManager(t, "19d2", "0536")
	supported, err := zxicManager.SupportsCLCC(context.Background(), zxicID)
	if err != nil {
		t.Fatalf("SupportsCLCC: %v", err)
	}
	if supported {
		t.Fatal("ZXIC SupportsCLCC = true, want false (firmware answers +CME ERROR: 6003)")
	}

	// A product under the same vendor that has not been verified keeps the
	// standard dialect, so it keeps the historical polling behavior.
	unverifiedManager, unverifiedID := newCLCCFamilyTestManager(t, "19d2", "ffff")
	supported, err = unverifiedManager.SupportsCLCC(context.Background(), unverifiedID)
	if err != nil {
		t.Fatalf("SupportsCLCC: %v", err)
	}
	if !supported {
		t.Fatal("unverified ZXIC sibling SupportsCLCC = false, want true")
	}

	quectelManager, quectelID := newCLCCFamilyTestManager(t, "2c7c", "0125")
	supported, err = quectelManager.SupportsCLCC(context.Background(), quectelID)
	if err != nil {
		t.Fatalf("SupportsCLCC: %v", err)
	}
	if !supported {
		t.Fatal("Quectel SupportsCLCC = false, want true")
	}
}

// A device that is not (or no longer) present must not make the caller skip
// polling: the caller's own presence checks decide, and the historical
// behavior for every unadapted modem is to poll.
func TestManagerSupportsCLCCUnknownDeviceDefaultsToTrue(t *testing.T) {
	manager, _ := newCLCCFamilyTestManager(t, "2c7c", "0125")
	supported, err := manager.SupportsCLCC(context.Background(), "no-such-device")
	if err != nil {
		t.Fatalf("SupportsCLCC: %v", err)
	}
	if !supported {
		t.Fatal("SupportsCLCC for unknown device = false, want true")
	}
}

// The capability answer reads the identity profile under the device
// operation lock. A caller that cannot take that lock in time must learn
// about it instead of waiting for a long operator scan to finish.
func TestManagerSupportsCLCCHonorsCallerContext(t *testing.T) {
	manager, id := newCLCCFamilyTestManager(t, "19d2", "0536")
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	state.opMu.Lock()
	t.Cleanup(func() { state.opMu.Unlock() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.SupportsCLCC(ctx, id); err == nil {
		t.Fatal("SupportsCLCC returned no error while the device lock was held")
	}
}
