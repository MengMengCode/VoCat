package ims

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	rtpClockRate                    = 8000
	rtpPacketSamples                = 160
	rtpTelephoneEventDefaultPayload = 101
	rtcpReportInterval              = 5 * time.Second
)

type rtpMedia struct {
	conn     *net.UDPConn
	rtcpConn *net.UDPConn

	mu                    sync.RWMutex
	remote                *net.UDPAddr
	rtcpRemote            *net.UDPAddr
	codec                 string
	payloadType           byte
	telephoneEvent        bool
	telephoneEventPayload byte
	telephoneEventFmtp    string
	telephoneEvents       [16]bool
	localDirection        string
	sdpSessionID          uint64
	sdpVersion            uint64
	rtcpCNAME             string
	lastDTMFEvent         byte
	lastDTMFTimestamp     uint32
	haveLastDTMF          bool

	writeMu   sync.Mutex
	pending   []int16
	sequence  uint16
	timestamp uint32
	ssrc      uint32
	nextWrite time.Time

	downlink chan []int16
	dtmf     chan byte
	closed   chan struct{}
	close    sync.Once
}

func newRTPMedia(local net.IP) (*rtpMedia, error) {
	address := &net.UDPAddr{IP: local, Port: 0}
	connection, err := net.ListenUDP("udp", address)
	if err != nil {
		return nil, fmt.Errorf("ims: open RTP socket: %w", err)
	}
	rtcpConnection, err := net.ListenUDP("udp", address)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("ims: open RTCP socket: %w", err)
	}
	seed := make([]byte, 10)
	if _, err := io.ReadFull(cryptorand.Reader, seed); err != nil {
		_ = connection.Close()
		_ = rtcpConnection.Close()
		return nil, fmt.Errorf("ims: initialize RTP state: %w", err)
	}
	media := &rtpMedia{
		conn: connection, rtcpConn: rtcpConnection, sequence: binary.BigEndian.Uint16(seed[:2]),
		timestamp: binary.BigEndian.Uint32(seed[2:6]), ssrc: binary.BigEndian.Uint32(seed[6:]),
		downlink: make(chan []int16, 64), dtmf: make(chan byte, 16), closed: make(chan struct{}),
	}
	media.sdpSessionID = binary.BigEndian.Uint64(seed[:8]) & 0x7fffffffffffffff
	if media.sdpSessionID == 0 {
		media.sdpSessionID = 1
	}
	media.sdpVersion = 1
	media.localDirection = "sendrecv"
	media.rtcpCNAME = fmt.Sprintf("vocat-%08x@localhost", media.ssrc)
	go media.receive()
	go media.receiveRTCP()
	go media.reportRTCP()
	return media, nil
}

func (media *rtpMedia) Codec() string {
	media.mu.RLock()
	defer media.mu.RUnlock()
	return media.codec
}

func (media *rtpMedia) ready() bool {
	media.mu.RLock()
	defer media.mu.RUnlock()
	return media.remote != nil && media.codec != ""
}

func (media *rtpMedia) offerSDP(local net.IP) []byte {
	return media.buildSDP(local, "8 0 101", []string{
		"a=rtpmap:8 PCMA/8000",
		"a=rtpmap:0 PCMU/8000",
		"a=rtpmap:101 telephone-event/8000",
		"a=fmtp:101 0-15",
	})
}

func (media *rtpMedia) answerSDP(local net.IP) []byte {
	media.mu.RLock()
	codec, payload := media.codec, media.payloadType
	hasTelephoneEvent := media.telephoneEvent
	telephonePayload := media.telephoneEventPayload
	telephoneFmtp := media.telephoneEventFmtp
	media.mu.RUnlock()
	if codec == "" {
		return media.offerSDP(local)
	}
	formats := strconv.Itoa(int(payload))
	attributes := []string{
		fmt.Sprintf("a=rtpmap:%d %s/8000", payload, codec),
	}
	if hasTelephoneEvent {
		formats += " " + strconv.Itoa(int(telephonePayload))
		attributes = append(attributes,
			fmt.Sprintf("a=rtpmap:%d telephone-event/8000", telephonePayload),
			fmt.Sprintf("a=fmtp:%d %s", telephonePayload, telephoneFmtp),
		)
	}
	return media.buildSDP(local, formats, attributes)
}

