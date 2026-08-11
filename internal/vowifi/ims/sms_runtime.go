package ims

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"vocat/internal/device"
	"vocat/internal/vowifi"
)

const (
	smsContentType               = "application/vnd.3gpp.sms"
	defaultSMSTR1M               = 40 * time.Second
	smsServerTransactionLifetime = 2 * defaultSMSTR1M
	maxSMSServerTransactions     = 256
)

var (
	ErrSMSCUnavailable        = errors.New("ims: SMS service-centre address is unavailable")
	ErrSMSRejected            = errors.New("ims: SMS MESSAGE was rejected")
	ErrSMSSubmitReportTimeout = errors.New("ims: SMS submit report timed out")
	errRPMessageType          = errors.New("ims: RP message type is invalid")
)

type smsCenterReader interface {
	ReadSMSCenter(context.Context, string) (string, error)
}

// ReceivedSMS is a decoded mobile-terminated SMS delivered over IMS.
type ReceivedSMS struct {
	MessageID              string
	DeviceID               string
	IMSI                   string
	From                   string
	Text                   string
	Timestamp              time.Time
	ServiceCenterTimestamp *time.Time
	Encoding               device.SMSEncoding
	Concat                 *device.SMSConcatInfo
	RPReference            int
	CallID                 string
	RawRPDU                string
	RawTPDU                string
}

// ReceivedSMSStatus is network delivery evidence for one submitted SMS part.
type ReceivedSMSStatus struct {
	DeviceID               string
	IMSI                   string
	To                     string
	MessageReference       int
	StatusCode             int
	DeliveryStatus         string
	ServiceCenterTimestamp *time.Time
	DischargeTimestamp     *time.Time
	Timestamp              time.Time
	RPReference            int
	CallID                 string
	RawRPDU                string
	RawTPDU                string
}

type sipTransactionKey struct {
	callID string
	cseq   uint32
	method string
}

type smsSubmitTransaction struct {
	callID  string
	reports chan rpMessage
}

type smsServerTransactionKey struct {
	via    string
	callID string
	cseq   uint32
}

type smsServerTransaction struct {
	response  []byte
	createdAt time.Time
	ready     chan struct{}
}

func (session *Session) startRuntimeReceivers() error {
	if session.runtimeStarted {
		return nil
	}
	if err := session.conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("ims: clear SIP connection deadline: %w", err)
	}
	if session.protectedUDP != nil {
		_ = session.protectedUDP.SetReadDeadline(time.Time{})
	}
	session.runtimeStarted = true

	session.receiveDone.Add(1)
	go session.readMainConnection()
	if session.securityActive && session.transport == "tcp" && session.protectedTCP != nil {
		session.receiveDone.Add(1)
		go session.acceptProtectedTCP()
	}
	if session.securityActive && session.transport == "udp" && session.protectedUDP != nil {
		session.receiveDone.Add(1)
		go session.readProtectedUDP()
	}
	return nil
}

func (session *Session) readMainConnection() {
	defer session.receiveDone.Done()
	for {
		var packet sipPacket
		var err error
		if session.transport == "tcp" {
			packet, err = readSIPPacket(session.reader)
		} else {
			buffer := make([]byte, 65535)
			var count int
			count, err = session.conn.Read(buffer)
			if err == nil {
				packet, err = parseSIPPacket(buffer[:count])
			}
		}
		if err != nil {
			if !session.isClosed() {
				session.publishFailure(fmt.Errorf("ims: SIP receive loop: %w", err))
			}
			return
		}
		session.dispatchPacket(packet, func(response []byte) error {
			session.writeMu.Lock()
			defer session.writeMu.Unlock()
			_, err := session.conn.Write(response)
			return err
		})
	}
}

func (session *Session) acceptProtectedTCP() {
	defer session.receiveDone.Done()
	for {
		connection, err := session.protectedTCP.AcceptTCP()
		if err != nil {
			return
		}
		if !session.validProtectedTCPSource(connection.RemoteAddr()) {
			_ = connection.Close()
			continue
		}
		session.inboundMu.Lock()
		session.inboundConnections[connection] = struct{}{}
		session.inboundMu.Unlock()
		session.receiveDone.Add(1)
		go session.readInboundTCP(connection)
	}
}

