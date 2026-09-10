package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"vocat/internal/modem"
)

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
		{command: `AT+CPMS="SM"`, response: okResponse("+CPMS: 2,40,0,23,2,40")},
		{
			command: "AT+CMGL=4",
			response: okResponse(
				"+CMGL: 7,0,,23",
				gsmPDU,
				"+CMGL: 8,1,,22",
				ucs2PDU,
			),
		},
		{command: `AT+CPMS="ME"`, response: okResponse("+CPMS: 0,23,2,40,2,40")},
		{command: "AT+CMGL=4", response: okResponse()},
		{command: `AT+CPMS="SM"`, response: okResponse("+CPMS: 2,40,0,23,2,40")},
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

	listing, err := manager.ListSMS(context.Background(), id)
	if err != nil {
		t.Fatalf("ListSMS: %v", err)
	}
	messages := listing.Messages
	if listing.Storage.SM != (SMSStorageArea{Used: 2, Total: 40}) ||
		listing.Storage.ME != (SMSStorageArea{Used: 0, Total: 23}) {
		t.Fatalf("storage = %#v", listing.Storage)
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

func TestManagerDeleteSMSFromStorageSelectsStorageAtomically(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+CPMS="SM"`, response: okResponse()},
		{command: "AT+CMGD=7", response: okResponse()},
	}}
	manager, id := newStartedTestManager(t, client)

	if err := manager.DeleteSMSFromStorage(context.Background(), id, " sm ", 7); err != nil {
		t.Fatalf("DeleteSMSFromStorage: %v", err)
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

func TestParseSelectedCPMSUsage(t *testing.T) {
	area, ok := parseSelectedCPMSUsage(okResponse("+CPMS: 23,23,23,23,23,23"))
	if !ok || area != (SMSStorageArea{Used: 23, Total: 23}) {
		t.Fatalf("select form = %#v, %v", area, ok)
	}
	area, ok = parseSelectedCPMSUsage(okResponse(`+CPMS: "SM",0,40,"ME",23,23,"MT",23,23`))
	if !ok || area != (SMSStorageArea{Used: 0, Total: 40}) {
		t.Fatalf("named form = %#v, %v", area, ok)
	}
	if _, ok = parseSelectedCPMSUsage(okResponse()); ok {
		t.Fatal("empty CPMS response should not parse")
	}
}
