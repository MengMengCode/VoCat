package vowifi

import (
	"context"
	"errors"
	"fmt"
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
// while CFUN=4. Only that verified state allows assuming no active PDP.
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
		{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
		{command: "AT+CGACT?", err: zxicVendorBusy("AT+CGACT?"), final: "+CME ERROR: 148"},
		{command: "AT+CFUN?", lines: []string{"+CFUN: 4"}},
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

func TestATAdapterPDPInventoryFailuresRemainErrors(t *testing.T) {
	disconnected := errors.New("serial disconnected")
	for _, test := range []struct {
		name           string
		mode           int
		err            error
		canceled, ec20 bool
	}{
		{name: "online refusal", mode: 1, err: zxicVendorBusy("AT+CGACT?")},
		{name: "minimum functionality unverified", mode: 0, err: zxicVendorBusy("AT+CGACT?")},
		{name: "transport", mode: 4, err: disconnected},
		{name: "timeout", mode: 4, err: context.DeadlineExceeded},
		{name: "cancellation", mode: 4, err: context.Canceled},
		{name: "canceled refusal", mode: 4, err: zxicVendorBusy("AT+CGACT?"), canceled: true},
		{name: "other refusal", mode: 4, err: &modem.CommandError{Command: "AT+CGACT?", Final: "+CME ERROR: 6003"}},
		{name: "invalid response", mode: 4},
		{name: "EC20 remains strict", mode: 4, err: zxicVendorBusy("AT+CGACT?"), ec20: true},
	} {
		for _, phase := range []string{"snapshot", "stop", "restore"} {
			t.Run(test.name+"/"+phase, func(t *testing.T) {
				modeStep := ec20TranscriptStep{command: "AT+CFUN?", lines: []string{fmt.Sprintf("+CFUN: %d", test.mode)}}
				pdpStep := ec20TranscriptStep{command: "AT+CGACT?", err: test.err, lines: []string{"+CGACT: invalid"}}
				var steps []ec20TranscriptStep
				switch phase {
				case "snapshot":
					steps = []ec20TranscriptStep{modeStep, pdpStep}
				case "restore":
					steps = []ec20TranscriptStep{modeStep, modeStep, pdpStep}
				case "stop":
					steps = []ec20TranscriptStep{pdpStep}
					if !test.ec20 && !test.canceled && (test.mode == 0 || test.mode == 1) {
						steps = append(steps, modeStep)
					}
				}
				transcript := &ec20Transcript{t: t, steps: steps}
				profile := zxicVendorProfile
				if test.ec20 {
					profile = ec20VendorProfile
				}
				adapter, err := newATModemAdapter(transcript, ATModemAdapterOptions{}, profile)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if test.canceled {
					cancel()
				}
				switch phase {
				case "snapshot":
					_, err = adapter.Snapshot(ctx, "test")
				case "stop":
					err = adapter.StopCellularData(ctx, "test")
				case "restore":
					err = adapter.Restore(ctx, "test", RadioSnapshot{OperatingMode: test.mode})
				}
				want := test.err
				if test.canceled {
					want = context.Canceled
				}
				if err == nil || (want != nil && !errors.Is(err, want)) {
					t.Fatalf("error = %v, want %v", err, want)
				}
				transcript.assertDone()
			})
		}
	}
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
