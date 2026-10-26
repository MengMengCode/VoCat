package vowifi

import (
	"fmt"
	"testing"
	"time"
)

func TestDITOSMSTimestampPolicyMatchesNativeAndSponsorSIMs(t *testing.T) {
	for _, identity := range []SIMIdentity{
		{HomeMCC: "515", HomeMNC: "66", ICCID: "89636624000000000001", IMSI: "515660000000001"},
		{HomeMCC: "515", HomeMNC: "066", ICCID: "89636624000000000001", IMSI: "515660000000001"},
		{HomeMCC: "204", HomeMNC: "04", ICCID: "89636626000000000001", IMSI: "204047616000001"},
	} {
		profile := ResolveCarrierProfile(identity)
		if profile.ID != "ipcc-dito-51566" || profile.SMSZeroSCTSTimezone != "Asia/Manila" {
			t.Fatalf("DITO timestamp policy for %s/%s = %#v", identity.HomeMCC, identity.HomeMNC, profile)
		}
	}
	for _, identity := range []SIMIdentity{
		{HomeMCC: "204", HomeMNC: "04", ICCID: "89310000000000000001", IMSI: "204047616000001"},
		{HomeMCC: "204", HomeMNC: "04", IMSI: "204047616000001"},
		{HomeMCC: "454", HomeMNC: "00", ICCID: "89852000000000000001", IMSI: "454000000000001"},
		{HomeMCC: "515", HomeMNC: "03", ICCID: "89630300000000000001", IMSI: "515030000000001"},
	} {
		if profile := ResolveCarrierProfile(identity); profile.SMSZeroSCTSTimezone != "" {
			t.Fatalf("unrelated subscription received DITO timestamp policy: %#v", profile)
		}
	}
}

func TestNormalizeSMSTimestampIsIndependentOfHostTimezoneAndMessageAge(t *testing.T) {
	profile := CarrierProfile{ID: "dito", SMSZeroSCTSTimezone: "Asia/Manila"}
	previousLocal := time.Local
	t.Cleanup(func() { time.Local = previousLocal })
	for _, hostOffset := range []int{0, 8, 9, -5} {
		time.Local = time.FixedZone("host", hostOffset*3600)
		// The old case models offline backlog; the future case also verifies
		// that normalization is not gated on the machine's current clock.
		for _, year := range []int{2020, 2026, 2090} {
			raw := time.Date(year, 10, 22, 13, 8, 46, 0, time.FixedZone("SMS", 0))
			original := raw
			corrected, evidence := profile.NormalizeSMSTimestamp(&raw)
			want := time.Date(year, 10, 22, 5, 8, 46, 0, time.UTC)
			if corrected == nil || !corrected.Equal(want) {
				t.Fatalf("host offset %d, year %d: corrected=%v, want %v", hostOffset, year, corrected, want)
			}
			if evidence == nil || !evidence.OriginalTimestamp.Equal(original) ||
				evidence.Timezone != "Asia/Manila" || evidence.CarrierProfile != "dito" ||
				evidence.Reason != "carrier_zero_scts_timezone" || raw != original {
				t.Fatalf("lost original SCTS or correction evidence: raw=%v evidence=%#v", raw, evidence)
			}
			// Rescanning the original and normalizing an already corrected
			// timestamp must both preserve the same instant.
			rescanned, _ := profile.NormalizeSMSTimestamp(&raw)
			if !rescanned.Equal(*corrected) {
				t.Fatalf("rescan changed timestamp: %v != %v", rescanned, corrected)
			}
			again, repeated := profile.NormalizeSMSTimestamp(corrected)
			if !again.Equal(*corrected) || repeated != nil {
				t.Fatalf("timestamp was corrected twice: %v, %#v", again, repeated)
			}
		}
	}
}

func TestNormalizeSMSTimestampPreservesDeclaredOffsetsAndUnknownCarriers(t *testing.T) {
	for _, offsetMinutes := range []int{-300, 330, 345, 480, 840} {
		raw := time.Date(2026, 10, 22, 13, 8, 46, 0, time.FixedZone("SMS", offsetMinutes*60))
		profile := CarrierProfile{SMSZeroSCTSTimezone: "Asia/Manila"}
		got, correction := profile.NormalizeSMSTimestamp(&raw)
		if got != &raw || correction != nil {
			t.Fatalf("valid offset %d was reinterpreted: %v, %#v", offsetMinutes, got, correction)
		}
	}
	raw := time.Date(2026, 10, 22, 13, 8, 46, 0, time.UTC)
	for _, zone := range []string{"", "UTC", "Local", "Invalid/Timezone"} {
		profile := CarrierProfile{SMSZeroSCTSTimezone: zone}
		if got, correction := profile.NormalizeSMSTimestamp(&raw); got != &raw || correction != nil {
			t.Fatalf("policy %q unexpectedly changed timestamp: %v, %#v", zone, got, correction)
		}
	}
	profile := CarrierProfile{SMSZeroSCTSTimezone: "Asia/Manila"}
	if got, correction := profile.NormalizeSMSTimestamp(nil); got != nil || correction != nil {
		t.Fatalf("missing SCTS changed: %v, %#v", got, correction)
	}
	zero := time.Time{}
	if got, correction := profile.NormalizeSMSTimestamp(&zero); got != &zero || correction != nil {
		t.Fatalf("zero SCTS changed: %v, %#v", got, correction)
	}
}

func TestCarrierProfileSMSTimestampTimezoneValidation(t *testing.T) {
	for _, zone := range []string{"Asia/Manila", "UTC", "", "Invalid/Timezone", "Local"} {
		encoded := fmt.Sprintf(`{"version":1,"profiles":[{"id":"timestamp","match":{"home_plmns":["51566"]},"sms":{"zero_scts_timezone":%q}}]}`, zone)
		rules, err := loadCarrierProfiles([]byte(encoded))
		valid := zone != "Invalid/Timezone" && zone != "Local"
		if (err == nil) != valid {
			t.Fatalf("timezone %q: load error=%v, want valid=%v", zone, err, valid)
		}
		if valid {
			profile := applyCarrierProfileRule(defaultCarrierProfile(), rules[0], "hplmn", SIMIdentity{})
			if profile.SMSZeroSCTSTimezone != zone {
				t.Fatalf("timezone %q lost during profile resolution: %#v", zone, profile)
			}
		}
	}
	if _, err := loadCarrierProfiles([]byte(`{"version":1,"profiles":[{"id":"missing-issuer","match":{"home_plmns":["20404"],"require_iccid":true},"sms":{"zero_scts_timezone":"Asia/Manila"}}]}`)); err == nil {
		t.Fatal("mandatory ICCID selector without a prefix was accepted")
	}
}
