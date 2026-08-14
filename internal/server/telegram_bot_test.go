package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func TestTelegramAPIURLSupportsBaseAndTemplate(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "base URL",
			baseURL: "https://api.telegram.org",
			want:    "https://api.telegram.org/bot123456:test-token/sendMessage",
		},
		{
			name:    "reverse proxy template",
			baseURL: "https://telegram.example.com/bot%s/%s",
			want:    "https://telegram.example.com/bot123456:test-token/sendMessage",
		},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			got, err := telegramAPIURL(item.baseURL, "123456:test-token", "sendMessage")
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != item.want {
				t.Fatalf("telegramAPIURL() = %q, want %q", got, item.want)
			}
		})
	}
}

func TestTelegramAPIURLRejectsMalformedTemplates(t *testing.T) {
	for _, value := range []string{
		"https://telegram.example.com/bot%s/sendMessage",
		"https://%s.example.com/bot/token/%s",
		"http://telegram.example.com/bot%s/%s",
	} {
		if _, err := telegramAPIURL(value, "123456:test-token", "sendMessage"); err == nil {
			t.Errorf("telegramAPIURL(%q) unexpectedly succeeded", value)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("telegramAPIURL(%q) returned an empty error", value)
		}
	}
}

func TestParseTelegramCommand(t *testing.T) {
	command, remainder := parseTelegramCommand("  /sms@vocat_bot EC20 +447700900123 hello world  ")
	if command != "sms" || remainder != "EC20 +447700900123 hello world" {
		t.Fatalf("parseTelegramCommand() = %q, %q", command, remainder)
	}
	if command, _ := parseTelegramCommand("ordinary message"); command != "" {
		t.Fatalf("non-command parsed as %q", command)
	}
}

func TestSplitTelegramArgumentsPreservesMessageBody(t *testing.T) {
	parts := splitTelegramArguments("  EC20   +447700900123   code with spaces  ", 3)
	if len(parts) != 3 || parts[0] != "EC20" || parts[1] != "+447700900123" || parts[2] != "code with spaces" {
		t.Fatalf("splitTelegramArguments() = %#v", parts)
	}
}

func TestValidTelegramDialNumber(t *testing.T) {
	for _, value := range []string{"10086", "+447700900123", "12345678901234567890"} {
		if !validTelegramDialNumber(value) {
			t.Errorf("validTelegramDialNumber(%q) = false", value)
		}
	}
	for _, value := range []string{"12", "+", "123;ATH", "12 34", "123456789012345678901"} {
		if validTelegramDialNumber(value) {
			t.Errorf("validTelegramDialNumber(%q) = true", value)
		}
	}
}

func TestResolveTelegramPhoneNumberPrefersCurrentSIMAssociation(t *testing.T) {
	snapshot := &device.Snapshot{
		ICCID: "89441000400128013903",
		Phone: device.PhoneNumber{Number: "00000000000"},
	}
	state := &vowifi.State{
		ICCID:       snapshot.ICCID,
		PhoneNumber: "+447386125520",
	}
	if got := resolveTelegramPhoneNumber("+447700900123", state, snapshot); got != "+447700900123" {
		t.Fatalf("resolved association number = %q", got)
	}
	if got := resolveTelegramPhoneNumber("", state, snapshot); got != "+447386125520" {
		t.Fatalf("resolved IMS number = %q", got)
	}
}

func TestResolveTelegramPhoneNumberRejectsPlaceholderAndStaleRuntime(t *testing.T) {
	snapshot := &device.Snapshot{
		ICCID: "current-card",
		Phone: device.PhoneNumber{Number: "00000000000"},
	}
	state := &vowifi.State{
		ICCID:       "previous-card",
		PhoneNumber: "+447386083638",
	}
	if got := resolveTelegramPhoneNumber("", state, snapshot); got != "--" {
		t.Fatalf("stale or placeholder number leaked as %q", got)
	}
	for _, placeholder := range []string{"00000000000", "1111111111", "+0000000000", "not-a-number"} {
		if usableTelegramPhoneNumber(placeholder) {
			t.Errorf("placeholder %q was accepted", placeholder)
		}
	}
}

