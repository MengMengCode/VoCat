package vowifi

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// qmiSMSCAddressPayload builds a QMI WMS Get SMSC Address TLV value: a
// three-character ASCII address type, a one-octet digit count, then digits.
func qmiSMSCAddressPayload(addressType, digits string) string {
	for len(addressType) < 3 {
		addressType += " "
	}
	return addressType[:3] + string([]byte{byte(len(digits))}) + digits
}

type fakeQMISMSCSession struct {
	fakeQMIUIMSession
	smsc    string
	smscErr error
	reads   int
}

func (session *fakeQMISMSCSession) GetSMSCAddress(context.Context) (string, error) {
	session.reads++
	if session.smscErr != nil {
		return "", session.smscErr
	}
	return session.smsc, nil
}

func TestQMIUIMAKAProviderReadSMSCenterKeepsInternationalType(t *testing.T) {
	session := &fakeQMISMSCSession{
		fakeQMIUIMSession: fakeQMIUIMSession{iccid: "894410215096799999"},
		// Vodafone UK returns its service centre without a leading "+" and
		// reports TON=international separately, exactly as AT+CSCA? does.
		smsc: qmiSMSCAddressPayload("145", "447802000332"),
	}
	provider, err := newQMIUIMAKAProvider(
		"/dev/wwan0qmi0",
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	smsc, err := provider.ReadSMSCenter(context.Background(), "wwan0")
	if err != nil {
		t.Fatalf("ReadSMSCenter: %v", err)
	}
	if smsc != "+447802000332" {
		t.Fatalf("SMSC = %q, want %q", smsc, "+447802000332")
	}
	if session.reads != 1 {
		t.Fatalf("WMS reads = %d, want 1", session.reads)
	}
	if !session.closed {
		t.Fatal("QMI session was not closed")
	}
}

func TestQMIUIMAKAProviderReadSMSCenterRejectsUnsupportedSession(t *testing.T) {
	session := &fakeQMIUIMSession{iccid: "894410215096799999"}
	provider, err := newQMIUIMAKAProvider(
		"/dev/wwan0qmi0",
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	if _, err := provider.ReadSMSCenter(context.Background(), "wwan0"); !errors.Is(err, ErrQMISMSCUnsupported) {
		t.Fatalf("error = %v, want %v", err, ErrQMISMSCUnsupported)
	}
	if !session.closed {
		t.Fatal("QMI session was not closed")
	}
}

func TestQMIUIMAKAProviderReadSMSCenterPropagatesWMSFailure(t *testing.T) {
	failure := errors.New("QMI service unavailable")
	session := &fakeQMISMSCSession{
		fakeQMIUIMSession: fakeQMIUIMSession{iccid: "894410215096799999"},
		smscErr:           failure,
	}
	provider, err := newQMIUIMAKAProvider(
		"/dev/wwan0qmi0",
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	if _, err := provider.ReadSMSCenter(context.Background(), "wwan0"); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want it to wrap %v", err, failure)
	}
}

func TestParseQMISMSCAddress(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "international type adds the plus",
			raw:  qmiSMSCAddressPayload("145", "447802000332"),
			want: "+447802000332",
		},
		{
			name: "national type is preserved unchanged",
			raw:  qmiSMSCAddressPayload("129", "13800210500"),
			want: "13800210500",
		},
		{
			name: "digits already carrying a plus stay international",
			raw:  qmiSMSCAddressPayload("", "+447802000332"),
			want: "+447802000332",
		},
		{
			name: "blank type is treated as unknown, not international",
			raw:  qmiSMSCAddressPayload("", "447802000332"),
			want: "447802000332",
		},
		{
			name: "trailing padding beyond the digit count is ignored",
			raw:  qmiSMSCAddressPayload("145", "447802000332") + "\x00\x00",
			want: "+447802000332",
		},
		{
			name: "a missing digit count falls back to the remaining payload",
			raw:  "145" + string([]byte{0}) + "447802000332",
			want: "+447802000332",
		},
		{
			name:    "truncated payload is rejected",
			raw:     "14",
			wantErr: true,
		},
		{
			name:    "non-numeric digits are rejected",
			raw:     qmiSMSCAddressPayload("145", "44780A000332"),
			wantErr: true,
		},
		{
			name:    "an empty address is rejected",
			raw:     qmiSMSCAddressPayload("145", ""),
			wantErr: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseQMISMSCAddress(testCase.raw)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("parseQMISMSCAddress(%q) = %q, want an error", testCase.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseQMISMSCAddress(%q): %v", testCase.raw, err)
			}
			if got != testCase.want {
				t.Fatalf("parseQMISMSCAddress(%q) = %q, want %q", testCase.raw, got, testCase.want)
			}
		})
	}
}

