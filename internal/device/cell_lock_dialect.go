package device

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"

	"vocat/internal/modem"
)

type cellLockDialect struct {
	name            string
	query           string
	writePrefix     string
	responsePrefix  string
	responseLabel   string
	lockedMode      int
	normalizeFields func(int, []string) []string
}

func (dialect cellLockDialect) lockCommand(target *CellLockTarget) string {
	if target == nil {
		return dialect.writePrefix + "0"
	}
	return fmt.Sprintf("%s%d,%d,%d", dialect.writePrefix, dialect.lockedMode, target.EARFCN, target.PCI)
}

func (dialect cellLockDialect) parseStatus(response modem.Response) (CellLockStatus, error) {
	unknown := fmt.Errorf("%s: unrecognized cell lock configuration", dialect.name)
	fields := cellLockFields(response, dialect.responsePrefix)
	if dialect.responseLabel != "" {
		if len(fields) < 2 || !strings.EqualFold(fields[0], dialect.responseLabel) {
			return CellLockStatus{}, unknown
		}
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return CellLockStatus{}, unknown
	}
	mode, err := strconv.Atoi(fields[0])
	if err != nil {
		return CellLockStatus{}, unknown
	}
	if dialect.normalizeFields != nil {
		fields = dialect.normalizeFields(mode, fields)
	}
	if mode == 0 && len(fields) == 1 {
		return CellLockStatus{}, nil
	}
	if mode != dialect.lockedMode || len(fields) != 3 {
		return CellLockStatus{}, unknown
	}
	target, valid := parseCellCandidateTarget(fields[1], fields[2])
	if !valid {
		return CellLockStatus{}, unknown
	}
	return CellLockStatus{Target: &target}, nil
}

// Lock confirmation requires one well-formed CSV record, unlike best-effort snapshots.
func cellLockFields(response modem.Response, prefix string) []string {
	var fields []string
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if fields != nil || strings.ContainsAny(line, "\r\n") || !strings.HasPrefix(strings.ToUpper(line), prefix) {
			return nil
		}
		reader := csv.NewReader(strings.NewReader(line[len(prefix):]))
		reader.TrimLeadingSpace = true
		var err error
		fields, err = reader.Read()
		if err != nil {
			return nil
		}
		for index := range fields {
			fields[index] = strings.TrimSpace(fields[index])
		}
	}
	return fields
}

func cellLockMatches(status CellLockStatus, target *CellLockTarget) bool {
	if status.Target == nil || target == nil {
		return status.Target == nil && target == nil
	}
	return *status.Target == *target
}