func (media *rtpMedia) buildSDP(local net.IP, formats string, attributes []string) []byte {
	media.mu.RLock()
	sessionID, sessionVersion := media.sdpSessionID, media.sdpVersion
	direction := media.localDirection
	media.mu.RUnlock()
	if direction == "" {
		direction = "sendrecv"
	}
	if local == nil || local.IsUnspecified() {
		if udp, ok := media.conn.LocalAddr().(*net.UDPAddr); ok {
			local = udp.IP
		}
	}
	if local == nil || local.IsUnspecified() {
		local = net.IPv4zero
	}
	family := "IP4"
	if local.To4() == nil {
		family = "IP6"
	}
	port := media.conn.LocalAddr().(*net.UDPAddr).Port
	lines := []string{
		"v=0",
		fmt.Sprintf("o=- %d %d IN %s %s", sessionID, sessionVersion, family, local.String()),
		"s=VoCat",
		fmt.Sprintf("c=IN %s %s", family, local.String()),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %s", port, formats),
	}
	if media.rtcpConn != nil {
		rtcpPort := media.rtcpConn.LocalAddr().(*net.UDPAddr).Port
		lines = append(lines, fmt.Sprintf("a=rtcp:%d IN %s %s", rtcpPort, family, local.String()))
	}
	lines = append(lines, attributes...)
	lines = append(lines, "a=ptime:20", "a="+direction, "")
	return []byte(strings.Join(lines, "\r\n"))
}

func (media *rtpMedia) configureRemote(body []byte) error {
	description, err := parseAudioDescription(body)
	if err != nil {
		return err
	}
	var codec string
	var payload byte
	var offeredAMR bool
	var telephoneEvent bool
	var telephonePayload byte
	var telephoneFmtp string
	var telephoneEvents [16]bool
	for _, value := range description.formats {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 0 || parsed > 127 {
			continue
		}
		mapping := description.mappings[parsed]
		name := strings.ToUpper(mapping.name)
		if name == "" {
			switch parsed {
			case 0:
				name, mapping.clock = "PCMU", 8000
			case 8:
				name, mapping.clock = "PCMA", 8000
			}
		}
		if (name == "PCMA" || name == "PCMU") && mapping.clock == 8000 && codec == "" {
			codec, payload = name, byte(parsed)
		}
		if strings.HasPrefix(name, "AMR") {
			offeredAMR = true
		}
		if name == "TELEPHONE-EVENT" && mapping.clock == 8000 && !telephoneEvent {
			events, normalized := negotiatedTelephoneEvents(description.fmtp[parsed])
			if normalized != "" {
				telephoneEvent = true
				telephonePayload = byte(parsed)
				telephoneFmtp = normalized
				telephoneEvents = events
			}
		}
	}
	if codec == "" {
		if offeredAMR {
			return errors.New("ims: remote endpoint offered only AMR audio; AMR-NB/WB transcoding is unavailable")
		}
		return errors.New("ims: remote endpoint did not accept PCMA or PCMU audio")
	}
	media.mu.Lock()
	media.remote = &net.UDPAddr{IP: append(net.IP(nil), description.address...), Port: description.port}
	media.rtcpRemote = &net.UDPAddr{IP: append(net.IP(nil), description.rtcpAddress...), Port: description.rtcpPort}
	media.codec = codec
	media.payloadType = payload
	media.telephoneEvent = telephoneEvent
	media.telephoneEventPayload = telephonePayload
	media.telephoneEventFmtp = telephoneFmtp
	media.telephoneEvents = telephoneEvents
	media.localDirection = answerDirection(description.direction)
	media.sdpVersion++
	media.mu.Unlock()
	return nil
}

type rtpMapping struct {
	name  string
	clock int
}

type audioDescription struct {
	address     net.IP
	port        int
	formats     []string
	mappings    map[int]rtpMapping
	fmtp        map[int]string
	rtcpAddress net.IP
	rtcpPort    int
	direction   string
}

