package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

const (
	DeviceTypeWiFi410      = "wifi_410"
	DeviceTypeDJI4G        = "dji_4g"
	DeviceTypePCIeEC20EC25 = "pcie_ec20_ec25"
)

// NormalizeDeviceType returns a stable persisted device type identifier.
// Empty values use the legacy EC20/EC25 type for backwards compatibility.
func NormalizeDeviceType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case DeviceTypeWiFi410:
		return DeviceTypeWiFi410
	case DeviceTypeDJI4G:
		return DeviceTypeDJI4G
	case "", DeviceTypePCIeEC20EC25:
		return DeviceTypePCIeEC20EC25
	default:
		return ""
	}
}

func (s *Store) UpsertDevice(ctx context.Context, value Device) error {
	return upsertDevice(ctx, s.db, value)
}

// SaveDeviceState stores configuration and the supplied runtime snapshots in
// one transaction. A nil runtime leaves that snapshot untouched.
func (s *Store) SaveDeviceState(
	ctx context.Context,
	value Device,
	runtime *DeviceRuntime,
	vowifi *VoWiFiRuntime,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin device state update: %w", err)
	}
	defer tx.Rollback()

	if err := upsertDevice(ctx, tx, value); err != nil {
		return err
	}
	if runtime != nil {
		snapshot := *runtime
		if snapshot.DeviceID == "" {
			snapshot.DeviceID = value.ID
		}
		if snapshot.DeviceID != value.ID {
			return errors.New("device runtime belongs to a different device")
		}
		if err := upsertDeviceRuntime(ctx, tx, snapshot); err != nil {
			return err
		}
	}
	if vowifi != nil {
		snapshot := *vowifi
		if snapshot.DeviceID == "" {
			snapshot.DeviceID = value.ID
		}
		if snapshot.DeviceID != value.ID {
			return errors.New("VoWiFi runtime belongs to a different device")
		}
		if err := upsertVoWiFiRuntime(ctx, tx, snapshot); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit device state update: %w", err)
	}
	return nil
}

