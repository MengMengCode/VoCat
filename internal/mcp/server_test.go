package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

const (
	configuredID = "modem-a"
	physicalID   = "usb-serial-1"
	testIMEI     = "864451060041234"
)

type recordedSend struct {
	PhysicalID string
	Phone      string
	Message    string
}

type recordedSwitch struct {
	PhysicalID string
	ICCID      string
	AID        string
}

type fakeDevices struct {
	entries  []device.Device
	esimInfo device.EsimInfo
	switches []recordedSwitch
	partial  bool
}

func (f *fakeDevices) List() []device.Device { return f.entries }

func (f *fakeDevices) Get(id string) (device.Device, error) {
	for _, entry := range f.entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return device.Device{}, store.ErrNotFound
}

func (f *fakeDevices) Refresh(_ context.Context, id string) (device.Snapshot, error) {
	entry, err := f.Get(id)
	if err != nil || entry.Snapshot == nil {
		return device.Snapshot{}, errors.New("no snapshot")
	}
	return *entry.Snapshot, nil
}

func (f *fakeDevices) SendSMS(_ context.Context, physicalID, phone, message string) (device.SMSSendResult, error) {
	if f.partial {
		// Second part lost in flight: the submission half-landed and the
		// modem reported an error afterwards.
		return device.SMSSendResult{
			To:               phone,
			SubmissionStatus: "partial",
			SubmittedAt:      time.Now().UTC(),
			PartsTotal:       2,
			PartsAttempted:   2,
			PartsAccepted:    1,
			AllPartsAccepted: false,
			Encoding:         device.SMSEncoding("ucs2"),
		}, errors.New("second part timed out")
	}
	return device.SMSSendResult{
		To:               phone,
		SubmissionStatus: "submitted",
		SubmittedAt:      time.Now().UTC(),
		PartsTotal:       1,
		PartsAttempted:   1,
		PartsAccepted:    1,
		AllPartsAccepted: true,
		AcceptedByModem:  true,
		ReferenceKnown:   true,
		MessageReference: 42,
		ModemFinal:       "OK",
		ModemEvidence:    []string{"+CMGS: 42"},
		Encoding:         device.SMSEncoding("ucs2"),
	}, nil
}

func (f *fakeDevices) ESIMListProfiles(_ context.Context, _ string) (device.EsimInfo, error) {
	return f.esimInfo, nil
}

func (f *fakeDevices) ESIMSwitchProfile(_ context.Context, physicalID, iccid, aid string) error {
	f.switches = append(f.switches, recordedSwitch{PhysicalID: physicalID, ICCID: iccid, AID: aid})
	return nil
}

type fakeVoWiFi struct {
	state      vowifi.State
	reconnects int
}

func (f *fakeVoWiFi) State(string) (vowifi.State, error) { return f.state, nil }

func (f *fakeVoWiFi) RequestReconnect(string) (vowifi.State, error) {
	f.reconnects++
	return f.state, nil
}

func newTestServer(t *testing.T, devices DeviceManager, wifi VoWiFiController) (*Server, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:        configuredID,
		Name:      "Lab modem",
		ModemIMEI: testIMEI,
	}); err != nil {
		t.Fatalf("UpsertDevice() error = %v", err)
	}
	if wifi == nil {
		wifi = &fakeVoWiFi{}
	}
	server, err := New(Options{
		Store:   database,
		Devices: devices,
		VoWiFi:  wifi,
		Version: "test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server, database
}

func onlineEntry() device.Device {
	return device.Device{
		ID:         physicalID,
		Discovered: true,
		Snapshot: &device.Snapshot{
			DeviceID:     physicalID,
			Responsive:   true,
			Model:        "EC25",
			IMEI:         testIMEI,
			ICCID:        "89860123456789012345",
			IMSI:         "460000000000000",
			OperatorName: "China Mobile",
			SignalRaw:    func() *int { value := 25; return &value }(),
			Phone:        device.PhoneNumber{Number: "+8613800138000"},
		},
	}
}

func TestListDevicesAndOverviewRedactIMSI(t *testing.T) {
	ctx := context.Background()
	fake := &fakeDevices{entries: []device.Device{onlineEntry()}}
	server, _ := newTestServer(t, fake, nil)

	_, result, err := server.listDevices(ctx, nil, struct{}{})
	if err != nil {
		t.Fatalf("listDevices() error = %v", err)
	}
	if len(result.Devices) != 1 {
		t.Fatalf("devices = %+v", result.Devices)
	}
	summary := result.Devices[0]
	if summary.ID != configuredID || !summary.Online || summary.Model != "EC25" ||
		summary.IMEI != testIMEI || summary.OperatorName != "China Mobile" {
		t.Fatalf("summary = %+v", summary)
	}

	_, overview, err := server.deviceOverview(ctx, nil, deviceIDInput{DeviceID: configuredID})
	if err != nil {
		t.Fatalf("deviceOverview() error = %v", err)
	}
	encoded, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "imsi") || strings.Contains(string(encoded), "460000000000000") {
		t.Fatalf("overview leaks IMSI: %s", encoded)
	}
	if !overview.Responsive || overview.ICCID == "" || overview.IMEI != testIMEI {
		t.Fatalf("overview = %+v", overview)
	}
}

