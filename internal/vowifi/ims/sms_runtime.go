package ims

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"vocat/internal/device"
	"vocat/internal/vowifi"
)

const (
	smsContentType          = "application/vnd.3gpp.sms"
	ussiContentType         = "application/vnd.3gpp.ussd"
	sipMessageRetransmitT1  = 500 * time.Millisecond
	sipMessageRetransmitMax = 4 * time.Second
)

var (
	ErrSMSCUnavailable = errors.New("ims: SMS service-centre address is unavailable")
	ErrSMSRejected     = errors.New("ims: SMS MESSAGE was rejected")
)

type smsCenterReader interface {
	ReadSMSCenter(context.Context, string) (string, error)
}

// ReceivedSMS is a decoded mobile-terminated SMS delivered over IMS.
type ReceivedSMS struct {
	MessageID              string
	DeviceID               string
	ICCID                  string
	ModemIMEI              string
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
	DecodeError            string
}

// ReceivedSMSStatus is network delivery evidence for one submitted SMS part.
type ReceivedSMSStatus struct {
	DeviceID               string
	ICCID                  string
	ModemIMEI              string
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
	DecodeError            string
}

// ReceivedUSSD is a decoded network-originated USSD message delivered over IMS
// (3GPP TS 24.390). Status carries the network's USSD operation code semantics
// ("final", "awaiting_input", "terminated") when present in the body.
type ReceivedUSSD struct {
	MessageID    string
	DeviceID     string
	ICCID        string
	IMSI         string
	From         string
	Text         string
	DCS          *int
	Status       string
	Continueable bool
	Timestamp    time.Time
	CallID       string
	RawBody      string
}

type sipTransactionKey struct {
	callID string
	cseq   uint32
	method string
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
	// Vodafone UK (and others) deliver MT SMS as SIP MESSAGE to the
	// ipsec-3gpp UE server port over UDP even when REGISTER used TCP.
	// Always read both sockets when they were reserved.
	if session.securityActive && session.protectedTCP != nil {
		session.receiveDone.Add(1)
		go session.acceptProtectedTCP()
	}
	if session.securityActive && session.protectedUDP != nil {
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
			session.logInboundSMS(slog.LevelWarn, "IMS inbound TCP rejected", nil,
				"stage", "source_filter", "remote", connection.RemoteAddr().String())
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
			if !session.isClosed() && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				session.logInboundSMS(slog.LevelWarn, "IMS protected SIP packet read failed", nil,
					"stage", "sip_parse", "transport", "tcp",
					"remote", connection.RemoteAddr().String(), "error", err)
			}
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
			session.logInboundSMS(slog.LevelWarn, "IMS inbound UDP rejected", nil,
				"stage", "source_filter", "remote", remote.String(), "packet_bytes", count)
			continue
		}
		packet, err := parseSIPPacket(buffer[:count])
		if err != nil {
			session.logInboundSMS(slog.LevelWarn, "IMS protected SIP packet parse failed", nil,
				"stage", "sip_parse", "transport", "udp", "remote", remote.String(),
				"packet_bytes", count, "error", err)
			continue
		}
		session.dispatchPacket(packet, func(response []byte) error {
			_, err := session.protectedUDP.WriteToUDP(response, remote)
			return err
		})
	}
}

func (session *Session) validProtectedTCPSource(address net.Addr) bool {
	remote, ok := address.(*net.TCPAddr)
	if !ok || !session.securityActive {
		return false
	}
	expected := addressIP(session.conn.RemoteAddr())
	// Require P-CSCF IP. Do not require port-c (50601): some cores originate
	// MESSAGE from an ephemeral port on the same P-CSCF.
	return expected != nil && expected.Equal(remote.IP)
}

