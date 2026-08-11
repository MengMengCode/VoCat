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
		"log_events", "card_policies", "traffic_buckets",
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

func TestMigration9KeepsIMSAPNIndependentFromCellularAPN(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "carrier-profile.db")
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
		INSERT INTO devices (id, name, created_at, updated_at)
		VALUES ('legacy', 'Legacy modem', 100, 100);
		INSERT INTO devices (id, name, apn, created_at, updated_at)
		VALUES ('legacy-apn', 'Legacy APN modem', 'carrier.data', 100, 100);
		PRAGMA user_version = 8;
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
	if got.IMSAPN != "ims" || got.IMSTransport != "tcp" || got.VoWiFiEAPMethod != "aka" {
		t.Fatalf("carrier profile defaults = %+v", got)
	}
	if got.IMSPrivateIdentity != "" || got.IMSPublicIdentity != "" ||
		got.IMSSMSCenter != "" || !got.IMSAllowIMSIDerivedIdentity || got.VoWiFiAllowSHA1 || got.VoWiFiUseMODP1024 {
		t.Fatalf("legacy carrier profile must preserve standards-based IMSI derivation: %+v", got)
	}
	withAPN, err := database.Device(ctx, "legacy-apn")
	if err != nil {
		t.Fatal(err)
	}
	if withAPN.IMSAPN != "ims" {
		t.Fatalf("migrated IMS APN = %q, want independent default ims", withAPN.IMSAPN)
	}
}

func TestMigration10AddsExplicitWeakCryptoOptInsToVersion9Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-crypto-profile.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 9; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create v%d schema: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO devices (id, name, created_at, updated_at)
		VALUES ('v9-device', 'Version 9 modem', 100, 100);
		PRAGMA user_version = 9;
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)
	got, err := database.Device(ctx, "v9-device")
	if err != nil {
		t.Fatal(err)
	}
	if got.VoWiFiAllowSHA1 || got.VoWiFiUseMODP1024 {
		t.Fatal("v9 device unexpectedly opted in to legacy IKE crypto")
	}
}

