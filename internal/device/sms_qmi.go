package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/qmiport"
)

const (
	qmiSMSStorageUIM uint8 = 0
	qmiSMSStorageNV  uint8 = 1
	qmiSMSFormatGW   uint8 = 0x06
	// OpenStick 410 can return the standard QMI INVALID_ARG (0x0030) after an
	// eUICC refresh. The old qmi-go fork called that value
	// CardCallControlRefFail, which hid the actual stale WMS-context symptom.
	// 0x0060 is the real CARD_CALL_CONTROL_FAILED value.
	qmiWMSInvalidArgCode      uint16 = 0x0030
	qmiWMSCardCallControlCode uint16 = 0x0060
	qmiWMSPrimarySubscription uint8  = 0
	// Repeating the eight-query WMS catch-up loop while the card is in a bad
	// state monopolizes the shared QMI control port and makes eSIM/VoWiFi
	// control requests look unstable. Probe again only after a cooldown.
	qmiSMSWMSBackoff = 5 * time.Minute

	qmiSMSRecoveryNone = iota
	qmiSMSRecoveryWMS
	qmiSMSRecoveryUIM
	qmiSMSRecoveryModem
)

var errQMIWMSScanSuspended = errors.New("QMI WMS SMS scan is suspended")

type qmiSMSListEntry struct {
	Index uint32
	Tag   qmi.MessageTagType
}

type qmiSMSSession interface {
	GetICCID(context.Context) (string, error)
	GetIMSI(context.Context) (string, error)
	GetTransportNetworkRegistrationStatus(context.Context) (qmi.WMSTransportNetworkRegistration, error)
	SendRawMessage(context.Context, uint8, []byte) error
	ListMessages(context.Context, uint8, qmi.MessageTagType) ([]qmiSMSListEntry, error)
	RawReadMessage(context.Context, uint8, uint32) ([]byte, error)
	Close() error
}

type qmiSMSAutoLister interface {
	ListMessagesAuto(context.Context, uint8) ([]qmiSMSListEntry, error)
}

// qmiWMSContextReinitializer is implemented by the production session. It is
// intentionally optional so unit-test sessions and older modem backends can
// continue to exercise the list/read state machine without pretending to
// support newer WMS requests.
type qmiWMSContextReinitializer interface {
	ResetWMS(context.Context) error
	BindWMSSubscription(context.Context, uint8) error
	GetWMSSubscriptionBinding(context.Context) (uint8, error)
	GetWMSServiceReady(context.Context) (qmi.WMSServiceReadyStatus, error)
}

type qmiWMSRouteInspector interface {
	GetWMSRoutes(context.Context) (*qmi.WMSRouteConfig, error)
	SetWMSRoutes(context.Context, []qmi.WMSRoute, bool) error
}

type qmiEFSMSSchemaReader interface {
	GetEFSMSSchema(context.Context) (qmi.UIMFileAttributes, error)
	ReadEFSMSSRecord(context.Context, uint16, uint16) (qmi.UIMRecordData, error)
}

type qmiSMSSessionOpener func(context.Context, string) (qmiSMSSession, error)

type productionQMISMSSession struct {
	client *qmi.Client
	uim    *qmi.UIMService
	wms    *qmi.WMSService
	lease  *qmiport.Lease
}

// smsQMIControlForState deliberately has stricter semantics than the shared
// eSIM native-QMI selector. A wwan* candidate is a native WWAN device even
// while discovery temporarily loses its QMI endpoint; SMS must fail closed in
// that interval instead of falling through to an AT port on the same device.
func (manager *Manager) smsQMIControlForState(
	state *managedDevice,
) (string, bool, error) {
	candidate := manager.candidateFor(state)
	deviceID := strings.TrimSpace(candidate.ID)
	if !strings.HasPrefix(deviceID, "wwan") {
		return "", false, nil
	}
	controlDevice := strings.TrimSpace(candidate.QMIControl)
	base := filepath.Base(controlDevice)
	if controlDevice == "" || !strings.HasPrefix(base, deviceID+"qmi") {
		return "", true, fmt.Errorf(
			"%w: native WWAN device %s has no matching QMI control path",
			ErrSMSTransportUnavailable,
			deviceID,
		)
	}
	return controlDevice, true, nil
}

func (manager *Manager) validateQMISMSControl(
	state *managedDevice,
	expected string,
) error {
	controlDevice, native, err := manager.smsQMIControlForState(state)
	if err != nil {
		return err
	}
	if !native || controlDevice != strings.TrimSpace(expected) {
		return fmt.Errorf(
			"%w: native WWAN QMI control changed during SMS operation",
			ErrSMSTransportUnavailable,
		)
	}
	return nil
}

// openQMISMSSession keeps UIM identity reads and WMS operations on one QMI
// client. qmiport owns the kernel endpoint lifetime, while qmi-proxy prevents
// this client from racing other QMI users for responses. SMS PDUs and SIM
// identifiers must never reach the library logger.
func openQMISMSSession(ctx context.Context, controlDevice string) (qmiSMSSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	openContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lease, err := qmiport.Acquire(openContext, controlDevice)
	if err != nil {
		return nil, err
	}
	opts := qmi.DefaultClientOptions()
	opts.UseProxy = true
	opts.Logf = func(qmi.ClientLogLevel, string, ...any) {}
	client, err := qmi.NewClientWithOptions(openContext, controlDevice, opts)
	if err != nil {
		lease.Release()
		return nil, err
	}
	uim, err := qmi.NewUIMServiceWithContext(openContext, client)
	if err != nil {
		_ = client.Close()
		lease.Release()
		return nil, err
	}
	wms, err := qmi.NewWMSServiceWithContext(openContext, client)
	if err != nil {
		_ = uim.Close()
		_ = client.Close()
		lease.Release()
		return nil, err
	}
	return &productionQMISMSSession{
		client: client,
		uim:    uim,
		wms:    wms,
		lease:  lease,
	}, nil
}

func (session *productionQMISMSSession) GetICCID(ctx context.Context) (string, error) {
	return session.uim.GetICCID(ctx)
}

func (session *productionQMISMSSession) GetIMSI(ctx context.Context) (string, error) {
	return session.uim.GetIMSI(ctx)
}

func (session *productionQMISMSSession) GetTransportNetworkRegistrationStatus(
	ctx context.Context,
) (qmi.WMSTransportNetworkRegistration, error) {
	return session.wms.GetTransportNetworkRegistrationStatus(ctx)
}

func (session *productionQMISMSSession) ResetWMS(ctx context.Context) error {
	return session.wms.Reset(ctx)
}

func (session *productionQMISMSSession) BindWMSSubscription(ctx context.Context, subscription uint8) error {
	return session.wms.BindSubscription(ctx, subscription)
}

func (session *productionQMISMSSession) GetWMSSubscriptionBinding(ctx context.Context) (uint8, error) {
	return session.wms.GetSubscriptionBinding(ctx)
}

