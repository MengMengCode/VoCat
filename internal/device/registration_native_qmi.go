package device

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

// nativeQMIRegistrationSession is the QMI NAS control surface used by
// OpenStick WWAN devices.  It deliberately stays separate from
// qmiRadioSession so AT-only devices and existing radio-control fakes do not
// acquire a mandatory NAS implementation.
type nativeQMIRegistrationSession interface {
	qmiRadioSession
	GetServingSystem(context.Context) (*qmi.ServingSystem, error)
	GetSystemSelectionPreference(context.Context) (*qmi.SystemSelectionPreference, error)
	SetSystemSelectionPreference(context.Context, qmi.SystemSelectionPreference) error
	InitiateNetworkRegister(context.Context, qmi.NASInitiateNetworkRegisterRequest) error
	ForceNetworkSearch(context.Context) error
	AttachDetach(context.Context, bool) error
}

const (
	nativeQMIRegistrationPollInterval               = 2 * time.Second
	nativeQMIRegistrationMaxAttempts                = 45
	nativeQMIRegistrationRadioCycleAfterAttempts    = 30
	nativeQMIRegistrationUnsupportedCycleAfterTries = 3
	nativeQMIRegistrationBackgroundTimeout          = 45 * time.Second
)

func isNativeQMICandidate(candidate modem.Candidate) bool {
	deviceID := strings.TrimSpace(candidate.ID)
	control := strings.TrimSpace(candidate.QMIControl)
	return nativeQMIControlMatches(deviceID, control)
}

func nativeQMIControlMatches(deviceID, control string) bool {
	deviceID = strings.TrimSpace(deviceID)
	control = strings.TrimSpace(control)
	if deviceID == "" || control == "" {
		return false
	}
	prefix := ""
	switch {
	case strings.HasPrefix(deviceID, "wwan"):
		prefix = deviceID + "qmi"
	case strings.HasPrefix(deviceID, "mhi-wwan"):
		prefix = "wwan" + strings.TrimPrefix(deviceID, "mhi-wwan") + "qmi"
	default:
		return false
	}
	return strings.HasPrefix(filepath.Base(control), prefix)
}

func (manager *Manager) openNativeQMIRegistration(
	ctx context.Context,
	candidate modem.Candidate,
) (nativeQMIRegistrationSession, error) {
	if manager == nil || manager.qmiRadioOpener == nil {
		return nil, errors.New("QMI NAS registration is unavailable")
	}
	control := strings.TrimSpace(candidate.QMIControl)
	if control == "" {
		return nil, errors.New("QMI NAS registration control device is unavailable")
	}
	session, err := manager.qmiRadioOpener(ctx, control)
	if err != nil {
		return nil, err
	}
	nas, ok := session.(nativeQMIRegistrationSession)
	if !ok {
		_ = session.Close()
		return nil, errors.New("QMI radio session does not expose NAS registration control")
	}
	return nas, nil
}

// startNativeQMIRegistrationReconcile continues registration after a radio
// transition. Bringing DMS online only proves that the RF switch completed;
// NAS may still report searching or PS detached seconds later, so the
// registration sequence continues after SetFlight returns. The per-device
// guard prevents repeated UI/poll callbacks from opening competing sessions.
func (manager *Manager) startNativeQMIRegistrationReconcile(id string) bool {
	if manager == nil {
		return false
	}
	state, err := manager.lookup(id)
	if err != nil {
		return false
	}
	candidate := manager.candidateFor(state)
	if !isNativeQMICandidate(candidate) {
		return false
	}
	manager.nativeQMIRegistrationMu.Lock()
	if _, running := manager.nativeQMIRegistrationInFlight[id]; running {
		manager.nativeQMIRegistrationMu.Unlock()
		return false
	}
	manager.nativeQMIRegistrationInFlight[id] = struct{}{}
	manager.nativeQMIRegistrationMu.Unlock()
	go func() {
		defer func() {
			manager.nativeQMIRegistrationMu.Lock()
			delete(manager.nativeQMIRegistrationInFlight, id)
			manager.nativeQMIRegistrationMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), nativeQMIRegistrationBackgroundTimeout)
		defer cancel()
		_, _ = manager.ReRegisterOperator(ctx, id)
	}()
	return true
}

