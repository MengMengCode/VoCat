package vowifi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"howett.net/plist"
)

const (
	maxIPCCBytes             = 32 << 20
	maxIPCCFiles             = 512
	maxIPCCPlistBytes        = 4 << 20
	maxIPCCPlistTotalBytes   = 64 << 20
	installedProfileFileMode = 0o600
)

var supportedSIMPLMN = regexp.MustCompile(`^[0-9]{5,6}$`)

// IPCCImportOptions controls deterministic bundle selection and profile ID
// generation. Bundle may be a full archive directory or the final .bundle
// name. ProfileID overrides the generated, filesystem-safe ID.
type IPCCImportOptions struct {
	Bundle    string
	ProfileID string
}

// IPCCImportWarning describes a value that was ambiguous, unsafe, or outside
// VoCat's portable carrier-profile schema. Such values are reported but never
// copied into the installed profile.
type IPCCImportWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// IPCCImportResult contains a reviewable carrier-profile document. Document
// is complete JSON and can be installed without retaining the Apple archive.
type IPCCImportResult struct {
	SourceFile   string              `json:"source_file"`
	SourceSHA256 string              `json:"source_sha256"`
	Bundle       string              `json:"bundle"`
	CarrierName  string              `json:"carrier_name"`
	ProfileID    string              `json:"profile_id"`
	Document     json.RawMessage     `json:"document"`
	Warnings     []IPCCImportWarning `json:"warnings,omitempty"`
}

type ipccPlist struct {
	name string
	root map[string]any
}

type ipccWarningSet struct {
	items []IPCCImportWarning
	seen  map[string]struct{}
}

func (set *ipccWarningSet) add(code, message, plistPath string) {
	if set.seen == nil {
		set.seen = make(map[string]struct{})
	}
	item := IPCCImportWarning{Code: code, Message: message, Path: plistPath}
	// Device-family override plists often repeat the same setting. Preserve the
	// first concrete path while keeping the review output compact.
	key := code + "\x00" + message
	if _, duplicate := set.seen[key]; duplicate {
		return
	}
	set.seen[key] = struct{}{}
	set.items = append(set.items, item)
}

// ImportCarrierIPCC converts a local Apple .ipcc/.zip archive into one
// reviewable VoCat carrier profile. It never contacts Apple and never installs the
// result. Device-specific and security-weakening values are deliberately
// omitted with structured warnings.
func ImportCarrierIPCC(filePath string, options IPCCImportOptions) (IPCCImportResult, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return IPCCImportResult{}, errors.New("IPCC path is empty")
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return IPCCImportResult{}, fmt.Errorf("stat IPCC %q: %w", filePath, err)
	}
	if !info.Mode().IsRegular() {
		return IPCCImportResult{}, fmt.Errorf("IPCC %q is not a regular file", filePath)
	}
	if info.Size() <= 0 || info.Size() > maxIPCCBytes {
		return IPCCImportResult{}, fmt.Errorf("IPCC %q size %d is outside 1..%d bytes", filePath, info.Size(), maxIPCCBytes)
	}
	encoded, err := os.ReadFile(filePath)
	if err != nil {
		return IPCCImportResult{}, fmt.Errorf("read IPCC %q: %w", filePath, err)
	}
	archive, err := zip.NewReader(bytes.NewReader(encoded), int64(len(encoded)))
	if err != nil {
		return IPCCImportResult{}, fmt.Errorf("open IPCC %q: %w", filePath, err)
	}
	if len(archive.File) > maxIPCCFiles {
		return IPCCImportResult{}, fmt.Errorf("IPCC contains %d files; maximum is %d", len(archive.File), maxIPCCFiles)
	}

	bundleRoots := carrierBundleRoots(archive.File)
	bundleRoot, err := selectCarrierBundle(bundleRoots, options.Bundle)
	if err != nil {
		return IPCCImportResult{}, err
	}
	plists, err := readCarrierBundlePlists(archive.File, bundleRoot)
	if err != nil {
		return IPCCImportResult{}, err
	}
	primary := plists[0]
	warnings := &ipccWarningSet{}
	carrierName := firstNonempty(
		plistString(primary.root["CarrierName"]),
		statusBarCarrierName(primary.root),
		strings.TrimSuffix(path.Base(bundleRoot), path.Ext(bundleRoot)),
	)

	matches, plmns, err := importCarrierSelectors(primary.root, plists, warnings)
	if err != nil {
		return IPCCImportResult{}, fmt.Errorf("import selectors from %s: %w", primary.name, err)
	}
	profileID := strings.TrimSpace(options.ProfileID)
	if profileID == "" {
		profileID = generatedIPCCProfileID(carrierName, plmns)
	}
	if !validInstalledProfileID(profileID) {
		return IPCCImportResult{}, fmt.Errorf("profile ID %q must match [a-z0-9][a-z0-9._-]{0,63}", profileID)
	}

	rule := carrierProfileRule{ID: profileID}
	if len(matches) == 1 {
		rule.Match = matches[0]
	} else {
		rule.MatchAny = matches
	}
	importCarrierEPDG(&rule, plists, warnings)
	importCarrierIKE(&rule, plists, warnings)
	importCarrierIMS(&rule, plists, warnings)
	inspectIgnoredCarrierFields(plists, warnings)
	if !validCarrierProfileRule(rule) {
		return IPCCImportResult{}, errors.New("converted IPCC profile is not valid")
	}

	sum := sha256.Sum256(encoded)
	document := struct {
		Version  int                  `json:"version"`
		Metadata map[string]string    `json:"metadata"`
		Profiles []carrierProfileRule `json:"profiles"`
	}{
		Version: CarrierProfileSchemaVersion,
		Metadata: map[string]string{
			"source":        "user-supplied Apple carrier bundle",
			"source_sha256": hex.EncodeToString(sum[:]),
			"bundle":        bundleRoot,
			"generated_by":  "vocat carrier import-ipcc",
		},
		Profiles: []carrierProfileRule{rule},
	}
	documentJSON, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return IPCCImportResult{}, fmt.Errorf("encode imported carrier profile: %w", err)
	}
	return IPCCImportResult{
		SourceFile:   filepath.Base(filePath),
		SourceSHA256: hex.EncodeToString(sum[:]),
		Bundle:       bundleRoot,
		CarrierName:  carrierName,
		ProfileID:    profileID,
		Document:     append(documentJSON, '\n'),
		Warnings:     warnings.items,
	}, nil
}

