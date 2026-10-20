package device

import (
	"context"
	"errors"
	"testing"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
	"vocat/internal/modem"
)

type qmiHotSwapTranscript struct {
	qmiSIMStartupTranscript
	card        qmi.CardStatusDetails
	offSlots    []uint8
	offErr      error
	afterOff    context.CancelFunc
	onCanceled  bool
	settling    int
	statusReads int
}

func (s *qmiHotSwapTranscript) Close() error { return nil }
func (s *qmiHotSwapTranscript) GetCardStatusDetails(context.Context) (*qmi.CardStatusDetails, qmi.SIMStatus, error) {
	if s.powered {
		s.statusReads++
		if s.statusReads <= s.settling {
			return &qmi.CardStatusDetails{CardState: 2, ErrorCode: 3}, qmi.SIMNotReady, nil
		}
		return &qmi.CardStatusDetails{CardState: 1, NumApp: 1, AppState: 7}, qmi.SIMReady, nil
	}
	return &s.card, qmi.SIMNotReady, nil
}

func TestNativeQMIHotSwapWaitsForATRAndRepairsPoweredDownSlot(t *testing.T) {
	for _, code := range []uint8{1, 6} {
		s := &qmiHotSwapTranscript{
			qmiSIMStartupTranscript: qmiSIMStartupTranscript{mode: qmi.ModeLowPower},
			card:                    qmi.CardStatusDetails{CardState: 2, ErrorCode: code}, settling: 2,
		}
		m := &Manager{}
		recovered, err := m.recoverNativeQMIHotSwapSession(context.Background(), "synthetic-control", s)
		if err != nil || !recovered || s.statusReads < 3 {
			t.Fatalf("transient no-ATR was treated as final: recovered=%v, reads=%d, err=%v", recovered, s.statusReads, err)
		}
		if code == 1 && len(s.offSlots) != 0 {
			t.Fatal("powered-down slot was powered off again")
		}
	}
}

func TestNativeQMIHotSwapRestoresPowerAfterUncertainPowerOff(t *testing.T) {
	s := &qmiHotSwapTranscript{
		qmiSIMStartupTranscript: qmiSIMStartupTranscript{mode: qmi.ModeLowPower},
		card:                    qmi.CardStatusDetails{CardState: 2, ErrorCode: 6}, offErr: context.DeadlineExceeded,
	}
	m := &Manager{}
	recovered, err := m.recoverNativeQMIHotSwapSession(context.Background(), "synthetic-control", s)
	if recovered || !errors.Is(err, context.DeadlineExceeded) || !s.powered || len(s.powerSlots) != 1 {
		t.Fatalf("uncertain power-off did not trigger power restoration: %v, %v", recovered, err)
	}
}
func (s *qmiHotSwapTranscript) PowerOffSIM(_ context.Context, slot uint8) error {
	s.offSlots = append(s.offSlots, slot)
	if s.afterOff != nil {
		s.afterOff()
	}
	return s.offErr
}
func (s *qmiHotSwapTranscript) PowerOnSIM(ctx context.Context, slot uint8) error {
	s.onCanceled = ctx.Err() != nil
	return s.qmiSIMStartupTranscript.PowerOnSIM(ctx, slot)
}

func TestNativeQMIHotSwapRecoversLatchedRemovalWithoutRF(t *testing.T) {
	for _, code := range []uint8{3, 6} {
		s := &qmiHotSwapTranscript{
			qmiSIMStartupTranscript: qmiSIMStartupTranscript{mode: qmi.ModeOnline},
			card:                    qmi.CardStatusDetails{CardState: 2, ErrorCode: code},
		}
		m := &Manager{}
		recovered, err := m.recoverNativeQMIHotSwapSession(context.Background(), "synthetic-control", s)
		if err != nil || !recovered || !s.powered {
			t.Fatalf("error %d recovery = %v, %v", code, recovered, err)
		}
		if len(s.requestedModes) != 1 || s.requestedModes[0] != qmi.ModeLowPower || s.mode != qmi.ModeLowPower {
			t.Fatal("recovery did not establish RF-off mode")
		}
		if len(s.offSlots) != 1 || s.offSlots[0] != 1 || len(s.powerSlots) != 1 || s.powerSlots[0] != 1 {
			t.Fatal("recovery did not cycle only physical slot 1")
		}
	}
}

