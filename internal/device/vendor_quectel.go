package device

import (
	"context"
	"errors"
	"strings"

	"vocat/internal/modem"
)

var quectel4GLockDialect = cellLockDialect{
	name:           "quectel-4g",
	query:          `AT+QNWLOCK="common/4g"`,
	writePrefix:    `AT+QNWLOCK="common/4g",`,
	responsePrefix: "+QNWLOCK:",
	responseLabel:  "common/4g",
	lockedMode:     1,
}

var quectelLTELockDialect = cellLockDialect{
	name:            "quectel-lte",
	query:           `AT+QNWLOCK="common/lte"`,
	writePrefix:     `AT+QNWLOCK="common/lte",`,
	responsePrefix:  "+QNWLOCK:",
	responseLabel:   "common/lte",
	lockedMode:      2,
	normalizeFields: normalizeQuectelLTEFields,
}

// LTE reports action, EARFCN, PCI and completion status. We expose configuration,
// not completion; cleared configuration can still carry the old coordinates.
func normalizeQuectelLTEFields(action int, fields []string) []string {
	if len(fields) != 4 || (fields[3] != "0" && fields[3] != "1") {
		return nil
	}
	if action == 0 {
		return fields[:1]
	}
	return fields[:3]
}

func (manager *Manager) readQuectelLock(ctx context.Context, state *managedDevice) (cellLockDialect, modem.Response, error) {
	dialect := quectel4GLockDialect
	response, err := manager.cellLockCommand(ctx, state, dialect.query)
	if !definiteATRejection(err) {
		return dialect, response, err
	}
	dialect = quectelLTELockDialect
	response, err = manager.cellLockCommand(ctx, state, dialect.query)
	if definiteATRejection(err) {
		err = errors.Join(ErrUnsupportedCapability, err)
	}
	return dialect, response, err
}

func (manager *Manager) quectelCells(ctx context.Context, state *managedDevice) (CellList, error) {
	result := CellList{Items: make([]CellInfo, 0)}
	serving, servingErr := manager.cellLockCommand(ctx, state, `AT+QENG="servingcell"`)
	if servingErr != nil && !definiteATRejection(servingErr) {
		return CellList{}, servingErr
	}
	if servingErr == nil {
		result.Items = parseServingCells(serving, decodeQENGServingRecord)
	}

	neighbors, neighborsErr := manager.cellLockCommand(ctx, state, `AT+QENG="neighbourcell"`)
	if neighborsErr != nil {
		if !definiteATRejection(neighborsErr) {
			return CellList{}, neighborsErr
		}
		result.NeighborsStatus = "unavailable"
		return result, nil
	}
	result.Items = mergeCells(result.Items, parseQENGNeighborCells(neighbors))
	result.NeighborsStatus = "available"
	return result, nil
}

func parseQENG(response modem.Response) servingMetrics {
	for _, line := range response.Lines {
		record, ok := decodeQENGServingRecord(line)
		if !ok {
			continue
		}
		return record.servingMetrics
	}
	return servingMetrics{}
}

func decodeQENGServingRecord(line string) (servingRecord, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(strings.ToUpper(line), "+QENG:") {
		return servingRecord{}, false
	}
	values := csvValues(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
	if len(values) < 3 || !strings.EqualFold(values[0], "servingcell") {
		return servingRecord{}, false
	}
	record := servingRecord{servingMetrics: servingMetrics{AccessTech: strings.ToUpper(values[2])}}
	if !strings.EqualFold(values[2], "LTE") || len(values) < 17 {
		return record, true
	}
	if decimalDigits(values[4], 3, 3) && decimalDigits(values[5], 2, 3) {
		record.PLMN = values[4] + values[5]
	}
	record.Channel = values[8]
	if values[9] != "" {
		record.Band = "B" + values[9]
	}
	record.PCI = values[7]
	record.RSRP = qengMetric(values[13])
	record.RSRQ = qengMetric(values[14])
	record.RSSI = qengMetric(values[15])
	record.SINR = qengMetric(values[16])
	return record, true
}

func qengMetric(value string) *int {
	value = strings.TrimSpace(value)
	if value == "" || value == "-32768" {
		return nil
	}
	return parseOptionalInt(value)
}

func parseQENGNeighborCells(response modem.Response) []CellInfo {
	cells := make([]CellInfo, 0)
	for _, line := range response.Lines {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+QENG:") {
			continue
		}
		values := csvValues(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
		if len(values) < 6 || !strings.EqualFold(values[1], "LTE") {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(values[0]))
		if label != "neighbourcell intra" && label != "neighbourcell inter" {
			continue
		}
		target, valid := parseCellCandidateTarget(values[2], values[3])
		if !valid {
			continue
		}
		rsrq := qengMetric(values[4])
		rsrp := qengMetric(values[5])
		// Frequency-level threshold records are not measured cell candidates.
		if rsrp != nil && *rsrp >= 0 || rsrq != nil && *rsrq > 0 {
			continue
		}
		cell := CellInfo{
			CellLockTarget: target, Source: "neighbor",
			RSRQ: rsrq, RSRP: rsrp,
		}
		if label == "neighbourcell intra" {
			if len(values) > 6 {
				cell.RSSI = qengMetric(values[6])
			}
			if len(values) > 7 {
				cell.SINR = qengMetric(values[7])
			}
		}
		cells = append(cells, cell)
	}
	return cells
}
