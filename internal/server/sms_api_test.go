package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/loghub"
	"vocat/internal/store"
)

type smsDeletionController struct {
	fakeDeviceController
	mu             sync.Mutex
	storedMessages []device.SMSMessage
	deleted        []string
}

func (controller *smsDeletionController) ListSMS(context.Context, string) ([]device.SMSMessage, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return append([]device.SMSMessage(nil), controller.storedMessages...), nil
}

func (controller *smsDeletionController) DeleteSMSFromStorage(
	_ context.Context,
	_ string,
	storageName string,
	index int,
) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	key := storageName + ":" + strconv.Itoa(index)
	controller.deleted = append(controller.deleted, key)
	remaining := controller.storedMessages[:0]
	for _, message := range controller.storedMessages {
		if message.Storage == storageName && message.Index == index {
			continue
		}
		remaining = append(remaining, message)
	}
	controller.storedMessages = remaining
	return nil
}

func TestSMSThreadAllDevicesUsesIMSIFilter(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for index, imsi := range []string{"imsi-a", "imsi-b"} {
		if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
			MessageID: "message-" + imsi,
			DeviceID:  "ec20",
			IMSI:      imsi,
			Peer:      "VOXI",
			Direction: "inbound",
			Body:      imsi,
			Timestamp: time.Unix(1_700_000_000+int64(index), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{store: database}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/sms/thread?device_id=all&imsi=imsi-a&peer=VOXI",
		nil,
	)
	response := httptest.NewRecorder()
	server.handleSMSThread(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0]["imsi"] != "imsi-a" {
		t.Fatalf("thread data = %#v", envelope.Data)
	}
}

func TestNative410DoesNotUseModemSMSStorage(t *testing.T) {
	if supportsModemSMSStorage(store.Device{DeviceType: store.DeviceTypeWiFi410}) {
		t.Fatal("native OpenStick 410 unexpectedly enabled modem SMS storage polling")
	}
	if !supportsModemSMSStorage(store.Device{DeviceType: store.DeviceTypePCIeEC20EC25}) {
		t.Fatal("EC20 modem SMS storage polling was disabled")
	}
}

func TestSyncModemSMSDoesNotRelabelStoredMessageAfterProfileSwitch(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const (
		deviceID = "ec20-1"
		imei     = "867394042309830"
	)
	if err := database.UpsertDevice(ctx, store.Device{
		ID: deviceID, Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		ModemIMEI: imei, SMSEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	receivedAt := time.Unix(1_700_000_000, 0).UTC()
	storedMessage := device.SMSMessage{
		Index: 7, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
		Direction: device.SMSDirectionReceived, From: "JETPAC", Text: "hello",
		ServiceCenterTimestamp: &receivedAt, RawPDU: "001122334455",
	}
	server := &Server{store: database, logger: regionTestLogger()}
	server.devices = fakeDeviceController{
		entry: device.Device{ID: deviceID, Discovered: true, Snapshot: &device.Snapshot{
			DeviceID: deviceID, IMEI: imei, ICCID: "iccid-a", IMSI: "imsi-a",
			Phone: device.PhoneNumber{Number: "+441111"},
		}},
		smsMessages: []device.SMSMessage{storedMessage},
	}
	server.syncModemSMS(ctx, deviceID)

	server.devices = fakeDeviceController{
		entry: device.Device{ID: deviceID, Discovered: true, Snapshot: &device.Snapshot{
			DeviceID: deviceID, IMEI: imei, ICCID: "iccid-b", IMSI: "imsi-b",
			Phone: device.PhoneNumber{Number: "+442222"},
		}},
		smsMessages: []device.SMSMessage{storedMessage},
	}
	server.syncModemSMS(ctx, deviceID)

	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ICCID != "iccid-a" ||
		messages[0].IMSI != "imsi-a" || messages[0].LocalPhone != "+441111" {
		t.Fatalf("message identity after profile B rescan = %#v", messages)
	}
}

func TestSMSThreadConfiguredDeviceUsesStableIMEI(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const imei = "867394042309830"
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20_2", Name: "EC20 renamed", ModemIMEI: imei,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		MessageID: "before-rename", DeviceID: "ec20_1", ModemIMEI: imei,
		IMSI: "imsi-a", Peer: "VOXI", Direction: "inbound", Body: "history",
	}); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: database}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/sms/thread?device_id=ec20_2&imsi=imsi-a&peer=VOXI",
		nil,
	)
	response := httptest.NewRecorder()
	server.handleSMSThread(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0]["modem_imei"] != imei {
		t.Fatalf("thread data = %#v", envelope.Data)
	}
}

