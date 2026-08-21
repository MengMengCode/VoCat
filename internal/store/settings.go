package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func DefaultNotificationSensitiveFields(channel string) []string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "telegram":
		return []string{"bot_token"}
	case "email":
		return []string{"password"}
	case "webhook":
		return []string{"secret"}
	case "pushplus":
		return []string{"token"}
	case "wecom":
		return []string{"urls"}
	case "lark":
		return []string{"url", "secret"}
	default:
		return nil
	}
}

func (s *Store) UpsertNotificationSetting(
	ctx context.Context,
	value NotificationSetting,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification setting update: %w", err)
	}
	defer tx.Rollback()
	if err := upsertNotificationSetting(ctx, tx, value); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit notification setting update: %w", err)
	}
	return nil
}

func upsertNotificationSetting(
	ctx context.Context,
	executor contextQueryExecer,
	value NotificationSetting,
) error {
	value.Channel = strings.ToLower(strings.TrimSpace(value.Channel))
	if value.Channel == "" {
		return errors.New("notification channel is required")
	}
	config, err := normalizeJSONObject(value.Config)
	if err != nil {
		return fmt.Errorf("normalize %s notification config: %w", value.Channel, err)
	}

	current, currentErr := notificationSetting(executor.QueryRowContext(
		ctx,
		notificationSettingSelect+` WHERE channel = ?`,
		value.Channel,
	))
	if currentErr != nil && !errors.Is(currentErr, ErrNotFound) {
		return fmt.Errorf("read %s notification setting before update: %w", value.Channel, currentErr)
	}
	fields := uniqueNonemptyStrings(
		DefaultNotificationSensitiveFields(value.Channel),
		value.SensitiveFields,
	)
	if currentErr == nil {
		fields = uniqueNonemptyStrings(fields, current.SensitiveFields)
		config, err = mergeJSONSecrets(config, current.Config, fields)
		if err != nil {
			return fmt.Errorf("preserve %s notification secrets: %w", value.Channel, err)
		}
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode notification sensitive fields: %w", err)
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
		INSERT INTO notification_settings (
			channel, enabled, config_json, sensitive_fields_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel) DO UPDATE SET
			enabled = excluded.enabled,
			config_json = excluded.config_json,
			sensitive_fields_json = excluded.sensitive_fields_json,
			updated_at = excluded.updated_at
	`,
		value.Channel, boolInt(value.Enabled), string(config),
		string(fieldsJSON), createdAt.Unix(), updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert %s notification setting: %w", value.Channel, err)
	}
	return nil
}

// SaveNotificationSettings applies a multi-channel settings form atomically.
func (s *Store) SaveNotificationSettings(
	ctx context.Context,
	values []NotificationSetting,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification settings batch: %w", err)
	}
	defer tx.Rollback()
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		channel := strings.ToLower(strings.TrimSpace(value.Channel))
		if _, duplicate := seen[channel]; duplicate {
			return fmt.Errorf("duplicate notification channel %q", channel)
		}
		if err := upsertNotificationSetting(ctx, tx, value); err != nil {
			return fmt.Errorf("save notification channel %d: %w", index, err)
		}
		seen[channel] = struct{}{}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit notification settings batch: %w", err)
	}
	return nil
}

func (s *Store) NotificationSetting(
	ctx context.Context,
	channel string,
) (NotificationSetting, error) {
	return notificationSetting(s.db.QueryRowContext(
		ctx,
		notificationSettingSelect+` WHERE channel = ?`,
		strings.ToLower(strings.TrimSpace(channel)),
	))
}

func (s *Store) ListNotificationSettings(ctx context.Context) ([]NotificationSetting, error) {
	rows, err := s.db.QueryContext(ctx, notificationSettingSelect+` ORDER BY channel`)
	if err != nil {
		return nil, fmt.Errorf("list notification settings: %w", err)
	}
	defer rows.Close()
	values := make([]NotificationSetting, 0)
	for rows.Next() {
		value, err := notificationSetting(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification setting: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification settings: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteNotificationSetting(ctx context.Context, channel string) error {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM notification_settings WHERE channel = ?`,
		strings.ToLower(strings.TrimSpace(channel)),
	)
	if err != nil {
		return fmt.Errorf("delete notification setting %q: %w", channel, err)
	}
	return requireAffected(result)
}

const notificationSettingSelect = `
	SELECT channel, enabled, config_json, sensitive_fields_json,
		created_at, updated_at
	FROM notification_settings`

