package ims

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/vowifi"
)

var (
	ErrCallNotFound = errors.New("ims: call not found")
	ErrCallState    = errors.New("ims: call is not in the required state")
)

const (
	terminalCallRetention      = 30 * time.Second
	maxRetainedTerminalCalls   = 256
	sipTransactionT1           = 500 * time.Millisecond
	sipTransactionT2           = 4 * time.Second
	reliableProvisionalT1      = 500 * time.Millisecond
	reliableProvisionalT2      = 4 * time.Second
	reliableProvisionalTimeout = 64 * reliableProvisionalT1
)

type imsCall struct {
	public     vowifi.Call
	callID     string
	target     string
	from       string
	to         string
	branch     string
	cseq       uint32
	invite     *sipRequest
	respond    func([]byte) error
	respondMu  sync.Mutex
	responses  chan *sipResponse
	remoteTag  string
	routes     []string
	terminated bool
	media      *rtpMedia
	pracked    map[uint32]struct{}

	// Outgoing INVITE client transaction and selected early-dialog state.
	inviteRequest   []byte
	inviteTarget    string
	inviteTo        string
	inviteRoutes    []string
	inviteProgress  chan struct{}
	progressOnce    sync.Once
	inviteFinal     bool
	cancelSent      bool
	lastFinalStatus int
	lastRSeq        uint32
	earlySDP        []byte
	lateDialogs     map[string]bool

	// Incoming reliable provisional response state (RFC 3262).
	reliableRSeq       uint32
	reliableLocalOffer bool
	inviteCSeq         uint32
	prackCSeq          uint32
	prackReceived      bool
	prackAnswer        []byte
	omitFinalSDP       bool
	remoteCSeq         uint32
	offerInProgress    bool
	reliableCancel     context.CancelFunc
	reliableDone       chan struct{}
	reliableGeneration uint64
	reliableSendMu     sync.Mutex
	initialResponse    []byte

	// Incoming INVITE final response ownership and ACK timer state.
	finalResponseClaimed bool
	finalResponseCode    int
	finalACKReceived     bool
	finalCancel          context.CancelFunc
	finalDone            chan struct{}
	finalGeneration      uint64
	finalSendMu          sync.Mutex
	retentionScheduled   bool
}

func (call *imsCall) sendResponse(response []byte) error {
	if call == nil || call.respond == nil {
		return errors.New("ims: call response transport is unavailable")
	}
	call.respondMu.Lock()
	defer call.respondMu.Unlock()
	return call.respond(response)
}

func (call *imsCall) signalInviteProgress() {
	if call == nil || call.inviteProgress == nil {
		return
	}
	call.progressOnce.Do(func() { close(call.inviteProgress) })
}

func (session *Session) Calls() []vowifi.Call {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	now := time.Now().UTC()
	calls := make([]vowifi.Call, 0, len(session.calls))
	for id, call := range session.calls {
		if call.public.EndedAt != nil && now.Sub(*call.public.EndedAt) > terminalCallRetention &&
			call.reliableCancel == nil && call.finalCancel == nil {
			delete(session.calls, id)
			continue
		}
		calls = append(calls, call.public)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].StartedAt.Before(calls[j].StartedAt) })
	return calls
}

