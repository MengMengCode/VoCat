//go:build !linux

package qmiproxy

import (
	"context"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"

	"vocat/internal/remoteproc"
)

// OpenClient preserves the library's platform-specific transport support.
func OpenClient(ctx context.Context, path string, opts qmi.ClientOptions) (*qmi.Client, error) {
	if _, err := remoteproc.CheckPort(path); err != nil {
		return nil, err
	}
	return qmi.NewClientWithOptions(ctx, path, opts)
}