// InstallCarrierIPCCResult atomically writes an already-reviewed import result
// to dir. Existing files are never replaced; importing an update therefore
// requires an explicit operator decision outside this function.
func InstallCarrierIPCCResult(result IPCCImportResult, dir string) (string, error) {
	if !validInstalledProfileID(result.ProfileID) {
		return "", fmt.Errorf("invalid profile ID %q", result.ProfileID)
	}
	if len(result.Document) == 0 {
		return "", errors.New("import result has no profile document")
	}
	if _, err := loadCarrierProfiles(result.Document); err != nil {
		return "", fmt.Errorf("validate imported profile: %w", err)
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("carrier profile directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create carrier profile directory %q: %w", dir, err)
	}
	target := filepath.Join(dir, result.ProfileID+".json")
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("carrier profile %q already exists", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat carrier profile %q: %w", target, err)
	}
	temporary, err := os.CreateTemp(dir, "."+result.ProfileID+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary carrier profile: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(installedProfileFileMode); err != nil {
		return "", fmt.Errorf("protect temporary carrier profile: %w", err)
	}
	if _, err := temporary.Write(result.Document); err != nil {
		return "", fmt.Errorf("write temporary carrier profile: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary carrier profile: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close temporary carrier profile: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", fmt.Errorf("install carrier profile %q: %w", target, err)
	}
	removeTemporary = false
	return target, nil
}

// ImportCarrierBundlePlists converts a set of parsed plists for one Apple
// carrier bundle into a validated carrierProfileRule.
func ImportCarrierBundlePlists(bundleName string, plistData map[string][]byte) (*carrierProfileRule, []IPCCImportWarning, error) {
	if len(plistData) == 0 {
		return nil, nil, errors.New("no plist data provided")
	}
	var primaryData []byte
	if data, ok := plistData["carrier.plist"]; ok {
		primaryData = data
	} else {
		for k, v := range plistData {
			if strings.EqualFold(path.Base(k), "carrier.plist") {
				primaryData = v
				break
			}
		}
	}
	if len(primaryData) == 0 {
		return nil, nil, fmt.Errorf("bundle %q has no carrier.plist", bundleName)
	}
	var primaryRoot map[string]any
	decoder := plist.NewDecoder(bytes.NewReader(primaryData))
	if err := decoder.Decode(&primaryRoot); err != nil {
		return nil, nil, fmt.Errorf("decode carrier.plist: %w", err)
	}
	if primaryRoot == nil {
		return nil, nil, errors.New("carrier.plist root is not a dictionary")
	}
	plists := []ipccPlist{{name: "carrier.plist", root: primaryRoot}}

	var overrideNames []string
	for k := range plistData {
		base := path.Base(k)
		if strings.HasPrefix(strings.ToLower(base), "overrides") && strings.EqualFold(path.Ext(base), ".plist") {
			overrideNames = append(overrideNames, k)
		}
	}
	sort.Strings(overrideNames)
	for _, k := range overrideNames {
		var overrideRoot map[string]any
		dec := plist.NewDecoder(bytes.NewReader(plistData[k]))
		if err := dec.Decode(&overrideRoot); err == nil && overrideRoot != nil {
			plists = append(plists, ipccPlist{name: k, root: overrideRoot})
		}
	}

	warnings := &ipccWarningSet{}
	carrierName := firstNonempty(
		plistString(primaryRoot["CarrierName"]),
		statusBarCarrierName(primaryRoot),
		strings.TrimSuffix(bundleName, path.Ext(bundleName)),
	)
	matches, plmns, err := importCarrierSelectors(primaryRoot, plists, warnings)
	if err != nil {
		return nil, warnings.items, fmt.Errorf("import selectors: %w", err)
	}
	profileID := generatedIPCCProfileID(carrierName, plmns)
	if !validInstalledProfileID(profileID) {
		return nil, warnings.items, fmt.Errorf("invalid profile ID %q", profileID)
	}
	rule := carrierProfileRule{ID: profileID}
	if len(matches) == 1 {
		rule.Match = matches[0]
	} else {
		rule.MatchAny = matches
	}
	importCarrierEPDG(&rule, plists, warnings)
	importCarrierIKE(&rule, plists, warnings)
	importCarrierIMS(&rule, plists, warnings)
	inspectIgnoredCarrierFields(plists, warnings)
	if !validCarrierProfileRule(rule) {
		return nil, warnings.items, errors.New("converted profile is not valid")
	}
	return &rule, warnings.items, nil
}

func carrierBundleRoots(files []*zip.File) []string {
	seen := make(map[string]struct{})
	for _, file := range files {
		name := path.Clean(strings.ReplaceAll(file.Name, "\\", "/"))
		if strings.Contains(strings.ToLower(name), "/signatures/") ||
			!strings.EqualFold(path.Base(name), "carrier.plist") {
			continue
		}
		root := path.Dir(name)
		if root == "." || root == "/" {
			continue
		}
		seen[root] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for root := range seen {
		result = append(result, root)
	}
	sort.Strings(result)
	return result
}

func selectCarrierBundle(roots []string, wanted string) (string, error) {
	if len(roots) == 0 {
		return "", errors.New("IPCC contains no carrier.plist bundle")
	}
	wanted = strings.TrimSpace(strings.ReplaceAll(wanted, "\\", "/"))
	if wanted != "" {
		for _, root := range roots {
			base := path.Base(root)
			if strings.EqualFold(root, wanted) || strings.EqualFold(base, wanted) ||
				strings.EqualFold(strings.TrimSuffix(base, path.Ext(base)), strings.TrimSuffix(wanted, path.Ext(wanted))) {
				return root, nil
			}
		}
		return "", fmt.Errorf("carrier bundle %q not found; choices: %s", wanted, strings.Join(roots, ", "))
	}
	if len(roots) != 1 {
		return "", fmt.Errorf("IPCC contains multiple carrier bundles; select one with --bundle: %s", strings.Join(roots, ", "))
	}
	return roots[0], nil
}

func readCarrierBundlePlists(files []*zip.File, root string) ([]ipccPlist, error) {
	var primary *zip.File
	overrides := make([]*zip.File, 0)
	rootPrefix := strings.TrimSuffix(root, "/") + "/"
	for _, file := range files {
		name := path.Clean(strings.ReplaceAll(file.Name, "\\", "/"))
		if !strings.HasPrefix(name, rootPrefix) || strings.Contains(strings.ToLower(name), "/signatures/") {
			continue
		}
		base := path.Base(name)
		switch {
		case strings.EqualFold(name, rootPrefix+"carrier.plist"):
			primary = file
		case strings.HasPrefix(strings.ToLower(base), "overrides") && strings.EqualFold(path.Ext(base), ".plist"):
			overrides = append(overrides, file)
		}
	}
	if primary == nil {
		return nil, fmt.Errorf("bundle %q has no carrier.plist", root)
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Name < overrides[j].Name })
	selected := append([]*zip.File{primary}, overrides...)
	result := make([]ipccPlist, 0, len(selected))
	var total uint64
	for _, file := range selected {
		if file.UncompressedSize64 > maxIPCCPlistBytes {
			return nil, fmt.Errorf("plist %q exceeds %d bytes", file.Name, maxIPCCPlistBytes)
		}
		total += file.UncompressedSize64
		if total > maxIPCCPlistTotalBytes {
			return nil, fmt.Errorf("selected plists exceed %d uncompressed bytes", maxIPCCPlistTotalBytes)
		}
		root, err := decodeIPCCPlist(file)
		if err != nil {
			return nil, fmt.Errorf("decode plist %q: %w", file.Name, err)
		}
		result = append(result, ipccPlist{name: file.Name, root: root})
	}
	return result, nil
}

func decodeIPCCPlist(file *zip.File) (map[string]any, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	encoded, err := io.ReadAll(io.LimitReader(reader, maxIPCCPlistBytes+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxIPCCPlistBytes {
		return nil, fmt.Errorf("plist exceeds %d bytes", maxIPCCPlistBytes)
	}
	decoder := plist.NewDecoder(bytes.NewReader(encoded))
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("plist root is not a dictionary")
	}
	return root, nil
}

func importCarrierSelectors(primary map[string]any, plists []ipccPlist, warnings *ipccWarningSet) ([]carrierProfileMatch, []string, error) {
	supportedSIMs := plistStrings(primary["SupportedSIMs"])
	supportedPLMNs := normalizedPLMNs(plistStrings(primary["SupportedPLMNs"]))
	plainPLMNs := make([]string, 0)
	qualified := make([]carrierProfileMatch, 0)
	for _, raw := range supportedSIMs {
		match, constrained, valid := parseAppleSupportedSIM(raw, warnings)
		if !valid {
			continue
		}
		if constrained {
			qualified = append(qualified, match)
		} else {
			plainPLMNs = append(plainPLMNs, match.HomePLMNs...)
		}
	}

	allPLMNs := normalizeIPCCStringList(append(append([]string(nil), plainPLMNs...), supportedPLMNs...), false)
	matches := qualified
	if len(matches) == 0 {
		if len(allPLMNs) == 0 {
			return nil, nil, errors.New("no supported MCC/MNC selector was found")
		}
		match := carrierProfileMatch{HomePLMNs: allPLMNs}
		iccidPrefixes := collectMatchingICCIDPrefixes(plists)
		if len(iccidPrefixes) > 0 {
			match.ICCIDPrefixes = iccidPrefixes
			warnings.add(
				"remote_provisioning_iccid_selector",
				"MatchingICCIDPrefixes was used only because the bundle has no GID/SPN selector; verify that it identifies subscriptions rather than only eSIM provisioning eligibility",
				"RemoteCardProvisioningSettings.MatchingICCIDPrefixes",
			)
		} else {
			warnings.add(
				"broad_plmn_selector",
				"the generated rule matches a whole home PLMN because the bundle exposes no GID, SPN, or ICCID discriminator",
				"SupportedSIMs",
			)
		}
		matches = []carrierProfileMatch{match}
	}
	matches = deduplicateCarrierMatches(matches)
	if len(matches) == 0 {
		return nil, nil, errors.New("all SupportedSIMs selectors were unsupported")
	}
	if len(allPLMNs) == 0 {
		for _, match := range matches {
			allPLMNs = append(allPLMNs, match.HomePLMNs...)
		}
		allPLMNs = normalizeIPCCStringList(allPLMNs, false)
	}
	return matches, allPLMNs, nil
}

func parseAppleSupportedSIM(raw string, warnings *ipccWarningSet) (carrierProfileMatch, bool, bool) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, "_")
	if len(parts) == 0 || !supportedSIMPLMN.MatchString(parts[0]) || canonicalPLMNValue(parts[0]) == "" {
		warnings.add("unsupported_sim_selector", "unsupported Apple SupportedSIMs value "+strconv.Quote(raw), "SupportedSIMs")
		return carrierProfileMatch{}, false, false
	}
	match := carrierProfileMatch{HomePLMNs: []string{parts[0]}}
	for _, qualifier := range parts[1:] {
		name, value, found := strings.Cut(qualifier, "-")
		value = strings.TrimSpace(value)
		if !found || value == "" {
			warnings.add("unsupported_sim_selector", "unsupported Apple SupportedSIMs qualifier "+strconv.Quote(qualifier), "SupportedSIMs")
			return carrierProfileMatch{}, false, false
		}
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "GID1":
			if trimmed := trimAppleHexMask(value); trimmed != "" {
				match.GID1Prefixes = append(match.GID1Prefixes, trimmed)
			}
		case "GID2":
			if trimmed := trimAppleHexMask(value); trimmed != "" {
				match.GID2Prefixes = append(match.GID2Prefixes, trimmed)
			}
		case "ICCID":
			if trimmed := strings.TrimRight(value, "Ff"); trimmed != "" {
				match.ICCIDPrefixes = append(match.ICCIDPrefixes, trimmed)
			}
		case "SPN":
			match.SPNs = append(match.SPNs, value)
		default:
			warnings.add("unsupported_sim_selector", "unsupported Apple SupportedSIMs qualifier "+strconv.Quote(name), "SupportedSIMs")
			return carrierProfileMatch{}, false, false
		}
	}
	constrained := len(match.GID1Prefixes) > 0 || len(match.GID2Prefixes) > 0 || len(match.ICCIDPrefixes) > 0 || len(match.SPNs) > 0
	return match, constrained, true
}

