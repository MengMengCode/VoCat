package server

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
	vowifiruntime "vocat/internal/vowifi/runtime"
)

const (
	telegramPollInterval       = 3 * time.Second
	telegramNotificationPeriod = 2 * time.Second
	telegramConfirmationTTL    = 2 * time.Minute
	telegramMenuTTL            = 15 * time.Minute
	telegramInputTTL           = 10 * time.Minute
	telegramMaxDialDuration    = 10 * time.Minute
)

var telegramTokenInURLPattern = regexp.MustCompile(`bot[0-9]{5,20}:[A-Za-z0-9_-]{20,128}`)

type telegramRuntimeConfig struct {
	Token   string
	ChatID  string
	AdminID int64
	BaseURL string
	Proxy   string
}

type telegramBot struct {
	server *Server

	clientMu    sync.Mutex
	clientProxy string
	client      *http.Client

	pendingMu sync.Mutex
	pending   map[string]telegramPendingAction

	logMu       sync.Mutex
	lastLogTime time.Time
	lastLogText string

	callMu      sync.Mutex
	activeDials map[string]struct{}

	inputMu sync.Mutex
	inputs  map[string]telegramInputState
}

type telegramPendingAction struct {
	Kind        string
	DeviceID    string
	Argument    string
	Text        string
	Duration    time.Duration
	ChatID      int64
	AdminID     int64
	CreatedAt   time.Time
	TargetAID   string
	TargetICCID string
}

type telegramInputState struct {
	Kind      string
	DeviceID  string
	Argument  string
	ChatID    int64
	AdminID   int64
	CreatedAt time.Time
}

type telegramAPIResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

type telegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *telegramMessage       `json:"message"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query"`
}

type telegramMessage struct {
	MessageID int64         `json:"message_id"`
	From      *telegramUser `json:"from"`
	Chat      telegramChat  `json:"chat"`
	Text      string        `json:"text"`
}