func (session *Session) dispatchPacket(packet sipPacket, respond func([]byte) error) {
	if packet.Response != nil {
		response := packet.Response
		cseq, method, err := cseqNumber(response.value("CSeq"))
		if err != nil {
			session.logOutboundSMS(slog.LevelWarn, "IMS SIP response could not be matched",
				"stage", "sip_response", "sip_status", response.StatusCode, "error", err)
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
		} else if method == "MESSAGE" {
			session.logOutboundSMS(slog.LevelWarn, "IMS SIP MESSAGE response was unmatched",
				"stage", "sip_response", "call_id", key.callID,
				"cseq", key.cseq, "sip_status", response.StatusCode)
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

	writeRequest := func() error {
		session.writeMu.Lock()
		defer session.writeMu.Unlock()
		_, err := session.conn.Write(request)
		return err
	}
	if err := writeRequest(); err != nil {
		return nil, fmt.Errorf("ims: send SIP %s: %w", key.method, err)
	}
	timer := time.NewTimer(session.provider.config.TransactionTimeout)
	defer timer.Stop()
	var retransmitTimer *time.Timer
	var retransmit <-chan time.Time
	retransmitInterval := sipMessageRetransmitT1
	retransmitCount := 0
	if session.transport == "udp" && key.method == "MESSAGE" {
		retransmitTimer = time.NewTimer(retransmitInterval)
		retransmit = retransmitTimer.C
		defer retransmitTimer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			if retransmitTimer != nil {
				return nil, fmt.Errorf(
					"ims: SIP %s transaction timed out after %d retransmissions",
					key.method,
					retransmitCount,
				)
			}
			return nil, fmt.Errorf("ims: SIP %s transaction timed out", key.method)
		case <-retransmit:
			if err := writeRequest(); err != nil {
				return nil, fmt.Errorf("ims: retransmit SIP %s: %w", key.method, err)
			}
			retransmitCount++
			session.logOutboundSMS(slog.LevelDebug, "IMS SIP MESSAGE retransmitted",
				"stage", "sip_retransmit", "call_id", key.callID,
				"cseq", key.cseq, "attempt", retransmitCount)
			retransmitInterval *= 2
			if retransmitInterval > sipMessageRetransmitMax {
				retransmitInterval = sipMessageRetransmitMax
			}
			retransmitTimer.Reset(retransmitInterval)
		case response := <-responses:
			if response.StatusCode >= 100 && response.StatusCode < 200 {
				if retransmitTimer != nil {
					if !retransmitTimer.Stop() {
						select {
						case <-retransmitTimer.C:
						default:
						}
					}
					retransmitInterval = sipMessageRetransmitMax
					retransmitTimer.Reset(retransmitInterval)
				}
				continue
			}
			return response, nil
		}
	}
}

func (session *Session) handleSIPRequest(request *sipRequest, respond func([]byte) error) {
	if session.handleCallRequest(request, respond) {
		return
	}
	status := 200
	ussiMessage := false
	switch request.Method {
	case "OPTIONS":
	case "MESSAGE":
		switch {
		case supportsSMSContentType(request.value("Content-Type")):
			// SMS body handled below.
		case supportsUSSIContentType(request.value("Content-Type")):
			ussiMessage = true
		default:
			status = 415
		}
	default:
		status = 405
	}
	response, err := buildSIPResponse(request, status, session.fromTag)
	if err != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS inbound SIP request response failed", request,
			"stage", "sip_response_build", "error", err)
	} else if err = respond(response); err != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS inbound SIP request response failed", request,
			"stage", "sip_response_send", "sip_status", status, "error", err)
	}
	if status != 200 || request.Method != "MESSAGE" {
		if request.Method == "MESSAGE" {
			session.logInboundSMS(slog.LevelWarn, "IMS inbound MESSAGE rejected", request,
				"stage", "content_type", "sip_status", status)
		}
		return
	}
	if ussiMessage {
		session.logInboundSMS(slog.LevelInfo, "IMS inbound USSD MESSAGE received", request,
			"stage", "sip_accepted")
		go session.processUSSIMessage(request)
		return
	}
	session.logInboundSMS(slog.LevelInfo, "IMS inbound SMS MESSAGE received", request,
		"stage", "sip_accepted")
	go session.processSMSMessage(request)
}

func supportsSMSContentType(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	if strings.EqualFold(mediaType, smsContentType) {
		return true
	}
	return strings.EqualFold(mediaType, "multipart/mixed") &&
		strings.TrimSpace(parameters["boundary"]) != ""
}

func supportsUSSIContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, ussiContentType)
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
		lines = append(lines, "Allow: REGISTER, MESSAGE, OPTIONS")
	}
	if status == 415 {
		lines = append(lines, "Accept: "+smsContentType+", "+ussiContentType)
	}
	lines = append(lines, "Content-Length: 0", "", "")
	return []byte(strings.Join(lines, "\r\n")), nil
}

