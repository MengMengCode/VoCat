package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/i18n"
	"vocat/internal/modem"
	"vocat/internal/pcsc"
	"vocat/internal/store"
	"vocat/internal/vowifi"
	vowifiruntime "vocat/internal/vowifi/runtime"
)

// DeviceController is the narrow hardware boundary used by the HTTP layer.
// device.Manager implements it; tests can provide a transcript-backed fake.
type DeviceController interface {
	Discover(context.Context) ([]device.Device, error)
	List() []device.Device
	Get(string) (device.Device, error)
	Refresh(context.Context, string) (device.Snapshot, error)
	ExecuteAT(context.Context, string, string) (modem.Response, error)
	Reboot(context.Context, string) error
	USSD(context.Context, string, string) (device.USSDResult, error)
	ContinueUSSD(context.Context, string, string) (device.USSDResult, error)
	CancelUSSD(context.Context, string) error
	SetFlight(context.Context, string, bool) (device.FlightResult, error)
	SetNetwork(context.Context, string, device.NetworkRequest) (device.NetworkResult, error)
	USBNetMode(context.Context, string) (device.USBNetMode, error)
	SetUSBNetMode(context.Context, string, int) (device.USBNetMode, error)
	SetUSBNetModeByPort(context.Context, string, int) (device.USBNetMode, error)
	OperatorSelection(context.Context, string) (device.OperatorSelection, error)
	SetOperatorSelection(context.Context, string, bool, string, *int) (device.OperatorSelection, error)
	ReRegisterOperator(context.Context, string) (device.OperatorSelection, error)
	ScanOperators(context.Context, string) (device.OperatorScanResult, error)
	SendSMS(context.Context, string, string, string) (device.SMSSendResult, error)
	ListSMS(context.Context, string) ([]device.SMSMessage, error)
	ReadSMS(context.Context, string, int) (device.SMSMessage, error)
	DeleteSMS(context.Context, string, int) error
	DeleteSMSFromStorage(context.Context, string, string, int) error
	ESIMInventory(context.Context, string) ([]device.EsimInventoryEntry, error)
	ESIMListProfiles(context.Context, string) (device.EsimInfo, error)
	ESIMSwitchProfile(context.Context, string, string, string) error
	ESIMDisableProfile(context.Context, string, string, string) error
	ESIMRenameProfile(context.Context, string, string, string, string) error
	ESIMDownloadProfile(context.Context, string, device.EsimDownloadParams, func(device.EsimProgress)) (*device.EsimDownloadResult, error)
	ESIMDeleteProfile(context.Context, string, string, string) (*device.EsimDeleteResult, error)
	ESIMChipInfo(context.Context, string) (*device.EsimChipInfo, error)
}

type cellularIMSController interface {
	CellularIMS(context.Context, string) (device.CellularIMSStatus, error)
	SetCellularIMS(context.Context, string, device.CellularIMSMode) (device.CellularIMSStatus, error)
}

type cellularNetworkStatusController interface {
	NetworkStatus(context.Context, string) (device.NetworkStatus, error)
}

type cellularNetworkEventController interface {
	SubscribeNetworkStatusEvents(context.Context) (<-chan string, error)
}

type cellularDeviceLifecycleController interface {
	SubscribeDeviceLifecycleEvents(context.Context) (<-chan device.DeviceLifecycleEvent, error)
}

type deviceConfigPayload struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	DeviceType         string `json:"device_type"`
	Interface          string `json:"interface"`
	ControlDevice      string `json:"control_device"`
	ATPort             string `json:"at_port"`
	USBPath            string `json:"usb_path"`
	AudioDevice        string `json:"audio_device"`
	ModemIMEI          string `json:"modem_imei"`
	SIMPIN             string `json:"sim_pin"`
	APN                string `json:"apn"`
	ProxyPort          int    `json:"proxy_port"`
	BaudRate           int    `json:"baud_rate"`
	DataBits           int    `json:"data_bits"`
	StopBits           int    `json:"stop_bits"`
	Parity             string `json:"parity"`
	DeviceBackend      string `json:"device_backend"`
	ESIMTransport      string `json:"esim_transport"`
	QMIUseProxy        bool   `json:"qmi_use_proxy"`
	QMIProxyPath       string `json:"qmi_proxy_path"`
	QMIProxyExecutable string `json:"qmi_proxy_executable"`
	NetworkEnabled     bool   `json:"network_enabled"`
	SMSEnabled         bool   `json:"sms_enabled"`
	VoWiFiEnabled      bool   `json:"vowifi_enabled"`
}

func (payload deviceConfigPayload) toStoreDevice() store.Device {
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = payload.ID
	}
	return store.Device{
		ID:                 strings.TrimSpace(payload.ID),
		Name:               name,
		DeviceType:         store.NormalizeDeviceType(payload.DeviceType),
		Interface:          strings.TrimSpace(payload.Interface),
		ControlDevice:      strings.TrimSpace(payload.ControlDevice),
		ATPort:             strings.TrimSpace(payload.ATPort),
		USBPath:            strings.TrimSpace(payload.USBPath),
		AudioDevice:        strings.TrimSpace(payload.AudioDevice),
		ModemIMEI:          strings.TrimSpace(payload.ModemIMEI),
		SIMPIN:             strings.TrimSpace(payload.SIMPIN),
		APN:                strings.TrimSpace(payload.APN),
		ProxyPort:          payload.ProxyPort,
		BaudRate:           payload.BaudRate,
		DataBits:           payload.DataBits,
		StopBits:           payload.StopBits,
		Parity:             payload.Parity,
		DeviceBackend:      payload.DeviceBackend,
		ESIMTransport:      payload.ESIMTransport,
		QMIUseProxy:        payload.QMIUseProxy,
		QMIProxyPath:       strings.TrimSpace(payload.QMIProxyPath),
		QMIProxyExecutable: strings.TrimSpace(payload.QMIProxyExecutable),
		NetworkEnabled:     payload.NetworkEnabled,
		SMSEnabled:         payload.SMSEnabled,
		VoWiFiEnabled:      payload.VoWiFiEnabled,
	}
}

func validDeviceID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			(index > 0 && (character == '.' || character == '_' || character == '-')) {
			continue
		}
		return false
	}
	return true
}

func (s *Server) routeDeviceAPI(w http.ResponseWriter, r *http.Request) bool {
	cleanPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/")
	switch cleanPath {
	case "dashboard/devices":
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": s.dashboardDevices()})
		return true
	case "dashboard/host":
		s.handleDashboardHost(w, r)
		return true
	case "devices":
		return s.handleDevices(w, r)
	case "devices/discovered":
		return s.handleDiscoveredDevices(w, r)
	case "devices/actions/rescan":
		return s.handleDeviceRescan(w, r)
	case "device-mgmt/discovered/fix-usbnet":
		return s.handleFixUSBNet(w, r)
	}

	segments := splitAPIPath(cleanPath)
	if len(segments) >= 2 && segments[0] == "devices" {
		id := segments[1]
		if id == "" {
			writeError(w, http.StatusBadRequest, "invalid_device", "device ID is empty")
			return true
		}
		return s.handleDevicePath(w, r, id, segments[2:])
	}
	return false
}

