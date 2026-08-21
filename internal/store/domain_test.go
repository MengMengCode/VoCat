package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigrationFromAuthenticationSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range migrationStatements(1) {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create v1 schema: %v", err)
		}
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO admins (id, username, password_hash, created_at, updated_at)
		VALUES (1, 'legacy-admin', X'0102', 100, 100)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	admin, err := database.CurrentAdmin(ctx)
	if err != nil {
		t.Fatalf("legacy admin missing after migration: %v", err)
	}
	if admin.Username != "legacy-admin" || !bytes.Equal(admin.PasswordHash, []byte{1, 2}) {
		t.Fatalf("legacy admin changed during migration: %+v", admin)
	}
	var version int
	if err := database.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{
		"devices", "device_runtime", "vowifi_runtime", "sms_messages",
		"local_proxy_config", "upstream_proxies", "country_rules",
		"device_proxy_bindings",
		"notification_settings", "app_settings", "audit_events",
		"log_events", "card_policies", "card_apn_profiles", "traffic_buckets",
		"sms_send_attempts",
	} {
		var found string
		err := database.db.QueryRowContext(ctx, `
			SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?
		`, table).Scan(&found)
		if err != nil || found != table {
			t.Fatalf("migrated table %q missing: %v", table, err)
		}
	}
}

func TestMigration7BackfillsSMSModemIMEI(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sms-imei.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 6; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create v%d schema: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO devices (id, name, modem_imei, created_at, updated_at)
		VALUES ('ec20_1', 'EC20', '867394042309830', 100, 100);
		INSERT INTO sms_messages (
			message_id, device_id, peer, direction, message_time, created_at, updated_at
		) VALUES ('legacy-message', 'ec20_1', 'VOXI', 'inbound', 100, 100, 100);
		PRAGMA user_version = 6;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	messages, err := database.ListSMSMessages(ctx, SMSFilter{ModemIMEI: "867394042309830"})
	if err != nil || len(messages) != 1 || messages[0].DeviceID != "ec20_1" {
		t.Fatalf("migrated SMS = %#v, %v", messages, err)
	}
}

func TestMigration12ConvertsOnlyKnownActiveDeviceBindingToICCID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "profile-proxy-binding.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 11; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create v%d schema: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO devices (id, name, created_at, updated_at) VALUES
			('known', 'Known', 100, 100), ('unknown', 'Unknown', 100, 100);
		INSERT INTO upstream_proxies (id, name, addr, created_at, updated_at)
			VALUES ('route', 'Route', '127.0.0.1:1080', 100, 100);
		INSERT INTO device_proxy_bindings (device_id, upstream_proxy_id, created_at, updated_at) VALUES
			('known', 'route', 100, 100), ('unknown', 'route', 100, 100);
		INSERT INTO vowifi_runtime (device_id, iccid, updated_at)
			VALUES ('known', '8944100000000000001', 100);
		PRAGMA user_version = 11;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	binding, err := database.DeviceProxyBinding(ctx, "8944100000000000001")
	if err != nil || binding.DeviceID != "known" || binding.UpstreamProxyID != "route" {
		t.Fatalf("migrated binding = %+v, %v", binding, err)
	}
	bindings, err := database.ListDeviceProxyBindings(ctx)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("migrated bindings = %+v, %v; unknown ICCID binding must be dropped", bindings, err)
	}
}

func TestMigration9NormalizesVoWiFiAirplanePolicy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rf-safe-policy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 8; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create v%d schema: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO card_policies (
			iccid, network_enabled, vowifi_enabled, airplane_enabled,
			created_at, updated_at
		) VALUES ('8900000000000000001', 0, 1, 0, 100, 100);
		PRAGMA user_version = 8;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	policy, err := database.CardPolicy(ctx, "8900000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.VoWiFiEnabled || !policy.AirplaneEnabled || policy.NetworkEnabled {
		t.Fatalf("migrated policy = %#v, want VoWiFi+airplane with data off", policy)
	}
}