func parseAudioDescription(body []byte) (audioDescription, error) {
	var sessionIP, mediaIP net.IP
	var port int
	var formats []string
	mappings := make(map[int]rtpMapping)
	fmtp := make(map[int]string)
	var rtcpIP net.IP
	var rtcpPort int
	seenMedia := false
	audioSelected := false
	inSelectedAudio := false
	direction := "sendrecv"
	for _, raw := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m="):
			seenMedia = true
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			mediaPort := 0
			if len(fields) >= 2 {
				mediaPort, _ = strconv.Atoi(strings.Split(fields[1], "/")[0])
			}
			isAudio := len(fields) >= 4 && mediaPort > 0 && mediaPort <= 65535 &&
				strings.EqualFold(fields[0], "audio") && strings.HasPrefix(strings.ToUpper(fields[2]), "RTP/AVP")
			inSelectedAudio = isAudio && !audioSelected
			if inSelectedAudio {
				audioSelected = true
				port = mediaPort
				formats = append([]string(nil), fields[3:]...)
			}
		case strings.HasPrefix(line, "c="):
			fields := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(fields) >= 3 {
				ip := net.ParseIP(strings.Split(fields[2], "/")[0])
				if inSelectedAudio {
					mediaIP = ip
				} else if !seenMedia {
					sessionIP = ip
				}
			}
		case !seenMedia && isSDPDirection(line):
			direction = strings.ToLower(strings.TrimPrefix(line, "a="))
		case inSelectedAudio && strings.HasPrefix(strings.ToLower(line), "a=rtpmap:"):
			colon := strings.IndexByte(line, ':')
			fields := strings.Fields(line[colon+1:])
			if len(fields) == 2 {
				pt, parseErr := strconv.Atoi(fields[0])
				parts := strings.Split(fields[1], "/")
				clock := 0
				if len(parts) >= 2 {
					clock, _ = strconv.Atoi(parts[1])
				}
				if parseErr == nil && pt >= 0 && pt <= 127 && len(parts) >= 1 {
					mappings[pt] = rtpMapping{name: parts[0], clock: clock}
				}
			}
		case inSelectedAudio && strings.HasPrefix(strings.ToLower(line), "a=fmtp:"):
			colon := strings.IndexByte(line, ':')
			fields := strings.Fields(line[colon+1:])
			if len(fields) >= 2 {
				if pt, parseErr := strconv.Atoi(fields[0]); parseErr == nil && pt >= 0 && pt <= 127 {
					fmtp[pt] = strings.Join(fields[1:], " ")
				}
			}
		case inSelectedAudio && strings.HasPrefix(strings.ToLower(line), "a=rtcp:"):
			colon := strings.IndexByte(line, ':')
			fields := strings.Fields(line[colon+1:])
			if len(fields) >= 1 {
				rtcpPort, _ = strconv.Atoi(fields[0])
			}
			if len(fields) >= 4 {
				rtcpIP = net.ParseIP(strings.Split(fields[3], "/")[0])
			}
		case inSelectedAudio && isSDPDirection(line):
			direction = strings.ToLower(strings.TrimPrefix(line, "a="))
		}
	}
	if mediaIP == nil {
		mediaIP = sessionIP
	}
	if mediaIP == nil || port < 1 || port > 65535 || len(formats) == 0 {
		return audioDescription{}, errors.New("ims: remote SDP has no usable audio endpoint")
	}
	if rtcpIP == nil {
		rtcpIP = mediaIP
	}
	if rtcpPort == 0 && port < 65535 {
		rtcpPort = port + 1
	}
	if rtcpIP == nil || rtcpPort < 1 || rtcpPort > 65535 {
		return audioDescription{}, errors.New("ims: remote SDP has no usable RTCP endpoint")
	}
	return audioDescription{
		address: append(net.IP(nil), mediaIP...), port: port,
		formats: append([]string(nil), formats...), mappings: mappings, fmtp: fmtp,
		rtcpAddress: append(net.IP(nil), rtcpIP...), rtcpPort: rtcpPort, direction: direction,
	}, nil
}

