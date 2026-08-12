package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

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

func TestBackgroundNativeSMSScanDoesNotPoisonControlHealth(t *testing.T) {
	manager, _, id := newStartedNativeSMSManager(t)
	manager.mu.Lock()
	state := manager.devices[id]
	state.candidate.QMIControl = ""
	state.lastError = "previous control-plane error"
	manager.mu.Unlock()

	_, err := manager.ListSMSBoundSubscriberQuiet(context.Background(), id)
	if !errors.Is(err, ErrSMSTransportUnavailable) {
		t.Fatalf("quiet scan error = %v, want QMI transport error", err)
	}
	entry, err := manager.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.LastError != "previous control-plane error" {
		t.Fatalf("quiet scan replaced control-plane error with %q", entry.LastError)
	}
}

func TestNativeQMISendTransportPreflight(t *testing.T) {
	blocked := []qmi.WMSTransportNetworkRegistration{
		qmi.WMSTransportNetworkRegistrationNoService,
		qmi.WMSTransportNetworkRegistrationFailure,
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
			name:      "limited-service-roaming-sms",
			transport: qmi.WMSTransportNetworkRegistrationLimitedService,
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
