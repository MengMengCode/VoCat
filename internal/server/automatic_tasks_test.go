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

// 验证任务正文仅移除重复标题，保留普通首行且不修改原始文本。
func TestAutomaticTaskNotificationDetailTextRemovesTitle(t *testing.T) {
	message := automaticTaskNotification{
		Title: "自动任务执行成功",
		Text:  "自动任务执行成功\n任务  检查网络\n结果  任务已完成",
	}
	if got := message.DetailText(); got != "任务  检查网络\n结果  任务已完成" {
		t.Fatalf("DetailText() = %q", got)
	}
	if message.Text != "自动任务执行成功\n任务  检查网络\n结果  任务已完成" {
		t.Fatalf("DetailText() changed the original text to %q", message.Text)
	}

	message.Text = "正文第一行\n正文第二行"
	if got := message.DetailText(); got != message.Text {
		t.Fatalf("DetailText() changed non-title body to %q", got)
	}

	message.Text = "提醒：自动任务执行成功后请确认\n结果  任务已完成"
	if got := message.DetailText(); got != message.Text {
		t.Fatalf("DetailText() changed a first line merely mentioning the title to %q", got)
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

func TestAutomaticTaskAvailabilityHidesRestrictedPaths(t *testing.T) {
	for _, test := range []struct {
		available   bool
		taskType    string
		environment string
		wantError   bool
	}{
		{false, "sms", "vowifi", false},
		{false, "call", "vowifi", false},
		{false, "sms", "cellular", false},
		{false, "call", "cellular", false},
		{false, "public_ip", "cellular", true},
		{true, "public_ip", "cellular", false},
	} {
		err := validateAutomaticTaskAvailability(test.available, test.taskType, test.environment)
		if (err != nil) != test.wantError {
			t.Fatalf("availability(%v, %q, %q) = %v", test.available, test.taskType, test.environment, err)
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