func qmiOperatorSelectionFromPreference(pref *qmi.SystemSelectionPreference) (OperatorSelection, error) {
	if pref == nil {
		return OperatorSelection{}, errors.New("QMI returned an empty system-selection preference")
	}
	accessTechnology := qmiAccessTechnologyFromModePreference(pref.ModePreference)
	if pref.HasManualNetworkSelection {
		mcc := fmt.Sprintf("%03d", pref.ManualNetworkSelection.MCC)
		mncWidth := 2
		if pref.ManualNetworkSelection.IncludesPCSDigit {
			mncWidth = 3
		}
		mnc := fmt.Sprintf("%0*d", mncWidth, pref.ManualNetworkSelection.MNC)
		return OperatorSelection{
			Mode:             1,
			Format:           2,
			Operator:         mcc + mnc,
			AccessTechnology: accessTechnology,
		}, nil
	}
	return OperatorSelection{Mode: 0, AccessTechnology: accessTechnology}, nil
}

func qmiManualRegisterRequest(
	plmn string,
	accessTechnologyValue *int,
) (qmi.NASInitiateNetworkRegisterRequest, error) {
	mcc, mnc, includesPCSDigit, err := qmiPLMNParts(plmn)
	if err != nil {
		return qmi.NASInitiateNetworkRegisterRequest{}, err
	}
	rat := uint8(0)
	if accessTechnologyValue != nil {
		if *accessTechnologyValue < 0 || *accessTechnologyValue > 9 {
			return qmi.NASInitiateNetworkRegisterRequest{}, errors.New("invalid operator access technology")
		}
		rat = qmiRATFromATCode(*accessTechnologyValue)
		if rat == 0 {
			return qmi.NASInitiateNetworkRegisterRequest{}, errors.New("unsupported operator access technology")
		}
	}
	return qmi.NASInitiateNetworkRegisterRequest{
		Mode:              qmi.NASNetworkRegisterManual,
		MCC:               mcc,
		MNC:               mnc,
		IncludesPCSDigit:  includesPCSDigit,
		RadioAccessTech:   rat,
		ChangeDuration:    qmi.NASChangeDurationPermanent,
		HasChangeDuration: true,
	}, nil
}

func qmiPLMNParts(plmn string) (mcc, mnc uint16, includesPCSDigit bool, err error) {
	plmn = strings.TrimSpace(plmn)
	if !decimalPLMN(plmn) {
		return 0, 0, false, errors.New("operator PLMN must contain 5 or 6 digits")
	}
	mccValue, parseErr := strconv.ParseUint(plmn[:3], 10, 16)
	if parseErr != nil {
		return 0, 0, false, fmt.Errorf("parse operator MCC: %w", parseErr)
	}
	mncValue, parseErr := strconv.ParseUint(plmn[3:], 10, 16)
	if parseErr != nil {
		return 0, 0, false, fmt.Errorf("parse operator MNC: %w", parseErr)
	}
	return uint16(mccValue), uint16(mncValue), len(plmn) == 6, nil
}

func qmiManualSelectionPreference(plmn string) (qmi.SystemSelectionPreference, qmi.ManualNetworkSelection, error) {
	return qmiManualSelectionPreferenceWithRAT(plmn, nil)
}