func (session *Session) DialCall(ctx context.Context, number string) (vowifi.Call, error) {
	number = strings.TrimSpace(number)
	if !validCallNumber(number) {
		return vowifi.Call{}, errors.New("ims: invalid dial number")
	}
	callToken, err := randomHex(18)
	if err != nil {
		return vowifi.Call{}, err
	}
	branch, err := randomHex(12)
	if err != nil {
		return vowifi.Call{}, err
	}
	callID := callToken + "@" + addressHost(session.conn.LocalAddr())
	target := "tel:" + number
	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	routes := append([]string(nil), session.evidence.ServiceRoute...)
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()
	media, err := newRTPMedia(session.localMediaIP())
	if err != nil {
		return vowifi.Call{}, err
	}
	body := media.offerSDP(session.localMediaIP())
	transportUpper := strings.ToUpper(session.transport)
	from := "<" + session.identity.public + ">;tag=" + session.fromTag
	to := "<" + target + ">"
	lines := []string{
		"INVITE " + target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", transportUpper, session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	lines = append(lines, securityHeaders...)
	if len(routes) == 0 {
		lines = append(lines, "Route: <sip:"+session.endpoint.address()+";transport="+session.transport+";lr>")
	} else {
		for _, route := range routes {
			lines = append(lines, "Route: "+route)
		}
	}
	lines = append(lines,
		"From: "+from,
		"To: "+to,
		"Call-ID: "+callID,
		fmt.Sprintf("CSeq: %d INVITE", cseq),
		"Contact: <sip:"+session.identity.user+"@"+session.contactAddress()+";transport="+session.transport+">",
		"P-Preferred-Identity: <"+session.identity.public+">",
		"Allow: INVITE, ACK, CANCEL, BYE, PRACK, OPTIONS, MESSAGE",
		"Supported: 100rel",
		"Content-Type: application/sdp",
		"Content-Length: "+strconv.Itoa(len(body)), "", "",
	)
	request := append([]byte(strings.Join(lines, "\r\n")), body...)
	responses := make(chan *sipResponse, 8)
	key := sipTransactionKey{callID: callID, cseq: cseq, method: "INVITE"}
	session.transactionsMu.Lock()
	if _, duplicate := session.transactions[key]; duplicate {
		session.transactionsMu.Unlock()
		_ = media.Close()
		return vowifi.Call{}, errors.New("ims: duplicate call transaction")
	}
	session.transactions[key] = responses
	session.transactionsMu.Unlock()
	call := &imsCall{
		public: vowifi.Call{ID: callID, Number: number, Direction: "outgoing", State: "dialing", StartedAt: time.Now().UTC()},
		callID: callID, target: target, from: from, to: to, branch: branch, cseq: cseq, responses: responses,
		routes: routes, media: media,
		inviteRequest: append([]byte(nil), request...), inviteTarget: target, inviteTo: to,
		inviteRoutes: append([]string(nil), routes...), inviteProgress: make(chan struct{}),
		lateDialogs: make(map[string]bool),
	}
	session.callMu.Lock()
	session.calls[callID] = call
	session.callMu.Unlock()
	session.writeMu.Lock()
	_, err = session.conn.Write(request)
	session.writeMu.Unlock()
	if err != nil {
		_ = media.Close()
		session.transactionsMu.Lock()
		delete(session.transactions, key)
		session.transactionsMu.Unlock()
		session.callMu.Lock()
		delete(session.calls, callID)
		session.callMu.Unlock()
		return vowifi.Call{}, fmt.Errorf("ims: send SIP INVITE: %w", err)
	}
	go session.watchOutgoingCall(call, key)
	return call.public, nil
}

func (session *Session) watchOutgoingCall(call *imsCall, key sipTransactionKey) {
	session.watchOutgoingCallWithTimers(call, key, sipTransactionT1, session.transactionTimeout())
}

func (session *Session) watchOutgoingCallWithTimers(
	call *imsCall,
	key sipTransactionKey,
	t1 time.Duration,
	timeout time.Duration,
) {
	if t1 <= 0 {
		t1 = sipTransactionT1
	}
	if timeout <= 0 {
		timeout = session.transactionTimeout()
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var retransmit *time.Timer
	var retransmitC <-chan time.Time
	interval := t1
	if session.transport == "udp" {
		retransmit = time.NewTimer(interval)
		retransmitC = retransmit.C
		defer retransmit.Stop()
	}
	provisionalReceived := false
	stopRetransmit := func() {
		if retransmit == nil {
			return
		}
		if !retransmit.Stop() {
			select {
			case <-retransmit.C:
			default:
			}
		}
		retransmitC = nil
	}
	defer func() {
		session.transactionsMu.Lock()
		delete(session.transactions, key)
		session.transactionsMu.Unlock()
	}()
	for {
		select {
		case <-session.refreshContext.Done():
			return
		case <-deadline.C:
			if session.callWasTerminated(call.callID) {
				session.finishCall(call.callID, "ended", 0, "")
				return
			}
			session.finishCall(call.callID, "failed", 0, "SIP INVITE transaction timed out")
			return
		case <-retransmitC:
			if err := session.writeRuntimeRequest(call.inviteRequest); err != nil {
				session.finishCall(call.callID, "failed", 0, "retransmit SIP INVITE: "+err.Error())
				return
			}
			interval *= 2
			if interval > sipTransactionT2 {
				interval = sipTransactionT2
			}
			retransmit.Reset(interval)
		case response := <-call.responses:
			if response == nil {
				continue
			}
			call.signalInviteProgress()
			if _, err := validateOutgoingINVITEResponse(call, response); err != nil {
				session.finishCall(call.callID, "failed", response.StatusCode, err.Error())
				return
			}
			if response.StatusCode < 200 {
				if !provisionalReceived {
					provisionalReceived = true
					stopRetransmit()
				}
				session.callMu.Lock()
				reliable, rseq, provisionalErr := applyProvisionalResponse(call, response)
				sendPRACK := false
				if reliable && provisionalErr == nil {
					if call.pracked == nil {
						call.pracked = make(map[uint32]struct{})
					}
					if _, duplicate := call.pracked[rseq]; !duplicate {
						call.pracked[rseq] = struct{}{}
						sendPRACK = true
					}
				}
				session.callMu.Unlock()
				if provisionalErr != nil {
					session.finishCall(call.callID, "failed", response.StatusCode, provisionalErr.Error())
					return
				}
				session.setCallDiagnostic(call.callID, response.StatusCode, response.Reason)
				if response.StatusCode >= 180 {
					session.setCallState(call.callID, "ringing")
				}
				if call.media != nil && call.media.ready() {
					session.setCallMediaReady(call.callID)
				}
				if sendPRACK {
					if err := session.sendPRACK(session.refreshContext, call, rseq); err != nil {
						session.finishCall(call.callID, "failed", response.StatusCode, err.Error())
						return
					}
				}
				continue
			}
			stopRetransmit()
			session.callMu.Lock()
			call.inviteFinal = true
			call.lastFinalStatus = response.StatusCode
			terminated := call.terminated || call.public.State == "ended" || call.public.State == "failed"
			selectedTag := call.remoteTag
			responseTag := headerParameter(response.value("To"), "tag")
			extraDialog := response.StatusCode < 300 && selectedTag != "" && responseTag != selectedTag
			if response.StatusCode < 300 && !terminated && !extraDialog {
				call.to = response.value("To")
				call.remoteTag = responseTag
				if contact := headerURI(response.value("Contact")); contact != "" {
					call.target = contact
				}
				if len(call.routes) == 0 {
					call.routes = reverseStrings(response.values("Record-Route"))
				}
			}
			session.callMu.Unlock()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if terminated || extraDialog {
					session.terminateUnselected2xxDialog(call, response)
					if terminated {
						session.finishCall(call.callID, "ended", response.StatusCode, response.Reason)
						return
					}
					// Do not let a fork overwrite the selected early dialog. Keep
					// waiting for that branch's final response.
					continue
				}
				var mediaErr error
				if len(response.Body) > 0 {
					session.callMu.Lock()
					earlySDP := append([]byte(nil), call.earlySDP...)
					session.callMu.Unlock()
					if len(earlySDP) > 0 {
						if string(earlySDP) != string(response.Body) {
							mediaErr = errors.New("ims: final INVITE response replaced the selected early-media answer")
						}
					} else {
						mediaErr = call.media.configureRemote(response.Body)
					}
				} else if !call.media.ready() {
					mediaErr = errors.New("ims: final INVITE response omitted SDP before media was negotiated")
				}
				ackErr := session.sendResponseACK(call, response, false)
				if ackErr != nil {
					session.finishCall(call.callID, "failed", response.StatusCode, ackErr.Error())
					return
				}
				if mediaErr != nil {
					session.finishCall(call.callID, "failed", response.StatusCode, mediaErr.Error())
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_ = session.sendDialogRequest(ctx, call, "BYE")
					}()
					return
				}
				mediaReady, codec := call.media.ready(), call.media.Codec()
				session.callMu.Lock()
				becameTerminated := call.terminated || call.public.State == "ended" || call.public.State == "failed"
				if !becameTerminated {
					call.public.MediaReady = mediaReady
					call.public.Codec = codec
					call.public.State = "active"
					call.public.EndedAt = nil
				}
				session.callMu.Unlock()
				if becameTerminated {
					session.terminateUnselected2xxDialog(call, response)
					session.finishCall(call.callID, "ended", response.StatusCode, response.Reason)
				}
			} else {
				if err := session.sendResponseACK(call, response, true); err != nil {
					session.finishCall(call.callID, "failed", response.StatusCode, err.Error())
					return
				}
				if terminated {
					// CANCEL normally causes the pending INVITE transaction to finish
					// with 487 Request Terminated.  It is the expected response to our
					// local hang-up, not a new network rejection.
					session.finishCall(call.callID, "ended", response.StatusCode, response.Reason)
				} else {
					session.finishCall(call.callID, "failed", response.StatusCode, response.Reason)
				}
			}
			return
		}
	}
}

func (session *Session) AnswerCall(_ context.Context, id string) (vowifi.Call, error) {
	session.callMu.Lock()
	call := session.calls[id]
	if call == nil {
		session.callMu.Unlock()
		return vowifi.Call{}, ErrCallNotFound
	}
	if call.public.Direction != "incoming" || call.public.State != "ringing" || call.invite == nil || call.respond == nil ||
		call.terminated || call.finalResponseClaimed || (call.reliableRSeq != 0 && !call.prackReceived) {
		session.callMu.Unlock()
		return vowifi.Call{}, ErrCallState
	}
	call.finalResponseClaimed = true
	call.finalResponseCode = 200
	request := call.invite
	session.callMu.Unlock()
	var body []byte
	if !call.omitFinalSDP {
		body = call.media.answerSDP(session.localMediaIP())
	}
	response, err := buildSIPResponseWithHeaders(request, 200, session.fromTag, body, []string{session.dialogContactHeader()})
	if err != nil {
		session.finishCall(id, "failed", 0, err.Error())
		return vowifi.Call{}, err
	}
	session.callMu.Lock()
	call.initialResponse = append([]byte(nil), response...)
	if call.omitFinalSDP || len(request.Body) > 0 {
		call.offerInProgress = false
	} else {
		call.offerInProgress = true // final 200 carried an offer; ACK must answer.
	}
	call.public.State = "active"
	session.callMu.Unlock()
	if err := call.sendResponse(response); err != nil {
		session.finishCall(id, "failed", 0, err.Error())
		return vowifi.Call{}, err
	}
	session.startFinalINVITEResponseTimer(call, response, sipTransactionT1, 64*sipTransactionT1)
	if call.media.ready() {
		session.setCallMediaReady(id)
	}
	session.callMu.Lock()
	result := call.public
	session.callMu.Unlock()
	return result, nil
}

