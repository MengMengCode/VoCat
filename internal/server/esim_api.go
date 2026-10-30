package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func esimUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusNotImplemented, "esim_operation_unavailable", "This specific eSIM operation is not implemented.")
}

type esimNotificationController interface {
	ESIMNotifications(context.Context, string) ([]device.EsimNotification, error)
	ESIMRetryNotification(context.Context, string, string, uint64) error
}

// handleESIM routes every /devices/{id}/esim* path.
func (s *Server) handleESIM(w http.ResponseWriter, r *http.Request, rest []string, physicalID string, physicalPresent bool, configuredIDs ...string) bool {
	configuredID := physicalID
	if len(configuredIDs) > 0 && strings.TrimSpace(configuredIDs[0]) != "" {
		configuredID = strings.TrimSpace(configuredIDs[0])
	}
	if len(rest) == 0 || (len(rest) == 1 && strings.TrimSpace(rest[0]) == "") {
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		s.writeEsimOverview(w, r, physicalID, physicalPresent, configuredID)
		return true
	}

	switch rest[0] {
	case "profiles":
		if len(rest) == 1 {
			if !requireMethod(w, r, http.MethodGet) {
				return true
			}
			s.writeEsimGroups(w, r, physicalID, physicalPresent, configuredID)
			return true
		}
		if len(rest) == 2 && r.Method == http.MethodDelete {
			s.handleEsimDelete(w, r, physicalID, physicalPresent, rest[1])
			return true
		}
		if len(rest) == 2 && r.Method == http.MethodPatch {
			s.handleEsimRename(w, r, physicalID, physicalPresent, rest[1])
			return true
		}
		esimUnavailable(w)
		return true
	case "notifications":
		if len(rest) == 1 {
			if !requireMethod(w, r, http.MethodGet) {
				return true
			}
			s.writeEsimNotifications(w, r, physicalID, physicalPresent)
			return true
		}
		if len(rest) == 4 && rest[2] == "actions" && rest[3] == "retry" {
			if !requireMethod(w, r, http.MethodPost) {
				return true
			}
			s.handleEsimNotificationRetry(w, r, physicalID, physicalPresent, rest[1])
			return true
		}
		esimUnavailable(w)
		return true
	case "actions":
		if len(rest) == 2 && rest[1] == "switch" {
			if !requireMethod(w, r, http.MethodPost) {
				return true
			}
			s.handleEsimSwitch(w, r, configuredID, physicalID, physicalPresent)
			return true
		}
		if len(rest) == 2 && rest[1] == "disable" {
			if !requireMethod(w, r, http.MethodPost) {
				return true
			}
			s.handleEsimDisable(w, r, physicalID, physicalPresent)
			return true
		}
		if len(rest) == 2 && rest[1] == "download" {
			if !requireMethod(w, r, http.MethodGet) {
				return true
			}
			s.handleEsimDownload(w, r, physicalID, physicalPresent)
			return true
		}
		// Any other provisioning action is not implemented.
		esimUnavailable(w)
		return true
	default:
		return false
	}
}

func (s *Server) writeEsimNotifications(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool) {
	controller, ok := s.devices.(esimNotificationController)
	if !ok || !physicalPresent {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": []any{}}})
		return
	}
	items, err := controller.ESIMNotifications(r.Context(), physicalID)
	if err != nil {
		s.writeDeviceError(w, err)
		return
	}
	if items == nil {
		items = []device.EsimNotification{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) handleEsimNotificationRetry(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool, rawSequenceNumber string) {
	controller, ok := s.devices.(esimNotificationController)
	if !ok {
		esimUnavailable(w)
		return
	}
	if !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return
	}
	sequenceNumber, err := strconv.ParseUint(strings.TrimSpace(rawSequenceNumber), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "notification sequence number is invalid")
		return
	}
	if err := controller.ESIMRetryNotification(r.Context(), physicalID, r.URL.Query().Get("aid_hex"), sequenceNumber); err != nil {
		s.writeDeviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"status":  "sent",
		"message": "通知已上报运营商并从 eUICC 待处理列表移除",
	}})
}

