package device

import (
	"context"
	"errors"
	"fmt"
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
	if deviceID == "" || control == "" || !strings.HasPrefix(deviceID, "wwan") {
		return false
	}
	base := strings.TrimSpace(control)
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	return strings.HasPrefix(base, deviceID+"qmi")
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

// startNativeQMIRegistrationReconcile mirrors VoHive's best-effort background
// reconcile after a radio/VoWiFi teardown.  Bringing DMS online only proves
// that the RF switch completed; NAS may still report searching or PS detached
// seconds later, so the registration sequence must continue after SetFlight
// has returned.  The per-device guard prevents repeated UI/poll callbacks from
// opening competing QMI sessions.
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
	if pref.HasManualNetworkSelection {
		mcc := fmt.Sprintf("%03d", pref.ManualNetworkSelection.MCC)
		mncWidth := 2
		if pref.ManualNetworkSelection.IncludesPCSDigit {
			mncWidth = 3
		}
		mnc := fmt.Sprintf("%0*d", mncWidth, pref.ManualNetworkSelection.MNC)
		return OperatorSelection{
			Mode:     1,
			Format:   2,
			Operator: mcc + mnc,
		}, nil
	}
	return OperatorSelection{Mode: 0}, nil
}

func qmiManualRegisterRequest(
	plmn string,
	accessTechnologyValue *int,
) (qmi.NASInitiateNetworkRegisterRequest, error) {
	plmn = strings.TrimSpace(plmn)
	if !decimalPLMN(plmn) {
		return qmi.NASInitiateNetworkRegisterRequest{}, errors.New("operator PLMN must contain 5 or 6 digits")
	}
	mcc, err := strconv.ParseUint(plmn[:3], 10, 16)
	if err != nil {
		return qmi.NASInitiateNetworkRegisterRequest{}, fmt.Errorf("parse operator MCC: %w", err)
	}
	mnc, err := strconv.ParseUint(plmn[3:], 10, 16)
	if err != nil {
		return qmi.NASInitiateNetworkRegisterRequest{}, fmt.Errorf("parse operator MNC: %w", err)
	}
	rat := uint8(0)
	if accessTechnologyValue != nil {
		if *accessTechnologyValue < 0 || *accessTechnologyValue > 9 {
			return qmi.NASInitiateNetworkRegisterRequest{}, errors.New("invalid operator access technology")
		}
		rat = qmiRATFromATCode(*accessTechnologyValue)
	}
	return qmi.NASInitiateNetworkRegisterRequest{
		Mode:              qmi.NASNetworkRegisterManual,
		MCC:               uint16(mcc),
		MNC:               uint16(mnc),
		IncludesPCSDigit:  len(plmn) == 6,
		RadioAccessTech:   rat,
		ChangeDuration:    qmi.NASChangeDurationPermanent,
		HasChangeDuration: true,
	}, nil
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

// ensureNativeQMIRegistration runs the same NAS sequence that VoHive uses on
// OpenStick: wake DMS online, reassert automatic selection, submit a NAS
// registration request, then attach PS and wait for the serving-system
// indication to become authoritative.  The modem's AT+COPS surface on this
// firmware only changes presentation; it does not reliably drive this NAS
// state machine.
func ensureNativeQMIRegistration(
	ctx context.Context,
	session nativeQMIRegistrationSession,
	request qmi.NASInitiateNetworkRegisterRequest,
	setAutomatic bool,
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
			// VoHive treats this as a best-effort policy update: some OpenStick
			// firmware accepts the preference but reports an unsupported result
			// for one of the optional NAS TLVs.  The explicit NAS register below
			// remains the authoritative trigger.
			if !isUnsupportedQMISelectionCommand(err) {
				return fmt.Errorf("restore automatic QMI NAS selection: %w", err)
			}
		}
	}
	registerIssued := false
	forceSearchIssued := false
	radioCycleIssued := false
	forceSearchUnsupported := false
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
			if serving.PSAttached {
				return nil
			}
			if err := session.AttachDetach(ctx, true); err != nil {
				return fmt.Errorf("attach QMI packet service: %w", err)
			}
		} else if serving.RegistrationState == qmi.RegStateDenied {
			return errors.New("QMI network registration was denied")
		} else if !registerIssued {
			if err := session.InitiateNetworkRegister(ctx, request); err != nil {
				if !(setAutomatic && isUnsupportedQMIRegistrationCommand(err, qmi.NASInitiateNetworkRegister)) {
					return fmt.Errorf("initiate QMI network registration: %w", err)
				}
			}
			registerIssued = true
		}

		searching := serving.RegistrationState == qmi.RegStateSearching
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
	if err := ensureNativeQMIRegistration(ctx, session, request, false); err != nil {
		return OperatorSelection{}, err
	}
	return OperatorSelection{Mode: 1, Format: 2, Operator: strings.TrimSpace(plmn)}, nil
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
		selection, err = qmiOperatorSelectionFromPreference(pref)
		if err != nil {
			return OperatorSelection{}, err
		}
	}
	if err := ensureNativeQMIRegistration(ctx, session, request, setAutomatic); err != nil {
		return OperatorSelection{}, err
	}
	return selection, nil
}
