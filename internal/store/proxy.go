package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) UpsertLocalProxy(ctx context.Context, value LocalProxyConfig) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local proxy update: %w", err)
	}
	defer tx.Rollback()
	if err := upsertLocalProxy(ctx, tx, value); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit local proxy update: %w", err)
	}
	return nil
}

func upsertLocalProxy(
	ctx context.Context,
	executor contextQueryExecer,
	value LocalProxyConfig,
) error {
	value.ID = strings.TrimSpace(value.ID)
	value.Name = strings.TrimSpace(value.Name)
	value.Mode = strings.ToLower(strings.TrimSpace(value.Mode))
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.ListenAddr = strings.TrimSpace(value.ListenAddr)
	if value.ID == "" || value.Name == "" || value.DeviceID == "" {
		return errors.New("local proxy id, name, and device id are required")
	}
	if value.Mode != "socks5" && value.Mode != "http" {
		return fmt.Errorf("unsupported local proxy mode %q", value.Mode)
	}
	if value.ListenAddr == "" {
		value.ListenAddr = "0.0.0.0"
	}
	if value.ListenPort < 1 || value.ListenPort > 65535 {
		return errors.New("local proxy listen port must be between 1 and 65535")
	}
	extra, err := normalizeJSONObject(value.Extra)
	if err != nil {
		return fmt.Errorf("normalize local proxy extra data: %w", err)
	}

	if !value.AuthEnabled {
		value.Username = ""
		value.Password = ""
	} else {
		current, currentErr := localProxy(
			executor.QueryRowContext(ctx, localProxySelect+` WHERE id = ?`, value.ID),
		)
		if currentErr != nil && !errors.Is(currentErr, ErrNotFound) {
			return fmt.Errorf("read local proxy before update: %w", currentErr)
		}
		if currentErr == nil && (value.Password == "" || value.Password == SecretMask) {
			value.Password = current.Password
		}
		if strings.TrimSpace(value.Username) == "" || value.Password == "" || value.Password == SecretMask {
			return errors.New("enabled local proxy authentication requires username and password")
		}
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
		INSERT INTO local_proxy_config (
			id, name, mode, device_id, listen_addr, listen_port, enabled,
			auth_enabled, username, password, extra_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			mode = excluded.mode,
			device_id = excluded.device_id,
			listen_addr = excluded.listen_addr,
			listen_port = excluded.listen_port,
			enabled = excluded.enabled,
			auth_enabled = excluded.auth_enabled,
			username = excluded.username,
			password = excluded.password,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at
	`,
		value.ID, value.Name, value.Mode, value.DeviceID, value.ListenAddr,
		value.ListenPort, boolInt(value.Enabled), boolInt(value.AuthEnabled),
		value.Username, value.Password, string(extra), createdAt.Unix(),
		updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert local proxy %q: %w", value.ID, err)
	}
	return nil
}

func (s *Store) LocalProxy(ctx context.Context, id string) (LocalProxyConfig, error) {
	return localProxy(s.db.QueryRowContext(ctx, localProxySelect+` WHERE id = ?`, id))
}

func (s *Store) ListLocalProxies(ctx context.Context) ([]LocalProxyConfig, error) {
	rows, err := s.db.QueryContext(ctx, localProxySelect+` ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list local proxies: %w", err)
	}
	defer rows.Close()
	values := make([]LocalProxyConfig, 0)
	for rows.Next() {
		value, err := localProxy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan local proxy: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local proxies: %w", err)
	}
	return values, nil
}

func (s *Store) ReplaceLocalProxies(ctx context.Context, values []LocalProxyConfig) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local proxy replacement: %w", err)
	}
	defer tx.Rollback()

	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if _, duplicate := seen[value.ID]; duplicate {
			return fmt.Errorf("duplicate local proxy id %q", value.ID)
		}
		if err := upsertLocalProxy(ctx, tx, value); err != nil {
			return fmt.Errorf("replace local proxy item %d: %w", index, err)
		}
		seen[value.ID] = struct{}{}
	}

	rows, err := tx.QueryContext(ctx, `SELECT id FROM local_proxy_config`)
	if err != nil {
		return fmt.Errorf("list stale local proxies: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan stale local proxy: %w", err)
		}
		if _, keep := seen[id]; !keep {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close local proxy cursor: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stale local proxies: %w", err)
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM local_proxy_config WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete stale local proxy %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit local proxy replacement: %w", err)
	}
	return nil
}

func (s *Store) DeleteLocalProxy(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM local_proxy_config WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete local proxy %q: %w", id, err)
	}
	return requireAffected(result)
}

const localProxySelect = `
	SELECT id, name, mode, device_id, listen_addr, listen_port, enabled,
		auth_enabled, username, password, extra_json, created_at, updated_at
	FROM local_proxy_config`