func TestTelegramCarrierPresentationSeparatesHomeAndServingNetworks(t *testing.T) {
	if got := telegramHomeCarrier("234336570710174"); !strings.Contains(got, "🇬🇧") || !strings.Contains(got, "23433") {
		t.Fatalf("home carrier = %q", got)
	}
	if got := telegramHomeCarrier("454006395879502", "Saily"); !strings.Contains(got, "1O1O / csl / Club Sim") || !strings.Contains(got, "45400") || !strings.Contains(got, "🇭🇰") || strings.Contains(got, "Saily") {
		t.Fatalf("profile brand overrode home carrier = %q", got)
	}
	if got := telegramHomeCarrier("999991234567890", "Unknown Brand"); got != "Unknown Brand" {
		t.Fatalf("unknown home carrier did not fall back to SPN: %q", got)
	}
	flight := &device.Snapshot{FlightMode: true, OperatorName: "stale network", RegistrationStatus: 1}
	if got := telegramCurrentNetwork(flight); got != "--（飞行模式）" {
		t.Fatalf("flight-mode serving network = %q", got)
	}
	serving := &device.Snapshot{OperatorCode: "46001", RegistrationStatus: 5, AccessTech: "LTE", Band: "B3"}
	if got := telegramCurrentNetwork(serving); !strings.Contains(got, "🇨🇳") || !strings.Contains(got, "已驻网（漫游）") {
		t.Fatalf("serving network = %q", got)
	}
}

func TestTelegramPendingActionIsAuthorizedOneShot(t *testing.T) {
	bot := &telegramBot{pending: make(map[string]telegramPendingAction)}
	action := telegramPendingAction{Kind: "call", ChatID: -1001, AdminID: 42, CreatedAt: time.Now()}
	token, err := bot.putPending(action)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bot.takePending(token, -1001, 41); ok {
		t.Fatal("different administrator consumed pending action")
	}
	if _, ok := bot.takePending(token, -1001, 42); ok {
		t.Fatal("an unauthorized attempt must invalidate the one-time action")
	}
}

func TestTelegramMenuCallbackParsing(t *testing.T) {
	prefix, token, operation, ok := parseTelegramMenuCallback("call:0123456789abcdef:answer")
	if !ok || prefix != "call" || token != "0123456789abcdef" || operation != "answer" {
		t.Fatalf("parsed callback = %q %q %q %t", prefix, token, operation, ok)
	}
	for _, invalid := range []string{"", "call:token", "unknown:token:op", "d::status"} {
		if _, _, _, ok := parseTelegramMenuCallback(invalid); ok {
			t.Fatalf("invalid callback %q was accepted", invalid)
		}
	}
}

