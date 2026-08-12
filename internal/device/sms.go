package device

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"vocat/internal/modem"
)

func (manager *Manager) SendSMS(
	ctx context.Context,
	id string,
	recipient string,
	text string,
) (SMSSendResult, error) {
	parts, err := prepareSMSParts(recipient, text)
	if err != nil {
		return SMSSendResult{}, err
	}
	result := newSMSSendResult(parts)
	state, err := manager.lookup(id)
	if err != nil {
		return result, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return result, err
	}
	controlDevice, nativeQMI, err := manager.smsQMIControlForState(state)
	if nativeQMI {
		result.Transport = SMSTransportCellularQMI
		if err != nil {
			result.SubmissionStatus = "transport_unavailable"
			manager.setResult(id, state, nil, err)
			return result, err
		}
		result, _, err = manager.sendSMSQMILocked(
			ctx,
			id,
			state,
			controlDevice,
			parts,
			result,
		)
		return result, err
	}
	if err != nil {
		return result, err
	}
	result.Transport = SMSTransportCellularAT
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return result, err
	}
	if err := manager.regionBlockError(state); err != nil {
		result.SubmissionStatus = "region_blocked"
		manager.setResult(id, state, nil, err)
		return result, err
	}
	return manager.sendSMSLocked(ctx, id, state, client, parts, result)
}

// SendSMSBoundSubscriber atomically reads the active SIM identity and submits
// an SMS while holding the device operation lock. This prevents a profile
// switch or another control operation from changing the SIM between identity
// attribution and any AT+CMGS command.
func (manager *Manager) SendSMSBoundSubscriber(
	ctx context.Context,
	id string,
	recipient string,
	text string,
) (SMSSendResult, SMSSubscriberIdentity, error) {
	parts, err := prepareSMSParts(recipient, text)
	if err != nil {
		return SMSSendResult{}, SMSSubscriberIdentity{}, err
	}
	result := newSMSSendResult(parts)
	state, err := manager.lookup(id)
	if err != nil {
		return result, SMSSubscriberIdentity{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return result, SMSSubscriberIdentity{}, err
	}
	controlDevice, nativeQMI, err := manager.smsQMIControlForState(state)
	if nativeQMI {
		result.Transport = SMSTransportCellularQMI
		if err != nil {
			result.SubmissionStatus = "transport_unavailable"
			manager.setResult(id, state, nil, err)
			return result, SMSSubscriberIdentity{}, err
		}
		return manager.sendSMSQMILocked(
			ctx,
			id,
			state,
			controlDevice,
			parts,
			result,
		)
	}
	if err != nil {
		return result, SMSSubscriberIdentity{}, err
	}
	result.Transport = SMSTransportCellularAT
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return result, SMSSubscriberIdentity{}, err
	}
	identity, err := manager.readSMSSubscriberIdentityLocked(ctx, client)
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
	result, err = manager.sendSMSLocked(ctx, id, state, client, parts, result)
	return result, identity, err
}

func newSMSSendResult(parts []preparedSMS) SMSSendResult {
	result := SMSSendResult{
		To:                parts[0].to,
		Encoding:          parts[0].encoding,
		DeliveryConfirmed: false,
		DeliveryStatus:    "unknown",
		SubmissionStatus:  "unknown",
		SubmittedAt:       time.Now().UTC(),
		PartsTotal:        len(parts),
		PartResults:       make([]SMSPartResult, 0, len(parts)),
	}
	if parts[0].concatReference != nil {
		reference := *parts[0].concatReference
		result.ConcatReference = &reference
	}
	return result
}

func (manager *Manager) readSMSSubscriberIdentityLocked(
	ctx context.Context,
	client modem.Client,
) (SMSSubscriberIdentity, error) {
	identity := SMSSubscriberIdentity{}
	var lastICCIDErr error
	for _, command := range []string{"AT+CCID", "AT+QCCID", "AT+ICCID"} {
		response, err := manager.command(ctx, client, command)
		if err != nil {
			lastICCIDErr = err
			continue
		}
		identity.ICCID = parseICCIDIdentifier(
			response,
			[]string{"+CCID:", "+QCCID:", "+ICCID:", "ICCID:"},
			18,
			22,
		)
		if identity.ICCID != "" {
			break
		}
		lastICCIDErr = errors.New("modem response contained no valid ICCID")
	}
	if identity.ICCID == "" {
		if lastICCIDErr == nil {
			lastICCIDErr = errors.New("modem returned no ICCID")
		}
		return identity, fmt.Errorf("%w: read ICCID: %w", ErrSMSSubscriberIdentity, lastICCIDErr)
	}

	response, err := manager.command(ctx, client, "AT+CIMI")
	if err != nil {
		return identity, fmt.Errorf("%w: read IMSI: %w", ErrSMSSubscriberIdentity, err)
	}
	identity.IMSI = parseIdentifier(response, []string{"+CIMI:"}, 10, 18)
	if identity.IMSI == "" {
		return identity, fmt.Errorf("%w: modem response contained no valid IMSI", ErrSMSSubscriberIdentity)
	}
	return identity, nil
}

