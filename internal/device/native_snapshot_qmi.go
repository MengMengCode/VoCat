package device

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/modem"
)

// qmiNativeSnapshotData contains the parts of a device snapshot that are
// available only through QMI on the OpenStick/Qualcomm WWAN path.
type qmiNativeSnapshotData struct {
	accessTech string
	band       string
	channel    string
	imsi       string
	phone      PhoneNumber
	phoneErr   error
}

// readNativeQMIIdentity reads the active card identity (ICCID + IMSI) through
// a single native QMI-DMS session. DMS reflects the baseband's live card,
// which is authoritative after an eSIM profile switch and does not depend on
// the AT ICCID/IMSI serial cache.
func (manager *Manager) readNativeQMIIdentity(ctx context.Context, id string) (iccid, imsi string) {
	controlDevice, native, err := manager.nativeQMIControl(id)
	if err != nil || !native || manager.qmiRadioOpener == nil {
		return "", ""
	}
	readContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*2)
	defer cancel()
	session, err := manager.qmiRadioOpener(readContext, controlDevice)
	if err != nil {
		return "", ""
	}
	defer session.Close()
	identity, ok := session.(qmiNativeSnapshotSession)
	if !ok {
		return "", ""
	}
	if raw, err := identity.GetICCID(readContext); err == nil {
		iccid = strings.TrimRight(strings.ToUpper(strings.TrimSpace(raw)), "F")
	}
	if raw, err := identity.GetIMSI(readContext); err == nil {
		if v := strings.TrimSpace(raw); v != "" {
			imsi = v
		}
	}
	return iccid, imsi
}

// readNativeQMISnapshot opens one QMI session and reads both the active RF
// tuple and DMS MSISDN.  Keeping the session scoped to one refresh avoids
// racing qmicli/AT refreshes while still leaving all existing optional QMI
// fakes and non-native modem paths untouched.
func (manager *Manager) readNativeQMISnapshot(
	ctx context.Context,
	candidate modem.Candidate,
) (qmiNativeSnapshotData, []string) {
	if manager == nil || !isNativeQMICandidate(candidate) || manager.qmiRadioOpener == nil {
		return qmiNativeSnapshotData{}, nil
	}
	control := strings.TrimSpace(candidate.QMIControl)
	queryContext, cancel := manager.withTimeout(ctx, manager.commandTimeout*4)
	defer cancel()
	session, err := manager.qmiRadioOpener(queryContext, control)
	if err != nil {
		return qmiNativeSnapshotData{}, []string{"open native QMI snapshot session: " + err.Error()}
	}
	defer session.Close()
	native, ok := session.(qmiNativeSnapshotSession)
	if !ok {
		return qmiNativeSnapshotData{}, nil
	}
	data := qmiNativeSnapshotData{}
	var warnings []string
	if raw, err := native.GetMSISDN(queryContext); err != nil {
		data.phoneErr = err
		if !isQMINotProvisioned(err) {
			warnings = append(warnings, "read QMI DMS MSISDN: "+err.Error())
		}
	} else if number := normalizePhoneNumber(raw); number != "" {
		data.phone = PhoneNumber{
			Number: number,
			Source: PhoneSourceQMIDMSMSISDN,
			Status: "号码来自 QMI DMS MSISDN",
		}
	}
	if raw, err := native.GetIMSI(queryContext); err == nil {
		if imsi := strings.TrimSpace(raw); imsi != "" {
			data.imsi = imsi
		}
	}

	var bandInfo *qmi.RFBandInfo
	if info, err := native.GetRFBandInfo(queryContext); err == nil {
		bandInfo = info
	}
	var cellInfo *qmi.CellLocationInfo
	if info, err := native.GetCellLocationInfo(queryContext); err == nil {
		cellInfo = info
	}
	data.accessTech, data.band, data.channel = qmiNativeNetworkMetrics(bandInfo, cellInfo)
	return data, warnings
}

// qmiNativeNetworkMetrics maps the QMI NAS RF/cell structures to the same
// values used by the existing AT+QENG parser.  QMI uses 0x08 for LTE and
// reports the LTE band number directly (for example band 3 + EARFCN 1600).
func qmiNativeNetworkMetrics(
	bandInfo *qmi.RFBandInfo,
	cellInfo *qmi.CellLocationInfo,
) (accessTech, band, channel string) {
	if bandInfo != nil {
		for _, entry := range bandInfo.Bands {
			if accessTech == "" {
				accessTech = qmiRadioInterfaceName(entry.RadioInterface)
			}
			if entry.RadioInterface == qmi.DMSRadioInterfaceLTE {
				accessTech = "LTE"
				band = qmiBandLabel(entry.RadioInterface, entry.ActiveBandClass)
				if entry.ActiveChannel > 0 {
					channel = strconv.FormatUint(uint64(entry.ActiveChannel), 10)
				}
				break
			}
			if entry.RadioInterface == qmi.DMSRadioInterfaceNR5G || entry.RadioInterface == 0x0C {
				accessTech = "NR5G"
				band = qmiBandLabel(entry.RadioInterface, entry.ActiveBandClass)
				if entry.ActiveChannel > 0 {
					channel = strconv.FormatUint(uint64(entry.ActiveChannel), 10)
				}
				break
			}
		}
	}

	// Some firmware exposes the cell-location EARFCN but omits the RF-band
	// tuple.  Use it as a channel fallback and derive the common LTE band
	// ranges where the mapping is unambiguous.
	if cellInfo != nil && cellInfo.LTE != nil {
		accessTech = "LTE"
		earFcn := cellInfo.LTE.EARFCN
		if channel == "" && earFcn > 0 {
			channel = strconv.FormatUint(uint64(earFcn), 10)
		}
		if band == "" {
			band = lteBandFromEARFCN(earFcn)
		}
	}
	return accessTech, band, channel
}