// esimInfo loads the eUICC profile list. The string result is "ok" (use info),
// "empty" (no usable eUICC — render the empty state), or "error" (an error
// response has already been written).
func (s *Server) esimInfo(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool, configuredID string) (string, []device.EsimInventoryEntry) {
	if s.devices == nil || !physicalPresent {
		return "empty", nil
	}
	info, err := s.readESIMInventory(r.Context(), configuredID, physicalID)
	if err != nil {
		if errors.Is(err, device.ErrNoEUICC) {
			return "empty", nil
		}
		s.writeDeviceError(w, err)
		return "error", nil
	}
	return "ok", info
}

// writeEsimOverview returns { chipInfo, profiles } for the eSIM tab.
func (s *Server) writeEsimOverview(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool, configuredID string) {
	status, info := s.esimInfo(w, r, physicalID, physicalPresent, configuredID)
	switch status {
	case "error":
		return
	case "empty":
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"chipInfo": nil, "profiles": []any{}}})
		return
	}
	chipInfo := esimInventoryChipInfo(info)
	groups := esimInventoryGroups(info)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"chipInfo": chipInfo,
			"profiles": groups,
		},
	})
}

func esimInventoryChipInfo(entries []device.EsimInventoryEntry) map[string]any {
	eids := make([]any, 0, len(entries))
	firmware := ""
	for _, entry := range entries {
		chip := entry.Chip
		eid := map[string]any{"eid": chip.EID, "aid": chip.AID}
		if chip.HasFreeNvram {
			eid["freeNvramBytes"] = chip.FreeNvramBytes
			eid["freeNvram"] = fmt.Sprintf("%.2f KB", float64(chip.FreeNvramBytes)/1024)
		}
		if chip.Manufacturer != "" {
			eid["manufacturer"] = chip.Manufacturer
		}
		if len(chip.Certificates) > 0 {
			eid["certificates"] = chip.Certificates
		}
		if len(chip.TrustedCIs) > 0 {
			eid["trustedCiKeyIds"] = chip.TrustedCIs
		}
		if chip.DefaultSmdpAddress != "" {
			eid["defaultSmdpAddress"] = chip.DefaultSmdpAddress
		}
		if chip.RootDsAddress != "" {
			eid["rootDsAddress"] = chip.RootDsAddress
		}
		if chip.SAS != "" {
			eid["sasAccreditationNumber"] = chip.SAS
		}
		eids = append(eids, eid)
		if firmware == "" {
			firmware = chip.FirmwareVer
		}
	}
	result := map[string]any{"eids": eids}
	if firmware != "" {
		result["firmware"] = firmware
	}
	return result
}

func esimInventoryGroups(entries []device.EsimInventoryEntry) []map[string]any {
	groups := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		groups = append(groups, esimGroups(entry.Info)...)
	}
	return groups
}

