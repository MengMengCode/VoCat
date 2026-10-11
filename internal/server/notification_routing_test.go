package server

import (
	"context"
	"testing"

	"vocat/internal/store"
)

func TestNotificationProvidersFilterDevices(t *testing.T) {
	// Matching notifications reach validation/transport and fail; filtered ones
	// return nil. Cancellation prevents any real network request.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bot := &telegramBot{server: newSettingsTestServer(t)}
	for _, scope := range []struct {
		ids  []string
		send bool
	}{{nil, true}, {[]string{}, false}, {[]string{"a"}, true}, {[]string{"b"}, false}} {
		config := map[string]any{
			"device_ids": scope.ids, "urls": []string{"://invalid"}, "url": "://invalid",
			"nickname": "/", "payload_template": "{", "smtp_host": "example.invalid",
		}
		for _, channel := range notificationChannels {
			for event, send := range map[string]func() error{
				"sms": func() error {
					message := smsNotification{DeviceID: "a"}
					if channel == "telegram" {
						return bot.sendSMSNotification(ctx, telegramRuntimeConfig{DeviceIDs: scope.ids}, store.SMSMessage{DeviceID: message.DeviceID})
					}
					return sendSMSNotification(ctx, channel, config, message)
				},
				"call": func() error {
					return sendCallNotification(ctx, channel, config, IncomingCallNotification{DeviceID: "a"})
				},
				"task": func() error {
					return sendAutomaticTaskNotification(ctx, channel, config, automaticTaskNotification{Task: store.AutomaticTask{DeviceID: "a"}})
				},
			} {
				if err := send(); (err != nil) != scope.send {
					t.Fatalf("%s/%s scope=%v: err=%v, want attempted=%v", channel, event, scope.ids, err, scope.send)
				}
			}
		}
	}
}

func TestManualNotificationIgnoresDeviceScope(t *testing.T) {
	ctx := context.Background()
	config := map[string]any{"device_ids": []string{}, "nickname": "/", "payload_template": "{"}
	for name, send := range map[string]func() error{
		"meow":  func() error { return sendMeowNotification(ctx, config, nil, "test", "test") },
		"wecom": func() error { return sendWecomNotificationTest(ctx, config) },
		"lark":  func() error { return sendLarkNotificationTest(ctx, config) },
	} {
		if err := send(); err == nil {
			t.Fatalf("%s skipped validation because of the device scope", name)
		}
	}
}