func (session *Session) HangupCall(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session.callMu.Lock()
	call := session.calls[id]
	if call == nil {
		session.callMu.Unlock()
		return ErrCallNotFound
	}
	state := call.public.State
	direction := call.public.Direction
	request, respond := call.invite, call.respond
	if call.terminated {
		session.callMu.Unlock()
		return ErrCallState
	}
	if direction == "incoming" && state == "ringing" {
		if call.finalResponseClaimed {
			session.callMu.Unlock()
			return ErrCallState
		}
		call.finalResponseClaimed = true
		call.finalResponseCode = 486
	}
	call.terminated = true
	progress := call.inviteProgress
	session.callMu.Unlock()
	if direction == "incoming" && state == "ringing" && request != nil && respond != nil {
		session.stopReliableProvisional(call)
		response, err := buildSIPResponseWithBody(request, 486, session.fromTag, nil)
		if err != nil {
			return err
		}
		session.callMu.Lock()
		call.initialResponse = append([]byte(nil), response...)
		session.callMu.Unlock()
		if err := call.sendResponse(response); err != nil {
			session.finishCall(id, "failed", 0, err.Error())
			return err
		}
		session.finishCall(id, "ended", 0, "")
		session.startFinalINVITEResponseTimer(call, response, sipTransactionT1, 64*sipTransactionT1)
		return nil
	}
	method := "BYE"
	if direction == "outgoing" && (state == "dialing" || state == "ringing") {
		method = "CANCEL"
		if progress != nil {
			select {
			case <-progress:
			case <-ctx.Done():
				session.finishCall(id, "ended", 0, "")
				return ctx.Err()
			case <-session.refreshContext.Done():
				session.finishCall(id, "ended", 0, "")
				return ErrSessionClosed
			}
		}
		session.callMu.Lock()
		if call.inviteFinal {
			session.callMu.Unlock()
			session.finishCall(id, "ended", 0, "")
			return nil
		}
		call.cancelSent = true
		session.callMu.Unlock()
	}
	err := session.sendDialogRequest(ctx, call, method)
	// A remote endpoint may already have removed the dialog and answer BYE with
	// 481. The local call must still leave the active list after a hang-up.
	session.finishCall(id, "ended", 0, "")
	return err
}

func (session *Session) callWasTerminated(id string) bool {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	call := session.calls[id]
	return call != nil && call.terminated
}

func applyProvisionalResponse(call *imsCall, response *sipResponse) (bool, uint32, error) {
	if call == nil || response == nil || response.StatusCode < 100 || response.StatusCode >= 200 {
		return false, 0, errors.New("ims: invalid provisional INVITE response")
	}
	to := strings.TrimSpace(response.value("To"))
	toTag := headerParameter(to, "tag")
	if response.StatusCode > 100 {
		localTag := headerParameter(call.from, "tag")
		if toTag == "" || localTag == "" || headerParameter(response.value("From"), "tag") != localTag {
			return false, 0, errors.New("ims: provisional response does not identify the INVITE dialog")
		}
		if call.remoteTag != "" && call.remoteTag != toTag {
			return false, 0, errors.New("ims: forked early dialogs are unsupported")
		}
	}
	reliable := headerHasToken(response.values("Require"), "100rel") || strings.TrimSpace(response.value("RSeq")) != ""
	var rseq uint32
	duplicateReliable := false
	if reliable {
		encodedRSeq := strings.TrimSpace(response.value("RSeq"))
		parsed, err := strconv.ParseUint(encodedRSeq, 10, 31)
		if err != nil || parsed == 0 {
			return false, 0, errors.New("ims: reliable provisional response omitted a valid RSeq")
		}
		rseq = uint32(parsed)
		if call.lastRSeq != 0 {
			switch {
			case rseq < call.lastRSeq:
				return false, 0, errors.New("ims: reliable provisional response RSeq moved backwards")
			case rseq == call.lastRSeq:
				duplicateReliable = true
			}
		}
	}
	if len(response.Body) > 0 {
		if call.media == nil {
			return false, 0, errors.New("ims: provisional response supplied SDP without a media session")
		}
		if len(call.earlySDP) > 0 {
			if string(call.earlySDP) != string(response.Body) {
				return false, 0, errors.New("ims: provisional response attempted to replace the selected early-media answer")
			}
		} else if !duplicateReliable {
			if err := call.media.configureRemote(response.Body); err != nil {
				return false, 0, err
			}
			call.earlySDP = append([]byte(nil), response.Body...)
		}
	}
	if response.StatusCode > 100 && call.remoteTag == "" {
		call.to = to
		call.remoteTag = toTag
		if contact := headerURI(response.value("Contact")); contact != "" {
			call.target = contact
		}
		if recordRoutes := response.values("Record-Route"); len(recordRoutes) > 0 {
			call.routes = reverseStrings(recordRoutes)
		}
	}
	if reliable && !duplicateReliable {
		call.lastRSeq = rseq
	}
	return reliable, rseq, nil
}

func headerHasToken(values []string, token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	for _, value := range values {
		for _, part := range strings.FieldsFunc(strings.ToLower(value), func(character rune) bool {
			return character == ',' || character == ' ' || character == '\t'
		}) {
			if strings.TrimSpace(part) == token {
				return true
			}
		}
	}
	return false
}

func validateOutgoingINVITEResponse(call *imsCall, response *sipResponse) (string, error) {
	if call == nil || response == nil {
		return "", errors.New("ims: missing outgoing INVITE response state")
	}
	if strings.TrimSpace(response.value("Call-ID")) != call.callID {
		return "", errors.New("ims: INVITE response Call-ID does not match the transaction")
	}
	cseq, method, err := cseqNumber(response.value("CSeq"))
	if err != nil || cseq != call.cseq || method != "INVITE" {
		return "", errors.New("ims: INVITE response CSeq does not match the transaction")
	}
	localTag := headerParameter(call.from, "tag")
	if localTag == "" || headerParameter(response.value("From"), "tag") != localTag {
		return "", errors.New("ims: INVITE response From tag does not match the transaction")
	}
	toTag := headerParameter(response.value("To"), "tag")
	if response.StatusCode > 100 && toTag == "" {
		return "", errors.New("ims: INVITE response omitted the remote dialog tag")
	}
	return toTag, nil
}

func (session *Session) buildResponseDialogRequest(
	call *imsCall,
	response *sipResponse,
	method string,
	cseq uint32,
	non2xxACK bool,
) ([]byte, error) {
	if call == nil || response == nil || session.conn == nil || session.conn.LocalAddr() == nil {
		return nil, errors.New("ims: SIP dialog transport is unavailable")
	}
	session.callMu.Lock()
	inviteTarget := call.inviteTarget
	inviteRoutes := append([]string(nil), call.inviteRoutes...)
	defaultTarget := call.target
	defaultRoutes := append([]string(nil), call.routes...)
	branchValue := call.branch
	from := call.from
	callID := call.callID
	session.callMu.Unlock()
	target := headerURI(response.value("Contact"))
	routes := reverseStrings(response.values("Record-Route"))
	branch, _ := randomHex(12)
	if non2xxACK {
		target = inviteTarget
		routes = inviteRoutes
		branch = branchValue
	}
	if target == "" {
		target = defaultTarget
	}
	if len(routes) == 0 && !non2xxACK {
		routes = defaultRoutes
	}
	if target == "" || branch == "" {
		return nil, errors.New("ims: SIP dialog response omitted a usable remote target")
	}
	lines := []string{
		method + " " + target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	lines = append(lines, runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)...)
	for _, route := range routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+from,
		"To: "+response.value("To"),
		"Call-ID: "+callID,
		fmt.Sprintf("CSeq: %d %s", cseq, method),
		"Content-Length: 0", "", "",
	)
	return []byte(strings.Join(lines, "\r\n")), nil
}