// esimChipInfo reads the eUICC chip header (EID, firmware, free NVRAM,
// manufacturer, CI certificates, SM-DP+/Root SM-DS addresses, SAS, info source)
// for the eSIM tab. On any read failure it returns a sparse object so the
// profile list still renders.
func (s *Server) esimChipInfo(r *http.Request, physicalID string) map[string]any {
	chip, err := s.devices.ESIMChipInfo(r.Context(), physicalID)
	if err != nil || chip == nil {
		return map[string]any{}
	}
	eid := map[string]any{
		"eid": chip.EID,
		"aid": chip.AID,
	}
	if chip.HasFreeNvram {
		eid["freeNvramBytes"] = chip.FreeNvramBytes
		eid["freeNvram"] = fmt.Sprintf("%.2f KB", float64(chip.FreeNvramBytes)/1024)
	}
	if chip.Manufacturer != "" {
		eid["manufacturer"] = chip.Manufacturer
	}
	if len(chip.Certificates) > 0 {
		eid["certificates"] = chip.Certificates
	}
	if len(chip.TrustedCIs) > 0 {
		eid["trustedCiKeyIds"] = chip.TrustedCIs
	}
	if chip.DefaultSmdpAddress != "" {
		eid["defaultSmdpAddress"] = chip.DefaultSmdpAddress
	}
	if chip.RootDsAddress != "" {
		eid["rootDsAddress"] = chip.RootDsAddress
	}
	if chip.SAS != "" {
		eid["sasAccreditationNumber"] = chip.SAS
	}
	chipMap := map[string]any{
		"eids": []any{eid},
	}
	if chip.FirmwareVer != "" {
		chipMap["firmware"] = chip.FirmwareVer
	}
	return chipMap
}

// writeEsimGroups returns just the profile groups for the /esim/profiles call.
func (s *Server) writeEsimGroups(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool, configuredID string) {
	status, info := s.esimInfo(w, r, physicalID, physicalPresent, configuredID)
	switch status {
	case "error":
		return
	case "empty":
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}})
		return
	}
	groups := esimInventoryGroups(info)
	writeJSON(w, http.StatusOK, map[string]any{"data": groups})
}

// GetProfilesInfo does not include an EID on every eUICC implementation. The
// EC20 hosts one physical eUICC, so associate the separately-read chip identity
// with that sole profile group. Without this, the SPA cannot match the group to
// its manufacturer/certificate/production metadata even though it was read.
func attachSingleEUICCIdentity(groups []map[string]any, chipInfo map[string]any) {
	if len(groups) != 1 {
		return
	}
	eids, ok := chipInfo["eids"].([]any)
	if !ok || len(eids) != 1 {
		return
	}
	identity, ok := eids[0].(map[string]any)
	if !ok {
		return
	}
	groupEID, _ := groups[0]["eid"].(string)
	chipEID, _ := identity["eid"].(string)
	if strings.TrimSpace(groupEID) == "" && strings.TrimSpace(chipEID) != "" {
		groups[0]["eid"] = strings.TrimSpace(chipEID)
	}
	groupAID, _ := groups[0]["aidHex"].(string)
	chipAID, _ := identity["aid"].(string)
	if strings.TrimSpace(groupAID) == "" && strings.TrimSpace(chipAID) != "" {
		groups[0]["aidHex"] = strings.TrimSpace(chipAID)
	}
}

// esimGroups flattens the eUICC profile list into the SPA's per-eUICC groups
// (the EC20 hosts a single eUICC, so this is normally one group).
func esimGroups(info device.EsimInfo) []map[string]any {
	profiles := make([]map[string]any, 0, len(info.Profiles))
	for _, p := range info.Profiles {
		profiles = append(profiles, map[string]any{
			"iccid":               p.ICCID,
			"name":                firstNonEmpty(p.Nickname, p.Name),
			"serviceProviderName": p.ServiceProvider,
			"state":               p.State,
			"stateText":           p.StateText,
			"classText":           p.Class,
		})
	}
	return []map[string]any{
		{
			"eid":      info.EID,
			"aidHex":   info.AID,
			"profiles": profiles,
		},
	}
}

