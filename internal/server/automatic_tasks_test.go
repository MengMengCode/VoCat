package server

import (
	"testing"
	"time"

	"vocat/internal/store"
)

func TestNextAutomaticRunUsesIntervalAndLocalClock(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, location)
	next, err := nextAutomaticRun("2026-08-01", "09:30", 3, now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 13, 9, 30, 0, 0, location)
	if !next.Equal(want) {
		t.Fatalf("next run = %v, want %v", next, want)
	}
}

func TestUSBSIMReaderAutomaticTasksRequireVoWiFi(t *testing.T) {
	reader := store.Device{DeviceType: store.DeviceTypeUSBSIMReader}
	for _, test := range []struct {
		taskType    string
		environment string
		wantError   bool
	}{
		{taskType: "sms", environment: "vowifi"},
		{taskType: "call", environment: "vowifi"},
		{taskType: "sms", environment: "cellular", wantError: true},
		{taskType: "public_ip", environment: "cellular", wantError: true},
	} {
		err := validateAutomaticTaskDeviceCapabilities(reader, test.taskType, test.environment)
		if (err != nil) != test.wantError {
			t.Errorf("type=%s environment=%s error=%v", test.taskType, test.environment, err)
		}
	}
}

func TestAutomaticSMSRetrySafetyPreventsDuplicateSubmission(t *testing.T) {
	unsafe := []byte(`{"data":{"parts_attempted":1,"parts_accepted":1,"retry_safe":false}}`)
	if automaticSMSRetrySafe(unsafe) {
		t.Fatal("partially submitted SMS was considered safe to retry")
	}
	safe := []byte(`{"data":{"parts_attempted":0,"parts_accepted":0}}`)
	if !automaticSMSRetrySafe(safe) {
		t.Fatal("unattempted SMS was not considered safe to retry")
	}
}
