package device

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"vocat/internal/loghub"
	"vocat/internal/modem"
)

func TestHardwareErrorDetailRedactsATPayload(t *testing.T) {
	const payload = "00880081221000112233445566778899AABBCCDDEEFF1000112233445566778899AABBCCDDEEFF00"
	commandErr := &modem.CommandError{
		Command: `AT+CSIM=78,"` + payload + `"`,
		Final:   "+CME ERROR: 13",
		Lines:   []string{payload},
	}
	err := fmt.Errorf("select ISIM: %w", errors.Join(errors.New("reader reset failed"), commandErr))
	detail := HardwareErrorDetail(err)
	if strings.Contains(detail, payload) || strings.Contains(detail, "AT+CSIM=") {
		t.Fatalf("hardware error exposed AT payload: %q", detail)
	}
	if !strings.Contains(detail, "select ISIM") || !strings.Contains(detail, "AT+CSIM failed: +CME ERROR: 13") {
		t.Fatalf("hardware error lost useful diagnostics: %q", detail)
	}
}

func TestManagerLogsNewHardwareFailuresWithoutPollingSpam(t *testing.T) {
	commandError := func() error {
		return &modem.CommandError{Command: "AT+CSQ", Final: "+CME ERROR: 13"}
	}
	client := &transcriptClient{steps: []clientStep{
		{command: "AT+CSQ", err: commandError()},
		{command: "AT+CSQ", err: commandError()},
		{command: "AT+CSQ", response: okResponse("+CSQ: 20,99")},
		{command: "AT+CSQ", err: commandError()},
	}}
	manager, id := newStartedTestManager(t, client)
	hub := loghub.New(nil, 100)
	manager.logger = slog.New(hub)

	for attempt := 0; attempt < 2; attempt++ {
		_, _ = manager.ExecuteAT(context.Background(), id, "AT+CSQ")
	}
	if entries := hub.History(10, slog.LevelDebug, ""); len(entries) != 1 {
		t.Fatalf("continuous failure produced %d log entries, want 1", len(entries))
	}
	_, _ = manager.ExecuteAT(context.Background(), id, "AT+CSQ")
	_, _ = manager.ExecuteAT(context.Background(), id, "AT+CSQ")

	entries := hub.History(10, slog.LevelDebug, "")
	if len(entries) != 2 {
		t.Fatalf("failure after recovery produced %d total log entries, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.Message != "hardware operation failed" || entry.Fields["device_id"] != id {
			t.Fatalf("hardware log entry = %#v", entry)
		}
		if entry.Fields["error"] != "AT+CSQ failed: +CME ERROR: 13" {
			t.Fatalf("hardware log detail = %#v", entry.Fields["error"])
		}
	}
	client.assertDone(t)
}