func (session *Session) processSMSMessage(request *sipRequest) {
	payload, payloadSource, err := extractSMSPayload(request)
	if err != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS decode failed", request,
			"stage", "mime", "error", err)
		session.sendLoggedDeliveryReport(request, buildRPError(0, 95), "rp_error")
		return
	}
	rpdu, err := parseRPDU(payload)
	if err != nil {
		reference := byte(0)
		if len(payload) > 1 {
			reference = payload[1]
		}
		session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS decode failed", request,
			"stage", "rpdu", "payload_source", payloadSource,
			"rp_reference", int(reference), "payload_bytes", len(payload), "error", err)
		session.sendLoggedDeliveryReport(request, buildRPError(reference, 95), "rp_error")
		return
	}
	if rpdu.messageType != 1 { // RP-DATA, network to MS.
		session.logInboundSMS(slog.LevelInfo, "IMS inbound SMS control message received", request,
			"stage", "rpdu", "payload_source", payloadSource,
			"rp_message_type", int(rpdu.messageType), "rp_reference", int(rpdu.reference))
		return
	}

	message, decodeErr := device.DecodeSMSDeliverTPDU(rpdu.tpdu)
	receivedAt := time.Now().UTC()
	callID := strings.TrimSpace(request.value("Call-ID"))
	carrierProfile := vowifi.ResolveCarrierProfile(session.request.Identity)

	if decodeErr != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS decode failed; persisting raw payload", request,
			"stage", "tpdu", "payload_source", payloadSource,
			"rp_reference", int(rpdu.reference), "tpdu_bytes", len(rpdu.tpdu),
			"carrier_profile", carrierProfile.ID,
			"direction", message.Direction, "error", decodeErr)
	}

	switch {
	case message.Direction == device.SMSDirectionStatusReport:
		status := ReceivedSMSStatus{
			DeviceID:               session.request.DeviceID,
			ICCID:                  session.request.Identity.ICCID,
			ModemIMEI:              strings.TrimSpace(session.request.Identity.IMEI),
			IMSI:                   session.request.Identity.IMSI,
			To:                     message.To,
			MessageReference:       intPtrValue(message.MessageReference),
			StatusCode:             intPtrValue(message.StatusCode),
			DeliveryStatus:         message.DeliveryStatus,
			ServiceCenterTimestamp: message.ServiceCenterTimestamp,
			DischargeTimestamp:     message.DischargeTimestamp,
			Timestamp:              receivedAt,
			RPReference:            int(rpdu.reference),
			CallID:                 callID,
			RawRPDU:                strings.ToUpper(hex.EncodeToString(payload)),
			RawTPDU:                strings.ToUpper(hex.EncodeToString(rpdu.tpdu)),
			DecodeError:            errorString(decodeErr),
		}
		if (message.MessageReference == nil || message.StatusCode == nil) && decodeErr == nil {
			session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS status report is incomplete", request,
				"stage", "tpdu", "rp_reference", int(rpdu.reference))
			session.sendLoggedDeliveryReport(request, buildRPError(rpdu.reference, 95), "rp_error")
			return
		}
		err := session.invokeSMSStatusCallback(status)
		if err != nil {
			session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS status persistence failed", request,
				"stage", "status_callback", "rp_reference", int(rpdu.reference), "error", err)
			session.sendLoggedDeliveryReport(request, buildRPError(rpdu.reference, 22), "rp_error")
			return
		}
		session.logInboundSMS(slog.LevelInfo, "IMS inbound SMS status report processed", request,
			"stage", "status_callback", "rp_reference", int(rpdu.reference),
			"status_code", status.StatusCode)
		session.sendLoggedDeliveryReport(request, []byte{0x02, rpdu.reference}, "rp_ack")

	case message.Direction == device.SMSDirectionReceived || decodeErr != nil:
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
			ICCID:                  session.request.Identity.ICCID,
			ModemIMEI:              strings.TrimSpace(session.request.Identity.IMEI),
			IMSI:                   session.request.Identity.IMSI,
			From:                   message.From,
			Text:                   message.Text,
			Timestamp:              receivedAt,
			ServiceCenterTimestamp: serviceCenterTimestamp,
			Encoding:               message.Encoding,
			Concat:                 message.Concat,
			RPReference:            int(rpdu.reference),
			CallID:                 callID,
			RawRPDU:                strings.ToUpper(hex.EncodeToString(payload)),
			RawTPDU:                strings.ToUpper(hex.EncodeToString(rpdu.tpdu)),
			DecodeError:            errorString(decodeErr),
		}
		err := session.invokeSMSCallback(received)
		if err != nil {
			session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS persistence failed", request,
				"stage", "sms_callback", "rp_reference", int(rpdu.reference), "error", err)
			session.sendLoggedDeliveryReport(request, buildRPError(rpdu.reference, 22), "rp_error")
			return
		}
		session.logInboundSMS(slog.LevelInfo, "IMS inbound SMS processed", request,
			"stage", "sms_callback", "payload_source", payloadSource,
			"rp_reference", int(rpdu.reference), "encoding", message.Encoding,
			"concatenated", message.Concat != nil, "decode_error", decodeErr != nil)
		session.sendLoggedDeliveryReport(request, []byte{0x02, rpdu.reference}, "rp_ack")

	default:
		session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS has unexpected TPDU direction", request,
			"stage", "tpdu", "rp_reference", int(rpdu.reference), "direction", message.Direction)
		session.sendLoggedDeliveryReport(request, buildRPError(rpdu.reference, 95), "rp_error")
	}
}

