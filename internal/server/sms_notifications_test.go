package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

func TestSMSNotificationTextMatchesUserFacingTemplate(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	previousLocation := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = previousLocation })
	message := smsNotification{
		DeviceID: "device-1", DeviceLabel: "EC20", Number: "+447386",
		LocalPhone: "+85291234567", ICCID: "card-a",
		Time: time.Date(2026, 8, 8, 17, 25, 35, 0, location), Content: "你好鸭",
	}
	want := "收到新短信\n设备  EC20\n本机号码  +85291234567\n号码  +447386\n时间  2026-08-08 17:25:35\n内容  你好鸭"
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
		LocalPhone: "+85291234567", ICCID: "card-a",
	}
	got := renderSMSWebhookTemplate("{{event}}|{{device_id}}|{{device_name}}|{{device_label}}|{{number}}|{{text}}|{{content}}|{{local_phone}}|{{iccid}}|{{recipient}}", message)
	want := "sms.received|device-1|客厅|EC20|+447386|hello|hello|+85291234567|card-a|+85291234567"
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
	for _, name := range []string{"content", "number", "local_phone", "iccid", "recipient", "device_id", "device_name", "device_label", "time"} {
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
	for _, name := range []string{"content", "number", "local_phone", "iccid", "recipient", "device_id", "device_name", "device_label", "time"} {
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

func TestBuildPushplusPayloadOmitsTimestamp(t *testing.T) {
	payload := buildPushplusPayload("my-token", "测试标题", "测试内容", "my-topic", "wechat")
	if _, ok := payload["timestamp"]; ok {
		t.Fatalf("pushplus payload should omit timestamp to avoid server expiration rejection, got: %v", payload["timestamp"])
	}
	if payload["token"] != "my-token" || payload["title"] != "测试标题" || payload["content"] != "测试内容" ||
		payload["template"] != "txt" || payload["topic"] != "my-topic" || payload["channel"] != "wechat" {
		t.Fatalf("pushplus payload = %#v", payload)
	}
}

func TestSMSNotificationReceivingSubscription(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{store: database}
	if err := database.UpsertDevice(ctx, store.Device{ID: "device-1", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	// The device currently has profile B. Neither profile A nor an unidentified
	// historical SMS may acquire B's phone number when a notification is built.
	if err := database.UpsertDeviceRuntime(ctx, store.DeviceRuntime{
		DeviceID: "device-1", ICCID: "card-b", IMSI: "imsi-b", PhoneNumber: "+442222",
	}); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []store.CardPolicy{
		{ICCID: "card-b", CustomPhoneNumber: "+442222"},
		{ICCID: "card-custom", CustomPhoneNumber: "+443333"},
	} {
		if err := database.UpsertCardPolicy(ctx, policy); err != nil {
			t.Fatal(err)
		}
	}
	for _, iccid := range []string{"card-custom", "card-associated"} {
		if err := database.UpsertPhoneAssociation(ctx, store.PhoneAssociation{
			ICCID: iccid, DeviceID: "device-1", Number: "+444444", Source: "ims",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, iccid, snapshot, phone, recipient, label string
	}{
		{"snapshot wins over later override", "card-custom", " +441111 ", "+441111", "+441111", "本机号码"},
		{"custom wins over association", "card-custom", "", "+443333", "+443333", "本机号码"},
		{"IMS association", "card-associated", "", "+444444", "+444444", "本机号码"},
		{"number snapshot without ICCID", "", "+441111", "+441111", "+441111", "本机号码"},
		{"ICCID fallback", "card-a", "", "", "card-a", "ICCID"},
		{"unknown historical card", "", "", "", "未知", "收信卡"},
		{"whitespace is missing", "  ", "  ", "", "未知", "收信卡"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := server.newSMSNotification(ctx, store.SMSMessage{
				DeviceID: "device-1", ICCID: test.iccid, LocalPhone: test.snapshot,
				IMSI: "imsi-a", Peer: "SmartApp", Body: "OTP 8085", Timestamp: time.Unix(1_700_000_000, 0),
			})
			if message.LocalPhone != test.phone || message.ICCID != strings.TrimSpace(test.iccid) ||
				message.Recipient() != test.recipient || message.Number != "SmartApp" {
				t.Fatalf("receiving subscription = %#v", message)
			}
			for name, output := range map[string]string{
				"standard": message.Text(), "detail": message.DetailText(), "telegram": message.TelegramText(),
			} {
				separator := "  "
				if name == "telegram" {
					separator = "："
				}
				if !strings.Contains(output, test.label+separator+test.recipient) ||
					!strings.Contains(output, "SmartApp") || !strings.Contains(output, "OTP 8085") {
					t.Fatalf("%s notification = %q", name, output)
				}
			}
			for name, values := range map[string]map[string]string{
				"wecom": wecomSMSValues(message), "lark": larkSMSValues(message),
			} {
				if values["local_phone"] != test.phone || values["iccid"] != message.ICCID || values["recipient"] != test.recipient {
					t.Fatalf("%s values = %#v", name, values)
				}
			}
		})
	}
}

func TestSMSNotificationRetryKeepsReceivingProfile(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{store: database, logger: regionTestLogger()}
	if err := database.UpsertDevice(ctx, store.Device{ID: "device-1", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertNotificationSetting(ctx, store.NotificationSetting{
		Channel: "webhook", Enabled: true, Config: []byte(`{"urls":["https://example.com/hook"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		DeviceID: "device-1", ICCID: "card-a", LocalPhone: "+441111", IMSI: "imsi-a",
		Peer: "SmartApp", Direction: "inbound", Body: "OTP 8085",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &smsNotificationCursor{initialized: true}
	attempts := 0
	sender := func(_ context.Context, _ string, _ map[string]any, message smsNotification) error {
		attempts++
		if message.LocalPhone != "+441111" || message.ICCID != "card-a" {
			t.Fatalf("attempt %d used a different receiving profile: %#v", attempts, message)
		}
		if attempts == 1 {
			return errors.New("temporary provider failure")
		}
		return nil
	}
	server.deliverSMSNotificationsOnce(ctx, "webhook", state, sender)
	if state.id != 0 || attempts != 1 {
		t.Fatalf("failed notification was not retained: %#v, attempts %d", state, attempts)
	}
	if err := database.UpsertDeviceRuntime(ctx, store.DeviceRuntime{
		DeviceID: "device-1", ICCID: "card-b", IMSI: "imsi-b", PhoneNumber: "+442222",
	}); err != nil {
		t.Fatal(err)
	}
	server.deliverSMSNotificationsOnce(ctx, "webhook", state, sender)
	if state.id != saved.ID || attempts != 2 {
		t.Fatalf("notification was not delivered on retry: %#v, attempts %d", state, attempts)
	}
}

func TestSMSNotificationSubscriptionPayloads(t *testing.T) {
	for _, phone := range []string{"+85291234567", ""} {
		message := smsNotification{
			DeviceID: "device-1", DeviceLabel: "EC20", Number: "SmartApp",
			LocalPhone: phone, ICCID: "card-a", Content: "OTP 8085", Time: time.Unix(1_700_000_000, 0),
		}
		for _, template := range []string{"", "{{number}}|{{local_phone}}|{{iccid}}|{{recipient}}", "legacy {{number}}: {{text}}"} {
			var payload map[string]string
			if err := json.Unmarshal(buildSMSWebhookPayload(template, message), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["local_phone"] != phone || payload["iccid"] != "card-a" ||
				payload["recipient"] != message.Recipient() || payload["number"] != "SmartApp" || payload["content"] != "OTP 8085" {
				t.Fatalf("webhook payload = %#v", payload)
			}
			want := message.Text()
			if template != "" {
				want = renderSMSWebhookTemplate(template, message)
			}
			if payload["message"] != want {
				t.Fatalf("webhook message = %q, want %q", payload["message"], want)
			}
		}
		// Test real JSON template rendering, including the variable whitelists.
		template := `{"local_phone":{{local_phone}},"iccid":{{iccid}},"recipient":{{recipient}},"number":{{number}}}`
		wecom, err := renderWecomPayload(template, wecomSMSValues(message))
		if err != nil {
			t.Fatal(err)
		}
		lark, err := renderLarkPayload(template, larkSMSValues(message))
		if err != nil {
			t.Fatal(err)
		}
		for _, rendered := range [][]byte{wecom, lark} {
			var payload map[string]string
			if err := json.Unmarshal(rendered, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["local_phone"] != phone || payload["iccid"] != "card-a" ||
				payload["recipient"] != message.Recipient() || payload["number"] != "SmartApp" {
				t.Fatalf("JSON template = %s", rendered)
			}
		}
	}
}
