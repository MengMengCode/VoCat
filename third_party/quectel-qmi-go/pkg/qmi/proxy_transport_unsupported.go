//go:build !linux

package qmi

import (
	"context"
	"fmt"
	"runtime"
)

func openProxyTransport(ctx context.Context, opts ClientOptions) (qmiTransport, error) {
	return nil, fmt.Errorf("qmi-proxy transport is unsupported on %s", runtime.GOOS)
}