type telegramUser struct {
	ID int64 `json:"id"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    telegramUser     `json:"from"`
	Message *telegramMessage `json:"message"`
	Data    string           `json:"data"`
}

type telegramMenuMessageContextKey struct{}

// StartTelegramBot starts both the Telegram command poller and durable inbound
// SMS notifier. Configuration is reloaded between polls, so saving Settings
// takes effect without restarting vocat.
func (s *Server) StartTelegramBot(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	bot := &telegramBot{
		server:      s,
		pending:     make(map[string]telegramPendingAction),
		activeDials: make(map[string]struct{}),
		inputs:      make(map[string]telegramInputState),
	}
	go bot.poll(ctx)
	go bot.notifyInboundSMS(ctx)
}

func (bot *telegramBot) poll(ctx context.Context) {
	activeToken := ""
	var offset int64
	for ctx.Err() == nil {
		config, enabled, err := bot.loadConfig(ctx)
		if err != nil {
			bot.warn("load Telegram bot configuration", err)
			if !waitTelegram(ctx, telegramPollInterval) {
				return
			}
			continue
		}
		if !enabled {
			activeToken = ""
			offset = 0
			if !waitTelegram(ctx, telegramPollInterval) {
				return
			}
			continue
		}
		if config.Token != activeToken {
			offset, err = bot.bootstrap(ctx, config)
			if err != nil {
				bot.warn("start Telegram bot polling", err)
				if !waitTelegram(ctx, telegramPollInterval) {
					return
				}
				continue
			}
			activeToken = config.Token
		}
		pollContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		updates, pollErr := bot.getUpdates(pollContext, config, offset, 5)
		cancel()
		if pollErr != nil {
			if ctx.Err() != nil {
				return
			}
			bot.warn("poll Telegram updates", pollErr)
			if !waitTelegram(ctx, telegramPollInterval) {
				return
			}
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			update := update
			go bot.handleUpdate(ctx, config, update)
		}
	}
}

// bootstrap discards stale Telegram updates. Replaying an old /sms, /call, or
// /switch command after a service restart would be unsafe even though each
// command has its own confirmation step.
func (bot *telegramBot) bootstrap(ctx context.Context, config telegramRuntimeConfig) (int64, error) {
	requestContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	updates, err := bot.getUpdates(requestContext, config, -1, 0)
	if err != nil {
		return 0, err
	}
	var offset int64
	for _, update := range updates {
		if update.UpdateID >= offset {
			offset = update.UpdateID + 1
		}
	}
	commands := []map[string]string{
		{"command": "menu", "description": "打开可视化操作菜单"},
		{"command": "status", "description": "查看全部设备状态"},
		{"command": "cancel", "description": "取消当前输入或操作"},
		{"command": "help", "description": "查看帮助"},
	}
	if err := bot.call(requestContext, config, "setMyCommands", map[string]any{"commands": commands}, nil); err != nil {
		bot.warn("register Telegram command menu", err)
	}
	return offset, nil
}

func (bot *telegramBot) getUpdates(
	ctx context.Context,
	config telegramRuntimeConfig,
	offset int64,
	timeout int,
) ([]telegramUpdate, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	}
	var updates []telegramUpdate
	if err := bot.call(ctx, config, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (bot *telegramBot) handleUpdate(ctx context.Context, config telegramRuntimeConfig, update telegramUpdate) {
	if callback := update.CallbackQuery; callback != nil {
		if callback.Message == nil || !bot.authorized(config, callback.Message.Chat.ID, callback.From.ID) {
			go func() { _ = bot.answerCallback(context.Background(), config, callback.ID, "无权限") }()
			return
		}
		go func() { _ = bot.answerCallback(context.Background(), config, callback.ID, "") }()
		bot.handleCallback(ctx, config, callback)
		return
	}
	message := update.Message
	if message == nil || message.From == nil || !bot.authorized(config, message.Chat.ID, message.From.ID) {
		return
	}
	command, remainder := parseTelegramCommand(message.Text)
	if _, waiting := bot.input(message.Chat.ID, message.From.ID); waiting &&
		command != "menu" && command != "start" && command != "cancel" {
		bot.handleInputMessage(ctx, config, message)
		return
	}
	if command == "" {
		bot.handleInputMessage(ctx, config, message)
		return
	}
	switch command {
	case "start", "menu":
		bot.sendMainMenu(ctx, config, message.Chat.ID)
	case "help":
		bot.sendHelp(ctx, config, message.Chat.ID)
	case "cancel":
		bot.clearInput(message.Chat.ID, message.From.ID)
		bot.sendText(ctx, config, message.Chat.ID, "已取消当前输入或操作。", bot.homeKeyboard())
	case "status", "devices":
		bot.sendDeviceStatus(ctx, config, message.Chat.ID, strings.TrimSpace(remainder))
	case "esim":
		deviceID := strings.TrimSpace(remainder)
		if deviceID == "" {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "esim")
		} else {
			bot.sendESIMProfiles(ctx, config, message.Chat.ID, message.From.ID, deviceID)
		}
	case "switch":
		parts := strings.Fields(remainder)
		if len(parts) == 0 {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "esim")
			return
		}
		if len(parts) == 1 {
			bot.sendESIMProfiles(ctx, config, message.Chat.ID, message.From.ID, parts[0])
			return
		}
		if len(parts) != 2 {
			bot.sendText(ctx, config, message.Chat.ID, "用法：/switch <设备ID> <目标ICCID>", nil)
			return
		}
		bot.confirmESIMSwitch(ctx, config, message.Chat.ID, message.From.ID, parts[0], parts[1])
	case "wfc", "wificalling":
		parts := strings.Fields(remainder)
		if len(parts) == 0 {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "wfc")
			return
		}
		if len(parts) == 1 {
			bot.sendVoWiFiMenu(ctx, config, message.Chat.ID, message.From.ID, parts[0])
			return
		}
		if len(parts) != 2 {
			bot.sendText(ctx, config, message.Chat.ID, "请选择菜单按钮，或使用 /wfc <设备ID> <status|on|off|reconnect>。", bot.homeKeyboard())
			return
		}
		bot.handleVoWiFi(ctx, config, message.Chat.ID, message.From.ID, parts[0], parts[1])
	case "sms":
		parts := splitTelegramArguments(remainder, 3)
		if len(parts) == 0 {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "sms")
			return
		}
		if len(parts) == 1 {
			bot.beginInput(ctx, config, message.Chat.ID, message.From.ID, parts[0], "sms_phone", "请输入收件人号码：")
			return
		}
		if len(parts) != 3 {
			bot.sendText(ctx, config, message.Chat.ID, "用法：/sms <设备ID> <号码> <短信内容>", nil)
			return
		}
		bot.confirmSMS(ctx, config, message.Chat.ID, message.From.ID, parts[0], parts[1], parts[2])
	case "call":
		parts := strings.Fields(remainder)
		if len(parts) == 0 {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "call")
			return
		}
		if len(parts) == 1 {
			bot.beginInput(ctx, config, message.Chat.ID, message.From.ID, parts[0], "call_number", "请输入要拨打的电话号码：")
			return
		}
		if len(parts) != 3 {
			bot.sendText(ctx, config, message.Chat.ID, "用法：/call <设备ID> <号码> <持续秒数>\n拨号后将在指定时间自动挂断，不处理通话音频。", nil)
			return
		}
		seconds, err := strconv.Atoi(parts[2])
		if err != nil || seconds < 1 || time.Duration(seconds)*time.Second > telegramMaxDialDuration {
			bot.sendText(ctx, config, message.Chat.ID, "持续时间必须是 1–600 秒。", nil)
			return
		}
		bot.confirmCall(ctx, config, message.Chat.ID, message.From.ID, parts[0], parts[1], time.Duration(seconds)*time.Second)
	case "answer":
		if strings.TrimSpace(remainder) == "" {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "answer")
		} else {
			bot.executeSimpleCallAction(ctx, config, message.Chat.ID, message.From.ID, strings.TrimSpace(remainder), "answer")
		}
	case "hangup":
		if strings.TrimSpace(remainder) == "" {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "hangup")
		} else {
			bot.executeSimpleCallAction(ctx, config, message.Chat.ID, message.From.ID, strings.TrimSpace(remainder), "hangup")
		}
	case "calls":
		if strings.TrimSpace(remainder) == "" {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "calls")
		} else {
			bot.executeSimpleCallAction(ctx, config, message.Chat.ID, message.From.ID, strings.TrimSpace(remainder), "status")
		}
	case "at":
		parts := splitTelegramArguments(remainder, 2)
		if len(parts) == 0 {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "at")
			return
		}
		if len(parts) == 1 {
			bot.beginInput(ctx, config, message.Chat.ID, message.From.ID, parts[0], "at", "请直接发送一条 AT 指令，例如 AT+CSQ：")
			return
		}
		if len(parts) != 2 {
			bot.sendText(ctx, config, message.Chat.ID, "用法：/at <设备ID> <AT指令>\n示例：/at EC20 AT+CSQ", nil)
			return
		}
		bot.handleATCommand(ctx, config, message.Chat.ID, message.From.ID, parts[0], parts[1])
	case "ussd":
		parts := strings.Fields(remainder)
		if len(parts) == 0 {
			bot.sendDevicePicker(ctx, config, message.Chat.ID, message.From.ID, "ussd")
			return
		}
		if len(parts) == 1 {
			bot.beginInput(ctx, config, message.Chat.ID, message.From.ID, parts[0], "ussd", "请直接发送 USSD 代码，例如 *100#：")
			return
		}
		if len(parts) != 2 {
			bot.sendText(ctx, config, message.Chat.ID, "用法：/ussd <设备ID> <USSD代码>\n示例：/ussd EC20 *100#", nil)
			return
		}
		bot.handleUSSDCommand(ctx, config, message.Chat.ID, message.From.ID, parts[0], parts[1])
	case "ussd_reply":
		parts := splitTelegramArguments(remainder, 2)
		if len(parts) != 2 {
			bot.sendText(ctx, config, message.Chat.ID, "用法：/ussd_reply <会话ID> <回复内容>", nil)
			return
		}
		bot.handleUSSDReply(ctx, config, message.Chat.ID, message.From.ID, parts[0], parts[1])
	case "ussd_cancel":
		sessionID := strings.TrimSpace(remainder)
		if sessionID == "" || strings.ContainsAny(sessionID, " \t\r\n") {
			bot.sendText(ctx, config, message.Chat.ID, "用法：/ussd_cancel <会话ID>", nil)
			return
		}
		bot.handleUSSDCancel(ctx, config, message.Chat.ID, message.From.ID, sessionID)
	default:
		bot.sendText(ctx, config, message.Chat.ID, "未知命令，请使用下方操作菜单。", bot.homeKeyboard())
	}
}

func (bot *telegramBot) handleCallback(ctx context.Context, config telegramRuntimeConfig, callback *telegramCallbackQuery) {
	data := strings.TrimSpace(callback.Data)
	chatID, adminID := callback.Message.Chat.ID, callback.From.ID
	ctx = context.WithValue(ctx, telegramMenuMessageContextKey{}, callback.Message.MessageID)
	if data == "menu:home" || data == "menu:devices" {
		if data == "menu:home" {
			bot.sendMainMenu(ctx, config, chatID)
		} else {
			bot.sendDevicePicker(ctx, config, chatID, adminID, "")
		}
		return
	}
	if data == "menu:status" {
		bot.sendDeviceStatus(ctx, config, chatID, "")
		return
	}
	if data == "menu:help" {
		bot.sendHelp(ctx, config, chatID)
		return
	}
	if data == "input:cancel" {
		bot.clearInput(chatID, adminID)
		bot.sendText(ctx, config, chatID, "已取消输入。", bot.homeKeyboard())
		return
	}
	if strings.HasPrefix(data, "pick:") {
		action, ok := bot.takePending(strings.TrimPrefix(data, "pick:"), chatID, adminID)
		if !ok || action.Kind != "menu_pick" {
			bot.sendExpiredMenu(ctx, config, chatID)
			return
		}
		bot.dispatchDeviceChoice(ctx, config, chatID, adminID, action.DeviceID, action.Argument)
		return
	}
	if prefix, token, operation, ok := parseTelegramMenuCallback(data); ok {
		consume := prefix == "es" || prefix == "dur" || prefix == "uc"
		var action telegramPendingAction
		var found bool
		if consume {
			action, found = bot.takePending(token, chatID, adminID)
		} else {
			action, found = bot.getPending(token, chatID, adminID)
		}
		if !found {
			bot.sendExpiredMenu(ctx, config, chatID)
			return
		}
		switch prefix {
		case "d":
			if action.Kind != "menu_device" {
				bot.sendExpiredMenu(ctx, config, chatID)
				return
			}
			bot.dispatchDeviceChoice(ctx, config, chatID, adminID, action.DeviceID, operation)
		case "w":
			if action.Kind != "menu_device" {
				bot.sendExpiredMenu(ctx, config, chatID)
				return
			}
			bot.handleVoWiFi(ctx, config, chatID, adminID, action.DeviceID, operation)
		case "call":
			if action.Kind != "menu_device" {
				bot.sendExpiredMenu(ctx, config, chatID)
				return
			}
			bot.dispatchCallAction(ctx, config, chatID, adminID, action.DeviceID, operation)
		case "es":
			if action.Kind != "menu_esim_profile" {
				bot.sendExpiredMenu(ctx, config, chatID)
				return
			}
			bot.confirmESIMSwitch(ctx, config, chatID, adminID, action.DeviceID, action.TargetICCID)
		case "dur":
			if action.Kind != "menu_call_duration" {
				bot.sendExpiredMenu(ctx, config, chatID)
				return
			}
			seconds, err := strconv.Atoi(operation)
			if err != nil || seconds < 1 || time.Duration(seconds)*time.Second > telegramMaxDialDuration {
				bot.sendText(ctx, config, chatID, "自动挂断时间无效。", bot.homeKeyboard())
				return
			}
			bot.confirmCall(ctx, config, chatID, adminID, action.DeviceID, action.Argument, time.Duration(seconds)*time.Second)
		case "uc":
			if action.Kind != "menu_ussd_session" {
				bot.sendExpiredMenu(ctx, config, chatID)
				return
			}
			bot.clearInput(chatID, adminID)
			bot.handleUSSDCancel(ctx, config, chatID, adminID, action.Argument)
		}
		return
	}
	decision, token, found := strings.Cut(data, ":")
	if !found || (decision != "confirm" && decision != "cancel") {
		return
	}
	action, ok := bot.takePending(token, callback.Message.Chat.ID, callback.From.ID)
	if !ok {
		bot.sendText(ctx, config, callback.Message.Chat.ID, "该确认已过期或已处理。", nil)
		return
	}
	if decision == "cancel" {
		bot.sendText(ctx, config, callback.Message.Chat.ID, "操作已取消。", bot.homeKeyboard())
		return
	}
	switch action.Kind {
	case "sms":
		bot.sendText(ctx, config, action.ChatID, "正在提交短信…", nil)
		result, err := bot.executeSMS(ctx, action)
		bot.finishAction(ctx, config, action, "telegram.sms.send", result, err)
	case "esim_switch":
		bot.sendText(ctx, config, action.ChatID, "正在切换 Profile 并等待模块恢复校验…", nil)
		result, err := bot.executeESIMSwitch(ctx, action)
		bot.finishAction(ctx, config, action, "telegram.esim.switch", result, err)
	case "call":
		result, err := bot.executeTimedCall(ctx, config, action)
		bot.finishAction(ctx, config, action, "telegram.call.dial", result, err)
	}
}

func parseTelegramMenuCallback(data string) (prefix, token, operation string, ok bool) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	switch parts[0] {
	case "d", "w", "call", "es", "dur", "uc":
		return parts[0], parts[1], parts[2], true
	default:
		return "", "", "", false
	}
}

func telegramKeyboard(rows ...[]map[string]string) map[string]any {
	return map[string]any{"inline_keyboard": rows}
}

func telegramButton(text, data string) map[string]string {
	return map[string]string{"text": text, "callback_data": data}
}

func (bot *telegramBot) homeKeyboard() map[string]any {
	return telegramKeyboard([]map[string]string{
		telegramButton("📱 选择设备", "menu:devices"),
		telegramButton("🏠 主菜单", "menu:home"),
	})
}

func (bot *telegramBot) sendMainMenu(ctx context.Context, config telegramRuntimeConfig, chatID int64) {
	bot.clearInput(chatID, config.AdminID)
	bot.sendMenuText(ctx, config, chatID,
		"Vocat 控制中心\n\n请选择设备或直接查看全部设备状态。发送 /menu 可随时返回这里。",
		telegramKeyboard(
			[]map[string]string{telegramButton("📱 设备操作", "menu:devices")},
			[]map[string]string{
				telegramButton("📊 全部状态", "menu:status"),
				telegramButton("❓ 帮助", "menu:help"),
			},
		),
	)
}

func (bot *telegramBot) sendDevicePicker(
	ctx context.Context,
	config telegramRuntimeConfig,
	chatID, adminID int64,
	next string,
) {
	configs, err := bot.server.store.ListDevices(ctx)
	if err != nil {
		bot.sendText(ctx, config, chatID, "读取设备失败："+err.Error(), bot.homeKeyboard())
		return
	}
	rows := make([][]map[string]string, 0, len(configs)+1)
	for _, stored := range configs {
		_, _, present := bot.server.physicalForConfig(stored)
		icon := "⚫"
		if present {
			icon = "🟢"
		}
		token, tokenErr := bot.putPending(telegramPendingAction{
			Kind: "menu_pick", DeviceID: stored.ID, Argument: next,
			ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
		})
		if tokenErr != nil {
			continue
		}
		label := fmt.Sprintf("%s %s", icon, firstNonEmpty(stored.Name, stored.ID))
		if stored.Name != "" && stored.Name != stored.ID {
			label += " · " + stored.ID
		}
		rows = append(rows, []map[string]string{telegramButton(truncateTelegramButton(label), "pick:"+token)})
	}
	rows = append(rows, []map[string]string{telegramButton("🏠 主菜单", "menu:home")})
	if len(configs) == 0 {
		bot.sendMenuText(ctx, config, chatID, "当前没有已配置设备。", telegramKeyboard(rows...))
		return
	}
	title := "请选择要操作的设备"
	if next != "" {
		title += "："
	}
	bot.sendMenuText(ctx, config, chatID, title, telegramKeyboard(rows...))
}

func truncateTelegramButton(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= 48 {
		return value
	}
	return string(runes[:47]) + "…"
}

func (bot *telegramBot) sendDeviceMenu(
	ctx context.Context,
	config telegramRuntimeConfig,
	chatID, adminID int64,
	deviceID string,
) {
	stored, err := bot.server.store.Device(ctx, deviceID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "读取设备失败："+err.Error(), bot.homeKeyboard())
		return
	}
	_, _, present := bot.server.physicalForConfig(stored)
	token, err := bot.putPending(telegramPendingAction{
		Kind: "menu_device", DeviceID: deviceID, ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
	})
	if err != nil {
		bot.sendText(ctx, config, chatID, "创建设备菜单失败："+err.Error(), bot.homeKeyboard())
		return
	}
	status := "⚫ 离线"
	if present {
		status = "🟢 在线"
	}
	bot.sendMenuText(ctx, config, chatID,
		fmt.Sprintf("📡 %s\n设备 ID：%s\n状态：%s\n\n请选择功能：", firstNonEmpty(stored.Name, deviceID), deviceID, status),
		telegramKeyboard(
			[]map[string]string{
				telegramButton("📊 状态", "d:"+token+":status"),
				telegramButton("📲 eSIM", "d:"+token+":esim"),
			},
			[]map[string]string{
				telegramButton("📶 VoWiFi", "d:"+token+":wfc"),
				telegramButton("✉️ 发送短信", "d:"+token+":sms"),
			},
			[]map[string]string{
				telegramButton("📞 通话", "d:"+token+":call"),
				telegramButton("🛠 AT / USSD", "d:"+token+":tools"),
			},
			[]map[string]string{
				telegramButton("⬅️ 设备列表", "menu:devices"),
				telegramButton("🏠 主菜单", "menu:home"),
			},
		),
	)
}

func (bot *telegramBot) dispatchDeviceChoice(
	ctx context.Context,
	config telegramRuntimeConfig,
	chatID, adminID int64,
	deviceID, operation string,
) {
	switch operation {
	case "":
		bot.sendDeviceMenu(ctx, config, chatID, adminID, deviceID)
	case "status":
		bot.sendDeviceStatus(ctx, config, chatID, deviceID)
	case "esim":
		bot.sendESIMProfiles(ctx, config, chatID, adminID, deviceID)
	case "wfc":
		bot.sendVoWiFiMenu(ctx, config, chatID, adminID, deviceID)
	case "sms":
		bot.beginInput(ctx, config, chatID, adminID, deviceID, "sms_phone", "请输入收件人号码：")
	case "call":
		bot.sendCallMenu(ctx, config, chatID, adminID, deviceID)
	case "tools":
		bot.sendToolsMenu(ctx, config, chatID, adminID, deviceID)
	case "at":
		bot.beginInput(ctx, config, chatID, adminID, deviceID, "at", "请直接发送一条 AT 指令，例如 AT+CSQ：")
	case "ussd":
		bot.beginInput(ctx, config, chatID, adminID, deviceID, "ussd", "请直接发送 USSD 代码，例如 *100#：")
	case "answer", "hangup", "calls":
		action := map[string]string{"answer": "answer", "hangup": "hangup", "calls": "status"}[operation]
		bot.executeSimpleCallAction(ctx, config, chatID, adminID, deviceID, action)
	default:
		bot.sendDeviceMenu(ctx, config, chatID, adminID, deviceID)
	}
}

func (bot *telegramBot) sendVoWiFiMenu(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID string) {
	stored, _, _, err := bot.device(deviceID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "VoWiFi 不可用："+err.Error(), bot.homeKeyboard())
		return
	}
	if bot.server.vowifi == nil {
		bot.sendText(ctx, config, chatID, "VoWiFi runtime 不可用。", bot.homeKeyboard())
		return
	}
	state, stateErr := bot.server.vowifi.State(deviceID)
	if stateErr != nil {
		bot.sendText(ctx, config, chatID, "读取 VoWiFi 状态失败："+stateErr.Error(), bot.homeKeyboard())
		return
	}
	token, err := bot.putPending(telegramPendingAction{Kind: "menu_device", DeviceID: deviceID, ChatID: chatID, AdminID: adminID, CreatedAt: time.Now()})
	if err != nil {
		return
	}
	bot.sendMenuText(ctx, config, chatID,
		fmt.Sprintf("📶 %s · VoWiFi\n策略：%s\n阶段：%s\nTunnel：%t · IMS：%t · SMS：%t", deviceID, map[bool]string{true: "已启用", false: "已关闭"}[stored.VoWiFiEnabled], firstNonEmpty(string(state.Phase), "idle"), state.TunnelReady, state.IMSReady, state.SMSReady),
		telegramKeyboard(
			[]map[string]string{
				telegramButton("✅ 开启", "w:"+token+":on"),
				telegramButton("⛔ 关闭", "w:"+token+":off"),
			},
			[]map[string]string{
				telegramButton("🔄 重新连接", "w:"+token+":reconnect"),
				telegramButton("📊 刷新状态", "w:"+token+":status"),
			},
			[]map[string]string{telegramButton("⬅️ 设备功能", "d:"+token+":menu")},
		),
	)
}

func (bot *telegramBot) sendCallMenu(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID string) {
	if _, _, _, err := bot.device(deviceID); err != nil {
		bot.sendText(ctx, config, chatID, "通话功能不可用："+err.Error(), bot.homeKeyboard())
		return
	}
	token, err := bot.putPending(telegramPendingAction{Kind: "menu_device", DeviceID: deviceID, ChatID: chatID, AdminID: adminID, CreatedAt: time.Now()})
	if err != nil {
		return
	}
	bot.sendMenuText(ctx, config, chatID, "📞 "+deviceID+" · 通话\n请选择操作：",
		telegramKeyboard(
			[]map[string]string{telegramButton("📱 拨打电话", "call:"+token+":dial")},
			[]map[string]string{
				telegramButton("✅ 接听", "call:"+token+":answer"),
				telegramButton("🔴 挂断", "call:"+token+":hangup"),
			},
			[]map[string]string{telegramButton("📋 当前通话", "call:"+token+":status")},
			[]map[string]string{telegramButton("⬅️ 设备功能", "d:"+token+":menu")},
		),
	)
}

func (bot *telegramBot) dispatchCallAction(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, operation string) {
	switch operation {
	case "dial":
		bot.beginInput(ctx, config, chatID, adminID, deviceID, "call_number", "请输入要拨打的电话号码：")
	case "answer", "hangup", "status":
		bot.executeSimpleCallAction(ctx, config, chatID, adminID, deviceID, operation)
	default:
		bot.sendCallMenu(ctx, config, chatID, adminID, deviceID)
	}
}

func (bot *telegramBot) sendToolsMenu(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID string) {
	token, err := bot.putPending(telegramPendingAction{Kind: "menu_device", DeviceID: deviceID, ChatID: chatID, AdminID: adminID, CreatedAt: time.Now()})
	if err != nil {
		return
	}
	bot.sendMenuText(ctx, config, chatID, "🛠 "+deviceID+" · 高级工具\n请选择操作：",
		telegramKeyboard(
			[]map[string]string{
				telegramButton("⌨️ AT 指令", "d:"+token+":at"),
				telegramButton("📟 USSD", "d:"+token+":ussd"),
			},
			[]map[string]string{telegramButton("⬅️ 设备功能", "d:"+token+":menu")},
		),
	)
}

func (bot *telegramBot) sendExpiredMenu(ctx context.Context, config telegramRuntimeConfig, chatID int64) {
	bot.sendMenuText(ctx, config, chatID, "该菜单已过期，请重新选择。", bot.homeKeyboard())
}

func (bot *telegramBot) sendHelp(ctx context.Context, config telegramRuntimeConfig, chatID int64) {
	text := strings.Join([]string{
		"Vocat Telegram 控制", "",
		"推荐直接发送 /menu，使用按钮完成设备和功能选择。以下命令仅用于兼容和高级操作：", "",
		"/status [设备ID] — 查看设备、SIM、蜂窝与 VoWiFi 状态",
		"/esim <设备ID> — 只读查看已安装 Profile",
		"/switch <设备ID> <ICCID> — 切换到已安装 Profile（需确认）",
		"/wfc <设备ID> <status|on|off|reconnect> — 管理 WiFi Calling",
		"/sms <设备ID> <号码> <内容> — 发送短信（需确认）",
		"/call <设备ID> <号码> <秒数> — 拨号并在 1–600 秒后自动挂断（需确认）",
		"/calls <设备ID> — 查看模块当前通话",
		"/answer <设备ID> — 按当前通道接听来电",
		"/hangup <设备ID> — 立即挂断",
		"/at <设备ID> <AT指令> — 执行经过安全校验的单行 AT 指令",
		"/ussd <设备ID> <代码> — 发送 USSD 指令",
		"/ussd_reply <会话ID> <内容> — 回复交互式 USSD 菜单",
		"/ussd_cancel <会话ID> — 取消交互式 USSD 会话",
		"",
		"Bot 不提供 eSIM 下载、删除或改名，也不采集或转发通话音频。控制命令只接受设置中的 Admin ID。",
	}, "\n")
	keyboard := telegramKeyboard(
		[]map[string]string{telegramButton("📱 使用操作菜单", "menu:devices")},
		[]map[string]string{telegramButton("🏠 主菜单", "menu:home")},
	)
	bot.sendMenuText(ctx, config, chatID, text, keyboard)
}

func (bot *telegramBot) sendDeviceStatus(ctx context.Context, config telegramRuntimeConfig, chatID int64, onlyID string) {
	configs, err := bot.server.store.ListDevices(ctx)
	if err != nil {
		bot.sendText(ctx, config, chatID, "读取设备失败："+err.Error(), nil)
		return
	}
	var blocks []string
	for _, stored := range configs {
		if onlyID != "" && stored.ID != onlyID {
			continue
		}
		entry, _, present := bot.server.physicalForConfig(stored)
		lines := []string{fmt.Sprintf("📡 %s (%s)", firstNonEmpty(stored.Name, stored.ID), stored.ID)}
		var wfcState *vowifi.State
		if bot.server.vowifi != nil {
			if state, stateErr := bot.server.vowifi.State(stored.ID); stateErr == nil {
				wfcState = &state
			}
		}
		if !present {
			lines = append(lines, "设备：离线")
		} else {
			lines = append(lines, "设备：在线 · "+strings.ToUpper(firstNonEmpty(stored.DeviceBackend, "AT")))
			if snapshot := entry.Snapshot; snapshot != nil {
				customNumber := ""
				associationNumber := ""
				if snapshot.ICCID != "" {
					if policy, policyErr := bot.server.store.CardPolicy(ctx, snapshot.ICCID); policyErr == nil {
						customNumber = policy.CustomPhoneNumber
					}
					if association, associationErr := bot.server.store.PhoneAssociation(ctx, snapshot.ICCID); associationErr == nil {
						associationNumber = association.Number
					}
				}
				lines = append(lines,
					"SIM："+map[bool]string{true: "Ready", false: firstNonEmpty(snapshot.SIMStatus, "未就绪")}[snapshot.SIMReady],
					"IMEI："+firstNonEmpty(snapshot.IMEI, stored.ModemIMEI, "--"),
					"ICCID："+firstNonEmpty(snapshot.ICCID, "--"),
					"IMSI："+firstNonEmpty(snapshot.IMSI, "--"),
					"号码："+resolveTelegramPhoneNumber(customNumber, associationNumber, wfcState, snapshot),
					"原运营商："+telegramSnapshotHomeCarrier(snapshot),
					"当前网络："+telegramCurrentNetwork(snapshot),
					"蜂窝模式："+map[bool]string{true: "飞行模式", false: "开启"}[snapshot.FlightMode],
				)
				if module := telegramModuleLine(snapshot); module != "" {
					lines = append(lines, module)
				}
				if signal := telegramSignalLine(snapshot); signal != "" {
					lines = append(lines, signal)
				}
			}
		}
		if wfcState != nil {
			lines = append(lines,
				fmt.Sprintf("VoWiFi：%s · Tunnel=%t IMS=%t SMS=%t", firstNonEmpty(string(wfcState.Phase), "idle"), wfcState.TunnelReady, wfcState.IMSReady, wfcState.SMSReady),
			)
			if wfcState.LastError != "" {
				lines = append(lines, "最后错误："+wfcState.LastError)
			}
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	if len(blocks) == 0 {
		bot.sendText(ctx, config, chatID, "未找到设备 "+onlyID, nil)
		return
	}
	bot.sendMenuText(ctx, config, chatID, strings.Join(blocks, "\n\n"), bot.homeKeyboard())
}

func resolveTelegramPhoneNumber(customNumber, associationNumber string, state *vowifi.State, snapshot *device.Snapshot) string {
	if usableTelegramPhoneNumber(customNumber) {
		return strings.TrimSpace(customNumber)
	}
	if usableTelegramPhoneNumber(associationNumber) {
		return strings.TrimSpace(associationNumber)
	}
	currentICCID := ""
	if snapshot != nil {
		currentICCID = strings.TrimSpace(snapshot.ICCID)
	}
	if state != nil && usableTelegramPhoneNumber(state.PhoneNumber) {
		stateICCID := strings.TrimSpace(state.ICCID)
		if currentICCID == "" || (stateICCID != "" && strings.EqualFold(currentICCID, stateICCID)) {
			return strings.TrimSpace(state.PhoneNumber)
		}
	}
	if snapshot != nil && usableTelegramPhoneNumber(snapshot.Phone.Number) {
		return strings.TrimSpace(snapshot.Phone.Number)
	}
	return "--"
}

func usableTelegramPhoneNumber(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+") {
		value = value[1:]
	}
	var digits []byte
	for index := 0; index < len(value); index++ {
		switch character := value[index]; {
		case character >= '0' && character <= '9':
			digits = append(digits, character)
		case character == ' ' || character == '-' || character == '(' || character == ')':
			continue
		default:
			return false
		}
	}
	if len(digits) < 5 || len(digits) > 20 {
		return false
	}
	allSame := true
	for _, digit := range digits[1:] {
		if digit != digits[0] {
			allSame = false
			break
		}
	}
	return !allSame
}

func telegramHomeCarrier(imsi string, spn ...string) string {
	identity := device.CarrierIdentity{IMSI: imsi}
	if len(spn) > 0 {
		identity.SPN = spn[0]
	}
	return telegramResolvedHomeCarrier(identity)
}

func telegramSnapshotHomeCarrier(snapshot *device.Snapshot) string {
	if snapshot == nil {
		return "--"
	}
	return telegramResolvedHomeCarrier(device.CarrierIdentity{
		IMSI: snapshot.IMSI, ICCID: snapshot.ICCID, SPN: snapshot.SPN,
		GID1: snapshot.GID1, GID2: snapshot.GID2, MNCLength: snapshot.MNCLength,
	})
}

func telegramResolvedHomeCarrier(identity device.CarrierIdentity) string {
	plmn, name, country, ok := device.CarrierForSIM(identity)
	if !ok {
		if strings.TrimSpace(identity.SPN) != "" {
			return strings.TrimSpace(identity.SPN)
		}
		return "--"
	}
	return strings.TrimSpace(strings.Join([]string{telegramCountryFlag(country), name, "(" + plmn + ")"}, " "))
}

func telegramCurrentNetwork(snapshot *device.Snapshot) string {
	if snapshot == nil {
		return "--"
	}
	if snapshot.FlightMode || snapshot.RadioOff {
		return "--（飞行模式）"
	}
	operatorName := strings.TrimSpace(snapshot.OperatorName)
	country := ""
	if databaseName, databaseCountry, ok := device.CarrierForPLMN(snapshot.OperatorCode); ok {
		if operatorName == "" {
			operatorName = databaseName
		}
		country = databaseCountry
	}
	operator := firstNonEmpty(operatorName, strings.TrimSpace(snapshot.OperatorCode), "--")
	if flag := telegramCountryFlag(country); flag != "" && operator != "--" {
		operator = flag + " " + operator
	}
	parts := []string{operator, telegramRegistrationText(snapshot.RegistrationStatus)}
	if radio := strings.TrimSpace(strings.Join([]string{snapshot.AccessTech, snapshot.Band}, " ")); radio != "" {
		parts = append(parts, radio)
	}
	return strings.Join(parts, " · ")
}

func telegramRegistrationText(status int) string {
	switch status {
	case 1:
		return "已驻网"
	case 5:
		return "已驻网（漫游）"
	case 2:
		return "搜索中"
	case 3:
		return "驻网被拒绝"
	default:
		return "未驻网"
	}
}

func telegramCountryFlag(country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
		return ""
	}
	return string([]rune{
		rune(0x1F1E6) + rune(country[0]-'A'),
		rune(0x1F1E6) + rune(country[1]-'A'),
	})
}

func telegramModuleLine(snapshot *device.Snapshot) string {
	if snapshot == nil {
		return ""
	}
	description := strings.TrimSpace(strings.Join([]string{snapshot.Manufacturer, snapshot.Model}, " "))
	if description == "" && strings.TrimSpace(snapshot.Firmware) == "" {
		return ""
	}
	parts := []string{firstNonEmpty(description, "--")}
	if strings.TrimSpace(snapshot.Firmware) != "" {
		parts = append(parts, snapshot.Firmware)
	}
	return "模块：" + strings.Join(parts, " · ")
}

func telegramSignalLine(snapshot *device.Snapshot) string {
	if snapshot == nil || snapshot.FlightMode || snapshot.RadioOff {
		return ""
	}
	metrics := make([]string, 0, 4)
	if snapshot.RSSIDBm != nil {
		metrics = append(metrics, fmt.Sprintf("%d dBm", *snapshot.RSSIDBm))
	}
	if snapshot.RSRP != nil {
		metrics = append(metrics, fmt.Sprintf("RSRP %d", *snapshot.RSRP))
	}
	if snapshot.RSRQ != nil {
		metrics = append(metrics, fmt.Sprintf("RSRQ %d", *snapshot.RSRQ))
	}
	if snapshot.SINR != nil {
		metrics = append(metrics, fmt.Sprintf("SINR %d", *snapshot.SINR))
	}
	if len(metrics) == 0 {
		return ""
	}
	return "信号：" + strings.Join(metrics, " · ")
}

func (bot *telegramBot) sendESIMProfiles(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID string) {
	if deviceID == "" {
		bot.sendText(ctx, config, chatID, "用法：/esim <设备ID>", nil)
		return
	}
	_, _, physicalID, err := bot.device(deviceID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "读取 eSIM 失败："+err.Error(), nil)
		return
	}
	readContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	inventory, err := bot.server.devices.ESIMInventory(readContext, physicalID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "读取 eSIM 失败："+err.Error(), nil)
		return
	}
	if len(inventory) == 0 {
		bot.sendText(ctx, config, chatID, "该设备没有可用的 eUICC/Profile。", nil)
		return
	}
	lines := []string{"📲 " + deviceID + " 已安装 Profile（只读）"}
	rows := make([][]map[string]string, 0)
	for index, group := range inventory {
		lines = append(lines, fmt.Sprintf("\neUICC #%d · …%s", index+1, tailDigits(group.Info.EID, 4)))
		for _, profile := range group.Info.Profiles {
			state := "Disabled"
			if profile.State == 1 {
				state = "Enabled"
			}
			name := firstNonEmpty(profile.Nickname, profile.Name, profile.ServiceProvider, "未命名")
			lines = append(lines, fmt.Sprintf("• %s · %s\n  %s", name, state, profile.ICCID))
			if profile.State != 1 {
				token, tokenErr := bot.putPending(telegramPendingAction{
					Kind: "menu_esim_profile", DeviceID: deviceID, TargetICCID: profile.ICCID,
					TargetAID: group.Info.AID, ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
				})
				if tokenErr == nil {
					label := "切换到 " + name + " · …" + tailDigits(profile.ICCID, 4)
					rows = append(rows, []map[string]string{telegramButton(truncateTelegramButton(label), "es:"+token+":select")})
				}
			}
		}
	}
	rows = append(rows, []map[string]string{
		telegramButton("⬅️ 设备列表", "menu:devices"),
		telegramButton("🏠 主菜单", "menu:home"),
	})
	bot.sendMenuText(ctx, config, chatID, strings.Join(lines, "\n"), telegramKeyboard(rows...))
}

func (bot *telegramBot) confirmESIMSwitch(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, iccid string) {
	_, _, physicalID, err := bot.device(deviceID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "无法切换："+err.Error(), nil)
		return
	}
	readContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	inventory, err := bot.server.devices.ESIMInventory(readContext, physicalID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "无法读取 Profile："+err.Error(), nil)
		return
	}
	var target *device.EsimProfile
	var targetAID string
	for groupIndex := range inventory {
		for profileIndex := range inventory[groupIndex].Info.Profiles {
			profile := &inventory[groupIndex].Info.Profiles[profileIndex]
			if profile.ICCID == iccid {
				target = profile
				targetAID = inventory[groupIndex].Info.AID
				break
			}
		}
	}
	if target == nil {
		bot.sendText(ctx, config, chatID, "目标 ICCID 不在该设备已安装 Profile 中。", nil)
		return
	}
	if target.State == 1 {
		bot.sendText(ctx, config, chatID, "目标 Profile 已经处于 Enabled。", nil)
		return
	}
	action := telegramPendingAction{
		Kind: "esim_switch", DeviceID: deviceID, ChatID: chatID, AdminID: adminID,
		CreatedAt: time.Now(), TargetAID: targetAID, TargetICCID: target.ICCID,
	}
	name := firstNonEmpty(target.Nickname, target.Name, target.ServiceProvider, "未命名")
	bot.askConfirmation(ctx, config, action, fmt.Sprintf("确认将设备 %s 切换到：\n%s\nICCID %s？\n\nBot 只会执行 EnableProfile，不会下载或删除 Profile。", deviceID, name, target.ICCID))
}

func (bot *telegramBot) confirmSMS(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, phone, text string) {
	phone = strings.TrimSpace(phone)
	text = strings.TrimSpace(text)
	if _, _, _, err := bot.device(deviceID); err != nil {
		bot.sendText(ctx, config, chatID, "无法发送："+err.Error(), nil)
		return
	}
	if blocked, reason := blockedSMSDestination(phone); blocked {
		bot.sendText(ctx, config, chatID, "无法发送："+reason, nil)
		return
	}
	if text == "" {
		bot.sendText(ctx, config, chatID, "短信内容不能为空。", nil)
		return
	}
	action := telegramPendingAction{
		Kind: "sms", DeviceID: deviceID, Argument: phone, Text: text,
		ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
	}
	bot.askConfirmation(ctx, config, action, fmt.Sprintf("确认通过设备 %s 发送短信？\n收件人：%s\n内容：%s", deviceID, phone, truncateTelegramText(text, 800)))
}

func (bot *telegramBot) confirmCall(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, number string, duration time.Duration) {
	if !validTelegramDialNumber(number) {
		bot.sendText(ctx, config, chatID, "拨号号码无效，只允许一个可选的前导 + 和 3–20 位数字。", nil)
		return
	}
	stored, entry, _, err := bot.device(deviceID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "无法拨号："+err.Error(), nil)
		return
	}
	transport, _, err := bot.telegramCallTransport(stored, entry)
	if err != nil {
		bot.sendText(ctx, config, chatID, "无法拨号："+err.Error(), nil)
		return
	}
	action := telegramPendingAction{
		Kind: "call", DeviceID: deviceID, Argument: number, Duration: duration,
		ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
	}
	bot.askConfirmation(ctx, config, action, fmt.Sprintf("确认通过设备 %s 拨打 %s？\n通道：%s\n持续：%d 秒，然后自动挂断。\n不会采集或处理通话音频。", deviceID, number, telegramCallTransportLabel(transport), int(duration/time.Second)))
}

func (bot *telegramBot) askConfirmation(ctx context.Context, config telegramRuntimeConfig, action telegramPendingAction, text string) {
	token, err := bot.putPending(action)
	if err != nil {
		bot.sendText(ctx, config, action.ChatID, "创建确认失败："+err.Error(), nil)
		return
	}
	keyboard := map[string]any{"inline_keyboard": [][]map[string]string{{
		{"text": "✅ 确认", "callback_data": "confirm:" + token},
		{"text": "❌ 取消", "callback_data": "cancel:" + token},
	}}}
	bot.sendText(ctx, config, action.ChatID, text, keyboard)
}

func telegramInputKey(chatID, adminID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(adminID, 10)
}

func (bot *telegramBot) beginInput(
	ctx context.Context,
	config telegramRuntimeConfig,
	chatID, adminID int64,
	deviceID, kind, prompt string,
) {
	if _, _, _, err := bot.device(deviceID); err != nil {
		bot.sendText(ctx, config, chatID, "设备不可用："+err.Error(), bot.homeKeyboard())
		return
	}
	bot.setInput(telegramInputState{
		Kind: kind, DeviceID: deviceID, ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
	})
	bot.sendText(ctx, config, chatID, prompt+"\n\n设备："+deviceID,
		telegramKeyboard([]map[string]string{telegramButton("❌ 取消", "input:cancel")}))
}

func (bot *telegramBot) setInput(state telegramInputState) {
	bot.inputMu.Lock()
	defer bot.inputMu.Unlock()
	if bot.inputs == nil {
		bot.inputs = make(map[string]telegramInputState)
	}
	state.CreatedAt = time.Now()
	bot.inputs[telegramInputKey(state.ChatID, state.AdminID)] = state
}

func (bot *telegramBot) clearInput(chatID, adminID int64) {
	bot.inputMu.Lock()
	defer bot.inputMu.Unlock()
	delete(bot.inputs, telegramInputKey(chatID, adminID))
}

func (bot *telegramBot) input(chatID, adminID int64) (telegramInputState, bool) {
	bot.inputMu.Lock()
	defer bot.inputMu.Unlock()
	state, ok := bot.inputs[telegramInputKey(chatID, adminID)]
	if ok && time.Since(state.CreatedAt) > telegramInputTTL {
		delete(bot.inputs, telegramInputKey(chatID, adminID))
		return telegramInputState{}, false
	}
	return state, ok
}

func (bot *telegramBot) handleInputMessage(ctx context.Context, config telegramRuntimeConfig, message *telegramMessage) {
	state, ok := bot.input(message.Chat.ID, message.From.ID)
	if !ok {
		bot.sendMainMenu(ctx, config, message.Chat.ID)
		return
	}
	value := strings.TrimSpace(message.Text)
	if value == "" {
		return
	}
	switch state.Kind {
	case "sms_phone":
		if blocked, reason := blockedSMSDestination(value); blocked {
			bot.sendText(ctx, config, message.Chat.ID, "号码不可用："+reason+"\n请重新输入号码。", telegramKeyboard([]map[string]string{telegramButton("❌ 取消", "input:cancel")}))
			return
		}
		state.Kind = "sms_text"
		state.Argument = value
		bot.setInput(state)
		bot.sendText(ctx, config, message.Chat.ID, "请输入短信内容：\n\n收件人："+value,
			telegramKeyboard([]map[string]string{telegramButton("❌ 取消", "input:cancel")}))
	case "sms_text":
		bot.clearInput(message.Chat.ID, message.From.ID)
		bot.confirmSMS(ctx, config, message.Chat.ID, message.From.ID, state.DeviceID, state.Argument, value)
	case "call_number":
		if !validTelegramDialNumber(value) {
			bot.sendText(ctx, config, message.Chat.ID, "号码无效，请输入 3–20 位数字，可带前导 +。",
				telegramKeyboard([]map[string]string{telegramButton("❌ 取消", "input:cancel")}))
			return
		}
		bot.clearInput(message.Chat.ID, message.From.ID)
		token, err := bot.putPending(telegramPendingAction{
			Kind: "menu_call_duration", DeviceID: state.DeviceID, Argument: value,
			ChatID: message.Chat.ID, AdminID: message.From.ID, CreatedAt: time.Now(),
		})
		if err != nil {
			bot.sendText(ctx, config, message.Chat.ID, "创建拨号操作失败："+err.Error(), bot.homeKeyboard())
			return
		}
		bot.sendText(ctx, config, message.Chat.ID, "请选择自动挂断时间：\n\n号码："+value,
			telegramKeyboard(
				[]map[string]string{
					telegramButton("10 秒", "dur:"+token+":10"),
					telegramButton("30 秒", "dur:"+token+":30"),
				},
				[]map[string]string{
					telegramButton("60 秒", "dur:"+token+":60"),
					telegramButton("120 秒", "dur:"+token+":120"),
				},
				[]map[string]string{telegramButton("❌ 取消", "input:cancel")},
			))
	case "at":
		bot.clearInput(message.Chat.ID, message.From.ID)
		bot.handleATCommand(ctx, config, message.Chat.ID, message.From.ID, state.DeviceID, value)
	case "ussd":
		bot.clearInput(message.Chat.ID, message.From.ID)
		bot.handleUSSDCommand(ctx, config, message.Chat.ID, message.From.ID, state.DeviceID, value)
	case "ussd_reply":
		bot.clearInput(message.Chat.ID, message.From.ID)
		bot.handleUSSDReply(ctx, config, message.Chat.ID, message.From.ID, state.Argument, value)
	default:
		bot.clearInput(message.Chat.ID, message.From.ID)
		bot.sendMainMenu(ctx, config, message.Chat.ID)
	}
}

func (bot *telegramBot) putPending(action telegramPendingAction) (string, error) {
	raw := make([]byte, 8)
	if _, err := cryptorand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	bot.pendingMu.Lock()
	defer bot.pendingMu.Unlock()
	now := time.Now()
	if bot.pending == nil {
		bot.pending = make(map[string]telegramPendingAction)
	}
	for key, value := range bot.pending {
		if now.Sub(value.CreatedAt) > telegramPendingLifetime(value.Kind) {
			delete(bot.pending, key)
		}
	}
	bot.pending[token] = action
	return token, nil
}

func telegramPendingLifetime(kind string) time.Duration {
	if strings.HasPrefix(kind, "menu_") {
		return telegramMenuTTL
	}
	return telegramConfirmationTTL
}

func (bot *telegramBot) getPending(token string, chatID, adminID int64) (telegramPendingAction, bool) {
	bot.pendingMu.Lock()
	defer bot.pendingMu.Unlock()
	action, ok := bot.pending[token]
	if !ok || action.ChatID != chatID || action.AdminID != adminID || time.Since(action.CreatedAt) > telegramPendingLifetime(action.Kind) {
		if ok {
			delete(bot.pending, token)
		}
		return telegramPendingAction{}, false
	}
	return action, true
}

func (bot *telegramBot) takePending(token string, chatID, adminID int64) (telegramPendingAction, bool) {
	bot.pendingMu.Lock()
	defer bot.pendingMu.Unlock()
	action, ok := bot.pending[token]
	if ok {
		delete(bot.pending, token)
	}
	if !ok || action.ChatID != chatID || action.AdminID != adminID || time.Since(action.CreatedAt) > telegramPendingLifetime(action.Kind) {
		return telegramPendingAction{}, false
	}
	return action, true
}

func (bot *telegramBot) executeSMS(ctx context.Context, action telegramPendingAction) (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"device_id": action.DeviceID,
		"phone":     action.Argument,
		"message":   action.Text,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/sms/send", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	bot.server.handleSMSSend(recorder, request)
	var response struct {
		Data  map[string]any `json:"data"`
		Error *apiError      `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return "", fmt.Errorf("decode SMS result: %w", err)
	}
	if recorder.Code >= http.StatusBadRequest || response.Error != nil {
		if response.Error != nil {
			return "", errors.New(response.Error.Message)
		}
		return "", fmt.Errorf("SMS submission returned HTTP %d", recorder.Code)
	}
	return fmt.Sprintf("短信已提交。\n通道：%v\n结果：%v\n送达确认：%v", response.Data["transport"], response.Data["outcome"], response.Data["delivery_confirmed"]), nil
}

