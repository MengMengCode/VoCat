package ims

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"vocat/internal/vowifi"
)

// 3GPP TS 24.390: Unstructured Supplementary Service Data (USSD) using IP Multimedia
// (IM) Core Network (CN) subsystem IMS.
const (
	ussiXMLContentType    = "application/vnd.3gpp.ussd+xml"
	ussiLegacyContentType = "application/vnd.3gpp.ussd"
	ussiInfoPackage       = "g.3gpp.ussd"
)

// ussdXMLData matches the 3GPP TS 24.390 subclause 5.1.3 XML schema:
//
//	<ussd-data>
//	    <language>en</language>
//	    <ussd-string>*135#</ussd-string>
//	    <error-code>1</error-code>
//	</ussd-data>
type ussdXMLData struct {
	XMLName   xml.Name `xml:"ussd-data"`
	Language  string   `xml:"language,omitempty"`
	String    string   `xml:"ussd-string,omitempty"`
	ErrorCode *int     `xml:"error-code,omitempty"`
}

func buildUSSDXML(text string, lang string) []byte {
	if lang == "" {
		lang = "en"
	}
	data := ussdXMLData{
		Language: lang,
		String:   text,
	}
	out, err := xml.MarshalIndent(data, "", "    ")
	if err != nil {
		return nil
	}
	return append([]byte(xml.Header), append(out, '\n')...)
}

func parseUSSDXML(data []byte) (text string, lang string, errCode *int, err error) {
	var val ussdXMLData
	if err := xml.Unmarshal(data, &val); err != nil {
		return "", "", nil, err
	}
	return strings.TrimSpace(val.String), strings.TrimSpace(val.Language), val.ErrorCode, nil
}

func extractUSSDPayloadFromSIP(body []byte, contentType string) (text string, lang string, dcs *int, errCode *int, err error) {
	if len(body) == 0 {
		return "", "", nil, nil, nil
	}
	mediaType, params, parseErr := mime.ParseMediaType(strings.TrimSpace(contentType))
	if parseErr == nil {
		if strings.EqualFold(mediaType, ussiXMLContentType) {
			t, l, ec, e := parseUSSDXML(body)
			return t, l, nil, ec, e
		}
		if strings.EqualFold(mediaType, "multipart/mixed") && params["boundary"] != "" {
			mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				part, pErr := mr.NextPart()
				if pErr != nil {
					break
				}
				partBody, _ := io.ReadAll(part)
				partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
				if strings.EqualFold(partType, ussiXMLContentType) {
					t, l, ec, e := parseUSSDXML(partBody)
					return t, l, nil, ec, e
				}
				if strings.EqualFold(partType, ussiLegacyContentType) {
					_, d, t := extractUSSDString(partBody)
					return t, "", d, nil, nil
				}
			}
		}
		if strings.EqualFold(mediaType, ussiLegacyContentType) {
			_, d, t := extractUSSDString(body)
			return t, "", d, nil, nil
		}
	}
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, []byte("<?xml")) || bytes.Contains(trimmed, []byte("<ussd-data")) {
		t, l, ec, e := parseUSSDXML(body)
		if e == nil {
			return t, l, nil, ec, nil
		}
	}
	_, d, t := extractUSSDString(body)
	if t != "" {
		return t, "", d, nil, nil
	}
	return string(body), "", nil, nil, nil
}

type ussiTurnEvent struct {
	method   string // "INFO", "BYE", "CANCEL", "ERROR"
	text     string
	language string
	dcs      *int
	errCode  *int
	err      error
}

type ussiDialog struct {
	callID        string
	target        string // in-dialog Contact URI
	inviteTarget  string
	from          string
	to            string
	branch        string
	cseq          uint32
	routes        []string
	remoteTag     string
	state         string // "active", "awaiting_input", "ended", "failed"
	cancelPending bool   // guarded by ussiMu; retain failed BYE state for cancellation retry
	turns         chan ussiTurnEvent
}

