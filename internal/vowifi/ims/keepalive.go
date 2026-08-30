package ims

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	sipFlowKeepaliveMin      = 95 * time.Second
	sipFlowKeepaliveMax      = 120 * time.Second
	sipFlowKeepaliveJitterLo = 80
	// RFC 5626 recommends a 95-120 second TCP keepalive range when no
	// Flow-Timer is supplied.  108 seconds is the rounded midpoint used when
	// answering an inbound Via with a bare RFC 6223 keep parameter.
	sipInboundKeepaliveIntervalSeconds = 108
)

// hasSIPOptionTag reports whether a comma-separated SIP option-tag header
// contains the requested token.
func hasSIPOptionTag(values []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return false
	}
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), wanted) {
				return true
			}
		}
	}
	return false
}

type sipViaKeepaliveDetails struct {
	present    bool
	negotiated bool
	rawValue   string
	interval   time.Duration
}

// parseSIPViaKeepaliveDetails parses the first RFC 6223 keep parameter from
// the Via header returned with a REGISTER response. A positive value is the
// peer's recommended interval in seconds. A zero value means the peer
// accepted keep-alives without recommending an interval. A bare keep
// parameter is only the sender's offer and does not complete negotiation.
func parseSIPViaKeepaliveDetails(values []string) sipViaKeepaliveDetails {
	for _, value := range values {
		parameters := strings.Split(value, ";")
		for _, parameter := range parameters[1:] {
			name, rawValue, hasValue := strings.Cut(strings.TrimSpace(parameter), "=")
			if !strings.EqualFold(strings.TrimSpace(name), "keep") {
				continue
			}
			if !hasValue {
				return sipViaKeepaliveDetails{present: true, rawValue: "keep"}
			}
			seconds, err := strconv.ParseInt(strings.TrimSpace(rawValue), 10, 32)
			if err != nil || seconds < 0 {
				return sipViaKeepaliveDetails{
					present:  true,
					rawValue: "keep=" + strings.TrimSpace(rawValue),
				}
			}
			return sipViaKeepaliveDetails{
				present:    true,
				negotiated: true,
				rawValue:   "keep=" + strings.TrimSpace(rawValue),
				interval:   time.Duration(seconds) * time.Second,
			}
		}
	}
	return sipViaKeepaliveDetails{}
}

// parseSIPViaKeepalive preserves the compact parser API used by the runtime
// and tests while keeping the raw response value available to diagnostics.
func parseSIPViaKeepalive(values []string) (bool, time.Duration) {
	details := parseSIPViaKeepaliveDetails(values)
	return details.present, details.interval
}

func effectiveSIPKeepalivePolicy(transport string, negotiated bool, interval time.Duration) (adopted bool, intervalSeconds int, policy string) {
	if transport != "tcp" {
		return false, 0, "disabled_for_non_tcp"
	}
	if !negotiated {
		return false, 0, "not_negotiated"
	}
	if interval > 0 {
		return true, int(interval / time.Second), "peer_interval_with_jitter"
	}
	return true, 0, "local_default_range"
}

// negotiateInboundSIPViaKeepalive updates only the topmost Via in an inbound
// request.  RFC 6223 uses a bare keep parameter in a request and a keep value
// in the response.  A Via without keep, or one that already has a value, is
// left unchanged so existing response behavior remains the default.
func (session *Session) negotiateInboundSIPViaKeepalive(request *sipRequest) {
	if session == nil || request == nil || request.Headers == nil || strings.EqualFold(request.Method, "ACK") {
		return
	}
	via := request.values("Via")
	if len(via) == 0 {
		return
	}
	originalVia := append([]string(nil), via...)
	keepPresent, _ := parseSIPViaKeepalive(via)
	if !keepPresent {
		return
	}
	updated, negotiated := addSIPViaKeepaliveInterval(via[0], sipInboundKeepaliveIntervalSeconds)
	if !negotiated {
		return
	}
	via[0] = updated
	request.Headers["via"] = via
	session.imsLogger().Debug("IMS inbound SIP keep-alive negotiated",
		"category", "ims",
		"subsystem", "sip",
		"device_id", session.request.DeviceID,
		"direction", "inbound",
		"stage", "inbound_keepalive_negotiation",
		"method", request.Method,
		"transport", session.transport,
		"interval_seconds", sipInboundKeepaliveIntervalSeconds,
		"via_before_raw", originalVia,
		"via_after_raw", via,
	)
}