func TestTelegramMenuPendingCanBeReusedButConfirmationCannot(t *testing.T) {
	bot := &telegramBot{pending: make(map[string]telegramPendingAction)}
	menuToken, err := bot.putPending(telegramPendingAction{
		Kind: "menu_device", DeviceID: "EC20", ChatID: 1, AdminID: 2, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bot.getPending(menuToken, 1, 2); !ok {
		t.Fatal("first menu lookup failed")
	}
	if _, ok := bot.getPending(menuToken, 1, 2); !ok {
		t.Fatal("menu token was unexpectedly consumed")
	}
	confirmToken, err := bot.putPending(telegramPendingAction{
		Kind: "sms", ChatID: 1, AdminID: 2, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bot.takePending(confirmToken, 1, 2); !ok {
		t.Fatal("confirmation token lookup failed")
	}
	if _, ok := bot.takePending(confirmToken, 1, 2); ok {
		t.Fatal("confirmation token was reusable")
	}
}

func TestTelegramInputStateIsScopedAndCancelable(t *testing.T) {
	bot := &telegramBot{inputs: make(map[string]telegramInputState)}
	bot.setInput(telegramInputState{Kind: "sms_phone", DeviceID: "EC20", ChatID: 10, AdminID: 20})
	if state, ok := bot.input(10, 20); !ok || state.DeviceID != "EC20" || state.Kind != "sms_phone" {
		t.Fatalf("input state = %#v, %t", state, ok)
	}
	if _, ok := bot.input(10, 21); ok {
		t.Fatal("another administrator read the input state")
	}
	bot.clearInput(10, 20)
	if _, ok := bot.input(10, 20); ok {
		t.Fatal("cleared input state remained available")
	}
}

func TestFormatTelegramATIncludesFinalResult(t *testing.T) {
	if got := formatTelegramAT(modem.Response{Final: "OK"}); got != "OK" {
		t.Fatalf("formatTelegramAT(OK) = %q", got)
	}
	if got := formatTelegramAT(modem.Response{Lines: []string{"+CLCC: 1"}, Final: "OK"}); got != "+CLCC: 1\nOK" {
		t.Fatalf("formatTelegramAT(lines) = %q", got)
	}
}

func TestTelegramExecutesGuardedATForConfiguredDevice(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "EC20", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	bot := &telegramBot{server: &Server{
		store: database,
		devices: fakeDeviceController{
			entry:      device.Device{ID: "EC20", Discovered: true},
			atResponse: modem.Response{Lines: []string{"+CSQ: 18,99"}, Final: "OK"},
		},
	}}
	result, err := bot.executeATCommand(context.Background(), "EC20", "AT+CSQ")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"设备：EC20", "> AT+CSQ", "+CSQ: 18,99", "OK"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("AT result %q does not contain %q", result, expected)
		}
	}
	if _, err := bot.executeATCommand(context.Background(), "EC20", "AT+CFUN=0"); err == nil {
		t.Fatal("guarded AT command unexpectedly succeeded")
	}
}

func TestTelegramExecutesInteractiveUSSDForConfiguredDevice(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "EC20", Name: "EC20"}); err != nil {
		t.Fatal(err)
	}
	bot := &telegramBot{server: &Server{
		store: database,
		devices: fakeDeviceController{
			entry: device.Device{ID: "EC20", Discovered: true},
			ussdResult: device.USSDResult{
				Code: "*100#", Text: "1. Balance\n2. Bundles", Status: "awaiting_input",
				SessionID: "0123456789abcdef", Continueable: true,
			},
		},
	}}
	result, err := bot.executeUSSDCommand(context.Background(), "EC20", "*100#")
	if err != nil {
		t.Fatal(err)
	}
	formatted := formatTelegramUSSD("EC20", result)
	for _, expected := range []string{
		"设备：EC20", "状态：awaiting_input", "1. Balance", "请直接发送回复内容",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("USSD result %q does not contain %q", formatted, expected)
		}
	}
}

func TestTelegramESIMSwitchQuiescesAndInvalidatesVoWiFiFirst(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "EC20", Name: "410", VoWiFiEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	controller := &recordingVoWiFiInvalidator{state: vowifi.State{
		DeviceID: "EC20", Enabled: true, Active: true, Phase: vowifi.PhaseSMSReady,
	}, events: &events}
	devices := &recordingESIMDeviceController{
		fakeDeviceController: fakeDeviceController{entry: device.Device{ID: "EC20", Discovered: true}},
		events:               &events,
		guard:                controller,
	}
	bot := &telegramBot{server: &Server{
		store: database, devices: devices, vowifi: controller, logger: regionTestLogger(),
	}}
	result, err := bot.executeESIMSwitch(context.Background(), telegramPendingAction{
		DeviceID: "EC20", TargetICCID: "8900000000000000001", TargetAID: "A0",
	})
	if err != nil {
		t.Fatalf("executeESIMSwitch: %v", err)
	}
	if !strings.Contains(result, "8900000000000000001") {
		t.Fatalf("switch result = %q", result)
	}
	if got := strings.Join(events, ","); got != "quiesce,begin,switch,release" {
		t.Fatalf("Telegram profile switch order = %q", got)
	}
}

