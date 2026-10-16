package device

import (
	"context"
	"errors"
	"testing"
)

func TestScanOperatorsParsesNetworks(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+COPS=?", response: okResponse(
			`+COPS: (2,"China Mobile","CMCC","46000",7),(1,"China Unicom","CU","46001",0),(3,"China Telecom","CT","46011",7)`,
		)},
	}}
	manager, id := newStartedTestManager(t, client)
	result, err := manager.ScanOperators(context.Background(), id)
	if err != nil {
		t.Fatalf("ScanOperators: %v", err)
	}
	if result.Status != "complete" {
		t.Fatalf("scan status = %q, want complete", result.Status)
	}
	if len(result.Operators) != 3 {
		t.Fatalf("operators = %d, want 3 (%+v)", len(result.Operators), result.Operators)
	}
	first := result.Operators[0]
	if first.Status != "current" || first.Numeric != "46000" || first.Name != "China Mobile" || first.Act != "LTE" {
		t.Fatalf("first operator = %+v", first)
	}
	if result.Operators[2].Status != "forbidden" || result.Operators[2].Act != "LTE" {
		t.Fatalf("third operator = %+v", result.Operators[2])
	}
	client.assertDone(t)
}

func TestParseOperatorScanHandlesEmptyAndMalformed(t *testing.T) {
	if got := parseOperatorScan(okResponse()); len(got) != 0 {
		t.Fatalf("empty response operators = %v", got)
	}
	if got := parseOperatorScan(okResponse(`+COPS: `)); len(got) != 0 {
		t.Fatalf("empty list operators = %v", got)
	}
	// A tuple with too few fields is skipped, valid siblings still parse.
	got := parseOperatorScan(okResponse(`+COPS: (1,"Only"),(1,"Good","G","31026",7)`))
	if len(got) != 1 || got[0].Numeric != "31026" {
		t.Fatalf("mixed malformed operators = %+v", got)
	}
}

func TestUSSDSessionLifecycle(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{
			{command: `AT+CUSD=1,"*100#",15`, response: okResponse()},
			{command: `AT+CUSD=1,"1",15`, response: okResponse()},
			{command: "AT+CUSD=2", response: okResponse()},
		},
		urcs: []string{
			`+CUSD: 1,"Main menu",15`,
			`+CUSD: 1,"Sub menu",15`,
		},
	}
	manager, id := newStartedTestManager(t, client)

	start, err := manager.USSD(context.Background(), id, "*100#")
	if err != nil {
		t.Fatalf("start USSD: %v", err)
	}
	if start.Status != "awaiting_input" || !start.Continueable || start.SessionID == "" {
		t.Fatalf("start result = %+v, want an awaiting_input session", start)
	}
	if start.Text != "Main menu" {
		t.Fatalf("start text = %q", start.Text)
	}

	cont, err := manager.ContinueUSSD(context.Background(), start.SessionID, "1")
	if err != nil {
		t.Fatalf("continue USSD: %v", err)
	}
	if cont.Status != "awaiting_input" || cont.SessionID != start.SessionID || cont.Text != "Sub menu" {
		t.Fatalf("continue result = %+v", cont)
	}

	if err := manager.CancelUSSD(context.Background(), start.SessionID); err != nil {
		t.Fatalf("cancel USSD: %v", err)
	}
	// After cancel the session is gone.
	if _, err := manager.ContinueUSSD(context.Background(), start.SessionID, "1"); !errors.Is(err, ErrUSSDSessionNotFound) {
		t.Fatalf("continue after cancel err = %v, want ErrUSSDSessionNotFound", err)
	}
	client.assertDone(t)
}

func TestUSSDFinalAnswerNeedsNoSession(t *testing.T) {
	client := &transcriptClient{
		steps: []clientStep{
			{command: `AT+CUSD=1,"*#06#",15`, response: okResponse()},
		},
		urcs: []string{`+CUSD: 0,"Your balance is 5.00",15`},
	}
	manager, id := newStartedTestManager(t, client)
	result, err := manager.USSD(context.Background(), id, "*#06#")
	if err != nil {
		t.Fatalf("USSD: %v", err)
	}
	if result.Status != "final" || result.Continueable || result.SessionID != "" {
		t.Fatalf("final result = %+v, want no session", result)
	}
	client.assertDone(t)
}

func TestUSSDCancelFailureRetainsSessionForRetry(t *testing.T) {
	client := &transcriptClient{steps: []clientStep{
		{command: `AT+CUSD=1,"*100#",15`, response: okResponse()},
		{command: "AT+CUSD=2", err: errors.New("injected cancellation failure")},
		{command: "AT+CUSD=2", response: okResponse()},
	}, urcs: []string{`+CUSD: 1,"Main menu",15`}}
	manager, id := newStartedTestManager(t, client)
	start, err := manager.USSD(context.Background(), id, "*100#")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CancelUSSD(context.Background(), start.SessionID); err == nil {
		t.Fatal("expected cancellation failure")
	}
	if got, err := manager.ussdSessionDevice(start.SessionID); err != nil || got != id {
		t.Fatalf("lost session after failure: %q %v", got, err)
	}
	if err := manager.CancelUSSD(context.Background(), start.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ussdSessionDevice(start.SessionID); !errors.Is(err, ErrUSSDSessionNotFound) {
		t.Fatalf("session remains after success: %v", err)
	}
	client.assertDone(t)
}
