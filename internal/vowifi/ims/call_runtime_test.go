package ims

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

type recordingSIPConn struct {
	mu     sync.Mutex
	writes [][]byte
}

func (connection *recordingSIPConn) Read([]byte) (int, error) { return 0, io.EOF }
func (connection *recordingSIPConn) Write(value []byte) (int, error) {
	connection.mu.Lock()
	connection.writes = append(connection.writes, append([]byte(nil), value...))
	connection.mu.Unlock()
	return len(value), nil
}
func (*recordingSIPConn) Close() error { return nil }
func (*recordingSIPConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5062}
}
func (*recordingSIPConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5060}
}
func (*recordingSIPConn) SetDeadline(time.Time) error      { return nil }
func (*recordingSIPConn) SetReadDeadline(time.Time) error  { return nil }
func (*recordingSIPConn) SetWriteDeadline(time.Time) error { return nil }
func (connection *recordingSIPConn) captured() [][]byte {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	result := make([][]byte, len(connection.writes))
	for index := range connection.writes {
		result[index] = append([]byte(nil), connection.writes[index]...)
	}
	return result
}

func TestIncomingCallCanRingAndAnswerWithMediaOffer(t *testing.T) {
	session := &Session{fromTag: "local-tag", calls: make(map[string]*imsCall)}
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
}