func (session *productionQMISMSSession) GetWMSServiceReady(ctx context.Context) (qmi.WMSServiceReadyStatus, error) {
	return session.wms.GetServiceReadyStatus(ctx)
}

func (session *productionQMISMSSession) GetWMSRoutes(ctx context.Context) (*qmi.WMSRouteConfig, error) {
	return session.wms.GetRoutes(ctx)
}

func (session *productionQMISMSSession) SetWMSRoutes(ctx context.Context, routes []qmi.WMSRoute, transferStatusReportToClient bool) error {
	return session.wms.SetRoutes(ctx, routes, transferStatusReportToClient)
}

func (session *productionQMISMSSession) GetEFSMSSchema(ctx context.Context) (qmi.UIMFileAttributes, error) {
	attrs, err := session.uim.GetFileAttributesWithSession(
		ctx,
		qmi.UIMSessionTypePrimaryGWProvisioning,
		0x6F3C,
		[]uint8{0x00, 0x3F, 0xFF, 0x7F},
	)
	if err != nil {
		return qmi.UIMFileAttributes{}, err
	}
	if attrs == nil {
		return qmi.UIMFileAttributes{}, errors.New("UIM returned no EF_SMS attributes")
	}
	return *attrs, nil
}

func (session *productionQMISMSSession) ReadEFSMSSRecord(ctx context.Context, recordNumber, recordLength uint16) (qmi.UIMRecordData, error) {
	record, err := session.uim.ReadRecordWithSession(
		ctx,
		qmi.UIMSessionTypePrimaryGWProvisioning,
		0x6F3C,
		[]uint8{0x00, 0x3F, 0xFF, 0x7F},
		recordNumber,
		recordLength,
	)
	if err != nil {
		return qmi.UIMRecordData{}, err
	}
	if record == nil {
		return qmi.UIMRecordData{}, errors.New("UIM returned no EF_SMS record")
	}
	return *record, nil
}

func (session *productionQMISMSSession) SendRawMessage(
	ctx context.Context,
	format uint8,
	pdu []byte,
) error {
	return session.wms.SendRawMessage(ctx, format, pdu)
}

func (session *productionQMISMSSession) ListMessages(
	ctx context.Context,
	storage uint8,
	tag qmi.MessageTagType,
) ([]qmiSMSListEntry, error) {
	entries, err := session.wms.ListMessages(ctx, storage, tag)
	if err != nil {
		return nil, err
	}
	result := make([]qmiSMSListEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, qmiSMSListEntry{Index: entry.Index, Tag: entry.Tag})
	}
	return result, nil
}

func (session *productionQMISMSSession) ListMessagesAuto(
	ctx context.Context,
	storage uint8,
) ([]qmiSMSListEntry, error) {
	entries, err := session.wms.ListMessagesAuto(ctx, storage)
	if err != nil {
		return nil, err
	}
	result := make([]qmiSMSListEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, qmiSMSListEntry{Index: entry.Index, Tag: entry.Tag})
	}
	return result, nil
}

func (session *productionQMISMSSession) RawReadMessage(
	ctx context.Context,
	storage uint8,
	index uint32,
) ([]byte, error) {
	return session.wms.RawReadMessage(ctx, storage, index)
}

func (session *productionQMISMSSession) Close() error {
	if session == nil {
		return nil
	}
	var closeErrors []error
	if session.wms != nil {
		closeErrors = append(closeErrors, session.wms.Close())
		session.wms = nil
	}
	if session.uim != nil {
		closeErrors = append(closeErrors, session.uim.Close())
		session.uim = nil
	}
	if session.client != nil {
		closeErrors = append(closeErrors, session.client.Close())
		session.client = nil
	}
	if session.lease != nil {
		session.lease.Release()
		session.lease = nil
	}
	return errors.Join(closeErrors...)
}

func (manager *Manager) openQMISMSSessionLocked(
	ctx context.Context,
	controlDevice string,
) (qmiSMSSession, error) {
	if manager.qmiSMSOpener == nil {
		return nil, errors.New("QMI UIM/WMS SMS transport is unavailable")
	}
	openContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*5)
	defer cancel()
	session, err := manager.qmiSMSOpener(openContext, controlDevice)
	if err != nil {
		return nil, fmt.Errorf("open QMI UIM/WMS SMS session: %w", err)
	}
	return session, nil
}

func (manager *Manager) readQMISMSSubscriberIdentityLocked(
	ctx context.Context,
	session qmiSMSSession,
) (SMSSubscriberIdentity, error) {
	identity := SMSSubscriberIdentity{}
	readContext, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	iccid, err := session.GetICCID(readContext)
	cancel()
	if err != nil {
		return identity, fmt.Errorf("%w: read QMI UIM ICCID: %w", ErrSMSSubscriberIdentity, err)
	}
	identity.ICCID = strings.TrimRight(strings.TrimSpace(iccid), "Ff")
	if !decimalDigits(identity.ICCID, 18, 22) {
		identity.ICCID = ""
		return identity, fmt.Errorf("%w: QMI UIM returned no valid ICCID", ErrSMSSubscriberIdentity)
	}

	readContext, cancel = manager.withTimeout(ctx, manager.commandTimeout)
	imsi, err := session.GetIMSI(readContext)
	cancel()
	if err != nil {
		return identity, fmt.Errorf("%w: read QMI UIM IMSI: %w", ErrSMSSubscriberIdentity, err)
	}
	identity.IMSI = strings.TrimSpace(imsi)
	if !decimalDigits(identity.IMSI, 10, 18) {
		identity.IMSI = ""
		return identity, fmt.Errorf("%w: QMI UIM returned no valid IMSI", ErrSMSSubscriberIdentity)
	}
	return identity, nil
}