func (bot *telegramBot) executeESIMSwitch(ctx context.Context, action telegramPendingAction) (string, error) {
	stored, _, physicalID, err := bot.device(action.DeviceID)
	if err != nil {
		return "", err
	}
	operationContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	desiredData := stored.NetworkEnabled && !stored.VoWiFiEnabled
	dataRuntime := bot.server.cellularDataRuntime()
	bot.server.clearPublicIP(stored.ID)
	dataRuntime.invalidateWithMaintenancePhase(stored.ID, desiredData, "recovering", "", "profile_switching")
	switchCompleted := false
	defer func() {
		if !switchCompleted {
			dataRuntime.invalidate(stored.ID, desiredData, "failed", "Telegram eSIM profile switch did not complete")
		}
	}()
	if err := bot.server.devices.ESIMSwitchProfile(operationContext, physicalID, action.TargetICCID, action.TargetAID); err != nil {
		return "", err
	}
	if desiredData {
		snapshot, refreshErr := bot.server.devices.Refresh(operationContext, physicalID)
		if refreshErr != nil {
			return "", fmt.Errorf("refresh modem after profile switch: %w", refreshErr)
		}
		request := bot.server.cellularNetworkRequest(operationContext, stored, &snapshot)
		request.Enabled = true
		dataRuntime.requestWithIdentity(stored.ID, physicalID, request, strings.TrimSpace(snapshot.ICCID))
	} else {
		dataRuntime.invalidate(stored.ID, false, "disabled", "")
	}
	switchCompleted = true
	return "Profile 切换成功，模块恢复后已校验当前 ICCID：" + action.TargetICCID, nil
}

