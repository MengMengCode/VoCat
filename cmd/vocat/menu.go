package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"vocat/internal/auth"
	"vocat/internal/config"
	"vocat/internal/store"
	"vocat/internal/update"
)

// envFilePath carries non-secret service settings such as the Web listen port.
// Administrator credentials live exclusively in the database.
const envFilePath = "/etc/vocat/env"

// legacyEnvFilePath was used by the standalone deploy/vocat.service. Keep it
// discoverable so the menu works on installations made before the installer
// and service template converged on /etc/vocat/env.
const legacyEnvFilePath = "/etc/vocat/vocat.env"

const systemdUnitPath = "/etc/systemd/system/vocat.service"

// defaultDatabasePath is the install-default SQLite location written into the
// systemd unit by scripts/install.sh. Used only when VOCAT_DATABASE_PATH is not
// already set in the operator's environment.
const defaultDatabasePath = "/opt/vocat/data/vocat.db"

// uiPreferencesSettingKey is the same app_settings key the Web UI's
// /api/settings/preferences handler reads and writes (see general_api.go). The
// menu toggles language through it so a single preference is shared with the
// SPA and the backend i18n layer.
const uiPreferencesSettingKey = "ui.preferences"

// loadMenuEnv ensures the menu reaches the production config that systemd
// would otherwise inject. When an operator runs `sudo vocat` on the host, the
// shell has not sourced /etc/vocat/env (a systemd EnvironmentFile, not a shell
// rc) and VOCAT_DATABASE_PATH is unset, so config.Load() would resolve a
// CWD-relative ./data/vocat.db — a different, empty database than
// /opt/vocat/data/vocat.db the service uses. This loads the installed env file
// and pins VOCAT_DATABASE_PATH to the install default, without overriding any
// value the operator already exported. Legacy credential entries are ignored.
func loadMenuEnv() {
	if _, ok := os.LookupEnv("VOCAT_DATABASE_PATH"); !ok {
		_ = os.Setenv("VOCAT_DATABASE_PATH", defaultDatabasePath)
	}
	if data, err := os.ReadFile(menuEnvFilePath()); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				continue
			}
			key := strings.TrimSpace(line[:eq])
			if key == "VOCAT_ADMIN_USERNAME" || key == "VOCAT_ADMIN_PASSWORD" || key == "VOCAT_ADMIN_PASSWORD_B64" {
				continue
			}
			val := strings.TrimSpace(line[eq+1:])
			if _, ok := os.LookupEnv(key); !ok {
				_ = os.Setenv(key, val)
			}
		}
	}
}

func menuEnvFilePath() string {
	if _, err := os.Stat(envFilePath); err == nil {
		return envFilePath
	}
	if _, err := os.Stat(legacyEnvFilePath); err == nil {
		return legacyEnvFilePath
	}
	return envFilePath
}

// runMenu is the interactive lifecycle menu: toggle language, reset credentials,
// change the Web listener port, restart the systemd unit, self-update, or fully
// uninstall vocat. It must run as root on the host (needs systemctl + the 0600
// env file). Docker deployments do not use it.
func runMenu(logger *slog.Logger) error {
	if os.Geteuid() != 0 {
		return errors.New("vocat menu must run as root (needs systemctl and /etc/vocat/env)")
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("vocat menu requires an interactive terminal")
	}

	loadMenuEnv()

	lang, langErr := loadMenuLanguage()
	menu := newMenu(lang)
	if langErr != nil {
		logger.Warn("menu: load language preference failed; defaulting to English", "error", langErr)
	}
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Println()
		fmt.Println(menu.title())
		for _, opt := range menu.options() {
			fmt.Printf("  %s\n", opt)
		}
		fmt.Print(menu.prompt())
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read menu choice: %w", err)
		}
		choice := strings.TrimSpace(line)
		switch choice {
		case "1":
			if err := menuToggleLanguage(menu, logger); err != nil {
				fmt.Println(menu.errorPrefix(err))
			}
		case "2":
			if err := menuResetAdminCredentials(reader, menu); err != nil {
				fmt.Println(menu.errorPrefix(err))
			}
		case "3":
			if err := menuChangeWebPort(reader, menu); err != nil {
				fmt.Println(menu.errorPrefix(err))
			}
		case "4":
			if err := menuRestart(menu); err != nil {
				fmt.Println(menu.errorPrefix(err))
			}
		case "5":
			if err := menuUpdate(menu, logger); err != nil {
				fmt.Println(menu.errorPrefix(err))
			}
		case "0":
			if err := menuUninstall(reader, menu); err != nil {
				fmt.Println(menu.errorPrefix(err))
			} else {
				return nil
			}
		default:
			fmt.Println(menu.invalid())
		}
	}
}