func isSDPDirection(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "a=sendrecv", "a=sendonly", "a=recvonly", "a=inactive":
		return true
	default:
		return false
	}
}

func answerDirection(remote string) string {
	switch strings.ToLower(strings.TrimSpace(remote)) {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		return "sendonly"
	case "inactive":
		return "inactive"
	default:
		return "sendrecv"
	}
}

func negotiatedTelephoneEvents(value string) ([16]bool, string) {
	var allowed [16]bool
	value = strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	if value == "" {
		for event := range allowed {
			allowed[event] = true
		}
		return allowed, "0-15"
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		bounds := strings.SplitN(item, "-", 2)
		start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
		if err != nil {
			continue
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				continue
			}
		}
		if start < 0 {
			start = 0
		}
		if end > 15 {
			end = 15
		}
		for event := start; event <= end && event < len(allowed); event++ {
			allowed[event] = true
		}
	}
	var parts []string
	for start := 0; start < len(allowed); {
		if !allowed[start] {
			start++
			continue
		}
		end := start
		for end+1 < len(allowed) && allowed[end+1] {
			end++
		}
		if start == end {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, end))
		}
		start = end + 1
	}
	return allowed, strings.Join(parts, ",")
}

func parseAudioSDP(body []byte) (net.IP, int, []string, map[int]string, error) {
	description, err := parseAudioDescription(body)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	mappings := make(map[int]string, len(description.mappings))
	for payload, mapping := range description.mappings {
		mappings[payload] = mapping.name
	}
	return description.address, description.port, description.formats, mappings, nil
}

func (media *rtpMedia) ReadPCM(ctx context.Context) ([]int16, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-media.closed:
		return nil, io.EOF
	case samples := <-media.downlink:
		return samples, nil
	}
}

func (media *rtpMedia) WritePCM(samples []int16) error {
	media.mu.RLock()
	var remote *net.UDPAddr
	if media.remote != nil {
		copy := *media.remote
		remote = &copy
	}
	codec, payload, direction := media.codec, media.payloadType, media.localDirection
	media.mu.RUnlock()
	if remote == nil || codec == "" {
		return errors.New("ims: RTP media is not negotiated")
	}
	if direction == "recvonly" || direction == "inactive" {
		return errors.New("ims: remote SDP placed the local media sender on hold")
	}
	media.writeMu.Lock()
	defer media.writeMu.Unlock()
	media.pending = append(media.pending, samples...)
	for len(media.pending) >= rtpPacketSamples {
		now := time.Now()
		if media.nextWrite.IsZero() || now.After(media.nextWrite.Add(200*time.Millisecond)) {
			media.nextWrite = now
		}
		if delay := time.Until(media.nextWrite); delay > 0 {
			if err := waitMediaInterval(context.Background(), media.closed, delay); err != nil {
				return err
			}
		}
		packet := make([]byte, 12+rtpPacketSamples)
		packet[0], packet[1] = 0x80, payload
		binary.BigEndian.PutUint16(packet[2:4], media.sequence)
		binary.BigEndian.PutUint32(packet[4:8], media.timestamp)
		binary.BigEndian.PutUint32(packet[8:12], media.ssrc)
		for index, sample := range media.pending[:rtpPacketSamples] {
			if codec == "PCMA" {
				packet[12+index] = linearToALaw(sample)
			} else {
				packet[12+index] = linearToMuLaw(sample)
			}
		}
		if _, err := media.conn.WriteToUDP(packet, remote); err != nil {
			return fmt.Errorf("ims: send RTP: %w", err)
		}
		media.pending = media.pending[rtpPacketSamples:]
		media.sequence++
		media.timestamp += rtpPacketSamples
		media.nextWrite = media.nextWrite.Add(20 * time.Millisecond)
	}
	return nil
}

// SendDTMF emits one RFC 4733 telephone-event. It is intentionally exposed on
// the concrete media type only; callers must not claim DTMF support unless the
// peer actually negotiated a telephone-event payload in SDP.
func (media *rtpMedia) SendDTMF(digit byte, duration time.Duration) error {
	return media.SendDTMFContext(context.Background(), digit, duration)
}

