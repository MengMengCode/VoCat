package ims

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestIncomingCallCanRingAndAnswerWithMediaOffer(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &Session{
		fromTag: "local-tag", calls: make(map[string]*imsCall), conn: client,
		identity: identitySet{user: "subscriber"}, transport: "udp",
	}
	packet, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:subscriber@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-incoming",
		"From: <tel:+447700900001>;tag=remote",
		"To: <sip:subscriber@example.test>",
		"Call-ID: incoming-call@example.test",
		"CSeq: 1 INVITE",
		"Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil || packet.Request == nil {
		t.Fatalf("parse INVITE: %v", err)
	}
	var responses [][]byte
	session.handleSIPRequest(packet.Request, func(response []byte) error {
		responses = append(responses, append([]byte(nil), response...))
		return nil
	})
	calls := session.Calls()
	if len(calls) != 1 || calls[0].Direction != "incoming" || calls[0].State != "ringing" || calls[0].Number != "+447700900001" {
		t.Fatalf("incoming Calls = %#v", calls)
	}
	if len(responses) != 1 || !strings.HasPrefix(string(responses[0]), "SIP/2.0 180 Ringing") {
		t.Fatalf("ringing response = %q", responses)
	}
	answered, err := session.AnswerCall(context.Background(), calls[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if answered.State != "active" || len(responses) != 2 || !strings.Contains(string(responses[1]), "a=sendrecv") {
		t.Fatalf("answered = %#v, response = %q", answered, responses[1])
	}
	if !strings.Contains(string(responses[1]), "Contact: <sip:subscriber@pipe;transport=udp>") {
		t.Fatalf("answer omitted dialog Contact: %q", responses[1])
	}
}

func TestIncomingCallCanBeRejected(t *testing.T) {
	session := &Session{fromTag: "local-tag", calls: make(map[string]*imsCall)}
	packet, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-a",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>",
		"Call-ID: reject-call", "CSeq: 1 INVITE", "Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil || packet.Request == nil {
		t.Fatalf("parse INVITE: %v", err)
	}
	var response []byte
	session.handleCallRequest(packet.Request, func(value []byte) error { response = append([]byte(nil), value...); return nil })
	if err := session.HangupCall(context.Background(), "reject-call"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(response), "SIP/2.0 486 Busy Here") {
		t.Fatalf("reject response = %q", response)
	}
	calls := session.Calls()
	if len(calls) != 1 || calls[0].State != "ended" || calls[0].EndedAt == nil {
		t.Fatalf("terminal call status = %#v", calls)
	}
}

func TestRejectedOutgoingCallRetainsSIPReason(t *testing.T) {
	session := &Session{calls: make(map[string]*imsCall)}
	call := &imsCall{public: vowifi.Call{ID: "rejected", State: "dialing"}}
	session.calls[call.public.ID] = call
	session.finishCall(call.public.ID, "failed", 484, "Address Incomplete\r\nignored")
	calls := session.Calls()
	if len(calls) != 1 || calls[0].State != "failed" || calls[0].SIPCode != 484 ||
		calls[0].Reason != "Address Incomplete ignored" || calls[0].EndedAt == nil {
		t.Fatalf("rejected call = %#v", calls)
	}
}