func (session *Session) invokeSMSCallback(received ReceivedSMS) error {
	if session.provider.config.OnSMS == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return session.provider.config.OnSMS(ctx, received)
}

func (session *Session) invokeSMSStatusCallback(status ReceivedSMSStatus) error {
	if session.provider.config.OnSMSStatus == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return session.provider.config.OnSMSStatus(ctx, status)
}

func intPtrValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func extractSMSPayload(request *sipRequest) ([]byte, string, error) {
	if request == nil {
		return nil, "", errors.New("ims: SMS MESSAGE is nil")
	}
	mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(request.value("Content-Type")))
	if err != nil {
		return nil, "", fmt.Errorf("ims: parse SMS Content-Type: %w", err)
	}
	if strings.EqualFold(mediaType, smsContentType) {
		payload, decodeErr := decodeSMSTransfer(request.Body, request.value("Content-Transfer-Encoding"))
		return payload, smsContentType, decodeErr
	}
	if !strings.EqualFold(mediaType, "multipart/mixed") {
		return nil, "", fmt.Errorf("ims: unsupported SMS Content-Type %q", mediaType)
	}
	boundary := strings.TrimSpace(parameters["boundary"])
	if boundary == "" {
		return nil, "", errors.New("ims: multipart SMS has no boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(request.Body), boundary)
	for {
		part, nextErr := reader.NextRawPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, "", fmt.Errorf("ims: read multipart SMS: %w", nextErr)
		}
		partType, _, parseErr := mime.ParseMediaType(strings.TrimSpace(part.Header.Get("Content-Type")))
		if parseErr != nil || !strings.EqualFold(partType, smsContentType) {
			_ = part.Close()
			continue
		}
		body, readErr := io.ReadAll(part)
		_ = part.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("ims: read multipart SMS payload: %w", readErr)
		}
		payload, decodeErr := decodeSMSTransfer(body, part.Header.Get("Content-Transfer-Encoding"))
		return payload, "multipart/mixed", decodeErr
	}
	return nil, "", errors.New("ims: multipart MESSAGE omitted application/vnd.3gpp.sms payload")
}

func decodeSMSTransfer(body []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "binary", "8bit":
		return append([]byte(nil), body...), nil
	case "base64":
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(body)))
		if err != nil {
			return nil, fmt.Errorf("ims: decode base64 SMS payload: %w", err)
		}
		return decoded, nil
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err != nil {
			return nil, fmt.Errorf("ims: decode quoted-printable SMS payload: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("ims: unsupported SMS Content-Transfer-Encoding %q", encoding)
	}
}

// encodeUSSDBody encodes a USSD string for a TS 24.390 application/vnd.3gpp.ussd
// body. To avoid carrier-specific GSM-7 packing conventions the body is always
// UTF-16 (big-endian) with DCS 0x48, which every USSI-capable P-CSCF accepts.
func encodeUSSDBody(text string) ([]byte, *int, error) {
	dcs := 0x48
	if text == "" {
		return nil, &dcs, nil
	}
	encoded := utf16.Encode([]rune(text))
	body := make([]byte, 0, len(encoded)*2)
	for _, unit := range encoded {
		body = append(body, byte(unit>>8), byte(unit))
	}
	return body, &dcs, nil
}

