package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"vocat/internal/auth"
	"vocat/internal/config"
	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/exportproxy"
	"vocat/internal/extensions"
	"vocat/internal/httpsmode"
	"vocat/internal/loghub"
	"vocat/internal/modem"
	"vocat/internal/server"
	"vocat/internal/store"
	"vocat/internal/update"
	"vocat/internal/vowifi"
	"vocat/internal/vowifi/ike"
	"vocat/internal/vowifi/ims"
	"vocat/internal/vowifi/integration"
	vowifiruntime "vocat/internal/vowifi/runtime"
	"vocat/web"
)

func main() {
	logs := loghub.New(slog.NewJSONHandler(os.Stdout, nil), 2000)
	logger := slog.New(logs)

	args := os.Args[1:]
	switch subcommand, rest := splitSubcommand(args); subcommand {
	case "":
		// No subcommand: TTY+root → interactive menu (operator on the host);
		// otherwise run the server. systemd runs vocat with stdin=/dev/null
		// (non-TTY) so the unit keeps starting the server unchanged. Non-root
		// on a TTY also falls through to the server rather than erroring on
		// runMenu's root requirement.
		if term.IsTerminal(int(os.Stdin.Fd())) && os.Geteuid() == 0 {
			if err := runMenu(logger); err != nil {
				logger.Error("menu failed", "error", err)
				os.Exit(1)
			}
		} else {
			if err := run(logger, logs); err != nil {
				logger.Error("server stopped", "error", err)
				os.Exit(1)
			}
		}
	case "serve":
		// Explicit foreground server. Use this when vocat with no arguments
		// would otherwise enter the menu (root on a TTY) but a server is wanted.
		if err := run(logger, logs); err != nil {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	case "version", "-v", "--version":
		runVersion()
	case "update":
		if err := update.Run(logger, rest); err != nil {
			logger.Error("update failed", "error", err)
			os.Exit(1)
		}
	case "menu":
		if err := runMenu(logger); err != nil {
			logger.Error("menu failed", "error", err)
			os.Exit(1)
		}
	case "develop":
		// Hidden subcommand: intentionally not listed in printUsage or the
		// interactive menu. It toggles the developer-mode flag that gates the
		// entire plugin/extension system; the flag takes effect on next start.
		if err := runDevelop(rest, logger); err != nil {
			logger.Error("develop failed", "error", err)
			os.Exit(2)
		}
	case "help", "-h", "--help":
		printUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "vocat: unknown subcommand %q\n\n", subcommand)
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

// splitSubcommand returns the first non-flag token as the subcommand and the
// remaining args. An empty arg list yields ("", nil) → server mode.
func splitSubcommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}

func run(logger *slog.Logger, logs *loghub.Hub) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if cfg.UsesDefaultCredentials() {
		logger.Warn(
			"default admin credentials are active; set VOCAT_ADMIN_PASSWORD before exposing the service",
		)
	}

	startupContext, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()

	database, err := store.Open(startupContext, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	developerEnabled := isDeveloperEnabled(startupContext, database)
	pluginRoot := filepath.Join(filepath.Dir(cfg.DatabasePath), "plugins")
	legacyExportProxyConfig := filepath.Join(pluginRoot, exportproxy.ReservedID, "data", "configs.json")
	if !developerEnabled {
		if err := developer.ResetExperimental(startupContext, database); err != nil {
			return fmt.Errorf("reset disabled developer settings: %w", err)
		}
		if err := exportproxy.RemoveLegacyConfig(legacyExportProxyConfig); err != nil {
			return fmt.Errorf("remove legacy export proxy configuration: %w", err)
		}
	}
	httpsManager, err := httpsmode.New(
		startupContext,
		database,
		filepath.Join(filepath.Dir(cfg.DatabasePath), "tls"),
		cfg.Address,
	)
	if err != nil {
		return fmt.Errorf("configure self-signed HTTPS: %w", err)
	}

	// The plugin/extension system is gated behind a hidden developer-mode flag.
	// When off (the default) the manager is never created and the server receives
	// a nil Extensions handle, so every /extensions* and /plugin-assets/* route
	// returns 503/404 and the SPA hides the plugin surface.
	var extensionManager *extensions.Manager
	var exportProxyManager *exportproxy.Manager
	if developerEnabled {
		exportProxyManager, err = exportproxy.New(startupContext, database, logger, legacyExportProxyConfig)
		if err != nil {
			return fmt.Errorf("create built-in export proxy: %w", err)
		}
		defer exportProxyManager.Close()
		extensionManager, err = extensions.NewManager(
			pluginRoot,
			logger,
		)
		if err != nil {
			return fmt.Errorf("create plugin manager: %w", err)
		}
		defer extensionManager.Close()
	} else {
		logger.Info("developer mode is off; plugin system disabled")
	}

	authService, err := auth.New(database, auth.Options{
		SessionTTL: cfg.SessionTTL,
	})
	if err != nil {
		return err
	}
	if err := authService.EnsureAdmin(
		startupContext,
		cfg.AdminUsername,
		cfg.AdminPassword,
	); err != nil {
		return err
	}

	deviceManager, err := device.NewManager(device.Options{})
	if err != nil {
		return fmt.Errorf("create device manager: %w", err)
	}
	if err := deviceManager.Start(startupContext); err != nil {
		logger.Warn("device discovery is not available at startup", "error", err)
	}
	if err := provisionDiscoveredDevices(startupContext, database, deviceManager); err != nil {
		logger.Warn("automatic first-run device provisioning failed", "error", err)
	}
	restoreDefaultCellularRadios(startupContext, logger, database, deviceManager)
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := deviceManager.Stop(stopContext); err != nil {
			logger.Warn("stop device manager", "error", err)
		}
	}()
	pollContext, cancelPolling := context.WithCancel(context.Background())
	defer cancelPolling()
	go pollDeviceSnapshots(pollContext, logger, database, deviceManager)
	go restoreConfiguredCellularData(pollContext, logger, database, deviceManager)
	go collectCellularTraffic(pollContext, logger, database)
	go persistLogsToStore(pollContext, logger, logs, database)
	if !developerEnabled {
		go disableAllDeveloperCellularData(pollContext, logger, database, deviceManager)
	} else {
		go watchDeveloperDisable(pollContext, logger, database, deviceManager, exportProxyManager, legacyExportProxyConfig)
	}

	vowifiManager, err := configureVoWiFiRuntime(
		startupContext,
		logger,
		database,
		deviceManager,
	)
	if err != nil {
		return fmt.Errorf("configure VoWiFi runtime: %w", err)
	}
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := vowifiManager.Close(stopContext); err != nil {
			logger.Warn("stop VoWiFi runtime", "error", err)
		}
	}()

	handler, err := server.New(server.Options{
		Store:               database,
		Auth:                authService,
		Devices:             deviceManager,
		VoWiFi:              vowifiManager,
		Logs:                logs,
		Assets:              web.Dist,
		Logger:              logger,
		SecureCookies:       cfg.SecureCookies,
		MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
		Extensions:          extensionManager,
		ExportProxy:         exportProxyManager,
		DeveloperEnabled:    developerEnabled,
		UpdateRepository:    strings.TrimSpace(os.Getenv("VOCAT_REPO")),
		UpdateToken:         strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		HTTPS:               httpsManager,
	})
	if err != nil {
		return err
	}
	go handler.StartLogRetentionLoop(pollContext, time.Minute)
	go handler.StartSMSSyncLoop(pollContext, 15*time.Second)
	handler.StartTelegramBot(pollContext)
	handler.StartSMSNotificationDispatchers(pollContext)

	serverConfig := func(handler http.Handler) *http.Server {
		return &http.Server{
			Addr:              cfg.Address,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       90 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
	}
	plainHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpsManager.Enabled() {
			host := strings.TrimSpace(r.Host)
			if host == "" {
				host = cfg.Address
			}
			http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		handler.ServeHTTP(w, r)
	})
	plainServer := serverConfig(plainHandler)
	tlsServer := serverConfig(handler)
	baseListener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Address, err)
	}
	protocolMux := httpsmode.NewMultiplexer(baseListener, httpsManager)

	signalContext, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	serverError := make(chan error, 2)
	go func() {
		logger.Info("HTTP server listening", "address", cfg.Address, "self_signed_https", httpsManager.Enabled())
		err := plainServer.Serve(protocolMux.Plain())
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverError <- err
	}()
	go func() {
		err := tlsServer.Serve(tls.NewListener(protocolMux.TLS(), httpsManager.TLSConfig()))
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverError <- err
	}()

	select {
	case err := <-serverError:
		_ = protocolMux.Close()
		return err
	case <-signalContext.Done():
		logger.Info("shutdown signal received")
	}
	// Long-lived SSE and polling handlers use this context. Stop them before
	// http.Server.Shutdown so they do not consume the entire graceful-shutdown
	// deadline while waiting for a stream that is intentionally still active.
	cancelPolling()

	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		cfg.ShutdownTimeout,
	)
	defer cancelShutdown()
	shutdownErrors := make(chan error, 2)
	go func() { shutdownErrors <- plainServer.Shutdown(shutdownContext) }()
	go func() { shutdownErrors <- tlsServer.Shutdown(shutdownContext) }()
	time.Sleep(10 * time.Millisecond)
	_ = protocolMux.Close()
	for range 2 {
		if err := <-shutdownErrors; err != nil {
			_ = plainServer.Close()
			_ = tlsServer.Close()
			return fmt.Errorf("graceful HTTP shutdown: %w", err)
		}
	}
	return nil
}