// loadMenuLanguage reads the persisted UI preference (the same ui.preferences
// app_setting the Web UI writes) and returns "zh" or "en". When no record
// exists yet it returns "en", matching the Web default in writeUIPreferences.
// Any failure is surfaced to the caller, which logs a warning and keeps the
// default rather than blocking the menu.
func loadMenuLanguage() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "en", fmt.Errorf("%w: %v", errMenuConfig, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return "en", fmt.Errorf("%w: %v", errMenuStore, err)
	}
	defer database.Close()

	setting, err := database.AppSetting(ctx, uiPreferencesSettingKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "en", nil
		}
		return "en", fmt.Errorf("%w: %v", errMenuStore, err)
	}
	var prefs struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(setting.Value, &prefs); err != nil {
		return "en", nil
	}
	if prefs.Language == "zh" {
		return "zh", nil
	}
	return "en", nil
}

func menuResetAdminCredentials(reader *bufio.Reader, m *menu) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuConfig, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuStore, err)
	}
	defer database.Close()

	authService, err := auth.New(database, auth.Options{SessionTTL: cfg.SessionTTL})
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuAuth, err)
	}
	admin, err := database.CurrentAdmin(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuStore, err)
	}

	fmt.Print(m.newUsername(admin.Username))
	username, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read administrator username: %w", err)
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = admin.Username
	}
	fmt.Print(m.newPassword())
	newPw, err := readPasswordMasked()
	if err != nil {
		return err
	}
	fmt.Print(m.confirmPassword())
	confirmPw, err := readPasswordMasked()
	if err != nil {
		return err
	}
	fmt.Println()
	if newPw != confirmPw {
		return errPasswordsDiffer
	}
	if err := authService.ResetAdminCredentials(ctx, username, newPw); err != nil {
		return fmt.Errorf("%w: %v", errMenuAuth, err)
	}
	fmt.Println(m.passwordChanged())
	return nil
}

// readPasswordMasked reads a password with echo disabled. term.ReadPassword
// does not return the trailing newline, so we print one for a clean prompt.
func readPasswordMasked() (string, error) {
	fd := int(os.Stdin.Fd())
	bytes, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(bytes), nil
}

// rewriteEnvValue replaces or appends one systemd EnvironmentFile value. The
// write is atomic and rejects line breaks so one setting cannot inject another.
func rewriteEnvValue(path, name, value string) error {
	if name == "" || strings.ContainsAny(name, "=\r\n\x00") || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("invalid environment setting")
	}
	if strings.HasPrefix(name, "VOCAT_ADMIN_") {
		return errors.New("administrator credentials cannot be stored in the environment file")
	}
	key := name + "="
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(line, "VOCAT_ADMIN_USERNAME=") || strings.HasPrefix(line, "VOCAT_ADMIN_PASSWORD=") || strings.HasPrefix(line, "VOCAT_ADMIN_PASSWORD_B64=") {
			lines[i] = ""
			continue
		}
		if strings.HasPrefix(line, key) {
			lines[i] = key + value
			replaced = true
			break
		}
	}
	lines = compactNonEmptyLines(lines)
	if !replaced {
		lines = append(lines, key+value)
	}
	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return writeEnvFileAtomic(path, []byte(content))
}

