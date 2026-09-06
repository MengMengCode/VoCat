package vowifi

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"testing"

	"vocat/internal/modem"
)

// This executor exercises the real adapter boundary, without a serial device.
type ec20PSIExecutor struct {
	mu  sync.Mutex
	run func(context.Context, string, string) (modem.Response, error)
}

func (e *ec20PSIExecutor) LockUICC()   { e.mu.Lock() }
func (e *ec20PSIExecutor) UnlockUICC() { e.mu.Unlock() }
func (e *ec20PSIExecutor) ExecuteAT(ctx context.Context, id, command string) (modem.Response, error) {
	return e.run(ctx, id, command)
}

func TestEC20ReadSMSCenterPSINoCacheAndLocks(t *testing.T) {
	e := &ec20PSIExecutor{}
	adapter, err := NewEC20Adapter(e, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	e.run = func(gotCtx context.Context, id, command string) (modem.Response, error) {
		if gotCtx != ctx || id != "device" {
			t.Fatal("context or trimmed device ID not forwarded")
		}
		if command != `AT+CRSM=178,28645,1,4,0,,"3F007F10"` {
			t.Fatalf("unexpected command %q", command)
		}
		if adapter.apduMu.TryLock() {
			adapter.apduMu.Unlock()
			t.Fatal("APDU lock not held")
		}
		if e.mu.TryLock() {
			e.mu.Unlock()
			t.Fatal("UICC lock not held")
		}
		uris := []string{"sip:first.example", "sip:second.example"}
		if calls >= len(uris) {
			t.Fatal("unexpected extra command")
		}
		uri := uris[calls]
		calls++
		return modem.Response{Final: "OK", Lines: []string{`+CRSM: 144,0,"` + hex.EncodeToString(append([]byte{0x80, byte(len(uri))}, []byte(uri)...)) + `"`}}, nil
	}
	for _, want := range []string{"sip:first.example", "sip:second.example"} {
		got, err := adapter.ReadSMSCenterPSI(ctx, " device ")
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if calls != 2 {
		t.Fatalf("read calls = %d; PSI must not be cached", calls)
	}
	if !adapter.apduMu.TryLock() {
		t.Fatal("APDU lock leaked")
	}
	adapter.apduMu.Unlock()
	if !e.mu.TryLock() {
		t.Fatal("UICC lock leaked")
	}
	e.mu.Unlock()
}

func TestEC20ReadSMSCenterPSIFailures(t *testing.T) {
	const good = `+CRSM: 144,0,"8003736970"`
	transportErr := errors.New("test transport failed")
	tests := []struct {
		name  string
		lines []string
		final string
		err   error
	}{
		{"transport", nil, "", transportErr},
		{"card_status", []string{`+CRSM: 106,130`}, "OK", nil},
		{"invalid_status", []string{`+CRSM: x,0,"8003736970"`}, "OK", nil},
		{"no_data", []string{`+CRSM: 144,0`}, "OK", nil},
		{"invalid_hex", []string{`+CRSM: 144,0,"XYZ"`}, "OK", nil},
		{"odd_hex", []string{`+CRSM: 144,0,"800"`}, "OK", nil},
		{"malformed_record", []string{`+CRSM: 144,0,"8008736970"`}, "OK", nil},
		{"missing_response", nil, "OK", nil},
		{"duplicate_response", []string{good, good}, "OK", nil},
		{"failed_final", []string{good}, "ERROR", nil},
		{"missing_final", []string{good}, "", nil},
		{"nonzero_success_sw2", []string{`+CRSM: 144,1,"8003736970"`}, "OK", nil},
		{"response_pending", []string{`+CRSM: 159,3,"8003736970"`}, "OK", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &ec20PSIExecutor{run: func(context.Context, string, string) (modem.Response, error) {
				return modem.Response{Lines: tt.lines, Final: tt.final}, tt.err
			}}
			adapter, err := NewEC20Adapter(e, EC20AdapterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := adapter.ReadSMSCenterPSI(context.Background(), "device")
			if err == nil || got != "" {
				t.Fatalf("got %q, %v; want rejection", got, err)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("lost transport error: %v", err)
			}
			if !adapter.apduMu.TryLock() {
				t.Fatal("APDU lock leaked on error")
			}
			adapter.apduMu.Unlock()
			if !e.mu.TryLock() {
				t.Fatal("UICC lock leaked on error")
			}
			e.mu.Unlock()
		})
	}
}

func TestEC20ReadSMSCenterPSIRejectsInvalidRequest(t *testing.T) {
	e := &ec20PSIExecutor{run: func(context.Context, string, string) (modem.Response, error) {
		t.Fatal("invalid request issued AT command")
		return modem.Response{}, nil
	}}
	adapter, err := NewEC20Adapter(e, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ReadSMSCenterPSI(context.Background(), " \t"); err == nil {
		t.Fatal("accepted empty device ID")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.ReadSMSCenterPSI(ctx, "device"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want canceled", err)
	}
}

func TestParseEC20SMSCenterPSIRecord(t *testing.T) {
	const uri = "sip:smsc.example.test"
	short := append([]byte{0x80, byte(len(uri))}, []byte(uri)...)
	tests := []struct {
		name   string
		record []byte
		want   string
	}{
		{"short", short, uri},
		{"padding", append(append([]byte{}, short...), 0xff, 0xff), uri},
		// SIM BER-TLV permits definite long forms; tolerate non-minimal encodings.
		{"long81", append([]byte{0x80, 0x81, byte(len(uri))}, []byte(uri)...), uri},
		{"long82", append([]byte{0x80, 0x82, 0, byte(len(uri))}, []byte(uri)...), uri},
		{"long_value", append([]byte{0x80, 0x81, 128}, bytes.Repeat([]byte{'a'}, 128)...), string(bytes.Repeat([]byte{'a'}, 128))},
		{"empty_record", nil, ""},
		{"padding_only", []byte{0xff, 0xff}, ""},
		{"empty_uri", []byte{0x80, 0}, ""},
		{"missing_length", []byte{0x80}, ""},
		{"truncated_value", []byte{0x80, 4, 's'}, ""},
		{"truncated_long_length", []byte{0x80, 0x82, 0}, ""},
		{"truncated_long_value", []byte{0x80, 0x82, 1, 0, 's'}, ""},
		{"indefinite_length", []byte{0x80, 0x80, 's', 0, 0}, ""},
		{"reserved_length", []byte{0x80, 0xff}, ""},
		{"excessive_length_octets", []byte{0x80, 0x84, 0xff, 0xff, 0xff, 0xff}, ""},
		{"wrong_tag", append([]byte{0x81, byte(len(uri))}, []byte(uri)...), ""},
		{"constructed_tag", append([]byte{0xa0, byte(len(uri))}, []byte(uri)...), ""},
		{"bare_uri", []byte(uri), ""},
		{"duplicate_uri", append(append([]byte{}, short...), short...), ""},
		{"unknown_tlv_after_uri", append(append([]byte{}, short...), 0x81, 0), ""},
		{"data_after_padding", append(append([]byte{}, short...), 0xff, 0x80, 0), ""},
		{"leading_padding", append([]byte{0xff}, short...), ""},
		{"zero_padding", append(append([]byte{}, short...), 0), ""},
		{"all_spaces", []byte{0x80, 2, ' ', ' '}, ""},
		{"nul_in_value", []byte{0x80, 3, 's', 0, 'p'}, ""},
		{"newline_in_value", []byte{0x80, 3, 's', '\n', 'p'}, ""},
		{"padding_in_value", []byte{0x80, 3, 's', 0xff, 'p'}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEC20SMSCenterPSIRecord(tt.record)
			if tt.want == "" {
				if err == nil || got != "" {
					t.Fatalf("got %q, %v; want rejection", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestEC20ReadSMSCenterPSITranscript(t *testing.T) {
	// Synthetic URI; the command and 125-byte / tag 80 layout match the live probe.
	const uri = "sip:smsc.ims.mnc001.mcc001.3gppnetwork.org;lr=1"
	record := append([]byte{0x80, byte(len(uri))}, []byte(uri)...)
	record = append(record, bytes.Repeat([]byte{0xff}, 125-len(record))...)
	transcript := &ec20Transcript{t: t, steps: []ec20TranscriptStep{{
		command: `AT+CRSM=178,28645,1,4,0,,"3F007F10"`,
		lines:   []string{`+CRSM: 144,0,"` + hex.EncodeToString(record) + `"`},
	}}}
	adapter, err := NewEC20Adapter(transcript, EC20AdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Runtime assertion makes the initial missing optional capability a test failure.
	reader, ok := any(adapter).(interface {
		ReadSMSCenterPSI(context.Context, string) (string, error)
	})
	if !ok {
		t.Fatal("EC20Adapter is missing the live SMS center PSI reader")
	}
	got, err := reader.ReadSMSCenterPSI(context.Background(), "test-device")
	if err != nil || got != uri {
		t.Fatalf("got %q, %v; want %q", got, err, uri)
	}
	transcript.assertDone()
}