// restoreDefaultCellularRadios repairs an interrupted VoWiFi teardown. CFUN=4
// survives process restarts, while the in-memory radio checkpoint does not. If
// VoWiFi is disabled and the current SIM has no explicit airplane policy, the
// automatic/default policy is cellular service and the modem must return to
// CFUN=1.
func restoreDefaultCellularRadios(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
) {
	configs, err := database.ListDevices(ctx)
	if err != nil {
		logger.Warn("startup cellular recovery: list devices", "error", err)
		return
	}
	mapper := integration.ATMapper{Store: database, Devices: manager}
	for _, config := range configs {
		if config.VoWiFiEnabled {
			continue
		}
		entry, err := mapper.Get(config.ID)
		if err != nil || entry.Snapshot == nil || !entry.Snapshot.FlightMode {
			continue
		}
		iccid := strings.TrimSpace(entry.Snapshot.ICCID)
		if iccid != "" {
			policy, policyErr := database.CardPolicy(ctx, iccid)
			switch {
			case policyErr == nil && policy.AirplaneEnabled:
				continue
			case policyErr != nil && !errors.Is(policyErr, store.ErrNotFound):
				logger.Warn("startup cellular recovery: read card policy", "device_id", config.ID, "error", policyErr)
				continue
			}
		}
		restoreContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = manager.SetFlight(restoreContext, entry.ID, false)
		cancel()
		if err != nil {
			logger.Warn("startup cellular recovery failed", "device_id", config.ID, "error", err)
			continue
		}
		logger.Info("restored cellular radio after disabled VoWiFi", "device_id", config.ID, "iccid", iccid)
	}
}

