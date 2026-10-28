package vowifi

import (
	"context"
	"errors"
	"testing"

	"vocat/internal/modem"
)

// The vendor profile is the only place the EC20 and ZXIC dialects differ.
// These tests pin both sides of each knob so a future chipset cannot
// silently inherit the wrong behavior.

func zxicVendorBusy(command string) error {
	return &modem.CommandError{Command: command, Final: "+CME ERROR: 148"}
}

// The ZXIC platform firmware answers AT+CGACT? with vendor +CME ERROR: 148
// whenever the radio is off or it cannot report PDP state. The PDP
// checkpoint never drives restoration (RestoreCellularData is false by
// default) and RF-off implies no active PDP, so a failed read degrades to
// "no active contexts" instead of wedging the whole VoWiFi enable.
func TestZXICAdapterSnapshotToleratesCGACTQueryFailure(t *testing.T) {
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{
		{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
		{command: "AT+CGACT?", err: zxicVendorBusy("AT+CGACT?"), final: "+CME ERROR: 148"},
	}}
	adapter, err := NewZXICAdapter(transcript, ATModemAdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := adapter.Snapshot(context.Background(), "zxic-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.OperatingMode != 4 {
		t.Fatalf("operating mode = %d, want 4", snapshot.OperatingMode)
	}
	if snapshot.CellularDataEnabled {
		t.Fatal("CellularDataEnabled = true, want false when the PDP read is unavailable")
	}
	transcript.assertDone()
}

func TestZXICAdapterStopCellularDataToleratesCGACTQueryFailure(t *testing.T) {
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{
		{command: "AT+CGACT?", err: zxicVendorBusy("AT+CGACT?"), final: "+CME ERROR: 148"},
	}}
	adapter, err := NewZXICAdapter(transcript, ATModemAdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StopCellularData(context.Background(), "zxic-1"); err != nil {
		t.Fatalf("StopCellularData: %v", err)
	}
	transcript.assertDone()
}

// The EC20 profile stays strict: a failed PDP inventory still fails the
// radio phase, because Quectel-class firmware answers the query in every
// radio state it can be probed from.
func TestEC20AdapterSnapshotPropagatesCGACTQueryFailure(t *testing.T) {
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{
		{command: "AT+CFUN?", lines: []string{"+CFUN: 1"}},
		{command: "AT+CGACT?", err: errors.New("query failed")},
	}}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Snapshot(context.Background(), "ec20-1"); err == nil {
		t.Fatal("Snapshot error = nil, want the PDP query failure to propagate")
	}
	transcript.assertDone()
}

// The ZXIC platform firmware rejects the whole AT+CCID family with vendor
// +CME ERROR: 6003 and exposes the ICCID only through AT+ZICCID?. The
// adapter must never burn the control channel probing commands the dialect
// declares unsupported.
func TestZXICAdapterReadICCIDUsesOnlyZICCIDQuery(t *testing.T) {
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{
		{command: "AT+ZICCID?", lines: []string{"+ZICCID: 89441000400317114751"}},
	}}
	adapter, err := NewZXICAdapter(transcript, ATModemAdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.readICCID(context.Background(), "zxic-1")
	if err != nil {
		t.Fatalf("readICCID: %v", err)
	}
	if got != "89441000400317114751" {
		t.Fatalf("ICCID = %q", got)
	}
	transcript.assertDone()
}