func splitAPIPath(value string) []string {
	raw := strings.Split(value, "/")
	result := make([]string, 0, len(raw))
	for _, segment := range raw {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			decoded = segment
		}
		result = append(result, decoded)
	}
	return result
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) bool {
	deviceLimit := developer.DeviceLimit(r.Context(), s.store, s.developerEnabled)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"device_limit": deviceLimit,
				"devices":      s.deviceSummaries(),
			},
		})
	case http.MethodPost:
		if s.devices == nil {
			writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
			return true
		}
		var request struct {
			Config json.RawMessage `json:"config"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		var payload deviceConfigPayload
		if err := json.Unmarshal(request.Config, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_device_config", "device config must be a valid JSON object")
			return true
		}
		if !validDeviceID(payload.ID) {
			writeError(w, http.StatusBadRequest, "invalid_device_id", "device ID must use 1-64 letters, digits, dots, underscores, or hyphens")
			return true
		}
		if strings.TrimSpace(payload.DeviceType) == "" || store.NormalizeDeviceType(payload.DeviceType) == "" {
			writeError(w, http.StatusBadRequest, "invalid_device_type", "select a supported device type")
			return true
		}
		if _, err := s.store.Device(r.Context(), payload.ID); err == nil {
			writeError(w, http.StatusConflict, "device_exists", "a device with this ID already exists")
			return true
		} else if !errors.Is(err, store.ErrNotFound) {
			s.writeStoreError(w, err)
			return true
		}
		configured, err := s.store.ListDevices(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return true
		}
		if len(configured) >= deviceLimit {
			writeError(w, http.StatusConflict, "device_limit_reached", i18n.Tf("设备数量已达上限，最多只能添加 %d 台设备", deviceLimit))
			return true
		}
		devices, err := s.devices.Discover(r.Context())
		if err != nil {
			s.writeDeviceError(w, err)
			return true
		}
		selected := findDiscoveredDevice(devices, payload)
		if selected == nil {
			writeError(w, http.StatusNotFound, "device_not_found", "the selected Linux modem was not discovered")
			return true
		}
		config := payload.toStoreDevice()
		isNative410 := config.DeviceType == store.DeviceTypeWiFi410
		// Newly added hardware starts fail-closed: RF is disabled immediately and
		// VoWiFi becomes the desired service on supported devices. Native 410
		// uses its QMI UIM/DMS/NAS adapter; only cellular SMS remains unavailable.
		config.VoWiFiEnabled = true
		if isNative410 {
			config.SMSEnabled = false
		}
		config.NetworkEnabled = false
		if !s.developerActive(r.Context()) {
			config.NetworkEnabled = false
		}
		fillConfigFromPhysical(&config, *selected)
		if pinSetter, ok := s.devices.(interface{ SetSIMPin(string, string) error }); ok {
			if err := pinSetter.SetSIMPin(selected.ID, config.SIMPIN); err != nil {
				s.writeDeviceError(w, err)
				return true
			}
		}
		if selector, ok := s.devices.(interface{ SetBackend(string, string) error }); ok {
			if err := selector.SetBackend(selected.ID, config.DeviceBackend); err != nil {
				s.writeDeviceError(w, err)
				return true
			}
		}
		if err := s.store.UpsertDevice(r.Context(), config); err != nil {
			s.writeStoreError(w, err)
			return true
		}
		// Persist the physical binding before the first RF command. A newly
		// enumerated USB modem may still be settling, or ModemManager may not yet
		// have released its AT port. Rejecting the whole add operation in that
		// transient window leaves no configured identity for the periodic recovery
		// loop to retry. The stored desired state remains fail-closed (cellular data
		// disabled, VoWiFi enabled); report the incomplete hardware transition as a
		// warning while allowing normal reconciliation to finish it later.
		flightWarning := ""
		if _, err := s.devices.SetFlight(r.Context(), selected.ID, true); err != nil {
			flightWarning = "device configuration was saved, but the modem did not enter airplane mode yet; VoCat will retry during recovery"
			s.logger.Warn("new device saved before initial airplane-mode transition completed",
				"device_id", config.ID,
				"physical_device_id", selected.ID,
				"error", device.HardwareErrorDetail(err),
			)
		}
		if selected.Snapshot != nil {
			iccid := strings.TrimSpace(selected.Snapshot.ICCID)
			if iccid != "" {
				_, policyErr := s.store.CardPolicy(r.Context(), iccid)
				if errors.Is(policyErr, store.ErrNotFound) {
					policyErr = s.store.UpsertCardPolicy(r.Context(), store.CardPolicy{
						ICCID: iccid, VoWiFiEnabled: true, AirplaneEnabled: true,
						IPVersion: "IPV4V6", Source: "default",
					})
				}
				if policyErr != nil {
					s.writeStoreError(w, policyErr)
					return true
				}
			}
		}
		if s.vowifi != nil && config.VoWiFiEnabled {
			if _, err := s.vowifi.RequestEnabled(config.ID, true); err != nil {
				s.logger.Warn("new device saved in safe airplane mode but VoWiFi start was not queued", "device_id", config.ID, "error", err)
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{
				"status":          "created",
				"id":              config.ID,
				"discovery_key":   selected.ID,
				"physical_device": s.configuredDeviceSummary(config, selected),
				"warning":         flightWarning,
			},
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

func findDiscoveredDevice(devices []device.Device, config deviceConfigPayload) *device.Device {
	if config.ModemIMEI != "" {
		for index := range devices {
			if devices[index].Snapshot != nil && devices[index].Snapshot.IMEI == config.ModemIMEI {
				return &devices[index]
			}
		}
	}
	if config.USBPath != "" {
		for index := range devices {
			if devices[index].Candidate.USBPath == config.USBPath {
				return &devices[index]
			}
		}
	}
	if config.ControlDevice != "" {
		for index := range devices {
			if devices[index].Candidate.QMIControl == config.ControlDevice {
				return &devices[index]
			}
		}
	}
	for index := range devices {
		candidate := devices[index].Candidate
		if config.ATPort != "" &&
			(candidate.ATPort.Path == config.ATPort || candidate.ATPort.OpenPath() == config.ATPort) {
			return &devices[index]
		}
	}
	return nil
}

func (s *Server) handleDiscoveredDevices(w http.ResponseWriter, r *http.Request) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	if s.devices == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"devices": []any{}}})
		return true
	}
	// This endpoint backs the add-device dialog. Always perform a new physical
	// scan instead of serving Manager.List(), which intentionally retains
	// unplugged configured devices so the main device list can show them offline.
	devices, err := s.devices.Discover(r.Context())
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	configured, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return true
	}
	result := make([]map[string]any, 0, len(devices))
	for _, entry := range devices {
		if !entry.Discovered {
			continue
		}
		candidate := entry.Candidate
		atPorts := make([]string, 0, len(candidate.Ports))
		for _, port := range candidate.Ports {
			if port.Role != modem.PortRoleDiagnostic {
				atPorts = append(atPorts, port.OpenPath())
			}
		}
		controlPath := candidate.QMIControl
		if controlPath == "" {
			controlPath = candidate.ATPort.OpenPath()
		}
		configuredID := ""
		for _, config := range configured {
			if physicalMatchesConfig(entry, config) {
				configuredID = config.ID
				break
			}
		}
		result = append(result, map[string]any{
			"hardware_kind":   candidate.HardwareKind,
			"reader_name":     candidate.ReaderName,
			"device_type":     discoveredDeviceType(candidate),
			"discovery_key":   entry.ID,
			"control_path":    controlPath,
			"net_interface":   candidate.NetworkInterface,
			"usb_path":        candidate.USBPath,
			"vendor_id":       parseHexID(candidate.VendorID),
			"product_id":      parseHexID(candidate.ProductID),
			"driver_name":     candidate.Product,
			"at_ports":        atPorts,
			"at_port":         candidate.ATPort.OpenPath(),
			"imei":            snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMEI }),
			"mode":            backendMode(candidate),
			"network_capable": candidate.HardwareKind != "pcsc" && (candidate.NetworkInterface != "" || candidate.QMIControl != ""),
			"configured":      configuredID != "",
			"configured_id":   configuredID,
			"degraded":        candidate.DiscoveryIssue != "" || (candidate.HardwareKind != "pcsc" && !candidate.HasATPort()),
			"discovery_issue": candidate.DiscoveryIssue,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"devices": result}})
	return true
}

func parseHexID(value string) int64 {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	number, _ := strconv.ParseInt(value, 16, 64)
	return number
}

func (s *Server) handleDeviceRescan(w http.ResponseWriter, r *http.Request) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return true
	}
	devices, err := s.devices.Discover(r.Context())
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"status": "ok", "devices": len(devices)},
	})
	return true
}

func (s *Server) handleDevicePath(
	w http.ResponseWriter,
	r *http.Request,
	id string,
	tail []string,
) bool {
	config, err := s.store.Device(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return true
	}
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodDelete:
			if err := s.store.DeleteDevice(r.Context(), id); err != nil {
				s.writeStoreError(w, err)
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{"deleted": true, "physical_device_untouched": true},
			})
		case http.MethodPut:
			var request struct {
				Config json.RawMessage `json:"config"`
			}
			if err := s.decodeJSON(w, r, &request); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return true
			}
			var payload deviceConfigPayload
			if err := json.Unmarshal(request.Config, &payload); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_device_config", "device config must be a valid JSON object")
				return true
			}
			if payload.ID != "" && payload.ID != id {
				writeError(w, http.StatusConflict, "immutable_device_id", "device ID cannot be changed")
				return true
			}
			next := payload.toStoreDevice()
			if !s.developerActive(r.Context()) {
				next.NetworkEnabled = false
			}
			next.ID = id
			next.CreatedAt = config.CreatedAt
			// VoWiFi/airplane transitions are transactional device actions. A
			// general config save must not silently bypass their RF-safe ordering.
			next.VoWiFiEnabled = config.VoWiFiEnabled
			if next.Name == id && strings.TrimSpace(payload.Name) == "" {
				next.Name = config.Name
			}
			if next.SIMPIN == "" || next.SIMPIN == store.SecretMask {
				next.SIMPIN = config.SIMPIN
			}
			if _, physicalID, present := s.physicalForConfig(next); present {
				if pinSetter, ok := s.devices.(interface{ SetSIMPin(string, string) error }); ok {
					if err := pinSetter.SetSIMPin(physicalID, next.SIMPIN); err != nil {
						s.writeDeviceError(w, err)
						return true
					}
				}
				if selector, ok := s.devices.(interface{ SetBackend(string, string) error }); ok {
					if err := selector.SetBackend(physicalID, next.DeviceBackend); err != nil {
						s.writeDeviceError(w, err)
						return true
					}
				}
			}
			if err := s.store.UpsertDevice(r.Context(), next); err != nil {
				s.writeStoreError(w, err)
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{"status": "saved", "config": storedDeviceConfig(next)},
			})
		default:
			w.Header().Set("Allow", "DELETE, PUT")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return true
	}

	entry, physicalID, physicalPresent := s.physicalForConfig(config)
	if config.DeviceType == store.DeviceTypeWiFi410 && native410UnsupportedOperation(tail) {
		writeError(w, http.StatusNotImplemented, "device_feature_unsupported", "this feature is not supported by the native OpenStick 410 backend")
		return true
	}
	if config.DeviceType == store.DeviceTypeUSBSIMReader && len(tail) > 0 {
		operation := strings.Join(tail, "/")
		unsupported := tail[0] == "network" || tail[0] == "operator_selection" ||
			operation == "actions/at" || operation == "actions/ussd" || operation == "actions/ussd/continue" ||
			operation == "actions/ussd/cancel" || operation == "actions/reboot" || operation == "usbnet-mode"
		if unsupported {
			writeError(w, http.StatusConflict, "wifi_calling_only_device", "USB SIM readers support WiFi Calling, IMS SMS and calls only")
			return true
		}
	}
	if len(tail) > 0 && tail[0] == "esim" {
		return s.handleESIM(w, r, tail[1:], physicalID, physicalPresent, config.ID)
	}
	switch strings.Join(tail, "/") {
	case "overview":
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"devices": []any{s.configuredDeviceOverview(config, entry, physicalPresent)}},
		})
	case "overview/stream":
		return s.handleOverviewStream(w, r, config, entry, physicalPresent)
	case "status":
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": s.configuredDeviceStatus(config, entry, physicalPresent)})
	case "config":
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"config": storedDeviceConfig(config)},
		})
	case "actions/refresh":
		if !requireMethod(w, r, http.MethodPost) {
			return true
		}
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		snapshot, err := s.devices.Refresh(r.Context(), physicalID)
		if err != nil {
			s.writeDeviceError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": snapshot})
	case "actions/at":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleAT(w, r, physicalID)
	case "actions/ussd":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleUSSD(w, r, config, physicalID)
	case "actions/ussd/continue":
		return s.handleUSSDContinue(w, r)
	case "actions/ussd/cancel":
		return s.handleUSSDCancel(w, r)
	case "actions/reboot":
		if !requireMethod(w, r, http.MethodPost) {
			return true
		}
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		desiredData := config.NetworkEnabled && !config.VoWiFiEnabled
		iccid := ""
		if entry.Snapshot != nil {
			iccid = strings.TrimSpace(entry.Snapshot.ICCID)
		}
		dataRuntime := s.cellularDataRuntime()
		invalidated := dataRuntime.invalidateWithModemPhase(config.ID, desiredData, "recovering", "", "rebooting")
		if err := s.devices.Reboot(r.Context(), physicalID); err != nil {
			dataRuntime.invalidate(config.ID, desiredData, "failed", err.Error())
			s.writeDeviceError(w, err)
			return true
		}
		s.clearPublicIP(config.ID)
		s.restoreCellularDataAfterModemReboot(config.ID, physicalID, iccid, invalidated.Revision, "manual modem reboot")
		writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{"status": "rebooting"}})
	case "flight-mode":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleFlightMode(w, r, config, physicalID)
	case "network":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleCellularData(w, r, config, physicalID)
	case "network/apns":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleAPNProfiles(w, r, physicalID)
	case "cellular-ims":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleCellularIMS(w, r, config, physicalID)
	case "network/public-ip":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		iccid := ""
		if entry.Snapshot != nil {
			iccid = entry.Snapshot.ICCID
		}
		return s.handleCellularPublicIP(w, r, config, iccid)
	case "usbnet-mode":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleUSBNetMode(w, r, physicalID)
	case "operator_selection":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleOperatorSelection(w, r, physicalID)
	case "operator_selection/reregister":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleOperatorReRegister(w, r, physicalID)
	case "operator_selection/scan":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleOperatorScan(w, r, physicalID)
	case "operator_selection/scan/stream":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleOperatorScanStream(w, r, physicalID)
	case "vowifi":
		return s.handleVoWiFiEnabled(w, r, config, physicalPresent)
	case "vowifi/actions/reconnect":
		return s.handleVoWiFiReconnect(w, r, config, physicalPresent)
	case "vowifi/e911/websheet":
		return s.handleE911Websheet(w, r, config)
	case "calls":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleCalls(w, r, config, physicalID)
	case "calls/dial", "calls/answer", "calls/hangup":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleCallAction(w, r, config, physicalID, tail[1])
	case "calls/media":
		if !s.requirePhysicalDevice(w, physicalPresent) {
			return true
		}
		return s.handleCallMedia(w, r, config)
	default:
		return false
	}
	return true
}

func native410UnsupportedOperation(tail []string) bool {
	if len(tail) == 0 {
		return false
	}
	operation := strings.Join(tail, "/")
	return tail[0] == "calls" || operation == "actions/reboot" || operation == "cellular-ims"
}

func (s *Server) handleUSBNetMode(w http.ResponseWriter, r *http.Request, physicalID string) bool {
	switch r.Method {
	case http.MethodGet:
		result, err := s.devices.USBNetMode(r.Context(), physicalID)
		if err != nil {
			s.writeDeviceError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodPatch:
		var request struct {
			Mode int `json:"mode"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		result, err := s.devices.SetUSBNetMode(r.Context(), physicalID, request.Mode)
		if err != nil {
			s.writeDeviceError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"mode": result.Mode, "name": result.Name,
				"reboot_required": true,
			},
		})
	default:
		w.Header().Set("Allow", "GET, PATCH")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