func encodeDialStringForURI(dial string) string {
	var sb strings.Builder
	for _, ch := range dial {
		if ch == '#' {
			sb.WriteString("%23")
		} else {
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

func buildUSSISDP(localIP net.IP) []byte {
	addrType := "IP4"
	if localIP != nil && localIP.To4() == nil {
		addrType = "IP6"
	}
	ipStr := "127.0.0.1"
	if localIP != nil {
		ipStr = localIP.String()
	}
	now := time.Now().Unix()
	sdp := fmt.Sprintf(
		"v=0\r\n"+
			"o=- %d %d IN %s %s\r\n"+
			"s=-\r\n"+
			"c=IN %s %s\r\n"+
			"t=0 0\r\n"+
			"m=audio 0 RTP/AVP 97 96\r\n"+
			"a=rtpmap:97 AMR\r\n"+
			"a=fmtp:97 mode-set=0,2,5,7; maxframes=2\r\n"+
			"a=rtpmap:96 telephone-event\r\n",
		now, now, addrType, ipStr, addrType, ipStr,
	)
	return []byte(sdp)
}

func buildUSSIMultipartBody(sdp []byte, xmlBody []byte, boundary string) []byte {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.SetBoundary(boundary)

	sdpHeader := make(textproto.MIMEHeader)
	sdpHeader.Set("Content-Type", "application/sdp")
	sdpPart, _ := writer.CreatePart(sdpHeader)
	_, _ = sdpPart.Write(sdp)

	xmlHeader := make(textproto.MIMEHeader)
	xmlHeader.Set("Content-Type", ussiXMLContentType)
	xmlHeader.Set("Content-Disposition", "render; handling=optional")
	xmlPart, _ := writer.CreatePart(xmlHeader)
	_, _ = xmlPart.Write(xmlBody)

	_ = writer.Close()
	return buf.Bytes()
}

// sendUSSI is the main entry point from Session.SendUSSI.
func (session *Session) sendUSSI(ctx context.Context, request vowifi.USSISubmitRequest) (vowifi.USSISubmitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session.mu.Lock()
	if session.closed || !session.evidence.Registered {
		session.mu.Unlock()
		return vowifi.USSISubmitResult{}, vowifi.ErrUSSINotReady
	}
	session.mu.Unlock()

	// If continuing an active dialog
	if request.Input != "" && request.Code == "" {
		return session.sendUSSISessionContinue(ctx, request)
	}

	// Starting a new dialog turn
	return session.sendUSSISessionStart(ctx, request)
}

func (session *Session) sendUSSISessionStart(ctx context.Context, request vowifi.USSISubmitRequest) (vowifi.USSISubmitResult, error) {
	rawCode := strings.TrimSpace(firstNonEmpty(request.Code, request.Input))
	if rawCode == "" {
		return vowifi.USSISubmitResult{}, errors.New("ims: USSI payload is empty")
	}

	dialog, response, err := session.sendUSSIInvite(ctx, rawCode)
	if err != nil {
		// If network explicitly returned 415 (Unsupported Media Type) or 405 (Method Not Allowed),
		// fall back to legacy SIP MESSAGE method for backward compatibility.
		if response != nil && (response.StatusCode == 415 || response.StatusCode == 405) {
			session.logInboundSMS(slog.LevelInfo, "IMS USSI INVITE unsupported by network, falling back to SIP MESSAGE", nil,
				"sip_status", response.StatusCode)
			return session.sendUSSIStandaloneMessage(ctx, request)
		}
		result := vowifi.USSISubmitResult{
			SubmissionStatus: "failed",
		}
		if response != nil {
			result.SIPCode = response.StatusCode
			result.SubmissionStatus = "rejected_by_ims"
		}
		return result, err
	}

	// Dialog established (200 OK received and ACK sent). Now wait for the first
	// INFO or BYE turn from the network.
	timeout := 15 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		go func() {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = session.CancelUSSI(cancelCtx, dialog.callID)
		}()
		return vowifi.USSISubmitResult{
			SessionID:        dialog.callID,
			SubmissionStatus: "failed",
			Status:           "failed",
			SIPCode:          200,
		}, ctx.Err()

	case <-timer.C:
		go func() {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = session.CancelUSSI(cancelCtx, dialog.callID)
		}()
		return vowifi.USSISubmitResult{
			SessionID:        dialog.callID,
			SubmissionStatus: "failed",
			Status:           "failed",
			SIPCode:          200,
		}, errors.New("ims: timed out waiting for network USSI response")

	case event := <-dialog.turns:
		if event.err != nil {
			session.ussiMu.Lock()
			delete(session.ussiDialogs, dialog.callID)
			session.ussiMu.Unlock()
			return vowifi.USSISubmitResult{
				SessionID:        dialog.callID,
				SubmissionStatus: "failed",
				Status:           "failed",
				SIPCode:          200,
			}, event.err
		}

		if event.errCode != nil && *event.errCode != 0 {
			session.ussiMu.Lock()
			delete(session.ussiDialogs, dialog.callID)
			session.ussiMu.Unlock()
			return vowifi.USSISubmitResult{
				SessionID:        dialog.callID,
				SubmissionStatus: "rejected_by_ims",
				Status:           "failed",
				SIPCode:          200,
			}, fmt.Errorf("ims: USSI returned error code %d", *event.errCode)
		}

		if event.method == "BYE" {
			// Network ended session immediately (e.g. single-turn query like balance)
			session.ussiMu.Lock()
			delete(session.ussiDialogs, dialog.callID)
			session.ussiMu.Unlock()
			return vowifi.USSISubmitResult{
				SessionID:        dialog.callID,
				Status:           "final",
				Text:             event.text,
				DCS:              event.dcs,
				Continueable:     false,
				SIPCode:          200,
				SubmissionStatus: "accepted_by_ims",
			}, nil
		}

		// Network sent INFO -> expecting user input (multi-turn)
		dialog.state = "awaiting_input"
		return vowifi.USSISubmitResult{
			SessionID:        dialog.callID,
			Status:           "awaiting_input",
			Text:             event.text,
			DCS:              event.dcs,
			Continueable:     true,
			SIPCode:          200,
			SubmissionStatus: "accepted_by_ims",
		}, nil
	}
}

func (session *Session) sendUSSISessionContinue(ctx context.Context, request vowifi.USSISubmitRequest) (vowifi.USSISubmitResult, error) {
	input := strings.TrimSpace(request.Input)
	if input == "" {
		return vowifi.USSISubmitResult{}, errors.New("ims: USSI continue input is empty")
	}

	session.ussiMu.Lock()
	var dialog *ussiDialog
	if request.SessionID != "" {
		dialog = session.ussiDialogs[request.SessionID]
	}
	if request.SessionID == "" && len(session.ussiDialogs) == 1 {
		for _, d := range session.ussiDialogs {
			dialog = d
			break
		}
	}
	cancelPending := dialog != nil && dialog.cancelPending
	session.ussiMu.Unlock()

	if dialog == nil {
		return vowifi.USSISubmitResult{}, vowifi.ErrUSSISessionNotFound
	}

	if cancelPending {
		return vowifi.USSISubmitResult{}, errors.New("ims: USSI cancellation pending; retry cancellation instead of sending input")
	}
	if err := session.sendUSSIInfo(ctx, dialog, input); err != nil {
		return vowifi.USSISubmitResult{
			SessionID:        dialog.callID,
			SubmissionStatus: "failed",
			Status:           "failed",
		}, err
	}

	timeout := 15 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return vowifi.USSISubmitResult{
			SessionID:        dialog.callID,
			SubmissionStatus: "failed",
			Status:           "failed",
		}, ctx.Err()

	case <-timer.C:
		return vowifi.USSISubmitResult{
			SessionID:        dialog.callID,
			SubmissionStatus: "failed",
			Status:           "failed",
		}, errors.New("ims: timed out waiting for network USSI response")

	case event := <-dialog.turns:
		if event.err != nil {
			session.ussiMu.Lock()
			delete(session.ussiDialogs, dialog.callID)
			session.ussiMu.Unlock()
			return vowifi.USSISubmitResult{
				SessionID:        dialog.callID,
				SubmissionStatus: "failed",
				Status:           "failed",
			}, event.err
		}

		if event.errCode != nil && *event.errCode != 0 {
			session.ussiMu.Lock()
			delete(session.ussiDialogs, dialog.callID)
			session.ussiMu.Unlock()
			return vowifi.USSISubmitResult{
				SessionID:        dialog.callID,
				SubmissionStatus: "rejected_by_ims",
				Status:           "failed",
			}, fmt.Errorf("ims: USSI returned error code %d", *event.errCode)
		}

		if event.method == "BYE" {
			session.ussiMu.Lock()
			delete(session.ussiDialogs, dialog.callID)
			session.ussiMu.Unlock()
			return vowifi.USSISubmitResult{
				SessionID:        dialog.callID,
				Status:           "final",
				Text:             event.text,
				DCS:              event.dcs,
				Continueable:     false,
				SIPCode:          200,
				SubmissionStatus: "accepted_by_ims",
			}, nil
		}

		dialog.state = "awaiting_input"
		return vowifi.USSISubmitResult{
			SessionID:        dialog.callID,
			Status:           "awaiting_input",
			Text:             event.text,
			DCS:              event.dcs,
			Continueable:     true,
			SIPCode:          200,
			SubmissionStatus: "accepted_by_ims",
		}, nil
	}
}

func (session *Session) sendUSSIInvite(ctx context.Context, dialstring string) (*ussiDialog, *sipResponse, error) {
	callToken, err := randomHex(18)
	if err != nil {
		return nil, nil, err
	}
	branch, err := randomHex(12)
	if err != nil {
		return nil, nil, err
	}
	fromTag, err := randomHex(8)
	if err != nil {
		return nil, nil, err
	}
	boundary, err := randomHex(16)
	if err != nil {
		return nil, nil, err
	}

	callID := callToken + "@" + addressHost(session.conn.LocalAddr())
	domain := session.identity.domain
	if domain == "" {
		domain = "ims.mnc001.mcc001.3gppnetwork.org"
	}

	encodedDial := encodeDialStringForURI(dialstring)
	// TS 24.390 Table A.1-1:
	// Request-URI: sip:<dialstring>;phone-context=<domain>@<domain>;user=dialstring
	requestURI := fmt.Sprintf("sip:%s;phone-context=%s@%s;user=dialstring", encodedDial, domain, domain)
	toURI := fmt.Sprintf("<sip:%s;phone-context=%s@%s;user=dialstring>", encodedDial, domain, domain)
	from := "<" + session.identity.public + ">;tag=" + fromTag

	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	routes := append([]string(nil), session.evidence.ServiceRoute...)
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()

	sdp := buildUSSISDP(session.localMediaIP())
	xmlBody := buildUSSDXML(dialstring, "en")
	multipartBody := buildUSSIMultipartBody(sdp, xmlBody, boundary)

	transportUpper := strings.ToUpper(session.transport)
	lines := []string{
		"INVITE " + requestURI + " SIP/2.0",
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
		"To: "+toURI,
		"Call-ID: "+callID,
		fmt.Sprintf("CSeq: %d INVITE", cseq),
		session.dialogContactHeader(),
		"P-Preferred-Identity: <"+session.identity.public+">",
		"P-Preferred-Service: "+mmtelServiceURN,
		`Accept-Contact: *;+g.3gpp.icsi-ref="`+mmtelFeatureTag+`"`,
	)
	lines = session.appendPAccessNetworkInfoHeader(lines)
	lines = append(lines,
		"User-Agent: "+session.imsUserAgent(),
		"Allow: INVITE, ACK, CANCEL, BYE, PRACK, UPDATE, REFER, MESSAGE, INFO",
		"Supported: 100rel, timer",
		"Accept: application/sdp, application/vnd.3gpp.ussd+xml, multipart/mixed",
		"Recv-Info: "+ussiInfoPackage,
		fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s", boundary),
		"Content-Length: "+strconv.Itoa(len(multipartBody)), "", "",
	)
	requestBytes := append([]byte(strings.Join(lines, "\r\n")), multipartBody...)

	dialog := &ussiDialog{
		callID:       callID,
		inviteTarget: requestURI,
		target:       requestURI,
		from:         from,
		to:           toURI,
		branch:       branch,
		cseq:         cseq,
		routes:       routes,
		state:        "invited",
		turns:        make(chan ussiTurnEvent, 8),
	}

	session.ussiMu.Lock()
	session.ussiDialogs[callID] = dialog
	session.ussiMu.Unlock()

	key := sipTransactionKey{callID: callID, cseq: cseq, method: "INVITE"}
	response, err := session.exchangeRuntime(ctx, requestBytes, key)
	if err != nil {
		session.ussiMu.Lock()
		delete(session.ussiDialogs, callID)
		session.ussiMu.Unlock()
		return nil, response, fmt.Errorf("ims: send USSI INVITE: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = session.sendRejectedUSSIInviteACK(dialog, response)
		session.ussiMu.Lock()
		delete(session.ussiDialogs, callID)
		session.ussiMu.Unlock()
		return nil, response, fmt.Errorf("ims: USSI rejected with SIP %d", response.StatusCode)
	}

	// Update dialog from 200 OK
	remoteTag := headerParameter(response.value("To"), "tag")
	if remoteTag != "" {
		dialog.remoteTag = remoteTag
		dialog.to = response.value("To")
	}
	if contact := headerURI(response.value("Contact")); contact != "" {
		dialog.target = contact
	}
	if recordRoutes := response.values("Record-Route"); len(recordRoutes) > 0 {
		dialog.routes = reverseStrings(recordRoutes)
	}
	dialog.state = "active"

	// Send 2xx ACK
	if ackErr := session.sendUSSIACK(dialog); ackErr != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS USSI ACK failed", nil, "error", ackErr)
	}

	return dialog, response, nil
}

func (session *Session) sendUSSIACK(dialog *ussiDialog) error {
	branch, _ := randomHex(12)
	lines := []string{
		"ACK " + dialog.target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	session.mu.Lock()
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()
	lines = append(lines, securityHeaders...)
	for _, route := range dialog.routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+dialog.from,
		"To: "+dialog.to,
		"Call-ID: "+dialog.callID,
		fmt.Sprintf("CSeq: %d ACK", dialog.cseq),
		"User-Agent: "+session.imsUserAgent(),
		"Content-Length: 0", "", "",
	)
	session.writeMu.Lock()
	_, err := session.conn.Write([]byte(strings.Join(lines, "\r\n")))
	session.writeMu.Unlock()
	return err
}

func (session *Session) sendRejectedUSSIInviteACK(dialog *ussiDialog, response *sipResponse) error {
	if dialog == nil || response == nil || response.StatusCode < 300 {
		return nil
	}
	if session == nil || session.conn == nil {
		return errors.New("ims: SIP connection unavailable for rejected USSI INVITE ACK")
	}
	target := dialog.inviteTarget
	if target == "" {
		target = dialog.target
	}
	to := strings.TrimSpace(response.value("To"))
	if to == "" {
		to = dialog.to
	}
	lines := []string{
		"ACK " + target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), dialog.branch),
		"Max-Forwards: 70",
	}
	session.mu.Lock()
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()
	lines = append(lines, securityHeaders...)
	for _, route := range dialog.routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+dialog.from,
		"To: "+to,
		"Call-ID: "+dialog.callID,
		fmt.Sprintf("CSeq: %d ACK", dialog.cseq),
		"User-Agent: "+session.imsUserAgent(),
		"Content-Length: 0", "", "",
	)
	session.writeMu.Lock()
	_, err := session.conn.Write([]byte(strings.Join(lines, "\r\n")))
	session.writeMu.Unlock()
	return err
}

