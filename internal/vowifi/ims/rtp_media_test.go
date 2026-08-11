package ims

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRTPMediaCarriesPCMOverPCMA(t *testing.T) {
	left, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	if err := left.configureRemote(right.offerSDP(net.IPv4(127, 0, 0, 1))); err != nil {
		t.Fatal(err)
	}
	if err := right.configureRemote(left.answerSDP(net.IPv4(127, 0, 0, 1))); err != nil {
		t.Fatal(err)
	}
	want := make([]int16, rtpPacketSamples)
	for index := range want {
		want[index] = int16(9000 * math.Sin(float64(index)*2*math.Pi/40))
	}
	if err := left.WritePCM(want); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := right.ReadPCM(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("received %d samples, want %d", len(got), len(want))
	}
	for index := range got {
		if difference := math.Abs(float64(got[index]) - float64(want[index])); difference > 700 {
			t.Fatalf("sample %d difference %.0f exceeds G.711 tolerance", index, difference)
		}
	}
}

func TestParseAudioSDPRejectsMissingEndpoint(t *testing.T) {
	if _, _, _, _, err := parseAudioSDP([]byte("v=0\r\nm=audio 0 RTP/AVP 8\r\n")); err == nil {
		t.Fatal("expected unusable SDP error")
	}
}

func TestRTPMediaAdvertisesAndNegotiatesTelephoneEventsAndRTCP(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()

	offer := string(media.offerSDP(net.IPv4(127, 0, 0, 1)))
	for _, required := range []string{
		"RTP/AVP 8 0 101",
		"a=rtpmap:101 telephone-event/8000",
		"a=fmtp:101 0-15",
		"a=rtcp:",
	} {
		if !strings.Contains(offer, required) {
			t.Fatalf("SDP offer omitted %q:\n%s", required, offer)
		}
	}

	remote := []byte(strings.Join([]string{
		"v=0",
		"c=IN IP4 127.0.0.1",
		"m=audio 41000 RTP/AVP 8 110",
		"a=rtpmap:8 PCMA/8000",
		"a=rtpmap:110 telephone-event/8000",
		"a=fmtp:110 0-15",
		"a=rtcp:41001 IN IP4 127.0.0.1",
		"",
	}, "\r\n"))
	if err := media.configureRemote(remote); err != nil {
		t.Fatal(err)
	}
	answer := string(media.answerSDP(net.IPv4(127, 0, 0, 1)))
	if !strings.Contains(answer, "RTP/AVP 8 110") ||
		!strings.Contains(answer, "a=rtpmap:110 telephone-event/8000") {
		t.Fatalf("SDP answer did not preserve negotiated telephone-event payload:\n%s", answer)
	}
	media.mu.RLock()
	rtcpRemote := media.rtcpRemote
	media.mu.RUnlock()
	if rtcpRemote == nil || rtcpRemote.Port != 41001 || !rtcpRemote.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("RTCP endpoint = %v, want 127.0.0.1:41001", rtcpRemote)
	}
}

func TestRTPMediaUsesStableSDPOriginAndNegotiatesHoldDirection(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()

	firstOffer := string(media.offerSDP(net.IPv4(127, 0, 0, 1)))
	secondOffer := string(media.offerSDP(net.IPv4(127, 0, 0, 1)))
	if firstOffer != secondOffer {
		t.Fatalf("unchanged SDP offer changed origin/version:\nfirst=%s\nsecond=%s", firstOffer, secondOffer)
	}
	remote := []byte(strings.Join([]string{
		"v=0", "o=- 9 3 IN IP4 127.0.0.1", "c=IN IP4 127.0.0.1", "a=sendonly",
		"m=audio 41100 RTP/AVP 8", "a=rtpmap:8 PCMA/8000", "",
	}, "\r\n"))
	if err := media.configureRemote(remote); err != nil {
		t.Fatal(err)
	}
	firstAnswer := string(media.answerSDP(net.IPv4(127, 0, 0, 1)))
	secondAnswer := string(media.answerSDP(net.IPv4(127, 0, 0, 1)))
	if firstAnswer != secondAnswer {
		t.Fatalf("unchanged SDP answer changed origin/version:\nfirst=%s\nsecond=%s", firstAnswer, secondAnswer)
	}
	if !strings.Contains(firstAnswer, "a=recvonly") {
		t.Fatalf("sendonly offer was not answered recvonly:\n%s", firstAnswer)
	}
	if err := media.WritePCM(make([]int16, rtpPacketSamples)); err == nil || !strings.Contains(err.Error(), "hold") {
		t.Fatalf("WritePCM while remotely held error = %v", err)
	}
}

