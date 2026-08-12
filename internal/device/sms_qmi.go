package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
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
)

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
			qmi.WMSTransportNetworkRegistrationInProcess,
			qmi.WMSTransportNetworkRegistrationLimitedService:
			// Roaming can expose SMS while packet data (or full CS service) is
			// unavailable. Let WMS raw-send provide the authoritative result for
			// both in-process and limited service instead of rejecting a valid
			// SMS-only roaming path in the client.
		case qmi.WMSTransportNetworkRegistrationNoService,
			qmi.WMSTransportNetworkRegistrationFailure:
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

func (manager *Manager) listSMSQMILocked(
	ctx context.Context,
	state *managedDevice,
	controlDevice string,
) (SMSSubscriberScan, error) {
	scan := SMSSubscriberScan{Transport: SMSTransportCellularQMI}
	session, err := manager.openQMISMSSessionLocked(ctx, controlDevice)
	if err != nil {
		return scan, err
	}
	defer session.Close()

	scan.Identity, err = manager.readQMISMSSubscriberIdentityLocked(ctx, session)
	if err != nil {
		return scan, err
	}
	if err := manager.validateQMISMSControl(state, controlDevice); err != nil {
		return scan, err
	}

	records := make(map[string]qmiSMSStoredRecord)
	var lastListErr error
	for _, storage := range qmiSMSStorages {
		complete := true
		for _, requestedTag := range qmiSMSListTags {
			listContext, cancel := manager.withTimeout(ctx, manager.commandTimeout)
			entries, listErr := session.ListMessages(listContext, storage.value, requestedTag)
			cancel()
			if listErr != nil {
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
