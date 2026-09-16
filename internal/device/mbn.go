package device

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"vocat/internal/modem"
)

const (
	rowGeneric3GPPMBN = "ROW_Generic_3GPP"
	MBNProfileCU      = "OpenMkt-Commercial-CU"
	MBNProfileCMCC    = "Volte_OpenMkt-Commercial-CMCC"
	MBNProfileCT      = "OpenMkt-Commercial-CT"
)

type mbnProfile struct {
	Name      string
	Selected  bool
	Activated bool
}

type mbnExecutor interface {
	Execute(context.Context, string) (modem.Response, error)
}

func parseMBNProfiles(response modem.Response) []mbnProfile {
	profiles := make([]mbnProfile, 0, len(response.Lines))
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		separator := strings.IndexByte(line, ':')
		if separator < 0 || !strings.EqualFold(strings.TrimSpace(line[:separator]), "+QMBNCFG") {
			continue
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimSpace(line[separator+1:])))
		reader.TrimLeadingSpace = true
		fields, err := reader.Read()
		if err != nil && !errors.Is(err, io.EOF) {
			continue
		}
		if len(fields) < 5 || !strings.EqualFold(strings.TrimSpace(fields[0]), "List") {
			continue
		}
		selected, selectedErr := strconv.Atoi(strings.TrimSpace(fields[2]))
		activated, activatedErr := strconv.Atoi(strings.TrimSpace(fields[3]))
		name := strings.TrimSpace(fields[4])
		if selectedErr != nil || activatedErr != nil || name == "" {
			continue
		}
		profiles = append(profiles, mbnProfile{
			Name: name, Selected: selected != 0, Activated: activated != 0,
		})
	}
	return profiles
}

func currentMBNProfile(profiles []mbnProfile) string {
	for _, profile := range profiles {
		if profile.Activated {
			return profile.Name
		}
	}
	for _, profile := range profiles {
		if profile.Selected {
			return profile.Name
		}
	}
	return ""
}

func availableMBNProfile(profiles []mbnProfile, wanted string) string {
	for _, profile := range profiles {
		if strings.EqualFold(profile.Name, wanted) {
			return profile.Name
		}
	}
	return ""
}

func parseMBNAutoSelection(response modem.Response) (enabled bool, ok bool) {
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		separator := strings.IndexByte(line, ':')
		if separator < 0 || !strings.EqualFold(strings.TrimSpace(line[:separator]), "+QMBNCFG") {
			continue
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimSpace(line[separator+1:])))
		reader.TrimLeadingSpace = true
		fields, err := reader.Read()
		if err != nil && !errors.Is(err, io.EOF) {
			continue
		}
		if len(fields) < 2 || !strings.EqualFold(strings.TrimSpace(fields[0]), "AutoSel") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err == nil && (value == 0 || value == 1) {
			return value == 1, true
		}
	}
	return false, false
}

func knownMBNCarrier(profile string) (carrier string, country string, known bool) {
	normalized := strings.ToUpper(strings.TrimSpace(profile))
	switch {
	case strings.Contains(normalized, "COMMERCIAL-CT") || strings.Contains(normalized, "OPNMKT_CT") ||
		strings.Contains(normalized, "CHINA_TELECOM") || strings.HasSuffix(normalized, "-CT") ||
		strings.HasSuffix(normalized, "_CT"):
		return "China Telecom", "CN", true
	case strings.Contains(normalized, "CMCC") || strings.Contains(normalized, "CHINA_MOBILE"):
		return "China Mobile", "CN", true
	case strings.Contains(normalized, "COMMERCIAL-CU") || strings.Contains(normalized, "OPNMKT_CU") ||
		strings.Contains(normalized, "CHINA_UNICOM") || strings.HasSuffix(normalized, "-CU") ||
		strings.HasSuffix(normalized, "_CU"):
		return "China Unicom", "CN", true
	default:
		return "", "", false
	}
}

func availableMBNContaining(profiles []mbnProfile, needle string) string {
	needle = strings.ToUpper(strings.TrimSpace(needle))
	if needle == "" {
		return ""
	}
	for _, profile := range profiles {
		if strings.Contains(strings.ToUpper(profile.Name), needle) {
			return profile.Name
		}
	}
	return ""
}