func upsertDevice(ctx context.Context, executor contextExecer, value Device) error {
	value.ID = strings.TrimSpace(value.ID)
	value.Name = strings.TrimSpace(value.Name)
	if value.ID == "" {
		return errors.New("device id is required")
	}
	if value.Name == "" {
		return errors.New("device name is required")
	}
	value.DeviceType = NormalizeDeviceType(value.DeviceType)
	if value.DeviceType == "" {
		return errors.New("unsupported device type")
	}
	if value.ProxyPort < 0 || value.ProxyPort > 65535 {
		return errors.New("device proxy port must be between 0 and 65535")
	}
	if value.BaudRate == 0 {
		value.BaudRate = 115200
	}
	if value.BaudRate < 1 {
		return errors.New("device baud rate must be positive")
	}
	if value.DataBits == 0 {
		value.DataBits = 8
	}
	if value.DataBits < 5 || value.DataBits > 8 {
		return errors.New("device data bits must be between 5 and 8")
	}
	if value.StopBits == 0 {
		value.StopBits = 1
	}
	if value.StopBits != 1 && value.StopBits != 2 {
		return errors.New("device stop bits must be 1 or 2")
	}
	if value.Parity == "" {
		value.Parity = "none"
	}
	value.Parity = strings.ToLower(strings.TrimSpace(value.Parity))
	switch value.Parity {
	case "none", "even", "odd", "mark", "space":
	default:
		return fmt.Errorf("unsupported device parity %q", value.Parity)
	}
	if value.DeviceBackend == "" {
		value.DeviceBackend = "at"
	}
	value.DeviceBackend = strings.ToLower(strings.TrimSpace(value.DeviceBackend))
	if value.DeviceBackend != "at" && value.DeviceBackend != "qmi" {
		return fmt.Errorf("unsupported device backend %q", value.DeviceBackend)
	}
	if value.ESIMTransport == "" {
		value.ESIMTransport = "at"
	}
	value.ESIMTransport = strings.ToLower(strings.TrimSpace(value.ESIMTransport))
	if value.ESIMTransport != "at" && value.ESIMTransport != "qmi" {
		return fmt.Errorf("unsupported eSIM transport %q", value.ESIMTransport)
	}
	carrierProfileUnset := strings.TrimSpace(value.IMSAPN) == "" &&
		strings.TrimSpace(value.IMSPrivateIdentity) == "" &&
		strings.TrimSpace(value.IMSPublicIdentity) == "" &&
		strings.TrimSpace(value.IMSSMSCenter) == "" &&
		strings.TrimSpace(value.IMSTransport) == "" &&
		strings.TrimSpace(value.VoWiFiEAPMethod) == ""
	if carrierProfileUnset {
		value.IMSAllowIMSIDerivedIdentity = true
	}
	value.IMSAPN = strings.TrimSpace(value.IMSAPN)
	if value.IMSAPN == "" {
		value.IMSAPN = "ims"
	}
	if !validIMSAPN(value.IMSAPN) {
		return errors.New("IMS APN contains invalid characters")
	}
	value.IMSPrivateIdentity = strings.TrimSpace(value.IMSPrivateIdentity)
	value.IMSPublicIdentity = strings.TrimSpace(value.IMSPublicIdentity)
	if (value.IMSPrivateIdentity == "") != (value.IMSPublicIdentity == "") {
		return errors.New("IMS private and public identities must be configured together")
	}
	if !value.IMSAllowIMSIDerivedIdentity && value.IMSPrivateIdentity == "" {
		return errors.New("IMS private and public identities are required when IMSI derivation is disabled")
	}
	for name, identity := range map[string]string{
		"private": value.IMSPrivateIdentity,
		"public":  value.IMSPublicIdentity,
	} {
		if len(identity) > 512 || strings.ContainsAny(identity, "\r\n") {
			return fmt.Errorf("IMS %s identity is invalid", name)
		}
	}
	value.IMSSMSCenter = strings.TrimSpace(value.IMSSMSCenter)
	if !validIMSSMSCenter(value.IMSSMSCenter) {
		return errors.New("IMS SMS service-centre address is invalid")
	}
	value.IMSTransport = strings.ToLower(strings.TrimSpace(value.IMSTransport))
	if value.IMSTransport == "" {
		value.IMSTransport = "tcp"
	}
	if value.IMSTransport != "tcp" && value.IMSTransport != "udp" {
		return fmt.Errorf("unsupported IMS transport %q", value.IMSTransport)
	}
	value.VoWiFiEAPMethod = strings.ToLower(strings.TrimSpace(value.VoWiFiEAPMethod))
	if value.VoWiFiEAPMethod == "" {
		value.VoWiFiEAPMethod = "aka"
	}
	if value.VoWiFiEAPMethod != "aka" && value.VoWiFiEAPMethod != "aka-prime" {
		return fmt.Errorf("unsupported VoWiFi EAP method %q", value.VoWiFiEAPMethod)
	}
	extra, err := normalizeJSONObject(value.Extra)
	if err != nil {
		return fmt.Errorf("normalize device extra data: %w", err)
	}
	now := time.Now().UTC()
	createdAt := value.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := value.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}

	_, err = executor.ExecContext(ctx, `
		INSERT INTO devices (
			id, name, device_type, interface, control_device, at_port, usb_path,
			audio_device, modem_imei, apn, ims_apn, ims_private_identity,
			ims_public_identity, ims_sms_center, ims_transport,
			ims_allow_imsi_derived_identity, vowifi_eap_method,
			vowifi_allow_sha1, vowifi_use_modp1024, proxy_port, baud_rate,
			data_bits, stop_bits, parity, device_backend, esim_transport,
			qmi_use_proxy, qmi_proxy_path, qmi_proxy_executable,
			network_enabled, sms_enabled, vowifi_enabled, extra_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			device_type = excluded.device_type,
			interface = excluded.interface,
			control_device = excluded.control_device,
			at_port = excluded.at_port,
			usb_path = excluded.usb_path,
			audio_device = excluded.audio_device,
			modem_imei = excluded.modem_imei,
			apn = excluded.apn,
			ims_apn = excluded.ims_apn,
			ims_private_identity = excluded.ims_private_identity,
			ims_public_identity = excluded.ims_public_identity,
			ims_sms_center = excluded.ims_sms_center,
			ims_transport = excluded.ims_transport,
			ims_allow_imsi_derived_identity = excluded.ims_allow_imsi_derived_identity,
			vowifi_eap_method = excluded.vowifi_eap_method,
			vowifi_allow_sha1 = excluded.vowifi_allow_sha1,
			vowifi_use_modp1024 = excluded.vowifi_use_modp1024,
			proxy_port = excluded.proxy_port,
			baud_rate = excluded.baud_rate,
			data_bits = excluded.data_bits,
			stop_bits = excluded.stop_bits,
			parity = excluded.parity,
			device_backend = excluded.device_backend,
			esim_transport = excluded.esim_transport,
			qmi_use_proxy = excluded.qmi_use_proxy,
			qmi_proxy_path = excluded.qmi_proxy_path,
			qmi_proxy_executable = excluded.qmi_proxy_executable,
			network_enabled = excluded.network_enabled,
			sms_enabled = excluded.sms_enabled,
			vowifi_enabled = excluded.vowifi_enabled,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at
	`,
		value.ID, value.Name, value.DeviceType, value.Interface, value.ControlDevice, value.ATPort,
		value.USBPath, value.AudioDevice, value.ModemIMEI, value.APN, value.IMSAPN,
		value.IMSPrivateIdentity, value.IMSPublicIdentity, value.IMSSMSCenter, value.IMSTransport,
		boolInt(value.IMSAllowIMSIDerivedIdentity), value.VoWiFiEAPMethod,
		boolInt(value.VoWiFiAllowSHA1), boolInt(value.VoWiFiUseMODP1024),
		value.ProxyPort, value.BaudRate, value.DataBits, value.StopBits,
		value.Parity, value.DeviceBackend, value.ESIMTransport,
		boolInt(value.QMIUseProxy), value.QMIProxyPath, value.QMIProxyExecutable,
		boolInt(value.NetworkEnabled), boolInt(value.SMSEnabled),
		boolInt(value.VoWiFiEnabled), string(extra), createdAt.Unix(),
		updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert device %q: %w", value.ID, err)
	}
	return nil
}

