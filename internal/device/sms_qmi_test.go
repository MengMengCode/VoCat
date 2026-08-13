package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

type fakeQMISMSListKey struct {
	storage uint8
	tag     qmi.MessageTagType
}

type fakeQMISMSReadKey struct {
	storage uint8
	index   uint32
}

type fakeQMISMSSession struct {
	iccid        string
	imsi         string
	iccidErr     error
	imsiErr      error
	transport    qmi.WMSTransportNetworkRegistration
	transportErr error
	sendErrs     []error
	sendFormats  []uint8
	sentPDUs     [][]byte
	lists        map[fakeQMISMSListKey][]qmiSMSListEntry
	listErrs     map[fakeQMISMSListKey]error
	reads        map[fakeQMISMSReadKey][]byte
	readErrs     map[fakeQMISMSReadKey]error
	afterSend    func(int)
	afterList    func(uint8, qmi.MessageTagType)
	calls        []string
	closeCount   int
}

type fakeQMIWMSRouteSession struct {
	*fakeQMISMSSession
	routes *qmi.WMSRouteConfig
}

func (session *fakeQMIWMSRouteSession) GetWMSRoutes(context.Context) (*qmi.WMSRouteConfig, error) {
	if session.routes == nil {
		return nil, nil
	}
	copyConfig := *session.routes
	copyConfig.Routes = append([]qmi.WMSRoute(nil), session.routes.Routes...)
	return &copyConfig, nil
}

func (session *fakeQMIWMSRouteSession) SetWMSRoutes(_ context.Context, routes []qmi.WMSRoute, transferStatusReportToClient bool) error {
	session.routes = &qmi.WMSRouteConfig{
		Routes:                       append([]qmi.WMSRoute(nil), routes...),
		TransferStatusReportToClient: transferStatusReportToClient,
	}
	return nil
}

type fakeQMIWMSReinitializer struct {
	ready      []qmi.WMSServiceReadyStatus
	readyCalls int
	resetCalls int
	bindCalls  int
	binding    uint8
	bindingErr error
	readyErr   error
}

func (fake *fakeQMIWMSReinitializer) ResetWMS(context.Context) error {
	fake.resetCalls++
	return nil
}

func (fake *fakeQMIWMSReinitializer) BindWMSSubscription(context.Context, uint8) error {
	fake.bindCalls++
	return nil
}

func (fake *fakeQMIWMSReinitializer) GetWMSSubscriptionBinding(context.Context) (uint8, error) {
	return fake.binding, fake.bindingErr
}

func (fake *fakeQMIWMSReinitializer) GetWMSServiceReady(context.Context) (qmi.WMSServiceReadyStatus, error) {
	fake.readyCalls++
	if fake.readyErr != nil {
		return qmi.WMSServiceReadyNotReady, fake.readyErr
	}
	if len(fake.ready) == 0 {
		return qmi.WMSServiceReady3GPP, nil
	}
	index := fake.readyCalls - 1
	if index >= len(fake.ready) {
		index = len(fake.ready) - 1
	}
	return fake.ready[index], nil
}

func (session *fakeQMISMSSession) GetICCID(context.Context) (string, error) {
	session.calls = append(session.calls, "get-iccid")
	return session.iccid, session.iccidErr
}

func (session *fakeQMISMSSession) GetIMSI(context.Context) (string, error) {
	session.calls = append(session.calls, "get-imsi")
	return session.imsi, session.imsiErr
}

func (session *fakeQMISMSSession) GetTransportNetworkRegistrationStatus(
	context.Context,
) (qmi.WMSTransportNetworkRegistration, error) {
	session.calls = append(session.calls, "get-transport")
	return session.transport, session.transportErr
}

func (session *fakeQMISMSSession) SendRawMessage(
	_ context.Context,
	format uint8,
	pdu []byte,
) error {
	session.calls = append(session.calls, "raw-send")
	session.sendFormats = append(session.sendFormats, format)
	session.sentPDUs = append(session.sentPDUs, append([]byte(nil), pdu...))
	if session.afterSend != nil {
		session.afterSend(len(session.sentPDUs))
	}
	if len(session.sendErrs) == 0 {
		return nil
	}
	err := session.sendErrs[0]
	session.sendErrs = session.sendErrs[1:]
	return err
}

func (session *fakeQMISMSSession) ListMessages(
	_ context.Context,
	storage uint8,
	tag qmi.MessageTagType,
) ([]qmiSMSListEntry, error) {
	session.calls = append(session.calls, fmt.Sprintf("list-%d-%d", storage, tag))
	key := fakeQMISMSListKey{storage: storage, tag: tag}
	entries := append([]qmiSMSListEntry(nil), session.lists[key]...)
	if session.afterList != nil {
		session.afterList(storage, tag)
	}
	return entries, session.listErrs[key]
}

func (session *fakeQMISMSSession) RawReadMessage(
	_ context.Context,
	storage uint8,
	index uint32,
) ([]byte, error) {
	session.calls = append(session.calls, fmt.Sprintf("read-%d-%d", storage, index))
	key := fakeQMISMSReadKey{storage: storage, index: index}
	return append([]byte(nil), session.reads[key]...), session.readErrs[key]
}

func (session *fakeQMISMSSession) Close() error {
	session.closeCount++
	return nil
}

