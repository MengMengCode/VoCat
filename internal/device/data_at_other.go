//go:build !linux

package device

import "context"

// The AT host data link needs Linux host networking (interface control plus
// busybox udhcpc with protected policy routing). Other platforms report the
// backend as unavailable so callers surface a clear error instead of a
// partially working data path.
func newATHostDataLink() atHostDataLink {
	return unavailableATHostDataLink{}
}

type unavailableATHostDataLink struct{}

func (unavailableATHostDataLink) Start(context.Context, string) (atHostDataSession, error) {
	return nil, ErrDataBackendUnavailable
}
