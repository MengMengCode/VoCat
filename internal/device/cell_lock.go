package device

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"vocat/internal/modem"
)

var ErrInvalidCellLockTarget = errors.New("invalid cell lock target")

type CellLockTarget struct {
	EARFCN int `json:"earfcn"`
	PCI    int `json:"pci"`
}

type CellLockStatus struct {
	Target *CellLockTarget `json:"target"`
}

type CellInfo struct {
	CellLockTarget
	PLMN   string `json:"plmn"`
	Source string `json:"source"`
	RSRP   *int   `json:"rsrp,omitempty"`
	RSRQ   *int   `json:"rsrq,omitempty"`
	RSSI   *int   `json:"rssi,omitempty"`
	SINR   *int   `json:"sinr,omitempty"`
}

type CellList struct {
	Items           []CellInfo `json:"items"`
	NeighborsStatus string     `json:"neighborsStatus"`
}

type cellLockQueries struct {
	cells    func(*Manager, context.Context, *managedDevice) (CellList, error)
	readLock func(*Manager, context.Context, *managedDevice) (cellLockDialect, modem.Response, error)
}

func ValidateCellLockTarget(target *CellLockTarget) error {
	if target == nil {
		return nil
	}
	if target.EARFCN < 0 || target.EARFCN > 262143 {
		return fmt.Errorf("%w: EARFCN must be between 0 and 262143", ErrInvalidCellLockTarget)
	}
	if target.PCI < 0 || target.PCI > 503 {
		return fmt.Errorf("%w: PCI must be between 0 and 503", ErrInvalidCellLockTarget)
	}
	return nil
}

func parseCellCandidateTarget(earfcn, pci string) (CellLockTarget, bool) {
	parsedEARFCN, earfcnErr := strconv.Atoi(strings.TrimSpace(earfcn))
	parsedPCI, pciErr := strconv.Atoi(strings.TrimSpace(pci))
	target := CellLockTarget{EARFCN: parsedEARFCN, PCI: parsedPCI}
	return target, earfcnErr == nil && pciErr == nil && ValidateCellLockTarget(&target) == nil
}

func parseServingCells(response modem.Response, decode func(string) (servingRecord, bool)) []CellInfo {
	cells := make([]CellInfo, 0, 1)
	for _, line := range response.Lines {
		record, ok := decode(line)
		if !ok || !strings.EqualFold(record.AccessTech, "LTE") {
			continue
		}
		target, valid := parseCellCandidateTarget(record.Channel, record.PCI)
		if !valid {
			continue
		}
		cells = append(cells, CellInfo{
			CellLockTarget: target, PLMN: record.PLMN, Source: "serving",
			RSRP: record.RSRP, RSRQ: record.RSRQ, RSSI: record.RSSI, SINR: record.SINR,
		})
	}
	return cells
}

func mergeCells(serving, neighbors []CellInfo) []CellInfo {
	merged := make([]CellInfo, 0, len(serving)+len(neighbors))
	seen := make(map[CellLockTarget]struct{}, len(serving)+len(neighbors))
	for _, cells := range [][]CellInfo{serving, neighbors} {
		for _, cell := range cells {
			if _, exists := seen[cell.CellLockTarget]; exists {
				continue
			}
			seen[cell.CellLockTarget] = struct{}{}
			merged = append(merged, cell)
		}
	}
	return merged
}

func (manager *Manager) Cells(ctx context.Context, id string) (CellList, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, queries, err := manager.prepareCellLock(ctx, id)
	if err != nil {
		return CellList{}, err
	}
	defer state.opMu.Unlock()
	return queries.cells(manager, ctx, state)
}

func (manager *Manager) CellLock(ctx context.Context, id string) (CellLockStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, queries, err := manager.prepareCellLock(ctx, id)
	if err != nil {
		return CellLockStatus{}, err
	}
	defer state.opMu.Unlock()
	dialect, response, err := queries.readLock(manager, ctx, state)
	if err != nil {
		return CellLockStatus{}, err
	}
	return dialect.parseStatus(response)
}

