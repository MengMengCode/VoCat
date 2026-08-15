package device

import (
	"context"
	"errors"
	"fmt"

	"vocat/internal/modem"
)

func (manager *Manager) readNativeQMIICCID(ctx context.Context, candidate modem.Candidate) (string, error) {
	if manager == nil || manager.qmiRadioOpener == nil {
		return "", errors.New("QMI UIM ICCID reader is unavailable")
	}
	if candidate.QMIControl == "" {
		return "", errors.New("QMI UIM control device is unavailable")
	}
	session, err := manager.qmiRadioOpener(ctx, candidate.QMIControl)
	if err != nil {
		return "", fmt.Errorf("open QMI UIM control: %w", err)
	}
	if session == nil {
		return "", errors.New("QMI UIM control returned an empty session")
	}
	defer session.Close()
	reader, ok := session.(nativeQMIICCIDSession)
	if !ok {
		return "", errors.New("QMI session does not expose UIM ICCID reading")
	}
	value, err := reader.GetICCID(ctx)
	if err != nil {
		return "", fmt.Errorf("read EF_ICCID: %w", err)
	}
	iccid := parseICCIDIdentifier(modem.Response{Lines: []string{value}}, nil, 18, 22)
	if iccid == "" {
		return "", errors.New("QMI UIM returned an invalid ICCID")
	}
	return iccid, nil
}