// operatorSelectionWire is the current-selection shape the SPA reads.
func operatorSelectionWire(sel device.OperatorSelection) map[string]any {
	mode := "automatic"
	if sel.Mode != 0 {
		mode = "manual"
	}
	return map[string]any{
		"mode":              mode,
		"plmn":              sel.Operator,
		"access_technology": sel.AccessTechnology,
	}
}

// accessTechnologyValue maps a RAT name (as surfaced to the UI by the scan) back
// to its numeric AT+COPS access technology. Returns nil for an unknown name.
func accessTechnologyValue(name string) *int {
	var value int
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "GSM":
		value = 0
	case "UTRAN", "UMTS", "WCDMA":
		value = 2
	case "EDGE":
		value = 3
	case "HSDPA":
		value = 4
	case "HSUPA":
		value = 5
	case "HSPA":
		value = 6
	case "LTE":
		value = 7
	case "NR5G", "NR":
		value = 9
	default:
		return nil
	}
	return &value
}

func (s *Server) handleOperatorSelection(w http.ResponseWriter, r *http.Request, physicalID string) bool {
	switch r.Method {
	case http.MethodGet:
		result, err := s.devices.OperatorSelection(r.Context(), physicalID)
		if err != nil {
			s.writeDeviceError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": operatorSelectionWire(result)})
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		// The SPA posts {mode, plmn, includes_pcs_digit, rat}; the legacy shape is
		// {automatic, plmn, access_technology}. Accept both. includes_pcs_digit is
		// accepted for contract compatibility but not currently applied to the command.
		var request struct {
			Mode             string `json:"mode"`
			Automatic        *bool  `json:"automatic"`
			PLMN             string `json:"plmn"`
			AccessTechnology *int   `json:"access_technology"`
			Rat              string `json:"rat"`
			IncludesPcsDigit bool   `json:"includes_pcs_digit"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		automatic := false
		switch strings.ToLower(strings.TrimSpace(request.Mode)) {
		case "manual":
			automatic = false
		case "automatic":
			automatic = true
		default:
			if request.Automatic != nil {
				automatic = *request.Automatic
			}
		}
		accessTechnology := request.AccessTechnology
		if rat := strings.TrimSpace(request.Rat); rat != "" {
			accessTechnology = accessTechnologyValue(rat)
		}
		// Manual PLMN selection can take tens of seconds while the modem
		// searches for and registers on the requested network. The server's
		// WriteTimeout would otherwise cut the response off, so clear the
		// connection's write deadline for this request like the SSE paths do.
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Time{})
		result, err := s.devices.SetOperatorSelection(r.Context(), physicalID, automatic, request.PLMN, accessTechnology)
		if err != nil {
			s.writeDeviceError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": operatorSelectionWire(result)})
	default:
		w.Header().Set("Allow", "GET, POST, PUT, PATCH")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

func (s *Server) handleOperatorReRegister(w http.ResponseWriter, r *http.Request, physicalID string) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	result, err := s.devices.ReRegisterOperator(r.Context(), physicalID)
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": operatorSelectionWire(result)})
	return true
}

func (s *Server) handleVoWiFiEnabled(
	w http.ResponseWriter,
	r *http.Request,
	config store.Device,
	physicalPresent bool,
) bool {
	if !requireMethod(w, r, http.MethodPatch) {
		return true
	}
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	if s.vowifi == nil {
		writeError(w, http.StatusServiceUnavailable, "vowifi_provider_unavailable", "the VoWiFi runtime is unavailable")
		return true
	}
	if request.Enabled && !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return true
	}
	if request.Enabled && config.NetworkEnabled {
		writeError(w, http.StatusConflict, "cellular_data_active", "disable roaming data before enabling VoWiFi")
		return true
	}
	if request.Enabled {
		entry, _, _ := s.physicalForConfig(config)
		imsi := snapshotString(entry.Snapshot, func(snapshot *device.Snapshot) string { return snapshot.IMSI })
		if reason := device.RegionBlockReason(imsi); reason != "" {
			writeError(w, http.StatusForbidden, "region_blocked", reason)
			return true
		}
	}

	// Establish RF-off synchronously before changing the asynchronous VoWiFi
	// lifecycle. This removes the attach window both when entering VoWiFi and
	// when leaving it: teardown starts from CFUN=4 and is required to remain
	// there until the user explicitly disables airplane mode.
	previous := config.VoWiFiEnabled
	dataRuntime := s.cellularDataRuntime()
	s.clearPublicIP(config.ID)
	desiredData := config.NetworkEnabled && !config.VoWiFiEnabled
	maintenancePhase := "disabled"
	if desiredData {
		maintenancePhase = "recovering"
	}
	dataRuntime.invalidateWithMaintenancePhase(config.ID, desiredData, maintenancePhase, "", "vowifi")
	liveICCID := ""
	entry, physicalID, present := s.physicalForConfig(config)
	if present {
		if _, err := s.devices.SetFlight(r.Context(), physicalID, true); err != nil {
			s.writeDeviceError(w, err)
			return true
		}
	}
	if entry.Snapshot != nil {
		iccid := strings.TrimSpace(entry.Snapshot.ICCID)
		if iccid != "" {
			liveICCID = iccid
			policy, policyErr := s.store.CardPolicy(r.Context(), iccid)
			if errors.Is(policyErr, store.ErrNotFound) {
				policy = store.CardPolicy{ICCID: iccid, IPVersion: "IPV4V6"}
				policyErr = nil
			}
			if policyErr != nil {
				s.writeStoreError(w, policyErr)
				return true
			}
			policy.VoWiFiEnabled = request.Enabled
			policy.AirplaneEnabled = true
			policy.NetworkEnabled = false
			policy.Source = "manual"
			if err := s.store.UpsertCardPolicy(r.Context(), policy); err != nil {
				s.writeStoreError(w, err)
				return true
			}
		}
	}

	rollbackCardPolicy := func() {
		if liveICCID == "" {
			return
		}
		policy, policyErr := s.store.CardPolicy(context.Background(), liveICCID)
		if policyErr != nil {
			return
		}
		policy.VoWiFiEnabled = previous
		policy.AirplaneEnabled = true
		policy.NetworkEnabled = false
		_ = s.store.UpsertCardPolicy(context.Background(), policy)
	}
	config.VoWiFiEnabled = request.Enabled
	config.NetworkEnabled = false
	if err := s.store.UpsertDevice(r.Context(), config); err != nil {
		rollbackCardPolicy()
		s.writeStoreError(w, err)
		return true
	}
	dataRuntime.invalidate(config.ID, false, "disabled", "")
	state, err := s.vowifi.RequestEnabled(config.ID, request.Enabled)
	if err != nil {
		// Repeating the same desired state while its asynchronous transaction is
		// already running is idempotent. Treat it as accepted instead of making a
		// harmless double-click (or a stale browser refresh) surface vowifi_busy.
		if errors.Is(err, vowifiruntime.ErrOperationInProgress) &&
			(state.Enabled == request.Enabled || previous == request.Enabled) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"data": map[string]any{
					"accepted": true,
					"enabled":  request.Enabled,
					"status":   "in_progress",
					"runtime":  state,
				},
			})
			return true
		}
		config.VoWiFiEnabled = previous
		rollbackCardPolicy()
		if restoreErr := s.store.UpsertDevice(r.Context(), config); restoreErr != nil {
			s.logger.Error(
				"restore VoWiFi policy after rejected runtime operation",
				"device_id", config.ID,
				"error", restoreErr,
			)
		}
		s.writeVoWiFiError(w, err)
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]any{
			"accepted": true,
			"enabled":  request.Enabled,
			"status":   map[bool]string{true: "starting", false: "stopping"}[request.Enabled],
			"runtime":  state,
		},
	})
	return true
}

func (s *Server) handleVoWiFiReconnect(
	w http.ResponseWriter,
	r *http.Request,
	config store.Device,
	physicalPresent bool,
) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	if s.vowifi == nil {
		writeError(w, http.StatusServiceUnavailable, "vowifi_provider_unavailable", "the VoWiFi runtime is unavailable")
		return true
	}
	if !config.VoWiFiEnabled {
		writeError(w, http.StatusConflict, "vowifi_disabled", "enable VoWiFi before requesting a reconnect")
		return true
	}
	if !physicalPresent {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return true
	}
	state, err := s.vowifi.RequestReconnect(config.ID)
	if err != nil {
		s.writeVoWiFiError(w, err)
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]any{
			"accepted": true,
			"status":   "reconnecting",
			"runtime":  state,
		},
	})
	return true
}

func (s *Server) writeVoWiFiError(w http.ResponseWriter, err error) {
	s.logger.Warn("VoWiFi operation failed",
		"category", "vowifi",
		"event", "vowifi.operation_failed",
		"raw_error", err,
	)
	switch {
	case errors.Is(err, vowifiruntime.ErrNotRegistered):
		writeError(w, http.StatusServiceUnavailable, "vowifi_device_unavailable", "the configured device has no VoWiFi runtime")
	case errors.Is(err, vowifiruntime.ErrOperationInProgress):
		writeError(w, http.StatusConflict, "vowifi_busy", "another VoWiFi operation is still in progress")
	case errors.Is(err, vowifiruntime.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, "vowifi_runtime_stopped", "the VoWiFi runtime is stopping")
	case errors.Is(err, vowifi.ErrNotRunning):
		writeError(w, http.StatusConflict, "vowifi_not_running", "VoWiFi is not running")
	default:
		writeError(w, http.StatusBadGateway, "vowifi_error", err.Error())
	}
}

// maxActionTimeoutMs bounds a client-supplied per-request timeout so a terminal
// action cannot hold the device operation lock indefinitely.
const maxActionTimeoutMs = 120000

// actionRequestContext applies the frontend's optional per-request timeout
// (timeout_ms) to the request context. Manager.withTimeout preserves an
// existing deadline, so setting one here makes the requested timeout take
// effect; a non-positive or absent value leaves the manager's defaults in place.
func actionRequestContext(parent context.Context, timeoutMs int) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeoutMs <= 0 {
		return context.WithCancel(parent)
	}
	if timeoutMs > maxActionTimeoutMs {
		timeoutMs = maxActionTimeoutMs
	}
	return context.WithTimeout(parent, time.Duration(timeoutMs)*time.Millisecond)
}

func (s *Server) handleAT(w http.ResponseWriter, r *http.Request, id string) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	var request struct {
		Command   string `json:"cmd"`
		TimeoutMs int    `json:"timeout_ms"`
		Force     bool   `json:"force"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	command := strings.TrimSpace(request.Command)
	if err := validateATCommand(command, request.Force); err != nil {
		writeError(w, http.StatusBadRequest, "unsafe_at_command", err.Error())
		return true
	}
	ctx, cancel := actionRequestContext(r.Context(), request.TimeoutMs)
	defer cancel()
	response, err := s.devices.ExecuteAT(ctx, id, command)
	if err != nil {
		var commandErr *modem.CommandError
		if errors.As(err, &commandErr) {
			// The modem answered with ERROR / +CME ERROR. An AT terminal must
			// surface that text (including the CME detail) as a normal response;
			// folding it into a 502 hides the real reason from the user.
			text := strings.Join(commandErr.Lines, "\n")
			if text != "" {
				text += "\n"
			}
			text += commandErr.Final
			s.logger.Warn("AT command rejected by modem",
				"category", "hardware",
				"event", "hardware.at_rejected",
				"device_id", id,
				"modem_final", commandErr.Final,
				"raw_response", text,
			)
			writeJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"response":    text,
					"final":       commandErr.Final,
					"duration_ms": 0,
					"urcs":        []string{},
				},
			})
			return true
		}
		s.writeDeviceError(w, err)
		return true
	}
	text := response.Text()
	if response.Final != "" {
		if text != "" {
			text += "\n"
		}
		text += response.Final
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"response":    text,
			"final":       response.Final,
			"duration_ms": response.Duration.Milliseconds(),
			"urcs":        response.URCs,
		},
	})
	return true
}

