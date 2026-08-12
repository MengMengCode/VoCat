package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const SMSRateWindow = time.Hour

// SMSRateReservation is the durable result of claiming one global outbound
// SMS slot. The quota is shared by every device, SIM, transport, and caller.
type SMSRateReservation struct {
	Allowed   bool
	Limit     int
	Used      int
	Remaining int
	ResetAt   time.Time
}

// ReserveSMSSend atomically claims one slot in the rolling one-hour window.
// It intentionally records submission attempts separately from SMS history so
// deleting a conversation cannot reset the global safety limit.
func (s *Store) ReserveSMSSend(
	ctx context.Context,
	deviceID string,
	limit int,
	now time.Time,
) (SMSRateReservation, error) {
	if limit < 1 {
		return SMSRateReservation{}, errors.New("SMS hourly limit must be positive")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	cutoff := now.Add(-SMSRateWindow).Unix()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO sms_send_attempts (device_id, created_at)
		SELECT ?, ?
		WHERE (
			SELECT COUNT(*) FROM sms_send_attempts WHERE created_at > ?
		) < ?
	`, strings.TrimSpace(deviceID), now.Unix(), cutoff, limit)
	if err != nil {
		return SMSRateReservation{}, fmt.Errorf("reserve global SMS send slot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SMSRateReservation{}, fmt.Errorf("read global SMS reservation result: %w", err)
	}

	status, err := s.smsRateStatus(ctx, limit, cutoff)
	if err != nil {
		return SMSRateReservation{}, err
	}
	status.Allowed = affected == 1
	if status.Allowed {
		// Old rows are irrelevant to enforcement. Pruning after the atomic claim
		// keeps the hot index compact without creating a delete-before-insert race.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sms_send_attempts WHERE created_at <= ?`, now.Add(-7*24*time.Hour).Unix())
	}
	return status, nil
}

func (s *Store) smsRateStatus(ctx context.Context, limit int, cutoff int64) (SMSRateReservation, error) {
	var used int
	var earliest *int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(created_at)
		FROM sms_send_attempts
		WHERE created_at > ?
	`, cutoff).Scan(&used, &earliest); err != nil {
		return SMSRateReservation{}, fmt.Errorf("read global SMS rate status: %w", err)
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	status := SMSRateReservation{Limit: limit, Used: used, Remaining: remaining}
	if earliest != nil {
		status.ResetAt = time.Unix(*earliest, 0).UTC().Add(SMSRateWindow)
	}
	return status, nil
}