func newStartedNativeSMSManager(t *testing.T) (*Manager, *staticOpener, string) {
	t.Helper()
	const id = "wwan0"
	atOpener := &staticOpener{client: &transcriptClient{}}
	manager, err := NewManager(Options{
		Discoverer: staticDiscoverer{candidates: []modem.Candidate{{
			ID:               id,
			Product:          "410 WiFi stick",
			QMIControl:       "/dev/wwan0qmi0",
			NetworkInterface: "wwan0",
			ATPort: modem.Port{
				Path: "/dev/wwan0at0",
				Name: "wwan0at0",
				Role: modem.PortRoleAT,
			},
		}}},
		Opener: atOpener,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager, atOpener, id
}

func TestQMIWMSContextErrorCodeClassification(t *testing.T) {
	invalidArgument := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSListMessages,
		ErrorCode: qmi.QMIErrInvalidArg,
	}
	if qmi.QMIErrInvalidArg != 0x0030 {
		t.Fatalf("QMIErrInvalidArg = 0x%04x, want 0x0030", qmi.QMIErrInvalidArg)
	}
	if qmi.QMIErrCardCallControlRefFail != 0x0060 {
		t.Fatalf("QMIErrCardCallControlRefFail = 0x%04x, want 0x0060", qmi.QMIErrCardCallControlRefFail)
	}
	if !isQMIWMSContextFailure(invalidArgument) || isQMIWMSCardCallControlFailure(invalidArgument) {
		t.Fatal("standard INVALID_ARG must trigger WMS context recovery, not card-call-control labeling")
	}

	cardCallControl := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSListMessages,
		ErrorCode: qmi.QMIErrCardCallControlRefFail,
	}
	if !isQMIWMSContextFailure(cardCallControl) || !isQMIWMSCardCallControlFailure(cardCallControl) {
		t.Fatal("0x0060 must trigger both recovery and the card-call-control classification")
	}

	otherService := *invalidArgument
	otherService.Service = qmi.ServiceUIM
	if isQMIWMSContextFailure(&otherService) {
		t.Fatal("an INVALID_ARG from another QMI service must not trigger WMS recovery")
	}
}

func TestQMIWMSNVFallbackRoutes(t *testing.T) {
	routes := []qmi.WMSRoute{
		{MessageType: qmi.WMSMessageTypePointToPoint, MessageClass: qmi.WMSMessageClass0, StorageType: qmi.WMSStorageTypeNone, ReceiptAction: qmi.WMSReceiptActionTransferOnly},
		{MessageType: qmi.WMSMessageTypePointToPoint, MessageClass: qmi.WMSMessageClass1, StorageType: qmi.WMSStorageTypeNone, ReceiptAction: qmi.WMSReceiptActionTransferOnly},
		{MessageType: qmi.WMSMessageTypePointToPoint, MessageClass: qmi.WMSMessageClass2, StorageType: qmi.WMSStorageTypeUIM, ReceiptAction: qmi.WMSReceiptActionStoreAndNotify},
		{MessageType: qmi.WMSMessageTypePointToPoint, MessageClass: qmi.WMSMessageClass3, StorageType: qmi.WMSStorageTypeNone, ReceiptAction: qmi.WMSReceiptActionTransferOnly},
	}
	fallback, changed := qmiWMSNVFallbackRoutes(routes)
	if !changed || countQMIWMSNVFallbackRoutes(routes) != 3 {
		t.Fatalf("fallback changed=%v count=%d", changed, countQMIWMSNVFallbackRoutes(routes))
	}
	for index, route := range fallback {
		if index == 2 {
			if route.StorageType != qmi.WMSStorageTypeUIM || route.ReceiptAction != qmi.WMSReceiptActionStoreAndNotify {
				t.Fatalf("healthy class-2 UIM route changed: %#v", route)
			}
			continue
		}
		if route.StorageType != qmi.WMSStorageTypeNV || route.ReceiptAction != qmi.WMSReceiptActionStoreAndNotify {
			t.Fatalf("route %d not moved to NV/store-and-notify: %#v", index, route)
		}
	}
	if _, changed := qmiWMSNVFallbackRoutes(fallback); changed {
		t.Fatal("NV/store-and-notify routes must be idempotent")
	}
}

func TestWaitForQMIWMSServiceReadyPollsTransientNotReady(t *testing.T) {
	fake := &fakeQMIWMSReinitializer{ready: []qmi.WMSServiceReadyStatus{
		qmi.WMSServiceReadyNotReady,
		qmi.WMSServiceReady3GPP,
	}}
	manager := &Manager{commandTimeout: time.Second}
	started := time.Now()
	ready, err := manager.waitForQMIWMSServiceReady(context.Background(), fake)
	if err != nil || ready != qmi.WMSServiceReady3GPP || fake.readyCalls != 2 {
		t.Fatalf("ready=%v err=%v calls=%d", ready, err, fake.readyCalls)
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Fatalf("poll returned without waiting for transient NOT_READY: %s", elapsed)
	}
}

