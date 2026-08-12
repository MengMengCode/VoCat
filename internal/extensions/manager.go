package extensions

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"vocat/internal/exportproxy"
	"vocat/internal/netguard"
)

const maxPackageBytes int64 = 64 << 20

// This syntactic guard gives the request boundary an explicit allowlist. The
// resolved addresses are still checked again by netguard before dialing.
var publicHTTPSURLPattern = regexp.MustCompile(`^https://(?:[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?(?:[/?#][^\r\n]*)?$`)

type Plugin struct {
	Manifest
	Enabled          bool   `json:"enabled"`
	BackendAvailable bool   `json:"backend_available"`
	BackendRunning   bool   `json:"backend_running"`
	BackendError     string `json:"backend_error,omitempty"`
	InstalledAt      string `json:"installed_at"`
	SHA256           string `json:"sha256"`

	dir       string
	command   *exec.Cmd
	backend   *url.URL
	installed time.Time
}

type stateFile struct {
	Enabled     bool      `json:"enabled"`
	InstalledAt time.Time `json:"installed_at"`
	SHA256      string    `json:"sha256"`
}

type Manager struct {
	root   string
	logger *slog.Logger
	client *http.Client

	mu      sync.RWMutex
	plugins map[string]*Plugin
}

func NewManager(root string, logger *slog.Logger) (*Manager, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create plugin directory: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	manager := &Manager{
		root: root, logger: logger, plugins: make(map[string]*Plugin),
		client: netguard.NewPublicHTTPClient(45*time.Second, true),
	}
	if err := manager.scan(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (manager *Manager) scan() error {
	entries, err := os.ReadDir(manager.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !pluginIDPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(manager.root, entry.Name())
		plugin, err := loadPlugin(dir)
		if err != nil {
			manager.logger.Warn("skip invalid plugin", "directory", dir, "error", err)
			continue
		}
		if plugin.ID == exportproxy.ReservedID {
			manager.logger.Info("skip legacy Export Proxy plugin; functionality is built in", "directory", dir)
			continue
		}
		manager.plugins[plugin.ID] = plugin
		if plugin.Enabled {
			manager.startLocked(plugin)
		}
	}
	return nil
}

func loadPlugin(dir string) (*Plugin, error) {
	file, err := os.Open(filepath.Join(dir, ManifestFilename))
	if err != nil {
		return nil, err
	}
	manifest, err := DecodeManifest(file)
	_ = file.Close()
	if err != nil {
		return nil, err
	}
	if filepath.Base(dir) != manifest.ID {
		return nil, errors.New("plugin directory does not match manifest id")
	}
	var state stateFile
	stateData, err := os.ReadFile(filepath.Join(dir, ".vocat-state.json"))
	if err == nil {
		if err := json.Unmarshal(stateData, &state); err != nil {
			return nil, fmt.Errorf("decode plugin state: %w", err)
		}
	}
	command, available := manifest.BackendCommand()
	if available {
		_, err = os.Stat(filepath.Join(dir, filepath.FromSlash(command)))
		available = err == nil
	}
	return &Plugin{
		Manifest: manifest, Enabled: state.Enabled, BackendAvailable: available,
		InstalledAt: state.InstalledAt.UTC().Format(time.RFC3339), SHA256: state.SHA256,
		dir: dir, installed: state.InstalledAt,
	}, nil
}

func (manager *Manager) List() []Plugin {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	result := make([]Plugin, 0, len(manager.plugins))
	for _, plugin := range manager.plugins {
		copy := *plugin
		copy.command = nil
		copy.backend = nil
		result = append(result, copy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (manager *Manager) InstallURL(ctx context.Context, rawURL, expectedSHA string) (Plugin, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !publicHTTPSURLPattern.MatchString(rawURL) {
		return Plugin{}, errors.New("plugin URL must be a public absolute HTTPS URL")
	}
	parsed, err := netguard.ValidatePublicURL(ctx, rawURL, true)
	if err != nil {
		return Plugin{}, fmt.Errorf("plugin URL must be a public absolute HTTPS URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Plugin{}, err
	}
	response, err := manager.client.Do(request)
	if err != nil {
		return Plugin{}, fmt.Errorf("download plugin: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Plugin{}, fmt.Errorf("download plugin: HTTP %d", response.StatusCode)
	}
	return manager.Install(response.Body, expectedSHA)
}

func (manager *Manager) Install(reader io.Reader, expectedSHA string) (Plugin, error) {
	temp, err := os.CreateTemp(manager.root, ".upload-*.vocat-plugin")
	if err != nil {
		return Plugin{}, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(reader, maxPackageBytes+1))
	closeErr := temp.Close()
	if copyErr != nil {
		return Plugin{}, copyErr
	}
	if closeErr != nil {
		return Plugin{}, closeErr
	}
	if written > maxPackageBytes {
		return Plugin{}, fmt.Errorf("plugin package exceeds %d MiB", maxPackageBytes>>20)
	}
	actualSHA := hex.EncodeToString(hash.Sum(nil))
	if expected := strings.ToLower(strings.TrimSpace(expectedSHA)); expected != "" && expected != actualSHA {
		return Plugin{}, errors.New("plugin package SHA-256 does not match")
	}

	archive, err := zip.OpenReader(tempName)
	if err != nil {
		return Plugin{}, errors.New("plugin package must be a ZIP archive")
	}
	defer archive.Close()
	manifest, err := manifestFromArchive(archive.File)
	if err != nil {
		return Plugin{}, err
	}
	if manifest.ID == exportproxy.ReservedID {
		return Plugin{}, errors.New("plugin ID export-proxy is reserved by the built-in Export Proxy feature")
	}
	staging, err := os.MkdirTemp(manager.root, ".install-"+manifest.ID+"-")
	if err != nil {
		return Plugin{}, err
	}
	defer os.RemoveAll(staging)
	if err := extractArchive(archive.File, staging); err != nil {
		return Plugin{}, err
	}
	installedAt := time.Now().UTC()
	state := stateFile{Enabled: true, InstalledAt: installedAt, SHA256: actualSHA}
	if err := writeState(staging, state); err != nil {
		return Plugin{}, err
	}
	target := filepath.Join(manager.root, manifest.ID)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.plugins[manifest.ID]; exists {
		return Plugin{}, fmt.Errorf("plugin %q is already installed; uninstall it before replacing", manifest.ID)
	}
	if err := os.Rename(staging, target); err != nil {
		return Plugin{}, fmt.Errorf("activate plugin: %w", err)
	}
	plugin, err := loadPlugin(target)
	if err != nil {
		_ = os.RemoveAll(target)
		return Plugin{}, err
	}
	manager.plugins[plugin.ID] = plugin
	manager.startLocked(plugin)
	return publicPlugin(plugin), nil
}

func manifestFromArchive(files []*zip.File) (Manifest, error) {
	for _, file := range files {
		name := strings.ReplaceAll(file.Name, `\`, "/")
		if name != ManifestFilename {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return Manifest{}, err
		}
		manifest, decodeErr := DecodeManifest(reader)
		_ = reader.Close()
		return manifest, decodeErr
	}
	return Manifest{}, fmt.Errorf("plugin package is missing root %s", ManifestFilename)
}

func extractArchive(files []*zip.File, staging string) error {
	var expanded int64
	for _, file := range files {
		name := strings.ReplaceAll(file.Name, `\`, "/")
		if strings.HasSuffix(name, "/") {
			continue
		}
		if !safeRelativePath(name) || file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin package contains unsafe path %q", file.Name)
		}
		expanded += int64(file.UncompressedSize64)
		if expanded > maxPackageBytes*4 {
			return errors.New("expanded plugin package is too large")
		}
		target := filepath.Join(staging, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o640)
		if file.Mode()&0o111 != 0 {
			mode = 0o750
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err == nil {
			_, err = io.Copy(output, input)
			_ = output.Close()
		}
		_ = input.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeState(dir string, state stateFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".vocat-state.json"), data, 0o600)
}

func (manager *Manager) SetEnabled(id string, enabled bool) (Plugin, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	plugin := manager.plugins[id]
	if plugin == nil {
		return Plugin{}, os.ErrNotExist
	}
	if plugin.Enabled == enabled {
		return publicPlugin(plugin), nil
	}
	plugin.Enabled = enabled
	if err := writeState(plugin.dir, stateFile{Enabled: enabled, InstalledAt: plugin.installed, SHA256: plugin.SHA256}); err != nil {
		plugin.Enabled = !enabled
		return Plugin{}, err
	}
	if enabled {
		manager.startLocked(plugin)
	} else {
		manager.stopLocked(plugin)
	}
	return publicPlugin(plugin), nil
}

func (manager *Manager) Uninstall(id string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	plugin := manager.plugins[id]
	if plugin == nil {
		return os.ErrNotExist
	}
	manager.stopLocked(plugin)
	delete(manager.plugins, id)
	clean := filepath.Clean(plugin.dir)
	if filepath.Dir(clean) != filepath.Clean(manager.root) || filepath.Base(clean) != id {
		return errors.New("refusing to remove plugin outside plugin directory")
	}
	return os.RemoveAll(clean)
}

func (manager *Manager) ServeAsset(w http.ResponseWriter, r *http.Request, id, name string) {
	manager.mu.RLock()
	plugin := manager.plugins[id]
	manager.mu.RUnlock()
	if plugin == nil || !plugin.Enabled || !safeRelativePath(name) {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(plugin.dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (manager *Manager) ProxyBackend(w http.ResponseWriter, r *http.Request, id string) {
	manager.mu.RLock()
	plugin := manager.plugins[id]
	var target *url.URL
	if plugin != nil && plugin.Enabled && plugin.BackendRunning && plugin.backend != nil {
		copy := *plugin.backend
		target = &copy
	}
	manager.mu.RUnlock()
	if target == nil {
		http.Error(w, "plugin backend is not running", http.StatusServiceUnavailable)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Header.Del("Cookie")
		request.Header.Del("X-CSRF-Token")
		request.Header.Del("Authorization")
		request.Header.Set("X-VoCat-Plugin-ID", id)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		return nil
	}
	prefix := "/api/extensions/" + id + "/backend"
	r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		manager.logger.Warn("plugin backend proxy failed", "plugin", id, "error", err)
		http.Error(w, "plugin backend is unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (manager *Manager) startLocked(plugin *Plugin) {
	commandPath, supported := plugin.BackendCommand()
	if !supported {
		if plugin.Backend != nil {
			plugin.BackendError = "backend does not support " + runtime.GOOS + "/" + runtime.GOARCH
		}
		return
	}
	fullCommand := filepath.Join(plugin.dir, filepath.FromSlash(commandPath))
	if _, err := os.Stat(fullCommand); err != nil {
		plugin.BackendError = "backend executable is missing"
		return
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(fullCommand, 0o750); err != nil {
			plugin.BackendError = "make backend executable: " + err.Error()
			return
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		plugin.BackendError = err.Error()
		return
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dataDir := filepath.Join(plugin.dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		plugin.BackendError = err.Error()
		return
	}
	command := exec.Command(fullCommand)
	command.Dir = plugin.dir
	command.Env = append(os.Environ(),
		"VOCAT_PLUGIN_ID="+plugin.ID,
		"VOCAT_PLUGIN_LISTEN="+address,
		"VOCAT_PLUGIN_DATA_DIR="+dataDir,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		plugin.BackendError = err.Error()
		return
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		plugin.BackendError = err.Error()
		return
	}
	plugin.command = command
	plugin.backend = &url.URL{Scheme: "http", Host: address}
	plugin.BackendRunning = true
	plugin.BackendError = ""
	go manager.captureOutput(plugin.ID, stdout)
	go manager.waitProcess(plugin.ID, command)
}

func (manager *Manager) captureOutput(id string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		manager.logger.Info("plugin output", "plugin", id, "message", scanner.Text())
	}
}

func (manager *Manager) waitProcess(id string, command *exec.Cmd) {
	err := command.Wait()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	plugin := manager.plugins[id]
	if plugin == nil || plugin.command != command {
		return
	}
	plugin.command = nil
	plugin.backend = nil
	plugin.BackendRunning = false
	if err != nil && plugin.Enabled {
		plugin.BackendError = err.Error()
		manager.logger.Warn("plugin backend exited", "plugin", id, "error", err)
	}
}

func (manager *Manager) stopLocked(plugin *Plugin) {
	command := plugin.command
	plugin.command = nil
	plugin.backend = nil
	plugin.BackendRunning = false
	if command != nil && command.Process != nil {
		_ = command.Process.Signal(os.Interrupt)
		go func() {
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			<-timer.C
			_ = command.Process.Kill()
		}()
	}
}

func (manager *Manager) Close() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, plugin := range manager.plugins {
		manager.stopLocked(plugin)
	}
}

func publicPlugin(plugin *Plugin) Plugin {
	copy := *plugin
	copy.command = nil
	copy.backend = nil
	return copy
}
