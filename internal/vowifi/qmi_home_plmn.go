package vowifi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// efADFileID is EF_AD (administrative data). Its fourth byte carries the
// length of the MNC inside the IMSI, which decides where the MCC/MNC split
// falls for the ePDG FQDN and the EAP Root NAI.
const efADFileID uint16 = 0x6FAD

// adfUSIMPath addresses ADF_USIM through the 3F00/7FFF alias. Native Qualcomm
// 410 firmware answers EF reads on this path while rejecting AT+CRSM outright
// (it replies 0x6A82, "file not found", for the same file).
var adfUSIMPath = []byte{0x00, 0x3F, 0xFF, 0x7F}

// qmiUIMFileSession is an optional capability of a QMI-UIM session: reading a
// transparent elementary file. It is separate from the AKA session interface
// so an authentication path cannot be widened by accident.
type qmiUIMFileSession interface {
	ReadTransparent(context.Context, uint16, []byte) ([]byte, error)
}

// ErrQMIUIMFileUnsupported reports a QMI session that cannot read elementary
// files, so the caller must fall back to its configured home PLMN.
var ErrQMIUIMFileUnsupported = errors.New("vocat: QMI session cannot read UICC elementary files")

type qmiHomePLMN struct {
	mcc string
	mnc string
}

// QMIUIMHomePLMNReader resolves the home PLMN from EF_AD over QMI-UIM.
//
// EC20/EC25 firmware exposes EF_AD through AT+CRSM, but native OpenStick/410
// firmware does not implement that command, so the AT path fails and the
// adapter is left with a compiled-in prefix table. Without this reader a 410
// only works with a SIM whose MNC length happens to be hard-coded, and every
// other card fails at SIM readiness before an ePDG is ever contacted.
//
// The MNC length is a fixed property of the card, so a result is cached per
// ICCID: ReadIdentity runs on every identity watchdog tick and must not open a
// QMI client each time.
type QMIUIMHomePLMNReader struct {
	openSession qmiUIMSessionOpener

	mu    sync.Mutex
	cache map[string]qmiHomePLMN
}

func NewQMIUIMHomePLMNReader() *QMIUIMHomePLMNReader {
	return newQMIUIMHomePLMNReader(openQMIUIMSession)
}

func newQMIUIMHomePLMNReader(opener qmiUIMSessionOpener) *QMIUIMHomePLMNReader {
	return &QMIUIMHomePLMNReader{
		openSession: opener,
		cache:       make(map[string]qmiHomePLMN),
	}
}

// Read returns the MCC and MNC that prefix the supplied IMSI. A cached entry is
// keyed by ICCID, so swapping the card re-reads the file.
func (reader *QMIUIMHomePLMNReader) Read(
	ctx context.Context,
	controlDevice string,
	iccid string,
	imsi string,
) (string, string, error) {
	if reader == nil || reader.openSession == nil {
		return "", "", errors.New("vocat: QMI-UIM home PLMN reader is not configured")
	}
	iccid = strings.TrimSpace(iccid)
	imsi = strings.TrimSpace(imsi)
	if iccid == "" || imsi == "" {
		return "", "", errors.New("vocat: QMI-UIM home PLMN needs both ICCID and IMSI")
	}
	if cached, ok := reader.cached(iccid); ok {
		if !strings.HasPrefix(imsi, cached.mcc+cached.mnc) {
			// The cache is keyed by card, so this means the IMSI changed under a
			// retained ICCID. Discard rather than split the new IMSI at a length
			// that was never read from it.
			reader.forget(iccid)
		} else {
			return cached.mcc, cached.mnc, nil
		}
	}

	controlDevice = strings.TrimSpace(controlDevice)
	if controlDevice == "" {
		return "", "", errors.New("vocat: QMI-UIM control device is required")
	}
	session, err := reader.openSession(ctx, controlDevice)
	if err != nil {
		return "", "", fmt.Errorf("vocat: open QMI-UIM session: %w", err)
	}
	defer session.Close()
	files, supported := session.(qmiUIMFileSession)
	if !supported {
		return "", "", ErrQMIUIMFileUnsupported
	}
	data, err := files.ReadTransparent(ctx, efADFileID, adfUSIMPath)
	if err != nil {
		return "", "", fmt.Errorf("vocat: read EF_AD over QMI-UIM: %w", err)
	}
	length, err := efADMNCLength(data)
	if err != nil {
		return "", "", err
	}
	if len(imsi) < 3+length {
		return "", "", errors.New("vocat: IMSI is shorter than the EF_AD home PLMN")
	}
	value := qmiHomePLMN{mcc: imsi[:3], mnc: imsi[3 : 3+length]}
	reader.remember(iccid, value)
	return value.mcc, value.mnc, nil
}

// efADMNCLength extracts the MNC length from EF_AD. TS 31.102 puts it in the
// low nibble of the fourth byte; only 2 and 3 are assignable, and anything
// else must fail closed rather than silently mis-split the IMSI.
func efADMNCLength(data []byte) (int, error) {
	if len(data) < 4 {
		return 0, fmt.Errorf("%w: EF_AD is shorter than four bytes", ErrEC20MNCUnavailable)
	}
	length := int(data[3] & 0x0f)
	if length != 2 && length != 3 {
		return 0, fmt.Errorf("%w: EF_AD reported MNC length %d", ErrEC20MNCUnavailable, length)
	}
	return length, nil
}

func (reader *QMIUIMHomePLMNReader) cached(iccid string) (qmiHomePLMN, bool) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	value, ok := reader.cache[iccid]
	return value, ok
}

func (reader *QMIUIMHomePLMNReader) remember(iccid string, value qmiHomePLMN) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.cache == nil {
		reader.cache = make(map[string]qmiHomePLMN)
	}
	reader.cache[iccid] = value
}

func (reader *QMIUIMHomePLMNReader) forget(iccid string) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	delete(reader.cache, iccid)
}