func trimAppleHexMask(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	return strings.TrimRight(value, "F")
}

func collectMatchingICCIDPrefixes(plists []ipccPlist) []string {
	values := make([]string, 0)
	for _, document := range plists {
		walkPlist(document.root, nil, func(path []string, value any) {
			if len(path) == 0 || !strings.EqualFold(path[len(path)-1], "MatchingICCIDPrefixes") {
				return
			}
			for _, prefix := range plistStrings(value) {
				prefix = strings.TrimRight(strings.TrimSpace(prefix), "Ff")
				if len(prefix) >= 5 && decimalString(prefix) {
					values = append(values, prefix)
				}
			}
		})
	}
	return normalizeIPCCStringList(values, false)
}

func importCarrierEPDG(rule *carrierProfileRule, plists []ipccPlist, warnings *ipccWarningSet) {
	addresses := make(map[string][]string)
	for _, document := range plists {
		for _, ike := range dictionariesForKey(document.root, "IKE") {
			address := strings.ToLower(strings.TrimSuffix(plistString(ike.value["RemoteAddress"]), "."))
			if address == "" {
				continue
			}
			if !validEPDGHostname(address) {
				warnings.add("unsupported_epdg_address", "ignored non-ePDG IKE RemoteAddress "+strconv.Quote(address), document.name+":"+strings.Join(ike.path, "."))
				continue
			}
			addresses[address] = append(addresses[address], document.name)
		}
	}
	keys := sortedMapKeys(addresses)
	switch len(keys) {
	case 0:
		warnings.add("epdg_not_explicit", "no unambiguous ePDG RemoteAddress was found; VoCat will derive the standard 3GPP hostname from the matched PLMN", "TechSettings.IKE.RemoteAddress")
	case 1:
		rule.EPDG.Hostname = keys[0]
	default:
		warnings.add("conflicting_epdg", "device override plists disagree on ePDG RemoteAddress; no address was imported: "+strings.Join(keys, ", "), "TechSettings.IKE.RemoteAddress")
	}
}