func (session *Session) readInboundTCP(connection net.Conn) {
	defer session.receiveDone.Done()
	defer func() {
		session.inboundMu.Lock()
		delete(session.inboundConnections, connection)
		session.inboundMu.Unlock()
		_ = connection.Close()
	}()
	reader := bufio.NewReader(connection)
	for {
		packet, err := readSIPPacket(reader)
		if err != nil {
			return
		}
		session.dispatchPacket(packet, func(response []byte) error {
			_, err := connection.Write(response)
			return err
		})
	}
}

func (session *Session) readProtectedUDP() {
	defer session.receiveDone.Done()
	buffer := make([]byte, 65535)
	for {
		count, remote, err := session.protectedUDP.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if !session.validProtectedUDPSource(remote) {
			continue
		}
		packet, err := parseSIPPacket(buffer[:count])
		if err != nil {
			continue
		}
		session.dispatchPacket(packet, func(response []byte) error {
			// Protected UDP requests arrive on the UE server port, but UE
			// responses use the client pair (port_uc -> port_ps).
			return session.writeRuntimeRequest(response)
		})
	}
}

func (session *Session) validProtectedTCPSource(address net.Addr) bool {
	remote, ok := address.(*net.TCPAddr)
	if !ok || !session.securityActive {
		return false
	}
	expected := addressIP(session.conn.RemoteAddr())
	return expected != nil && expected.Equal(remote.IP) &&
		remote.Port == session.securityAgreement.selected.portClient
}

func (session *Session) dispatchPacket(packet sipPacket, respond func([]byte) error) {
	if packet.Response != nil {
		response := packet.Response
		cseq, method, err := cseqNumber(response.value("CSeq"))
		if err != nil {
			return
		}
		key := sipTransactionKey{
			callID: strings.TrimSpace(response.value("Call-ID")),
			cseq:   cseq,
			method: method,
		}
		session.transactionsMu.Lock()
		channel := session.transactions[key]
		session.transactionsMu.Unlock()
		if channel != nil {
			select {
			case channel <- response:
			default:
			}
		} else if method == "INVITE" && response.StatusCode >= 200 {
			// 2xx retransmissions live above the INVITE client transaction, and a
			// retransmitted non-2xx final still requires the transaction ACK.
			session.handleUnmatchedInviteResponse(response)
		}
		return
	}
	if packet.Request != nil {
		session.handleSIPRequest(packet.Request, respond)
	}
}

func (session *Session) exchangeRuntime(
	ctx context.Context,
	request []byte,
	key sipTransactionKey,
) (*sipResponse, error) {
	return session.exchangeRuntimeWithTimers(ctx, request, key, sipTransactionT1, session.transactionTimeout())
}

func (session *Session) exchangeRuntimeWithTimers(
	ctx context.Context,
	request []byte,
	key sipTransactionKey,
	t1 time.Duration,
	timeout time.Duration,
) (*sipResponse, error) {
	if t1 <= 0 {
		t1 = sipTransactionT1
	}
	if timeout <= 0 {
		timeout = session.transactionTimeout()
	}
	responses := make(chan *sipResponse, 4)
	session.transactionsMu.Lock()
	if _, duplicate := session.transactions[key]; duplicate {
		session.transactionsMu.Unlock()
		return nil, errors.New("ims: duplicate SIP transaction")
	}
	session.transactions[key] = responses
	session.transactionsMu.Unlock()
	defer func() {
		session.transactionsMu.Lock()
		delete(session.transactions, key)
		session.transactionsMu.Unlock()
	}()

	if err := session.writeRuntimeRequest(request); err != nil {
		return nil, fmt.Errorf("ims: send SIP %s: %w", key.method, err)
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
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("ims: SIP %s transaction timed out", key.method)
		case <-retransmitC:
			if err := session.writeRuntimeRequest(request); err != nil {
				return nil, fmt.Errorf("ims: retransmit SIP %s: %w", key.method, err)
			}
			interval *= 2
			if interval > sipTransactionT2 {
				interval = sipTransactionT2
			}
			retransmit.Reset(interval)
		case response := <-responses:
			if response.StatusCode >= 100 && response.StatusCode < 200 {
				// Timer E switches to T2 after a provisional response for a
				// non-INVITE client transaction.
				if retransmit != nil {
					if !retransmit.Stop() {
						select {
						case <-retransmit.C:
						default:
						}
					}
					interval = sipTransactionT2
					retransmit.Reset(interval)
				}
				continue
			}
			return response, nil
		}
	}
}

