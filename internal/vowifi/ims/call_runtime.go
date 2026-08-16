package ims

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"vocat/internal/vowifi"
)

var (
	ErrCallNotFound = errors.New("ims: call not found")
	ErrCallState    = errors.New("ims: call is not in the required state")
)

const terminalCallRetention = 30 * time.Second

const (
	mmtelServiceURN = "urn:urn-7:3gpp-service.ims.icsi.mmtel"
	mmtelFeatureTag = "urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
)

type imsCall struct {
	public         vowifi.Call
	callID         string
	target         string
	from           string
	to             string
	branch         string
	cseq           uint32
	inviteTarget   string
	invite         *sipRequest
	respond        func([]byte) error
	responses      chan *sipResponse
	remoteTag      string
	routes         []string
	terminated     bool
	media          *rtpMedia
	pracked        map[string]bool
	sessionExpires int
	sessionCancel  context.CancelFunc
}

func (session *Session) Calls() []vowifi.Call {
	session.callMu.Lock()
	defer session.callMu.Unlock()
	now := time.Now().UTC()
	calls := make([]vowifi.Call, 0, len(session.calls))
	for id, call := range session.calls {
		if call.public.EndedAt != nil && now.Sub(*call.public.EndedAt) > terminalCallRetention {
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
	target := callTargetURI(number, session.identity.domain)
	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	routes := append([]string(nil), session.evidence.ServiceRoute...)
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	preferredIdentity := session.callPreferredIdentityLocked()
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
		session.dialogContactHeader(),
		"P-Preferred-Identity: <"+preferredIdentity+">",
		"P-Preferred-Service: "+mmtelServiceURN,
		`Accept-Contact: *;+g.3gpp.icsi-ref="`+mmtelFeatureTag+`"`,
		"P-Access-Network-Info: "+session.pAccessNetworkInfo(),
		"User-Agent: "+session.callUserAgent(),
		"Allow: INVITE, ACK, CANCEL, BYE, OPTIONS, MESSAGE, PRACK, UPDATE, INFO",
		"Supported: 100rel, timer, replaces",
		"Session-Expires: 1800;refresher=uac",
		"Min-SE: 90",
		"Accept: application/sdp",
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
		callID: callID, target: target, inviteTarget: target, from: from, to: to, branch: branch, cseq: cseq, responses: responses,
		routes: routes, media: media, pracked: make(map[string]bool),
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
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	defer func() {
		session.transactionsMu.Lock()
		delete(session.transactions, key)
		session.transactionsMu.Unlock()
	}()
	for {
		select {
		case <-session.refreshContext.Done():
			return
		case <-timer.C:
			if session.callWasTerminated(call.callID) {
				session.finishCall(call.callID, "ended", 0, "")
				return
			}
			session.finishCall(call.callID, "failed", 0, "SIP INVITE transaction timed out")
			return
		case response := <-call.responses:
			if response == nil {
				continue
			}
			diagnostic := callResponseDiagnostic(response)
			session.logCallResponse(response, diagnostic)
			if response.StatusCode < 200 {
				session.setCallDiagnostic(call.callID, response.StatusCode, diagnostic)
				session.updateCallDialogFromResponse(call, response)
				if len(response.Body) > 0 {
					if mediaErr := call.media.configureRemote(response.Body); mediaErr == nil {
						session.setCallMediaReady(call.callID)
						session.setCallState(call.callID, "early_media")
					}
				} else if response.StatusCode >= 180 {
					session.setCallState(call.callID, "ringing")
				}
				if reliableProvisional(response) {
					go session.sendPRACK(call, response)
				}
				continue
			}
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				session.updateCallDialogFromResponse(call, response)
				mediaErr := call.media.configureRemote(response.Body)
				_ = session.sendACK(call)
				if mediaErr != nil {
					session.finishCall(call.callID, "failed", response.StatusCode, mediaErr.Error())
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_ = session.sendDialogRequest(ctx, call, "BYE")
					}()
					return
				}
				session.setCallMediaReady(call.callID)
				session.setCallState(call.callID, "active")
				session.startSessionTimer(call, response.value("Session-Expires"))
			} else if session.callWasTerminated(call.callID) {
				// CANCEL normally causes the pending INVITE transaction to finish
				// with 487 Request Terminated.  It is the expected response to our
				// local hang-up, not a new network rejection.
				session.finishCall(call.callID, "ended", response.StatusCode, diagnostic)
			} else {
				session.finishCall(call.callID, "failed", response.StatusCode, diagnostic)
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
	if call.public.Direction != "incoming" || call.public.State != "ringing" || call.invite == nil || call.respond == nil {
		session.callMu.Unlock()
		return vowifi.Call{}, ErrCallState
	}
	request, respond := call.invite, call.respond
	session.callMu.Unlock()
	response, err := buildSIPResponseWithBody(
		request,
		200,
		session.fromTag,
		call.media.answerSDP(session.localMediaIP()),
		session.dialogContactHeader(),
	)
	if err != nil {
		return vowifi.Call{}, err
	}
	if err := respond(response); err != nil {
		return vowifi.Call{}, err
	}
	session.setCallState(id, "active")
	session.startSessionTimer(call, request.value("Session-Expires"))
	if call.media.ready() {
		session.setCallMediaReady(id)
	}
	session.callMu.Lock()
	result := call.public
	session.callMu.Unlock()
	return result, nil
}

func (session *Session) HangupCall(ctx context.Context, id string) error {
	session.callMu.Lock()
	call := session.calls[id]
	if call == nil {
		session.callMu.Unlock()
		return ErrCallNotFound
	}
	state := call.public.State
	direction := call.public.Direction
	request, respond := call.invite, call.respond
	call.terminated = true
	session.callMu.Unlock()
	if direction == "incoming" && state == "ringing" && request != nil && respond != nil {
		response, err := buildSIPResponseWithBody(request, 486, session.fromTag, nil)
		if err != nil {
			return err
		}
		if err := respond(response); err != nil {
			return err
		}
		session.finishCall(id, "ended", 0, "")
		return nil
	}
	method := "BYE"
	if direction == "outgoing" && (state == "dialing" || state == "ringing" || state == "early_media") {
		method = "CANCEL"
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

func (session *Session) handleCallRequest(request *sipRequest, respond func([]byte) error) bool {
	switch request.Method {
	case "INVITE":
		callID := strings.TrimSpace(request.value("Call-ID"))
		if callID == "" {
			return true
		}
		session.callMu.Lock()
		existing := session.calls[callID]
		session.callMu.Unlock()
		if existing != nil && existing.public.State == "active" {
			return session.handleDialogOffer(request, respond, existing)
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
			callID: callID, target: target, inviteTarget: request.URI, from: request.value("To") + ";tag=" + session.fromTag,
			to: request.value("From"), invite: request, respond: respond, routes: request.values("Record-Route"), media: media,
			pracked: make(map[string]bool),
		}
		session.callMu.Lock()
		session.calls[callID] = call
		session.callMu.Unlock()
		response, err := buildSIPResponseWithBody(request, 180, session.fromTag, nil)
		if err == nil {
			_ = respond(response)
		}
		return true
	case "PRACK":
		response, err := buildSIPResponseWithBody(request, 200, session.fromTag, nil)
		if err == nil {
			_ = respond(response)
		}
		return true
	case "UPDATE":
		callID := strings.TrimSpace(request.value("Call-ID"))
		session.callMu.Lock()
		call := session.calls[callID]
		session.callMu.Unlock()
		if call == nil {
			return false
		}
		return session.handleDialogOffer(request, respond, call)
	case "ACK":
		callID := strings.TrimSpace(request.value("Call-ID"))
		session.callMu.Lock()
		call := session.calls[callID]
		session.callMu.Unlock()
		if call != nil && call.media != nil && !call.media.ready() && len(request.Body) > 0 {
			if err := call.media.configureRemote(request.Body); err != nil {
				session.finishCall(callID, "failed", 0, err.Error())
			} else {
				session.setCallMediaReady(callID)
			}
		}
		return true
	case "CANCEL", "BYE":
		response, err := buildSIPResponseWithBody(request, 200, session.fromTag, nil)
		if err == nil {
			_ = respond(response)
		}
		callID := strings.TrimSpace(request.value("Call-ID"))
		if request.Method == "CANCEL" {
			session.callMu.Lock()
			call := session.calls[callID]
			session.callMu.Unlock()
			if call != nil && call.invite != nil && call.respond != nil {
				if terminated, buildErr := buildSIPResponseWithBody(call.invite, 487, session.fromTag, nil); buildErr == nil {
					_ = call.respond(terminated)
				}
			}
		}
		session.finishCall(callID, "ended", 0, "")
		return true
	default:
		return false
	}
}

func (session *Session) sendACK(call *imsCall) error {
	request := session.buildDialogRequest(call, "ACK", call.cseq)
	session.writeMu.Lock()
	_, err := session.conn.Write(request)
	session.writeMu.Unlock()
	return err
}

func reliableProvisional(response *sipResponse) bool {
	if response == nil || strings.TrimSpace(response.value("RSeq")) == "" {
		return false
	}
	for _, token := range strings.Split(strings.ToLower(response.value("Require")), ",") {
		if strings.TrimSpace(token) == "100rel" {
			return true
		}
	}
	return false
}

func (session *Session) updateCallDialogFromResponse(call *imsCall, response *sipResponse) {
	if call == nil || response == nil {
		return
	}
	session.callMu.Lock()
	defer session.callMu.Unlock()
	call.to = response.value("To")
	call.remoteTag = headerParameter(call.to, "tag")
	if contact := headerURI(response.value("Contact")); contact != "" {
		call.target = contact
	}
	if recordRoutes := response.values("Record-Route"); len(recordRoutes) > 0 {
		call.routes = reverseStrings(recordRoutes)
	}
}

func (session *Session) sendPRACK(call *imsCall, response *sipResponse) {
	rseq := strings.TrimSpace(response.value("RSeq"))
	inviteCSeq := strings.TrimSpace(response.value("CSeq"))
	if rseq == "" || inviteCSeq == "" {
		return
	}
	key := rseq + "|" + inviteCSeq
	session.callMu.Lock()
	if call.pracked == nil {
		call.pracked = make(map[string]bool)
	}
	if call.pracked[key] || call.public.EndedAt != nil {
		session.callMu.Unlock()
		return
	}
	call.pracked[key] = true
	target, to, from := call.target, call.to, call.from
	routes := append([]string(nil), call.routes...)
	session.callMu.Unlock()

	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	session.mu.Unlock()
	branch, _ := randomHex(12)
	lines := []string{
		"PRACK " + target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	session.mu.Lock()
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()
	lines = append(lines, securityHeaders...)
	for _, route := range routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+from, "To: "+to, "Call-ID: "+call.callID,
		fmt.Sprintf("CSeq: %d PRACK", cseq), "RAck: "+rseq+" "+inviteCSeq,
		"P-Access-Network-Info: "+session.pAccessNetworkInfo(),
		"User-Agent: "+session.callUserAgent(),
		"Content-Length: 0", "", "",
	)
	ctx, cancel := context.WithTimeout(session.refreshContext, 10*time.Second)
	defer cancel()
	result, err := session.exchangeRuntime(ctx, []byte(strings.Join(lines, "\r\n")), sipTransactionKey{callID: call.callID, cseq: cseq, method: "PRACK"})
	if err != nil || result.StatusCode < 200 || result.StatusCode >= 300 {
		reason := "reliable provisional response could not be acknowledged"
		if err != nil {
			reason = err.Error()
		}
		session.finishCall(call.callID, "failed", 0, reason)
	}
}

func (session *Session) handleDialogOffer(request *sipRequest, respond func([]byte) error, call *imsCall) bool {
	var body []byte
	if len(request.Body) > 0 {
		if err := call.media.configureRemote(request.Body); err != nil {
			if response, buildErr := buildSIPResponseWithBody(request, 488, session.fromTag, nil); buildErr == nil {
				_ = respond(response)
			}
			return true
		}
		body = call.media.answerSDP(session.localMediaIP())
		session.setCallMediaReady(call.callID)
	}
	extraHeaders := []string(nil)
	if request.Method == "INVITE" {
		extraHeaders = append(extraHeaders, session.dialogContactHeader())
	}
	response, err := buildSIPResponseWithBody(request, 200, session.fromTag, body, extraHeaders...)
	if err == nil {
		_ = respond(response)
		session.startSessionTimer(call, request.value("Session-Expires"))
	}
	return true
}

func (session *Session) startSessionTimer(call *imsCall, header string) {
	value := strings.TrimSpace(strings.Split(header, ";")[0])
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 90 || seconds > 86400 || call == nil {
		return
	}
	ctx, cancel := context.WithCancel(session.refreshContext)
	session.callMu.Lock()
	if call.sessionCancel != nil {
		call.sessionCancel()
	}
	call.sessionExpires = seconds
	call.sessionCancel = cancel
	session.callMu.Unlock()
	go func() {
		interval := time.Duration(seconds) * time.Second / 2
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				refreshContext, cancelRefresh := context.WithTimeout(ctx, 15*time.Second)
				refreshErr := session.sendDialogRequest(refreshContext, call, "UPDATE")
				cancelRefresh()
				if refreshErr != nil {
					session.finishCall(call.callID, "failed", 0, "SIP session refresh failed")
					return
				}
				timer.Reset(interval)
			}
		}
	}()
}

func (session *Session) sendDialogRequest(ctx context.Context, call *imsCall, method string) error {
	cseq := call.cseq
	if method == "BYE" || method == "UPDATE" {
		session.mu.Lock()
		cseq = session.cseq
		session.cseq++
		session.mu.Unlock()
	}
	request := session.buildDialogRequest(call, method, cseq)
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

func (session *Session) buildDialogRequest(call *imsCall, method string, cseq uint32) []byte {
	branch, _ := randomHex(12)
	if method == "CANCEL" {
		branch = call.branch
	}
	target := call.target
	if method == "CANCEL" && call.inviteTarget != "" {
		target = call.inviteTarget
	}
	to := call.to
	if to == "" {
		to = "<" + call.target + ">"
	}
	lines := []string{
		method + " " + target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	session.mu.Lock()
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()
	lines = append(lines, securityHeaders...)
	for _, route := range call.routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+call.from,
		"To: "+to,
		"Call-ID: "+call.callID,
		fmt.Sprintf("CSeq: %d %s", cseq, method),
		"Supported: 100rel, timer",
		"User-Agent: "+session.callUserAgent(),
	)
	if method != "CANCEL" {
		lines = append(lines, "P-Access-Network-Info: "+session.pAccessNetworkInfo())
	}
	if method == "UPDATE" {
		lines = append(lines, session.dialogContactHeader())
	}
	lines = append(lines, "Content-Length: 0", "", "")
	if method == "UPDATE" && call.sessionExpires > 0 {
		lines = append(lines[:len(lines)-3], fmt.Sprintf("Session-Expires: %d;refresher=uac", call.sessionExpires), "Content-Length: 0", "", "")
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func (session *Session) localMediaIP() net.IP {
	var localAddress net.Addr
	if session.conn != nil {
		localAddress = session.conn.LocalAddr()
	}
	return addressIP(localAddress)
}

func buildSIPResponseWithBody(request *sipRequest, status int, tag string, body []byte, extraHeaders ...string) ([]byte, error) {
	reasons := map[int]string{180: "Ringing", 200: "OK", 486: "Busy Here", 487: "Request Terminated", 488: "Not Acceptable Here"}
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
	lines = append(lines, "From: "+from, "To: "+to, "Call-ID: "+callID, "CSeq: "+cseq)
	for _, header := range extraHeaders {
		if strings.TrimSpace(header) != "" {
			lines = append(lines, header)
		}
	}
	if value := strings.TrimSpace(request.value("Session-Expires")); value != "" && status >= 200 && status < 300 && (request.Method == "INVITE" || request.Method == "UPDATE") {
		lines = append(lines, "Supported: timer", "Session-Expires: "+value)
	}
	if len(body) > 0 {
		lines = append(lines, "Content-Type: application/sdp")
	}
	lines = append(lines, "Content-Length: "+strconv.Itoa(len(body)), "", "")
	return append([]byte(strings.Join(lines, "\r\n")), body...), nil
}

func (session *Session) dialogContactHeader() string {
	if session == nil || session.conn == nil || strings.TrimSpace(session.identity.user) == "" {
		return ""
	}
	contact := "Contact: <sip:" + session.identity.user + "@" + session.contactAddress() + ";transport=" + session.transport + ">"
	if strings.TrimSpace(session.instanceID) != "" {
		contact += `;+sip.instance="<` + session.instanceID + `>"`
	}
	return contact + `;audio;+g.3gpp.icsi-ref="` + mmtelFeatureTag + `"`
}

func callTargetURI(number, domain string) string {
	if strings.HasPrefix(number, "+") {
		return "tel:" + number
	}
	return "tel:" + number + ";phone-context=" + strings.TrimSpace(domain)
}

// callPreferredIdentityLocked selects only a number that IMS explicitly
// associated with this registration. It deliberately falls back to the IMPU
// and never derives a telephone number from IMSI digits.
func (session *Session) callPreferredIdentityLocked() string {
	if number, _, ok := vowifi.ExtractAssociatedMSISDN(session.evidence); ok {
		return "tel:" + number
	}
	return session.identity.public
}

func (session *Session) pAccessNetworkInfo() string {
	profile := vowifi.ResolveCarrierProfile(session.request.Identity)
	node := strings.TrimSpace(profile.PANINode)
	if node == "" {
		node = "000000000000"
	}
	value := "IEEE-802.11;i-wlan-node-id=" + node
	if country := strings.ToUpper(strings.TrimSpace(profile.PANICountry)); country != "" {
		value += ";country=" + country
	}
	return value + ";network-provided"
}

func (session *Session) callUserAgent() string {
	if session != nil && session.provider != nil {
		if value := strings.TrimSpace(session.provider.config.UserAgent); value != "" {
			return value
		}
	}
	return "vocat/1"
}

func callResponseDiagnostic(response *sipResponse) string {
	if response == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if reason := safeSIPDiagnostic(response.Reason); reason != "" {
		parts = append(parts, reason)
	}
	for _, name := range []string{"Reason", "Warning"} {
		for _, value := range response.values(name) {
			if value = safeSIPDiagnostic(value); value != "" {
				parts = append(parts, name+": "+value)
			}
		}
	}
	return safeSIPDiagnostic(strings.Join(parts, "; "))
}

func (session *Session) logCallResponse(response *sipResponse, diagnostic string) {
	if session == nil || session.provider == nil || session.provider.config.Logger == nil || response == nil {
		return
	}
	session.provider.config.Logger.Info("IMS call response",
		"carrier_profile", vowifi.ResolveCarrierProfile(session.request.Identity).ID,
		"sip_status", response.StatusCode,
		"diagnostic", diagnostic,
		"content_type", safeSIPDiagnostic(response.value("Content-Type")),
		"body_bytes", len(response.Body),
	)
}

func (session *Session) setCallState(id, state string) {
	session.callMu.Lock()
	if call := session.calls[id]; call != nil {
		call.public.State = state
		if state == "active" && call.public.AnsweredAt == nil {
			now := time.Now().UTC()
			call.public.AnsweredAt = &now
		}
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

func (session *Session) SendDTMF(ctx context.Context, id, digits string) error {
	if len(digits) == 0 || len(digits) > 32 {
		return errors.New("ims: DTMF must contain between 1 and 32 digits")
	}
	session.callMu.Lock()
	call := session.calls[id]
	if call == nil {
		session.callMu.Unlock()
		return ErrCallNotFound
	}
	if call.public.State != "active" || call.media == nil || !call.media.ready() {
		session.callMu.Unlock()
		return ErrCallState
	}
	media := call.media
	session.callMu.Unlock()
	for index := 0; index < len(digits); index++ {
		if err := media.SendDTMF(ctx, digits[index]); err != nil {
			return err
		}
	}
	return nil
}

func (session *Session) finishCall(id, state string, code int, reason string) {
	now := time.Now().UTC()
	var media *rtpMedia
	session.callMu.Lock()
	if call := session.calls[id]; call != nil {
		media = call.media
		call.public.State = state
		if code != 0 {
			call.public.SIPCode = code
		}
		if reason = safeSIPDiagnostic(reason); reason != "" {
			call.public.Reason = reason
		}
		call.public.EndedAt = &now
		if call.sessionCancel != nil {
			call.sessionCancel()
			call.sessionCancel = nil
		}
	}
	session.callMu.Unlock()
	if media != nil {
		_ = media.Close()
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
var _ vowifi.CallDTMFController = (*Session)(nil)