func notificationSetting(row rowScanner) (NotificationSetting, error) {
	var value NotificationSetting
	var enabled int
	var config, fields string
	var createdAt, updatedAt int64
	err := row.Scan(
		&value.Channel, &enabled, &config, &fields, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationSetting{}, ErrNotFound
	}
	if err != nil {
		return NotificationSetting{}, err
	}
	if err := json.Unmarshal([]byte(fields), &value.SensitiveFields); err != nil {
		return NotificationSetting{}, fmt.Errorf("decode sensitive fields: %w", err)
	}
	value.Enabled = enabled != 0
	value.Config = []byte(config)
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func uniqueNonemptyStrings(groups ...[]string) []string {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item != "" {
				seen[item] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for item := range seen {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func (s *Store) UpsertAppSetting(ctx context.Context, value AppSetting) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin app setting update: %w", err)
	}
	defer tx.Rollback()
	if err := upsertAppSetting(ctx, tx, value); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit app setting update: %w", err)
	}
	return nil
}

func upsertAppSetting(
	ctx context.Context,
	executor contextQueryExecer,
	value AppSetting,
) error {
	value.Key = strings.TrimSpace(value.Key)
	if value.Key == "" {
		return errors.New("app setting key is required")
	}
	normalized, err := normalizeJSONValue(value.Value)
	if err != nil {
		return fmt.Errorf("normalize app setting %q: %w", value.Key, err)
	}
	if value.Sensitive && maskedJSONValue(normalized) {
		current, currentErr := appSetting(executor.QueryRowContext(
			ctx,
			appSettingSelect+` WHERE key = ?`,
			value.Key,
		))
		switch {
		case currentErr == nil:
			normalized = current.Value
		case errors.Is(currentErr, ErrNotFound):
			return fmt.Errorf("new sensitive app setting %q requires a value", value.Key)
		default:
			return fmt.Errorf("read app setting before update: %w", currentErr)
		}
	}
	updatedAt := value.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = executor.ExecContext(ctx, `
		INSERT INTO app_settings (key, value_json, sensitive, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value_json = excluded.value_json,
			sensitive = excluded.sensitive,
			updated_at = excluded.updated_at
	`, value.Key, string(normalized), boolInt(value.Sensitive), updatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert app setting %q: %w", value.Key, err)
	}
	return nil
}

func (s *Store) AppSetting(ctx context.Context, key string) (AppSetting, error) {
	return appSetting(s.db.QueryRowContext(
		ctx,
		appSettingSelect+` WHERE key = ?`,
		strings.TrimSpace(key),
	))
}

func (s *Store) ListAppSettings(ctx context.Context) ([]AppSetting, error) {
	rows, err := s.db.QueryContext(ctx, appSettingSelect+` ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list app settings: %w", err)
	}
	defer rows.Close()
	values := make([]AppSetting, 0)
	for rows.Next() {
		value, err := appSetting(rows)
		if err != nil {
			return nil, fmt.Errorf("scan app setting: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate app settings: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteAppSetting(ctx context.Context, key string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete app setting %q: %w", key, err)
	}
	return requireAffected(result)
}

const appSettingSelect = `
	SELECT key, value_json, sensitive, updated_at
	FROM app_settings`

func appSetting(row rowScanner) (AppSetting, error) {
	var value AppSetting
	var sensitive int
	var raw string
	var updatedAt int64
	err := row.Scan(&value.Key, &raw, &sensitive, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AppSetting{}, ErrNotFound
	}
	if err != nil {
		return AppSetting{}, err
	}
	value.Value = []byte(raw)
	value.Sensitive = sensitive != 0
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func maskedJSONValue(value json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(value), []byte(`null`)) {
		return true
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text == "" || text == SecretMask
	}
	return false
}

func (s *Store) UpsertCardPolicy(ctx context.Context, value CardPolicy) error {
	value.ICCID = strings.TrimSpace(value.ICCID)
	value.CustomPhoneNumber = strings.TrimSpace(value.CustomPhoneNumber)
	if value.ICCID == "" {
		return errors.New("card policy ICCID is required")
	}
	value.IPVersion = strings.ToUpper(strings.TrimSpace(value.IPVersion))
	switch value.IPVersion {
	case "", "IP", "IPV6", "IPV4V6":
	default:
		return fmt.Errorf("unsupported card policy IP version %q", value.IPVersion)
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO card_policies (
			iccid, network_enabled, vowifi_enabled, airplane_enabled,
			apn, ip_version, custom_phone_number, cellular_ims_enabled, cellular_ims_managed,
			source, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(iccid) DO UPDATE SET
			network_enabled = excluded.network_enabled,
			vowifi_enabled = excluded.vowifi_enabled,
			airplane_enabled = excluded.airplane_enabled,
			apn = excluded.apn,
			ip_version = excluded.ip_version,
			custom_phone_number = excluded.custom_phone_number,
			cellular_ims_enabled = excluded.cellular_ims_enabled,
			cellular_ims_managed = excluded.cellular_ims_managed,
			source = excluded.source,
			updated_at = excluded.updated_at
	`,
		value.ICCID, boolInt(value.NetworkEnabled), boolInt(value.VoWiFiEnabled),
		boolInt(value.AirplaneEnabled), value.APN, value.IPVersion,
		value.CustomPhoneNumber, boolInt(value.CellularIMSEnabled), boolInt(value.CellularIMSManaged), value.Source,
		createdAt.Unix(), updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert card policy %q: %w", value.ICCID, err)
	}
	return nil
}

func (s *Store) CardPolicy(ctx context.Context, iccid string) (CardPolicy, error) {
	return cardPolicy(s.db.QueryRowContext(
		ctx,
		cardPolicySelect+` WHERE iccid = ?`,
		strings.TrimSpace(iccid),
	))
}

func (s *Store) ListCardPolicies(ctx context.Context) ([]CardPolicy, error) {
	rows, err := s.db.QueryContext(ctx, cardPolicySelect+` ORDER BY iccid`)
	if err != nil {
		return nil, fmt.Errorf("list card policies: %w", err)
	}
	defer rows.Close()
	values := make([]CardPolicy, 0)
	for rows.Next() {
		value, err := cardPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card policy: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate card policies: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteCardPolicy(ctx context.Context, iccid string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM card_policies WHERE iccid = ?`, iccid)
	if err != nil {
		return fmt.Errorf("delete card policy %q: %w", iccid, err)
	}
	return requireAffected(result)
}

const cardPolicySelect = `
	SELECT iccid, network_enabled, vowifi_enabled, airplane_enabled,
		apn, ip_version, custom_phone_number, cellular_ims_enabled, cellular_ims_managed,
		source, created_at, updated_at
	FROM card_policies`

func cardPolicy(row rowScanner) (CardPolicy, error) {
	var value CardPolicy
	var networkEnabled, vowifiEnabled, airplaneEnabled, cellularIMSEnabled, cellularIMSManaged int
	var createdAt, updatedAt int64
	err := row.Scan(
		&value.ICCID, &networkEnabled, &vowifiEnabled, &airplaneEnabled,
		&value.APN, &value.IPVersion, &value.CustomPhoneNumber, &cellularIMSEnabled, &cellularIMSManaged,
		&value.Source, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CardPolicy{}, ErrNotFound
	}
	if err != nil {
		return CardPolicy{}, err
	}
	value.NetworkEnabled = networkEnabled != 0
	value.VoWiFiEnabled = vowifiEnabled != 0
	value.AirplaneEnabled = airplaneEnabled != 0
	value.CellularIMSEnabled = cellularIMSEnabled != 0
	value.CellularIMSManaged = cellularIMSManaged != 0
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) UpsertCardAPNProfile(ctx context.Context, value CardAPNProfile) (CardAPNProfile, error) {
	value.ICCID = strings.TrimSpace(value.ICCID)
	value.APN = strings.TrimSpace(value.APN)
	value.IPVersion = strings.ToUpper(strings.TrimSpace(value.IPVersion))
	if value.ICCID == "" || value.APN == "" {
		return CardAPNProfile{}, errors.New("card APN profile ICCID and APN are required")
	}
	if value.IPVersion == "" {
		value.IPVersion = "IPV4V6"
	}
	switch value.IPVersion {
	case "IP", "IPV6", "IPV4V6":
	default:
		return CardAPNProfile{}, fmt.Errorf("unsupported card APN profile IP version %q", value.IPVersion)
	}
	value.RoamingIPVersion = strings.ToUpper(strings.TrimSpace(value.RoamingIPVersion))
	if value.RoamingIPVersion == "" {
		value.RoamingIPVersion = "IP"
	}
	switch value.RoamingIPVersion {
	case "IP", "IPV6", "IPV4V6":
	default:
		return CardAPNProfile{}, fmt.Errorf("unsupported card APN roaming IP version %q", value.RoamingIPVersion)
	}
	value.AuthType = strings.ToUpper(strings.TrimSpace(value.AuthType))
	if value.AuthType == "" {
		value.AuthType = "NONE"
	}
	switch value.AuthType {
	case "NONE", "PAP", "CHAP", "PAP_OR_CHAP":
	default:
		return CardAPNProfile{}, fmt.Errorf("unsupported card APN authentication type %q", value.AuthType)
	}
	value.Username = strings.TrimSpace(value.Username)
	value.Proxy = strings.TrimSpace(value.Proxy)
	value.MCC = strings.TrimSpace(value.MCC)
	value.MNC = strings.TrimSpace(value.MNC)
	now := time.Now().UTC().Unix()
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO card_apn_profiles (
			iccid, apn, username, password, proxy, mcc, mnc,
			ip_version, roaming_ip_version, auth_type, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(iccid, apn, ip_version) DO UPDATE SET
			username = excluded.username, password = excluded.password,
			proxy = excluded.proxy, mcc = excluded.mcc, mnc = excluded.mnc,
			roaming_ip_version = excluded.roaming_ip_version,
			auth_type = excluded.auth_type, updated_at = excluded.updated_at
		RETURNING id, iccid, apn, username, password, proxy, mcc, mnc,
			ip_version, roaming_ip_version, auth_type, created_at, updated_at
	`, value.ICCID, value.APN, value.Username, value.Password, value.Proxy, value.MCC, value.MNC,
		value.IPVersion, value.RoamingIPVersion, value.AuthType, now, now).Scan(
		&value.ID, &value.ICCID, &value.APN, &value.Username, &value.Password,
		&value.Proxy, &value.MCC, &value.MNC, &value.IPVersion,
		&value.RoamingIPVersion, &value.AuthType, &createdAt, &updatedAt,
	)
	if err != nil {
		return CardAPNProfile{}, fmt.Errorf("upsert card APN profile: %w", err)
	}
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) ListCardAPNProfiles(ctx context.Context, iccid string) ([]CardAPNProfile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, iccid, apn, username, password, proxy, mcc, mnc,
			ip_version, roaming_ip_version, auth_type, created_at, updated_at
		FROM card_apn_profiles WHERE iccid = ? ORDER BY id
	`, strings.TrimSpace(iccid))
	if err != nil {
		return nil, fmt.Errorf("list card APN profiles: %w", err)
	}
	defer rows.Close()
	values := make([]CardAPNProfile, 0)
	for rows.Next() {
		var value CardAPNProfile
		var createdAt, updatedAt int64
		if err := rows.Scan(&value.ID, &value.ICCID, &value.APN, &value.Username,
			&value.Password, &value.Proxy, &value.MCC, &value.MNC, &value.IPVersion,
			&value.RoamingIPVersion, &value.AuthType, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan card APN profile: %w", err)
		}
		value.CreatedAt = time.Unix(createdAt, 0).UTC()
		value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate card APN profiles: %w", err)
	}
	return values, nil
}

func (s *Store) CardAPNProfileByAPN(ctx context.Context, iccid, apn, ipVersion string) (CardAPNProfile, error) {
	profiles, err := s.ListCardAPNProfiles(ctx, iccid)
	if err != nil {
		return CardAPNProfile{}, err
	}
	for _, profile := range profiles {
		if strings.EqualFold(profile.APN, strings.TrimSpace(apn)) &&
			strings.EqualFold(profile.IPVersion, strings.TrimSpace(ipVersion)) {
			return profile, nil
		}
	}
	return CardAPNProfile{}, ErrNotFound
}

func (s *Store) UpdateCardAPNProfile(ctx context.Context, value CardAPNProfile) (CardAPNProfile, error) {
	value.ICCID = strings.TrimSpace(value.ICCID)
	value.APN = strings.TrimSpace(value.APN)
	value.Username = strings.TrimSpace(value.Username)
	value.Proxy = strings.TrimSpace(value.Proxy)
	value.MCC = strings.TrimSpace(value.MCC)
	value.MNC = strings.TrimSpace(value.MNC)
	value.IPVersion = strings.ToUpper(strings.TrimSpace(value.IPVersion))
	value.RoamingIPVersion = strings.ToUpper(strings.TrimSpace(value.RoamingIPVersion))
	value.AuthType = strings.ToUpper(strings.TrimSpace(value.AuthType))
	if value.ID < 1 || value.ICCID == "" || value.APN == "" {
		return CardAPNProfile{}, errors.New("card APN profile ID, ICCID, and APN are required")
	}
	now := time.Now().UTC().Unix()
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE card_apn_profiles SET
			apn = ?, username = ?, password = ?, proxy = ?, mcc = ?, mnc = ?,
			ip_version = ?, roaming_ip_version = ?, auth_type = ?, updated_at = ?
		WHERE id = ? AND iccid = ?
		RETURNING id, iccid, apn, username, password, proxy, mcc, mnc,
			ip_version, roaming_ip_version, auth_type, created_at, updated_at
	`, value.APN, value.Username, value.Password, value.Proxy, value.MCC, value.MNC,
		value.IPVersion, value.RoamingIPVersion, value.AuthType, now, value.ID, value.ICCID).Scan(
		&value.ID, &value.ICCID, &value.APN, &value.Username, &value.Password,
		&value.Proxy, &value.MCC, &value.MNC, &value.IPVersion,
		&value.RoamingIPVersion, &value.AuthType, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CardAPNProfile{}, ErrNotFound
	}
	if err != nil {
		return CardAPNProfile{}, fmt.Errorf("update card APN profile: %w", err)
	}
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) DeleteCardAPNProfile(ctx context.Context, iccid string, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM card_apn_profiles WHERE iccid = ? AND id = ?`, strings.TrimSpace(iccid), id)
	if err != nil {
		return fmt.Errorf("delete card APN profile: %w", err)
	}
	return requireAffected(result)
}

func (s *Store) UpsertTrafficBucket(ctx context.Context, value TrafficBucket) error {
	return s.writeTrafficBucket(ctx, value, false)
}

// AddTrafficBucket atomically accumulates counters for concurrent collectors.
func (s *Store) AddTrafficBucket(ctx context.Context, value TrafficBucket) error {
	return s.writeTrafficBucket(ctx, value, true)
}

func (s *Store) writeTrafficBucket(
	ctx context.Context,
	value TrafficBucket,
	accumulate bool,
) error {
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.Bucket = strings.TrimSpace(value.Bucket)
	if value.DeviceID == "" || value.Bucket == "" {
		return errors.New("traffic bucket device id and bucket are required")
	}
	if value.PeriodStart.IsZero() {
		return errors.New("traffic bucket period start is required")
	}
	if value.RXBytes < 0 || value.TXBytes < 0 {
		return errors.New("traffic byte counters cannot be negative")
	}
	update := `
		rx_bytes = excluded.rx_bytes,
		tx_bytes = excluded.tx_bytes`
	if accumulate {
		update = `
			rx_bytes = traffic_buckets.rx_bytes + excluded.rx_bytes,
			tx_bytes = traffic_buckets.tx_bytes + excluded.tx_bytes`
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO traffic_buckets (
			device_id, bucket, period_start, rx_bytes, tx_bytes
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(device_id, bucket, period_start) DO UPDATE SET`+update,
		value.DeviceID, value.Bucket, value.PeriodStart.UTC().Unix(),
		value.RXBytes, value.TXBytes,
	)
	if err != nil {
		return fmt.Errorf("write traffic bucket: %w", err)
	}
	return nil
}