func TestTelegramErrorsRedactBotTokens(t *testing.T) {
	token := "1234567890:abcdefghijklmnopqrstuvwxyzABCDE"
	err := errors.New(`Post "https://api.telegram.org/bot` + token + `/getUpdates": context canceled`)
	redacted := redactTelegramError(err, token)
	if strings.Contains(redacted.Error(), token) || !strings.Contains(redacted.Error(), "bot[REDACTED]") {
		t.Fatalf("redacted error = %q", redacted)
	}
}

func TestTelegramCallTransportFollowsConfiguredCardMode(t *testing.T) {
	controller := &telegramTestCallController{state: vowifi.State{IMSReady: true, Phase: vowifi.PhaseIMSReady}}
	bot := &telegramBot{server: &Server{vowifi: controller}}

	transport, gotController, err := bot.telegramCallTransport(
		store.Device{ID: "EC20", VoWiFiEnabled: true},
		device.Device{Snapshot: &device.Snapshot{FlightMode: true}},
	)
	if err != nil || transport != "vowifi" || gotController == nil {
		t.Fatalf("VoWiFi route = %q, %#v, %v", transport, gotController, err)
	}

	transport, gotController, err = bot.telegramCallTransport(
		store.Device{ID: "EC20", VoWiFiEnabled: false},
		device.Device{Snapshot: &device.Snapshot{FlightMode: false}},
	)
	if err != nil || transport != "cellular" || gotController != nil {
		t.Fatalf("cellular route = %q, %#v, %v", transport, gotController, err)
	}
}

func TestTelegramCallTransportDoesNotFallBackFromUnreadyVoWiFi(t *testing.T) {
	controller := &telegramTestCallController{state: vowifi.State{
		Phase: vowifi.PhaseFailed, LastError: "SIP registration was rejected: SIP 403",
	}}
	bot := &telegramBot{server: &Server{vowifi: controller}}
	_, _, err := bot.telegramCallTransport(
		store.Device{ID: "EC20", VoWiFiEnabled: true},
		device.Device{Snapshot: &device.Snapshot{FlightMode: true}},
	)
	if err == nil || !strings.Contains(err.Error(), "SIP 403") {
		t.Fatalf("unready VoWiFi route error = %v", err)
	}
}

func TestTelegramExecuteTimedCallRejectsEmergencyBeforeTransport(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "EC20", Name: "EC20", VoWiFiEnabled: true}); err != nil {
		t.Fatal(err)
	}
	controller := &telegramTestCallController{state: vowifi.State{IMSReady: true}}
	bot := &telegramBot{server: &Server{
		store:  database,
		vowifi: controller,
		devices: fakeDeviceController{entry: device.Device{
			ID: "EC20", Discovered: true, Snapshot: &device.Snapshot{},
		}},
	}}
	_, err = bot.executeTimedCall(context.Background(), telegramRuntimeConfig{}, telegramPendingAction{
		DeviceID: "EC20", Argument: "112", Duration: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "紧急") {
		t.Fatalf("emergency Telegram dial error = %v", err)
	}
	if controller.dialed != "" {
		t.Fatalf("emergency number reached Telegram VoWiFi transport: %q", controller.dialed)
	}
}

func TestTelegramTimedVoWiFiCallUsesIMSAndHangsUpByCallID(t *testing.T) {
	controller := &telegramTestCallController{state: vowifi.State{IMSReady: true}}
	controller.dialResult = vowifi.Call{ID: "ims-call-1", Number: "+447700900123", Direction: "outgoing", State: "dialing"}
	controller.calls = []vowifi.Call{{ID: "ims-call-1", Number: "+447700900123", Direction: "outgoing", State: "active"}}
	bot := &telegramBot{server: &Server{vowifi: controller}}
	result, err := bot.executeTimedVoWiFiCall(context.Background(), telegramRuntimeConfig{}, telegramPendingAction{
		DeviceID: "EC20", Argument: "+447700900123", Duration: 20 * time.Millisecond,
	}, controller)
	if err != nil || !strings.Contains(result, "已接通") {
		t.Fatalf("timed VoWiFi result = %q, %v", result, err)
	}
	if controller.dialed != "+447700900123" || len(controller.hungUp) != 1 || controller.hungUp[0] != "ims-call-1" {
		t.Fatalf("IMS actions dial=%q hangup=%#v", controller.dialed, controller.hungUp)
	}
}