func TestNormalizeSMSDeviceFilter(t *testing.T) {
	if got := normalizeSMSDeviceFilter(" ALL "); got != "" {
		t.Fatalf("all filter = %q", got)
	}
	if got := normalizeSMSDeviceFilter("EC20"); got != "EC20" {
		t.Fatalf("device filter = %q", got)
	}
}

func TestSupportsModemSMSStorageRejectsUSBReader(t *testing.T) {
	if supportsModemSMSStorage(store.Device{DeviceType: store.DeviceTypeUSBSIMReader}) {
		t.Fatal("USB SIM reader must not be polled with modem SMS AT commands")
	}
	if !supportsModemSMSStorage(store.Device{DeviceType: store.DeviceTypePCIeEC20EC25}) {
		t.Fatal("cellular modem should retain modem SMS storage synchronization")
	}
}

func TestSyncModemSMSLogsMultipartMessageOnlyOnceAcrossStoragesAndPolls(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const (
		deviceID = "ec20-1"
		imei     = "867394042309830"
	)
	if err := database.UpsertDevice(ctx, store.Device{
		ID: deviceID, Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		ModemIMEI: imei, SMSEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	receivedAt := time.Unix(1_700_000_000, 0).UTC()
	messages := make([]device.SMSMessage, 0, 6)
	for _, storageName := range []string{"SM", "ME"} {
		for sequence, body := range []string{"part one ", "part two ", "part three"} {
			messages = append(messages, device.SMSMessage{
				Index:                  sequence + 1,
				Storage:                storageName,
				StorageStatus:          device.SMSStatusReceivedUnread,
				Direction:              device.SMSDirectionReceived,
				From:                   "+447700900123",
				Text:                   body,
				Encoding:               device.SMSEncodingGSM7PDU,
				ServiceCenterTimestamp: &receivedAt,
				Concat: &device.SMSConcatInfo{
					Reference: 23,
					Total:     3,
					Sequence:  sequence + 1,
				},
				RawPDU: storageName + body,
			})
		}
	}
	hub := loghub.New(slog.NewTextHandler(io.Discard, nil), 100)
	server := &Server{
		store:  database,
		logger: slog.New(hub),
		devices: fakeDeviceController{
			entry: device.Device{
				ID: deviceID, Discovered: true,
				Snapshot: &device.Snapshot{DeviceID: deviceID, IMEI: imei, IMSI: "23433"},
			},
			smsMessages: messages,
		},
	}

	server.syncModemSMS(ctx, deviceID)
	server.syncModemSMS(ctx, deviceID)

	receivedLogs := hub.History(100, slog.LevelInfo, "cellular SMS received")
	if len(receivedLogs) != 1 {
		t.Fatalf("sms.received logs = %d, want 1: %#v", len(receivedLogs), receivedLogs)
	}
	parts := receivedLogs[0].Fields["parts"]
	if receivedLogs[0].Fields["event"] != "sms.received" || (parts != int64(3) && parts != 3) {
		t.Fatalf("received log fields = %#v", receivedLogs[0].Fields)
	}
	stored, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Body != "part one part two part three" {
		t.Fatalf("stored messages = %#v", stored)
	}
}

func TestSyncModemSMSSeparatesReusedConcatReferencesWithoutCursorChurn(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const (
		deviceID = "ec20-1"
		imei     = "867394042309830"
		peer     = "+447700900123"
	)
	if err := database.UpsertDevice(ctx, store.Device{
		ID: deviceID, Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		ModemIMEI: imei, SMSEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	receivedAt := time.Unix(1_700_000_000, 0).UTC()
	part := func(index, sequence int, body string) device.SMSMessage {
		return device.SMSMessage{
			Index: index, Storage: "SM", StorageStatus: device.SMSStatusReceivedUnread,
			Direction: device.SMSDirectionReceived, From: peer, Text: body,
			Encoding: device.SMSEncodingGSM7PDU, ServiceCenterTimestamp: &receivedAt,
			Concat: &device.SMSConcatInfo{Reference: 7, Total: 2, Sequence: sequence},
			RawPDU: body,
		}
	}
	messages := []device.SMSMessage{
		// Storage slot 21 and 43 contain unrelated messages that the modem did
		// not return in this filtered view. Multipart segments still belong to
		// the first segment's actual slot, rather than an inferred consecutive
		// slot number.
		part(20, 1, "old-a "), part(25, 2, "old-b"),
		part(42, 1, "new-a "), part(47, 2, "new-b"),
	}
	server := &Server{
		store: database, logger: regionTestLogger(),
		devices: fakeDeviceController{
			entry: device.Device{ID: deviceID, Discovered: true,
				Snapshot: &device.Snapshot{DeviceID: deviceID, IMEI: imei, IMSI: "23433"}},
			smsMessages: messages,
		},
	}

	server.syncModemSMS(ctx, deviceID)
	first, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: deviceID})
	if err != nil || len(first) != 2 {
		t.Fatalf("first sync messages = %#v, %v", first, err)
	}
	latest, err := database.LatestSMSMessageID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server.syncModemSMS(ctx, deviceID)
	second, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: deviceID})
	if err != nil || len(second) != 2 {
		t.Fatalf("second sync messages = %#v, %v", second, err)
	}
	fresh, err := database.ListInboundSMSAfterID(ctx, latest, 10)
	if err != nil || len(fresh) != 0 {
		t.Fatalf("notification rows after repeated scan = %#v, %v", fresh, err)
	}
}

