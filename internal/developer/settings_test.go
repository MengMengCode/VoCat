package developer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"vocat/internal/exportproxy"
	"vocat/internal/httpsmode"
	"vocat/internal/store"
)

func TestResetExperimentalRestoresDefaults(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "vocat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := SetDeviceLimit(ctx, database, 8); err != nil {
		t.Fatal(err)
	}
	if err := SetSMSHourlyLimit(ctx, database, 18); err != nil {
		t.Fatal(err)
	}
	enabled, _ := json.Marshal(map[string]bool{"enabled": true})
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: httpsmode.SettingKey, Value: enabled}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(ctx, store.Device{ID: "modem-1", Name: "modem-1", NetworkEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "8901000000000000001", NetworkEnabled: true, IPVersion: "IPV4V6"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: exportproxy.SettingKey, Value: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if err := ResetExperimental(ctx, database); err != nil {
		t.Fatal(err)
	}
	if limit := DeviceLimit(ctx, database, true); limit != DefaultDeviceLimit {
		t.Fatalf("device limit = %d, want %d", limit, DefaultDeviceLimit)
	}
	if limit := SMSHourlyLimit(ctx, database); limit != DefaultSMSHourlyLimit {
		t.Fatalf("SMS hourly limit = %d, want %d", limit, DefaultSMSHourlyLimit)
	}
	setting, err := database.AppSetting(ctx, httpsmode.SettingKey)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(setting.Value, &document); err != nil || document.Enabled {
		t.Fatalf("HTTPS setting = %s, error = %v", setting.Value, err)
	}
	device, err := database.Device(ctx, "modem-1")
	if err != nil || device.NetworkEnabled {
		t.Fatalf("device roaming data was not disabled: %+v, %v", device, err)
	}
	policy, err := database.CardPolicy(ctx, "8901000000000000001")
	if err != nil || policy.NetworkEnabled {
		t.Fatalf("card roaming policy was not disabled: %+v, %v", policy, err)
	}
	if _, err := database.AppSetting(ctx, exportproxy.SettingKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("export proxy configurations were not deleted: %v", err)
	}
}

func TestSetDeviceLimitValidatesRange(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "vocat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if SetDeviceLimit(ctx, database, 0) == nil || SetDeviceLimit(ctx, database, MaxDeviceLimit+1) == nil {
		t.Fatal("out-of-range device limit was accepted")
	}
}

func TestSetSMSHourlyLimitValidatesRange(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "vocat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if SetSMSHourlyLimit(ctx, database, 0) == nil || SetSMSHourlyLimit(ctx, database, MaxSMSHourlyLimit+1) == nil {
		t.Fatal("out-of-range SMS hourly limit was accepted")
	}
	if err := SetSMSHourlyLimit(ctx, database, 15); err != nil {
		t.Fatal(err)
	}
	if got := SMSHourlyLimit(ctx, database); got != 15 {
		t.Fatalf("SMS hourly limit = %d, want 15", got)
	}
}

func TestStoredLimitsAboveHardMaximumAreClamped(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "vocat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for key, limit := range map[string]int{
		DeviceLimitSettingKey: 99,
		SMSHourlyLimitKey:     99,
	} {
		value, _ := json.Marshal(map[string]int{"limit": limit})
		if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: key, Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	if got := DeviceLimit(ctx, database, true); got != MaxDeviceLimit {
		t.Fatalf("device limit = %d, want %d", got, MaxDeviceLimit)
	}
	if got := SMSHourlyLimit(ctx, database); got != MaxSMSHourlyLimit {
		t.Fatalf("SMS hourly limit = %d, want %d", got, MaxSMSHourlyLimit)
	}
}