func validateATCommand(command string, force bool) error {
	upper := strings.ToUpper(command)
	if len(command) < 2 || len(command) > 512 || !strings.HasPrefix(upper, "AT") {
		return errors.New("AT command must start with AT and contain at most 512 characters")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		return errors.New("AT command must contain exactly one line")
	}
	if force {
		return nil
	}
	canonical := strings.NewReplacer(" ", "", "\t", "").Replace(upper)
	for _, blocked := range []string{
		`+QCFG="USBNET"`,
		`+QCFG="USBCFG"`,
		"+QPOWD",
		"+CFUN=",
		"+CGATT=",
		"+CGACT=",
		"+CGDATA",
		"+QNETDEVCTL=",
		"+QIACT",
		"+QIDEACT",
		"+CMGS",
		"+CMSS",
		"+CMGC",
		"+QCMGS",
		"+CUSD=",
		"D",
		"A",
		"H",
	} {
		for _, segment := range strings.Split(canonical[2:], ";") {
			if strings.HasPrefix(segment, blocked) {
				return fmt.Errorf("AT%s is reserved for a guarded device action", blocked)
			}
		}
	}
	return nil
}

// imsUSSIController is the optional VoWiFi runtime capability used to route a
// USSD request over IMS (3GPP TS 24.390) when VoWiFi is enabled and the IMS
// session is registered. device.Manager does not implement it; the VoWiFi
// runtime manager does.
type imsUSSIController interface {
	SendUSSI(context.Context, string, vowifi.USSISubmitRequest) (vowifi.USSISubmitResult, error)
}

// openUSSDSession mirrors device.Manager.openUSSDSession but lives on the HTTP
// server so a USSI awaiting-input reply can hand back a token the existing
// continue/cancel endpoints understand. The token is only a device handle;
// the IMS session owns the actual dialog.
func (s *Server) openUSSDSession(deviceID string) string {
	return s.ussdSessions.open(deviceID)
}

// ussdSessionDevice resolves a USSD session token created by openUSSDSession
// back to its device id, matching device.ErrUSSDSessionNotFound semantics.
func (s *Server) ussdSessionDevice(sessionID string) (string, error) {
	return s.ussdSessions.device(sessionID)
}

// dropUSSDSession releases a USSD session token.
func (s *Server) dropUSSDSession(sessionID string) {
	s.ussdSessions.drop(sessionID)
}