func TestParseAudioDescriptionKeepsSelectedAudioMediaScoped(t *testing.T) {
	description, err := parseAudioDescription([]byte(strings.Join([]string{
		"v=0", "c=IN IP4 192.0.2.1", "a=recvonly",
		"m=audio 0 RTP/AVP 8", "a=rtpmap:8 PCMA/8000",
		"m=audio 41200 RTP/AVP 0", "c=IN IP4 192.0.2.2", "a=rtpmap:0 PCMU/8000", "a=sendonly",
		"m=video 41300 RTP/AVP 96", "c=IN IP4 203.0.113.9", "a=inactive", "",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if description.port != 41200 || !description.address.Equal(net.ParseIP("192.0.2.2")) ||
		description.direction != "sendonly" || len(description.formats) != 1 || description.formats[0] != "0" {
		t.Fatalf("selected audio description = %#v", description)
	}
}

func TestRTPMediaPacesBufferedPCM(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	sink, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	port := sink.LocalAddr().(*net.UDPAddr).Port
	remote := []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio " + strconv.Itoa(port) + " RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n")
	if err := media.configureRemote(remote); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := media.WritePCM(make([]int16, 3*rtpPacketSamples)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 30*time.Millisecond {
		t.Fatalf("three PCM packets were emitted as a burst in %v", elapsed)
	}
}

func TestRTPMediaConstrainsDTMFToNegotiatedFmtp(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	remote := []byte(strings.Join([]string{
		"v=0", "c=IN IP4 127.0.0.1", "m=audio 41400 RTP/AVP 8 110",
		"a=rtpmap:8 PCMA/8000", "a=rtpmap:110 telephone-event/8000", "a=fmtp:110 5,10-11,16-20", "",
	}, "\r\n"))
	if err := media.configureRemote(remote); err != nil {
		t.Fatal(err)
	}
	answer := string(media.answerSDP(net.IPv4(127, 0, 0, 1)))
	if !strings.Contains(answer, "a=fmtp:110 5,10-11") {
		t.Fatalf("answer did not intersect implemented telephone events:\n%s", answer)
	}
	if err := media.SendDTMF('6', 40*time.Millisecond); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unnegotiated DTMF event error = %v", err)
	}
}

func TestRTPMediaCarriesRFC4733DTMF(t *testing.T) {
	left, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	if err := left.configureRemote(right.offerSDP(net.IPv4(127, 0, 0, 1))); err != nil {
		t.Fatal(err)
	}
	if err := right.configureRemote(left.answerSDP(net.IPv4(127, 0, 0, 1))); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := left.SendDTMF('5', 120*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	digit, err := right.ReadDTMF(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if digit != '5' {
		t.Fatalf("received DTMF %q, want 5", digit)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Fatalf("DTMF was emitted as an instantaneous burst in %v", elapsed)
	}
}

func TestRTPMediaRejectsAMRWithoutPretendingToTranscode(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	remote := []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 41000 RTP/AVP 96 97\r\na=rtpmap:96 AMR/8000\r\na=rtpmap:97 AMR-WB/16000\r\n")
	err = media.configureRemote(remote)
	if err == nil || !strings.Contains(err.Error(), "AMR") {
		t.Fatalf("AMR-only offer error = %v, want explicit unsupported-AMR error", err)
	}
}

func TestValidateRTCPCompoundPacket(t *testing.T) {
	valid := buildRTCPReceiverReport(0x01020304, "receiver@example.test")
	if err := validateRTCPCompound(valid); err != nil {
		t.Fatalf("valid compound receiver report rejected: %v", err)
	}
	if err := validateRTCPCompound([]byte{0x80, 201, 0, 2, 1, 2, 3, 4}); err == nil {
		t.Fatal("truncated RTCP packet was accepted")
	}
	if err := validateRTCPCompound([]byte{0x80, 201, 0, 1, 1, 2, 3, 4}); err == nil {
		t.Fatal("bare receiver report without SDES CNAME was accepted")
	}
}

func TestRTCPReportIsCompoundWithCNAME(t *testing.T) {
	packet := buildRTCPReceiverReport(0x01020304, "vocat-test@example.test")
	if err := validateRTCPCompound(packet); err != nil {
		t.Fatalf("generated compound RTCP rejected: %v", err)
	}
	if len(packet) < 16 || packet[1] != 201 {
		t.Fatalf("compound RTCP does not start with RR: %x", packet)
	}
	firstLength := (int(binary.BigEndian.Uint16(packet[2:4])) + 1) * 4
	if firstLength >= len(packet) || packet[firstLength+1] != 202 || !strings.Contains(string(packet[firstLength:]), "vocat-test@example.test") {
		t.Fatalf("compound RTCP omitted SDES CNAME: %x", packet)
	}
}

func TestMalformedPacketsCannotRewriteSymmetricMediaPorts(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	source, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	rtcpSource, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer rtcpSource.Close()
	remoteSDP := []byte(strings.Join([]string{
		"v=0", "c=IN IP4 127.0.0.1", "m=audio 41000 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000", "a=rtcp:41001 IN IP4 127.0.0.1", "",
	}, "\r\n"))
	if err := media.configureRemote(remoteSDP); err != nil {
		t.Fatal(err)
	}
	_, _ = source.WriteToUDP([]byte{0x80, 8}, media.conn.LocalAddr().(*net.UDPAddr))
	_, _ = rtcpSource.WriteToUDP([]byte{0x80, 201, 0, 2}, media.rtcpConn.LocalAddr().(*net.UDPAddr))
	time.Sleep(20 * time.Millisecond)
	media.mu.RLock()
	rtpPort, rtcpPort := media.remote.Port, media.rtcpRemote.Port
	media.mu.RUnlock()
	if rtpPort != 41000 || rtcpPort != 41001 {
		t.Fatalf("malformed packets rewrote negotiated ports to RTP=%d RTCP=%d", rtpPort, rtcpPort)
	}

	validRTP := make([]byte, 13)
	validRTP[0], validRTP[1] = 0x80, 8
	_, _ = source.WriteToUDP(validRTP, media.conn.LocalAddr().(*net.UDPAddr))
	validRTCP := buildRTCPReceiverReport(0x01020304, "symmetric@example.test")
	_, _ = rtcpSource.WriteToUDP(validRTCP, media.rtcpConn.LocalAddr().(*net.UDPAddr))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		media.mu.RLock()
		rtpPort, rtcpPort = media.remote.Port, media.rtcpRemote.Port
		media.mu.RUnlock()
		if rtpPort == source.LocalAddr().(*net.UDPAddr).Port && rtcpPort == rtcpSource.LocalAddr().(*net.UDPAddr).Port {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("valid packets did not update symmetric ports: RTP=%d RTCP=%d", rtpPort, rtcpPort)
}