func (session *Session) transactionTimeout() time.Duration {
	if session != nil && session.provider != nil && session.provider.config.TransactionTimeout > 0 {
		return session.provider.config.TransactionTimeout
	}
	return defaultTransactionTimeout
}

func (session *Session) writeRuntimeRequest(request []byte) error {
	if session == nil || session.conn == nil {
		return errors.New("SIP transport is unavailable")
	}
	session.writeMu.Lock()
	_, err := session.conn.Write(request)
	session.writeMu.Unlock()
	return err
}

func (session *Session) handleSIPRequest(request *sipRequest, respond func([]byte) error) {
	if session.handleCallRequest(request, respond) {
		return
	}
	var serverKey smsServerTransactionKey
	var cacheable bool
	var serverTransaction *smsServerTransaction
	if request.Method == "MESSAGE" {
		serverKey, cacheable = smsServerKey(request)
		if cacheable {
			var duplicateResponse []byte
			serverTransaction, duplicateResponse = session.reserveSMSServerTransaction(serverKey)
			if serverTransaction == nil {
				if len(duplicateResponse) > 0 {
					_ = respond(duplicateResponse)
				}
				return
			}
		}
	}
	status := 200
	processMT := false
	switch request.Method {
	case "OPTIONS":
	case "MESSAGE":
		contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(request.value("Content-Type"), ";", 2)[0]))
		if contentType != smsContentType {
			status = 415
			break
		}
		message, parseErr := parseRPDU(request.Body)
		messageType := byte(0xff)
		if len(request.Body) > 0 {
			messageType = request.Body[0] & 0x07
		}
		if messageType == 3 || messageType == 5 {
			if parseErr != nil || !session.queueSMSSubmitReport(request, message) {
				status = 488
			}
			break
		}
		processMT = true
	default:
		status = 405
	}
	response, err := buildSIPResponse(request, status, session.fromTag)
	if cacheable {
		session.completeSMSServerTransaction(serverTransaction, response, err)
	}
	if err != nil {
		return
	}
	_ = respond(response)
	if status != 200 || !processMT {
		return
	}
	go session.processSMSMessage(request)
}

func smsServerKey(request *sipRequest) (smsServerTransactionKey, bool) {
	if request == nil {
		return smsServerTransactionKey{}, false
	}
	vias := request.values("Via")
	callID := strings.TrimSpace(request.value("Call-ID"))
	cseq, method, err := cseqNumber(request.value("CSeq"))
	if len(vias) == 0 || callID == "" || err != nil || method != "MESSAGE" {
		return smsServerTransactionKey{}, false
	}
	return smsServerTransactionKey{
		via:    strings.TrimSpace(vias[0]),
		callID: callID,
		cseq:   cseq,
	}, true
}

func (session *Session) reserveSMSServerTransaction(
	key smsServerTransactionKey,
) (*smsServerTransaction, []byte) {
	now := time.Now()
	session.smsServerMu.Lock()
	if session.smsServer == nil {
		session.smsServer = make(map[smsServerTransactionKey]*smsServerTransaction)
	}
	for existingKey, entry := range session.smsServer {
		if now.Sub(entry.createdAt) >= smsServerTransactionLifetime {
			delete(session.smsServer, existingKey)
		}
	}
	if entry := session.smsServer[key]; entry != nil {
		ready := entry.ready
		session.smsServerMu.Unlock()
		<-ready
		return nil, append([]byte(nil), entry.response...)
	}
	if len(session.smsServer) >= maxSMSServerTransactions {
		var oldestKey smsServerTransactionKey
		var oldestTime time.Time
		for existingKey, entry := range session.smsServer {
			if oldestTime.IsZero() || entry.createdAt.Before(oldestTime) {
				oldestKey = existingKey
				oldestTime = entry.createdAt
			}
		}
		delete(session.smsServer, oldestKey)
	}
	entry := &smsServerTransaction{
		createdAt: now,
		ready:     make(chan struct{}),
	}
	session.smsServer[key] = entry
	session.smsServerMu.Unlock()
	return entry, nil
}

func (session *Session) completeSMSServerTransaction(
	transaction *smsServerTransaction,
	response []byte,
	err error,
) {
	if transaction == nil {
		return
	}
	if err == nil {
		transaction.response = append([]byte(nil), response...)
	}
	close(transaction.ready)
}

