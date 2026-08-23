//go:build !linux

package device

import (
	"context"

	"vocat/internal/modem"
)

func openQMIDataSession(context.Context, string) (qmiDataSession, error) {
	return nil, ErrDataBackendUnavailable
}

func invalidateQMINetworkSession(*managedDevice, modem.Candidate) {}

func (manager *Manager) NetworkStatus(context.Context, string) (NetworkStatus, error) {
	return NetworkStatus{}, ErrDataBackendUnavailable
}

func (manager *Manager) setQMINetwork(
	context.Context,
	*managedDevice,
	modem.Candidate,
	bool,
	string,
	string,
	string,
	string,
	string,
) (NetworkResult, error) {
	return NetworkResult{}, ErrDataBackendUnavailable
}