func TestUnknownDeviceFailsClosed(t *testing.T) {
	ctx := context.Background()
	server, _ := newTestServer(t, &fakeDevices{}, nil)
	if _, _, err := server.deviceOverview(ctx, nil, deviceIDInput{DeviceID: "ghost"}); err == nil {
		t.Fatal("overview of unknown device succeeded")
	}
	if _, _, err := server.smsSend(ctx, nil, smsSendInput{DeviceID: "ghost", Phone: "+15551234567", Message: "hi"}); err == nil {
		t.Fatal("send via unknown device succeeded")
	}
}

func TestSMSSendGuardrails(t *testing.T) {
	ctx := context.Background()
	server, database := newTestServer(t, &fakeDevices{entries: []device.Device{onlineEntry()}}, nil)

	// Barred destination is rejected before any modem interaction.
	if _, _, err := server.smsSend(ctx, nil, smsSendInput{
		DeviceID: configuredID,
		Phone:    "+8613800138000",
		Message:  "blocked",
	}); err == nil || !strings.Contains(err.Error(), "+86") {
		t.Fatalf("barred destination error = %v", err)
	}

	// Message validation runs before quota: a malformed recipient must not
	// consume the single allowed send slot.
	if _, _, err := server.smsSend(ctx, nil, smsSendInput{
		DeviceID: configuredID,
		Phone:    "not-a-phone",
		Message:  "x",
	}); err == nil {
		t.Fatal("invalid recipient accepted")
	}

	// Allow exactly one submission per rolling hour.
	if err := database.UpsertAppSetting(ctx, store.AppSetting{
		Key:   "developer.sms_hourly_limit",
		Value: json.RawMessage(`{"limit":1}`),
	}); err != nil {
		t.Fatalf("UpsertAppSetting() error = %v", err)
	}

	_, first, err := server.smsSend(ctx, nil, smsSendInput{
		DeviceID: configuredID,
		Phone:    "+15557654321",
		Message:  "hello from the agent",
	})
	if err != nil {
		t.Fatalf("first send error = %v", err)
	}
	if first.PartsAccepted != 1 || !first.AllPartsAccepted {
		t.Fatalf("first send output = %+v", first)
	}
	if _, _, err := server.smsSend(ctx, nil, smsSendInput{
		DeviceID: configuredID,
		Phone:    "+15557654321",
		Message:  "second",
	}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("second send error = %v, want rate limit", err)
	}

	events, err := database.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "mcp.sms.send" && event.Actor == "mcp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mcp.sms.send audit event: %+v", events)
	}
}

