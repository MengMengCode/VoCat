package vowifi

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
)

type fakeQMIFileSession struct {
	fakeQMIUIMSession
	files      map[uint16][]byte
	readErr    error
	reads      int
	lastPath   []byte
	lastFileID uint16
}

func (session *fakeQMIFileSession) ReadTransparent(
	_ context.Context,
	fileID uint16,
	path []byte,
) ([]byte, error) {
	session.reads++
	session.lastFileID = fileID
	session.lastPath = append([]byte(nil), path...)
	if session.readErr != nil {
		return nil, session.readErr
	}
	data, ok := session.files[fileID]
	if !ok {
		return nil, errors.New("file not found")
	}
	return append([]byte(nil), data...), nil
}

// efADTwoDigitMNC is the EF_AD image a live OpenStick 410 returned over
// QMI-UIM for a Vodafone UK USIM, whose IMSI splits as MCC 234 / MNC 15.
const efADTwoDigitMNC = "00000102"

func newHomePLMNSession(t *testing.T, efAD string) *fakeQMIFileSession {
	t.Helper()
	data, err := hex.DecodeString(efAD)
	if err != nil {
		t.Fatalf("decode EF_AD fixture: %v", err)
	}
	return &fakeQMIFileSession{
		fakeQMIUIMSession: fakeQMIUIMSession{iccid: "894410215096799999"},
		files:             map[uint16][]byte{efADFileID: data},
	}
}

func TestQMIUIMHomePLMNReaderSplitsIMSIByEFAD(t *testing.T) {
	session := newHomePLMNSession(t, efADTwoDigitMNC)
	reader := newQMIUIMHomePLMNReader(
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	mcc, mnc, err := reader.Read(context.Background(), "/dev/wwan0qmi0", "894410215096799999", "234159876543210")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mcc != "234" || mnc != "15" {
		t.Fatalf("home PLMN = %s/%s, want 234/15", mcc, mnc)
	}
	if session.lastFileID != efADFileID {
		t.Fatalf("file ID = %#x, want %#x", session.lastFileID, efADFileID)
	}
	if hex.EncodeToString(session.lastPath) != "003fff7f" {
		t.Fatalf("path = %x, want the ADF_USIM alias 003fff7f", session.lastPath)
	}
	if !session.closed {
		t.Fatal("QMI session was not closed")
	}
}

func TestQMIUIMHomePLMNReaderCachesPerCard(t *testing.T) {
	session := newHomePLMNSession(t, efADTwoDigitMNC)
	reader := newQMIUIMHomePLMNReader(
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	for attempt := 0; attempt < 3; attempt++ {
		if _, _, err := reader.Read(
			context.Background(), "/dev/wwan0qmi0", "894410215096799999", "234159876543210",
		); err != nil {
			t.Fatalf("Read attempt %d: %v", attempt, err)
		}
	}
	if session.reads != 1 {
		t.Fatalf("EF_AD reads = %d, want 1; the identity watchdog must not reopen QMI on every tick", session.reads)
	}
}

func TestQMIUIMHomePLMNReaderRereadsWhenIMSIStopsMatching(t *testing.T) {
	session := newHomePLMNSession(t, efADTwoDigitMNC)
	reader := newQMIUIMHomePLMNReader(
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	if _, _, err := reader.Read(
		context.Background(), "/dev/wwan0qmi0", "894410215096799999", "234159876543210",
	); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	// Same ICCID, different IMSI: the cached split must not be applied blindly.
	mcc, mnc, err := reader.Read(
		context.Background(), "/dev/wwan0qmi0", "894410215096799999", "515660123456789",
	)
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if mcc != "515" || mnc != "66" {
		t.Fatalf("home PLMN = %s/%s, want 515/66", mcc, mnc)
	}
	if session.reads != 2 {
		t.Fatalf("EF_AD reads = %d, want 2", session.reads)
	}
}

func TestQMIUIMHomePLMNReaderFailsClosedOnUnsupportedSession(t *testing.T) {
	session := &fakeQMIUIMSession{iccid: "894410215096799999"}
	reader := newQMIUIMHomePLMNReader(
		func(context.Context, string) (qmiUIMSession, error) { return session, nil },
	)
	_, _, err := reader.Read(context.Background(), "/dev/wwan0qmi0", "894410215096799999", "234159876543210")
	if !errors.Is(err, ErrQMIUIMFileUnsupported) {
		t.Fatalf("error = %v, want %v", err, ErrQMIUIMFileUnsupported)
	}
}

func TestEFADMNCLength(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		want    int
		wantErr bool
	}{
		{name: "two digit MNC", data: efADTwoDigitMNC, want: 2},
		{name: "three digit MNC", data: "00000103", want: 3},
		{name: "high nibble is ignored", data: "000001f2", want: 2},
		{name: "unassignable length is rejected", data: "00000104", wantErr: true},
		{name: "zero length is rejected", data: "00000100", wantErr: true},
		{name: "short file is rejected", data: "000001", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := hex.DecodeString(testCase.data)
			if err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			got, err := efADMNCLength(data)
			if testCase.wantErr {
				if !errors.Is(err, ErrEC20MNCUnavailable) {
					t.Fatalf("error = %v, want %v", err, ErrEC20MNCUnavailable)
				}
				return
			}
			if err != nil {
				t.Fatalf("efADMNCLength: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("MNC length = %d, want %d", got, testCase.want)
			}
		})
	}
}