func importCarrierIKE(rule *carrierProfileRule, plists []ipccPlist, warnings *ipccWarningSet) {
	groups := make(map[int]struct{})
	eapMethods := make(map[string]struct{})
	for _, document := range plists {
		for _, located := range dictionariesForKey(document.root, "IKE") {
			ike := located.value
			for _, proposal := range plistDictionaries(ike["Proposals"]) {
				if group, ok := plistInt(proposal["DHGroup"]); ok {
					groups[group] = struct{}{}
				}
				if method := strings.ToUpper(plistString(proposal["EAPMethod"])); method != "" {
					eapMethods[method] = struct{}{}
				}
			}
			if validate, ok := plistBool(ike["ValidateRemoteCertificate"]); ok && !validate {
				warnings.add("remote_certificate_bypass_ignored", "ValidateRemoteCertificate=false was not imported", document.name+":"+strings.Join(located.path, ".")+".ValidateRemoteCertificate")
			}
			if enabled, ok := plistBool(ike["DeadPeerDetectionEnabled"]); ok {
				if !enabled {
					warnings.add("disabled_dpd_ignored", "Apple disables DPD for this device family; VoCat keeps its safe liveness defaults", document.name+":"+strings.Join(located.path, ".")+".DeadPeerDetectionEnabled")
				} else if _, hasInterval := ike["DeadPeerDetectionInterval"]; hasInterval {
					warnings.add("dpd_override_ignored", "device-specific DPD timing was not imported; VoCat keeps its runtime defaults", document.name+":"+strings.Join(located.path, "."))
				}
			}
		}
	}
	if len(groups) > 0 {
		unknown := make([]string, 0)
		_, hasModern := groups[14]
		_, hasLegacy := groups[2]
		for group := range groups {
			if group != 2 && group != 14 {
				unknown = append(unknown, strconv.Itoa(group))
			}
		}
		sort.Strings(unknown)
		switch {
		case len(unknown) > 0:
			warnings.add("unsupported_ike_group", "unsupported IKE DH group(s) were not imported: "+strings.Join(unknown, ", "), "TechSettings.IKE.Proposals")
		case hasModern:
			rule.IKE.Proposal = IKEProposalModern
		case hasLegacy:
			rule.IKE.Proposal = IKEProposalLegacy
		}
	}
	for method := range eapMethods {
		if method != "EAP-AKA" && method != "EAP-AKA'" {
			warnings.add("unsupported_eap_method", "VoCat does not import Apple EAP method "+strconv.Quote(method), "TechSettings.IKE.Proposals.EAPMethod")
		}
	}
}

