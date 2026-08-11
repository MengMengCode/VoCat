package vowifi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// qmiSMSCSession is an optional capability of a QMI-UIM session. Reading the
// service-centre address is a read-only WMS query, so it stays out of the
// narrow AKA session interface and is requested only when SMS over IMS needs a
// TS-Service-Centre address.
type qmiSMSCSession interface {
	GetSMSCAddress(context.Context) (string, error)
}

// qmiSMSParameterSession reads EF_SMSP, the SIM's own SMS parameter record.
// Observed on a live 410: WMS answered QMI error 0x34 (device not ready) on a
// freshly allocated client and succeeded seconds later, while this UICC read
// worked on the first attempt in the same radio state. It is therefore the
// fallback rather than the other way round only because WMS also reflects an
// SMSC provisioned in NV rather than on the card.
type qmiSMSParameterSession interface {
	ReadSMSParameterRecord(context.Context) ([]byte, error)
}

// efSMSPFileID is EF_SMSP, the SMS parameter record that holds the card's
// TS-Service-Centre address. It is read through the ADF_USIM path: the same
// file identifier under DF_TELECOM is rejected by 410 firmware.
const efSMSPFileID uint16 = 0x6F42

var (
	// ErrQMISMSCUnsupported reports a QMI session that offers neither the WMS
	// service-centre query nor an EF_SMSP read. IMS receive stays available;
	// only submission needs the address.
	ErrQMISMSCUnsupported = errors.New("vocat: QMI session cannot read the SMS service-centre address")

	_ SMSCenterReader = (*QMIUIMAKAProvider)(nil)
)

// ReadSMSCenter returns the service-centre address the modem holds for this
// UICC, read over QMI WMS.
//
// EC20/EC25 firmware answers AT+CSCA?, but native OpenStick/410 WWAN firmware
// implements only part of TS 27.007 on its AT port while exposing the same
// UICC and message service completely through QMI. Without this path a 410
// reaches IMS registration and can still receive, yet settles on
// "ims_registered_sms_unavailable" and refuses every submission, because the
// AT probe is the only source of a TS-SCA. The query reads a stored value
// rather than the network, so it remains available while a VoWiFi session
// keeps cellular RF off, including when the subscriber is roaming and the home
// SMSC is the only correct submission target.
func (provider *QMIUIMAKAProvider) ReadSMSCenter(
	ctx context.Context,
	// The provider is constructed for one device and bound to its QMI control
	// node, so the caller's device ID is not used to address the modem.
	_ string,
) (string, error) {
	if provider == nil {
		return "", errors.New("vocat: QMI-UIM provider is not configured")
	}
	// Serialize with AKA so a readiness probe cannot open a second QMI client
	// on the same control node while an authentication exchange is in flight.
	provider.mu.Lock()
	defer provider.mu.Unlock()

	session, err := provider.session(ctx)
	if err != nil {
		return "", err
	}
	defer session.Close()

	var failures []error
	if reader, supported := session.(qmiSMSCSession); supported {
		raw, readErr := reader.GetSMSCAddress(ctx)
		if readErr == nil {
			value, parseErr := parseQMISMSCAddress(raw)
			if parseErr == nil {
				return value, nil
			}
			failures = append(failures, parseErr)
		} else {
			failures = append(failures, fmt.Errorf("vocat: read QMI-WMS SMS service centre: %w", readErr))
		}
	}
	if reader, supported := session.(qmiSMSParameterSession); supported {
		record, readErr := reader.ReadSMSParameterRecord(ctx)
		if readErr == nil {
			value, decodeErr := decodeEFSMSPServiceCentre(record)
			if decodeErr == nil {
				return value, nil
			}
			failures = append(failures, decodeErr)
		} else {
			failures = append(failures, fmt.Errorf("vocat: read EF_SMSP over QMI-UIM: %w", readErr))
		}
	}
	if len(failures) == 0 {
		return "", ErrQMISMSCUnsupported
	}
	return "", errors.Join(failures...)
}

// decodeEFSMSPServiceCentre extracts the TS-Service-Centre address from an
// EF_SMSP record (TS 31.102 4.2.27). The record ends with a fixed 28-byte
// block — parameter indicators, TP-Destination Address, TS-Service-Centre
// Address, TP-PID, TP-DCS, TP-VP — preceded by a variable-length alpha
// identifier, so the block is located from the end rather than from a guessed
// alpha length. TON is preserved as a leading "+" exactly as the AT+CSCA? and
// WMS paths do.
func decodeEFSMSPServiceCentre(record []byte) (string, error) {
	const trailerLength = 28
	if len(record) < trailerLength {
		return "", errors.New("vocat: EF_SMSP record is shorter than its fixed parameter block")
	}
	trailer := record[len(record)-trailerLength:]
	if trailer[0]&0x02 != 0 {
		// Parameter indicator bit 2 set means the card stores no service centre.
		return "", errors.New("vocat: EF_SMSP holds no SMS service-centre address")
	}
	address := trailer[13:25]
	length := int(address[0])
	if length < 2 || length > len(address)-1 {
		return "", errors.New("vocat: EF_SMSP service-centre address length is invalid")
	}
	digits := decodeSwappedBCDDigits(address[2 : 1+length])
	if !validDigits(digits, 3, 20) {
		return "", errors.New("vocat: EF_SMSP service-centre address is invalid")
	}
	if address[1]&0x70 == 0x10 {
		return "+" + digits, nil
	}
	return digits, nil
}

// decodeSwappedBCDDigits reads TS 23.040 semi-octets, dropping the 0xf padding
// nibble that terminates an odd-length address.
func decodeSwappedBCDDigits(data []byte) string {
	var digits strings.Builder
	for _, octet := range data {
		low := octet & 0x0f
		high := octet >> 4
		if low > 9 {
			break
		}
		digits.WriteByte('0' + low)
		if high > 9 {
			break
		}
		digits.WriteByte('0' + high)
	}
	return digits.String()
}

// parseQMISMSCAddress decodes a QMI WMS Get SMSC Address payload: a
// three-character ASCII address type holding the decimal TS 24.011 address
// octet (145 for international), a one-octet digit count, then ASCII digits.
//
// The type is preserved as a leading "+" exactly as the AT+CSCA? path does
// with <tosca>, so SMS over IMS builds the same international tel URI and RP
// destination from either transport. A type the firmware leaves blank is
// treated as unknown rather than international: a missing "+" is corrected by
// the SMSC, while a fabricated one is not.
func parseQMISMSCAddress(raw string) (string, error) {
	if len(raw) < 4 {
		return "", errors.New("vocat: QMI returned a truncated SMS service-centre address")
	}
	digits := raw[4:]
	if count := int(raw[3]); count > 0 && count <= len(digits) {
		digits = digits[:count]
	}
	digits = strings.TrimSpace(digits)
	international := strings.HasPrefix(digits, "+")
	digits = strings.TrimPrefix(digits, "+")
	if toa, convErr := strconv.Atoi(strings.TrimSpace(raw[:3])); convErr == nil && toa&0x70 == 0x10 {
		international = true
	}
	if !validDigits(digits, 3, 20) {
		return "", errors.New("vocat: QMI returned an invalid SMS service-centre address")
	}
	if international {
		return "+" + digits, nil
	}
	return digits, nil
}
