package device

import (
	"errors"
	"testing"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

func TestQMINativeNetworkMetricsPreferActiveRFInfo(t *testing.T) {
	accessTech, band, channel := qmiNativeNetworkMetrics(
		&qmi.RFBandInfo{Bands: []qmi.RFBandInfoEntry{{
			RadioInterface:  qmi.DMSRadioInterfaceLTE,
			ActiveBandClass: 3,
			ActiveChannel:   1600,
		}}},
		&qmi.CellLocationInfo{LTE: &qmi.LTECellLocationInfo{EARFCN: 1600}},
	)
	if accessTech != "LTE" || band != "B3" || channel != "1600" {
		t.Fatalf("QMI metrics = %q/%q/%q, want LTE/B3/1600", accessTech, band, channel)
	}
}

func TestQMIBandLabelMapsLibQMIActiveBandEnum(t *testing.T) {
	if got := qmiBandLabel(qmi.DMSRadioInterfaceLTE, 122); got != "B3" {
		t.Fatalf("QMI EUTRAN-3 enum label = %q, want B3", got)
	}
	if got := qmiBandLabel(qmi.DMSRadioInterfaceNR5G, 269); got != "n78" {
		t.Fatalf("QMI NR5G-78 enum label = %q, want n78", got)
	}
}

func TestQMINativeNetworkMetricsFallbackToCellLocation(t *testing.T) {
	accessTech, band, channel := qmiNativeNetworkMetrics(
		nil,
		&qmi.CellLocationInfo{LTE: &qmi.LTECellLocationInfo{EARFCN: 1600}},
	)
	if accessTech != "LTE" || band != "B3" || channel != "1600" {
		t.Fatalf("cell metrics = %q/%q/%q, want LTE/B3/1600", accessTech, band, channel)
	}
}

func TestIsQMINotProvisioned(t *testing.T) {
	if !isQMINotProvisioned(&qmi.QMIError{ErrorCode: qmiErrorNotProvisioned}) {
		t.Fatal("NotProvisioned QMI error was not recognized")
	}
	if isQMINotProvisioned(errors.New("temporary modem failure")) {
		t.Fatal("ordinary error was classified as NotProvisioned")
	}
}

func TestMaskSnapshotForRadioOffClearsStaleServingState(t *testing.T) {
	signal := 31
	snapshot := &Snapshot{
		RegistrationStatus: 5,
		RegistrationSource: "QMI NAS",
		PSAttached:         true,
		OperatorName:       "China Mobile",
		OperatorCode:       "46000",
		AccessTech:         "LTE",
		Band:               "B3",
		Channel:            "1300",
		SignalRaw:          &signal,
		RSRP:               &signal,
		Phone:              PhoneNumber{Number: "+8613800138000"},
		IMSI:               "515027106574535",
	}

	maskSnapshotForRadioOff(snapshot)
	if snapshot.RegistrationStatus != 0 || snapshot.RegistrationSource != "QMI DMS" || snapshot.PSAttached ||
		snapshot.OperatorName != "" || snapshot.OperatorCode != "" || snapshot.AccessTech != "" ||
		snapshot.Band != "" || snapshot.Channel != "" || snapshot.SignalRaw != nil || snapshot.RSRP != nil {
		t.Fatalf("masked snapshot retained stale serving state: %#v", snapshot)
	}
	if snapshot.IMSI == "" || snapshot.Phone.Number == "" {
		t.Fatalf("mask removed identity fields: %#v", snapshot)
	}
}