func chinaUnicomMBNName(profiles []mbnProfile) string {
	if name := availableMBNProfile(profiles, MBNProfileCU); name != "" {
		return name
	}
	if name := availableMBNContaining(profiles, "COMMERCIAL-CU"); name != "" {
		return name
	}
	for _, profile := range profiles {
		if carrier, _, known := knownMBNCarrier(profile.Name); known && carrier == "China Unicom" {
			return profile.Name
		}
	}
	return ""
}

func validMBNProfileToken(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z', character >= 'a' && character <= 'z',
			character >= '0' && character <= '9', character == '_', character == '-':
		default:
			return false
		}
	}
	return true
}

// NormalizeCardMBNProfile canonicalizes a stored or requested per-card MBN
// override. Empty / "auto" keeps the HPLMN heuristic.
func NormalizeCardMBNProfile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "auto") {
		return "", nil
	}
	switch strings.ToUpper(strings.ReplaceAll(value, " ", "_")) {
	case "CU", "UNICOM", "CHINA_UNICOM", "OPENMKT-COMMERCIAL-CU", "OPENMKT_COMMERCIAL_CU":
		return MBNProfileCU, nil
	case "CMCC", "MOBILE", "CHINA_MOBILE", "VOLTE_OPENMKT-COMMERCIAL-CMCC", "VOLTE_OPENMKT_COMMERCIAL_CMCC":
		return MBNProfileCMCC, nil
	case "CT", "TELECOM", "CHINA_TELECOM", "OPENMKT-COMMERCIAL-CT", "OPENMKT_COMMERCIAL_CT":
		return MBNProfileCT, nil
	case "ROW", "ROW_GENERIC", "ROW_GENERIC_3GPP", "ROW_GENERIC-3GPP":
		return rowGeneric3GPPMBN, nil
	}
	if !validMBNProfileToken(value) {
		return "", fmt.Errorf("unsupported MBN profile %q", value)
	}
	return value, nil
}

func resolveMBNOverride(profiles []mbnProfile, override string) (string, error) {
	normalized, err := NormalizeCardMBNProfile(override)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", nil
	}
	if name := availableMBNProfile(profiles, normalized); name != "" {
		return name, nil
	}
	switch normalized {
	case MBNProfileCU:
		if name := chinaUnicomMBNName(profiles); name != "" {
			return name, nil
		}
	case MBNProfileCMCC:
		if name := availableMBNContaining(profiles, "CMCC"); name != "" {
			return name, nil
		}
	case MBNProfileCT:
		if name := availableMBNProfile(profiles, MBNProfileCT); name != "" {
			return name, nil
		}
		if name := availableMBNContaining(profiles, "COMMERCIAL-CT"); name != "" {
			return name, nil
		}
	case rowGeneric3GPPMBN:
		if name := availableMBNProfile(profiles, rowGeneric3GPPMBN); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("MBN profile %q is not present on this modem", normalized)
}