func TestNativeQMISendBindsLiveIdentityAndAcceptsWithoutMessageReference(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	manager.mu.Lock()
	manager.devices[id].snapshot = &Snapshot{IMSI: "460001234567890"}
	manager.mu.Unlock()

	session := &fakeQMISMSSession{
		iccid:     "8986001234567890123",
		imsi:      "515031234567890",
		transport: qmi.WMSTransportNetworkRegistrationFullService,
	}
	var openedPath string
	manager.qmiSMSOpener = func(_ context.Context, path string) (qmiSMSSession, error) {
		openedPath = path
		return session, nil
	}

	result, identity, err := manager.SendSMSBoundSubscriber(
		context.Background(),
		id,
		"0012345",
		"HELLO",
	)
	if err != nil {
		t.Fatalf("SendSMSBoundSubscriber: %v", err)
	}
	if identity.ICCID != session.iccid || identity.IMSI != session.imsi {
		t.Fatalf("identity = %#v", identity)
	}
	if result.Transport != SMSTransportCellularQMI || result.Encoding != SMSEncodingGSM7PDU ||
		!result.AcceptedByModem ||
		!result.AllPartsAccepted || result.ReferenceKnown || result.MessageReference != 0 ||
		result.PartsAttempted != 1 || result.PartsAccepted != 1 ||
		result.SubmissionStatus != "accepted_by_modem" || len(result.PartResults) != 1 ||
		result.PartResults[0].ReferenceKnown || !result.PartResults[0].AcceptedByModem {
		t.Fatalf("result = %#v", result)
	}
	if openedPath != "/dev/wwan0qmi0" || atOpener.openCount != 0 {
		t.Fatalf("QMI path/AT opens = %q/%d", openedPath, atOpener.openCount)
	}
	if len(session.sentPDUs) != 1 || session.sendFormats[0] != qmiSMSFormatGW ||
		len(session.sentPDUs[0]) < 2 || session.sentPDUs[0][0] != 0 {
		t.Fatalf("raw sends = %X formats=%v", session.sentPDUs, session.sendFormats)
	}
	decoded, decodeErr := decodeSMSPDU(hex.EncodeToString(session.sentPDUs[0]))
	if decodeErr != nil || decoded.To != "+12345" || decoded.Text != "HELLO" {
		t.Fatalf("raw-send PDU = %#v, %v", decoded, decodeErr)
	}
	if !reflect.DeepEqual(session.calls, []string{
		"get-iccid", "get-imsi", "get-transport", "raw-send",
	}) {
		t.Fatalf("calls = %v", session.calls)
	}
	if session.closeCount != 1 {
		t.Fatalf("close count = %d", session.closeCount)
	}
}

func TestNativeQMIUnboundSendAlsoUsesLiveIdentity(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	session := &fakeQMISMSSession{
		iccid:     "8986001234567890123",
		imsi:      "515031234567890",
		transport: qmi.WMSTransportNetworkRegistrationFullService,
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	result, err := manager.SendSMS(context.Background(), id, "+12345", "HELLO")
	if err != nil || !result.AcceptedByModem || result.Transport != SMSTransportCellularQMI {
		t.Fatalf("SendSMS = (%#v, %v)", result, err)
	}
	if len(session.calls) < 3 || session.calls[0] != "get-iccid" || session.calls[1] != "get-imsi" {
		t.Fatalf("calls = %v", session.calls)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times", atOpener.openCount)
	}
}

func TestNativeWWANSMSFailsClosedWhileQMIControlIsMissing(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	manager.mu.Lock()
	manager.devices[id].candidate.QMIControl = ""
	manager.mu.Unlock()
	qmiOpenCount := 0
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		qmiOpenCount++
		return nil, errors.New("unexpected QMI open")
	}

	result, err := manager.SendSMS(context.Background(), id, "+12345", "HELLO")
	if !errors.Is(err, ErrSMSTransportUnavailable) ||
		result.Transport != SMSTransportCellularQMI || result.SubmissionStatus != "transport_unavailable" {
		t.Fatalf("SendSMS = (%#v, %v)", result, err)
	}
	boundResult, _, err := manager.SendSMSBoundSubscriber(
		context.Background(), id, "+12345", "HELLO",
	)
	if !errors.Is(err, ErrSMSTransportUnavailable) ||
		boundResult.Transport != SMSTransportCellularQMI {
		t.Fatalf("SendSMSBoundSubscriber = (%#v, %v)", boundResult, err)
	}
	if _, err := manager.ListSMS(context.Background(), id); !errors.Is(err, ErrSMSTransportUnavailable) {
		t.Fatalf("ListSMS error = %v", err)
	}
	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if !errors.Is(err, ErrSMSTransportUnavailable) || scan.Transport != SMSTransportCellularQMI {
		t.Fatalf("ListSMSBoundSubscriber = (%#v, %v)", scan, err)
	}
	if qmiOpenCount != 0 || atOpener.openCount != 0 {
		t.Fatalf("QMI/AT opens = %d/%d", qmiOpenCount, atOpener.openCount)
	}
}