func compactNonEmptyLines(lines []string) []string {
	result := lines[:0]
	for _, line := range lines {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func writeEnvFileAtomic(path string, content []byte) error {
	dirIndex := strings.LastIndexAny(path, "/\\")
	if dirIndex < 0 {
		return errors.New("environment file path has no directory")
	}
	dir := path[:dirIndex]
	tmp, err := os.CreateTemp(dir, ".vocat-env-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func menuChangeWebPort(reader *bufio.Reader, m *menu) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errNoSystemctl
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuConfig, err)
	}
	_, currentPortText, err := net.SplitHostPort(strings.TrimSpace(cfg.Address))
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuConfig, err)
	}
	fmt.Println(m.currentWebAddress(cfg.Address))
	fmt.Println(m.reverseProxyNotice())
	fmt.Print(m.newWebPort(currentPortText))
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read Web port: %w", err)
	}
	portText := strings.TrimSpace(line)
	if portText == "" {
		fmt.Println(m.webPortCancelled())
		return nil
	}
	newAddress, newPort, err := webAddressWithPort(cfg.Address, portText)
	if err != nil {
		return errInvalidWebPort
	}
	currentPort, _ := strconv.Atoi(currentPortText)
	if newPort == currentPort {
		fmt.Println(m.webPortUnchanged())
		return nil
	}

	listener, err := net.Listen("tcp", newAddress)
	if err != nil {
		return fmt.Errorf("%w: %v", errWebPortUnavailable, err)
	}
	_ = listener.Close()

	environmentPath := menuEnvFilePath()
	original, readErr := os.ReadFile(environmentPath)
	originalExisted := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("%w: %v", errMenuPortWrite, readErr)
	}
	if err := rewriteEnvValue(environmentPath, "VOCAT_ADDR", newAddress); err != nil {
		return fmt.Errorf("%w: %v", errMenuPortWrite, err)
	}
	if err := restartVocatService(); err != nil {
		rollbackErr := restoreMenuEnvFile(environmentPath, original, originalExisted)
		_ = restartVocatService()
		if rollbackErr != nil {
			return fmt.Errorf("%w: %v; rollback failed: %v", errRestartFailed, err, rollbackErr)
		}
		return fmt.Errorf("%w: %v", errRestartFailed, err)
	}
	if err := waitForWebListener(newAddress, 5*time.Second); err != nil {
		rollbackErr := restoreMenuEnvFile(environmentPath, original, originalExisted)
		_ = restartVocatService()
		if rollbackErr != nil {
			return fmt.Errorf("%w: %v; rollback failed: %v", errRestartFailed, err, rollbackErr)
		}
		return fmt.Errorf("%w: %v", errRestartFailed, err)
	}
	_ = os.Setenv("VOCAT_ADDR", newAddress)
	fmt.Println(m.webPortChanged(newAddress))
	return nil
}

func webAddressWithPort(address, portText string) (string, int, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errInvalidWebPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), port, nil
}

func waitForWebListener(address string, timeout time.Duration) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	target := net.JoinHostPort(host, port)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", target, 500*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil
		}
		lastErr = dialErr
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("Web listener %s did not become reachable: %w", target, lastErr)
}

func restoreMenuEnvFile(path string, content []byte, existed bool) error {
	if existed {
		return writeEnvFileAtomic(path, content)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// menuToggleLanguage flips the persisted language preference between "zh" and
// "en" by writing the same ui.preferences app_setting the Web UI uses, then
// switches the menu's own language so the next prompt renders in the new
// language. The Web SPA picks up the change on its next preferences fetch; the
// menu never needs to call i18n.Set itself since it carries its own lang copy.
func menuToggleLanguage(m *menu, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuConfig, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuStore, err)
	}
	defer database.Close()

	current := m.lang
	next := "zh"
	if current == "zh" {
		next = "en"
	}
	payload, err := json.Marshal(map[string]string{"language": next})
	if err != nil {
		return fmt.Errorf("%w: %v", errMenuStore, err)
	}
	if err := database.UpsertAppSetting(ctx, store.AppSetting{
		Key:   uiPreferencesSettingKey,
		Value: payload,
	}); err != nil {
		return fmt.Errorf("%w: %v", errMenuStore, err)
	}
	m.lang = next
	fmt.Println(m.languageSwitched())
	return nil
}

func menuRestart(m *menu) error {
	if err := restartVocatService(); err != nil {
		return err
	}
	fmt.Println(m.restarted())
	return nil
}

func restartVocatService() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errNoSystemctl
	}
	cmd := exec.Command("systemctl", "restart", "vocat")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", errRestartFailed, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "is-active", "--quiet", "vocat").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: service is not active: %s", errRestartFailed, strings.TrimSpace(string(out)))
	}
	return nil
}

// menuUpdate delegates to the self-updater in internal/update. It resolves the
// repo the same way update.Run itself does ($VOCAT_REPO or the default) and lets
// that package handle the check/download/verify/replace/restart flow. The
// running menu process keeps the old binary until the operator exits; only the
// systemd service runs the new build after restartService.
func menuUpdate(m *menu, logger *slog.Logger) error {
	repo := strings.TrimSpace(os.Getenv("VOCAT_REPO"))
	if repo == "" {
		repo = update.DefaultRepository
	}
	fmt.Println(m.updateChecking())
	if err := update.Run(logger, []string{"--repo", repo}); err != nil {
		return fmt.Errorf("%w: %v", errUpdateFailed, err)
	}
	return nil
}

