package ims

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestIMSReceivedSMSTimestampCorrectionPreservesArrivalAndRawTPDU(t *testing.T) {
	for _, test := range []struct {
		name     string
		identity vowifi.SIMIdentity
		zoneByte byte
		wantUTC  string
		correct  bool
	}{
		{"DITO mislabeled UTC", vowifi.SIMIdentity{HomeMCC: "515", HomeMNC: "66", ICCID: "89636624000000000001"}, 0x00, "2026-10-22T05:08:46Z", true},
		{"DITO correct offset", vowifi.SIMIdentity{HomeMCC: "515", HomeMNC: "66", ICCID: "89636624000000000001"}, 0x23, "2026-10-22T05:08:46Z", false},
		{"unrelated UTC carrier", vowifi.SIMIdentity{HomeMCC: "999", HomeMNC: "01"}, 0x00, "2026-10-22T13:08:46Z", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tpdu, err := hex.DecodeString("0405912143F500006201223180640005C82293F904")
			if err != nil {
				t.Fatal(err)
			}
			tpdu[14] = test.zoneByte
			rpdu := append([]byte{0x01, 0x2a, 0x00, 0x00, byte(len(tpdu))}, tpdu...)
			var received *ReceivedSMS
			session := &Session{
				request: vowifi.IMSRequest{DeviceID: "reader", Identity: test.identity},
				provider: &Provider{config: Config{
					Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
					OnSMS: func(_ context.Context, message ReceivedSMS) error {
						received = &message
						return nil
					},
				}},
			}
			before := time.Now().UTC()
			// No From/PAI is needed to exercise the decoded callback. The RP-ACK
			// target is deliberately absent so this unit test never sends traffic.
			session.processSMSMessage(&sipRequest{
				Method: "MESSAGE", Body: rpdu,
				Headers: map[string][]string{"content-type": {smsContentType}, "call-id": {"timestamp-test"}},
			})
			if received == nil || received.ServiceCenterTimestamp == nil {
				t.Fatalf("missing decoded SMS: %#v", received)
			}
			want, _ := time.Parse(time.RFC3339, test.wantUTC)
			if !received.ServiceCenterTimestamp.Equal(want) || (received.TimestampCorrection != nil) != test.correct {
				t.Fatalf("SCTS=%v correction=%#v, want %v corrected=%v", received.ServiceCenterTimestamp, received.TimestampCorrection, want, test.correct)
			}
			if received.Timestamp.Before(before) || received.Timestamp.After(time.Now().UTC()) {
				t.Fatalf("arrival timestamp was replaced with the SMSC clock: %v", received.Timestamp)
			}
			if received.RawTPDU != strings.ToUpper(hex.EncodeToString(tpdu)) {
				t.Fatal("original TPDU was changed")
			}
			if test.correct && received.TimestampCorrection.OriginalTimestamp.UTC().Format(time.RFC3339) != "2026-10-22T13:08:46Z" {
				t.Fatalf("lost raw network timestamp: %#v", received.TimestampCorrection)
			}
		})
	}
}