func (session *Session) sendResponseACK(call *imsCall, response *sipResponse, non2xx bool) error {
	request, err := session.buildResponseDialogRequest(call, response, "ACK", call.cseq, non2xx)
	if err != nil {
		return err
	}
	session.writeMu.Lock()
	_, err = session.conn.Write(request)
	session.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("ims: send SIP ACK: %w", err)
	}
	return nil
}

func (session *Session) sendResponseDialogBYE(ctx context.Context, call *imsCall, response *sipResponse) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	session.mu.Unlock()
	request, err := session.buildResponseDialogRequest(call, response, "BYE", cseq, false)
	if err != nil {
		return err
	}
	result, err := session.exchangeRuntime(ctx, request, sipTransactionKey{callID: call.callID, cseq: cseq, method: "BYE"})
	if err != nil {
		return err
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fmt.Errorf("ims: SIP BYE for unselected dialog was rejected with %d", result.StatusCode)
	}
	return nil
}

func (session *Session) terminateUnselected2xxDialog(call *imsCall, response *sipResponse) {
	tag := headerParameter(response.value("To"), "tag")
	if tag == "" {
		return
	}
	session.callMu.Lock()
	if call.lateDialogs == nil {
		call.lateDialogs = make(map[string]bool)
	}
	byeNeeded := !call.lateDialogs[tag]
	if byeNeeded {
		call.lateDialogs[tag] = true
	}
	session.callMu.Unlock()
	if err := session.sendResponseACK(call, response, false); err != nil {
		session.publishFailure(err)
		return
	}
	if !byeNeeded {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), session.transactionTimeout())
		defer cancel()
		if err := session.sendResponseDialogBYE(ctx, call, response); err != nil {
			session.publishFailure(err)
		}
	}()
}

func (session *Session) handleUnmatchedInviteResponse(response *sipResponse) {
	if response == nil {
		return
	}
	callID := strings.TrimSpace(response.value("Call-ID"))
	session.callMu.Lock()
	call := session.calls[callID]
	if call == nil || call.public.Direction != "outgoing" {
		session.callMu.Unlock()
		return
	}
	remoteTag := call.remoteTag
	terminal := call.terminated || call.public.State == "ended" || call.public.State == "failed"
	lastFinalStatus := call.lastFinalStatus
	session.callMu.Unlock()
	if _, err := validateOutgoingINVITEResponse(call, response); err != nil {
		return
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		toTag := headerParameter(response.value("To"), "tag")
		if terminal || remoteTag == "" || toTag != remoteTag {
			session.terminateUnselected2xxDialog(call, response)
			return
		}
		if err := session.sendResponseACK(call, response, false); err != nil {
			session.publishFailure(err)
		}
		return
	}
	if response.StatusCode == lastFinalStatus {
		if err := session.sendResponseACK(call, response, true); err != nil {
			session.publishFailure(err)
		}
	}
}

func (session *Session) sendPRACK(ctx context.Context, call *imsCall, rseq uint32) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	session.mu.Unlock()
	request := session.buildPRACKRequest(call, rseq, cseq)
	response, err := session.exchangeRuntime(ctx, request, sipTransactionKey{
		callID: call.callID,
		cseq:   cseq,
		method: "PRACK",
	})
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ims: SIP PRACK rejected with %d", response.StatusCode)
	}
	return nil
}

func (session *Session) buildPRACKRequest(call *imsCall, rseq, cseq uint32) []byte {
	branch, _ := randomHex(12)
	to := call.to
	if to == "" {
		to = "<" + call.target + ">"
	}
	lines := []string{
		"PRACK " + call.target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	lines = append(lines, runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)...)
	for _, route := range call.routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+call.from,
		"To: "+to,
		"Call-ID: "+call.callID,
		fmt.Sprintf("CSeq: %d PRACK", cseq),
		fmt.Sprintf("RAck: %d %d INVITE", rseq, call.cseq),
		"Content-Length: 0", "", "",
	)
	return []byte(strings.Join(lines, "\r\n"))
}

func (session *Session) startReliableProvisional(
	call *imsCall,
	response []byte,
	respond func([]byte) error,
	t1 time.Duration,
	timeout time.Duration,
) {
	if call == nil || respond == nil || t1 <= 0 || timeout <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	session.callMu.Lock()
	if call.reliableCancel != nil {
		call.reliableCancel()
	}
	call.reliableCancel = cancel
	call.reliableDone = done
	call.reliableGeneration++
	generation := call.reliableGeneration
	session.callMu.Unlock()
	go func() {
		defer close(done)
		interval := t1
		retransmit := time.NewTimer(interval)
		deadline := time.NewTimer(timeout)
		defer retransmit.Stop()
		defer deadline.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				call.reliableSendMu.Lock()
				if !session.consumeReliableProvisional(call, generation) {
					call.reliableSendMu.Unlock()
					return
				}
				var timeoutResponse []byte
				if call.invite != nil {
					timeoutResponse, _ = buildSIPResponseWithBody(call.invite, 504, session.fromTag, nil)
				}
				session.callMu.Lock()
				if !call.finalResponseClaimed {
					call.finalResponseClaimed = true
					call.finalResponseCode = 504
					call.terminated = true
					call.initialResponse = append([]byte(nil), timeoutResponse...)
				}
				session.callMu.Unlock()
				if len(timeoutResponse) > 0 {
					_ = respond(timeoutResponse)
				}
				session.finishCall(call.callID, "failed", 0, "reliable provisional response timed out waiting for PRACK")
				if len(timeoutResponse) > 0 {
					session.startFinalINVITEResponseTimer(call, timeoutResponse, sipTransactionT1, 64*sipTransactionT1)
				}
				// Publish the terminal state before releasing reliableSendMu. A
				// PRACK becoming runnable on the same timer tick must observe either
				// an accepted PRACK or the final 504, never both outcomes.
				call.reliableSendMu.Unlock()
				return
			case <-retransmit.C:
				call.reliableSendMu.Lock()
				if !session.reliableProvisionalPending(call, generation) {
					call.reliableSendMu.Unlock()
					return
				}
				if err := respond(response); err != nil {
					call.reliableSendMu.Unlock()
					session.finishCall(call.callID, "failed", 0, "retransmit reliable provisional response: "+err.Error())
					return
				}
				call.reliableSendMu.Unlock()
				interval *= 2
				if interval > reliableProvisionalT2 {
					interval = reliableProvisionalT2
				}
				retransmit.Reset(interval)
			}
		}
	}()
}

func (session *Session) reliableProvisionalPending(call *imsCall, generation uint64) bool {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	return session.calls[call.callID] == call && !call.prackReceived &&
		call.reliableCancel != nil && call.reliableGeneration == generation
}

func (session *Session) consumeReliableProvisional(call *imsCall, generation uint64) bool {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	if session.calls[call.callID] != call || call.prackReceived ||
		call.reliableCancel == nil || call.reliableGeneration != generation {
		return false
	}
	call.reliableCancel = nil
	call.reliableGeneration++
	return true
}

