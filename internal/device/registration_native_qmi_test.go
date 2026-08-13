package device

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

type fakeNativeQMIRegistrationSession struct {
	mode             qmi.OperatingMode
	serving          []*qmi.ServingSystem
	selection        *qmi.SystemSelectionPreference
	setModes         []qmi.OperatingMode
	setPreferences   []qmi.SystemSelectionPreference
	registerRequests []qmi.NASInitiateNetworkRegisterRequest
	forceSearches    int
	attachRequests   []bool
	closeCount       int
}

func (session *fakeNativeQMIRegistrationSession) GetOperatingMode(context.Context) (qmi.OperatingMode, error) {
	return session.mode, nil
}

func (session *fakeNativeQMIRegistrationSession) SetOperatingMode(_ context.Context, mode qmi.OperatingMode) error {
	session.mode = mode
	session.setModes = append(session.setModes, mode)
	return nil
}

func (session *fakeNativeQMIRegistrationSession) Close() error {
	session.closeCount++
	return nil
}

func (session *fakeNativeQMIRegistrationSession) GetServingSystem(context.Context) (*qmi.ServingSystem, error) {
	if len(session.serving) == 0 {
		return &qmi.ServingSystem{RegistrationState: qmi.RegStateSearching}, nil
	}
	current := session.serving[0]
	if len(session.serving) > 1 {
		session.serving = session.serving[1:]
	}
	return current, nil
}

func (session *fakeNativeQMIRegistrationSession) GetSystemSelectionPreference(context.Context) (*qmi.SystemSelectionPreference, error) {
	if session.selection == nil {
		return &qmi.SystemSelectionPreference{}, nil
	}
	return session.selection, nil
}

func (session *fakeNativeQMIRegistrationSession) SetSystemSelectionPreference(_ context.Context, pref qmi.SystemSelectionPreference) error {
	session.selection = &pref
	session.setPreferences = append(session.setPreferences, pref)
	return nil
}

func (session *fakeNativeQMIRegistrationSession) InitiateNetworkRegister(_ context.Context, req qmi.NASInitiateNetworkRegisterRequest) error {
	session.registerRequests = append(session.registerRequests, req)
	return nil
}

func (session *fakeNativeQMIRegistrationSession) ForceNetworkSearch(context.Context) error {
	session.forceSearches++
	return nil
}

func (session *fakeNativeQMIRegistrationSession) AttachDetach(_ context.Context, attached bool) error {
	session.attachRequests = append(session.attachRequests, attached)
	return nil
}

func TestEnsureNativeQMIRegistrationDrivesNASSequence(t *testing.T) {
	session := &fakeNativeQMIRegistrationSession{
		mode: qmi.ModeOnline,
		serving: []*qmi.ServingSystem{
			{RegistrationState: qmi.RegStateSearching},
			{RegistrationState: qmi.RegStateSearching},
			{RegistrationState: qmi.RegStateRegistered, PSAttached: false},
			{RegistrationState: qmi.RegStateRegistered, PSAttached: true},
		},
	}

	if err := ensureNativeQMIRegistration(context.Background(), session, qmiRegistrationRequestAutomatic(), true); err != nil {
		t.Fatalf("ensure native QMI registration: %v", err)
	}
	if len(session.setModes) != 0 {
		t.Fatalf("operating mode writes = %#v, want no mode transition", session.setModes)
	}
	if len(session.setPreferences) != 1 || !session.setPreferences[0].HasNetworkSelectionPreference ||
		session.setPreferences[0].NetworkSelectionPreference != qmi.NASNetworkSelectionAutomatic {
		t.Fatalf("selection writes = %#v, want automatic", session.setPreferences)
	}
	if len(session.registerRequests) != 1 || session.registerRequests[0].Mode != qmi.NASNetworkRegisterAutomatic {
		t.Fatalf("registration requests = %#v, want one automatic request", session.registerRequests)
	}
	if session.forceSearches != 1 {
		t.Fatalf("force-search count = %d, want 1", session.forceSearches)
	}
	if len(session.attachRequests) != 1 || !session.attachRequests[0] {
		t.Fatalf("attach requests = %#v, want one attach", session.attachRequests)
	}
}

func TestEnsureNativeQMIRegistrationRefusesFlightMode(t *testing.T) {
	for _, mode := range []qmi.OperatingMode{qmi.ModeLowPower, qmi.ModeOffline, qmi.ModeShutdown, qmi.ModePersistLow, qmi.ModeOnlyLowPower, qmi.ModeReset} {
		t.Run(fmt.Sprintf("mode_%d", mode), func(t *testing.T) {
			session := &fakeNativeQMIRegistrationSession{mode: mode}
			err := ensureNativeQMIRegistration(context.Background(), session, qmiRegistrationRequestAutomatic(), true)
			if !errors.Is(err, ErrRadioFlightMode) {
				t.Fatalf("ensure native QMI registration error = %v, want ErrRadioFlightMode", err)
			}
			if len(session.setModes) != 0 || len(session.registerRequests) != 0 || len(session.attachRequests) != 0 {
				t.Fatalf("flight-mode registration mutated QMI session: modes=%v register=%v attach=%v", session.setModes, session.registerRequests, session.attachRequests)
			}
		})
	}
}

