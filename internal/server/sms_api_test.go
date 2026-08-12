package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type recordingSMSDeviceController struct {
	fakeDeviceController
	sendCalls          int
	boundSendCalls     int
	boundSendResult    device.SMSSendResult
	boundSendIdentity  device.SMSSubscriberIdentity
	boundSendErr       error
	onBoundSend        func()
	boundListMessages  []device.SMSMessage
	boundListIdentity  device.SMSSubscriberIdentity
	boundListTransport string
	boundListErr       error
	boundListCalls     int
	quietListCalls     int
}

func (controller *recordingSMSDeviceController) ListSMSBoundSubscriber(
	context.Context,
	string,
) (device.SMSSubscriberScan, error) {
	controller.boundListCalls++
	return device.SMSSubscriberScan{
		Messages:  controller.boundListMessages,
		Identity:  controller.boundListIdentity,
		Storages:  []string{"SM", "ME"},
		Transport: controller.boundListTransport,
	}, controller.boundListErr
}

func (controller *recordingSMSDeviceController) ListSMSBoundSubscriberQuiet(
	ctx context.Context,
	id string,
) (device.SMSSubscriberScan, error) {
	controller.quietListCalls++
	return controller.ListSMSBoundSubscriber(ctx, id)
}

func (controller *recordingSMSDeviceController) SendSMS(
	context.Context,
	string,
	string,
	string,
) (device.SMSSendResult, error) {
	controller.sendCalls++
	return device.SMSSendResult{}, errors.New("cellular SMS must not be called")
}

func (controller *recordingSMSDeviceController) SendSMSBoundSubscriber(
	context.Context,
	string,
	string,
	string,
) (device.SMSSendResult, device.SMSSubscriberIdentity, error) {
	controller.boundSendCalls++
	if controller.onBoundSend != nil {
		controller.onBoundSend()
	}
	return controller.boundSendResult, controller.boundSendIdentity, controller.boundSendErr
}

type recordingIMSSMSController struct {
	state      vowifi.State
	stateErr   error
	sendResult vowifi.SMSSubmitResult
	sendErr    error
	sendCalls  int
	onSend     func()
}

func (controller *recordingIMSSMSController) State(string) (vowifi.State, error) {
	return controller.state, controller.stateErr
}

func (controller *recordingIMSSMSController) RequestEnabled(string, bool) (vowifi.State, error) {
	return controller.state, controller.stateErr
}

func (controller *recordingIMSSMSController) RequestReconnect(string) (vowifi.State, error) {
	return controller.state, controller.stateErr
}

func (controller *recordingIMSSMSController) SendSMS(
	context.Context,
	string,
	vowifi.SMSSubmitRequest,
) (vowifi.SMSSubmitResult, error) {
	controller.sendCalls++
	if controller.onSend != nil {
		controller.onSend()
	}
	return controller.sendResult, controller.sendErr
}

type recordingDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (writer *recordingDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	writer.deadlines = append(writer.deadlines, deadline)
	return nil
}

func newVoWiFiSMSServer(
	t *testing.T,
	state vowifi.State,
) (*Server, *store.Store, *recordingSMSDeviceController, *recordingIMSSMSController, *device.Snapshot) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830", VoWiFiEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := &device.Snapshot{
		DeviceID: "ec20", ICCID: "8944100000000000000", IMSI: "234150123456789", IMEI: "867394042309830",
	}
	devices := &recordingSMSDeviceController{fakeDeviceController: fakeDeviceController{
		entry: device.Device{ID: "ec20", Discovered: true, Snapshot: snapshot},
	}}
	controller := &recordingIMSSMSController{state: state}
	server := &Server{
		store: database, devices: devices, vowifi: controller,
		logger: regionTestLogger(), maxRequestBodyBytes: 4096,
	}
	return server, database, devices, controller, snapshot
}