// menuUninstall performs full removal: stop/disable the unit, delete the unit,
// remove /opt/vocat (binary + data + SQLite DB), remove the env file, reload
// systemd, and best-effort delete the vocat user.
func menuUninstall(reader *bufio.Reader, m *menu) error {
	fmt.Println(m.uninstallWarn())
	fmt.Print(m.uninstallConfirm())
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if strings.TrimSpace(line) != "yes" {
		fmt.Println(m.uninstallCancelled())
		return nil
	}

	runIgnore := func(name string, args ...string) {
		_ = exec.Command(name, args...).Run()
	}
	runIgnore("systemctl", "stop", "vocat")
	runIgnore("systemctl", "disable", "vocat")
	_ = os.Remove(systemdUnitPath)
	_ = os.RemoveAll("/opt/vocat")
	_ = os.Remove(envFilePath)
	_ = os.Remove(legacyEnvFilePath)
	_ = os.Remove("/etc/vocat") // succeeds only when empty
	runIgnore("systemctl", "daemon-reload")
	runIgnore("userdel", "vocat")

	fmt.Println(m.uninstalled())
	return nil
}

// menu-local sentinel errors so callers can map them to localized messages.
var (
	errPasswordsDiffer    = errors.New("menu: passwords do not match")
	errNoSystemctl        = errors.New("menu: systemctl not found")
	errRestartFailed      = errors.New("menu: restart failed")
	errUpdateFailed       = errors.New("menu: update failed")
	errMenuConfig         = errors.New("menu: load configuration")
	errMenuStore          = errors.New("menu: open database")
	errMenuAuth           = errors.New("menu: auth service")
	errMenuPortWrite      = errors.New("menu: write Web port")
	errInvalidWebPort     = errors.New("menu: invalid Web port")
	errWebPortUnavailable = errors.New("menu: Web port unavailable")
)

// ---- i18n ----

type menu struct{ lang string }

func newMenu(lang string) *menu { return &menu{lang: lang} }

// msg returns the localized string for a key. Each key carries [zh, en].
func (m *menu) msg(key string) string {
	const zh, en = 0, 1
	table := map[string][2]string{
		"title":               {"vocat 管理菜单", "vocat management menu"},
		"opt_lang":            {"1) 切换中英文", "1) Toggle language"},
		"opt_change":          {"2) 修改账号密码", "2) Change admin credentials"},
		"opt_port":            {"3) 修改 Web 监听端口", "3) Change Web listening port"},
		"opt_restart":         {"4) 重启软件", "4) Restart software"},
		"opt_update":          {"5) 更新软件", "5) Update software"},
		"opt_uninstall":       {"0) 卸载软件", "0) Uninstall software"},
		"prompt":              {"请选择: ", "Select: "},
		"invalid":             {"无效选项，请重试。按 Ctrl+C 退出。", "Invalid choice, try again. Press Ctrl+C to exit."},
		"new_username":        {"新用户名（直接回车保留 %s）: ", "New username (Enter to keep %s): "},
		"new_pw":              {"新密码 (至少 6 位): ", "New password (min 6 chars): "},
		"confirm_pw":          {"确认新密码: ", "Confirm new password: "},
		"pw_changed":          {"管理员账号密码已修改，现有 Web 会话已退出。", "Administrator credentials changed; existing Web sessions were signed out."},
		"current_web_address": {"当前 Web 监听地址: %s", "Current Web listening address: %s"},
		"new_web_port":        {"新端口 (1-65535，直接回车取消，当前 %s): ", "New port (1-65535, Enter to cancel, current %s): "},
		"web_port_cancelled":  {"已取消修改端口。", "Web port change cancelled."},
		"web_port_unchanged":  {"端口未改变。", "Web port is unchanged."},
		"web_port_changed":    {"Web 监听地址已改为 %s，软件已重启。", "Web listening address changed to %s; software restarted."},
		"reverse_proxy_notice": {
			"如使用 Nginx/Caddy 等反向代理，请同步修改其上游端口。",
			"If you use Nginx, Caddy, or another reverse proxy, update its upstream port too.",
		},
		"lang_switched": {
			"语言已切换。Web 界面下次刷新后同步。",
			"Language switched. The web UI syncs on next refresh.",
		},
		"upd_checking": {"正在检查更新…", "Checking for updates…"},
		"restarted":    {"软件已重启。", "Software restarted."},
		"uninstall_warn": {
			"警告: 将删除程序、数据与配置,且不可恢复!",
			"WARNING: removes the program, data and config. Irreversible!",
		},
		"uninstall_confirm":   {"输入 yes 确认卸载: ", "Type yes to confirm uninstall: "},
		"uninstall_cancelled": {"已取消卸载。", "Uninstall cancelled."},
		"uninstalled":         {"vocat 已卸载。", "vocat uninstalled."},
	}
	entry, ok := table[key]
	if !ok {
		return key
	}
	if m.lang == "en" {
		return entry[en]
	}
	return entry[zh]
}