func localProxy(row rowScanner) (LocalProxyConfig, error) {
	var value LocalProxyConfig
	var enabled, authEnabled int
	var extra string
	var createdAt, updatedAt int64
	err := row.Scan(
		&value.ID, &value.Name, &value.Mode, &value.DeviceID,
		&value.ListenAddr, &value.ListenPort, &enabled, &authEnabled,
		&value.Username, &value.Password, &extra, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalProxyConfig{}, ErrNotFound
	}
	if err != nil {
		return LocalProxyConfig{}, err
	}
	value.Enabled = enabled != 0
	value.AuthEnabled = authEnabled != 0
	value.Extra = []byte(extra)
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) UpsertUpstreamProxy(ctx context.Context, value UpstreamProxy) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upstream proxy update: %w", err)
	}
	defer tx.Rollback()
	if err := upsertUpstreamProxy(ctx, tx, value); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upstream proxy update: %w", err)
	}
	return nil
}

func upsertUpstreamProxy(
	ctx context.Context,
	executor contextQueryExecer,
	value UpstreamProxy,
) error {
	value.ID = strings.TrimSpace(value.ID)
	value.Name = strings.TrimSpace(value.Name)
	value.Addr = strings.TrimSpace(value.Addr)
	value.Username = strings.TrimSpace(value.Username)
	if value.ID == "" || value.Name == "" || value.Addr == "" {
		return errors.New("upstream proxy id, name, and address are required")
	}
	extra, err := normalizeJSONObject(value.Extra)
	if err != nil {
		return fmt.Errorf("normalize upstream proxy extra data: %w", err)
	}
	if value.Username == "" {
		value.Password = ""
	} else if value.Password == "" || value.Password == SecretMask {
		current, currentErr := upstreamProxy(
			executor.QueryRowContext(ctx, upstreamProxySelect+` WHERE id = ?`, value.ID),
		)
		if currentErr != nil && !errors.Is(currentErr, ErrNotFound) {
			return fmt.Errorf("read upstream proxy before update: %w", currentErr)
		}
		if currentErr == nil {
			value.Password = current.Password
		}
		if value.Password == SecretMask {
			value.Password = ""
		}
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
		INSERT INTO upstream_proxies (
			id, name, addr, username, password, enabled, extra_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			addr = excluded.addr,
			username = excluded.username,
			password = excluded.password,
			enabled = excluded.enabled,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at
	`,
		value.ID, value.Name, value.Addr, value.Username, value.Password,
		boolInt(value.Enabled), string(extra), createdAt.Unix(), updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert upstream proxy %q: %w", value.ID, err)
	}
	return nil
}

func (s *Store) UpstreamProxy(ctx context.Context, id string) (UpstreamProxy, error) {
	return upstreamProxy(s.db.QueryRowContext(ctx, upstreamProxySelect+` WHERE id = ?`, id))
}

func (s *Store) ListUpstreamProxies(ctx context.Context) ([]UpstreamProxy, error) {
	rows, err := s.db.QueryContext(ctx, upstreamProxySelect+` ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list upstream proxies: %w", err)
	}
	defer rows.Close()
	values := make([]UpstreamProxy, 0)
	for rows.Next() {
		value, err := upstreamProxy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan upstream proxy: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate upstream proxies: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteUpstreamProxy(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM upstream_proxies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete upstream proxy %q: %w", id, err)
	}
	return requireAffected(result)
}

const upstreamProxySelect = `
	SELECT id, name, addr, username, password, enabled, extra_json,
		created_at, updated_at
	FROM upstream_proxies`

func upstreamProxy(row rowScanner) (UpstreamProxy, error) {
	var value UpstreamProxy
	var enabled int
	var extra string
	var createdAt, updatedAt int64
	err := row.Scan(
		&value.ID, &value.Name, &value.Addr, &value.Username,
		&value.Password, &enabled, &extra, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return UpstreamProxy{}, ErrNotFound
	}
	if err != nil {
		return UpstreamProxy{}, err
	}
	value.Enabled = enabled != 0
	value.Extra = []byte(extra)
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) UpsertDeviceProxyBinding(ctx context.Context, value DeviceProxyBinding) error {
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.ICCID = strings.TrimSpace(value.ICCID)
	value.ProfileName = strings.TrimSpace(value.ProfileName)
	value.UpstreamProxyID = strings.TrimSpace(value.UpstreamProxyID)
	if value.DeviceID == "" || value.ICCID == "" || value.UpstreamProxyID == "" {
		return errors.New("profile proxy binding requires device ID, ICCID, and upstream proxy ID")
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
		INSERT INTO device_proxy_bindings (
			iccid, device_id, profile_name, upstream_proxy_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(iccid) DO UPDATE SET
			device_id = excluded.device_id,
			profile_name = excluded.profile_name,
			upstream_proxy_id = excluded.upstream_proxy_id,
			updated_at = excluded.updated_at
	`, value.ICCID, value.DeviceID, value.ProfileName, value.UpstreamProxyID, createdAt.Unix(), updatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert proxy binding for ICCID %q: %w", value.ICCID, err)
	}
	return nil
}

func (s *Store) DeviceProxyBinding(ctx context.Context, iccid string) (DeviceProxyBinding, error) {
	return deviceProxyBinding(s.db.QueryRowContext(
		ctx,
		deviceProxyBindingSelect+` WHERE iccid = ?`,
		strings.TrimSpace(iccid),
	))
}

func (s *Store) ListDeviceProxyBindings(ctx context.Context) ([]DeviceProxyBinding, error) {
	rows, err := s.db.QueryContext(ctx, deviceProxyBindingSelect+` ORDER BY device_id, profile_name COLLATE NOCASE, iccid`)
	if err != nil {
		return nil, fmt.Errorf("list device proxy bindings: %w", err)
	}
	defer rows.Close()
	values := make([]DeviceProxyBinding, 0)
	for rows.Next() {
		value, err := deviceProxyBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan device proxy binding: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device proxy bindings: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteDeviceProxyBinding(ctx context.Context, iccid string) error {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM device_proxy_bindings WHERE iccid = ?`,
		strings.TrimSpace(iccid),
	)
	if err != nil {
		return fmt.Errorf("delete proxy binding for ICCID %q: %w", iccid, err)
	}
	return requireAffected(result)
}

const deviceProxyBindingSelect = `
	SELECT device_id, iccid, profile_name, upstream_proxy_id, created_at, updated_at
	FROM device_proxy_bindings`

func deviceProxyBinding(row rowScanner) (DeviceProxyBinding, error) {
	var value DeviceProxyBinding
	var createdAt, updatedAt int64
	err := row.Scan(&value.DeviceID, &value.ICCID, &value.ProfileName, &value.UpstreamProxyID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceProxyBinding{}, ErrNotFound
	}
	if err != nil {
		return DeviceProxyBinding{}, err
	}
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func (s *Store) UpsertCountryRule(ctx context.Context, value CountryRule) error {
	value.CountryCode = strings.ToUpper(strings.TrimSpace(value.CountryCode))
	value.CountryName = strings.TrimSpace(value.CountryName)
	value.UpstreamProxyID = strings.TrimSpace(value.UpstreamProxyID)
	if len(value.CountryCode) != 2 {
		return errors.New("country rule requires a two-letter country code")
	}
	for _, character := range value.CountryCode {
		if character < 'A' || character > 'Z' {
			return errors.New("country rule requires an ISO alpha-2 country code")
		}
	}
	if value.UpstreamProxyID == "" {
		return errors.New("country rule upstream proxy id is required")
	}
	extra, err := normalizeJSONObject(value.Extra)
	if err != nil {
		return fmt.Errorf("normalize country rule extra data: %w", err)
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
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO country_rules (
			country_code, country_name, upstream_proxy_id, enabled,
			extra_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(country_code) DO UPDATE SET
			country_name = excluded.country_name,
			upstream_proxy_id = excluded.upstream_proxy_id,
			enabled = excluded.enabled,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at
	`,
		value.CountryCode, value.CountryName, value.UpstreamProxyID,
		boolInt(value.Enabled), string(extra), createdAt.Unix(), updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert country rule %q: %w", value.CountryCode, err)
	}
	return nil
}

func (s *Store) CountryRule(ctx context.Context, countryCode string) (CountryRule, error) {
	return countryRule(s.db.QueryRowContext(
		ctx,
		countryRuleSelect+` WHERE country_code = ?`,
		strings.ToUpper(strings.TrimSpace(countryCode)),
	))
}

func (s *Store) ListCountryRules(ctx context.Context) ([]CountryRule, error) {
	rows, err := s.db.QueryContext(ctx, countryRuleSelect+` ORDER BY country_code`)
	if err != nil {
		return nil, fmt.Errorf("list country rules: %w", err)
	}
	defer rows.Close()
	values := make([]CountryRule, 0)
	for rows.Next() {
		value, err := countryRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan country rule: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate country rules: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteCountryRule(ctx context.Context, countryCode string) error {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM country_rules WHERE country_code = ?`,
		strings.ToUpper(strings.TrimSpace(countryCode)),
	)
	if err != nil {
		return fmt.Errorf("delete country rule %q: %w", countryCode, err)
	}
	return requireAffected(result)
}

const countryRuleSelect = `
	SELECT country_code, country_name, upstream_proxy_id, enabled,
		extra_json, created_at, updated_at
	FROM country_rules`

func countryRule(row rowScanner) (CountryRule, error) {
	var value CountryRule
	var enabled int
	var extra string
	var createdAt, updatedAt int64
	err := row.Scan(
		&value.CountryCode, &value.CountryName, &value.UpstreamProxyID,
		&enabled, &extra, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CountryRule{}, ErrNotFound
	}
	if err != nil {
		return CountryRule{}, err
	}
	value.Enabled = enabled != 0
	value.Extra = []byte(extra)
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}
