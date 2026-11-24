//go:build !linux

package ims

import "net"

func configureProtectedTCPDialer(_ *net.Dialer) {}

func configureProtectedTCPListener(_ *net.ListenConfig) {}