func TestMigration8DefaultsExistingDevicesToPCIeType(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "device-type.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 7; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create v%d schema: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO devices (id, name, created_at, updated_at)
		VALUES ('legacy', 'Legacy modem', 100, 100);
		PRAGMA user_version = 7;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	got, err := database.Device(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceType != DeviceTypePCIeEC20EC25 {
		t.Fatalf("legacy device type = %q", got.DeviceType)
	}
}

func TestMigration19AcceptsDevelopmentDatabaseAndPreservesCardData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "development-schema.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 16; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create v%d schema: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO devices (id, name, created_at, updated_at)
		VALUES ('ec20-1', 'EC20', 100, 100);
		INSERT INTO card_policies (
			iccid, network_enabled, vowifi_enabled, airplane_enabled,
			apn, ip_version, source, created_at, updated_at, custom_phone_number
		) VALUES (
			'8900000000000000019', 0, 1, 1,
			'ims', 'IPV4V6', 'user', 100, 100, '447700900019'
		);
		INSERT INTO card_apn_profiles (
			iccid, apn, ip_version, created_at, updated_at,
			username, password, proxy, mcc, mnc, roaming_ip_version, auth_type
		) VALUES (
			'8900000000000000019', 'mobile.example', 'IPV4V6', 100, 100,
			'user', 'secret', '', '234', '10', 'IP', 'PAP'
		);
		PRAGMA user_version = 16;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	policy, err := database.CardPolicy(ctx, "8900000000000000019")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.VoWiFiEnabled || !policy.AirplaneEnabled || policy.CustomPhoneNumber != "447700900019" {
		t.Fatalf("migrated card policy = %#v", policy)
	}
	profiles, err := database.ListCardAPNProfiles(ctx, "8900000000000000019")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].APN != "mobile.example" || profiles[0].Username != "user" || profiles[0].AuthType != "PAP" {
		t.Fatalf("migrated APN profiles = %#v", profiles)
	}

	var version int
	if err := database.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	for _, column := range []string{
		"ims_apn", "ims_private_identity", "ims_public_identity", "ims_sms_center",
		"ims_transport", "ims_allow_imsi_derived_identity", "vowifi_eap_method",
		"vowifi_allow_sha1", "vowifi_use_modp1024",
	} {
		var count int
		if err := database.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM pragma_table_info('devices') WHERE name = ?
		`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("migration 19 column %q count = %d", column, count)
		}
	}
}

func TestMigration19AcceptsDevelopmentColumnsAlreadyPresent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "development-columns.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 18; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create v%d schema: %v", version, err)
			}
		}
	}
	// The development build added these columns while still reporting schema
	// 18. Migration 19 must treat that layout as compatible rather than fail on
	// the first duplicate ALTER TABLE statement.
	for _, statement := range migrationStatements(19) {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create development column: %v", err)
		}
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 18`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	var version int
	if err := database.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
}

