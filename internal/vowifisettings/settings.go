package vowifisettings

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"vocat/internal/store"
)

const (
	MTUCompatibilityKey = "vowifi.mtu_compatibility"
	IMSAPNKey           = "vowifi.ims_apn"

	// DefaultIMSAPN is the APN a 3GPP ePDG expects for IMS when the carrier
	// does not provision a dedicated one.
	DefaultIMSAPN = "ims"
)

// imsAPNPattern matches the modem/card APN charset so the value can be used
// as an IKE IDr FQDN without escaping.
var imsAPNPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9])?$`)

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

// IMSAPN returns the dedicated APN used by the VoWiFi (ePDG/IKE) tunnel.
// VoWiFi is an IMS service, so the tunnel must request the IMS APN and must not
// reuse the cellular data APN persisted on the device or card policy. The
// setting is optional and falls back to DefaultIMSAPN.
func IMSAPN(ctx context.Context, database *store.Store) string {
	if database == nil {
		return DefaultIMSAPN
	}
	setting, err := database.AppSetting(ctx, IMSAPNKey)
	if err != nil {
		return DefaultIMSAPN
	}
	var value struct {
		APN string `json:"apn"`
	}
	if json.Unmarshal(setting.Value, &value) != nil {
		return DefaultIMSAPN
	}
	apn := strings.TrimSpace(value.APN)
	if !imsAPNPattern.MatchString(apn) {
		return DefaultIMSAPN
	}
	return apn
}

// SetIMSAPN persists the dedicated IMS APN used by the VoWiFi tunnel. An empty
// value restores DefaultIMSAPN.
func SetIMSAPN(ctx context.Context, database *store.Store, apn string) error {
	apn = strings.TrimSpace(apn)
	if apn == "" {
		apn = DefaultIMSAPN
	}
	if !imsAPNPattern.MatchString(apn) {
		return errors.New("IMS APN must contain only letters, digits, dots, underscores, or hyphens")
	}
	value, err := json.Marshal(map[string]string{"apn": apn})
	if err != nil {
		return err
	}
	return database.UpsertAppSetting(ctx, store.AppSetting{Key: IMSAPNKey, Value: value})
}
