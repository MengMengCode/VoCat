package store

func migrationStatements(version int) []string {
	switch version {
	case 1:
		return []string{
			`CREATE TABLE IF NOT EXISTS admins (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				username TEXT NOT NULL UNIQUE,
				password_hash BLOB NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS sessions (
				token_hash BLOB PRIMARY KEY,
				admin_id INTEGER NOT NULL,
				csrf_hash BLOB NOT NULL,
				expires_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				FOREIGN KEY (admin_id) REFERENCES admins(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at)`,
		}
	case 2:
		return domainSchemaV2
	case 3:
		return []string{
			`CREATE TABLE phone_associations (
				iccid TEXT PRIMARY KEY,
				device_id TEXT NOT NULL DEFAULT '',
				number TEXT NOT NULL,
				source TEXT NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE INDEX phone_associations_device_idx
				ON phone_associations(device_id, updated_at DESC)`,
		}
	case 4:
		return []string{
			// The conversation UI is a received-message view. Keep the SMSC
			// timestamp for diagnostics, but display the first local receipt time.
			`UPDATE sms_messages
			SET extra_json = json_set(
					extra_json,
					'$.service_center_timestamp_unix', message_time,
					'$.received_at_unix', created_at
				),
				message_time = created_at,
				updated_at = CAST(strftime('%s', 'now') AS INTEGER)
			WHERE source = 'ims'
				AND json_valid(extra_json)
				AND COALESCE(json_extract(extra_json, '$.raw_tpdu'), '') <> ''`,
		}
	case 5:
		return []string{
			// Earlier IMS persistence labelled every non-delivered submission as
			// accepted_by_ims, including explicit SIP/RP rejection evidence.
			`UPDATE sms_messages
			SET delivery_state = 'failed',
				updated_at = CAST(strftime('%s', 'now') AS INTEGER)
			WHERE source = 'ims'
				AND direction IN ('outbound', 'sent')
				AND delivery_state = 'accepted_by_ims'
				AND (
					LOWER(status) LIKE '%reject%'
					OR LOWER(status) LIKE '%fail%'
					OR LOWER(status) LIKE '%partial%'
				)`,
		}
	case 6:
		return []string{
			`CREATE TABLE IF NOT EXISTS device_proxy_bindings (
				device_id TEXT PRIMARY KEY,
				upstream_proxy_id TEXT NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
				FOREIGN KEY (upstream_proxy_id) REFERENCES upstream_proxies(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS device_proxy_bindings_proxy_idx
				ON device_proxy_bindings(upstream_proxy_id)`,
		}
	case 7:
		return []string{
			`ALTER TABLE sms_messages
				ADD COLUMN modem_imei TEXT NOT NULL DEFAULT ''`,
			`UPDATE sms_messages
			SET modem_imei = COALESCE((
				SELECT NULLIF(d.modem_imei, '')
				FROM devices d
				WHERE d.id = sms_messages.device_id
			), '')
			WHERE modem_imei = ''`,
			`DELETE FROM sms_messages
			WHERE modem_imei <> '' AND message_id <> ''
				AND id NOT IN (
					SELECT MIN(id)
					FROM sms_messages
					WHERE modem_imei <> '' AND message_id <> ''
					GROUP BY modem_imei, message_id
				)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS sms_messages_hardware_external_id_idx
				ON sms_messages(modem_imei, message_id)
				WHERE modem_imei <> '' AND message_id <> ''`,
			`CREATE INDEX IF NOT EXISTS sms_messages_hardware_thread_idx
				ON sms_messages(modem_imei, imsi, peer, message_time DESC, id DESC)`,
		}
	case 8:
		return []string{
			`ALTER TABLE devices
				ADD COLUMN device_type TEXT NOT NULL DEFAULT 'pcie_ec20_ec25'`,
		}
	case 9:
		return []string{
			`ALTER TABLE devices
				ADD COLUMN ims_apn TEXT NOT NULL DEFAULT 'ims'`,
			`ALTER TABLE devices
				ADD COLUMN ims_private_identity TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE devices
				ADD COLUMN ims_public_identity TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE devices
				ADD COLUMN ims_sms_center TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE devices
				ADD COLUMN ims_transport TEXT NOT NULL DEFAULT 'tcp'`,
			`ALTER TABLE devices
				ADD COLUMN ims_allow_imsi_derived_identity INTEGER NOT NULL DEFAULT 1
					CHECK (ims_allow_imsi_derived_identity IN (0, 1))`,
			`ALTER TABLE devices
				ADD COLUMN vowifi_eap_method TEXT NOT NULL DEFAULT 'aka'`,
		}
	case 10:
		return []string{
			`ALTER TABLE devices
				ADD COLUMN vowifi_allow_sha1 INTEGER NOT NULL DEFAULT 0
					CHECK (vowifi_allow_sha1 IN (0, 1))`,
			`ALTER TABLE devices
				ADD COLUMN vowifi_use_modp1024 INTEGER NOT NULL DEFAULT 0
					CHECK (vowifi_use_modp1024 IN (0, 1))`,
		}
	default:
		return nil
	}
}