func (s *Server) handleEsimRename(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool, iccid string) {
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return
	}
	if !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return
	}
	iccid = strings.TrimSpace(iccid)
	if iccid == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "iccid is required")
		return
	}
	var request struct {
		Name        string `json:"name"`
		AIDHex      string `json:"aid_hex"`
		AIDHexCamel string `json:"aidHex"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	nickname := strings.TrimSpace(request.Name)
	if nickname == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "profile nickname is required")
		return
	}
	aidHex := firstNonEmpty(request.AIDHex, request.AIDHexCamel)
	if err := s.devices.ESIMRenameProfile(r.Context(), physicalID, iccid, nickname, aidHex); err != nil {
		s.writeDeviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "renamed", "iccid": iccid, "name": nickname}})
}

// handleEsimSwitch enables one already-installed profile by ICCID (切卡). The
// eUICC EnableProfile command needs no authentication key.
func (s *Server) handleEsimSwitch(w http.ResponseWriter, r *http.Request, configuredID string, physicalID string, physicalPresent bool) {
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return
	}
	if !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return
	}
	var request struct {
		ICCID       string `json:"iccid"`
		AIDHex      string `json:"aid_hex"`
		AIDHexCamel string `json:"aidHex"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	iccid := strings.TrimSpace(request.ICCID)
	if iccid == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "iccid is required")
		return
	}
	// Profile recovery may outlive the shared HTTP write deadline.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	policy, err := s.switchESIMProfile(r.Context(), configuredID, physicalID, iccid,
		firstNonEmpty(request.AIDHex, request.AIDHexCamel))
	if err != nil {
		var storeError esimSwitchStoreError
		if errors.As(err, &storeError) {
			s.writeStoreError(w, storeError.err)
		} else {
			s.writeDeviceError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"status": "switched", "iccid": iccid, "verified": true,
		"card_policy": cardPolicyResponse(policy),
	}})
}

type esimSwitchStoreError struct{ err error }

func (e esimSwitchStoreError) Error() string { return e.err.Error() }
func (e esimSwitchStoreError) Unwrap() error { return e.err }

// SwitchESIMProfile exposes the same coordinated transition used by the web UI
// to automation. It must not bypass VoWiFi leases, RF safety, SMS synchronization
// or the target subscription's saved policy.
func (s *Server) SwitchESIMProfile(ctx context.Context, configuredID, iccid, aidHex string) error {
	if s.devices == nil {
		return errors.New("device manager is unavailable")
	}
	iccid = strings.TrimSpace(iccid)
	if iccid == "" {
		return errors.New("iccid is required")
	}
	config, err := s.store.Device(ctx, configuredID)
	if err != nil {
		return err
	}
	_, physicalID, present := s.physicalForConfig(config)
	if !present {
		return device.ErrNotFound
	}
	_, err = s.switchESIMProfile(ctx, configuredID, physicalID, iccid, strings.TrimSpace(aidHex))
	return err
}

