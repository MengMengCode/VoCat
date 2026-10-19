package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SMSRawRecord archives one physical modem record, separately from normal
// inbox/export data. Slot reuse creates a new record; rescans keep its identity
// and first-seen time, even after a profile switch or a decoder upgrade.
type SMSRawRecord struct {
	ID          int64     `json:"id"`
	DeviceID    string    `json:"device_id"`
	ModemIMEI   string    `json:"modem_imei"`
	ICCID       string    `json:"iccid"`
	IMSI        string    `json:"imsi"`
	Storage     string    `json:"storage"`
	Slot        int       `json:"slot"`
	RawPDU      string    `json:"raw_pdu"`
	RawUserData string    `json:"raw_user_data"`
	Encoding    string    `json:"encoding"`
	DecodeError string    `json:"decode_error"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

func (s *Store) SaveSMSRawRecord(ctx context.Context, value SMSRawRecord) error {
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.ModemIMEI = strings.TrimSpace(value.ModemIMEI)
	value.RawPDU = strings.ToUpper(strings.TrimSpace(value.RawPDU))
	value.Storage = strings.ToUpper(strings.TrimSpace(value.Storage))
	if value.DeviceID == "" || value.RawPDU == "" || value.Slot < 0 ||
		(value.Storage != "SM" && value.Storage != "ME") {
		return errors.New("raw SMS requires a device, storage, slot, and PDU")
	}
	digest := sha256.Sum256([]byte(value.RawPDU))
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sms_raw_records (
		hardware_key, device_id, modem_imei, iccid, imsi, storage, slot, pdu_hash,
		raw_pdu, raw_user_data, encoding, decode_error, first_seen, last_seen
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(hardware_key, storage, slot, pdu_hash) DO UPDATE SET
		raw_user_data = excluded.raw_user_data, encoding = excluded.encoding,
		decode_error = excluded.decode_error, last_seen = excluded.last_seen`,
		smsHardwareKey(value.ModemIMEI, value.DeviceID), value.DeviceID, value.ModemIMEI,
		strings.TrimSpace(value.ICCID), strings.TrimSpace(value.IMSI), value.Storage,
		value.Slot, hex.EncodeToString(digest[:]), value.RawPDU,
		value.RawUserData, value.Encoding, value.DecodeError, now, now)
	if err != nil {
		return fmt.Errorf("archive raw SMS: %w", err)
	}
	return nil
}

func (s *Store) ListSMSRawRecords(ctx context.Context, deviceID string, limit int) ([]SMSRawRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, device_id, modem_imei, iccid,
		imsi, storage, slot, raw_pdu, raw_user_data, encoding, decode_error,
		first_seen, last_seen FROM sms_raw_records
		WHERE (? = '' OR device_id = ?) ORDER BY id DESC LIMIT ?`,
		strings.TrimSpace(deviceID), strings.TrimSpace(deviceID), normalizedLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list raw SMS records: %w", err)
	}
	defer rows.Close()
	values := make([]SMSRawRecord, 0)
	for rows.Next() {
		var value SMSRawRecord
		var firstSeen, lastSeen int64
		if err := rows.Scan(&value.ID, &value.DeviceID, &value.ModemIMEI, &value.ICCID,
			&value.IMSI, &value.Storage, &value.Slot, &value.RawPDU, &value.RawUserData,
			&value.Encoding, &value.DecodeError, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan raw SMS record: %w", err)
		}
		value.FirstSeen = time.Unix(firstSeen, 0).UTC()
		value.LastSeen = time.Unix(lastSeen, 0).UTC()
		values = append(values, value)
	}
	return values, rows.Err()
}