func (bot *telegramBot) executeTimedCall(ctx context.Context, config telegramRuntimeConfig, action telegramPendingAction) (string, error) {
	stored, entry, physicalID, err := bot.device(action.DeviceID)
	if err != nil {
		return "", err
	}
	transport, controller, err := bot.telegramCallTransport(stored, entry)
	if err != nil {
		return "", err
	}
	if !bot.beginTelegramDial(action.DeviceID) {
		return "", errors.New("该设备已有一个由 Telegram 发起的限时拨号任务")
	}
	defer bot.endTelegramDial(action.DeviceID)

	if transport == "vowifi" {
		return bot.executeTimedVoWiFiCall(ctx, config, action, controller)
	}
	return bot.executeTimedCellularCall(ctx, config, action, physicalID)
}

func (bot *telegramBot) executeSimpleCallAction(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, action string) {
	if deviceID == "" {
		bot.sendText(ctx, config, chatID, fmt.Sprintf("用法：/%s <设备ID>", map[string]string{"status": "calls", "answer": "answer", "hangup": "hangup"}[action]), nil)
		return
	}
	stored, entry, physicalID, err := bot.device(deviceID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "通话操作失败："+err.Error(), nil)
		return
	}
	transport, controller, err := bot.telegramCallTransport(stored, entry)
	result := ""
	if err == nil {
		if transport == "vowifi" {
			result, err = bot.executeSimpleVoWiFiCallAction(ctx, deviceID, action, controller)
		} else {
			result, err = bot.executeSimpleCellularCallAction(ctx, physicalID, action)
		}
	}
	outcome := "success"
	if err != nil {
		outcome = "failure"
		bot.sendText(ctx, config, chatID, "通话操作失败："+err.Error(), bot.homeKeyboard())
	} else {
		bot.sendText(ctx, config, chatID, result, bot.homeKeyboard())
	}
	bot.server.recordAudit(ctx, fmt.Sprintf("telegram:%d", adminID), "telegram.call."+action, "device", deviceID, outcome, firstNonEmpty(transport, "unknown"))
}