func qmiManualSelectionPreferenceWithRAT(
	plmn string,
	accessTechnologyValue *int,
) (qmi.SystemSelectionPreference, qmi.ManualNetworkSelection, error) {
	mcc, mnc, includesPCSDigit, err := qmiPLMNParts(plmn)
	if err != nil {
		return qmi.SystemSelectionPreference{}, qmi.ManualNetworkSelection{}, err
	}
	selection := qmi.ManualNetworkSelection{
		MCC:              mcc,
		MNC:              mnc,
		IncludesPCSDigit: includesPCSDigit,
	}
	pref := qmi.SystemSelectionPreference{
		NetworkSelectionPreference:    qmi.NASNetworkSelectionManual,
		HasNetworkSelectionPreference: true,
		ManualNetworkSelection:        selection,
		HasManualNetworkSelection:     true,
		ChangeDuration:                qmi.NASChangeDurationPermanent,
		HasChangeDuration:             true,
	}
	if accessTechnologyValue != nil {
		modePreference, ok := qmiModePreferenceFromATCode(*accessTechnologyValue)
		if !ok {
			return qmi.SystemSelectionPreference{}, qmi.ManualNetworkSelection{}, errors.New("unsupported operator access technology")
		}
		pref.ModePreference = modePreference
		pref.HasModePreference = true
	}
	return pref, selection, nil
}

func qmiRATFromATCode(value int) uint8 {
	switch value {
	case 0, 3: // GSM / EDGE
		return 0x04
	case 2, 4, 5, 6: // UTRAN / HSDPA / HSUPA / HSPA
		return 0x05
	case 7: // LTE
		return 0x08
	case 9: // NR5G
		return 0x0C
	default:
		return 0
	}
}

func qmiModePreferenceFromATCode(value int) (uint16, bool) {
	switch value {
	case 0, 3: // GSM / EDGE
		return qmi.NASRatModePreferenceGSM, true
	case 2, 4, 5, 6: // UTRAN / HSDPA / HSUPA / HSPA
		return qmi.NASRatModePreferenceUMTS, true
	case 7: // LTE
		return qmi.NASRatModePreferenceLTE, true
	case 9: // NR5G
		return qmi.NASRatModePreferenceNR5G, true
	default:
		return 0, false
	}
}

func qmiRATFromServingRadioInterface(value uint8) uint8 {
	switch value {
	case 4, 5, 8:
		return value
	case 10: // NAS serving-system NR5G value
		return 0x0C
	default:
		return 0
	}
}

func qmiRATFromModePreference(value uint16) uint8 {
	switch {
	case value&qmi.NASRatModePreferenceNR5G != 0:
		return 0x0C
	case value&qmi.NASRatModePreferenceLTE != 0:
		return 0x08
	case value&qmi.NASRatModePreferenceUMTS != 0:
		return 0x05
	case value&qmi.NASRatModePreferenceGSM != 0:
		return 0x04
	default:
		return 0
	}
}

func qmiAccessTechnologyFromModePreference(value uint16) string {
	switch {
	case value&qmi.NASRatModePreferenceNR5G != 0:
		return "NR5G"
	case value&qmi.NASRatModePreferenceLTE != 0:
		return "LTE"
	case value&qmi.NASRatModePreferenceUMTS != 0:
		return "UTRAN"
	case value&qmi.NASRatModePreferenceGSM != 0:
		return "GSM"
	default:
		return ""
	}
}

func qmiRegistrationRequestAutomatic() qmi.NASInitiateNetworkRegisterRequest {
	return qmi.NASInitiateNetworkRegisterRequest{
		Mode:              qmi.NASNetworkRegisterAutomatic,
		ChangeDuration:    qmi.NASChangeDurationPermanent,
		HasChangeDuration: true,
	}
}

func qmiSelectionAutomaticPreference() qmi.SystemSelectionPreference {
	return qmi.SystemSelectionPreference{
		NetworkSelectionPreference:    qmi.NASNetworkSelectionAutomatic,
		HasNetworkSelectionPreference: true,
		ChangeDuration:                qmi.NASChangeDurationPermanent,
		HasChangeDuration:             true,
	}
}