func (manager *Manager) sendSMSQMILocked(
	ctx context.Context,
	id string,
	state *managedDevice,
	controlDevice string,
	parts []preparedSMS,
	result SMSSendResult,
) (SMSSendResult, SMSSubscriberIdentity, error) {
	result.Transport = SMSTransportCellularQMI
	if result.Encoding == SMSEncodingGSM7Text {
		result.Encoding = SMSEncodingGSM7PDU
	}
	session, err := manager.openQMISMSSessionLocked(ctx, controlDevice)
	if err != nil {
		result.SubmissionStatus = "setup_failed"
		manager.setResult(id, state, nil, err)
		return result, SMSSubscriberIdentity{}, err
	}
	defer session.Close()

	identity, err := manager.readQMISMSSubscriberIdentityLocked(ctx, session)
	if err != nil {
		result.SubmissionStatus = "identity_failed"
		manager.setResult(id, state, nil, err)
		return result, identity, err
	}
	if reason := RegionBlockReason(identity.IMSI); reason != "" {
		err = fmt.Errorf("%w: %s", ErrRegionBlocked, reason)
		result.SubmissionStatus = "region_blocked"
		manager.setResult(id, state, nil, err)
		return result, identity, err
	}
	if err := manager.validateQMISMSControl(state, controlDevice); err != nil {
		result.SubmissionStatus = "transport_unavailable"
		result.ModemFinal = "QMI control changed"
		result.ModemEvidence = append(result.ModemEvidence, err.Error())
		manager.setResult(id, state, nil, err)
		return result, identity, err
	}

	transportContext, cancel := manager.withTimeout(ctx, manager.commandTimeout)
	transport, transportErr := session.GetTransportNetworkRegistrationStatus(transportContext)
	cancel()
	switch {
	case transportErr == nil:
		result.ModemEvidence = append(
			result.ModemEvidence,
			"QMI WMS transport: "+transport.String(),
		)
		switch transport {
		case qmi.WMSTransportNetworkRegistrationFullService,
			qmi.WMSTransportNetworkRegistrationInProcess:
			// Full service is ready, while in-process is explicitly allowed to
			// let raw-send provide the authoritative modem result.
		case qmi.WMSTransportNetworkRegistrationNoService,
			qmi.WMSTransportNetworkRegistrationFailure,
			qmi.WMSTransportNetworkRegistrationLimitedService:
			err = fmt.Errorf(
				"%w: QMI WMS transport is %s",
				ErrSMSTransportUnavailable,
				transport.String(),
			)
			result.SubmissionStatus = "transport_unavailable"
			result.ModemFinal = "QMI WMS " + transport.String()
			manager.setResult(id, state, nil, err)
			return result, identity, err
		default:
			err = fmt.Errorf(
				"%w: QMI WMS returned unknown transport state 0x%02x",
				ErrSMSTransportUnavailable,
				uint8(transport),
			)
			result.SubmissionStatus = "transport_unavailable"
			result.ModemFinal = "QMI WMS transport unknown"
			result.ModemEvidence = append(result.ModemEvidence, err.Error())
			manager.setResult(id, state, nil, err)
			return result, identity, err
		}
	case isUnsupportedQMIWMSOperation(transportErr):
		// Some otherwise functional WMS implementations do not expose 0x004A.
		// Raw-send remains authoritative; do not alter routes or choose IMS here.
		result.ModemEvidence = append(
			result.ModemEvidence,
			"QMI WMS transport query unsupported; raw-send allowed",
		)
	default:
		err = fmt.Errorf("query QMI WMS transport registration: %w", transportErr)
		result.SubmissionStatus = "setup_failed"
		result.ModemFinal = "QMI WMS transport query failed"
		result.ModemEvidence = append(result.ModemEvidence, err.Error())
		manager.setResult(id, state, nil, err)
		return result, identity, err
	}

	for _, part := range parts {
		if controlErr := manager.validateQMISMSControl(state, controlDevice); controlErr != nil {
			result.ModemFinal = "QMI control changed"
			result.ModemEvidence = append(
				result.ModemEvidence,
				fmt.Sprintf("before part %d/%d: %v", part.part, part.total, controlErr),
			)
			if result.PartsAccepted > 0 {
				result.SubmissionStatus = "partially_accepted_by_modem"
			} else {
				result.SubmissionStatus = "transport_unavailable"
			}
			manager.setResult(id, state, nil, controlErr)
			return result, identity, controlErr
		}
		pdu, pduErr := qmiRawSubmitPDU(part)
		if pduErr != nil {
			result.SubmissionStatus = "setup_failed"
			partErr := fmt.Errorf("encode SMS part %d/%d for QMI WMS: %w", part.part, part.total, pduErr)
			manager.setResult(id, state, nil, partErr)
			return result, identity, partErr
		}
		sendContext, cancelSend := manager.withTimeout(ctx, manager.smsTimeout)
		submitErr := session.SendRawMessage(sendContext, qmiSMSFormatGW, pdu)
		cancelSend()

		partResult := SMSPartResult{
			Part:             part.part,
			Total:            part.total,
			ReferenceKnown:   false,
			AcceptedByModem:  submitErr == nil,
			SubmissionStatus: "accepted_by_modem",
			ModemFinal:       "QMI WMS raw-send OK",
			ModemEvidence:    []string{"QMI WMS raw-send accepted"},
			SubmittedAt:      time.Now().UTC(),
		}
		if submitErr != nil {
			partResult.SubmissionStatus = qmiSMSFailureStatus(submitErr)
			partResult.ModemFinal = "QMI WMS raw-send failed"
			partResult.ModemEvidence = []string{fmt.Sprintf("QMI WMS raw-send error: %v", submitErr)}
		}
		result.PartResults = append(result.PartResults, partResult)
		result.PartsAttempted++
		if partResult.AcceptedByModem {
			result.PartsAccepted++
		}
		result.ModemFinal = partResult.ModemFinal
		for _, evidence := range partResult.ModemEvidence {
			if len(parts) == 1 {
				result.ModemEvidence = append(result.ModemEvidence, evidence)
			} else {
				result.ModemEvidence = append(
					result.ModemEvidence,
					fmt.Sprintf("part %d/%d: %s", part.part, part.total, evidence),
				)
			}
		}
		if submitErr == nil {
			continue
		}
		switch {
		case len(parts) == 1:
			result.SubmissionStatus = partResult.SubmissionStatus
		case result.PartsAccepted > 0:
			result.SubmissionStatus = "partially_accepted_by_modem"
		default:
			result.SubmissionStatus = partResult.SubmissionStatus
		}
		partErr := fmt.Errorf("submit SMS part %d/%d through QMI WMS: %w", part.part, part.total, submitErr)
		manager.setResult(id, state, nil, partErr)
		return result, identity, partErr
	}

	result.AcceptedByModem = true
	result.AllPartsAccepted = true
	result.SubmissionStatus = "accepted_by_modem"
	manager.setResult(id, state, nil, nil)
	return result, identity, nil
}

func qmiRawSubmitPDU(part preparedSMS) ([]byte, error) {
	if part.encoding != SMSEncodingGSM7Text {
		pdu, err := hex.DecodeString(strings.TrimSpace(string(part.payload)))
		if err != nil || len(pdu) < 2 {
			if err == nil {
				err = errors.New("encoded SMS PDU is too short")
			}
			return nil, err
		}
		return pdu, nil
	}
	septets, ok := encodeGSM7(string(part.payload))
	if !ok {
		return nil, errors.New("SMS text could not be encoded as GSM-7")
	}
	pduHex, _, err := encodeSubmitPDU(part.to, septets, nil)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(pduHex)
}

func qmiSMSFailureStatus(err error) string {
	if qmi.GetQMIError(err) != nil {
		return "rejected_by_modem"
	}
	return "unknown"
}

