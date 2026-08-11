//go:build linux

package ims

import (
	"context"
	"net"
	"os"
	"testing"
)

func TestNativeIPSecInstallLifecycle(t *testing.T) {
	if os.Getenv("VOCAT_NATIVE_XFRM_TEST") != "1" {
		t.Skip("set VOCAT_NATIVE_XFRM_TEST=1 on an isolated Linux host")
	}
	config := testIPSecSAConfig()
	config.LocalIP = net.ParseIP("192.168.32.175")
	config.RemoteIP = net.ParseIP("213.20.31.8")
	config.AuthAlgorithm = securityAlgorithmMD5
	config.EncryptionAlgorithm = securityEncryptionNull
	config.EncryptionKey = nil
	config.IntegrityKey = config.IntegrityKey[:16]
	handle, err := installNativeIPSec(context.Background(), config)
	if err != nil {
		t.Fatalf("installNativeIPSec() error = %v", err)
	}
	if handle == nil {
		t.Fatal("installNativeIPSec() returned a nil handle")
	}
	if err := handle.Close(context.Background()); err != nil {
		t.Fatalf("native XFRM Close() error = %v", err)
	}
}