func TestTelegramTimedCellularCallUsesATDCLCCAndATH(t *testing.T) {
	commands := make([]string, 0, 3)
	devices := fakeDeviceController{atHandler: func(command string) (modem.Response, error) {
		commands = append(commands, command)
		switch {
		case strings.HasPrefix(command, "ATD"):
			return modem.Response{Final: "OK"}, nil
		case command == "AT+CLCC":
			return modem.Response{Lines: []string{`+CLCC: 1,0,2,0,0,"+447700900123",145`}, Final: "OK"}, nil
		case command == "ATH":
			return modem.Response{Final: "OK"}, nil
		default:
			return modem.Response{}, errors.New("unexpected command")
		}
	}}
	bot := &telegramBot{server: &Server{devices: devices}}
	result, err := bot.executeTimedCellularCall(context.Background(), telegramRuntimeConfig{}, telegramPendingAction{
		DeviceID: "EC20", Argument: "+447700900123", Duration: 20 * time.Millisecond,
	}, "physical")
	if err != nil || !strings.Contains(result, "正在拨号") {
		t.Fatalf("timed cellular result = %q, %v", result, err)
	}
	joined := strings.Join(commands, ",")
	for _, expected := range []string{"ATD+447700900123;", "AT+CLCC", "ATH"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("commands %q omit %q", joined, expected)
		}
	}
}

func TestTelegramVoWiFiFailureIncludesSIPDiagnostic(t *testing.T) {
	err := telegramVoWiFiCallFailure(vowifi.Call{State: "failed", SIPCode: 403, Reason: "Forbidden"})
	if !strings.Contains(err.Error(), "SIP 403") || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("VoWiFi diagnostic = %q", err)
	}
}

func TestTelegramVoWiFi487IsReportedAsCancelledOutcome(t *testing.T) {
	result, err := telegramVoWiFiCallOutcome("888", vowifi.Call{
		State: "failed", SIPCode: 487, Reason: "Request Terminated",
	})
	if err != nil || !strings.Contains(result, "取消或终止") || !strings.Contains(result, "SIP 487") {
		t.Fatalf("487 outcome = %q, %v", result, err)
	}
}

type telegramTestCallController struct {
	state      vowifi.State
	calls      []vowifi.Call
	dialResult vowifi.Call
	dialErr    error
	dialed     string
	hungUp     []string
}

func (controller *telegramTestCallController) State(string) (vowifi.State, error) {
	return controller.state, nil
}

func (controller *telegramTestCallController) RequestEnabled(string, bool) (vowifi.State, error) {
	return controller.state, nil
}

func (controller *telegramTestCallController) RequestReconnect(string) (vowifi.State, error) {
	return controller.state, nil
}

func (controller *telegramTestCallController) Calls(string) ([]vowifi.Call, error) {
	return append([]vowifi.Call(nil), controller.calls...), nil
}

func (controller *telegramTestCallController) DialCall(_ context.Context, _ string, number string) (vowifi.Call, error) {
	controller.dialed = number
	return controller.dialResult, controller.dialErr
}

func (controller *telegramTestCallController) AnswerCall(_ context.Context, _ string, id string) (vowifi.Call, error) {
	for _, call := range controller.calls {
		if call.ID == id {
			call.State = "active"
			return call, nil
		}
	}
	return vowifi.Call{}, errors.New("call not found")
}

func (controller *telegramTestCallController) HangupCall(_ context.Context, _ string, id string) error {
	controller.hungUp = append(controller.hungUp, id)
	return nil
}