func TestDeleteSMSRemovesModemCopyBeforeDatabaseRow(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const (
		deviceID = "ec20-1"
		imei     = "867394042309830"
	)
	if err := database.UpsertDevice(ctx, store.Device{
		ID: deviceID, Name: "EC20", DeviceType: store.DeviceTypePCIeEC20EC25,
		ModemIMEI: imei, SMSEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	receivedAt := time.Unix(1_700_000_000, 0).UTC()
	modemMessages := make([]device.SMSMessage, 0, 6)
	for _, storageName := range []string{"SM", "ME"} {
		for sequence, body := range []string{"cloud offer ", "claim link ", "reply R"} {
			modemMessages = append(modemMessages, device.SMSMessage{
				Index: sequence + 1, Storage: storageName,
				StorageStatus: device.SMSStatusReceivedUnread,
				Direction:     device.SMSDirectionReceived,
				From:          "+447700900123", Text: body,
				Encoding:               device.SMSEncodingGSM7PDU,
				ServiceCenterTimestamp: &receivedAt,
				Concat: &device.SMSConcatInfo{
					Reference: 23, Total: 3, Sequence: sequence + 1,
				},
				RawPDU: storageName + body,
			})
		}
	}
	controller := &smsDeletionController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: deviceID, Discovered: true,
			Snapshot: &device.Snapshot{DeviceID: deviceID, IMEI: imei, IMSI: "23433"},
		}},
		storedMessages: modemMessages,
	}
	hub := loghub.New(slog.NewTextHandler(io.Discard, nil), 100)
	server := &Server{store: database, logger: slog.New(hub), devices: controller}
	server.syncModemSMS(ctx, deviceID)
	stored, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: deviceID})
	if err != nil || len(stored) != 1 {
		t.Fatalf("initial stored messages = %#v, %v", stored, err)
	}
	deletedID := stored[0].ID

	request := httptest.NewRequest(http.MethodDelete, "/api/sms/messages/"+strconv.FormatInt(deletedID, 10), nil)
	response := httptest.NewRecorder()
	server.handleSMSMessage(response, request, strconv.FormatInt(deletedID, 10))
	if response.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", response.Code, response.Body.String())
	}
	controller.mu.Lock()
	deleted := append([]string(nil), controller.deleted...)
	remainingOnModem := len(controller.storedMessages)
	controller.mu.Unlock()
	sort.Strings(deleted)
	wantDeleted := []string{"ME:1", "ME:2", "ME:3", "SM:1", "SM:2", "SM:3"}
	if strings.Join(deleted, ",") != strings.Join(wantDeleted, ",") || remainingOnModem != 0 {
		t.Fatalf("modem deletion = %v, remaining = %d", deleted, remainingOnModem)
	}

	server.syncModemSMS(ctx, deviceID)
	stored, err = database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: deviceID})
	if err != nil || len(stored) != 0 {
		t.Fatalf("messages after delete and resync = %#v, %v", stored, err)
	}
	fresh, err := database.ListInboundSMSAfterID(ctx, deletedID, 10)
	if err != nil || len(fresh) != 0 {
		t.Fatalf("notification rows after delete and resync = %#v, %v", fresh, err)
	}
	if logs := hub.History(100, slog.LevelInfo, "cellular SMS received"); len(logs) != 1 {
		t.Fatalf("sms.received logs after delete and resync = %d, want 1", len(logs))
	}
}