// sendSMSLocked performs setup and every CMGS part. The caller must hold
// state.opMu and must complete any subscriber identity checks beforehand.
func (manager *Manager) sendSMSLocked(
	ctx context.Context,
	id string,
	state *managedDevice,
	client modem.Client,
	parts []preparedSMS,
	result SMSSendResult,
) (SMSSendResult, error) {
	if result.Transport == "" {
		result.Transport = SMSTransportCellularAT
	}
	for _, command := range parts[0].setup {
		if _, err := manager.command(ctx, client, command); err != nil {
			result.SubmissionStatus = "setup_failed"
			manager.setResult(id, state, nil, err)
			return result, err
		}
	}

	for _, part := range parts {
		response, submitErr := manager.prompt(
			ctx,
			client,
			part.prompt,
			part.payload,
		)
		reference, found := parseCMGSReference(response)
		partResult := SMSPartResult{
			Part:             part.part,
			Total:            part.total,
			MessageReference: reference,
			ReferenceKnown:   found,
			AcceptedByModem:  response.OK() && found,
			SubmissionStatus: "unknown",
			ModemFinal:       response.Final,
			ModemEvidence:    append([]string(nil), response.Lines...),
			SubmittedAt:      time.Now().UTC(),
		}
		switch {
		case submitErr != nil && found:
			partResult.SubmissionStatus = "reference_returned_without_final"
		case submitErr != nil:
			var commandErr *modem.CommandError
			if errors.As(submitErr, &commandErr) {
				partResult.SubmissionStatus = "rejected_by_modem"
			}
		case !found:
			partResult.SubmissionStatus = "unconfirmed_without_reference"
		default:
			partResult.SubmissionStatus = "accepted_by_modem"
		}
		result.PartResults = append(result.PartResults, partResult)
		result.PartsAttempted++
		if partResult.AcceptedByModem {
			result.PartsAccepted++
		}
		result.ModemFinal = response.Final
		if len(parts) == 1 {
			result.MessageReference = reference
			result.ReferenceKnown = found
			result.AcceptedByModem = partResult.AcceptedByModem
			result.ModemEvidence = append([]string(nil), response.Lines...)
		} else {
			for _, line := range response.Lines {
				result.ModemEvidence = append(
					result.ModemEvidence,
					fmt.Sprintf("part %d/%d: %s", part.part, part.total, line),
				)
			}
		}

		if submitErr == nil && !found {
			submitErr = ErrSMSReferenceMissing
		}
		if submitErr == nil {
			continue
		}
		var commandErr *modem.CommandError
		switch {
		case len(parts) == 1:
			result.SubmissionStatus = partResult.SubmissionStatus
		case result.PartsAccepted > 0:
			result.SubmissionStatus = "partially_accepted_by_modem"
		case errors.As(submitErr, &commandErr):
			result.SubmissionStatus = "rejected_by_modem"
		default:
			result.SubmissionStatus = "unknown"
		}
		if errors.Is(submitErr, modem.ErrCommandTimeout) ||
			errors.Is(submitErr, context.Canceled) ||
			errors.Is(submitErr, context.DeadlineExceeded) {
			// A timeout after Ctrl-Z has an inherently uncertain outcome. Close
			// this session so a late +CMGS/OK cannot corrupt the next command;
			// callers must decide whether it is safe to retry.
			_ = client.Close()
			state.client = nil
		}
		partErr := fmt.Errorf(
			"submit SMS part %d/%d: %w",
			part.part,
			part.total,
			submitErr,
		)
		manager.setResult(id, state, nil, partErr)
		return result, partErr
	}

	result.AcceptedByModem = true
	result.AllPartsAccepted = true
	result.SubmissionStatus = "accepted_by_modem"
	manager.setResult(id, state, nil, nil)
	return result, nil
}

// smsListStorages are the message storage areas ListSMS enumerates. AT+CMGL
// only reads the single storage currently selected by CPMS mem1, so a message
// that landed in SIM storage (SM) is invisible while the module memory (ME) is
// selected, and vice versa. Reading both guarantees nothing is missed regardless
// of where the network or SIM placed it. MT is the union of SM and ME, so it is
// not listed separately.
var smsListStorages = []string{"SM", "ME"}