func addSIPViaKeepaliveInterval(value string, seconds int) (string, bool) {
	first := value
	remainder := ""
	if index := strings.IndexByte(value, ','); index >= 0 {
		first, remainder = value[:index], value[index:]
	}
	parts := strings.Split(first, ";")
	for index := 1; index < len(parts); index++ {
		name, _, hasValue := strings.Cut(strings.TrimSpace(parts[index]), "=")
		if !strings.EqualFold(strings.TrimSpace(name), "keep") || hasValue {
			continue
		}
		parts[index] = fmt.Sprintf("keep=%d", seconds)
		return strings.Join(parts, ";") + remainder, true
	}
	return value, false
}

func sipFlowRecoveryDelay(consecutiveFailures int) time.Duration {
	if consecutiveFailures < 0 {
		consecutiveFailures = 0
	}
	upper := sipFlowRecoveryBase
	for index := 0; index < consecutiveFailures && upper < sipFlowRecoveryMax; index++ {
		if upper > sipFlowRecoveryMax/2 {
			upper = sipFlowRecoveryMax
			break
		}
		upper *= 2
	}
	if upper > sipFlowRecoveryMax {
		upper = sipFlowRecoveryMax
	}
	lower := upper / 2
	return randomDuration(lower, upper)
}

func randomDuration(lower, upper time.Duration) time.Duration {
	if upper <= lower {
		return lower
	}
	span := int64(upper - lower)
	random, err := cryptorand.Int(cryptorand.Reader, big.NewInt(span+1))
	if err != nil {
		return lower + time.Duration(span/2)
	}
	return lower + time.Duration(random.Int64())
}

func (session *Session) sipCRLFKeepaliveEnabled() bool {
	if session == nil || session.transport != "tcp" {
		return false
	}
	session.keepaliveMu.Lock()
	negotiated := session.keepaliveNegotiated
	session.keepaliveMu.Unlock()
	return negotiated
}

func (session *Session) flowKeepaliveDelay() time.Duration {
	session.keepaliveMu.Lock()
	keepaliveInterval := session.keepaliveInterval
	session.keepaliveMu.Unlock()
	if keepaliveInterval > 0 {
		lower := keepaliveInterval * sipFlowKeepaliveJitterLo / 100
		if lower < time.Second {
			lower = time.Second
		}
		return randomDuration(lower, keepaliveInterval)
	}
	return randomDuration(sipFlowKeepaliveMin, sipFlowKeepaliveMax)
}

func (session *Session) startSIPFlowKeepalive() {
	if !session.sipCRLFKeepaliveEnabled() {
		return
	}
	session.keepaliveMu.Lock()
	if session.keepaliveRunning {
		session.keepaliveMu.Unlock()
		return
	}
	keepaliveInterval := session.keepaliveInterval
	keepaliveNegotiated := session.keepaliveNegotiated
	adopted, intervalSeconds, policy := effectiveSIPKeepalivePolicy(
		session.transport,
		keepaliveNegotiated,
		keepaliveInterval,
	)
	session.keepaliveSentCount.Store(0)
	session.keepalivePeerPingCount.Store(0)
	session.keepalivePongSentCount.Store(0)
	session.keepaliveRunning = true
	session.keepaliveMu.Unlock()
	session.imsLogger().Debug("IMS SIP CRLF keepalive started",
		"category", "ims",
		"subsystem", "sip",
		"device_id", session.request.DeviceID,
		"stage", "flow_keepalive",
		"transport", session.transport,
		"keepalive_negotiated", adopted,
		"ue_keep_requested", true,
		"ue_keep_value", "keep",
		"peer_interval_seconds", int(keepaliveInterval/time.Second),
		"keepalive_policy", policy,
		"effective_interval_seconds", intervalSeconds,
	)
	session.receiveDone.Add(1)
	go session.runSIPFlowKeepalive()
}