func TestMigration4PreservesIMSRedeliveryAndUsesReceiptTime(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ims-redelivery.db")
	legacy := openTestStore(t, path)
	mustSaveDevice(t, legacy, "ec20-1", "EC20")
	smscTime := time.Unix(1_700_000_000, 0).UTC()
	firstReceipt := smscTime.Add(2 * time.Hour)
	rawTPDU := "040ED0D637396C7EBBCB000062808051715140"
	for index, receivedAt := range []time.Time{firstReceipt, firstReceipt.Add(30 * time.Minute)} {
		extra, err := json.Marshal(map[string]any{"raw_tpdu": rawTPDU, "call_id": index})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.SaveSMSMessage(ctx, SMSMessage{
			MessageID: fmt.Sprintf("legacy-call-%d", index),
			DeviceID:  "ec20-1",
			Peer:      "Vodafone",
			Direction: "inbound",
			Body:      "same message",
			Timestamp: smscTime,
			Status:    "received",
			Source:    "ims",
			CreatedAt: receivedAt,
			Extra:     extra,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.db.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	migrated := openTestStore(t, path)
	messages, err := migrated.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count after migration = %d, want 2", len(messages))
	}
	if !messages[0].Timestamp.Equal(firstReceipt.Add(30*time.Minute)) ||
		!messages[1].Timestamp.Equal(firstReceipt) {
		t.Fatalf("message times = %v / %v, want both receipt times", messages[0].Timestamp, messages[1].Timestamp)
	}
	var extra map[string]any
	if err := json.Unmarshal(messages[0].Extra, &extra); err != nil {
		t.Fatal(err)
	}
	if extra["service_center_timestamp_unix"] != float64(smscTime.Unix()) {
		t.Fatalf("service center time was not retained: %#v", extra)
	}
}

func TestDeviceStateRoundTripAndCascade(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	rsrp, rsrq, sinr := -95, -12, 15
	attached, inserted := true, true
	mode := 1
	device := Device{
		ID:             "ec20-1",
		Name:           "EC20 一号",
		DeviceType:     DeviceTypeDJI4G,
		Interface:      "wwan0",
		ControlDevice:  "/dev/cdc-wdm0",
		ATPort:         "/dev/ttyUSB2",
		APN:            "ims",
		ProxyPort:      1080,
		QMIUseProxy:    true,
		NetworkEnabled: true,
		SMSEnabled:     true,
		VoWiFiEnabled:  true,
		Extra:          json.RawMessage(`{"slot":1}`),
	}
	runtime := DeviceRuntime{
		Running:           true,
		Healthy:           true,
		ControlOnline:     true,
		NetworkConnected:  true,
		Operator:          "China Mobile",
		SignalDBM:         -71,
		SignalRSRP:        &rsrp,
		SignalRSRQ:        &rsrq,
		SignalSINR:        &sinr,
		ICCID:             "8986000000000000000",
		IMSI:              "460001234567890",
		PSAttached:        &attached,
		SIMInserted:       &inserted,
		OperatingMode:     &mode,
		PhoneNumber:       "+8613800138000",
		PhoneNumberSource: "cnum",
		Traffic:           json.RawMessage(`{"rx":"1 MiB"}`),
	}
	vowifi := VoWiFiRuntime{
		Phase:             "sms_ready",
		SIMReady:          true,
		AccessReady:       true,
		TunnelReady:       true,
		IMSReady:          true,
		SMSReady:          true,
		LocalPhone:        "+8613800138000",
		PhoneNumberSource: "ims",
		Tunnel:            json.RawMessage(`{"ifname":"ipsec0"}`),
	}
	if err := database.SaveDeviceState(ctx, device, &runtime, &vowifi); err != nil {
		t.Fatalf("SaveDeviceState() error = %v", err)
	}

	gotDevice, err := database.Device(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotDevice.DeviceType != DeviceTypeDJI4G || gotDevice.BaudRate != 115200 || gotDevice.DataBits != 8 ||
		gotDevice.StopBits != 1 || gotDevice.DeviceBackend != "at" {
		t.Fatalf("device defaults not applied: %+v", gotDevice)
	}
	gotRuntime, err := database.DeviceRuntime(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRuntime.PhoneNumber != runtime.PhoneNumber ||
		gotRuntime.SignalRSRP == nil || *gotRuntime.SignalRSRP != rsrp ||
		gotRuntime.PSAttached == nil || !*gotRuntime.PSAttached {
		t.Fatalf("runtime did not round trip: %+v", gotRuntime)
	}
	gotVoWiFi, err := database.VoWiFiRuntime(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotVoWiFi.SMSReady || gotVoWiFi.LocalPhone != vowifi.LocalPhone {
		t.Fatalf("VoWiFi runtime did not round trip: %+v", gotVoWiFi)
	}
	if err := database.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DeviceRuntime(ctx, device.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("runtime should cascade on device deletion, got %v", err)
	}
	if _, err := database.VoWiFiRuntime(ctx, device.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("VoWiFi runtime should cascade on device deletion, got %v", err)
	}
}

func TestUSBSIMReaderConfigurationIsWiFiCallingOnly(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	err := database.UpsertDevice(ctx, Device{
		ID: "reader-1", Name: "USB SIM", DeviceType: DeviceTypeUSBSIMReader,
		USBPath: "1-3", ControlDevice: "Reader 00 00", SIMPIN: "1234",
		DeviceBackend: "at", ESIMTransport: "at", NetworkEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.Device(ctx, "reader-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceBackend != "pcsc" || got.ESIMTransport != "pcsc" || got.NetworkEnabled || !got.SMSEnabled || !got.VoWiFiEnabled || got.SIMPIN != "1234" {
		t.Fatalf("reader config = %+v", got)
	}
	bad := got
	bad.SIMPIN = "12x4"
	if err := database.UpsertDevice(ctx, bad); err == nil {
		t.Fatal("non-numeric SIM PIN was accepted")
	}
}

func TestSMSPersistenceAndDerivedThreads(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "客厅")
	if err := database.UpsertDeviceRuntime(ctx, DeviceRuntime{
		DeviceID:    "ec20-1",
		PhoneNumber: "+8613800138000",
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	if err := database.SaveSMSMessages(ctx, []SMSMessage{
		{
			MessageID: "network-1", DeviceID: "ec20-1", IMSI: "46000",
			Peer: "10086", Direction: "inbound", Body: "第一条",
			Timestamp: base, Status: "received",
		},
		{
			MessageID: "network-2", DeviceID: "ec20-1", IMSI: "46000",
			Peer: "10086", Direction: "outbound", Body: "第二条",
			Timestamp: base.Add(time.Minute), Status: "sent", Read: true,
		},
		{
			MessageID: "network-3", DeviceID: "ec20-1", IMSI: "46000",
			Peer: "95533", Direction: "received", Body: "银行提醒",
			Timestamp: base.Add(2 * time.Minute), Status: "received",
		},
	}); err != nil {
		t.Fatalf("SaveSMSMessages() error = %v", err)
	}

	// A modem retry updates the stable external id instead of duplicating it.
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "network-1", DeviceID: "ec20-1", IMSI: "46000",
		Peer: "10086", Direction: "inbound", Body: "第一条（完整）",
		Timestamp: base, Status: "received",
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(messages))
	}
	if !messages[2].Timestamp.Equal(base) {
		t.Fatalf("retry changed the original message time to %v", messages[2].Timestamp)
	}
	contacts, err := database.ListSMSContacts(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 2 || contacts[0].Peer != "95533" ||
		contacts[0].UnreadCount != 1 || contacts[1].Peer != "10086" ||
		contacts[1].MessageCount != 2 || contacts[1].UnreadCount != 1 ||
		contacts[1].LocalPhone != "+8613800138000" {
		t.Fatalf("unexpected derived contacts: %+v", contacts)
	}
	marked, err := database.MarkSMSThreadRead(ctx, "ec20-1", "46000", "10086")
	if err != nil || marked != 1 {
		t.Fatalf("MarkSMSThreadRead() = %d, %v", marked, err)
	}
	contacts, err = database.ListSMSContacts(ctx, SMSFilter{Peer: "10086"})
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].UnreadCount != 0 {
		t.Fatalf("thread should be read: %+v", contacts)
	}

	// A subsequent periodic modem AT sync with raw unread state must not revert is_read back to 0.
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "network-1", DeviceID: "ec20-1", IMSI: "46000",
		Peer: "10086", Direction: "inbound", Body: "第一条（完整）",
		Timestamp: base, Status: "received", Read: false,
	}); err != nil {
		t.Fatal(err)
	}
	contacts, err = database.ListSMSContacts(ctx, SMSFilter{Peer: "10086"})
	if err != nil || len(contacts) != 1 || contacts[0].UnreadCount != 0 {
		t.Fatalf("thread read state must survive modem rescan: %+v", contacts)
	}

	deleted, err := database.DeleteSMSThread(ctx, "ec20-1", "46000", "10086")
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteSMSThread() = %d, %v", deleted, err)
	}
}

func TestSMSHistoryFollowsModemIMEIAfterDeviceIDRename(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	const imei = "867394042309830"
	if err := database.UpsertDevice(ctx, Device{
		ID: "ec20_1", Name: "EC20 old", ModemIMEI: imei,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "network-old", DeviceID: "ec20_1",
		IMSI: "23415", Peer: "VOXI", Direction: "inbound", Body: "before rename",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteDevice(ctx, "ec20_1"); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(ctx, Device{
		ID: "ec20_2", Name: "EC20 renamed", ModemIMEI: imei,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "network-new", DeviceID: "ec20_2", ModemIMEI: imei,
		IMSI: "23415", Peer: "VOXI", Direction: "inbound", Body: "after rename",
		Timestamp: time.Unix(1_700_000_060, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	contacts, err := database.ListSMSContacts(ctx, SMSFilter{ModemIMEI: imei})
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].DeviceID != "ec20_2" ||
		contacts[0].ModemIMEI != imei || contacts[0].MessageCount != 2 {
		t.Fatalf("renamed hardware contact = %#v", contacts)
	}
	messages, err := database.ListSMSMessages(ctx, SMSFilter{
		ModemIMEI: imei, IMSI: "23415", Peer: "VOXI",
	})
	if err != nil || len(messages) != 2 {
		t.Fatalf("renamed hardware messages = %#v, %v", messages, err)
	}

	// A retry that arrives after the rename updates the same hardware message,
	// rather than duplicating it under the new configured ID.
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "network-old", DeviceID: "ec20_2", ModemIMEI: imei,
		IMSI: "23415", Peer: "VOXI", Direction: "inbound", Body: "retry",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	messages, err = database.ListSMSMessages(ctx, SMSFilter{ModemIMEI: imei})
	if err != nil || len(messages) != 2 {
		t.Fatalf("retry after rename messages = %#v, %v", messages, err)
	}
}

func TestListInboundSMSAfterIDUsesDurableInsertionCursor(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	old, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "old-inbound", DeviceID: "ec20-1", Peer: "10086",
		Direction: "inbound", Body: "old", Status: "received",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "new-outbound", DeviceID: "ec20-1", Peer: "10010",
		Direction: "outbound", Body: "sent", Status: "sent",
	}); err != nil {
		t.Fatal(err)
	}
	newInbound, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "new-inbound", DeviceID: "ec20-1", Peer: "95533",
		Direction: "received", Body: "new", Status: "received",
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := database.LatestSMSMessageID(ctx)
	if err != nil || latest != newInbound.ID {
		t.Fatalf("LatestSMSMessageID() = %d, %v; want %d", latest, err, newInbound.ID)
	}
	messages, err := database.ListInboundSMSAfterID(ctx, old.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != newInbound.ID {
		t.Fatalf("ListInboundSMSAfterID() = %#v", messages)
	}
}

func TestApplySMSDeliveryReportTracksEverySubmittedPart(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	extra := json.RawMessage(`{
		"transport":"ims",
		"part_results":[{"reference":42},{"reference":43}]
	}`)
	sent, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "ims-submit-1", DeviceID: "ec20-1", IMSI: "23415",
		Peer: "+447700900123", Direction: "outbound", Body: "multipart",
		Timestamp: time.Now().UTC(), Status: "accepted_by_ims", Source: "ims",
		PartsTotal: 2, DeliveryState: "accepted_by_ims", Read: true, Extra: extra,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		DeviceID: "ec20-1", IMSI: "23415", Peer: "+447700900123", Source: "ims",
		MessageReference: 42, StatusCode: 0, DeliveryState: "delivered",
	})
	if err != nil || first.ID != sent.ID || first.DeliveryState != "pending_delivery_report" {
		t.Fatalf("first delivery report = (%#v, %v)", first, err)
	}
	second, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		DeviceID: "ec20-1", IMSI: "23415", Peer: "+447700900123", Source: "ims",
		MessageReference: 43, StatusCode: 0, DeliveryState: "delivered",
	})
	if err != nil || second.ID != sent.ID || second.DeliveryState != "delivered" {
		t.Fatalf("second delivery report = (%#v, %v)", second, err)
	}
	var savedExtra map[string]any
	if err := json.Unmarshal(second.Extra, &savedExtra); err != nil {
		t.Fatal(err)
	}
	reports, _ := savedExtra["delivery_reports"].(map[string]any)
	if len(reports) != 2 {
		t.Fatalf("delivery reports = %#v", reports)
	}
}

func TestProxyCredentialsAndCountryRules(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	if err := database.UpsertLocalProxy(ctx, LocalProxyConfig{
		ID: "local-1", Name: "SOCKS", Mode: "socks5", DeviceID: "ec20-1",
		ListenAddr: "127.0.0.1", ListenPort: 1080, Enabled: true,
		AuthEnabled: true, Username: "user", Password: "local-secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertLocalProxy(ctx, LocalProxyConfig{
		ID: "local-1", Name: "SOCKS 新", Mode: "socks5", DeviceID: "ec20-1",
		ListenAddr: "127.0.0.1", ListenPort: 1080, Enabled: true,
		AuthEnabled: true, Username: "user", Password: "",
	}); err != nil {
		t.Fatal(err)
	}
	local, err := database.LocalProxy(ctx, "local-1")
	if err != nil {
		t.Fatal(err)
	}
	if local.Password != "local-secret" || local.Redacted().Password != SecretMask ||
		local.Public().Password != "" {
		t.Fatalf("local proxy credential semantics failed: %+v", local)
	}

	if err := database.UpsertUpstreamProxy(ctx, UpstreamProxy{
		ID: "up-1", Name: "上游", Addr: "127.0.0.1:2080",
		Username: "up-user", Password: "up-secret", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertUpstreamProxy(ctx, UpstreamProxy{
		ID: "up-1", Name: "上游新", Addr: "127.0.0.1:2080",
		Username: "up-user", Password: SecretMask, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	upstream, err := database.UpstreamProxy(ctx, "up-1")
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Password != "up-secret" {
		t.Fatalf("blank/masked update erased upstream secret: %+v", upstream)
	}
	if got := RedactText(
		"connect local-secret through up-secret",
		local,
		upstream,
	); strings.Contains(got, "secret") {
		t.Fatalf("RedactText leaked credentials: %q", got)
	}
	if err := database.UpsertCountryRule(ctx, CountryRule{
		CountryCode: "cn", CountryName: "中国", UpstreamProxyID: "up-1",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	rule, err := database.CountryRule(ctx, "CN")
	if err != nil || rule.CountryCode != "CN" {
		t.Fatalf("CountryRule() = %+v, %v", rule, err)
	}
	if err := database.UpsertDeviceProxyBinding(ctx, DeviceProxyBinding{
		DeviceID: "ec20-1", ICCID: "8944100000000000001", ProfileName: "Vodafone", UpstreamProxyID: "up-1",
	}); err != nil {
		t.Fatal(err)
	}
	binding, err := database.DeviceProxyBinding(ctx, "8944100000000000001")
	if err != nil || binding.UpstreamProxyID != "up-1" || binding.DeviceID != "ec20-1" || binding.ProfileName != "Vodafone" {
		t.Fatalf("DeviceProxyBinding() = %+v, %v", binding, err)
	}
	if err := database.DeleteUpstreamProxy(ctx, "up-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CountryRule(ctx, "CN"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("country rule should cascade with upstream deletion, got %v", err)
	}
	if _, err := database.DeviceProxyBinding(ctx, "8944100000000000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device binding should cascade with upstream deletion, got %v", err)
	}
}

func TestNotificationAndAppSecretPreservation(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	if err := database.SaveNotificationSettings(ctx, []NotificationSetting{
		{
			Channel: "email",
			Config:  json.RawMessage(`{"password":"mail-secret"}`),
		},
		{
			Channel: "webhook",
			Config:  json.RawMessage(`not-json`),
		},
	}); err == nil {
		t.Fatal("invalid notification batch was accepted")
	}
	if _, err := database.NotificationSetting(ctx, "email"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("notification batch was not rolled back: %v", err)
	}
	if err := database.UpsertNotificationSetting(ctx, NotificationSetting{
		Channel: "telegram",
		Enabled: true,
		Config:  json.RawMessage(`{"bot_token":"telegram-secret","chat_id":"1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertNotificationSetting(ctx, NotificationSetting{
		Channel: "telegram",
		Enabled: true,
		Config:  json.RawMessage(`{"bot_token":"","chat_id":"2"}`),
	}); err != nil {
		t.Fatal(err)
	}
	setting, err := database.NotificationSetting(ctx, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(setting.Config, &config); err != nil {
		t.Fatal(err)
	}
	if config["bot_token"] != "telegram-secret" || config["chat_id"] != "2" {
		t.Fatalf("notification merge lost data: %s", setting.Config)
	}
	if bytes.Contains(setting.Redacted().Config, []byte("telegram-secret")) ||
		bytes.Contains(setting.Public().Config, []byte("telegram-secret")) {
		t.Fatal("notification views leaked secret")
	}
	if got := RedactText("token=telegram-secret", setting); strings.Contains(got, "telegram-secret") {
		t.Fatalf("notification secret leaked in text: %q", got)
	}

	if err := database.UpsertAppSetting(ctx, AppSetting{
		Key: "provider.token", Value: json.RawMessage(`"app-secret"`), Sensitive: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAppSetting(ctx, AppSetting{
		Key: "provider.token", Value: json.RawMessage(`"********"`), Sensitive: true,
	}); err != nil {
		t.Fatal(err)
	}
	appSetting, err := database.AppSetting(ctx, "provider.token")
	if err != nil {
		t.Fatal(err)
	}
	if string(appSetting.Value) != `"app-secret"` ||
		string(appSetting.Redacted().Value) != `"********"` ||
		string(appSetting.Public().Value) != `null` {
		t.Fatalf("unexpected sensitive app setting: %+v", appSetting)
	}
}

func TestNotificationArraySecretPreservation(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	originalURL := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=first-secret"
	if err := database.UpsertNotificationSetting(ctx, NotificationSetting{
		Channel: "wecom", Enabled: true,
		Config: json.RawMessage(`{"urls":["` + originalURL + `"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	setting, err := database.NotificationSetting(ctx, "wecom")
	if err != nil {
		t.Fatal(err)
	}
	var redacted map[string]any
	if err := json.Unmarshal(setting.Redacted().Config, &redacted); err != nil {
		t.Fatal(err)
	}
	urls, ok := redacted["urls"].([]any)
	if !ok || len(urls) != 1 || urls[0] != SecretMask {
		t.Fatalf("redacted URLs = %#v", redacted["urls"])
	}
	if err := database.UpsertNotificationSetting(ctx, NotificationSetting{
		Channel: "wecom", Enabled: true,
		Config: json.RawMessage(`{"urls":["` + SecretMask + `","https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=second-secret"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	setting, err = database.NotificationSetting(ctx, "wecom")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(setting.Config, []byte(originalURL)) || !bytes.Contains(setting.Config, []byte("second-secret")) {
		t.Fatalf("stored URLs = %s", setting.Config)
	}
}

func TestLarkNotificationSecretsAreRedactedAndPreserved(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	originalURL := "https://open.feishu.cn/open-apis/bot/v2/hook/lark-token"
	if err := database.UpsertNotificationSetting(ctx, NotificationSetting{
		Channel: "lark", Enabled: true,
		Config: json.RawMessage(`{"url":"` + originalURL + `","secret":"signing-secret"}`),
	}); err != nil {
		t.Fatal(err)
	}
	setting, err := database.NotificationSetting(ctx, "lark")
	if err != nil {
		t.Fatal(err)
	}
	var redacted map[string]any
	if err := json.Unmarshal(setting.Redacted().Config, &redacted); err != nil {
		t.Fatal(err)
	}
	if redacted["url"] != SecretMask || redacted["secret"] != SecretMask {
		t.Fatalf("redacted Lark config = %#v", redacted)
	}
	if err := database.UpsertNotificationSetting(ctx, NotificationSetting{
		Channel: "lark", Enabled: true,
		Config: json.RawMessage(`{"url":"` + SecretMask + `","secret":"` + SecretMask + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	setting, err = database.NotificationSetting(ctx, "lark")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(setting.Config, []byte(originalURL)) || !bytes.Contains(setting.Config, []byte("signing-secret")) {
		t.Fatalf("stored Lark config = %s", setting.Config)
	}
}

func TestNotificationRedactionKeepsEmptySensitiveValuesEmpty(t *testing.T) {
	setting := NotificationSetting{
		Config:          json.RawMessage(`{"url":"","secret":""}`),
		SensitiveFields: []string{"url", "secret"},
	}
	var redacted map[string]any
	if err := json.Unmarshal(setting.Redacted().Config, &redacted); err != nil {
		t.Fatal(err)
	}
	if redacted["url"] != "" || redacted["secret"] != "" {
		t.Fatalf("empty sensitive values were masked: %#v", redacted)
	}
}

func TestEventsPoliciesAndTraffic(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	old := time.Unix(1_700_000_000, 0).UTC()
	recent := old.Add(time.Hour)
	if _, err := database.AppendAuditEvent(ctx, AuditEvent{
		Actor: "admin", Action: "device.update", EntityType: "device",
		EntityID: "ec20-1", Outcome: "ok", CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendAuditEvent(ctx, AuditEvent{
		Actor: "system", Action: "device.refresh", EntityType: "device",
		EntityID: "ec20-1", Outcome: "ok", CreatedAt: recent,
	}); err != nil {
		t.Fatal(err)
	}
	audits, err := database.ListAuditEvents(ctx, AuditFilter{Actor: "admin"})
	if err != nil || len(audits) != 1 || audits[0].Action != "device.update" {
		t.Fatalf("audit filter result = %+v, %v", audits, err)
	}
	if _, err := database.AppendLogEvent(ctx, LogEvent{
		Time: old, Level: "warn", Message: "old warning",
		Fields: json.RawMessage(`{"device":"ec20-1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendLogEvent(ctx, LogEvent{
		Time: recent, Level: "info", Message: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	logs, err := database.ListLogEvents(ctx, LogFilter{Level: "info"})
	if err != nil || len(logs) != 1 || logs[0].Message != "ready" {
		t.Fatalf("log filter result = %+v, %v", logs, err)
	}
	if _, err := database.AppendLogEvent(ctx, LogEvent{
		Time: recent, Level: "info", Message: " HTTP REQUEST ",
	}); err != nil {
		t.Fatal(err)
	}
	logs, err = database.ListLogEvents(ctx, LogFilter{Level: "info", ExcludeMessage: "http request"})
	if err != nil || len(logs) != 1 || logs[0].Message != "ready" {
		t.Fatalf("excluded log filter result = %+v, %v", logs, err)
	}
	auditDeleted, logDeleted, err := database.PruneEvents(
		ctx,
		old.Add(time.Minute),
		old.Add(time.Minute),
	)
	if err != nil || auditDeleted != 1 || logDeleted != 1 {
		t.Fatalf("PruneEvents() = %d, %d, %v", auditDeleted, logDeleted, err)
	}

	if err := database.UpsertCardPolicy(ctx, CardPolicy{
		ICCID: "89860001", NetworkEnabled: true, VoWiFiEnabled: true,
		APN: "ims", IPVersion: "ipv4v6", CustomPhoneNumber: "+8613800138000",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertCardPolicy(ctx, CardPolicy{
		ICCID: "89860002", VoWiFiEnabled: true, AirplaneEnabled: true,
	}); err != nil {
		t.Fatalf("RF-safe VoWiFi policy was rejected: %v", err)
	}
	policy, err := database.CardPolicy(ctx, "89860001")
	if err != nil || !policy.VoWiFiEnabled || policy.CustomPhoneNumber != "+8613800138000" {
		t.Fatalf("CardPolicy() = %+v, %v", policy, err)
	}
	safePolicy, err := database.CardPolicy(ctx, "89860002")
	if err != nil || !safePolicy.VoWiFiEnabled || !safePolicy.AirplaneEnabled {
		t.Fatalf("safe CardPolicy() = %+v, %v", safePolicy, err)
	}

	period := old.Truncate(time.Hour)
	if err := database.UpsertTrafficBucket(ctx, TrafficBucket{
		DeviceID: "ec20-1", Bucket: "hour", PeriodStart: period,
		RXBytes: 100, TXBytes: 25,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.AddTrafficBucket(ctx, TrafficBucket{
		DeviceID: "ec20-1", Bucket: "hour", PeriodStart: period,
		RXBytes: 5, TXBytes: 10,
	}); err != nil {
		t.Fatal(err)
	}
	buckets, err := database.ListTrafficBuckets(ctx, TrafficFilter{
		DeviceID: "ec20-1", Bucket: "hour",
	})
	if err != nil || len(buckets) != 1 ||
		buckets[0].RXBytes != 105 || buckets[0].TXBytes != 35 ||
		buckets[0].TotalBytes() != 140 {
		t.Fatalf("traffic buckets = %+v, %v", buckets, err)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", path, err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return database
}

func mustSaveDevice(t *testing.T, database *Store, id, name string) {
	t.Helper()
	if err := database.UpsertDevice(context.Background(), Device{
		ID: id, Name: name, SMSEnabled: true,
	}); err != nil {
		t.Fatalf("UpsertDevice() error = %v", err)
	}
}