func TestCancelledOutgoingInviteDoesNotBecomeFailedOn487(t *testing.T) {
	call := &imsCall{
		public:     vowifi.Call{ID: "cancelled", State: "dialing"},
		callID:     "cancelled",
		responses:  make(chan *sipResponse, 1),
		terminated: true,
	}
	session := &Session{
		calls:          map[string]*imsCall{call.callID: call},
		transactions:   make(map[sipTransactionKey]chan *sipResponse),
		refreshContext: context.Background(),
	}
	key := sipTransactionKey{callID: call.callID, cseq: 1, method: "INVITE"}
	go session.watchOutgoingCall(call, key)
	call.responses <- &sipResponse{StatusCode: 487, Reason: "Request Terminated"}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		calls := session.Calls()
		if len(calls) == 1 && calls[0].EndedAt != nil {
			if calls[0].State != "ended" || calls[0].SIPCode != 487 {
				t.Fatalf("cancelled INVITE = %#v", calls[0])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancelled INVITE did not reach a terminal state")
}

func TestValidCallNumber(t *testing.T) {
	if !validCallNumber("+447700900000") || validCallNumber("12\r\nBYE") {
		t.Fatal("call number validation mismatch")
	}
}

func TestOutgoingLocalNumberUsesIMSPhoneContextAndMMTelHeaders(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	refreshContext, cancelRefresh := context.WithCancel(context.Background())
	defer cancelRefresh()
	session := &Session{
		provider: &Provider{config: Config{
			UserAgent:    "VoCat Test",
			SecurityMode: SecurityDisabled,
		}},
		request: vowifi.IMSRequest{Identity: vowifi.SIMIdentity{
			HomeMCC: "234", HomeMNC: "33", IMSI: "234330000000001", ICCID: "8944300000000000000",
		}},
		identity: identitySet{
			domain: "ims.mnc033.mcc234.3gppnetwork.org",
			public: "sip:234330000000001@ims.mnc033.mcc234.3gppnetwork.org",
			user:   "234330000000001",
		},
		endpoint:       pcscfEndpoint{host: "pcscf.test", port: 5060},
		transport:      "tcp",
		conn:           client,
		fromTag:        "local-tag",
		instanceID:     "urn:uuid:00000000-0000-4000-8000-000000000001",
		cseq:           1,
		transactions:   make(map[sipTransactionKey]chan *sipResponse),
		calls:          make(map[string]*imsCall),
		refreshContext: refreshContext,
		evidence: vowifi.IMSEvidence{
			PAssociatedURI: []string{"<tel:+447700900123>"},
			ServiceRoute:   []string{"<sip:route.ims.test;lr>"},
		},
	}
	wireResult := make(chan string, 1)
	go func() {
		packet, _ := io.ReadAll(peer)
		wireResult <- string(packet)
	}()

	call, err := session.DialCall(context.Background(), "888")
	if err != nil {
		t.Fatal(err)
	}
	session.finishCall(call.ID, "ended", 0, "test complete")
	cancelRefresh()
	_ = client.Close()
	wire := <-wireResult

	for _, expected := range []string{
		"INVITE tel:888;phone-context=ims.mnc033.mcc234.3gppnetwork.org SIP/2.0\r\n",
		"To: <tel:888;phone-context=ims.mnc033.mcc234.3gppnetwork.org>\r\n",
		"P-Preferred-Identity: <tel:+447700900123>\r\n",
		"P-Preferred-Service: " + mmtelServiceURN + "\r\n",
		`Accept-Contact: *;+g.3gpp.icsi-ref="` + mmtelFeatureTag + `"` + "\r\n",
		"P-Access-Network-Info: IEEE-802.11;i-wlan-node-id=000000000000;country=GB;network-provided\r\n",
		"User-Agent: VoCat Test\r\n",
		"Accept: application/sdp\r\n",
	} {
		if !strings.Contains(wire, expected) {
			t.Fatalf("INVITE omitted %q:\n%s", expected, wire)
		}
	}
}

func TestCallTargetURIUsesPhoneContextOnlyForLocalNumbers(t *testing.T) {
	domain := "ims.mnc033.mcc234.3gppnetwork.org"
	if got := callTargetURI("888", domain); got != "tel:888;phone-context="+domain {
		t.Fatalf("local target = %q", got)
	}
	if got := callTargetURI("+447700900123", domain); got != "tel:+447700900123" {
		t.Fatalf("global target = %q", got)
	}
}

func TestCallResponseDiagnosticIncludesNetworkReason(t *testing.T) {
	response := &sipResponse{
		StatusCode: 487,
		Reason:     "Request Terminated",
		Headers: map[string][]string{
			"reason": {`Q.850;cause=31;text="Normal, unspecified"`},
		},
	}
	want := `Request Terminated; Reason: Q.850;cause=31;text="Normal, unspecified"`
	if got := callResponseDiagnostic(response); got != want {
		t.Fatalf("diagnostic = %q, want %q", got, want)
	}
}