// efSMSPVodafoneUK is the EF_SMSP record 1 a live OpenStick 410 returned over
// QMI-UIM: a 12-byte "VODAFONE" alpha identifier, parameter indicators 0xE1
// (TP-DA absent, service centre present), an absent TP-DA, and a TS-Service
// Centre Address of 0x91 +447785016005 — the same value AT+CSCA? reported.
const efSMSPVodafoneUK = "564f4441464f4e45ffffffff" + "e1" +
	"ffffffffffffffffffffffff" + "0791447758100650ffffffff" + "0000a9"

type fakeQMISMSParameterSession struct {
	fakeQMIUIMSession
	smsc      string
	smscErr   error
	record    []byte
	recordErr error
}

func (session *fakeQMISMSParameterSession) GetSMSCAddress(context.Context) (string, error) {
	if session.smscErr != nil {
		return "", session.smscErr
	}
	return session.smsc, nil
}

func (session *fakeQMISMSParameterSession) ReadSMSParameterRecord(context.Context) ([]byte, error) {
	if session.recordErr != nil {
		return nil, session.recordErr
	}
	return append([]byte(nil), session.record...), nil
}

func TestQMIUIMAKAProviderReadSMSCenterFallsBackToEFSMSP(t *testing.T) {
	record, err := hex.DecodeString(efSMSPVodafoneUK)
	if err != nil {
		t.Fatalf("decode EF_SMSP fixture: %v", err)
	}
	session := &fakeQMISMSParameterSession{
		fakeQMIUIMSession: fakeQMIUIMSession{iccid: "894410215096799999"},
		// Observed on hardware: a freshly allocated WMS client can answer QMI
		// error 0x34 (device not ready) while the UICC read succeeds.
		smscErr: errors.New("QMI error: service=0x05 msg=0x0034 result=0x0001 error=0x0034"),
		record:  record,
	}
	provider, err := newQMIUIMAKAProvider(
		"/dev/wwan0qmi0",
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	smsc, err := provider.ReadSMSCenter(context.Background(), "wwan0")
	if err != nil {
		t.Fatalf("ReadSMSCenter: %v", err)
	}
	if smsc != "+447785016005" {
		t.Fatalf("SMSC = %q, want %q", smsc, "+447785016005")
	}
}

func TestQMIUIMAKAProviderReadSMSCenterPrefersWMS(t *testing.T) {
	record, err := hex.DecodeString(efSMSPVodafoneUK)
	if err != nil {
		t.Fatalf("decode EF_SMSP fixture: %v", err)
	}
	session := &fakeQMISMSParameterSession{
		fakeQMIUIMSession: fakeQMIUIMSession{iccid: "894410215096799999"},
		// The live WMS payload: ASCII type "145", a length octet, then digits
		// that already carry the leading "+".
		smsc:   qmiSMSCAddressPayload("145", "+447785016005"),
		record: record,
	}
	provider, err := newQMIUIMAKAProvider(
		"/dev/wwan0qmi0",
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	if err != nil {
		t.Fatalf("newQMIUIMAKAProvider: %v", err)
	}
	smsc, err := provider.ReadSMSCenter(context.Background(), "wwan0")
	if err != nil {
		t.Fatalf("ReadSMSCenter: %v", err)
	}
	if smsc != "+447785016005" {
		t.Fatalf("SMSC = %q, want %q", smsc, "+447785016005")
	}
}

func TestDecodeEFSMSPServiceCentre(t *testing.T) {
	cases := []struct {
		name    string
		record  string
		want    string
		wantErr bool
	}{
		{name: "live Vodafone UK record", record: efSMSPVodafoneUK, want: "+447785016005"},
		{
			name:   "a national type keeps the address unchanged",
			record: "ffffffffffffffffffffffff" + "e1" + "ffffffffffffffffffffffff" + "0781447758100650ffffffff" + "0000a9",
			want:   "447785016005",
		},
		{
			name:   "an odd digit count drops the padding nibble",
			record: "ffffffffffffffffffffffff" + "e1" + "ffffffffffffffffffffffff" + "07914477581006f0ffffffff" + "0000a9",
			want:   "+44778501600",
		},
		{
			name:    "an absent service centre is rejected",
			record:  "ffffffffffffffffffffffff" + "e3" + "ffffffffffffffffffffffff" + "0791447758100650ffffffff" + "0000a9",
			wantErr: true,
		},
		{
			name:    "an invalid address length is rejected",
			record:  "ffffffffffffffffffffffff" + "e1" + "ffffffffffffffffffffffff" + "ff91447758100650ffffffff" + "0000a9",
			wantErr: true,
		},
		{name: "a truncated record is rejected", record: "0791447758100650", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			record, err := hex.DecodeString(strings.ReplaceAll(testCase.record, " ", ""))
			if err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			got, err := decodeEFSMSPServiceCentre(record)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("decodeEFSMSPServiceCentre = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeEFSMSPServiceCentre: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("service centre = %q, want %q", got, testCase.want)
			}
		})
	}
}
