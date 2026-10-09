package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"vocat/internal/device"
	"vocat/internal/store"
)

type simResetController interface {
	ResetSIM(context.Context, string) error
}

// readESIMInventory 仅对已确认的 eSTK 管理应用异常尝试一次恢复。
// 检测返回后已释放 UICC 锁，才能停止需要该锁完成鉴权清理的 VoWiFi。
func (s *Server) readESIMInventory(ctx context.Context, configuredID, physicalID string) ([]device.EsimInventoryEntry, error) {
	info, err := s.devices.ESIMInventory(ctx, physicalID)
	if err == nil {
		s.smsSyncMu.Lock()
		delete(s.esimSIMResetAttempted, physicalID)
		s.smsSyncMu.Unlock()
		return info, nil
	}
	resetter, supported := s.devices.(simResetController)
	if !supported || s.store == nil || !errors.Is(err, device.ErrESIMManagementUnavailable) {
		return info, err
	}
	// 与切换 profile 和 SIM 短信同步串行；并发页面请求必须先重新读取，不能沿用过期的失败。
	s.smsSyncMu.Lock()
	defer s.smsSyncMu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	info, err = s.devices.ESIMInventory(ctx, physicalID)
	if !errors.Is(err, device.ErrESIMManagementUnavailable) {
		if err == nil {
			delete(s.esimSIMResetAttempted, physicalID)
		}
		return info, err
	}
	if s.esimSIMResetAttempted[physicalID] {
		return nil, err
	}
	config, configErr := s.store.Device(ctx, configuredID)
	if configErr != nil {
		return nil, configErr
	}
	if config.DeviceType != store.DeviceTypePCIeEC20EC25 {
		return nil, err
	}
	if s.esimSIMResetAttempted == nil {
		s.esimSIMResetAttempted = make(map[string]bool)
	}
	s.esimSIMResetAttempted[physicalID] = true
	// 开始停止运行时后，由独立且有界的上下文完成整个恢复和收尾。
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	info, err = s.resetSIMForESIM(recoveryCtx, configuredID, physicalID, resetter)
	if err == nil {
		delete(s.esimSIMResetAttempted, physicalID)
	} else if s.logger != nil {
		// 同次故障仅记录这一次真正的恢复失败，普通 SIM 不进入此分支。
		s.logger.Warn("eSIM SIM reset recovery failed", "device_id", configuredID, "error", device.HardwareErrorDetail(err))
	}
	return info, err
}

func (s *Server) resetSIMForESIM(ctx context.Context, configuredID, physicalID string, resetter simResetController) (info []device.EsimInventoryEntry, err error) {
	endMaintenance := func() {}
	if maintenance, ok := s.vowifi.(VoWiFiMaintenanceController); ok {
		if err := maintenance.BeginMaintenance(configuredID); err != nil {
			return nil, fmt.Errorf("prepare VoWiFi for SIM reset: %w", err)
		}
		endMaintenance = func() { maintenance.EndMaintenance(configuredID) }
	}
	defer func() {
		endMaintenance()
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		// 重新读取保存的配置，避免恢复期间覆盖用户刚修改的 VoWiFi 意愿。
		config, restoreErr := s.store.Device(restoreCtx, configuredID)
		if restoreErr == nil && s.vowifi != nil {
			_, restoreErr = s.vowifi.RequestEnabled(configuredID, config.VoWiFiEnabled)
		}
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore VoWiFi after SIM reset: %w", restoreErr))
		}
	}()
	if err = s.quiesceVoWiFiForProfileSwitch(ctx, configuredID); err != nil {
		return nil, err
	}
	if err = resetter.ResetSIM(ctx, physicalID); err != nil {
		return nil, err
	}
	info, err = s.devices.ESIMInventory(ctx, physicalID)
	if errors.Is(err, device.ErrNoEUICC) {
		// 已明确识别到 eSTK 异常；恢复后仍无法识别，不能伪装成普通 SIM 的正常空结果。
		return nil, fmt.Errorf("%w after SIM reset", device.ErrESIMManagementUnavailable)
	}
	return info, err
}
