package device

import (
	"context"
	"strings"

	"vocat/internal/modem"
)

// ScannedOperator is one network reported by an operator scan (AT+COPS=?).
type ScannedOperator struct {
	// Status is "current", "available", "forbidden", or "unknown".
	Status  string `json:"status"`
	Name    string `json:"name"`
	Short   string `json:"shortName,omitempty"`
	Numeric string `json:"numeric"`
	Country string `json:"countryCode,omitempty"`
	Act     string `json:"act,omitempty"`
}

// OperatorScanResult is the outcome of a full operator scan.
type OperatorScanResult struct {
	Status    string            `json:"status"` // "complete" or "failed"
	Operators []ScannedOperator `json:"operators"`
}

// ScanOperators runs the current AT profile's network scan command. Scans can
// take tens of seconds, so it uses the manager's scan timeout rather than the
// normal command timeout and remains abortable through the caller's context.
func (manager *Manager) ScanOperators(
	ctx context.Context,
	id string,
) (OperatorScanResult, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return OperatorScanResult{}, err
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return OperatorScanResult{}, err
	}
	candidate := manager.candidateFor(state)
	profile := operatorProfileForState(state, candidate)
	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		manager.setResult(id, state, nil, err)
		return OperatorScanResult{}, err
	}
	scanContext, cancel := manager.withTimeout(ctx, manager.scanTimeout)
	defer cancel()
	response, err := client.Execute(scanContext, profile.ScanCommand())
	if err != nil {
		manager.setResult(id, state, nil, err)
		return OperatorScanResult{Status: "failed", Operators: []ScannedOperator{}}, err
	}
	result := OperatorScanResult{
		Status:    "complete",
		Operators: mapATOperatorRecords(profile.ParseScan(response)),
	}
	manager.setResult(id, state, nil, nil)
	return result, nil
}

// parseOperatorScan parses the +COPS: list returned by AT+COPS=?. Each entry is
// a parenthesised tuple (stat,"long","short","numeric"[,act]).
func parseOperatorScan(response modem.Response) []ScannedOperator {
	profile := modem.ResolveATProfile(modem.Candidate{}).Operator
	return mapATOperatorRecords(profile.ParseScan(response))
}

func operatorProfileForState(state *managedDevice, candidate modem.Candidate) modem.OperatorATProfile {
	if state.atProfile.Operator != nil {
		return state.atProfile.Operator
	}
	return modem.ResolveATProfile(candidate).Operator
}

func mapATOperatorRecords(records []modem.ATOperatorRecord) []ScannedOperator {
	operators := make([]ScannedOperator, 0, len(records))
	for _, record := range records {
		name, country, _ := CarrierForPLMN(record.Numeric)
		if name == "" {
			name = strings.TrimSpace(record.LongName)
		}
		if name == "" {
			name = strings.TrimSpace(record.Numeric)
		}
		operators = append(operators, ScannedOperator{
			Status:  operatorScanStatus(record.StatusCode),
			Name:    name,
			Short:   strings.TrimSpace(record.ShortName),
			Numeric: strings.TrimSpace(record.Numeric),
			Country: country,
			Act:     accessTechnology(record.AccessTechnologyCode),
		})
	}
	return operators
}

// carrierNameForPLMN resolves the numeric serving PLMN through the bundled
// global carrier database. Some EC20 firmware returns an empty, localized, or
// stale long name even though the MCC/MNC is correct. The numeric identity is
// the authoritative value used for network selection.
func carrierNameForPLMN(plmn, fallback string) string {
	if name, _, ok := CarrierForPLMN(plmn); ok {
		return name
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	return strings.TrimSpace(plmn)
}

func operatorScanStatus(code string) string {
	switch strings.TrimSpace(code) {
	case "1":
		return "available"
	case "2":
		return "current"
	case "3":
		return "forbidden"
	default:
		return "unknown"
	}
}
