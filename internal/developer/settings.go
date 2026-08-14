package developer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"vocat/internal/exportproxy"
	"vocat/internal/httpsmode"
	"vocat/internal/store"
)

func Enabled(ctx context.Context, database *store.Store) bool {
	setting, err := database.AppSetting(ctx, EnabledSettingKey)
	if err != nil {
		return false
	}
	var document struct {
		Enabled bool `json:"enabled"`
	}
	return json.Unmarshal(setting.Value, &document) == nil && document.Enabled
}

const (
	EnabledSettingKey     = "developer.enabled"
	DeviceLimitSettingKey = "developer.device_limit"
	SMSHourlyLimitKey     = "developer.sms_hourly_limit"
	DefaultDeviceLimit    = 5
	MaxDeviceLimit        = 10
	DefaultSMSHourlyLimit = 10
	MaxSMSHourlyLimit     = 20
)

func DeviceLimit(ctx context.Context, database *store.Store, enabled bool) int {
	if !enabled {
		return DefaultDeviceLimit
	}
	setting, err := database.AppSetting(ctx, DeviceLimitSettingKey)
	if err != nil {
		return DefaultDeviceLimit
	}
	var document struct {
		Limit int `json:"limit"`
	}
	if json.Unmarshal(setting.Value, &document) != nil || document.Limit < 1 {
		return DefaultDeviceLimit
	}
	if document.Limit > MaxDeviceLimit {
		return MaxDeviceLimit
	}
	return document.Limit
}

func SetDeviceLimit(ctx context.Context, database *store.Store, limit int) error {
	if limit < 1 || limit > MaxDeviceLimit {
		return fmt.Errorf("device limit must be between 1 and %d", MaxDeviceLimit)
	}
	value, err := json.Marshal(map[string]int{"limit": limit})
	if err != nil {
		return err
	}
	return database.UpsertAppSetting(ctx, store.AppSetting{Key: DeviceLimitSettingKey, Value: value})
}

// SMSHourlyLimit is enforced regardless of developer mode. Developer mode
// only controls whether administrators can see and modify this value.
func SMSHourlyLimit(ctx context.Context, database *store.Store) int {
	setting, err := database.AppSetting(ctx, SMSHourlyLimitKey)
	if err != nil {
		return DefaultSMSHourlyLimit
	}
	var document struct {
		Limit int `json:"limit"`
	}
	if json.Unmarshal(setting.Value, &document) != nil || document.Limit < 1 {
		return DefaultSMSHourlyLimit
	}
	if document.Limit > MaxSMSHourlyLimit {
		return MaxSMSHourlyLimit
	}
	return document.Limit
}

func SetSMSHourlyLimit(ctx context.Context, database *store.Store, limit int) error {
	if limit < 1 || limit > MaxSMSHourlyLimit {
		return fmt.Errorf("SMS hourly limit must be between 1 and %d", MaxSMSHourlyLimit)
	}
	value, err := json.Marshal(map[string]int{"limit": limit})
	if err != nil {
		return err
	}
	return database.UpsertAppSetting(ctx, store.AppSetting{Key: SMSHourlyLimitKey, Value: value})
}

// ResetExperimental restores every mutable developer-only setting. It is
// called both by `vocat develop off` and at startup whenever developer mode is
// disabled, so stale database values cannot silently remain active.
func ResetExperimental(ctx context.Context, database *store.Store) error {
	httpsValue, err := json.Marshal(map[string]bool{"enabled": false})
	if err != nil {
		return err
	}
	var resetErrors []error
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: httpsmode.SettingKey, Value: httpsValue}); err != nil {
		resetErrors = append(resetErrors, fmt.Errorf("reset self-signed HTTPS: %w", err))
	}
	if err := SetDeviceLimit(ctx, database, DefaultDeviceLimit); err != nil {
		resetErrors = append(resetErrors, fmt.Errorf("reset device limit: %w", err))
	}
	if err := SetSMSHourlyLimit(ctx, database, DefaultSMSHourlyLimit); err != nil {
		resetErrors = append(resetErrors, fmt.Errorf("reset SMS hourly limit: %w", err))
	}
	if err := database.DeleteAppSetting(ctx, exportproxy.SettingKey); err != nil && !errors.Is(err, store.ErrNotFound) {
		resetErrors = append(resetErrors, fmt.Errorf("delete export proxy configurations: %w", err))
	}
	devices, err := database.ListDevices(ctx)
	if err != nil {
		resetErrors = append(resetErrors, fmt.Errorf("list devices while disabling roaming data: %w", err))
	} else {
		for _, device := range devices {
			if !device.NetworkEnabled {
				continue
			}
			device.NetworkEnabled = false
			if err := database.UpsertDevice(ctx, device); err != nil {
				resetErrors = append(resetErrors, fmt.Errorf("disable roaming data for device %s: %w", device.ID, err))
			}
		}
	}
	policies, err := database.ListCardPolicies(ctx)
	if err != nil {
		resetErrors = append(resetErrors, fmt.Errorf("list card policies while disabling roaming data: %w", err))
	} else {
		for _, policy := range policies {
			if !policy.NetworkEnabled {
				continue
			}
			policy.NetworkEnabled = false
			if err := database.UpsertCardPolicy(ctx, policy); err != nil {
				resetErrors = append(resetErrors, fmt.Errorf("disable roaming policy for card %s: %w", policy.ICCID, err))
			}
		}
	}
	return errors.Join(resetErrors...)
}