func (s *Server) switchESIMProfile(ctx context.Context, configuredID, physicalID, iccid, aidHex string) (store.CardPolicy, error) {
	// Keep the subscription identity used by the periodic SM/ME scanner stable
	// for the entire profile transition. syncModemSMS takes the same lock and
	// validates its before/after snapshots before persisting any message.
	if err := lockProfileTransition(ctx, &s.smsSyncMu); err != nil {
		return store.CardPolicy{}, err
	}
	endMaintenance := func() {}
	transferred := false
	defer func() {
		if !transferred {
			endMaintenance()
			s.smsSyncMu.Unlock()
		}
	}()
	dataRuntime := s.cellularDataRuntime()
	s.clearPublicIP(configuredID)
	desiredData := true
	if storedConfig, configErr := s.store.Device(ctx, configuredID); configErr == nil {
		desiredData = storedConfig.NetworkEnabled && !storedConfig.VoWiFiEnabled
	}
	dataRuntime.invalidateWithMaintenancePhase(configuredID, desiredData, "recovering", "", "profile_switching")
	switchCompleted := false
	defer func() {
		if !switchCompleted {
			dataRuntime.invalidate(configuredID, desiredData, "failed", "eSIM profile switch did not complete")
		}
	}()
	if maintenance, ok := s.vowifi.(VoWiFiMaintenanceController); ok {
		if err := maintenance.BeginMaintenance(configuredID); err != nil {
			return store.CardPolicy{}, fmt.Errorf("prepare VoWiFi for profile switch: %w", err)
		}
		var released sync.Once
		endMaintenance = func() {
			released.Do(func() { maintenance.EndMaintenance(configuredID) })
		}
	}
	// A live VoWiFi runtime owns the SIM/QMI session while AKA, IMS and SMS are
	// active. Tear it down before touching flight mode or the ISD-R logical
	// channel; otherwise native-WWAN devices wait on the QMI lease until the HTTP
	// request times out. This only changes the runtime desired state. The saved
	// per-ICCID policy is left intact and the target profile's policy is restored
	// after the verified switch below.
	if err := s.quiesceVoWiFiForProfileSwitch(ctx, configuredID); err != nil {
		return store.CardPolicy{}, err
	}
	// Profile operations run with RF disabled. The eUICC remains accessible in
	// CFUN=4. Devices that consume the requested eUICC REFRESH stay online;
	// older AT modems enter the reset recovery path and reapply CFUN=4 when the
	// port returns.
	if _, err := s.devices.SetFlight(ctx, physicalID, true); err != nil {
		return store.CardPolicy{}, err
	}
	switchErr := s.devices.ESIMSwitchProfile(ctx, physicalID, iccid, aidHex)
	// The device manager keeps a committed profile change alive across client
	// disconnects. Apply its verified result to the saved policy and runtime even
	// when the original request was canceled, with a separate time budget.
	finalizeContext, cancelFinalize := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancelFinalize()
	ctx = finalizeContext
	if switchErr != nil {
		if waiter, ok := s.devices.(esimRecoveryWaiter); ok {
			var recoveryErr error
			transferred, recoveryErr = s.waitProfileRecovery(ctx, waiter, configuredID, physicalID, endMaintenance)
			if recoveryErr != nil {
				if !transferred {
					s.stopUnverifiedProfileRuntime(ctx, configuredID)
				}
				return store.CardPolicy{}, errors.Join(switchErr, fmt.Errorf("profile-switch recovery: %w", recoveryErr))
			}
		}
		s.restoreProfileSwitchFailureState(ctx, configuredID, physicalID, endMaintenance)
		return store.CardPolicy{}, switchErr
	}
	if _, err := s.devices.SetFlight(ctx, physicalID, true); err != nil {
		return store.CardPolicy{}, err
	}
	policy, err := s.store.CardPolicy(ctx, iccid)
	if errors.Is(err, store.ErrNotFound) {
		policy = defaultCardPolicy(iccid)
		if err := s.store.UpsertCardPolicy(ctx, policy); err != nil {
			return store.CardPolicy{}, esimSwitchStoreError{err}
		}
	} else if err != nil {
		return store.CardPolicy{}, esimSwitchStoreError{err}
	}
	// Never replace a returning profile's policy with defaults. VoWiFi still
	// implies airplane mode, but every user-selected value and APN belongs to
	// this ICCID and is restored when the profile becomes active again.
	if policy.VoWiFiEnabled && (!policy.AirplaneEnabled || policy.NetworkEnabled) {
		policy.AirplaneEnabled = true
		policy.NetworkEnabled = false
		if err := s.store.UpsertCardPolicy(ctx, policy); err != nil {
			return store.CardPolicy{}, esimSwitchStoreError{err}
		}
	}
	config, err := s.store.Device(ctx, configuredID)
	if err != nil {
		return store.CardPolicy{}, esimSwitchStoreError{err}
	}
	config.VoWiFiEnabled = policy.VoWiFiEnabled
	config.NetworkEnabled = false
	config.APN = policy.APN
	if err := s.store.UpsertDevice(ctx, config); err != nil {
		return store.CardPolicy{}, esimSwitchStoreError{err}
	}
	dataRuntime.invalidate(configuredID, false, "disabled", "")
	switchCompleted = true
	// The target profile is now active and its persisted policy has replaced the
	// old runtime configuration. Allow reconciliation again before requesting
	// the target profile's desired VoWiFi state.
	endMaintenance()
	canRestoreFlightImmediately := s.vowifi == nil
	if s.vowifi != nil {
		state, stateErr := s.vowifi.State(configuredID)
		if policy.VoWiFiEnabled {
			switch {
			case stateErr == nil && state.Enabled:
				_, err = s.vowifi.RequestReconnect(configuredID)
			default:
				_, err = s.vowifi.RequestEnabled(configuredID, true)
			}
		} else if stateErr == nil && state.Enabled {
			_, err = s.vowifi.RequestEnabled(configuredID, false)
		} else {
			canRestoreFlightImmediately = true
		}
		if err != nil {
			s.logger.Warn("profile switched but saved VoWiFi state was not queued", "device_id", configuredID, "iccid", iccid, "enabled", policy.VoWiFiEnabled, "error", err)
		}
	}
	if !policy.VoWiFiEnabled && canRestoreFlightImmediately && !policy.AirplaneEnabled {
		if _, err := s.devices.SetFlight(ctx, physicalID, false); err != nil {
			s.logger.Warn("profile switched but saved airplane state will require reconciliation", "device_id", configuredID, "iccid", iccid, "error", err)
		}
	}
	return policy, nil
}