func TestSMSThreadsReadChronologically(t *testing.T) {
	ctx := context.Background()
	server, _ := newTestServer(t, &fakeDevices{entries: []device.Device{onlineEntry()}}, nil)

	for _, body := range []string{"first", "second"} {
		if _, err := server.options.Store.SaveSMSMessage(ctx, store.SMSMessage{
			MessageID: "test-" + body,
			DeviceID:  configuredID,
			ModemIMEI: testIMEI,
			Peer:      "+15557654321",
			Direction: "inbound",
			Body:      body,
			Source:    "cellular_at",
		}); err != nil {
			t.Fatalf("SaveSMSMessage() error = %v", err)
		}
	}

	_, threads, err := server.smsListThreads(ctx, nil, deviceIDInput{DeviceID: configuredID})
	if err != nil {
		t.Fatalf("smsListThreads() error = %v", err)
	}
	if len(threads.Threads) != 1 || threads.Threads[0].MessageCount != 2 {
		t.Fatalf("threads = %+v", threads.Threads)
	}

	_, thread, err := server.smsReadThread(ctx, nil, smsReadThreadInput{
		DeviceID: configuredID,
		Peer:     "+15557654321",
	})
	if err != nil {
		t.Fatalf("smsReadThread() error = %v", err)
	}
	if len(thread.Messages) != 2 ||
		thread.Messages[0].Body != "first" ||
		thread.Messages[1].Body != "second" {
		t.Fatalf("messages not chronological: %+v", thread.Messages)
	}
}

func TestESIMSwitchUsesConfiguredOperationAndAudits(t *testing.T) {
	ctx := context.Background()
	fake := &fakeDevices{
		entries: []device.Device{onlineEntry()},
		esimInfo: device.EsimInfo{
			EID: "89049032000000000000000000000000",
			Profiles: []device.EsimProfile{
				{ICCID: "8986000000000000001", AID: "AID1", State: 1, StateText: "enabled"},
				{ICCID: "8986000000000000002", AID: "AID2", State: 0, StateText: "disabled"},
			},
		},
	}
	server, database := newTestServer(t, fake, nil)
	var operations []recordedSwitch
	server.options.SwitchESIM = func(_ context.Context, deviceID, iccid, aid string) error {
		if iccid != "8986000000000000002" {
			return errors.New("target profile not installed")
		}
		operations = append(operations, recordedSwitch{PhysicalID: deviceID, ICCID: iccid, AID: aid})
		return nil
	}

	_, listed, err := server.esimListProfiles(ctx, nil, deviceIDInput{DeviceID: configuredID})
	if err != nil {
		t.Fatalf("esimListProfiles() error = %v", err)
	}
	if len(listed.Profiles) != 2 || !listed.Profiles[0].Enabled {
		t.Fatalf("profiles = %+v", listed.Profiles)
	}

	_, result, err := server.esimSwitchProfile(ctx, nil, esimSwitchInput{
		DeviceID: configuredID,
		ICCID:    "8986000000000000002",
	})
	if err != nil {
		t.Fatalf("esimSwitchProfile() error = %v", err)
	}
	if result.SwitchedTo != "8986000000000000002" {
		t.Fatalf("switched to = %+v", result)
	}
	if len(operations) != 1 || operations[0].AID != "" || operations[0].PhysicalID != configuredID || len(fake.switches) != 0 {
		t.Fatalf("configured operations = %+v, direct switches = %+v", operations, fake.switches)
	}

	// Unknown ICCIDs fail without touching the modem.
	if _, _, err := server.esimSwitchProfile(ctx, nil, esimSwitchInput{
		DeviceID: configuredID,
		ICCID:    "8986000000000000099",
	}); err == nil {
		t.Fatal("unknown ICCID accepted")
	}

	events, err := database.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "mcp.esim.switch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mcp.esim.switch audit event: %+v", events)
	}
}

func TestVowifiToolsRedactIMSIAndAudit(t *testing.T) {
	ctx := context.Background()
	wifi := &fakeVoWiFi{state: vowifi.State{
		DeviceID:        configuredID,
		Enabled:         true,
		Active:          true,
		Phase:           "ims_registered",
		IMSReady:        true,
		SMSReady:        true,
		IMSI:            "460000000000000",
		PhoneNumber:     "+8613800138000",
		CarrierProfile:  "cmcc",
		IMSRegistration: "registered",
		LastErrorClass:  "",
	}}
	server, database := newTestServer(t, &fakeDevices{}, wifi)

	_, status, err := server.vowifiStatus(ctx, nil, deviceIDInput{DeviceID: configuredID})
	if err != nil {
		t.Fatalf("vowifiStatus() error = %v", err)
	}
	if !status.Enabled || !status.IMSReady {
		t.Fatalf("status = %+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "460000000000000") {
		t.Fatalf("vowifi status leaks IMSI: %s", encoded)
	}

	if _, _, err := server.vowifiReconnect(ctx, nil, deviceIDInput{DeviceID: configuredID}); err != nil {
		t.Fatalf("vowifiReconnect() error = %v", err)
	}
	if wifi.reconnects != 1 {
		t.Fatalf("reconnects = %d", wifi.reconnects)
	}
	events, err := database.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "mcp.vowifi.reconnect" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mcp.vowifi.reconnect audit event: %+v", events)
	}
}