func (s *Server) handleUSSD(w http.ResponseWriter, r *http.Request, config store.Device, id string) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	var request struct {
		Command   string `json:"command"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	ctx, cancel := actionRequestContext(r.Context(), request.TimeoutMs)
	defer cancel()

	cmd := strings.TrimSpace(request.Command)
	if cmd == "*#06#" || cmd == "*#06" {
		imei := config.ModemIMEI
		if imei == "" {
			if runtime, runtimeErr := s.store.DeviceRuntime(ctx, id); runtimeErr == nil {
				imei = runtime.IMEI
			}
		}
		if imei != "" {
			writeUSSDResult(w, device.USSDResult{
				Text:   fmt.Sprintf("IMEI: %s", imei),
				Status: "final",
			})
			return true
		}
	}
	if cmd == "*#0000#" || cmd == "*#0000" {
		firmware := ""
		if runtime, runtimeErr := s.store.DeviceRuntime(ctx, id); runtimeErr == nil {
			firmware = runtime.Firmware
		}
		if firmware != "" {
			writeUSSDResult(w, device.USSDResult{
				Text:   fmt.Sprintf("Software Version: %s", firmware),
				Status: "final",
			})
			return true
		}
	}

	// VoWiFi-first: when VoWiFi owns the radio the cellular CUSD path has no
	// network to talk to (CFUN=4 returns +CME ERROR: 30). Route over IMS/USSI
	// when the IMS session is registered, and fall back to cellular CUSD only
	// when USSI is not ready or the runtime is unavailable.
	if config.VoWiFiEnabled && s.vowifi != nil {
		sender, canSendIMS := s.vowifi.(imsUSSIController)
		if canSendIMS {
			if state, stateErr := s.vowifi.State(id); stateErr == nil && state.IMSReady {
				result, sendErr := sender.SendUSSI(ctx, id, vowifi.USSISubmitRequest{Code: request.Command})
				if sendErr == nil {
					writeUSSDResult(w, ussdResultFromUSSI(result, id, s))
					return true
				}
				if !errors.Is(sendErr, vowifi.ErrUSSINotReady) {
					s.writeDeviceError(w, sendErr)
					return true
				}
				// ErrUSSINotReady: fall through to cellular CUSD.
			}
		}
	}
	result, err := s.devices.USSD(ctx, id, request.Command)
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeUSSDResult(w, result)
	return true
}

// ussdResultFromUSSI maps a USSI result onto the device.USSDResult shape that
// writeUSSDResult expects. A USSI awaiting-input reply opens a server-side
// session token via the device manager so the existing continue/cancel
// endpoints keep working; the token maps back to the device and the continue
// handler re-enters the USSI path through the same imsUSSIController.
func ussdResultFromUSSI(result vowifi.USSISubmitResult, deviceID string, server *Server) device.USSDResult {
	mapped := device.USSDResult{
		Text:         result.Text,
		Raw:          result.Raw,
		DCS:          result.DCS,
		Status:       result.Status,
		Continueable: result.Continueable,
	}
	// USSI has no inline continue/terminate flag in the 2xx response body, so
	// treat any non-empty successful reply as potentially multi-round. The cancel
	// endpoint drops the local token; the network will time the dialog out if it
	// was actually final.
	if mapped.Status != "failed" && mapped.Status != "terminated" && mapped.Text != "" {
		mapped.Status = "awaiting_input"
		mapped.Continueable = true
		mapped.SessionID = server.openUSSDSession(deviceID)
	}
	return mapped
}

func (s *Server) handleFlightMode(w http.ResponseWriter, r *http.Request, config store.Device, physicalID string) bool {
	if !requireMethod(w, r, http.MethodPatch) {
		return true
	}
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	if config.VoWiFiEnabled {
		writeError(w, http.StatusConflict, "vowifi_owns_airplane_mode", "airplane mode is locked on while VoWiFi is enabled")
		return true
	}
	dataRuntime := s.cellularDataRuntime()
	desiredData := config.NetworkEnabled && !config.VoWiFiEnabled
	if request.Enabled {
		s.clearPublicIP(config.ID)
		phase := "disabled"
		if desiredData {
			phase = "recovering"
		}
		dataRuntime.invalidateWithMaintenancePhase(config.ID, desiredData, phase, "", "flight_mode")
	}
	result, err := s.devices.SetFlight(r.Context(), physicalID, request.Enabled)
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	// Unlike VoWiFi, CFUN airplane state is not represented in the device row.
	// Persist it against the live ICCID so a restart can distinguish an
	// intentional airplane policy from an interrupted VoWiFi teardown.
	if entry, getErr := s.devices.Get(physicalID); getErr == nil && entry.Snapshot != nil {
		iccid := strings.TrimSpace(entry.Snapshot.ICCID)
		if iccid != "" {
			policy, policyErr := s.store.CardPolicy(r.Context(), iccid)
			if errors.Is(policyErr, store.ErrNotFound) {
				policy = store.CardPolicy{ICCID: iccid, IPVersion: "IPV4V6"}
				policyErr = nil
			}
			if policyErr != nil {
				s.writeStoreError(w, policyErr)
				return true
			}
			policy.AirplaneEnabled = request.Enabled
			if request.Enabled {
				policy.VoWiFiEnabled = false
			}
			policy.Source = "manual"
			if err := s.store.UpsertCardPolicy(r.Context(), policy); err != nil {
				s.writeStoreError(w, err)
				return true
			}
		}
	}
	if !request.Enabled {
		if desiredData {
			// Prefer the live ICCID/profile request when the modem snapshot is
			// available. After leaving flight mode the snapshot may still be
			// temporarily unavailable, so queue a config-based fallback instead
			// of leaving the runtime parked in the maintenance phase.
			networkRequest := s.cellularNetworkRequest(r.Context(), config, nil)
			networkRequest.Enabled = true
			identity := ""
			if entry, getErr := s.devices.Get(physicalID); getErr == nil && entry.Snapshot != nil {
				networkRequest = s.cellularNetworkRequest(r.Context(), config, entry.Snapshot)
				networkRequest.Enabled = true
				identity = strings.TrimSpace(entry.Snapshot.ICCID)
			}
			dataRuntime.requestWithIdentity(config.ID, physicalID, networkRequest, identity)
		} else {
			dataRuntime.invalidate(config.ID, false, "disabled", "")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

type modemAPNProfile struct {
	CID       int    `json:"cid"`
	APN       string `json:"apn"`
	IPVersion string `json:"ip_version"`
}

func parseModemAPNProfiles(lines []string) []modemAPNProfile {
	profiles := make([]modemAPNProfile, 0)
	seen := make(map[string]bool)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		prefix := strings.Index(strings.ToUpper(line), "+CGDCONT:")
		if prefix < 0 {
			continue
		}
		record, err := csv.NewReader(strings.NewReader(strings.TrimSpace(line[prefix+len("+CGDCONT:"):]))).Read()
		if err != nil || len(record) < 3 {
			continue
		}
		cid, err := strconv.Atoi(strings.TrimSpace(record[0]))
		if err != nil || cid < 1 {
			continue
		}
		ipVersion := strings.ToUpper(strings.TrimSpace(record[1]))
		if ipVersion == "IPV4" {
			ipVersion = "IP"
		}
		if ipVersion != "IP" && ipVersion != "IPV6" && ipVersion != "IPV4V6" {
			continue
		}
		apn := strings.TrimSpace(record[2])
		if apn == "" || !device.ValidAPN(apn) {
			continue
		}
		key := strings.ToLower(apn) + "\x00" + ipVersion
		if seen[key] {
			continue
		}
		seen[key] = true
		profiles = append(profiles, modemAPNProfile{CID: cid, APN: apn, IPVersion: ipVersion})
	}
	return profiles
}

func (s *Server) handleAPNProfiles(w http.ResponseWriter, r *http.Request, physicalID string) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	response, err := s.devices.ExecuteAT(r.Context(), physicalID, "AT+CGDCONT?")
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"items": parseModemAPNProfiles(response.Lines),
	}})
	return true
}

func (s *Server) handleCellularIMS(
	w http.ResponseWriter,
	r *http.Request,
	config store.Device,
	physicalID string,
) bool {
	controller, ok := s.devices.(cellularIMSController)
	if !ok {
		writeError(w, http.StatusNotImplemented, "cellular_ims_unsupported", "cellular IMS control is unavailable")
		return true
	}
	entry, _ := s.devices.Get(physicalID)
	iccid := ""
	if entry.Snapshot != nil {
		iccid = strings.TrimSpace(entry.Snapshot.ICCID)
	}
	switch r.Method {
	case http.MethodGet:
		status, statusErr := controller.CellularIMS(r.Context(), physicalID)
		if statusErr != nil {
			writeError(w, http.StatusUnprocessableEntity, "cellular_ims_unsupported", statusErr.Error())
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": cellularIMSResponse(iccid, status)})
	case http.MethodPatch:
		var request struct {
			Mode    *string `json:"mode"`
			Enabled *bool   `json:"enabled"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		if request.Mode == nil && request.Enabled == nil {
			writeError(w, http.StatusBadRequest, "invalid_cellular_ims", "mode is required")
			return true
		}
		mode := device.CellularIMSModeMBNDefault
		if request.Mode != nil {
			mode = device.CellularIMSMode(strings.ToLower(strings.TrimSpace(*request.Mode)))
		} else if *request.Enabled {
			// Backward compatibility with PR #77: false meant restoring the MBN
			// default (QCFG ims=0), never force-disabling IMS.
			mode = device.CellularIMSModeForceEnabled
		}
		if mode != device.CellularIMSModeMBNDefault && mode != device.CellularIMSModeForceEnabled &&
			mode != device.CellularIMSModeForceDisabled {
			writeError(w, http.StatusBadRequest, "invalid_cellular_ims", "mode must be mbn_default, force_enabled, or force_disabled")
			return true
		}
		status, applyErr := controller.SetCellularIMS(r.Context(), physicalID, mode)
		if applyErr != nil {
			writeError(w, http.StatusBadGateway, "cellular_ims_apply_failed", "IMS configuration could not be applied: "+applyErr.Error())
			return true
		}
		if status.Rebooting {
			desiredData := config.NetworkEnabled && !config.VoWiFiEnabled
			invalidated := s.cellularDataRuntime().invalidateWithModemPhase(config.ID, desiredData, "recovering", "", "rebooting")
			s.restoreCellularDataAfterModemReboot(config.ID, physicalID, iccid, invalidated.Revision, "cellular IMS reboot")
		}
		statusCode := http.StatusOK
		if status.Rebooting {
			statusCode = http.StatusAccepted
		}
		writeJSON(w, statusCode, map[string]any{"data": cellularIMSResponse(iccid, status)})
	default:
		w.Header().Set("Allow", "GET, PATCH")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

// restoreCellularDataAfterModemReboot rebuilds the QMI/AT data session
// destroyed by AT+CFUN=1,1. The desired data state already lives in the
// device/card policy; this waits for the same SIM to register before replaying
// it. generation prevents an older reboot from overriding a newer user action.
func (s *Server) restoreCellularDataAfterModemReboot(configID, physicalID, iccid string, generation uint64, reason string) {
	go func() {
		ctx, cancel := context.WithTimeout(s.cellularDataRuntime().rootContext(), 3*time.Minute)
		defer cancel()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		var lastErr error
		for {
			select {
			case <-ctx.Done():
				detail := "modem did not become ready before cellular data recovery timed out"
				if lastErr != nil {
					detail += ": " + lastErr.Error()
				}
				s.cellularDataRuntime().invalidateIfCurrent(configID, generation, "recovering", true, "failed", detail)
				s.logger.Warn("restore cellular data after modem reboot timed out", "device_id", configID, "reason", reason, "error", lastErr)
				return
			case <-ticker.C:
			}
			if !s.cellularDataRuntime().isCurrent(configID, generation, "recovering") {
				return
			}
			config, err := s.store.Device(ctx, configID)
			if err != nil {
				s.cellularDataRuntime().invalidateIfCurrent(configID, generation, "recovering", true, "failed", err.Error())
				return
			}
			if !config.NetworkEnabled || config.VoWiFiEnabled {
				s.cellularDataRuntime().invalidateIfCurrent(configID, generation, "recovering", false, "disabled", "")
				return
			}
			entry, err := s.devices.Get(physicalID)
			if err != nil || entry.Snapshot == nil || !entry.Snapshot.SIMReady ||
				(iccid != "" && !strings.EqualFold(strings.TrimSpace(entry.Snapshot.ICCID), iccid)) ||
				!entry.Snapshot.PSAttached {
				lastErr = err
				continue
			}
			request := s.cellularNetworkRequest(ctx, config, entry.Snapshot)
			dataRuntime := s.cellularDataRuntime()
			runtime, accepted := dataRuntime.requestIfCurrent(config.ID, generation, "recovering", physicalID, request)
			if !accepted {
				return
			}
			restoreCtx, cancelRestore := context.WithTimeout(ctx, 90*time.Second)
			_, err = dataRuntime.wait(restoreCtx, config.ID, runtime.Revision)
			cancelRestore()
			if err != nil {
				lastErr = err
				continue
			}
			s.logger.Info("restored cellular data after modem reboot", "device_id", configID, "interface", config.Interface, "reason", reason)
			return
		}
	}()
}

func (s *Server) cellularNetworkRequest(ctx context.Context, config store.Device, snapshot *device.Snapshot) device.NetworkRequest {
	request := device.NetworkRequest{
		Enabled: true, APN: strings.TrimSpace(config.APN), IPVersion: "IPV4V6", Backend: config.DeviceBackend,
	}
	if snapshot == nil {
		return request
	}
	iccid := strings.TrimSpace(snapshot.ICCID)
	policy, err := s.store.CardPolicy(ctx, iccid)
	if err != nil {
		return request
	}
	request.APN = strings.TrimSpace(policy.APN)
	if policy.IPVersion != "" {
		request.IPVersion = policy.IPVersion
	}
	profile, err := s.store.CardAPNProfileByAPN(ctx, iccid, policy.APN, policy.IPVersion)
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

func cellularIMSResponse(iccid string, status device.CellularIMSStatus) map[string]any {
	return map[string]any{
		"iccid": iccid, "mode": status.Mode,
		"desired_enabled": status.Mode == device.CellularIMSModeForceEnabled,
		"supported":       status.Supported,
		"configured":      status.Configured, "registered": status.Registered,
		"volte_capable": status.VoLTECapable,
		"cs_known":      status.CSKnown, "cs_registered": status.CSRegistered,
		"changed": status.Changed, "rebooting": status.Rebooting,
	}
}

func (s *Server) handleCellularData(
	w http.ResponseWriter,
	r *http.Request,
	config store.Device,
	physicalID string,
) bool {
	if !s.developerActive(r.Context()) {
		writeError(w, http.StatusForbidden, "developer_mode_required", "roaming data is available only in developer mode")
		return true
	}
	switch r.Method {
	case http.MethodGet:
		runtime := s.cellularDataRuntime().status(config.ID, config.NetworkEnabled)
		if observer, ok := s.devices.(cellularNetworkStatusController); ok {
			probeContext, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			observed, observeErr := observer.NetworkStatus(probeContext, physicalID)
			cancel()
			if observeErr == nil && config.NetworkEnabled && !observed.Connected && strings.TrimSpace(observed.Detail) != "" {
				observeErr = errors.New(observed.Detail)
			}
			runtime = s.cellularDataRuntime().observe(config.ID, config.NetworkEnabled, observed.Connected, observeErr)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"enabled":           config.NetworkEnabled,
			"desired_enabled":   runtime.DesiredEnabled,
			"connected":         runtime.Connected,
			"phase":             runtime.Phase,
			"modem_phase":       runtime.ModemPhase,
			"maintenance_phase": runtime.MaintenancePhase,
			"last_error":        runtime.LastError,
			"interface":         config.Interface,
			"apn":               config.APN,
			"export_proxy_only": true,
		}})
	case http.MethodPatch, http.MethodPut:
		var request struct {
			Enabled bool   `json:"enabled"`
			APN     string `json:"apn"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		if request.Enabled && config.VoWiFiEnabled {
			writeError(w, http.StatusConflict, "vowifi_owns_radio", "disable VoWiFi before enabling cellular roaming data")
			return true
		}
		if !request.Enabled && s.exportProxy != nil {
			if _, active := s.exportProxy.EnabledConfigForDevice(config.ID); active {
				writeError(w, http.StatusConflict, "export_proxy_active", i18n.T("请先禁用该设备已绑定的导出代理，再关闭漫游数据"))
				return true
			}
		}
		apn := strings.TrimSpace(request.APN)
		policyIPVersion := "IPV4V6"
		activeICCID := ""
		isRoaming := false
		var activePolicy store.CardPolicy
		var activeAPNProfile store.CardAPNProfile
		if entry, getErr := s.devices.Get(physicalID); getErr == nil && entry.Snapshot != nil {
			activeICCID = strings.TrimSpace(entry.Snapshot.ICCID)
			isRoaming = entry.Snapshot.RegistrationStatus == 5
			if stored, policyErr := s.store.CardPolicy(r.Context(), activeICCID); policyErr == nil {
				activePolicy = stored
				if apn == "" {
					apn = strings.TrimSpace(stored.APN)
				}
				if stored.IPVersion != "" {
					policyIPVersion = stored.IPVersion
				}
			}
		}
		if apn == "" && activePolicy.ICCID == "" {
			apn = strings.TrimSpace(config.APN)
		}
		if !device.ValidAPN(apn) {
			writeError(w, http.StatusBadRequest, "invalid_apn", "APN must contain only letters, digits, dots, underscores, or hyphens")
			return true
		}
		if profile, profileErr := s.store.CardAPNProfileByAPN(r.Context(), activeICCID, apn, policyIPVersion); profileErr == nil {
			activeAPNProfile = profile
		}
		effectiveIPVersion := policyIPVersion
		if isRoaming && activeAPNProfile.RoamingIPVersion != "" {
			effectiveIPVersion = activeAPNProfile.RoamingIPVersion
		}
		networkRequest := device.NetworkRequest{
			Enabled: request.Enabled, APN: apn, IPVersion: effectiveIPVersion,
			Username: activeAPNProfile.Username, Password: activeAPNProfile.Password,
			Authentication: activeAPNProfile.AuthType, Backend: config.DeviceBackend,
		}
		// Persist the desired state before starting hardware reconciliation. The
		// background worker is server-owned, so a browser disconnect cannot leave
		// hardware changed while the stored policy still says the opposite.
		previousConfig := config
		config.NetworkEnabled = request.Enabled
		config.APN = apn
		if err := s.store.UpsertDevice(r.Context(), config); err != nil {
			s.writeStoreError(w, err)
			return true
		}
		if validICCID(activeICCID) {
			if activePolicy.ICCID == "" {
				activePolicy = defaultCardPolicy(activeICCID)
			}
			activePolicy.APN = apn
			activePolicy.IPVersion = policyIPVersion
			activePolicy.NetworkEnabled = request.Enabled
			if strings.TrimSpace(request.APN) != "" {
				activePolicy.Source = "manual"
			}
			if err := s.store.UpsertCardPolicy(r.Context(), activePolicy); err != nil {
				rollbackContext, cancelRollback := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.store.UpsertDevice(rollbackContext, previousConfig)
				cancelRollback()
				s.writeStoreError(w, err)
				return true
			}
		}
		if !request.Enabled {
			// Clear the cached public exit as soon as the user accepts the
			// disable request; the hardware stop itself is asynchronous.
			s.clearPublicIP(config.ID)
		}
		runtime := s.cellularDataRuntime().requestWithIdentity(config.ID, physicalID, networkRequest, activeICCID)
		writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{
			"enabled": request.Enabled, "desired_enabled": runtime.DesiredEnabled,
			"connected": runtime.Connected, "phase": runtime.Phase,
			"modem_phase":       runtime.ModemPhase,
			"maintenance_phase": runtime.MaintenancePhase,
			"revision":          runtime.Revision, "interface": config.Interface,
			"backend": config.DeviceBackend, "export_proxy_only": true,
		}})
	default:
		w.Header().Set("Allow", "GET, PATCH, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

func (s *Server) requirePhysicalDevice(w http.ResponseWriter, present bool) bool {
	if s.devices == nil {
		writeError(w, http.StatusServiceUnavailable, "device_manager_unavailable", "device manager is unavailable")
		return false
	}
	if !present {
		writeError(w, http.StatusServiceUnavailable, "physical_device_missing", "the configured modem is not present on this Linux host")
		return false
	}
	return true
}

func (s *Server) writeDeviceError(w http.ResponseWriter, err error) {
	s.logger.Warn("hardware operation failed",
		"category", "hardware",
		"event", "hardware.operation_failed",
		"raw_error", device.HardwareErrorDetail(err),
	)
	switch {
	case errors.Is(err, device.ErrNotFound):
		writeError(w, http.StatusNotFound, "device_not_found", "device was not found or is no longer present")
	case errors.Is(err, device.ErrNotStarted):
		writeError(w, http.StatusServiceUnavailable, "device_manager_not_started", "device manager is not started")
	case errors.Is(err, device.ErrNoATPort):
		writeError(w, http.StatusServiceUnavailable, "at_port_unavailable", "device has no usable AT port")
	case errors.Is(err, device.ErrDataBackendUnavailable):
		writeError(w, http.StatusNotImplemented, "data_backend_unavailable", err.Error())
	case errors.Is(err, device.ErrEUICCChannelStuck):
		writeError(w, http.StatusServiceUnavailable, "euicc_channel_stuck", err.Error())
	case errors.Is(err, device.ErrESIMDeleteProfileNotFound):
		writeError(w, http.StatusNotFound, "esim_profile_not_found", "The profile is no longer present on this eUICC. Refresh the profile list and try again.")
	case errors.Is(err, device.ErrESIMDeleteProfileNotDisabled):
		writeError(w, http.StatusConflict, "esim_profile_enabled", "The active profile cannot be deleted. Enable another profile first, then delete this disabled profile.")
	case errors.Is(err, device.ErrESIMDeleteDisallowedByPolicy):
		writeError(w, http.StatusConflict, "esim_delete_disallowed_by_policy", "This profile's policy does not allow it to be deleted.")
	case errors.Is(err, device.ErrESIMNicknameTooLong):
		writeError(w, http.StatusBadRequest, "esim_nickname_too_long", "Profile nickname must not exceed 64 characters.")
	case errors.Is(err, device.ErrESIMNicknameProfileNotFound):
		writeError(w, http.StatusNotFound, "esim_profile_not_found", "The profile is no longer present on this eUICC. Refresh the profile list and try again.")
	case errors.Is(err, device.ErrESIMDisableProfileNotFound):
		writeError(w, http.StatusNotFound, "esim_profile_not_found", "The profile is no longer present on this eUICC. Refresh the profile list and try again.")
	case errors.Is(err, device.ErrESIMProfileNotEnabled):
		writeError(w, http.StatusConflict, "esim_profile_not_enabled", "This profile is already disabled. Refresh the profile list before retrying.")
	case errors.Is(err, device.ErrESIMDisableDisallowedByPolicy):
		writeError(w, http.StatusConflict, "esim_disable_disallowed_by_policy", "This profile's policy does not allow it to be disabled.")
	case errors.Is(err, device.ErrESIMDisableCATBusy):
		writeError(w, http.StatusConflict, "esim_cat_busy", "The eUICC is busy with a SIM Toolkit operation. Wait a moment and retry disabling the profile.")
	case errors.Is(err, device.ErrInvalidNetworkAPN):
		writeError(w, http.StatusBadRequest, "invalid_apn", "APN must contain only letters, digits, dots, underscores, or hyphens")
	case errors.Is(err, device.ErrRegionBlocked):
		writeError(w, http.StatusForbidden, "region_blocked", err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, modem.ErrCommandTimeout):
		writeError(w, http.StatusGatewayTimeout, "modem_timeout", "the modem did not answer before the command timeout")
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusRequestTimeout, "request_canceled", "the modem request was canceled")
	default:
		writeError(w, http.StatusBadGateway, "modem_error", "the device operation failed")
	}
}

func (s *Server) deviceSummaries() []map[string]any {
	configs, err := s.store.ListDevices(context.Background())
	if err != nil {
		s.logger.Error("list configured devices", "error", err)
		return []map[string]any{}
	}
	result := make([]map[string]any, 0, len(configs))
	for _, config := range configs {
		entry, _, present := s.physicalForConfig(config)
		if present {
			result = append(result, s.configuredDeviceSummary(config, &entry))
		} else {
			result = append(result, s.configuredDeviceSummary(config, nil))
		}
	}
	return result
}

func (s *Server) dashboardDevices() []map[string]any {
	devices := s.deviceSummaries()
	result := make([]map[string]any, 0, len(devices))
	for _, entry := range devices {
		modemStatus, _ := entry["modem"].(map[string]any)
		runtime, _ := entry["vowifi_runtime"].(map[string]any)
		vowifiActive, _ := runtime["tunnel_ready"].(bool)
		result = append(result, map[string]any{
			"id":                entry["id"],
			"name":              entry["name"],
			"device_type":       entry["device_type"],
			"interface":         entry["interface"],
			"proxy_port":        entry["proxy_port"],
			"public_ip":         entry["public_ip"],
			"healthy":           entry["healthy"],
			"operator":          modemStatus["operator"],
			"signal_dbm":        modemStatus["signal_dbm"],
			"network_mode":      modemStatus["network_mode"],
			"network_duplex":    modemStatus["network_duplex"],
			"vowifi_active":     vowifiActive,
			"vowifi_runtime":    runtime,
			"network_connected": entry["network_connected"],
			"model":             modemStatus["model"],
		})
	}
	return result
}

func (s *Server) physicalForConfig(config store.Device) (device.Device, string, bool) {
	if s.devices == nil {
		return device.Device{ID: config.ID}, "", false
	}
	if entry, err := s.devices.Get(config.ID); err == nil && entry.Discovered {
		return entry, entry.ID, true
	}
	for _, entry := range s.devices.List() {
		if entry.Discovered && physicalMatchesConfig(entry, config) {
			return entry, entry.ID, true
		}
	}
	return device.Device{ID: config.ID}, "", false
}

func physicalMatchesConfig(entry device.Device, config store.Device) bool {
	candidate := entry.Candidate
	if entry.ID == config.ID {
		return true
	}
	if config.ModemIMEI != "" && entry.Snapshot != nil && entry.Snapshot.IMEI != "" {
		return config.ModemIMEI == entry.Snapshot.IMEI
	}
	if config.USBPath != "" && candidate.USBPath != "" {
		if config.USBPath == candidate.USBPath {
			return true
		}
		// Sysfs paths may be stored through /sys/class symlinks while a
		// subsequent discovery returns the resolved device path. Keep checking
		// the selected AT/QMI nodes instead of rejecting a modem whose physical
		// path spelling changed but whose control plane is unchanged.
	}
	if candidate.HardwareKind == pcsc.HardwareKind && config.ControlDevice != "" && candidate.ReaderName != "" {
		if config.ControlDevice == candidate.ReaderName {
			return true
		}
	}
	// Control and serial device nodes are allocation-order dependent. They are
	// only legacy fallbacks when no physical USB path or readable IMEI exists.
	if config.ATPort != "" &&
		(config.ATPort == candidate.ATPort.Path || config.ATPort == candidate.ATPort.OpenPath()) {
		return true
	}
	if config.ControlDevice != "" && config.ControlDevice == candidate.QMIControl {
		return true
	}
	return false
}

func (s *Server) configuredDeviceSummary(
	config store.Device,
	entry *device.Device,
) map[string]any {
	var result map[string]any
	if entry != nil {
		result = deviceSummary(*entry)
	} else {
		result = deviceSummary(device.Device{ID: config.ID})
	}
	result["id"] = config.ID
	result["name"] = config.Name
	result["device_type"] = store.NormalizeDeviceType(config.DeviceType)
	result["interface"] = config.Interface
	result["proxy_port"] = config.ProxyPort
	result["esim_transport"] = config.ESIMTransport
	result["sms_enabled"] = config.SMSEnabled
	result["network_enabled"] = config.NetworkEnabled
	result["developer_enabled"] = s.developerActive(context.Background())
	dataRuntime := s.cellularDataRuntime().status(config.ID, config.NetworkEnabled)
	result["network_connected"] = dataRuntime.Connected
	result["data_connected"] = dataRuntime.Connected
	result["network_phase"] = dataRuntime.Phase
	result["modem_phase"] = dataRuntime.ModemPhase
	result["maintenance_phase"] = dataRuntime.MaintenancePhase
	result["network_error"] = dataRuntime.LastError
	result["vowifi_enabled"] = config.VoWiFiEnabled
	var runtimeResponse map[string]any
	runtimeMatchesCard := true
	if s.vowifi != nil {
		if runtime, err := s.vowifi.State(config.ID); err == nil {
			runtimeMatchesCard = voWiFiRuntimeMatchesSnapshot(runtime.ICCID, entry)
			if runtimeMatchesCard {
				runtimeResponse = liveVoWiFiRuntime(runtime)
			} else {
				runtimeResponse = idleVoWiFiRuntime(config.ID, snapshotForEntry(entry))
			}
		}
	}
	if runtimeResponse == nil {
		if runtime, err := s.store.VoWiFiRuntime(context.Background(), config.ID); err == nil {
			currentICCID := ""
			var currentSnapshot *device.Snapshot
			if entry != nil {
				currentSnapshot = entry.Snapshot
				if entry.Snapshot != nil {
					currentICCID = strings.TrimSpace(entry.Snapshot.ICCID)
				}
			}
			runtimeMatchesCard = currentICCID == "" || runtime.ICCID == "" ||
				strings.EqualFold(currentICCID, strings.TrimSpace(runtime.ICCID))
			if runtimeMatchesCard {
				runtimeResponse = storedVoWiFiRuntime(runtime)
			} else {
				// The saved IMS session belongs to a different eSIM profile. Never
				// project its registration or number onto the currently selected SIM.
				runtimeResponse = idleVoWiFiRuntime(config.ID, currentSnapshot)
			}
		}
	}
	if runtimeResponse == nil {
		runtimeResponse = idleVoWiFiRuntime(config.ID, snapshotForEntry(entry))
	}
	result["vowifi_runtime"] = runtimeResponse
	runtimeEnabled, _ := runtimeResponse["enabled"].(bool)
	runtimeTunnelReady, _ := runtimeResponse["tunnel_ready"].(bool)
	result["vowifi_active"] = config.VoWiFiEnabled && runtimeMatchesCard && runtimeEnabled && runtimeTunnelReady
	// Numbers are SIM-owned data. Resolve the association by the live ICCID
	// instead of reusing the last VoWiFi runtime attached to this device ID.
	if entry != nil && entry.Snapshot != nil {
		currentICCID := strings.TrimSpace(entry.Snapshot.ICCID)
		if currentICCID != "" {
			if association, err := s.store.PhoneAssociation(context.Background(), currentICCID); err == nil {
				result["local_phone"] = association.Number
				result["phone_number_source"] = association.Source
				if modemStatus, ok := result["modem"].(map[string]any); ok {
					modemStatus["phone_number"] = association.Number
					modemStatus["phone_number_source"] = association.Source
				}
			}
		}
	}
	return result
}

func (s *Server) configuredDeviceOverview(
	config store.Device,
	entry device.Device,
	present bool,
) map[string]any {
	developerActive := s.developerActive(context.Background())
	var physical *device.Device
	if present {
		physical = &entry
	}
	result := s.configuredDeviceSummary(config, physical)
	result["id"] = config.ID
	result["name"] = config.Name
	result["interface"] = config.Interface
	// ttyUSB allocation changes across USB reconnects and boot cycles. The AT
	// terminal must use only the currently discovered physical port; a stored
	// path may point at another modem after enumeration order changes.
	liveATPort := ""
	if present {
		liveATPort = entry.Candidate.ATPort.OpenPath()
	}
	result["at_port"] = liveATPort
	result["audio_device"] = config.AudioDevice
	result["backend_mode"] = config.DeviceBackend
	result["control_device"] = config.ControlDevice
	result["esim_transport"] = config.ESIMTransport
	result["sms_enabled"] = config.SMSEnabled
	result["network_enabled"] = developerActive && config.NetworkEnabled
	result["public_ip_info"] = s.overviewPublicIP(config, physical, developerActive)
	result["vowifi_enabled"] = config.VoWiFiEnabled
	result["radio_live_ok"] = present && entry.Snapshot != nil && entry.Snapshot.Responsive

	// Live network state: on-demand sample of the cellular interface counters,
	// kept warm by the 2s overview SSE cadence. Only meaningful when the modem
	// data path is enabled and an interface is configured.
	if connected, _ := result["network_connected"].(bool); developerActive && connected && strings.TrimSpace(config.Interface) != "" {
		live := s.netTraffic.sample(config.ID, config.Interface, time.Now())
		result["private_ip"] = live.ipv4
		result["traffic"] = map[string]string{
			"rx":      formatLiveBytes(float64(live.minuteRx)),
			"tx":      formatLiveBytes(float64(live.minuteTx)),
			"rate":    formatLiveBytes(live.rxRate) + "/s",
			"rate_tx": formatLiveBytes(live.txRate) + "/s",
		}
		result["traffic_raw"] = map[string]int64{
			"rx":      live.minuteRx,
			"tx":      live.minuteTx,
			"rate":    int64(live.rxRate),
			"rate_tx": int64(live.txRate),
		}
		result["traffic_meta"] = map[string]any{"status": live.status}
	} else {
		result["traffic"] = map[string]string{}
		result["traffic_raw"] = map[string]int64{}
		result["traffic_meta"] = map[string]any{}
	}
	return result
}

func (s *Server) configuredDeviceStatus(
	config store.Device,
	entry device.Device,
	present bool,
) map[string]any {
	var physical *device.Device
	if present {
		physical = &entry
	}
	summary := s.configuredDeviceSummary(config, physical)
	lastUpdated := time.Time{}
	if physical != nil {
		lastUpdated = physical.LastUpdated
	}
	result := map[string]any{
		"healthy":               summary["healthy"],
		"public_ip":             summary["public_ip"],
		"network_connected":     summary["network_connected"],
		"network_phase":         summary["network_phase"],
		"network_error":         summary["network_error"],
		"modem":                 summary["modem"],
		"vowifi":                summary["vowifi_runtime"],
		"sim_service_table":     map[string]any{},
		"pnn":                   []any{},
		"opl":                   []any{},
		"last_hardware_refresh": lastUpdated,
	}
	result["id"] = config.ID
	result["name"] = config.Name
	result["interface"] = config.Interface
	result["proxy_port"] = config.ProxyPort
	return result
}

func storedVoWiFiRuntime(runtime store.VoWiFiRuntime) map[string]any {
	extra, _ := rawJSONObject(runtime.Extra).(map[string]any)
	enabled, _ := extra["enabled"].(bool)
	active, _ := extra["active"].(bool)
	return map[string]any{
		"device_id":            runtime.DeviceID,
		"phase":                runtime.Phase,
		"enabled":              enabled,
		"active":               active,
		"carrier_profile":      extra["carrier_profile"],
		"carrier_profile_from": extra["carrier_profile_from"],
		"dataplane_mode":       runtime.DataplaneMode,
		"iccid":                runtime.ICCID,
		"imsi":                 runtime.IMSI,
		"sim_ready":            runtime.SIMReady,
		"access_ready":         runtime.AccessReady,
		"tunnel_ready":         runtime.TunnelReady,
		"ims_ready":            runtime.IMSReady,
		"sms_ready":            runtime.SMSReady,
		"reg_status":           runtime.RegStatus,
		"reg_status_text":      runtime.RegStatusText,
		"network_mode":         runtime.NetworkMode,
		"local_phone":          runtime.LocalPhone,
		"phone_number_source":  runtime.PhoneNumberSource,
		"last_error_class":     runtime.LastErrorClass,
		"last_error":           runtime.LastError,
		"last_reason":          runtime.LastReason,
		"updated_at":           runtime.UpdatedAt,
		"tunnel":               rawJSONObject(runtime.Tunnel),
		"imscore":              rawJSONObject(runtime.IMSCore),
		"smsip":                rawJSONObject(runtime.SMSIP),
	}
}

func liveVoWiFiRuntime(runtime vowifi.State) map[string]any {
	return map[string]any{
		"device_id":            runtime.DeviceID,
		"phase":                string(runtime.Phase),
		"enabled":              runtime.Enabled,
		"active":               runtime.Active,
		"carrier_profile":      runtime.CarrierProfile,
		"carrier_profile_from": runtime.CarrierProfileFrom,
		"dataplane_mode":       runtime.DataplaneMode,
		"iccid":                runtime.ICCID,
		"imsi":                 runtime.IMSI,
		"sim_ready":            runtime.SIMReady,
		"access_ready":         runtime.AccessReady,
		"tunnel_ready":         runtime.TunnelReady,
		"ims_ready":            runtime.IMSReady,
		"sms_ready":            runtime.SMSReady,
		"reg_status":           map[bool]int{true: 1, false: 0}[runtime.IMSReady],
		"reg_status_text":      map[bool]string{true: "registered", false: "not registered"}[runtime.IMSReady],
		"network_mode":         "Wi-Fi",
		"local_phone":          runtime.PhoneNumber,
		"phone_number_source":  runtime.PhoneNumberSource,
		"last_error_class":     runtime.LastErrorClass,
		"last_error":           runtime.LastError,
		"last_reason":          runtime.LastReason,
		"updated_at":           runtime.UpdatedAt,
		"tunnel": map[string]any{
			"established":    runtime.TunnelReady,
			"name":           runtime.TunnelName,
			"dataplane_mode": runtime.DataplaneMode,
			"epdg":           runtime.EPDG,
			"proxy_mode":     runtime.ProxyMode,
			"proxy_id":       runtime.ProxyID,
			"security_audit": runtime.Security,
		},
		"imscore": map[string]any{
			"registered":         runtime.IMSReady,
			"registration_state": runtime.IMSRegistration,
			"associated_number":  runtime.PhoneNumber,
			"number_source":      runtime.PhoneNumberSource,
		},
		"smsip": map[string]any{"ready": runtime.SMSReady},
	}
}

func snapshotForEntry(entry *device.Device) *device.Snapshot {
	if entry == nil {
		return nil
	}
	return entry.Snapshot
}

func voWiFiRuntimeMatchesSnapshot(runtimeICCID string, entry *device.Device) bool {
	current := strings.TrimSpace(snapshotString(snapshotForEntry(entry), func(snapshot *device.Snapshot) string {
		return snapshot.ICCID
	}))
	runtimeICCID = strings.TrimSpace(runtimeICCID)
	return current == "" || runtimeICCID == "" || strings.EqualFold(current, runtimeICCID)
}

func rawJSONObject(value json.RawMessage) any {
	var result any
	if len(value) != 0 && json.Unmarshal(value, &result) == nil {
		return result
	}
	return map[string]any{}
}

func deviceSummary(entry device.Device) map[string]any {
	snapshot := entry.Snapshot
	healthy := entry.Discovered && snapshot != nil && snapshot.Responsive && entry.LastError == ""
	phone := ""
	phoneSource := ""
	if snapshot != nil {
		phone = snapshot.Phone.Number
		phoneSource = snapshot.Phone.Source
	}
	mode := 0
	modeKnown := false
	if snapshot != nil {
		mode = snapshot.OperatingMode
		modeKnown = snapshot.ModeKnown
	}
	runtime := idleVoWiFiRuntime(entry.ID, snapshot)
	summary := modemSummary(snapshot, phone, phoneSource)
	if model, _ := summary["model"].(string); strings.TrimSpace(model) == "" {
		if p := strings.TrimSpace(entry.Candidate.Product); p != "" {
			summary["model"] = p
		} else if id := strings.TrimSpace(entry.ID); id != "" {
			summary["model"] = id
		}
	}
	return map[string]any{
		"id":                       entry.ID,
		"name":                     deviceName(entry),
		"running":                  entry.Discovered,
		"healthy":                  healthy,
		"control_online":           healthy,
		"physical_present":         entry.Discovered,
		"worker_running":           entry.Discovered,
		"data_connected":           false,
		"radio_registered":         snapshot != nil && (snapshot.RegistrationStatus == 1 || snapshot.RegistrationStatus == 5),
		"lifecycle_phase":          lifecyclePhase(entry),
		"lifecycle_reason":         entry.LastError,
		"public_ip":                "",
		"private_ip":               "",
		"interface":                entry.Candidate.NetworkInterface,
		"esim_transport":           backendMode(entry.Candidate),
		"sms_enabled":              true,
		"network_enabled":          false,
		"vowifi_enabled":           false,
		"vowifi_active":            false,
		"vowifi_runtime":           runtime,
		"modem":                    summary,
		"local_phone":              phone,
		"phone_number_source":      phoneSource,
		"network_connected":        false,
		"registration_state_label": registrationLabel(snapshot),
		"flight_mode":              modeKnown && (mode == 0 || mode == 4),
	}
}

func deviceOverview(entry device.Device) map[string]any {
	result := deviceSummary(entry)
	result["at_port"] = entry.Candidate.ATPort.OpenPath()
	result["audio_device"] = ""
	result["backend_mode"] = backendMode(entry.Candidate)
	result["control_device"] = firstNonEmpty(entry.Candidate.QMIControl, entry.Candidate.ATPort.OpenPath())
	result["radio_live_ok"] = entry.Snapshot != nil && entry.Snapshot.Responsive
	result["traffic"] = map[string]string{}
	result["traffic_raw"] = map[string]int64{}
	result["traffic_meta"] = map[string]any{}
	return result
}

func deviceStatus(entry device.Device) map[string]any {
	summary := deviceSummary(entry)
	return map[string]any{
		"id":                    entry.ID,
		"name":                  summary["name"],
		"healthy":               summary["healthy"],
		"interface":             summary["interface"],
		"public_ip":             "",
		"proxy_port":            1080,
		"network_connected":     false,
		"modem":                 summary["modem"],
		"vowifi":                summary["vowifi_runtime"],
		"sim_service_table":     map[string]any{},
		"pnn":                   []any{},
		"opl":                   []any{},
		"last_hardware_refresh": entry.LastUpdated,
	}
}

func storedDeviceConfig(config store.Device) map[string]any {
	simPIN := ""
	if strings.TrimSpace(config.SIMPIN) != "" {
		simPIN = store.SecretMask
	}
	return map[string]any{
		"id":                   config.ID,
		"name":                 config.Name,
		"device_type":          store.NormalizeDeviceType(config.DeviceType),
		"interface":            config.Interface,
		"control_device":       config.ControlDevice,
		"at_port":              config.ATPort,
		"usb_path":             config.USBPath,
		"audio_device":         config.AudioDevice,
		"modem_imei":           config.ModemIMEI,
		"sim_pin":              simPIN,
		"apn":                  config.APN,
		"proxy_port":           config.ProxyPort,
		"baud_rate":            config.BaudRate,
		"data_bits":            config.DataBits,
		"stop_bits":            config.StopBits,
		"parity":               config.Parity,
		"device_backend":       config.DeviceBackend,
		"esim_transport":       config.ESIMTransport,
		"qmi_use_proxy":        config.QMIUseProxy,
		"qmi_proxy_path":       config.QMIProxyPath,
		"qmi_proxy_executable": config.QMIProxyExecutable,
		"network_enabled":      config.NetworkEnabled,
		"sms_enabled":          config.SMSEnabled,
		"vowifi_enabled":       config.VoWiFiEnabled,
	}
}

func fillConfigFromPhysical(config *store.Device, entry device.Device) {
	candidate := entry.Candidate
	if candidate.HardwareKind == "pcsc" {
		config.DeviceType = store.DeviceTypeUSBSIMReader
		config.ControlDevice = candidate.ReaderName
		config.ATPort = ""
		config.Interface = ""
		config.DeviceBackend = "pcsc"
		config.ESIMTransport = "pcsc"
		config.NetworkEnabled = false
		config.SMSEnabled = true
		config.VoWiFiEnabled = true
	} else if modem.IsDJI4GUSB(candidate.VendorID, candidate.ProductID) {
		config.DeviceType = store.DeviceTypeDJI4G
	}
	if config.Interface == "" {
		config.Interface = candidate.NetworkInterface
	}
	if config.ControlDevice == "" {
		config.ControlDevice = firstNonEmpty(candidate.QMIControl, candidate.ATPort.OpenPath())
	}
	if config.ATPort == "" {
		config.ATPort = candidate.ATPort.OpenPath()
	}
	if config.USBPath == "" {
		config.USBPath = candidate.USBPath
	}
	if config.ModemIMEI == "" && entry.Snapshot != nil {
		config.ModemIMEI = entry.Snapshot.IMEI
	}
	if config.DeviceBackend == "" {
		config.DeviceBackend = backendMode(candidate)
	}
	if config.ESIMTransport == "" {
		config.ESIMTransport = config.DeviceBackend
	}
}

func discoveredDeviceType(candidate modem.Candidate) string {
	if candidate.HardwareKind == "pcsc" {
		return store.DeviceTypeUSBSIMReader
	}
	if modem.IsDJI4GUSB(candidate.VendorID, candidate.ProductID) {
		return store.DeviceTypeDJI4G
	}
	return ""
}

func modemSummary(snapshot *device.Snapshot, phone string, phoneSource string) map[string]any {
	if snapshot == nil {
		return map[string]any{
			"operator":                  "",
			"native_mcc":                "",
			"native_mnc":                "",
			"native_spn":                "",
			"operator_country_code":     "",
			"card_mcc":                  "",
			"card_mnc":                  "",
			"card_country":              "",
			"home_carrier_name":         "",
			"home_carrier_plmn":         "",
			"home_carrier_country_code": "",
			"service_blocked":           false,
			"blocked_reason":            "",
			"network_mode":              "",
			"radio_band":                "",
			"radio_channel":             0,
			"signal_dbm":                0,
			"signal_sinr":               0,
			"imei":                      "",
			"iccid":                     "",
			"reg_status":                0,
			"reg_status_text":           "not refreshed",
			"sim_inserted":              false,
			"phone_number":              phone,
			"phone_number_source":       phoneSource,
			"model":                     "",
		}
	}
	mcc, mnc := splitPLMN(snapshot.OperatorCode)
	_, operatorCountryCode, _ := device.CarrierForPLMN(snapshot.OperatorCode)
	cardMCC, cardMNC := device.CardMCCMNCWithLength(snapshot.IMSI, snapshot.MNCLength)
	homePLMN, homeCarrier, homeCountry, _ := device.CarrierForSIM(device.CarrierIdentity{
		IMSI: snapshot.IMSI, ICCID: snapshot.ICCID, SPN: snapshot.SPN,
		GID1: snapshot.GID1, GID2: snapshot.GID2, MNCLength: snapshot.MNCLength,
	})
	blockedReason := device.RegionBlockReason(snapshot.IMSI)
	return map[string]any{
		"operator":                  snapshot.OperatorName,
		"native_mcc":                mcc,
		"native_mnc":                mnc,
		"native_spn":                snapshot.SPN,
		"operator_country_code":     operatorCountryCode,
		"card_mcc":                  cardMCC,
		"card_mnc":                  cardMNC,
		"card_country":              countryNameForMCC(cardMCC),
		"home_carrier_name":         homeCarrier,
		"home_carrier_plmn":         homePLMN,
		"home_carrier_country_code": homeCountry,
		"service_blocked":           blockedReason != "",
		"blocked_reason":            blockedReason,
		"network_mode":              snapshot.AccessTech,
		"network_duplex":            "",
		"radio_band":                snapshot.Band,
		"radio_channel":             parseDecimal(snapshot.Channel),
		"signal_dbm":                pointerInt(snapshot.RSSIDBm),
		"signal_rsrp":               pointerInt(snapshot.RSRP),
		"signal_rsrq":               pointerInt(snapshot.RSRQ),
		"signal_sinr":               pointerInt(snapshot.SINR),
		"imei":                      snapshot.IMEI,
		"iccid":                     snapshot.ICCID,
		"imsi":                      snapshot.IMSI,
		"firmware":                  snapshot.Firmware,
		"model":                     snapshot.Model,
		"reg_status":                snapshot.RegistrationStatus,
		"reg_status_text":           registrationText(snapshot),
		"ps_attached":               snapshot.PSAttached,
		"sim_inserted":              snapshotHasSIM(snapshot),
		"operating_mode":            snapshot.OperatingMode,
		"phone_number":              phone,
		"phone_number_source":       phoneSource,
	}
}

func snapshotHasSIM(snapshot *device.Snapshot) bool {
	if snapshot == nil {
		return false
	}
	if snapshot.SIMReady || strings.TrimSpace(snapshot.ICCID) != "" || strings.TrimSpace(snapshot.IMSI) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(snapshot.SIMStatus)) {
	case "", "unknown", "not_inserted", "not inserted", "absent":
		return false
	default:
		// PIN/PUK and other explicit UICC states prove that a card is present.
		return true
	}
}

func idleVoWiFiRuntime(id string, snapshot *device.Snapshot) map[string]any {
	iccid := ""
	imsi := ""
	phone := ""
	source := ""
	simReady := false
	if snapshot != nil {
		iccid = snapshot.ICCID
		imsi = snapshot.IMSI
		phone = snapshot.Phone.Number
		source = snapshot.Phone.Source
		simReady = snapshot.SIMReady
	}
	return map[string]any{
		"device_id":           id,
		"phase":               "idle",
		"enabled":             false,
		"active":              false,
		"dataplane_mode":      "",
		"iccid":               iccid,
		"imsi":                imsi,
		"sim_ready":           simReady,
		"access_ready":        false,
		"tunnel_ready":        false,
		"ims_ready":           false,
		"sms_ready":           false,
		"reg_status":          0,
		"reg_status_text":     "not started",
		"network_mode":        "",
		"local_phone":         phone,
		"phone_number_source": source,
		"last_error_class":    "",
		"last_error":          "",
		"last_reason":         "disabled",
		"updated_at":          time.Now().UTC(),
	}
}

func deviceName(entry device.Device) string {
	if entry.Snapshot != nil && strings.TrimSpace(entry.Snapshot.Model) != "" {
		return entry.Snapshot.Model
	}
	if strings.TrimSpace(entry.Candidate.Product) != "" {
		return entry.Candidate.Product
	}
	return entry.ID
}

func backendMode(candidate modem.Candidate) string {
	if candidate.HardwareKind == "pcsc" {
		return "pcsc"
	}
	if candidate.QMIControl != "" {
		return "qmi"
	}
	return "at"
}

func lifecyclePhase(entry device.Device) string {
	switch {
	case !entry.Discovered:
		return "missing"
	case entry.LastError != "":
		return "degraded"
	case entry.Snapshot == nil:
		return "discovered"
	case entry.Snapshot.Responsive:
		return "ready"
	default:
		return "unresponsive"
	}
}

func registrationLabel(snapshot *device.Snapshot) string {
	if snapshot == nil {
		return "unknown"
	}
	switch snapshot.RegistrationStatus {
	case 1, 5:
		return "registered"
	case 2:
		return "searching"
	case 3:
		return "denied"
	default:
		return "unknown"
	}
}

func registrationText(snapshot *device.Snapshot) string {
	if snapshot == nil {
		return "unknown"
	}
	switch snapshot.RegistrationStatus {
	case 1:
		return "registered"
	case 5:
		return "registered (roaming)"
	case 2:
		return "searching"
	case 3:
		return "registration denied"
	case 0:
		return "not registered"
	default:
		return "unknown"
	}
}

func splitPLMN(value string) (string, string) {
	value = strings.TrimSpace(value)
	if len(value) < 5 {
		return "", ""
	}
	return value[:3], value[3:]
}

func parseDecimal(value string) int {
	number, _ := strconv.Atoi(strings.TrimSpace(value))
	return number
}

func pointerInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func snapshotString(
	snapshot *device.Snapshot,
	selector func(*device.Snapshot) string,
) string {
	if snapshot == nil {
		return ""
	}
	return selector(snapshot)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
