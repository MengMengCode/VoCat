//go:build !linux

package device

// Non-Linux builds cannot own a production QMI data session.
func stopOwnedQMIDataSessionLocked(state *managedDevice, _ string) error {
	if state.dataSession != nil {
		return ErrDataBackendUnavailable
	}
	return nil
}