// decodeUSSDBody reverses encodeUSSDBody using the data coding scheme carried
// alongside the USSD string. DCS 0x00/0x0F => GSM 7-bit default alphabet
// (unpacked one code per byte, as some carriers send); 0x48 => UCS2/UTF-16.
// Any other DCS is treated as raw bytes.
func decodeUSSDBody(body []byte, dcs int) string {
	switch dcs {
	case 0x00, 0x0F:
		if decoded, ok := device.DecodeGSM7Septets(string(body)); ok {
			return decoded
		}
	}
	if dcs == 0x48 && len(body) > 0 && len(body)%2 == 0 {
		units := make([]uint16, 0, len(body)/2)
		for index := 0; index < len(body); index += 2 {
			units = append(units, uint16(body[index])<<8|uint16(body[index+1]))
		}
		return string(utf16.Decode(units))
	}
	return string(body)
}

// processUSSIMessage decodes a network-originated USSD MESSAGE and hands it to
// the OnUSSD callback. Unlike SMS there is no RP-ACK transport, so the 200 OK
// has already been sent by handleSIPRequest and this routine only logs callback
// failures.
func (session *Session) processUSSIMessage(request *sipRequest) {
	body, dcs, text, err := extractUSSDBody(request)
	if err != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS inbound USSD decode failed", request,
			"stage", "mime", "error", err)
		return
	}
	callID := strings.TrimSpace(request.value("Call-ID"))
	received := ReceivedUSSD{
		MessageID: fmt.Sprintf("ims-ussd:%s", callID),
		DeviceID:  session.request.DeviceID,
		ICCID:     session.request.Identity.ICCID,
		IMSI:      session.request.Identity.IMSI,
		From:      firstURI(request.value("P-Asserted-Identity")),
		Text:      text,
		DCS:       dcs,
		Status:    "final",
		Timestamp: time.Now().UTC(),
		CallID:    callID,
		RawBody:   strings.ToUpper(hex.EncodeToString(body)),
	}
	if session.provider.config.OnUSSD != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = session.provider.config.OnUSSD(ctx, received)
		cancel()
	}
	if err != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS inbound USSD callback failed", request,
			"stage", "ussd_callback", "error", err)
		return
	}
	session.logInboundSMS(slog.LevelInfo, "IMS inbound USSD processed", request,
		"stage", "ussd_callback", "dcs", dcsPointerToInt(dcs))
}

