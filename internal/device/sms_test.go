package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"vocat/internal/modem"
)

func TestManagerSendSMSBoundSubscriberReadsLiveIdentityBeforeCMGS(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{
			{command: "AT+CCID", err: errors.New("unsupported")},
			{command: "AT+QCCID", response: okResponse("+QCCID: 8986001234567890123F")},
			{command: "AT+CIMI", response: okResponse("515031234567890")},
			{command: "AT+CMGF=1", response: okResponse()},
			{command: `AT+CSCS="GSM"`, response: okResponse()},
			{command: "AT+CSMP=49,167,0,0", response: okResponse()},
		},
		promptSteps: []promptClientStep{{
			command:  `AT+CMGS="+12345"`,
			payload:  "HELLO",
			response: okResponse("+CMGS: 23"),
		}},
	}
	manager, id := newStartedTestManager(t, client)
	// A stale blocked snapshot must not override the freshly read live IMSI.
	manager.mu.Lock()
	manager.devices[id].snapshot = &Snapshot{IMSI: "460001234567890"}
	manager.mu.Unlock()

	result, identity, err := manager.SendSMSBoundSubscriber(
		context.Background(),
		id,
		"0012345",
		"HELLO",
	)
	if err != nil {
		t.Fatalf("SendSMSBoundSubscriber: %v", err)
	}
	if identity.ICCID != "8986001234567890123" || identity.IMSI != "515031234567890" {
		t.Fatalf("identity = %#v", identity)
	}
	if result.To != "+12345" || result.PartsAttempted != 1 || !result.AcceptedByModem {
		t.Fatalf("result = %#v", result)
	}
	client.assertDone(t)
}

func TestManagerSendSMSBoundSubscriberFailsClosedWithoutIMSI(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CCID", response: okResponse("+CCID: 8986001234567890123F")},
		{command: "AT+CIMI", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)

	result, identity, err := manager.SendSMSBoundSubscriber(
		context.Background(), id, "+12345", "HELLO",
	)
	if !errors.Is(err, ErrSMSSubscriberIdentity) {
		t.Fatalf("error = %v", err)
	}
	if identity.ICCID != "8986001234567890123" || identity.IMSI != "" {
		t.Fatalf("identity = %#v", identity)
	}
	if result.PartsAttempted != 0 || result.PartsAccepted != 0 || result.SubmissionStatus != "identity_failed" {
		t.Fatalf("result = %#v", result)
	}
	client.assertDone(t)
}

func TestManagerSendSMSBoundSubscriberUsesLiveIMSIForRegionBlock(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CCID", response: okResponse("+CCID: 8986001234567890123F")},
		{command: "AT+CIMI", response: okResponse("460001234567890")},
	}}
	manager, id := newStartedTestManager(t, client)
	manager.mu.Lock()
	manager.devices[id].snapshot = &Snapshot{IMSI: "515031234567890"}
	manager.mu.Unlock()

	result, identity, err := manager.SendSMSBoundSubscriber(
		context.Background(), id, "+12345", "HELLO",
	)
	if !errors.Is(err, ErrRegionBlocked) {
		t.Fatalf("error = %v", err)
	}
	if identity.IMSI != "460001234567890" || result.PartsAttempted != 0 || result.SubmissionStatus != "region_blocked" {
		t.Fatalf("identity/result = %#v / %#v", identity, result)
	}
	client.assertDone(t)
}

type blockingSMSIdentityClient struct {
	*transcriptClient
	identityStarted chan struct{}
	releaseIdentity chan struct{}
}

func (client *blockingSMSIdentityClient) Execute(
	ctx context.Context,
	command string,
) (modem.Response, error) {
	if command == "AT+CCID" {
		close(client.identityStarted)
		select {
		case <-client.releaseIdentity:
		case <-ctx.Done():
			return modem.Response{}, ctx.Err()
		}
	}
	return client.transcriptClient.Execute(ctx, command)
}