func restoreConfiguredCellularData(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
) {
	configs, err := database.ListDevices(ctx)
	if err != nil {
		logger.Warn("startup cellular data recovery: list devices", "error", err)
		return
	}
	mapper := integration.ATMapper{Store: database, Devices: manager}
	for _, config := range configs {
		if !config.NetworkEnabled || config.VoWiFiEnabled {
			continue
		}
		entry, err := mapper.Get(config.ID)
		if err != nil {
			continue
		}
		dataContext, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, err = manager.SetNetwork(dataContext, entry.ID, device.NetworkRequest{
			Enabled: true, APN: config.APN, IPVersion: "IPV4V6",
		})
		cancel()
		if err != nil {
			logger.Warn("startup cellular data recovery failed", "device_id", config.ID, "error", err)
			continue
		}
		logger.Info("restored protected cellular data route", "device_id", config.ID, "interface", config.Interface)
	}
}

func disableAllDeveloperCellularData(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
) {
	configs, err := database.ListDevices(ctx)
	if err != nil {
		logger.Warn("developer cleanup: list devices", "error", err)
		return
	}
	mapper := integration.ATMapper{Store: database, Devices: manager}
	for _, config := range configs {
		entry, err := mapper.Get(config.ID)
		if err != nil {
			continue
		}
		disableContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = manager.SetNetwork(disableContext, entry.ID, device.NetworkRequest{Enabled: false})
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("developer cleanup: stop cellular data", "device_id", config.ID, "error", err)
		}
	}
}