func isUnsupportedQMIWMSOperation(err error) bool {
	if err == nil {
		return false
	}
	var unsupported *qmi.NotSupportedError
	if errors.As(err, &unsupported) {
		return true
	}
	qmiErr := qmi.GetQMIError(err)
	if qmiErr == nil {
		return false
	}
	return qmiErr.ErrorCode == qmi.QMIErrInvalidQmiCmd ||
		qmiErr.ErrorCode == qmi.QMIErrNotSupported ||
		qmiErr.ErrorCode == qmi.QMIErrOpDeviceUnsupported
}

func isQMIWMSCardCallControlFailure(err error) bool {
	qmiErr := qmi.GetQMIError(err)
	return qmiErr != nil && qmiErr.Service == qmi.ServiceWMS &&
		qmiErr.ErrorCode == qmiWMSCardCallControlCode
}

// isQMIWMSContextFailure covers the two values that have appeared in the
// field: standard INVALID_ARG (0x0030) from the OpenStick and the actual
// CARD_CALL_CONTROL_FAILED (0x0060) used by other firmware. Both require a
// fresh WMS context before touching UIM storage; only the latter is labelled
// a card call-control failure in operator-facing logs.
func isQMIWMSContextFailure(err error) bool {
	qmiErr := qmi.GetQMIError(err)
	return qmiErr != nil && qmiErr.Service == qmi.ServiceWMS &&
		(qmiErr.ErrorCode == qmiWMSInvalidArgCode || qmiErr.ErrorCode == qmiWMSCardCallControlCode)
}

// qmiErrorLogAttrs preserves the raw QMI result/error identifiers in the
// persisted event stream. Serializing an error interface alone can become an
// empty JSON object in loghub, which would hide whether the modem returned
// INVALID_ARG (0x0030) or CARD_CALL_CONTROL_FAILED (0x0060).
func qmiErrorLogAttrs(err error) []any {
	attrs := []any{"error", err}
	if qmiErr := qmi.GetQMIError(err); qmiErr != nil {
		attrs = append(attrs,
			"qmi_service", qmiErr.Service,
			"qmi_message_id", qmiErr.MessageID,
			"qmi_result", qmiErr.Result,
			"qmi_error_code", qmiErr.ErrorCode,
		)
	}
	return attrs
}