func TestManagerSendSMSBoundSubscriberHoldsOperationLockThroughCMGS(t *testing.T) {
	transcript := &transcriptClient{
		steps: []clientStep{
			{command: "AT+CCID", response: okResponse("+CCID: 8986001234567890123F")},
			{command: "AT+CIMI", response: okResponse("515031234567890")},
			{command: "AT+CMGF=1", response: okResponse()},
			{command: `AT+CSCS="GSM"`, response: okResponse()},
			{command: "AT+CSMP=49,167,0,0", response: okResponse()},
			{command: "AT+CSQ", response: okResponse("+CSQ: 20,99")},
		},
		promptSteps: []promptClientStep{{
			command:  `AT+CMGS="+12345"`,
			payload:  "HELLO",
			response: okResponse("+CMGS: 24"),
		}},
	}
	client := &blockingSMSIdentityClient{
		transcriptClient: transcript,
		identityStarted:  make(chan struct{}),
		releaseIdentity:  make(chan struct{}),
	}
	manager, id := newStartedTestManager(t, client)
	sendDone := make(chan error, 1)
	go func() {
		_, _, err := manager.SendSMSBoundSubscriber(context.Background(), id, "+12345", "HELLO")
		sendDone <- err
	}()
	<-client.identityStarted

	controlDone := make(chan error, 1)
	go func() {
		_, err := manager.ExecuteAT(context.Background(), id, "AT+CSQ")
		controlDone <- err
	}()
	select {
	case err := <-controlDone:
		t.Fatalf("control operation interleaved during identity read: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(client.releaseIdentity)
	if err := <-sendDone; err != nil {
		t.Fatalf("SendSMSBoundSubscriber: %v", err)
	}
	if err := <-controlDone; err != nil {
		t.Fatalf("ExecuteAT: %v", err)
	}
	transcript.assertDone(t)
}

func TestManagerSendSMSDirectGSM7ReturnsAcceptanceEvidence(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{
			{command: "AT+CMGF=1", response: okResponse()},
			{command: `AT+CSCS="GSM"`, response: okResponse()},
			{command: "AT+CSMP=49,167,0,0", response: okResponse()},
		},
		promptSteps: []promptClientStep{{
			command:  `AT+CMGS="+12345"`,
			payload:  "HELLO",
			response: okResponse("+CMGS: 23"),
		}},
	}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.SendSMS(
		context.Background(),
		id,
		"+12 345",
		"HELLO",
	)
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if result.To != "+12345" ||
		result.Encoding != SMSEncodingGSM7Text ||
		!result.AcceptedByModem ||
		!result.ReferenceKnown ||
		result.MessageReference != 23 ||
		result.DeliveryConfirmed ||
		result.DeliveryStatus != "unknown" ||
		result.SubmissionStatus != "accepted_by_modem" ||
		result.PartsTotal != 1 ||
		result.PartsAttempted != 1 ||
		result.PartsAccepted != 1 ||
		!result.AllPartsAccepted ||
		len(result.PartResults) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.ModemEvidence) != 1 || result.ModemEvidence[0] != "+CMGS: 23" {
		t.Fatalf("evidence = %#v", result.ModemEvidence)
	}
	client.assertDone(t)
}

func TestManagerSendMultipartSMSReturnsEveryPartReference(t *testing.T) {
	var concatReference = -1
	validatePart := func(sequence int, wantText string) func(string) error {
		return func(payload string) error {
			message, err := decodeSMSPDU(payload)
			if err != nil {
				return fmt.Errorf("decode part %d: %w", sequence, err)
			}
			if message.Text != wantText || message.Concat == nil ||
				message.Concat.Total != 2 ||
				message.Concat.Sequence != sequence {
				return fmt.Errorf("part %d decoded as %#v", sequence, message)
			}
			if concatReference < 0 {
				concatReference = message.Concat.Reference
			} else if message.Concat.Reference != concatReference {
				return fmt.Errorf(
					"part %d concat reference %d, want %d",
					sequence,
					message.Concat.Reference,
					concatReference,
				)
			}
			return nil
		}
	}
	client := &transcriptClient{
		steps: []clientStep{{command: "AT+CMGF=0", response: okResponse()}},
		promptSteps: []promptClientStep{
			{
				command:      "AT+CMGS=150",
				validateBody: validatePart(1, strings.Repeat("A", 153)),
				response:     okResponse("+CMGS: 31"),
			},
			{
				command:      "AT+CMGS=24",
				validateBody: validatePart(2, strings.Repeat("A", 8)),
				response:     okResponse("+CMGS: 32"),
			},
		},
	}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.SendSMS(
		context.Background(),
		id,
		"+12345",
		strings.Repeat("A", 161),
	)
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if result.PartsTotal != 2 ||
		result.PartsAttempted != 2 ||
		result.PartsAccepted != 2 ||
		!result.AcceptedByModem ||
		!result.AllPartsAccepted ||
		result.ReferenceKnown ||
		result.ConcatReference == nil ||
		*result.ConcatReference != concatReference ||
		len(result.PartResults) != 2 ||
		result.PartResults[0].MessageReference != 31 ||
		result.PartResults[1].MessageReference != 32 ||
		!result.PartResults[0].AcceptedByModem ||
		!result.PartResults[1].AcceptedByModem ||
		result.DeliveryConfirmed {
		t.Fatalf("result = %#v", result)
	}
	client.assertDone(t)
}

