package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

type qmiSIMStartupTranscript struct {
	mode             qmi.OperatingMode
	identityErr      error
	setErr           error
	powerErr         error
	reads            int
	modeReads        int
	powerSlots       []uint8
	requestedModes   []qmi.OperatingMode
	powered          bool
	ignoreModeChange bool
	cancel           context.CancelFunc
}

type blockingQMIICCIDTranscript struct{ qmiSIMStartupTranscript }

func (s *blockingQMIICCIDTranscript) GetICCID(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestVoWiFiNativeQMIUnresponsiveCardDoesNotHoldLeaseForLifecycleTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s := &blockingQMIICCIDTranscript{qmiSIMStartupTranscript: qmiSIMStartupTranscript{mode: qmi.ModeLowPower}}
	start := time.Now()
	_, recovered, err := readVoWiFiNativeQMIICCID(ctx, s)
	if !errors.Is(err, context.DeadlineExceeded) || recovered || ctx.Err() != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("unresponsive card consumed lifecycle deadline: recovered=%v, parent=%v, elapsed=%v, err=%v", recovered, ctx.Err(), time.Since(start), err)
	}
	if len(s.powerSlots) != 0 || len(s.requestedModes) != 0 {
		t.Fatal("unresponsive read changed SIM or radio power without a recovery condition")
	}
}

func (s *qmiSIMStartupTranscript) GetICCID(context.Context) (string, error) {
	s.reads++
	if s.identityErr != nil && !s.powered {
		return "", s.identityErr
	}
	return "synthetic-card", nil
}
func (s *qmiSIMStartupTranscript) GetOperatingMode(context.Context) (qmi.OperatingMode, error) {
	s.modeReads++
	return s.mode, nil
}
func (s *qmiSIMStartupTranscript) SetOperatingMode(_ context.Context, mode qmi.OperatingMode) error {
	s.requestedModes = append(s.requestedModes, mode)
	if s.setErr != nil {
		return s.setErr
	}
	if !s.ignoreModeChange {
		s.mode = mode
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}
func (s *qmiSIMStartupTranscript) PowerOnSIM(_ context.Context, slot uint8) error {
	s.powerSlots = append(s.powerSlots, slot)
	if s.powerErr != nil {
		return s.powerErr
	}
	s.powered = true
	return nil
}

func TestVoWiFiNativeQMIRecoversShutdownBeforeReadingSIM(t *testing.T) {
	s := &qmiSIMStartupTranscript{mode: qmi.ModeShutdown, identityErr: errors.New("synthetic UIM no ATR")}
	iccid, recovered, err := readVoWiFiNativeQMIICCID(context.Background(), s)
	if err != nil || !recovered || iccid != "synthetic-card" {
		t.Fatalf("recovery result = %q, %v, %v", iccid, recovered, err)
	}
	if len(s.requestedModes) != 1 || s.requestedModes[0] != qmi.ModeLowPower || s.mode != qmi.ModeLowPower {
		t.Fatalf("recovery requested unsafe RF modes: %v", s.requestedModes)
	}
	if len(s.powerSlots) != 1 || s.powerSlots[0] != 1 || s.reads != 2 {
		t.Fatal("SIM was not initialized and reread on physical slot 1")
	}
}

func TestVoWiFiNativeQMILeavesWorkingAndNonShutdownSIMsAlone(t *testing.T) {
	readErr := errors.New("synthetic SIM absent or PIN locked")
	for _, mode := range []qmi.OperatingMode{qmi.ModeOnline, qmi.ModeLowPower, qmi.ModeOffline, qmi.ModePersistLow, qmi.ModeOnlyLowPower, qmi.ModeFactoryTest} {
		s := &qmiSIMStartupTranscript{mode: mode, identityErr: readErr}
		_, recovered, err := readVoWiFiNativeQMIICCID(context.Background(), s)
		if !errors.Is(err, readErr) || recovered || len(s.requestedModes) != 0 || len(s.powerSlots) != 0 {
			t.Fatalf("unrelated read failure caused a SIM/RF change in mode %d", mode)
		}
	}
	s := &qmiSIMStartupTranscript{mode: qmi.ModeShutdown}
	_, recovered, err := readVoWiFiNativeQMIICCID(context.Background(), s)
	if err != nil || recovered || s.modeReads != 0 || len(s.requestedModes) != 0 || len(s.powerSlots) != 0 {
		t.Fatal("working identity caused power changes")
	}
}

func TestVoWiFiNativeQMIRecoveryFailureAndCancellation(t *testing.T) {
	failure := errors.New("synthetic hardware rejection")
	for _, s := range []*qmiSIMStartupTranscript{
		{mode: qmi.ModeShutdown, identityErr: failure, setErr: failure},
		{mode: qmi.ModeShutdown, identityErr: failure, powerErr: failure},
	} {
		_, recovered, err := readVoWiFiNativeQMIICCID(context.Background(), s)
		if err == nil || recovered {
			t.Fatal("failed recovery reported success")
		}
		for _, mode := range s.requestedModes {
			if mode != qmi.ModeLowPower {
				t.Fatal("failed recovery enabled RF")
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &qmiSIMStartupTranscript{mode: qmi.ModeShutdown, identityErr: failure, ignoreModeChange: true, cancel: cancel}
	_, recovered, err := readVoWiFiNativeQMIICCID(ctx, s)
	if !errors.Is(err, context.Canceled) || recovered || len(s.powerSlots) != 0 {
		t.Fatalf("cancelled recovery continued: %v", err)
	}
}