func watchDeveloperDisable(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
	exportProxy *exportproxy.Manager,
	legacyConfigPath string,
) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if developer.Enabled(ctx, database) {
				continue
			}
			if exportProxy != nil {
				if err := exportProxy.DeleteAllAndDisable(ctx); err != nil && ctx.Err() == nil {
					logger.Warn("developer cleanup: delete export proxies", "error", err)
				}
			}
			if err := exportproxy.RemoveLegacyConfig(legacyConfigPath); err != nil {
				logger.Warn("developer cleanup: remove legacy export proxy configuration", "error", err)
			}
			if err := developer.ResetExperimental(ctx, database); err != nil && ctx.Err() == nil {
				logger.Warn("developer cleanup: reset settings", "error", err)
			}
			disableAllDeveloperCellularData(ctx, logger, database, manager)
			logger.Info("developer mode disabled; roaming data and export proxies were removed")
			return
		}
	}
}

func configureVoWiFiRuntime(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	deviceManager *device.Manager,
) (*vowifiruntime.Manager, error) {
	mapper := integration.ATMapper{
		Store:   database,
		Devices: deviceManager,
	}
	adapter, err := vowifi.NewEC20Adapter(mapper, vowifi.EC20AdapterOptions{
		// The test deployment is deliberately non-cellular. VoWiFi teardown
		// may restore CFUN, but it must never reactivate a PDP context.
		RestoreCellularData: false,
	})
	if err != nil {
		return nil, err
	}
	projector := integration.StateProjector{
		Store:   database,
		Devices: mapper,
	}
	manager := vowifiruntime.New(vowifiruntime.Options{
		Logger:  logger,
		OnState: projector.Save,
		Factory: func(factoryContext context.Context, deviceID string) (*vowifi.Orchestrator, error) {
			deviceConfig, err := database.Device(factoryContext, deviceID)
			if err != nil {
				return nil, fmt.Errorf("load device %q VoWiFi config: %w", deviceID, err)
			}
			return newVoWiFiOrchestrator(deviceConfig, database, adapter)
		},
	})

	configured, err := database.ListDevices(ctx)
	if err != nil {
		_ = manager.Close(context.Background())
		return nil, err
	}
	for _, deviceConfig := range configured {
		if err := manager.Ensure(ctx, deviceConfig.ID); err != nil {
			_ = manager.Close(context.Background())
			return nil, fmt.Errorf("register device %q VoWiFi runtime: %w", deviceConfig.ID, err)
		}
		if deviceConfig.VoWiFiEnabled {
			if _, err := manager.RequestEnabled(deviceConfig.ID, true); err != nil {
				_ = manager.Close(context.Background())
				return nil, fmt.Errorf("start device %q VoWiFi policy: %w", deviceConfig.ID, err)
			}
		}
	}
	return manager, nil
}