func importCarrierIMS(rule *carrierProfileRule, plists []ipccPlist, warnings *ipccWarningSet) {
	useIPSec := false
	for _, document := range plists {
		for _, signaling := range dictionariesForKey(document.root, "Signaling") {
			if value, ok := plistBool(signaling.value["UseIPSec"]); ok {
				if value {
					useIPSec = true
				} else {
					warnings.add("disabled_ims_ipsec_ignored", "UseIPSec=false was not imported because VoWiFi IMS security cannot be weakened automatically", document.name+":"+strings.Join(signaling.path, ".")+".UseIPSec")
				}
			}
			if strings.EqualFold(plistString(signaling.value["CountryOfOriginationFormat"]), "PANI") {
				enabled := true
				rule.IMS.PANIEnabled = &enabled
				rule.IMS.PANICountry = "AUTO"
			}
		}
	}
	if useIPSec {
		// Apple does not describe the negotiated ESP algorithm in a portable
		// field. Keep VoCat's safe AES-CBC default while recording the intent.
		rule.IMS.IPSecEncryption = "aes-cbc"
	}
}

func inspectIgnoredCarrierFields(plists []ipccPlist, warnings *ipccWarningSet) {
	for _, document := range plists {
		walkPlist(document.root, nil, func(keyPath []string, value any) {
			if len(keyPath) == 0 {
				return
			}
			key := strings.ToLower(keyPath[len(keyPath)-1])
			fullPath := document.name + ":" + strings.Join(keyPath, ".")
			switch {
			case key == "enablewificallingwithoutentitlement":
				if enabled, ok := plistBool(value); ok && enabled {
					warnings.add("entitlement_bypass_ignored", "Wi-Fi Calling entitlement bypass was not imported", fullPath)
				}
			case key == "apns":
				warnings.add("apn_settings_ignored", "APN settings and credentials are outside the VoCat carrier-profile importer", fullPath)
			case key == "media" && strings.Contains(strings.ToLower(strings.Join(keyPath, ".")), "imsconfig"):
				warnings.add("device_media_overrides_ignored", "device-family media and codec overrides require hardware validation and were not imported", fullPath)
			case strings.Contains(key, "emergency") || strings.Contains(key, "e911"):
				warnings.add("emergency_settings_ignored", "emergency-service settings are never imported", fullPath)
			}
		})
	}
}