func (manager *Manager) ListSMS(
	ctx context.Context,
	id string,
) ([]SMSMessage, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return nil, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return nil, err
	}
	controlDevice, nativeQMI, err := manager.smsQMIControlForState(state)
	if nativeQMI {
		if err != nil {
			manager.setResult(id, state, nil, err)
			return nil, err
		}
		scan, scanErr := manager.listSMSQMILocked(ctx, state, controlDevice)
		manager.setResult(id, state, nil, scanErr)
		return scan.Messages, scanErr
	}
	if err != nil {
		return nil, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return nil, err
	}
	messages, _, err := manager.listSMSLocked(ctx, client)
	manager.setResult(id, state, nil, err)
	return messages, err
}

// ListSMSBoundSubscriber atomically reads the active SIM identity and scans
// modem SMS storage while holding the same device operation lock. This keeps
// messages found during a profile switch from being attributed using a stale
// snapshot belonging to a different subscriber.
func (manager *Manager) ListSMSBoundSubscriber(
	ctx context.Context,
	id string,
) (SMSSubscriberScan, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return SMSSubscriberScan{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return SMSSubscriberScan{}, err
	}
	controlDevice, nativeQMI, err := manager.smsQMIControlForState(state)
	if nativeQMI {
		if err != nil {
			manager.setResult(id, state, nil, err)
			return SMSSubscriberScan{Transport: SMSTransportCellularQMI}, err
		}
		scan, scanErr := manager.listSMSQMILocked(ctx, state, controlDevice)
		manager.setResult(id, state, nil, scanErr)
		return scan, scanErr
	}
	if err != nil {
		return SMSSubscriberScan{}, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return SMSSubscriberScan{Transport: SMSTransportCellularAT}, err
	}
	identity, err := manager.readSMSSubscriberIdentityLocked(ctx, client)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return SMSSubscriberScan{
			Identity:  identity,
			Transport: SMSTransportCellularAT,
		}, err
	}
	messages, storages, err := manager.listSMSLocked(ctx, client)
	manager.setResult(id, state, nil, err)
	return SMSSubscriberScan{
		Messages:  messages,
		Identity:  identity,
		Storages:  storages,
		Transport: SMSTransportCellularAT,
	}, err
}

// listSMSLocked scans every supported receive storage. The caller owns the
// device operation lock and the AT client for the whole scan.
func (manager *Manager) listSMSLocked(
	ctx context.Context,
	client modem.Client,
) ([]SMSMessage, []string, error) {
	if _, err := manager.command(ctx, client, "AT+CMGF=0"); err != nil {
		return nil, nil, err
	}
	var messages []SMSMessage
	var listedStorages []string
	var lastErr error
	listed := false
	for _, storage := range smsListStorages {
		// Select this storage for reading (mem1 only, so send/receive routing is
		// untouched). An unsupported storage reports an error; skip it rather than
		// fail the whole listing.
		if _, err := manager.command(
			ctx,
			client,
			fmt.Sprintf("AT+CPMS=%q", storage),
		); err != nil {
			lastErr = err
			continue
		}
		response, err := manager.command(ctx, client, "AT+CMGL=4")
		if err != nil {
			lastErr = err
			continue
		}
		listed = true
		listedStorages = append(listedStorages, storage)
		for _, message := range parseCMGL(response) {
			message.Storage = storage
			message.Transport = SMSTransportCellularAT
			messages = append(messages, message)
		}
	}
	if !listed && lastErr != nil {
		return nil, nil, lastErr
	}
	return messages, listedStorages, nil
}

func (manager *Manager) ReadSMS(
	ctx context.Context,
	id string,
	index int,
) (SMSMessage, error) {
	if index <= 0 {
		return SMSMessage{}, ErrSMSInvalidMessageIndex
	}
	state, err := manager.lookup(id)
	if err != nil {
		return SMSMessage{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return SMSMessage{}, err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return SMSMessage{}, err
	}
	if _, err := manager.command(ctx, client, "AT+CMGF=0"); err != nil {
		manager.setResult(id, state, nil, err)
		return SMSMessage{}, err
	}
	response, err := manager.command(
		ctx,
		client,
		fmt.Sprintf("AT+CMGR=%d", index),
	)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return SMSMessage{}, err
	}
	message, err := parseCMGR(index, response)
	message.Transport = SMSTransportCellularAT
	manager.setResult(id, state, nil, err)
	return message, err
}

func (manager *Manager) DeleteSMS(
	ctx context.Context,
	id string,
	index int,
) error {
	if index <= 0 {
		return ErrSMSInvalidMessageIndex
	}
	state, err := manager.lookup(id)
	if err != nil {
		return err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return err
	}
	client, err := manager.clientLocked(ctx, state, manager.candidateFor(state))
	if err != nil {
		manager.setResult(id, state, nil, err)
		return err
	}
	_, err = manager.command(ctx, client, fmt.Sprintf("AT+CMGD=%d", index))
	manager.setResult(id, state, nil, err)
	return err
}