func TestDeviceCarrierProfileZeroValueUsesCompatibleDefaults(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	if err := database.UpsertDevice(ctx, Device{ID: "auto", Name: "Auto provisioned"}); err != nil {
		t.Fatal(err)
	}
	got, err := database.Device(ctx, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if got.IMSAPN != "ims" || got.IMSTransport != "tcp" || got.VoWiFiEAPMethod != "aka" ||
		!got.IMSAllowIMSIDerivedIdentity || got.VoWiFiAllowSHA1 || got.VoWiFiUseMODP1024 {
		t.Fatalf("zero-value carrier profile = %+v", got)
	}
}

func TestDeviceCarrierProfileRejectsMissingExplicitIdentities(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	err := database.UpsertDevice(ctx, Device{
		ID:                          "strict",
		Name:                        "Strict carrier profile",
		IMSAPN:                      "ims",
		IMSTransport:                "tcp",
		IMSAllowIMSIDerivedIdentity: false,
		VoWiFiEAPMethod:             "aka",
	})
	if err == nil || !strings.Contains(err.Error(), "required when IMSI derivation is disabled") {
		t.Fatalf("UpsertDevice() error = %v, want missing explicit identities rejection", err)
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
		ID:                          "ec20-1",
		Name:                        "EC20 一号",
		DeviceType:                  DeviceTypeDJI4G,
		Interface:                   "wwan0",
		ControlDevice:               "/dev/cdc-wdm0",
		ATPort:                      "/dev/ttyUSB2",
		APN:                         "internet",
		IMSAPN:                      "ims",
		IMSPrivateIdentity:          "460001234567890@ims.mnc000.mcc460.3gppnetwork.org",
		IMSPublicIdentity:           "sip:+8613800138000@ims.mnc000.mcc460.3gppnetwork.org",
		IMSSMSCenter:                "+8613800100500",
		IMSTransport:                "udp",
		IMSAllowIMSIDerivedIdentity: false,
		VoWiFiEAPMethod:             "aka-prime",
		VoWiFiAllowSHA1:             true,
		VoWiFiUseMODP1024:           true,
		ProxyPort:                   1080,
		QMIUseProxy:                 true,
		NetworkEnabled:              true,
		SMSEnabled:                  true,
		VoWiFiEnabled:               true,
		Extra:                       json.RawMessage(`{"slot":1}`),
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
		gotDevice.StopBits != 1 || gotDevice.DeviceBackend != "at" ||
		gotDevice.APN != "internet" || gotDevice.IMSAPN != "ims" ||
		gotDevice.IMSPrivateIdentity != device.IMSPrivateIdentity ||
		gotDevice.IMSPublicIdentity != device.IMSPublicIdentity ||
		gotDevice.IMSSMSCenter != device.IMSSMSCenter || gotDevice.IMSTransport != "udp" ||
		gotDevice.IMSAllowIMSIDerivedIdentity || gotDevice.VoWiFiEAPMethod != "aka-prime" ||
		!gotDevice.VoWiFiAllowSHA1 || !gotDevice.VoWiFiUseMODP1024 {
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

func TestSMSPersistenceAndDerivedThreads(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "客厅")
	if err := database.UpsertDeviceRuntime(ctx, DeviceRuntime{
		DeviceID:    "ec20-1",
		IMSI:        "46000",
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
	smsc := sent.Timestamp.Add(time.Second)
	first, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "part-42",
		DeviceID: "ec20-1", IMSI: "23415", Peer: "+447700900123", Source: "ims",
		MessageReference: 42, StatusCode: 0, DeliveryState: "delivered", ServiceCenterTime: &smsc,
	})
	if err != nil || first.ID != sent.ID || first.DeliveryState != "pending_delivery_report" {
		t.Fatalf("first delivery report = (%#v, %v)", first, err)
	}
	second, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "part-43",
		DeviceID: "ec20-1", IMSI: "23415", Peer: "+447700900123", Source: "ims",
		MessageReference: 43, StatusCode: 0, DeliveryState: "delivered", ServiceCenterTime: &smsc,
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

func TestApplySMSDeliveryReportIDKeepsRescanOnOriginalSubmission(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	base := time.Unix(1_700_000_000, 0).UTC()
	save := func(messageID, imsi string, at time.Time) SMSMessage {
		t.Helper()
		value, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: messageID, DeviceID: "ec20-1", IMSI: imsi,
			Peer: "+447700900123", Direction: "outbound", Body: messageID,
			Timestamp: at, Source: "ims", Extra: json.RawMessage(`{"message_reference":17}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	old := save("old-submit", "23415", base)
	oldSCTS := base.Add(time.Minute)
	if _, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "status-pdu-sha256:old", DeviceID: "ec20-1", IMSI: "23415",
		Peer: "+447700900123", Source: "ims", MessageReference: 17,
		DeliveryState: "delivered", ServiceCenterTime: &oldSCTS,
	}); err != nil {
		t.Fatal(err)
	}
	newer := save("new-submit", "23416", base.Add(time.Hour))

	rescanned, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "status-pdu-sha256:old", DeviceID: "ec20-1",
		Peer: "+447700900123", Source: "ims", MessageReference: 17,
		DeliveryState: "delivered",
	})
	if err != nil || rescanned.ID != old.ID {
		t.Fatalf("rescanned report = (%#v, %v), want old id %d", rescanned, err, old.ID)
	}
	gotNew, err := database.SMSMessage(ctx, newer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(gotNew.Extra, []byte("delivery_reports")) {
		t.Fatalf("rescan mutated newer reused-reference submission: %s", gotNew.Extra)
	}
}

func TestApplySMSDeliveryReportWithoutIMSIRejectsAmbiguousReference(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	for index, imsi := range []string{"23415", "23416"} {
		if _, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: fmt.Sprintf("submit-%d", index), DeviceID: "ec20-1", IMSI: imsi,
			Peer: "peer", Direction: "outbound", Body: "body", Source: "ims",
			Extra: json.RawMessage(`{"message_reference":31}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		DeviceID: "ec20-1", Peer: "peer", Source: "ims", MessageReference: 31,
		DeliveryState: "delivered",
	})
	if !errors.Is(err, ErrSMSDeliveryReportAmbiguous) {
		t.Fatalf("ApplySMSDeliveryReport() error = %v, want ambiguity", err)
	}
	messages, err := database.ListSMSMessages(ctx, SMSFilter{DeviceID: "ec20-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if bytes.Contains(message.Extra, []byte("delivery_reports")) {
			t.Fatalf("ambiguous report mutated message %d: %s", message.ID, message.Extra)
		}
	}
}

func TestApplySMSDeliveryReportServiceCenterTimeSelectsUniqueSubmission(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	base := time.Unix(1_700_000_000, 0).UTC()
	var old SMSMessage
	for index, item := range []struct {
		imsi string
		at   time.Time
	}{{"23415", base}, {"23416", base.Add(time.Hour)}} {
		extra, err := json.Marshal(map[string]any{"part_results": []any{
			map[string]any{"reference": 44, "submittedAt": item.at},
		}})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := database.SaveSMSMessage(ctx, SMSMessage{
			MessageID: fmt.Sprintf("submit-%d", index), DeviceID: "ec20-1", IMSI: item.imsi,
			Peer: "peer", Direction: "outbound", Body: "body", Timestamp: item.at,
			Source: "ims", Extra: extra,
		})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			old = saved
		}
	}
	smsc := base.Add(time.Minute)
	got, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "unique-by-smsc", DeviceID: "ec20-1", Peer: "peer", Source: "ims",
		MessageReference: 44, ServiceCenterTime: &smsc, DeliveryState: "delivered",
	})
	if err != nil || got.ID != old.ID {
		t.Fatalf("timestamp-attributed report = (%#v, %v), want old id %d", got, err, old.ID)
	}
}

func TestApplySMSDeliveryReportRejectsSoleCandidateNewerThanSCTS(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	base := time.Unix(1_700_000_000, 0).UTC()
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "new-submit", DeviceID: "ec20-1", IMSI: "sim-b", Peer: "peer",
		Direction: "outbound", Body: "new", Timestamp: base.Add(time.Hour), Source: "cellular_at",
		Extra: json.RawMessage(`{"message_reference":9,"reference_known":true,"accepted_by_modem":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	oldSCTS := base.Add(time.Minute)
	_, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "old-card-report", DeviceID: "ec20-1", Peer: "peer", Source: "cellular_at",
		MessageReference: 9, ServiceCenterTime: &oldSCTS, DeliveryState: "delivered",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApplySMSDeliveryReport() error = %v, want no plausible target", err)
	}
}

func TestApplySMSDeliveryReportIgnoresUnknownAndUnacceptedReferences(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	base := time.Unix(1_700_000_000, 0).UTC()
	sent, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "partial-submit", DeviceID: "ec20-1", IMSI: "sim-a", Peer: "peer",
		Direction: "outbound", Body: "partial", Timestamp: base, Source: "cellular_at", PartsTotal: 3,
		Extra: json.RawMessage(`{
			"message_reference":0,"reference_known":false,"accepted_by_modem":false,
			"all_parts_accepted":false,
			"part_results":[
				{"messageReference":0,"referenceKnown":false,"acceptedByModem":false},
				{"messageReference":5,"referenceKnown":true,"acceptedByModem":true},
				{"messageReference":6,"referenceKnown":true,"acceptedByModem":false}
			]
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	smsc := base.Add(time.Minute)
	if _, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "not-real-zero", DeviceID: "ec20-1", IMSI: "sim-a", Peer: "peer",
		Source: "cellular_at", MessageReference: 0, ServiceCenterTime: &smsc,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown top-level TP-MR matched: %v", err)
	}
	updated, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "accepted-five", DeviceID: "ec20-1", IMSI: "sim-a", Peer: "peer",
		Source: "cellular_at", MessageReference: 5, ServiceCenterTime: &smsc,
		DeliveryState: "delivered",
	})
	if err != nil || updated.ID != sent.ID || updated.DeliveryState != "partial" {
		t.Fatalf("accepted partial report = (%#v, %v)", updated, err)
	}
}