func (session *Session) queueSMSSubmitReport(request *sipRequest, message rpMessage) bool {
	inReplyTo := strings.TrimSpace(request.value("In-Reply-To"))
	if inReplyTo == "" {
		return false
	}
	session.smsSubmitMu.Lock()
	defer session.smsSubmitMu.Unlock()
	transaction := session.smsSubmit[message.reference]
	if transaction == nil || transaction.callID != inReplyTo {
		return false
	}
	select {
	case transaction.reports <- message:
	default:
		// A retransmission using a fresh SIP transaction still refers to the
		// same outstanding RP transaction. The first report remains decisive.
	}
	return true
}

func buildSIPResponse(request *sipRequest, status int, tag string) ([]byte, error) {
	reason := map[int]string{200: "OK", 405: "Method Not Allowed", 415: "Unsupported Media Type", 488: "Not Acceptable Here"}[status]
	if reason == "" {
		return nil, errors.New("ims: unsupported SIP response status")
	}
	via := request.values("Via")
	from := request.value("From")
	to := request.value("To")
	callID := request.value("Call-ID")
	cseq := request.value("CSeq")
	if len(via) == 0 || from == "" || to == "" || callID == "" || cseq == "" {
		return nil, errors.New("ims: request omitted a mandatory response header")
	}
	if !strings.Contains(strings.ToLower(to), ";tag=") {
		to += ";tag=" + tag
	}
	lines := []string{fmt.Sprintf("SIP/2.0 %d %s", status, reason)}
	for _, value := range via {
		lines = append(lines, "Via: "+value)
	}
	lines = append(lines,
		"From: "+from,
		"To: "+to,
		"Call-ID: "+callID,
		"CSeq: "+cseq,
	)
	if status == 405 {
		lines = append(lines, "Allow: REGISTER, MESSAGE, INVITE, ACK, CANCEL, BYE, PRACK, OPTIONS")
	}
	if status == 415 {
		lines = append(lines, "Accept: "+smsContentType)
	}
	lines = append(lines, "Content-Length: 0", "", "")
	return []byte(strings.Join(lines, "\r\n")), nil
}

func (session *Session) processSMSMessage(request *sipRequest) {
	rpdu, err := parseRPDU(request.Body)
	if err != nil {
		// Without the RP-Message-Reference the network cannot correlate an
		// RP-ERROR, so TS 24.011 requires the too-short message to be ignored.
		if len(request.Body) >= 2 {
			cause := byte(96)
			if errors.Is(err, errRPMessageType) {
				cause = 97
			}
			session.sendDeliveryReport(request, buildRPError(rpdu.reference, cause))
		}
		return
	}
	if rpdu.messageType != 1 { // Only RP-DATA, network to MS, is valid on this path.
		// The even MTI values are defined only in the MS-to-network direction.
		// Received from the network they are reserved and require cause 97, just
		// like the direction-independent reserved values rejected by parseRPDU.
		session.sendDeliveryReport(request, buildRPError(rpdu.reference, 97))
		return
	}
	message, err := device.DecodeSMSDeliverTPDU(rpdu.tpdu)
	if err != nil {
		session.sendDeliveryReport(request, buildRPError(rpdu.reference, 95))
		return
	}
	receivedAt := time.Now().UTC()
	callID := strings.TrimSpace(request.value("Call-ID"))
	if message.Direction == device.SMSDirectionStatusReport {
		if message.MessageReference == nil || message.StatusCode == nil {
			session.sendDeliveryReport(request, buildRPError(rpdu.reference, 95))
			return
		}
		status := ReceivedSMSStatus{
			DeviceID:               session.request.DeviceID,
			IMSI:                   session.request.Identity.IMSI,
			To:                     message.To,
			MessageReference:       *message.MessageReference,
			StatusCode:             *message.StatusCode,
			DeliveryStatus:         message.DeliveryStatus,
			ServiceCenterTimestamp: message.ServiceCenterTimestamp,
			DischargeTimestamp:     message.DischargeTimestamp,
			Timestamp:              receivedAt,
			RPReference:            int(rpdu.reference),
			CallID:                 callID,
			RawRPDU:                strings.ToUpper(hex.EncodeToString(request.Body)),
			RawTPDU:                strings.ToUpper(hex.EncodeToString(rpdu.tpdu)),
		}
		if session.provider.config.OnSMSStatus != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = session.provider.config.OnSMSStatus(ctx, status)
			cancel()
		}
		if err != nil {
			session.sendDeliveryReport(request, buildRPError(rpdu.reference, 22))
			return
		}
		session.sendDeliveryReport(request, []byte{0x02, rpdu.reference})
		return
	}
	if message.Direction != device.SMSDirectionReceived {
		session.sendDeliveryReport(request, buildRPError(rpdu.reference, 95))
		return
	}
	var serviceCenterTimestamp *time.Time
	if message.ServiceCenterTimestamp != nil {
		value := message.ServiceCenterTimestamp.UTC()
		serviceCenterTimestamp = &value
	}
	received := ReceivedSMS{
		// A retransmission inside the same SIP transaction is idempotent, but a
		// fresh Call-ID/RP reference is a distinct network delivery and must stay
		// visible even when its TPDU and text happen to be identical.
		MessageID:              fmt.Sprintf("ims:%s:%d", callID, rpdu.reference),
		DeviceID:               session.request.DeviceID,
		IMSI:                   session.request.Identity.IMSI,
		From:                   message.From,
		Text:                   message.Text,
		Timestamp:              receivedAt,
		ServiceCenterTimestamp: serviceCenterTimestamp,
		Encoding:               message.Encoding,
		Concat:                 message.Concat,
		RPReference:            int(rpdu.reference),
		CallID:                 callID,
		RawRPDU:                strings.ToUpper(hex.EncodeToString(request.Body)),
		RawTPDU:                strings.ToUpper(hex.EncodeToString(rpdu.tpdu)),
	}
	if session.provider.config.OnSMS != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = session.provider.config.OnSMS(ctx, received)
		cancel()
	}
	if err != nil {
		session.sendDeliveryReport(request, buildRPError(rpdu.reference, 22))
		return
	}
	session.sendDeliveryReport(request, []byte{0x02, rpdu.reference})
}

