package vowifisettings

import (
	"context"
	"encoding/json"
	"vocat/internal/store"
)

const MTUCompatibilityKey = "vowifi.mtu_compatibility"

// MTUCompatibility is opt-in, including for existing installations.
func MTUCompatibility(ctx context.Context, database *store.Store) bool {
	if database == nil {
		return false
	}
	setting, err := database.AppSetting(ctx, MTUCompatibilityKey)
	if err != nil {
		return false
	}
	var value struct {
		Enabled bool `json:"enabled"`
	}
	return json.Unmarshal(setting.Value, &value) == nil && value.Enabled
}

func SetMTUCompatibility(ctx context.Context, database *store.Store, enabled bool) error {
	value, err := json.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	return database.UpsertAppSetting(ctx, store.AppSetting{Key: MTUCompatibilityKey, Value: value})
}