func TestQMIManualRegisterRequestMapsPLMNAndRAT(t *testing.T) {
	rat := 7
	request, err := qmiManualRegisterRequest("46001", &rat)
	if err != nil {
		t.Fatalf("manual request: %v", err)
	}
	if request.Mode != qmi.NASNetworkRegisterManual || request.MCC != 460 || request.MNC != 1 ||
		request.IncludesPCSDigit || request.RadioAccessTech != 0x08 || !request.HasChangeDuration ||
		request.ChangeDuration != qmi.NASChangeDurationPermanent {
		t.Fatalf("manual request = %#v", request)
	}
}

func TestQMIManualSelectionPreferenceMapsPLMN(t *testing.T) {
	pref, selection, err := qmiManualSelectionPreference("46001")
	if err != nil {
		t.Fatalf("manual preference: %v", err)
	}
	if pref.NetworkSelectionPreference != qmi.NASNetworkSelectionManual ||
		!pref.HasNetworkSelectionPreference || !pref.HasManualNetworkSelection ||
		!pref.HasChangeDuration || pref.ChangeDuration != qmi.NASChangeDurationPermanent {
		t.Fatalf("manual preference = %#v", pref)
	}
	if selection.MCC != 460 || selection.MNC != 1 || selection.IncludesPCSDigit {
		t.Fatalf("manual selection = %#v", selection)
	}
}

func TestEnsureNativeQMIRegistrationWaitsForManualTarget(t *testing.T) {
	session := &fakeNativeQMIRegistrationSession{
		mode: qmi.ModeOnline,
		serving: []*qmi.ServingSystem{
			{RegistrationState: qmi.RegStateRegistered, PSAttached: true, MCC: 460, MNC: 0},
			{RegistrationState: qmi.RegStateSearching},
			{RegistrationState: qmi.RegStateRegistered, PSAttached: false, MCC: 460, MNC: 1},
			{RegistrationState: qmi.RegStateRegistered, PSAttached: true, MCC: 460, MNC: 1},
		},
	}
	request, err := qmiManualRegisterRequest("46001", nil)
	if err != nil {
		t.Fatalf("manual request: %v", err)
	}
	target := qmi.ManualNetworkSelection{MCC: 460, MNC: 1}
	if err := ensureNativeQMIRegistrationForTarget(context.Background(), session, request, false, &target); err != nil {
		t.Fatalf("ensure manual registration: %v", err)
	}
	if len(session.registerRequests) != 1 || session.registerRequests[0].Mode != qmi.NASNetworkRegisterManual ||
		session.registerRequests[0].MCC != 460 || session.registerRequests[0].MNC != 1 {
		t.Fatalf("registration requests = %#v, want one manual 46001 request", session.registerRequests)
	}
	if session.forceSearches != 1 {
		t.Fatalf("force-search count = %d, want 1", session.forceSearches)
	}
	if len(session.attachRequests) != 1 || !session.attachRequests[0] {
		t.Fatalf("attach requests = %#v, want one attach", session.attachRequests)
	}
}

func TestNativeQMIRegistrationCyclesEarlyWhenForceSearchUnsupported(t *testing.T) {
	if got := nativeQMIRegistrationRadioCycleThreshold(true); got != 3 {
		t.Fatalf("unsupported force-search threshold = %d, want 3", got)
	}
	if got := nativeQMIRegistrationRadioCycleThreshold(false); got != 30 {
		t.Fatalf("supported force-search threshold = %d, want 30", got)
	}
}

func TestIsNativeQMICandidateRequiresOpenStickWWANPair(t *testing.T) {
	tests := []struct {
		name      string
		candidate modem.Candidate
		want      bool
	}{
		{
			name: "native",
			candidate: modem.Candidate{
				ID:         "wwan0",
				QMIControl: "/dev/wwan0qmi0",
			},
			want: true,
		},
		{
			name: "different control device",
			candidate: modem.Candidate{
				ID:         "wwan0",
				QMIControl: "/dev/cdc-wdm0",
			},
			want: false,
		},
		{
			name: "non native id",
			candidate: modem.Candidate{
				ID:         "usb0",
				QMIControl: "/dev/usb0qmi0",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNativeQMICandidate(tt.candidate); got != tt.want {
				t.Fatalf("isNativeQMICandidate() = %v, want %v", got, tt.want)
			}
		})
	}
}