func submitVoWiFiSMSTestRequest(server *Server) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sms/send",
		strings.NewReader(`{"device_id":"ec20","phone":"+447700900123","message":"hello"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSMSSend(response, request)
	return response
}

func requireAPIErrorCode(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	var envelope struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, response.Body.String())
	}
	if envelope.Error == nil || envelope.Error.Code != code {
		t.Fatalf("error response = %#v", envelope.Error)
	}
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

func TestSMSThreadRequiresExplicitSubscriberScope(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{store: database}
	request := httptest.NewRequest(http.MethodGet, "/api/sms/thread?device_id=all&peer=VOXI", nil)
	response := httptest.NewRecorder()
	server.handleSMSThread(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	requireAPIErrorCode(t, response, "subscriber_required")
}

func TestSMSThreadDeleteOnlyRemovesExactSIM(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const imei = "867394042309830"
	for _, imsi := range []string{"imsi-a", "imsi-b"} {
		if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
			MessageID: "delete-" + imsi, DeviceID: "old-device", ModemIMEI: imei,
			IMSI: imsi, Peer: "VOXI", Direction: "inbound", Body: imsi,
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{store: database}
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/sms/thread?device_id=all&modem_imei="+imei+"&imsi=imsi-a&peer=VOXI",
		nil,
	)
	response := httptest.NewRecorder()
	server.handleSMSThread(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	a, err := database.ListSMSMessages(ctx, store.SMSFilter{
		ModemIMEI: imei, IMSI: "imsi-a", IMSIExact: true, Peer: "VOXI", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := database.ListSMSMessages(ctx, store.SMSFilter{
		ModemIMEI: imei, IMSI: "imsi-b", IMSIExact: true, Peer: "VOXI", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 0 || len(b) != 1 || b[0].IMSI != "imsi-b" {
		t.Fatalf("remaining threads: sim-a=%#v sim-b=%#v", a, b)
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

func TestSMSSendClearsServerWriteDeadline(t *testing.T) {
	writer := &recordingDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	request := httptest.NewRequest(http.MethodPost, "/api/sms/send", nil)
	server := &Server{}
	server.handleSMSSend(writer, request)
	if len(writer.deadlines) != 1 || !writer.deadlines[0].IsZero() {
		t.Fatalf("write deadlines = %#v, want one cleared deadline", writer.deadlines)
	}
}

func TestVoWiFiSMSSendFailsClosedWithoutCellularFallback(t *testing.T) {
	ready := vowifi.State{
		DeviceID: "ec20", Phase: vowifi.PhaseSMSReady, IMSReady: true, SMSReady: true,
		ICCID: "8944100000000000000", IMSI: "234150123456789",
	}
	tests := []struct {
		name      string
		configure func(*recordingIMSSMSController)
	}{
		{
			name: "state error",
			configure: func(controller *recordingIMSSMSController) {
				controller.stateErr = errors.New("runtime unavailable")
			},
		},
		{
			name: "runtime not ready",
			configure: func(controller *recordingIMSSMSController) {
				controller.state.Phase = vowifi.PhaseIMSReady
				controller.state.SMSReady = false
			},
		},
		{
			name: "session became not ready",
			configure: func(controller *recordingIMSSMSController) {
				controller.sendErr = vowifi.ErrSMSNotReady
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, devices, controller, _ := newVoWiFiSMSServer(t, ready)
			test.configure(controller)
			response := submitVoWiFiSMSTestRequest(server)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			requireAPIErrorCode(t, response, "ims_sms_not_ready")
			if devices.sendCalls != 0 {
				t.Fatalf("cellular SMS calls = %d", devices.sendCalls)
			}
		})
	}
}

func TestVoWiFiSMSSendRejectsLiveSIMIdentityMismatch(t *testing.T) {
	state := vowifi.State{
		DeviceID: "ec20", Phase: vowifi.PhaseSMSReady, IMSReady: true, SMSReady: true,
		ICCID: "8944100000000000000", IMSI: "234150999999999",
	}
	server, _, devices, controller, _ := newVoWiFiSMSServer(t, state)
	response := submitVoWiFiSMSTestRequest(server)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	requireAPIErrorCode(t, response, "ims_sms_not_ready")
	if controller.sendCalls != 0 || devices.sendCalls != 0 {
		t.Fatalf("IMS calls = %d, cellular calls = %d", controller.sendCalls, devices.sendCalls)
	}
}

func TestVoWiFiSMSSendPersistsCapturedRuntimeIMSI(t *testing.T) {
	const runtimeIMSI = "234150123456789"
	state := vowifi.State{
		DeviceID: "ec20", Phase: vowifi.PhaseSMSReady, IMSReady: true, SMSReady: true,
		ICCID: "8944100000000000000", IMSI: runtimeIMSI,
	}
	server, database, devices, controller, snapshot := newVoWiFiSMSServer(t, state)
	controller.sendResult = vowifi.SMSSubmitResult{
		To: "+447700900123", Encoding: "gsm7", SubmittedAt: time.Now().UTC(),
		PartsTotal: 1, PartsAttempted: 1, PartsAccepted: 1, AllPartsAccepted: true,
		SubmissionStatus: "accepted_by_ims",
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	controller.onSend = func() {
		// Simulate the live modem snapshot changing after the pre-send identity
		// check. Persistence must remain attributed to the IMS session that sent it.
		snapshot.IMSI = "234150000000000"
		cancelRequest()
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sms/send",
		strings.NewReader(`{"device_id":"ec20","phone":"+447700900123","message":"hello"}`),
	).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSMSSend(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if devices.sendCalls != 0 {
		t.Fatalf("cellular SMS calls = %d", devices.sendCalls)
	}
	messages, err := database.ListSMSMessages(context.Background(), store.SMSFilter{
		DeviceID: "ec20", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].IMSI != runtimeIMSI {
		t.Fatalf("stored messages = %#v", messages)
	}
}

func TestVoWiFiSMSSendPersistenceFailurePreservesRetryEvidence(t *testing.T) {
	state := vowifi.State{
		DeviceID: "ec20", Phase: vowifi.PhaseSMSReady, IMSReady: true, SMSReady: true,
		ICCID: "8944100000000000000", IMSI: "234150123456789",
	}
	server, database, devices, controller, _ := newVoWiFiSMSServer(t, state)
	submittedAt := time.Now().UTC()
	controller.sendResult = vowifi.SMSSubmitResult{
		To: "+447700900123", Encoding: "gsm7", SubmittedAt: submittedAt,
		PartsTotal: 2, PartsAttempted: 2, PartsAccepted: 1,
		SubmissionStatus: "partially_accepted_by_ims",
		PartResults: []vowifi.SMSSubmitPart{
			{
				Part: 1, Total: 2, Accepted: true,
				SubmissionStatus: "accepted_by_ims", SubmittedAt: submittedAt,
			},
			{
				Part: 2, Total: 2, Accepted: false,
				SubmissionStatus: "rejected_by_ims", SubmittedAt: submittedAt,
			},
		},
	}
	controller.sendErr = errors.New("IMS rejected part 2")
	var closeErr error
	controller.onSend = func() { closeErr = database.Close() }
	response := submitVoWiFiSMSTestRequest(server)
	if closeErr != nil {
		t.Fatalf("close store: %v", closeErr)
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code             string                 `json:"code"`
			Transport        string                 `json:"transport"`
			RetrySafe        *bool                  `json:"retry_safe"`
			PartsAttempted   int                    `json:"parts_attempted"`
			PartsAccepted    int                    `json:"parts_accepted"`
			PartResults      []vowifi.SMSSubmitPart `json:"part_results"`
			PersistenceState string                 `json:"persistence_state"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "sms_persistence_failed" ||
		envelope.Error.Transport != "ims" ||
		envelope.Error.RetrySafe == nil || *envelope.Error.RetrySafe ||
		envelope.Error.PartsAttempted != 2 || envelope.Error.PartsAccepted != 1 ||
		len(envelope.Error.PartResults) != 2 || envelope.Error.PersistenceState != "failed" {
		t.Fatalf("error evidence = %#v", envelope.Error)
	}
	if controller.sendCalls != 1 || devices.sendCalls != 0 {
		t.Fatalf("IMS calls = %d, cellular calls = %d", controller.sendCalls, devices.sendCalls)
	}
}