func (session *Session) sendDeliveryReport(request *sipRequest, report []byte) {
	target := firstURI(request.value("P-Asserted-Identity"))
	if target == "" {
		target = firstURI(request.value("From"))
	}
	if target == "" {
		return
	}
	_, _ = session.sendSIPMessage(
		context.Background(),
		target,
		report,
		strings.TrimSpace(request.value("Call-ID")),
	)
}

func (session *Session) SendSMS(ctx context.Context, request vowifi.SMSSubmitRequest) (vowifi.SMSSubmitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session.smsMu.Lock()
	defer session.smsMu.Unlock()

	session.mu.Lock()
	if session.closed || !session.evidence.Registered || !session.smsContactConfirmed {
		session.mu.Unlock()
		return vowifi.SMSSubmitResult{}, vowifi.ErrSMSNotReady
	}
	smsc := strings.TrimSpace(session.request.Identity.SMSC)
	session.mu.Unlock()
	if smsc == "" {
		reader, ok := session.provider.aka.(smsCenterReader)
		var readErr error
		if ok {
			smsc, readErr = reader.ReadSMSCenter(ctx, session.request.DeviceID)
		}
		if strings.TrimSpace(smsc) == "" {
			smsc = session.provider.config.SMSCenter
		}
		if strings.TrimSpace(smsc) == "" {
			return vowifi.SMSSubmitResult{}, errors.Join(ErrSMSCUnavailable, readErr)
		}
	}
	parts, err := device.PrepareSMSSubmitTPDUs(request.Recipient, request.Text)
	if err != nil {
		return vowifi.SMSSubmitResult{}, err
	}
	now := time.Now().UTC()
	result := vowifi.SMSSubmitResult{
		To:               parts[0].To,
		Encoding:         string(parts[0].Encoding),
		SubmittedAt:      now,
		PartsTotal:       len(parts),
		ConcatReference:  parts[0].ConcatReference,
		SubmissionStatus: "pending",
		PartResults:      make([]vowifi.SMSSubmitPart, 0, len(parts)),
	}
	psi := "tel:" + normalizeE164(smsc)
	for _, part := range parts {
		reference, referenceErr := session.allocateRPReference()
		if referenceErr != nil {
			result.SubmissionStatus = "failed"
			return result, referenceErr
		}
		if len(part.TPDU) < 2 {
			return result, errors.New("ims: SMS-SUBMIT TPDU is truncated")
		}
		// Use the same value for TP-MR and RP-Message-Reference so an
		// SMS-STATUS-REPORT can be mapped back to this submitted part.
		part.TPDU[1] = reference
		rpdu, buildErr := buildRPData(reference, smsc, part.TPDU)
		if buildErr != nil {
			return result, buildErr
		}
		result.PartsAttempted++
		partResult := vowifi.SMSSubmitPart{
			Part: part.Part, Total: part.Total, Reference: int(reference), SubmittedAt: time.Now().UTC(),
		}
		message, key, buildErr := session.buildSIPMessage(psi, rpdu, "")
		if buildErr != nil {
			partResult.SubmissionStatus = "send_failed"
			result.PartResults = append(result.PartResults, partResult)
			result.SubmissionStatus = "failed"
			return result, buildErr
		}
		transaction, registerErr := session.registerSMSSubmit(reference, key.callID)
		if registerErr != nil {
			partResult.SubmissionStatus = "send_failed"
			result.PartResults = append(result.PartResults, partResult)
			result.SubmissionStatus = "failed"
			return result, registerErr
		}
		response, sendErr := session.exchangeRuntime(ctx, message, key)
		if response != nil {
			partResult.SIPCode = response.StatusCode
		}
		if sendErr != nil {
			session.unregisterSMSSubmit(reference, transaction)
			partResult.SubmissionStatus = "send_failed"
			result.PartResults = append(result.PartResults, partResult)
			result.SubmissionStatus = "failed"
			return result, sendErr
		}
		if response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			session.unregisterSMSSubmit(reference, transaction)
			partResult.SubmissionStatus = "rejected_by_ims"
			result.PartResults = append(result.PartResults, partResult)
			result.SubmissionStatus = "rejected"
			if response == nil {
				return result, fmt.Errorf("%w: missing SIP response", ErrSMSRejected)
			}
			return result, fmt.Errorf("%w: SIP %d", ErrSMSRejected, response.StatusCode)
		}
		report, reportErr := session.waitSMSSubmitReport(ctx, reference, transaction)
		if reportErr != nil {
			partResult.SubmissionStatus = "submit_report_unknown"
			result.PartResults = append(result.PartResults, partResult)
			result.SubmissionStatus = "unknown"
			return result, reportErr
		}
		if report.messageType == 5 {
			partResult.SubmissionStatus = "rejected_by_ims"
			result.PartResults = append(result.PartResults, partResult)
			result.SubmissionStatus = "rejected"
			return result, fmt.Errorf("%w: RP-ERROR cause %d", ErrSMSRejected, report.cause)
		}
		partResult.Accepted = true
		partResult.SubmissionStatus = "accepted_by_ims"
		result.PartsAccepted++
		result.PartResults = append(result.PartResults, partResult)
	}
	result.AllPartsAccepted = true
	result.SubmissionStatus = "accepted_by_ims"
	return result, nil
}

