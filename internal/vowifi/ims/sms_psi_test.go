package ims

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/vowifi"
)

const smsPSITestCenter = "+12025550100"
const smsPSITestURI = "sip:+12025550101@sms.example.test;user=phone"

type smsPSITestAKA struct {
	*recordingAKA
	psi  string
	err  error
	read func(context.Context) (string, error)
}

func (a *smsPSITestAKA) ReadSMSCenterPSI(ctx context.Context, _ string) (string, error) {
	if a.read != nil {
		return a.read(ctx)
	}
	return a.psi, a.err
}

// A real loopback registrar and MESSAGE receiver, never a modem or external SMS.
func smsPSITestSession(t *testing.T, aka vowifi.AKAProvider, publicIdentity ...string) (*Session, <-chan *sipRequest, *bytes.Buffer) {
	t.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	listener.SetDeadline(time.Now().Add(10 * time.Second))
	seen := make(chan *sipRequest, 16)
	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, remote, err := listener.ReadFromUDP(buffer)
			if err != nil {
				done <- err
				return
			}
			packet, err := parseSIPPacket(buffer[:n])
			if err != nil || packet.Request == nil {
				done <- fmt.Errorf("invalid request: %v", err)
				return
			}
			r := packet.Request
			code, reason := 200, "OK"
			var extra []string
			closing := false
			switch r.Method {
			case "REGISTER":
				if r.value("Expires") == "0" {
					closing = true
				} else if r.value("Authorization") == "" {
					code, reason = 401, "Unauthorized"
					extra = []string{`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `", algorithm=AKAv1-MD5, qop="auth"`}
				} else {
					extra = []string{"Contact: " + r.value("Contact") + ";expires=600", "P-Associated-URI: <sip:12025550100@msg.example.test>"}
				}
			case "MESSAGE":
				seen <- r
				code, reason = 202, "Accepted"
			default:
				done <- fmt.Errorf("unexpected method %s", r.Method)
				return
			}
			if _, err = listener.WriteToUDP(testResponse(code, reason, r.value("Call-ID"), r.value("CSeq"), extra), remote); err != nil {
				done <- err
				return
			}
			if closing {
				done <- nil
				return
			}
		}
	}()
	logs := new(bytes.Buffer)
	configured := ""
	if len(publicIdentity) > 0 {
		configured = publicIdentity[0]
	}
	provider, err := NewProvider(aka, Config{PublicIdentity: configured, PCSCF: listener.LocalAddr().String(), LocalAddress: "127.0.0.1", Transport: "udp", TransactionTimeout: time.Second, SecurityMode: SecurityDisabled, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	started, err := provider.Start(context.Background(), vowifi.IMSRequest{DeviceID: "psi-offline", Identity: vowifi.SIMIdentity{IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01", SMSC: smsPSITestCenter}, Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{Established: true, LocalIPv4: "127.0.0.1", PCSCF: []string{listener.LocalAddr().String()}}}})
	if err != nil {
		t.Fatal(err)
	}
	session := started.(*Session)
	t.Cleanup(func() {
		if err := session.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return session, seen, logs
}

func smsPSITestSubmit(t *testing.T, session *Session, seen <-chan *sipRequest) *sipRequest {
	t.Helper()
	result, err := session.SendSMS(context.Background(), vowifi.SMSSubmitRequest{Recipient: "+12025550123", Text: "OFFLINE TEST"})
	if err != nil || !result.AllPartsAccepted || result.PartsAccepted != 1 || result.PartResults[0].SIPCode != 202 {
		t.Fatalf("SendSMS = %#v, %v", result, err)
	}
	select {
	case r := <-seen:
		return r
	case <-time.After(time.Second):
		t.Fatal("no MESSAGE captured")
		return nil
	}
}

func TestSMSPSIValidation(t *testing.T) {
	valid := []string{
		smsPSITestURI, "sips:smsc@sms.example.test:5061;transport=tls",
		"SIP:smsc@sms.example.test", "sip:sms.example.test", "sip:smsc@[2001:db8::1]:5060",
		"sip:service%2Dcentre@sms.example.test;user=phone", "tel:+12025550100", "tel:+1-202-555-0100",
		"tel:5550100;phone-context=example.test",
	}
	invalid := []string{
		" ", "sip:", "sips:", "tel:", "https://sms.example.test", "sms.example.test",
		" sip:smsc@sms.example.test", "sip:smsc@sms.example.test ", "sip:sm sc@sms.example.test",
		"sip:smsc@sms.example.test\r\nX-Evil:yes", "sip:smsc@sms.example.test\x00", "sip:smsc@sms.example.test\x7f",
		"sip:smsc@sms.example.test%0d%0aX-Evil:yes", "sip:sm%09sc@sms.example.test", "sip:sm%20sc@sms.example.test",
		"sip:sm%250asc@sms.example.test", "sip:sm%sc@sms.example.test", "sip:sm%00sc@sms.example.test",
		"sip:smsc@sms.example.test?Subject=evil", "sip:smsc@sms.example.test%3fSubject=evil",
		"<sip:smsc@sms.example.test>", "sip:smsc@sms.example.test>;tag=evil", "sip:sm%3esc@sms.example.test",
		"sip:@sms.example.test", "sip:smsc@", "sip:a@@sms.example.test", "sip:a:b@sms.example.test",
		"sip:smsc@999.999.999.999", "sip:smsc@sms.123", "sip:smsc@192.0.2.999",
		"sip:smsc@-bad.example.test", "sip:smsc@bad-.example.test", "sip:smsc@bad..example.test", "sip:smsc@bad_host.example.test",
		"sip:smsc@sms.example.test/path", "sip:smsc@sms.example.test:abc", "sip:smsc@sms.example.test:65536", "sip:smsc@sms.example.test:",
		"sip:smsc@[not-ip]", "sip:smsc@2001:db8::1", "sip:smsc@%65xample.test", "sip:smsc@sms.example.test;", "sip:smsc@sms.example.test;=x",
		"tel:+", "tel:+not-a-number", "tel:5550100", "tel:+12025550100?To=evil", "tel:+12025550100;phone-context=",
		"sip:sm\u00a0sc@sms.example.test", "sip:sm%c2%85sc@sms.example.test", "sip:smsc@sms.example.test#fragment",
	}
	for _, group := range []struct {
		values []string
		valid  bool
	}{{valid, true}, {invalid, false}} {
		for _, value := range group.values {
			t.Run(value, func(t *testing.T) {
				session := &Session{provider: &Provider{aka: &smsPSITestAKA{psi: value}}}
				got, err := session.smsTarget(context.Background(), smsPSITestCenter)
				if err != nil {
					t.Fatal(err)
				}
				want := "tel:" + smsPSITestCenter
				if group.valid {
					want = value
				}
				if got != want {
					t.Fatalf("target = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestSMSPSIFallbackRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		aka  vowifi.AKAProvider
	}{
		{"unsupported", &recordingAKA{}},
		{"read_error", &smsPSITestAKA{err: errors.New("SIM read failed")}},
		{"invalid", &smsPSITestAKA{psi: "sip:unsafe\r\nX-Evil"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := new(bytes.Buffer)
			session := &Session{provider: &Provider{aka: tc.aka, config: Config{
				Logger: slog.New(slog.NewTextHandler(logs, nil)),
			}}}
			target, err := session.smsTarget(context.Background(), smsPSITestCenter)
			if err != nil || target != "tel:"+smsPSITestCenter {
				t.Fatalf("target = %q, %v; want numeric SMSC fallback", target, err)
			}
			if strings.Contains(logs.String(), "SIM read failed") || strings.Contains(logs.String(), "X-Evil") {
				t.Fatal("raw PSI/error leaked into logs")
			}
		})
	}
}

type smsPSICanceledCenterAKA struct{ *recordingAKA }

func (*smsPSICanceledCenterAKA) ReadSMSCenter(context.Context, string) (string, error) {
	return "", fmt.Errorf("SIM read: %w", context.Canceled)
}

func TestSMSPSICancellationNeverSends(t *testing.T) {
	for _, name := range []string{"before_read", "unsupported", "during_read", "reader_canceled", "reader_deadline", "numeric_reader_canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}
			reader := &smsPSITestAKA{recordingAKA: base, psi: smsPSITestURI}
			var aka vowifi.AKAProvider = reader
			want := context.Canceled
			switch name {
			case "before_read":
				cancel()
			case "unsupported":
				aka = base
				cancel()
			case "during_read":
				reader.read = func(context.Context) (string, error) { cancel(); return smsPSITestURI, nil }
			case "reader_canceled":
				reader.err = fmt.Errorf("read: %w", context.Canceled)
			case "reader_deadline":
				reader.err = fmt.Errorf("read: %w", context.DeadlineExceeded)
				want = context.DeadlineExceeded
			case "numeric_reader_canceled":
				aka = &smsPSICanceledCenterAKA{base}
			}
			session, seen, _ := smsPSITestSession(t, aka)
			if name == "numeric_reader_canceled" {
				session.request.Identity.SMSC = ""
				session.provider.config.SMSCenter = smsPSITestCenter
			}
			result, err := session.SendSMS(ctx, vowifi.SMSSubmitRequest{Recipient: "+12025550123", Text: "OFFLINE TEST"})
			if !errors.Is(err, want) {
				t.Errorf("SendSMS error=%v want %v", err, want)
			}
			if result.PartsAttempted != 0 {
				t.Errorf("attempted %d parts after cancellation", result.PartsAttempted)
			}
			if (name == "during_read" || name == "reader_canceled" || name == "reader_deadline") && result.SubmissionStatus != "failed" {
				t.Errorf("submission status=%q, want failed", result.SubmissionStatus)
			}
			select {
			case r := <-seen:
				t.Fatalf("sent MESSAGE after cancellation: %s", r.URI)
			case <-time.After(30 * time.Millisecond):
			}
		})
	}
}

type smsPSICancelLog struct {
	slog.Handler
	cancel  context.CancelFunc
	message string
}

func (h smsPSICancelLog) Enabled(context.Context, slog.Level) bool { return true }
func (h smsPSICancelLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == h.message {
		h.cancel()
	}
	return nil
}

func TestSMSPSICancellationBetweenParts(t *testing.T) {
	aka := &smsPSITestAKA{recordingAKA: &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}, psi: smsPSITestURI}
	session, seen, _ := smsPSITestSession(t, aka)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session.provider.config.Logger = slog.New(smsPSICancelLog{Handler: slog.Default().Handler(), cancel: cancel, message: "IMS SIP MESSAGE response received"})
	result, err := session.SendSMS(ctx, vowifi.SMSSubmitRequest{Recipient: "+12025550123", Text: strings.Repeat("A", 200)})
	if !errors.Is(err, context.Canceled) || result.PartsAttempted != 1 || result.PartsAccepted != 1 {
		t.Fatalf("SendSMS=%#v %v", result, err)
	}
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("missing expected MESSAGE")
	}
	select {
	case <-seen:
		t.Fatal("MESSAGE sent after cancellation")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestSMSPSISendUsesSIMTargetPreservesRPDU(t *testing.T) {
	aka := &smsPSITestAKA{recordingAKA: &recordingAKA{result: vowifi.AKAResult{RES: []byte{1, 2, 3, 4}}}, psi: smsPSITestURI}
	session, seen, logs := smsPSITestSession(t, aka)
	selected := smsPSITestSubmit(t, session, seen)
	if selected.URI != smsPSITestURI || selected.value("To") != "<"+smsPSITestURI+">" {
		t.Fatalf("SIM PSI ignored: URI=%q To=%q", selected.URI, selected.value("To"))
	}
	aka.psi = ""
	session.mu.Lock()
	session.nextRPReference = selected.Body[1]
	session.mu.Unlock()
	fallback := smsPSITestSubmit(t, session, seen)
	if fallback.URI != "tel:"+smsPSITestCenter || fallback.value("To") != "<tel:"+smsPSITestCenter+">" {
		t.Fatalf("fallback target = %q / %q", fallback.URI, fallback.value("To"))
	}
	if strings.Contains(logs.String(), "IMS outbound SMS route selected") {
		t.Fatal("fix must not add a per-message diagnostic log")
	}
	if !bytes.Equal(selected.Body, fallback.Body) {
		t.Fatalf("PSI changed RPDU: %x vs %x", selected.Body, fallback.Body)
	}
	parts, err := device.PrepareSMSSubmitTPDUs("+12025550123", "OFFLINE TEST")
	if err != nil {
		t.Fatal(err)
	}
	parts[0].TPDU[1] = selected.Body[1]
	want, err := buildRPData(selected.Body[1], smsPSITestCenter, parts[0].TPDU)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(selected.Body, want) {
		t.Fatalf("numeric SMSC/TPDU changed: %x want %x", selected.Body, want)
	}
}
