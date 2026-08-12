//go:build !linux || (!amd64 && !arm64)

package pcsc

import "context"

type unsupportedBackend struct{}

func newNativeBackend() Backend { return unsupportedBackend{} }

func (unsupportedBackend) Readers(context.Context) ([]Reader, error) {
	return nil, ErrUnsupported
}

func (unsupportedBackend) Open(context.Context, Selector) (Card, error) {
	return nil, ErrUnsupported
}