func isUnsupportedQMIRegistrationCommand(err error, messageID uint16) bool {
	qmiErr := qmi.GetQMIError(err)
	if qmiErr == nil || qmiErr.Service != qmi.ServiceNAS || qmiErr.MessageID != messageID {
		return false
	}
	switch qmiErr.ErrorCode {
	case qmi.QMIErrMalformedMsg,
		qmi.QMIErrInvalidRegisterAction,
		qmi.QMIErrNoEffect,
		qmi.QMIErrNotSupported,
		qmi.QMIErrInvalidQmiCmd,
		qmi.QMIErrOpDeviceUnsupported:
		return true
	default:
		return false
	}
}

func isUnsupportedQMIForceSearch(err error) bool {
	qmiErr := qmi.GetQMIError(err)
	if qmiErr == nil || qmiErr.Service != qmi.ServiceNAS || qmiErr.MessageID != qmi.NASForceNetworkSearch {
		return false
	}
	return qmiErr.ErrorCode == qmi.QMIErrNotSupported ||
		qmiErr.ErrorCode == qmi.QMIErrInvalidQmiCmd ||
		qmiErr.ErrorCode == qmi.QMIErrOpDeviceUnsupported
}

func isUnsupportedQMISelectionCommand(err error) bool {
	qmiErr := qmi.GetQMIError(err)
	if qmiErr == nil || qmiErr.Service != qmi.ServiceNAS || qmiErr.MessageID != qmi.NASSetSystemSelectionPreference {
		return false
	}
	switch qmiErr.ErrorCode {
	case qmi.QMIErrMalformedMsg,
		qmi.QMIErrInvalidRegisterAction,
		qmi.QMIErrNoEffect,
		qmi.QMIErrNotSupported,
		qmi.QMIErrInvalidQmiCmd,
		qmi.QMIErrOpDeviceUnsupported:
		return true
	default:
		return false
	}
}

func qmiRegistrationStateRegistered(state qmi.RegistrationState) bool {
	return state == qmi.RegStateRegistered || state == qmi.RegStateRoaming
}

func nativeQMIRegistrationRadioCycleThreshold(forceSearchUnsupported bool) int {
	if forceSearchUnsupported {
		return nativeQMIRegistrationUnsupportedCycleAfterTries
	}
	return nativeQMIRegistrationRadioCycleAfterAttempts
}

// triggerNativeQMIManualRegistration applies the manual preference that was
// written by the caller and starts a fresh NAS search.  On the OpenStick 410
// firmware, NAS_FORCE_NETWORK_SEARCH is the reliable trigger; sending
// NAS_INITIATE_NETWORK_REGISTER with RadioAccessTech=0 is rejected as an
// invalid profile.  Older firmware may not expose force-search, so fall back
// to an explicit RAT (or the current serving RAT) when that command is not
// supported.
func triggerNativeQMIManualRegistration(
	ctx context.Context,
	session nativeQMIRegistrationSession,
	request *qmi.NASInitiateNetworkRegisterRequest,
	serving *qmi.ServingSystem,
) (forceSearchIssued bool, forceSearchUnsupported bool, err error) {
	if request == nil {
		return false, false, errors.New("QMI manual registration request is unavailable")
	}
	if err := session.ForceNetworkSearch(ctx); err == nil {
		return true, false, nil
	} else if !isUnsupportedQMIForceSearch(err) {
		return false, false, fmt.Errorf("force QMI network search: %w", err)
	}

	forceSearchUnsupported = true
	if request.RadioAccessTech == 0 && serving != nil {
		request.RadioAccessTech = qmiRATFromServingRadioInterface(serving.RadioInterface)
	}
	if request.RadioAccessTech == 0 {
		return false, true, errors.New("QMI manual registration requires a supported radio access technology")
	}
	if err := session.InitiateNetworkRegister(ctx, *request); err != nil {
		return false, true, fmt.Errorf("initiate manual QMI network registration: %w", err)
	}
	return false, true, nil
}

// ensureNativeQMIRegistration runs the NAS registration sequence used on
// OpenStick.  The modem's AT+COPS surface on this firmware only changes
// presentation; it does not reliably drive this NAS state machine.
func ensureNativeQMIRegistration(
	ctx context.Context,
	session nativeQMIRegistrationSession,
	request qmi.NASInitiateNetworkRegisterRequest,
	setAutomatic bool,
) error {
	return ensureNativeQMIRegistrationForTarget(ctx, session, request, setAutomatic, nil)
}

