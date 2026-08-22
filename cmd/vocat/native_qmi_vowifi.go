package main

import (
	"context"

	"vocat/internal/device"
	"vocat/internal/vowifi"
	"vocat/internal/vowifi/integration"
)

// nativeQMIControllerMapper keeps the configured Web/API device ID stable
// while Linux exposes the physical MHI modem under its discovery ID.
type nativeQMIControllerMapper struct {
	Mapper  integration.ATMapper
	Devices *device.Manager
}

func (mapper nativeQMIControllerMapper) physical(configuredID string) (string, error) {
	entry, err := mapper.Mapper.Get(configuredID)
	if err != nil {
		return "", err
	}
	return entry.ID, nil
}

func (mapper nativeQMIControllerMapper) ReadNativeQMIIdentity(ctx context.Context, id string) (string, string, string, string, string, error) {
	physical, err := mapper.physical(id)
	if err != nil {
		return "", "", "", "", "", err
	}
	return mapper.Devices.ReadNativeQMIIdentity(ctx, physical)
}

func (mapper nativeQMIControllerMapper) ReadSIMMetadata(ctx context.Context, id string) (vowifi.SIMMetadata, error) {
	return mapper.Mapper.ReadSIMMetadata(ctx, id)
}

func (mapper nativeQMIControllerMapper) ReadSMSCenter(ctx context.Context, id string) (string, error) {
	return mapper.Mapper.ReadSMSCenter(ctx, id)
}

func (mapper nativeQMIControllerMapper) ProbeNativeQMIApplication(ctx context.Context, id, preference string) ([]byte, string, error) {
	physical, err := mapper.physical(id)
	if err != nil {
		return nil, "", err
	}
	return mapper.Devices.ProbeNativeQMIApplication(ctx, physical, preference)
}

func (mapper nativeQMIControllerMapper) AuthenticateNativeQMI(ctx context.Context, id string, aid, apdu []byte) ([]byte, error) {
	physical, err := mapper.physical(id)
	if err != nil {
		return nil, err
	}
	return mapper.Devices.AuthenticateNativeQMI(ctx, physical, aid, apdu)
}

func (mapper nativeQMIControllerMapper) NativeQMIRadioSnapshot(ctx context.Context, id string) (int, bool, error) {
	physical, err := mapper.physical(id)
	if err != nil {
		return 0, false, err
	}
	return mapper.Devices.NativeQMIRadioSnapshot(ctx, physical)
}

func (mapper nativeQMIControllerMapper) StopNativeQMICellularData(ctx context.Context, id string) error {
	physical, err := mapper.physical(id)
	if err != nil {
		return err
	}
	return mapper.Devices.StopNativeQMICellularData(ctx, physical)
}

func (mapper nativeQMIControllerMapper) SetNativeQMIRadioOff(ctx context.Context, id string, off bool) error {
	physical, err := mapper.physical(id)
	if err != nil {
		return err
	}
	return mapper.Devices.SetNativeQMIRadioOff(ctx, physical, off)
}