func (s *Server) quiesceVoWiFiForProfileSwitch(ctx context.Context, configuredID string) error {
	if s.vowifi == nil {
		return nil
	}
	state, err := s.vowifi.State(configuredID)
	if err != nil {
		return fmt.Errorf("stop VoWiFi before switching profile: %w", err)
	}
	if !state.Enabled && !state.Active && state.Phase == vowifi.PhaseIdle {
		return nil
	}
	if _, err := s.vowifi.RequestEnabled(configuredID, false); err != nil {
		return fmt.Errorf("stop VoWiFi before switching profile: %w", err)
	}
	waitContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err = s.vowifi.State(configuredID)
		if err != nil {
			return fmt.Errorf("wait for VoWiFi to stop before switching profile: %w", err)
		}
		if !state.Enabled && !state.Active && state.Phase == vowifi.PhaseIdle {
			return nil
		}
		select {
		case <-waitContext.Done():
			return fmt.Errorf("wait for VoWiFi to stop before switching profile: %w", waitContext.Err())
		case <-ticker.C:
		}
	}
}

// A transport error does not prove EnableProfile was rejected. Restore only
// the live subscription's policy, never the old device's cached Wi-Fi intent.
// An unreadable or unknown card stays RF-off with VoWiFi stopped.
func (s *Server) restoreProfileSwitchFailureState(ctx context.Context, configuredID, physicalID string, beforeResume ...func()) {
	if s.store == nil || s.devices == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	config, err := s.store.Device(ctx, configuredID)
	if err != nil {
		_, _ = s.devices.SetFlight(ctx, physicalID, true)
		return
	}
	policy := store.CardPolicy{AirplaneEnabled: true}
	if snapshot, refreshErr := s.devices.Refresh(ctx, physicalID); refreshErr == nil && strings.TrimSpace(snapshot.ICCID) != "" {
		if livePolicy, policyErr := s.store.CardPolicy(ctx, strings.TrimSpace(snapshot.ICCID)); policyErr == nil {
			policy = livePolicy
		}
	}
	config.VoWiFiEnabled = policy.VoWiFiEnabled
	config.NetworkEnabled = false
	config.APN = policy.APN
	if err := s.store.UpsertDevice(ctx, config); err != nil {
		if s.logger != nil {
			s.logger.Warn("profile switch failed; live card policy could not be saved", "device_id", configuredID, "error", err)
		}
		return
	}
	flightEnabled := policy.AirplaneEnabled || policy.VoWiFiEnabled
	if _, err := s.devices.SetFlight(ctx, physicalID, flightEnabled); err != nil {
		if s.logger != nil {
			s.logger.Warn("profile switch failed; error restoring airplane mode", "device_id", configuredID, "error", err)
		}
		return
	}
	// Keep background reconciliation suppressed until the live identity's
	// persisted policy and RF state are ready. Release only to apply that policy.
	for _, release := range beforeResume {
		release()
	}
	if s.vowifi != nil {
		if _, err := s.vowifi.RequestEnabled(configuredID, policy.VoWiFiEnabled); err != nil && s.logger != nil {
			s.logger.Warn("profile switch failed; error restoring VoWiFi runtime", "device_id", configuredID, "error", err)
		}
	}
}