func TestManagerSendMultipartSMSStopsAndPreservesPartialEvidence(t *testing.T) {
	validatePart := func(sequence int, wantText string) func(string) error {
		return func(payload string) error {
			message, err := decodeSMSPDU(payload)
			if err != nil {
				return err
			}
			if message.Concat == nil ||
				message.Concat.Sequence != sequence ||
				message.Text != wantText {
				return fmt.Errorf("part %d decoded as %#v", sequence, message)
			}
			return nil
		}
	}
	secondError := &modem.CommandError{
		Command: "AT+CMGS=24",
		Final:   "+CMS ERROR: 500",
	}
	client := &transcriptClient{
		steps: []clientStep{{command: "AT+CMGF=0", response: okResponse()}},
		promptSteps: []promptClientStep{
			{
				command:      "AT+CMGS=150",
				validateBody: validatePart(1, strings.Repeat("A", 153)),
				response:     okResponse("+CMGS: 41"),
			},
			{
				command:      "AT+CMGS=24",
				validateBody: validatePart(2, strings.Repeat("A", 8)),
				response: modem.Response{
					Final: "+CMS ERROR: 500",
				},
				err: secondError,
			},
		},
	}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.SendSMS(
		context.Background(),
		id,
		"+12345",
		strings.Repeat("A", 161),
	)
	var commandErr *modem.CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error = %v", err)
	}
	if result.PartsTotal != 2 ||
		result.PartsAttempted != 2 ||
		result.PartsAccepted != 1 ||
		result.AcceptedByModem ||
		result.AllPartsAccepted ||
		result.SubmissionStatus != "partially_accepted_by_modem" ||
		len(result.PartResults) != 2 ||
		!result.PartResults[0].AcceptedByModem ||
		result.PartResults[0].MessageReference != 41 ||
		result.PartResults[1].AcceptedByModem ||
		result.PartResults[1].SubmissionStatus != "rejected_by_modem" ||
		result.DeliveryConfirmed {
		t.Fatalf("result = %#v", result)
	}
	client.assertDone(t)
}

func TestManagerSendSMSUsesUCS2PDUForChinese(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{
			{command: "AT+CMGF=0", response: okResponse()},
		},
		promptSteps: []promptClientStep{{
			command:  "AT+CMGS=14",
			payload:  "00210005912143F50008044F60597D",
			response: okResponse("+CMGS: 0"),
		}},
	}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.SendSMS(
		context.Background(),
		id,
		"+12345",
		"你好",
	)
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if result.Encoding != SMSEncodingUCS2PDU ||
		!result.ReferenceKnown ||
		result.MessageReference != 0 ||
		!result.AllPartsAccepted ||
		result.DeliveryConfirmed {
		t.Fatalf("result = %#v", result)
	}
	client.assertDone(t)
}

func TestManagerSendSMSTimeoutNeverClaimsAcceptanceOrDelivery(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{
			{command: "AT+CMGF=1", response: okResponse()},
			{command: `AT+CSCS="GSM"`, response: okResponse()},
			{command: "AT+CSMP=49,167,0,0", response: okResponse()},
		},
		promptSteps: []promptClientStep{{
			command: `AT+CMGS="12345"`,
			payload: "HELLO",
			response: modem.Response{
				Lines: []string{"+CMGS: 77"},
			},
			err: modem.ErrCommandTimeout,
		}},
	}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.SendSMS(
		context.Background(),
		id,
		"12345",
		"HELLO",
	)
	if !errors.Is(err, modem.ErrCommandTimeout) {
		t.Fatalf("error = %v", err)
	}
	if result.AcceptedByModem || result.DeliveryConfirmed ||
		!result.ReferenceKnown || result.MessageReference != 77 ||
		result.SubmissionStatus != "reference_returned_without_final" ||
		result.DeliveryStatus != "unknown" {
		t.Fatalf("result = %#v", result)
	}
	client.mu.Lock()
	closeCount := client.closeCount
	client.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("close count = %d, want 1 after uncertain timeout", closeCount)
	}
	client.assertDone(t)
}