func TestAnswerAndHangupAtomicallyClaimIncomingFinalResponse(t *testing.T) {
	connection := &recordingSIPConn{}
	session := &Session{
		fromTag: "local-tag", conn: connection, transport: "tcp", refreshContext: context.Background(),
		calls: make(map[string]*imsCall), transactions: make(map[sipTransactionKey]chan *sipResponse),
	}
	packet, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/TCP 192.0.2.10:5060;branch=z9hG4bK-claim",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
		"Call-ID: final-claim", "CSeq: 1 INVITE", "Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	var responseMu sync.Mutex
	var responses [][]byte
	session.handleCallRequest(packet.Request, func(value []byte) error {
		responseMu.Lock()
		responses = append(responses, append([]byte(nil), value...))
		responseMu.Unlock()
		return nil
	})
	responseMu.Lock()
	responses = nil // Ignore the initial 180 for the final-response assertion.
	responseMu.Unlock()
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	go func() {
		<-start
		_, _ = session.AnswerCall(context.Background(), "final-claim")
		done <- struct{}{}
	}()
	go func() {
		<-start
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_ = session.HangupCall(ctx, "final-claim")
		done <- struct{}{}
	}()
	close(start)
	<-done
	<-done
	responseMu.Lock()
	defer responseMu.Unlock()
	finals := 0
	for _, value := range responses {
		if strings.HasPrefix(string(value), "SIP/2.0 200 OK") || strings.HasPrefix(string(value), "SIP/2.0 486 Busy Here") {
			finals++
		}
	}
	if finals != 1 {
		t.Fatalf("Answer/Hangup emitted %d final INVITE responses: %q", finals, responses)
	}
	if call := session.calls["final-claim"]; call != nil {
		session.stopFinalINVITEResponse(call)
		if call.media != nil {
			_ = call.media.Close()
		}
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

func TestIncomingCANCELMustMatchOriginalINVITETransaction(t *testing.T) {
	session := &Session{fromTag: "local-tag", calls: make(map[string]*imsCall)}
	invite, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-cancel-match",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
		"Call-ID: cancel-match", "CSeq: 7 INVITE", "Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	var inviteResponse []byte
	session.handleCallRequest(invite.Request, func(value []byte) error {
		inviteResponse = append([]byte(nil), value...)
		return nil
	})
	call := session.calls["cancel-match"]
	if call == nil || call.media == nil {
		t.Fatal("INVITE did not create a ringing call")
	}
	defer call.media.Close()
	makeCANCEL := func(branch string, cseq int) *sipRequest {
		packet, parseErr := parseSIPPacket([]byte(strings.Join([]string{
			"CANCEL sip:user@example.test SIP/2.0",
			"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=" + branch,
			"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
			"Call-ID: cancel-match", "CSeq: " + strconv.Itoa(cseq) + " CANCEL",
			"Content-Length: 0", "", "",
		}, "\r\n")))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return packet.Request
	}
	var cancelResponse []byte
	session.handleCallRequest(makeCANCEL("z9hG4bK-wrong", 7), func(value []byte) error {
		cancelResponse = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(cancelResponse), "SIP/2.0 481") || call.public.State != "ringing" {
		t.Fatalf("mismatched CANCEL changed call: response=%q call=%#v", cancelResponse, call.public)
	}
	cancelResponse = nil
	session.handleCallRequest(makeCANCEL("z9hG4bK-cancel-match", 8), func(value []byte) error {
		cancelResponse = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(cancelResponse), "SIP/2.0 481") || call.public.State != "ringing" {
		t.Fatalf("wrong-CSeq CANCEL changed call: response=%q call=%#v", cancelResponse, call.public)
	}
	cancelResponse = nil
	session.handleCallRequest(makeCANCEL("z9hG4bK-cancel-match", 7), func(value []byte) error {
		cancelResponse = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(cancelResponse), "SIP/2.0 200 OK") ||
		!strings.HasPrefix(string(inviteResponse), "SIP/2.0 487 Request Terminated") || call.public.State != "ended" {
		t.Fatalf("matching CANCEL outcome: cancel=%q invite=%q call=%#v", cancelResponse, inviteResponse, call.public)
	}
}

func TestIncomingBYERequiresDialogAndMonotonicCSeq(t *testing.T) {
	call := &imsCall{
		public: vowifi.Call{ID: "bye-call", State: "active"}, callID: "bye-call",
		remoteTag: "remote", remoteCSeq: 20,
	}
	session := &Session{fromTag: "local-tag", calls: map[string]*imsCall{"bye-call": call}}
	makeBYE := func(remoteTag string, cseq int) *sipRequest {
		packet, err := parseSIPPacket([]byte(strings.Join([]string{
			"BYE sip:user@example.test SIP/2.0",
			"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-bye-" + strconv.Itoa(cseq),
			"From: <tel:+1>;tag=" + remoteTag, "To: <sip:user@example.test>;tag=local-tag",
			"Call-ID: bye-call", "CSeq: " + strconv.Itoa(cseq) + " BYE", "Content-Length: 0", "", "",
		}, "\r\n")))
		if err != nil {
			t.Fatal(err)
		}
		return packet.Request
	}
	var response []byte
	session.handleCallRequest(makeBYE("wrong", 21), func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 481") || call.public.State != "active" {
		t.Fatalf("wrong-dialog BYE changed call: response=%q call=%#v", response, call.public)
	}
	response = nil
	session.handleCallRequest(makeBYE("remote", 20), func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 500") || call.public.State != "active" {
		t.Fatalf("replayed BYE changed call: response=%q call=%#v", response, call.public)
	}
	response = nil
	session.handleCallRequest(makeBYE("remote", 21), func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 200 OK") || call.public.State != "ended" || call.remoteCSeq != 21 {
		t.Fatalf("valid BYE outcome: response=%q call=%#v cseq=%d", response, call.public, call.remoteCSeq)
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
		public:       vowifi.Call{ID: "cancelled", Direction: "outgoing", State: "dialing"},
		callID:       "cancelled",
		responses:    make(chan *sipResponse, 1),
		terminated:   true,
		from:         "<sip:user@example.test>;tag=local",
		to:           "<tel:+1>",
		cseq:         1,
		branch:       "cancelled-branch",
		inviteTarget: "tel:+1",
		inviteTo:     "<tel:+1>",
	}
	connection := &recordingSIPConn{}
	session := &Session{
		calls:          map[string]*imsCall{call.callID: call},
		transactions:   make(map[sipTransactionKey]chan *sipResponse),
		refreshContext: context.Background(),
		conn:           connection,
		transport:      "tcp",
	}
	key := sipTransactionKey{callID: call.callID, cseq: 1, method: "INVITE"}
	go session.watchOutgoingCall(call, key)
	call.responses <- &sipResponse{StatusCode: 487, Reason: "Request Terminated", Headers: map[string][]string{
		"call-id": {"cancelled"}, "cseq": {"1 INVITE"},
		"from": {"<sip:user@example.test>;tag=local"}, "to": {"<tel:+1>;tag=remote"},
	}}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		calls := session.Calls()
		if len(calls) == 1 && calls[0].EndedAt != nil {
			if calls[0].State != "ended" || calls[0].SIPCode != 487 {
				t.Fatalf("cancelled INVITE = %#v", calls[0])
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	if call.public.EndedAt == nil {
		t.Fatal("cancelled INVITE did not reach a terminal state")
	}
	for time.Now().Before(deadline) {
		session.transactionsMu.Lock()
		_, pending := session.transactions[key]
		session.transactionsMu.Unlock()
		if !pending {
			break
		}
		time.Sleep(time.Millisecond)
	}
	writes := connection.captured()
	if len(writes) != 1 || !strings.HasPrefix(string(writes[0]), "ACK tel:+1 SIP/2.0") ||
		!strings.Contains(string(writes[0]), "branch=z9hG4bKcancelled-branch") {
		t.Fatalf("non-2xx ACK = %q", writes)
	}
	session.dispatchPacket(sipPacket{Response: &sipResponse{StatusCode: 487, Reason: "Request Terminated", Headers: map[string][]string{
		"call-id": {"cancelled"}, "cseq": {"1 INVITE"},
		"from": {"<sip:user@example.test>;tag=local"}, "to": {"<tel:+1>;tag=remote"},
	}}}, func([]byte) error { return nil })
	writes = connection.captured()
	if len(writes) != 2 || string(writes[0]) != string(writes[1]) {
		t.Fatalf("retransmitted non-2xx was not re-ACKed: %q", writes)
	}
}

func TestCancelledOutgoingInviteACKsAndBYEsLate2xx(t *testing.T) {
	connection := &recordingSIPConn{}
	call := &imsCall{
		public: vowifi.Call{ID: "late-2xx", Direction: "outgoing", State: "dialing"},
		callID: "late-2xx", target: "tel:+1", inviteTarget: "tel:+1", inviteTo: "<tel:+1>",
		from: "<sip:user@example.test>;tag=local", to: "<tel:+1>", branch: "late", cseq: 4,
		responses: make(chan *sipResponse, 4), terminated: true, lateDialogs: make(map[string]bool),
	}
	session := &Session{
		conn: connection, transport: "tcp", refreshContext: context.Background(), cseq: 10,
		calls: map[string]*imsCall{call.callID: call}, transactions: make(map[sipTransactionKey]chan *sipResponse),
	}
	key := sipTransactionKey{callID: call.callID, cseq: call.cseq, method: "INVITE"}
	session.transactions[key] = call.responses
	go session.watchOutgoingCall(call, key)
	late := &sipResponse{StatusCode: 200, Reason: "OK", Headers: map[string][]string{
		"call-id": {call.callID}, "cseq": {"4 INVITE"},
		"from": {call.from}, "to": {"<tel:+1>;tag=late-remote"},
		"contact": {"<sip:late@192.0.2.20:5060>"},
	}}
	call.responses <- late
	deadline := time.Now().Add(time.Second)
	var writes [][]byte
	for time.Now().Before(deadline) {
		writes = connection.captured()
		if len(writes) >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(writes) < 2 || !strings.HasPrefix(string(writes[0]), "ACK sip:late@192.0.2.20:5060 SIP/2.0") ||
		!strings.HasPrefix(string(writes[1]), "BYE sip:late@192.0.2.20:5060 SIP/2.0") {
		t.Fatalf("late 2xx cleanup requests = %q", writes)
	}
	bye, err := parseSIPPacket(writes[1])
	if err != nil {
		t.Fatal(err)
	}
	byeCSeq, _, err := cseqNumber(bye.Request.value("CSeq"))
	if err != nil {
		t.Fatal(err)
	}
	session.dispatchPacket(sipPacket{Response: &sipResponse{StatusCode: 200, Reason: "OK", Headers: map[string][]string{
		"call-id": {call.callID}, "cseq": {strconv.FormatUint(uint64(byeCSeq), 10) + " BYE"},
	}}}, func([]byte) error { return nil })
	if call.public.State == "active" || call.public.EndedAt == nil {
		t.Fatalf("late 2xx resurrected cancelled call: %#v", call.public)
	}
}

func TestOutgoingCANCELWaitsForProvisionalAndReusesINVITEFields(t *testing.T) {
	connection := &recordingSIPConn{}
	call := &imsCall{
		public: vowifi.Call{ID: "cancel-wait", Direction: "outgoing", State: "dialing"},
		callID: "cancel-wait", target: "sip:mutated@192.0.2.30", to: "<tel:+1>;tag=early",
		from: "<sip:user@example.test>;tag=local", branch: "original-branch", cseq: 8,
		inviteTarget: "tel:+1", inviteTo: "<tel:+1>", inviteRoutes: []string{"<sip:orig.example.test;lr>"},
		inviteProgress: make(chan struct{}),
	}
	session := &Session{
		conn: connection, transport: "tcp", refreshContext: context.Background(),
		calls: map[string]*imsCall{call.callID: call}, transactions: make(map[sipTransactionKey]chan *sipResponse),
	}
	done := make(chan error, 1)
	go func() { done <- session.HangupCall(context.Background(), call.callID) }()
	time.Sleep(20 * time.Millisecond)
	if writes := connection.captured(); len(writes) != 0 {
		t.Fatalf("CANCEL was sent before a provisional response: %q", writes)
	}
	call.signalInviteProgress()
	deadline := time.Now().Add(time.Second)
	var request []byte
	for time.Now().Before(deadline) {
		writes := connection.captured()
		if len(writes) > 0 {
			request = writes[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	text := string(request)
	for _, required := range []string{
		"CANCEL tel:+1 SIP/2.0", "branch=z9hG4bKoriginal-branch", "To: <tel:+1>",
		"Route: <sip:orig.example.test;lr>", "CSeq: 8 CANCEL",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("CANCEL omitted original INVITE field %q:\n%s", required, text)
		}
	}
	session.dispatchPacket(sipPacket{Response: &sipResponse{StatusCode: 200, Headers: map[string][]string{
		"call-id": {call.callID}, "cseq": {"8 CANCEL"},
	}}}, func([]byte) error { return nil })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HangupCall did not complete after CANCEL response")
	}
}

func TestUDPINVITEClientRetransmitsUntilProvisional(t *testing.T) {
	connection := &recordingSIPConn{}
	call := &imsCall{
		public: vowifi.Call{ID: "timer-a", Direction: "outgoing", State: "dialing"}, callID: "timer-a",
		from: "<sip:user@example.test>;tag=local", to: "<tel:+1>", target: "tel:+1",
		inviteTarget: "tel:+1", inviteTo: "<tel:+1>", branch: "timer-a", cseq: 2,
		inviteRequest: []byte("INVITE tel:+1 SIP/2.0\r\n\r\n"), inviteProgress: make(chan struct{}),
		responses: make(chan *sipResponse, 4), lateDialogs: make(map[string]bool),
	}
	session := &Session{
		conn: connection, transport: "udp", refreshContext: context.Background(),
		calls: map[string]*imsCall{call.callID: call}, transactions: make(map[sipTransactionKey]chan *sipResponse),
	}
	key := sipTransactionKey{callID: call.callID, cseq: call.cseq, method: "INVITE"}
	session.transactions[key] = call.responses
	watchDone := make(chan struct{})
	go func() {
		session.watchOutgoingCallWithTimers(call, key, 5*time.Millisecond, 100*time.Millisecond)
		close(watchDone)
	}()
	deadline := time.Now().Add(80 * time.Millisecond)
	before := 0
	for time.Now().Before(deadline) {
		before = len(connection.captured())
		if before >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if before < 2 {
		t.Fatalf("Timer A retransmissions = %d, want at least 2", before)
	}
	call.responses <- &sipResponse{StatusCode: 180, Reason: "Ringing", Headers: map[string][]string{
		"call-id": {call.callID}, "cseq": {"2 INVITE"}, "from": {call.from}, "to": {"<tel:+1>;tag=remote"},
	}}
	deadline = time.Now().Add(80 * time.Millisecond)
	for time.Now().Before(deadline) {
		session.callMu.Lock()
		ringing := call.public.State == "ringing"
		session.callMu.Unlock()
		if ringing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stoppedAt := len(connection.captured())
	time.Sleep(20 * time.Millisecond)
	if after := len(connection.captured()); after != stoppedAt {
		t.Fatalf("Timer A continued after provisional response: before=%d after=%d", stoppedAt, after)
	}
	call.responses <- &sipResponse{StatusCode: 486, Reason: "Busy Here", Headers: map[string][]string{
		"call-id": {call.callID}, "cseq": {"2 INVITE"}, "from": {call.from}, "to": {"<tel:+1>;tag=remote"},
	}}
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("INVITE watcher did not finish after final response")
	}
	session.callMu.Lock()
	result := call.public
	session.callMu.Unlock()
	if result.State != "failed" {
		t.Fatalf("final non-2xx state = %#v", result)
	}
}

func TestUDPNonINVITEClientUsesTimerE(t *testing.T) {
	connection := &recordingSIPConn{}
	session := &Session{
		conn: connection, transport: "udp", transactions: make(map[sipTransactionKey]chan *sipResponse),
	}
	key := sipTransactionKey{callID: "timer-e", cseq: 3, method: "BYE"}
	done := make(chan error, 1)
	go func() {
		_, err := session.exchangeRuntimeWithTimers(context.Background(), []byte("BYE sip:x SIP/2.0\r\n\r\n"), key, 5*time.Millisecond, 100*time.Millisecond)
		done <- err
	}()
	deadline := time.Now().Add(80 * time.Millisecond)
	writes := 0
	for time.Now().Before(deadline) {
		writes = len(connection.captured())
		if writes >= 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if writes < 3 { // initial send plus two Timer E retransmissions
		t.Fatalf("Timer E writes = %d, want at least 3", writes)
	}
	session.dispatchPacket(sipPacket{Response: &sipResponse{StatusCode: 200, Headers: map[string][]string{
		"call-id": {key.callID}, "cseq": {"3 BYE"},
	}}}, func([]byte) error { return nil })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Timer E transaction did not complete")
	}
}

func TestValidCallNumber(t *testing.T) {
	if !validCallNumber("+447700900000") || validCallNumber("12\r\nBYE") {
		t.Fatal("call number validation mismatch")
	}
}

func TestReliable183ConfiguresEarlyMediaAndBuildsPRACK(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	call := &imsCall{
		callID: "reliable@example.test",
		target: "tel:+447700900001",
		from:   "<sip:user@example.test>;tag=local",
		to:     "<tel:+447700900001>",
		cseq:   7,
		media:  media,
	}
	response := &sipResponse{
		StatusCode: 183,
		Reason:     "Session Progress",
		Headers: map[string][]string{
			"to":           {"<tel:+447700900001>;tag=remote"},
			"contact":      {"<sip:callee@192.0.2.20:5060>"},
			"record-route": {"<sip:proxy.example.test;lr>"},
			"require":      {"100rel"},
			"rseq":         {"42"},
		},
		Body: []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 42000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"),
	}
	call.from = "<sip:user@example.test>;tag=local"
	response.Headers["from"] = []string{"<sip:user@example.test>;tag=local"}
	response.Headers["call-id"] = []string{"reliable@example.test"}
	response.Headers["cseq"] = []string{"7 INVITE"}
	reliable, rseq, err := applyProvisionalResponse(call, response)
	if err != nil {
		t.Fatal(err)
	}
	if !reliable || rseq != 42 || !media.ready() || call.remoteTag != "remote" {
		t.Fatalf("provisional state: reliable=%v rseq=%d ready=%v tag=%q", reliable, rseq, media.ready(), call.remoteTag)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	session := &Session{conn: left, transport: "tcp"}
	request := string(session.buildPRACKRequest(call, rseq, 8))
	for _, required := range []string{
		"PRACK sip:callee@192.0.2.20:5060 SIP/2.0",
		"RAck: 42 7 INVITE",
		"CSeq: 8 PRACK",
		"Route: <sip:proxy.example.test;lr>",
	} {
		if !strings.Contains(request, required) {
			t.Fatalf("PRACK omitted %q:\n%s", required, request)
		}
	}
}

func TestReliableProvisionalRejectsRSeqRollbackAndForkedDialog(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	call := &imsCall{
		callID: "early-guard", cseq: 12, from: "<sip:user@example.test>;tag=local",
		to: "<tel:+1>", target: "tel:+1", media: media,
	}
	body := []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 42000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n")
	makeResponse := func(tag string, rseq uint32) *sipResponse {
		return &sipResponse{StatusCode: 183, Headers: map[string][]string{
			"call-id": {call.callID}, "cseq": {"12 INVITE"}, "from": {call.from},
			"to": {"<tel:+1>;tag=" + tag}, "contact": {"<sip:" + tag + "@192.0.2.20>"},
			"require": {"100rel"}, "rseq": {strconv.FormatUint(uint64(rseq), 10)},
		}, Body: append([]byte(nil), body...)}
	}
	if _, _, err := applyProvisionalResponse(call, makeResponse("selected", 50)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := applyProvisionalResponse(call, makeResponse("selected", 49)); err == nil {
		t.Fatal("RSeq rollback was accepted")
	}
	if _, _, err := applyProvisionalResponse(call, makeResponse("fork", 51)); err == nil {
		t.Fatal("forked early dialog overwrote the selected branch")
	}
	if call.remoteTag != "selected" || call.target != "sip:selected@192.0.2.20" || call.lastRSeq != 50 {
		t.Fatalf("early-dialog guard state = tag %q target %q RSeq %d", call.remoteTag, call.target, call.lastRSeq)
	}
	if reliable, rseq, err := applyProvisionalResponse(call, makeResponse("selected", 50)); err != nil || !reliable || rseq != 50 {
		t.Fatalf("retransmitted reliable provisional = reliable %v RSeq %d err %v", reliable, rseq, err)
	}
}

func TestIncomingRequired100relIsAcknowledgedByPRACK(t *testing.T) {
	session := &Session{fromTag: "local-tag", calls: make(map[string]*imsCall)}
	body := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 43000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"
	invite, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-a",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>",
		"Call-ID: reliable-incoming", "CSeq: 9 INVITE",
		"Record-Route: <sip:edge.example.test;lr>",
		"Require: 100rel",
		"Content-Type: application/sdp",
		"Content-Length: " + strconv.Itoa(len(body)), "", body,
	}, "\r\n")))
	if err != nil || invite.Request == nil {
		t.Fatalf("parse INVITE: %v", err)
	}
	var response []byte
	session.handleCallRequest(invite.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	parsed, err := parseSIPResponse(response)
	if err != nil {
		t.Fatalf("parse reliable provisional: %v: %q", err, response)
	}
	if parsed.StatusCode != 183 || !headerHasToken(parsed.values("Require"), "100rel") {
		t.Fatalf("reliable provisional = %q", response)
	}
	if parsed.value("Contact") == "" || parsed.value("Record-Route") != "<sip:edge.example.test;lr>" {
		t.Fatalf("dialog-establishing headers missing: %q", response)
	}
	rseq, err := strconv.ParseUint(parsed.value("RSeq"), 10, 32)
	if err != nil || rseq == 0 {
		t.Fatalf("RSeq = %q: %v", parsed.value("RSeq"), err)
	}
	wrongPRACK, err := parseSIPPacket([]byte(strings.Join([]string{
		"PRACK sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-wrong",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>;tag=local-tag",
		"Call-ID: reliable-incoming", "CSeq: 10 PRACK",
		fmtRAck(uint32(rseq)+1, 9),
		"Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil || wrongPRACK.Request == nil {
		t.Fatalf("parse wrong PRACK: %v", err)
	}
	session.handleCallRequest(wrongPRACK.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 481") || session.calls["reliable-incoming"].prackReceived {
		t.Fatalf("wrong RAck changed reliable state: response=%q call=%#v", response, session.calls["reliable-incoming"])
	}
	prack, err := parseSIPPacket([]byte(strings.Join([]string{
		"PRACK sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-prack",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>;tag=local-tag",
		"Call-ID: reliable-incoming", "CSeq: 10 PRACK",
		fmtRAck(uint32(rseq), 9),
		"Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil || prack.Request == nil {
		t.Fatalf("parse PRACK: %v", err)
	}
	response = nil
	session.handleCallRequest(prack.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 200 OK") {
		t.Fatalf("PRACK response = %q", response)
	}
	if call := session.calls["reliable-incoming"]; call == nil || !call.prackReceived {
		t.Fatalf("incoming reliable provisional was not acknowledged: %#v", call)
	}
	answered, err := session.AnswerCall(context.Background(), "reliable-incoming")
	if err != nil || answered.State != "active" {
		t.Fatalf("answer reliable call: %#v %v", answered, err)
	}
	final, err := parseSIPResponse(response)
	if err != nil || final.StatusCode != 200 || len(final.Body) != 0 || final.value("Contact") == "" ||
		final.value("Record-Route") != "<sip:edge.example.test;lr>" {
		t.Fatalf("final reliable INVITE response repeated SDP or missed dialog headers: %v %q", err, response)
	}
	if call := session.calls["reliable-incoming"]; call != nil && call.media != nil {
		defer call.media.Close()
	}
}

func TestIncomingPRACKAnswerCompletesReliableLocalOffer(t *testing.T) {
	session := &Session{fromTag: "local-tag", calls: make(map[string]*imsCall)}
	invite, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-offerless",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>",
		"Call-ID: prack-offer", "CSeq: 20 INVITE", "Require: 100rel",
		"Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil || invite.Request == nil {
		t.Fatalf("parse offerless INVITE: %v", err)
	}
	var response []byte
	session.handleCallRequest(invite.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	provisional, err := parseSIPResponse(response)
	if err != nil || provisional.StatusCode != 183 || len(provisional.Body) == 0 {
		t.Fatalf("offerless reliable provisional: %v %q", err, response)
	}
	rseq, err := strconv.ParseUint(provisional.value("RSeq"), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	body := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 45000 RTP/AVP 8 110\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:110 telephone-event/8000\r\n"
	prack, err := parseSIPPacket([]byte(strings.Join([]string{
		"PRACK sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-prack-offer",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>;tag=local-tag",
		"Call-ID: prack-offer", "CSeq: 21 PRACK", fmtRAck(uint32(rseq), 20),
		"Content-Type: application/sdp", "Content-Length: " + strconv.Itoa(len(body)), "", body,
	}, "\r\n")))
	if err != nil || prack.Request == nil {
		t.Fatalf("parse offer-bearing PRACK: %v", err)
	}
	response = nil
	session.handleCallRequest(prack.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	answer, err := parseSIPResponse(response)
	if err != nil || answer.StatusCode != 200 || len(answer.Body) != 0 {
		t.Fatalf("PRACK completion response: err=%v response=%q", err, response)
	}
	if call := session.calls["prack-offer"]; call != nil && call.media != nil {
		defer call.media.Close()
	}
}

func TestIncomingPRACKNewOfferReceivesSDPAnswer(t *testing.T) {
	session := &Session{fromTag: "local-tag", calls: make(map[string]*imsCall)}
	inviteBody := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 45100 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"
	invite, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-prack-new-offer",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>",
		"Call-ID: prack-new-offer", "CSeq: 30 INVITE", "Require: 100rel",
		"Content-Type: application/sdp", "Content-Length: " + strconv.Itoa(len(inviteBody)), "", inviteBody,
	}, "\r\n")))
	if err != nil || invite.Request == nil {
		t.Fatalf("parse INVITE offer: %v", err)
	}
	var response []byte
	session.handleCallRequest(invite.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	provisional, err := parseSIPResponse(response)
	if err != nil || provisional.StatusCode != 183 || len(provisional.Body) == 0 {
		t.Fatalf("reliable answer: %v %q", err, response)
	}
	rseq, err := strconv.ParseUint(provisional.value("RSeq"), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	newOffer := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 45200 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	prack, err := parseSIPPacket([]byte(strings.Join([]string{
		"PRACK sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-prack-new-offer-2",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>;tag=local-tag",
		"Call-ID: prack-new-offer", "CSeq: 31 PRACK", fmtRAck(uint32(rseq), 30),
		"Content-Type: application/sdp", "Content-Length: " + strconv.Itoa(len(newOffer)), "", newOffer,
	}, "\r\n")))
	if err != nil || prack.Request == nil {
		t.Fatalf("parse PRACK new offer: %v", err)
	}
	response = nil
	session.handleCallRequest(prack.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	answer, err := parseSIPResponse(response)
	if err != nil || answer.StatusCode != 200 || len(answer.Body) == 0 ||
		!strings.Contains(string(answer.Body), "PCMU/8000") {
		t.Fatalf("PRACK new-offer answer: err=%v response=%q", err, response)
	}
	if call := session.calls["prack-new-offer"]; call != nil && call.media != nil {
		defer call.media.Close()
	}
}

func TestIncomingUPDATEIsExplicitlyRejected(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	call := &imsCall{
		public: vowifi.Call{ID: "update-call", State: "active"},
		callID: "update-call", remoteTag: "a", remoteCSeq: 10,
		media: media,
	}
	session := &Session{fromTag: "local-tag", calls: map[string]*imsCall{"update-call": call}}
	body := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 44000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	update, err := parseSIPPacket([]byte(strings.Join([]string{
		"UPDATE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-update",
		"From: <tel:+1>;tag=a", "To: <sip:user@example.test>;tag=local-tag",
		"Call-ID: update-call", "CSeq: 11 UPDATE",
		"Content-Type: application/sdp",
		"Content-Length: " + strconv.Itoa(len(body)), "", body,
	}, "\r\n")))
	if err != nil || update.Request == nil {
		t.Fatalf("parse UPDATE: %v", err)
	}
	var response []byte
	session.handleCallRequest(update.Request, func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 405 Method Not Allowed") ||
		strings.Contains(string(response), "UPDATE, OPTIONS") {
		t.Fatalf("UPDATE response = %q", response)
	}
	media.mu.RLock()
	remote := media.remote
	media.mu.RUnlock()
	if remote != nil {
		t.Fatalf("rejected UPDATE changed media to %v", remote)
	}
}

func TestSessionSendDTMFRequiresAnActiveNegotiatedCall(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	call := &imsCall{
		public: vowifi.Call{ID: "dtmf-call", State: "ringing"},
		callID: "dtmf-call", media: media,
	}
	session := &Session{calls: map[string]*imsCall{"dtmf-call": call}}
	if err := session.SendDTMF(context.Background(), "dtmf-call", '5', 100*time.Millisecond); !errors.Is(err, ErrCallState) {
		t.Fatalf("ringing-call DTMF error = %v, want ErrCallState", err)
	}
	if err := session.SendDTMF(context.Background(), "missing", '5', 100*time.Millisecond); !errors.Is(err, ErrCallNotFound) {
		t.Fatalf("missing-call DTMF error = %v, want ErrCallNotFound", err)
	}
}

func TestInitialINVITERetransmissionReplaysWithoutReplacingMedia(t *testing.T) {
	session := &Session{fromTag: "local-tag", calls: make(map[string]*imsCall)}
	requestBytes := []byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-retransmit",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
		"Call-ID: retransmit-call", "CSeq: 40 INVITE", "Content-Length: 0", "", "",
	}, "\r\n"))
	packet, err := parseSIPPacket(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	var responses [][]byte
	respond := func(value []byte) error {
		responses = append(responses, append([]byte(nil), value...))
		return nil
	}
	session.handleCallRequest(packet.Request, respond)
	call := session.calls["retransmit-call"]
	if call == nil || call.media == nil {
		t.Fatal("first INVITE did not create a call")
	}
	defer call.media.Close()
	firstMedia := call.media
	session.handleCallRequest(packet.Request, respond)
	if len(responses) != 2 || string(responses[0]) != string(responses[1]) {
		t.Fatalf("INVITE retransmission responses = %q", responses)
	}
	if session.calls["retransmit-call"] != call || call.media != firstMedia {
		t.Fatal("INVITE retransmission replaced the call or leaked its original media")
	}
}

func TestReINVITEUpdatesExistingMediaAndRejectsReplay(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	if err := media.configureRemote([]byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 46000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n")); err != nil {
		t.Fatal(err)
	}
	call := &imsCall{
		public: vowifi.Call{ID: "reinvite", State: "active"}, callID: "reinvite",
		remoteTag: "remote", remoteCSeq: 40, media: media,
	}
	session := &Session{fromTag: "local-tag", calls: map[string]*imsCall{"reinvite": call}}
	body := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 46100 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	packet, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-reinvite",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>;tag=local-tag",
		"Call-ID: reinvite", "CSeq: 41 INVITE", "Content-Type: application/sdp",
		"Content-Length: " + strconv.Itoa(len(body)), "", body,
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	var response []byte
	session.handleCallRequest(packet.Request, func(value []byte) error { response = append([]byte(nil), value...); return nil })
	if !strings.HasPrefix(string(response), "SIP/2.0 200 OK") || session.calls["reinvite"].media != media {
		t.Fatalf("re-INVITE response/call = %q %#v", response, session.calls["reinvite"])
	}
	media.mu.RLock()
	port, codec := media.remote.Port, media.codec
	media.mu.RUnlock()
	if port != 46100 || codec != "PCMU" {
		t.Fatalf("re-INVITE media = %d %s", port, codec)
	}
	response = nil
	session.handleCallRequest(packet.Request, func(value []byte) error { response = append([]byte(nil), value...); return nil })
	if !strings.HasPrefix(string(response), "SIP/2.0 491 Request Pending") || session.calls["reinvite"].media != media {
		t.Fatalf("replayed re-INVITE was not rejected safely: %q", response)
	}
}

func TestUPDATERejectsOutOfOrderOfferAndAllowsBodylessTargetRefresh(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	call := &imsCall{
		public: vowifi.Call{ID: "update-order", State: "active"}, callID: "update-order",
		remoteTag: "remote", remoteCSeq: 50, media: media, offerInProgress: true,
	}
	session := &Session{fromTag: "local-tag", calls: map[string]*imsCall{"update-order": call}}
	makeUPDATE := func(cseq int, body string, contact string) *sipRequest {
		lines := []string{
			"UPDATE sip:user@example.test SIP/2.0",
			"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-update-order",
			"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>;tag=local-tag",
			"Call-ID: update-order", "CSeq: " + strconv.Itoa(cseq) + " UPDATE",
		}
		if contact != "" {
			lines = append(lines, "Contact: <"+contact+">")
		}
		if body != "" {
			lines = append(lines, "Content-Type: application/sdp")
		}
		lines = append(lines, "Content-Length: "+strconv.Itoa(len(body)), "", body)
		packet, parseErr := parseSIPPacket([]byte(strings.Join(lines, "\r\n")))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return packet.Request
	}
	var response []byte
	session.handleCallRequest(makeUPDATE(51, "", "sip:new-target@192.0.2.30"), func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 405 Method Not Allowed") || call.target != "" || !call.offerInProgress {
		t.Fatalf("bodyless target refresh = %q target=%q", response, call.target)
	}
	offer := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 47000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"
	response = nil
	session.handleCallRequest(makeUPDATE(51, offer, ""), func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 405 Method Not Allowed") {
		t.Fatalf("out-of-order UPDATE = %q", response)
	}
	response = nil
	session.handleCallRequest(makeUPDATE(52, offer, ""), func(value []byte) error {
		response = append([]byte(nil), value...)
		return nil
	})
	if !strings.HasPrefix(string(response), "SIP/2.0 405 Method Not Allowed") {
		t.Fatalf("concurrent UPDATE offer = %q", response)
	}
}

func TestReliableProvisionalRetransmitsThenTimesOutWithFinalResponse(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	requestPacket, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-timeout",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
		"Call-ID: reliable-timeout", "CSeq: 60 INVITE", "Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	call := &imsCall{
		public: vowifi.Call{ID: "reliable-timeout", State: "ringing"}, callID: "reliable-timeout",
		invite: requestPacket.Request, media: media,
	}
	session := &Session{fromTag: "local-tag", calls: map[string]*imsCall{"reliable-timeout": call}}
	var mu sync.Mutex
	var responses [][]byte
	respond := func(value []byte) error {
		mu.Lock()
		responses = append(responses, append([]byte(nil), value...))
		mu.Unlock()
		return nil
	}
	session.startReliableProvisional(call, []byte("provisional"), respond, 5*time.Millisecond, 35*time.Millisecond)
	<-call.reliableDone
	mu.Lock()
	captured := append([][]byte(nil), responses...)
	mu.Unlock()
	if len(captured) < 3 || !strings.HasPrefix(string(captured[len(captured)-1]), "SIP/2.0 504 Server Time-out") {
		t.Fatalf("reliable retransmissions/final response = %q", captured)
	}
	calls := session.Calls()
	if len(calls) != 1 || calls[0].State != "failed" || calls[0].EndedAt == nil {
		t.Fatalf("timed-out reliable call = %#v", calls)
	}
}

func TestIncomingFinal2xxRetransmitsUntilMatchingACK(t *testing.T) {
	invite, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-final-ack",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
		"Call-ID: final-ack", "CSeq: 80 INVITE", "Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	call := &imsCall{
		public: vowifi.Call{ID: "final-ack", Direction: "incoming", State: "active"}, callID: "final-ack",
		invite: invite.Request, inviteCSeq: 80, remoteTag: "remote", finalResponseClaimed: true,
		finalResponseCode: 200,
	}
	session := &Session{fromTag: "local-tag", transport: "udp", calls: map[string]*imsCall{call.callID: call}}
	var mu sync.Mutex
	var retransmissions [][]byte
	call.respond = func(value []byte) error {
		mu.Lock()
		retransmissions = append(retransmissions, append([]byte(nil), value...))
		mu.Unlock()
		return nil
	}
	session.startFinalINVITEResponseTimer(call, []byte("final-200"), 5*time.Millisecond, 80*time.Millisecond)
	time.Sleep(12 * time.Millisecond)
	ack, err := parseSIPPacket([]byte(strings.Join([]string{
		"ACK sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-new-ack",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>;tag=local-tag",
		"Call-ID: final-ack", "CSeq: 80 ACK", "Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	session.handleCallRequest(ack.Request, func([]byte) error { return nil })
	<-call.finalDone
	mu.Lock()
	count := len(retransmissions)
	mu.Unlock()
	if count == 0 || !call.finalACKReceived || call.public.State != "active" {
		t.Fatalf("final 2xx ACK state: retransmissions=%d ack=%v call=%#v", count, call.finalACKReceived, call.public)
	}
	time.Sleep(15 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(retransmissions) != count {
		t.Fatalf("final 2xx kept retransmitting after ACK: before=%d after=%d", count, len(retransmissions))
	}
}

func TestIncomingFinal2xxACKTimeoutFailsCall(t *testing.T) {
	call := &imsCall{
		public: vowifi.Call{ID: "final-timeout", Direction: "incoming", State: "active"}, callID: "final-timeout",
		finalResponseClaimed: true, finalResponseCode: 200,
	}
	session := &Session{transport: "tcp", calls: map[string]*imsCall{call.callID: call}}
	call.respond = func([]byte) error { return nil }
	session.startFinalINVITEResponseTimer(call, []byte("final-200"), 5*time.Millisecond, 20*time.Millisecond)
	<-call.finalDone
	if call.public.State != "failed" || call.public.EndedAt == nil || !strings.Contains(call.public.Reason, "ACK") {
		t.Fatalf("unacknowledged final 2xx call = %#v", call.public)
	}
}

func TestNon2xxFinalACKMustMatchINVITETransaction(t *testing.T) {
	invite, err := parseSIPPacket([]byte(strings.Join([]string{
		"INVITE sip:user@example.test SIP/2.0",
		"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-non2xx",
		"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
		"Call-ID: non2xx-ack", "CSeq: 90 INVITE", "Content-Length: 0", "", "",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	call := &imsCall{
		public: vowifi.Call{ID: "non2xx-ack", Direction: "incoming", State: "ended"}, callID: "non2xx-ack",
		invite: invite.Request, inviteCSeq: 90, remoteTag: "remote", finalResponseClaimed: true,
		finalResponseCode: 486,
	}
	session := &Session{fromTag: "local-tag", transport: "udp", calls: map[string]*imsCall{call.callID: call}}
	call.respond = func([]byte) error { return nil }
	session.startFinalINVITEResponseTimer(call, []byte("final-486"), 5*time.Millisecond, 80*time.Millisecond)
	makeACK := func(branch string) *sipRequest {
		packet, parseErr := parseSIPPacket([]byte(strings.Join([]string{
			"ACK sip:user@example.test SIP/2.0",
			"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=" + branch,
			"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>;tag=local-tag",
			"Call-ID: non2xx-ack", "CSeq: 90 ACK", "Content-Length: 0", "", "",
		}, "\r\n")))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return packet.Request
	}
	session.handleCallRequest(makeACK("z9hG4bK-wrong"), func([]byte) error { return nil })
	if call.finalACKReceived {
		t.Fatal("wrong-branch ACK stopped non-2xx server transaction")
	}
	session.handleCallRequest(makeACK("z9hG4bK-non2xx"), func([]byte) error { return nil })
	<-call.finalDone
	if !call.finalACKReceived {
		t.Fatal("matching non-2xx ACK did not stop server transaction")
	}
}

func TestPRACKAndReliableDeadlineHaveOnlyOneOutcome(t *testing.T) {
	delays := []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond}
	for index, delay := range delays {
		callID := "prack-deadline-" + strconv.Itoa(index)
		invite, err := parseSIPPacket([]byte(strings.Join([]string{
			"INVITE sip:user@example.test SIP/2.0",
			"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-" + callID,
			"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>",
			"Call-ID: " + callID, "CSeq: 70 INVITE", "Content-Length: 0", "", "",
		}, "\r\n")))
		if err != nil {
			t.Fatal(err)
		}
		call := &imsCall{
			public: vowifi.Call{ID: callID, State: "ringing"}, callID: callID,
			invite: invite.Request, inviteCSeq: 70, remoteCSeq: 70,
			reliableRSeq: 91, remoteTag: "remote",
		}
		session := &Session{fromTag: "local-tag", calls: map[string]*imsCall{callID: call}}
		var timerMu sync.Mutex
		var timerResponses [][]byte
		session.startReliableProvisional(call, []byte("provisional"), func(value []byte) error {
			timerMu.Lock()
			timerResponses = append(timerResponses, append([]byte(nil), value...))
			timerMu.Unlock()
			return nil
		}, time.Second, 10*time.Millisecond)
		prack, err := parseSIPPacket([]byte(strings.Join([]string{
			"PRACK sip:user@example.test SIP/2.0",
			"Via: SIP/2.0/UDP 192.0.2.10:5060;branch=z9hG4bK-prack-" + callID,
			"From: <tel:+1>;tag=remote", "To: <sip:user@example.test>;tag=local-tag",
			"Call-ID: " + callID, "CSeq: 71 PRACK", fmtRAck(91, 70),
			"Content-Length: 0", "", "",
		}, "\r\n")))
		if err != nil {
			t.Fatal(err)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		var prackResponse []byte
		session.handleCallRequest(prack.Request, func(value []byte) error {
			prackResponse = append([]byte(nil), value...)
			return nil
		})
		<-call.reliableDone
		timerMu.Lock()
		timedOut := false
		for _, value := range timerResponses {
			if strings.HasPrefix(string(value), "SIP/2.0 504") {
				timedOut = true
			}
		}
		timerMu.Unlock()
		prackAccepted := strings.HasPrefix(string(prackResponse), "SIP/2.0 200 OK")
		if prackAccepted == timedOut {
			t.Fatalf("delay %v produced ambiguous reliable outcome: PRACK=%q timer=%q", delay, prackResponse, timerResponses)
		}
		if !prackAccepted && !strings.HasPrefix(string(prackResponse), "SIP/2.0 481") {
			t.Fatalf("delay %v PRACK response = %q", delay, prackResponse)
		}
	}
}

func fmtRAck(rseq, inviteCSeq uint32) string {
	return "RAck: " + strconv.FormatUint(uint64(rseq), 10) + " " + strconv.FormatUint(uint64(inviteCSeq), 10) + " INVITE"
}
