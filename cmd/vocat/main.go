package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
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
	"sync"
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
	"vocat/internal/pcsc"
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

const (
	flightModeTransitionTimeout = 45 * time.Second
	deviceStartupTimeout        = 4 * time.Minute
)

func deviceStartupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), deviceStartupTimeout)
}

func startupFlightModeContext(parent context.Context) (context.Context, context.CancelFunc) {
	// Bound each EC20 CFUN transition inside the larger device-startup budget.
	return context.WithTimeout(parent, flightModeTransitionTimeout)
}

func main() {
	logs := loghub.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}), 2000)
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
	case "doctor":
		if err := runDoctor(rest); err != nil {
			logger.Error("doctor failed", "error", err)
			os.Exit(1)
		}
	case "carrier":
		if err := runCarrier(rest, os.Stdout); err != nil {
			logger.Error("carrier command failed", "error", err)
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
	case "bootstrap-admin":
		// Installer-only command. The password is read from stdin so it never
		// appears in argv, an environment file, or process listings.
		if err := runBootstrapAdmin(rest); err != nil {
			logger.Error("bootstrap admin failed", "error", err)
			os.Exit(1)
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
	carrierProfileDir := filepath.Join(filepath.Dir(cfg.DatabasePath), "carrier-profiles.d")
	if err := vowifi.LoadCarrierProfileDirectory(carrierProfileDir); err != nil {
		return fmt.Errorf("load installed carrier profiles: %w", err)
	}
	instanceLock, err := lockServerInstance(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer instanceLock.Close()
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
	if _, adminErr := database.CurrentAdmin(startupContext); adminErr != nil {
		if errors.Is(adminErr, store.ErrNotFound) {
			return errors.New("administrator is not initialized; run vocat bootstrap-admin before starting the service")
		}
		return fmt.Errorf("read administrator: %w", adminErr)
	}

	cardReaders := pcsc.New()
	deviceLogger := logger.With("category", "hardware")
	deviceManager, err := device.NewManager(device.Options{CardReaders: cardReaders, Logger: deviceLogger})
	if err != nil {
		return fmt.Errorf("create device manager: %w", err)
	}
	// Device startup has a separate budget because EC20 CFUN transitions can
	// temporarily remove the USB serial transport, and OpenStick 410 deliberately
	// delays its persisted VoWiFi policy after a cold boot. Do not reuse the short
	// database/configuration deadline for this hardware lifecycle.
	deviceStartupContext, cancelDeviceStartup := deviceStartupContext(startupContext)
	defer cancelDeviceStartup()
	if err := deviceManager.Start(deviceStartupContext); err != nil {
		logger.Warn("device discovery is not available at startup", "error", err)
	}
	if err := provisionDiscoveredDevices(deviceStartupContext, database, deviceManager); err != nil {
		logger.Warn("automatic first-run device provisioning failed", "error", err)
	}
	configureDeviceBackends(deviceStartupContext, logger, database, deviceManager)
	restoreDefaultCellularRadios(deviceStartupContext, logger, database, deviceManager)
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := deviceManager.Stop(stopContext); err != nil {
			logger.Warn("stop device manager", "error", err)
		}
	}()
	pollContext, cancelPolling := context.WithCancel(context.Background())
	defer cancelPolling()
	go pollDeviceSnapshots(pollContext, deviceLogger, database, deviceManager)
	go collectCellularTraffic(pollContext, logger, database)
	go persistLogsToStore(pollContext, logger, logs, database)
	if !developerEnabled {
		go disableAllDeveloperCellularData(pollContext, logger, database, deviceManager)
	} else {
		go watchDeveloperDisable(pollContext, logger, database, deviceManager, exportProxyManager, legacyExportProxyConfig)
	}

	var onIncomingCall func(context.Context, ims.ReceivedCall) error

	vowifiManager, err := configureVoWiFiRuntime(
		deviceStartupContext,
		logger,
		database,
		deviceManager,
		cardReaders,
		func(ctx context.Context, call ims.ReceivedCall) error {
			if onIncomingCall != nil {
				return onIncomingCall(ctx, call)
			}
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("configure VoWiFi runtime: %w", err)
	}
	go reconcileCardPolicies(pollContext, logger, database, deviceManager, vowifiManager)
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
	onIncomingCall = func(ctx context.Context, call ims.ReceivedCall) error {
		deviceConfig, _ := database.Device(ctx, call.DeviceID)
		handler.NotifyIncomingCall(ctx, server.IncomingCallNotification{
			DeviceID:    call.DeviceID,
			DeviceName:  strings.TrimSpace(deviceConfig.Name),
			DeviceLabel: firstNonEmpty(deviceConfig.Name, deviceConfig.ID, "--"),
			Caller:      call.Caller,
			Called:      call.Called,
			Time:        call.Timestamp,
			Environment: "vowifi",
		})
		return nil
	}
	go handler.StartLogRetentionLoop(pollContext, time.Minute)
	go handler.StartSMSSyncLoop(pollContext, 15*time.Second)
	handler.StartCellularDataReconciler(pollContext)
	handler.StartTelegramBot(pollContext)
	handler.StartSMSNotificationDispatchers(pollContext)
	go handler.StartCellularCallMonitor(pollContext)
	handler.StartAutomaticTasks(pollContext)

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
		cancelDeviceStartup()
		_ = protocolMux.Close()
		return err
	case <-signalContext.Done():
		// Stop delayed OpenStick 410 startup work before the deferred VoWiFi
		// manager shutdown begins, so it cannot enqueue a late enable request.
		cancelDeviceStartup()
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

func configureDeviceBackends(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
) {
	configs, err := database.ListDevices(ctx)
	if err != nil {
		logger.Warn("configure device backends: list devices", "error", err)
		return
	}
	mapper := integration.ATMapper{Store: database, Devices: manager}
	for _, config := range configs {
		entry, mapErr := mapper.Get(config.ID)
		if mapErr != nil {
			continue
		}
		if config.DeviceType == store.DeviceTypeUSBSIMReader {
			if err := manager.SetSIMPin(entry.ID, config.SIMPIN); err != nil {
				logger.Warn("configure USB SIM reader", "device_id", config.ID, "error", err)
			}
			continue
		}
		if err := manager.SetBackend(entry.ID, config.DeviceBackend); err != nil {
			logger.Warn("configure device backend", "device_id", config.ID, "backend", config.DeviceBackend, "error", err)
		}
	}
}

// restoreDefaultCellularRadios applies an explicitly saved cellular policy
// after restart. Missing policies remain RF-off and are claimed by the safe
// default policy; there is no automatic cellular fallback.
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
		if config.DeviceType == store.DeviceTypeUSBSIMReader {
			continue
		}
		if config.VoWiFiEnabled {
			continue
		}
		entry, err := mapper.Get(config.ID)
		if err != nil || entry.Snapshot == nil || !entry.Snapshot.FlightMode {
			continue
		}
		iccid := strings.TrimSpace(entry.Snapshot.ICCID)
		if iccid == "" {
			continue
		}
		if iccid != "" {
			policy, policyErr := database.CardPolicy(ctx, iccid)
			switch {
			case policyErr == nil && policy.AirplaneEnabled:
				continue
			case errors.Is(policyErr, store.ErrNotFound):
				continue
			case policyErr != nil && !errors.Is(policyErr, store.ErrNotFound):
				logger.Warn("startup cellular recovery: read card policy", "device_id", config.ID, "error", policyErr)
				continue
			}
		}
		restoreContext, cancel := startupFlightModeContext(ctx)
		_, err = manager.SetFlight(restoreContext, entry.ID, false)
		cancel()
		if err != nil {
			logger.Warn("startup cellular recovery failed", "device_id", config.ID, "error", err)
			continue
		}
		logger.Info("restored cellular radio after disabled VoWiFi", "device_id", config.ID, "iccid", iccid)
	}
}

func configuredCellularNetworkRequest(
	ctx context.Context,
	database *store.Store,
	config store.Device,
	snapshot *device.Snapshot,
) device.NetworkRequest {
	request := device.NetworkRequest{
		Enabled: true, APN: config.APN, IPVersion: "IPV4V6", Backend: config.DeviceBackend,
	}
	if snapshot == nil {
		return request
	}
	iccid := strings.TrimSpace(snapshot.ICCID)
	policy, err := database.CardPolicy(ctx, iccid)
	if err != nil {
		return request
	}
	request.APN = policy.APN
	if policy.IPVersion != "" {
		request.IPVersion = policy.IPVersion
	}
	profile, err := database.CardAPNProfileByAPN(ctx, iccid, policy.APN, policy.IPVersion)
	if err != nil {
		return request
	}
	request.Username = profile.Username
	request.Password = profile.Password
	request.Authentication = profile.AuthType
	if snapshot.RegistrationStatus == 5 && profile.RoamingIPVersion != "" {
		request.IPVersion = profile.RoamingIPVersion
	}
	return request
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
		if config.DeviceType == store.DeviceTypeUSBSIMReader {
			continue
		}
		entry, err := mapper.Get(config.ID)
		if err != nil {
			continue
		}
		disableContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = manager.SetNetwork(disableContext, entry.ID, device.NetworkRequest{Enabled: false, Backend: config.DeviceBackend})
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("developer cleanup: stop cellular data", "device_id", config.ID)
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
	cardReaders *pcsc.Service,
	onIncomingCall func(context.Context, ims.ReceivedCall) error,
) (*vowifiruntime.Manager, error) {
	mapper := integration.ATMapper{
		Store:   database,
		Devices: deviceManager,
	}
	ec20Adapter, err := vowifi.NewEC20Adapter(mapper, vowifi.EC20AdapterOptions{
		// The test deployment is deliberately non-cellular. VoWiFi teardown
		// may restore CFUN, but it must never reactivate a PDP context.
		RestoreCellularData: false,
		// VoWiFi is always fail-closed with respect to cellular RF. Its teardown
		// leaves CFUN=4; only the explicit airplane-mode-off endpoint restores
		// CFUN=1.
		PureAirplanePolicy: func(deviceID string) bool {
			deviceConfig, configErr := database.Device(context.Background(), deviceID)
			return configErr == nil && deviceConfig.VoWiFiEnabled
		},
	})
	if err != nil {
		return nil, err
	}
	nativeQMIAdapter, err := vowifi.NewNativeQMIAdapter(nativeQMIControllerMapper{Mapper: mapper, Devices: deviceManager}, func(deviceID string) bool {
		deviceConfig, configErr := database.Device(context.Background(), deviceID)
		return configErr == nil && deviceConfig.VoWiFiEnabled
	})
	if err != nil {
		return nil, err
	}
	pcscAdapter, err := vowifi.NewPCSCAdapter(cardReaders, func(ctx context.Context, deviceID string) (pcsc.Selector, string, error) {
		config, resolveErr := database.Device(ctx, strings.TrimSpace(deviceID))
		if resolveErr != nil {
			return pcsc.Selector{}, "", resolveErr
		}
		return pcsc.Selector{USBPath: config.USBPath, ReaderName: config.ControlDevice}, config.SIMPIN, nil
	})
	if err != nil {
		return nil, err
	}
	projector := integration.StateProjector{
		Store:   database,
		Devices: mapper,
	}
	manager := vowifiruntime.New(vowifiruntime.Options{
		Logger:  logger.With("category", "vowifi"),
		OnState: projector.Save,
		Factory: func(factoryContext context.Context, deviceID string) (*vowifi.Orchestrator, error) {
			deviceConfig, err := database.Device(factoryContext, deviceID)
			if err != nil {
				return nil, fmt.Errorf("load device %q VoWiFi config: %w", deviceID, err)
			}
			adapter := vowifiDeviceAdapter(ec20Adapter)
			if deviceConfig.DeviceType == store.DeviceTypeUSBSIMReader {
				adapter = pcscAdapter
			} else if deviceConfig.DeviceType == store.DeviceTypeWiFi410 {
				adapter = nativeQMIAdapter
			}
			return newVoWiFiOrchestrator(deviceConfig, database, adapter, logger, onIncomingCall)
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
			if entry, mapErr := mapper.Get(deviceConfig.ID); mapErr == nil {
				flightErr := protectVoWiFiStartupRadio(ctx, deviceManager, entry.ID)
				if flightErr != nil {
					// A modem can be temporarily unavailable while OpenWrt/procd is
					// restarting the service (notably after loading XFRM modules). Do
					// not take the Web/API service down with it: the orchestrator below
					// remains fail-closed and its runtime manager retries until CFUN=4
					// can be established.
					logger.Warn(
						"VoWiFi startup radio protection deferred to automatic retry",
						"device_id", deviceConfig.ID,
						"error", flightErr,
					)
				}
			}
			requestEnable := func() error {
				_, requestErr := manager.RequestEnabled(deviceConfig.ID, true)
				return requestErr
			}
			if err := requestVoWiFiStartup(
				ctx,
				logger,
				deviceConfig.DeviceType,
				deviceConfig.ID,
				wifi410VoWiFiStartupDelay,
				requestEnable,
			); err != nil {
				_ = manager.Close(context.Background())
				return nil, fmt.Errorf("start device %q VoWiFi policy: %w", deviceConfig.ID, err)
			}
		}
	}
	return manager, nil
}

const (
	vowifiStartupRadioAttempts = 3
	vowifiStartupRadioDelay    = time.Second
	wifi410VoWiFiStartupDelay  = 80 * time.Second
)

// requestVoWiFiStartup delays only the persisted startup policy for OpenStick
// 410 devices. Their Qualcomm UIM and Vodafone ePDG path need a short quiet
// period after a cold boot; user-triggered reconnects and every other device
// type continue to execute immediately.
func requestVoWiFiStartup(
	ctx context.Context,
	logger *slog.Logger,
	deviceType string,
	deviceID string,
	delay time.Duration,
	request func() error,
) error {
	if deviceType != store.DeviceTypeWiFi410 || delay <= 0 {
		return request()
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(
		"OpenStick 410 VoWiFi startup delayed",
		"device_id", deviceID,
		"delay", delay,
	)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := request(); err != nil {
			logger.Warn(
				"OpenStick 410 delayed VoWiFi startup failed",
				"device_id", deviceID,
				"error", err,
			)
		}
	}()
	return nil
}

func shouldDelayWiFi410VoWiFi(deviceType string, now, notBefore time.Time) bool {
	return deviceType == store.DeviceTypeWiFi410 && now.Before(notBefore)
}

type flightModeSetter interface {
	SetFlight(context.Context, string, bool) (device.FlightResult, error)
}

func protectVoWiFiStartupRadio(ctx context.Context, manager flightModeSetter, physicalID string) error {
	return protectVoWiFiStartupRadioWithRetry(
		ctx,
		manager,
		physicalID,
		vowifiStartupRadioAttempts,
		vowifiStartupRadioDelay,
	)
}

func protectVoWiFiStartupRadioWithRetry(
	ctx context.Context,
	manager flightModeSetter,
	physicalID string,
	attempts int,
	delay time.Duration,
) error {
	if attempts <= 0 {
		return nil
	}
	retryBudget := time.Duration(attempts)*flightModeTransitionTimeout + time.Duration(attempts-1)*delay
	retryContext, cancelRetry := context.WithTimeout(ctx, retryBudget)
	defer cancelRetry()
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		flightContext, cancel := context.WithTimeout(retryContext, flightModeTransitionTimeout)
		_, lastErr = manager.SetFlight(flightContext, physicalID, true)
		cancel()
		if lastErr == nil {
			return nil
		}
		if attempt+1 == attempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-retryContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return errors.Join(lastErr, retryContext.Err())
		case <-timer.C:
		}
	}
	return lastErr
}

