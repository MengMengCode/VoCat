//go:build linux

package ims

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

type linuxIPSecInstaller struct {
	ipCommand string
}

func defaultIPSecInstaller() IPSecSAInstaller {
	return linuxIPSecInstaller{ipCommand: "ip"}
}

type linuxIPSecHandle struct {
	mu        sync.Mutex
	ipCommand string
	config    IPSecSAConfig
	pair      IPSecFlowPair
	closed    bool
}

func (installer linuxIPSecInstaller) Install(ctx context.Context, config IPSecSAConfig) (IPSecSAHandle, error) {
	command := installer.ipCommand
	if command == "" {
		command = "ip"
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, errors.New("ims: Linux iproute2 is required for ipsec-3gpp")
	}
	install, err := buildXFRMInstallPlan(config)
	if err != nil {
		return nil, err
	}
	handle := &linuxIPSecHandle{
		ipCommand: command,
		config:    cloneIPSecSAConfig(config),
	}
	for _, operation := range install {
		if err := runIPCommand(ctx, command, operation); err != nil {
			_ = handle.cleanup(context.Background())
			zeroBytes(handle.config.EncryptionKey)
			zeroBytes(handle.config.IntegrityKey)
			return nil, fmt.Errorf("%w: %v", ErrIPSecInstall, err)
		}
	}
	zeroBytes(handle.config.EncryptionKey)
	zeroBytes(handle.config.IntegrityKey)
	return handle, nil
}

func (installer linuxIPSecInstaller) InstallPair(
	ctx context.Context,
	config IPSecSAConfig,
	pair IPSecFlowPair,
) (IPSecSAHandle, error) {
	command := installer.ipCommand
	if command == "" {
		command = "ip"
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, errors.New("ims: Linux iproute2 is required for ipsec-3gpp")
	}
	install, err := buildXFRMFlowPairInstallPlan(config, pair)
	if err != nil {
		return nil, err
	}
	handle := &linuxIPSecHandle{
		ipCommand: command,
		config:    cloneIPSecSAConfig(config),
		pair:      pair,
	}
	for _, operation := range install {
		if err := runIPCommand(ctx, command, operation); err != nil {
			_ = handle.cleanup(context.Background())
			zeroBytes(handle.config.EncryptionKey)
			zeroBytes(handle.config.IntegrityKey)
			return nil, fmt.Errorf("%w: %v", ErrIPSecInstall, err)
		}
	}
	zeroBytes(handle.config.EncryptionKey)
	zeroBytes(handle.config.IntegrityKey)
	return handle, nil
}

func (handle *linuxIPSecHandle) Close(ctx context.Context) error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return nil
	}
	handle.closed = true
	return handle.cleanup(ctx)
}

func (handle *linuxIPSecHandle) cleanup(ctx context.Context) error {
	var cleanupErrors []error
	cleanupPlan := buildXFRMCleanupPlan(handle.config)
	if handle.pair != 0 {
		cleanupPlan, _ = buildXFRMFlowPairCleanupPlan(handle.config, handle.pair)
	}
	for _, operation := range cleanupPlan {
		if err := runIPCommand(ctx, handle.ipCommand, operation); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func runIPCommand(ctx context.Context, command string, operation xfrmOperation) error {
	output, err := exec.CommandContext(ctx, command, operation.arguments...).CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	if strings.Contains(strings.ToLower(message), "protocol not supported") ||
		strings.Contains(strings.ToLower(message), "operation not supported") {
		return fmt.Errorf(
			"%s: host kernel lacks XFRM/IPsec support; install matching kmod-ipsec and kmod-ipsec4/6 (OpenWrt), or enable CONFIG_XFRM_USER and ESP in the kernel: %s",
			operation.description,
			message,
		)
	}
	// Operation descriptions contain no SPI keys or subscriber identity.
	return fmt.Errorf("%s: %s", operation.description, message)
}