func qmiBandLabel(radioInterface uint8, activeBandClass uint16) string {
	if activeBandClass == 0 {
		return ""
	}
	if radioInterface == qmi.DMSRadioInterfaceLTE {
		if band, ok := qmiLTEBandNumbers[activeBandClass]; ok {
			return fmt.Sprintf("B%d", band)
		}
		// A few modem firmwares return the human LTE band number instead of
		// libqmi's QMI_NAS_ACTIVE_BAND_EUTRAN_* enum value.
		if activeBandClass < 100 {
			return fmt.Sprintf("B%d", activeBandClass)
		}
	}
	if radioInterface == qmi.DMSRadioInterfaceNR5G || radioInterface == 0x0C {
		if band, ok := qmiNR5GBandNumbers[activeBandClass]; ok {
			return fmt.Sprintf("n%d", band)
		}
		if activeBandClass < 200 {
			return fmt.Sprintf("n%d", activeBandClass)
		}
	}
	return ""
}

// QMI NAS encodes LTE bands as an enum (EUTRAN-3 is 122), not as the band
// number itself.  Keep this mapping local because the qmi-go model exposes the
// raw enum value for compatibility with non-libqmi users.
var qmiLTEBandNumbers = map[uint16]uint16{
	120: 1, 121: 2, 122: 3, 123: 4, 124: 5, 125: 6, 126: 7, 127: 8,
	128: 9, 129: 10, 130: 11, 131: 12, 132: 13, 133: 14, 134: 17,
	143: 18, 144: 19, 145: 20, 146: 21, 152: 23, 147: 24, 148: 25,
	153: 26, 164: 27, 158: 28, 159: 29, 160: 30, 165: 31, 154: 32,
	135: 33, 136: 34, 137: 35, 138: 36, 139: 37, 140: 38, 141: 39,
	142: 40, 149: 41, 150: 42, 151: 43, 163: 46, 166: 47, 167: 48,
	161: 66, 168: 71, 155: 125, 156: 126, 157: 127, 162: 250,
}

var qmiNR5GBandNumbers = map[uint16]uint16{
	250: 1, 251: 2, 252: 3, 253: 5, 254: 7, 255: 8, 256: 20,
	257: 28, 258: 38, 259: 41, 260: 50, 261: 51, 262: 66,
	263: 70, 264: 71, 265: 74, 266: 75, 267: 76, 268: 77,
	269: 78, 270: 79, 271: 80, 272: 81, 273: 82, 274: 83,
	275: 84, 276: 85, 277: 257, 278: 258, 279: 259, 280: 260,
	281: 261, 282: 12, 283: 25, 284: 34, 285: 39, 286: 40,
	287: 65, 288: 86, 289: 48, 290: 14, 291: 13, 292: 18,
	293: 26, 294: 30,
}

func qmiRadioInterfaceName(value uint8) string {
	switch value {
	case qmi.DMSRadioInterfaceGSM:
		return "GSM"
	case qmi.DMSRadioInterfaceUMTS:
		return "UMTS"
	case qmi.DMSRadioInterfaceLTE:
		return "LTE"
	case qmi.DMSRadioInterfaceNR5G, 0x0C:
		return "NR5G"
	default:
		return ""
	}
}

func lteBandFromEARFCN(earfcn uint16) string {
	n := uint32(earfcn)
	switch {
	case n <= 599:
		return "B1"
	case n <= 1199:
		return "B2"
	case n <= 1949:
		return "B3"
	case n <= 2399:
		return "B4"
	case n <= 2649:
		return "B5"
	case n <= 2749:
		return "B6"
	case n <= 3449:
		return "B7"
	case n <= 3799:
		return "B8"
	case n >= 36000 && n <= 36199:
		return "B33"
	case n >= 36200 && n <= 36349:
		return "B34"
	case n >= 36350 && n <= 36949:
		return "B35"
	case n >= 36950 && n <= 37549:
		return "B36"
	case n >= 37550 && n <= 37749:
		return "B37"
	case n >= 37750 && n <= 38249:
		return "B38"
	case n >= 38250 && n <= 38649:
		return "B39"
	case n >= 38650 && n <= 39649:
		return "B40"
	case n >= 39650 && n <= 41589:
		return "B41"
	case n >= 41590 && n <= 43589:
		return "B42"
	case n >= 43590 && n <= 45589:
		return "B43"
	case n >= 65536 && n <= 67135:
		return "B65"
	case n >= 67136 && n <= 67535:
		return "B66"
	case n >= 67536 && n <= 67835:
		return "B67"
	case n >= 67836 && n <= 68335:
		return "B68"
	default:
		return ""
	}
}

const qmiErrorNotProvisioned uint16 = 0x0010

func isQMINotProvisioned(err error) bool {
	qmiErr := qmi.GetQMIError(err)
	return qmiErr != nil && qmiErr.ErrorCode == qmiErrorNotProvisioned
}