func (session *Session) stopReliableProvisional(call *imsCall) {
	if call == nil {
		return
	}
	session.callMu.Lock()
	cancel := call.reliableCancel
	call.reliableCancel = nil
	call.reliableGeneration++
	session.callMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (session *Session) startFinalINVITEResponseTimer(
	call *imsCall,
	response []byte,
	t1 time.Duration,
	timeout time.Duration,
) {
	if call == nil || call.respond == nil || t1 <= 0 || timeout <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	session.callMu.Lock()
	call.finalDone = done
	if call.finalACKReceived {
		session.callMu.Unlock()
		cancel()
		close(done)
		return
	}
	if call.finalCancel != nil {
		call.finalCancel()
	}
	call.finalCancel = cancel
	call.finalGeneration++
	generation := call.finalGeneration
	session.callMu.Unlock()
	go func() {
		defer close(done)
		interval := t1
		var retransmit *time.Timer
		var retransmitC <-chan time.Time
		if session.transport == "udp" {
			retransmit = time.NewTimer(interval)
			retransmitC = retransmit.C
			defer retransmit.Stop()
		}
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				call.finalSendMu.Lock()
				if !session.consumeFinalINVITEResponse(call, generation) {
					call.finalSendMu.Unlock()
					return
				}
				session.callMu.Lock()
				code := call.finalResponseCode
				session.callMu.Unlock()
				if code >= 200 && code < 300 {
					session.finishCall(call.callID, "failed", code, "final INVITE response timed out waiting for ACK")
				}
				call.finalSendMu.Unlock()
				return
			case <-retransmitC:
				call.finalSendMu.Lock()
				if !session.finalINVITEResponsePending(call, generation) {
					call.finalSendMu.Unlock()
					return
				}
				if err := call.sendResponse(response); err != nil {
					session.consumeFinalINVITEResponse(call, generation)
					call.finalSendMu.Unlock()
					session.finishCall(call.callID, "failed", 0, "retransmit final INVITE response: "+err.Error())
					return
				}
				call.finalSendMu.Unlock()
				interval *= 2
				if interval > sipTransactionT2 {
					interval = sipTransactionT2
				}
				retransmit.Reset(interval)
			}
		}
	}()
}

func (session *Session) finalINVITEResponsePending(call *imsCall, generation uint64) bool {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	return session.calls[call.callID] == call && !call.finalACKReceived &&
		call.finalCancel != nil && call.finalGeneration == generation
}

func (session *Session) consumeFinalINVITEResponse(call *imsCall, generation uint64) bool {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	if session.calls[call.callID] != call || call.finalACKReceived ||
		call.finalCancel == nil || call.finalGeneration != generation {
		return false
	}
	call.finalCancel = nil
	call.finalGeneration++
	return true
}