// ensureNativeQMIRegistrationForTarget is the manual-lock variant of the
// registration sequence. A modem can remain registered on the old PLMN while
// it processes a new manual request, so a successful registered/PS-attached
// state is only authoritative when it is on the requested PLMN.
func ensureNativeQMIRegistrationForTarget(
	ctx context.Context,
	session nativeQMIRegistrationSession,
	request qmi.NASInitiateNetworkRegisterRequest,
	setAutomatic bool,
	target *qmi.ManualNetworkSelection,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if session == nil {
		return errors.New("QMI NAS registration session is unavailable")
	}
	if request.Mode == 0 {
		request = qmiRegistrationRequestAutomatic()
	}

	mode, err := session.GetOperatingMode(ctx)
	if err != nil {
		return fmt.Errorf("read QMI operating mode: %w", err)
	}
	if mode == qmi.ModeLowPower || mode == qmi.ModeOffline || mode == qmi.ModeShutdown || mode == qmi.ModeReset {
		if err := session.SetOperatingMode(ctx, qmi.ModeOnline); err != nil {
			return fmt.Errorf("restore QMI online mode: %w", err)
		}
		if err := waitNativeQMIRegistration(ctx); err != nil {
			return fmt.Errorf("wait for QMI online mode: %w", err)
		}
		mode, err = session.GetOperatingMode(ctx)
		if err != nil {
			return fmt.Errorf("recheck QMI operating mode: %w", err)
		}
		if mode == qmi.ModeLowPower || mode == qmi.ModeOffline || mode == qmi.ModeShutdown || mode == qmi.ModeReset {
			return fmt.Errorf("QMI operating mode remained non-online after recovery: %d", mode)
		}
	}

	if setAutomatic {
		if err := session.SetSystemSelectionPreference(ctx, qmiSelectionAutomaticPreference()); err != nil {
			// Some OpenStick firmware accepts the preference but reports an
			// unsupported result for an optional NAS TLV. The explicit NAS register
			// below remains the authoritative trigger.
			if !isUnsupportedQMISelectionCommand(err) {
				return fmt.Errorf("restore automatic QMI NAS selection: %w", err)
			}
		}
	}
	registerIssued := false
	forceSearchIssued := false
	radioCycleIssued := false
	forceSearchUnsupported := false
	manualTarget := target != nil && request.Mode == qmi.NASNetworkRegisterManual
	for attempt := 1; attempt <= nativeQMIRegistrationMaxAttempts; attempt++ {
		serving, servingErr := session.GetServingSystem(ctx)
		if servingErr != nil {
			if err := waitNativeQMIRegistration(ctx); err != nil {
				return fmt.Errorf("read QMI serving system: %w", servingErr)
			}
			continue
		}
		if serving == nil {
			return errors.New("QMI serving system returned no data")
		}
		if qmiRegistrationStateRegistered(serving.RegistrationState) {
			if target == nil || qmiServingSystemMatchesTarget(serving, *target) {
				if serving.PSAttached {
					return nil
				}
				if err := session.AttachDetach(ctx, true); err != nil {
					return fmt.Errorf("attach QMI packet service: %w", err)
				}
			} else if !registerIssued {
				if manualTarget {
					var triggerErr error
					forceSearchIssued, forceSearchUnsupported, triggerErr = triggerNativeQMIManualRegistration(
						ctx, session, &request, serving,
					)
					if triggerErr != nil {
						return triggerErr
					}
				} else if err := session.InitiateNetworkRegister(ctx, request); err != nil {
					return fmt.Errorf("initiate QMI network registration: %w", err)
				}
				registerIssued = true
			}
		} else if serving.RegistrationState == qmi.RegStateDenied {
			return errors.New("QMI network registration was denied")
		} else if !registerIssued {
			if manualTarget {
				var triggerErr error
				forceSearchIssued, forceSearchUnsupported, triggerErr = triggerNativeQMIManualRegistration(
					ctx, session, &request, serving,
				)
				if triggerErr != nil {
					return triggerErr
				}
			} else if err := session.InitiateNetworkRegister(ctx, request); err != nil {
				if !(setAutomatic && isUnsupportedQMIRegistrationCommand(err, qmi.NASInitiateNetworkRegister)) {
					return fmt.Errorf("initiate QMI network registration: %w", err)
				}
			}
			registerIssued = true
		}

		searching := serving.RegistrationState == qmi.RegStateSearching
		if target != nil && qmiRegistrationStateRegistered(serving.RegistrationState) && !qmiServingSystemMatchesTarget(serving, *target) {
			searching = true
		}
		if searching && registerIssued && !forceSearchIssued && !forceSearchUnsupported && attempt >= 2 {
			forceSearchIssued = true
			if err := session.ForceNetworkSearch(ctx); err != nil {
				if isUnsupportedQMIForceSearch(err) {
					forceSearchUnsupported = true
				} else {
					return fmt.Errorf("force QMI network search: %w", err)
				}
			}
		}
		radioCycleAfter := nativeQMIRegistrationRadioCycleThreshold(forceSearchUnsupported)
		if searching && registerIssued && !radioCycleIssued && attempt >= radioCycleAfter {
			radioCycleIssued = true
			if err := session.SetOperatingMode(ctx, qmi.ModeLowPower); err == nil {
				_ = waitNativeQMIRegistration(ctx)
				_ = session.SetOperatingMode(ctx, qmi.ModeOnline)
				registerIssued = false
			}
		}
		if err := waitNativeQMIRegistration(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("QMI network registration/PS attach timed out after %d attempts", nativeQMIRegistrationMaxAttempts)
}

func qmiServingSystemMatchesTarget(serving *qmi.ServingSystem, target qmi.ManualNetworkSelection) bool {
	return serving != nil && serving.MCC == target.MCC && serving.MNC == target.MNC
}

func waitNativeQMIRegistration(ctx context.Context) error {
	timer := time.NewTimer(nativeQMIRegistrationPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (manager *Manager) nativeQMIOperatorSelectionLocked(
	ctx context.Context,
	candidate modem.Candidate,
) (OperatorSelection, error) {
	session, err := manager.openNativeQMIRegistration(ctx, candidate)
	if err != nil {
		return OperatorSelection{}, fmt.Errorf("open QMI NAS operator selection: %w", err)
	}
	defer session.Close()
	pref, err := session.GetSystemSelectionPreference(ctx)
	if err != nil {
		return OperatorSelection{}, fmt.Errorf("read QMI system selection preference: %w", err)
	}
	return qmiOperatorSelectionFromPreference(pref)
}

func (manager *Manager) setNativeQMIOperatorSelectionLocked(
	ctx context.Context,
	candidate modem.Candidate,
	automatic bool,
	plmn string,
	accessTechnologyValue *int,
) (OperatorSelection, error) {
	session, err := manager.openNativeQMIRegistration(ctx, candidate)
	if err != nil {
		return OperatorSelection{}, fmt.Errorf("open QMI NAS operator selection: %w", err)
	}
	defer session.Close()

	if automatic {
		request := qmiRegistrationRequestAutomatic()
		if err := ensureNativeQMIRegistration(ctx, session, request, true); err != nil {
			return OperatorSelection{}, err
		}
		pref, err := session.GetSystemSelectionPreference(ctx)
		if err != nil {
			return OperatorSelection{}, fmt.Errorf("read QMI system selection preference: %w", err)
		}
		return qmiOperatorSelectionFromPreference(pref)
	}
	request, err := qmiManualRegisterRequest(plmn, accessTechnologyValue)
	if err != nil {
		return OperatorSelection{}, err
	}
	preference, target, err := qmiManualSelectionPreferenceWithRAT(plmn, accessTechnologyValue)
	if err != nil {
		return OperatorSelection{}, err
	}
	// InitiateNetworkRegister is only a one-shot trigger on this firmware. The
	// manual preference must be written separately or the next reconcile will
	// read automatic selection and undo the requested lock.
	if err := session.SetSystemSelectionPreference(ctx, preference); err != nil {
		return OperatorSelection{}, fmt.Errorf("set manual QMI network selection: %w", err)
	}
	if err := ensureNativeQMIRegistrationForTarget(ctx, session, request, false, &target); err != nil {
		manager.restoreNativeQMISelectionAfterFailure(session, candidate.ID)
		return OperatorSelection{}, err
	}
	actual, err := session.GetSystemSelectionPreference(ctx)
	if err != nil {
		manager.restoreNativeQMISelectionAfterFailure(session, candidate.ID)
		return OperatorSelection{}, fmt.Errorf("verify manual QMI network selection: %w", err)
	}
	if actual == nil || !actual.HasManualNetworkSelection || actual.ManualNetworkSelection != target {
		manager.restoreNativeQMISelectionAfterFailure(session, candidate.ID)
		return OperatorSelection{}, fmt.Errorf("modem did not retain manual PLMN %s", strings.TrimSpace(plmn))
	}
	return qmiOperatorSelectionFromPreference(actual)
}

// restoreNativeQMISelectionAfterFailure prevents a failed manual lock from
// leaving the modem in a searching/manual state.  The caller may already have
// exhausted its request deadline, so rollback uses a fresh bounded context and
// schedules the normal background reconcile as a second line of defence.
func (manager *Manager) restoreNativeQMISelectionAfterFailure(
	session nativeQMIRegistrationSession,
	deviceID string,
) {
	if manager == nil || session == nil {
		return
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), manager.longTimeout)
	defer cancel()
	_ = session.SetSystemSelectionPreference(rollbackCtx, qmiSelectionAutomaticPreference())
	_ = session.InitiateNetworkRegister(rollbackCtx, qmiRegistrationRequestAutomatic())
	_ = session.ForceNetworkSearch(rollbackCtx)
	if strings.TrimSpace(deviceID) != "" {
		manager.startNativeQMIRegistrationReconcile(deviceID)
	}
}

func (manager *Manager) reRegisterNativeQMIOperatorLocked(
	ctx context.Context,
	candidate modem.Candidate,
) (OperatorSelection, error) {
	session, err := manager.openNativeQMIRegistration(ctx, candidate)
	if err != nil {
		return OperatorSelection{}, fmt.Errorf("open QMI NAS re-registration: %w", err)
	}
	defer session.Close()
	pref, err := session.GetSystemSelectionPreference(ctx)
	if err != nil {
		return OperatorSelection{}, fmt.Errorf("read QMI system selection preference: %w", err)
	}
	request := qmiRegistrationRequestAutomatic()
	setAutomatic := true
	selection := OperatorSelection{Mode: 0}
	if pref != nil && pref.HasManualNetworkSelection {
		setAutomatic = false
		request.Mode = qmi.NASNetworkRegisterManual
		request.MCC = pref.ManualNetworkSelection.MCC
		request.MNC = pref.ManualNetworkSelection.MNC
		request.IncludesPCSDigit = pref.ManualNetworkSelection.IncludesPCSDigit
		request.ChangeDuration = qmi.NASChangeDurationPermanent
		request.HasChangeDuration = true
		if pref.HasModePreference {
			request.RadioAccessTech = qmiRATFromModePreference(pref.ModePreference)
		}
		selection, err = qmiOperatorSelectionFromPreference(pref)
		if err != nil {
			return OperatorSelection{}, err
		}
	}
	var target *qmi.ManualNetworkSelection
	if pref != nil && pref.HasManualNetworkSelection {
		target = &pref.ManualNetworkSelection
	}
	if err := ensureNativeQMIRegistrationForTarget(ctx, session, request, setAutomatic, target); err != nil {
		return OperatorSelection{}, err
	}
	return selection, nil
}