func (m *menu) title() string   { return m.msg("title") }
func (m *menu) prompt() string  { return m.msg("prompt") }
func (m *menu) invalid() string { return m.msg("invalid") }
func (m *menu) newUsername(current string) string {
	return fmt.Sprintf(m.msg("new_username"), current)
}
func (m *menu) newPassword() string     { return m.msg("new_pw") }
func (m *menu) confirmPassword() string { return m.msg("confirm_pw") }
func (m *menu) passwordChanged() string { return m.msg("pw_changed") }
func (m *menu) currentWebAddress(address string) string {
	return fmt.Sprintf(m.msg("current_web_address"), address)
}
func (m *menu) newWebPort(port string) string { return fmt.Sprintf(m.msg("new_web_port"), port) }
func (m *menu) webPortCancelled() string      { return m.msg("web_port_cancelled") }
func (m *menu) webPortUnchanged() string      { return m.msg("web_port_unchanged") }
func (m *menu) webPortChanged(address string) string {
	return fmt.Sprintf(m.msg("web_port_changed"), address)
}
func (m *menu) reverseProxyNotice() string { return m.msg("reverse_proxy_notice") }
func (m *menu) languageSwitched() string   { return m.msg("lang_switched") }
func (m *menu) updateChecking() string     { return m.msg("upd_checking") }
func (m *menu) restarted() string          { return m.msg("restarted") }
func (m *menu) uninstallWarn() string      { return m.msg("uninstall_warn") }
func (m *menu) uninstallConfirm() string   { return m.msg("uninstall_confirm") }
func (m *menu) uninstallCancelled() string { return m.msg("uninstall_cancelled") }
func (m *menu) uninstalled() string        { return m.msg("uninstalled") }

func (m *menu) options() []string {
	return []string{
		m.msg("opt_lang"),
		m.msg("opt_change"),
		m.msg("opt_port"),
		m.msg("opt_restart"),
		m.msg("opt_update"),
		m.msg("opt_uninstall"),
	}
}

func (m *menu) errorPrefix(err error) string {
	switch {
	case errors.Is(err, errPasswordsDiffer):
		if m.lang == "en" {
			return "Passwords do not match."
		}
		return "两次输入的密码不一致。"
	case errors.Is(err, errNoSystemctl):
		if m.lang == "en" {
			return "systemctl not found."
		}
		return "未找到 systemctl。"
	case errors.Is(err, errRestartFailed):
		if m.lang == "en" {
			return "Restart failed."
		}
		return "重启失败。"
	case errors.Is(err, errUpdateFailed):
		if m.lang == "en" {
			return "Update failed."
		}
		return "更新失败。"
	case errors.Is(err, errMenuConfig):
		if m.lang == "en" {
			return "Failed to load configuration."
		}
		return "加载配置失败。"
	case errors.Is(err, errMenuStore):
		if m.lang == "en" {
			return "Failed to open the database."
		}
		return "打开数据库失败。"
	case errors.Is(err, errMenuAuth):
		if m.lang == "en" {
			return "Auth service error."
		}
		return "认证服务错误。"
	case errors.Is(err, errInvalidWebPort):
		if m.lang == "en" {
			return "Invalid port. Enter a number from 1 to 65535."
		}
		return "端口无效，请输入 1 到 65535。"
	case errors.Is(err, errWebPortUnavailable):
		if m.lang == "en" {
			return "The new Web port is unavailable or already in use."
		}
		return "新的 Web 端口不可用或已被占用。"
	case errors.Is(err, errMenuPortWrite):
		if m.lang == "en" {
			return "Failed to save the Web listening port to " + menuEnvFilePath() + "."
		}
		return "无法将 Web 监听端口保存到 " + menuEnvFilePath() + "。"
	default:
		if m.lang == "en" {
			return "Error: " + err.Error()
		}
		return "错误: " + err.Error()
	}
}