func (session *Session) stopFinalINVITEResponse(call *imsCall) {
	if call == nil {
		return
	}
	session.callMu.Lock()
	cancel := call.finalCancel
	call.finalCancel = nil
	call.finalGeneration++
	session.callMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (session *Session) handleReINVITE(request *sipRequest, respond func([]byte) error, call *imsCall) bool {
	status := 481
	var body []byte
	cseq, method, cseqErr := cseqNumber(request.value("CSeq"))
	session.callMu.Lock()
	dialogMatches := requestMatchesCallDialog(request, session.fromTag, call)
	terminal := call.public.State == "ended" || call.public.State == "failed"
	pending := call.offerInProgress || (call.reliableRSeq != 0 && !call.prackReceived)
	if cseqErr == nil && method == "INVITE" && dialogMatches && !terminal {
		switch {
		case cseq <= call.remoteCSeq || pending:
			status = 491
		case len(request.Body) == 0 || call.media == nil:
			call.remoteCSeq = cseq
			status = 488
		default:
			call.remoteCSeq = cseq
			call.offerInProgress = true
			status = 200
		}
	}
	session.callMu.Unlock()
	if status == 200 {
		if err := call.media.configureRemote(request.Body); err != nil {
			status = 488
		} else {
			body = call.media.answerSDP(session.localMediaIP())
		}
		session.callMu.Lock()
		call.offerInProgress = false
		if status == 200 {
			if contact := headerURI(request.value("Contact")); contact != "" {
				call.target = contact
			}
		}
		session.callMu.Unlock()
	}
	extra := []string(nil)
	if status == 200 {
		extra = []string{session.dialogContactHeader()}
	}
	response, err := buildSIPResponseWithHeaders(request, status, session.fromTag, body, extra)
	if err == nil {
		_ = respond(response)
	}
	return true
}

func requestMatchesCallDialog(request *sipRequest, localTag string, call *imsCall) bool {
	if request == nil || call == nil || localTag == "" {
		return false
	}
	remoteTag := call.remoteTag
	if remoteTag == "" {
		remoteTag = headerParameter(call.to, "tag")
	}
	return remoteTag != "" && headerParameter(request.value("To"), "tag") == localTag &&
		headerParameter(request.value("From"), "tag") == remoteTag
}

func (session *Session) handleCallRequest(request *sipRequest, respond func([]byte) error) bool {
	switch request.Method {
	case "INVITE":
		callID := strings.TrimSpace(request.value("Call-ID"))
		if callID == "" {
			return true
		}
		session.callMu.Lock()
		existing := session.calls[callID]
		var replay []byte
		sameInitialTransaction := false
		if existing != nil {
			cseq, method, cseqErr := cseqNumber(request.value("CSeq"))
			if cseqErr == nil && method == "INVITE" && cseq == existing.inviteCSeq &&
				headerParameter(request.value("To"), "tag") == "" &&
				headerParameter(request.value("From"), "tag") == existing.remoteTag {
				sameInitialTransaction = true
				replay = append([]byte(nil), existing.initialResponse...)
			}
		}
		session.callMu.Unlock()
		if existing != nil {
			if sameInitialTransaction {
				if len(replay) > 0 {
					_ = existing.sendResponse(replay)
				}
				return true
			}
			return session.handleReINVITE(request, respond, existing)
		}
		inviteCSeq, inviteMethod, cseqErr := cseqNumber(request.value("CSeq"))
		if cseqErr != nil || inviteMethod != "INVITE" {
			return true
		}
		number := identityNumber(request.value("From"))
		target := headerURI(request.value("Contact"))
		if target == "" {
			target = request.URI
		}
		media, err := newRTPMedia(session.localMediaIP())
		if err != nil {
			if response, buildErr := buildSIPResponseWithBody(request, 488, session.fromTag, nil); buildErr == nil {
				_ = respond(response)
			}
			return true
		}
		if len(request.Body) > 0 {
			if err := media.configureRemote(request.Body); err != nil {
				_ = media.Close()
				if response, buildErr := buildSIPResponseWithBody(request, 488, session.fromTag, nil); buildErr == nil {
					_ = respond(response)
				}
				return true
			}
		}
		call := &imsCall{
			public: vowifi.Call{ID: callID, Number: number, Direction: "incoming", State: "ringing", StartedAt: time.Now().UTC()},
			callID: callID, target: target, from: request.value("To") + ";tag=" + session.fromTag,
			to: request.value("From"), invite: request, respond: respond, routes: request.values("Record-Route"), media: media,
			inviteCSeq: inviteCSeq, remoteCSeq: inviteCSeq,
			remoteTag:       headerParameter(request.value("From"), "tag"),
			offerInProgress: true,
		}
		reliable := headerHasToken(request.values("Require"), "100rel")
		if reliable {
			call.reliableRSeq = uint32(time.Now().UnixNano() & 0x7fffffff)
			if call.reliableRSeq == 0 {
				call.reliableRSeq = 1
			}
		}
		session.callMu.Lock()
		session.calls[callID] = call
		session.callMu.Unlock()
		status := 180
		var provisionalBody []byte
		extraHeaders := []string{session.dialogContactHeader()}
		if reliable {
			extraHeaders = append(extraHeaders,
				"Require: 100rel",
				fmt.Sprintf("RSeq: %d", call.reliableRSeq),
			)
			status = 183
			if len(request.Body) > 0 {
				provisionalBody = media.answerSDP(session.localMediaIP())
			} else {
				// RFC 3262 section 5: when INVITE had no offer, the first
				// reliable provisional response must carry one, and PRACK
				// carries the answer.
				call.reliableLocalOffer = true
				provisionalBody = media.offerSDP(session.localMediaIP())
			}
		}
		response, err := buildSIPResponseWithHeaders(request, status, session.fromTag, provisionalBody, extraHeaders)
		if err == nil {
			session.callMu.Lock()
			call.initialResponse = append([]byte(nil), response...)
			session.callMu.Unlock()
			if sendErr := call.sendResponse(response); sendErr != nil {
				session.finishCall(callID, "failed", 0, "send provisional response: "+sendErr.Error())
			} else if reliable {
				session.startReliableProvisional(call, response, call.sendResponse, reliableProvisionalT1, reliableProvisionalTimeout)
			}
		}
		return true
	case "ACK":
		callID := strings.TrimSpace(request.value("Call-ID"))
		session.callMu.Lock()
		call := session.calls[callID]
		session.callMu.Unlock()
		if call != nil {
			call.finalSendMu.Lock()
			session.callMu.Lock()
			validACK := requestMatchesFinalACK(request, session.fromTag, call)
			pendingOffer := call.offerInProgress
			finalCode := call.finalResponseCode
			if validACK {
				call.finalACKReceived = true
			}
			session.callMu.Unlock()
			if validACK {
				session.stopFinalINVITEResponse(call)
			}
			if validACK && finalCode >= 200 && finalCode < 300 && pendingOffer && call.media != nil {
				if len(request.Body) == 0 {
					session.finishCall(callID, "failed", 0, "ACK omitted the SDP answer to the final INVITE offer")
				} else if err := call.media.configureRemote(request.Body); err != nil {
					session.finishCall(callID, "failed", 0, err.Error())
				} else {
					session.callMu.Lock()
					call.offerInProgress = false
					session.callMu.Unlock()
					session.setCallMediaReady(callID)
				}
			}
			call.finalSendMu.Unlock()
		}
		return true
	case "PRACK":
		callID := strings.TrimSpace(request.value("Call-ID"))
		session.callMu.Lock()
		call := session.calls[callID]
		session.callMu.Unlock()
		status := 481
		var responseBody []byte
		if call != nil {
			call.reliableSendMu.Lock()
			rseq, inviteCSeq, method, rackErr := parseRAck(request.value("RAck"))
			prackCSeq, prackMethod, cseqErr := cseqNumber(request.value("CSeq"))
			session.callMu.Lock()
			dialogMatches := requestMatchesCallDialog(request, session.fromTag, call)
			sequenceMatches := call.prackReceived && prackCSeq == call.prackCSeq ||
				!call.prackReceived && prackCSeq > call.remoteCSeq
			terminal := call.public.State == "ended" || call.public.State == "failed"
			alreadyPRACKed := call.prackReceived
			reliableLocalOffer := call.reliableLocalOffer
			reliableRSeq := call.reliableRSeq
			callInviteCSeq := call.inviteCSeq
			cachedAnswer := append([]byte(nil), call.prackAnswer...)
			media := call.media
			session.callMu.Unlock()
			if rackErr == nil && cseqErr == nil && method == "INVITE" && prackMethod == "PRACK" &&
				rseq == reliableRSeq && inviteCSeq == callInviteCSeq && prackCSeq > callInviteCSeq &&
				dialogMatches && sequenceMatches && !terminal {
				status = 200
				if alreadyPRACKed {
					responseBody = cachedAnswer
				} else if reliableLocalOffer && len(request.Body) == 0 {
					status = 488
				} else if len(request.Body) > 0 && media != nil {
					if err := media.configureRemote(request.Body); err != nil {
						status = 488
					} else if !reliableLocalOffer {
						// The INVITE offer was answered in reliable 1xx, so a
						// PRACK body is a new offer and its answer belongs in 200.
						responseBody = media.answerSDP(session.localMediaIP())
					}
				}
				if status == 200 && !alreadyPRACKed {
					session.callMu.Lock()
					if call.public.State == "ended" || call.public.State == "failed" {
						status = 481
						responseBody = nil
					} else {
						call.prackReceived = true
						call.prackCSeq = prackCSeq
						call.remoteCSeq = prackCSeq
						call.prackAnswer = append([]byte(nil), responseBody...)
						call.omitFinalSDP = true
						call.offerInProgress = false
					}
					session.callMu.Unlock()
					if status == 200 {
						session.stopReliableProvisional(call)
					}
					if status == 200 && media != nil && media.ready() {
						session.setCallMediaReady(callID)
					}
				}
			}
			call.reliableSendMu.Unlock()
		}
		response, err := buildSIPResponseWithBody(request, status, session.fromTag, responseBody)
		if err == nil {
			_ = respond(response)
		}
		return true
	case "UPDATE":
		// VoCat does not yet implement the complete UPDATE target-refresh and
		// offer/answer transaction state machine. Reject it explicitly instead of
		// advertising partial support that can corrupt an outstanding offer.
		response, err := buildSIPResponseWithBody(request, 405, session.fromTag, nil)
		if err == nil {
			_ = respond(response)
		}
		return true
	case "CANCEL":
		callID := strings.TrimSpace(request.value("Call-ID"))
		session.callMu.Lock()
		call := session.calls[callID]
		session.callMu.Unlock()
		status := 481
		responseSent := false
		if call != nil {
			call.reliableSendMu.Lock()
			session.callMu.Lock()
			matching := call.public.Direction == "incoming" && requestMatchesInviteCANCEL(request, call.invite)
			send487 := false
			if matching {
				status = 200
				if call.public.State == "ringing" && !call.finalResponseClaimed {
					call.finalResponseClaimed = true
					call.finalResponseCode = 487
					call.terminated = true
					send487 = true
				}
			}
			session.callMu.Unlock()
			if status == 200 {
				if response, buildErr := buildSIPResponseWithBody(request, 200, session.fromTag, nil); buildErr == nil {
					_ = respond(response)
					responseSent = true
				}
			}
			if send487 {
				session.stopReliableProvisional(call)
				if terminated, buildErr := buildSIPResponseWithBody(call.invite, 487, session.fromTag, nil); buildErr == nil {
					session.callMu.Lock()
					call.initialResponse = append([]byte(nil), terminated...)
					session.callMu.Unlock()
					_ = call.sendResponse(terminated)
					session.finishCall(callID, "ended", 0, "")
					session.startFinalINVITEResponseTimer(call, terminated, sipTransactionT1, 64*sipTransactionT1)
				}
			}
			call.reliableSendMu.Unlock()
		}
		if !responseSent {
			response, err := buildSIPResponseWithBody(request, status, session.fromTag, nil)
			if err == nil {
				_ = respond(response)
			}
		}
		return true
	case "BYE":
		callID := strings.TrimSpace(request.value("Call-ID"))
		cseq, method, cseqErr := cseqNumber(request.value("CSeq"))
		status := 481
		session.callMu.Lock()
		call := session.calls[callID]
		if call != nil && cseqErr == nil && method == "BYE" &&
			requestMatchesCallDialog(request, session.fromTag, call) && call.public.State == "active" && !call.terminated {
			if cseq <= call.remoteCSeq {
				status = 500
			} else {
				call.remoteCSeq = cseq
				call.terminated = true
				status = 200
			}
		}
		session.callMu.Unlock()
		response, err := buildSIPResponseWithBody(request, status, session.fromTag, nil)
		if err == nil {
			_ = respond(response)
		}
		if status == 200 {
			session.finishCall(callID, "ended", 0, "")
		}
		return true
	default:
		return false
	}
}

func requestMatchesInviteCANCEL(cancel *sipRequest, invite *sipRequest) bool {
	if cancel == nil || invite == nil || cancel.Method != "CANCEL" || invite.Method != "INVITE" ||
		strings.TrimSpace(cancel.URI) != strings.TrimSpace(invite.URI) ||
		strings.TrimSpace(cancel.value("Call-ID")) != strings.TrimSpace(invite.value("Call-ID")) ||
		headerURI(cancel.value("From")) != headerURI(invite.value("From")) ||
		headerURI(cancel.value("To")) != headerURI(invite.value("To")) ||
		headerParameter(cancel.value("From"), "tag") != headerParameter(invite.value("From"), "tag") ||
		headerParameter(cancel.value("To"), "tag") != headerParameter(invite.value("To"), "tag") ||
		headerParameter(cancel.value("Via"), "branch") == "" ||
		headerParameter(cancel.value("Via"), "branch") != headerParameter(invite.value("Via"), "branch") {
		return false
	}
	cancelCSeq, cancelMethod, cancelErr := cseqNumber(cancel.value("CSeq"))
	inviteCSeq, inviteMethod, inviteErr := cseqNumber(invite.value("CSeq"))
	return cancelErr == nil && inviteErr == nil && cancelMethod == "CANCEL" && inviteMethod == "INVITE" &&
		cancelCSeq == inviteCSeq
}

func requestMatchesFinalACK(ack *sipRequest, localTag string, call *imsCall) bool {
	if ack == nil || call == nil || !call.finalResponseClaimed || call.finalResponseCode < 200 ||
		!requestMatchesCallDialog(ack, localTag, call) {
		return false
	}
	cseq, method, err := cseqNumber(ack.value("CSeq"))
	if err != nil || method != "ACK" || cseq != call.inviteCSeq {
		return false
	}
	if call.finalResponseCode >= 300 {
		inviteBranch := headerParameter(call.invite.value("Via"), "branch")
		return inviteBranch != "" && headerParameter(ack.value("Via"), "branch") == inviteBranch &&
			strings.TrimSpace(ack.URI) == strings.TrimSpace(call.invite.URI)
	}
	return true
}

func (session *Session) sendACK(call *imsCall) error {
	request := session.buildDialogRequest(call, "ACK", call.cseq)
	session.writeMu.Lock()
	_, err := session.conn.Write(request)
	session.writeMu.Unlock()
	return err
}

func (session *Session) sendDialogRequest(ctx context.Context, call *imsCall, method string) error {
	cseq := call.cseq
	if method == "BYE" {
		session.mu.Lock()
		cseq = session.cseq
		session.cseq++
		session.mu.Unlock()
	}
	var request []byte
	if method == "CANCEL" {
		request = session.buildCANCELRequest(call)
	} else {
		request = session.buildDialogRequest(call, method, cseq)
	}
	if method == "ACK" {
		session.writeMu.Lock()
		_, err := session.conn.Write(request)
		session.writeMu.Unlock()
		return err
	}
	response, err := session.exchangeRuntime(ctx, request, sipTransactionKey{callID: call.callID, cseq: cseq, method: method})
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ims: SIP %s rejected with %d", method, response.StatusCode)
	}
	return nil
}