func TestNativeQMISendTransportPreflight(t *testing.T) {
	blocked := []qmi.WMSTransportNetworkRegistration{
		qmi.WMSTransportNetworkRegistrationNoService,
		qmi.WMSTransportNetworkRegistrationFailure,
		qmi.WMSTransportNetworkRegistrationLimitedService,
		qmi.WMSTransportNetworkRegistration(0xff),
	}
	for _, status := range blocked {
		t.Run(status.String(), func(t *testing.T) {
			manager, atOpener, id := newStartedNativeSMSManager(t)
			session := &fakeQMISMSSession{
				iccid:     "8986001234567890123",
				imsi:      "515031234567890",
				transport: status,
			}
			manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
				return session, nil
			}

			result, err := manager.SendSMS(context.Background(), id, "+12345", "HELLO")
			if !errors.Is(err, ErrSMSTransportUnavailable) {
				t.Fatalf("error = %v", err)
			}
			if result.SubmissionStatus != "transport_unavailable" || result.PartsAttempted != 0 ||
				len(session.sentPDUs) != 0 || atOpener.openCount != 0 {
				t.Fatalf("result/session = %#v / %#v", result, session)
			}
		})
	}

	for _, tc := range []struct {
		name         string
		transport    qmi.WMSTransportNetworkRegistration
		transportErr error
	}{
		{
			name:      "in-process",
			transport: qmi.WMSTransportNetworkRegistrationInProcess,
		},
		{
			name: "unsupported",
			transportErr: &qmi.QMIError{
				Service:   qmi.ServiceWMS,
				MessageID: qmi.WMSGetTransportNetworkRegistrationStatus,
				ErrorCode: qmi.QMIErrOpDeviceUnsupported,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, atOpener, id := newStartedNativeSMSManager(t)
			session := &fakeQMISMSSession{
				iccid:        "8986001234567890123",
				imsi:         "515031234567890",
				transport:    tc.transport,
				transportErr: tc.transportErr,
			}
			manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
				return session, nil
			}

			result, err := manager.SendSMS(context.Background(), id, "+12345", "HELLO")
			if err != nil || !result.AcceptedByModem || len(session.sentPDUs) != 1 {
				t.Fatalf("SendSMS = (%#v, %v), sends=%d", result, err, len(session.sentPDUs))
			}
			if atOpener.openCount != 0 {
				t.Fatalf("AT opener used %d times", atOpener.openCount)
			}
		})
	}
}

