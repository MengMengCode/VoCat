package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// APIToken describes one stored bearer credential. The raw token exists only
// at creation time; TokenHash never leaves the database.
type APIToken struct {
	ID         int64
	Name       string
	ReadOnly   bool
	ExpiresAt  time.Time // zero means the token never expires
	CreatedAt  time.Time
	LastUsedAt time.Time // zero means the token was never presented
}

// CreateAPIToken stores the SHA-256 digest of a new bearer token. Callers
// check name uniqueness first; the UNIQUE constraint is the backstop.
func (s *Store) CreateAPIToken(
	ctx context.Context,
	name string,
	tokenHash []byte,
	readOnly bool,
	expiresAt time.Time,
) (APIToken, error) {
	var expiry int64
	if !expiresAt.IsZero() {
		expiry = expiresAt.UTC().Unix()
	}
	created := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (name, token_hash, read_only, expires_at, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, 0)
	`, name, tokenHash, boolToInt(readOnly), expiry, created.Unix())
	if err != nil {
		return APIToken{}, fmt.Errorf("create api token: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return APIToken{}, fmt.Errorf("read api token id: %w", err)
	}
	return APIToken{
		ID:        id,
		Name:      name,
		ReadOnly:  readOnly,
		ExpiresAt: expiresAt,
		CreatedAt: created,
	}, nil
}

// APITokens lists every stored token, oldest first.
func (s *Store) APITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, read_only, expires_at, created_at, last_used_at
		FROM api_tokens
		ORDER BY created_at, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()
	var tokens []APIToken
	for rows.Next() {
		token, scanErr := scanAPIToken(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return tokens, nil
}

// APITokenByName resolves a token by its user-chosen name.
func (s *Store) APITokenByName(ctx context.Context, name string) (APIToken, error) {
	return scanAPIToken(s.db.QueryRowContext(ctx, `
		SELECT id, name, read_only, expires_at, created_at, last_used_at
		FROM api_tokens
		WHERE name = ?
	`, name))
}

// APITokenByHash resolves a token by the SHA-256 digest of the presented
// credential.
func (s *Store) APITokenByHash(ctx context.Context, tokenHash []byte) (APIToken, error) {
	return scanAPIToken(s.db.QueryRowContext(ctx, `
		SELECT id, name, read_only, expires_at, created_at, last_used_at
		FROM api_tokens
		WHERE token_hash = ?
	`, tokenHash))
}

// TouchAPIToken records usage at most once per threshold so authenticated
// requests do not turn into a write per request.
func (s *Store) TouchAPIToken(ctx context.Context, tokenHash []byte, threshold time.Duration) error {
	now := time.Now().UTC().Unix()
	floor := now - int64(threshold/time.Second)
	_, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens
		SET last_used_at = ?
		WHERE token_hash = ? AND last_used_at < ?
	`, now, tokenHash, floor)
	if err != nil {
		return fmt.Errorf("touch api token: %w", err)
	}
	return nil
}

// DeleteAPIToken removes one token by name.
func (s *Store) DeleteAPIToken(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM api_tokens WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read api token delete result: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpiredAPITokens prunes tokens whose expiry has passed. Tokens with
// no expiry keep expires_at = 0 and are never selected.
func (s *Store) DeleteExpiredAPITokens(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(
		ctx,
		"DELETE FROM api_tokens WHERE expires_at > 0 AND expires_at <= ?",
		now.UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf("delete expired api tokens: %w", err)
	}
	return nil
}

func scanAPIToken(row rowScanner) (APIToken, error) {
	var token APIToken
	var readOnly int
	var expiresAt int64
	var createdAt int64
	var lastUsedAt int64
	err := row.Scan(&token.ID, &token.Name, &readOnly, &expiresAt, &createdAt, &lastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, err
	}
	token.ReadOnly = readOnly != 0
	if expiresAt > 0 {
		token.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	}
	token.CreatedAt = time.Unix(createdAt, 0).UTC()
	if lastUsedAt > 0 {
		token.LastUsedAt = time.Unix(lastUsedAt, 0).UTC()
	}
	return token, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
