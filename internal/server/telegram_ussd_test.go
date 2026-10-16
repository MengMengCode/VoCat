package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"vocat/internal/vowifi"
)

type telegramUSSDTransport struct{ texts []string }

func (transport *telegramUSSDTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	transport.texts = append(transport.texts, payload.Text)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
}

func TestTelegramUSSIStartReplyAndCancelUseSameBinding(t *testing.T) {
	s, cellular, ims := newUSSDRoutingServer(t, true)
	transport := &telegramUSSDTransport{}
	bot := &telegramBot{server: s, client: &http.Client{Transport: transport}}
	// A public literal avoids DNS. RoundTrip intercepts every request; no real
	// Telegram message or modem operation is performed by this test.
	config := telegramRuntimeConfig{Token: "123456:test-token", BaseURL: "https://8.8.8.8", ChatID: "1", AdminID: 2}
	ctx := context.Background()
	bot.handleUSSDCommand(ctx, config, 1, 2, "saved-1-6", "#999#")
	state, ok := bot.input(1, 2)
	if !ok || state.Kind != "ussd_reply" || state.DeviceID != "saved-1-6" || s.ussdNetworkSession(state.Argument) != "dialog-1" {
		t.Fatalf("incorrect input binding: %+v", state)
	}
	token := state.Argument
	message := &telegramMessage{From: &telegramUser{ID: 2}, Chat: telegramChat{ID: 1}, Text: "1"}
	bot.handleInputMessage(ctx, config, message)
	state, ok = bot.input(1, 2)
	if !ok || state.Argument != token || state.DeviceID != "saved-1-6" || ims.lastDeviceID != "saved-1-6" || ims.lastRequest.SessionID != "dialog-1" || ims.lastRequest.Input != "1" {
		t.Fatalf("reply lost binding: state=%+v IMS=%+v", state, ims)
	}
	ims.sendErr = vowifi.ErrUSSINotReady
	bot.handleInputMessage(ctx, config, message)
	if state, ok := bot.input(1, 2); !ok || state.Argument != token {
		t.Fatal("failed reply lost input state")
	}
	var menuToken string
	for key, action := range bot.pending {
		if action.Kind == "menu_ussd_session" && action.Argument == token {
			menuToken = key
			break
		}
	}
	if menuToken == "" {
		t.Fatal("missing cancellation button")
	}
	callback := &telegramCallbackQuery{Data: "uc:" + menuToken + ":cancel", From: telegramUser{ID: 2}, Message: &telegramMessage{Chat: telegramChat{ID: 1}}}
	ims.cancelErr = errors.New("BYE failed")
	bot.handleCallback(ctx, config, callback)
	if state, ok := bot.input(1, 2); !ok || state.Argument != token {
		t.Fatal("failed cancellation lost input state")
	}
	if _, ok := bot.getPending(menuToken, 1, 2); !ok {
		t.Fatal("failed cancellation consumed its retry button")
	}
	if !strings.Contains(transport.texts[len(transport.texts)-1], "BYE failed") {
		t.Fatalf("cancellation error was hidden: %v", transport.texts)
	}
	ims.cancelErr = nil
	bot.handleCallback(ctx, config, callback)
	if _, ok := bot.input(1, 2); ok {
		t.Fatal("successful cancellation retained reply state")
	}
	if ims.cancelCalled != 2 || ims.cancelDeviceID != "saved-1-6" || ims.cancelSessionID != "dialog-1" {
		t.Fatalf("cancel lost binding: %+v", ims)
	}
	if cellular.starts+cellular.continues+cellular.cancels != 0 {
		t.Fatal("Telegram IMS operation reached cellular")
	}
	if len(transport.texts) != 5 {
		t.Fatalf("missing Telegram responses: %v", transport.texts)
	}
}

func TestTelegramUSSIFinalReplyClearsInputAndToken(t *testing.T) {
	s, cellular, ims := newUSSDRoutingServer(t, true)
	bot := &telegramBot{server: s, client: &http.Client{Transport: &telegramUSSDTransport{}}}
	config := telegramRuntimeConfig{Token: "123456:test-token", BaseURL: "https://8.8.8.8", ChatID: "1"}
	bot.handleUSSDCommand(context.Background(), config, 1, 2, "saved-1-6", "#999#")
	state, _ := bot.input(1, 2)
	ims.sendResult = vowifi.USSISubmitResult{Status: "final", Text: "balance", SessionID: "dialog-1"}
	bot.handleUSSDReply(context.Background(), config, 1, 2, state.Argument, "1")
	if _, ok := bot.input(1, 2); ok {
		t.Fatal("final reply retained input")
	}
	if _, err := s.ussdSessionDevice(state.Argument); err == nil {
		t.Fatal("final reply retained token")
	}
	if cellular.continues != 0 {
		t.Fatal("final IMS reply reached cellular")
	}
}