func TestNativeQMISendTransportQueryTimeoutFailsWithoutATFallback(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	session := &fakeQMISMSSession{
		iccid:        "8986001234567890123",
		imsi:         "515031234567890",
		transportErr: context.DeadlineExceeded,
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	result, err := manager.SendSMS(context.Background(), id, "+12345", "HELLO")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if result.Transport != SMSTransportCellularQMI || result.SubmissionStatus != "setup_failed" ||
		result.PartsAttempted != 0 || len(session.sentPDUs) != 0 || atOpener.openCount != 0 {
		t.Fatalf("result/session/AT = %#v / sends=%d / opens=%d", result, len(session.sentPDUs), atOpener.openCount)
	}
}

func TestNativeQMIRawSendTimeoutIsUnknownWithoutATFallback(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	session := &fakeQMISMSSession{
		iccid:     "8986001234567890123",
		imsi:      "515031234567890",
		transport: qmi.WMSTransportNetworkRegistrationFullService,
		sendErrs:  []error{context.DeadlineExceeded},
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	result, err := manager.SendSMS(context.Background(), id, "+12345", "HELLO")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if result.Transport != SMSTransportCellularQMI || result.SubmissionStatus != "unknown" ||
		result.PartsAttempted != 1 || result.PartsAccepted != 0 || len(result.PartResults) != 1 ||
		result.PartResults[0].SubmissionStatus != "unknown" || result.PartResults[0].AcceptedByModem ||
		len(session.sentPDUs) != 1 || atOpener.openCount != 0 {
		t.Fatalf("result/session/AT = %#v / sends=%d / opens=%d", result, len(session.sentPDUs), atOpener.openCount)
	}
}

func TestNativeQMIMultipartSendPreservesPartialAcceptanceEvidence(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	reject := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSRawSend,
		ErrorCode: qmi.QMIErrCallFailed,
	}
	session := &fakeQMISMSSession{
		iccid:     "8986001234567890123",
		imsi:      "515031234567890",
		transport: qmi.WMSTransportNetworkRegistrationFullService,
		sendErrs:  []error{nil, reject},
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	result, err := manager.SendSMS(
		context.Background(),
		id,
		"+12345",
		strings.Repeat("A", 161),
	)
	if !errors.Is(err, reject) {
		t.Fatalf("error = %v", err)
	}
	if result.Transport != SMSTransportCellularQMI || result.PartsTotal != 2 ||
		result.PartsAttempted != 2 || result.PartsAccepted != 1 || result.AcceptedByModem ||
		result.AllPartsAccepted || result.SubmissionStatus != "partially_accepted_by_modem" ||
		len(result.PartResults) != 2 || !result.PartResults[0].AcceptedByModem ||
		result.PartResults[1].AcceptedByModem ||
		result.PartResults[1].SubmissionStatus != "rejected_by_modem" {
		t.Fatalf("result = %#v", result)
	}
	if len(session.sentPDUs) != 2 || atOpener.openCount != 0 {
		t.Fatalf("sends/AT opens = %d/%d", len(session.sentPDUs), atOpener.openCount)
	}
	for index, raw := range session.sentPDUs {
		message, decodeErr := decodeSMSPDU(hex.EncodeToString(raw))
		if decodeErr != nil || message.Concat == nil || message.Concat.Total != 2 ||
			message.Concat.Sequence != index+1 {
			t.Fatalf("part %d = %#v, %v", index+1, message, decodeErr)
		}
	}
}

func TestNativeQMIMultipartStopsIfLiveControlChanges(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	session := &fakeQMISMSSession{
		iccid:     "8986001234567890123",
		imsi:      "515031234567890",
		transport: qmi.WMSTransportNetworkRegistrationFullService,
	}
	session.afterSend = func(count int) {
		if count != 1 {
			return
		}
		manager.mu.Lock()
		manager.devices[id].candidate.QMIControl = ""
		manager.mu.Unlock()
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	result, err := manager.SendSMS(
		context.Background(), id, "+12345", strings.Repeat("A", 161),
	)
	if !errors.Is(err, ErrSMSTransportUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if result.SubmissionStatus != "partially_accepted_by_modem" || result.PartsTotal != 2 ||
		result.PartsAttempted != 1 || result.PartsAccepted != 1 || len(result.PartResults) != 1 ||
		!result.PartResults[0].AcceptedByModem || len(session.sentPDUs) != 1 {
		t.Fatalf("result/session = %#v / sends=%d", result, len(session.sentPDUs))
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times", atOpener.openCount)
	}
}

func TestNativeQMIListScansUIMAndNVWithTagMetadata(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	const (
		deliverPDU = "000405912143F500004210203040500005C82293F904"
		submitPDU  = "00010005912143F500000100"
	)
	deliverRaw, _ := hex.DecodeString(deliverPDU)
	submitRaw, _ := hex.DecodeString(submitPDU)
	session := &fakeQMISMSSession{
		iccid: "8986001234567890123",
		imsi:  "515031234567890",
		lists: map[fakeQMISMSListKey][]qmiSMSListEntry{
			{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: {
				{Index: 7, Tag: qmi.TagTypeMTNotRead},
			},
			{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTRead}: {
				{Index: 8, Tag: qmi.TagTypeMTRead},
			},
			{storage: qmiSMSStorageNV, tag: qmi.TagTypeMOSent}: {
				{Index: 3, Tag: qmi.TagTypeMOSent},
			},
			{storage: qmiSMSStorageNV, tag: qmi.TagTypeMONotSent}: {
				{Index: 4, Tag: qmi.TagTypeMONotSent},
			},
		},
		reads: map[fakeQMISMSReadKey][]byte{
			{storage: qmiSMSStorageUIM, index: 7}: deliverRaw,
			{storage: qmiSMSStorageUIM, index: 8}: {0xde, 0xad},
			// Exercise the direct-TPDU fallback while preserving the actual raw PDU.
			{storage: qmiSMSStorageNV, index: 3}: submitRaw[1:],
		},
		readErrs: map[fakeQMISMSReadKey]error{
			{storage: qmiSMSStorageNV, index: 4}: errors.New("slot read failed"),
		},
	}
	var openedPath string
	manager.qmiSMSOpener = func(_ context.Context, path string) (qmiSMSSession, error) {
		openedPath = path
		return session, nil
	}

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("ListSMSBoundSubscriber: %v", err)
	}
	if scan.Transport != SMSTransportCellularQMI || scan.Identity.ICCID != session.iccid ||
		scan.Identity.IMSI != session.imsi || !reflect.DeepEqual(scan.Storages, []string{"SM"}) {
		t.Fatalf("scan identity/storages = %#v", scan)
	}
	if openedPath != "/dev/wwan0qmi0" || atOpener.openCount != 0 || session.closeCount != 1 {
		t.Fatalf("path/AT/close = %q/%d/%d", openedPath, atOpener.openCount, session.closeCount)
	}
	if len(scan.Messages) != 4 {
		t.Fatalf("messages = %#v", scan.Messages)
	}
	for _, message := range scan.Messages {
		if message.Transport != SMSTransportCellularQMI {
			t.Fatalf("message transport = %#v", message)
		}
	}
	if got := scan.Messages[0]; got.Index != 7 || got.Storage != "SM" ||
		got.StorageStatus != SMSStatusReceivedUnread || got.Direction != SMSDirectionReceived ||
		got.Text != "HELLO" || got.RawPDU != deliverPDU || got.DecodeError != "" {
		t.Fatalf("SM unread = %#v", got)
	}
	if got := scan.Messages[1]; got.Index != 8 || got.StorageStatus != SMSStatusReceivedRead ||
		got.Direction != SMSDirectionReceived || got.RawPDU != "DEAD" || got.DecodeError == "" {
		t.Fatalf("SM malformed = %#v", got)
	}
	if got := scan.Messages[2]; got.Index != 3 || got.Storage != "ME" ||
		got.StorageStatus != SMSStatusStoredSent || got.Direction != SMSDirectionSubmitted ||
		got.Text != "@" || got.RawPDU != submitPDU[2:] || got.DecodeError != "" {
		t.Fatalf("ME sent = %#v", got)
	}
	if got := scan.Messages[3]; got.Index != 4 || got.StorageStatus != SMSStatusStoredUnsent ||
		got.Direction != SMSDirectionSubmitted || got.RawPDU != "" ||
		!strings.Contains(got.DecodeError, "slot read failed") {
		t.Fatalf("ME unreadable = %#v", got)
	}
	if len(session.calls) < 2 || session.calls[0] != "get-iccid" || session.calls[1] != "get-imsi" {
		t.Fatalf("calls = %v", session.calls)
	}
}

func TestNativeQMIListSkipsBrokenUIMWhenNVRouteIsAvailable(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	nvRaw, _ := hex.DecodeString("000405912143F500004210203040500005C82293F904")
	contextErr := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSListMessages,
		Result:    1,
		ErrorCode: qmi.QMIErrInvalidArg,
	}
	session := &fakeQMIWMSRouteSession{
		fakeQMISMSSession: &fakeQMISMSSession{
			iccid: "8986001234567890123",
			imsi:  "515031234567890",
			listErrs: map[fakeQMISMSListKey]error{
				{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: contextErr,
			},
			lists: map[fakeQMISMSListKey][]qmiSMSListEntry{
				{storage: qmiSMSStorageNV, tag: qmi.TagTypeMTNotRead}: {{Index: 9, Tag: qmi.TagTypeMTNotRead}},
			},
			reads: map[fakeQMISMSReadKey][]byte{
				{storage: qmiSMSStorageNV, index: 9}: nvRaw,
			},
		},
		routes: &qmi.WMSRouteConfig{Routes: []qmi.WMSRoute{
			{MessageType: qmi.WMSMessageTypePointToPoint, MessageClass: qmi.WMSMessageClass0, StorageType: qmi.WMSStorageTypeNV, ReceiptAction: qmi.WMSReceiptActionStoreAndNotify},
			{MessageType: qmi.WMSMessageTypePointToPoint, MessageClass: qmi.WMSMessageClass2, StorageType: qmi.WMSStorageTypeUIM, ReceiptAction: qmi.WMSReceiptActionStoreAndNotify},
		}},
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if err != nil || len(scan.Messages) != 1 || scan.Messages[0].Text != "HELLO" ||
		!reflect.DeepEqual(scan.Storages, []string{"ME"}) {
		t.Fatalf("NV fallback scan = (%#v, %v)", scan, err)
	}
	if session.closeCount != 1 || atOpener.openCount != 0 {
		t.Fatalf("session/AT opens = %d/%d", session.closeCount, atOpener.openCount)
	}
}

func TestNativeQMIListHandsBrokenNVWMSPathToATFallback(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	pdu := "000405912143F500004210203040500005C82293F904"
	contextErr := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSListMessages,
		Result:    1,
		ErrorCode: qmi.QMIErrInvalidArg,
	}
	session := &fakeQMIWMSRouteSession{
		fakeQMISMSSession: &fakeQMISMSSession{
			iccid: "8986001234567890123",
			imsi:  "515031234567890",
			listErrs: map[fakeQMISMSListKey]error{
				{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: contextErr,
				{storage: qmiSMSStorageNV, tag: qmi.TagTypeMTNotRead}:  contextErr,
			},
		},
		routes: &qmi.WMSRouteConfig{Routes: []qmi.WMSRoute{
			{MessageType: qmi.WMSMessageTypePointToPoint, MessageClass: qmi.WMSMessageClass0, StorageType: qmi.WMSStorageTypeNV, ReceiptAction: qmi.WMSReceiptActionStoreAndNotify},
		}},
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}
	atOpener.client = &transcriptClient{steps: []clientStep{
		{command: "AT+CMGF=0", response: okResponse()},
		{command: `AT+CPMS="SM"`, response: okResponse()},
		{command: "AT+CMGL=4", response: okResponse("+CMGL: 7,0,,23", pdu)},
		{command: `AT+CPMS="ME"`, response: okResponse()},
		{command: "AT+CMGL=4", response: okResponse()},
	}}

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if err != nil || scan.Transport != SMSTransportCellularAT || len(scan.Messages) != 1 ||
		scan.Messages[0].Text != "HELLO" || !reflect.DeepEqual(scan.Storages, []string{"SM", "ME"}) {
		t.Fatalf("AT fallback scan = (%#v, %v)", scan, err)
	}
	if session.closeCount != 1 || atOpener.openCount != 1 {
		t.Fatalf("session/AT opens = %d/%d", session.closeCount, atOpener.openCount)
	}
}

func TestNativeQMIListRecoversCardCallControlFailureAfterEuiccRefresh(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	cardCallControl := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSListMessages,
		Result:    1,
		ErrorCode: qmi.QMIErrCardCallControlRefFail,
	}
	failedWMS := &fakeQMISMSSession{
		iccid:    "89634261387110862674",
		imsi:     "515027106574535",
		listErrs: map[fakeQMISMSListKey]error{{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: cardCallControl},
	}
	retriedWMS := &fakeQMISMSSession{
		iccid: "89634261387110862674",
		imsi:  "515027106574535",
	}
	recoveryUIM := &fakeDeviceQMIUIMSession{iccid: "89634261387110862674"}
	var smsOpens int
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		smsOpens++
		if smsOpens == 1 {
			return failedWMS, nil
		}
		return retriedWMS, nil
	}
	manager.qmiEUICCOpener = func(context.Context, string) (qmiEUICCSession, error) {
		return recoveryUIM, nil
	}

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("ListSMSBoundSubscriber: %v", err)
	}
	if scan.Transport != SMSTransportCellularQMI || scan.Identity.ICCID != retriedWMS.iccid ||
		scan.Identity.IMSI != retriedWMS.imsi || len(scan.Messages) != 0 ||
		!reflect.DeepEqual(scan.Storages, []string{"SM", "ME"}) {
		t.Fatalf("recovered scan = %#v", scan)
	}
	if smsOpens != 2 || failedWMS.closeCount != 1 || retriedWMS.closeCount != 1 {
		t.Fatalf("WMS opens/closes = %d/%d/%d", smsOpens, failedWMS.closeCount, retriedWMS.closeCount)
	}
	if recoveryUIM.resetCount != 1 || len(recoveryUIM.powerOff) != 1 ||
		recoveryUIM.powerOff[0] != qmiEUICCSlot || len(recoveryUIM.powerOn) != 1 ||
		recoveryUIM.powerOn[0] != qmiEUICCSlot {
		t.Fatalf("UIM recovery reset/off/on = %d/%v/%v", recoveryUIM.resetCount, recoveryUIM.powerOff, recoveryUIM.powerOn)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times", atOpener.openCount)
	}
}

func TestNativeQMIListFallsBackToModemResetWhenUIMRecoveryIsInsufficient(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	nvRaw, _ := hex.DecodeString("000405912143F500004210203040500005C82293F904")
	cardCallControl := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSListMessages,
		Result:    1,
		ErrorCode: qmi.QMIErrCardCallControlRefFail,
	}
	wmsSessions := []*fakeQMISMSSession{
		{iccid: "89634261387110862674", imsi: "515027106574535", listErrs: map[fakeQMISMSListKey]error{{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: cardCallControl}},
		{iccid: "89634261387110862674", imsi: "515027106574535", listErrs: map[fakeQMISMSListKey]error{{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: cardCallControl}},
		{
			iccid: "89634261387110862674",
			imsi:  "515027106574535",
			listErrs: map[fakeQMISMSListKey]error{
				{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: cardCallControl,
			},
			lists: map[fakeQMISMSListKey][]qmiSMSListEntry{
				{storage: qmiSMSStorageNV, tag: qmi.TagTypeMTNotRead}: {{Index: 9, Tag: qmi.TagTypeMTNotRead}},
			},
			reads: map[fakeQMISMSReadKey][]byte{
				{storage: qmiSMSStorageNV, index: 9}: nvRaw,
			},
		},
	}
	var smsOpens int
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		if smsOpens >= len(wmsSessions) {
			return nil, errors.New("unexpected WMS session")
		}
		session := wmsSessions[smsOpens]
		smsOpens++
		return session, nil
	}
	recoveryUIM := &fakeDeviceQMIUIMSession{iccid: "89634261387110862674"}
	manager.qmiEUICCOpener = func(context.Context, string) (qmiEUICCSession, error) {
		return recoveryUIM, nil
	}
	radio := &fakeQMIRadioSession{mode: qmi.ModeOnline}
	var radioOpens int
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		radioOpens++
		return radio, nil
	}

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("ListSMSBoundSubscriber: %v", err)
	}
	if len(scan.Messages) != 1 || scan.Messages[0].Index != 9 ||
		scan.Messages[0].Text != "HELLO" || !reflect.DeepEqual(scan.Storages, []string{"ME"}) {
		t.Fatalf("recovered scan = %#v", scan)
	}
	if smsOpens != 3 || radioOpens != 2 || radio.networkResets != 1 ||
		len(radio.setModes) != 2 || radio.setModes[0] != qmi.ModeReset || radio.setModes[1] != qmi.ModeOnline {
		t.Fatalf("WMS/radio recovery opens=%d/%d modes=%v network_resets=%d", smsOpens, radioOpens, radio.setModes, radio.networkResets)
	}
	if recoveryUIM.resetCount != 1 || len(recoveryUIM.powerOff) != 1 || len(recoveryUIM.powerOn) != 1 {
		t.Fatalf("UIM recovery reset/off/on = %d/%v/%v", recoveryUIM.resetCount, recoveryUIM.powerOff, recoveryUIM.powerOn)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times", atOpener.openCount)
	}
}

func TestNativeQMIListFallsBackToATWhenWMSCardControlAffectsBothStorages(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	pdu := "000405912143F500004210203040500005C82293F904"
	cardCallControl := &qmi.QMIError{
		Service:   qmi.ServiceWMS,
		MessageID: qmi.WMSListMessages,
		Result:    1,
		ErrorCode: qmi.QMIErrCardCallControlRefFail,
	}
	wmsSessions := make([]*fakeQMISMSSession, 3)
	for index := range wmsSessions {
		wmsSessions[index] = &fakeQMISMSSession{
			iccid: "89634261387110862674",
			imsi:  "515027106574535",
			listErrs: map[fakeQMISMSListKey]error{
				{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: cardCallControl,
				{storage: qmiSMSStorageNV, tag: qmi.TagTypeMTNotRead}:  cardCallControl,
			},
		}
	}
	var smsOpens int
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		if smsOpens >= len(wmsSessions) {
			return nil, errors.New("unexpected WMS session")
		}
		session := wmsSessions[smsOpens]
		smsOpens++
		return session, nil
	}
	recoveryUIM := &fakeDeviceQMIUIMSession{iccid: "89634261387110862674"}
	manager.qmiEUICCOpener = func(context.Context, string) (qmiEUICCSession, error) {
		return recoveryUIM, nil
	}
	radio := &fakeQMIRadioSession{mode: qmi.ModeOnline}
	manager.qmiRadioOpener = func(context.Context, string) (qmiRadioSession, error) {
		return radio, nil
	}
	atOpener.client = &transcriptClient{steps: []clientStep{
		{command: "AT+CMGF=0", response: okResponse()},
		{command: `AT+CPMS="SM"`, response: okResponse()},
		{command: "AT+CMGL=4", response: okResponse("+CMGL: 7,0,,23", pdu)},
		{command: `AT+CPMS="ME"`, response: okResponse()},
		{command: "AT+CMGL=4", response: okResponse()},
	}}

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if err != nil || scan.Transport != SMSTransportCellularAT ||
		scan.Identity.IMSI != "515027106574535" || len(scan.Messages) != 1 ||
		scan.Messages[0].Text != "HELLO" || !reflect.DeepEqual(scan.Storages, []string{"SM", "ME"}) {
		t.Fatalf("AT fallback scan = (%#v, %v)", scan, err)
	}
	if smsOpens != 3 || radio.networkResets != 1 || atOpener.openCount != 1 {
		t.Fatalf("recovery opens WMS/radio/AT = %d/%d/%d", smsOpens, radio.networkResets, atOpener.openCount)
	}
}

func TestNativeQMIListFailsAtomicScanIfLiveControlChanges(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	raw, _ := hex.DecodeString("000405912143F500004210203040500005C82293F904")
	changed := false
	session := &fakeQMISMSSession{
		iccid: "8986001234567890123",
		imsi:  "515031234567890",
		lists: map[fakeQMISMSListKey][]qmiSMSListEntry{
			{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: {
				{Index: 1, Tag: qmi.TagTypeMTNotRead},
			},
		},
		reads: map[fakeQMISMSReadKey][]byte{
			{storage: qmiSMSStorageUIM, index: 1}: raw,
		},
	}
	session.afterList = func(uint8, qmi.MessageTagType) {
		if changed {
			return
		}
		changed = true
		manager.mu.Lock()
		manager.devices[id].candidate.QMIControl = "/dev/wwan0qmi1"
		manager.mu.Unlock()
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if !errors.Is(err, ErrSMSTransportUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if scan.Transport != SMSTransportCellularQMI || len(scan.Storages) != 0 ||
		len(scan.Messages) != 0 || scan.Identity.ICCID != session.iccid {
		t.Fatalf("scan = %#v", scan)
	}
	for _, call := range session.calls {
		if strings.HasPrefix(call, "read-") {
			t.Fatalf("raw-read ran after QMI control changed: calls=%v", session.calls)
		}
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times", atOpener.openCount)
	}
}

func TestDecodeQMIStoredSMSUnwrapsRPDataAndPreservesRawPDU(t *testing.T) {
	full, _ := hex.DecodeString("000405912143F500004210203040500005C82293F904")
	tpdu := full[1:]
	rpData := append([]byte{0x01, 0x23, 0x00, 0x00, byte(len(tpdu))}, tpdu...)
	message, err := decodeQMIStoredSMS(rpData)
	if err != nil || message.Direction != SMSDirectionReceived || message.Text != "HELLO" ||
		message.RawPDU != strings.ToUpper(hex.EncodeToString(rpData)) {
		t.Fatalf("decodeQMIStoredSMS = (%#v, %v)", message, err)
	}
}

func TestNativeQMIListSMSUnboundUsesQMIBackend(t *testing.T) {
	manager, atOpener, id := newStartedNativeSMSManager(t)
	raw, _ := hex.DecodeString("000405912143F500004210203040500005C82293F904")
	session := &fakeQMISMSSession{
		iccid: "8986001234567890123",
		imsi:  "515031234567890",
		lists: map[fakeQMISMSListKey][]qmiSMSListEntry{
			{storage: qmiSMSStorageUIM, tag: qmi.TagTypeMTNotRead}: {
				{Index: 1, Tag: qmi.TagTypeMTNotRead},
			},
		},
		reads: map[fakeQMISMSReadKey][]byte{
			{storage: qmiSMSStorageUIM, index: 1}: raw,
		},
	}
	manager.qmiSMSOpener = func(context.Context, string) (qmiSMSSession, error) {
		return session, nil
	}

	messages, err := manager.ListSMS(context.Background(), id)
	if err != nil || len(messages) != 1 || messages[0].Transport != SMSTransportCellularQMI ||
		messages[0].Text != "HELLO" {
		t.Fatalf("ListSMS = (%#v, %v)", messages, err)
	}
	if atOpener.openCount != 0 {
		t.Fatalf("AT opener used %d times", atOpener.openCount)
	}
}

func TestATSMSResultsExposeCellularATTransport(t *testing.T) {
	t.Run("send", func(t *testing.T) {
		client := &transcriptClient{
			steps: []clientStep{
				{command: "AT+CMGF=1", response: okResponse()},
				{command: `AT+CSCS="GSM"`, response: okResponse()},
				{command: "AT+CSMP=49,167,0,0", response: okResponse()},
			},
			promptSteps: []promptClientStep{{
				command:  `AT+CMGS="+12345"`,
				payload:  "HELLO",
				response: okResponse("+CMGS: 9"),
			}},
		}
		manager, id := newStartedTestManager(t, client)
		result, err := manager.SendSMS(context.Background(), id, "+12345", "HELLO")
		if err != nil || result.Transport != SMSTransportCellularAT {
			t.Fatalf("SendSMS = (%#v, %v)", result, err)
		}
		client.assertDone(t)
	})

	t.Run("scan", func(t *testing.T) {
		const pdu = "000405912143F500004210203040500005C82293F904"
		client := &transcriptClient{steps: []clientStep{
			{command: "AT+CCID", response: okResponse("+CCID: 8986001234567890123F")},
			{command: "AT+CIMI", response: okResponse("515031234567890")},
			{command: "AT+CMGF=0", response: okResponse()},
			{command: `AT+CPMS="SM"`, response: okResponse()},
			{command: "AT+CMGL=4", response: okResponse("+CMGL: 7,0,,23", pdu)},
			{command: `AT+CPMS="ME"`, response: okResponse()},
			{command: "AT+CMGL=4", response: okResponse()},
		}}
		manager, id := newStartedTestManager(t, client)
		scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
		if err != nil || scan.Transport != SMSTransportCellularAT || len(scan.Messages) != 1 ||
			scan.Messages[0].Transport != SMSTransportCellularAT {
			t.Fatalf("ListSMSBoundSubscriber = (%#v, %v)", scan, err)
		}
		client.assertDone(t)
	})
}