func (session *Session) runSIPFlowKeepalive() {
	defer session.receiveDone.Done()
	defer func() {
		session.keepaliveMu.Lock()
		session.keepaliveRunning = false
		session.keepaliveMu.Unlock()
		sentCount := session.keepaliveSentCount.Load()
		peerPingCount := session.keepalivePeerPingCount.Load()
		pongSentCount := session.keepalivePongSentCount.Load()
		session.imsLogger().Debug("IMS SIP CRLF keepalive stopped",
			"category", "ims",
			"subsystem", "sip",
			"device_id", session.request.DeviceID,
			"stage", "flow_keepalive",
			"transport", session.transport,
			"sent_count", sentCount,
			"peer_ping_count", peerPingCount,
			"pong_sent_count", pongSentCount,
		)
	}()

	ctx := session.refreshContext
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if ctx.Err() != nil || session.isClosed() {
			return
		}
		connection, _, changed, err := session.currentOutboundFlow()
		if err != nil {
			if !waitForSIPFlowEvent(ctx, changed, time.Second) {
				return
			}
			continue
		}
		if !session.sipCRLFKeepaliveEnabled() {
			return
		}

		timer := time.NewTimer(session.flowKeepaliveDelay())
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-changed:
			if !timer.Stop() {
				<-timer.C
			}
			continue
		case <-timer.C:
		}
		if !session.isCurrentOutboundFlow(connection) {
			continue
		}
		if !session.sipCRLFKeepaliveEnabled() {
			return
		}

		session.writeMu.Lock()
		_, writeErr := connection.Write([]byte("\r\n\r\n"))
		session.writeMu.Unlock()
		if writeErr != nil {
			session.failSIPFlow(connection, "keepalive_write", writeErr)
			continue
		}
		session.keepaliveSentCount.Add(1)
	}
}

func waitForSIPFlowEvent(ctx context.Context, changed <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-changed:
		return true
	case <-timer.C:
		return true
	}
}

func (session *Session) handleSIPCRLF(connection net.Conn, ping bool) error {
	if !ping {
		return nil
	}
	session.keepaliveMu.Lock()
	keepaliveRunning := session.keepaliveRunning
	session.keepaliveMu.Unlock()
	if keepaliveRunning {
		session.keepalivePeerPingCount.Add(1)
	}
	session.writeMu.Lock()
	_, err := connection.Write([]byte("\r\n"))
	session.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("ims: send SIP CRLF pong: %w", err)
	}
	if keepaliveRunning {
		session.keepalivePongSentCount.Add(1)
	}
	return nil
}

func (session *Session) failSIPFlow(connection net.Conn, reason string, err error) {
	if session == nil || session.isClosed() {
		return
	}
	if session.detachOutboundFlow(connection) {
		recoveryScheduled := session.outboundRegistrationConfirmed()
		session.imsLogger().Warn("IMS SIP flow failed",
			"category", "ims", "subsystem", "sip", "stage", "flow_keepalive",
			"reason", reason, "error", err, "recovery_scheduled", recoveryScheduled)
		if recoveryScheduled {
			session.startSIPFlowRecovery()
		}
	}
}

func appendSIPOptionTag(header, option string) string {
	header = strings.TrimSpace(header)
	option = strings.TrimSpace(option)
	if option == "" || hasSIPOptionTag([]string{header}, option) {
		return header
	}
	if header == "" {
		return option
	}
	return header + ", " + option
}

func (session *Session) appendSupportedOptions(header string) string {
	// RFC 5626 outbound is negotiated only in REGISTER. It is not a
	// capability to repeat on MESSAGE, INVITE, or other dialog requests.
	return appendSIPOptionTag(header, "path")
}