func (session *Session) buildCANCELRequest(call *imsCall) []byte {
	if call == nil || session.conn == nil || session.conn.LocalAddr() == nil {
		return nil
	}
	target := call.inviteTarget
	if target == "" {
		target = call.target
	}
	to := call.inviteTo
	if to == "" {
		to = call.to
	}
	lines := []string{
		"CANCEL " + target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), call.branch),
		"Max-Forwards: 70",
	}
	lines = append(lines, runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)...)
	for _, route := range call.inviteRoutes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+call.from,
		"To: "+to,
		"Call-ID: "+call.callID,
		fmt.Sprintf("CSeq: %d CANCEL", call.cseq),
		"Content-Length: 0", "", "",
	)
	return []byte(strings.Join(lines, "\r\n"))
}

func (session *Session) buildDialogRequest(call *imsCall, method string, cseq uint32) []byte {
	branch, _ := randomHex(12)
	if method == "CANCEL" {
		branch = call.branch
	}
	to := call.to
	if to == "" {
		to = "<" + call.target + ">"
	}
	lines := []string{
		method + " " + call.target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	lines = append(lines, runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)...)
	for _, route := range call.routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+call.from,
		"To: "+to,
		"Call-ID: "+call.callID,
		fmt.Sprintf("CSeq: %d %s", cseq, method),
		"Content-Length: 0", "", "",
	)
	return []byte(strings.Join(lines, "\r\n"))
}

func parseRAck(value string) (uint32, uint32, string, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 3 {
		return 0, 0, "", errors.New("ims: malformed RAck header")
	}
	rseq, err := strconv.ParseUint(fields[0], 10, 31)
	if err != nil || rseq == 0 {
		return 0, 0, "", errors.New("ims: malformed RAck RSeq")
	}
	cseq, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil {
		return 0, 0, "", errors.New("ims: malformed RAck CSeq")
	}
	return uint32(rseq), uint32(cseq), strings.ToUpper(fields[2]), nil
}

func (session *Session) localMediaIP() net.IP {
	var localAddress net.Addr
	if session.conn != nil {
		localAddress = session.conn.LocalAddr()
	}
	return addressIP(localAddress)
}

func (session *Session) dialogContactHeader() string {
	user := strings.TrimSpace(session.identity.user)
	if user == "" {
		user = "vocat"
	}
	transport := strings.ToLower(strings.TrimSpace(session.transport))
	if transport == "" {
		transport = "udp"
	}
	address := "localhost"
	if session.conn != nil {
		if session.provider != nil {
			address = session.contactAddress()
		} else if session.conn.LocalAddr() != nil {
			address = session.conn.LocalAddr().String()
		}
	}
	return "Contact: <sip:" + user + "@" + address + ";transport=" + transport + ">"
}

func buildSIPResponseWithBody(request *sipRequest, status int, tag string, body []byte) ([]byte, error) {
	return buildSIPResponseWithHeaders(request, status, tag, body, nil)
}