type locatedDictionary struct {
	path  []string
	value map[string]any
}

func dictionariesForKey(root map[string]any, wanted string) []locatedDictionary {
	result := make([]locatedDictionary, 0)
	walkPlist(root, nil, func(keyPath []string, value any) {
		if len(keyPath) == 0 || !strings.EqualFold(keyPath[len(keyPath)-1], wanted) {
			return
		}
		if dictionary, ok := value.(map[string]any); ok {
			result = append(result, locatedDictionary{path: append([]string(nil), keyPath...), value: dictionary})
		}
	})
	return result
}

func walkPlist(value any, keyPath []string, visit func([]string, any)) {
	visit(keyPath, value)
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkPlist(typed[key], appendPath(keyPath, key), visit)
		}
	case []any:
		for index, item := range typed {
			walkPlist(item, appendPath(keyPath, strconv.Itoa(index)), visit)
		}
	}
}

func appendPath(base []string, item string) []string {
	result := make([]string, len(base), len(base)+1)
	copy(result, base)
	return append(result, item)
}

func plistStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			return []string{strings.TrimSpace(typed)}
		}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if value := plistString(item); value != "" {
				result = append(result, value)
			}
		}
		return result
	case []string:
		return normalizeIPCCStringList(typed, false)
	}
	return nil
}

