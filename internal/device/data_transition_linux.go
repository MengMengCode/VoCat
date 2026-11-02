//go:build linux

package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// stopOwnedQMIDataSessionLocked releases only this process's WDS owner before
// the AT backend takes over its interface. It never opens a QMI client when no
// owned session exists. The caller holds dataMu throughout the handoff.
func stopOwnedQMIDataSessionLocked(state *managedDevice, networkInterface string) error {
	if state.dataSession == nil {
		disableQMIDataEvents(state)
		return nil
	}
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), qmiDataStopTimeout)
	defer cancelCleanup()
	if networkInterface = strings.TrimSpace(networkInterface); networkInterface != "" {
		ipCommand, err := lookPathIP("ip")
		if err != nil {
			return fmt.Errorf("%w: remove the previous QMI host path before AT takeover", ErrDataBackendUnavailable)
		}
		// The QMI backend owns the global addresses on this dedicated modem
		// interface. Remove its host path even for AT disable, which will not
		// start DHCP and therefore cannot rely on DHCP's startup cleanup.
		routeErr := clearExportProxyRouteWithError(cleanupContext, networkInterface)
		_, addressErr := hostCommandOutput(cleanupContext, ipCommand, "-4", "addr", "flush", "dev", networkInterface, "scope", "global")
		_, linkErr := hostCommandOutput(cleanupContext, ipCommand, "link", "set", "dev", networkInterface, "down")
		if err := errors.Join(routeErr, addressErr, linkErr); err != nil {
			return fmt.Errorf("remove owned QMI host path before AT takeover: %w", err)
		}
	}
	if err := state.dataSession.Stop(cleanupContext, state.dataSessionHandle); !stoppedQMIDataError(err) {
		return fmt.Errorf("stop owned QMI session before AT takeover: %w", err)
	}
	closeErr := state.dataSession.Close()
	// Stop has completed and Close is terminal even when releasing the WDS
	// client ID reports an error: the production session closes and clears its
	// transport in that case. Never retry Stop through that consumed owner.
	disableQMIDataEvents(state)
	state.dataSession = nil
	state.dataSessionHandle = 0
	state.dataSessionControl = ""
	if closeErr != nil {
		return fmt.Errorf("close owned QMI session before AT takeover: %w", closeErr)
	}
	return nil
}