func buildSIPResponseWithHeaders(request *sipRequest, status int, tag string, body []byte, extraHeaders []string) ([]byte, error) {
	reasons := map[int]string{
		180: "Ringing", 183: "Session Progress", 200: "OK",
		405: "Method Not Allowed",
		481: "Call/Transaction Does Not Exist", 486: "Busy Here",
		487: "Request Terminated", 488: "Not Acceptable Here",
		491: "Request Pending", 500: "Server Internal Error",
		504: "Server Time-out",
	}
	reason := reasons[status]
	if reason == "" {
		return nil, errors.New("ims: unsupported call response status")
	}
	via := request.values("Via")
	from, to := request.value("From"), request.value("To")
	callID, cseq := request.value("Call-ID"), request.value("CSeq")
	if len(via) == 0 || from == "" || to == "" || callID == "" || cseq == "" {
		return nil, errors.New("ims: call request omitted a mandatory response header")
	}
	if !strings.Contains(strings.ToLower(to), ";tag=") {
		to += ";tag=" + tag
	}
	lines := []string{fmt.Sprintf("SIP/2.0 %d %s", status, reason)}
	for _, value := range via {
		lines = append(lines, "Via: "+value)
	}
	if request.Method == "INVITE" && status > 100 && status < 300 {
		for _, value := range request.values("Record-Route") {
			lines = append(lines, "Record-Route: "+value)
		}
	}
	lines = append(lines, "From: "+from, "To: "+to, "Call-ID: "+callID, "CSeq: "+cseq)
	lines = append(lines, extraHeaders...)
	if status == 405 {
		lines = append(lines, "Allow: INVITE, ACK, CANCEL, BYE, PRACK, OPTIONS, MESSAGE")
	}
	if len(body) > 0 {
		lines = append(lines, "Content-Type: application/sdp")
	}
	lines = append(lines, "Content-Length: "+strconv.Itoa(len(body)), "", "")
	return append([]byte(strings.Join(lines, "\r\n")), body...), nil
}

func (session *Session) setCallState(id, state string) {
	session.callMu.Lock()
	if call := session.calls[id]; call != nil {
		call.public.State = state
		if state != "ended" && state != "failed" {
			call.public.EndedAt = nil
		}
	}
	session.callMu.Unlock()
}

func (session *Session) setCallDiagnostic(id string, code int, reason string) {
	session.callMu.Lock()
	if call := session.calls[id]; call != nil {
		call.public.SIPCode = code
		call.public.Reason = safeSIPDiagnostic(reason)
	}
	session.callMu.Unlock()
}

func (session *Session) setCallMediaReady(id string) {
	session.callMu.Lock()
	if call := session.calls[id]; call != nil && call.media != nil {
		call.public.MediaReady = call.media.ready()
		call.public.Codec = call.media.Codec()
	}
	session.callMu.Unlock()
}

func (session *Session) CallMedia(_ context.Context, id string) (vowifi.CallMedia, error) {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	call := session.calls[id]
	if call == nil {
		return nil, ErrCallNotFound
	}
	if call.public.State != "active" || call.media == nil || !call.media.ready() {
		return nil, ErrCallState
	}
	return call.media, nil
}

// SendDTMF addresses an active call by its stable Call-ID and emits an RFC
// 4733 telephone-event only when that payload was negotiated in SDP. The
// HTTP/orchestrator layer can expose this optional concrete capability without
// claiming DTMF support for signalling-only sessions.
func (session *Session) SendDTMF(ctx context.Context, id string, digit byte, duration time.Duration) error {
	session.callMu.Lock()
	call := session.calls[id]
	if call == nil {
		session.callMu.Unlock()
		return ErrCallNotFound
	}
	state, media := call.public.State, call.media
	session.callMu.Unlock()
	if state != "active" || media == nil || !media.ready() {
		return ErrCallState
	}
	return media.SendDTMFContext(ctx, digit, duration)
}

func (session *Session) finishCall(id, state string, code int, reason string) {
	now := time.Now().UTC()
	var media *rtpMedia
	var cancelReliable context.CancelFunc
	var scheduleCleanup bool
	var finished *imsCall
	session.callMu.Lock()
	if call := session.calls[id]; call != nil {
		finished = call
		media = call.media
		cancelReliable = call.reliableCancel
		call.reliableCancel = nil
		call.reliableGeneration++
		call.public.State = state
		if code != 0 {
			call.public.SIPCode = code
		}
		if reason = safeSIPDiagnostic(reason); reason != "" {
			call.public.Reason = reason
		}
		call.public.EndedAt = &now
		if !call.retentionScheduled {
			call.retentionScheduled = true
			scheduleCleanup = true
		}
	}
	session.pruneTerminalCallsLocked()
	session.callMu.Unlock()
	if cancelReliable != nil {
		cancelReliable()
	}
	if media != nil {
		_ = media.Close()
	}
	if scheduleCleanup {
		time.AfterFunc(terminalCallRetention, func() { session.removeExpiredTerminalCall(id, finished) })
	}
}

func (session *Session) removeExpiredTerminalCall(id string, expected *imsCall) {
	session.callMu.Lock()
	call := session.calls[id]
	if call != expected || call == nil || call.public.EndedAt == nil {
		session.callMu.Unlock()
		return
	}
	if call.reliableCancel != nil || call.finalCancel != nil || time.Since(*call.public.EndedAt) < terminalCallRetention {
		session.callMu.Unlock()
		time.AfterFunc(time.Second, func() { session.removeExpiredTerminalCall(id, expected) })
		return
	}
	delete(session.calls, id)
	session.callMu.Unlock()
}

func (session *Session) pruneTerminalCallsLocked() {
	type retained struct {
		id   string
		call *imsCall
		at   time.Time
	}
	var terminal []retained
	for id, call := range session.calls {
		if call.public.EndedAt != nil {
			terminal = append(terminal, retained{id: id, call: call, at: *call.public.EndedAt})
		}
	}
	if len(terminal) <= maxRetainedTerminalCalls {
		return
	}
	sort.Slice(terminal, func(left, right int) bool { return terminal[left].at.Before(terminal[right].at) })
	for _, item := range terminal[:len(terminal)-maxRetainedTerminalCalls] {
		if item.call.reliableCancel != nil {
			item.call.reliableCancel()
			item.call.reliableCancel = nil
			item.call.reliableGeneration++
		}
		if item.call.finalCancel != nil {
			item.call.finalCancel()
			item.call.finalCancel = nil
			item.call.finalGeneration++
		}
		delete(session.calls, item.id)
	}
}

func validCallNumber(value string) bool {
	if len(value) < 2 || len(value) > 32 {
		return false
	}
	for index, character := range value {
		if character >= '0' && character <= '9' || index == 0 && character == '+' || character == '*' || character == '#' {
			continue
		}
		return false
	}
	return true
}

func identityNumber(value string) string {
	value = strings.TrimSpace(value)
	if start := strings.Index(value, "<"); start >= 0 {
		if end := strings.Index(value[start:], ">"); end > 0 {
			value = value[start+1 : start+end]
		}
	}
	value = strings.TrimPrefix(value, "sip:")
	value = strings.TrimPrefix(value, "tel:")
	if at := strings.Index(value, "@"); at >= 0 {
		value = value[:at]
	}
	return strings.TrimSpace(value)
}

func headerParameter(value, name string) string {
	needle := ";" + strings.ToLower(name) + "="
	lower := strings.ToLower(value)
	index := strings.Index(lower, needle)
	if index < 0 {
		return ""
	}
	value = value[index+len(needle):]
	if end := strings.IndexAny(value, ";,> \t"); end >= 0 {
		value = value[:end]
	}
	return strings.Trim(value, `"`)
}

func headerURI(value string) string {
	value = strings.TrimSpace(value)
	if start := strings.Index(value, "<"); start >= 0 {
		if end := strings.Index(value[start+1:], ">"); end >= 0 {
			return strings.TrimSpace(value[start+1 : start+1+end])
		}
	}
	if end := strings.Index(value, ";"); end >= 0 {
		value = value[:end]
	}
	if strings.HasPrefix(strings.ToLower(value), "sip:") || strings.HasPrefix(strings.ToLower(value), "tel:") {
		return strings.TrimSpace(value)
	}
	return ""
}

func reverseStrings(values []string) []string {
	result := append([]string(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

var _ vowifi.CallController = (*Session)(nil)
var _ vowifi.CallMediaController = (*Session)(nil)