func (s *Store) Device(ctx context.Context, id string) (Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx, deviceSelect+` WHERE id = ?`, id))
}

func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, deviceSelect+` ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	values := make([]Device, 0)
	for rows.Next() {
		value, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate devices: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete device %q: %w", id, err)
	}
	defer tx.Rollback()
	var modemIMEI string
	if err := tx.QueryRowContext(ctx, `SELECT modem_imei FROM devices WHERE id = ?`, id).Scan(&modemIMEI); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read device %q before deletion: %w", id, err)
	}
	// SMS history must outlive a mutable configured device ID. Anchor any
	// legacy ID-owned rows to the physical modem before the device row and its
	// runtime records are removed.
	if strings.TrimSpace(modemIMEI) != "" {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM sms_messages AS legacy
			WHERE legacy.device_id = ? AND legacy.modem_imei = '' AND legacy.message_id <> ''
				AND EXISTS (
					SELECT 1 FROM sms_messages current
					WHERE current.modem_imei = ?
						AND current.message_id = legacy.message_id
				)
		`, id, strings.TrimSpace(modemIMEI)); err != nil {
			return fmt.Errorf("deduplicate SMS history for device %q: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sms_messages
			SET modem_imei = ?, updated_at = ?
			WHERE device_id = ? AND modem_imei = ''
		`, strings.TrimSpace(modemIMEI), time.Now().UTC().Unix(), id); err != nil {
			return fmt.Errorf("anchor SMS history for device %q: %w", id, err)
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete device %q: %w", id, err)
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete device %q: %w", id, err)
	}
	return nil
}

const deviceSelect = `
	SELECT id, name, device_type, interface, control_device, at_port, usb_path,
		audio_device, modem_imei, apn, ims_apn, ims_private_identity,
		ims_public_identity, ims_sms_center, ims_transport,
		ims_allow_imsi_derived_identity, vowifi_eap_method,
		vowifi_allow_sha1, vowifi_use_modp1024, proxy_port, baud_rate, data_bits,
		stop_bits, parity, device_backend, esim_transport, qmi_use_proxy,
		qmi_proxy_path, qmi_proxy_executable, network_enabled, sms_enabled,
		vowifi_enabled, extra_json, created_at, updated_at
	FROM devices`

func scanDevice(row rowScanner) (Device, error) {
	var value Device
	var qmiUseProxy, networkEnabled, smsEnabled, vowifiEnabled, imsAllowDerived, vowifiAllowSHA1, vowifiUseMODP1024 int
	var extra string
	var createdAt, updatedAt int64
	err := row.Scan(
		&value.ID, &value.Name, &value.DeviceType, &value.Interface, &value.ControlDevice,
		&value.ATPort, &value.USBPath, &value.AudioDevice, &value.ModemIMEI,
		&value.APN, &value.IMSAPN, &value.IMSPrivateIdentity, &value.IMSPublicIdentity,
		&value.IMSSMSCenter, &value.IMSTransport, &imsAllowDerived, &value.VoWiFiEAPMethod,
		&vowifiAllowSHA1, &vowifiUseMODP1024,
		&value.ProxyPort, &value.BaudRate, &value.DataBits,
		&value.StopBits, &value.Parity, &value.DeviceBackend,
		&value.ESIMTransport, &qmiUseProxy, &value.QMIProxyPath,
		&value.QMIProxyExecutable, &networkEnabled, &smsEnabled,
		&vowifiEnabled, &extra, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	value.QMIUseProxy = qmiUseProxy != 0
	value.IMSAllowIMSIDerivedIdentity = imsAllowDerived != 0
	value.VoWiFiAllowSHA1 = vowifiAllowSHA1 != 0
	value.VoWiFiUseMODP1024 = vowifiUseMODP1024 != 0
	value.NetworkEnabled = networkEnabled != 0
	value.SMSEnabled = smsEnabled != 0
	value.VoWiFiEnabled = vowifiEnabled != 0
	value.DeviceType = NormalizeDeviceType(value.DeviceType)
	value.Extra = []byte(extra)
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func validIMSAPN(value string) bool {
	if len(value) < 1 || len(value) > 253 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validIMSSMSCenter(value string) bool {
	if value == "" {
		return true
	}
	digits := strings.TrimPrefix(value, "+")
	if len(digits) < 3 || len(digits) > 20 {
		return false
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (s *Store) UpsertDeviceRuntime(ctx context.Context, value DeviceRuntime) error {
	return upsertDeviceRuntime(ctx, s.db, value)
}

func upsertDeviceRuntime(
	ctx context.Context,
	executor contextExecer,
	value DeviceRuntime,
) error {
	if strings.TrimSpace(value.DeviceID) == "" {
		return errors.New("device runtime device id is required")
	}
	traffic, err := normalizeJSONObject(value.Traffic)
	if err != nil {
		return fmt.Errorf("normalize device traffic: %w", err)
	}
	extra, err := normalizeJSONObject(value.Extra)
	if err != nil {
		return fmt.Errorf("normalize device runtime extra data: %w", err)
	}
	updatedAt := value.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = executor.ExecContext(ctx, `
		INSERT INTO device_runtime (
			device_id, running, healthy, control_online, physical_present,
			worker_running, data_connected, radio_registered, network_connected,
			flight_mode, lifecycle_phase, lifecycle_reason, public_ip, private_ip,
			operator, native_mcc, native_mnc, native_spn, network_mode,
			network_duplex, radio_band, radio_channel, signal_dbm, signal_rsrp,
			signal_rsrq, signal_sinr, imei, iccid, imsi, firmware, reg_status,
			reg_status_text, ps_attached, sim_inserted, operating_mode,
			phone_number, phone_number_source, traffic_json, extra_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			running = excluded.running,
			healthy = excluded.healthy,
			control_online = excluded.control_online,
			physical_present = excluded.physical_present,
			worker_running = excluded.worker_running,
			data_connected = excluded.data_connected,
			radio_registered = excluded.radio_registered,
			network_connected = excluded.network_connected,
			flight_mode = excluded.flight_mode,
			lifecycle_phase = excluded.lifecycle_phase,
			lifecycle_reason = excluded.lifecycle_reason,
			public_ip = excluded.public_ip,
			private_ip = excluded.private_ip,
			operator = excluded.operator,
			native_mcc = excluded.native_mcc,
			native_mnc = excluded.native_mnc,
			native_spn = excluded.native_spn,
			network_mode = excluded.network_mode,
			network_duplex = excluded.network_duplex,
			radio_band = excluded.radio_band,
			radio_channel = excluded.radio_channel,
			signal_dbm = excluded.signal_dbm,
			signal_rsrp = excluded.signal_rsrp,
			signal_rsrq = excluded.signal_rsrq,
			signal_sinr = excluded.signal_sinr,
			imei = excluded.imei,
			iccid = excluded.iccid,
			imsi = excluded.imsi,
			firmware = excluded.firmware,
			reg_status = excluded.reg_status,
			reg_status_text = excluded.reg_status_text,
			ps_attached = excluded.ps_attached,
			sim_inserted = excluded.sim_inserted,
			operating_mode = excluded.operating_mode,
			phone_number = excluded.phone_number,
			phone_number_source = excluded.phone_number_source,
			traffic_json = excluded.traffic_json,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at
	`,
		value.DeviceID, boolInt(value.Running), boolInt(value.Healthy),
		boolInt(value.ControlOnline), boolInt(value.PhysicalPresent),
		boolInt(value.WorkerRunning), boolInt(value.DataConnected),
		boolInt(value.RadioRegistered), boolInt(value.NetworkConnected),
		boolInt(value.FlightMode), value.LifecyclePhase, value.LifecycleReason,
		value.PublicIP, value.PrivateIP, value.Operator, value.NativeMCC,
		value.NativeMNC, value.NativeSPN, value.NetworkMode,
		value.NetworkDuplex, value.RadioBand, value.RadioChannel,
		value.SignalDBM, nullableInt(value.SignalRSRP), nullableInt(value.SignalRSRQ),
		nullableInt(value.SignalSINR), value.IMEI, value.ICCID, value.IMSI,
		value.Firmware, value.RegStatus, value.RegStatusText,
		nullableBool(value.PSAttached), nullableBool(value.SIMInserted),
		nullableInt(value.OperatingMode), value.PhoneNumber,
		value.PhoneNumberSource, string(traffic), string(extra), updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert runtime for device %q: %w", value.DeviceID, err)
	}
	return nil
}

func (s *Store) DeviceRuntime(ctx context.Context, deviceID string) (DeviceRuntime, error) {
	return scanDeviceRuntime(s.db.QueryRowContext(ctx, deviceRuntimeSelect+` WHERE device_id = ?`, deviceID))
}

const deviceRuntimeSelect = `
	SELECT device_id, running, healthy, control_online, physical_present,
		worker_running, data_connected, radio_registered, network_connected,
		flight_mode, lifecycle_phase, lifecycle_reason, public_ip, private_ip,
		operator, native_mcc, native_mnc, native_spn, network_mode,
		network_duplex, radio_band, radio_channel, signal_dbm, signal_rsrp,
		signal_rsrq, signal_sinr, imei, iccid, imsi, firmware, reg_status,
		reg_status_text, ps_attached, sim_inserted, operating_mode,
		phone_number, phone_number_source, traffic_json, extra_json, updated_at
	FROM device_runtime`

func scanDeviceRuntime(row rowScanner) (DeviceRuntime, error) {
	var value DeviceRuntime
	var running, healthy, controlOnline, physicalPresent int
	var workerRunning, dataConnected, radioRegistered, networkConnected int
	var flightMode int
	var signalRSRP, signalRSRQ, signalSINR sql.NullInt64
	var psAttached, simInserted, operatingMode sql.NullInt64
	var traffic, extra string
	var updatedAt int64
	err := row.Scan(
		&value.DeviceID, &running, &healthy, &controlOnline, &physicalPresent,
		&workerRunning, &dataConnected, &radioRegistered, &networkConnected,
		&flightMode, &value.LifecyclePhase, &value.LifecycleReason,
		&value.PublicIP, &value.PrivateIP, &value.Operator, &value.NativeMCC,
		&value.NativeMNC, &value.NativeSPN, &value.NetworkMode,
		&value.NetworkDuplex, &value.RadioBand, &value.RadioChannel,
		&value.SignalDBM, &signalRSRP, &signalRSRQ, &signalSINR, &value.IMEI,
		&value.ICCID, &value.IMSI, &value.Firmware, &value.RegStatus,
		&value.RegStatusText, &psAttached, &simInserted, &operatingMode,
		&value.PhoneNumber, &value.PhoneNumberSource, &traffic, &extra, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceRuntime{}, ErrNotFound
	}
	if err != nil {
		return DeviceRuntime{}, err
	}
	value.Running = running != 0
	value.Healthy = healthy != 0
	value.ControlOnline = controlOnline != 0
	value.PhysicalPresent = physicalPresent != 0
	value.WorkerRunning = workerRunning != 0
	value.DataConnected = dataConnected != 0
	value.RadioRegistered = radioRegistered != 0
	value.NetworkConnected = networkConnected != 0
	value.FlightMode = flightMode != 0
	value.SignalRSRP = nullIntPointer(signalRSRP)
	value.SignalRSRQ = nullIntPointer(signalRSRQ)
	value.SignalSINR = nullIntPointer(signalSINR)
	value.PSAttached = nullBoolPointer(psAttached)
	value.SIMInserted = nullBoolPointer(simInserted)
	value.OperatingMode = nullIntPointer(operatingMode)
	value.Traffic = []byte(traffic)
	value.Extra = []byte(extra)
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) DeleteDeviceRuntime(ctx context.Context, deviceID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM device_runtime WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("delete runtime for device %q: %w", deviceID, err)
	}
	return requireAffected(result)
}

func (s *Store) UpsertVoWiFiRuntime(ctx context.Context, value VoWiFiRuntime) error {
	return upsertVoWiFiRuntime(ctx, s.db, value)
}

func upsertVoWiFiRuntime(
	ctx context.Context,
	executor contextExecer,
	value VoWiFiRuntime,
) error {
	if strings.TrimSpace(value.DeviceID) == "" {
		return errors.New("VoWiFi runtime device id is required")
	}
	tunnel, err := normalizeJSONObject(value.Tunnel)
	if err != nil {
		return fmt.Errorf("normalize VoWiFi tunnel state: %w", err)
	}
	imscore, err := normalizeJSONObject(value.IMSCore)
	if err != nil {
		return fmt.Errorf("normalize VoWiFi IMS state: %w", err)
	}
	smsip, err := normalizeJSONObject(value.SMSIP)
	if err != nil {
		return fmt.Errorf("normalize VoWiFi SMS state: %w", err)
	}
	extra, err := normalizeJSONObject(value.Extra)
	if err != nil {
		return fmt.Errorf("normalize VoWiFi extra state: %w", err)
	}
	updatedAt := value.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = executor.ExecContext(ctx, `
		INSERT INTO vowifi_runtime (
			device_id, phase, dataplane_mode, iccid, imsi, sim_ready,
			access_ready, tunnel_ready, ims_ready, sms_ready, reg_status,
			reg_status_text, network_mode, local_phone, phone_number_source,
			last_error_class, last_error, last_reason, tunnel_json,
			imscore_json, smsip_json, extra_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			phase = excluded.phase,
			dataplane_mode = excluded.dataplane_mode,
			iccid = excluded.iccid,
			imsi = excluded.imsi,
			sim_ready = excluded.sim_ready,
			access_ready = excluded.access_ready,
			tunnel_ready = excluded.tunnel_ready,
			ims_ready = excluded.ims_ready,
			sms_ready = excluded.sms_ready,
			reg_status = excluded.reg_status,
			reg_status_text = excluded.reg_status_text,
			network_mode = excluded.network_mode,
			local_phone = excluded.local_phone,
			phone_number_source = excluded.phone_number_source,
			last_error_class = excluded.last_error_class,
			last_error = excluded.last_error,
			last_reason = excluded.last_reason,
			tunnel_json = excluded.tunnel_json,
			imscore_json = excluded.imscore_json,
			smsip_json = excluded.smsip_json,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at
	`,
		value.DeviceID, value.Phase, value.DataplaneMode, value.ICCID,
		value.IMSI, boolInt(value.SIMReady), boolInt(value.AccessReady),
		boolInt(value.TunnelReady), boolInt(value.IMSReady),
		boolInt(value.SMSReady), value.RegStatus, value.RegStatusText,
		value.NetworkMode, value.LocalPhone, value.PhoneNumberSource,
		value.LastErrorClass, value.LastError, value.LastReason,
		string(tunnel), string(imscore), string(smsip), string(extra),
		updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert VoWiFi runtime for device %q: %w", value.DeviceID, err)
	}
	return nil
}

func (s *Store) VoWiFiRuntime(ctx context.Context, deviceID string) (VoWiFiRuntime, error) {
	return scanVoWiFiRuntime(s.db.QueryRowContext(ctx, vowifiRuntimeSelect+` WHERE device_id = ?`, deviceID))
}

const vowifiRuntimeSelect = `
	SELECT device_id, phase, dataplane_mode, iccid, imsi, sim_ready,
		access_ready, tunnel_ready, ims_ready, sms_ready, reg_status,
		reg_status_text, network_mode, local_phone, phone_number_source,
		last_error_class, last_error, last_reason, tunnel_json, imscore_json,
		smsip_json, extra_json, updated_at
	FROM vowifi_runtime`

func scanVoWiFiRuntime(row rowScanner) (VoWiFiRuntime, error) {
	var value VoWiFiRuntime
	var simReady, accessReady, tunnelReady, imsReady, smsReady int
	var tunnel, imscore, smsip, extra string
	var updatedAt int64
	err := row.Scan(
		&value.DeviceID, &value.Phase, &value.DataplaneMode, &value.ICCID,
		&value.IMSI, &simReady, &accessReady, &tunnelReady, &imsReady,
		&smsReady, &value.RegStatus, &value.RegStatusText, &value.NetworkMode,
		&value.LocalPhone, &value.PhoneNumberSource, &value.LastErrorClass,
		&value.LastError, &value.LastReason, &tunnel, &imscore, &smsip,
		&extra, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return VoWiFiRuntime{}, ErrNotFound
	}
	if err != nil {
		return VoWiFiRuntime{}, err
	}
	value.SIMReady = simReady != 0
	value.AccessReady = accessReady != 0
	value.TunnelReady = tunnelReady != 0
	value.IMSReady = imsReady != 0
	value.SMSReady = smsReady != 0
	value.Tunnel = []byte(tunnel)
	value.IMSCore = []byte(imscore)
	value.SMSIP = []byte(smsip)
	value.Extra = []byte(extra)
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) DeleteVoWiFiRuntime(ctx context.Context, deviceID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM vowifi_runtime WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("delete VoWiFi runtime for device %q: %w", deviceID, err)
	}
	return requireAffected(result)
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func nullIntPointer(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}

func nullBoolPointer(value sql.NullInt64) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Int64 != 0
	return &result
}