func (manager *Manager) qmiSMSScanSuspended(controlDevice string) bool {
	controlDevice = strings.TrimSpace(controlDevice)
	if manager == nil || controlDevice == "" {
		return false
	}
	now := time.Now()
	manager.qmiSMSBackoffMu.Lock()
	defer manager.qmiSMSBackoffMu.Unlock()
	until := manager.qmiSMSBackoffUntil[controlDevice]
	if until.IsZero() {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(manager.qmiSMSBackoffUntil, controlDevice)
	return false
}

func (manager *Manager) suspendQMIWMSScan(controlDevice string, cause error) {
	controlDevice = strings.TrimSpace(controlDevice)
	if manager == nil || controlDevice == "" {
		return
	}
	now := time.Now()
	until := now.Add(qmiSMSWMSBackoff)
	manager.qmiSMSBackoffMu.Lock()
	previous := manager.qmiSMSBackoffUntil[controlDevice]
	manager.qmiSMSBackoffUntil[controlDevice] = until
	manager.qmiSMSBackoffMu.Unlock()
	if now.Before(previous) {
		return
	}
	manager.logEvent(slog.LevelWarn, "QMI WMS SMS scan suspended",
		"category", "sms", "event", "qmi_wms_scan_suspended",
		"control_path", controlDevice, "cooldown_seconds", int(qmiSMSWMSBackoff/time.Second), "error", cause)
}

func (manager *Manager) clearQMIWMSScanBackoff(controlDevice string) {
	controlDevice = strings.TrimSpace(controlDevice)
	if manager == nil || controlDevice == "" {
		return
	}
	manager.qmiSMSBackoffMu.Lock()
	delete(manager.qmiSMSBackoffUntil, controlDevice)
	manager.qmiSMSBackoffMu.Unlock()
}

func (manager *Manager) markQMIWMSContextPending(deviceID string) {
	deviceID = strings.TrimSpace(deviceID)
	if manager == nil || deviceID == "" {
		return
	}
	manager.qmiSMSContextMu.Lock()
	if manager.qmiSMSContextPending == nil {
		manager.qmiSMSContextPending = make(map[string]bool)
	}
	manager.qmiSMSContextPending[deviceID] = true
	manager.qmiSMSContextMu.Unlock()
}

func (manager *Manager) qmiWMSContextPending(deviceID string) bool {
	deviceID = strings.TrimSpace(deviceID)
	if manager == nil || deviceID == "" {
		return false
	}
	manager.qmiSMSContextMu.Lock()
	pending := manager.qmiSMSContextPending[deviceID]
	manager.qmiSMSContextMu.Unlock()
	return pending
}

func (manager *Manager) clearQMIWMSContextPending(deviceID string) {
	deviceID = strings.TrimSpace(deviceID)
	if manager == nil || deviceID == "" {
		return
	}
	manager.qmiSMSContextMu.Lock()
	delete(manager.qmiSMSContextPending, deviceID)
	manager.qmiSMSContextMu.Unlock()
}

type qmiSMSStorage struct {
	value uint8
	name  string
	rank  int
}

var qmiSMSStorages = []qmiSMSStorage{
	{value: qmiSMSStorageUIM, name: "SM", rank: 0},
	{value: qmiSMSStorageNV, name: "ME", rank: 1},
}

var qmiSMSListTags = []qmi.MessageTagType{
	qmi.TagTypeMTNotRead,
	qmi.TagTypeMTRead,
	qmi.TagTypeMONotSent,
	qmi.TagTypeMOSent,
}

type qmiSMSStoredRecord struct {
	storage qmiSMSStorage
	index   uint32
	tag     qmi.MessageTagType
}

// reinitializeQMIWMSContextLocked rebuilds only the WMS service state. It is
// deliberately separate from UIM/modem resets: the OpenStick reports standard
// INVALID_ARG (0x0030) when the WMS client still points at the pre-REFRESH
// subscription/storage context, and a modem reset is needlessly disruptive in
// that case.
func (manager *Manager) reinitializeQMIWMSContextLocked(
	ctx context.Context,
	id string,
	controlDevice string,
	session qmiSMSSession,
) error {
	reinitializer, ok := session.(qmiWMSContextReinitializer)
	if !ok {
		manager.logEvent(slog.LevelDebug, "QMI WMS context rebuild unavailable",
			"category", "sms", "event", "qmi_wms_context_rebuild_unavailable",
			"device_id", id, "control_path", controlDevice)
		return nil
	}
	call := func(operation string, fn func(context.Context) error) error {
		callContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*2)
		defer cancel()
		if err := fn(callContext); err != nil {
			return fmt.Errorf("%s: %w", operation, err)
		}
		return nil
	}

	if err := call("WMS reset", reinitializer.ResetWMS); err != nil {
		return err
	}
	manager.logEvent(slog.LevelInfo, "QMI WMS service reset",
		"category", "sms", "event", "qmi_wms_reset", "device_id", id,
		"control_path", controlDevice)

	if err := call("WMS bind primary subscription", func(callContext context.Context) error {
		return reinitializer.BindWMSSubscription(callContext, qmiWMSPrimarySubscription)
	}); err != nil {
		if !isUnsupportedQMIWMSOperation(err) {
			return err
		}
		manager.logEvent(slog.LevelDebug, "QMI WMS subscription binding unsupported",
			"category", "sms", "event", "qmi_wms_bind_unsupported",
			"device_id", id, "control_path", controlDevice, "error", err)
	} else if err := call("WMS get subscription binding", func(callContext context.Context) error {
		binding, bindingErr := reinitializer.GetWMSSubscriptionBinding(callContext)
		if bindingErr != nil {
			return bindingErr
		}
		if binding != qmiWMSPrimarySubscription {
			return fmt.Errorf("WMS bound subscription is %d, want primary (%d)", binding, qmiWMSPrimarySubscription)
		}
		return nil
	}); err != nil {
		if !isUnsupportedQMIWMSOperation(err) {
			return err
		}
		manager.logEvent(slog.LevelDebug, "QMI WMS binding query unsupported",
			"category", "sms", "event", "qmi_wms_binding_query_unsupported",
			"device_id", id, "control_path", controlDevice, "error", err)
	}

	ready, err := manager.waitForQMIWMSServiceReady(ctx, reinitializer)
	if err != nil {
		if !isUnsupportedQMIWMSOperation(err) {
			return err
		}
		manager.logEvent(slog.LevelDebug, "QMI WMS service-ready query unsupported",
			"category", "sms", "event", "qmi_wms_service_ready_unsupported",
			"device_id", id, "control_path", controlDevice, "error", err)
	} else {
		manager.logEvent(slog.LevelInfo, "QMI WMS service ready",
			"category", "sms", "event", "qmi_wms_service_ready", "device_id", id,
			"control_path", controlDevice, "state", ready.String())
	}

	routeInspector, hasRouteInspector := session.(qmiWMSRouteInspector)
	var observedRoutes *qmi.WMSRouteConfig
	if hasRouteInspector {
		routeContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*2)
		routes, routeErr := routeInspector.GetWMSRoutes(routeContext)
		cancel()
		if routeErr != nil {
			if !isUnsupportedQMIWMSOperation(routeErr) {
				manager.logEvent(slog.LevelWarn, "QMI WMS route query failed",
					"category", "sms", "event", "qmi_wms_routes_query_failed",
					"device_id", id, "control_path", controlDevice, "error", routeErr)
			}
		} else if routes != nil {
			observedRoutes = routes
			manager.logEvent(slog.LevelInfo, "QMI WMS routes observed",
				"category", "sms", "event", "qmi_wms_routes_observed",
				"device_id", id, "control_path", controlDevice,
				"route_count", len(routes.Routes),
				"transfer_status_report", routes.TransferStatusReportToClient)
			for index, route := range routes.Routes {
				manager.logEvent(slog.LevelDebug, "QMI WMS route",
					"category", "sms", "event", "qmi_wms_route",
					"device_id", id, "control_path", controlDevice, "index", index,
					"message_type", route.MessageType, "message_class", route.MessageClass,
					"storage", route.StorageType, "receipt_action", route.ReceiptAction)
			}
		}
	}
	if hasRouteInspector && observedRoutes != nil {
		fallbackRoutes, changed := qmiWMSNVFallbackRoutes(observedRoutes.Routes)
		if changed {
			routeContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*2)
			routeErr := routeInspector.SetWMSRoutes(
				routeContext,
				fallbackRoutes,
				observedRoutes.TransferStatusReportToClient,
			)
			cancel()
			if routeErr != nil {
				manager.logEvent(slog.LevelWarn, "QMI WMS NV route fallback failed",
					"category", "sms", "event", "qmi_wms_nv_route_fallback_failed",
					"device_id", id, "control_path", controlDevice,
					"changed_routes", countQMIWMSNVFallbackRoutes(observedRoutes.Routes),
					"error", routeErr)
			} else {
				manager.logEvent(slog.LevelInfo, "QMI WMS routes moved to NV storage",
					"category", "sms", "event", "qmi_wms_nv_route_fallback_applied",
					"device_id", id, "control_path", controlDevice,
					"changed_routes", countQMIWMSNVFallbackRoutes(observedRoutes.Routes))
			}
		}
	}

	if schemaReader, ok := session.(qmiEFSMSSchemaReader); ok {
		schemaContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*2)
		attrs, schemaErr := schemaReader.GetEFSMSSchema(schemaContext)
		cancel()
		if schemaErr != nil {
			manager.logEvent(slog.LevelWarn, "QMI UIM EF_SMS probe failed",
				"category", "sms", "event", "qmi_uim_ef_sms_probe_failed",
				"device_id", id, "control_path", controlDevice, "file_id", "0x6F3C",
				"error", schemaErr)
		} else {
			manager.logEvent(slog.LevelInfo, "QMI UIM EF_SMS available",
				"category", "sms", "event", "qmi_uim_ef_sms_available",
				"device_id", id, "control_path", controlDevice, "file_id", "0x6F3C",
				"file_size", attrs.FileSize, "record_size", attrs.RecordSize,
				"record_count", attrs.RecordCount, "card_sw1", attrs.CardResult.SW1,
				"card_sw2", attrs.CardResult.SW2)
			recordLength := attrs.RecordSize
			if recordLength == 0 {
				recordLength = 176
			}
			recordContext, recordCancel := manager.withTimeout(ctx, manager.commandTimeout*2)
			record, recordErr := schemaReader.ReadEFSMSSRecord(recordContext, 1, recordLength)
			recordCancel()
			if recordErr != nil {
				manager.logEvent(slog.LevelWarn, "QMI UIM EF_SMS record probe failed",
					"category", "sms", "event", "qmi_uim_ef_sms_record_probe_failed",
					"device_id", id, "control_path", controlDevice, "record_number", 1,
					"record_length", recordLength, "error", recordErr)
			} else {
				manager.logEvent(slog.LevelInfo, "QMI UIM EF_SMS record probe succeeded",
					"category", "sms", "event", "qmi_uim_ef_sms_record_probe_succeeded",
					"device_id", id, "control_path", controlDevice, "record_number", 1,
					"record_length", len(record.Data), "card_sw1", record.CardResult.SW1,
					"card_sw2", record.CardResult.SW2)
			}
		}
	}
	return nil
}