// SetCellLock saves and verifies configuration without changing RF mode or reconnecting.
func (manager *Manager) SetCellLock(
	ctx context.Context,
	id string,
	target *CellLockTarget,
) (CellLockStatus, error) {
	if err := ValidateCellLockTarget(target); err != nil {
		return CellLockStatus{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, manager.longTimeout)
	defer cancel()
	state, queries, err := manager.prepareCellLock(ctx, id)
	if err != nil {
		return CellLockStatus{}, err
	}
	defer state.opMu.Unlock()
	// Select the dialect even if the current configuration is not a single target.
	dialect, _, err := queries.readLock(manager, ctx, state)
	if err != nil {
		return CellLockStatus{}, fmt.Errorf("read cell lock configuration: %w", err)
	}
	command := dialect.lockCommand(target)
	if err := lockMutexContext(ctx, &state.dataMu); err != nil {
		return CellLockStatus{}, err
	}
	defer state.dataMu.Unlock()
	if err := ctx.Err(); err != nil {
		return CellLockStatus{}, err
	}

	// Finish write/readback despite caller cancellation, within the original deadline.
	deadline, _ := ctx.Deadline()
	writeCtx, cancelWrite := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancelWrite()
	if _, err := manager.cellLockCommand(writeCtx, state, command); err != nil {
		return CellLockStatus{}, err
	}
	response, err := manager.cellLockCommand(writeCtx, state, dialect.query)
	if err != nil {
		return CellLockStatus{}, err
	}
	status, err := dialect.parseStatus(response)
	if err != nil {
		return CellLockStatus{}, err
	}
	if !cellLockMatches(status, target) {
		return status, fmt.Errorf("%s readback does not match the requested cell lock", dialect.name)
	}
	return status, nil
}

func SupportsCellLock(candidate modem.Candidate) bool {
	_, err := cellLockQueriesFor(candidate)
	return err == nil
}

func cellLockQueriesFor(candidate modem.Candidate) (cellLockQueries, error) {
	switch {
	case modem.IsML307(candidate):
		return cellLockQueries{cells: (*Manager).ml307Cells, readLock: (*Manager).readML307Lock}, nil
	case modem.IsQuectelUSBModem(candidate.VendorID) || modem.IsDJI4GUSB(candidate.VendorID, candidate.ProductID):
		return cellLockQueries{cells: (*Manager).quectelCells, readLock: (*Manager).readQuectelLock}, nil
	default:
		return cellLockQueries{}, ErrUnsupportedCapability
	}
}

// prepareCellLock returns with opMu held on success; the caller must unlock it.
func (manager *Manager) prepareCellLock(ctx context.Context, id string) (*managedDevice, cellLockQueries, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return nil, cellLockQueries{}, err
	}
	if err := lockMutexContext(ctx, &state.opMu); err != nil {
		return nil, cellLockQueries{}, err
	}
	ready := false
	defer func() {
		if !ready {
			state.opMu.Unlock()
		}
	}()
	if err := manager.validateActive(id, state); err != nil {
		return nil, cellLockQueries{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, cellLockQueries{}, err
	}
	candidate := manager.candidateFor(state)
	queries, err := cellLockQueriesFor(candidate)
	if err != nil {
		return nil, cellLockQueries{}, err
	}
	if _, err := manager.clientLocked(ctx, state, candidate); err != nil {
		return nil, cellLockQueries{}, err
	}
	ready = true
	return state, queries, nil
}

// Called with opMu held; discard the client on missing finals to isolate late replies.
func (manager *Manager) cellLockCommand(ctx context.Context, state *managedDevice, command string) (modem.Response, error) {
	if err := ctx.Err(); err != nil {
		return modem.Response{}, err
	}
	response, err := manager.command(ctx, state.client, command)
	if response.Final == "" {
		_ = state.client.Close()
		state.client = nil
		if err == nil {
			err = fmt.Errorf("%s: modem command completed without a final result", command)
		}
	}
	return response, err
}