func TestReadOnlyServerOmitsWriteTools(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	readOnly, err := New(Options{
		Store:    database,
		Devices:  &fakeDevices{},
		VoWiFi:   &fakeVoWiFi{},
		ReadOnly: true,
		Version:  "test",
	})
	if err != nil {
		t.Fatalf("New(read-only) error = %v", err)
	}

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	runContext, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = readOnly.sdk.Run(runContext, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	for _, writeTool := range []string{"sms_send", "esim_switch_profile", "vowifi_reconnect"} {
		if names[writeTool] {
			t.Fatalf("read-only server exposes write tool %s: %v", writeTool, names)
		}
	}
	for _, readTool := range []string{"list_devices", "device_overview", "sms_list_threads", "sms_read_thread", "esim_list_profiles", "vowifi_status"} {
		if !names[readTool] {
			t.Fatalf("read-only server missing read tool %s: %v", readTool, names)
		}
	}
}

func TestSMSSendOfflineDeviceDoesNotBurnQuota(t *testing.T) {
	ctx := context.Background()
	// modem-a has a live physical match; modem-b is configured but offline.
	server, database := newTestServer(t, &fakeDevices{entries: []device.Device{onlineEntry()}}, nil)
	if err := database.UpsertDevice(ctx, store.Device{
		ID:        "modem-b",
		Name:      "Offline modem",
		ModemIMEI: "000000000000000",
	}); err != nil {
		t.Fatalf("UpsertDevice() error = %v", err)
	}
	if err := database.UpsertAppSetting(ctx, store.AppSetting{
		Key:   "developer.sms_hourly_limit",
		Value: json.RawMessage(`{"limit":1}`),
	}); err != nil {
		t.Fatalf("UpsertAppSetting() error = %v", err)
	}

	if _, _, err := server.smsSend(ctx, nil, smsSendInput{
		DeviceID: "modem-b",
		Phone:    "+15557654321",
		Message:  "anyone out there",
	}); err == nil || !strings.Contains(err.Error(), "not online") {
		t.Fatalf("offline send error = %v, want not online", err)
	}

	// The failed offline attempt consumed no send slot: the single allowed
	// submission still works through the online device.
	if _, output, err := server.smsSend(ctx, nil, smsSendInput{
		DeviceID: configuredID,
		Phone:    "+15557654321",
		Message:  "hello",
	}); err != nil {
		t.Fatalf("send after offline failure error = %v, want success", err)
	} else if !output.AllPartsAccepted {
		t.Fatalf("output = %+v", output)
	}
}

func TestSMSSendReportsPartialSubmission(t *testing.T) {
	ctx := context.Background()
	server, database := newTestServer(t, &fakeDevices{entries: []device.Device{onlineEntry()}, partial: true}, nil)
	if err := database.UpsertAppSetting(ctx, store.AppSetting{
		Key:   "developer.sms_hourly_limit",
		Value: json.RawMessage(`{"limit":5}`),
	}); err != nil {
		t.Fatal(err)
	}
	_, output, err := server.smsSend(ctx, nil, smsSendInput{
		DeviceID: configuredID,
		Phone:    "+15557654321",
		Message:  "long message that splits",
	})
	if err != nil {
		t.Fatalf("partial send error = %v, want a result the agent can inspect", err)
	}
	if output.Status != "partial_or_unconfirmed" || output.Warning == "" {
		t.Fatalf("output = %+v, want partial status with warning", output)
	}
	if output.PartsAttempted != 2 || output.PartsAccepted != 1 {
		t.Fatalf("parts = %d/%d, want 1 of 2", output.PartsAccepted, output.PartsAttempted)
	}
}