type vowifiDeviceAdapter interface {
	vowifi.SIMIdentityReader
	vowifi.AKAProvider
	vowifi.RadioController
}

// receivedIMSSMSMessageID returns a session-independent, subscription-scoped
// identity for an IMS SMS.
func receivedIMSSMSMessageID(message ims.ReceivedSMS) string {
	if message.Concat == nil || message.Concat.Total <= 1 {
		return message.MessageID
	}
	subscriptionID := firstNonEmpty(
		strings.TrimSpace(message.ICCID),
		strings.TrimSpace(message.IMSI),
	)
	if subscriptionID == "" {
		// Without a subscription identity, reusing the concat reference after an
		// eSIM switch is indistinguishable from a later segment. Keep the IMS
		// delivery identity instead of risking a cross-profile merge.
		return message.MessageID
	}
	// A segment of a carrier-split long SMS over IMS. Address the whole
	// message by the configured device rather than the current IMS session's
	// reported IMEI, and include the subscription so a later eSIM profile cannot
	// reuse the same sender/reference tuple and merge into the old message.
	fingerprint := sha256.Sum256([]byte(subscriptionID))
	deviceSubscription := message.DeviceID + ":subscription:" + hex.EncodeToString(fingerprint[:])
	return store.StableConcatMessageID(
		"ims", "", deviceSubscription, message.From,
		message.Concat.Reference, message.Concat.Total,
	)
}