// waitForQMIWMSServiceReady gives the modem time to finish the UIM refresh
// that WMS RESET exposes as a transient NOT_READY state. A NAS registration
// can already be visible while this service-specific state is still zero.
func (manager *Manager) waitForQMIWMSServiceReady(
	ctx context.Context,
	reinitializer qmiWMSContextReinitializer,
) (qmi.WMSServiceReadyStatus, error) {
	waitTimeout := 5 * time.Second
	if manager != nil && manager.commandTimeout > 0 && 2*manager.commandTimeout > waitTimeout {
		waitTimeout = 2 * manager.commandTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Use a fresh bounded child even when the caller already has a long
	// recovery deadline; withTimeout intentionally preserves an existing
	// deadline and would otherwise let a permanently NOT_READY modem block the
	// WMS stage for the full modem-recovery timeout.
	waitContext, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	for {
		ready, err := reinitializer.GetWMSServiceReady(waitContext)
		if err != nil {
			return qmi.WMSServiceReadyNotReady, err
		}
		if ready == qmi.WMSServiceReady3GPP || ready == qmi.WMSServiceReady3GPPAnd3GPP2 {
			return ready, nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-waitContext.Done():
			if err := waitContext.Err(); err != nil {
				return qmi.WMSServiceReadyNotReady, fmt.Errorf("WMS service ready state remained %s: %w", ready.String(), err)
			}
			return qmi.WMSServiceReadyNotReady, fmt.Errorf("WMS service ready state remained %s", ready.String())
		case <-timer.C:
		}
	}
}

// qmiWMSNVFallbackRoutes keeps ordinary point-to-point SMS in modem NV
// storage when a profile leaves those routes in transfer-only/unknown storage
// mode. The application polls WMS storage, so a transfer-only route would
// acknowledge the SMS without leaving a record for the next scan. A healthy
// class-2 UIM route is retained because EF_SMS is the standard storage for
// SIM-data SMS and is independently verified by the probe above.
func qmiWMSNVFallbackRoutes(routes []qmi.WMSRoute) ([]qmi.WMSRoute, bool) {
	result := append([]qmi.WMSRoute(nil), routes...)
	changed := false
	for index, route := range result {
		if route.MessageType != qmi.WMSMessageTypePointToPoint {
			continue
		}
		if route.StorageType == qmi.WMSStorageTypeUIM &&
			route.ReceiptAction == qmi.WMSReceiptActionStoreAndNotify {
			continue
		}
		if route.StorageType != qmi.WMSStorageTypeNone &&
			route.ReceiptAction != qmi.WMSReceiptActionTransferOnly &&
			route.ReceiptAction != qmi.WMSReceiptActionTransferAndAck {
			continue
		}
		result[index].StorageType = qmi.WMSStorageTypeNV
		result[index].ReceiptAction = qmi.WMSReceiptActionStoreAndNotify
		changed = true
	}
	return result, changed
}

func countQMIWMSNVFallbackRoutes(routes []qmi.WMSRoute) int {
	_, changed := qmiWMSNVFallbackRoutes(routes)
	if !changed {
		return 0
	}
	count := 0
	for _, route := range routes {
		if route.MessageType != qmi.WMSMessageTypePointToPoint {
			continue
		}
		if route.StorageType != qmi.WMSStorageTypeNone ||
			(route.ReceiptAction != qmi.WMSReceiptActionTransferOnly &&
				route.ReceiptAction != qmi.WMSReceiptActionTransferAndAck) {
			continue
		}
		count++
	}
	return count
}

// qmiWMSHasNVStoredPointToPointRoute reports whether the modem has a usable
// NV route for ordinary point-to-point SMS.  On the affected OpenStick build,
// EF_SMS is readable through UIM but WMS List Messages(UIM) still returns
// INVALID_ARG after a profile refresh.  Once ordinary messages are routed to
// NV, repeatedly recovering the broken UIM adapter only prevents the scan from
// reaching the storage that can actually contain the message.
func (manager *Manager) qmiWMSHasNVStoredPointToPointRoute(
	ctx context.Context,
	session qmiSMSSession,
) bool {
	inspector, ok := session.(qmiWMSRouteInspector)
	if !ok {
		return false
	}
	routeContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*2)
	defer cancel()
	config, err := inspector.GetWMSRoutes(routeContext)
	if err != nil || config == nil {
		return false
	}
	for _, route := range config.Routes {
		if route.MessageType == qmi.WMSMessageTypePointToPoint &&
			route.StorageType == qmi.WMSStorageTypeNV &&
			route.ReceiptAction == qmi.WMSReceiptActionStoreAndNotify {
			return true
		}
	}
	return false
}

func (manager *Manager) listSMSQMILocked(
	ctx context.Context,
	state *managedDevice,
	controlDevice string,
) (SMSSubscriberScan, error) {
	return manager.listSMSQMILockedAttempt(ctx, state, controlDevice, qmiSMSRecoveryWMS)
}

