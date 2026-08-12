//go:build !linux

package device

import (
	"context"
	"fmt"

	"vocat/internal/modem"
)

func setQMINetwork(
	context.Context,
	modem.Candidate,
	bool,
	string,
	string,
	string,
	string,
	string,
) (NetworkResult, error) {
	return NetworkResult{}, fmt.Errorf("%w: QMI control is supported only on Linux", ErrDataBackendUnavailable)
}
