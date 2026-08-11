package vowifi

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
)

type fakeQMIManagerRadioDeviceManager struct {
	entry          device.Device
	getErr         error
	refreshResults []qmiRadioRefreshResult
	flightResults  []device.FlightResult
	flightErr      error
	networkErr     error
	calls          []string
	networkRequest device.NetworkRequest
}

type qmiRadioRefreshResult struct {
	snapshot device.Snapshot
	err      error
}

func (fake *fakeQMIManagerRadioDeviceManager) Get(id string) (device.Device, error) {
	fake.calls = append(fake.calls, "get:"+id)
	return fake.entry, fake.getErr
}

func (fake *fakeQMIManagerRadioDeviceManager) Refresh(
	_ context.Context,
	id string,
) (device.Snapshot, error) {
	fake.calls = append(fake.calls, "refresh:"+id)
	if len(fake.refreshResults) == 0 {
		return device.Snapshot{}, errors.New("unexpected refresh")
	}
	result := fake.refreshResults[0]
	fake.refreshResults = fake.refreshResults[1:]
	return result.snapshot, result.err
}

func (fake *fakeQMIManagerRadioDeviceManager) SetFlight(
	_ context.Context,
	id string,
	enabled bool,
) (device.FlightResult, error) {
	fake.calls = append(fake.calls, "flight:"+id+":"+map[bool]string{true: "on", false: "off"}[enabled])
	if fake.flightErr != nil {
		return device.FlightResult{}, fake.flightErr
	}
	if len(fake.flightResults) == 0 {
		return device.FlightResult{}, errors.New("unexpected flight request")
	}
	result := fake.flightResults[0]
	fake.flightResults = fake.flightResults[1:]
	return result, nil
}

func (fake *fakeQMIManagerRadioDeviceManager) SetNetwork(
	_ context.Context,
	id string,
	request device.NetworkRequest,
) (device.NetworkResult, error) {
	fake.calls = append(fake.calls, "network:"+id)
	fake.networkRequest = request
	return device.NetworkResult{Enabled: request.Enabled}, fake.networkErr
}