func TestManagerListReadAndDeleteSMS(t *testing.T) {
	const gsmPDU = "000405912143F500004210203040500005C82293F904"
	const ucs2PDU = "000405912143F5000842102030405000044F60597D"
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CMGF=0", response: okResponse()},
		{command: `AT+CPMS="SM"`, response: okResponse()},
		{
			command: "AT+CMGL=4",
			response: okResponse(
				"+CMGL: 7,0,,23",
				gsmPDU,
				"+CMGL: 8,1,,22",
				ucs2PDU,
			),
		},
		{command: `AT+CPMS="ME"`, response: okResponse()},
		{command: "AT+CMGL=4", response: okResponse()},
		{command: "AT+CMGF=0", response: okResponse()},
		{
			command: "AT+CMGR=7",
			response: okResponse(
				"+CMGR: 0,,23",
				gsmPDU,
			),
		},
		{command: "AT+CMGD=7", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)

	messages, err := manager.ListSMS(context.Background(), id)
	if err != nil {
		t.Fatalf("ListSMS: %v", err)
	}
	if len(messages) != 2 ||
		messages[0].Index != 7 ||
		messages[0].Storage != "SM" ||
		messages[0].StorageStatus != SMSStatusReceivedUnread ||
		messages[0].Text != "HELLO" ||
		messages[1].Index != 8 ||
		messages[1].Storage != "SM" ||
		messages[1].StorageStatus != SMSStatusReceivedRead ||
		messages[1].Text != "你好" {
		t.Fatalf("messages = %#v", messages)
	}

	message, err := manager.ReadSMS(context.Background(), id, 7)
	if err != nil {
		t.Fatalf("ReadSMS: %v", err)
	}
	if message.Index != 7 ||
		message.StorageStatus != SMSStatusReceivedUnread ||
		message.Text != "HELLO" {
		t.Fatalf("message = %#v", message)
	}
	if err := manager.DeleteSMS(context.Background(), id, 7); err != nil {
		t.Fatalf("DeleteSMS: %v", err)
	}
	client.assertDone(t)
}

func TestManagerListSMSBoundSubscriberReadsIdentityInsideStorageScan(t *testing.T) {
	const gsmPDU = "000405912143F500004210203040500005C82293F904"
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CCID", response: okResponse("+CCID: 8986001234567890123F")},
		{command: "AT+CIMI", response: okResponse("515031234567890")},
		{command: "AT+CMGF=0", response: okResponse()},
		{command: `AT+CPMS="SM"`, response: okResponse()},
		{command: "AT+CMGL=4", response: okResponse("+CMGL: 7,0,,23", gsmPDU)},
		{command: `AT+CPMS="ME"`, response: okResponse()},
		{command: "AT+CMGL=4", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)

	scan, err := manager.ListSMSBoundSubscriber(context.Background(), id)
	if err != nil {
		t.Fatalf("ListSMSBoundSubscriber: %v", err)
	}
	if scan.Identity.ICCID != "8986001234567890123" || scan.Identity.IMSI != "515031234567890" {
		t.Fatalf("identity = %#v", scan.Identity)
	}
	if len(scan.Messages) != 1 || scan.Messages[0].Storage != "SM" || scan.Messages[0].Text != "HELLO" {
		t.Fatalf("messages = %#v", scan.Messages)
	}
	if len(scan.Storages) != 2 || scan.Storages[0] != "SM" || scan.Storages[1] != "ME" {
		t.Fatalf("storages = %#v", scan.Storages)
	}
	client.assertDone(t)
}

func TestManagerSendSMSRequiresMessageReference(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{
			{command: "AT+CMGF=1", response: okResponse()},
			{command: `AT+CSCS="GSM"`, response: okResponse()},
			{command: "AT+CSMP=49,167,0,0", response: okResponse()},
		},
		promptSteps: []promptClientStep{{
			command:  `AT+CMGS="12345"`,
			payload:  "HELLO",
			response: okResponse(),
		}},
	}
	manager, id := newStartedTestManager(t, client)

	result, err := manager.SendSMS(
		context.Background(),
		id,
		"12345",
		"HELLO",
	)
	if !errors.Is(err, ErrSMSReferenceMissing) {
		t.Fatalf("error = %v", err)
	}
	if result.AcceptedByModem ||
		result.ReferenceKnown ||
		result.DeliveryConfirmed ||
		result.SubmissionStatus != "unconfirmed_without_reference" {
		t.Fatalf("result = %#v", result)
	}
	client.assertDone(t)
}