func newVoWiFiOrchestrator(
	deviceConfig store.Device,
	database *store.Store,
	adapter vowifiDeviceAdapter,
	logger *slog.Logger,
	onIncomingCall func(context.Context, ims.ReceivedCall) error,
) (*vowifi.Orchestrator, error) {
	apn := deviceConfig.APN
	if apn == "" {
		apn = "ims"
	}
	vowifiLogger := logger.With("category", "vowifi", "device_id", deviceConfig.ID)
	tunnelProvider, err := ike.NewProvider(ike.Config{
		APN: apn, Logger: vowifiLogger, AutoProposalFallback: true,
	})
	if err != nil {
		return nil, fmt.Errorf("device %q IKE provider: %w", deviceConfig.ID, err)
	}
	imsProvider, err := ims.NewProvider(adapter, ims.Config{
		Logger: vowifiLogger,
		// Carrier-specific transport and SMSC defaults live in the shared data
		// profile. Prefer network-provided P-CSCF hints, then safely try the
		// alternate transport only if no SIP response was observed.
		Transport:             "tcp",
		AutoTransportFallback: true,
		OnIncomingCall:        onIncomingCall,
		OnSMS: func(ctx context.Context, message ims.ReceivedSMS) error {
			localPhone, _ := database.PhoneNumberForICCID(ctx, message.ICCID)
			modemIMEI := firstNonEmpty(message.ModemIMEI, deviceConfig.ModemIMEI)
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
				"decode_error":             message.DecodeError,
			})
			partsTotal := 1
			if message.Concat != nil && message.Concat.Total > 0 {
				partsTotal = message.Concat.Total
			}
			messageID := receivedIMSSMSMessageID(message)
			_, saveErr := database.SaveSMSMessage(ctx, store.SMSMessage{
				MessageID:  messageID,
				DeviceID:   message.DeviceID,
				ModemIMEI:  modemIMEI,
				ICCID:      message.ICCID,
				IMSI:       message.IMSI,
				LocalPhone: localPhone,
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
				ModemIMEI:         firstNonEmpty(report.ModemIMEI, deviceConfig.ModemIMEI),
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
		OnUSSD: func(ctx context.Context, message ims.ReceivedUSSD) error {
			localPhone, _ := database.PhoneNumberForICCID(ctx, message.ICCID)
			extra, _ := json.Marshal(map[string]any{
				"transport":   "ims-ussd",
				"dcs":         message.DCS,
				"call_id":     message.CallID,
				"received_at": message.Timestamp,
				"raw_body":    message.RawBody,
			})
			_, saveErr := database.SaveSMSMessage(ctx, store.SMSMessage{
				MessageID:  message.MessageID,
				DeviceID:   message.DeviceID,
				ModemIMEI:  deviceConfig.ModemIMEI,
				ICCID:      message.ICCID,
				IMSI:       message.IMSI,
				LocalPhone: localPhone,
				Peer:       message.From,
				Direction:  "inbound",
				Body:       message.Text,
				Timestamp:  message.Timestamp,
				Status:     "received",
				Source:     "ims-ussd",
				PartsTotal: 1,
				Read:       false,
				Extra:      extra,
			})
			return saveErr
		},
	})
	if err != nil {
		return nil, fmt.Errorf("device %q IMS provider: %w", deviceConfig.ID, err)
	}
	orchestrator, err := vowifi.New(vowifi.Dependencies{
		SIM:    adapter,
		AKA:    adapter,
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
		esimTransport := backend
		if candidate.QMIControl != "" {
			backend = "qmi"
			control = candidate.QMIControl
			esimTransport = backend
		}
		if candidate.HardwareKind == pcsc.HardwareKind {
			backend = "pcsc"
			control = candidate.ReaderName
			deviceType = store.DeviceTypeUSBSIMReader
			esimTransport = "pcsc"
		}
		name := candidate.Product
		if name == "" || strings.EqualFold(name, "Android") {
			name = "Quectel EC20 / EC25"
		}
		supportsSMS := deviceType != store.DeviceTypeWiFi410
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
			ESIMTransport:  esimTransport,
			NetworkEnabled: false,
			SMSEnabled:     supportsSMS,
			VoWiFiEnabled:  true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func provisionedDeviceType(candidate modem.Candidate) string {
	controlName := filepath.Base(filepath.Clean(candidate.QMIControl))
	if candidate.HardwareKind == "wwan" &&
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
			if loghub.IsHTTPAccessEntry(entry) {
				continue
			}
			entry = loghub.SanitizeEntry(entry)
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
		// Hotplug can replace the physical discovery ID. Rebind each configured
		// device's selected QMI/AT control plane before collecting its snapshot.
		configureDeviceBackends(ctx, logger, database, manager)
		entries := manager.List()
		// Each physical modem owns its own operation lock. Refresh them in
		// parallel so a slow or wedged EC20 on one hub port cannot delay signal
		// and identity updates for every other modem by 30 seconds at a time.
		var refreshGroup sync.WaitGroup
		refreshSlots := make(chan struct{}, 4)
		for _, entry := range entries {
			if !entry.Discovered || entry.Candidate.DiscoveryIssue != "" {
				continue
			}
			entry := entry
			refreshGroup.Add(1)
			go func() {
				defer refreshGroup.Done()
				select {
				case refreshSlots <- struct{}{}:
					defer func() { <-refreshSlots }()
				case <-ctx.Done():
					return
				}
				refreshContext, cancelRefresh := context.WithTimeout(ctx, 30*time.Second)
				snapshot, refreshErr := manager.Refresh(refreshContext, entry.ID)
				cancelRefresh()
				if refreshErr != nil && ctx.Err() == nil {
					logger.Warn("modem snapshot refresh failed", "device_id", entry.ID, "error", refreshErr)
				}
				if refreshErr == nil && ctx.Err() == nil {
					enforceCardRegion(ctx, logger, database, manager, entry.ID, &snapshot)
					enforceDefaultSafeCardPolicy(ctx, logger, database, manager, entry.ID, &snapshot)
				}
			}()
		}
		refreshGroup.Wait()
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

// enforceDefaultSafeCardPolicy handles a newly inserted physical SIM or a
// profile that has never had a policy. RF is turned off before the default is
// persisted; the VoWiFi runtime reconciler then starts service asynchronously.
func enforceDefaultSafeCardPolicy(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
	physicalID string,
	snapshot *device.Snapshot,
) {
	if snapshot == nil || !snapshot.SIMReady || strings.TrimSpace(snapshot.ICCID) == "" ||
		device.RegionBlockReason(snapshot.IMSI) != "" {
		return
	}
	iccid := strings.TrimSpace(snapshot.ICCID)
	if _, err := database.CardPolicy(ctx, iccid); err == nil {
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		logger.Warn("default card policy: read policy", "iccid", iccid, "error", err)
		return
	}
	flightContext, cancel := context.WithTimeout(ctx, flightModeTransitionTimeout)
	_, err := manager.SetFlight(flightContext, physicalID, true)
	cancel()
	if err != nil {
		logger.Warn("default card policy: failed to establish airplane mode", "device_id", physicalID, "iccid", iccid, "error", err)
		return
	}
	if err := database.UpsertCardPolicy(ctx, store.CardPolicy{
		ICCID: iccid, VoWiFiEnabled: true, AirplaneEnabled: true,
		IPVersion: "IPV4V6", Source: "default",
	}); err != nil {
		logger.Warn("default card policy: persist policy", "iccid", iccid, "error", err)
		return
	}
	mapper := integration.ATMapper{Store: database, Devices: manager}
	configs, err := database.ListDevices(ctx)
	if err != nil {
		return
	}
	for _, config := range configs {
		entry, mapErr := mapper.Get(config.ID)
		if mapErr != nil || entry.ID != physicalID {
			continue
		}
		config.NetworkEnabled = false
		config.VoWiFiEnabled = true
		if err := database.UpsertDevice(ctx, config); err != nil {
			logger.Warn("default card policy: update device policy", "device_id", config.ID, "error", err)
		}
		break
	}
	logger.Info("new SIM protected by default VoWiFi/airplane policy", "device_id", physicalID, "iccid", iccid)
}

func reconcileCardPolicies(
	ctx context.Context,
	logger *slog.Logger,
	database *store.Store,
	manager *device.Manager,
	vowifiManager *vowifiruntime.Manager,
) {
	observedCards := make(map[string]string)
	wifi410StartupNotBefore := time.Now().Add(wifi410VoWiFiStartupDelay)
	reconcile := func() {
		policies, policyListErr := database.ListCardPolicies(ctx)
		if policyListErr == nil {
			for _, policy := range policies {
				if !policy.VoWiFiEnabled || (policy.AirplaneEnabled && !policy.NetworkEnabled) {
					continue
				}
				policy.AirplaneEnabled = true
				policy.NetworkEnabled = false
				if err := database.UpsertCardPolicy(ctx, policy); err != nil {
					logger.Warn("reconcile card policy: normalize stored RF-safe VoWiFi policy", "iccid", policy.ICCID, "error", err)
				}
			}
		}
		configs, err := database.ListDevices(ctx)
		if err != nil {
			return
		}
		mapper := integration.ATMapper{Store: database, Devices: manager}
		for _, config := range configs {
			entry, mapErr := mapper.Get(config.ID)
			if mapErr != nil || entry.Snapshot == nil {
				if config.DeviceType == store.DeviceTypeUSBSIMReader && observedCards[config.ID] != "missing" {
					if state, stateErr := vowifiManager.State(config.ID); stateErr == nil && state.ICCID != "" {
						_, _ = vowifiManager.RequestReconnect(config.ID)
					}
					observedCards[config.ID] = "missing"
				}
				continue
			}
			iccid := strings.TrimSpace(entry.Snapshot.ICCID)
			if iccid == "" {
				if config.DeviceType == store.DeviceTypeUSBSIMReader && observedCards[config.ID] != "missing" {
					if state, stateErr := vowifiManager.State(config.ID); stateErr == nil && state.ICCID != "" {
						_, _ = vowifiManager.RequestReconnect(config.ID)
					}
					observedCards[config.ID] = "missing"
				}
				continue
			}
			previousObserved := observedCards[config.ID]
			observedCards[config.ID] = iccid
			policy, policyErr := database.CardPolicy(ctx, iccid)
			if policyErr != nil {
				continue
			}
			if policy.VoWiFiEnabled && (!policy.AirplaneEnabled || policy.NetworkEnabled) {
				policy.AirplaneEnabled = true
				policy.NetworkEnabled = false
				if err := database.UpsertCardPolicy(ctx, policy); err != nil {
					logger.Warn("reconcile card policy: normalize RF-safe VoWiFi policy", "device_id", config.ID, "iccid", iccid, "error", err)
					continue
				}
			}
			deviceChanged := false
			if config.VoWiFiEnabled != policy.VoWiFiEnabled || (policy.VoWiFiEnabled && config.NetworkEnabled) {
				config.VoWiFiEnabled = policy.VoWiFiEnabled
				if policy.VoWiFiEnabled {
					config.NetworkEnabled = false
				}
				deviceChanged = true
			}
			if config.APN != strings.TrimSpace(policy.APN) {
				config.APN = strings.TrimSpace(policy.APN)
				deviceChanged = true
			}
			if deviceChanged {
				if err := database.UpsertDevice(ctx, config); err != nil {
					logger.Warn("reconcile card policy: update device", "device_id", config.ID, "error", err)
					continue
				}
			}
			state, stateErr := vowifiManager.State(config.ID)
			if policy.VoWiFiEnabled {
				if !entry.Snapshot.FlightMode {
					flightContext, cancel := context.WithTimeout(ctx, flightModeTransitionTimeout)
					_, _ = manager.SetFlight(flightContext, entry.ID, true)
					cancel()
				}
				switch {
				case stateErr != nil || !state.Enabled:
					if shouldDelayWiFi410VoWiFi(config.DeviceType, time.Now(), wifi410StartupNotBefore) {
						continue
					}
					_, _ = vowifiManager.RequestEnabled(config.ID, true)
				case state.ICCID != "" && !strings.EqualFold(strings.TrimSpace(state.ICCID), iccid):
					_, _ = vowifiManager.RequestReconnect(config.ID)
				case config.DeviceType == store.DeviceTypeUSBSIMReader && previousObserved == "missing":
					_, _ = vowifiManager.RequestReconnect(config.ID)
				}
				continue
			}
			if stateErr == nil && state.Enabled {
				_, _ = vowifiManager.RequestEnabled(config.ID, false)
				continue
			}
			if policy.AirplaneEnabled != entry.Snapshot.FlightMode {
				flightContext, cancel := context.WithTimeout(ctx, flightModeTransitionTimeout)
				_, _ = manager.SetFlight(flightContext, entry.ID, policy.AirplaneEnabled)
				cancel()
			}
		}
	}
	reconcile()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
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
			flightContext, cancelFlight := context.WithTimeout(ctx, flightModeTransitionTimeout)
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
			policy, policyErr := database.CardPolicy(ctx, snapshot.ICCID)
			if errors.Is(policyErr, store.ErrNotFound) {
				policy = store.CardPolicy{ICCID: snapshot.ICCID, IPVersion: "IPV4V6"}
				policyErr = nil
			}
			policy.NetworkEnabled = false
			policy.VoWiFiEnabled = false
			policy.AirplaneEnabled = true
			policy.Source = cardPolicySourceRegionBlock
			if policyErr != nil && ctx.Err() == nil {
				logger.Warn("region block: failed to read card policy", "device_id", id, "iccid", snapshot.ICCID, "error", policyErr)
			} else if err := database.UpsertCardPolicy(ctx, policy); err != nil && ctx.Err() == nil {
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

// liftCardRegionBlock removes the regional marker once an allowed SIM is
// confirmed. It deliberately does not restore RF: the replacement SIM is
// picked up by enforceDefaultSafeCardPolicy and remains in airplane/VoWiFi
// mode until an explicit user action.
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
	for _, policy := range outstanding {
		if err := database.DeleteCardPolicy(ctx, policy.ICCID); err != nil && ctx.Err() == nil {
			logger.Warn(
				"region block: failed to clear auto policy",
				"iccid", policy.ICCID, "error", err,
			)
		}
	}
	logger.Info(
		"region marker removed; allowed SIM remains RF protected",
		"device_id", id, "iccid", snapshot.ICCID, "imsi", snapshot.IMSI,
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