func (s *Server) handleEsimDisable(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool) {
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return
	}
	if !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return
	}
	var request struct {
		ICCID       string `json:"iccid"`
		AIDHex      string `json:"aid_hex"`
		AIDHexCamel string `json:"aidHex"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	iccid := strings.TrimSpace(request.ICCID)
	if iccid == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "iccid is required")
		return
	}
	aidHex := firstNonEmpty(request.AIDHex, request.AIDHexCamel)
	if err := s.devices.ESIMDisableProfile(r.Context(), physicalID, iccid, aidHex); err != nil {
		s.writeDeviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "disabled", "iccid": iccid, "recovering": true}})
}

func (s *Server) handleEsimDelete(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool, iccid string) {
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return
	}
	if !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return
	}
	iccid = strings.TrimSpace(iccid)
	if iccid == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "iccid is required")
		return
	}
	result, err := s.devices.ESIMDeleteProfile(r.Context(), physicalID, iccid, r.URL.Query().Get("aid_hex"))
	if err != nil {
		s.writeDeviceError(w, err)
		return
	}
	data := map[string]any{
		"status":     "deleted",
		"iccid":      iccid,
		"spaceDelta": map[string]any{"direction": "reclaimed", "bytes": result.SpaceDelta},
	}
	if result.Warning != "" {
		data["warning"] = result.Warning
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// handleEsimDownload streams one eSIM profile download (写卡) as Server-Sent
// Events. The SPA drives it with GET + query params (smdp/matching_id/
// confirmation_code/aid_hex/imei) and reads `data: {step,msg,pct,...}` lines.
// The event field names (step/msg/pct/code/space_delta/warning) match the
// reference contract byte-for-byte, so the frontend needs no changes.
func (s *Server) handleEsimDownload(w http.ResponseWriter, r *http.Request, physicalID string, physicalPresent bool) {
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return
	}
	if !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return
	}
	query := r.URL.Query()
	params := device.EsimDownloadParams{
		SMDP:             query.Get("smdp"),
		MatchingID:       query.Get("matching_id"),
		ConfirmationCode: query.Get("confirmation_code"),
		AIDHex:           query.Get("aid_hex"),
		IMEI:             query.Get("imei"),
	}
	if strings.TrimSpace(params.SMDP) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "smdp 为必填项")
		return
	}

	controller := beginSSE(w)
	emit := func(payload map[string]any) {
		// A failed write means the client went away; r.Context() is then already
		// cancelled, so the device layer stops the download on its own.
		_ = writeSSEEvent(w, controller, "progress", payload)
	}

	result, err := s.devices.ESIMDownloadProfile(r.Context(), physicalID, params, func(p device.EsimProgress) {
		emit(map[string]any{"step": p.Step, "msg": p.Msg, "pct": p.Pct})
	})
	if err != nil {
		emit(map[string]any{
			"step": "error",
			"msg":  "下载失败: " + err.Error(),
			"pct":  -1,
			"code": device.ESIMDownloadErrorCode(err),
		})
		return
	}
	done := map[string]any{
		"step":        "done",
		"msg":         "Profile 下载完成",
		"pct":         100,
		"space_delta": map[string]any{"direction": "consumed", "bytes": result.SpaceDelta},
	}
	if result.Warning != "" {
		done["warning"] = result.Warning
	}
	emit(done)
}