func (s *Store) ListTrafficBuckets(
	ctx context.Context,
	filter TrafficFilter,
) ([]TrafficBucket, error) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if filter.DeviceID != "" {
		clauses = append(clauses, `device_id = ?`)
		args = append(args, filter.DeviceID)
	}
	if filter.Bucket != "" {
		clauses = append(clauses, `bucket = ?`)
		args = append(args, filter.Bucket)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, `period_start >= ?`)
		args = append(args, filter.Since.UTC().Unix())
	}
	if !filter.Until.IsZero() {
		clauses = append(clauses, `period_start < ?`)
		args = append(args, filter.Until.UTC().Unix())
	}
	query := `
		SELECT device_id, bucket, period_start, rx_bytes, tx_bytes
		FROM traffic_buckets`
	if len(clauses) > 0 {
		query += ` WHERE ` + strings.Join(clauses, ` AND `)
	}
	query += ` ORDER BY period_start ASC, device_id LIMIT ?`
	args = append(args, normalizedLimit(filter.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list traffic buckets: %w", err)
	}
	defer rows.Close()
	values := make([]TrafficBucket, 0)
	for rows.Next() {
		var value TrafficBucket
		var periodStart int64
		if err := rows.Scan(
			&value.DeviceID, &value.Bucket, &periodStart,
			&value.RXBytes, &value.TXBytes,
		); err != nil {
			return nil, fmt.Errorf("scan traffic bucket: %w", err)
		}
		value.PeriodStart = time.Unix(periodStart, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate traffic buckets: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteTrafficBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM traffic_buckets WHERE period_start < ?`,
		before.UTC().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("delete old traffic buckets: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read deleted traffic bucket count: %w", err)
	}
	return affected, nil
}
