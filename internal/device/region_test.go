package device

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// injectSnapshot stores a snapshot on the managed device so region guards observe
// its IMSI without replaying the full readSnapshot AT transcript.
func injectSnapshot(t *testing.T, manager *Manager, id string, snapshot *Snapshot) {
	t.Helper()
	state, err := manager.lookup(id)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	manager.setResult(id, state, snapshot, nil)
}

func TestCardMCCMNC(t *testing.T) {
	t.Parallel()
	if mcc, _ := CardMCCMNC("460001234567890"); mcc != "460" {
		t.Fatalf("CardMCCMNC mcc = %q, want 460", mcc)
	}
	if mcc, mnc := CardMCCMNCWithLength("454006395879502", 2); mcc != "454" || mnc != "00" {
		t.Fatalf("CardMCCMNCWithLength = (%q, %q), want (454, 00)", mcc, mnc)
	}
	for _, bad := range []string{"", "4600", "4600X1234"} {
		if mcc, _ := CardMCCMNC(bad); mcc != "" {
			t.Fatalf("CardMCCMNC(%q) mcc = %q, want empty", bad, mcc)
		}
	}
}

func TestRegionBlockReason(t *testing.T) {
	t.Parallel()
	for _, imsi := range []string{"460001234567890", "461001234567890"} {
		reason := RegionBlockReason(imsi)
		if reason == "" {
			t.Fatalf("RegionBlockReason(%q) did not block", imsi)
		}
		if !strings.Contains(reason, "中国") {
			t.Fatalf("RegionBlockReason(%q) = %q, want it to name 中国", imsi, reason)
		}
	}
	for _, imsi := range []string{"310260123456789", "001011234567890", ""} {
		if reason := RegionBlockReason(imsi); reason != "" {
			t.Fatalf("RegionBlockReason(%q) = %q, want empty (fail-open)", imsi, reason)
		}
	}
}

func TestSetNetworkBlockedForRestrictedRegionSIM(t *testing.T) {
	client := &transcriptClient{}
	manager, id := newStartedTestManager(t, client)
	injectSnapshot(t, manager, id, &Snapshot{DeviceID: id, IMSI: "460001234567890"})
	_, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	})
	if !errors.Is(err, ErrRegionBlocked) {
		t.Fatalf("error = %v, want ErrRegionBlocked", err)
	}
	client.assertDone(t)
}

func TestSetNetworkAllowedForServedRegionSIM(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)
	injectSnapshot(t, manager, id, &Snapshot{DeviceID: id, IMSI: "310260123456789"})
	result, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	})
	if err != nil {
		t.Fatalf("enable network: %v", err)
	}
	if !result.Enabled {
		t.Fatalf("enable result = %#v", result)
	}
	client.assertDone(t)
}

// A device whose SIM region is not yet known (no snapshot) must not be denied:
// only a confirmed blocked MCC blocks service (fail-open).
func TestSetNetworkAllowedWhenSIMRegionUnknown(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+CGDCONT=1,"IPV4V6","internet"`, response: okResponse()},
		{command: "AT+CGATT=1", response: okResponse()},
		{command: "AT+CGACT=1,1", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)
	if _, err := manager.SetNetwork(context.Background(), id, NetworkRequest{
		Enabled: true, APN: "internet", IPVersion: "IPV4V6",
	}); err != nil {
		t.Fatalf("enable network with unknown region: %v", err)
	}
	client.assertDone(t)
}

func TestSendSMSBlockedForRestrictedRegionSIM(t *testing.T) {
	client := &transcriptClient{}
	manager, id := newStartedTestManager(t, client)
	injectSnapshot(t, manager, id, &Snapshot{DeviceID: id, IMSI: "460001234567890"})
	result, err := manager.SendSMS(context.Background(), id, "+15551234567", "hello")
	if !errors.Is(err, ErrRegionBlocked) {
		t.Fatalf("error = %v, want ErrRegionBlocked", err)
	}
	if result.PartsAttempted != 0 {
		t.Fatalf("PartsAttempted = %d, want 0 for a blocked region", result.PartsAttempted)
	}
	if result.SubmissionStatus != "region_blocked" {
		t.Fatalf("SubmissionStatus = %q, want region_blocked", result.SubmissionStatus)
	}
	client.assertDone(t)
}