var domainSchemaV2 = []string{
	`CREATE TABLE devices (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		interface TEXT NOT NULL DEFAULT '',
		control_device TEXT NOT NULL DEFAULT '',
		at_port TEXT NOT NULL DEFAULT '',
		usb_path TEXT NOT NULL DEFAULT '',
		audio_device TEXT NOT NULL DEFAULT '',
		modem_imei TEXT NOT NULL DEFAULT '',
		apn TEXT NOT NULL DEFAULT '',
		proxy_port INTEGER NOT NULL DEFAULT 0 CHECK (proxy_port BETWEEN 0 AND 65535),
		baud_rate INTEGER NOT NULL DEFAULT 115200 CHECK (baud_rate > 0),
		data_bits INTEGER NOT NULL DEFAULT 8,
		stop_bits INTEGER NOT NULL DEFAULT 1,
		parity TEXT NOT NULL DEFAULT 'none',
		device_backend TEXT NOT NULL DEFAULT 'at',
		esim_transport TEXT NOT NULL DEFAULT 'at',
		qmi_use_proxy INTEGER NOT NULL DEFAULT 1 CHECK (qmi_use_proxy IN (0, 1)),
		qmi_proxy_path TEXT NOT NULL DEFAULT '',
		qmi_proxy_executable TEXT NOT NULL DEFAULT '',
		network_enabled INTEGER NOT NULL DEFAULT 0 CHECK (network_enabled IN (0, 1)),
		sms_enabled INTEGER NOT NULL DEFAULT 1 CHECK (sms_enabled IN (0, 1)),
		vowifi_enabled INTEGER NOT NULL DEFAULT 0 CHECK (vowifi_enabled IN (0, 1)),
		extra_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE INDEX devices_interface_idx ON devices(interface)`,
	`CREATE INDEX devices_imei_idx ON devices(modem_imei)`,

	`CREATE TABLE device_runtime (
		device_id TEXT PRIMARY KEY,
		running INTEGER NOT NULL DEFAULT 0 CHECK (running IN (0, 1)),
		healthy INTEGER NOT NULL DEFAULT 0 CHECK (healthy IN (0, 1)),
		control_online INTEGER NOT NULL DEFAULT 0 CHECK (control_online IN (0, 1)),
		physical_present INTEGER NOT NULL DEFAULT 0 CHECK (physical_present IN (0, 1)),
		worker_running INTEGER NOT NULL DEFAULT 0 CHECK (worker_running IN (0, 1)),
		data_connected INTEGER NOT NULL DEFAULT 0 CHECK (data_connected IN (0, 1)),
		radio_registered INTEGER NOT NULL DEFAULT 0 CHECK (radio_registered IN (0, 1)),
		network_connected INTEGER NOT NULL DEFAULT 0 CHECK (network_connected IN (0, 1)),
		flight_mode INTEGER NOT NULL DEFAULT 0 CHECK (flight_mode IN (0, 1)),
		lifecycle_phase TEXT NOT NULL DEFAULT '',
		lifecycle_reason TEXT NOT NULL DEFAULT '',
		public_ip TEXT NOT NULL DEFAULT '',
		private_ip TEXT NOT NULL DEFAULT '',
		operator TEXT NOT NULL DEFAULT '',
		native_mcc TEXT NOT NULL DEFAULT '',
		native_mnc TEXT NOT NULL DEFAULT '',
		native_spn TEXT NOT NULL DEFAULT '',
		network_mode TEXT NOT NULL DEFAULT '',
		network_duplex TEXT NOT NULL DEFAULT '',
		radio_band TEXT NOT NULL DEFAULT '',
		radio_channel INTEGER NOT NULL DEFAULT 0,
		signal_dbm INTEGER NOT NULL DEFAULT 0,
		signal_rsrp INTEGER,
		signal_rsrq INTEGER,
		signal_sinr INTEGER,
		imei TEXT NOT NULL DEFAULT '',
		iccid TEXT NOT NULL DEFAULT '',
		imsi TEXT NOT NULL DEFAULT '',
		firmware TEXT NOT NULL DEFAULT '',
		reg_status INTEGER NOT NULL DEFAULT 0,
		reg_status_text TEXT NOT NULL DEFAULT '',
		ps_attached INTEGER CHECK (ps_attached IN (0, 1)),
		sim_inserted INTEGER CHECK (sim_inserted IN (0, 1)),
		operating_mode INTEGER,
		phone_number TEXT NOT NULL DEFAULT '',
		phone_number_source TEXT NOT NULL DEFAULT '',
		traffic_json TEXT NOT NULL DEFAULT '{}',
		extra_json TEXT NOT NULL DEFAULT '{}',
		updated_at INTEGER NOT NULL,
		FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX device_runtime_iccid_idx ON device_runtime(iccid)`,
	`CREATE INDEX device_runtime_imsi_idx ON device_runtime(imsi)`,

	`CREATE TABLE vowifi_runtime (
		device_id TEXT PRIMARY KEY,
		phase TEXT NOT NULL DEFAULT '',
		dataplane_mode TEXT NOT NULL DEFAULT '',
		iccid TEXT NOT NULL DEFAULT '',
		imsi TEXT NOT NULL DEFAULT '',
		sim_ready INTEGER NOT NULL DEFAULT 0 CHECK (sim_ready IN (0, 1)),
		access_ready INTEGER NOT NULL DEFAULT 0 CHECK (access_ready IN (0, 1)),
		tunnel_ready INTEGER NOT NULL DEFAULT 0 CHECK (tunnel_ready IN (0, 1)),
		ims_ready INTEGER NOT NULL DEFAULT 0 CHECK (ims_ready IN (0, 1)),
		sms_ready INTEGER NOT NULL DEFAULT 0 CHECK (sms_ready IN (0, 1)),
		reg_status INTEGER NOT NULL DEFAULT 0,
		reg_status_text TEXT NOT NULL DEFAULT '',
		network_mode TEXT NOT NULL DEFAULT '',
		local_phone TEXT NOT NULL DEFAULT '',
		phone_number_source TEXT NOT NULL DEFAULT '',
		last_error_class TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		last_reason TEXT NOT NULL DEFAULT '',
		tunnel_json TEXT NOT NULL DEFAULT '{}',
		imscore_json TEXT NOT NULL DEFAULT '{}',
		smsip_json TEXT NOT NULL DEFAULT '{}',
		extra_json TEXT NOT NULL DEFAULT '{}',
		updated_at INTEGER NOT NULL,
		FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX vowifi_runtime_ims_ready_idx ON vowifi_runtime(ims_ready)`,

	`CREATE TABLE sms_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id TEXT NOT NULL DEFAULT '',
		device_id TEXT NOT NULL,
		imsi TEXT NOT NULL DEFAULT '',
		peer TEXT NOT NULL,
		direction TEXT NOT NULL,
		body TEXT NOT NULL DEFAULT '',
		message_time INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		parts_total INTEGER NOT NULL DEFAULT 1 CHECK (parts_total > 0),
		delivery_state TEXT NOT NULL DEFAULT '',
		is_read INTEGER NOT NULL DEFAULT 0 CHECK (is_read IN (0, 1)),
		extra_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE UNIQUE INDEX sms_messages_external_id_idx
		ON sms_messages(device_id, message_id)
		WHERE message_id <> ''`,
	`CREATE INDEX sms_messages_thread_idx
		ON sms_messages(device_id, imsi, peer, message_time DESC, id DESC)`,
	`CREATE INDEX sms_messages_time_idx ON sms_messages(message_time DESC)`,

	`CREATE TABLE local_proxy_config (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		mode TEXT NOT NULL CHECK (mode IN ('socks5', 'http')),
		device_id TEXT NOT NULL,
		listen_addr TEXT NOT NULL,
		listen_port INTEGER NOT NULL CHECK (listen_port BETWEEN 1 AND 65535),
		enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
		auth_enabled INTEGER NOT NULL DEFAULT 0 CHECK (auth_enabled IN (0, 1)),
		username TEXT NOT NULL DEFAULT '',
		password TEXT NOT NULL DEFAULT '',
		extra_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX local_proxy_device_idx ON local_proxy_config(device_id)`,

	`CREATE TABLE upstream_proxies (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		addr TEXT NOT NULL,
		username TEXT NOT NULL DEFAULT '',
		password TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
		extra_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,

	`CREATE TABLE country_rules (
		country_code TEXT PRIMARY KEY,
		country_name TEXT NOT NULL DEFAULT '',
		upstream_proxy_id TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
		extra_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		FOREIGN KEY (upstream_proxy_id) REFERENCES upstream_proxies(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX country_rules_proxy_idx ON country_rules(upstream_proxy_id)`,

	`CREATE TABLE notification_settings (
		channel TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
		config_json TEXT NOT NULL DEFAULT '{}',
		sensitive_fields_json TEXT NOT NULL DEFAULT '[]',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,

	`CREATE TABLE app_settings (
		key TEXT PRIMARY KEY,
		value_json TEXT NOT NULL,
		sensitive INTEGER NOT NULL DEFAULT 0 CHECK (sensitive IN (0, 1)),
		updated_at INTEGER NOT NULL
	)`,

	`CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		actor TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL,
		entity_type TEXT NOT NULL DEFAULT '',
		entity_id TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT '',
		remote_addr TEXT NOT NULL DEFAULT '',
		details_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX audit_events_created_idx ON audit_events(created_at DESC, id DESC)`,
	`CREATE INDEX audit_events_entity_idx ON audit_events(entity_type, entity_id, created_at DESC)`,

	`CREATE TABLE log_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_time INTEGER NOT NULL,
		level TEXT NOT NULL,
		message TEXT NOT NULL,
		caller TEXT NOT NULL DEFAULT '',
		fields_json TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX log_events_time_idx ON log_events(event_time DESC, id DESC)`,
	`CREATE INDEX log_events_level_idx ON log_events(level, event_time DESC)`,

	`CREATE TABLE card_policies (
		iccid TEXT PRIMARY KEY,
		network_enabled INTEGER NOT NULL DEFAULT 0 CHECK (network_enabled IN (0, 1)),
		vowifi_enabled INTEGER NOT NULL DEFAULT 0 CHECK (vowifi_enabled IN (0, 1)),
		airplane_enabled INTEGER NOT NULL DEFAULT 0 CHECK (airplane_enabled IN (0, 1)),
		apn TEXT NOT NULL DEFAULT '',
		ip_version TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		CHECK (NOT (vowifi_enabled = 1 AND airplane_enabled = 1))
	)`,

	`CREATE TABLE traffic_buckets (
		device_id TEXT NOT NULL,
		bucket TEXT NOT NULL,
		period_start INTEGER NOT NULL,
		rx_bytes INTEGER NOT NULL DEFAULT 0 CHECK (rx_bytes >= 0),
		tx_bytes INTEGER NOT NULL DEFAULT 0 CHECK (tx_bytes >= 0),
		PRIMARY KEY (device_id, bucket, period_start)
	)`,
	`CREATE INDEX traffic_buckets_period_idx
		ON traffic_buckets(bucket, period_start DESC)`,
}