func normalizeIPCCStringList(values []string, lower bool) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func plistDictionaries(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return []map[string]any{typed}
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if dictionary, ok := item.(map[string]any); ok {
				result = append(result, dictionary)
			}
		}
		return result
	default:
		return nil
	}
}

func plistString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func plistBool(value any) (bool, bool) {
	result, ok := value.(bool)
	return result, ok
}

func plistInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case uint64:
		return int(typed), uint64(int(typed)) == typed
	case float64:
		return int(typed), float64(int(typed)) == typed
	default:
		return 0, false
	}
}

func normalizedPLMNs(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if supportedSIMPLMN.MatchString(value) && canonicalPLMNValue(value) != "" {
			result = append(result, value)
		}
	}
	return normalizeIPCCStringList(result, false)
}

func deduplicateCarrierMatches(matches []carrierProfileMatch) []carrierProfileMatch {
	result := make([]carrierProfileMatch, 0, len(matches))
	seen := make(map[string]struct{})
	for _, match := range matches {
		encoded, _ := json.Marshal(match)
		key := string(encoded)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, match)
	}
	return result
}

func statusBarCarrierName(root map[string]any) string {
	for _, item := range plistDictionaries(root["StatusBarImages"]) {
		if name := firstNonempty(plistString(item["CarrierName"]), plistString(item["StatusBarCarrierName"])); name != "" {
			return name
		}
	}
	return ""
}

func generatedIPCCProfileID(carrierName string, plmns []string) string {
	base := slugCarrierProfileID(carrierName)
	if base == "" {
		base = "carrier"
	}
	if len(plmns) > 0 {
		base += "-" + plmns[0]
	}
	base = "ipcc-" + base
	if len(base) > 64 {
		base = strings.TrimRight(base[:64], "-._")
	}
	return base
}

func slugCarrierProfileID(value string) string {
	var result strings.Builder
	separator := false
	for _, item := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case item >= 'a' && item <= 'z', item >= '0' && item <= '9':
			if separator && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(item)
			separator = false
		case unicode.IsSpace(item), item == '-', item == '_', item == '.':
			separator = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func validInstalledProfileID(value string) bool {
	if len(value) < 1 || len(value) > 64 || !asciiLowerOrDigit(rune(value[0])) {
		return false
	}
	for _, item := range value {
		if asciiLowerOrDigit(item) || item == '-' || item == '_' || item == '.' {
			continue
		}
		return false
	}
	return true
}

func asciiLowerOrDigit(item rune) bool {
	return item >= 'a' && item <= 'z' || item >= '0' && item <= '9'
}

func validEPDGHostname(value string) bool {
	if len(value) < 4 || len(value) > 253 || !strings.Contains(strings.ToLower(value), "epdg") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, item := range label {
			if item >= 'a' && item <= 'z' || item >= '0' && item <= '9' || item == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func consensusPositiveInt(values []int) (int, bool) {
	if len(values) == 0 || values[0] <= 0 {
		return 0, false
	}
	for _, value := range values[1:] {
		if value != values[0] {
			return 0, false
		}
	}
	return values[0], true
}

func sortedMapKeys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