func (bot *telegramBot) telegramCallTransport(stored store.Device, entry device.Device) (string, VoWiFiCallController, error) {
	if stored.VoWiFiEnabled {
		if bot.server.vowifi == nil {
			return "", nil, errors.New("设备卡片处于 VoWiFi 模式，但 VoWiFi 运行时不可用")
		}
		state, err := bot.server.vowifi.State(stored.ID)
		if err != nil {
			return "", nil, fmt.Errorf("读取 VoWiFi 通话状态失败: %w", err)
		}
		if !state.IMSReady {
			detail := firstNonEmpty(state.LastError, state.LastReason, "IMS 尚未注册")
			return "", nil, fmt.Errorf("设备卡片处于 VoWiFi 模式，但 IMS 未就绪（%s）：%s", firstNonEmpty(string(state.Phase), "idle"), detail)
		}
		controller, ok := bot.server.vowifi.(VoWiFiCallController)
		if !ok {
			return "", nil, errors.New("当前 VoWiFi 运行时不支持 IMS 通话信令")
		}
		return "vowifi", controller, nil
	}
	if entry.Snapshot != nil && entry.Snapshot.FlightMode {
		return "", nil, errors.New("设备处于飞行模式且 VoWiFi 未启用，无法通过基站拨号")
	}
	return "cellular", nil, nil
}

func telegramCallTransportLabel(transport string) string {
	if transport == "vowifi" {
		return "VoWiFi IMS"
	}
	return "基站直连"
}

func (bot *telegramBot) beginTelegramDial(deviceID string) bool {
	bot.callMu.Lock()
	defer bot.callMu.Unlock()
	if bot.activeDials == nil {
		bot.activeDials = make(map[string]struct{})
	}
	if _, exists := bot.activeDials[deviceID]; exists {
		return false
	}
	bot.activeDials[deviceID] = struct{}{}
	return true
}

func (bot *telegramBot) endTelegramDial(deviceID string) {
	bot.callMu.Lock()
	delete(bot.activeDials, deviceID)
	bot.callMu.Unlock()
}

func (bot *telegramBot) executeTimedVoWiFiCall(
	ctx context.Context,
	config telegramRuntimeConfig,
	action telegramPendingAction,
	controller VoWiFiCallController,
) (string, error) {
	dialContext, cancelDial := context.WithTimeout(ctx, 20*time.Second)
	call, err := controller.DialCall(dialContext, action.DeviceID, action.Argument)
	cancelDial()
	if err != nil {
		return "", fmt.Errorf("VoWiFi IMS 拨号失败: %w", err)
	}
	hangupAt := time.Now().Add(action.Duration)
	_ = bot.sendText(ctx, config, action.ChatID, fmt.Sprintf(
		"📞 已通过 VoWiFi IMS 提交 %s，Call-ID：%s\n将在 %d 秒后自动挂断，并持续检查 SIP 结果。",
		action.Argument, call.ID, int(action.Duration/time.Second),
	), nil)

	timer := time.NewTimer(time.Until(hangupAt))
	defer timer.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	seenNetwork := false
	lastState := call.State
	for {
		select {
		case <-ctx.Done():
			bot.bestEffortVoWiFiHangup(controller, action.DeviceID, call.ID)
			return "", ctx.Err()
		case <-timer.C:
			current, found, listErr := telegramFindVoWiFiCall(controller, action.DeviceID, call.ID)
			if listErr != nil {
				bot.bestEffortVoWiFiHangup(controller, action.DeviceID, call.ID)
				return "", fmt.Errorf("读取 IMS 通话状态失败: %w", listErr)
			}
			if found && current.State == "failed" {
				return telegramVoWiFiCallOutcome(action.Argument, current)
			}
			if found {
				lastState = current.State
				if current.State == "ringing" || current.State == "active" {
					seenNetwork = true
				}
			}
			if found && current.State != "ended" {
				hangContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				err = controller.HangupCall(hangContext, action.DeviceID, call.ID)
				cancel()
				if err != nil {
					return "", fmt.Errorf("VoWiFi 拨号已执行，但自动挂断失败: %w", err)
				}
			}
			if seenNetwork {
				return fmt.Sprintf("VoWiFi 拨号完成：%s；已确认 %s，%d 秒后自动挂断。", action.Argument, telegramIMSStateLabel(lastState), int(action.Duration/time.Second)), nil
			}
			return fmt.Sprintf("VoWiFi INVITE 已发送并在 %d 秒后取消；运营商未返回振铃或接通确认。", int(action.Duration/time.Second)), nil
		case <-ticker.C:
			current, found, listErr := telegramFindVoWiFiCall(controller, action.DeviceID, call.ID)
			if listErr != nil {
				bot.bestEffortVoWiFiHangup(controller, action.DeviceID, call.ID)
				return "", fmt.Errorf("读取 IMS 通话状态失败: %w", listErr)
			}
			if !found {
				bot.bestEffortVoWiFiHangup(controller, action.DeviceID, call.ID)
				return "", errors.New("IMS 通话记录在拨号过程中消失")
			}
			lastState = current.State
			switch current.State {
			case "ringing", "active":
				seenNetwork = true
			case "failed":
				return telegramVoWiFiCallOutcome(action.Argument, current)
			case "ended":
				if seenNetwork {
					return fmt.Sprintf("VoWiFi 通话 %s 已由网络或对端提前结束（最后状态：%s）。", action.Argument, telegramIMSStateLabel(lastState)), nil
				}
				return fmt.Sprintf("📴 VoWiFi 呼叫 %s 在振铃前结束%s。", action.Argument, telegramSIPDiagnostic(current)), nil
			}
		}
	}
}

