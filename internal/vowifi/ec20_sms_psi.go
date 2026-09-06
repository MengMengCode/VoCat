package vowifi

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ReadSMSCenterPSI reads record 1 of EF_PSISMSC (6FE5) under DF_TELECOM.
// It deliberately does not cache card data or alter the modem's radio mode.
func (adapter *EC20Adapter) ReadSMSCenterPSI(ctx context.Context, deviceID string) (string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "", errors.New("vocat: EC20 device ID is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Match AKA transaction lock order; CRSM must not interrupt APDU exchanges.
	adapter.apduMu.Lock()
	defer adapter.apduMu.Unlock()
	if locker, ok := adapter.executor.(EC20UICCLocker); ok {
		locker.LockUICC()
		defer locker.UnlockUICC()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	response, err := adapter.execute(ctx, deviceID, `AT+CRSM=178,28645,1,4,0,,"3F007F10"`)
	if err != nil {
		return "", fmt.Errorf("vocat: read EC20 SMS center PSI: %w", err)
	}
	if strings.TrimSpace(response.Final) != "OK" {
		return "", errors.New("vocat: EC20 SMS center PSI read did not finish OK")
	}
	count := 0
	for _, line := range response.Lines {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+CRSM:") {
			count++
		}
	}
	if count != 1 {
		return "", errors.New("vocat: EC20 SMS center PSI read requires exactly one CRSM response")
	}
	record, err := parseCRSMData(response)
	if err != nil {
		return "", fmt.Errorf("vocat: read EC20 SMS center PSI: %w", err)
	}
	// This single-command path requires a completed 9000 response, not a
	// pending 9Fxx GET RESPONSE. Do not return data from incomplete reads.
	fields := parseCSV(valueAfterATPrefix(response, "+CRSM:"))
	if len(fields) != 3 || fields[0] != "144" || fields[1] != "0" {
		return "", errors.New("vocat: EC20 SMS center PSI read requires a complete 9000 data response")
	}
	return parseEC20SMSCenterPSIRecord(record)
}

// parseEC20SMSCenterPSIRecord accepts one primitive tag-80 URI followed only
// by optional FF record padding. Unknown tags, duplicate URIs, and embedded
// padding are not guessed around. URI routing semantics belong to the caller.
func parseEC20SMSCenterPSIRecord(record []byte) (string, error) {
	if len(record) < 2 || record[0] != 0x80 {
		return "", errors.New("vocat: SMS center PSI record has no URI TLV")
	}
	offset, n := 2, int(record[1])
	if n&0x80 != 0 {
		// Definite BER lengths only, with at most two length octets (a UICC
		// record cannot need more). Accept non-minimal 81/82 card encodings.
		octets := n & 0x7f
		if octets == 0 || octets > 2 || octets > len(record)-offset {
			return "", errors.New("vocat: SMS center PSI record has invalid BER length")
		}
		n = 0
		for i := 0; i < octets; i++ {
			n = n<<8 | int(record[offset])
			offset++
		}
	}
	if n == 0 || n > len(record)-offset {
		return "", errors.New("vocat: SMS center PSI record has invalid URI length")
	}
	for _, b := range record[offset+n:] {
		if b != 0xff {
			return "", errors.New("vocat: SMS center PSI record has ambiguous or unexpected trailing data")
		}
	}
	value := record[offset : offset+n]
	for _, b := range value {
		if b < 0x21 || b > 0x7e {
			return "", errors.New("vocat: SMS center PSI record URI is not visible ASCII")
		}
	}
	return string(value), nil
}