func (manager *Manager) cardMBNOverride(ctx context.Context, iccid string) (string, error) {
	if manager == nil || manager.mbnProfileForICCID == nil {
		return "", nil
	}
	value, err := manager.mbnProfileForICCID(ctx, strings.TrimSpace(iccid))
	if err != nil {
		return "", err
	}
	normalized, err := NormalizeCardMBNProfile(value)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func mbnMatchesHPLMN(profile, hplmn string) (known bool, matches bool) {
	expectedCarrier, expectedCountry, known := knownMBNCarrier(profile)
	if !known {
		return false, false
	}
	hplmn = strings.TrimSpace(hplmn)
	if len(hplmn) < 3 {
		return false, false
	}
	actualCountry, countryFound := CountryForMCC(hplmn[:3])
	if countryFound && !strings.EqualFold(actualCountry, expectedCountry) {
		return true, false
	}
	actualCarrier, _, found := CarrierForPLMN(hplmn)
	if !found {
		return false, false
	}
	return true, strings.EqualFold(actualCarrier, expectedCarrier)
}

func disableMBNAutoSelection(ctx context.Context, executor mbnExecutor) (wasEnabled bool, err error) {
	autoResponse, err := executor.Execute(ctx, `AT+QMBNCFG="AutoSel"`)
	if err != nil {
		return false, fmt.Errorf("query automatic MBN selection: %w", err)
	}
	autoSelectionEnabled, ok := parseMBNAutoSelection(autoResponse)
	if !ok {
		return false, errors.New("modem returned no automatic MBN selection state")
	}
	if !autoSelectionEnabled {
		return false, nil
	}
	if _, err := executor.Execute(ctx, `AT+QMBNCFG="AutoSel",0`); err != nil {
		return true, fmt.Errorf("disable automatic MBN selection: %w", err)
	}
	return true, nil
}

func restoreMBNAutoSelection(ctx context.Context, executor mbnExecutor) error {
	if _, err := executor.Execute(ctx, `AT+QMBNCFG="AutoSel",1`); err != nil {
		return fmt.Errorf("restore automatic MBN selection: %w", err)
	}
	return nil
}

func selectMBNProfile(ctx context.Context, executor mbnExecutor, previous, wanted string, keepAutoSelOff bool) (changed bool, selected string, err error) {
	if wanted == "" {
		return false, previous, errors.New("MBN profile name is empty")
	}
	if strings.EqualFold(previous, wanted) {
		if keepAutoSelOff {
			if _, err := disableMBNAutoSelection(ctx, executor); err != nil {
				return false, previous, err
			}
		}
		return false, previous, nil
	}
	autoWasEnabled, err := disableMBNAutoSelection(ctx, executor)
	if err != nil {
		return false, previous, err
	}
	if _, err := executor.Execute(ctx, fmt.Sprintf(`AT+QMBNCFG="Select",%q`, wanted)); err != nil {
		if autoWasEnabled {
			if restoreErr := restoreMBNAutoSelection(ctx, executor); restoreErr != nil {
				return false, previous, errors.Join(fmt.Errorf("select %s MBN: %w", wanted, err), restoreErr)
			}
		}
		return false, previous, fmt.Errorf("select %s MBN: %w", wanted, err)
	}
	return true, wanted, nil
}

// reconcileMBNSelection changes recognized operator-specific profiles when they
// do not match the SIM HPLMN. A non-empty override selects that MBN even when
// ROW_Generic_3GPP is missing, and keeps AutoSel off so the modem cannot pull
// another operator profile back in. Empty override keeps the original
// ROW_Generic_3GPP heuristic.
func reconcileMBNSelection(ctx context.Context, executor mbnExecutor, hplmn, override string) (changed bool, previous string, selected string, err error) {
	response, err := executor.Execute(ctx, `AT+QMBNCFG="List"`)
	if err != nil {
		return false, "", "", err
	}
	profiles := parseMBNProfiles(response)
	previous = currentMBNProfile(profiles)
	if previous == "" {
		return false, "", "", errors.New("modem returned no selected MBN profile")
	}
	if strings.TrimSpace(override) != "" {
		wanted, resolveErr := resolveMBNOverride(profiles, override)
		if resolveErr != nil {
			return false, previous, previous, resolveErr
		}
		changed, selected, err = selectMBNProfile(ctx, executor, previous, wanted, true)
		return changed, previous, selected, err
	}
	if strings.EqualFold(previous, rowGeneric3GPPMBN) {
		return false, previous, previous, nil
	}
	known, matches := mbnMatchesHPLMN(previous, hplmn)
	if !known || matches {
		return false, previous, previous, nil
	}
	generic := availableMBNProfile(profiles, rowGeneric3GPPMBN)
	if generic == "" {
		return false, previous, previous, fmt.Errorf("operator MBN %q does not match HPLMN %s, but %s is unavailable", previous, hplmn, rowGeneric3GPPMBN)
	}
	changed, selected, err = selectMBNProfile(ctx, executor, previous, generic, false)
	return changed, previous, selected, err
}

func profileSwitchHPLMN(snapshot Snapshot) string {
	plmn, _, _, ok := CarrierForSIM(CarrierIdentity{
		IMSI: snapshot.IMSI, ICCID: snapshot.ICCID, SPN: snapshot.SPN,
		GID1: snapshot.GID1, GID2: snapshot.GID2, MNCLength: snapshot.MNCLength,
	})
	if ok {
		return plmn
	}
	mcc, mnc := CardMCCMNCWithLength(snapshot.IMSI, snapshot.MNCLength)
	return mcc + mnc
}

func isEC20Candidate(candidate modem.Candidate) bool {
	return strings.EqualFold(strings.TrimSpace(candidate.VendorID), "2c7c") &&
		strings.Contains(strings.ToUpper(candidate.Product), "EC20")
}

// ReconcileEC20MBNAfterProfileSwitch prevents an EC20 from carrying a known
// operator MBN across an unrelated eSIM profile. A changed MBN requires a full
// module restart; the caller restores the target profile's saved network policy
// after this method returns.
func (manager *Manager) ReconcileEC20MBNAfterProfileSwitch(ctx context.Context, id, expectedICCID string) error {
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	if !isEC20Candidate(manager.candidateFor(state)) {
		return nil
	}

	manager.lockESIM()
	defer manager.unlockESIM()
	if err := manager.waitForESIMRecovery(ctx, id); err != nil {
		return err
	}
	snapshot, err := manager.Refresh(ctx, id)
	if err != nil {
		return fmt.Errorf("read new profile identity before MBN validation: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(snapshot.ICCID), strings.TrimSpace(expectedICCID)) {
		return fmt.Errorf("active ICCID %q does not match expected ICCID %q", snapshot.ICCID, expectedICCID)
	}
	override, err := manager.cardMBNOverride(ctx, expectedICCID)
	if err != nil {
		return fmt.Errorf("read card MBN policy: %w", err)
	}
	hplmn := profileSwitchHPLMN(snapshot)
	if override == "" && len(hplmn) < 5 {
		return errors.New("new eSIM profile did not expose a usable HPLMN for MBN validation")
	}
	preserveFlightMode := snapshot.FlightMode

	state.opMu.Lock()
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		state.opMu.Unlock()
		return fmt.Errorf("open EC20 for MBN validation: %w", err)
	}
	commandContext, cancelCommand := context.WithTimeout(ctx, manager.longTimeout)
	changed, previous, selected, err := reconcileMBNSelection(commandContext, client, hplmn, override)
	cancelCommand()
	if err != nil || !changed {
		state.opMu.Unlock()
		if err != nil {
			return fmt.Errorf("validate EC20 MBN after eSIM switch: %w", err)
		}
		return nil
	}

	state.dataMu.Lock()
	invalidateQMINetworkSession(state, manager.candidateFor(state))
	state.dataMu.Unlock()
	rebootContext, cancelReboot := context.WithTimeout(ctx, manager.longTimeout)
	_, rebootErr := client.Execute(rebootContext, "AT+CFUN=1,1")
	cancelReboot()
	_ = client.Close()
	state.client = nil
	state.preFlightMode = nil
	manager.clearSnapshot(id, state)
	state.opMu.Unlock()

	if manager.logger != nil {
		manager.logger.Info(
			"EC20 MBN did not match switched eSIM HPLMN; selected replacement profile",
			"device_id", id,
			"iccid", expectedICCID,
			"previous_mbn", previous,
			"selected_mbn", selected,
			"mbn_override", override,
			"hplmn", hplmn,
		)
		if rebootErr != nil {
			manager.logger.Warn("EC20 restart response lost after MBN selection", "device_id", id, "error", rebootErr)
		}
	}

	manager.refreshAfterProfileSwitch(id)
	if err := manager.verifySwitchedICCID(ctx, id, expectedICCID); err != nil {
		return fmt.Errorf("verify eSIM after EC20 MBN restart: %w", err)
	}
	if preserveFlightMode {
		if _, err := manager.SetFlight(ctx, id, true); err != nil {
			return fmt.Errorf("restore airplane mode after EC20 MBN restart: %w", err)
		}
	}

	verifyContext, cancelVerify := context.WithTimeout(ctx, manager.commandTimeout)
	response, err := manager.ExecuteAT(verifyContext, id, `AT+QMBNCFG="List"`)
	cancelVerify()
	if err != nil {
		return fmt.Errorf("verify EC20 MBN after restart: %w", err)
	}
	if current := currentMBNProfile(parseMBNProfiles(response)); !strings.EqualFold(current, selected) {
		return fmt.Errorf("EC20 MBN restart retained %q instead of %s", current, selected)
	}
	return nil
}

func (manager *Manager) reconcileEC20MBNAfterProfileSwitchBestEffort(ctx context.Context, id, expectedICCID string) {
	if err := manager.ReconcileEC20MBNAfterProfileSwitch(ctx, id, expectedICCID); err != nil && manager.logger != nil {
		// EnableProfile has already been committed and its ICCID verified. Keep
		// this compatibility repair separate from switch success so callers can
		// still restore the new card's saved radio, APN and VoWiFi policy.
		manager.logger.Warn(
			"EC20 MBN recovery after eSIM profile switch did not complete",
			"device_id", id,
			"error", err,
		)
	}
}