func newVoWiFiOrchestrator(
	deviceConfig store.Device,
	database *store.Store,
	adapter *vowifi.EC20Adapter,
) (*vowifi.Orchestrator, error) {
	var akaProvider vowifi.AKAProvider = adapter
	if deviceConfig.DeviceType == store.DeviceTypeWiFi410 {
		qmiProvider, err := vowifi.NewQMIUIMAKAProvider(deviceConfig.ControlDevice)
		if err != nil {
			return nil, fmt.Errorf("device %q QMI-UIM provider: %w", deviceConfig.ID, err)
		}
		akaProvider = qmiProvider
	}
	apn := deviceConfig.APN
	if apn == "" {
		apn = "ims"
	}
	tunnelProvider, err := ike.NewProvider(ike.Config{APN: apn})
	if err != nil {
		return nil, fmt.Errorf("device %q IKE provider: %w", deviceConfig.ID, err)
	}
	imsProvider, err := ims.NewProvider(akaProvider, ims.Config{
		// The userspace SWu data plane currently carries the protected P-CSCF
		// signalling path over TCP.
		Transport: "tcp",
		// Some Vodafone UK SIM profiles leave AT+CSCA empty; Vodafone publishes
		// this service-centre number for manual SMS setup.
		SMSCenter: "+447785016005",
		OnSMS: func(ctx context.Context, message ims.ReceivedSMS) error {
			extra, _ := json.Marshal(map[string]any{
				"transport":                "ims",
				"encoding":                 message.Encoding,
				"concat":                   message.Concat,
				"rp_reference":             message.RPReference,
				"call_id":                  message.CallID,
				"received_at":              message.Timestamp,
				"service_center_timestamp": message.ServiceCenterTimestamp,
				"raw_rpdu":                 message.RawRPDU,
				"raw_tpdu":                 message.RawTPDU,
			})
			partsTotal := 1
			if message.Concat != nil && message.Concat.Total > 0 {
				partsTotal = message.Concat.Total
			}
			messageID := message.MessageID
			if message.Concat != nil && message.Concat.Total > 1 {
				// A segment of a carrier-split long SMS over IMS. Address the whole
				// message with a stable id so SaveSMSMessage folds every segment
				// into one progressively merged row instead of one row per segment.
				messageID = store.StableConcatMessageID(
					"ims", deviceConfig.ModemIMEI, message.DeviceID, message.From,
					message.Concat.Reference, message.Concat.Total,
				)
			}
			_, saveErr := database.SaveSMSMessage(ctx, store.SMSMessage{
				MessageID:  messageID,
				DeviceID:   message.DeviceID,
				ModemIMEI:  deviceConfig.ModemIMEI,
				IMSI:       message.IMSI,
				Peer:       message.From,
				Direction:  "inbound",
				Body:       message.Text,
				Timestamp:  message.Timestamp,
				Status:     "received",
				Source:     "ims",
				PartsTotal: partsTotal,
				Read:       false,
				Extra:      extra,
			})
			return saveErr
		},
		OnSMSStatus: func(ctx context.Context, report ims.ReceivedSMSStatus) error {
			deliveryReport := store.SMSDeliveryReport{
				DeviceID:          report.DeviceID,
				ModemIMEI:         deviceConfig.ModemIMEI,
				IMSI:              report.IMSI,
				Peer:              report.To,
				Source:            "ims",
				MessageReference:  report.MessageReference,
				StatusCode:        report.StatusCode,
				DeliveryState:     report.DeliveryStatus,
				ServiceCenterTime: report.ServiceCenterTimestamp,
				DischargeTime:     report.DischargeTimestamp,
				ReceivedAt:        report.Timestamp,
			}
			var applyErr error
			for attempt := 0; attempt < 10; attempt++ {
				_, applyErr = database.ApplySMSDeliveryReport(ctx, deliveryReport)
				if !errors.Is(applyErr, store.ErrNotFound) {
					return applyErr
				}
				// A status report can race the API handler persisting the SIP 202
				// result. Give that write a brief chance to complete.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
			// A late report from before this process started must still be
			// acknowledged, otherwise the SMSC will keep retransmitting it.
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("device %q IMS provider: %w", deviceConfig.ID, err)
	}
	orchestrator, err := vowifi.New(vowifi.Dependencies{
		SIM:    adapter,
		AKA:    akaProvider,
		Radio:  adapter,
		Proxy:  integration.ProxyResolver{Store: database},
		Tunnel: tunnelProvider,
		IMS:    imsProvider,
		Phones: integration.PhoneStore{Store: database, DeviceID: deviceConfig.ID},
	}, vowifi.Options{
		DeviceID:           deviceConfig.ID,
		AllowIMSWithoutSMS: true,
	})
	if err != nil {
		return nil, fmt.Errorf("device %q VoWiFi orchestrator: %w", deviceConfig.ID, err)
	}
	return orchestrator, nil
}

func provisionDiscoveredDevices(
	ctx context.Context,
	database *store.Store,
	manager *device.Manager,
) error {
	configured, err := database.ListDevices(ctx)
	if err != nil {
		return err
	}
	if len(configured) != 0 {
		return nil
	}
	for _, discovered := range manager.List() {
		candidate := discovered.Candidate
		deviceType := provisionedDeviceType(candidate)
		backend := "at"
		control := candidate.ATPort.OpenPath()
		if candidate.QMIControl != "" {
			backend = "qmi"
			control = candidate.QMIControl
		}
		name := candidate.Product
		if name == "" || strings.EqualFold(name, "Android") {
			name = "Quectel EC20 / EC25"
		}
		if err := database.UpsertDevice(ctx, store.Device{
			ID:             discovered.ID,
			Name:           name,
			DeviceType:     deviceType,
			Interface:      candidate.NetworkInterface,
			ControlDevice:  control,
			ATPort:         candidate.ATPort.OpenPath(),
			USBPath:        candidate.USBPath,
			ProxyPort:      1080,
			BaudRate:       115200,
			DataBits:       8,
			StopBits:       1,
			Parity:         "none",
			DeviceBackend:  backend,
			ESIMTransport:  backend,
			NetworkEnabled: false,
			SMSEnabled:     true,
			VoWiFiEnabled:  false,
		}); err != nil {
			return err
		}
	}
	return nil
}

func provisionedDeviceType(candidate modem.Candidate) string {
	classPath := filepath.ToSlash(filepath.Clean(candidate.USBPath))
	controlName := filepath.Base(filepath.Clean(candidate.QMIControl))
	if strings.Contains(classPath, "/class/wwan/") &&
		strings.HasPrefix(controlName, "wwan") && strings.Contains(controlName, "qmi") {
		return store.DeviceTypeWiFi410
	}
	return store.DeviceTypePCIeEC20EC25
}

// persistLogsToStore subscribes to the live log hub and durably appends every
// entry to the log_events table, so runtime logs survive restarts and can be
// pruned by the configured retention policy.
func persistLogsToStore(
	ctx context.Context,
	logger *slog.Logger,
	logs *loghub.Hub,
	database *store.Store,
) {
	entries, cancel := logs.Subscribe(512)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-entries:
			if !ok {
				return
			}
			var fields json.RawMessage
			if len(entry.Fields) > 0 {
				if raw, err := json.Marshal(entry.Fields); err == nil {
					fields = raw
				}
			}
			if _, err := database.AppendLogEvent(ctx, store.LogEvent{
				Time:    entry.Time,
				Level:   entry.Level,
				Message: entry.Message,
				Caller:  entry.Caller,
				Fields:  fields,
			}); err != nil && ctx.Err() == nil {
				logger.Warn("persist log event failed", "error", err)
			}
		}
	}
}

func pollDeviceSnapshots(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
) {
	refresh := func() {
		discoveryContext, cancelDiscovery := context.WithTimeout(ctx, 10*time.Second)
		_, err := manager.Discover(discoveryContext)
		cancelDiscovery()
		if err != nil {
			logger.Debug("periodic modem discovery failed", "error", err)
			return
		}
		for _, entry := range manager.List() {
			if !entry.Discovered {
				continue
			}
			refreshContext, cancelRefresh := context.WithTimeout(ctx, 30*time.Second)
			snapshot, err := manager.Refresh(refreshContext, entry.ID)
			cancelRefresh()
			if err != nil && ctx.Err() == nil {
				logger.Warn("modem snapshot refresh failed", "device_id", entry.ID, "error", err)
			}
			if err == nil && ctx.Err() == nil {
				enforceCardRegion(ctx, logger, database, manager, entry.ID, &snapshot)
			}
		}
	}
	refresh()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

// cardPolicySourceRegionBlock marks a card policy that was written automatically
// because the inserted SIM belongs to a region the product does not serve. It
// doubles as the persistent record that the radio was forced off by us, so the
// block survives restarts and can be lifted when an allowed card is detected.
const cardPolicySourceRegionBlock = "auto_region_block"

// enforceCardRegion applies the regional service policy for one refreshed
// device. A SIM whose IMSI home MCC is blocked (mainland China, 460/461) is
// denied service: the radio is forced into airplane mode and a blocking card
// policy is persisted. The check is fail-open — it only acts on a positively
// read blocked IMSI — and the lift path only runs once the current card is
// positively confirmed to be allowed, so an unreadable IMSI never causes a
// block or a spurious restore.
func enforceCardRegion(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
	id string,
	snapshot *device.Snapshot,
) {
	if snapshot == nil || !snapshot.SIMReady {
		return
	}
	imsi := strings.TrimSpace(snapshot.IMSI)
	if imsi == "" {
		// Region unknown: hold the current state rather than block or restore.
		return
	}
	if reason := device.RegionBlockReason(imsi); reason != "" {
		if !snapshot.FlightMode {
			flightContext, cancelFlight := context.WithTimeout(ctx, 30*time.Second)
			_, err := manager.SetFlight(flightContext, id, true)
			cancelFlight()
			if err != nil && ctx.Err() == nil {
				logger.Warn(
					"region block: failed to force airplane mode",
					"device_id", id, "error", err,
				)
			}
		}
		if snapshot.ICCID != "" {
			policy := store.CardPolicy{
				ICCID:           snapshot.ICCID,
				NetworkEnabled:  false,
				VoWiFiEnabled:   false,
				AirplaneEnabled: true,
				IPVersion:       "IPV4V6",
				Source:          cardPolicySourceRegionBlock,
			}
			if err := database.UpsertCardPolicy(ctx, policy); err != nil && ctx.Err() == nil {
				logger.Warn(
					"region block: failed to persist card policy",
					"device_id", id, "iccid", snapshot.ICCID, "error", err,
				)
			}
		}
		logger.Warn(
			"blocked SIM detected; service disabled and radio forced off",
			"device_id", id, "iccid", snapshot.ICCID, "imsi", imsi, "reason", reason,
		)
		return
	}
	liftCardRegionBlock(ctx, logger, database, manager, id, snapshot)
}

// liftCardRegionBlock reverses an automatic region block once the current SIM
// is positively confirmed to be allowed. It restores the radio only when an
// outstanding auto-forced block exists, so it never overrides a flight mode the
// user enabled deliberately.
func liftCardRegionBlock(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
	id string,
	snapshot *device.Snapshot,
) {
	policies, err := database.ListCardPolicies(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn("region block: failed to list card policies", "error", err)
		}
		return
	}
	outstanding := make([]store.CardPolicy, 0, 1)
	for _, policy := range policies {
		if policy.Source == cardPolicySourceRegionBlock {
			outstanding = append(outstanding, policy)
		}
	}
	if len(outstanding) == 0 {
		return
	}
	if snapshot.FlightMode {
		flightContext, cancelFlight := context.WithTimeout(ctx, 30*time.Second)
		_, err := manager.SetFlight(flightContext, id, false)
		cancelFlight()
		if err != nil && ctx.Err() == nil {
			logger.Warn(
				"region block: failed to restore radio",
				"device_id", id, "error", err,
			)
			return
		}
	}
	for _, policy := range outstanding {
		if err := database.DeleteCardPolicy(ctx, policy.ICCID); err != nil && ctx.Err() == nil {
			logger.Warn(
				"region block: failed to clear auto policy",
				"iccid", policy.ICCID, "error", err,
			)
		}
	}
	logger.Info(
		"region block lifted; SIM is allowed",
		"device_id", id, "iccid", snapshot.ICCID, "imsi", snapshot.IMSI,
	)
}
