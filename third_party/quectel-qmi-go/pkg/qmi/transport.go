package qmi

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

const (
	defaultProxyPath        = "qmi-proxy"
	defaultProxyExecutable  = "/usr/libexec/qmi-proxy"
	defaultProxyOpenTimeout = 5 * time.Second
)

type qmiTransport interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
	SetReadDeadline(time.Time) error
}

func openRawTransport(path string) (qmiTransport, error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open QMI device %s: %w", path, err)
	}
	return f, nil
}

var (
	openRawTransportHook   = openRawTransport
	openProxyTransportHook = openProxyTransport
)
