package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

func TestSMSNotificationReadyWaitsForAllLongSMSParts(t *testing.T) {
	partial := store.SMSMessage{
		MessageID: "concat:cellular_at:imei:sender:1:2",
		Extra:     json.RawMessage(`{"concat_complete":false}`),
	}
	if smsNotificationReady(partial) {
		t.Fatal("incomplete long SMS is ready for notification")
	}

	partial.Extra = json.RawMessage(`{"concat_complete":true}`)
	if !smsNotificationReady(partial) {
		t.Fatal("complete long SMS is not ready for notification")
	}
}

func TestSMSNotificationTextMatchesUserFacingTemplate(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	previousLocation := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = previousLocation })
	message := smsNotification{
		DeviceID: "device-1", DeviceLabel: "EC20", Number: "+447386",
		Time: time.Date(2026, 8, 8, 17, 25, 35, 0, location), Content: "你好鸭",
	}
	want := "收到新短信\n设备  EC20\n号码  +447386\n时间  2026-08-08 17:25:35\n内容  你好鸭"
	if got := message.Text(); got != want {
		t.Fatalf("smsNotification.Text() = %q, want %q", got, want)
	}
	if strings.HasPrefix(message.DetailText(), "收到新短信") {
		t.Fatalf("DetailText() unexpectedly repeats the title: %q", message.DetailText())
	}
}

func TestRenderSMSWebhookTemplate(t *testing.T) {
	message := smsNotification{
		DeviceID: "device-1", DeviceName: "客厅", DeviceLabel: "EC20",
		Number: "+447386", Time: time.Unix(1_700_000_000, 0), Content: "hello",
	}
	got := renderSMSWebhookTemplate("{{event}}|{{device_id}}|{{device_name}}|{{device_label}}|{{number}}|{{text}}|{{content}}", message)
	want := "sms.received|device-1|客厅|EC20|+447386|hello|hello"
	if got != want {
		t.Fatalf("renderSMSWebhookTemplate() = %q, want %q", got, want)
	}
}

func TestWecomSMSValuesIncludeRenderedSMSFields(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	message := smsNotification{
		DeviceID: "device-1", DeviceName: "客厅", DeviceLabel: "EC20",
		Number: "+447386", Time: time.Date(2026, 8, 8, 17, 25, 35, 0, location), Content: "hello",
	}
	values := wecomSMSValues(message)
	if values["event"] != "sms.received" || values["title"] != "收到新短信" || values["message"] != message.Text() {
		t.Fatalf("common values = %#v", values)
	}
	wantLocalTime := message.Time.Local().Format("2006-01-02 15:04:05")
	if values["content"] != "hello" || values["number"] != "+447386" || values["device_label"] != "EC20" || values["time"] != wantLocalTime {
		t.Fatalf("SMS values = %#v", values)
	}
}

func TestWecomAutomaticTaskValuesLeaveSMSFieldsEmpty(t *testing.T) {
	values := wecomAutomaticTaskValues(automaticTaskNotification{
		Title: "自动任务执行成功", Text: "任务已完成", Time: time.Unix(1_700_000_000, 0),
	})
	if values["event"] != "automatic_task.completed" || values["title"] != "自动任务执行成功" || values["message"] != "任务已完成" {
		t.Fatalf("common values = %#v", values)
	}
	for _, name := range []string{"content", "number", "device_id", "device_name", "device_label", "time"} {
		if values[name] != "" {
			t.Fatalf("%s = %q, want empty", name, values[name])
		}
	}
}

func TestLarkTemplateValuesCoverSMSAndAutomaticTasks(t *testing.T) {
	message := smsNotification{
		DeviceID: "device-1", DeviceName: "客厅", DeviceLabel: "EC20",
		Number: "+447386", Time: time.Unix(1_700_000_000, 0), Content: "hello",
	}
	smsValues := larkSMSValues(message)
	if smsValues["event"] != "sms.received" || smsValues["title"] != "收到新短信" ||
		smsValues["message"] != message.Text() || smsValues["content"] != "hello" ||
		smsValues["device_label"] != "EC20" {
		t.Fatalf("Lark SMS values = %#v", smsValues)
	}

	taskValues := larkAutomaticTaskValues(automaticTaskNotification{
		Title: "自动任务执行成功", Text: "任务已完成", Time: time.Unix(1_700_000_000, 0),
	})
	if taskValues["event"] != "automatic_task.completed" || taskValues["title"] != "自动任务执行成功" || taskValues["message"] != "任务已完成" {
		t.Fatalf("Lark automatic task values = %#v", taskValues)
	}
	for _, name := range []string{"content", "number", "device_id", "device_name", "device_label", "time"} {
		if taskValues[name] != "" {
			t.Fatalf("%s = %q, want empty", name, taskValues[name])
		}
	}
}

func TestValidateSMSNotificationConfig(t *testing.T) {
	valid := map[string]map[string]any{
		"bark":     {"urls": []any{"https://api.day.app/key"}},
		"email":    {"smtp_host": "smtp.example.com", "from_address": "from@example.com", "to_addresses": []any{"to@example.com"}},
		"pushplus": {"token": "secret"},
		"webhook":  {"urls": []any{"https://example.com/hook"}},
		"wecom": {
			"urls":             []any{"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"},
			"payload_template": `{"msgtype":"text","text":{"content":{{message}}}}`,
		},
		"lark": {
			"url":              "https://open.larksuite.com/open-apis/bot/v2/hook/secret",
			"signing_enabled":  true,
			"secret":           "signing-secret",
			"payload_template": `{"msg_type":"text","content":{"text":{{message}}}}`,
		},
	}
	for channel, config := range valid {
		if err := validateSMSNotificationConfig(channel, config); err != nil {
			t.Errorf("validateSMSNotificationConfig(%q) error = %v", channel, err)
		}
	}
	if err := validateSMSNotificationConfig("pushplus", map[string]any{}); err == nil {
		t.Fatal("missing Pushplus token was accepted")
	}
}
