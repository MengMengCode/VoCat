package vowifisettings

import (
	"context"
	"strings"
	"testing"

	"vocat/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return database
}

func TestIMSAPNDefaultsToIMS(t *testing.T) {
	database := openTestStore(t)
	if got := IMSAPN(context.Background(), database); got != DefaultIMSAPN {
		t.Fatalf("IMSAPN with no persisted setting = %q, want %q", got, DefaultIMSAPN)
	}
}

// TestIMSAPNIsIndependentOfCellularDataAPN reproduces issue #147: a card that
// carries a custom cellular data APN must not make the VoWiFi ePDG tunnel
// request that APN. Wi-Fi Calling is an IMS service and must use the IMS APN.
func TestIMSAPNIsIndependentOfCellularDataAPN(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	if err := database.UpsertDevice(ctx, store.Device{
		ID:         "ec20",
		Name:       "ec20",
		DeviceType: store.DeviceTypePCIeEC20EC25,
		APN:        "payg.talkmobile.co.uk",
	}); err != nil {
		t.Fatal(err)
	}
	if got := IMSAPN(ctx, database); got != DefaultIMSAPN {
		t.Fatalf("IMSAPN with device data APN %q = %q, want %q",
			"payg.talkmobile.co.uk", got, DefaultIMSAPN)
	}
}

func TestSetIMSAPNRoundTripsAndRejectsInvalidValues(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	if err := SetIMSAPN(ctx, database, "  operator.ims  "); err != nil {
		t.Fatal(err)
	}
	if got := IMSAPN(ctx, database); got != "operator.ims" {
		t.Fatalf("IMSAPN after SetIMSAPN = %q, want %q", got, "operator.ims")
	}
	for _, invalid := range []string{"bad apn", "a/b", "apn:@", strings.Repeat("x", 101)} {
		if err := SetIMSAPN(ctx, database, invalid); err == nil {
			t.Fatalf("SetIMSAPN(%q) accepted an invalid APN", invalid)
		}
	}
	if got := IMSAPN(ctx, database); got != "operator.ims" {
		t.Fatalf("IMSAPN changed after rejected values = %q", got)
	}
}