func (session *Session) allocateRPReference() (byte, error) {
	session.smsSubmitMu.Lock()
	defer session.smsSubmitMu.Unlock()
	for attempts := 0; attempts < 256; attempts++ {
		value := session.nextRPReference
		session.nextRPReference++
		if _, active := session.smsSubmit[value]; !active {
			return value, nil
		}
	}
	return 0, errors.New("ims: all RP message references are active")
}

func (session *Session) registerSMSSubmit(reference byte, callID string) (*smsSubmitTransaction, error) {
	transaction := &smsSubmitTransaction{
		callID:  callID,
		reports: make(chan rpMessage, 1),
	}
	session.smsSubmitMu.Lock()
	defer session.smsSubmitMu.Unlock()
	if session.smsSubmit == nil {
		session.smsSubmit = make(map[byte]*smsSubmitTransaction)
	}
	if _, active := session.smsSubmit[reference]; active {
		return nil, errors.New("ims: duplicate RP message reference")
	}
	session.smsSubmit[reference] = transaction
	return transaction, nil
}

func (session *Session) unregisterSMSSubmit(reference byte, transaction *smsSubmitTransaction) {
	session.smsSubmitMu.Lock()
	if session.smsSubmit[reference] == transaction {
		delete(session.smsSubmit, reference)
	}
	session.smsSubmitMu.Unlock()
}