func (media *rtpMedia) SendDTMFContext(ctx context.Context, digit byte, duration time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	event, ok := dtmfEventCode(digit)
	if !ok {
		return errors.New("ims: unsupported DTMF digit")
	}
	media.mu.RLock()
	var remote *net.UDPAddr
	if media.remote != nil {
		copy := *media.remote
		copy.IP = append(net.IP(nil), media.remote.IP...)
		remote = &copy
	}
	negotiated, payload := media.telephoneEvent, media.telephoneEventPayload
	allowed := media.telephoneEvents
	direction := media.localDirection
	media.mu.RUnlock()
	if remote == nil || !negotiated {
		return errors.New("ims: telephone-event DTMF was not negotiated")
	}
	if int(event) >= len(allowed) || !allowed[event] {
		return fmt.Errorf("ims: DTMF event %d was not allowed by the negotiated fmtp", event)
	}
	if direction == "recvonly" || direction == "inactive" {
		return errors.New("ims: remote SDP placed the local media sender on hold")
	}
	if duration < 40*time.Millisecond {
		duration = 40 * time.Millisecond
	}
	if duration > 5*time.Second {
		duration = 5 * time.Second
	}
	durationSamples := int(duration * rtpClockRate / time.Second)
	if durationSamples > 65535 {
		durationSamples = 65535
	}

	media.writeMu.Lock()
	defer media.writeMu.Unlock()
	eventTimestamp := media.timestamp
	media.timestamp += uint32(durationSamples)
	send := func(elapsed int, end bool, marker bool) error {
		packet := make([]byte, 16)
		packet[0] = 0x80
		packet[1] = payload
		if marker {
			packet[1] |= 0x80 // marker starts a new event
		}
		binary.BigEndian.PutUint16(packet[2:4], media.sequence)
		binary.BigEndian.PutUint32(packet[4:8], eventTimestamp)
		binary.BigEndian.PutUint32(packet[8:12], media.ssrc)
		packet[12] = event
		packet[13] = 10 // E bit plus a conservative volume value in dBm0
		if end {
			packet[13] |= 0x80
		}
		binary.BigEndian.PutUint16(packet[14:16], uint16(elapsed))
		if _, err := media.conn.WriteToUDP(packet, remote); err != nil {
			return fmt.Errorf("ims: send DTMF RTP event: %w", err)
		}
		media.sequence++
		return nil
	}
	elapsed := 0
	first := true
	for elapsed < durationSamples {
		if err := waitMediaInterval(ctx, media.closed, 20*time.Millisecond); err != nil {
			return err
		}
		elapsed += rtpPacketSamples
		if elapsed > durationSamples {
			elapsed = durationSamples
		}
		if err := send(elapsed, elapsed == durationSamples, first); err != nil {
			return err
		}
		first = false
	}
	// RFC 4733 recommends repeating the final event packet. Space the copies
	// on the same 20 ms cadence instead of emitting an instantaneous burst.
	for repeat := 0; repeat < 2; repeat++ {
		if err := waitMediaInterval(ctx, media.closed, 20*time.Millisecond); err != nil {
			return err
		}
		if err := send(durationSamples, true, false); err != nil {
			return err
		}
	}
	media.nextWrite = time.Now().Add(20 * time.Millisecond)
	return nil
}

func waitMediaInterval(ctx context.Context, closed <-chan struct{}, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-closed:
		return io.EOF
	case <-timer.C:
		return nil
	}
}

func (media *rtpMedia) ReadDTMF(ctx context.Context) (byte, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-media.closed:
		return 0, io.EOF
	case digit := <-media.dtmf:
		return digit, nil
	}
}

func dtmfEventCode(digit byte) (byte, bool) {
	switch {
	case digit >= '0' && digit <= '9':
		return digit - '0', true
	case digit == '*':
		return 10, true
	case digit == '#':
		return 11, true
	case digit >= 'A' && digit <= 'D':
		return 12 + digit - 'A', true
	case digit >= 'a' && digit <= 'd':
		return 12 + digit - 'a', true
	default:
		return 0, false
	}
}