func telegramFindVoWiFiCall(controller VoWiFiCallController, deviceID, callID string) (vowifi.Call, bool, error) {
	calls, err := controller.Calls(deviceID)
	if err != nil {
		return vowifi.Call{}, false, err
	}
	for _, call := range calls {
		if call.ID == callID {
			return call, true, nil
		}
	}
	return vowifi.Call{}, false, nil
}

func telegramVoWiFiCallFailure(call vowifi.Call) error {
	return fmt.Errorf("VoWiFi 呼叫被拒绝或失败%s", telegramSIPDiagnostic(call))
}

func telegramVoWiFiCallOutcome(number string, call vowifi.Call) (string, error) {
	diagnostic := telegramSIPDiagnostic(call)
	switch call.SIPCode {
	case 408:
		return fmt.Sprintf("📵 VoWiFi 呼叫 %s 等待响应超时%s，未接通。", number, diagnostic), nil
	case 480:
		return fmt.Sprintf("📵 VoWiFi 呼叫 %s 暂时无人接听或对端不可用%s。", number, diagnostic), nil
	case 486, 600:
		return fmt.Sprintf("📵 VoWiFi 呼叫 %s 对方忙线%s。", number, diagnostic), nil
	case 487:
		return fmt.Sprintf("📴 VoWiFi 呼叫 %s 已在接通前取消或终止%s；这不是 IMS 注册失败。", number, diagnostic), nil
	case 603:
		return fmt.Sprintf("📵 VoWiFi 呼叫 %s 被对端拒接%s。", number, diagnostic), nil
	default:
		return "", telegramVoWiFiCallFailure(call)
	}
}

func telegramSIPDiagnostic(call vowifi.Call) string {
	parts := make([]string, 0, 2)
	if call.SIPCode != 0 {
		parts = append(parts, fmt.Sprintf("SIP %d", call.SIPCode))
	}
	if reason := strings.TrimSpace(call.Reason); reason != "" {
		parts = append(parts, reason)
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, " · ") + "）"
}

func telegramIMSStateLabel(state string) string {
	switch state {
	case "dialing":
		return "正在拨号"
	case "ringing":
		return "对端振铃"
	case "active":
		return "已接通"
	case "ended":
		return "已结束"
	case "failed":
		return "失败"
	default:
		return firstNonEmpty(state, "未知状态")
	}
}

func (bot *telegramBot) bestEffortVoWiFiHangup(controller VoWiFiCallController, deviceID, callID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = controller.HangupCall(ctx, deviceID, callID)
}

func (bot *telegramBot) executeTimedCellularCall(
	ctx context.Context,
	config telegramRuntimeConfig,
	action telegramPendingAction,
	physicalID string,
) (string, error) {
	dialContext, cancelDial := context.WithTimeout(ctx, 20*time.Second)
	response, err := bot.server.devices.ExecuteAT(dialContext, physicalID, "ATD"+action.Argument+";")
	cancelDial()
	if err != nil {
		return "", fmt.Errorf("基站拨号失败: %w", err)
	}
	if !response.OK() {
		return "", fmt.Errorf("基站未接受拨号: %s", formatTelegramAT(response))
	}
	hangupAt := time.Now().Add(action.Duration)
	_ = bot.sendText(ctx, config, action.ChatID, fmt.Sprintf(
		"📞 已通过基站提交 %s，将在 %d 秒后自动挂断，并持续检查 CLCC 状态。",
		action.Argument, int(action.Duration/time.Second),
	), nil)

	timer := time.NewTimer(time.Until(hangupAt))
	defer timer.Stop()
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	startedAt := time.Now()
	seenCall := false
	confirmed := false
	lastState := -1
	for {
		calls, queryErr := bot.telegramCellularCalls(ctx, physicalID)
		if queryErr != nil {
			if final, ok := telegramCallFinalError(queryErr); ok {
				if confirmed && final == "NO CARRIER" {
					return fmt.Sprintf("基站通话 %s 已由网络或对端提前结束。", action.Argument), nil
				}
				return "", fmt.Errorf("基站呼叫失败: %s", telegramCallFinalLabel(final))
			}
			bot.bestEffortCellularHangup(physicalID)
			return "", fmt.Errorf("查询基站通话状态失败: %w", queryErr)
		}
		current, found := telegramFindOutgoingCellularCall(calls, action.Argument)
		if found {
			seenCall = true
			lastState = telegramCellularCallInt(current, "state")
			if lastState == 0 || lastState == 1 || lastState == 3 {
				confirmed = true
			}
		} else if seenCall {
			if confirmed {
				return fmt.Sprintf("基站通话 %s 已由网络或对端提前结束（最后状态：%s）。", action.Argument, telegramCLCCStateLabel(lastState)), nil
			}
			return "", fmt.Errorf("基站呼叫在振铃前结束（最后状态：%s）", telegramCLCCStateLabel(lastState))
		} else if time.Since(startedAt) >= 5*time.Second {
			bot.bestEffortCellularHangup(physicalID)
			return "", errors.New("模块接受了 ATD，但 5 秒内没有建立任何 CLCC 呼叫记录")
		}

		select {
		case <-ctx.Done():
			bot.bestEffortCellularHangup(physicalID)
			return "", ctx.Err()
		case <-timer.C:
			if err := bot.hangupCellularCall(physicalID); err != nil {
				return "", fmt.Errorf("基站拨号已执行，但自动挂断失败: %w", err)
			}
			if !seenCall {
				return fmt.Sprintf("基站已接受拨号并在 %d 秒后自动挂断；持续时间过短，未取得 CLCC 状态。", int(action.Duration/time.Second)), nil
			}
			return fmt.Sprintf("基站拨号完成：%s；模块确认%s，%d 秒后自动挂断。", action.Argument, telegramCLCCStateLabel(lastState), int(action.Duration/time.Second)), nil
		case <-ticker.C:
		}
	}
}

func (bot *telegramBot) telegramCellularCalls(ctx context.Context, physicalID string) ([]map[string]any, error) {
	queryContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	response, err := bot.server.devices.ExecuteAT(queryContext, physicalID, "AT+CLCC")
	if err != nil {
		return nil, err
	}
	return parseCLCC(response), nil
}

func telegramFindOutgoingCellularCall(calls []map[string]any, number string) (map[string]any, bool) {
	cleanNumber := strings.TrimPrefix(strings.TrimSpace(number), "+")
	var fallback map[string]any
	for _, call := range calls {
		if telegramCellularCallInt(call, "direction") != 0 {
			continue
		}
		if fallback == nil {
			fallback = call
		}
		candidate := strings.TrimPrefix(strings.TrimSpace(fmt.Sprint(call["number"])), "+")
		if candidate == "<nil>" || candidate == "" || candidate == cleanNumber {
			return call, true
		}
	}
	return fallback, fallback != nil
}

func telegramCellularCallInt(call map[string]any, key string) int {
	value, _ := call[key].(int)
	return value
}

func telegramCLCCStateLabel(state int) string {
	switch state {
	case 0:
		return "已接通"
	case 1:
		return "保持中"
	case 2:
		return "正在拨号"
	case 3:
		return "对端振铃"
	case 4:
		return "来电振铃"
	case 5:
		return "来电等待"
	default:
		return fmt.Sprintf("未知状态(%d)", state)
	}
}

func telegramCallFinalError(err error) (string, bool) {
	var commandErr *modem.CommandError
	if !errors.As(err, &commandErr) {
		return "", false
	}
	final := strings.ToUpper(strings.TrimSpace(commandErr.Final))
	switch final {
	case "NO CARRIER", "BUSY", "NO ANSWER":
		return final, true
	default:
		return "", false
	}
}

func telegramCallFinalLabel(final string) string {
	switch final {
	case "BUSY":
		return "对方忙线（BUSY）"
	case "NO ANSWER":
		return "无人接听（NO ANSWER）"
	case "NO CARRIER":
		return "网络或对端已结束呼叫（NO CARRIER）"
	default:
		return final
	}
}

func (bot *telegramBot) hangupCellularCall(physicalID string) error {
	hangContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	response, err := bot.server.devices.ExecuteAT(hangContext, physicalID, "ATH")
	if err != nil {
		if final, ok := telegramCallFinalError(err); ok && final == "NO CARRIER" {
			return nil
		}
		return err
	}
	if !response.OK() {
		return fmt.Errorf("模块未确认 ATH: %s", formatTelegramAT(response))
	}
	return nil
}

func (bot *telegramBot) bestEffortCellularHangup(physicalID string) {
	_ = bot.hangupCellularCall(physicalID)
}

func (bot *telegramBot) executeSimpleVoWiFiCallAction(ctx context.Context, deviceID, action string, controller VoWiFiCallController) (string, error) {
	calls, err := controller.Calls(deviceID)
	if err != nil {
		return "", err
	}
	operationContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch action {
	case "status":
		return formatTelegramIMSCalls(calls), nil
	case "answer":
		callID, err := resolveVoWiFiCallID(controller, deviceID, "", "ringing")
		if err != nil {
			return "", err
		}
		call, err := controller.AnswerCall(operationContext, deviceID, callID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("📞 已通过 VoWiFi IMS 接听 %s（%s）。", firstNonEmpty(call.Number, "未知号码"), call.ID), nil
	case "hangup":
		count := 0
		var failures []string
		for _, call := range calls {
			if call.State == "ended" || call.State == "failed" {
				continue
			}
			if err := controller.HangupCall(operationContext, deviceID, call.ID); err != nil {
				failures = append(failures, err.Error())
				continue
			}
			count++
		}
		if len(failures) > 0 {
			return "", errors.New(strings.Join(failures, "; "))
		}
		if count == 0 {
			return "", errors.New("当前没有可挂断的 VoWiFi 通话")
		}
		return fmt.Sprintf("已通过 VoWiFi IMS 挂断 %d 路通话。", count), nil
	default:
		return "", errors.New("未知通话操作")
	}
}

func formatTelegramIMSCalls(calls []vowifi.Call) string {
	if len(calls) == 0 {
		return "VoWiFi IMS：当前没有通话。"
	}
	lines := []string{"VoWiFi IMS 通话："}
	for _, call := range calls {
		direction := map[bool]string{true: "来电", false: "去电"}[call.Direction == "incoming"]
		lines = append(lines, fmt.Sprintf("• %s · %s · %s · %s%s", firstNonEmpty(call.Number, "未知号码"), direction, telegramIMSStateLabel(call.State), call.ID, telegramSIPDiagnostic(call)))
	}
	return strings.Join(lines, "\n")
}

func (bot *telegramBot) executeSimpleCellularCallAction(ctx context.Context, physicalID, action string) (string, error) {
	switch action {
	case "status":
		calls, err := bot.telegramCellularCalls(ctx, physicalID)
		if err != nil {
			if final, ok := telegramCallFinalError(err); ok && final == "NO CARRIER" {
				return "基站直连：当前没有活动通话。", nil
			}
			return "", err
		}
		return formatTelegramCellularCalls(calls), nil
	case "answer":
		operationContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		response, err := bot.server.devices.ExecuteAT(operationContext, physicalID, "ATA")
		cancel()
		if err != nil {
			return "", err
		}
		if !response.OK() {
			return "", fmt.Errorf("模块未确认 ATA: %s", formatTelegramAT(response))
		}
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			calls, queryErr := bot.telegramCellularCalls(ctx, physicalID)
			if queryErr != nil {
				return "", queryErr
			}
			for _, call := range calls {
				if telegramCellularCallInt(call, "direction") == 1 && telegramCellularCallInt(call, "state") == 0 {
					return "📞 基站来电已接通。", nil
				}
			}
			if !waitTelegram(ctx, 500*time.Millisecond) {
				return "", ctx.Err()
			}
		}
		return "", errors.New("模块接受了 ATA，但 8 秒内未确认来电已接通")
	case "hangup":
		if err := bot.hangupCellularCall(physicalID); err != nil {
			return "", err
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			calls, queryErr := bot.telegramCellularCalls(ctx, physicalID)
			if queryErr != nil {
				if final, ok := telegramCallFinalError(queryErr); ok && final == "NO CARRIER" {
					return "已通过基站发送 ATH，并确认通话结束。", nil
				}
				return "", queryErr
			}
			if len(calls) == 0 {
				return "已通过基站发送 ATH，并确认没有活动通话。", nil
			}
			if !waitTelegram(ctx, 400*time.Millisecond) {
				return "", ctx.Err()
			}
		}
		return "", errors.New("模块接受了 ATH，但 4 秒后仍报告活动通话")
	default:
		return "", errors.New("未知通话操作")
	}
}