func (manager *Manager) prompt(
	ctx context.Context,
	client modem.Client,
	command string,
	payload []byte,
) (modem.Response, error) {
	promptClient, ok := client.(modem.PromptClient)
	if !ok {
		return modem.Response{}, ErrSMSPromptUnsupported
	}
	commandCtx, cancel := manager.withTimeout(ctx, manager.smsTimeout)
	defer cancel()
	response, err := promptClient.ExecutePrompt(commandCtx, command, payload)
	if err != nil {
		return response, fmt.Errorf("%s: %w", command, err)
	}
	return response, nil
}

func parseCMGSReference(response modem.Response) (int, bool) {
	value := valueAfterPrefix(response, "+CMGS:")
	values := csvValues(value)
	if len(values) == 0 {
		return 0, false
	}
	reference, err := strconv.Atoi(strings.TrimSpace(values[0]))
	if err != nil || reference < 0 || reference > 255 {
		return 0, false
	}
	return reference, true
}

type smsRecordHeader struct {
	index       int
	status      SMSStorageStatus
	modemLength int
	err         error
}

func parseCMGL(response modem.Response) []SMSMessage {
	var result []SMSMessage
	for index := 0; index < len(response.Lines); {
		line := strings.TrimSpace(response.Lines[index])
		if !strings.HasPrefix(strings.ToUpper(line), "+CMGL:") {
			index++
			continue
		}
		header := parseCMGLHeader(line)
		index++
		rawPDU := ""
		if index < len(response.Lines) &&
			!strings.HasPrefix(
				strings.ToUpper(strings.TrimSpace(response.Lines[index])),
				"+CMGL:",
			) {
			rawPDU = strings.TrimSpace(response.Lines[index])
			index++
		}
		message := SMSMessage{
			Index:         header.index,
			StorageStatus: header.status,
			ModemLength:   header.modemLength,
			Direction:     SMSDirectionUnknown,
			Encoding:      SMSEncodingUnknown,
			RawPDU:        strings.ToUpper(rawPDU),
		}
		switch {
		case header.err != nil:
			message.DecodeError = header.err.Error()
		case rawPDU == "":
			message.DecodeError = "CMGL record has no PDU"
		default:
			decoded, decodeErr := decodeSMSPDU(rawPDU)
			decoded.Index = header.index
			decoded.StorageStatus = header.status
			decoded.ModemLength = header.modemLength
			message = decoded
			if decodeErr != nil {
				message.DecodeError = decodeErr.Error()
			}
		}
		result = append(result, message)
	}
	return result
}

func parseCMGLHeader(line string) smsRecordHeader {
	value := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
	values := csvValues(value)
	header := smsRecordHeader{status: SMSStatusUnknown}
	if len(values) < 2 {
		header.err = errors.New("invalid CMGL header")
		return header
	}
	var ok bool
	header.index, ok = parseDecimal(values[0])
	if !ok || header.index <= 0 {
		header.err = errors.New("invalid CMGL message index")
	}
	header.status = parseSMSStorageStatus(values[1])
	if len(values) >= 3 {
		if length, found := parseDecimal(values[len(values)-1]); found {
			header.modemLength = length
		}
	}
	return header
}

func parseCMGR(index int, response modem.Response) (SMSMessage, error) {
	for lineIndex, line := range response.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "+CMGR:") {
			continue
		}
		values := csvValues(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
		if len(values) < 1 {
			return SMSMessage{}, errors.New("invalid CMGR header")
		}
		status := parseSMSStorageStatus(values[0])
		modemLength := 0
		if length, found := parseDecimal(values[len(values)-1]); found {
			modemLength = length
		}
		if lineIndex+1 >= len(response.Lines) {
			return SMSMessage{}, errors.New("CMGR response has no PDU")
		}
		message, decodeErr := decodeSMSPDU(response.Lines[lineIndex+1])
		message.Index = index
		message.StorageStatus = status
		message.ModemLength = modemLength
		if decodeErr != nil {
			message.DecodeError = decodeErr.Error()
		}
		// Return the raw record even when decoding is incomplete.
		return message, nil
	}
	return SMSMessage{}, errors.New("modem did not return a CMGR record")
}

func parseSMSStorageStatus(value string) SMSStorageStatus {
	value = strings.ToUpper(strings.Trim(strings.TrimSpace(value), `"`))
	switch value {
	case "0", "REC UNREAD":
		return SMSStatusReceivedUnread
	case "1", "REC READ":
		return SMSStatusReceivedRead
	case "2", "STO UNSENT":
		return SMSStatusStoredUnsent
	case "3", "STO SENT":
		return SMSStatusStoredSent
	default:
		return SMSStatusUnknown
	}
}