func dcsPointerToInt(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

// extractUSSDBody decodes a TS 24.390 USSD body. The body is a sequence of
// information elements; the common form is an optional language/network
// indicator followed by the USSD string with its DCS. We scan for a component
// whose length leaves a trailing DCS+string pair, returning the string, its
// DCS, and the raw bytes.
func extractUSSDBody(request *sipRequest) (raw []byte, dcs *int, text string, err error) {
	if request == nil {
		return nil, nil, "", errors.New("ims: USSD MESSAGE is nil")
	}
	body, decodeErr := decodeSMSTransfer(request.Body, request.value("Content-Transfer-Encoding"))
	if decodeErr != nil {
		return nil, nil, "", fmt.Errorf("ims: decode USSD body: %w", decodeErr)
	}
	raw, dcs, text = extractUSSDString(body)
	return raw, dcs, text, nil
}

// extractUSSDString walks the TS 24.390 information elements looking for the
// USSD string component: [length][DCS][octets...]. A leading 0xAB language
// indicator pair is skipped. If no structured component is found, the whole
// body is treated as a DCS 0x0F string.
func extractUSSDString(body []byte) (raw []byte, dcs *int, text string) {
	for offset := 0; offset+1 < len(body); {
		if body[offset] == 0xAB {
			// Language/network indicator: [0xAB][length of language].
			if offset+1 >= len(body) {
				break
			}
			skip := int(body[offset+1])
			offset += 2 + skip
			continue
		}
		// USSD string component: [length][DCS][octets...], length counts
		// everything after the length byte (DCS + string octets).
		length := int(body[offset])
		if length < 1 || offset+1+length > len(body) {
			break
		}
		dcsValue := int(body[offset+1])
		stringBytes := body[offset+2 : offset+1+length]
		dcs = &dcsValue
		return body, dcs, decodeUSSDBody(stringBytes, dcsValue)
	}
	zero := 0x0F
	return body, &zero, decodeUSSDBody(body, zero)
}

// SendUSSI submits a USSD dialog turn over IMS. The first turn carries the
// service code in request.Code; a follow-up turn on an open dialog carries the
// menu reply in request.Input. USSI does not require the +g.3gpp.smsip contact
// to be confirmed — only IMS registration.
func (session *Session) SendUSSI(ctx context.Context, request vowifi.USSISubmitRequest) (vowifi.USSISubmitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
	// TS 24.390 §5.2.1: [language indicator]? [length][DCS][USSD string].
	// The length byte counts the DCS plus the string octets that follow it.
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
	// A 2xx response may carry the network's reply in the same MESSAGE body.
	text, replyDCS := session.parseUSSIReply(response)
	result.Text = text
	result.DCS = replyDCS
	result.Status = "final"
	result.Continueable = false
	result.Raw = strings.ToUpper(hex.EncodeToString(message))
	if result.Status == "" {
		result.Status = "final"
	}
	result.SubmissionStatus = "accepted_by_ims"
	return result, nil
}

// parseUSSIReply decodes the USSD body of a 2xx response when the network
// returned the dialog reply inline. A missing body is a final empty reply.
func (session *Session) parseUSSIReply(response *sipResponse) (string, *int) {
	if response == nil || len(response.Body) == 0 {
		return "", nil
	}
	if !supportsUSSIContentType(response.value("Content-Type")) {
		return "", nil
	}
	_, dcs, text := extractUSSDString(response.Body)
	return text, dcs
}

func (session *Session) ussiTarget() string {
	if domain := strings.TrimSpace(session.identity.domain); domain != "" {
		return "sip:" + domain
	}
	return session.identity.public
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (session *Session) logInboundSMS(level slog.Level, message string, request *sipRequest, attributes ...any) {
	logger := slog.Default()
	if session != nil && session.provider != nil && session.provider.config.Logger != nil {
		logger = session.provider.config.Logger
	}
	base := []any{"category", "sms", "subsystem", "ims", "device_id", session.request.DeviceID}
	if request != nil {
		base = append(base,
			"call_id", strings.TrimSpace(request.value("Call-ID")),
			"content_type", strings.TrimSpace(request.value("Content-Type")),
			"body_bytes", len(request.Body),
		)
	}
	logger.Log(context.Background(), level, message, append(base, attributes...)...)
}

func (session *Session) sendLoggedDeliveryReport(request *sipRequest, report []byte, reportType string) {
	if err := session.sendDeliveryReport(request, report); err != nil {
		session.logInboundSMS(slog.LevelWarn, "IMS inbound SMS delivery report failed", request,
			"stage", "delivery_report", "report_type", reportType, "error", err)
		return
	}
	session.logInboundSMS(slog.LevelDebug, "IMS inbound SMS delivery report sent", request,
		"stage", "delivery_report", "report_type", reportType)
}

func (session *Session) sendDeliveryReport(request *sipRequest, report []byte) error {
	target := firstURI(request.value("P-Asserted-Identity"))
	if target == "" {
		target = firstURI(request.value("From"))
	}
	if target == "" {
		return errors.New("ims: SMS MESSAGE omitted a delivery-report target")
	}
	response, err := session.sendSIPMessage(
		context.Background(),
		target,
		report,
		strings.TrimSpace(request.value("Call-ID")),
	)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ims: SMS delivery report returned SIP %d", response.StatusCode)
	}
	return nil
}

func (session *Session) SendSMS(ctx context.Context, request vowifi.SMSSubmitRequest) (vowifi.SMSSubmitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session.smsMu.Lock()
	defer session.smsMu.Unlock()

	session.mu.Lock()
	if session.closed || !session.evidence.Registered || !session.smsCapabilityReady() {
		session.mu.Unlock()
		return vowifi.SMSSubmitResult{}, vowifi.ErrSMSNotReady
	}
	smsc := strings.TrimSpace(session.request.Identity.SMSC)
	session.mu.Unlock()
	smscSource := "sim"
	if smsc == "" {
		smscSource = "sim_reader"
		reader, ok := session.provider.aka.(smsCenterReader)
		var readErr error
		if ok {
			smsc, readErr = reader.ReadSMSCenter(ctx, session.request.DeviceID)
		}
		if strings.TrimSpace(smsc) == "" {
			smsc = smsCenterForIdentity(session.provider.config, session.request.Identity)
			smscSource = "plmn_fallback"
		}
		if strings.TrimSpace(smsc) == "" {
			smsc = session.provider.config.SMSCenter
			smscSource = "configured_fallback"
		}
		if strings.TrimSpace(smsc) == "" {
			return vowifi.SMSSubmitResult{}, errors.Join(ErrSMSCUnavailable, readErr)
		}
		session.mu.Lock()
		session.request.Identity.SMSC = smsc
		session.mu.Unlock()
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
	session.logOutboundSMS(slog.LevelInfo, "IMS outbound SMS submission started",
		"stage", "prepare", "parts", len(parts), "smsc_source", smscSource,
		"recipient_type", smsRecipientType(parts[0].To))
	psi := "tel:" + normalizeE164(smsc)
	for _, part := range parts {
		reference := session.allocateRPReference()
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
		response, sendErr := session.sendSIPMessage(ctx, psi, rpdu, "")
		partResult := vowifi.SMSSubmitPart{
			Part: part.Part, Total: part.Total, Reference: int(reference), SubmittedAt: time.Now().UTC(),
		}
		if response != nil {
			partResult.SIPCode = response.StatusCode
		}
		if sendErr == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
			partResult.Accepted = true
			partResult.SubmissionStatus = "accepted_by_ims"
			result.PartsAccepted++
		} else {
			partResult.SubmissionStatus = "rejected_by_ims"
		}
		result.PartResults = append(result.PartResults, partResult)
		if sendErr != nil {
			session.logOutboundSMS(slog.LevelWarn, "IMS outbound SMS submission failed",
				"stage", "sip_transaction", "part", part.Part,
				"rp_reference", int(reference), "error", sendErr)
			result.SubmissionStatus = "failed"
			return result, sendErr
		}
		if !partResult.Accepted {
			session.logOutboundSMS(slog.LevelWarn, "IMS outbound SMS was rejected",
				"stage", "sip_response", "part", part.Part,
				"rp_reference", int(reference), "sip_status", response.StatusCode)
			result.SubmissionStatus = "rejected"
			return result, fmt.Errorf("%w: SIP %d", ErrSMSRejected, response.StatusCode)
		}
	}
	result.AllPartsAccepted = true
	result.SubmissionStatus = "accepted_by_ims"
	session.logOutboundSMS(slog.LevelInfo, "IMS outbound SMS submission accepted",
		"stage", "sip_response", "parts", result.PartsAccepted)
	return result, nil
}

func smsCenterForIdentity(config Config, identity vowifi.SIMIdentity) string {
	if identitySMSC := strings.TrimSpace(identity.SMSC); identitySMSC != "" {
		return identitySMSC
	}
	plmn := strings.TrimSpace(identity.HomeMCC) + strings.TrimSpace(identity.HomeMNC)
	if configured := strings.TrimSpace(config.SMSCenterByPLMN[plmn]); configured != "" {
		return configured
	}
	return strings.TrimSpace(vowifi.ResolveCarrierProfile(identity).SMSCenter)
}

func smsRecipientType(recipient string) string {
	recipient = strings.TrimSpace(recipient)
	digits := strings.TrimPrefix(recipient, "+")
	switch {
	case strings.HasPrefix(recipient, "+"):
		return "international"
	case len(digits) <= 6:
		return "short_code"
	default:
		return "national"
	}
}

func (session *Session) logOutboundSMS(level slog.Level, message string, attributes ...any) {
	logger := slog.Default()
	if session != nil && session.provider != nil && session.provider.config.Logger != nil {
		logger = session.provider.config.Logger
	}
	plmn := strings.TrimSpace(session.request.Identity.HomeMCC) + strings.TrimSpace(session.request.Identity.HomeMNC)
	base := []any{
		"category", "sms",
		"subsystem", "ims",
		"device_id", session.request.DeviceID,
		"home_plmn", plmn,
		"transport", session.transport,
		"security", session.effectiveSecurityMode(),
	}
	logger.Log(context.Background(), level, message, append(base, attributes...)...)
}

func (session *Session) allocateRPReference() byte {
	session.mu.Lock()
	defer session.mu.Unlock()
	value := session.nextRPReference
	session.nextRPReference++
	return value
}

func (session *Session) sendSIPMessage(
	ctx context.Context,
	target string,
	body []byte,
	inReplyTo string,
) (*sipResponse, error) {
	return session.sendSIPMessageWith(ctx, target, body, inReplyTo, smsContentType, "smsip")
}

// sendSIPMessageWith is the parameterized MESSAGE transaction used by both SMS
// and USSI. acceptContactTag is the 3gpp feature tag (e.g. "smsip" or "ussd")
// advertised via Accept-Contact; pass an empty string to omit the header.
func (session *Session) sendSIPMessageWith(
	ctx context.Context,
	target string,
	body []byte,
	inReplyTo string,
	contentType string,
	acceptContactTag string,
) (*sipResponse, error) {
	callToken, err := randomHex(18)
	if err != nil {
		return nil, err
	}
	branch, err := randomHex(12)
	if err != nil {
		return nil, err
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
	)
	if pani := session.pAccessNetworkInfo(); pani != "" {
		lines = append(lines, "P-Access-Network-Info: "+pani)
	}
	if acceptContactTag != "" {
		lines = append(lines, "Accept-Contact: *;+g.3gpp."+acceptContactTag)
	}
	lines = append(lines,
		"Request-Disposition: no-fork",
		"Allow: MESSAGE",
		"User-Agent: "+session.imsUserAgent(),
	)
	if inReplyTo != "" {
		lines = append(lines, "In-Reply-To: "+inReplyTo)
	}
	lines = append(lines,
		"Content-Type: "+contentType,
		"Content-Transfer-Encoding: binary",
		"Content-Length: "+strconv.Itoa(len(body)),
		"", "",
	)
	request := append([]byte(strings.Join(lines, "\r\n")), body...)
	session.logOutboundSMS(slog.LevelDebug, "IMS SIP MESSAGE transaction started",
		"stage", "sip_send", "call_id", callID, "cseq", cseq,
		"body_bytes", len(body), "service_routes", len(serviceRoutes))
	response, exchangeErr := session.exchangeRuntime(
		ctx,
		request,
		sipTransactionKey{callID: callID, cseq: cseq, method: "MESSAGE"},
	)
	if exchangeErr != nil {
		session.logOutboundSMS(slog.LevelWarn, "IMS SIP MESSAGE transaction failed",
			"stage", "sip_transaction", "call_id", callID, "cseq", cseq, "error", exchangeErr)
		return response, exchangeErr
	}
	session.logOutboundSMS(slog.LevelDebug, "IMS SIP MESSAGE response received",
		"stage", "sip_response", "call_id", callID, "cseq", cseq,
		"sip_status", response.StatusCode)
	return response, nil
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
	tpdu        []byte
}

func parseRPDU(data []byte) (rpMessage, error) {
	if len(data) < 2 {
		return rpMessage{}, errors.New("ims: RPDU is truncated")
	}
	result := rpMessage{messageType: data[0] & 0x07, reference: data[1]}
	if result.messageType != 1 {
		return result, nil
	}
	index := 2
	for count := 0; count < 2; count++ {
		if index >= len(data) {
			return rpMessage{}, errors.New("ims: RP-DATA address is truncated")
		}
		length := int(data[index])
		index++
		if length > len(data)-index {
			return rpMessage{}, errors.New("ims: RP-DATA address length is invalid")
		}
		index += length
	}
	if index >= len(data) {
		return rpMessage{}, errors.New("ims: RP-DATA omitted user data")
	}
	length := int(data[index])
	index++
	if length == 0 || length > len(data)-index {
		return rpMessage{}, errors.New("ims: RP-DATA user-data length is invalid")
	}
	result.tpdu = append([]byte(nil), data[index:index+length]...)
	return result, nil
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
	value = strings.TrimSpace(value)
	var result strings.Builder
	for index, character := range value {
		if character >= '0' && character <= '9' || (index == 0 && character == '+') {
			result.WriteRune(character)
		}
	}
	return result.String()
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
var _ vowifi.USSISender = (*Session)(nil)
