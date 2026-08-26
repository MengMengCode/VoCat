package ims

import (
	"bufio"
	"strings"
	"testing"
	"time"
)

func TestReadSIPPacketWithKeepaliveReportsPeerPingAndReadsSIPMessage(t *testing.T) {
	response := testResponse(200, "OK", "keepalive-test", "7 REGISTER", nil)
	reader := bufio.NewReader(strings.NewReader("\r\n\r\n" + string(response)))
	var events []bool

	packet, hasPacket, err := readSIPPacketWithKeepalive(reader, func(ping bool) error {
		events = append(events, ping)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasPacket || packet.Response == nil || packet.Response.StatusCode != 200 {
		t.Fatalf("packet=%#v hasPacket=%t", packet, hasPacket)
	}
	if len(events) != 1 || !events[0] {
		t.Fatalf("CRLF events=%v, want one peer ping", events)
	}
}

func TestReadSIPPacketWithKeepaliveIgnoresAmbiguousSingleCRLF(t *testing.T) {
	response := testResponse(200, "OK", "keepalive-test", "8 REGISTER", nil)
	reader := bufio.NewReader(strings.NewReader("\r\n" + string(response)))
	var events []bool

	packet, hasPacket, err := readSIPPacketWithKeepalive(reader, func(ping bool) error {
		events = append(events, ping)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasPacket || packet.Response == nil || packet.Response.StatusCode != 200 {
		t.Fatalf("packet=%#v hasPacket=%t", packet, hasPacket)
	}
	if len(events) != 0 {
		t.Fatalf("CRLF events=%v, want no pong event", events)
	}
}

func TestSIPFlowRecoveryDelayStaysWithinConfiguredRange(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		lower    time.Duration
		upper    time.Duration
	}{
		{name: "first retry", failures: 0, lower: 15 * time.Second, upper: 30 * time.Second},
		{name: "second retry", failures: 1, lower: 30 * time.Second, upper: 60 * time.Second},
		{name: "later retry", failures: 5, lower: 8 * time.Minute, upper: 16 * time.Minute},
		{name: "capped retry", failures: 10, lower: 15 * time.Minute, upper: 30 * time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for sample := 0; sample < 20; sample++ {
				delay := sipFlowRecoveryDelay(test.failures)
				if delay < test.lower || delay > test.upper {
					t.Fatalf("failures=%d delay=%s, want range [%s,%s]", test.failures, delay, test.lower, test.upper)
				}
			}
		})
	}
}

func TestNegotiatedKeepaliveDelayIsBeforePeerInterval(t *testing.T) {
	session := &Session{transport: "tcp", keepaliveInterval: 30 * time.Second}
	for sample := 0; sample < 50; sample++ {
		delay := session.flowKeepaliveDelay()
		if delay < 24*time.Second || delay > 30*time.Second {
			t.Fatalf("negotiated keepalive delay=%s, want range [24s,30s]", delay)
		}
	}
}

func TestSIPCRLFKeepaliveRequiresViaNegotiation(t *testing.T) {
	session := &Session{
		transport: "tcp",
	}
	if session.sipCRLFKeepaliveEnabled() {
		t.Fatal("CRLF keepalive must not run without Via keep negotiation")
	}
	session.keepaliveNegotiated = true
	if !session.sipCRLFKeepaliveEnabled() {
		t.Fatal("Via keep negotiation should enable CRLF keepalive")
	}
}

func TestParseSIPViaKeepalive(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		want     bool
		interval time.Duration
	}{
		{name: "no parameter", values: []string{"SIP/2.0/TCP pcscf.example.test:5060;rport"}},
		{name: "bare keep compatibility permission", values: []string{"SIP/2.0/TCP pcscf.example.test:5060;rport;keep"}, want: true},
		{name: "recommended interval", values: []string{"SIP/2.0/TCP pcscf.example.test:5060;rport;keep=90"}, want: true, interval: 90 * time.Second},
		{name: "zero chooses local interval", values: []string{"SIP/2.0/TCP pcscf.example.test:5060;rport;keep=0"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, interval := parseSIPViaKeepalive(test.values)
			if got != test.want || interval != test.interval {
				t.Fatalf("parseSIPViaKeepalive() = (%t, %s), want (%t, %s)", got, interval, test.want, test.interval)
			}
		})
	}
}

func TestAddSIPViaKeepaliveIntervalOnlyChangesBareTopViaKeep(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		want       string
		negotiated bool
	}{
		{
			name:       "bare keep",
			value:      "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1;keep",
			want:       "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1;keep=108",
			negotiated: true,
		},
		{
			name:  "no keep",
			value: "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1",
			want:  "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1",
		},
		{
			name:  "keep already has interval",
			value: "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1;keep=90",
			want:  "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1;keep=90",
		},
		{
			name:       "comma separated top Via",
			value:      "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1;keep, SIP/2.0/TCP proxy.example.test:5060;branch=z9hG4bK-2",
			want:       "SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-1;keep=108, SIP/2.0/TCP proxy.example.test:5060;branch=z9hG4bK-2",
			negotiated: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, negotiated := addSIPViaKeepaliveInterval(test.value, sipInboundKeepaliveIntervalSeconds)
			if got != test.want || negotiated != test.negotiated {
				t.Fatalf("addSIPViaKeepaliveInterval() = (%q, %t), want (%q, %t)", got, negotiated, test.want, test.negotiated)
			}
		})
	}
}

func TestInboundSIPResponseNegotiatesBareViaKeep(t *testing.T) {
	request := &sipRequest{
		Method: "OPTIONS",
		Headers: map[string][]string{
			"via":     {"SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-test;keep"},
			"from":    {"<sip:caller@example.test>;tag=remote"},
			"to":      {"<sip:user@example.test>"},
			"call-id": {"keep-negotiation-test"},
			"cseq":    {"1 OPTIONS"},
		},
	}
	session := &Session{transport: "tcp", fromTag: "local"}
	var response []byte
	session.handleSIPRequest(request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.Contains(string(response), "Via: SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-test;keep=108\r\n") {
		t.Fatalf("response did not negotiate Via keep: %q", response)
	}
}

func TestInboundACKDoesNotNegotiateViaKeep(t *testing.T) {
	request := &sipRequest{
		Method: "ACK",
		Headers: map[string][]string{
			"via": {"SIP/2.0/TCP pcscf.example.test:5060;branch=z9hG4bK-test;keep"},
		},
	}
	session := &Session{transport: "tcp"}
	session.negotiateInboundSIPViaKeepalive(request)
	if got := request.value("Via"); !strings.HasSuffix(got, ";keep") {
		t.Fatalf("ACK Via changed to %q", got)
	}
}

func TestAppendSupportedOptionsIncludesPathOnly(t *testing.T) {
	session := &Session{transport: "tcp"}
	if got := session.appendSupportedOptions("100rel, timer"); got != "100rel, timer, path" {
		t.Fatalf("unconfirmed Supported header=%q", got)
	}
	if got := session.appendSupportedOptions("path"); got != "path" {
		t.Fatalf("duplicate path Supported header=%q", got)
	}
}
