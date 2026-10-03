package device

import (
	"context"
	"math"
	"strconv"
	"strings"

	"vocat/internal/modem"
)

var ml307LockDialect = cellLockDialect{
	name:           "ml307a",
	query:          "AT+MLOCKFREQ?",
	writePrefix:    "AT+MLOCKFREQ=",
	responsePrefix: "+MLOCKFREQ:",
	lockedMode:     1,
}

func (manager *Manager) readML307Lock(ctx context.Context, state *managedDevice) (cellLockDialect, modem.Response, error) {
	response, err := manager.cellLockCommand(ctx, state, ml307LockDialect.query)
	return ml307LockDialect, response, err
}

func (manager *Manager) ml307Cells(ctx context.Context, state *managedDevice) (CellList, error) {
	response, err := manager.cellLockCommand(ctx, state, `AT+MUESTATS="cell"`)
	if err != nil {
		return CellList{}, err
	}
	return CellList{
		Items:           parseServingCells(response, decodeMUESTATSServingRecord),
		NeighborsStatus: "unsupported",
	}, nil
}

func parseMUESTATSCell(response modem.Response) servingMetrics {
	for _, line := range response.Lines {
		record, ok := decodeMUESTATSServingRecord(line)
		if !ok {
			continue
		}
		if !record.Complete {
			record.RSRP, record.RSRQ, record.RSSI, record.SINR = nil, nil, nil, nil
		}
		return record.servingMetrics
	}
	return servingMetrics{}
}

func parseMUESTATSSBand(response modem.Response) string {
	for _, line := range response.Lines {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+MUESTATS:") {
			continue
		}
		values := csvValues(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
		if len(values) == 2 && strings.EqualFold(values[0], "sband") && decimalDigits(values[1], 1, 3) {
			return values[1]
		}
	}
	return ""
}

func decodeMUESTATSServingRecord(line string) (servingRecord, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(strings.ToUpper(line), "+MUESTATS:") {
		return servingRecord{}, false
	}
	values := csvValues(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
	if len(values) < 7 || !strings.EqualFold(values[0], "scell") {
		return servingRecord{}, false
	}
	record := servingRecord{
		servingMetrics: servingMetrics{AccessTech: mueAccessTechnology(values[1]), Channel: values[4]},
		PCI:            values[6],
	}
	if decimalDigits(values[2], 3, 3) && decimalDigits(values[3], 2, 3) {
		record.PLMN = values[2] + values[3]
	}
	if len(values) > 7 {
		record.RSRP = parseDeciMetric(values[7])
	}
	if len(values) > 8 {
		record.RSRQ = parseDeciMetric(values[8])
	}
	if len(values) > 9 {
		record.RSSI = parseDeciMetric(values[9])
	}
	if len(values) > 10 {
		record.SINR = parseDeciMetric(values[10])
	}
	record.Complete = len(values) >= 11
	return record, true
}

func parseDeciMetric(value string) *int {
	value = strings.TrimSpace(value)
	if value == "" || value == "-32768" {
		return nil
	}
	raw, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return nil
	}
	result := int(math.Round(float64(raw) / 10))
	return &result
}

func mueAccessTechnology(value string) string {
	switch strings.TrimSpace(value) {
	case "1":
		return "GSM"
	case "2":
		return "WCDMA"
	case "3":
		return "TDSCDMA"
	case "4":
		return "LTE"
	case "5":
		return "eMTC"
	case "6":
		return "NB-IoT"
	case "7":
		return "CDMA"
	case "8":
		return "EVDO"
	default:
		return ""
	}
}