func TestQMIManagerRadioLifecycleUsesResolvedDeviceManager(t *testing.T) {
	fake := &fakeQMIManagerRadioDeviceManager{
		entry: device.Device{
			ID:         "wwan0",
			Discovered: true,
			Candidate: modem.Candidate{
				ID:               "wwan0",
				QMIControl:       "/dev/wwan0qmi0",
				NetworkInterface: "wwan0",
			},
		},
		refreshResults: []qmiRadioRefreshResult{{snapshot: device.Snapshot{
			DeviceID:      "wwan0",
			OperatingMode: 1,
			ModeKnown:     true,
		}}},
		flightResults: []device.FlightResult{
			{CurrentMode: 0, FlightMode: true, RadioOff: true},
			{PreviousMode: 0, CurrentMode: 1, Changed: true},
		},
	}
	radio, err := NewQMIManagerRadio(fake, QMIManagerRadioOptions{
		ResolveDeviceID: func(_ context.Context, id string) (string, error) {
			if id != "configured-410" {
				t.Fatalf("configured id = %q", id)
			}
			return "wwan0", nil
		},
	})
	if err != nil {
		t.Fatalf("NewQMIManagerRadio: %v", err)
	}

	snapshot, err := radio.Snapshot(context.Background(), "configured-410")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.OperatingMode != 1 || snapshot.CellularDataEnabled || snapshot.PureAirplanePolicy {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := radio.EnterVoWiFiRFOff(context.Background(), "configured-410"); err != nil {
		t.Fatalf("EnterVoWiFiRFOff: %v", err)
	}
	if err := radio.StopCellularData(context.Background(), "configured-410"); err != nil {
		t.Fatalf("StopCellularData: %v", err)
	}
	if err := radio.Restore(context.Background(), "configured-410", snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	wantCalls := []string{
		"get:wwan0",
		"refresh:wwan0",
		"get:wwan0",
		"flight:wwan0:on",
		"get:wwan0",
		"network:wwan0",
		"get:wwan0",
		"flight:wwan0:off",
	}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", fake.calls, wantCalls)
	}
	if fake.networkRequest.Enabled || fake.networkRequest.APN != "" || fake.networkRequest.IPVersion != "IP" {
		t.Fatalf("network request = %#v", fake.networkRequest)
	}
}

func TestQMIManagerRadioNormalizesResidualRFOffToOnlineRestore(t *testing.T) {
	fake := &fakeQMIManagerRadioDeviceManager{
		entry: device.Device{ID: "wwan0", Discovered: true, Candidate: modem.Candidate{
			ID: "wwan0", QMIControl: "/dev/wwan0qmi0", NetworkInterface: "wwan0",
		}, Snapshot: &device.Snapshot{
			DeviceID:      "wwan0",
			OperatingMode: 7,
			ModeKnown:     true,
			FlightMode:    true,
			RadioOff:      true,
		}},
		flightResults: []device.FlightResult{{PreviousMode: 7, CurrentMode: 1, Changed: true}},
	}
	radio, err := NewQMIManagerRadio(fake, QMIManagerRadioOptions{})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := radio.Snapshot(context.Background(), "wwan0")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.OperatingMode != 1 {
		t.Fatalf("restore mode = %d, want online mode 1", snapshot.OperatingMode)
	}
	if err := radio.Restore(context.Background(), "wwan0", snapshot); err != nil {
		t.Fatal(err)
	}
	if want := []string{"get:wwan0", "get:wwan0", "flight:wwan0:off"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %#v, want %#v", fake.calls, want)
	}
}

func TestQMIManagerRadioPreservesExplicitAirplanePolicy(t *testing.T) {
	fake := &fakeQMIManagerRadioDeviceManager{
		entry: device.Device{ID: "wwan0", Discovered: true, Candidate: modem.Candidate{
			ID: "wwan0", QMIControl: "/dev/wwan0qmi0", NetworkInterface: "wwan0",
		}, Snapshot: &device.Snapshot{
			DeviceID:      "wwan0",
			OperatingMode: 7,
			ModeKnown:     true,
			FlightMode:    true,
			RadioOff:      true,
		}},
		flightResults: []device.FlightResult{{CurrentMode: 0, FlightMode: true, RadioOff: true}},
	}
	radio, err := NewQMIManagerRadio(fake, QMIManagerRadioOptions{
		PureAirplanePolicy: func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := radio.Snapshot(context.Background(), "wwan0")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.PureAirplanePolicy || snapshot.OperatingMode != 7 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := radio.Restore(context.Background(), "wwan0", snapshot); err != nil {
		t.Fatal(err)
	}
	if want := []string{"get:wwan0", "get:wwan0", "flight:wwan0:on"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %#v, want %#v", fake.calls, want)
	}
}

func TestQMIManagerRadioRejectsATFallbackCandidate(t *testing.T) {
	fake := &fakeQMIManagerRadioDeviceManager{
		entry: device.Device{ID: "usb-modem", Discovered: true, Candidate: modem.Candidate{
			ID:               "usb-modem",
			QMIControl:       "/dev/cdc-wdm0",
			NetworkInterface: "wwan0",
		}},
		flightResults: []device.FlightResult{{CurrentMode: 0, FlightMode: true, RadioOff: true}},
	}
	radio, err := NewQMIManagerRadio(fake, QMIManagerRadioOptions{})
	if err != nil {
		t.Fatal(err)
	}

	err = radio.EnterVoWiFiRFOff(context.Background(), "usb-modem")
	if err == nil {
		t.Fatal("expected a non-native candidate to fail closed")
	}
	if want := []string{"get:usb-modem"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %#v, want no SetFlight fallback", fake.calls)
	}
}