// listSMSQMILockedAttempt performs one WMS scan. A standard INVALID_ARG
// (0x0030) after an eUICC REFRESH first rebuilds only the WMS service context;
// UIM and modem resets are retained as progressively narrower fallbacks. The
// staged recovery value prevents a permanently wedged card from causing an
// unbounded reset loop.
func (manager *Manager) listSMSQMILockedAttempt(
	ctx context.Context,
	state *managedDevice,
	controlDevice string,
	recoveryStage int,
) (SMSSubscriberScan, error) {
	scan := SMSSubscriberScan{Transport: SMSTransportCellularQMI}
	if manager.qmiSMSScanSuspended(controlDevice) {
		return scan, errQMIWMSScanSuspended
	}
	session, err := manager.openQMISMSSessionLocked(ctx, controlDevice)
	if err != nil {
		return scan, err
	}
	sessionClosed := false
	closeSession := func() {
		if !sessionClosed {
			_ = session.Close()
			sessionClosed = true
		}
	}
	defer closeSession()

	scan.Identity, err = manager.readQMISMSSubscriberIdentityLocked(ctx, session)
	if err != nil {
		return scan, err
	}
	if err := manager.validateQMISMSControl(state, controlDevice); err != nil {
		return scan, err
	}
	if manager.qmiWMSContextPending(state.candidate.ID) {
		if err := manager.reinitializeQMIWMSContextLocked(
			ctx, state.candidate.ID, controlDevice, session,
		); err != nil {
			attrs := []any{
				"category", "sms", "event", "qmi_wms_profile_refresh_reinit_failed",
				"device_id", state.candidate.ID, "control_path", controlDevice,
			}
			attrs = append(attrs, qmiErrorLogAttrs(err)...)
			manager.logEvent(slog.LevelWarn, "QMI WMS profile-refresh reinitialization failed",
				attrs...)
		} else {
			manager.clearQMIWMSContextPending(state.candidate.ID)
			manager.logEvent(slog.LevelInfo, "QMI WMS profile-refresh reinitialization completed",
				"category", "sms", "event", "qmi_wms_profile_refresh_reinit_completed",
				"device_id", state.candidate.ID, "control_path", controlDevice)
		}
	}

	records := make(map[string]qmiSMSStoredRecord)
	var lastListErr error
	checkedNVRoute := false
	nvRouteAvailable := false
	for _, storage := range qmiSMSStorages {
		complete := true
		for _, requestedTag := range qmiSMSListTags {
			listContext, cancel := manager.withTimeout(ctx, manager.commandTimeout)
			entries, listErr := session.ListMessages(listContext, storage.value, requestedTag)
			cancel()
			if listErr != nil && isQMIWMSContextFailure(listErr) &&
				(requestedTag == qmi.TagTypeMTNotRead || requestedTag == qmi.TagTypeMTRead) {
				if autoLister, ok := session.(qmiSMSAutoLister); ok {
					autoContext, cancelAuto := manager.withTimeout(ctx, manager.commandTimeout)
					entries, listErr = autoLister.ListMessagesAuto(autoContext, storage.value)
					cancelAuto()
				}
			}
			if listErr != nil {
				if isQMIWMSContextFailure(listErr) {
					if !checkedNVRoute {
						checkedNVRoute = true
						nvRouteAvailable = manager.qmiWMSHasNVStoredPointToPointRoute(ctx, session)
					}
					if nvRouteAvailable && storage.value == qmiSMSStorageUIM {
						complete = false
						lastListErr = fmt.Errorf(
							"list QMI WMS %s messages with tag %d: %w",
							storage.name, requestedTag, listErr,
						)
						manager.logEvent(slog.LevelInfo, "QMI WMS UIM list skipped after NV route fallback",
							"category", "sms", "event", "qmi_wms_uim_list_skipped",
							"control_path", controlDevice, "storage", storage.name,
							"message_class", "point-to-point", "reason", "nv_route_available",
							"error", listErr)
						continue
					}
					if nvRouteAvailable && storage.value == qmiSMSStorageNV {
						lastListErr = fmt.Errorf(
							"list QMI WMS %s messages with tag %d: %w",
							storage.name, requestedTag, listErr,
						)
						manager.logEvent(slog.LevelInfo, "QMI WMS list handed to AT fallback",
							"category", "sms", "event", "qmi_wms_list_at_fallback",
							"control_path", controlDevice, "storage", storage.name,
							"reason", "nv_route_available", "error", listErr)
						return scan, lastListErr
					}
					if recoveryStage != qmiSMSRecoveryNone {
						// The WMS-only stage is an optional enhancement. Test and
						// legacy sessions may not implement the new reset/bind
						// operations; in that case go straight to the existing UIM
						// recovery instead of opening a second, unusable WMS client.
						effectiveRecoveryStage := recoveryStage
						if recoveryStage == qmiSMSRecoveryWMS {
							if _, available := session.(qmiWMSContextReinitializer); !available {
								effectiveRecoveryStage = qmiSMSRecoveryUIM
							}
						}
						// WMS and UIM share the QMI control path. Release the
						// WMS client before resetting UIM so the recovery can
						// acquire a clean client/lease.
						closeSession()
						recoveryName := "WMS"
						if effectiveRecoveryStage == qmiSMSRecoveryUIM {
							recoveryName = "UIM"
						} else if effectiveRecoveryStage == qmiSMSRecoveryModem {
							recoveryName = "modem"
						}
						startAttrs := []any{
							"category", "sms", "event", "qmi_wms_recovery_started",
							"control_path", controlDevice, "stage", recoveryName,
						}
						startAttrs = append(startAttrs, qmiErrorLogAttrs(listErr)...)
						manager.logEvent(slog.LevelWarn, "QMI WMS SMS recovery started", startAttrs...)
						recoveryContext, cancelRecovery := context.WithTimeout(
							context.WithoutCancel(ctx), manager.longTimeout,
						)
						var recoveryErr error
						if effectiveRecoveryStage == qmiSMSRecoveryWMS {
							recoverySession, openErr := manager.openQMISMSSessionLocked(
								recoveryContext, controlDevice,
							)
							if openErr != nil {
								recoveryErr = openErr
							} else {
								recoveryErr = manager.reinitializeQMIWMSContextLocked(
									recoveryContext, state.candidate.ID, controlDevice, recoverySession,
								)
								_ = recoverySession.Close()
							}
						} else if effectiveRecoveryStage == qmiSMSRecoveryUIM {
							recoveryErr = manager.recoverQMIEuiccChannelLocked(
								recoveryContext, state.candidate.ID, state,
							)
						} else {
							recoveryErr = manager.resetNativeQMIModemForSMSLocked(
								recoveryContext, state.candidate.ID, state,
							)
						}
						cancelRecovery()
						if recoveryErr == nil {
							if effectiveRecoveryStage == qmiSMSRecoveryWMS {
								manager.clearQMIWMSContextPending(state.candidate.ID)
							}
							manager.clearQMIWMSScanBackoff(controlDevice)
							manager.logEvent(slog.LevelInfo, "QMI WMS SMS recovery completed",
								"category", "sms", "event", "qmi_wms_recovery_completed",
								"control_path", controlDevice, "stage", recoveryName)
							nextStage := qmiSMSRecoveryNone
							if effectiveRecoveryStage == qmiSMSRecoveryWMS {
								nextStage = qmiSMSRecoveryUIM
							} else if effectiveRecoveryStage == qmiSMSRecoveryUIM {
								nextStage = qmiSMSRecoveryModem
							}
							return manager.listSMSQMILockedAttempt(
								ctx, state, controlDevice, nextStage,
							)
						}
						failureAttrs := []any{
							"category", "sms", "event", "qmi_wms_recovery_failed",
							"control_path", controlDevice, "stage", recoveryName,
						}
						failureAttrs = append(failureAttrs, qmiErrorLogAttrs(recoveryErr)...)
						manager.logEvent(slog.LevelWarn, "QMI WMS SMS recovery failed", failureAttrs...)
					}
					// A WMS context/card error is often tied to the SIM/UIM storage on
					// affected OpenStick firmware. Do not discard ME/NV records
					// (where the modem may still have delivered the roaming SMS)
					// just because the UIM storage cannot be listed.
					if storage.value == qmiSMSStorageUIM {
						complete = false
						lastListErr = fmt.Errorf(
							"list QMI WMS %s messages with tag %d: %w",
							storage.name, requestedTag, listErr,
						)
						continue
					}
					manager.suspendQMIWMSScan(controlDevice, listErr)
					return scan, listErr
				}
				complete = false
				lastListErr = fmt.Errorf("list QMI WMS %s messages with tag %d: %w", storage.name, requestedTag, listErr)
				continue
			}
			for _, entry := range entries {
				tag := entry.Tag
				if !isKnownQMISMSMessageTag(tag) {
					tag = requestedTag
				}
				key := fmt.Sprintf("%d:%d", storage.value, entry.Index)
				records[key] = qmiSMSStoredRecord{
					storage: storage,
					index:   entry.Index,
					tag:     tag,
				}
			}
		}
		if complete {
			scan.Storages = append(scan.Storages, storage.name)
		}
	}

	ordered := make([]qmiSMSStoredRecord, 0, len(records))
	for _, record := range records {
		ordered = append(ordered, record)
	}
	if controlErr := manager.validateQMISMSControl(state, controlDevice); controlErr != nil {
		scan.Storages = nil
		return scan, controlErr
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].storage.rank != ordered[j].storage.rank {
			return ordered[i].storage.rank < ordered[j].storage.rank
		}
		return ordered[i].index < ordered[j].index
	})
	unreadableStorages := make(map[string]struct{})
	for _, record := range ordered {
		if controlErr := manager.validateQMISMSControl(state, controlDevice); controlErr != nil {
			// The storage baseline is no longer atomic. Preserve already decoded
			// records as evidence, but do not claim either storage was fully scanned.
			scan.Storages = nil
			return scan, controlErr
		}
		readContext, cancel := manager.withTimeout(ctx, manager.commandTimeout)
		raw, readErr := session.RawReadMessage(readContext, record.storage.value, record.index)
		cancel()
		message := SMSMessage{
			Index:         int(record.index),
			Storage:       record.storage.name,
			Transport:     SMSTransportCellularQMI,
			StorageStatus: qmiSMSStorageStatus(record.tag),
			Direction:     qmiSMSDirection(record.tag),
			Encoding:      SMSEncodingUnknown,
		}
		if readErr != nil {
			unreadableStorages[record.storage.name] = struct{}{}
			message.DecodeError = fmt.Sprintf("QMI WMS raw-read failed: %v", readErr)
			scan.Messages = append(scan.Messages, message)
			continue
		}
		decoded, decodeErr := decodeQMIStoredSMS(raw)
		decoded.Index = int(record.index)
		decoded.Storage = record.storage.name
		decoded.Transport = SMSTransportCellularQMI
		decoded.StorageStatus = qmiSMSStorageStatus(record.tag)
		decoded.ModemLength = len(raw)
		if decodeErr != nil || decoded.Direction == SMSDirectionUnknown {
			decoded.Direction = qmiSMSDirection(record.tag)
		}
		if decodeErr != nil {
			decoded.DecodeError = decodeErr.Error()
		}
		scan.Messages = append(scan.Messages, decoded)
	}
	if len(unreadableStorages) > 0 {
		completeStorages := scan.Storages[:0]
		for _, storage := range scan.Storages {
			if _, unreadable := unreadableStorages[storage]; !unreadable {
				completeStorages = append(completeStorages, storage)
			}
		}
		scan.Storages = completeStorages
	}

	if len(scan.Storages) == 0 && lastListErr != nil {
		return scan, lastListErr
	}
	return scan, nil
}