func dtmfEventDigit(event byte) (byte, bool) {
	switch {
	case event <= 9:
		return '0' + event, true
	case event == 10:
		return '*', true
	case event == 11:
		return '#', true
	case event >= 12 && event <= 15:
		return 'A' + event - 12, true
	default:
		return 0, false
	}
}

func (media *rtpMedia) receive() {
	packet := make([]byte, 2048)
	for {
		count, source, err := media.conn.ReadFromUDP(packet)
		if err != nil {
			return
		}
		media.mu.RLock()
		var remote *net.UDPAddr
		if media.remote != nil {
			copy := *media.remote
			copy.IP = append(net.IP(nil), media.remote.IP...)
			remote = &copy
		}
		codec, payload := media.codec, media.payloadType
		telephoneEvent, telephonePayload := media.telephoneEvent, media.telephoneEventPayload
		direction := media.localDirection
		media.mu.RUnlock()
		if remote == nil || !remote.IP.Equal(source.IP) || direction == "sendonly" || direction == "inactive" ||
			count < 12 || packet[0]>>6 != 2 {
			continue
		}
		header, payloadEnd, valid := rtpPayloadBounds(packet[:count])
		if !valid {
			continue
		}
		packetPayload := packet[1] & 0x7f
		isTelephoneEvent := telephoneEvent && packetPayload == telephonePayload
		if isTelephoneEvent {
			if payloadEnd-header < 4 || packet[header+1]&0x80 == 0 {
				continue
			}
		} else if packetPayload != payload || payloadEnd == header {
			continue
		}
		// Learn a symmetric RTP port only after the packet has a valid RTP
		// structure and a payload type negotiated for this media stream.
		media.mu.Lock()
		if media.remote != nil && media.remote.IP.Equal(source.IP) {
			media.remote.Port = source.Port
		}
		media.mu.Unlock()
		if isTelephoneEvent {
			event := packet[header]
			timestamp := binary.BigEndian.Uint32(packet[4:8])
			digit, ok := dtmfEventDigit(event)
			if !ok {
				continue
			}
			media.mu.Lock()
			duplicate := media.haveLastDTMF && media.lastDTMFEvent == event && media.lastDTMFTimestamp == timestamp
			if !duplicate {
				media.haveLastDTMF = true
				media.lastDTMFEvent = event
				media.lastDTMFTimestamp = timestamp
			}
			media.mu.Unlock()
			if !duplicate {
				select {
				case media.dtmf <- digit:
				default:
				}
			}
			continue
		}
		samples := make([]int16, payloadEnd-header)
		for index, encoded := range packet[header:payloadEnd] {
			if codec == "PCMA" {
				samples[index] = aLawToLinear(encoded)
			} else {
				samples[index] = muLawToLinear(encoded)
			}
		}
		select {
		case media.downlink <- samples:
		default:
			// Keep real-time behavior by dropping the oldest queued packet.
			select {
			case <-media.downlink:
			default:
			}
			select {
			case media.downlink <- samples:
			default:
			}
		}
	}
}

func rtpPayloadBounds(packet []byte) (int, int, bool) {
	if len(packet) < 12 || packet[0]>>6 != 2 {
		return 0, 0, false
	}
	header := 12 + int(packet[0]&0x0f)*4
	if header > len(packet) {
		return 0, 0, false
	}
	if packet[0]&0x10 != 0 {
		if len(packet) < header+4 {
			return 0, 0, false
		}
		header += 4 + int(binary.BigEndian.Uint16(packet[header+2:header+4]))*4
		if header > len(packet) {
			return 0, 0, false
		}
	}
	end := len(packet)
	if packet[0]&0x20 != 0 {
		padding := int(packet[len(packet)-1])
		if padding == 0 || padding > end-header {
			return 0, 0, false
		}
		end -= padding
	}
	return header, end, header < end
}

