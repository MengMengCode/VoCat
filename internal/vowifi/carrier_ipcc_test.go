package vowifi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"howett.net/plist"
)

type testIPCCPlist struct {
	value  map[string]any
	format int
}

func TestImportCarrierIPCCConvertsBinaryAndXMLPlistsSafely(t *testing.T) {
	archivePath := writeTestIPCC(t, map[string]testIPCCPlist{
		"Payload/O2_Giffgaff_UK.bundle/carrier.plist": {
			format: plist.XMLFormat,
			value: map[string]any{
				"CarrierName":    "giffgaff",
				"SupportedSIMs":  []any{"23410_GID1-508FFFFF"},
				"SupportedPLMNs": []any{"23410"},
				"apns":           []any{map[string]any{"apn": "giffgaff.com"}},
			},
		},
		"Payload/O2_Giffgaff_UK.bundle/overrides_D1.plist": {
			format: plist.BinaryFormat,
			value: map[string]any{
				"TechSettings": map[string]any{
					"IKE": map[string]any{
						"RemoteAddress":             "epdg.epc.mnc010.mcc234.pub.3gppnetwork.org",
						"ValidateRemoteCertificate": false,
						"DeadPeerDetectionEnabled":  false,
						"Proposals": []any{map[string]any{
							"DHGroup": 14, "EAPMethod": "EAP-AKA",
						}},
					},
				},
				"IMSConfig": map[string]any{
					"EnableWiFiCallingWithoutEntitlement": true,
					"Signaling":                           map[string]any{"UseIPSec": true},
					"Media":                               map[string]any{"SupportPCMA": false},
					"Emergency":                           map[string]any{"E911OverITechSupported": true},
				},
			},
		},
	})
	result, err := ImportCarrierIPCC(archivePath, IPCCImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.CarrierName != "giffgaff" || result.ProfileID != "ipcc-giffgaff-23410" || result.SourceSHA256 == "" {
		t.Fatalf("import metadata = %#v", result)
	}
	var document carrierProfileDocument
	if err := json.Unmarshal(result.Document, &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != CarrierProfileSchemaVersion || len(document.Profiles) != 1 {
		t.Fatalf("document = %#v", document)
	}
	rule := document.Profiles[0]
	if rule.Match.HomePLMNs[0] != "23410" || rule.Match.GID1Prefixes[0] != "508" {
		t.Fatalf("converted selector = %#v", rule.Match)
	}
	if rule.EPDG.Hostname != "epdg.epc.mnc010.mcc234.pub.3gppnetwork.org" || rule.IKE.Proposal != IKEProposalModern {
		t.Fatalf("converted IKE profile = %#v", rule)
	}
	if rule.IMS.IPSecEncryption != "aes-cbc" {
		t.Fatalf("converted IMS profile = %#v", rule.IMS)
	}
	for _, code := range []string{
		"remote_certificate_bypass_ignored",
		"disabled_dpd_ignored",
		"entitlement_bypass_ignored",
		"apn_settings_ignored",
		"device_media_overrides_ignored",
		"emergency_settings_ignored",
	} {
		if !hasIPCCWarning(result.Warnings, code) {
			t.Errorf("missing warning %q: %#v", code, result.Warnings)
		}
	}
}

func TestImportCarrierIPCCRejectsAmbiguousBundleAndConflictingEPDG(t *testing.T) {
	archivePath := writeTestIPCC(t, map[string]testIPCCPlist{
		"Payload/One.bundle/carrier.plist": {
			format: plist.XMLFormat,
			value:  map[string]any{"CarrierName": "One", "SupportedSIMs": []any{"99901"}},
		},
		"Payload/One.bundle/overrides_A.plist": {
			format: plist.XMLFormat,
			value:  map[string]any{"TechSettings": map[string]any{"IKE": map[string]any{"RemoteAddress": "epdg.one.example"}}},
		},
		"Payload/One.bundle/overrides_B.plist": {
			format: plist.BinaryFormat,
			value:  map[string]any{"TechSettings": map[string]any{"IKE": map[string]any{"RemoteAddress": "epdg.two.example"}}},
		},
		"Payload/Two.bundle/carrier.plist": {
			format: plist.BinaryFormat,
			value:  map[string]any{"CarrierName": "Two", "SupportedSIMs": []any{"99902"}},
		},
	})

	if _, err := ImportCarrierIPCC(archivePath, IPCCImportOptions{}); err == nil {
		t.Fatal("multi-bundle IPCC imported without --bundle")
	}
	result, err := ImportCarrierIPCC(archivePath, IPCCImportOptions{Bundle: "One"})
	if err != nil {
		t.Fatal(err)
	}
	var document carrierProfileDocument
	if err := json.Unmarshal(result.Document, &document); err != nil {
		t.Fatal(err)
	}
	if document.Profiles[0].EPDG.Hostname != "" || !hasIPCCWarning(result.Warnings, "conflicting_epdg") {
		t.Fatalf("conflicting ePDG was not quarantined: %#v, %#v", document.Profiles[0], result.Warnings)
	}
}

func TestInstallCarrierIPCCResultLoadsExternalProfileAtEqualSpecificity(t *testing.T) {
	archivePath := writeTestIPCC(t, map[string]testIPCCPlist{
		"Payload/Test.bundle/carrier.plist": {
			format: plist.BinaryFormat,
			value: map[string]any{
				"CarrierName":    "Installed Test",
				"SupportedSIMs":  []any{"23410_GID1-508FFFFF"},
				"SupportedPLMNs": []any{"23410"},
			},
		},
	})
	result, err := ImportCarrierIPCC(archivePath, IPCCImportOptions{ProfileID: "installed-giffgaff-test"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	emptyDir := t.TempDir()
	t.Cleanup(func() {
		if err := LoadCarrierProfileDirectory(emptyDir); err != nil {
			t.Errorf("clear external profiles: %v", err)
		}
	})
	target, err := InstallCarrierIPCCResult(result, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(target) != "installed-giffgaff-test.json" {
		t.Fatalf("installed path = %q", target)
	}
	if _, err := InstallCarrierIPCCResult(result, dir); err == nil {
		t.Fatal("second install overwrote an existing profile")
	}
	if err := LoadCarrierProfileDirectory(dir); err != nil {
		t.Fatal(err)
	}
	profile := ResolveCarrierProfile(SIMIdentity{HomeMCC: "234", HomeMNC: "10", GID1: "508FFFFF"})
	if profile.ID != "installed-giffgaff-test" {
		t.Fatalf("installed equal-specificity profile did not override builtin: %#v", profile)
	}
}

func writeTestIPCC(t *testing.T, files map[string]testIPCCPlist) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "carrier.ipcc")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, item := range files {
		var encoded bytes.Buffer
		if err := plist.NewEncoderForFormat(&encoded, item.format).Encode(item.value); err != nil {
			t.Fatal(err)
		}
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(encoded.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func hasIPCCWarning(warnings []IPCCImportWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