func formatTelegramCellularCalls(calls []map[string]any) string {
	if len(calls) == 0 {
		return "基站直连：当前没有活动通话。"
	}
	lines := []string{"基站直连通话："}
	for _, call := range calls {
		direction := map[bool]string{true: "来电", false: "去电"}[telegramCellularCallInt(call, "direction") == 1]
		number := strings.TrimSpace(fmt.Sprint(call["number"]))
		if number == "" || number == "<nil>" {
			number = "未知号码"
		}
		lines = append(lines, fmt.Sprintf("• #%d · %s · %s · %s", telegramCellularCallInt(call, "index"), number, direction, telegramCLCCStateLabel(telegramCellularCallInt(call, "state"))))
	}
	return strings.Join(lines, "\n")
}

func (bot *telegramBot) handleATCommand(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, command string) {
	result, err := bot.executeATCommand(ctx, deviceID, command)
	outcome := "success"
	if err != nil {
		outcome = "failure"
		bot.sendText(ctx, config, chatID, "AT 指令执行失败："+err.Error(), bot.homeKeyboard())
	} else {
		bot.sendText(ctx, config, chatID, result, bot.homeKeyboard())
	}
	bot.server.recordAudit(ctx, fmt.Sprintf("telegram:%d", adminID), "telegram.at.execute", "device", deviceID, outcome, "telegram")
}

func (bot *telegramBot) executeATCommand(ctx context.Context, deviceID, command string) (string, error) {
	command = strings.TrimSpace(command)
	if err := validateATCommand(command, false); err != nil {
		return "", err
	}
	_, _, physicalID, err := bot.device(deviceID)
	if err != nil {
		return "", err
	}
	operationContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	response, err := bot.server.devices.ExecuteAT(operationContext, physicalID, command)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("设备：%s\n> %s\n\n%s", deviceID, command, formatTelegramAT(response)), nil
}

func (bot *telegramBot) handleUSSDCommand(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, code string) {
	result, err := bot.executeUSSDCommand(ctx, deviceID, code)
	outcome := "success"
	if err != nil {
		outcome = "failure"
		bot.sendText(ctx, config, chatID, "USSD 指令执行失败："+err.Error(), bot.homeKeyboard())
	} else {
		bot.sendUSSDResult(ctx, config, chatID, adminID, deviceID, result)
	}
	bot.server.recordAudit(ctx, fmt.Sprintf("telegram:%d", adminID), "telegram.ussd.start", "device", deviceID, outcome, "telegram")
}

func (bot *telegramBot) executeUSSDCommand(ctx context.Context, deviceID, code string) (device.USSDResult, error) {
	_, _, physicalID, err := bot.device(deviceID)
	if err != nil {
		return device.USSDResult{}, err
	}
	operationContext, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return bot.server.devices.USSD(operationContext, physicalID, strings.TrimSpace(code))
}

func (bot *telegramBot) handleUSSDReply(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, sessionID, input string) {
	operationContext, cancel := context.WithTimeout(ctx, 90*time.Second)
	result, err := bot.server.devices.ContinueUSSD(operationContext, strings.TrimSpace(sessionID), strings.TrimSpace(input))
	cancel()
	outcome := "success"
	if err != nil {
		outcome = "failure"
		bot.sendText(ctx, config, chatID, "USSD 回复失败："+err.Error(), bot.homeKeyboard())
	} else {
		bot.sendUSSDResult(ctx, config, chatID, adminID, "", result)
	}
	bot.server.recordAudit(ctx, fmt.Sprintf("telegram:%d", adminID), "telegram.ussd.reply", "ussd_session", "interactive", outcome, "telegram")
}

func (bot *telegramBot) handleUSSDCancel(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, sessionID string) {
	operationContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := bot.server.devices.CancelUSSD(operationContext, strings.TrimSpace(sessionID))
	cancel()
	outcome := "success"
	if err != nil {
		outcome = "failure"
		bot.sendText(ctx, config, chatID, "取消 USSD 会话失败："+err.Error(), bot.homeKeyboard())
	} else {
		bot.sendText(ctx, config, chatID, "USSD 会话已取消。", bot.homeKeyboard())
	}
	bot.server.recordAudit(ctx, fmt.Sprintf("telegram:%d", adminID), "telegram.ussd.cancel", "ussd_session", "interactive", outcome, "telegram")
}

func (bot *telegramBot) sendUSSDResult(
	ctx context.Context,
	config telegramRuntimeConfig,
	chatID, adminID int64,
	deviceID string,
	result device.USSDResult,
) {
	if !result.Continueable || strings.TrimSpace(result.SessionID) == "" {
		bot.clearInput(chatID, adminID)
		bot.sendText(ctx, config, chatID, formatTelegramUSSD(deviceID, result), bot.homeKeyboard())
		return
	}
	sessionID := strings.TrimSpace(result.SessionID)
	bot.setInput(telegramInputState{
		Kind: "ussd_reply", DeviceID: deviceID, Argument: sessionID,
		ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
	})
	token, err := bot.putPending(telegramPendingAction{
		Kind: "menu_ussd_session", Argument: sessionID,
		ChatID: chatID, AdminID: adminID, CreatedAt: time.Now(),
	})
	if err != nil {
		bot.sendText(ctx, config, chatID, formatTelegramUSSD(deviceID, result), bot.homeKeyboard())
		return
	}
	bot.sendText(ctx, config, chatID, formatTelegramUSSD(deviceID, result),
		telegramKeyboard(
			[]map[string]string{telegramButton("❌ 取消 USSD 会话", "uc:"+token+":cancel")},
			[]map[string]string{telegramButton("🏠 主菜单", "menu:home")},
		))
}

func formatTelegramUSSD(deviceID string, result device.USSDResult) string {
	lines := make([]string, 0, 7)
	if strings.TrimSpace(deviceID) != "" {
		lines = append(lines, "设备："+strings.TrimSpace(deviceID))
	}
	if strings.TrimSpace(result.Code) != "" {
		lines = append(lines, "USSD："+strings.TrimSpace(result.Code))
	}
	lines = append(lines, "状态："+firstNonEmpty(strings.TrimSpace(result.Status), "final"))
	if strings.TrimSpace(result.Text) != "" {
		lines = append(lines, "\n"+strings.TrimSpace(result.Text))
	} else if strings.TrimSpace(result.Raw) != "" {
		lines = append(lines, "\n"+strings.TrimSpace(result.Raw))
	} else {
		lines = append(lines, "\n网络未返回文本内容。")
	}
	if result.Continueable && strings.TrimSpace(result.SessionID) != "" {
		lines = append(lines,
			"\n网络正在等待输入。",
			"请直接发送回复内容，或点击下方按钮取消会话。",
		)
	}
	return strings.Join(lines, "\n")
}

func (bot *telegramBot) handleVoWiFi(ctx context.Context, config telegramRuntimeConfig, chatID, adminID int64, deviceID, operation string) {
	stored, entry, physicalID, err := bot.device(deviceID)
	if err != nil {
		bot.sendText(ctx, config, chatID, "VoWiFi 操作失败："+err.Error(), bot.homeKeyboard())
		return
	}
	if bot.server.vowifi == nil {
		bot.sendText(ctx, config, chatID, "VoWiFi runtime 不可用。", bot.homeKeyboard())
		return
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation == "status" {
		bot.sendVoWiFiMenu(ctx, config, chatID, adminID, deviceID)
		return
	}
	var state vowifi.State
	switch operation {
	case "on", "off":
		enabled := operation == "on"
		if enabled && stored.NetworkEnabled {
			bot.sendText(ctx, config, chatID, "VoWiFi 操作失败：请先关闭漫游数据。", bot.homeKeyboard())
			return
		}
		if enabled && entry.Snapshot != nil {
			if reason := device.RegionBlockReason(entry.Snapshot.IMSI); reason != "" {
				bot.sendText(ctx, config, chatID, "VoWiFi 操作被拒绝："+reason, nil)
				return
			}
		}
		previous := stored.VoWiFiEnabled
		dataRuntime := bot.server.cellularDataRuntime()
		desiredData := stored.NetworkEnabled && !stored.VoWiFiEnabled
		maintenancePhase := "disabled"
		bot.server.clearPublicIP(stored.ID)
		if desiredData {
			maintenancePhase = "recovering"
		}
		dataRuntime.invalidateWithMaintenancePhase(stored.ID, desiredData, maintenancePhase, "", "vowifi")
		// Telegram follows the same fail-closed transaction as the web API:
		// RF is disabled before either starting or stopping VoWiFi. Stopping it
		// never implicitly permits cellular registration.
		if _, err = bot.server.devices.SetFlight(ctx, physicalID, true); err != nil {
			bot.sendText(ctx, config, chatID, "VoWiFi 操作失败：无法进入飞行模式："+err.Error(), bot.homeKeyboard())
			return
		}
		iccid := ""
		if entry.Snapshot != nil {
			iccid = strings.TrimSpace(entry.Snapshot.ICCID)
		}
		if iccid != "" {
			policy, policyErr := bot.server.store.CardPolicy(ctx, iccid)
			if errors.Is(policyErr, store.ErrNotFound) {
				policy = store.CardPolicy{ICCID: iccid, IPVersion: "IPV4V6"}
				policyErr = nil
			}
			if policyErr != nil {
				bot.sendText(ctx, config, chatID, "VoWiFi 操作失败："+policyErr.Error(), bot.homeKeyboard())
				return
			}
			policy.VoWiFiEnabled = enabled
			policy.AirplaneEnabled = true
			policy.NetworkEnabled = false
			policy.Source = "manual"
			if err = bot.server.store.UpsertCardPolicy(ctx, policy); err != nil {
				bot.sendText(ctx, config, chatID, "VoWiFi 操作失败："+err.Error(), bot.homeKeyboard())
				return
			}
		}
		stored.VoWiFiEnabled = enabled
		stored.NetworkEnabled = false
		if err = bot.server.store.UpsertDevice(ctx, stored); err == nil {
			dataRuntime.invalidate(stored.ID, false, "disabled", "")
			state, err = bot.server.vowifi.RequestEnabled(deviceID, enabled)
		}
		if err != nil {
			stored.VoWiFiEnabled = previous
			_ = bot.server.store.UpsertDevice(ctx, stored)
			if iccid != "" {
				if policy, policyErr := bot.server.store.CardPolicy(ctx, iccid); policyErr == nil {
					policy.VoWiFiEnabled = previous
					policy.AirplaneEnabled = true
					policy.NetworkEnabled = false
					_ = bot.server.store.UpsertCardPolicy(ctx, policy)
				}
			}
			if errors.Is(err, vowifiruntime.ErrOperationInProgress) && state.Enabled == enabled {
				err = nil
			}
		}
	case "reconnect":
		if !stored.VoWiFiEnabled {
			err = errors.New("请先启用 VoWiFi")
		} else {
			state, err = bot.server.vowifi.RequestReconnect(deviceID)
		}
	default:
		bot.sendText(ctx, config, chatID, "操作必须是 status、on、off 或 reconnect。", nil)
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "failure"
		bot.sendText(ctx, config, chatID, "VoWiFi 操作失败："+err.Error(), bot.homeKeyboard())
	} else {
		bot.sendText(ctx, config, chatID, "VoWiFi 操作已受理。\n"+formatTelegramVoWiFiState(state), bot.homeKeyboard())
	}
	bot.server.recordAudit(ctx, fmt.Sprintf("telegram:%d", adminID), "telegram.vowifi."+operation, "device", deviceID, outcome, "telegram")
}

func (bot *telegramBot) finishAction(ctx context.Context, config telegramRuntimeConfig, action telegramPendingAction, auditAction, result string, err error) {
	outcome := "success"
	if err != nil {
		outcome = "failure"
		bot.sendText(ctx, config, action.ChatID, "操作失败："+err.Error(), bot.homeKeyboard())
	} else {
		bot.sendText(ctx, config, action.ChatID, "✅ "+result, bot.homeKeyboard())
	}
	bot.server.recordAudit(ctx, fmt.Sprintf("telegram:%d", action.AdminID), auditAction, "device", action.DeviceID, outcome, "telegram")
}

func (bot *telegramBot) notifyInboundSMS(ctx context.Context) {
	cursorInitialized := false
	var cursor int64
	for ctx.Err() == nil {
		if !cursorInitialized {
			latest, err := bot.server.store.LatestSMSMessageID(ctx)
			if err != nil {
				bot.warn("initialize Telegram SMS cursor", err)
				if !waitTelegram(ctx, telegramNotificationPeriod) {
					return
				}
				continue
			}
			cursor, cursorInitialized = latest, true
		}
		config, enabled, err := bot.loadConfig(ctx)
		if err != nil {
			bot.warn("load Telegram SMS notification configuration", err)
		} else if !enabled {
			if latest, latestErr := bot.server.store.LatestSMSMessageID(ctx); latestErr == nil {
				cursor = latest
			}
		} else {
			messages, listErr := bot.server.store.ListInboundSMSAfterID(ctx, cursor, 100)
			if listErr != nil {
				bot.warn("list Telegram SMS notifications", listErr)
			} else {
				for _, message := range messages {
					if !smsMessageReadyToNotify(message) {
						// Empty decoded messages and multipart messages still waiting
						// for segments are not user-notifiable. Advance the cursor;
						// a completed multipart row re-enters with a fresh id.
						cursor = message.ID
						continue
					}
					text := fmt.Sprintf("📩 新短信\n设备：%s\n来自：%s\n时间：%s\n\n%s", message.DeviceID, message.Peer, message.Timestamp.Local().Format("2006-01-02 15:04:05"), message.Body)
					if sendErr := bot.sendText(ctx, config, 0, text, nil); sendErr != nil {
						bot.warn("send Telegram SMS notification", sendErr)
						break
					}
					cursor = message.ID
				}
			}
		}
		if !waitTelegram(ctx, telegramNotificationPeriod) {
			return
		}
	}
}

func (bot *telegramBot) device(deviceID string) (store.Device, device.Device, string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return store.Device{}, device.Device{}, "", errors.New("设备 ID 不能为空")
	}
	stored, err := bot.server.store.Device(context.Background(), deviceID)
	if err != nil {
		return store.Device{}, device.Device{}, "", err
	}
	entry, physicalID, present := bot.server.physicalForConfig(stored)
	if !present {
		return stored, entry, "", errors.New("设备不在线")
	}
	return stored, entry, physicalID, nil
}