func (session *Session) waitSMSSubmitReport(
	ctx context.Context,
	reference byte,
	transaction *smsSubmitTransaction,
) (rpMessage, error) {
	defer session.unregisterSMSSubmit(reference, transaction)
	timeout := session.smsTR1M
	if timeout <= 0 {
		timeout = defaultSMSTR1M
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var sessionDone <-chan struct{}
	if session.refreshContext != nil {
		sessionDone = session.refreshContext.Done()
	}
	select {
	case report := <-transaction.reports:
		return report, nil
	case <-ctx.Done():
		return rpMessage{}, ctx.Err()
	case <-sessionDone:
		return rpMessage{}, ErrSessionClosed
	case <-timer.C:
		return rpMessage{}, ErrSMSSubmitReportTimeout
	}
}

func (session *Session) sendSIPMessage(
	ctx context.Context,
	target string,
	body []byte,
	inReplyTo string,
) (*sipResponse, error) {
	request, key, err := session.buildSIPMessage(target, body, inReplyTo)
	if err != nil {
		return nil, err
	}
	return session.exchangeRuntime(ctx, request, key)
}

func (session *Session) buildSIPMessage(
	target string,
	body []byte,
	inReplyTo string,
) ([]byte, sipTransactionKey, error) {
	callToken, err := randomHex(18)
	if err != nil {
		return nil, sipTransactionKey{}, err
	}
	branch, err := randomHex(12)
	if err != nil {
		return nil, sipTransactionKey{}, err
	}
	callID := callToken + "@" + addressHost(session.conn.LocalAddr())
	session.mu.Lock()
	cseq := session.cseq
	session.cseq++
	serviceRoutes := append([]string(nil), session.evidence.ServiceRoute...)
	securityHeaders := runtimeSecurityHeaders(
		session.securityActive,
		session.securityAgreement.verifyValue,
	)
	session.mu.Unlock()
	transportUpper := strings.ToUpper(session.transport)
	lines := []string{
		"MESSAGE " + target + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", transportUpper, session.conn.LocalAddr().String(), branch),
		"Max-Forwards: 70",
	}
	lines = append(lines, securityHeaders...)
	if len(serviceRoutes) == 0 {
		lines = append(lines, "Route: <sip:"+session.endpoint.address()+";transport="+session.transport+";lr>")
	} else {
		for _, route := range serviceRoutes {
			lines = append(lines, "Route: "+route)
		}
	}
	lines = append(lines,
		"From: <"+session.identity.public+">;tag="+session.fromTag,
		"To: <"+target+">",
		"Call-ID: "+callID,
		fmt.Sprintf("CSeq: %d MESSAGE", cseq),
		"P-Preferred-Identity: <"+session.identity.public+">",
		"Accept-Contact: *;+g.3gpp.smsip",
	)
	if inReplyTo != "" {
		lines = append(lines, "In-Reply-To: "+inReplyTo)
	}
	lines = append(lines,
		"Content-Type: "+smsContentType,
		"Content-Transfer-Encoding: binary",
		"Content-Length: "+strconv.Itoa(len(body)),
		"", "",
	)
	request := append([]byte(strings.Join(lines, "\r\n")), body...)
	return request, sipTransactionKey{callID: callID, cseq: cseq, method: "MESSAGE"}, nil
}

func runtimeSecurityHeaders(active bool, verifyValue string) []string {
	verifyValue = strings.TrimSpace(verifyValue)
	if !active || verifyValue == "" {
		return nil
	}
	// RFC 3329 requires every request following a security agreement to
	// mirror Security-Server and repeat both sec-agree option tags. Omitting
	// these fields causes Vodafone's P-CSCF to reject MESSAGE with SIP 494.
	return []string{
		"Security-Verify: " + verifyValue,
		"Require: sec-agree",
		"Proxy-Require: sec-agree",
	}
}

type rpMessage struct {
	messageType byte
	reference   byte
	cause       byte
	tpdu        []byte
}

func parseRPDU(data []byte) (rpMessage, error) {
	if len(data) < 2 {
		return rpMessage{}, errors.New("ims: RPDU is truncated")
	}
	result := rpMessage{messageType: data[0] & 0x07, reference: data[1]}
	if data[0]&0xf8 != 0 {
		return result, fmt.Errorf("%w: spare bits are non-zero", errRPMessageType)
	}
	switch result.messageType {
	case 0, 1: // RP-DATA, MS to network or network to MS.
		index := 2
		for count := 0; count < 2; count++ {
			if index >= len(data) {
				return result, errors.New("ims: RP-DATA address is truncated")
			}
			length := int(data[index])
			index++
			if length > len(data)-index {
				return result, errors.New("ims: RP-DATA address length is invalid")
			}
			index += length
		}
		if index >= len(data) {
			return result, errors.New("ims: RP-DATA omitted user data")
		}
		length := int(data[index])
		index++
		if length == 0 || length != len(data)-index {
			return result, errors.New("ims: RP-DATA user-data length is invalid")
		}
		result.tpdu = append([]byte(nil), data[index:]...)
		return result, nil
	case 2, 3: // RP-ACK, optionally followed by RP-User-Data.
		if err := parseRPUserData(data, 2, &result); err != nil {
			return result, err
		}
		return result, nil
	case 4, 5: // RP-ERROR with mandatory RP-Cause.
		if len(data) < 4 {
			return result, errors.New("ims: RP-ERROR omitted cause")
		}
		causeLength := int(data[2])
		if causeLength == 0 || causeLength > len(data)-3 {
			return result, errors.New("ims: RP-ERROR cause length is invalid")
		}
		result.cause = data[3] & 0x7f
		if err := parseRPUserData(data, 3+causeLength, &result); err != nil {
			return result, err
		}
		return result, nil
	default:
		return result, fmt.Errorf("%w: reserved MTI %d", errRPMessageType, result.messageType)
	}
}

func parseRPUserData(data []byte, index int, result *rpMessage) error {
	if index == len(data) {
		return nil
	}
	if index < 0 || index+2 > len(data) || data[index] != 0x41 {
		return errors.New("ims: RPDU optional user-data IE is invalid")
	}
	length := int(data[index+1])
	index += 2
	if length == 0 || length != len(data)-index {
		return errors.New("ims: RPDU optional user-data length is invalid")
	}
	result.tpdu = append([]byte(nil), data[index:]...)
	return nil
}

func buildRPData(reference byte, smsc string, tpdu []byte) ([]byte, error) {
	address, err := encodeRPAddress(smsc)
	if err != nil {
		return nil, err
	}
	if len(tpdu) == 0 || len(tpdu) > 232 {
		return nil, errors.New("ims: SMS TPDU length is invalid")
	}
	result := []byte{0x00, reference, 0x00, byte(len(address))}
	result = append(result, address...)
	result = append(result, byte(len(tpdu)))
	result = append(result, tpdu...)
	return result, nil
}

func buildRPError(reference byte, cause byte) []byte {
	return []byte{0x04, reference, 0x01, cause & 0x7f}
}

func encodeRPAddress(value string) ([]byte, error) {
	value = normalizeE164(value)
	digits := strings.TrimPrefix(value, "+")
	if len(digits) < 3 || len(digits) > 20 {
		return nil, ErrSMSCUnavailable
	}
	if strings.Count(value, "+") > 1 || (strings.Contains(value, "+") && !strings.HasPrefix(value, "+")) {
		return nil, ErrSMSCUnavailable
	}
	toa := byte(0x81)
	if strings.HasPrefix(value, "+") {
		toa = 0x91
	}
	encoded := make([]byte, (len(digits)+1)/2)
	for index := 0; index < len(digits); index += 2 {
		if digits[index] < '0' || digits[index] > '9' {
			return nil, ErrSMSCUnavailable
		}
		low := digits[index] - '0'
		high := byte(0x0f)
		if index+1 < len(digits) {
			if digits[index+1] < '0' || digits[index+1] > '9' {
				return nil, ErrSMSCUnavailable
			}
			high = digits[index+1] - '0'
		}
		encoded[index/2] = high<<4 | low
	}
	return append([]byte{toa}, encoded...), nil
}

func normalizeE164(value string) string {
	return strings.TrimSpace(value)
}

func firstURI(value string) string {
	value = strings.TrimSpace(strings.SplitN(value, ",", 2)[0])
	if start := strings.IndexByte(value, '<'); start >= 0 {
		if end := strings.IndexByte(value[start+1:], '>'); end >= 0 {
			return strings.TrimSpace(value[start+1 : start+1+end])
		}
	}
	if semicolon := strings.IndexByte(value, ';'); semicolon >= 0 {
		value = value[:semicolon]
	}
	return strings.TrimSpace(value)
}

func (session *Session) isClosed() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.closed
}

func (session *Session) closeInboundConnections() {
	session.inboundMu.Lock()
	connections := make([]net.Conn, 0, len(session.inboundConnections))
	for connection := range session.inboundConnections {
		connections = append(connections, connection)
	}
	session.inboundMu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

var _ vowifi.SMSSender = (*Session)(nil)