func TestCellularSMSSendPersistsAtomicallyCapturedIMSI(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := &device.Snapshot{
		DeviceID: "ec20", ICCID: "old-iccid", IMSI: "234150000000000", IMEI: "867394042309830",
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{
			entry: device.Device{ID: "ec20", Discovered: true, Snapshot: snapshot},
		},
		boundSendResult: device.SMSSendResult{
			To: "+447700900123", Encoding: device.SMSEncodingGSM7Text,
			SubmittedAt: time.Now().UTC(), PartsTotal: 1, PartsAttempted: 1,
			PartsAccepted: 1, AllPartsAccepted: true, SubmissionStatus: "accepted_by_modem",
		},
		boundSendIdentity: device.SMSSubscriberIdentity{
			ICCID: "live-iccid", IMSI: "234150123456789",
		},
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	devices.onBoundSend = cancelRequest
	server := &Server{
		store: database, devices: devices, logger: regionTestLogger(), maxRequestBodyBytes: 4096,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sms/send",
		strings.NewReader(`{"device_id":"ec20","phone":"+447700900123","message":"hello"}`),
	).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSMSSend(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if devices.boundSendCalls != 1 || devices.sendCalls != 0 {
		t.Fatalf("bound calls = %d, legacy calls = %d", devices.boundSendCalls, devices.sendCalls)
	}
	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].IMSI != devices.boundSendIdentity.IMSI {
		t.Fatalf("stored messages = %#v", messages)
	}
}

func TestRoamingQualcomm410SMSSendUsesQMIWithoutCellularData(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "wwan0", Name: "410", DeviceType: store.DeviceTypeWiFi410,
		ModemIMEI: "867394042309830", NetworkEnabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "wwan0", Discovered: true,
			Snapshot: &device.Snapshot{
				DeviceID: "wwan0", IMEI: "867394042309830",
				ICCID: "8944100000000000000", IMSI: "234150000000000",
				RegistrationStatus: 5,
			},
		}},
		boundSendResult: device.SMSSendResult{
			To: "+447700900123", Encoding: device.SMSEncodingGSM7PDU,
			Transport: "cellular_qmi", SubmittedAt: time.Now().UTC(),
			PartsTotal: 1, PartsAttempted: 1, PartsAccepted: 1,
			AcceptedByModem: true, AllPartsAccepted: true,
			SubmissionStatus: "accepted_by_modem", DeliveryStatus: "unknown",
		},
		boundSendIdentity: device.SMSSubscriberIdentity{
			ICCID: "8944100000000000000", IMSI: "234150123456789",
		},
	}
	server := &Server{
		store: database, devices: devices, logger: regionTestLogger(), maxRequestBodyBytes: 4096,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sms/send",
		strings.NewReader(`{"device_id":"wwan0","phone":"+447700900123","message":"hello"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSMSSend(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data["transport"] != "cellular_qmi" {
		t.Fatalf("transport = %#v", envelope.Data["transport"])
	}
	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "wwan0", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Source != "cellular_qmi" ||
		messages[0].IMSI != devices.boundSendIdentity.IMSI {
		t.Fatalf("stored messages = %#v", messages)
	}
}

func TestCellularQMIMultipartPartialResponsePersistsOneSubmission(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "wwan0", Name: "410", DeviceType: store.DeviceTypeWiFi410,
		ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	submittedAt := time.Now().UTC()
	concatReference := 73
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "wwan0", Discovered: true,
			Snapshot: &device.Snapshot{DeviceID: "wwan0", IMEI: "867394042309830"},
		}},
		boundSendResult: device.SMSSendResult{
			To: "+447700900123", Transport: "cellular_qmi",
			Encoding: device.SMSEncodingGSM7PDU, SubmittedAt: submittedAt,
			PartsTotal: 2, PartsAttempted: 2, PartsAccepted: 1,
			SubmissionStatus: "partially_accepted_by_modem",
			ConcatReference:  &concatReference,
			PartResults: []device.SMSPartResult{
				{
					Part: 1, Total: 2, AcceptedByModem: true,
					SubmissionStatus: "accepted_by_modem", SubmittedAt: submittedAt,
				},
				{
					Part: 2, Total: 2, AcceptedByModem: false,
					SubmissionStatus: "rejected_by_modem", SubmittedAt: submittedAt,
				},
			},
		},
		boundSendIdentity: device.SMSSubscriberIdentity{ICCID: "iccid-a", IMSI: "sim-a"},
		boundSendErr:      errors.New("QMI WMS rejected part 2"),
	}
	server := &Server{
		store: database, devices: devices, logger: regionTestLogger(), maxRequestBodyBytes: 4096,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sms/send",
		strings.NewReader(`{"device_id":"wwan0","phone":"+447700900123","message":"multipart"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSMSSend(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Transport      string `json:"transport"`
			RetrySafe      *bool  `json:"retry_safe"`
			PartsAttempted int    `json:"parts_attempted"`
			PartsAccepted  int    `json:"parts_accepted"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Transport != "cellular_qmi" ||
		envelope.Data.RetrySafe == nil || *envelope.Data.RetrySafe ||
		envelope.Data.PartsAttempted != 2 || envelope.Data.PartsAccepted != 1 {
		t.Fatalf("response data = %#v", envelope.Data)
	}
	if devices.boundSendCalls != 1 || devices.sendCalls != 0 {
		t.Fatalf("bound calls = %d, legacy calls = %d", devices.boundSendCalls, devices.sendCalls)
	}
	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "wwan0", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Source != "cellular_qmi" ||
		messages[0].IMSI != "sim-a" || messages[0].PartsTotal != 2 {
		t.Fatalf("stored messages = %#v", messages)
	}
}

func TestCellularSMSSendPersistenceFailurePreservesRetryEvidence(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "wwan0", Name: "410", DeviceType: store.DeviceTypeWiFi410,
		ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	submittedAt := time.Now().UTC()
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "wwan0", Discovered: true,
			Snapshot: &device.Snapshot{DeviceID: "wwan0", IMEI: "867394042309830"},
		}},
		boundSendResult: device.SMSSendResult{
			To: "+447700900123", Transport: "cellular_qmi",
			Encoding: device.SMSEncodingGSM7PDU, SubmittedAt: submittedAt,
			PartsTotal: 2, PartsAttempted: 2, PartsAccepted: 1,
			SubmissionStatus: "partially_accepted_by_modem",
			PartResults: []device.SMSPartResult{
				{
					Part: 1, Total: 2, AcceptedByModem: true,
					SubmissionStatus: "accepted_by_modem", SubmittedAt: submittedAt,
				},
				{
					Part: 2, Total: 2, AcceptedByModem: false,
					SubmissionStatus: "rejected_by_modem", SubmittedAt: submittedAt,
				},
			},
		},
		boundSendIdentity: device.SMSSubscriberIdentity{ICCID: "iccid-a", IMSI: "sim-a"},
		boundSendErr:      errors.New("QMI WMS rejected part 2"),
	}
	var closeErr error
	devices.onBoundSend = func() { closeErr = database.Close() }
	server := &Server{
		store: database, devices: devices, logger: regionTestLogger(), maxRequestBodyBytes: 4096,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sms/send",
		strings.NewReader(`{"device_id":"wwan0","phone":"+447700900123","message":"multipart"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSMSSend(response, request)
	if closeErr != nil {
		t.Fatalf("close store: %v", closeErr)
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code             string                 `json:"code"`
			Transport        string                 `json:"transport"`
			RetrySafe        *bool                  `json:"retry_safe"`
			PartsTotal       int                    `json:"parts_total"`
			PartsAttempted   int                    `json:"parts_attempted"`
			PartsAccepted    int                    `json:"parts_accepted"`
			PartResults      []device.SMSPartResult `json:"part_results"`
			PersistenceState string                 `json:"persistence_state"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "sms_persistence_failed" ||
		envelope.Error.Transport != "cellular_qmi" ||
		envelope.Error.RetrySafe == nil || *envelope.Error.RetrySafe ||
		envelope.Error.PartsTotal != 2 || envelope.Error.PartsAttempted != 2 ||
		envelope.Error.PartsAccepted != 1 || len(envelope.Error.PartResults) != 2 ||
		envelope.Error.PersistenceState != "failed" {
		t.Fatalf("error evidence = %#v", envelope.Error)
	}
	if devices.boundSendCalls != 1 || devices.sendCalls != 0 {
		t.Fatalf("bound calls = %d, legacy calls = %d", devices.boundSendCalls, devices.sendCalls)
	}
}

func TestModemSMSSyncPersistsAtomicallyCapturedIMSI(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "ec20", Discovered: true,
			Snapshot: &device.Snapshot{IMSI: "234150000000000", IMEI: "867394042309830"},
		}},
		boundListIdentity: device.SMSSubscriberIdentity{
			ICCID: "live-iccid", IMSI: "234150123456789",
		},
		boundListMessages: []device.SMSMessage{{
			Index: 1, Storage: "SM", StorageStatus: device.SMSStatusReceivedUnread,
			Direction: device.SMSDirectionReceived, From: "+447700900123", Text: "hello",
			RawPDU: "000405912143F500004210203040500005C82293F904",
		}},
	}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}
	server.syncModemSMS(ctx, "ec20")
	if devices.boundListCalls != 1 {
		t.Fatalf("bound list calls = %d", devices.boundListCalls)
	}
	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].IMSI != devices.boundListIdentity.IMSI {
		t.Fatalf("stored messages = %#v", messages)
	}
}

func TestRoamingQualcomm410SMSSyncPersistsQMITransport(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "wwan0", Name: "410", DeviceType: store.DeviceTypeWiFi410,
		ModemIMEI: "867394042309830", NetworkEnabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	messageReference := 0
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "wwan0", Discovered: true,
			Snapshot: &device.Snapshot{
				DeviceID: "wwan0", IMEI: "867394042309830",
				IMSI: "234150123456789", RegistrationStatus: 5,
			},
		}},
		boundListIdentity: device.SMSSubscriberIdentity{
			ICCID: "8944100000000000000", IMSI: "234150123456789",
		},
		boundListTransport: "cellular_qmi",
		boundListMessages: []device.SMSMessage{{
			Index: 7, Storage: "SM", StorageStatus: device.SMSStatusStoredSent,
			Direction: device.SMSDirectionSubmitted, To: "+447700900123", Text: "roaming hello",
			MessageReference: &messageReference,
			RawPDU:           "000405912143F500004210203040500005C82293F904",
		}},
	}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}
	server.syncModemSMS(ctx, "wwan0")
	if devices.boundListCalls != 1 {
		t.Fatalf("bound list calls = %d", devices.boundListCalls)
	}
	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "wwan0", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Source != "cellular_qmi" ||
		messages[0].IMSI != devices.boundListIdentity.IMSI {
		t.Fatalf("stored messages = %#v", messages)
	}
	var extra map[string]any
	if err := json.Unmarshal(messages[0].Extra, &extra); err != nil {
		t.Fatal(err)
	}
	if referenceKnown, ok := extra["reference_known"]; !ok || referenceKnown != false {
		t.Fatalf("reference_known = %#v (extra=%#v)", referenceKnown, extra)
	}
	if accepted, ok := extra["accepted_by_modem"]; !ok || accepted != false {
		t.Fatalf("accepted_by_modem = %#v (extra=%#v)", accepted, extra)
	}
}

func TestNativeWWANSMSSyncWaitsForVoWiFiRadioRestoration(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	// Deliberately retain a stale USB type: live wwan* discovery is the
	// authoritative signal for the native 410 QMI backend.
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "wwan0", Name: "410", DeviceType: store.DeviceTypePCIeEC20EC25,
		ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "wwan0", Discovered: true,
			Snapshot: &device.Snapshot{
				DeviceID: "wwan0", IMEI: "867394042309830",
			},
		}},
		boundListIdentity: device.SMSSubscriberIdentity{
			ICCID: "8944100000000000000", IMSI: "234150123456789",
		},
		boundListTransport: "cellular_qmi",
	}
	vowifiController := &fakeVoWiFiController{state: vowifi.State{
		Enabled: true, Phase: vowifi.PhaseSMSReady, IMSReady: true, SMSReady: true,
	}}
	server := &Server{
		store: database, devices: devices, vowifi: vowifiController, logger: regionTestLogger(),
	}

	server.syncModemSMS(ctx, "wwan0")
	if devices.boundListCalls != 0 {
		t.Fatalf("stable VoWiFi QMI scans = %d, want 0", devices.boundListCalls)
	}

	vowifiController.state = vowifi.State{Enabled: false, Phase: vowifi.PhaseStopping}
	server.syncModemSMS(ctx, "wwan0")
	if devices.boundListCalls != 0 {
		t.Fatalf("stopping VoWiFi QMI scans = %d, want 0", devices.boundListCalls)
	}

	vowifiController.state = vowifi.State{
		Enabled: false, Phase: vowifi.PhaseIdle,
		CleanupErrors: []string{"restore radio: operation failed"},
	}
	server.syncModemSMS(ctx, "wwan0")
	if devices.boundListCalls != 0 {
		t.Fatalf("failed radio restoration QMI scans = %d, want 0", devices.boundListCalls)
	}

	vowifiController.state = vowifi.State{
		Enabled: false, Phase: vowifi.PhaseIdle,
		CleanupErrors: []string{"close IMS: timeout"},
	}
	server.syncModemSMS(ctx, "wwan0")
	if devices.boundListCalls != 1 {
		t.Fatalf("post-restoration QMI scans = %d, want 1", devices.boundListCalls)
	}
	if devices.quietListCalls != 1 {
		t.Fatalf("background QMI scans using quiet reader = %d, want 1", devices.quietListCalls)
	}
}

func TestModemSMSStorageSameSlotAndPDUDoesNotCrossSIM(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	stored := device.SMSMessage{
		Index: 1, Storage: "SM", StorageStatus: device.SMSStatusReceivedUnread,
		Direction: device.SMSDirectionReceived, From: "+447700900123", Text: "same PDU",
		RawPDU: "000405912143F500004210203040500005C82293F904",
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "ec20", Discovered: true,
			Snapshot: &device.Snapshot{IMEI: "867394042309830"},
		}},
		boundListIdentity: device.SMSSubscriberIdentity{ICCID: "iccid-a", IMSI: "sim-a"},
		boundListMessages: []device.SMSMessage{stored},
	}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}
	server.syncModemSMS(ctx, "ec20")
	devices.boundListIdentity = device.SMSSubscriberIdentity{ICCID: "iccid-b", IMSI: "sim-b"}
	server.syncModemSMS(ctx, "ec20")

	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("stored messages = %#v", messages)
	}
	byIMSI := make(map[string]store.SMSMessage, len(messages))
	for _, message := range messages {
		byIMSI[message.IMSI] = message
	}
	if byIMSI["sim-a"].ID == 0 || byIMSI["sim-b"].ID == 0 ||
		byIMSI["sim-a"].MessageID == byIMSI["sim-b"].MessageID {
		t.Fatalf("subscriber-scoped messages = %#v", messages)
	}
}

func TestSMSStorageMessageIDScopesTransportHardwareAndSubscriber(t *testing.T) {
	digest := [32]byte{1, 2, 3}
	base := smsStorageMessageID("cellular_at", "imei-a", "sim-a", "SM", 7, digest)
	variants := []string{
		smsStorageMessageID("cellular_qmi", "imei-a", "sim-a", "SM", 7, digest),
		smsStorageMessageID("cellular_at", "imei-b", "sim-a", "SM", 7, digest),
		smsStorageMessageID("cellular_at", "imei-a", "sim-b", "SM", 7, digest),
	}
	for _, variant := range variants {
		if variant == base {
			t.Fatalf("message ID %q did not change with provenance", variant)
		}
	}
}

func TestSMSRawPDUFingerprintNormalizesHexFormatting(t *testing.T) {
	if smsRawPDUFingerprint(" aaBb ") != smsRawPDUFingerprint("AABB") {
		t.Fatal("equivalent hex PDU formatting produced different fingerprints")
	}
}

func TestMEConcatSubscriberEpochScopesBothSIMIdentifiers(t *testing.T) {
	base := smsMEConcatSubscriberEpoch(device.SMSSubscriberIdentity{
		ICCID: "iccid-a", IMSI: "sim-a",
	})
	if base == "" || base == smsMEConcatSubscriberEpoch(device.SMSSubscriberIdentity{
		ICCID: "iccid-b", IMSI: "sim-a",
	}) {
		t.Fatal("ME concat epoch did not change with ICCID")
	}
	if base == smsMEConcatSubscriberEpoch(device.SMSSubscriberIdentity{
		ICCID: "iccid-a", IMSI: "sim-b",
	}) {
		t.Fatal("ME concat epoch did not change with IMSI")
	}
	if base != smsMEConcatSubscriberEpoch(device.SMSSubscriberIdentity{
		ICCID: "  iccid-a  ", IMSI: " sim-a ",
	}) {
		t.Fatal("ME concat epoch changed with identifier whitespace")
	}
}

func TestMEAttributionUsesScanStartBaselineSnapshot(t *testing.T) {
	server := &Server{}
	const (
		provenance = "cellular_qmi\x00867394042309830"
		imsi       = "sim-a"
	)
	baselineTrusted := server.beginSMSMEBaseline(provenance, imsi)
	if baselineTrusted {
		t.Fatal("first ME baseline unexpectedly trusted")
	}
	server.completeSMSMEBaseline(provenance, imsi)
	if got := smsStorageMessageIMSI("ME", imsi, baselineTrusted); got != "" {
		t.Fatalf("same-scan ME attribution changed after concurrent completion: %q", got)
	}
	if got := smsStorageMessageIMSI("SM", imsi, baselineTrusted); got != imsi {
		t.Fatalf("SM attribution = %q, want %q", got, imsi)
	}
}

func TestIncompleteMEScanDoesNotCompleteSubscriberBaseline(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		invalid   device.SMSMessage
	}{
		{
			name:      "AT decode error",
			transport: "cellular_at",
			invalid: device.SMSMessage{
				Index: 1, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
				Direction: device.SMSDirectionReceived, From: "+447700900123",
				Text: "fallback", RawPDU: "AA", DecodeError: "malformed PDU",
			},
		},
		{
			name:      "QMI raw read failure",
			transport: "cellular_qmi",
			invalid: device.SMSMessage{
				Index: 1, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
				Direction:   device.SMSDirectionReceived,
				DecodeError: "QMI WMS raw-read failed: slot read failed",
			},
		},
		{
			name:      "AT empty peer",
			transport: "cellular_at",
			invalid: device.SMSMessage{
				Index: 1, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
				Direction: device.SMSDirectionReceived, Text: "missing peer", RawPDU: "AA",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := store.Open(ctx, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := database.UpsertDevice(ctx, store.Device{
				ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830",
			}); err != nil {
				t.Fatal(err)
			}
			devices := &recordingSMSDeviceController{
				fakeDeviceController: fakeDeviceController{entry: device.Device{
					ID: "ec20", Discovered: true,
					Snapshot: &device.Snapshot{IMEI: "867394042309830"},
				}},
				boundListIdentity:  device.SMSSubscriberIdentity{ICCID: "iccid-a", IMSI: "sim-a"},
				boundListMessages:  []device.SMSMessage{test.invalid},
				boundListTransport: test.transport,
			}
			server := &Server{store: database, devices: devices, logger: regionTestLogger()}

			server.syncModemSMS(ctx, "ec20")
			devices.boundListMessages = []device.SMSMessage{{
				Index: 1, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
				Direction: device.SMSDirectionReceived, From: "+447700900123",
				Text: "pre-existing", RawPDU: "AA",
			}}
			server.syncModemSMS(ctx, "ec20")
			devices.boundListMessages = append(devices.boundListMessages, device.SMSMessage{
				Index: 2, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
				Direction: device.SMSDirectionReceived, From: "+447700900123",
				Text: "after baseline", RawPDU: "BB",
			})
			server.syncModemSMS(ctx, "ec20")

			messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			byBody := make(map[string]string, len(messages))
			for _, message := range messages {
				byBody[message.Body] = message.IMSI
			}
			if byBody["pre-existing"] != "" || byBody["after baseline"] != "sim-a" {
				t.Fatalf("subscriber attribution = %#v (messages=%#v)", byBody, messages)
			}
		})
	}
}

func TestModemMESMSRequiresSubscriberBaselineBeforeAttribution(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "ec20", Discovered: true,
			Snapshot: &device.Snapshot{IMSI: "sim-a", IMEI: "867394042309830"},
		}},
		boundListIdentity: device.SMSSubscriberIdentity{ICCID: "iccid-a", IMSI: "sim-a"},
		boundListMessages: []device.SMSMessage{{
			Index: 1, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
			Direction: device.SMSDirectionReceived, From: "+447700900123", Text: "pre-existing",
			RawPDU: "AA",
		}},
	}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}

	server.syncModemSMS(ctx, "ec20")
	devices.boundListMessages = append(devices.boundListMessages, device.SMSMessage{
		Index: 2, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
		Direction: device.SMSDirectionReceived, From: "+447700900123", Text: "after baseline",
		RawPDU: "BB",
	})
	server.syncModemSMS(ctx, "ec20")

	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("stored messages = %#v", messages)
	}
	byBody := make(map[string]string, len(messages))
	for _, message := range messages {
		byBody[message.Body] = message.IMSI
	}
	if byBody["pre-existing"] != "" || byBody["after baseline"] != "sim-a" {
		t.Fatalf("subscriber attribution = %#v", byBody)
	}

	devices.boundListIdentity = device.SMSSubscriberIdentity{ICCID: "iccid-b", IMSI: "sim-b"}
	devices.boundListMessages = append(devices.boundListMessages, device.SMSMessage{
		Index: 3, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
		Direction: device.SMSDirectionReceived, From: "+447700900123", Text: "present at switch",
		RawPDU: "CC",
	})
	server.syncModemSMS(ctx, "ec20")
	messages, err = database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Body == "present at switch" && message.IMSI != "" {
			t.Fatalf("new subscriber first-scan ME message = %#v", message)
		}
	}

	devices.boundListMessages = append(devices.boundListMessages, device.SMSMessage{
		Index: 4, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
		Direction: device.SMSDirectionReceived, From: "+447700900123", Text: "after switch baseline",
		RawPDU: "DD",
	})
	server.syncModemSMS(ctx, "ec20")
	messages, err = database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Body == "after switch baseline" && message.IMSI != "sim-b" {
			t.Fatalf("new subscriber post-baseline ME message = %#v", message)
		}
	}
}

func TestModemMEIncompleteConcatDoesNotMergeAcrossSIM(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "ec20", Discovered: true,
			Snapshot: &device.Snapshot{IMEI: "867394042309830"},
		}},
		boundListIdentity:  device.SMSSubscriberIdentity{ICCID: "iccid-a", IMSI: "sim-a"},
		boundListTransport: "cellular_qmi",
	}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}

	// SIM A establishes a baseline, then contributes only the first segment.
	server.syncModemSMS(ctx, "ec20")
	devices.boundListMessages = []device.SMSMessage{{
		Index: 20, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
		Direction: device.SMSDirectionReceived, From: "+447700900125",
		Text: "sim-a-part", RawPDU: "AA20",
		Concat: &device.SMSConcatInfo{Reference: 91, Total: 2, Sequence: 1},
	}}
	server.syncModemSMS(ctx, "ec20")
	beforeSwitch, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeSwitch) != 1 || beforeSwitch[0].Body != "sim-a-part" ||
		beforeSwitch[0].IMSI != "sim-a" || store.ConcatSMSReadyToNotify(beforeSwitch[0].MessageID, beforeSwitch[0].Extra) {
		t.Fatalf("SIM A partial before switch = %#v", beforeSwitch)
	}
	originalAID := beforeSwitch[0].ID
	originalAMessageID := beforeSwitch[0].MessageID

	// SIM B reuses the same sender/reference/total for segment two. ME is
	// modem-owned, so its first full scan still contains SIM A's retained first
	// segment. The old physical segment must keep its original occurrence rather
	// than being copied into SIM B's epoch and merged with the new segment.
	devices.boundListIdentity = device.SMSSubscriberIdentity{ICCID: "iccid-b", IMSI: "sim-b"}
	devices.boundListMessages = append(devices.boundListMessages, device.SMSMessage{
		Index: 21, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
		Direction: device.SMSDirectionReceived, From: "+447700900125",
		Text: "sim-b-part", RawPDU: "BB21",
		Concat: &device.SMSConcatInfo{Reference: 91, Total: 2, Sequence: 2},
	})
	server.syncModemSMS(ctx, "ec20")

	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("cross-SIM concat rows = %#v", messages)
	}
	byBody := make(map[string]store.SMSMessage, len(messages))
	for _, message := range messages {
		byBody[message.Body] = message
	}
	if byBody["sim-a-part"].ID != originalAID ||
		byBody["sim-a-part"].MessageID != originalAMessageID ||
		byBody["sim-a-part"].IMSI != "sim-a" ||
		byBody["sim-b-part"].ID == 0 || byBody["sim-b-part"].IMSI != "" ||
		byBody["sim-a-part"].MessageID == byBody["sim-b-part"].MessageID ||
		store.ConcatSMSReadyToNotify(byBody["sim-a-part"].MessageID, byBody["sim-a-part"].Extra) ||
		store.ConcatSMSReadyToNotify(byBody["sim-b-part"].MessageID, byBody["sim-b-part"].Extra) {
		t.Fatalf("cross-SIM concat attribution = %#v", messages)
	}

	// A process restart loses the in-memory ME baseline, but the durable segment
	// occurrence owners must keep both partial rows stable without cursor churn.
	restartedServer := &Server{store: database, devices: devices, logger: regionTestLogger()}
	restartedServer.syncModemSMS(ctx, "ec20")
	afterRestart, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRestart) != 2 {
		t.Fatalf("cross-SIM concat rows after restart = %#v", afterRestart)
	}
	for _, message := range afterRestart {
		before := byBody[message.Body]
		if before.ID == 0 || message.ID != before.ID || message.MessageID != before.MessageID ||
			store.ConcatSMSReadyToNotify(message.MessageID, message.Extra) {
			t.Fatalf("cross-SIM concat row changed after restart: before=%#v after=%#v", before, message)
		}
	}
}

func TestModemMEPostBaselineMessageSurvivesServerRestartWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "ec20", Name: "EC20", ModemIMEI: "867394042309830",
	}); err != nil {
		t.Fatal(err)
	}
	devices := &recordingSMSDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{
			ID: "ec20", Discovered: true,
			Snapshot: &device.Snapshot{IMEI: "867394042309830"},
		}},
		boundListIdentity:  device.SMSSubscriberIdentity{ICCID: "iccid-a", IMSI: "sim-a"},
		boundListTransport: "cellular_qmi",
	}
	firstServer := &Server{store: database, devices: devices, logger: regionTestLogger()}
	// Establish an empty, complete baseline, then observe a new ME occurrence.
	firstServer.syncModemSMS(ctx, "ec20")
	devices.boundListMessages = []device.SMSMessage{
		{
			Index: 9, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
			Direction: device.SMSDirectionReceived, From: "+447700900123",
			Text: "after baseline", RawPDU: "aa",
		},
		{
			Index: 10, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
			Direction: device.SMSDirectionReceived, From: "+447700900124",
			Text: "long-1", RawPDU: "bb",
			Concat: &device.SMSConcatInfo{Reference: 44, Total: 2, Sequence: 1},
		},
		{
			Index: 11, Storage: "ME", StorageStatus: device.SMSStatusReceivedUnread,
			Direction: device.SMSDirectionReceived, From: "+447700900124",
			Text: "long-2", RawPDU: "cc",
			Concat: &device.SMSConcatInfo{Reference: 44, Total: 2, Sequence: 2},
		},
	}
	firstServer.syncModemSMS(ctx, "ec20")
	beforeRestartMessages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeRestartMessages) != 2 {
		t.Fatalf("stored messages before restart = %#v", beforeRestartMessages)
	}
	beforeRestartByPeer := make(map[string]store.SMSMessage, len(beforeRestartMessages))
	for _, message := range beforeRestartMessages {
		beforeRestartByPeer[message.Peer] = message
	}

	// A fresh server has no in-memory baseline. Its first scan must still hit
	// the durable ME occurrence and preserve the subscriber assigned earlier.
	devices.boundListMessages[0].RawPDU = " AA "
	devices.boundListMessages[1].RawPDU = " BB "
	devices.boundListMessages[2].RawPDU = " CC "
	restartedServer := &Server{store: database, devices: devices, logger: regionTestLogger()}
	restartedServer.syncModemSMS(ctx, "ec20")

	messages, err := database.ListSMSMessages(ctx, store.SMSFilter{DeviceID: "ec20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("stored messages after restart = %#v", messages)
	}
	byPeer := make(map[string]store.SMSMessage, len(messages))
	for _, message := range messages {
		byPeer[message.Peer] = message
	}
	if byPeer["+447700900123"].IMSI != "sim-a" ||
		byPeer["+447700900124"].IMSI != "sim-a" ||
		byPeer["+447700900124"].PartsTotal != 2 {
		t.Fatalf("stored messages after restart = %#v", messages)
	}
	for peer, before := range beforeRestartByPeer {
		after := byPeer[peer]
		if after.ID != before.ID || after.MessageID != before.MessageID {
			t.Fatalf("message changed across restart: before=%#v after=%#v", before, after)
		}
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