func decodeQMIStoredSMS(raw []byte) (SMSMessage, error) {
	rawHex := strings.ToUpper(hex.EncodeToString(raw))
	if len(raw) == 0 {
		return SMSMessage{
			Direction:     SMSDirectionUnknown,
			Encoding:      SMSEncodingUnknown,
			StorageStatus: SMSStatusUnknown,
			RawPDU:        rawHex,
		}, errors.New("QMI WMS returned an empty SMS PDU")
	}

	var (
		fallbackMessage SMSMessage
		decodeErrors    []string
	)
	if looksLikeSMSCPrefixedPDU(raw) {
		fullMessage, fullErr := decodeSMSPDU(rawHex)
		fullMessage.RawPDU = rawHex
		if fullErr == nil {
			return fullMessage, nil
		}
		fallbackMessage = fullMessage
		decodeErrors = append(decodeErrors, "SMSC form: "+fullErr.Error())
	}
	if rpTPDU, ok := extractQMIIncomingRPDataTPDU(raw); ok {
		rpMessage, rpErr := decodeSMSPDU("00" + hex.EncodeToString(rpTPDU))
		rpMessage.RawPDU = rawHex
		if rpErr == nil {
			return rpMessage, nil
		}
		if fallbackMessage.RawPDU == "" {
			fallbackMessage = rpMessage
		}
		decodeErrors = append(decodeErrors, "RP-DATA form: "+rpErr.Error())
	}
	tpduMessage, tpduErr := decodeSMSPDU("00" + rawHex)
	tpduMessage.RawPDU = rawHex
	if tpduErr == nil {
		return tpduMessage, nil
	}
	if fallbackMessage.RawPDU == "" {
		fallbackMessage = tpduMessage
	}
	decodeErrors = append(decodeErrors, "TPDU form: "+tpduErr.Error())
	return fallbackMessage, fmt.Errorf("decode QMI WMS PDU: %s", strings.Join(decodeErrors, "; "))
}

func looksLikeSMSCPrefixedPDU(raw []byte) bool {
	if len(raw) < 2 {
		return false
	}
	length := int(raw[0])
	if length == 0 {
		return true
	}
	return length >= 2 && length+1 < len(raw) && raw[1]&0x80 != 0
}

func extractQMIIncomingRPDataTPDU(raw []byte) ([]byte, bool) {
	if len(raw) < 5 || raw[0] != 0x01 {
		return nil, false
	}
	index := 2 // RP-MTI + RP-MR
	for range 2 {
		if index >= len(raw) {
			return nil, false
		}
		length := int(raw[index])
		index++
		if index+length > len(raw) {
			return nil, false
		}
		index += length
	}
	if index >= len(raw) {
		return nil, false
	}
	length := int(raw[index])
	index++
	if length <= 0 || index+length > len(raw) {
		return nil, false
	}
	return raw[index : index+length], true
}

func qmiSMSStorageStatus(tag qmi.MessageTagType) SMSStorageStatus {
	switch tag {
	case qmi.TagTypeMTNotRead:
		return SMSStatusReceivedUnread
	case qmi.TagTypeMTRead:
		return SMSStatusReceivedRead
	case qmi.TagTypeMONotSent:
		return SMSStatusStoredUnsent
	case qmi.TagTypeMOSent:
		return SMSStatusStoredSent
	default:
		return SMSStatusUnknown
	}
}

func qmiSMSDirection(tag qmi.MessageTagType) SMSDirection {
	switch tag {
	case qmi.TagTypeMTNotRead, qmi.TagTypeMTRead:
		return SMSDirectionReceived
	case qmi.TagTypeMONotSent, qmi.TagTypeMOSent:
		return SMSDirectionSubmitted
	default:
		return SMSDirectionUnknown
	}
}

func isKnownQMISMSMessageTag(tag qmi.MessageTagType) bool {
	switch tag {
	case qmi.TagTypeMTNotRead, qmi.TagTypeMTRead, qmi.TagTypeMONotSent, qmi.TagTypeMOSent:
		return true
	default:
		return false
	}
}
