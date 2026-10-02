package store

import (
	"context"
	"fmt"
)

// ExportSMSMessages visits every matching stored message without changing read
// state or contacting a modem. A single SELECT keeps one SQLite read snapshot;
// rows are streamed rather than accumulated in memory. The visitor must not
// query this Store, which may have only one database connection.
//
// Pagination fields are deliberately ignored. Conversations use the same
// hardware/subscription identity as ListSMSContacts, then chronological order
// with the durable row ID as a tie-breaker.
func (s *Store) ExportSMSMessages(ctx context.Context, filter SMSFilter, visit func(SMSMessage) error) error {
	filter.BeforeID = 0
	where, args := smsWhere(filter, "")
	rows, err := s.db.QueryContext(ctx, smsMessageSelect+where+`
		ORDER BY COALESCE(NULLIF(modem_imei, ''), 'device:' || device_id),
			COALESCE(NULLIF(iccid, ''), NULLIF('imsi:' || imsi, 'imsi:'), 'unknown'),
			peer, message_time, id`, args...)
	if err != nil {
		return fmt.Errorf("query SMS export: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		message, err := scanSMSMessage(rows)
		if err != nil {
			return fmt.Errorf("scan SMS export: %w", err)
		}
		if err := visit(message); err != nil {
			return fmt.Errorf("write SMS export: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate SMS export: %w", err)
	}
	return ctx.Err()
}