func (bot *telegramBot) authorized(config telegramRuntimeConfig, chatID, userID int64) bool {
	return config.AdminID > 0 && userID == config.AdminID && strconv.FormatInt(chatID, 10) == config.ChatID
}

func (bot *telegramBot) loadConfig(ctx context.Context) (telegramRuntimeConfig, bool, error) {
	setting, err := bot.server.store.NotificationSetting(ctx, "telegram")
	if errors.Is(err, store.ErrNotFound) {
		return telegramRuntimeConfig{}, false, nil
	}
	if err != nil {
		return telegramRuntimeConfig{}, false, err
	}
	if !setting.Enabled {
		return telegramRuntimeConfig{}, false, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(setting.Config, &raw); err != nil {
		return telegramRuntimeConfig{}, false, fmt.Errorf("decode Telegram config: %w", err)
	}
	config := telegramRuntimeConfig{
		Token:   configString(raw, "bot_token"),
		ChatID:  configString(raw, "chat_id"),
		BaseURL: configString(raw, "base_url"),
		Proxy:   configString(raw, "proxy"),
	}
	if config.BaseURL == "" {
		config.BaseURL = defaultTelegramBaseURL
	}
	if admin := configString(raw, "admin_id"); admin != "" {
		config.AdminID, err = strconv.ParseInt(admin, 10, 64)
		if err != nil || config.AdminID <= 0 {
			return telegramRuntimeConfig{}, false, errors.New("telegram.admin_id must be a positive integer")
		}
	}
	if !telegramTokenPattern.MatchString(config.Token) || config.ChatID == "" {
		return telegramRuntimeConfig{}, false, errors.New("Telegram bot token or chat id is invalid")
	}
	return config, true, nil
}

func (bot *telegramBot) call(ctx context.Context, config telegramRuntimeConfig, method string, payload any, result any) error {
	// Telegram polling is a long-lived notification channel and must use the
	// same administrator-configured destination exceptions as test messages,
	// SMS pushes and automatic-task notifications. This keeps SSRF protection
	// enabled while allowing explicit DNS Fake-IP ranges such as 198.18/15.
	ctx = bot.notificationDestinationContext(ctx)
	base, err := validateTelegramAPIURL(ctx, config.BaseURL, config.Token, method)
	if err != nil {
		return redactTelegramError(err, config.Token)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client, err := bot.httpClient(ctx, config.Proxy)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return redactTelegramError(err, config.Token)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "vocat-telegram-bot/1")
	response, err := client.Do(request)
	if err != nil {
		return redactTelegramError(err, config.Token)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	var envelope telegramAPIResponse
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		return fmt.Errorf("Telegram %s failed: HTTP %d %s", method, response.StatusCode, envelope.Description)
	}
	if result != nil && len(envelope.Result) != 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode Telegram %s result: %w", method, err)
		}
	}
	return nil
}

func (bot *telegramBot) httpClient(ctx context.Context, proxy string) (*http.Client, error) {
	proxy = strings.TrimSpace(proxy)
	bot.clientMu.Lock()
	defer bot.clientMu.Unlock()
	if bot.client != nil && bot.clientProxy == proxy {
		return bot.client, nil
	}
	client, err := persistentRestrictedHTTPClient(ctx, 10*time.Second, proxy)
	if err != nil {
		return nil, err
	}
	if bot.client != nil {
		bot.client.CloseIdleConnections()
	}
	bot.clientProxy = proxy
	bot.client = client
	return client, nil
}

func (bot *telegramBot) notificationDestinationContext(ctx context.Context) context.Context {
	if bot.server == nil {
		return ctx
	}
	return bot.server.notificationDestinationContext(ctx)
}

func (bot *telegramBot) sendText(ctx context.Context, config telegramRuntimeConfig, chatID int64, text string, replyMarkup any) error {
	method, payload := telegramTextRequest("sendMessage", config.ChatID, chatID, 0, text, replyMarkup)
	requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return bot.call(requestContext, config, method, payload, nil)
}

func (bot *telegramBot) sendMenuText(ctx context.Context, config telegramRuntimeConfig, chatID int64, text string, replyMarkup any) error {
	messageID, _ := ctx.Value(telegramMenuMessageContextKey{}).(int64)
	if messageID == 0 {
		return bot.sendText(ctx, config, chatID, text, replyMarkup)
	}
	method, payload := telegramTextRequest("editMessageText", config.ChatID, chatID, messageID, text, replyMarkup)
	requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := bot.call(requestContext, config, method, payload, nil)
	cancel()
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return nil
	}
	bot.warn("edit Telegram menu message", err)
	return bot.sendText(ctx, config, chatID, text, replyMarkup)
}

func telegramTextRequest(method, configuredChatID string, chatID, messageID int64, text string, replyMarkup any) (string, map[string]any) {
	target := configuredChatID
	if chatID != 0 {
		target = strconv.FormatInt(chatID, 10)
	}
	payload := map[string]any{
		"chat_id": target,
		"text":    truncateTelegramText(text, 3900),
	}
	if method == "editMessageText" && messageID != 0 {
		payload["message_id"] = messageID
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	return method, payload
}

func (bot *telegramBot) answerCallback(ctx context.Context, config telegramRuntimeConfig, callbackID, text string) error {
	payload := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		payload["text"] = text
	}
	requestContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return bot.call(requestContext, config, "answerCallbackQuery", payload, nil)
}

func (bot *telegramBot) warn(message string, err error) {
	if err == nil || bot.server.logger == nil {
		return
	}
	now := time.Now()
	text := redactTelegramText(err.Error(), "")
	bot.logMu.Lock()
	if text == bot.lastLogText && now.Sub(bot.lastLogTime) < time.Minute {
		bot.logMu.Unlock()
		return
	}
	bot.lastLogText, bot.lastLogTime = text, now
	bot.logMu.Unlock()
	bot.server.logger.Warn(message, "error", text)
}

func redactTelegramError(err error, token string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactTelegramText(err.Error(), token))
}

func redactTelegramText(value, token string) string {
	if strings.TrimSpace(token) != "" {
		value = strings.ReplaceAll(value, token, "[REDACTED]")
	}
	return telegramTokenInURLPattern.ReplaceAllString(value, "bot[REDACTED]")
}

func parseTelegramCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	commandToken, remainder, _ := strings.Cut(text, " ")
	commandToken = strings.TrimPrefix(commandToken, "/")
	if at := strings.IndexByte(commandToken, '@'); at >= 0 {
		commandToken = commandToken[:at]
	}
	return strings.ToLower(strings.TrimSpace(commandToken)), strings.TrimSpace(remainder)
}

func splitTelegramArguments(value string, count int) []string {
	fields := strings.Fields(value)
	if len(fields) == 0 || count <= 0 {
		return nil
	}
	if len(fields) <= count {
		return fields
	}
	result := append([]string(nil), fields[:count-1]...)
	return append(result, strings.Join(fields[count-1:], " "))
}

func validTelegramDialNumber(number string) bool {
	number = strings.TrimSpace(number)
	if strings.HasPrefix(number, "+") {
		number = number[1:]
	}
	if len(number) < 3 || len(number) > 20 {
		return false
	}
	for _, character := range number {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func formatTelegramAT(response modem.Response) string {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(response.Text()); text != "" {
		parts = append(parts, text)
	}
	if final := strings.TrimSpace(response.Final); final != "" {
		parts = append(parts, final)
	}
	if len(parts) == 0 {
		return "模块没有返回结果"
	}
	return strings.Join(parts, "\n")
}

func formatTelegramVoWiFiState(state vowifi.State) string {
	lines := []string{
		fmt.Sprintf("状态：%s", firstNonEmpty(string(state.Phase), "idle")),
		fmt.Sprintf("SIM=%t Access=%t Tunnel=%t IMS=%t SMS=%t", state.SIMReady, state.AccessReady, state.TunnelReady, state.IMSReady, state.SMSReady),
	}
	if state.LastReason != "" {
		lines = append(lines, "原因："+state.LastReason)
	}
	if state.LastError != "" {
		lines = append(lines, "错误："+state.LastError)
	}
	return strings.Join(lines, "\n")
}

func truncateTelegramText(value string, maximum int) string {
	runes := []rune(value)
	if maximum <= 0 || len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum]) + "…"
}

func tailDigits(value string, count int) string {
	value = strings.TrimSpace(value)
	if count <= 0 || len(value) <= count {
		return value
	}
	return value[len(value)-count:]
}

func waitTelegram(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