func TestNativeQMIHotSwapLeavesAbsentLockedAndOtherErrorsAlone(t *testing.T) {
	for _, card := range []qmi.CardStatusDetails{
		{CardState: 0},
		{CardState: 1, PIN1State: qmi.PINStatusNotVerified},
		{CardState: 1, PIN1State: qmi.PINStatusBlocked},
		{CardState: 1, NumApp: 1, AppState: 7},
		{CardState: 2, ErrorCode: 4},
		{CardState: 2, ErrorCode: 5},
	} {
		s := &qmiHotSwapTranscript{card: card}
		m := &Manager{}
		recovered, err := m.recoverNativeQMIHotSwapSession(context.Background(), "synthetic-control", s)
		if err != nil || recovered || len(s.offSlots) != 0 || len(s.powerSlots) != 0 || len(s.requestedModes) != 0 {
			t.Fatalf("unrelated card state changed power: %+v", card)
		}
	}
}

func TestNativeQMIHotSwapAlwaysRestoresPowerAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &qmiHotSwapTranscript{
		qmiSIMStartupTranscript: qmiSIMStartupTranscript{mode: qmi.ModeLowPower},
		card:                    qmi.CardStatusDetails{CardState: 2, ErrorCode: 6}, afterOff: cancel,
	}
	m := &Manager{}
	recovered, err := m.recoverNativeQMIHotSwapSession(ctx, "synthetic-control", s)
	if recovered || !errors.Is(err, context.Canceled) || !s.powered || s.onCanceled {
		t.Fatalf("cancelled operation stranded SIM power: recovered=%v, powered=%v, canceled=%v, err=%v", recovered, s.powered, s.onCanceled, err)
	}
}

func TestNativeQMIHotSwapRequiresVerifiedRFOffAndThrottlesFailure(t *testing.T) {
	s := &qmiHotSwapTranscript{
		qmiSIMStartupTranscript: qmiSIMStartupTranscript{mode: qmi.ModeOnline, ignoreModeChange: true},
		card:                    qmi.CardStatusDetails{CardState: 2, ErrorCode: 6},
	}
	m := &Manager{}
	if recovered, err := m.recoverNativeQMIHotSwapSession(context.Background(), "synthetic-control", s); recovered || err == nil || len(s.offSlots) != 0 {
		t.Fatal("cycled SIM without verified RF-off mode")
	}
	s.ignoreModeChange = false
	if recovered, err := m.recoverNativeQMIHotSwapSession(context.Background(), "synthetic-control", s); recovered || err != nil || len(s.requestedModes) != 1 {
		t.Fatal("failed recovery was not throttled")
	}
}

func TestNativeQMISnapshotDoesNotUseGenericCFUNCycle(t *testing.T) {
	client := &lenientATClient{}
	m := &Manager{}
	_, err := m.readSnapshot(context.Background(), "mhi-wwan0", modem.Candidate{ID: "mhi-wwan0", QMIControl: "/dev/wwan0qmi0"}, "qmi", "synthetic-previous-card", nil, client)
	if err != nil || client.saw("AT+CFUN=0") || client.saw("AT+CFUN=4") {
		t.Fatalf("native SIM recovery used generic CFUN cycling: %v, %v", client.commands, err)
	}
}

func TestNativeQMISIMInterfaceEnablesSIMWithoutEnablingRF(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 0")},
		{command: "AT+CFUN=4", response: okResponse()},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 4")},
	}}
	m, _ := newStartedTestManager(t, client)
	prepared, err := m.prepareNativeQMISIMInterface(context.Background(), client)
	if err != nil || !prepared {
		t.Fatalf("prepare SIM interface = %v, %v", prepared, err)
	}
	client.assertDone(t)
}

func TestNativeQMISIMInterfaceLeavesExistingModesAlone(t *testing.T) {
	for _, mode := range []string{"1", "4", "7"} {
		client := &transcriptClient{steps: []clientStep{
			{command: "AT+CFUN?", response: okResponse("+CFUN: " + mode)},
		}}
		m, _ := newStartedTestManager(t, client)
		prepared, err := m.prepareNativeQMISIMInterface(context.Background(), client)
		if err != nil || prepared {
			t.Fatalf("changed existing CFUN=%s: %v, %v", mode, prepared, err)
		}
		client.assertDone(t)
	}
}

func TestNativeQMISIMInterfaceRequiresVerifiedCFUN4(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CFUN?", response: okResponse("+CFUN: 0")},
		{command: "AT+CFUN=4", response: okResponse()},
		{command: "AT+CFUN?", response: okResponse("+CFUN: 0")},
	}}
	m, _ := newStartedTestManager(t, client)
	prepared, err := m.prepareNativeQMISIMInterface(context.Background(), client)
	if err == nil || prepared {
		t.Fatal("reported success without verified SIM interface mode")
	}
	client.assertDone(t)
}