func TestSMSSendOutcome(t *testing.T) {
	tests := []struct {
		name      string
		all       bool
		accepted  int
		total     int
		delivered bool
		want      string
	}{
		{name: "delivered", all: true, accepted: 1, total: 1, delivered: true, want: "delivered"},
		{name: "accepted but unconfirmed", all: true, accepted: 2, total: 2, want: "accepted_unconfirmed"},
		{name: "partial", accepted: 1, total: 2, want: "partial"},
		{name: "failed", total: 1, want: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := smsSendOutcome(test.all, test.accepted, test.total, test.delivered); got != test.want {
				t.Fatalf("smsSendOutcome() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBlockedSMSDestination(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		block bool
	}{
		{"e164 china", "+8613800138000", true},
		{"no plus china", "8613800138000", true},
		{"international prefix china", "008613800138000", true},
		{"spaced china", "+86 138 0013 8000", true},
		{"dashed china", "+86-138-0013-8000", true},
		{"us e164", "+12025550177", false},
		{"us no plus", "12025550177", false},
		{"uk e164", "+447700900123", false},
		{"italy", "+393331234567", false},
		{"russia", "+79161234567", false},
		{"japan", "+819012345678", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blocked, _ := blockedSMSDestination(test.phone)
			if blocked != test.block {
				t.Fatalf("blockedSMSDestination(%q) blocked = %v, want %v", test.phone, blocked, test.block)
			}
		})
	}
}

func TestHandleSMSSendEnforcesGlobalHourlyLimit(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := developer.SetSMSHourlyLimit(ctx, database, 1); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(ctx, store.Device{ID: "ec20_1", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	if reservation, err := database.ReserveSMSSend(ctx, "another-device", 1, time.Now().UTC()); err != nil || !reservation.Allowed {
		t.Fatalf("seed global SMS reservation = %+v, %v", reservation, err)
	}
	server := &Server{
		store:               database,
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		devices: fakeDeviceController{entry: device.Device{
			ID:         "ec20_1",
			Discovered: true,
			Snapshot:   &device.Snapshot{DeviceID: "ec20_1"},
		}},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sms/send",
		strings.NewReader(`{"device_id":"ec20_1","phone":"+447700900123","message":"hello"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSMSSend(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header is missing")
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "sms_rate_limited" {
		t.Fatalf("error code = %q, want sms_rate_limited", envelope.Error.Code)
	}
}