func (media *rtpMedia) receiveRTCP() {
	packet := make([]byte, 2048)
	for {
		count, source, err := media.rtcpConn.ReadFromUDP(packet)
		if err != nil {
			return
		}
		media.mu.RLock()
		var expected *net.UDPAddr
		if media.rtcpRemote != nil {
			copy := *media.rtcpRemote
			copy.IP = append(net.IP(nil), media.rtcpRemote.IP...)
			expected = &copy
		}
		media.mu.RUnlock()
		if expected == nil || !expected.IP.Equal(source.IP) || validateRTCPCompound(packet[:count]) != nil {
			continue
		}
		media.mu.Lock()
		if media.rtcpRemote != nil && media.rtcpRemote.IP.Equal(source.IP) {
			media.rtcpRemote.Port = source.Port // learn only from structurally valid RTCP
		}
		media.mu.Unlock()
	}
}

func (media *rtpMedia) reportRTCP() {
	ticker := time.NewTicker(rtcpReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-media.closed:
			return
		case <-ticker.C:
			media.mu.RLock()
			var remote *net.UDPAddr
			if media.rtcpRemote != nil {
				copy := *media.rtcpRemote
				copy.IP = append(net.IP(nil), media.rtcpRemote.IP...)
				remote = &copy
			}
			media.mu.RUnlock()
			if remote == nil {
				continue
			}
			// We do not negotiate reduced-size RTCP, so every transmission is a
			// compound RR + SDES(CNAME), while the zero report-block count avoids
			// inventing packet-loss statistics we do not track yet.
			report := buildRTCPReceiverReport(media.ssrc, media.rtcpCNAME)
			_, _ = media.rtcpConn.WriteToUDP(report, remote)
		}
	}
}

func buildRTCPReceiverReport(ssrc uint32, cname string) []byte {
	if len(cname) > 255 {
		cname = cname[:255]
	}
	report := make([]byte, 8)
	report[0], report[1] = 0x80, 201 // Receiver Report, zero report blocks.
	binary.BigEndian.PutUint16(report[2:4], 1)
	binary.BigEndian.PutUint32(report[4:8], ssrc)

	chunkLength := 4 + 2 + len(cname) + 1 // SSRC, CNAME item, END item.
	padding := (4 - chunkLength%4) % 4
	sdes := make([]byte, 4+chunkLength+padding)
	sdes[0], sdes[1] = 0x81, 202 // one SDES chunk
	binary.BigEndian.PutUint16(sdes[2:4], uint16(len(sdes)/4-1))
	binary.BigEndian.PutUint32(sdes[4:8], ssrc)
	sdes[8], sdes[9] = 1, byte(len(cname)) // CNAME
	copy(sdes[10:], cname)
	// The zero-valued END item and alignment padding are already present.
	return append(report, sdes...)
}

func validateRTCPCompound(packet []byte) error {
	if len(packet) < 8 {
		return errors.New("ims: RTCP packet is truncated")
	}
	haveCNAME := false
	for offset := 0; offset < len(packet); {
		if len(packet)-offset < 4 || packet[offset]>>6 != 2 {
			return errors.New("ims: RTCP packet has an invalid header")
		}
		packetType := packet[offset+1]
		if packetType < 200 || packetType > 207 {
			return errors.New("ims: RTCP packet type is unsupported")
		}
		if offset == 0 && packetType != 200 && packetType != 201 {
			return errors.New("ims: RTCP compound packet does not begin with SR or RR")
		}
		length := (int(binary.BigEndian.Uint16(packet[offset+2:offset+4])) + 1) * 4
		if length < 4 || offset+length > len(packet) {
			return errors.New("ims: RTCP packet is truncated")
		}
		end := offset + length
		if packet[offset]&0x20 != 0 {
			if end != len(packet) {
				return errors.New("ims: only the final RTCP packet may be padded")
			}
			padding := int(packet[end-1])
			if padding == 0 || padding > length-4 {
				return errors.New("ims: RTCP packet has invalid padding")
			}
			end -= padding
		}
		count := int(packet[offset] & 0x1f)
		switch packetType {
		case 200: // Sender Report: SSRC + sender info + report blocks.
			if end-offset < 28+24*count {
				return errors.New("ims: RTCP sender report is truncated")
			}
		case 201: // Receiver Report: SSRC + report blocks.
			if end-offset < 8+24*count {
				return errors.New("ims: RTCP receiver report is truncated")
			}
		case 202:
			found, err := validateRTCPSDES(packet[offset:end], count)
			if err != nil {
				return err
			}
			haveCNAME = haveCNAME || found
		case 203: // BYE contains one SSRC/CSRC for every source count.
			if end-offset < 4+4*count {
				return errors.New("ims: RTCP BYE packet is truncated")
			}
		case 204, 205, 206:
			if end-offset < 12 {
				return errors.New("ims: RTCP feedback or APP packet is truncated")
			}
		case 207:
			if end-offset < 8 {
				return errors.New("ims: RTCP extended report is truncated")
			}
		}
		offset += length
	}
	if !haveCNAME {
		return errors.New("ims: RTCP compound packet omitted SDES CNAME")
	}
	return nil
}

