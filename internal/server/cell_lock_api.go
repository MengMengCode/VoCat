package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"vocat/internal/device"
)

type cellLockController interface {
	Cells(context.Context, string) (device.CellList, error)
	CellLock(context.Context, string) (device.CellLockStatus, error)
	SetCellLock(context.Context, string, *device.CellLockTarget) (device.CellLockStatus, error)
}

func (s *Server) handleCells(w http.ResponseWriter, r *http.Request, physicalID string) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	controller, ok := s.devices.(cellLockController)
	if !ok {
		writeError(w, http.StatusNotImplemented, "cell_query_unsupported", "cell querying is not available for this modem")
		return true
	}
	cells, err := controller.Cells(r.Context(), physicalID)
	if err != nil {
		if errors.Is(err, device.ErrUnsupportedCapability) {
			writeError(w, http.StatusNotImplemented, "cell_query_unsupported", "cell querying is not available for this modem model or firmware")
		} else {
			s.writeDeviceError(w, err)
		}
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": cells})
	return true
}

func (s *Server) handleCellLock(w http.ResponseWriter, r *http.Request, configID, physicalID string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return true
	}
	controller, ok := s.devices.(cellLockController)
	if !ok {
		s.writeCellLockError(w, device.ErrUnsupportedCapability)
		return true
	}
	if r.Method == http.MethodGet {
		status, err := controller.CellLock(r.Context(), physicalID)
		if err != nil {
			s.writeCellLockError(w, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"data": status})
		}
		return true
	}

	var target *device.CellLockTarget
	if r.Method == http.MethodPut {
		var request struct {
			EARFCN *int `json:"earfcn"`
			PCI    *int `json:"pci"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		if request.EARFCN == nil || request.PCI == nil {
			writeError(w, http.StatusBadRequest, "invalid_cell_lock_target", "earfcn and pci are required")
			return true
		}
		target = &device.CellLockTarget{EARFCN: *request.EARFCN, PCI: *request.PCI}
		if err := device.ValidateCellLockTarget(target); err != nil {
			s.writeCellLockError(w, err)
			return true
		}
	}
	// Configuration changes can invalidate the exit IP, but do not manage data sessions.
	s.clearPublicIP(configID)
	defer s.clearPublicIP(configID)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	status, err := controller.SetCellLock(r.Context(), physicalID, target)
	if err != nil {
		s.writeCellLockError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	}
	return true
}

func (s *Server) writeCellLockError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, device.ErrUnsupportedCapability):
		writeError(w, http.StatusNotImplemented, "cell_lock_unsupported", "cell locking is not supported by this modem or firmware")
	case errors.Is(err, device.ErrInvalidCellLockTarget):
		writeError(w, http.StatusBadRequest, "invalid_cell_lock_target", err.Error())
	default:
		s.writeDeviceError(w, err)
	}
}
