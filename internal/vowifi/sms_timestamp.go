package vowifi

import (
	"strings"
	"time"
	_ "time/tzdata" // Carrier timestamp policies must also work in minimal static deployments.
)

// SMSTimestampCorrection records the network's original value separately from
// the interpreted time. The raw PDU remains authoritative diagnostic evidence.
type SMSTimestampCorrection struct {
	OriginalTimestamp time.Time `json:"original_timestamp"`
	Timezone          string    `json:"timezone"`
	CarrierProfile    string    `json:"carrier_profile"`
	Reason            string    `json:"reason"`
}

func validSMSTimestampTimezone(value string) bool {
	name := strings.TrimSpace(value)
	if name == "" {
		return true
	}
	// "Local" depends on the deployment rather than the sending SMSC.
	if name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// NormalizeSMSTimestamp reinterprets a zero-offset SCTS only for a carrier
// explicitly known to mislabel its local wall clock as UTC. It does not guess
// from time.Now or time.Local: delayed delivery and rescans must be deterministic.
// Nonzero network offsets and the caller's original value are left intact.
func (profile CarrierProfile) NormalizeSMSTimestamp(timestamp *time.Time) (*time.Time, *SMSTimestampCorrection) {
	name := strings.TrimSpace(profile.SMSZeroSCTSTimezone)
	if timestamp == nil || timestamp.IsZero() || name == "" || name == "Local" {
		return timestamp, nil
	}
	_, offset := timestamp.Zone()
	if offset != 0 {
		return timestamp, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return timestamp, nil // Installed profiles are validated before use.
	}
	corrected := time.Date(timestamp.Year(), timestamp.Month(), timestamp.Day(),
		timestamp.Hour(), timestamp.Minute(), timestamp.Second(), timestamp.Nanosecond(), location)
	if corrected.Equal(*timestamp) {
		return timestamp, nil
	}
	return &corrected, &SMSTimestampCorrection{
		OriginalTimestamp: *timestamp,
		Timezone:          name,
		CarrierProfile:    profile.ID,
		Reason:            "carrier_zero_scts_timezone",
	}
}