func validateRTCPSDES(packet []byte, count int) (bool, error) {
	if len(packet) < 4 {
		return false, errors.New("ims: RTCP SDES packet is truncated")
	}
	position := 4
	haveCNAME := false
	for chunk := 0; chunk < count; chunk++ {
		if position+4 > len(packet) {
			return false, errors.New("ims: RTCP SDES chunk is truncated")
		}
		position += 4 // SSRC/CSRC identifier.
		ended := false
		for position < len(packet) {
			itemType := packet[position]
			position++
			if itemType == 0 {
				ended = true
				for position%4 != 0 {
					if position >= len(packet) || packet[position] != 0 {
						return false, errors.New("ims: RTCP SDES alignment is malformed")
					}
					position++
				}
				break
			}
			if position >= len(packet) {
				return false, errors.New("ims: RTCP SDES item is truncated")
			}
			itemLength := int(packet[position])
			position++
			if position+itemLength > len(packet) {
				return false, errors.New("ims: RTCP SDES item is truncated")
			}
			if itemType == 1 && itemLength > 0 {
				haveCNAME = true
			}
			position += itemLength
		}
		if !ended {
			return false, errors.New("ims: RTCP SDES chunk omitted END")
		}
	}
	if position != len(packet) {
		return false, errors.New("ims: RTCP SDES packet has trailing data")
	}
	return haveCNAME, nil
}

func (media *rtpMedia) Close() error {
	media.close.Do(func() {
		close(media.closed)
		_ = media.conn.Close()
		_ = media.rtcpConn.Close()
	})
	return nil
}

func linearToMuLaw(sample int16) byte {
	value := int(sample)
	sign := byte(0)
	if value < 0 {
		sign, value = 0x80, -value
		if value > 32767 {
			value = 32767
		}
	}
	value += 132
	if value > 32635 {
		value = 32635
	}
	exponent := 7
	for mask := 0x4000; exponent > 0 && value&mask == 0; mask >>= 1 {
		exponent--
	}
	mantissa := (value >> (exponent + 3)) & 0x0f
	return ^(sign | byte(exponent<<4) | byte(mantissa))
}

func muLawToLinear(value byte) int16 {
	value = ^value
	magnitude := ((int(value)&0x0f)<<3 + 132) << ((value & 0x70) >> 4)
	magnitude -= 132
	if value&0x80 != 0 {
		return int16(-magnitude)
	}
	return int16(magnitude)
}

func linearToALaw(sample int16) byte {
	value := int(sample)
	mask := byte(0xd5)
	if value < 0 {
		mask, value = 0x55, -value-1
	}
	if value > 32767 {
		value = 32767
	}
	var encoded byte
	if value < 256 {
		encoded = byte(value >> 4)
	} else {
		exponent := 1
		for threshold := 512; exponent < 7 && value >= threshold; threshold <<= 1 {
			exponent++
		}
		encoded = byte(exponent<<4) | byte((value>>(exponent+3))&0x0f)
	}
	return encoded ^ mask
}

func aLawToLinear(value byte) int16 {
	value ^= 0x55
	magnitude := int(value&0x0f)<<4 + 8
	exponent := int((value & 0x70) >> 4)
	if exponent != 0 {
		magnitude = (magnitude + 0x100) << (exponent - 1)
	}
	if value&0x80 == 0 {
		return int16(-magnitude)
	}
	return int16(magnitude)
}