func TestApplySMSDeliveryReportDoesNotRegressFinalState(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	base := time.Unix(1_700_000_000, 0).UTC()
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "submit", DeviceID: "ec20-1", IMSI: "sim-a", Peer: "peer",
		Direction: "outbound", Body: "body", Timestamp: base, Source: "ims",
		Extra: json.RawMessage(`{"part_results":[{"reference":7,"accepted":true}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	smsc := base.Add(time.Minute)
	if _, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "final-delivered", DeviceID: "ec20-1", IMSI: "sim-a", Peer: "peer", Source: "ims",
		MessageReference: 7, ServiceCenterTime: &smsc, DeliveryState: "delivered", ReceivedAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "late-temporary", DeviceID: "ec20-1", IMSI: "sim-a", Peer: "peer", Source: "ims",
		MessageReference: 7, ServiceCenterTime: &smsc, DeliveryState: "temporary_error", ReceivedAt: base.Add(3 * time.Minute),
	})
	if err != nil || updated.DeliveryState != "delivered" {
		t.Fatalf("late temporary report = (%#v, %v)", updated, err)
	}
	rescanned, err := database.ApplySMSDeliveryReport(ctx, SMSDeliveryReport{
		ReportID: "late-temporary", DeviceID: "ec20-1", IMSI: "sim-a", Peer: "peer", Source: "ims",
		MessageReference: 7, DeliveryState: "temporary_error",
	})
	if err != nil || rescanned.ID != updated.ID || rescanned.DeliveryState != "delivered" {
		t.Fatalf("rescan binding/state = (%#v, %v)", rescanned, err)
	}
}

func TestListSMSContactsDoesNotBorrowPhoneAcrossSIMs(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	if err := database.UpsertDeviceRuntime(ctx, DeviceRuntime{
		DeviceID: "ec20-1", IMSI: "current-sim", PhoneNumber: "+15550002",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "historical", DeviceID: "ec20-1", IMSI: "old-sim",
		Peer: "peer", Direction: "inbound", Body: "old",
	}); err != nil {
		t.Fatal(err)
	}
	contacts, err := database.ListSMSContacts(ctx, SMSFilter{DeviceID: "ec20-1", IMSI: "old-sim", IMSIExact: true})
	if err != nil || len(contacts) != 1 {
		t.Fatalf("ListSMSContacts() = %#v, %v", contacts, err)
	}
	if contacts[0].LocalPhone != "" {
		t.Fatalf("historical SIM borrowed current phone %q", contacts[0].LocalPhone)
	}
}

func TestSMSRescanCannotMoveMessageToAnotherSIM(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	first, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "modem:ME:1:fixed", DeviceID: "ec20", ModemIMEI: "867394042309830",
		IMSI: "sim-a", Peer: "+447700900123", Direction: "inbound", Body: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	rescanned, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: first.MessageID, DeviceID: "ec20", ModemIMEI: first.ModemIMEI,
		IMSI: "sim-b", Peer: first.Peer, Direction: first.Direction, Body: first.Body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rescanned.ID != first.ID || rescanned.IMSI != "sim-a" {
		t.Fatalf("rescanned message = %#v, want original SIM attribution", rescanned)
	}
}

func TestSMSRescanCannotClaimUnknownMEMessageForCurrentSIM(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	first, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: "modem:ME:2:fixed", DeviceID: "ec20", ModemIMEI: "867394042309830",
		Peer: "+447700900123", Direction: "inbound", Body: "unknown subscriber",
	})
	if err != nil {
		t.Fatal(err)
	}
	rescanned, err := database.SaveSMSMessage(ctx, SMSMessage{
		MessageID: first.MessageID, DeviceID: first.DeviceID, ModemIMEI: first.ModemIMEI,
		IMSI: "sim-b", Peer: first.Peer, Direction: first.Direction, Body: first.Body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rescanned.ID != first.ID || rescanned.IMSI != "" {
		t.Fatalf("rescanned message = %#v, want unknown subscriber to remain immutable", rescanned)
	}
}

func TestSMSFilterExactIMSIAndAtomicHardwareThreadDelete(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, ":memory:")
	mustSaveDevice(t, database, "ec20-1", "EC20")
	const imei = "867394042309830"
	values := make([]SMSMessage, 0, 1005)
	for index := 0; index < 1003; index++ {
		values = append(values, SMSMessage{
			MessageID: fmt.Sprintf("bulk-%d", index), DeviceID: "ec20-1", ModemIMEI: imei,
			IMSI: "sim-a", Peer: "peer", Direction: "inbound", Body: "bulk",
		})
	}
	values = append(values,
		SMSMessage{MessageID: "empty-imsi", DeviceID: "ec20-1", ModemIMEI: imei, Peer: "peer", Direction: "inbound", Body: "empty"},
		SMSMessage{MessageID: "other-sim", DeviceID: "ec20-1", ModemIMEI: imei, IMSI: "sim-b", Peer: "peer", Direction: "inbound", Body: "other"},
	)
	if err := database.SaveSMSMessages(ctx, values); err != nil {
		t.Fatal(err)
	}
	empty, err := database.ListSMSMessages(ctx, SMSFilter{ModemIMEI: imei, IMSIExact: true, IMSI: "", Limit: 10})
	if err != nil || len(empty) != 1 || empty[0].MessageID != "empty-imsi" {
		t.Fatalf("exact empty IMSI = %#v, %v", empty, err)
	}
	nonempty, err := database.ListSMSMessages(ctx, SMSFilter{ModemIMEI: imei, IMSIExact: true, IMSI: "sim-b", Limit: 10})
	if err != nil || len(nonempty) != 1 || nonempty[0].MessageID != "other-sim" {
		t.Fatalf("exact nonempty IMSI = %#v, %v", nonempty, err)
	}
	deleted, err := database.DeleteSMSThreadByFilter(ctx, SMSFilter{
		ModemIMEI: imei, IMSI: "sim-a", IMSIExact: true, Peer: "peer", Limit: 1,
	})
	if err != nil || deleted != 1003 {
		t.Fatalf("DeleteSMSThreadByFilter() = %d, %v", deleted, err)
	}
	remaining, err := database.ListSMSMessages(ctx, SMSFilter{ModemIMEI: imei, Limit: 10})
	if err != nil || len(remaining) != 2 {
		t.Fatalf("remaining cross-SIM rows = %#v, %v", remaining, err)
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
		DeviceID: "ec20-1", UpstreamProxyID: "up-1",
	}); err != nil {
		t.Fatal(err)
	}
	binding, err := database.DeviceProxyBinding(ctx, "ec20-1")
	if err != nil || binding.UpstreamProxyID != "up-1" {
		t.Fatalf("DeviceProxyBinding() = %+v, %v", binding, err)
	}
	if err := database.DeleteUpstreamProxy(ctx, "up-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CountryRule(ctx, "CN"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("country rule should cascade with upstream deletion, got %v", err)
	}
	if _, err := database.DeviceProxyBinding(ctx, "ec20-1"); !errors.Is(err, ErrNotFound) {
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
		APN: "ims", IPVersion: "ipv4v6",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertCardPolicy(ctx, CardPolicy{
		ICCID: "invalid", VoWiFiEnabled: true, AirplaneEnabled: true,
	}); err == nil {
		t.Fatal("invalid mutually exclusive card policy was accepted")
	}
	policy, err := database.CardPolicy(ctx, "89860001")
	if err != nil || !policy.VoWiFiEnabled {
		t.Fatalf("CardPolicy() = %+v, %v", policy, err)
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
