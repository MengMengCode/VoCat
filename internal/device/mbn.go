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

const rowGeneric3GPPMBN = "ROW_Generic_3GPP"

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
	case strings.Contains(normalized, "OPNMKT_CT") || strings.Contains(normalized, "CHINA_TELECOM"):
		return "China Telecom", "CN", true
	case strings.Contains(normalized, "CMCC") || strings.Contains(normalized, "CHINA_MOBILE"):
		return "China Mobile", "CN", true
	case strings.Contains(normalized, "OPNMKT_CU") || strings.Contains(normalized, "CHINA_UNICOM"):
		return "China Unicom", "CN", true
	default:
		return "", "", false
	}
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

// reconcileMBNSelection changes only recognized operator-specific profiles.
// Unknown vendor profiles are left untouched because their HPLMN coverage
// cannot be inferred safely from the profile name.
func reconcileMBNSelection(ctx context.Context, executor mbnExecutor, hplmn string) (changed bool, current string, err error) {
	response, err := executor.Execute(ctx, `AT+QMBNCFG="List"`)
	if err != nil {
		return false, "", err
	}
	profiles := parseMBNProfiles(response)
	current = currentMBNProfile(profiles)
	if current == "" {
		return false, "", errors.New("modem returned no selected MBN profile")
	}
	if strings.EqualFold(current, rowGeneric3GPPMBN) {
		return false, current, nil
	}
	known, matches := mbnMatchesHPLMN(current, hplmn)
	if !known || matches {
		return false, current, nil
	}
	generic := availableMBNProfile(profiles, rowGeneric3GPPMBN)
	if generic == "" {
		return false, current, fmt.Errorf("operator MBN %q does not match HPLMN %s, but %s is unavailable", current, hplmn, rowGeneric3GPPMBN)
	}
	autoResponse, err := executor.Execute(ctx, `AT+QMBNCFG="AutoSel"`)
	if err != nil {
		return false, current, fmt.Errorf("query automatic MBN selection: %w", err)
	}
	autoSelectionEnabled, ok := parseMBNAutoSelection(autoResponse)
	if !ok {
		return false, current, errors.New("modem returned no automatic MBN selection state")
	}
	if autoSelectionEnabled {
		if _, err := executor.Execute(ctx, `AT+QMBNCFG="AutoSel",0`); err != nil {
			return false, current, fmt.Errorf("disable automatic MBN selection: %w", err)
		}
	}
	if _, err := executor.Execute(ctx, fmt.Sprintf(`AT+QMBNCFG="Select",%q`, generic)); err != nil {
		if autoSelectionEnabled {
			if _, restoreErr := executor.Execute(ctx, `AT+QMBNCFG="AutoSel",1`); restoreErr != nil {
				return false, current, errors.Join(
					fmt.Errorf("select %s MBN: %w", generic, err),
					fmt.Errorf("restore automatic MBN selection: %w", restoreErr),
				)
			}
		}
		return false, current, fmt.Errorf("select %s MBN: %w", generic, err)
	}
	return true, current, nil
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

// reconcileEC20MBNAfterProfileSwitch prevents an EC20 from carrying a known
// operator MBN across an unrelated eSIM profile. A changed MBN requires a full
// module restart; the caller restores the target profile's saved network policy
// after this method returns.
func (manager *Manager) reconcileEC20MBNAfterProfileSwitch(ctx context.Context, id, expectedICCID string) error {
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
	hplmn := profileSwitchHPLMN(snapshot)
	if len(hplmn) < 5 {
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
	changed, previous, err := reconcileMBNSelection(commandContext, client, hplmn)
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
			"EC20 MBN did not match switched eSIM HPLMN; selected generic profile",
			"device_id", id,
			"previous_mbn", previous,
			"selected_mbn", rowGeneric3GPPMBN,
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
	if current := currentMBNProfile(parseMBNProfiles(response)); !strings.EqualFold(current, rowGeneric3GPPMBN) {
		return fmt.Errorf("EC20 MBN restart retained %q instead of %s", current, rowGeneric3GPPMBN)
	}
	return nil
}

func (manager *Manager) reconcileEC20MBNAfterProfileSwitchBestEffort(ctx context.Context, id, expectedICCID string) {
	if err := manager.reconcileEC20MBNAfterProfileSwitch(ctx, id, expectedICCID); err != nil && manager.logger != nil {
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