func (session *Session) sendUSSIInfo(ctx context.Context, dialog *ussiDialog, input string) error {
	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	session.mu.Unlock()
	dialog.cseq = cseq

	xmlBody := buildUSSDXML(input, "en")
	branch, _ := randomHex(12)
	lines := []string{
		"INFO " + dialog.target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	session.mu.Lock()
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()
	lines = append(lines, securityHeaders...)
	for _, route := range dialog.routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+dialog.from,
		"To: "+dialog.to,
		"Call-ID: "+dialog.callID,
		fmt.Sprintf("CSeq: %d INFO", cseq),
		"Info-Package: "+ussiInfoPackage,
		"Content-Type: "+ussiXMLContentType,
		"Content-Disposition: info-package",
		"User-Agent: "+session.imsUserAgent(),
		"Content-Length: "+strconv.Itoa(len(xmlBody)), "", "",
	)
	reqBytes := append([]byte(strings.Join(lines, "\r\n")), xmlBody...)
	resp, err := session.exchangeRuntime(ctx, reqBytes, sipTransactionKey{callID: dialog.callID, cseq: cseq, method: "INFO"})
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ims: USSI INFO rejected with SIP %d", resp.StatusCode)
	}
	return nil
}

func (session *Session) sendUSSIBye(ctx context.Context, dialog *ussiDialog) error {
	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	session.mu.Unlock()
	dialog.cseq = cseq

	branch, _ := randomHex(12)
	lines := []string{
		"BYE " + dialog.target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", strings.ToUpper(session.transport), session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	session.mu.Lock()
	securityHeaders := runtimeSecurityHeaders(session.securityActive, session.securityAgreement.verifyValue)
	session.mu.Unlock()
	lines = append(lines, securityHeaders...)
	for _, route := range dialog.routes {
		lines = append(lines, "Route: "+route)
	}
	lines = append(lines,
		"From: "+dialog.from,
		"To: "+dialog.to,
		"Call-ID: "+dialog.callID,
		fmt.Sprintf("CSeq: %d BYE", cseq),
		"User-Agent: "+session.imsUserAgent(),
		"Content-Length: 0", "", "",
	)
	reqBytes := []byte(strings.Join(lines, "\r\n"))
	resp, err := session.exchangeRuntime(ctx, reqBytes, sipTransactionKey{callID: dialog.callID, cseq: cseq, method: "BYE"})
	if err != nil {
		return err
	}
	// 481 confirms that the remote dialog is already gone, including when a
	// previous BYE succeeded but its response was lost (RFC 3261, 15.1.1).
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && resp.StatusCode != 481 {
		return fmt.Errorf("ims: USSI BYE rejected with SIP %d", resp.StatusCode)
	}
	return nil
}

// CancelUSSI aborts an active USSI dialog by sending BYE.
func (session *Session) CancelUSSI(ctx context.Context, sessionID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	session.ussiMu.Lock()
	var dialog *ussiDialog
	if sessionID != "" {
		dialog = session.ussiDialogs[sessionID]
	} else if len(session.ussiDialogs) == 1 {
		for _, d := range session.ussiDialogs {
			dialog = d
		}
	}
	if dialog != nil {
		dialog.cancelPending = true
	}
	session.ussiMu.Unlock()
	if dialog == nil {
		return vowifi.ErrUSSISessionNotFound
	}
	// RFC 3261 section 15.1.1: once BYE is submitted, do not resume the
	// application session. Retain its cancellation record on failure so the
	// caller sees the error and can retry cancelling this exact dialog.
	if err := session.sendUSSIBye(ctx, dialog); err != nil {
		return err
	}
	session.ussiMu.Lock()
	delete(session.ussiDialogs, dialog.callID)
	session.ussiMu.Unlock()
	return nil
}

// handleUSSIRequest processes incoming SIP INFO, BYE, and CANCEL requests for USSI.
func (session *Session) handleUSSIRequest(request *sipRequest, respond func([]byte) error) bool {
	callID := strings.TrimSpace(request.value("Call-ID"))
	session.ussiMu.Lock()
	dialog := session.ussiDialogs[callID]
	session.ussiMu.Unlock()

	switch request.Method {
	case "INFO":
		infoPkg := strings.TrimSpace(request.value("Info-Package"))
		isUSSI := dialog != nil || infoPkg == ussiInfoPackage || strings.Contains(strings.ToLower(request.value("Content-Type")), "ussd")
		if !isUSSI {
			return false
		}
		// TS 24.390 §5.1.2.1: receiver of INFO responds with 200 OK without MIME body
		response, err := buildSIPResponse(request, 200, session.fromTag)
		if err == nil {
			_ = respond(response)
		}
		text, lang, dcs, errCode, _ := extractUSSDPayloadFromSIP(request.Body, request.value("Content-Type"))
		if dialog != nil {
			select {
			case dialog.turns <- ussiTurnEvent{
				method:   "INFO",
				text:     text,
				language: lang,
				dcs:      dcs,
				errCode:  errCode,
			}:
			default:
			}
		}
		return true

	case "BYE":
		if dialog == nil {
			return false
		}
		response, err := buildSIPResponse(request, 200, session.fromTag)
		if err == nil {
			_ = respond(response)
		}
		text, lang, dcs, errCode, _ := extractUSSDPayloadFromSIP(request.Body, request.value("Content-Type"))
		select {
		case dialog.turns <- ussiTurnEvent{
			method:   "BYE",
			text:     text,
			language: lang,
			dcs:      dcs,
			errCode:  errCode,
		}:
		default:
		}
		session.ussiMu.Lock()
		delete(session.ussiDialogs, callID)
		session.ussiMu.Unlock()
		return true

	case "CANCEL":
		if dialog == nil {
			return false
		}
		response, err := buildSIPResponse(request, 200, session.fromTag)
		if err == nil {
			_ = respond(response)
		}
		select {
		case dialog.turns <- ussiTurnEvent{
			method: "CANCEL",
			err:    errors.New("ims: USSI dialog cancelled by network"),
		}:
		default:
		}
		session.ussiMu.Lock()
		delete(session.ussiDialogs, callID)
		session.ussiMu.Unlock()
		return true

	default:
		return false
	}
}

// sendUSSIStandaloneMessage provides fallback to legacy SIP MESSAGE for cores that reject INVITE sessions.
func (session *Session) sendUSSIStandaloneMessage(ctx context.Context, request vowifi.USSISubmitRequest) (vowifi.USSISubmitResult, error) {
	session.smsMu.Lock()
	defer session.smsMu.Unlock()

	session.mu.Lock()
	if session.closed || !session.evidence.Registered {
		session.mu.Unlock()
		return vowifi.USSISubmitResult{}, vowifi.ErrUSSINotReady
	}
	target := session.ussiTarget()
	session.mu.Unlock()

	payload := strings.TrimSpace(firstNonEmpty(request.Input, request.Code))
	if payload == "" {
		return vowifi.USSISubmitResult{}, errors.New("ims: USSI payload is empty")
	}
	body, dcs, err := encodeUSSDBody(payload)
	if err != nil {
		return vowifi.USSISubmitResult{}, err
	}
	stringOctets := body
	length := len(stringOctets) + 1
	if length > 255 {
		return vowifi.USSISubmitResult{}, errors.New("ims: USSD string exceeds 254 octets")
	}
	message := make([]byte, 0, 2+len(stringOctets))
	message = append(message, byte(length), byte(*dcs))
	message = append(message, stringOctets...)
	response, sendErr := session.sendSIPMessageWith(ctx, target, message, "", ussiContentType, "ussd")
	result := vowifi.USSISubmitResult{
		SubmissionStatus: "pending",
	}
	if response != nil {
		result.SIPCode = response.StatusCode
	}
	if sendErr != nil {
		result.SubmissionStatus = "failed"
		result.Raw = strings.ToUpper(hex.EncodeToString(message))
		return result, sendErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.SubmissionStatus = "rejected_by_ims"
		result.Status = "failed"
		result.Raw = strings.ToUpper(hex.EncodeToString(message))
		return result, fmt.Errorf("ims: USSI rejected with SIP %d", response.StatusCode)
	}
	text, replyDCS := session.parseUSSIReply(response)
	result.Text = text
	result.DCS = replyDCS
	result.Status = "final"
	result.Continueable = false
	result.Raw = strings.ToUpper(hex.EncodeToString(message))
	result.SubmissionStatus = "accepted_by_ims"
	return result, nil
}
