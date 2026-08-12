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
